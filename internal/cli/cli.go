// Package cli は、quenta のコマンドの引数と環境変数を読み、操作を呼び、結果を JSON で書く
package cli

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/quenta/internal/ops"
	"github.com/TadahiroYamamura/quenta/internal/store"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

// Code は、失敗したときに返すエラーコード
type Code string

const (
	CodeSessionRequired        Code = "session_required"
	CodeInvalidArgument        Code = "invalid_argument"
	CodeTitleRequired          Code = "title_required"
	CodeReasonRequired         Code = "reason_required"
	CodeUnsupportedDataVersion Code = "unsupported_data_version"
	CodeInternalError          Code = "internal_error"
	CodeNotFound               Code = "not_found"
	CodeAlreadyFinished        Code = "already_finished"
	CodeIDConflict             Code = "id_conflict"
	CodeBusyTimeout            Code = "busy_timeout"
)

// failure は、エラーコードと、何が起きたかの説明の文
type failure struct {
	code    Code
	message string
}

func (f *failure) Error() string { return f.message }

func fail(code Code, format string, a ...any) *failure {
	return &failure{code: code, message: fmt.Sprintf(format, a...)}
}

// codeOf は、モデルの判定が返すエラーを、同じ名前のエラーコードに写す。IDTaken は ops が作り直しに回すので、ここに届けば実装の誤り
func codeOf(f task.Failure) Code {
	switch f {
	case task.SessionRequired:
		return CodeSessionRequired
	case task.TitleRequired:
		return CodeTitleRequired
	case task.ReasonRequired:
		return CodeReasonRequired
	case task.NotFound:
		return CodeNotFound
	case task.AlreadyFinished:
		return CodeAlreadyFinished
	}
	return CodeInternalError
}

// Env は、コマンドが読む環境
type Env struct {
	Getenv func(string) string
	Stdout io.Writer // 成功の JSON と、使い方のテキスト
	Stderr io.Writer // 失敗の JSON
	Now    func() time.Time
	Random io.Reader
}

// DefaultEnv は、OS の環境
func DefaultEnv(getenv func(string) string, stdout, stderr io.Writer) Env {
	return Env{Getenv: getenv, Stdout: stdout, Stderr: stderr, Now: time.Now, Random: rand.Reader}
}

// Run は、quenta を args（コマンド名を除く）で動かし、終了コードを返す
func Run(args []string, env Env) int {
	args = dropValuelessLastFlag(args)
	c := &command{env: env}
	root := c.root()
	root.SetArgs(args)
	root.SetOut(env.Stdout)
	root.SetErr(io.Discard)
	// --help・-h は、ほかの条件にかかわらず使い方を返す（主張 error-precedence）。cobra は、知らないフラグがあると --help より先に誤りにするので、先に見る
	if hasHelpFlag(args) {
		cmd, _, err := root.Find(args)
		if err != nil {
			cmd = root
		}
		cmd.Help()
		return 0
	}
	err := root.Execute()
	if err == nil {
		return 0
	}
	var f *failure
	if !errors.As(err, &f) {
		// cobra が返す、引数の解釈の誤り（知らないフラグ、知らないサブコマンドなど）
		f = fail(CodeInvalidArgument, "引数の誤り: %v", err)
		if task.IsBlank(env.Getenv("QUENTA_SESSION")) {
			f = fail(CodeSessionRequired, "QUENTA_SESSION がないか、空白だけ")
		}
	}
	writeJSON(env.Stderr, errorOutput{Error: errorBody{Code: string(f.code), Message: f.message}})
	return 1
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

// dropValuelessLastFlag は、値をとるフラグが値なしで最後に置かれたら、そのフラグを省いたのと同じに扱う（主張 cli-title-omitted など）
func dropValuelessLastFlag(args []string) []string {
	if n := len(args); n > 0 {
		switch args[n-1] {
		case "--title", "--body", "--reason":
			return args[:n-1]
		}
	}
	return args
}

type command struct {
	env Env
}

func (c *command) root() *cobra.Command {
	root := &cobra.Command{
		Use:           "quenta",
		Short:         "AI エージェントが使うタスク管理の CLI",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	tasks := &cobra.Command{
		Use:   "task",
		Short: "タスクを登録し、一覧で見て、完了・中止にする",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	tasks.AddCommand(c.add(), c.list(), c.show(), c.done(), c.cancel())
	root.AddCommand(tasks)
	root.CompletionOptions.DisableDefaultCmd = true
	return root
}

func (c *command) add() *cobra.Command {
	var title, body string
	cmd := &cobra.Command{
		Use:   "add --title <タイトル> [--body <本文>]",
		Short: "タスクを登録する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			actor, err := c.session()
			if err != nil {
				return err
			}
			if err := validUTF8(map[string]string{"--title": title, "--body": body}); err != nil {
				return err
			}
			if task.IsBlank(title) {
				return fail(CodeTitleRequired, "--title がないか、タイトルが空白だけ")
			}
			o, closer, err := c.open()
			if err != nil {
				return err
			}
			defer closer()
			t, err := o.Add(actor, title, body)
			if err != nil {
				return c.opError(err)
			}
			writeJSON(c.env.Stdout, taskOutput{Task: toTaskJSON(t)})
			return nil
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "タイトル")
	cmd.Flags().StringVar(&body, "body", "", "本文（省ける）")
	return cmd
}

func (c *command) list() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "list [--all]",
		Short: "タスクの一覧を、作成日時の古い順に返す。既定では終わっていないタスクだけ",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			actor, err := c.session()
			if err != nil {
				return err
			}
			o, closer, err := c.open()
			if err != nil {
				return err
			}
			defer closer()
			ts, err := o.List(actor, all)
			if err != nil {
				return c.opError(err)
			}
			out := listOutput{Tasks: make([]taskJSON, 0, len(ts))}
			for _, t := range ts {
				out.Tasks = append(out.Tasks, toTaskJSON(t))
			}
			writeJSON(c.env.Stdout, out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "終わったタスクも含める")
	return cmd
}

func (c *command) show() *cobra.Command {
	return &cobra.Command{
		Use:   "show <ID>",
		Short: "タスクと、そのイベントを返す",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := c.session(); err != nil {
				return err
			}
			if err := validUTF8(map[string]string{"<ID>": args[0]}); err != nil {
				return err
			}
			o, closer, err := c.open()
			if err != nil {
				return err
			}
			defer closer()
			t, es, err := o.Show(args[0])
			if err != nil {
				return c.opError(err)
			}
			out := showOutput{Task: toTaskJSON(t), Events: make([]eventJSON, 0, len(es))}
			for _, e := range es {
				out.Events = append(out.Events, toEventJSON(e))
			}
			writeJSON(c.env.Stdout, out)
			return nil
		},
	}
}

func (c *command) done() *cobra.Command {
	return c.finish("done", "タスクを完了にする", task.Completing)
}

func (c *command) cancel() *cobra.Command {
	return c.finish("cancel", "タスクを中止にする", task.Canceling)
}

func (c *command) finish(name, short string, k task.FinishKind) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   name + " <ID> --reason <理由>",
		Short: short,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			actor, err := c.session()
			if err != nil {
				return err
			}
			if err := validUTF8(map[string]string{"<ID>": args[0], "--reason": reason}); err != nil {
				return err
			}
			if task.IsBlank(reason) {
				return fail(CodeReasonRequired, "--reason がないか、理由が空白だけ")
			}
			o, closer, err := c.open()
			if err != nil {
				return err
			}
			defer closer()
			t, err := o.Finish(actor, args[0], reason, k)
			if err != nil {
				return c.opError(err)
			}
			writeJSON(c.env.Stdout, taskOutput{Task: toTaskJSON(t)})
			return nil
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "理由")
	return cmd
}

// exactArgs は、位置の引数の数の誤りを、引数の誤りとして返す（cobra の誤りと同じく Run が invalid_argument にする）
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return fmt.Errorf("位置の引数は %d 個（%d 個ある）", n, len(args))
		}
		return nil
	}
}

// session は、QUENTA_SESSION を主体として、前後の空白も含めてそのまま返す
func (c *command) session() (string, error) {
	s := c.env.Getenv("QUENTA_SESSION")
	if task.IsBlank(s) {
		return "", fail(CodeSessionRequired, "QUENTA_SESSION がないか、空白だけ")
	}
	if !utf8.ValidString(s) {
		return "", fail(CodeInvalidArgument, "QUENTA_SESSION が正しい UTF-8 でない")
	}
	return s, nil
}

func validUTF8(values map[string]string) error {
	for _, name := range []string{"<ID>", "--title", "--body", "--reason"} {
		if v, ok := values[name]; ok && !utf8.ValidString(v) {
			return fail(CodeInvalidArgument, "%s が正しい UTF-8 でない", name)
		}
	}
	return nil
}

// dataPath は、データのファイルの場所を返す（主張 data-location）
func (c *command) dataPath() (string, error) {
	if x := c.env.Getenv("XDG_DATA_HOME"); x != "" && filepath.IsAbs(x) {
		return filepath.Join(x, "quenta", "quenta.db"), nil
	}
	if h := c.env.Getenv("HOME"); h != "" {
		return filepath.Join(h, ".local", "share", "quenta", "quenta.db"), nil
	}
	return "", fail(CodeInternalError, "XDG_DATA_HOME が使えず、HOME もないので、データの置き場所が決まらない")
}

func (c *command) open() (*ops.Ops, func(), error) {
	path, err := c.dataPath()
	if err != nil {
		return nil, nil, err
	}
	s, err := store.Open(path)
	if err != nil {
		return nil, nil, c.opError(err)
	}
	o := &ops.Ops{Store: s, Random: c.env.Random, Now: func() int64 { return c.env.Now().Unix() }}
	return o, func() { s.Close() }, nil
}

// opError は、操作のエラーをエラーコードに写す
func (c *command) opError(err error) error {
	var f task.Failure
	switch {
	case errors.As(err, &f):
		return fail(codeOf(f), "%s", f.Error())
	case errors.Is(err, store.ErrUnsupportedVersion):
		return fail(CodeUnsupportedDataVersion, "%v", err)
	case errors.Is(err, store.ErrBusy):
		return fail(CodeBusyTimeout, "%v", err)
	case errors.Is(err, ops.ErrIDConflict):
		return fail(CodeIDConflict, "%v", err)
	}
	return fail(CodeInternalError, "%v", err)
}

type taskJSON struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type eventJSON struct {
	Actor  string  `json:"actor"`
	From   *string `json:"from"`
	To     string  `json:"to"`
	Reason string  `json:"reason"`
	At     string  `json:"at"`
}

type taskOutput struct {
	Task taskJSON `json:"task"`
}

type listOutput struct {
	Tasks []taskJSON `json:"tasks"`
}

type showOutput struct {
	Task   taskJSON    `json:"task"`
	Events []eventJSON `json:"events"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorOutput struct {
	Error errorBody `json:"error"`
}

// formatTime は、日時を UTC の RFC 3339 の形（秒まで）にする
func formatTime(sec int64) string {
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

func toTaskJSON(t task.Task) taskJSON {
	return taskJSON{ID: t.ID, Title: t.Title, Body: t.Body, Status: string(t.Status), CreatedAt: formatTime(t.CreatedAt)}
}

func toEventJSON(e task.Event) eventJSON {
	var from *string
	if e.From != nil {
		s := string(*e.From)
		from = &s
	}
	return eventJSON{Actor: e.Actor, From: from, To: string(e.To), Reason: e.Reason, At: formatTime(e.At)}
}

// writeJSON は、1 行の JSON と改行を書く。HTML の文字（<、>、&）は、そのまま書く
func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

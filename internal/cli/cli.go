// Package cli は、quenta のコマンドの引数と環境変数を読み、操作を呼び、結果を JSON で書く
package cli

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
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
	// cobra は、使い方を書けなかったことを返さないので、書き込みの誤りを見張る
	out := &watchedWriter{w: env.Stdout}
	env.Stdout = out
	c := &command{env: env}
	root := c.root()
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(io.Discard)
	// --help・-h は、ほかの条件にかかわらず使い方を返す（主張 error-precedence）。cobra は、知らないフラグがあると --help より先に誤りにするので、先に見る
	if hasHelpFlag(args) {
		cmd, _, findErr := root.Find(args)
		if findErr != nil {
			cmd = root // 知らないサブコマンドなら、quenta の使い方を返す
		}
		err := cmd.Help()
		if err == nil && out.err != nil {
			err = fail(CodeInternalError, "使い方を標準出力に書けない: %v", out.err)
		}
		if err != nil {
			return failed(err, env)
		}
		return 0
	}
	err := root.Execute()
	if err == nil && out.err != nil {
		err = fail(CodeInternalError, "標準出力に書けない: %v", out.err)
	}
	if err == nil {
		return 0
	}
	return failed(err, env)
}

// failed は、失敗を JSON で標準エラーに書き、終了コードを返す
func failed(err error, env Env) int {
	var f *failure
	if !errors.As(err, &f) {
		// cobra が返す、引数の解釈の誤り（知らないフラグ、知らないサブコマンドなど）
		f = fail(CodeInvalidArgument, "引数の誤り: %v", err)
		if task.IsBlank(env.Getenv("QUENTA_SESSION")) {
			f = fail(CodeSessionRequired, "QUENTA_SESSION がないか、空白だけ")
		}
	}
	// 標準エラーにも書けなければ、書く先がないので、終了コードだけで伝える
	_ = writeJSON(env.Stderr, errorOutput{Error: errorBody{Code: string(f.code), Message: f.message}})
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

// valueFlags は、サブコマンドごとの、値をとるフラグ
var valueFlags = map[string][]string{
	"add":    {"--title", "--body"},
	"done":   {"--reason"},
	"cancel": {"--reason"},
}

// dropValuelessLastFlag は、そのサブコマンドの値をとるフラグが、値なしで最後に置かれたら、そのフラグを省いたのと同じに扱う（主張 cli-title-omitted など）。
// そのフラグを持たないサブコマンドや、-- の後に置かれたものは消さない（引数の誤りとして invalid_argument になる。#26）
func dropValuelessLastFlag(args []string) []string {
	n := len(args)
	if n < 3 || args[0] != "task" || slices.Contains(args[:n-1], "--") {
		return args
	}
	if slices.Contains(valueFlags[args[1]], args[n-1]) {
		return args[:n-1]
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
		RunE:          func(cmd *cobra.Command, _ []string) error { return helpOrFail(cmd) },
	}
	tasks := &cobra.Command{
		Use:   "task",
		Short: "タスクを登録し、一覧で見て、完了・中止にする",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return helpOrFail(cmd) },
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
			o, closeStore, err := c.open()
			if err != nil {
				return err
			}
			t, err := o.Add(actor, title, body)
			if err := c.closeAfter(err, closeStore); err != nil {
				return err
			}
			return c.output(taskOutput{Task: toTaskJSON(t)})
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
			o, closeStore, err := c.open()
			if err != nil {
				return err
			}
			ts, err := o.List(actor, all)
			if err := c.closeAfter(err, closeStore); err != nil {
				return err
			}
			out := listOutput{Tasks: make([]taskJSON, 0, len(ts))}
			for _, t := range ts {
				out.Tasks = append(out.Tasks, toTaskJSON(t))
			}
			return c.output(out)
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
			o, closeStore, err := c.open()
			if err != nil {
				return err
			}
			t, es, err := o.Show(args[0])
			if err := c.closeAfter(err, closeStore); err != nil {
				return err
			}
			out := showOutput{Task: toTaskJSON(t), Events: make([]eventJSON, 0, len(es))}
			for _, e := range es {
				out.Events = append(out.Events, toEventJSON(e))
			}
			return c.output(out)
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
			o, closeStore, err := c.open()
			if err != nil {
				return err
			}
			t, err := o.Finish(actor, args[0], reason, k)
			if err := c.closeAfter(err, closeStore); err != nil {
				return err
			}
			return c.output(taskOutput{Task: toTaskJSON(t)})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "理由")
	return cmd
}

// exactArgs は、位置の引数の数の誤りを、引数の誤りとして返す（cobra の誤りと同じく Run が invalid_argument にする）
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return fmt.Errorf("<ID> の引数は %d 個（%d 個ある）", n, len(args))
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
	if h := c.env.Getenv("HOME"); h != "" && filepath.IsAbs(h) {
		return filepath.Join(h, ".local", "share", "quenta", "quenta.db"), nil
	}
	return "", fail(CodeInternalError, "XDG_DATA_HOME が使えず、HOME もないか絶対パスでないので、データの置き場所が決まらない")
}

func (c *command) open() (*ops.Ops, func() error, error) {
	path, err := c.dataPath()
	if err != nil {
		return nil, nil, err
	}
	s, err := store.Open(path)
	if err != nil {
		return nil, nil, c.opError(err)
	}
	o := &ops.Ops{Store: s, Random: c.env.Random, Now: func() int64 { return c.env.Now().Unix() }}
	return o, s.Close, nil
}

// closeAfter は、操作の後にデータのファイルを閉じ、操作か閉じるときのエラーを、エラーコードに写して返す。
// 結果の JSON は、閉じた後に書く（閉じられずに失敗を返すとき、標準出力に成功の JSON を書いてしまわないため）
func (c *command) closeAfter(opErr error, closeStore func() error) error {
	closeErr := closeStore()
	if opErr != nil {
		return c.opError(errors.Join(opErr, closeErr))
	}
	if closeErr != nil {
		return fail(CodeInternalError, "データのファイルを閉じられない（書き込みは済んでいる）: %v", closeErr)
	}
	return nil
}

// output は、成功の JSON を標準出力に書く。書けなければ internal_error にする
func (c *command) output(v any) error {
	if err := writeJSON(c.env.Stdout, v); err != nil {
		return fail(CodeInternalError, "結果を標準出力に書けない: %v", err)
	}
	return nil
}

// helpOrFail は、使い方を書く。書けなかったことは、Run が見張りの書き込み先から知る
func helpOrFail(cmd *cobra.Command) error {
	if err := cmd.Help(); err != nil {
		return fail(CodeInternalError, "使い方を書けない: %v", err)
	}
	return nil
}

// watchedWriter は、書き込みの最初の誤りを覚える
type watchedWriter struct {
	w   io.Writer
	err error
}

func (w *watchedWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if err != nil && w.err == nil {
		w.err = err
	}
	return n, err
}

// opError は、操作のエラーをエラーコードに写す
func (c *command) opError(err error) error {
	var f task.Failure
	var already *failure
	switch {
	case errors.As(err, &already):
		return already
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
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

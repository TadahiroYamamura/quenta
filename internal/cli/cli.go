// Package cli は、quenta のサブコマンドと JSON の入出力を持つ。
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/TadahiroYamamura/quenta/internal/app"
	"github.com/TadahiroYamamura/quenta/internal/store"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

const timeLayout = "2006-01-02T15:04:05.000Z07:00"

type env struct {
	getenv  func(string) string
	stdout  io.Writer
	appOpts []app.Option
}

// Run は、quenta を args で実行し、終了コードを返す。成功の JSON は stdout に、エラーの JSON は stderr に書く
func Run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, appOpts ...app.Option) int {
	// 不正な UTF-8 は、仕様に渡すと U+FFFD に置き換わり、入力と違う値が保存される。
	// タイトルなどを後から直す操作がないので、置き換えずに入り口で拒む（#1 の 2 回目のレビューで人が決めた）
	for i, a := range args {
		if !utf8.ValidString(a) {
			writeError(stderr, task.Errorf(task.CodeInvalidArgument, "引数の %d 番目が UTF-8 として正しくない", i+1))
			return 1
		}
	}
	e := &env{getenv: getenv, stdout: stdout, appOpts: appOpts}
	root := newRoot(e)
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		writeError(stderr, err)
		return 1
	}
	return 0
}

func newRoot(e *env) *cobra.Command {
	root := &cobra.Command{
		Use:           "quenta",
		Short:         "AI エージェントが使うタスク管理の CLI。出力は JSON",
		Args:          noArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	// cobra が引数を解釈できなかったときのエラーも、エラーコードを持つ JSON で返す
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return task.Errorf(task.CodeInvalidArgument, "%s", err)
	})
	root.CompletionOptions.DisableDefaultCmd = true

	taskCmd := &cobra.Command{
		Use:   "task",
		Short: "タスクを登録し、一覧で見て、完了または中止にする",
		Args:  noArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	taskCmd.AddCommand(newAdd(e), newList(e), newShow(e), newFinish(e, "done", task.OutcomeComplete), newFinish(e, "cancel", task.OutcomeCancel))
	root.AddCommand(taskCmd)
	return root
}

func newAdd(e *env) *cobra.Command {
	var title, body string
	cmd := &cobra.Command{
		Use:   "add --title <タイトル> [--body <本文>]",
		Short: "タスクを登録する。状態は todo。主体として環境変数 QUENTA_SESSION が要る",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return e.withApp(func(a *app.App) error {
				actor, err := e.session()
				if err != nil {
					return err
				}
				t, err := a.Add(cmd.Context(), title, body, actor)
				if err != nil {
					return err
				}
				return e.write(map[string]any{"task": toTaskJSON(t)})
			})
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "タイトル（空白だけは不可）")
	cmd.Flags().StringVar(&body, "body", "", "本文（省略できる）")
	return cmd
}

func newList(e *env) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "list [--all]",
		Short: "終わっていないタスクを、作成日時の古い順に返す。本文は含まない（task show で見る）",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return e.withApp(func(a *app.App) error {
				ts, err := a.List(cmd.Context(), all)
				if err != nil {
					return err
				}
				out := make([]taskSummaryJSON, len(ts))
				for i, t := range ts {
					out[i] = toTaskSummaryJSON(t)
				}
				return e.write(map[string]any{"tasks": out})
			})
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "終わったタスク（done・canceled）も含める")
	return cmd
}

func newShow(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "show <ID>",
		Short: "タスクの内容と、そのタスクのイベント（状態の変更の経緯）を返す",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return e.withApp(func(a *app.App) error {
				t, evs, err := a.Show(cmd.Context(), args[0])
				if err != nil {
					return err
				}
				out := make([]eventJSON, len(evs))
				for i, ev := range evs {
					out[i] = toEventJSON(ev)
				}
				return e.write(struct {
					Task   taskJSON    `json:"task"`
					Events []eventJSON `json:"events"`
				}{toTaskJSON(t), out})
			})
		},
	}
}

func newFinish(e *env, name string, o task.Outcome) *cobra.Command {
	var reason string
	short := map[task.Outcome]string{
		task.OutcomeComplete: "タスクを完了（done）にする",
		task.OutcomeCancel:   "タスクを中止（canceled）にする",
	}[o]
	cmd := &cobra.Command{
		Use:   name + " <ID> --reason <理由>",
		Short: short + "。理由と、主体として環境変数 QUENTA_SESSION が要る。終わったタスクは変えられない",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return e.withApp(func(a *app.App) error {
				actor, err := e.session()
				if err != nil {
					return err
				}
				t, err := a.Finish(cmd.Context(), args[0], o, reason, actor)
				if err != nil {
					return err
				}
				return e.write(map[string]any{"task": toTaskJSON(t)})
			})
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "理由（空白だけは不可）")
	return cmd
}

func noArgs(cmd *cobra.Command, args []string) error {
	if err := cobra.NoArgs(cmd, args); err != nil {
		return task.Errorf(task.CodeInvalidArgument, "%s", err)
	}
	return nil
}

func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(n)(cmd, args); err != nil {
			return task.Errorf(task.CodeInvalidArgument, "%s", err)
		}
		return nil
	}
}

// session は、主体（環境変数 QUENTA_SESSION）を返す。不正な UTF-8 なら、引数と同じ理由で拒む
func (e *env) session() (string, error) {
	s := e.getenv("QUENTA_SESSION")
	if !utf8.ValidString(s) {
		return "", task.Errorf(task.CodeInvalidArgument, "環境変数 QUENTA_SESSION が UTF-8 として正しくない")
	}
	return s, nil
}

func (e *env) withApp(fn func(*app.App) error) error {
	path, err := store.DefaultPath(e.getenv)
	if err != nil {
		return err
	}
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	defer s.Close()
	return fn(app.New(s, e.appOpts...))
}

func (e *env) write(v any) error {
	enc := json.NewEncoder(e.stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

type taskJSON struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type taskSummaryJSON struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type eventJSON struct {
	Actor  string  `json:"actor"`
	From   *string `json:"from"`
	To     string  `json:"to"`
	Reason *string `json:"reason"`
	At     string  `json:"at"`
}

type errorJSON struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func formatTime(ms int64) string { return time.UnixMilli(ms).UTC().Format(timeLayout) }

func toTaskJSON(t task.Task) taskJSON {
	return taskJSON{ID: t.ID, Title: t.Title, Body: t.Body, Status: string(t.Status), CreatedAt: formatTime(t.CreatedAt)}
}

func toTaskSummaryJSON(t task.Task) taskSummaryJSON {
	return taskSummaryJSON{ID: t.ID, Title: t.Title, Status: string(t.Status), CreatedAt: formatTime(t.CreatedAt)}
}

func toEventJSON(e task.Event) eventJSON {
	out := eventJSON{Actor: e.Actor, To: string(e.To), Reason: e.Reason, At: formatTime(e.At)}
	if e.From != nil {
		s := string(*e.From)
		out.From = &s
	}
	return out
}

func writeError(w io.Writer, err error) {
	var out errorJSON
	if te, ok := errorsAsTask(err); ok {
		out.Error.Code = string(te.Code)
		out.Error.Message = te.Message
	} else {
		out.Error.Code = string(task.CodeInternal)
		out.Error.Message = err.Error()
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	// stderr に書けなければ伝える先がないので、終了コードだけで失敗を伝える
	_ = enc.Encode(out)
}

func errorsAsTask(err error) (*task.Error, bool) { return errors.AsType[*task.Error](err) }

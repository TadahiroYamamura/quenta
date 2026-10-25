package task

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode"
)

// 実装が呼ぶ仕様の関数（NewTask・FinishArgsError・Finish・IsVisible）のプロパティベースのテスト。
// 無作為な入力で呼び、spec/task.dfy の補題（NewTaskResult・FinishArgsErrorResult・FinishRejectsFinished・FinishOneResult）が述べる結果を、
// 仕様から変換したコードを使わずに Go で書いた期待値と比べる。
// internal/app のプロパティベースのテストは、手本（task.Model）が同じ仕様の関数を使うので、仕様そのものの誤りはそちらでは見つからないため

const propRuns = 2000

var (
	propPieces   = []string{"", " ", "\t", "\n", "　", " ", "​", "a", "タスク", "🎉", "\"", "\x00"}
	propStatuses = []Status{StatusTodo, StatusDone, StatusCanceled}
	propOutcomes = []Outcome{OutcomeComplete, OutcomeCancel}
)

// randomText は、空白・ゼロ幅スペース・ふつうの文字を 0〜3 個つないだ文字列を作る
func randomText(rng *rand.Rand) string {
	var b strings.Builder
	for range rng.IntN(4) {
		b.WriteString(propPieces[rng.IntN(len(propPieces))])
	}
	return b.String()
}

// blank は、仕様の IsBlank の期待値。空、または Go の unicode.IsSpace が空白とする文字だけ
func blank(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return !unicode.IsSpace(r) }) < 0
}

func finished(t *testing.T, s Status) bool {
	t.Helper()
	switch s {
	case StatusTodo:
		return false
	case StatusDone, StatusCanceled:
		return true
	}
	t.Fatalf("テストが知らない状態: %q", s)
	return false
}

func outcomeStatus(t *testing.T, o Outcome) Status {
	t.Helper()
	switch o {
	case OutcomeComplete:
		return StatusDone
	case OutcomeCancel:
		return StatusCanceled
	}
	t.Fatalf("テストが知らない終わらせ方: %d", o)
	return ""
}

func Test無作為な入力で登録は補題NewTaskResultどおりの結果を返す(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 0))
	for i := range propRuns {
		id, title, body, actor := fmt.Sprintf("%08x", rng.Uint32()), randomText(rng), randomText(rng), randomText(rng)
		now := rng.Int64() - rng.Int64()
		c, err := NewTask(id, title, body, actor, now)
		desc := fmt.Sprintf("#%d title=%q body=%q actor=%q", i, title, body, actor)
		switch {
		case blank(actor):
			if CodeOf(err) != CodeSessionRequired {
				t.Fatalf("%s: got %v, want session_required", desc, err)
			}
		case blank(title):
			if CodeOf(err) != CodeTitleRequired {
				t.Fatalf("%s: got %v, want title_required", desc, err)
			}
		default:
			if err != nil {
				t.Fatalf("%s: %v", desc, err)
			}
			if want := (Task{ID: id, Title: title, Body: body, Status: StatusTodo, CreatedAt: now}); c.Task() != want {
				t.Fatalf("%s: task got %+v, want %+v", desc, c.Task(), want)
			}
			if want := (Event{TaskID: id, Actor: actor, To: StatusTodo, At: now}); !eventEqual(c.Event(), want) {
				t.Fatalf("%s: event got %+v, want %+v", desc, c.Event(), want)
			}
			if !c.IsNew() || !c.FromSpec() {
				t.Fatalf("%s: IsNew=%v FromSpec=%v", desc, c.IsNew(), c.FromSpec())
			}
		}
	}
}

func Test無作為な入力で完了と中止は補題FinishArgsErrorResultとFinishOneResultどおりの結果を返す(t *testing.T) {
	rng := rand.New(rand.NewPCG(2, 0))
	for i := range propRuns {
		// DB から読んだタスクは不変条件（タイトルが空でない）を満たす前提なので、タイトルには空白でない文字を必ず含める
		cur := Task{
			ID: fmt.Sprintf("%08x", rng.Uint32()), Title: randomText(rng) + "t", Body: randomText(rng),
			Status: propStatuses[rng.IntN(len(propStatuses))], CreatedAt: rng.Int64() - rng.Int64(),
		}
		o := propOutcomes[rng.IntN(len(propOutcomes))]
		reason, actor := randomText(rng), randomText(rng)
		now := rng.Int64() - rng.Int64()
		desc := fmt.Sprintf("#%d task=%+v outcome=%d reason=%q actor=%q", i, cur, o, reason, actor)

		argErr := FinishArgsError(reason, actor)
		c, err := Finish(cur, o, reason, actor, now)
		switch {
		case blank(actor):
			if CodeOf(argErr) != CodeSessionRequired || CodeOf(err) != CodeSessionRequired {
				t.Fatalf("%s: got %v / %v, want session_required", desc, argErr, err)
			}
		case blank(reason):
			if CodeOf(argErr) != CodeReasonRequired || CodeOf(err) != CodeReasonRequired {
				t.Fatalf("%s: got %v / %v, want reason_required", desc, argErr, err)
			}
		case finished(t, cur.Status):
			if argErr != nil || CodeOf(err) != CodeAlreadyFinished {
				t.Fatalf("%s: got %v / %v, want nil / already_finished", desc, argErr, err)
			}
		default:
			if argErr != nil || err != nil {
				t.Fatalf("%s: got %v / %v", desc, argErr, err)
			}
			// 状態だけが変わり、ID・タイトル・本文・作成日時は変わらない
			want := cur
			want.Status = outcomeStatus(t, o)
			if c.Task() != want {
				t.Fatalf("%s: task got %+v, want %+v", desc, c.Task(), want)
			}
			from := cur.Status
			if we := (Event{TaskID: cur.ID, Actor: actor, From: &from, To: want.Status, Reason: &reason, At: now}); !eventEqual(c.Event(), we) {
				t.Fatalf("%s: event got %+v, want %+v", desc, c.Event(), we)
			}
			if c.IsNew() || !c.FromSpec() {
				t.Fatalf("%s: IsNew=%v FromSpec=%v", desc, c.IsNew(), c.FromSpec())
			}
		}
	}
}

func Test無作為な入力で一覧に出すかは終わっていないかと指定どおりに決まる(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 0))
	for i := range propRuns {
		tk := Task{ID: "x", Title: randomText(rng) + "t", Status: propStatuses[rng.IntN(len(propStatuses))]}
		inc := rng.IntN(2) == 0
		got, err := IsVisible(tk, inc)
		if want := inc || !finished(t, tk.Status); err != nil || got != want {
			t.Fatalf("#%d status=%s includeFinished=%v: got %v, %v, want %v", i, tk.Status, inc, got, err, want)
		}
	}
}

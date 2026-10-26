package task

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"unicode"
)

// 実装が呼ぶ仕様の関数（NewTask・Finish・IsVisible）のプロパティベースのテスト。ShowTask は入力が少ないので、単体テストで全部の場合を回す。
// 無作為な入力で呼び、spec/task.dfy の補題（NewTaskErrors・NewTaskResult・FinishErrors・FinishRejectsFinished・FinishResult）が述べる結果を、
// 仕様から変換したコードを使わずに Go で書いた期待値と比べる。
// internal/app のプロパティベースのテストは、手本（task.Model）が同じ仕様の関数を使うので、仕様そのものの誤りはそちらでは見つからないため

const propRuns = 2000

var (
	propPieces   = []string{"", " ", "\t", "\n", "　", "\u00a0", "\u200b", "a", "タスク", "🎉", "\"", "\x00"}
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

// requireAll は、すべての分岐を少なくとも 1 回通ったことを確かめる。
// 入力の作り方が偏って、確かめたつもりの分岐を一度も通らない、ということがないようにする
func requireAll(t *testing.T, seen map[string]int, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if seen[k] == 0 {
			t.Errorf("%s を一度も通っていない（通った分岐: %v）", k, seen)
		}
	}
}

func Test無作為な入力で登録は補題NewTaskErrorsとNewTaskResultどおりの結果を返す(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 0))
	seen := map[string]int{}
	defer func() { requireAll(t, seen, "session_required", "title_required", "id_conflict", "ok") }()
	for i := range propRuns {
		id, title, body, actor := fmt.Sprintf("%08x", rng.Uint32()), randomText(rng), randomText(rng), randomText(rng)
		idTaken := rng.IntN(4) == 0
		now := rng.Int64() - rng.Int64()
		c, err := NewTask(id, idTaken, title, body, actor, now)
		desc := fmt.Sprintf("#%d idTaken=%v title=%q body=%q actor=%q", i, idTaken, title, body, actor)
		switch {
		case blank(actor):
			seen["session_required"]++
			if CodeOf(err) != CodeSessionRequired {
				t.Fatalf("%s: got %v, want session_required", desc, err)
			}
		case blank(title):
			seen["title_required"]++
			if CodeOf(err) != CodeTitleRequired {
				t.Fatalf("%s: got %v, want title_required", desc, err)
			}
		case idTaken:
			seen["id_conflict"]++
			if CodeOf(err) != CodeIDConflict {
				t.Fatalf("%s: got %v, want id_conflict", desc, err)
			}
		default:
			seen["ok"]++
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

func Test無作為な入力で完了と中止は補題FinishErrorsとFinishResultどおりの結果を返す(t *testing.T) {
	rng := rand.New(rand.NewPCG(2, 0))
	seen := map[string]int{}
	defer func() {
		requireAll(t, seen, "session_required", "reason_required", "not_found", "already_finished:done", "already_finished:canceled", "ok:done", "ok:canceled")
	}()
	for i := range propRuns {
		// DB から読んだタスクは不変条件（タイトルが空でない）を満たす前提なので、タイトルには空白でない文字を必ず含める
		cur := Task{
			ID: fmt.Sprintf("%08x", rng.Uint32()), Title: randomText(rng) + "t", Body: randomText(rng),
			Status: propStatuses[rng.IntN(len(propStatuses))], CreatedAt: rng.Int64() - rng.Int64(),
		}
		var found *Task
		if rng.IntN(4) != 0 {
			found = &cur
		}
		o := propOutcomes[rng.IntN(len(propOutcomes))]
		reason, actor := randomText(rng), randomText(rng)
		now := rng.Int64() - rng.Int64()
		desc := fmt.Sprintf("#%d found=%v task=%+v outcome=%d reason=%q actor=%q", i, found != nil, cur, o, reason, actor)

		c, err := Finish(found, o, reason, actor, now)
		switch {
		case blank(actor):
			seen["session_required"]++
			if CodeOf(err) != CodeSessionRequired {
				t.Fatalf("%s: got %v, want session_required", desc, err)
			}
		case blank(reason):
			seen["reason_required"]++
			if CodeOf(err) != CodeReasonRequired {
				t.Fatalf("%s: got %v, want reason_required", desc, err)
			}
		case found == nil:
			seen["not_found"]++
			if CodeOf(err) != CodeNotFound {
				t.Fatalf("%s: got %v, want not_found", desc, err)
			}
		case finished(t, cur.Status):
			seen["already_finished:"+string(cur.Status)]++
			if CodeOf(err) != CodeAlreadyFinished {
				t.Fatalf("%s: got %v, want already_finished", desc, err)
			}
		default:
			if err != nil {
				t.Fatalf("%s: %v", desc, err)
			}
			// 状態だけが変わり、ID・タイトル・本文・作成日時は変わらない
			want := cur
			want.Status = outcomeStatus(t, o)
			seen["ok:"+string(want.Status)]++
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
	seen := map[string]int{}
	defer func() {
		requireAll(t, seen, "todo:false", "todo:true", "done:false", "done:true", "canceled:false", "canceled:true")
	}()
	for i := range propRuns {
		tk := Task{ID: "x", Title: randomText(rng) + "t", Status: propStatuses[rng.IntN(len(propStatuses))]}
		inc := rng.IntN(2) == 0
		seen[fmt.Sprintf("%s:%v", tk.Status, inc)]++
		got, err := visible(tk, inc)
		if want := inc || !finished(t, tk.Status); err != nil || got != want {
			t.Fatalf("#%d status=%s includeFinished=%v: got %v, %v, want %v", i, tk.Status, inc, got, err, want)
		}
	}
}

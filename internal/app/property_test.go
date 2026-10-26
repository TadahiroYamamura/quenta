package app

import (
	"context"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/TadahiroYamamura/quenta/internal/store"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

// プロパティベースのテスト。無作為な操作の列を、実装（App と SQLite）と仕様の手本（task.Model。Dafny の State の操作を変換したもの）の両方に送り、
// 各歩で次を確かめる。括弧内は spec/task.dfy の対応する性質
//   - 実装の結果（エラーコード、タスク、イベント、一覧、詳細）が手本と同じ（AppliedLikeModel・FoundObserved・IdTakenObserved のもとでの
//     ImplAddMatchesModel・ImplFinishMatchesModel・ImplShowMatchesModel、AddResult・FinishTaskResult・ListOrdered）
//   - 実装から読み出した状態で不変条件が成り立つ（Valid。AddPreservesValid・FinishPreservesValid）
//   - 終わったタスクは変わらない（FinishedTasksNeverChange）
//   - イベントは追記だけ（EventsAppendOnly）
//   - エラーのときは何も変わらない（仕様ではエラーのとき新しい状態を返さない）
//
// ID は少ない候補から選び、重なり（IdConflict と作り直し）がよく起きるようにする。
// 時刻も狭い範囲から無作為に選び、作成日時が同じものや、登録した順と逆になるものを作る

const (
	propRuns  = 150
	propSteps = 40
)

var (
	propIDs     = []string{"00000001", "00000002", "00000003", "00000004", "00000005", "00000006"}
	propTitles  = []string{"", " ", "　", "\n", "t", " 前後に空白 ", "日本語🎉"}
	propBodies  = []string{"", "本文", " "}
	propActors  = []string{"", " ", "s1", "s2"}
	propReasons = []string{"", "\t", "r", "理由"}
)

type stepClock struct{ ms int64 }

func (c *stepClock) now() time.Time { return time.UnixMilli(c.ms) }

type stepIDs struct {
	list []string
	i    int
}

func (g *stepIDs) next() (string, error) {
	if g.i >= len(g.list) {
		return "", fmt.Errorf("候補の ID を使い切った")
	}
	id := g.list[g.i]
	g.i++
	return id, nil
}

// 守る: applied-like-model, found-observed, id-taken-observed, tasks-observed, stored-state-valid, add-result, finish-task-result, finished-unchanged-by-add, tasks-never-disappear-add, events-append-only-add, events-append-only-finish, list-only-visible, list-all-visible, list-no-duplicates, list-order-by-created, show-result, unique-ids
func Test無作為な操作の列で実装は仕様の手本と同じ結果になり仕様の性質が成り立つ(t *testing.T) {
	seen := map[string]int{}
	for seed := uint64(1); seed <= propRuns; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			runProperty(t, seed, seen)
		})
	}
	// 操作の列が偏って、確かめたつもりの場面を一度も通っていない、ということがないようにする
	want := []string{
		"add:ok", "add:retried", "add:session_required", "add:title_required", "add:internal",
		"finish:ok:done", "finish:ok:canceled", "finish:session_required", "finish:reason_required", "finish:not_found", "finish:already_finished",
		"list:tie",
	}
	for _, k := range want {
		if seen[k] == 0 {
			t.Errorf("%s を一度も通っていない（通った場面: %v）", k, seen)
		}
	}
}

func runProperty(t *testing.T, seed uint64, seen map[string]int) {
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(seed, 0))
	s, err := store.Open(filepath.Join(t.TempDir(), "quenta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	clock := &stepClock{}
	gen := &stepIDs{}
	a := New(s, WithClock(clock.now), WithIDGenerator(gen.next))
	m := task.NewModel()
	pick := func(xs []string) string { return xs[rng.IntN(len(xs))] }

	for step := 0; step < propSteps; step++ {
		clock.ms = rng.Int64N(6)
		prevTasks, prevEvents := readAll(t, s)

		var desc string
		var implErr, modelErr error
		if rng.IntN(2) == 0 {
			title, body, actor := pick(propTitles), pick(propBodies), pick(propActors)
			// 実装が作り直しに使う ID の候補。手本でも同じ順に試す
			gen.list, gen.i = make([]string, maxIDAttempts), 0
			for i := range gen.list {
				gen.list[i] = pick(propIDs)
			}
			desc = fmt.Sprintf("add title=%q body=%q actor=%q ids=%v t=%d", title, body, actor, gen.list, clock.ms)
			_, implErr = a.Add(ctx, title, body, actor)
			modelErr = modelAdd(m, gen.list, title, body, actor, clock.ms)
			seen["add:"+outcomeKey(implErr)]++
			if implErr == nil && gen.i > 1 {
				seen["add:retried"]++
			}
		} else {
			id := pick(append(propIDs, "ffffffff"))
			o := []task.Outcome{task.OutcomeComplete, task.OutcomeCancel}[rng.IntN(2)]
			reason, actor := pick(propReasons), pick(propActors)
			desc = fmt.Sprintf("finish id=%s outcome=%d reason=%q actor=%q t=%d", id, o, reason, actor, clock.ms)
			var done task.Task
			done, implErr = a.Finish(ctx, id, o, reason, actor)
			modelErr = m.Finish(id, o, reason, actor, clock.ms)
			key := "finish:" + outcomeKey(implErr)
			if implErr == nil {
				key += ":" + string(done.Status)
			}
			seen[key]++
		}
		fail := func(format string, args ...any) {
			t.Helper()
			t.Fatalf("seed=%d step=%d %s: %s", seed, step, desc, fmt.Sprintf(format, args...))
		}

		if ic, mc := task.CodeOf(implErr), task.CodeOf(modelErr); (implErr == nil) != (modelErr == nil) || (implErr != nil && ic != mc) {
			fail("エラーが手本と違う: 実装=%v 手本=%v", implErr, modelErr)
		}

		tasks, events := readAll(t, s)
		wantTasks, err := m.Tasks()
		if err != nil {
			t.Fatal(err)
		}
		wantEvents, err := m.Events()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(nonNil(tasks), nonNil(wantTasks)) {
			fail("タスクが手本と違う:\n実装=%+v\n手本=%+v", tasks, wantTasks)
		}
		if !reflect.DeepEqual(nonNil(events), nonNil(wantEvents)) {
			fail("イベントが手本と違う:\n実装=%+v\n手本=%+v", events, wantEvents)
		}

		valid, err := task.ValidState(tasks, events)
		if err != nil {
			t.Fatal(err)
		}
		if !valid {
			fail("不変条件が成り立たない: tasks=%+v events=%+v", tasks, events)
		}

		if implErr != nil && (!reflect.DeepEqual(tasks, prevTasks) || !reflect.DeepEqual(events, prevEvents)) {
			fail("エラーなのに状態が変わった")
		}

		if len(events) < len(prevEvents) || !reflect.DeepEqual(nonNil(events[:len(prevEvents)]), nonNil(prevEvents)) {
			fail("イベントが追記だけになっていない")
		}

		for i, p := range prevTasks {
			if p.Status != task.StatusTodo && tasks[i] != p {
				fail("終わったタスクが変わった: %+v -> %+v", p, tasks[i])
			}
		}

		for _, inc := range []bool{false, true} {
			got, err := a.List(ctx, inc)
			if err != nil {
				t.Fatal(err)
			}
			want, err := m.List(inc)
			if err != nil {
				t.Fatal(err)
			}
			if hasCreatedAtTie(got) {
				seen["list:tie"]++
			}
			if !reflect.DeepEqual(nonNil(got), nonNil(want)) {
				fail("一覧（includeFinished=%v）が手本と違う:\n実装=%+v\n手本=%+v", inc, got, want)
			}
		}

		for _, id := range append(propIDs, "ffffffff") {
			gt, gevs, gerr := a.Show(ctx, id)
			wt, wevs, werr := m.Show(id)
			if task.CodeOf(gerr) != task.CodeOf(werr) || (gerr == nil) != (werr == nil) {
				fail("詳細 %s のエラーが手本と違う: 実装=%v 手本=%v", id, gerr, werr)
			}
			if gerr == nil && (gt != wt || !reflect.DeepEqual(nonNil(gevs), nonNil(wevs))) {
				fail("詳細 %s が手本と違う:\n実装=%+v %+v\n手本=%+v %+v", id, gt, gevs, wt, wevs)
			}
		}
	}
	if !m.Valid() {
		t.Fatalf("seed=%d: 手本の状態で不変条件が成り立たない", seed)
	}
}

func outcomeKey(err error) string {
	if err == nil {
		return "ok"
	}
	return string(task.CodeOf(err))
}

// hasCreatedAtTie は、作成日時が同じタスクが並んでいるか（登録した順で並べる場面を通ったか）
func hasCreatedAtTie(ts []task.Task) bool {
	for i := 1; i < len(ts); i++ {
		if ts[i-1].CreatedAt == ts[i].CreatedAt {
			return true
		}
	}
	return false
}

// modelAdd は、実装の登録と同じく、ID が既にあれば次の候補で作り直す
func modelAdd(m *task.Model, candidates []string, title, body, actor string, now int64) error {
	for _, id := range candidates {
		err := m.Add(id, title, body, actor, now)
		if task.CodeOf(err) == task.CodeIDConflict {
			continue
		}
		return err
	}
	return task.Errorf(task.CodeInternal, "候補の ID がすべて既にある")
}

func readAll(t *testing.T, s *store.Store) ([]task.Task, []task.Event) {
	t.Helper()
	ctx := context.Background()
	tasks, err := s.AllTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.AllEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return tasks, events
}

// nonNil は、nil の slice と空の slice を同じものとして比べるため
func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

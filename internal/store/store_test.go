package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"

	"github.com/TadahiroYamamura/quenta/internal/task"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quenta", "quenta.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func ptr[T any](v T) *T { return &v }

func registration(t task.Task, actor string) task.Event {
	return task.Event{TaskID: t.ID, Actor: actor, To: task.StatusTodo, At: t.CreatedAt}
}

func TestDefaultPathはXDG_DATA_HOMEが絶対パスならその下を使い無ければHOMEの下を使う(t *testing.T) {
	cases := []struct {
		name      string
		env       map[string]string
		want      string
		wantError bool
	}{
		{"XDG_DATA_HOME が絶対パス", map[string]string{"XDG_DATA_HOME": "/x/data", "HOME": "/home/u"}, "/x/data/quenta/quenta.db", false},
		{"XDG_DATA_HOME が未設定", map[string]string{"HOME": "/home/u"}, "/home/u/.local/share/quenta/quenta.db", false},
		{"XDG_DATA_HOME が相対パスなら無視する", map[string]string{"XDG_DATA_HOME": "rel/data", "HOME": "/home/u"}, "/home/u/.local/share/quenta/quenta.db", false},
		{"どちらもない", map[string]string{}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DefaultPath(func(k string) string { return c.env[k] })
			if (err != nil) != c.wantError {
				t.Fatalf("err = %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func Test保存したタスクとイベントは読み出しても値が変わらない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	texts := []string{"", "日本語", "絵文字🎉\n改行\t\"引用符\" 'single'", "  前後の空白  ", "\x00NUL"}
	times := []int64{0, -1, math.MaxInt64, math.MinInt64, 1700000000123}
	var want []task.Task
	var wantEvents []task.Event
	for i, text := range texts {
		tk := task.Task{ID: string(rune('a'+i)) + "0000000", Title: text, Body: text, Status: task.StatusTodo, CreatedAt: times[i]}
		ev := registration(tk, text+"actor")
		if err := s.InsertTask(ctx, tk, ev); err != nil {
			t.Fatal(err)
		}
		want = append(want, tk)
		wantEvents = append(wantEvents, ev)
	}
	// 変更前・理由のあるイベントも往復させる
	reason := "理由🎉"
	finished, err := s.UpdateTask(ctx, want[0].ID, func(cur task.Task) (task.Task, task.Event, error) {
		next := cur
		next.Status = task.StatusCanceled
		return next, task.Event{TaskID: cur.ID, Actor: "s", From: ptr(task.StatusTodo), To: task.StatusCanceled, Reason: &reason, At: math.MinInt64}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want[0] = finished
	wantEvents = append(wantEvents, task.Event{TaskID: want[0].ID, Actor: "s", From: ptr(task.StatusTodo), To: task.StatusCanceled, Reason: &reason, At: math.MinInt64})

	got, err := s.AllTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tasks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("task %d: got %+v, want %+v", i, got[i], want[i])
		}
		one, err := s.GetTask(ctx, want[i].ID)
		if err != nil || one != want[i] {
			t.Errorf("GetTask %d: got %+v, %v", i, one, err)
		}
	}
	gotEvents, err := s.AllEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gotEvents) != len(wantEvents) {
		t.Fatalf("got %d events, want %d", len(gotEvents), len(wantEvents))
	}
	for i := range wantEvents {
		if !eventEqual(gotEvents[i], wantEvents[i]) {
			t.Errorf("event %d: got %+v, want %+v", i, gotEvents[i], wantEvents[i])
		}
	}
	of, err := s.EventsOf(ctx, want[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(of) != 2 || !eventEqual(of[0], wantEvents[0]) || !eventEqual(of[1], wantEvents[len(wantEvents)-1]) {
		t.Errorf("EventsOf: got %+v", of)
	}
}

func Test既にあるIDで保存しようとするとid_conflictを返し既存のタスクもイベントも変わらない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	orig := task.Task{ID: "0a1b2c3d", Title: "元", Body: "元の本文", Status: task.StatusTodo, CreatedAt: 1}
	if err := s.InsertTask(ctx, orig, registration(orig, "s1")); err != nil {
		t.Fatal(err)
	}
	dup := task.Task{ID: "0a1b2c3d", Title: "後", Body: "後の本文", Status: task.StatusTodo, CreatedAt: 2}
	err := s.InsertTask(ctx, dup, registration(dup, "s2"))
	if got := task.CodeOf(err); got != task.CodeIDConflict {
		t.Fatalf("got %s (%v), want id_conflict", got, err)
	}
	all, _ := s.AllTasks(ctx)
	if len(all) != 1 || all[0] != orig {
		t.Errorf("tasks: %+v", all)
	}
	evs, _ := s.AllEvents(ctx)
	if len(evs) != 1 || evs[0].Actor != "s1" {
		t.Errorf("events: %+v", evs)
	}
}

func Test変更がエラーを返すとタスクもイベントも保存しない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	orig := task.Task{ID: "0a1b2c3d", Title: "t", Status: task.StatusTodo, CreatedAt: 1}
	if err := s.InsertTask(ctx, orig, registration(orig, "s1")); err != nil {
		t.Fatal(err)
	}
	boom := task.Errorf(task.CodeAlreadyFinished, "x")
	_, err := s.UpdateTask(ctx, orig.ID, func(cur task.Task) (task.Task, task.Event, error) {
		return task.Task{}, task.Event{}, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	got, _ := s.GetTask(ctx, orig.ID)
	evs, _ := s.AllEvents(ctx)
	if got != orig || len(evs) != 1 {
		t.Errorf("変わった: %+v %+v", got, evs)
	}
}

func Testイベントを保存できなければタスクの変更も残らない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	orig := task.Task{ID: "0a1b2c3d", Title: "t", Status: task.StatusTodo, CreatedAt: 1}
	if err := s.InsertTask(ctx, orig, registration(orig, "s1")); err != nil {
		t.Fatal(err)
	}
	// 存在しないタスクを指すイベントは外部キーの制約で保存できない
	_, err := s.UpdateTask(ctx, orig.ID, func(cur task.Task) (task.Task, task.Event, error) {
		next := cur
		next.Status = task.StatusDone
		return next, task.Event{TaskID: "ffffffff", Actor: "s", From: ptr(task.StatusTodo), To: task.StatusDone, Reason: ptr("r"), At: 2}, nil
	})
	if err == nil {
		t.Fatal("エラーにならなかった")
	}
	got, _ := s.GetTask(ctx, orig.ID)
	if got != orig {
		t.Errorf("タスクが変わった: %+v", got)
	}
}

func Test存在しないIDを読むとnot_foundを返す(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.GetTask(ctx, "ffffffff"); task.CodeOf(err) != task.CodeNotFound {
		t.Errorf("GetTask: %v", err)
	}
	called := false
	_, err := s.UpdateTask(ctx, "ffffffff", func(cur task.Task) (task.Task, task.Event, error) {
		called = true
		return cur, task.Event{}, nil
	})
	if task.CodeOf(err) != task.CodeNotFound || called {
		t.Errorf("UpdateTask: %v, called=%v", err, called)
	}
}

func Test一覧は作成日時の古い順で作成日時が同じなら登録した順に返す(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	// 登録した順と作成日時の順をずらし、同じ作成日時も混ぜる
	in := []task.Task{
		{ID: "00000001", Title: "a", Status: task.StatusTodo, CreatedAt: 30},
		{ID: "00000002", Title: "b", Status: task.StatusTodo, CreatedAt: 10},
		{ID: "00000003", Title: "c", Status: task.StatusTodo, CreatedAt: 30},
		{ID: "00000004", Title: "d", Status: task.StatusTodo, CreatedAt: 20},
		{ID: "00000005", Title: "e", Status: task.StatusTodo, CreatedAt: 10},
	}
	for _, tk := range in {
		if err := s.InsertTask(ctx, tk, registration(tk, "s")); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"00000002", "00000005", "00000004", "00000001", "00000003"}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("got order %v, want %v", ids(got), want)
		}
	}
}

func Test閉じて開き直しても保存したタスクとイベントが残っている(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	tk := task.Task{ID: "0a1b2c3d", Title: "t", Status: task.StatusTodo, CreatedAt: 1}
	if err := s.InsertTask(ctx, tk, registration(tk, "s")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.GetTask(ctx, tk.ID)
	if err != nil || got != tk {
		t.Errorf("got %+v, %v", got, err)
	}
	evs, _ := s2.EventsOf(ctx, tk.ID)
	if len(evs) != 1 {
		t.Errorf("events: %+v", evs)
	}
}

func TestデータベースはWALモードで開く(t *testing.T) {
	s, _ := openTemp(t)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q", mode)
	}
}

func Test未知の状態が保存されていると読み出しはエラーを返す(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.db.Exec(`INSERT INTO tasks (id, title, body, status, created_at) VALUES ('0a1b2c3d', 't', '', 'doing', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTask(ctx, "0a1b2c3d"); err == nil {
		t.Error("GetTask がエラーにならなかった")
	}
	if _, err := s.ListTasks(ctx); err == nil {
		t.Error("ListTasks がエラーにならなかった")
	}
	if _, err := s.db.Exec(`INSERT INTO events (task_id, actor, from_status, to_status, reason, at) VALUES ('0a1b2c3d', 's', 'doing', 'done', 'r', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllEvents(ctx); err == nil {
		t.Error("AllEvents がエラーにならなかった")
	}
}

func ids(ts []task.Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func eventEqual(a, b task.Event) bool {
	return a.TaskID == b.TaskID && a.Actor == b.Actor && a.To == b.To && a.At == b.At &&
		ptrEqual(a.From, b.From) && ptrEqual(a.Reason, b.Reason)
}

func ptrEqual[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

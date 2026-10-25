package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strings"
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

// add は、仕様の NewTask で作った登録の状態の変更を保存する
func add(t *testing.T, s *Store, id, title, body, actor string, now int64) task.Task {
	t.Helper()
	c, err := task.NewTask(id, title, body, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertTask(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return c.Task()
}

// finish は、仕様の Finish で id のタスクを完了・中止にする
func finish(s *Store, id string, o task.Outcome, reason, actor string, now int64) (task.Task, error) {
	return s.UpdateTask(context.Background(), id, func(cur task.Task) (task.Change, error) {
		return task.Finish(cur, o, reason, actor, now)
	})
}

func ptr[T any](v T) *T { return &v }

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
		id := string(rune('a'+i)) + "0000000"
		tk := add(t, s, id, "t"+text, text, "actor"+text, times[i])
		want = append(want, tk)
		wantEvents = append(wantEvents, task.Event{TaskID: id, Actor: "actor" + text, To: task.StatusTodo, At: times[i]})
	}
	// 変更前・理由のあるイベントも往復させる
	reason := "理由🎉"
	finished, err := finish(s, want[0].ID, task.OutcomeCancel, reason, "s", math.MinInt64)
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
	orig := add(t, s, "0a1b2c3d", "元", "元の本文", "s1", 1)
	dup, err := task.NewTask("0a1b2c3d", "後", "後の本文", "s2", 2)
	if err != nil {
		t.Fatal(err)
	}
	err = s.InsertTask(ctx, dup)
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
	orig := add(t, s, "0a1b2c3d", "t", "", "s1", 1)
	boom := task.Errorf(task.CodeAlreadyFinished, "x")
	_, err := s.UpdateTask(ctx, orig.ID, func(cur task.Task) (task.Change, error) {
		return task.Change{}, boom
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

// failEventsBy は、主体が actor のイベントの保存を失敗させるトリガーを、テストの DB に置く
func failEventsBy(t *testing.T, s *Store, actor string) {
	t.Helper()
	if _, err := s.db.Exec(`CREATE TRIGGER test_fail_event BEFORE INSERT ON events WHEN NEW.actor = '` + actor + `'
		BEGIN SELECT RAISE(ABORT, 'テストで失敗させた'); END`); err != nil {
		t.Fatal(err)
	}
}

func Testイベントを保存できなければタスクの変更も残らない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	orig := add(t, s, "0a1b2c3d", "t", "", "s1", 1)
	failEventsBy(t, s, "boom")
	if _, err := finish(s, orig.ID, task.OutcomeComplete, "r", "boom", 2); err == nil {
		t.Fatal("エラーにならなかった")
	}
	got, _ := s.GetTask(ctx, orig.ID)
	evs, _ := s.AllEvents(ctx)
	if got != orig || len(evs) != 1 {
		t.Errorf("変わった: %+v %+v", got, evs)
	}
}

func Test登録のイベントを保存できなければタスクも残らない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	failEventsBy(t, s, "boom")
	c, err := task.NewTask("0a1b2c3d", "t", "", "boom", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertTask(ctx, c); err == nil {
		t.Fatal("エラーにならなかった")
	}
	all, _ := s.AllTasks(ctx)
	if len(all) != 0 {
		t.Errorf("タスクが残った: %+v", all)
	}
}

func Test存在しないタスクを指すイベントは外部キーの制約で保存できない(t *testing.T) {
	s, _ := openTemp(t)
	_, err := s.db.Exec(`INSERT INTO events (task_id, actor, to_status, at) VALUES ('ffffffff', 's', 'todo', 1)`)
	if err == nil {
		t.Error("保存できてしまった")
	}
}

func Test仕様の関数が作っていない状態の変更や別のタスクの変更は保存しない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	a := add(t, s, "0000000a", "a", "", "s", 1)
	b := add(t, s, "0000000b", "b", "", "s", 1)

	// 登録でない状態の変更を、登録として保存しない
	other, err := task.Finish(a, task.OutcomeComplete, "r", "s", 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertTask(ctx, other); task.CodeOf(err) != task.CodeInternal {
		t.Errorf("InsertTask(完了の変更): %v", err)
	}
	// b の変更として、a の完了を保存しない
	if _, err := s.UpdateTask(ctx, b.ID, func(task.Task) (task.Change, error) { return other, nil }); task.CodeOf(err) != task.CodeInternal {
		t.Errorf("UpdateTask(別のタスクの変更): %v", err)
	}
	// 外で作った値（ゼロ値）は、SQL を実行する前に、仕様の関数が作った値でないことを理由に拒む
	err = s.inTx(ctx, func(tx *sql.Tx) error { return save(ctx, tx, task.Change{}) })
	if te, ok := errors.AsType[*task.Error](err); !ok || te.Code != task.CodeInternal || !strings.Contains(te.Message, "仕様の関数が作っていない") {
		t.Errorf("save(ゼロ値): %v", err)
	}
	all, _ := s.AllTasks(ctx)
	if len(all) != 2 || all[0] != a || all[1] != b {
		t.Errorf("変わった: %+v", all)
	}
	if evs, _ := s.AllEvents(ctx); len(evs) != 2 {
		t.Errorf("イベントが増えた: %+v", evs)
	}
}

func Test存在しないIDを読むとnot_foundを返す(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.GetTask(ctx, "ffffffff"); task.CodeOf(err) != task.CodeNotFound {
		t.Errorf("GetTask: %v", err)
	}
	called := false
	_, err := s.UpdateTask(ctx, "ffffffff", func(cur task.Task) (task.Change, error) {
		called = true
		return task.Change{}, nil
	})
	if task.CodeOf(err) != task.CodeNotFound || called {
		t.Errorf("UpdateTask: %v, called=%v", err, called)
	}
}

func Test一覧は作成日時の古い順で作成日時が同じなら登録した順に返す(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	// 登録した順と作成日時の順をずらし、同じ作成日時も混ぜる
	for _, x := range []struct {
		id string
		at int64
	}{{"00000001", 30}, {"00000002", 10}, {"00000003", 30}, {"00000004", 20}, {"00000005", 10}} {
		add(t, s, x.id, "t", "", "s", x.at)
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

// ---------- DB の制約（spec/task.dfy の保証の対応表で「DB の制約」としたもの） ----------

// snapshotRows は、tasks と events の全行を、登録した順・記録した順の番号も含めて返す
func snapshotRows(t *testing.T, s *Store) []string {
	t.Helper()
	var out []string
	for _, q := range []string{
		`SELECT seq || '|' || id || '|' || title || '|' || body || '|' || status || '|' || created_at FROM tasks ORDER BY seq`,
		`SELECT seq || '|' || task_id || '|' || actor || '|' || IFNULL(from_status, '-') || '|' || to_status || '|' || IFNULL(reason, '-') || '|' || at FROM events ORDER BY seq`,
	} {
		rows, err := s.db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var r string
			if err := rows.Scan(&r); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		rows.Close()
	}
	return out
}

func assertRejected(t *testing.T, s *Store, before []string, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err == nil {
			t.Errorf("拒まれなかった: %s", q)
		}
	}
	if after := snapshotRows(t, s); !equalStrings(after, before) {
		t.Errorf("行が変わった:\n前 %v\n後 %v", before, after)
	}
}

func Testタスクは削除できない(t *testing.T) {
	s, _ := openTemp(t)
	add(t, s, "0000000a", "a", "", "s", 1)
	add(t, s, "0000000b", "b", "", "s", 2)
	// 登録のイベントがあるタスクは、外部キーの制約でも削除を拒まれる。
	// トリガーだけで拒むことを確かめるため、イベントのないタスクも直接入れる
	if _, err := s.db.Exec(`INSERT INTO tasks (id, title, body, status, created_at) VALUES ('0000000c', 'c', '', 'todo', 3)`); err != nil {
		t.Fatal(err)
	}
	assertRejected(t, s, snapshotRows(t, s),
		`DELETE FROM tasks WHERE id = '0000000c'`,
		`DELETE FROM tasks WHERE id = '0000000a'`,
		`DELETE FROM tasks`,
	)
}

func Test上書きで保存しようとすると拒まれる(t *testing.T) {
	s, _ := openTemp(t)
	add(t, s, "0000000a", "a", "本文", "s", 1)
	assertRejected(t, s, snapshotRows(t, s),
		// 削除してから入れ直す形の上書き（登録した順も変わる）
		`INSERT OR REPLACE INTO tasks (id, title, body, status, created_at) VALUES ('0000000a', '上書き', '', 'todo', 1)`,
		`REPLACE INTO tasks (id, title, body, status, created_at) VALUES ('0000000a', '上書き', '', 'todo', 1)`,
		// 書き換える形の上書き
		`INSERT INTO tasks (id, title, body, status, created_at) VALUES ('0000000a', '上書き', '', 'todo', 1)
		 ON CONFLICT(id) DO UPDATE SET title = excluded.title`,
	)
}

func Testタスクの状態以外の列は書き換えられない(t *testing.T) {
	s, _ := openTemp(t)
	add(t, s, "0000000a", "a", "本文", "s", 1)
	assertRejected(t, s, snapshotRows(t, s),
		`UPDATE tasks SET id = '0000000z' WHERE id = '0000000a'`,
		`UPDATE tasks SET title = '別' WHERE id = '0000000a'`,
		`UPDATE tasks SET body = '別' WHERE id = '0000000a'`,
		`UPDATE tasks SET created_at = 99 WHERE id = '0000000a'`,
		`UPDATE tasks SET seq = 99 WHERE id = '0000000a'`,
	)
	// 状態以外の列を同じ値で書き、状態を変える UPDATE は通る（完了・中止を妨げない）
	if _, err := s.db.Exec(`UPDATE tasks SET title = 'a', body = '本文', status = 'done' WHERE id = '0000000a'`); err != nil {
		t.Errorf("状態の書き換えが拒まれた: %v", err)
	}
}

func Test終わったタスクは書き換えられない(t *testing.T) {
	s, _ := openTemp(t)
	add(t, s, "0000000a", "a", "", "s", 1)
	add(t, s, "0000000b", "b", "", "s", 1)
	if _, err := finish(s, "0000000a", task.OutcomeComplete, "r", "s", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := finish(s, "0000000b", task.OutcomeCancel, "r", "s", 2); err != nil {
		t.Fatal(err)
	}
	assertRejected(t, s, snapshotRows(t, s),
		`UPDATE tasks SET status = 'todo' WHERE id = '0000000a'`,
		`UPDATE tasks SET status = 'canceled' WHERE id = '0000000a'`,
		`UPDATE tasks SET status = 'todo' WHERE id = '0000000b'`,
		`UPDATE tasks SET status = 'done' WHERE id = '0000000b'`,
	)
}

func Testイベントは書き換えも削除もできない(t *testing.T) {
	s, _ := openTemp(t)
	add(t, s, "0000000a", "a", "", "s", 1)
	if _, err := finish(s, "0000000a", task.OutcomeComplete, "r", "s", 2); err != nil {
		t.Fatal(err)
	}
	assertRejected(t, s, snapshotRows(t, s),
		`UPDATE events SET reason = '別' WHERE task_id = '0000000a'`,
		`UPDATE events SET actor = '別'`,
		`DELETE FROM events WHERE task_id = '0000000a'`,
		`DELETE FROM events`,
		`INSERT OR REPLACE INTO events (seq, task_id, actor, to_status, at) VALUES (1, '0000000a', '別', 'todo', 1)`,
	)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func Test閉じて開き直しても保存したタスクとイベントが残っている(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	tk := add(t, s, "0a1b2c3d", "t", "", "s", 1)
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

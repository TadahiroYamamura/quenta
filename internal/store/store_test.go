package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

// insert は、仕様の NewTask で作った登録の状態の変更を保存する
func insert(s *Store, id, title, body, actor string, now int64) (task.Task, error) {
	return s.InsertTask(context.Background(), id, func(idTaken bool) (task.Change, error) {
		return task.NewTask(id, idTaken, title, body, actor, now)
	})
}

func add(t *testing.T, s *Store, id, title, body, actor string, now int64) task.Task {
	t.Helper()
	tk, err := insert(s, id, title, body, actor, now)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// finish は、仕様の Finish で id のタスクを完了・中止にする
func finish(s *Store, id string, o task.Outcome, reason, actor string, now int64) (task.Task, error) {
	return s.UpdateTask(context.Background(), id, func(found *task.Task) (task.Change, error) {
		return task.Finish(found, o, reason, actor, now)
	})
}

// find は、id のタスクを読む。なければテストを失敗させる
func find(t *testing.T, s *Store, id string) task.Task {
	t.Helper()
	got, err := s.FindTask(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatalf("タスク %s がない", id)
	}
	return *got
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
		if one := find(t, s, want[i].ID); one != want[i] {
			t.Errorf("FindTask %d: got %+v", i, one)
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

// 守る: id-taken-observed, new-task-id-conflict
func Test登録はIDのタスクがあるかを調べて渡しあればid_conflictを返し何も変えない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	orig := add(t, s, "0a1b2c3d", "元", "元の本文", "s1", 1)
	var observed []bool
	observe := func(id string) error {
		_, err := s.InsertTask(ctx, id, func(idTaken bool) (task.Change, error) {
			observed = append(observed, idTaken)
			return task.NewTask(id, idTaken, "後", "", "s2", 2)
		})
		return err
	}
	if err := observe("0a1b2c3d"); task.CodeOf(err) != task.CodeIDConflict {
		t.Errorf("既にある ID: got %v, want id_conflict", err)
	}
	if err := observe("0a1b2c3e"); err != nil {
		t.Errorf("まだない ID: %v", err)
	}
	if len(observed) != 2 || !observed[0] || observed[1] {
		t.Errorf("渡した事実が DB と違う: %v（want [true false]）", observed)
	}
	if got := find(t, s, "0a1b2c3d"); got != orig {
		t.Errorf("既存のタスクが変わった: %+v", got)
	}
}

// 守る: unique-ids, id-taken-observed, new-task-id-conflict, list-no-duplicates
func Test調べた事実が誤っていても既にあるIDでの保存は一意の制約が拒みid_conflictを返し何も変えない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	orig := add(t, s, "0a1b2c3d", "元", "元の本文", "s1", 1)
	// 調べてから書くまでのあいだに、別の書き手が同じ ID を保存した場面を、調べた事実を無視して作る
	_, err := s.InsertTask(ctx, "0a1b2c3d", func(bool) (task.Change, error) {
		return task.NewTask("0a1b2c3d", false, "後", "後の本文", "s2", 2)
	})
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

// 守る: applied-like-model
func Test仕様の関数がエラーを返すとタスクもイベントも保存しない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	orig := add(t, s, "0a1b2c3d", "t", "", "s1", 1)
	before := snapshotRows(t, s)
	boom := task.Errorf(task.CodeAlreadyFinished, "x")
	_, err := s.UpdateTask(ctx, orig.ID, func(*task.Task) (task.Change, error) {
		return task.Change{}, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("UpdateTask: got %v", err)
	}
	_, err = s.InsertTask(ctx, "0a1b2c3e", func(bool) (task.Change, error) {
		return task.Change{}, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("InsertTask: got %v", err)
	}
	if after := snapshotRows(t, s); !equalStrings(after, before) {
		t.Errorf("行が変わった:\n前 %v\n後 %v", before, after)
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

// 守る: applied-like-model
func Testイベントを保存できなければタスクの変更も残らない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	orig := add(t, s, "0a1b2c3d", "t", "", "s1", 1)
	failEventsBy(t, s, "boom")
	if _, err := finish(s, orig.ID, task.OutcomeComplete, "r", "boom", 2); err == nil {
		t.Fatal("エラーにならなかった")
	}
	got := find(t, s, orig.ID)
	evs, _ := s.AllEvents(ctx)
	if got != orig || len(evs) != 1 {
		t.Errorf("変わった: %+v %+v", got, evs)
	}
}

// 守る: applied-like-model
func Test登録のイベントを保存できなければタスクも残らない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	failEventsBy(t, s, "boom")
	if _, err := insert(s, "0a1b2c3d", "t", "", "boom", 1); err == nil {
		t.Fatal("エラーにならなかった")
	}
	all, _ := s.AllTasks(ctx)
	if len(all) != 0 {
		t.Errorf("タスクが残った: %+v", all)
	}
}

// 守る: applied-like-model
func Test存在しないタスクを指すイベントは外部キーの制約で保存できない(t *testing.T) {
	s, _ := openTemp(t)
	_, err := s.db.Exec(`INSERT INTO events (task_id, actor, to_status, at) VALUES ('ffffffff', 's', 'todo', 1)`)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
		t.Errorf("外部キーの制約で拒まれなかった: %v", err)
	}
}

// 守る: applied-like-model
func Test仕様の関数が作っていない状態の変更や別のタスクの変更は保存しない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	a := add(t, s, "0000000a", "a", "", "s", 1)
	b := add(t, s, "0000000b", "b", "", "s", 1)

	// 登録でない状態の変更を、登録として保存しない
	other, err := task.Finish(&a, task.OutcomeComplete, "r", "s", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertTask(ctx, "0000000c", func(bool) (task.Change, error) { return other, nil }); task.CodeOf(err) != task.CodeInternal {
		t.Errorf("InsertTask(完了の変更): %v", err)
	}
	// 0000000c の登録として、別の ID のタスクの登録を保存しない
	if _, err := s.InsertTask(ctx, "0000000c", func(idTaken bool) (task.Change, error) {
		return task.NewTask("0000000d", idTaken, "d", "", "s", 2)
	}); task.CodeOf(err) != task.CodeInternal {
		t.Errorf("InsertTask(別の ID の登録): %v", err)
	}
	// 同じ ID でも、完了の変更を登録として保存しない（登録か完了・中止かの確かめだけで弾く）
	if _, err := s.InsertTask(ctx, a.ID, func(bool) (task.Change, error) { return other, nil }); task.CodeOf(err) != task.CodeInternal {
		t.Errorf("InsertTask(同じ ID の完了の変更): %v", err)
	}
	// 同じ ID でも、登録の変更を完了・中止として保存しない
	if _, err := s.UpdateTask(ctx, "0000000c", func(*task.Task) (task.Change, error) {
		return task.NewTask("0000000c", false, "c", "", "s", 2)
	}); task.CodeOf(err) != task.CodeInternal {
		t.Errorf("UpdateTask(同じ ID の登録): %v", err)
	}
	// b の変更として、a の完了を保存しない
	if _, err := s.UpdateTask(ctx, b.ID, func(*task.Task) (task.Change, error) { return other, nil }); task.CodeOf(err) != task.CodeInternal {
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

// 守る: found-observed
func Testタスクを探した結果を渡し存在しないIDならnilを渡す(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	a := add(t, s, "0000000a", "a", "", "s", 1)
	if got, err := s.FindTask(ctx, "ffffffff"); got != nil || err != nil {
		t.Errorf("FindTask(ない ID): %+v, %v", got, err)
	}
	for _, c := range []struct {
		id   string
		want *task.Task
	}{{"ffffffff", nil}, {a.ID, &a}} {
		var passed *task.Task
		called := false
		_, err := s.UpdateTask(ctx, c.id, func(found *task.Task) (task.Change, error) {
			called, passed = true, found
			return task.Finish(found, task.OutcomeComplete, "", "s", 2)
		})
		if task.CodeOf(err) != task.CodeReasonRequired || !called {
			t.Errorf("UpdateTask(%s): %v, called=%v", c.id, err, called)
		}
		if (passed == nil) != (c.want == nil) || (passed != nil && *passed != *c.want) {
			t.Errorf("UpdateTask(%s) が渡した結果: %+v, want %+v", c.id, passed, c.want)
		}
	}
}

// 守る: found-observed
func Test探す処理が別のタスクを返すとinternalを返し何も変えない(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	add(t, s, "0000000a", "a", "", "s", 1)
	add(t, s, "0000000b", "b", "", "s", 2)
	before := snapshotRows(t, s)
	// 探し違えて、指定と違う ID の行を返す問い合わせに差し替える
	orig := findTaskQuery
	findTaskQuery = `SELECT id, title, body, status, created_at FROM tasks WHERE id <> ? ORDER BY seq LIMIT 1`
	t.Cleanup(func() { findTaskQuery = orig })

	if _, err := s.FindTask(ctx, "0000000b"); task.CodeOf(err) != task.CodeInternal {
		t.Errorf("FindTask: %v", err)
	}
	called := false
	_, err := s.UpdateTask(ctx, "0000000b", func(found *task.Task) (task.Change, error) {
		called = true
		return task.Finish(found, task.OutcomeComplete, "r", "s", 3)
	})
	if task.CodeOf(err) != task.CodeInternal || called {
		t.Errorf("UpdateTask: %v, called=%v", err, called)
	}
	if after := snapshotRows(t, s); !equalStrings(after, before) {
		t.Errorf("行が変わった:\n前 %v\n後 %v", before, after)
	}
}

// 守る: list-order-by-created, list-order-ties
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

// ---------- DB の制約（spec/guarantees.toml で「DB の制約」としたもの） ----------

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

// assertRejected は、各文が、エラーの文に by を含む制約（トリガー）で拒まれ、行が変わらないことを確かめる。
// 拒んだのが期待した制約であることまで見るのは、ほかの制約（外部キーなど）や SQL の書き誤りで拒まれても通ってしまわないため
func assertRejected(t *testing.T, s *Store, before []string, by string, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		_, err := s.db.Exec(q)
		if err == nil {
			t.Errorf("拒まれなかった: %s", q)
		} else if !strings.Contains(err.Error(), by) {
			t.Errorf("期待した制約（%s）でなく、別の理由で拒まれた: %s: %v", by, q, err)
		}
	}
	if after := snapshotRows(t, s); !equalStrings(after, before) {
		t.Errorf("行が変わった:\n前 %v\n後 %v", before, after)
	}
}

// 守る: tasks-never-disappear-add, tasks-never-disappear-finish, applied-like-model
func Testタスクは削除できない(t *testing.T) {
	s, _ := openTemp(t)
	add(t, s, "0000000a", "a", "", "s", 1)
	add(t, s, "0000000b", "b", "", "s", 2)
	// 登録のイベントがあるタスクは、外部キーの制約でも削除を拒まれる。
	// トリガーだけで拒むことを確かめるため、イベントのないタスクも直接入れる
	if _, err := s.db.Exec(`INSERT INTO tasks (id, title, body, status, created_at) VALUES ('0000000c', 'c', '', 'todo', 3)`); err != nil {
		t.Fatal(err)
	}
	assertRejected(t, s, snapshotRows(t, s), "tasks の行は削除できない",
		`DELETE FROM tasks WHERE id = '0000000c'`,
		`DELETE FROM tasks WHERE id = '0000000a'`,
		`DELETE FROM tasks`,
	)
}

// 守る: tasks-never-disappear-add, add-result, applied-like-model
func Test上書きで保存しようとすると拒まれる(t *testing.T) {
	s, _ := openTemp(t)
	add(t, s, "0000000a", "a", "本文", "s", 1)
	before := snapshotRows(t, s)
	// 削除してから入れ直す形の上書き（登録した順も変わる）は、削除を拒むトリガーが拒む
	assertRejected(t, s, before, "tasks の行は削除できない",
		`INSERT OR REPLACE INTO tasks (id, title, body, status, created_at) VALUES ('0000000a', '上書き', '', 'todo', 1)`,
		`REPLACE INTO tasks (id, title, body, status, created_at) VALUES ('0000000a', '上書き', '', 'todo', 1)`,
	)
	// 書き換える形の上書きは、状態以外の列の書き換えを拒むトリガーが拒む
	assertRejected(t, s, before, "tasks の状態以外の列は書き換えられない",
		`INSERT INTO tasks (id, title, body, status, created_at) VALUES ('0000000a', '上書き', '', 'todo', 1)
		 ON CONFLICT(id) DO UPDATE SET title = excluded.title`,
	)
}

// 守る: tasks-never-disappear-finish, finish-task-result, applied-like-model
func Testタスクの状態以外の列は書き換えられない(t *testing.T) {
	s, _ := openTemp(t)
	add(t, s, "0000000a", "a", "本文", "s", 1)
	assertRejected(t, s, snapshotRows(t, s), "tasks の状態以外の列は書き換えられない",
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

// 守る: finish-already-finished, finish-rejects-finished, finished-unchanged-by-add, finished-unchanged-by-finish, applied-like-model
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
	assertRejected(t, s, snapshotRows(t, s), "終わったタスクは書き換えられない",
		`UPDATE tasks SET status = 'todo' WHERE id = '0000000a'`,
		`UPDATE tasks SET status = 'canceled' WHERE id = '0000000a'`,
		`UPDATE tasks SET status = 'todo' WHERE id = '0000000b'`,
		`UPDATE tasks SET status = 'done' WHERE id = '0000000b'`,
	)
}

// 守る: finish-rejects-finished, finished-unchanged-by-add, finished-unchanged-by-finish
func Test仕様が終わったとみなす状態とトリガーが書き換えを拒む状態が一致する(t *testing.T) {
	statuses, err := task.SpecStatuses()
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) == 0 {
		t.Fatal("仕様の状態が 1 つもない")
	}
	for i, st := range statuses {
		finished, err := task.IsFinished(st)
		if err != nil {
			t.Fatal(err)
		}
		t.Run(string(st), func(t *testing.T) {
			s, _ := openTemp(t)
			id := fmt.Sprintf("%08x", i)
			if _, err := s.db.Exec(`INSERT INTO tasks (id, title, body, status, created_at) VALUES (?, 't', '', ?, 1)`, id, string(st)); err != nil {
				t.Fatal(err)
			}
			// 状態を同じ値で書く UPDATE でも、終わったタスクならトリガーが拒む
			_, err := s.db.Exec(`UPDATE tasks SET status = status WHERE id = ?`, id)
			rejected := err != nil && strings.Contains(err.Error(), "終わったタスクは書き換えられない")
			if err != nil && !rejected {
				t.Fatalf("期待した制約でなく、別の理由で拒まれた: %v", err)
			}
			if rejected != finished {
				t.Errorf("仕様の IsFinished=%v、トリガーが拒んだ=%v", finished, rejected)
			}
		})
	}
}

// 守る: events-append-only-add, events-append-only-finish, applied-like-model
func Testイベントは書き換えも削除もできない(t *testing.T) {
	s, _ := openTemp(t)
	add(t, s, "0000000a", "a", "", "s", 1)
	if _, err := finish(s, "0000000a", task.OutcomeComplete, "r", "s", 2); err != nil {
		t.Fatal(err)
	}
	before := snapshotRows(t, s)
	assertRejected(t, s, before, "events の行は書き換えられない",
		`UPDATE events SET reason = '別' WHERE task_id = '0000000a'`,
		`UPDATE events SET actor = '別'`,
	)
	assertRejected(t, s, before, "events の行は削除できない",
		`DELETE FROM events WHERE task_id = '0000000a'`,
		`DELETE FROM events`,
		// 上書きによる削除も、recursive_triggers を有効にしているので削除を拒むトリガーが拒む
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
	if got := find(t, s2, tk.ID); got != tk {
		t.Errorf("got %+v", got)
	}
	evs, _ := s2.EventsOf(ctx, tk.ID)
	if len(evs) != 1 {
		t.Errorf("events: %+v", evs)
	}
}

func Testデータベースはほかのセッションの書き込みを5秒まで待つ設定で開く(t *testing.T) {
	s, _ := openTemp(t)
	var ms int
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&ms); err != nil {
		t.Fatal(err)
	}
	if ms != 5000 {
		t.Errorf("busy_timeout = %d", ms)
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
	if _, err := s.FindTask(ctx, "0a1b2c3d"); err == nil {
		t.Error("FindTask がエラーにならなかった")
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

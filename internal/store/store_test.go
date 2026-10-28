package store_test

import (
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TadahiroYamamura/quenta/db"
	"github.com/TadahiroYamamura/quenta/internal/oracle"
	Q "github.com/TadahiroYamamura/quenta/internal/spec/Quenta"
	_dafny "github.com/TadahiroYamamura/quenta/internal/spec/dafny"
	"github.com/TadahiroYamamura/quenta/internal/store"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

func open(t *testing.T) (*store.Store, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quenta.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	return s, raw, path
}

func mustExec(t *testing.T, raw *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := raw.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func state(t *testing.T, raw *sql.DB) oracle.State {
	t.Helper()
	st, err := oracle.ReadState(raw)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func ptr(s task.Status) *task.Status { return &s }

func addWrite(id, title, body, actor string, now int64) task.AddWrite {
	return task.AddWrite{
		Task:  task.Task{ID: id, Title: title, Body: body, Status: task.Todo, CreatedAt: now},
		Event: task.Event{TaskID: id, Actor: actor, To: task.Todo, At: now},
	}
}

// @assurance:evidence db-roundtrip
func Testタスクとイベントは渡された値のまま保存され読み出しても変わらない(t *testing.T) {
	s, raw, _ := open(t)
	values := []string{" 前後に空白 ", "\u3000全角\u3000", "\t\n", "多言語 مرحبا 😀", strings.Repeat("長", 40000), "<&>\"'", ""}
	for i, v := range values {
		id := fmt.Sprintf("%08x", i)
		w := addWrite(id, v+"題", v, v+"主体", int64(i))
		if err := s.Write(func(tx *store.Tx) error { return tx.ApplyAdd(w) }); err != nil {
			t.Fatal(err)
		}
		fw := task.FinishWrite{ID: id, Status: task.Done, Event: task.Event{TaskID: id, Actor: v + "主体", From: ptr(task.Todo), To: task.Done, Reason: v + "理由", At: int64(i) + 1}}
		if err := s.Write(func(tx *store.Tx) error { return tx.ApplyFinish(fw) }); err != nil {
			t.Fatal(err)
		}
		err := s.Write(func(tx *store.Tx) error {
			got, err := tx.FindTask(id)
			if err != nil {
				return err
			}
			want := w.Task
			want.Status = task.Done
			if got == nil || *got != want {
				t.Fatalf("読み出したタスク %+v、書いたタスク %+v", got, want)
			}
			es, err := tx.EventsOf(id)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(es, []task.Event{w.Event, fw.Event}) {
				t.Fatalf("読み出したイベント %+v", es)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := len(state(t, raw).Tasks); n != len(values) {
		t.Fatalf("タスクが %d 個", n)
	}
}

// @assurance:evidence db-task-id-unique
func Test同じIDの行を直接入れると拒まれる(t *testing.T) {
	_, raw, _ := open(t)
	mustExec(t, raw, "INSERT INTO tasks VALUES ('00000001', 't', '', 'todo', 1)")
	if _, err := raw.Exec("INSERT INTO tasks VALUES ('00000001', 'u', '', 'todo', 2)"); err == nil {
		t.Fatal("同じ ID の行が入った")
	}
	// 大文字と小文字は別の ID
	mustExec(t, raw, "INSERT INTO tasks VALUES ('0000000A', 't', '', 'todo', 1)")
	mustExec(t, raw, "INSERT INTO tasks VALUES ('0000000a', 't', '', 'todo', 1)")
}

// @assurance:evidence db-finished-unchangeable
func Test終わったタスクの状態をUPDATEで書き換えると拒まれる(t *testing.T) {
	_, raw, _ := open(t)
	for _, st := range task.Statuses {
		for _, to := range task.Statuses {
			id := "id-" + string(st) + "-" + string(to)
			mustExec(t, raw, "INSERT INTO tasks VALUES (?, 't', '', ?, 1)", id, string(st))
			_, err := raw.Exec("UPDATE tasks SET status = ? WHERE id = ?", string(to), id)
			if st.IsFinished() && err == nil {
				t.Fatalf("%s のタスクを %s に書き換えられた", st, to)
			}
			if !st.IsFinished() && err != nil {
				t.Fatalf("%s のタスクを %s に書き換えられなかった: %v", st, to, err)
			}
		}
	}
}

// @assurance:evidence db-status-known
func Test知らない状態の値は直接入れても書き換えても拒まれる(t *testing.T) {
	_, raw, _ := open(t)
	for _, bad := range []string{"bogus", "TODO", "", " todo"} {
		if _, err := raw.Exec("INSERT INTO tasks VALUES ('x', 't', '', ?, 1)", bad); err == nil {
			t.Fatalf("tasks.status に %q が入った", bad)
		}
		if _, err := raw.Exec("INSERT INTO events (task_id, actor, from_status, to_status, reason, at) VALUES ('x', 'a', ?, 'todo', '', 1)", bad); err == nil {
			t.Fatalf("events.from_status に %q が入った", bad)
		}
		if _, err := raw.Exec("INSERT INTO events (task_id, actor, from_status, to_status, reason, at) VALUES ('x', 'a', NULL, ?, '', 1)", bad); err == nil {
			t.Fatalf("events.to_status に %q が入った", bad)
		}
	}
	mustExec(t, raw, "INSERT INTO tasks VALUES ('y', 't', '', 'todo', 1)")
	if _, err := raw.Exec("UPDATE tasks SET status = 'bogus' WHERE id = 'y'"); err == nil {
		t.Fatal("todo のタスクの状態を知らない値に書き換えられた")
	}
	// 変更前の「なし」と、知っている値は入る
	for _, st := range task.Statuses {
		mustExec(t, raw, "INSERT INTO events (task_id, actor, from_status, to_status, reason, at) VALUES ('y', 'a', ?, ?, '', 1)", string(st), string(st))
	}
	mustExec(t, raw, "INSERT INTO events (task_id, actor, from_status, to_status, reason, at) VALUES ('y', 'a', NULL, 'todo', '', 1)")
}

// @assurance:evidence db-id-exact-match
func TestIDで探すと文字どおりに一致するタスクだけが見つかる(t *testing.T) {
	s, raw, _ := open(t)
	for _, id := range []string{"0000000a", "abcdef01", "a", " b"} {
		mustExec(t, raw, "INSERT INTO tasks VALUES (?, 't', '', 'todo', 1)", id)
	}
	cases := map[string]bool{
		"0000000a": true, "abcdef01": true, "a": true, " b": true,
		"0000000A": false, "ABCDEF01": false, "": false, "0000000a ": false, " 0000000a": false, "0000000": false, "b": false, "A": false, "abcdef0": false,
	}
	err := s.Write(func(tx *store.Tx) error {
		for id, want := range cases {
			got, err := tx.FindTask(id)
			if err != nil {
				return err
			}
			if (got != nil) != want || (got != nil && got.ID != id) {
				t.Errorf("ID %q で探すと %+v（見つかるはず: %v）", id, got, want)
			}
			taken, err := tx.IDTaken(id)
			if err != nil {
				return err
			}
			if taken != want {
				t.Errorf("ID %q が登録済みか: %v（%v のはず）", id, taken, want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// @assurance:evidence db-events-in-record-order
func Testイベントは記録の順にそのタスクのものだけが漏れなく読み出される(t *testing.T) {
	s, raw, _ := open(t)
	rnd := rand.New(rand.NewPCG(1, 1))
	want := map[string][]task.Event{}
	ids := []string{"00000001", "00000002", "00000003"}
	err := s.Write(func(tx *store.Tx) error {
		for _, id := range ids {
			if err := tx.ApplyAdd(addWrite(id, "t", "", "a", 0)); err != nil {
				return err
			}
			want[id] = append(want[id], addWrite(id, "t", "", "a", 0).Event)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 日時が同じイベントを、タスクを混ぜて記録する（日時では並べず、記録の順で並ぶことを確かめる）
	for i := range 50 {
		id := ids[rnd.IntN(len(ids))]
		e := task.Event{TaskID: id, Actor: fmt.Sprintf("a%d", i), To: task.Todo, Reason: fmt.Sprint(i), At: int64(rnd.IntN(2))}
		mustExec(t, raw, "INSERT INTO events (task_id, actor, from_status, to_status, reason, at) VALUES (?, ?, NULL, 'todo', ?, ?)", e.TaskID, e.Actor, e.Reason, e.At)
		want[id] = append(want[id], e)
	}
	err = s.Write(func(tx *store.Tx) error {
		for _, id := range append(ids, "ffffffff") {
			got, err := tx.EventsOf(id)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(got, want[id]) && !(len(got) == 0 && len(want[id]) == 0) {
				t.Errorf("%s のイベント: %+v（%+v のはず）", id, got, want[id])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// @assurance:evidence db-decide-in-transaction
func Test判定に使う事実を読む前から書き込みのトランザクションを持っている(t *testing.T) {
	s, raw, _ := open(t)
	err := s.Write(func(tx *store.Tx) error {
		// まだ何も書いていない（読む前）時点で、ほかの接続は書き込みを始められない
		_, err := raw.Exec("INSERT INTO tasks VALUES ('other', 't', '', 'todo', 1)")
		if err == nil {
			t.Error("書き込みのトランザクションの中で、ほかの接続が書き込めた")
		}
		_, err = tx.IDTaken("other")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// @assurance:evidence db-busy
func Testほかの接続が書き込み中なら待ち待ちきれなければErrBusyで何も変えない(t *testing.T) {
	s, raw, _ := open(t)
	conn, err := raw.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = s.Write(func(tx *store.Tx) error { return tx.ApplyAdd(addWrite("00000001", "t", "", "a", 0)) })
	if !errors.Is(err, store.ErrBusy) {
		t.Fatalf("待ちきれなかったのに %v", err)
	}
	if d := time.Since(start); d < store.BusyTimeout-500*time.Millisecond || d < time.Second {
		t.Fatalf("%v しか待たなかった（%v 待つはず）", d, store.BusyTimeout)
	}
	if _, err := conn.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if n := len(state(t, raw).Tasks); n != 0 {
		t.Fatalf("待ちきれなかったのにタスクが %d 個ある", n)
	}
	// ほかの接続の書き込みが終われば、書き込める
	if err := s.Write(func(tx *store.Tx) error { return tx.ApplyAdd(addWrite("00000001", "t", "", "a", 0)) }); err != nil {
		t.Fatal(err)
	}
}

// @assurance:evidence db-constraint-violation
// @assurance:evidence db-change-atomic
func Test書き込みを制約が拒むとエラーになりタスクもイベントも変わらない(t *testing.T) {
	s, raw, _ := open(t)
	if err := s.Write(func(tx *store.Tx) error { return tx.ApplyAdd(addWrite("00000001", "t", "", "a", 0)) }); err != nil {
		t.Fatal(err)
	}
	mustExec(t, raw, "INSERT INTO tasks VALUES ('done0001', 't', '', 'done', 0)")
	before := state(t, raw)
	writes := map[string]func(*store.Tx) error{
		"すでにある ID で登録する": func(tx *store.Tx) error { return tx.ApplyAdd(addWrite("00000001", "u", "", "a", 1)) },
		"知らない状態で登録する": func(tx *store.Tx) error {
			w := addWrite("00000002", "u", "", "a", 1)
			w.Task.Status = "bogus"
			return tx.ApplyAdd(w)
		},
		"イベントの状態が知らない値（タスクは入った後）": func(tx *store.Tx) error {
			w := addWrite("00000003", "u", "", "a", 1)
			w.Event.To = "bogus"
			return tx.ApplyAdd(w)
		},
		"終わったタスクの状態を変える": func(tx *store.Tx) error {
			return tx.ApplyFinish(task.FinishWrite{ID: "done0001", Status: task.Canceled, Event: task.Event{TaskID: "done0001", Actor: "a", From: ptr(task.Done), To: task.Canceled, Reason: "r"}})
		},
		"状態を変えた後のイベントが知らない値": func(tx *store.Tx) error {
			return tx.ApplyFinish(task.FinishWrite{ID: "00000001", Status: task.Done, Event: task.Event{TaskID: "00000001", Actor: "a", From: ptr(task.Todo), To: "bogus", Reason: "r"}})
		},
		"ない ID の状態を変える": func(tx *store.Tx) error {
			return tx.ApplyFinish(task.FinishWrite{ID: "ffffffff", Status: task.Done, Event: task.Event{TaskID: "ffffffff", Actor: "a", From: ptr(task.Todo), To: task.Done, Reason: "r"}})
		},
	}
	for name, w := range writes {
		if err := s.Write(w); err == nil {
			t.Errorf("%s: エラーにならなかった", name)
		}
		if after := state(t, raw); !reflect.DeepEqual(after, before) {
			t.Errorf("%s: 中身が変わった\n前: %+v\n後: %+v", name, before, after)
		}
	}
}

// @assurance:evidence db-change-atomic
func Test書き込みの途中でエラーになるとその操作の書き込みはすべて取り消される(t *testing.T) {
	s, raw, _ := open(t)
	err := s.Write(func(tx *store.Tx) error {
		if err := tx.ApplyAdd(addWrite("00000001", "t", "", "a", 0)); err != nil {
			return err
		}
		return errors.New("途中の誤り")
	})
	if err == nil {
		t.Fatal("エラーにならなかった")
	}
	if st := state(t, raw); len(st.Tasks) != 0 || len(st.Events) != 0 {
		t.Fatalf("取り消されなかった: %+v", st)
	}
}

// @assurance:evidence internal-error
func Testデータのファイルを開けないときと知らない移行の番号のデータのファイルはエラーで何も変えない(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "broken", "quenta.db")
	if err := os.MkdirAll(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(broken); err == nil || errors.Is(err, store.ErrUnsupportedVersion) || errors.Is(err, store.ErrBusy) {
		t.Fatalf("ディレクトリの場所を開くと %v", err)
	}
	newer := filepath.Join(dir, "newer.db")
	raw, err := sql.Open("sqlite", "file:"+newer)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, raw, "PRAGMA user_version = 9999")
	if _, err := store.Open(newer); !errors.Is(err, store.ErrUnsupportedVersion) {
		t.Fatalf("知らない移行の番号のデータのファイルを開くと %v", err)
	}
	var v int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 9999 {
		t.Fatalf("移行の番号が %d に変わった（%v）", v, err)
	}
	var n int
	if err := raw.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&n); err != nil || n != 0 {
		t.Fatalf("表が %d 個できた（%v）", n, err)
	}
	raw.Close()
}

// @assurance:evidence data-file-wal
func TestデータのファイルはWALモードで使う(t *testing.T) {
	_, raw, _ := open(t)
	var mode string
	if err := raw.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode が %q", mode)
	}
}

func TestDBの構造は移行を全部当てた構造とschema_sqlから作った構造が一致する(t *testing.T) {
	_, raw, _ := open(t)
	fromSchema, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fromSchema.Close()
	mustExec(t, fromSchema, db.Schema)
	read := func(d *sql.DB) []string {
		rows, err := d.Query("SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var r []string
		for rows.Next() {
			var a, b, c string
			var s sql.NullString
			if err := rows.Scan(&a, &b, &c, &s); err != nil {
				t.Fatal(err)
			}
			r = append(r, a+"|"+b+"|"+c+"|"+normalize(s.String))
		}
		return r
	}
	if got, want := read(raw), read(fromSchema); !slices.Equal(got, want) {
		t.Fatalf("移行で作った構造と schema.sql が違う\n移行: %v\nschema.sql: %v", got, want)
	}
}

// normalize は、SQL の文から、注釈と空白の違いを除く
func normalize(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if i := strings.Index(l, "--"); i >= 0 {
			l = l[:i]
		}
		lines = append(lines, strings.Join(strings.Fields(l), " "))
	}
	return strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
}

// randomState は、無作為なタスクとイベントを DB に直接入れ、同じ中身を返す
func randomState(t *testing.T, rnd *rand.Rand, raw *sql.DB) oracle.State {
	t.Helper()
	ids := []string{"00000001", "00000002", "0000000a", "ffffffff", "abcdef01"}
	for _, i := range rnd.Perm(len(ids))[:rnd.IntN(len(ids)+1)] {
		mustExec(t, raw, "INSERT INTO tasks VALUES (?, 't', 'b', ?, ?)", ids[i], string(task.Statuses[rnd.IntN(3)]), rnd.Int64N(3))
		mustExec(t, raw, "INSERT INTO events (task_id, actor, from_status, to_status, reason, at) VALUES (?, 'a', NULL, 'todo', '', 0)", ids[i])
	}
	return state(t, raw)
}

func modelTasks(t *testing.T, s _dafny.Sequence) []task.Task {
	t.Helper()
	ts, err := oracle.GoTasks(s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// @assurance:evidence db-facts-as-read
func Test問い合わせの結果は同じ中身のモデルの問い合わせと一致する(t *testing.T) {
	for seed := range 200 {
		s, raw, _ := open(t)
		rnd := rand.New(rand.NewPCG(uint64(seed), 7))
		st := randomState(t, rnd, raw)
		ms, err := st.Model()
		if err != nil {
			t.Fatal(err)
		}
		err = s.Write(func(tx *store.Tx) error {
			for _, id := range []string{"00000001", "0000000a", "0000000A", "ffffffff", "", "zzzzzzzz"} {
				taken, err := tx.IDTaken(id)
				if err != nil {
					return err
				}
				if taken != oracle.M.QueryIdTaken(ms, oracle.Str(id)) {
					t.Fatalf("ID %q が登録済みか: 実装 %v、モデル %v", id, taken, !taken)
				}
				found, err := tx.FindTask(id)
				if err != nil {
					return err
				}
				mq := oracle.M.QueryTask(ms, oracle.Str(id))
				if mq.Is_None() != (found == nil) {
					t.Fatalf("ID %q のタスク: 実装 %+v、モデル %v", id, found, mq)
				}
				if found != nil {
					mt, ok := mq.Dtor_value().(Q.Task)
					if !ok {
						t.Fatalf("モデルの問い合わせの結果がタスクでない: %T", mq.Dtor_value())
					}
					want, err := oracle.GoTask(mt)
					if err != nil {
						return err
					}
					if *found != want {
						t.Fatalf("ID %q のタスク: 実装 %+v、モデル %+v", id, *found, want)
					}
				}
			}
			all, err := tx.Tasks()
			if err != nil {
				return err
			}
			want := modelTasks(t, oracle.M.QueryTasks(ms))
			byID := func(a, b task.Task) int { return strings.Compare(a.ID, b.ID) }
			slices.SortFunc(all, byID)
			slices.SortFunc(want, byID)
			if all == nil {
				all = []task.Task{}
			}
			if !reflect.DeepEqual(all, want) {
				t.Fatalf("タスクの全体（集まりとして比べる）: 実装 %+v、モデル %+v", all, want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// @assurance:evidence db-writes-as-model
func Test判定が返した書き込みは同じ中身のモデルの書き込みと同じ変化を起こす(t *testing.T) {
	for seed := range 200 {
		s, raw, _ := open(t)
		rnd := rand.New(rand.NewPCG(uint64(seed), 9))
		st := randomState(t, rnd, raw)
		ms, err := st.Model()
		if err != nil {
			t.Fatal(err)
		}
		var want Q.State
		if rnd.IntN(2) == 0 {
			id := []string{"00000009", "0000000b", "00000001"}[rnd.IntN(3)]
			taken := oracle.M.QueryIdTaken(ms, oracle.Str(id))
			w, err := task.AddDecide("a", "題", "本文", id, 5, taken)
			if err != nil {
				continue // 判定がエラーを返した書き込みは、主張の外
			}
			mw := oracle.M.AddDecide(oracle.Str("a"), oracle.Str("題"), oracle.Str("本文"), oracle.Str(id), oracle.Int(5), taken).Dtor_value().(Q.AddWrite)
			want = oracle.M.ApplyAdd(ms, mw)
			if err := s.Write(func(tx *store.Tx) error { return tx.ApplyAdd(w) }); err != nil {
				t.Fatal(err)
			}
		} else {
			id := []string{"00000001", "00000002", "0000000a", "ffffffff", "abcdef01"}[rnd.IntN(5)]
			var found *task.Task
			if err := s.Write(func(tx *store.Tx) error { var e error; found, e = tx.FindTask(id); return e }); err != nil {
				t.Fatal(err)
			}
			k := task.FinishKinds[rnd.IntN(2)]
			w, err := task.FinishDecide("a", id, "理由", k, 5, found)
			if err != nil {
				continue
			}
			mk, _ := oracle.Kind(k)
			mw := oracle.M.FinishDecide(oracle.Str("a"), oracle.Str(id), oracle.Str("理由"), mk, oracle.Int(5), oracle.M.QueryTask(ms, oracle.Str(id))).Dtor_value().(Q.FinishWrite)
			want = oracle.M.ApplyFinish(ms, mw)
			if err := s.Write(func(tx *store.Tx) error { return tx.ApplyFinish(w) }); err != nil {
				t.Fatal(err)
			}
		}
		wantGo, err := oracle.GoState(want)
		if err != nil {
			t.Fatal(err)
		}
		if got := state(t, raw); !reflect.DeepEqual(got, wantGo) {
			t.Fatalf("書き込んだ後の中身: 実装 %+v、モデル %+v", got, wantGo)
		}
	}
}

// @assurance:evidence internal-error
func Test移行を当て済みのデータのファイルは開き直しても移行を当て直さず中身も変わらない(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quenta.db")
	for i := range 3 {
		s, err := store.Open(path)
		if err != nil {
			t.Fatalf("%d 回目に開くと %v", i+1, err)
		}
		if err := s.Write(func(tx *store.Tx) error { return tx.ApplyAdd(addWrite(fmt.Sprintf("%08x", i), "t", "", "a", 0)) }); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var v int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 1 {
		t.Fatalf("移行の番号が %d（%v）", v, err)
	}
	if n := len(state(t, raw).Tasks); n != 3 {
		t.Fatalf("タスクが %d 個", n)
	}
}

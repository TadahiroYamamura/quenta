package ops_test

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/TadahiroYamamura/quenta/internal/oracle"
	"github.com/TadahiroYamamura/quenta/internal/ops"
	"github.com/TadahiroYamamura/quenta/internal/store"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

func newOps(t *testing.T, random io.Reader) (*ops.Ops, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "quenta.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &ops.Ops{Store: s, Random: random, Now: func() int64 { return 100 }}, db
}

// @assurance:evidence task-id-format
func Test作るIDは16進の小文字8桁で毎回ちがう値になる(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}$`)
	seen := map[string]bool{}
	for range 2000 {
		id, err := task.NewID(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(id) {
			t.Fatalf("ID の形が違う: %q", id)
		}
		seen[id] = true
	}
	// 2000 個のうち重なるのは、ランダムなら 1 組あるかないか。大きく減るなら、ランダムでない
	if len(seen) < 1990 {
		t.Fatalf("2000 個の ID のうち、違う値が %d 個しかない", len(seen))
	}
	// 材料のバイトがそのまま 16 進の小文字になる
	if id, err := task.NewID(bytes.NewReader([]byte{0xab, 0xcd, 0x01, 0xef})); err != nil || id != "abcd01ef" {
		t.Fatalf("材料 abcd01ef から作った ID が %q（%v）", id, err)
	}
}

// @assurance:evidence task-add-retries-on-id-collision
func TestIDが重なったら作り直して登録する(t *testing.T) {
	// 1 つ目の登録は 00000001。2 つ目は 00000001 が重なり、作り直した 00000002 で登録する
	o, db := newOps(t, bytes.NewReader([]byte{0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 2}))
	if _, err := o.Add("s", "1 つ目", ""); err != nil {
		t.Fatal(err)
	}
	got, err := o.Add("s", "2 つ目", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "00000002" {
		t.Fatalf("作り直した ID が %q", got.ID)
	}
	st, err := oracle.ReadState(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Tasks) != 2 || len(st.Events) != 2 {
		t.Fatalf("タスク %d 個、イベント %d 件（2 つずつのはず）", len(st.Tasks), len(st.Events))
	}
}

// @assurance:evidence task-add-retries-on-id-collision
func TestIDが上限の回数まで重なり続けたらid_conflictで何も登録しない(t *testing.T) {
	b := bytes.Repeat([]byte{0, 0, 0, 1}, ops.MaxIDAttempts+1)
	o, db := newOps(t, bytes.NewReader(b))
	if _, err := o.Add("s", "1 つ目", ""); err != nil {
		t.Fatal(err)
	}
	_, err := o.Add("s", "2 つ目", "")
	if !errors.Is(err, ops.ErrIDConflict) {
		t.Fatalf("上限まで重なったのに %v", err)
	}
	st, err := oracle.ReadState(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Tasks) != 1 || len(st.Events) != 1 {
		t.Fatalf("タスク %d 個、イベント %d 件（1 つずつのはず）", len(st.Tasks), len(st.Events))
	}
}

// @assurance:evidence task-add-retries-on-id-collision
func Test上限の回数の手前で重ならなくなれば登録する(t *testing.T) {
	b := append(bytes.Repeat([]byte{0, 0, 0, 1}, ops.MaxIDAttempts), 0, 0, 0, 2)
	o, _ := newOps(t, bytes.NewReader(b))
	if _, err := o.Add("s", "1 つ目", ""); err != nil {
		t.Fatal(err)
	}
	got, err := o.Add("s", "2 つ目", "")
	if err != nil {
		t.Fatalf("上限の回数の最後で重ならなかったのに %v", err)
	}
	if got.ID != "00000002" {
		t.Fatalf("ID が %q", got.ID)
	}
}

// @assurance:evidence task-id-format
func TestIDの材料を読めなければ登録はエラーで何も登録しない(t *testing.T) {
	o, db := newOps(t, bytes.NewReader([]byte{0, 1}))
	if _, err := o.Add("s", "t", ""); err == nil {
		t.Fatal("材料が足りないのに登録できた")
	}
	st, err := oracle.ReadState(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Tasks) != 0 {
		t.Fatalf("タスクが %d 個", len(st.Tasks))
	}
}

func Test表示は登録したタスクとイベントを返し見つからなければNotFoundを返す(t *testing.T) {
	o, _ := newOps(t, bytes.NewReader([]byte{0, 0, 0, 1}))
	added, err := o.Add("s", "t", "b")
	if err != nil {
		t.Fatal(err)
	}
	got, es, err := o.Show(added.ID)
	if err != nil || got != added || len(es) != 1 || es[0].To != task.Todo {
		t.Fatalf("表示: %+v %+v %v", got, es, err)
	}
	if _, _, err := o.Show("ffffffff"); !errors.Is(err, task.NotFound) {
		t.Fatalf("ない ID の表示: %v", err)
	}
}

func Test実行時の確認はIDか状態がイベントと食い違う書き込みを捕まえる(t *testing.T) {
	ok := task.AddWrite{Task: task.Task{ID: "a", Status: task.Todo}, Event: task.Event{TaskID: "a", To: task.Todo}}
	if err := ops.CheckAddWriteForTest(ok); err != nil {
		t.Fatalf("食い違いのない登録を捕まえた: %v", err)
	}
	badID, badStatus := ok, ok
	badID.Event.TaskID = "b"
	badStatus.Event.To = task.Done
	for name, w := range map[string]task.AddWrite{"ID": badID, "状態": badStatus} {
		if err := ops.CheckAddWriteForTest(w); !errors.Is(err, ops.ErrCheckFailed) {
			t.Fatalf("登録の%sの食い違いを捕まえなかった: %v", name, err)
		}
	}
	fok := task.FinishWrite{ID: "a", Status: task.Done, Event: task.Event{TaskID: "a", To: task.Done}}
	if err := ops.CheckFinishWriteForTest(fok); err != nil {
		t.Fatalf("食い違いのない完了を捕まえた: %v", err)
	}
	fID, fStatus := fok, fok
	fID.Event.TaskID = "b"
	fStatus.Event.To = task.Canceled
	for name, w := range map[string]task.FinishWrite{"ID": fID, "状態": fStatus} {
		if err := ops.CheckFinishWriteForTest(w); !errors.Is(err, ops.ErrCheckFailed) {
			t.Fatalf("完了の%sの食い違いを捕まえなかった: %v", name, err)
		}
	}
}

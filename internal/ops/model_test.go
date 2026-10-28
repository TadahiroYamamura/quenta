package ops_test

import (
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/TadahiroYamamura/quenta/internal/oracle"
	"github.com/TadahiroYamamura/quenta/internal/ops"
	Q "github.com/TadahiroYamamura/quenta/internal/spec/Quenta"
	_dafny "github.com/TadahiroYamamura/quenta/internal/spec/dafny"
	"github.com/TadahiroYamamura/quenta/internal/store"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

// idSource は、ID の材料のバイトを、少ない候補から選んで渡す。ID がわざと重なるようにし、作り直す経路を通すため。渡した ID を記録する
type idSource struct {
	rnd   *rand.Rand
	drawn []string
}

var idPool = [][4]byte{{0, 0, 0, 1}, {0, 0, 0, 2}, {0xab, 0xcd, 0, 3}, {0xff, 0, 0, 4}, {0x12, 0x34, 0x56, 0x78}, {0, 0, 0, 5}}

func (s *idSource) Read(p []byte) (int, error) {
	if len(p) != 4 {
		return 0, fmt.Errorf("ID の材料は 4 バイトずつ読む（%d バイト読もうとした）", len(p))
	}
	b := idPool[s.rnd.IntN(len(idPool))]
	copy(p, b[:])
	s.drawn = append(s.drawn, fmt.Sprintf("%x", b))
	return 4, nil
}

// 主体・タイトル・本文・理由の候補。空、空白だけ（いろいろな空白の文字）、前後に空白、空白でない文字を含む
var texts = []string{"", " ", "\t\n", "\u3000", "\u00a0\u2028", "a", " a ", "セッション", "\u3000x\u3000", "\u200b", "<&>"}

type harness struct {
	t     *testing.T
	rnd   *rand.Rand
	ids   *idSource
	impl  *ops.Ops
	db    *sql.DB
	model Q.State
	now   int64
}

func newHarness(t *testing.T, seed uint64) *harness {
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
	rnd := rand.New(rand.NewPCG(seed, seed))
	h := &harness{t: t, rnd: rnd, ids: &idSource{rnd: rnd}, db: db, model: oracle.M.Empty(), now: 1_700_000_000}
	h.impl = &ops.Ops{Store: s, Random: h.ids, Now: func() int64 { return h.now }}
	return h
}

func (h *harness) text() string { return texts[h.rnd.IntN(len(texts))] }

// knownID は、登録済みの ID か、どれとも一致しない ID を選ぶ
func (h *harness) knownID() string {
	ts, err := oracle.GoTasks(h.model.Dtor_tasks())
	if err != nil {
		h.t.Fatal(err)
	}
	if len(ts) == 0 || h.rnd.IntN(5) == 0 {
		return []string{"ffffffff", "", "0000000A", "00000001 "}[h.rnd.IntN(4)]
	}
	return ts[h.rnd.IntN(len(ts))].ID
}

// failureOf は、実装の操作のエラーを、判定のエラーの種類に写す（判定のエラーでなければ nil）
func failureOf(err error) (task.Failure, bool) {
	var f task.Failure
	return f, errors.As(err, &f)
}

// stepAdd は、登録をモデルと実装の両方で行い、結果を比べる。実装は ID が重なると作り直すので、モデルにも、実装が引いた ID を順に渡す
func (h *harness) stepAdd() {
	actor, title, body := h.text(), h.text(), h.text()
	h.ids.drawn = nil
	got, err := h.impl.Add(actor, title, body)
	if len(h.ids.drawn) == 0 {
		h.t.Fatalf("実装が ID を 1 つも引かなかった")
	}
	var want Q.Result
	for _, id := range h.ids.drawn {
		want = oracle.M.Add(h.model, oracle.Str(actor), oracle.Str(title), oracle.Str(body), oracle.Str(id), oracle.Int(h.now))
		if !(want.Is_Err() && want.Dtor_error().Is_IdTaken()) {
			break
		}
	}
	if want.Is_Err() {
		wf, ferr := oracle.Failure(want.Dtor_error())
		if ferr != nil {
			h.t.Fatal(ferr)
		}
		if wf == task.IDTaken {
			if !errors.Is(err, ops.ErrIDConflict) {
				h.t.Fatalf("登録（%q, %q）: モデルは ID が重なり続けたが、実装は %v", actor, title, err)
			}
			return
		}
		if f, ok := failureOf(err); !ok || f != wf {
			h.t.Fatalf("登録（%q, %q）: モデルは %v、実装は %v", actor, title, wf, err)
		}
		return
	}
	if err != nil {
		h.t.Fatalf("登録（%q, %q）: モデルは成功、実装は %v", actor, title, err)
	}
	h.adopt(want)
	ts, _ := oracle.GoTasks(h.model.Dtor_tasks())
	if added := ts[len(ts)-1]; got != added {
		h.t.Fatalf("登録の結果: モデルは %+v、実装は %+v", added, got)
	}
}

func (h *harness) stepFinish(k task.FinishKind) {
	actor, reason, id := h.text(), h.text(), h.knownID()
	got, err := h.impl.Finish(actor, id, reason, k)
	var want Q.Result
	if k == task.Completing {
		want = oracle.M.Complete(h.model, oracle.Str(actor), oracle.Str(id), oracle.Str(reason), oracle.Int(h.now))
	} else {
		want = oracle.M.Cancel(h.model, oracle.Str(actor), oracle.Str(id), oracle.Str(reason), oracle.Int(h.now))
	}
	if want.Is_Err() {
		wf, ferr := oracle.Failure(want.Dtor_error())
		if ferr != nil {
			h.t.Fatal(ferr)
		}
		if f, ok := failureOf(err); !ok || f != wf {
			h.t.Fatalf("完了・中止（%q, %q, %q）: モデルは %v、実装は %v", actor, id, reason, wf, err)
		}
		return
	}
	if err != nil {
		h.t.Fatalf("完了・中止（%q, %q, %q）: モデルは成功、実装は %v", actor, id, reason, err)
	}
	h.adopt(want)
	ts, _ := oracle.GoTasks(h.model.Dtor_tasks())
	for _, t := range ts {
		if t.ID == id && t != got {
			h.t.Fatalf("完了・中止の結果: モデルは %+v、実装は %+v", t, got)
		}
	}
}

func (h *harness) stepList() {
	actor, all := h.text(), h.rnd.IntN(2) == 0
	got, err := h.impl.List(actor, all)
	want := oracle.M.List(h.model, oracle.Str(actor), all)
	if want.Is_Err() {
		wf, ferr := oracle.Failure(want.Dtor_error())
		if ferr != nil {
			h.t.Fatal(ferr)
		}
		if f, ok := failureOf(err); !ok || f != wf {
			h.t.Fatalf("一覧（%q, %v）: モデルは %v、実装は %v", actor, all, wf, err)
		}
		return
	}
	if err != nil {
		h.t.Fatalf("一覧（%q, %v）: モデルは成功、実装は %v", actor, all, err)
	}
	seq, ok := want.Dtor_value().(_dafny.Sequence)
	if !ok {
		h.t.Fatalf("モデルの一覧の結果が列でない: %T", want.Dtor_value())
	}
	wantTasks, err := oracle.GoTasks(seq)
	if err != nil {
		h.t.Fatal(err)
	}
	if got == nil {
		got = []task.Task{}
	}
	if !reflect.DeepEqual(got, wantTasks) {
		h.t.Fatalf("一覧（%q, %v）: モデルは %+v、実装は %+v", actor, all, wantTasks, got)
	}
}

// adopt は、モデルの成功の結果を、次の状態にする
func (h *harness) adopt(r Q.Result) {
	s, ok := r.Dtor_value().(Q.State)
	if !ok {
		h.t.Fatalf("モデルの結果が状態でない: %T", r.Dtor_value())
	}
	h.model = s
}

// compareState は、実装の DB の中身をモデルの状態に写し、モデルの状態と比べる
func (h *harness) compareState(step int) {
	got, err := oracle.ReadState(h.db)
	if err != nil {
		h.t.Fatal(err)
	}
	want, err := oracle.GoState(h.model)
	if err != nil {
		h.t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		h.t.Fatalf("%d 歩目の後の状態が違う\n実装: %+v\nモデル: %+v", step, got, want)
	}
}

// run は、無作為な操作の列（登録・一覧・完了・中止）を、モデルと実装の両方に送り、1 歩ごとに結果と状態の全体を比べる
func run(t *testing.T, seeds, steps int) {
	for seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			h := newHarness(t, uint64(seed))
			for step := range steps {
				// 日時は、同じ秒のまま進まないこともあるように進める（作成日時が同じタスクを作るため）
				h.now += int64(h.rnd.IntN(2))
				switch h.rnd.IntN(4) {
				case 0:
					h.stepAdd()
				case 1:
					h.stepFinish(task.Completing)
				case 2:
					h.stepFinish(task.Canceling)
				case 3:
					h.stepList()
				}
				h.compareState(step)
			}
		})
	}
}

// @assurance:evidence task-add-saves-todo
// @assurance:evidence task-add-records-event
// @assurance:evidence task-id-unique
// @assurance:evidence task-list-unfinished
// @assurance:evidence task-list-all
// @assurance:evidence task-list-order
// @assurance:evidence task-done-sets-done
// @assurance:evidence task-cancel-sets-canceled
// @assurance:evidence finished-task-unchangeable
// @assurance:evidence task-not-found
// @assurance:evidence event-actor-as-given
// @assurance:evidence model-changes-only-target
// @assurance:evidence finished-task-preserved
// @assurance:evidence status-matches-last-event
// @assurance:evidence db-writes-as-model
func Test無作為な操作の列で実装の結果と状態がモデルと一致する(t *testing.T) {
	run(t, 40, 60)
}

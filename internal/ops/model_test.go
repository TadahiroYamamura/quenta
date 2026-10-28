package ops_test

import (
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/quenta/internal/oracle"
	"github.com/TadahiroYamamura/quenta/internal/ops"
	Q "github.com/TadahiroYamamura/quenta/internal/spec/Quenta"
	_dafny "github.com/TadahiroYamamura/quenta/internal/spec/dafny"
	"github.com/TadahiroYamamura/quenta/internal/store"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

// idSource は、ID の材料のバイトを渡す。4 回に 1 回は少ない候補から選び、ID をわざと重ねて作り直す経路を通す。
// ほかは毎回違う値にして、タスクがたまっても登録できるようにする。渡した ID を記録する
type idSource struct {
	rnd   *rand.Rand
	drawn []string
}

var idPool = [][4]byte{{0, 0, 0, 1}, {0, 0, 0, 2}, {0xab, 0xcd, 0, 3}, {0xff, 0, 0, 4}, {0x12, 0x34, 0x56, 0x78}, {0, 0, 0, 5}}

func (s *idSource) Read(p []byte) (int, error) {
	if len(p) != 4 {
		return 0, fmt.Errorf("ID の材料は 4 バイトずつ読む（%d バイト読もうとした）", len(p))
	}
	var b [4]byte
	if s.rnd.IntN(4) == 0 {
		b = idPool[s.rnd.IntN(len(idPool))]
	} else {
		v := s.rnd.Uint32()
		b = [4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	}
	copy(p, b[:])
	s.drawn = append(s.drawn, fmt.Sprintf("%x", b))
	return 4, nil
}

// 主体・タイトル・本文・理由の候補。空白だけのもの（いろいろな空白の文字）と、空白だけでないもの（前後に空白、空白に見えるが空白でない文字を含む）
var (
	blankTexts    = []string{"", " ", "\t\n", "\u3000", "\u00a0\u2028"}
	nonBlankTexts = []string{"a", " a ", "セッション", "\u3000x\u3000", "\u200b", "<&>"}
)

type harness struct {
	t       *testing.T
	outcome map[string]int // 操作と結果の組（例: "登録 成功"）ごとに、起きた回数
	rnd   *rand.Rand
	ids   *idSource
	impl  *ops.Ops
	db    *sql.DB
	model Q.State
	now   int64
}

func newHarness(t *testing.T, base, seed uint64) *harness {
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
	rnd := rand.New(rand.NewPCG(base, seed))
	h := &harness{t: t, rnd: rnd, ids: &idSource{rnd: rnd}, db: db, now: 1_700_000_000, outcome: map[string]int{}}
	h.impl = &ops.Ops{Store: s, Random: h.ids, Now: func() int64 { return h.now }}
	h.initialState()
	return h
}

// initialState は、始めの状態を、操作を通さずに DB へ直接作り、モデルにも同じ状態を渡す。
// 完了・中止の事前条件に関わる状態（タスクがある、終わっている）を、登録の操作に頼らずに作るため。
// 作る状態は、仕様の不変条件（ID が重ならない、状態が最後のイベントの変更後と一致する）を満たすものだけ
func (h *harness) initialState() {
	st := oracle.State{Tasks: []task.Task{}, Events: []task.Event{}}
	for _, i := range h.rnd.Perm(len(idPool))[:h.rnd.IntN(len(idPool)+1)] {
		id := fmt.Sprintf("%x", idPool[i])
		status := task.Statuses[h.rnd.IntN(len(task.Statuses))]
		at := h.now - int64(h.rnd.IntN(3))
		st.Tasks = append(st.Tasks, task.Task{ID: id, Title: h.text() + "t", Body: h.text(), Status: status, CreatedAt: at})
		st.Events = append(st.Events, task.Event{TaskID: id, Actor: "a", To: task.Todo, At: at})
		if status != task.Todo {
			from := task.Todo
			st.Events = append(st.Events, task.Event{TaskID: id, Actor: "a", From: &from, To: status, Reason: "r", At: at})
		}
	}
	for _, t := range st.Tasks {
		if _, err := h.db.Exec("INSERT INTO tasks VALUES (?, ?, ?, ?, ?)", t.ID, t.Title, t.Body, string(t.Status), t.CreatedAt); err != nil {
			h.t.Fatal(err)
		}
	}
	for _, e := range st.Events {
		var from any
		if e.From != nil {
			from = string(*e.From)
		}
		if _, err := h.db.Exec("INSERT INTO events (task_id, actor, from_status, to_status, reason, at) VALUES (?, ?, ?, ?, ?, ?)", e.TaskID, e.Actor, from, string(e.To), e.Reason, e.At); err != nil {
			h.t.Fatal(err)
		}
	}
	m, err := st.Model()
	if err != nil {
		h.t.Fatal(err)
	}
	if !oracle.M.Inv(m) {
		h.t.Fatalf("始めの状態が仕様の不変条件を満たさない: %+v", st)
	}
	h.model = m
}

// text は、1 割の見込みで空白だけの候補を、ほかは空白だけでない候補を選ぶ。空白だけの入力の判定は、プロパティベーステストで境界の組み合わせをすべて確かめている。
// ここでは、操作を成功させて状態を進めた列で、実装とモデルが一致するかを見るため、空白だけの候補を少なくする
func (h *harness) text() string {
	if h.rnd.IntN(10) == 0 {
		return blankTexts[h.rnd.IntN(len(blankTexts))]
	}
	return nonBlankTexts[h.rnd.IntN(len(nonBlankTexts))]
}

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
	h.record("登録", err)
	if len(h.ids.drawn) > 1 {
		h.outcome["登録 ID の作り直し"]++
	}
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
	ts, err := oracle.GoTasks(h.model.Dtor_tasks())
	if err != nil {
		h.t.Fatal(err)
	}
	if added := ts[len(ts)-1]; got != added {
		h.t.Fatalf("登録の結果: モデルは %+v、実装は %+v", added, got)
	}
}

func (h *harness) stepFinish(k task.FinishKind) {
	actor, reason, id := h.text(), h.text(), h.knownID()
	got, err := h.impl.Finish(actor, id, reason, k)
	h.record(map[task.FinishKind]string{task.Completing: "完了", task.Canceling: "中止"}[k], err)
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
	ts, err := oracle.GoTasks(h.model.Dtor_tasks())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, t := range ts {
		if t.ID == id && t != got {
			h.t.Fatalf("完了・中止の結果: モデルは %+v、実装は %+v", t, got)
		}
	}
}

func (h *harness) stepList() {
	actor, all := h.text(), h.rnd.IntN(2) == 0
	got, err := h.impl.List(actor, all)
	h.record("一覧", err)
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

var failureNames = map[task.Failure]string{
	task.SessionRequired: "session_required", task.TitleRequired: "title_required", task.ReasonRequired: "reason_required",
	task.NotFound: "not_found", task.AlreadyFinished: "already_finished", task.IDTaken: "id_taken",
}

// record は、操作の結果を数える
func (h *harness) record(op string, err error) {
	switch f, ok := failureOf(err); {
	case err == nil:
		h.outcome[op+" 成功"]++
	case ok:
		h.outcome[op+" "+failureNames[f]]++
	case errors.Is(err, ops.ErrIDConflict):
		h.outcome[op+" id_conflict"]++
	default:
		h.outcome[op+" そのほか"]++
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
func run(t *testing.T, seeds, steps int) map[string]int {
	base := oracle.Seed(t)
	total := map[string]int{}
	for seed := range seeds {
		t.Run(fmt.Sprintf("系列=%d", seed), func(t *testing.T) {
			h := newHarness(t, base, uint64(seed))
			defer func() {
				for k, v := range h.outcome {
					total[k] += v
				}
			}()
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
	return total
}

// minOutcomes は、操作の列の全体で、少なくともこれだけ起きることを求める結果の回数。
// 入力の候補が変わって、成功やエラーの経路を通らなくなったまま、テストが通り続けないようにするため。
// 回数は、2026-10-28 に計った割合（40 系列 × 60 歩で、登録の成功 約 480、完了・中止の成功 約 140 ずつ、各エラー 40〜260）の、およそ 3 分の 1 から 5 分の 1
var minOutcomes = map[string]int{
	"登録 成功": 150, "登録 session_required": 15, "登録 title_required": 15, "登録 ID の作り直し": 10,
	"完了 成功": 40, "完了 session_required": 10, "完了 reason_required": 15, "完了 not_found": 20, "完了 already_finished": 50,
	"中止 成功": 40, "中止 session_required": 10, "中止 reason_required": 15, "中止 not_found": 20, "中止 already_finished": 50,
	"一覧 成功": 150, "一覧 session_required": 15,
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
	got := run(t, 40, 60)
	t.Logf("結果の回数: %v", got)
	for k, min := range minOutcomes {
		if got[k] < min {
			t.Errorf("%s が %d 回しか起きなかった（%d 回以上起きるはず。入力の候補が偏っていないか見る）", k, got[k], min)
		}
	}
	for k, v := range got {
		if strings.HasSuffix(k, " そのほか") {
			t.Errorf("%s: 判定のエラーでない誤りが %d 回起きた", k, v)
		}
	}
}

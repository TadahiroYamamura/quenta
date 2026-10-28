package task_test

import (
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"

	"github.com/TadahiroYamamura/quenta/internal/oracle"
	Q "github.com/TadahiroYamamura/quenta/internal/spec/Quenta"
	_dafny "github.com/TadahiroYamamura/quenta/internal/spec/dafny"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

// whiteSpaces は、仕様（spec/quenta.dfy の IsWhiteSpace）が空白とする文字のすべて。実装の判定から作らない（実装が誤れば、入力も一緒にずれるため）
var whiteSpaces = func() []rune {
	var rs []rune
	for r := rune(0); r <= 0x3000; r++ {
		if oracle.M.IsWhiteSpace(_dafny.CodePoint(r)) {
			rs = append(rs, r)
		}
	}
	return rs
}()

// boundaryTexts は、空白だけかの判定の境界になる文字列。どのテストでも必ず含める
var boundaryTexts = func() []string {
	xs := []string{"", "x", " x ", "\u3000x\u3000", "\u200b", "\ufeff", strings.Repeat("長", 2000), "多言語 مرحبا 😀"}
	for _, r := range whiteSpaces {
		xs = append(xs, string(r), string(r)+"x", "x"+string(r))
	}
	return append(xs, string(whiteSpaces))
}()

// randomText は、空白の文字と、そうでない文字を混ぜた、無作為な文字列を作る
func randomText(rnd *rand.Rand) string {
	n := rnd.IntN(6)
	var b strings.Builder
	for range n {
		switch rnd.IntN(3) {
		case 0:
			b.WriteRune(whiteSpaces[rnd.IntN(len(whiteSpaces))])
		case 1:
			b.WriteRune(rune('a' + rnd.IntN(26)))
		default:
			b.WriteRune(rune(0x80 + rnd.IntN(0x3000)))
		}
	}
	return b.String()
}

// texts は、境界の文字列のすべてと、無作為な文字列を n 個返す
func texts(rnd *rand.Rand, n int) []string {
	xs := append([]string{}, boundaryTexts...)
	for range n {
		xs = append(xs, randomText(rnd))
	}
	return xs
}

func pick(rnd *rand.Rand, xs []string) string { return xs[rnd.IntN(len(xs))] }

func modelFailure(t *testing.T, r Q.Result) (task.Failure, bool) {
	t.Helper()
	if r.Is_Ok() {
		return 0, false
	}
	f, err := oracle.Failure(r.Dtor_error())
	if err != nil {
		t.Fatal(err)
	}
	return f, true
}

func implFailure(t *testing.T, err error) (task.Failure, bool) {
	t.Helper()
	if err == nil {
		return 0, false
	}
	f, ok := err.(task.Failure)
	if !ok {
		t.Fatalf("判定が、判定のエラーでないエラーを返した: %v", err)
	}
	return f, true
}

// checkAdd は、登録の判定を実装とモデルで比べる
func checkAdd(t *testing.T, actor, title, body, id string, now int64, taken bool) (task.AddWrite, bool) {
	t.Helper()
	got, err := task.AddDecide(actor, title, body, id, now, taken)
	want := oracle.M.AddDecide(oracle.Str(actor), oracle.Str(title), oracle.Str(body), oracle.Str(id), oracle.Int(now), taken)
	gf, gerr := implFailure(t, err)
	wf, werr := modelFailure(t, want)
	if gerr != werr || gf != wf {
		t.Fatalf("登録の判定（%q, %q, %q, %v）: モデルは %v（エラー %v）、実装は %v（エラー %v）", actor, title, body, taken, wf, werr, gf, gerr)
	}
	if werr {
		return got, false
	}
	w, ok := want.Dtor_value().(Q.AddWrite)
	if !ok {
		t.Fatalf("モデルの書き込む内容が AddWrite でない: %T", want.Dtor_value())
	}
	wt, err := oracle.GoTask(w.Dtor_task())
	if err != nil {
		t.Fatal(err)
	}
	we, err := oracle.GoEvent(w.Dtor_event())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, task.AddWrite{Task: wt, Event: we}) {
		t.Fatalf("登録の書き込む内容: モデルは %+v %+v、実装は %+v", wt, we, got)
	}
	return got, true
}

// @assurance:evidence task-add-title-required
// @assurance:evidence task-add-id-taken
// @assurance:evidence session-required
// @assurance:evidence model-error-precedence
// @assurance:evidence model-errors-only-when
func Test登録の判定はどの入力でもモデルと同じエラーか同じ書き込む内容を返す(t *testing.T) {
	rnd := rand.New(rand.NewPCG(oracle.Seed(t), 2))
	xs := texts(rnd, 200)
	for range 20000 {
		checkAdd(t, pick(rnd, xs), pick(rnd, xs), pick(rnd, xs), pick(rnd, xs), rnd.Int64N(4_000_000_000)-1_000_000_000, rnd.IntN(2) == 0)
	}
	// 境界の主体とタイトルの組み合わせは、すべて回す
	for _, actor := range boundaryTexts {
		for _, title := range boundaryTexts {
			for _, taken := range []bool{false, true} {
				checkAdd(t, actor, title, "", "00000001", 0, taken)
			}
		}
	}
}

// @assurance:evidence task-add-empty-body
func Test本文が何であっても登録の成否とエラーは変わらず成功すれば本文はそのまま入る(t *testing.T) {
	rnd := rand.New(rand.NewPCG(oracle.Seed(t), 4))
	bodies := texts(rnd, 200)
	for _, actor := range []string{"s", " ", ""} {
		for _, title := range []string{"t", "\u3000", ""} {
			for _, taken := range []bool{false, true} {
				base, baseOK := checkAdd(t, actor, title, "", "00000001", 0, taken)
				_, baseErr := task.AddDecide(actor, title, "", "00000001", 0, taken)
				for _, body := range bodies {
					w, ok := checkAdd(t, actor, title, body, "00000001", 0, taken)
					_, err := task.AddDecide(actor, title, body, "00000001", 0, taken)
					if ok != baseOK || err != baseErr {
						t.Fatalf("本文 %q で成否かエラーが変わった（空の本文: %v %v、この本文: %v %v）", body, baseOK, baseErr, ok, err)
					}
					if ok && (w.Task.Body != body || w.Task.Title != base.Task.Title) {
						t.Fatalf("本文 %q がそのまま入らなかった: %q", body, w.Task.Body)
					}
				}
			}
		}
	}
}

func founds() []*task.Task {
	r := []*task.Task{nil}
	for _, st := range task.Statuses {
		r = append(r, &task.Task{ID: "00000001", Title: "t", Body: "b", Status: st, CreatedAt: 5})
	}
	return r
}

func checkFinish(t *testing.T, actor, id, reason string, k task.FinishKind, now int64, found *task.Task) {
	t.Helper()
	got, err := task.FinishDecide(actor, id, reason, k, now, found)
	mk, kerr := oracle.Kind(k)
	if kerr != nil {
		t.Fatal(kerr)
	}
	mf := Q.Companion_Option_.Create_None_()
	if found != nil {
		ft, ferr := oracle.Task(*found)
		if ferr != nil {
			t.Fatal(ferr)
		}
		mf = Q.Companion_Option_.Create_Some_(ft)
	}
	want := oracle.M.FinishDecide(oracle.Str(actor), oracle.Str(id), oracle.Str(reason), mk, oracle.Int(now), mf)
	gf, gerr := implFailure(t, err)
	wf, werr := modelFailure(t, want)
	if gerr != werr || gf != wf {
		t.Fatalf("完了・中止の判定（%q, %q, %v, %+v）: モデルは %v（エラー %v）、実装は %v（エラー %v）", actor, reason, k, found, wf, werr, gf, gerr)
	}
	if werr {
		return
	}
	w, ok := want.Dtor_value().(Q.FinishWrite)
	if !ok {
		t.Fatalf("モデルの書き込む内容が FinishWrite でない: %T", want.Dtor_value())
	}
	wid, _ := oracle.GoStr(w.Dtor_id())
	ws, err := oracle.GoStatus(w.Dtor_status())
	if err != nil {
		t.Fatal(err)
	}
	we, err := oracle.GoEvent(w.Dtor_event())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, task.FinishWrite{ID: wid, Status: ws, Event: we}) {
		t.Fatalf("完了・中止の書き込む内容: モデルは %v %v %+v、実装は %+v", wid, ws, we, got)
	}
}

// @assurance:evidence finish-reason-required
// @assurance:evidence session-required
// @assurance:evidence model-error-precedence
// @assurance:evidence model-errors-only-when
func Test完了と中止の判定はどの入力でもモデルと同じエラーか同じ書き込む内容を返す(t *testing.T) {
	rnd := rand.New(rand.NewPCG(oracle.Seed(t), 6))
	xs := texts(rnd, 200)
	fs := founds()
	for range 20000 {
		k := task.FinishKinds[rnd.IntN(len(task.FinishKinds))]
		checkFinish(t, pick(rnd, xs), "00000001", pick(rnd, xs), k, rnd.Int64N(1000), fs[rnd.IntN(len(fs))])
	}
	for _, actor := range boundaryTexts {
		for _, reason := range boundaryTexts {
			for _, k := range task.FinishKinds {
				for _, f := range fs {
					checkFinish(t, actor, "00000001", reason, k, 9, f)
				}
			}
		}
	}
}

// @assurance:evidence session-required
// @assurance:evidence task-list-order
func Test一覧の判定は作成日時が同じタスクを含む集まりでもモデルと同じ列を返す(t *testing.T) {
	rnd := rand.New(rand.NewPCG(oracle.Seed(t), 8))
	ids := []string{"00000001", "0000000a", "000000a0", "ffffffff", "abcdef01", "a", "ab", "b", "\u00e9", "z"}
	for range 3000 {
		var ts []task.Task
		for _, i := range rnd.Perm(len(ids))[:rnd.IntN(len(ids)+1)] {
			ts = append(ts, task.Task{ID: ids[i], Title: "t", Status: task.Statuses[rnd.IntN(3)], CreatedAt: rnd.Int64N(3)})
		}
		actor := boundaryTexts[rnd.IntN(len(boundaryTexts))]
		all := rnd.IntN(2) == 0
		got, err := task.ListDecide(actor, all, ts)
		mts, terr := oracle.Tasks(ts)
		if terr != nil {
			t.Fatal(terr)
		}
		want := oracle.M.ListDecide(oracle.Str(actor), all, mts)
		gf, gerr := implFailure(t, err)
		wf, werr := modelFailure(t, want)
		if gerr != werr || gf != wf {
			t.Fatalf("一覧の判定（%q）: モデルは %v、実装は %v", actor, wf, gf)
		}
		if werr {
			continue
		}
		seq, ok := want.Dtor_value().(_dafny.Sequence)
		if !ok {
			t.Fatalf("モデルの一覧の結果が列でない: %T", want.Dtor_value())
		}
		wts, err := oracle.GoTasks(seq)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			got = []task.Task{}
		}
		if !reflect.DeepEqual(got, wts) {
			t.Fatalf("一覧の判定: モデルは %+v、実装は %+v", wts, got)
		}
	}
}

// @assurance:evidence session-required
// @assurance:evidence task-add-title-required
// @assurance:evidence finish-reason-required
func Test空白の文字の判定はすべての文字でモデルと一致する(t *testing.T) {
	// 空白だけかの判定（IsBlank）は、文字ごとの判定と、空の文字列の扱いを、ほかのテストの境界の文字列で確かめる
	for r := rune(0); r <= 0x10FFFF; r++ {
		if 0xD800 <= r && r <= 0xDFFF {
			continue // サロゲートは文字（Unicode のスカラー値）でない
		}
		if task.IsWhiteSpace(r) != oracle.M.IsWhiteSpace(_dafny.CodePoint(r)) {
			t.Fatalf("U+%04X の空白の判定が、実装とモデルで違う", r)
		}
	}
}

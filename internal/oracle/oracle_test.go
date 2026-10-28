package oracle

import (
	"reflect"
	"testing"

	Q "github.com/TadahiroYamamura/quenta/internal/spec/Quenta"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

func Test文字列と整数と状態とタスクとイベントは往復で値が変わらない(t *testing.T) {
	for _, s := range []string{"", " ", "\u3000x\t\n", "多言語 مرحبا 😀", "\u0000", "\U0010FFFF"} {
		got, err := GoStr(Str(s))
		if err != nil || got != s {
			t.Fatalf("文字列 %q が %q に変わった（%v）", s, got, err)
		}
	}
	for _, i := range []int64{0, 1, -1, 1 << 62, -(1 << 62), 9223372036854775807, -9223372036854775808} {
		if got := Int(i).Int64(); got != i {
			t.Fatalf("整数 %d が %d に変わった", i, got)
		}
	}
	for _, st := range task.Statuses {
		m, err := Status(st)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := GoStatus(m); err != nil || got != st {
			t.Fatalf("状態 %q が %q に変わった（%v）", st, got, err)
		}
	}
	if _, err := Status("bogus"); err == nil {
		t.Fatal("知らない状態を変換できた")
	}
	for _, k := range task.FinishKinds {
		if _, err := Kind(k); err != nil {
			t.Fatal(err)
		}
	}
	done := task.Done
	st := State{
		Tasks:  []task.Task{{ID: "a", Title: " t ", Body: "", Status: task.Todo, CreatedAt: -5}, {ID: "b", Title: "u", Body: "b", Status: task.Done, CreatedAt: 7}},
		Events: []task.Event{{TaskID: "a", Actor: "s", To: task.Todo, At: -5}, {TaskID: "b", Actor: "s", From: &done, To: task.Canceled, Reason: "r", At: 8}},
	}
	m, err := st.Model()
	if err != nil {
		t.Fatal(err)
	}
	got, err := GoState(m)
	if err != nil || !reflect.DeepEqual(got, st) {
		t.Fatalf("状態の全体が変わった: %+v（%v）", got, err)
	}
}

func Testモデルのエラーのすべての種類が実装の判定のエラーに写る(t *testing.T) {
	c := Q.Companion_Error_
	want := map[task.Failure]Q.Error{
		task.SessionRequired: c.Create_SessionRequired_(), task.TitleRequired: c.Create_TitleRequired_(), task.ReasonRequired: c.Create_ReasonRequired_(),
		task.NotFound: c.Create_NotFound_(), task.AlreadyFinished: c.Create_AlreadyFinished_(), task.IDTaken: c.Create_IdTaken_(),
	}
	if len(want) != len(task.Failures) {
		t.Fatalf("判定のエラーの種類 %d 個のうち、%d 個しか確かめていない", len(task.Failures), len(want))
	}
	for f, m := range want {
		if got, err := Failure(m); err != nil || got != f {
			t.Fatalf("%v が %v に写った（%v）", m, got, err)
		}
	}
}

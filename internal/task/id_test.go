package task_test

import (
	"bytes"
	"testing"
	"unicode"

	"github.com/TadahiroYamamura/quenta/internal/task"
)

// @assurance:evidence task-id-format
func TestIDの材料を4バイト読めなければエラーを返す(t *testing.T) {
	for _, b := range [][]byte{{}, {1}, {1, 2, 3}} {
		if id, err := task.NewID(bytes.NewReader(b)); err == nil {
			t.Fatalf("材料 %d バイトで ID %q を作った", len(b), id)
		}
	}
}

func Test判定のエラーのすべての種類に説明の文がある(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range task.Failures {
		m := f.Error()
		if m == "" || m == task.Failure(0).Error() || seen[m] {
			t.Fatalf("%d の説明の文が、空か、知らない値の文か、ほかと同じ: %q", f, m)
		}
		seen[m] = true
	}
}

// @assurance:evidence task-add-title-required
// @assurance:evidence finish-reason-required
// @assurance:evidence session-required
func Test空白の文字の判定はすべての文字でGoのUnicodeのWhite_Spaceの表と一致する(t *testing.T) {
	// 期待値は、仕様とも実装とも別の出典（Go の unicode パッケージの表）から取る。仕様と実装が同じように誤っていても気づけるように
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if task.IsWhiteSpace(r) != unicode.Is(unicode.White_Space, r) {
			t.Fatalf("U+%04X: 実装は %v、Go の表（Unicode %s）は %v", r, task.IsWhiteSpace(r), unicode.Version, !task.IsWhiteSpace(r))
		}
	}
}

func Test完了か中止かの知らない値ではエラーを返し書き込む内容を作らない(t *testing.T) {
	found := &task.Task{ID: "a", Status: task.Todo}
	for _, k := range []task.FinishKind{0, task.FinishKind(len(task.FinishKinds) + 1)} {
		if w, err := task.FinishDecide("s", "a", "r", k, 0, found); err == nil {
			t.Fatalf("知らない値 %d で書き込む内容を作った: %+v", k, w)
		}
	}
}

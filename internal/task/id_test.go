package task_test

import (
	"bytes"
	"testing"

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

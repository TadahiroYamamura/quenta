//go:build checkpower

package ops_test

import (
	"crypto/rand"
	"testing"

	"github.com/TadahiroYamamura/quenta/internal/task"
)

// 実行時の確認の検出力を測るためだけのテスト（make check-power で使う）。操作を実行して、エラーにならないことだけを見る。判定はほかに持たない
func Test操作を実行してエラーにならない(t *testing.T) {
	o, _ := newOps(t, nil)
	o.Random = rand.Reader
	for i := range 20 {
		a, err := o.Add("s", "t", "b")
		if err != nil {
			t.Fatal(err)
		}
		k := task.Completing
		if i%2 == 1 {
			k = task.Canceling
		}
		if _, err := o.Finish("s", a.ID, "r", k); err != nil {
			t.Fatal(err)
		}
		if _, err := o.List("s", true); err != nil {
			t.Fatal(err)
		}
	}
}

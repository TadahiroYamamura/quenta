package oracle

import (
	"io"
	"testing"
)

// MustClose は、閉じるときのエラーを、テストの誤りとして挙げる
func MustClose(t testing.TB, c io.Closer) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("閉じられない: %v", err)
	}
}

// CloseLater は、テストの終わりに閉じ、閉じるときのエラーをテストの誤りとして挙げる
func CloseLater(t testing.TB, c io.Closer) {
	t.Helper()
	t.Cleanup(func() { MustClose(t, c) })
}

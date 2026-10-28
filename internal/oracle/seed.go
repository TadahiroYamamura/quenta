package oracle

import (
	"math/rand/v2"
	"os"
	"strconv"
	"testing"
)

// Seed は、無作為な入力を作るテストのシード値を返す。毎回違う値を使い、試す入力の範囲を動かすたびに広げる。
// 使った値はテストのログに書くので、落ちたときは QUENTA_TEST_SEED=<値> を渡して、同じ入力で動かし直せる
func Seed(t *testing.T) uint64 {
	t.Helper()
	seed := rand.Uint64()
	if s := os.Getenv("QUENTA_TEST_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("QUENTA_TEST_SEED が整数でない: %q", s)
		}
		seed = v
	}
	t.Logf("シード値: %d（同じ入力で動かし直すには QUENTA_TEST_SEED=%d）", seed, seed)
	return seed
}

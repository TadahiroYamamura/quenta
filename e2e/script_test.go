package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// 受け入れ条件の台本（testdata/*.txtar）を実行する。台本の exec quenta は、TestMain でビルドした CLI を別のプロセスとして呼ぶ。
// 台本ごとに、HOME・XDG_DATA_HOME・作業ディレクトリを一時ディレクトリの下に分け、利用者のデータに触れない
func Test受け入れ条件の台本がすべて通る(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata",
		Setup: func(env *testscript.Env) error {
			home := filepath.Join(env.WorkDir, "home")
			data := filepath.Join(env.WorkDir, "data")
			for _, d := range []string{home, data} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					return err
				}
			}
			env.Setenv("HOME", home)
			env.Setenv("XDG_DATA_HOME", data)
			env.Setenv("PATH", filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"))
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"setjson": setjson,
		},
	})
}

// setjson <変数名> <項目のパス> は、直前のコマンドの標準出力の JSON から、パス（例: task.id、events.1.at）の値を取り出し、
// 環境変数に入れる。値を取り出すだけで、判定はしない（判定は台本の stdout・cmpenv で行う。CLAUDE.md の「E2E テストの台本」）
func setjson(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("setjson は ! を付けて使えない")
	}
	if len(args) != 2 {
		ts.Fatalf("使い方: setjson <変数名> <項目のパス>")
	}
	var v any
	if err := json.Unmarshal([]byte(ts.ReadFile("stdout")), &v); err != nil {
		ts.Fatalf("直前の標準出力が JSON でない: %v", err)
	}
	for _, key := range strings.Split(args[1], ".") {
		switch cur := v.(type) {
		case map[string]any:
			next, ok := cur[key]
			if !ok {
				ts.Fatalf("項目 %q がない（パス %s）", key, args[1])
			}
			v = next
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(cur) {
				ts.Fatalf("配列の位置 %q が範囲の外（パス %s）", key, args[1])
			}
			v = cur[i]
		default:
			ts.Fatalf("%q の手前が、オブジェクトでも配列でもない（パス %s）", key, args[1])
		}
	}
	s, ok := v.(string)
	if !ok {
		s = fmt.Sprint(v)
	}
	ts.Setenv(args[0], s)
}

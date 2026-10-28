// Package e2e は、e2e/testdata/*.txtar の台本を、cmd/quenta をビルドしたバイナリで動かす
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// binDir は、ビルドした quenta を置くディレクトリ。台本の PATH の先頭に置く
var binDir string

// TestMain は、本番と同じ入り口（cmd/quenta）をビルドしてから、台本を動かす
func TestMain(m *testing.M) {
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	dir, err := os.MkdirTemp("", "quenta-e2e-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ビルドの置き場所を作れない:", err)
		return 1
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintln(os.Stderr, "ビルドの置き場所を消せない:", err)
		}
	}()
	out, err := exec.Command("go", "build", "-o", filepath.Join(dir, "quenta"), "github.com/TadahiroYamamura/quenta/cmd/quenta").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "quenta をビルドできない: %v\n%s", err, out)
		return 1
	}
	binDir = dir
	return m.Run()
}

func TestScripts(t *testing.T) {
	repo, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	// install-with-make の台本が台本の中で go build を動かすので、Go の道具の置き場所だけは外から渡す（testscript は HOME を /no-home にする）
	goenv, err := exec.Command("go", "env", "GOCACHE", "GOMODCACHE", "GOPATH").Output()
	if err != nil {
		t.Fatal(err)
	}
	gopaths := strings.Split(strings.TrimSpace(string(goenv)), "\n")
	if len(gopaths) != 3 {
		t.Fatalf("go env の出力が 3 行でない: %q", goenv)
	}
	testscript.Run(t, testscript.Params{
		Dir: "testdata",
		// 台本には、外の環境変数（QUENTA_SESSION・XDG_DATA_HOME など）を、Go の道具の置き場所のほかは渡さない。台本の始めに QUENTA_SESSION がないことを、台本が前提にするため
		Setup: func(env *testscript.Env) error {
			env.Setenv("PATH", binDir+string(os.PathListSeparator)+env.Getenv("PATH"))
			env.Setenv("QUENTA_REPO", repo)
			env.Setenv("GOCACHE", gopaths[0])
			env.Setenv("GOMODCACHE", gopaths[1])
			env.Setenv("GOPATH", gopaths[2])
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"setjson": setjson,
		},
	})
}

// setjson <変数名> <項目のパス> は、直前のコマンドの標準出力の JSON から値を取り出し、環境変数に入れる（CLAUDE.md の「E2E テストの台本」）
func setjson(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 2 {
		ts.Fatalf("使い方: setjson <変数名> <項目のパス>")
	}
	var v any
	if err := json.Unmarshal([]byte(ts.ReadFile("stdout")), &v); err != nil {
		ts.Fatalf("直前の標準出力が JSON でない: %v", err)
	}
	for _, key := range strings.Split(args[1], ".") {
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			if v, ok = x[key]; !ok {
				ts.Fatalf("項目 %q がない", key)
			}
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(x) {
				ts.Fatalf("配列に位置 %q がない", key)
			}
			v = x[i]
		default:
			ts.Fatalf("項目 %q の手前が、項目も位置も持たない", key)
		}
	}
	s, ok := v.(string)
	if !ok {
		s = fmt.Sprint(v)
	}
	ts.Setenv(args[0], s)
}

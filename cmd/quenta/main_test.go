package main

import (
	"os/exec"
	"strings"
	"testing"
)

// @assurance:evidence binary-excludes-generated
func Test本番のバイナリはDafnyから生成したコードもそれを使うテストの補助も含まない(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/TadahiroYamamura/quenta/cmd/quenta").Output()
	if err != nil {
		t.Fatal(err)
	}
	deps := strings.Fields(string(out))
	if len(deps) == 0 || !strings.Contains(string(out), "github.com/TadahiroYamamura/quenta/internal/cli") {
		t.Fatalf("依存の一覧が取れていない: %q", out)
	}
	for _, d := range deps {
		if strings.HasPrefix(d, "github.com/TadahiroYamamura/quenta/internal/spec") || d == "github.com/TadahiroYamamura/quenta/internal/oracle" {
			t.Fatalf("本番のバイナリの依存に %s がある", d)
		}
	}
}

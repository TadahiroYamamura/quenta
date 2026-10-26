// Package e2e は、ビルドした quenta を別のプロセスとして呼んで確かめる。
// 受け入れ条件は testdata の台本（script_test.go が実行する）で、CLAUDE.md の実装の制約はこのファイルのテストで確かめる
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

var binary string

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "quenta-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	binary = filepath.Join(dir, "quenta")
	_, file, _, _ := runtime.Caller(0)
	build := exec.Command("go", "build", "-o", binary, "./cmd/quenta")
	build.Dir = filepath.Join(filepath.Dir(file), "..")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "ビルドできない: %v\n%s", err, out)
		return 1
	}
	return m.Run()
}

// sandbox は、利用者のデータに触れないよう、HOME・XDG_DATA_HOME・作業ディレクトリをすべて一時ディレクトリにする
type sandbox struct {
	t       *testing.T
	home    string
	data    string
	cwd     string
	session string
	noXDG   bool
	extra   []string // 追加の環境変数（KEY=VALUE）
}

func newSandbox(t *testing.T) *sandbox {
	return &sandbox{t: t, home: t.TempDir(), data: t.TempDir(), cwd: t.TempDir(), session: "session-1"}
}

type result struct {
	code           int
	stdout, stderr []byte
}

func (s *sandbox) quenta(args ...string) result {
	s.t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = s.cwd
	// 親の環境変数を引き継がない（利用者の XDG_DATA_HOME や QUENTA_SESSION を使わないため）
	cmd.Env = []string{"HOME=" + s.home, "PATH=" + os.Getenv("PATH"), "TZ=Asia/Tokyo"}
	if !s.noXDG {
		cmd.Env = append(cmd.Env, "XDG_DATA_HOME="+s.data)
	}
	if s.session != "" {
		cmd.Env = append(cmd.Env, "QUENTA_SESSION="+s.session)
	}
	cmd.Env = append(cmd.Env, s.extra...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		s.t.Fatalf("起動できない: %v", err)
	}
	return result{code, stdout.Bytes(), stderr.Bytes()}
}

type taskJSON struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

type eventJSON struct {
	Actor  string  `json:"actor"`
	From   *string `json:"from"`
	To     string  `json:"to"`
	Reason *string `json:"reason"`
	At     string  `json:"at"`
}

type showJSON struct {
	Task   taskJSON    `json:"task"`
	Events []eventJSON `json:"events"`
}

func ok[T any](t *testing.T, r result) T {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("失敗した: code=%d stderr=%s", r.code, r.stderr)
	}
	var v T
	dec := json.NewDecoder(bytes.NewReader(r.stdout))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("出力を読めない: %v: %s", err, r.stdout)
	}
	return v
}

func failsWith(t *testing.T, r result, code string) {
	t.Helper()
	if r.code == 0 {
		t.Fatalf("成功してしまった: %s", r.stdout)
	}
	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.stderr, &e); err != nil {
		t.Fatalf("エラーが JSON でない: %q", r.stderr)
	}
	if e.Error.Code != code {
		t.Errorf("エラーコード = %q, want %q (%s)", e.Error.Code, code, e.Error.Message)
	}
}

func (s *sandbox) add(title string, extra ...string) taskJSON {
	s.t.Helper()
	return ok[struct {
		Task taskJSON `json:"task"`
	}](s.t, s.quenta(append([]string{"task", "add", "--title", title}, extra...)...)).Task
}

func (s *sandbox) show(id string) showJSON {
	s.t.Helper()
	return ok[showJSON](s.t, s.quenta("task", "show", id))
}

// 受け入れ条件ではなく、CLAUDE.md の実装の制約「特定のエージェントに依存しない」を確かめる
func Test主体はQUENTA_SESSIONだけから読みエージェント固有の環境変数は読まない(t *testing.T) {
	agentVars := []string{"CLAUDE_CODE_SESSION_ID=claude-session", "CLAUDE_SESSION_ID=claude-session"}

	// QUENTA_SESSION がなければ、エージェント固有の環境変数があっても主体がないとみなす
	s := newSandbox(t)
	s.session = ""
	s.extra = agentVars
	failsWith(t, s.quenta("task", "add", "--title", "A"), "session_required")

	// 両方あれば、主体は QUENTA_SESSION の値そのもの
	s = newSandbox(t)
	s.extra = agentVars
	tk := s.add("A")
	ok[any](t, s.quenta("task", "done", tk.ID, "--reason", "r"))
	for _, e := range s.show(tk.ID).Events {
		if e.Actor != "session-1" {
			t.Errorf("主体 = %q, want %q", e.Actor, "session-1")
		}
	}
}

// 受け入れ条件ではなく、CLAUDE.md の実装の制約「git を前提にしない」「対話的な入力をしない」を確かめる。
//   - PATH には、呼ばれたら印のファイルを書くだけの偽の git だけを置く。PATH から探すコマンドは、偽の git のほかは呼べない
//   - 作業ディレクトリに偽の .git/ を置き、操作の前後で中身と更新日時が変わらないことを確かめる
//   - 標準入力は開いたまま何も書かない。入力を待てば時間切れで落ちる
//
// 次の実装は、.git/ を変えず外から観察できないので見分けられない
//   - .git/ を読んで結果を捨てる
//   - 絶対パスで本物の git の読み取りだけのコマンド（rev-parse など）を呼ぶ
func Test操作の間にgitを呼ばず_gitディレクトリを変えず標準入力を待たない(t *testing.T) {
	s := newSandbox(t)
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "git-was-called")
	fakeGit := "#!/bin/sh\necho \"$@\" >> '" + marker + "'\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(fakeGit), 0o755); err != nil {
		t.Fatal(err)
	}
	gitDir := filepath.Join(s.cwd, ".git")
	for name, content := range map[string]string{
		"HEAD":                 "ref: refs/heads/main\n",
		"config":               "[core]\n\trepositoryformatversion = 0\n",
		"refs/heads/main":      "0123456789abcdef0123456789abcdef01234567\n",
		"info/exclude":         "",
		"objects/.placeholder": "",
	} {
		path := filepath.Join(gitDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 更新日時の変化を見分けられるよう、過去の日時にそろえる
	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := filepath.WalkDir(gitDir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, past, past)
	}); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, gitDir)

	run := func(args ...string) result {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = s.cwd
		cmd.Env = []string{"HOME=" + s.home, "PATH=" + binDir, "XDG_DATA_HOME=" + s.data, "QUENTA_SESSION=" + s.session}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		defer stdin.Close()
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err = cmd.Run()
		if ctx.Err() != nil {
			t.Fatalf("%v: 10 秒で終わらなかった（入力を待っている）", args)
		}
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("起動できない: %v", err)
		}
		return result{code, stdout.Bytes(), stderr.Bytes()}
	}
	tk := ok[struct {
		Task taskJSON `json:"task"`
	}](t, run("task", "add", "--title", "A", "--body", "b")).Task
	ok[any](t, run("task", "list", "--all"))
	ok[any](t, run("task", "show", tk.ID))
	ok[any](t, run("task", "done", tk.ID, "--reason", "r"))
	failsWith(t, run("task", "cancel", tk.ID, "--reason", "r"), "already_finished")
	failsWith(t, run("task", "add"), "title_required")

	if args, err := os.ReadFile(marker); err == nil {
		t.Errorf("git を呼んだ: %q", args)
	}
	if after := snapshot(t, gitDir); !reflect.DeepEqual(after, before) {
		t.Errorf(".git/ が変わった:\n前 %v\n後 %v", before, after)
	}
}

// snapshot は、ディレクトリの下のすべてのファイルとディレクトリの、中身・権限・更新日時を返す
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		var content []byte
		if !d.IsDir() {
			if content, err = os.ReadFile(path); err != nil {
				return err
			}
		}
		rel, _ := filepath.Rel(root, path)
		out[filepath.ToSlash(rel)] = fmt.Sprintf("%v %v %q", info.Mode(), info.ModTime().UnixNano(), content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

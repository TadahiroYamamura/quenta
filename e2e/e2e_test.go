// Package e2e は、ビルドした quenta を別のプロセスとして呼び、Issue #1 の受け入れ条件を 1 項目ずつ確かめる。
// テスト名の AC と番号は、受け入れ条件の番号
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
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

type summaryJSON struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
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

func (s *sandbox) list(args ...string) []summaryJSON {
	s.t.Helper()
	return ok[struct {
		Tasks []summaryJSON `json:"tasks"`
	}](s.t, s.quenta(append([]string{"task", "list"}, args...)...)).Tasks
}

var timeFormat = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

func TestAC01_タスクをタイトルと本文で登録するとtodoのタスクとして保存されIDとタイトルと本文と状態と作成日時がJSONで返る(t *testing.T) {
	s := newSandbox(t)
	before := time.Now().UTC().Truncate(time.Millisecond)
	tk := s.add("牛乳を買う", "--body", "低脂肪のもの")
	after := time.Now().UTC()

	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(tk.ID) {
		t.Errorf("ID が 16 進 8 桁でない: %q", tk.ID)
	}
	if tk.Title != "牛乳を買う" || tk.Body != "低脂肪のもの" || tk.Status != "todo" {
		t.Errorf("got %+v", tk)
	}
	if !timeFormat.MatchString(tk.CreatedAt) {
		t.Fatalf("作成日時の形が違う: %q", tk.CreatedAt)
	}
	created, _ := time.Parse(time.RFC3339Nano, tk.CreatedAt)
	if created.Before(before) || created.After(after) {
		t.Errorf("作成日時 %v が登録の前後（%v〜%v）にない", created, before, after)
	}
	// 保存されている
	if got := s.show(tk.ID).Task; got != tk {
		t.Errorf("保存された内容: %+v", got)
	}
}

func TestAC02_本文は省略できる(t *testing.T) {
	s := newSandbox(t)
	tk := s.add("本文なし")
	if tk.Body != "" || tk.Status != "todo" {
		t.Errorf("got %+v", tk)
	}
	if got := s.show(tk.ID).Task; got != tk {
		t.Errorf("保存された内容: %+v", got)
	}
}

func TestAC03_タイトルが空なら登録できずエラーコードが返る(t *testing.T) {
	for _, args := range [][]string{
		{"task", "add"},
		{"task", "add", "--title", ""},
		{"task", "add", "--title", "  \t　"},
		{"task", "add", "--title", "", "--body", "本文だけ"},
	} {
		s := newSandbox(t)
		failsWith(t, s.quenta(args...), "title_required")
		if got := s.list("--all"); len(got) != 0 {
			t.Errorf("%v: 登録された: %+v", args, got)
		}
	}
}

func TestAC04_一覧を見ると終わっていないタスクだけが返る(t *testing.T) {
	s := newSandbox(t)
	a := s.add("A", "--body", "本文A")
	b := s.add("B")
	c := s.add("C")
	d := s.add("D")
	ok[any](t, s.quenta("task", "done", b.ID, "--reason", "済んだ"))
	ok[any](t, s.quenta("task", "cancel", d.ID, "--reason", "要らない"))
	// 各項目は、登録したときに返った値と、すべての項目で一致する（本文は一覧に含まない）
	want := []summaryJSON{summaryOf(a, "todo"), summaryOf(c, "todo")}
	if got := s.list(); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestAC05_終わったタスクも含めるよう指定するとすべてのタスクが返る(t *testing.T) {
	s := newSandbox(t)
	a := s.add("A")
	b := s.add("B", "--body", "本文B")
	c := s.add("C")
	ok[any](t, s.quenta("task", "done", a.ID, "--reason", "済んだ"))
	ok[any](t, s.quenta("task", "cancel", c.ID, "--reason", "要らない"))
	want := []summaryJSON{summaryOf(a, "done"), summaryOf(b, "todo"), summaryOf(c, "canceled")}
	if got := s.list("--all"); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// summaryOf は、登録したときに返ったタスクから、一覧に出るはずの項目を作る
func summaryOf(t taskJSON, status string) summaryJSON {
	return summaryJSON{ID: t.ID, Title: t.Title, Status: status, CreatedAt: t.CreatedAt}
}

func TestAC06_IDを指定してタスクを見るとタスクの内容とそのタスクのイベントが返る(t *testing.T) {
	s := newSandbox(t)
	tk := s.add("見る", "--body", "本文")
	other := s.add("別のタスク")
	ok[any](t, s.quenta("task", "done", other.ID, "--reason", "別の理由"))

	got := s.show(tk.ID)
	if got.Task != tk {
		t.Errorf("task: %+v", got.Task)
	}
	// 別のタスクのイベントは含まない。登録もイベントとして残る
	if len(got.Events) != 1 {
		t.Fatalf("events: %+v", got.Events)
	}
	if e := got.Events[0]; e.Actor != "session-1" || e.From != nil || e.To != "todo" || e.Reason != nil || e.At != tk.CreatedAt {
		t.Errorf("登録のイベント: %+v", e)
	}
}

func TestAC07_理由を付けて完了にするとdoneになりイベントが記録される(t *testing.T) {
	s := newSandbox(t)
	tk := s.add("完了させる")
	s.session = "session-2"
	res := ok[struct {
		Task taskJSON `json:"task"`
	}](t, s.quenta("task", "done", tk.ID, "--reason", "テストが通った"))
	want := tk
	want.Status = "done"
	if res.Task != want {
		t.Errorf("返った task: %+v", res.Task)
	}
	got := s.show(tk.ID)
	if got.Task != want {
		t.Errorf("保存された task: %+v", got.Task)
	}
	if len(got.Events) != 2 {
		t.Fatalf("events: %+v", got.Events)
	}
	e := got.Events[1]
	if e.Actor != "session-2" || e.From == nil || *e.From != "todo" || e.To != "done" || e.Reason == nil || *e.Reason != "テストが通った" || !timeFormat.MatchString(e.At) {
		t.Errorf("完了のイベント: %+v", e)
	}
}

func TestAC08_理由を付けて中止にするとcanceledになりイベントが記録される(t *testing.T) {
	s := newSandbox(t)
	tk := s.add("中止する")
	s.session = "session-2"
	res := ok[struct {
		Task taskJSON `json:"task"`
	}](t, s.quenta("task", "cancel", tk.ID, "--reason", "方針が変わった"))
	want := tk
	want.Status = "canceled"
	if res.Task != want {
		t.Errorf("返った task: %+v", res.Task)
	}
	got := s.show(tk.ID)
	if got.Task != want {
		t.Errorf("保存された task: %+v", got.Task)
	}
	if len(got.Events) != 2 {
		t.Fatalf("events: %+v", got.Events)
	}
	e := got.Events[1]
	if e.Actor != "session-2" || e.From == nil || *e.From != "todo" || e.To != "canceled" || e.Reason == nil || *e.Reason != "方針が変わった" {
		t.Errorf("中止のイベント: %+v", e)
	}
}

func TestAC09_理由がないと完了にも中止にもできずエラーコードが返りタスクは変わらない(t *testing.T) {
	s := newSandbox(t)
	tk := s.add("理由なし")
	before := s.show(tk.ID)
	for _, cmd := range []string{"done", "cancel"} {
		for _, reason := range [][]string{{}, {"--reason", ""}, {"--reason", " \n　"}} {
			failsWith(t, s.quenta(append([]string{"task", cmd, tk.ID}, reason...)...), "reason_required")
		}
	}
	if after := s.show(tk.ID); !reflect.DeepEqual(after, before) {
		t.Errorf("変わった: %+v -> %+v", before, after)
	}
}

func TestAC10_終わったタスクの状態は変えられずエラーコードが返りタスクは変わらない(t *testing.T) {
	s := newSandbox(t)
	done := s.add("完了済み")
	canceled := s.add("中止済み")
	ok[any](t, s.quenta("task", "done", done.ID, "--reason", "r"))
	ok[any](t, s.quenta("task", "cancel", canceled.ID, "--reason", "r"))
	for _, id := range []string{done.ID, canceled.ID} {
		before := s.show(id)
		for _, cmd := range []string{"done", "cancel"} {
			failsWith(t, s.quenta("task", cmd, id, "--reason", "もう一度"), "already_finished")
		}
		if after := s.show(id); !reflect.DeepEqual(after, before) {
			t.Errorf("変わった: %+v -> %+v", before, after)
		}
	}
}

func TestAC11_存在しないIDを指定するとエラーコードが返る(t *testing.T) {
	s := newSandbox(t)
	s.add("ある")
	for _, args := range [][]string{
		{"task", "show", "ffffffff"},
		{"task", "done", "ffffffff", "--reason", "r"},
		{"task", "cancel", "ffffffff", "--reason", "r"},
	} {
		failsWith(t, s.quenta(args...), "not_found")
	}
}

func TestAC12_別々にCLIを起動しても登録したタスクと状態の変更が残っている(t *testing.T) {
	s := newSandbox(t)
	// 1 回の起動ごとに 1 つの操作をする
	a := s.add("A")
	b := s.add("B")
	ok[any](t, s.quenta("task", "done", a.ID, "--reason", "r"))
	got := s.list("--all")
	if len(got) != 2 || got[0].ID != a.ID || got[0].Status != "done" || got[1].ID != b.ID || got[1].Status != "todo" {
		t.Errorf("got %+v", got)
	}
	if evs := s.show(a.ID).Events; len(evs) != 2 {
		t.Errorf("イベントが残っていない: %+v", evs)
	}
}

func TestAC13_データはXDG_DATA_HOMEの下にだけ作られカレントディレクトリには何も作られない(t *testing.T) {
	s := newSandbox(t)
	tk := s.add("A", "--body", "b")
	ok[any](t, s.quenta("task", "list"))
	ok[any](t, s.quenta("task", "show", tk.ID))
	ok[any](t, s.quenta("task", "done", tk.ID, "--reason", "r"))
	failsWith(t, s.quenta("task", "add", "--title", ""), "title_required")

	if files := walk(t, s.cwd); len(files) != 0 {
		t.Errorf("カレントディレクトリに作った: %v", files)
	}
	if files := walk(t, s.home); len(files) != 0 {
		t.Errorf("HOME に作った: %v", files)
	}
	// SQLite が一時的に作る -wal・-shm は、閉じるときに消える
	if files := walk(t, s.data); !reflect.DeepEqual(files, []string{"quenta", "quenta/quenta.db"}) {
		t.Errorf("XDG_DATA_HOME の下: %v", files)
	}
}

func TestAC13_XDG_DATA_HOMEが未設定ならHOMEの下の_local_shareにだけ作られる(t *testing.T) {
	s := newSandbox(t)
	s.noXDG = true
	s.add("A")
	if files := walk(t, s.cwd); len(files) != 0 {
		t.Errorf("カレントディレクトリに作った: %v", files)
	}
	want := []string{".local", ".local/share", ".local/share/quenta", ".local/share/quenta/quenta.db"}
	if files := walk(t, s.home); !reflect.DeepEqual(files, want) {
		t.Errorf("HOME の下: %v", files)
	}
	if files := walk(t, s.data); len(files) != 0 {
		t.Errorf("使っていない XDG_DATA_HOME に作った: %v", files)
	}
}

func walk(t *testing.T, root string) []string {
	t.Helper()
	out := []string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

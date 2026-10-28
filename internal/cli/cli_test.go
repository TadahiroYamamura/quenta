package cli

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TadahiroYamamura/quenta/internal/oracle"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

type fixture struct {
	t    *testing.T
	env  map[string]string
	now  time.Time
	data string
}

func newFixture(t *testing.T) *fixture {
	dir := t.TempDir()
	return &fixture{t: t, env: map[string]string{"QUENTA_SESSION": "s1", "XDG_DATA_HOME": dir, "HOME": filepath.Join(dir, "home")}, now: time.Unix(1_700_000_000, 0), data: filepath.Join(dir, "quenta", "quenta.db")}
}

func (f *fixture) run(args ...string) (int, string) {
	var out bytes.Buffer
	code := Run(args, Env{Getenv: func(k string) string { return f.env[k] }, Stdout: &out, Now: func() time.Time { return f.now }, Random: rand.Reader})
	return code, out.String()
}

func (f *fixture) ok(args ...string) map[string]any {
	f.t.Helper()
	code, out := f.run(args...)
	if code != 0 {
		f.t.Fatalf("%v: 終了コード %d、出力 %s", args, code, out)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		f.t.Fatalf("%v: 出力が JSON でない: %q", args, out)
	}
	return v
}

// fail は、失敗の出力を確かめ、エラーコードと message を返す
func (f *fixture) fail(args ...string) (string, string) {
	f.t.Helper()
	code, out := f.run(args...)
	if code == 0 {
		f.t.Fatalf("%v: 成功した: %s", args, out)
	}
	var v map[string]map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		f.t.Fatalf("%v: 失敗の出力が JSON でない: %q", args, out)
	}
	if len(v) != 1 || len(v["error"]) != 2 {
		f.t.Fatalf("%v: 失敗の出力の項目が error.code と error.message だけでない: %s", args, out)
	}
	c, _ := v["error"]["code"].(string)
	m, _ := v["error"]["message"].(string)
	if c == "" || m == "" {
		f.t.Fatalf("%v: code か message が空: %s", args, out)
	}
	return c, m
}

func (f *fixture) code(want Code, args ...string) string {
	f.t.Helper()
	c, m := f.fail(args...)
	if c != string(want) {
		f.t.Fatalf("%v: エラーコード %q（%q のはず）: %s", args, c, want, m)
	}
	return m
}

func (f *fixture) add(title string, extra ...string) map[string]any {
	f.t.Helper()
	return f.ok(append([]string{"task", "add", "--title", title}, extra...)...)["task"].(map[string]any)
}

func (f *fixture) show(id string) map[string]any { return f.ok("task", "show", id) }

// @assurance:evidence cli-body-omitted
func Test本文のフラグを省くか値なしで最後に置くと本文は空になる(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{{"task", "add", "--title", "t"}, {"task", "add", "--title", "t", "--body"}} {
		if b := f.ok(args...)["task"].(map[string]any)["body"]; b != "" {
			t.Fatalf("%v: 本文が %q", args, b)
		}
	}
}

// @assurance:evidence cli-title-omitted
func Testタイトルのフラグを省くか値なしで最後に置くとtitle_requiredになる(t *testing.T) {
	f := newFixture(t)
	f.code(CodeTitleRequired, "task", "add")
	f.code(CodeTitleRequired, "task", "add", "--body", "b")
	f.code(CodeTitleRequired, "task", "add", "--body", "b", "--title")
	f.code(CodeTitleRequired, "task", "add", "--title=")
	if _, err := os.Stat(f.data); !os.IsNotExist(err) {
		t.Fatalf("データのファイルができた: %v", err)
	}
}

// @assurance:evidence cli-reason-omitted
func Test理由のフラグを省くか値なしで最後に置くと完了も中止もreason_requiredになる(t *testing.T) {
	f := newFixture(t)
	id := f.add("t")["id"].(string)
	for _, cmd := range []string{"done", "cancel"} {
		f.code(CodeReasonRequired, "task", cmd, id)
		f.code(CodeReasonRequired, "task", cmd, id, "--reason")
	}
	if st := f.show(id)["task"].(map[string]any)["status"]; st != "todo" {
		t.Fatalf("状態が %v", st)
	}
}

// @assurance:evidence cli-list-all-flag
func Test一覧はallの有無で終わったタスクを含めるかが変わる(t *testing.T) {
	f := newFixture(t)
	a := f.add("a")["id"].(string)
	f.now = f.now.Add(time.Second)
	b := f.add("b")["id"].(string)
	f.ok("task", "done", b, "--reason", "r")
	ids := func(v map[string]any) []string {
		var r []string
		for _, x := range v["tasks"].([]any) {
			r = append(r, x.(map[string]any)["id"].(string))
		}
		return r
	}
	if got := ids(f.ok("task", "list")); !slices.Equal(got, []string{a}) {
		t.Fatalf("--all なし: %v", got)
	}
	if got := ids(f.ok("task", "list", "--all")); !slices.Equal(got, []string{a, b}) {
		t.Fatalf("--all あり: %v", got)
	}
}

// @assurance:evidence cli-session-from-env
func Test主体はQUENTA_SESSIONの値をそのまま使いエージェント固有の環境変数は読まない(t *testing.T) {
	f := newFixture(t)
	for _, s := range []string{" s 1 ", "\u3000x\t", "セッション"} {
		f.env["QUENTA_SESSION"] = s
		id := f.add("t")["id"].(string)
		ev := f.show(id)["events"].([]any)[0].(map[string]any)
		if ev["actor"] != s {
			t.Fatalf("主体が %q（%q のはず）", ev["actor"], s)
		}
	}
	delete(f.env, "QUENTA_SESSION")
	f.env["CLAUDE_CODE_SESSION_ID"] = "claude-1"
	f.code(CodeSessionRequired, "task", "add", "--title", "t")
	f.code(CodeSessionRequired, "task", "list")
}

// @assurance:evidence cli-now
func Test登録と完了と中止には操作の時点の日時を秒で渡しJSONはUTCのRFC3339で返す(t *testing.T) {
	f := newFixture(t)
	f.now = time.Date(2026, 10, 27, 21, 34, 56, 789_000_000, time.FixedZone("JST", 9*3600))
	tk := f.add("t")
	if tk["created_at"] != "2026-10-27T12:34:56Z" {
		t.Fatalf("作成日時が %v", tk["created_at"])
	}
	f.now = f.now.Add(65 * time.Second)
	f.ok("task", "done", tk["id"].(string), "--reason", "r")
	evs := f.show(tk["id"].(string))["events"].([]any)
	if at := evs[1].(map[string]any)["at"]; at != "2026-10-27T12:36:01Z" {
		t.Fatalf("完了の日時が %v", at)
	}
	f.now = f.now.Add(time.Second)
	id2 := f.add("u")["id"].(string)
	f.now = f.now.Add(time.Second)
	f.ok("task", "cancel", id2, "--reason", "r")
	if at := f.show(id2)["events"].([]any)[1].(map[string]any)["at"]; at != "2026-10-27T12:36:03Z" {
		t.Fatalf("中止の日時が %v", at)
	}
}

// @assurance:evidence cli-model-error-code
func Testモデルのエラーはすべて同じ名前のエラーコードに写りIDの重なりは写らない(t *testing.T) {
	want := map[task.Failure]Code{
		task.SessionRequired: CodeSessionRequired, task.TitleRequired: CodeTitleRequired, task.ReasonRequired: CodeReasonRequired,
		task.NotFound: CodeNotFound, task.AlreadyFinished: CodeAlreadyFinished, task.IDTaken: CodeInternalError,
	}
	if len(want) != len(task.Failures) {
		t.Fatalf("判定のエラーの種類 %d 個のうち、%d 個しか確かめていない", len(task.Failures), len(want))
	}
	for _, f := range task.Failures {
		if got := codeOf(f); got != want[f] {
			t.Errorf("%v は %q に写った（%q のはず）", f, got, want[f])
		}
	}
}

// @assurance:evidence cli-input-checked-before-open
func Test主体とタイトルと理由が空白だけならデータのファイルを開かずにエラーを返す(t *testing.T) {
	var spaces []string
	for r := rune(0); r <= 0x3000; r++ {
		if task.IsWhiteSpace(r) {
			spaces = append(spaces, string(r))
		}
	}
	f := newFixture(t)
	// データのファイルの場所をディレクトリにして、開けば internal_error になるようにする
	if err := os.MkdirAll(f.data, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, sp := range append(spaces, strings.Join(spaces, ""), "") {
		f.env["QUENTA_SESSION"] = sp
		f.code(CodeSessionRequired, "task", "add", "--title", "t")
		f.code(CodeSessionRequired, "task", "list")
		f.code(CodeSessionRequired, "task", "show", "00000001")
		f.env["QUENTA_SESSION"] = "s"
		f.code(CodeTitleRequired, "task", "add", "--title", sp)
		f.code(CodeReasonRequired, "task", "done", "00000001", "--reason", sp)
		f.code(CodeReasonRequired, "task", "cancel", "00000001", "--reason", sp)
	}
	// 空白でない文字を 1 つでも含み、ほかのエラーの条件にも当てはまらなければ、開く（開けないので internal_error）
	f.code(CodeInternalError, "task", "add", "--title", " x ")
	f.code(CodeInternalError, "task", "done", "00000001", "--reason", "\u3000r")
}

// @assurance:evidence cli-invalid-utf8
func Test正しくないUTF8の引数とQUENTA_SESSIONはinvalid_argumentで拒む(t *testing.T) {
	bad := []string{"\xff", "a\xc3", "\xed\xa0\x80"} // 正しくないバイト、途中で切れた文字、サロゲートを表すバイト列
	f := newFixture(t)
	id := f.add("t")["id"].(string)
	for _, b := range bad {
		for name, args := range map[string][]string{
			"--title":  {"task", "add", "--title", b},
			"--body":   {"task", "add", "--title", "t", "--body", b},
			"--reason": {"task", "done", id, "--reason", b},
			"<ID>":     {"task", "show", b},
		} {
			if m := f.code(CodeInvalidArgument, args...); !strings.Contains(m, name) {
				t.Errorf("message に %s がない: %s", name, m)
			}
		}
		f.env["QUENTA_SESSION"] = "s" + b
		if m := f.code(CodeInvalidArgument, "task", "list"); !strings.Contains(m, "QUENTA_SESSION") {
			t.Errorf("message に QUENTA_SESSION がない: %s", m)
		}
		f.env["QUENTA_SESSION"] = "s1"
	}
	if st := f.ok("task", "list", "--all")["tasks"].([]any); len(st) != 1 {
		t.Fatalf("拒んだのにタスクが %d 個", len(st))
	}
}

// @assurance:evidence cli-args-as-given
func Test引数の値は前後の空白も改行も含めてそのまま渡る(t *testing.T) {
	f := newFixture(t)
	for _, v := range []string{"  前後  ", "\n改行\n", "\t\u3000混ぜる\u2028", "x\r\n"} {
		id := f.add(v, "--body", v)["id"].(string)
		f.ok("task", "done", id, "--reason", v)
		got := f.show(id)
		tk := got["task"].(map[string]any)
		if tk["title"] != v || tk["body"] != v || got["events"].([]any)[1].(map[string]any)["reason"] != v {
			t.Fatalf("値 %q が変わった: %v", v, got)
		}
	}
	f.code(CodeNotFound, "task", "show", " 00000001")
}

// @assurance:evidence cli-list-output
func Test一覧のJSONは一覧の判定が返したタスクをその順で漏れなくそれだけ並べる(t *testing.T) {
	f := newFixture(t)
	for i := range 12 {
		if i%3 == 0 {
			f.now = f.now.Add(time.Second)
		}
		id := f.add("t" + strconv.Itoa(i))["id"].(string)
		if i%4 == 0 {
			f.ok("task", "done", id, "--reason", "r")
		}
	}
	db, err := sql.Open("sqlite", "file:"+f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := oracle.ReadState(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, all := range []bool{false, true} {
		want, err := task.ListDecide("s1", all, st.Tasks)
		if err != nil {
			t.Fatal(err)
		}
		args := []string{"task", "list"}
		if all {
			args = append(args, "--all")
		}
		var got []task.Task
		for _, x := range f.ok(args...)["tasks"].([]any) {
			m := x.(map[string]any)
			ts, _ := time.Parse(time.RFC3339, m["created_at"].(string))
			got = append(got, task.Task{ID: m["id"].(string), Title: m["title"].(string), Body: m["body"].(string), Status: task.Status(m["status"].(string)), CreatedAt: ts.Unix()})
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("--all=%v: JSON %v、判定 %v", all, got, want)
		}
	}
}

// @assurance:evidence cli-returns-changed-task
func Test登録と完了と中止は書き込んだ後のタスクを返す(t *testing.T) {
	f := newFixture(t)
	a := f.add("a", "--body", "b")
	if a["status"] != "todo" || a["title"] != "a" || a["body"] != "b" {
		t.Fatalf("登録の出力: %v", a)
	}
	d := f.ok("task", "done", a["id"].(string), "--reason", "r")["task"].(map[string]any)
	if d["status"] != "done" || d["id"] != a["id"] || d["created_at"] != a["created_at"] {
		t.Fatalf("完了の出力: %v", d)
	}
	c := f.ok("task", "cancel", f.add("c")["id"].(string), "--reason", "r")["task"].(map[string]any)
	if c["status"] != "canceled" {
		t.Fatalf("中止の出力: %v", c)
	}
}

func keys(m map[string]any) []string {
	var r []string
	for k := range m {
		r = append(r, k)
	}
	slices.Sort(r)
	return r
}

// @assurance:evidence task-output-fields
func Testタスクとイベントの出力は決まった項目を持つ(t *testing.T) {
	f := newFixture(t)
	taskKeys := []string{"body", "created_at", "id", "status", "title"}
	a := f.add("a")
	if !slices.Equal(keys(a), taskKeys) {
		t.Fatalf("登録のタスクの項目: %v", keys(a))
	}
	f.ok("task", "done", a["id"].(string), "--reason", "r")
	for _, args := range [][]string{{"task", "list", "--all"}} {
		for _, x := range f.ok(args...)["tasks"].([]any) {
			if !slices.Equal(keys(x.(map[string]any)), taskKeys) {
				t.Fatalf("一覧のタスクの項目: %v", keys(x.(map[string]any)))
			}
		}
	}
	s := f.show(a["id"].(string))
	if !slices.Equal(keys(s), []string{"events", "task"}) || !slices.Equal(keys(s["task"].(map[string]any)), taskKeys) {
		t.Fatalf("表示の項目: %v", s)
	}
	evs := s["events"].([]any)
	for _, e := range evs {
		if !slices.Equal(keys(e.(map[string]any)), []string{"actor", "at", "from", "reason", "to"}) {
			t.Fatalf("イベントの項目: %v", e)
		}
	}
	first := evs[0].(map[string]any)
	if first["from"] != nil || first["reason"] != "" {
		t.Fatalf("登録のイベントの変更前は null、理由は空のはず: %v", first)
	}
	_, out := f.run("task", "show", a["id"].(string))
	if !strings.Contains(out, `"from":null`) {
		t.Fatalf("変更前が null と書かれていない: %s", out)
	}
}

// @assurance:evidence invalid-argument
func Test引数の誤りはinvalid_argumentで何も変えない(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{
		{"task", "show"}, {"task", "done", "--reason", "r"}, {"task", "show", "a", "b"}, {"task", "list", "extra"},
		{"task", "add", "--title", "t", "--unknown"}, {"task", "frobnicate"}, {"frobnicate"}, {"task", "add", "--title", "t", "extra"},
	} {
		f.code(CodeInvalidArgument, args...)
	}
	if _, err := os.Stat(f.data); !os.IsNotExist(err) {
		t.Fatalf("引数の誤りなのにデータのファイルができた: %v", err)
	}
}

// @assurance:evidence error-precedence
func Test複数のエラーの条件が当てはまるときは決めた順の先のものを返す(t *testing.T) {
	f := newFixture(t)
	id := f.add("t")["id"].(string)
	f.ok("task", "done", id, "--reason", "r")
	// --help はほかの条件より先
	f.env["QUENTA_SESSION"] = ""
	if code, out := f.run("task", "add", "--unknown", "--help"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Fatalf("--help: %d %s", code, out)
	}
	// session_required は invalid_argument より先
	f.code(CodeSessionRequired, "task", "add", "--title", "", "--unknown")
	f.code(CodeSessionRequired, "task", "show")
	f.env["QUENTA_SESSION"] = "s1"
	// invalid_argument は title_required・reason_required より先
	f.code(CodeInvalidArgument, "task", "add", "--title", "", "--unknown")
	f.code(CodeInvalidArgument, "task", "done", "--reason", "")
	// reason_required は not_found・already_finished より先
	f.code(CodeReasonRequired, "task", "done", "ffffffff", "--reason", "")
	f.code(CodeReasonRequired, "task", "cancel", id, "--reason", "")
	// title_required・reason_required は unsupported_data_version より先。unsupported_data_version は not_found より先
	g := newFixture(t)
	if err := os.MkdirAll(filepath.Dir(g.data), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+g.data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 9999"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	g.code(CodeTitleRequired, "task", "add", "--title", " ")
	g.code(CodeReasonRequired, "task", "done", "ffffffff", "--reason", " ")
	g.code(CodeUnsupportedDataVersion, "task", "done", "ffffffff", "--reason", "r")
	// internal_error は not_found より先
	h := newFixture(t)
	if err := os.MkdirAll(h.data, 0o700); err != nil {
		t.Fatal(err)
	}
	h.code(CodeInternalError, "task", "done", "ffffffff", "--reason", "r")
	// not_found と already_finished
	f.code(CodeNotFound, "task", "done", "ffffffff", "--reason", "r")
	f.code(CodeAlreadyFinished, "task", "cancel", id, "--reason", "r")
}

// @assurance:evidence cli-error-shape
func Test失敗のJSONはerrorのcodeとmessageだけを持ちinvalid_argumentは誤った引数の名前を含む(t *testing.T) {
	f := newFixture(t)
	id := f.add("t")["id"].(string)
	f.ok("task", "done", id, "--reason", "r")
	f.code(CodeTitleRequired, "task", "add", "--title", "")
	f.code(CodeReasonRequired, "task", "done", id, "--reason", "")
	f.code(CodeNotFound, "task", "show", "ffffffff")
	f.code(CodeAlreadyFinished, "task", "done", id, "--reason", "r")
	for arg, args := range map[string][]string{
		"--unknown": {"task", "add", "--title", "t", "--unknown"},
		"extra":     {"task", "list", "extra"},
		"frob":      {"task", "frob"},
	} {
		if m := f.code(CodeInvalidArgument, args...); !strings.Contains(m, arg) {
			t.Errorf("invalid_argument の message に %q がない: %s", arg, m)
		}
	}
	f.env["QUENTA_SESSION"] = ""
	f.code(CodeSessionRequired, "task", "list")
}

// @assurance:evidence output-json
func Test成功はJSONで終了コード0失敗はJSONで0以外ヘルプはテキストで0(t *testing.T) {
	f := newFixture(t)
	f.ok("task", "add", "--title", "t")
	f.ok("task", "list")
	f.fail("task", "add", "--title", "")
	for _, args := range [][]string{{"--help"}, {"-h"}, {}, {"task"}, {"task", "-h"}, {"task", "add", "--help"}} {
		code, out := f.run(args...)
		if code != 0 || !strings.Contains(out, "Usage:") || strings.HasPrefix(out, "{") {
			t.Fatalf("%v: 終了コード %d、出力 %q", args, code, out)
		}
	}
}

// buildQuenta は、quenta をビルドして場所を返す
func buildQuenta(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "quenta")
	out, err := exec.Command("go", "build", "-o", bin, "github.com/TadahiroYamamura/quenta/cmd/quenta").CombinedOutput()
	if err != nil {
		t.Fatalf("ビルドできない: %v\n%s", err, out)
	}
	return bin
}

// argLimit は、その OS で 1 つの引数として渡せる長さの、手前の長さ
func argLimit(t *testing.T, env []string) int {
	t.Helper()
	switch runtime.GOOS {
	case "linux":
		return 131072 - 1024 // 1 つの引数の上限（MAX_ARG_STRLEN）の手前
	case "darwin":
		out, err := exec.Command("getconf", "ARG_MAX").Output()
		if err != nil {
			t.Fatal(err)
		}
		max, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			t.Fatal(err)
		}
		used := 0
		for _, e := range env {
			used += len(e) + 1 + 8
		}
		return max - used - 64*1024 // 引数と環境変数の合計の上限（ARG_MAX）から、環境の分と余裕を引いた長さ
	}
	t.Skipf("%s は対象の OS でない", runtime.GOOS)
	return 0
}

// @assurance:evidence cli-no-length-limit
func Testタイトルと本文と理由は長さを理由に拒まずOSの引数の上限の手前の長さでもそのまま返る(t *testing.T) {
	bin := buildQuenta(t)
	dir := t.TempDir()
	env := []string{"QUENTA_SESSION=s1", "XDG_DATA_HOME=" + dir, "PATH=" + os.Getenv("PATH")}
	n := argLimit(t, env)
	long := strings.Repeat("長", n/3) // 「長」は UTF-8 で 3 バイト
	run := func(args ...string) map[string]any {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v...: %v %s", args[:2], err, out)
		}
		var v map[string]any
		if err := json.Unmarshal(out, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	a := run("task", "add", "--title", long)["task"].(map[string]any)
	b := run("task", "add", "--title", "t", "--body", long)["task"].(map[string]any)
	run("task", "done", a["id"].(string), "--reason", long)
	c := run("task", "add", "--title", "t")["task"].(map[string]any)
	run("task", "cancel", c["id"].(string), "--reason", long)
	if got := run("task", "show", a["id"].(string)); got["task"].(map[string]any)["title"] != long || got["events"].([]any)[1].(map[string]any)["reason"] != long {
		t.Fatal("長いタイトルか、完了の理由が変わった")
	}
	if got := run("task", "show", b["id"].(string)); got["task"].(map[string]any)["body"] != long {
		t.Fatal("長い本文が変わった")
	}
	if got := run("task", "show", c["id"].(string)); got["events"].([]any)[1].(map[string]any)["reason"] != long {
		t.Fatal("長い中止の理由が変わった")
	}
}

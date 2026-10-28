package cli

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TadahiroYamamura/quenta/internal/oracle"
	_dafny "github.com/TadahiroYamamura/quenta/internal/spec/dafny"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

type fixture struct {
	t      *testing.T
	env    map[string]string
	now    time.Time
	data   string
	random io.Reader
}

func newFixture(t *testing.T) *fixture {
	dir := t.TempDir()
	return &fixture{t: t, env: map[string]string{"QUENTA_SESSION": "s1", "XDG_DATA_HOME": dir, "HOME": filepath.Join(dir, "home")}, now: time.Unix(1_700_000_000, 0), data: filepath.Join(dir, "quenta", "quenta.db"), random: rand.Reader}
}

// run は、quenta を動かし、終了コードと、出力（成功なら標準出力、失敗なら標準エラー）を返す。成功で標準エラーに、失敗で標準出力に何か書けば落とす
func (f *fixture) run(args ...string) (int, string) {
	var out, errOut bytes.Buffer
	code := Run(args, Env{Getenv: func(k string) string { return f.env[k] }, Stdout: &out, Stderr: &errOut, Now: func() time.Time { return f.now }, Random: f.random})
	if code == 0 {
		if errOut.Len() > 0 {
			f.t.Fatalf("%v: 成功したのに標準エラーに書いた: %q", args, errOut.String())
		}
		return code, out.String()
	}
	if out.Len() > 0 {
		f.t.Fatalf("%v: 失敗したのに標準出力に書いた: %q", args, out.String())
	}
	return code, errOut.String()
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
	c, cok := v["error"]["code"].(string)
	m, mok := v["error"]["message"].(string)
	if !cok || !mok {
		f.t.Fatalf("%v: error.code か error.message が文字列でない: %s", args, out)
	}
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
	return obj(f.t, f.ok(append([]string{"task", "add", "--title", title}, extra...)...)["task"])
}

func (f *fixture) show(id string) map[string]any { return f.ok("task", "show", id) }

// @assurance:evidence cli-body-omitted
func Test本文のフラグを省くか値なしで最後に置くと本文は空になる(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{{"task", "add", "--title", "t"}, {"task", "add", "--title", "t", "--body"}} {
		if b := obj(t, f.ok(args...)["task"])["body"]; b != "" {
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
	id := str(t, f.add("t")["id"])
	for _, cmd := range []string{"done", "cancel"} {
		f.code(CodeReasonRequired, "task", cmd, id)
		f.code(CodeReasonRequired, "task", cmd, id, "--reason")
	}
	if st := obj(t, f.show(id)["task"])["status"]; st != "todo" {
		t.Fatalf("状態が %v", st)
	}
}

// @assurance:evidence cli-list-all-flag
func Test一覧はallの有無で終わったタスクを含めるかが変わる(t *testing.T) {
	f := newFixture(t)
	a := str(t, f.add("a")["id"])
	f.now = f.now.Add(time.Second)
	b := str(t, f.add("b")["id"])
	f.ok("task", "done", b, "--reason", "r")
	ids := func(v map[string]any) []string {
		var r []string
		for _, x := range arr(t, v["tasks"]) {
			r = append(r, str(t, obj(t, x)["id"]))
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
		id := str(t, f.add("t")["id"])
		ev := obj(t, arr(t, f.show(id)["events"])[0])
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
	f.ok("task", "done", str(t, tk["id"]), "--reason", "r")
	evs := arr(t, f.show(str(t, tk["id"]))["events"])
	if at := obj(t, evs[1])["at"]; at != "2026-10-27T12:36:01Z" {
		t.Fatalf("完了の日時が %v", at)
	}
	f.now = f.now.Add(time.Second)
	id2 := str(t, f.add("u")["id"])
	f.now = f.now.Add(time.Second)
	f.ok("task", "cancel", id2, "--reason", "r")
	if at := obj(t, arr(t, f.show(id2)["events"])[1])["at"]; at != "2026-10-27T12:36:03Z" {
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
		if oracle.M.IsWhiteSpace(_dafny.CodePoint(r)) { // 仕様が空白とする文字。実装の判定から作らない
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
// @assurance:evidence cli-error-shape
func Test正しくないUTF8の引数とQUENTA_SESSIONはinvalid_argumentで拒む(t *testing.T) {
	bad := []string{"\xff", "a\xc3", "\xed\xa0\x80"} // 正しくないバイト、途中で切れた文字、サロゲートを表すバイト列
	f := newFixture(t)
	id := str(t, f.add("t")["id"])
	before := f.state()
	for _, b := range bad {
		for _, c := range []struct {
			name string
			args []string
		}{
			{"--title", []string{"task", "add", "--title", b}},
			{"--body", []string{"task", "add", "--title", "t", "--body", b}},
			{"--reason", []string{"task", "done", id, "--reason", b}},
			{"--reason", []string{"task", "cancel", id, "--reason", b}},
			{"<ID>", []string{"task", "show", b}},
			{"<ID>", []string{"task", "done", b, "--reason", "r"}},
			{"<ID>", []string{"task", "cancel", b, "--reason", "r"}},
		} {
			if m := f.code(CodeInvalidArgument, c.args...); !strings.Contains(m, c.name) {
				t.Errorf("%v: message に %s がない: %s", c.args, c.name, m)
			}
		}
		f.env["QUENTA_SESSION"] = "s" + b
		for _, args := range [][]string{{"task", "list"}, {"task", "add", "--title", "t"}, {"task", "done", id, "--reason", "r"}, {"task", "cancel", id, "--reason", "r"}, {"task", "show", id}} {
			if m := f.code(CodeInvalidArgument, args...); !strings.Contains(m, "QUENTA_SESSION") {
				t.Errorf("%v: message に QUENTA_SESSION がない: %s", args, m)
			}
		}
		f.env["QUENTA_SESSION"] = "s1"
	}
	// タスクもイベントも変わっていない
	if after := f.state(); !reflect.DeepEqual(after, before) {
		t.Fatalf("拒んだのに中身が変わった\n前: %+v\n後: %+v", before, after)
	}
}

// @assurance:evidence cli-args-as-given
func Test引数の値は前後の空白も改行も含めてそのまま渡る(t *testing.T) {
	f := newFixture(t)
	for _, v := range []string{"  前後  ", "\n改行\n", "\t\u3000混ぜる\u2028", "x\r\n"} {
		id := str(t, f.add(v, "--body", v)["id"])
		f.ok("task", "done", id, "--reason", v)
		got := f.show(id)
		tk := obj(t, got["task"])
		if tk["title"] != v || tk["body"] != v || obj(t, arr(t, got["events"])[1])["reason"] != v {
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
		id := str(t, f.add("t" + strconv.Itoa(i))["id"])
		if i%4 == 0 {
			f.ok("task", "done", id, "--reason", "r")
		}
	}
	db, err := sql.Open("sqlite", "file:"+f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer oracle.MustClose(t, db)
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
		for _, x := range arr(t, f.ok(args...)["tasks"]) {
			m := obj(t, x)
			ts, err := time.Parse(time.RFC3339, str(t, m["created_at"]))
			if err != nil {
				t.Fatalf("作成日時が RFC 3339 でない: %v", err)
			}
			got = append(got, task.Task{ID: str(t, m["id"]), Title: str(t, m["title"]), Body: str(t, m["body"]), Status: task.Status(str(t, m["status"])), CreatedAt: ts.Unix()})
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
	d := obj(t, f.ok("task", "done", str(t, a["id"]), "--reason", "r")["task"])
	if d["status"] != "done" || d["id"] != a["id"] || d["created_at"] != a["created_at"] {
		t.Fatalf("完了の出力: %v", d)
	}
	c := obj(t, f.ok("task", "cancel", str(t, f.add("c")["id"]), "--reason", "r")["task"])
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
	if d := obj(t, f.ok("task", "done", str(t, a["id"]), "--reason", "r")["task"]); !slices.Equal(keys(d), taskKeys) {
		t.Fatalf("完了のタスクの項目: %v", keys(d))
	}
	if c := obj(t, f.ok("task", "cancel", str(t, f.add("c")["id"]), "--reason", "r")["task"]); !slices.Equal(keys(c), taskKeys) {
		t.Fatalf("中止のタスクの項目: %v", keys(c))
	}
	for _, args := range [][]string{{"task", "list", "--all"}} {
		for _, x := range arr(t, f.ok(args...)["tasks"]) {
			if !slices.Equal(keys(obj(t, x)), taskKeys) {
				t.Fatalf("一覧のタスクの項目: %v", keys(obj(t, x)))
			}
		}
	}
	s := f.show(str(t, a["id"]))
	if !slices.Equal(keys(s), []string{"events", "task"}) || !slices.Equal(keys(obj(t, s["task"])), taskKeys) {
		t.Fatalf("表示の項目: %v", s)
	}
	evs := arr(t, s["events"])
	for _, e := range evs {
		if !slices.Equal(keys(obj(t, e)), []string{"actor", "at", "from", "reason", "to"}) {
			t.Fatalf("イベントの項目: %v", e)
		}
	}
	first := obj(t, evs[0])
	if first["from"] != nil || first["reason"] != "" {
		t.Fatalf("登録のイベントの変更前は null、理由は空のはず: %v", first)
	}
	_, out := f.run("task", "show", str(t, a["id"]))
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
	id := str(t, f.add("t")["id"])
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
	// session_required は title_required・reason_required より先（ふだん通る経路で、両方が当てはまる入力）
	f.env["QUENTA_SESSION"] = " "
	f.code(CodeSessionRequired, "task", "add", "--title", " ")
	f.code(CodeSessionRequired, "task", "done", id, "--reason", " ")
	f.code(CodeSessionRequired, "task", "cancel", "ffffffff", "--reason", " ")
	f.env["QUENTA_SESSION"] = "s1"
	// invalid_argument は title_required・reason_required より先
	f.code(CodeInvalidArgument, "task", "add", "--title", "", "--unknown")
	f.code(CodeInvalidArgument, "task", "done", "--reason", "")
	f.code(CodeInvalidArgument, "task", "add", "--title", "", "--body", "\xff")
	f.code(CodeInvalidArgument, "task", "done", "\xff", "--reason", "")
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
	oracle.MustClose(t, raw)
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
	id := str(t, f.add("t")["id"])
	f.ok("task", "done", id, "--reason", "r")
	f.code(CodeTitleRequired, "task", "add", "--title", "")
	f.code(CodeReasonRequired, "task", "done", id, "--reason", "")
	f.code(CodeNotFound, "task", "show", "ffffffff")
	f.code(CodeAlreadyFinished, "task", "done", id, "--reason", "r")
	for arg, args := range map[string][]string{
		"--unknown": {"task", "add", "--title", "t", "--unknown"},
		"extra":     {"task", "list", "extra"},
		"frob":      {"task", "frob"},
		"<ID>":      {"task", "show"},
	} {
		if m := f.code(CodeInvalidArgument, args...); !strings.Contains(m, arg) {
			t.Errorf("invalid_argument の message に %q がない: %s", arg, m)
		}
	}
	// ID の引数がない・多いときも、message は <ID> を含む（#25）
	for _, args := range [][]string{{"task", "show"}, {"task", "show", "a", "b"}, {"task", "done", "--reason", "r"}, {"task", "cancel", "a", "b", "--reason", "r"}} {
		if m := f.code(CodeInvalidArgument, args...); !strings.Contains(m, "<ID>") {
			t.Errorf("%v: invalid_argument の message に <ID> がない: %s", args, m)
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
	// --help は、指定したサブコマンドの使い方を返す。知らないサブコマンドなら、quenta の使い方を返す
	if _, out := f.run("task", "add", "--help"); !strings.Contains(out, "--title") {
		t.Fatalf("task add --help が登録の使い方でない: %q", out)
	}
	if code, out := f.run("frobnicate", "--help"); code != 0 || !strings.Contains(out, "task") {
		t.Fatalf("知らないサブコマンドの --help: %d %q", code, out)
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
	a := obj(t, run("task", "add", "--title", long)["task"])
	b := obj(t, run("task", "add", "--title", "t", "--body", long)["task"])
	run("task", "done", str(t, a["id"]), "--reason", long)
	c := obj(t, run("task", "add", "--title", "t")["task"])
	run("task", "cancel", str(t, c["id"]), "--reason", long)
	if got := run("task", "show", str(t, a["id"])); obj(t, got["task"])["title"] != long || obj(t, arr(t, got["events"])[1])["reason"] != long {
		t.Fatal("長いタイトルか、完了の理由が変わった")
	}
	if got := run("task", "show", str(t, b["id"])); obj(t, got["task"])["body"] != long {
		t.Fatal("長い本文が変わった")
	}
	if got := run("task", "show", str(t, c["id"])); obj(t, arr(t, got["events"])[1])["reason"] != long {
		t.Fatal("長い中止の理由が変わった")
	}
}

// @assurance:evidence internal-error
func TestXDG_DATA_HOMEが使えなければHOMEの下に置きHOMEもなければinternal_errorを返す(t *testing.T) {
	for _, x := range []string{"", "relative/dir"} {
		f := newFixture(t)
		f.env["XDG_DATA_HOME"] = x
		f.ok("task", "list")
		if _, err := os.Stat(filepath.Join(f.env["HOME"], ".local", "share", "quenta", "quenta.db")); err != nil {
			t.Fatalf("XDG_DATA_HOME=%q: HOME の下にデータのファイルがない: %v", x, err)
		}
		f.env["HOME"] = ""
		f.code(CodeInternalError, "task", "list")
		// HOME が絶対パスでなければ、HOME がないのと同じ（#27）。カレントディレクトリに何も作らない
		f.env["HOME"] = "relhome"
		f.code(CodeInternalError, "task", "list")
		if _, err := os.Stat("relhome"); !os.IsNotExist(err) {
			t.Fatalf("カレントディレクトリに relhome ができた: %v", err)
		}
	}
}

func (f *fixture) state() oracle.State {
	f.t.Helper()
	db, err := sql.Open("sqlite", "file:"+f.data)
	if err != nil {
		f.t.Fatal(err)
	}
	defer oracle.MustClose(f.t, db)
	st, err := oracle.ReadState(db)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

// @assurance:evidence cli-error-shape
// @assurance:evidence task-add-retries-on-id-collision
func TestIDが重なり続けるとid_conflictを返し何も登録しない(t *testing.T) {
	f := newFixture(t)
	f.random = bytes.NewReader(bytes.Repeat([]byte{0, 0, 0, 1}, 100))
	f.add("1 つ目")
	f.code(CodeIDConflict, "task", "add", "--title", "2 つ目")
	if st := f.state(); len(st.Tasks) != 1 || len(st.Events) != 1 {
		t.Fatalf("タスク %d 個、イベント %d 件", len(st.Tasks), len(st.Events))
	}
}

// @assurance:evidence cli-error-shape
// @assurance:evidence db-busy
func Testほかのセッションの書き込みを待ちきれなければbusy_timeoutを返し何も変えない(t *testing.T) {
	f := newFixture(t)
	f.add("t")
	db, err := sql.Open("sqlite", "file:"+f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer oracle.MustClose(t, db)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer oracle.MustClose(t, conn)
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	f.code(CodeBusyTimeout, "task", "add", "--title", "u")
	if _, err := conn.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if st := f.state(); len(st.Tasks) != 1 {
		t.Fatalf("タスクが %d 個", len(st.Tasks))
	}
}

// @assurance:evidence db-constraint-violation
func Test書き込みをDBの制約が拒むとinternal_errorを返しタスクもイベントも変わらない(t *testing.T) {
	f := newFixture(t)
	id := str(t, f.add("t")["id"])
	// テストの中でだけ、イベントの書き込みを拒むトリガーを足し、実装の書き込みを DB に拒ませる
	db, err := sql.Open("sqlite", "file:"+f.data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER test_reject_events BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT, 'test_reject_events'); END"); err != nil {
		t.Fatal(err)
	}
	oracle.MustClose(t, db)
	before := f.state()
	f.code(CodeInternalError, "task", "add", "--title", "u")
	f.code(CodeInternalError, "task", "done", id, "--reason", "r")
	if after := f.state(); !reflect.DeepEqual(after, before) {
		t.Fatalf("中身が変わった\n前: %+v\n後: %+v", before, after)
	}
}

// @assurance:evidence task-id-format
func Test本番の乱数で作ったIDは16進の小文字8桁(t *testing.T) {
	f := newFixture(t)
	f.random = DefaultEnv(nil, nil, nil).Random // 本番と同じ乱数の元
	re := regexp.MustCompile(`^[0-9a-f]{8}$`)
	for range 20 {
		if id := str(t, f.add("t")["id"]); !re.MatchString(id) {
			t.Fatalf("ID の形が違う: %q", id)
		}
	}
}

// obj・arr・str は、JSON の値を、型を確かめて取り出す。型が違えば、値を書いて落とす
func obj(t testing.TB, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("JSON のオブジェクトでない: %#v", v)
	}
	return m
}

func arr(t testing.TB, v any) []any {
	t.Helper()
	a, ok := v.([]any)
	if !ok {
		t.Fatalf("JSON の配列でない: %#v", v)
	}
	return a
}

func str(t testing.TB, v any) string {
	t.Helper()
	x, ok := v.(string)
	if !ok {
		t.Fatalf("JSON の文字列でない: %#v", v)
	}
	return x
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// @assurance:evidence output-json
func Test結果を標準出力に書けなければ成功にせずinternal_errorを標準エラーに書く(t *testing.T) {
	f := newFixture(t)
	var errOut bytes.Buffer
	env := Env{Getenv: func(k string) string { return f.env[k] }, Stdout: failingWriter{}, Stderr: &errOut, Now: func() time.Time { return f.now }, Random: f.random}
	for _, args := range [][]string{{"task", "add", "--title", "t"}, {"task", "list"}, {"--help"}, {"task"}} {
		errOut.Reset()
		if code := Run(args, env); code == 0 {
			t.Fatalf("%v: 標準出力に書けないのに終了コードが 0", args)
		}
		if args[0] == "task" && len(args) > 1 && !strings.Contains(errOut.String(), `"code":"internal_error"`) {
			t.Fatalf("%v: 標準エラーに internal_error がない: %q", args, errOut.String())
		}
	}
}

// @assurance:evidence invalid-argument
// @assurance:evidence error-precedence
func Test値なしで最後に置いたフラグはそのフラグを持つコマンドでだけ省いたのと同じに扱う(t *testing.T) {
	f := newFixture(t)
	id := str(t, f.add("t")["id"])
	before := f.state()
	// そのフラグを持たないコマンドや、-- の後に置いたものは、引数の誤り（#26）
	for _, args := range [][]string{
		{"task", "done", id, "--reason", "r", "--title"},
		{"task", "cancel", id, "--reason", "r", "--body"},
		{"task", "list", "--title"},
		{"task", "show", id, "--body"},
		{"task", "add", "--title", "x", "--", "--title"},
		{"task", "done", id, "--title"},
	} {
		f.code(CodeInvalidArgument, args...)
	}
	if after := f.state(); !reflect.DeepEqual(after, before) {
		t.Fatalf("引数の誤りなのに中身が変わった\n前: %+v\n後: %+v", before, after)
	}
	// そのフラグを持つコマンドでは、省いたのと同じ
	f.code(CodeTitleRequired, "task", "add", "--body", "b", "--title")
	f.code(CodeReasonRequired, "task", "done", id, "--reason")
	if b := obj(t, f.ok("task", "add", "--title", "t", "--body")["task"])["body"]; b != "" {
		t.Fatalf("本文が %q", b)
	}
}

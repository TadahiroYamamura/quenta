package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/TadahiroYamamura/quenta/internal/app"
)

type result struct {
	code           int
	stdout, stderr string
}

type harness struct {
	t   *testing.T
	env map[string]string
	ids []string
	now time.Time
}

func newHarness(t *testing.T) *harness {
	return &harness{
		t:   t,
		env: map[string]string{"XDG_DATA_HOME": t.TempDir(), "QUENTA_SESSION": "s1"},
		now: time.Date(2026, 10, 25, 1, 2, 3, 456_000_000, time.UTC),
	}
}

func (h *harness) run(args ...string) result {
	h.t.Helper()
	var stdout, stderr bytes.Buffer
	opts := []app.Option{app.WithClock(func() time.Time { return h.now })}
	if len(h.ids) > 0 {
		opts = append(opts, app.WithIDGenerator(func() (string, error) {
			id := h.ids[0]
			h.ids = h.ids[1:]
			return id, nil
		}))
	}
	code := Run(context.Background(), args, func(k string) string { return h.env[k] }, &stdout, &stderr, opts...)
	return result{code, stdout.String(), stderr.String()}
}

// keys は、JSON のオブジェクトのキーを並べて返す（出力の項目が固定されていることを確かめるため）
func keys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("オブジェクトでない: %s", raw)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func decode(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("JSON でない: %q", s)
	}
	return m
}

func assertError(t *testing.T, r result, code string) {
	t.Helper()
	if r.code != 1 {
		t.Errorf("終了コード = %d, want 1", r.code)
	}
	if r.stdout != "" {
		t.Errorf("エラーなのに標準出力に書いた: %q", r.stdout)
	}
	m := decode(t, r.stderr)
	if got := keys(t, []byte(r.stderr)); !reflect.DeepEqual(got, []string{"error"}) {
		t.Errorf("エラーの JSON のキー = %v", got)
	}
	if got := keys(t, m["error"]); !reflect.DeepEqual(got, []string{"code", "message"}) {
		t.Errorf("error のキー = %v", got)
	}
	var e struct{ Code, Message string }
	json.Unmarshal(m["error"], &e)
	if e.Code != code {
		t.Errorf("エラーコード = %q, want %q (message=%q)", e.Code, code, e.Message)
	}
	if e.Message == "" {
		t.Error("message が空")
	}
}

func assertOK(t *testing.T, r result) map[string]json.RawMessage {
	t.Helper()
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("失敗した: code=%d stderr=%q", r.code, r.stderr)
	}
	if strings.Count(r.stdout, "\n") != 1 || !strings.HasSuffix(r.stdout, "\n") {
		t.Errorf("出力が 1 行の JSON でない: %q", r.stdout)
	}
	return decode(t, r.stdout)
}

func Test登録はtaskの項目を固定の形のJSONで返し日時はUTCのRFC3339でミリ秒まで出す(t *testing.T) {
	h := newHarness(t)
	h.ids = []string{"0a1b2c3d"}
	m := assertOK(t, h.run("task", "add", "--title", "タイトル", "--body", "本文"))
	if got := keys(t, []byte(h.run("task", "show", "0a1b2c3d").stdout)); !reflect.DeepEqual(got, []string{"events", "task"}) {
		t.Errorf("show のキー = %v", got)
	}
	if got := keys(t, m["task"]); !reflect.DeepEqual(got, []string{"body", "created_at", "id", "status", "title"}) {
		t.Errorf("task のキー = %v", got)
	}
	var tk map[string]string
	json.Unmarshal(m["task"], &tk)
	want := map[string]string{"id": "0a1b2c3d", "title": "タイトル", "body": "本文", "status": "todo", "created_at": "2026-10-25T01:02:03.456Z"}
	if !reflect.DeepEqual(tk, want) {
		t.Errorf("got %v, want %v", tk, want)
	}
}

func Test日時はミリ秒が0でも3桁で出しローカルの時刻帯にかかわらずUTCで出す(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("JST", 9*3600))
	m := assertOK(t, h.run("task", "add", "--title", "t"))
	var tk map[string]string
	json.Unmarshal(m["task"], &tk)
	if tk["created_at"] != "2026-01-01T18:04:05.000Z" {
		t.Errorf("created_at = %q", tk["created_at"])
	}
}

func Test一覧はtasksに本文を含まない項目を並べ空なら空の配列を返す(t *testing.T) {
	h := newHarness(t)
	m := assertOK(t, h.run("task", "list"))
	if string(m["tasks"]) != "[]" {
		t.Errorf("空の一覧 = %s", m["tasks"])
	}
	h.ids = []string{"0a1b2c3d"}
	assertOK(t, h.run("task", "add", "--title", "t", "--body", "本文"))
	m = assertOK(t, h.run("task", "list"))
	var ts []json.RawMessage
	json.Unmarshal(m["tasks"], &ts)
	if len(ts) != 1 {
		t.Fatalf("tasks = %s", m["tasks"])
	}
	if got := keys(t, ts[0]); !reflect.DeepEqual(got, []string{"created_at", "id", "status", "title"}) {
		t.Errorf("一覧の項目のキー = %v", got)
	}
}

func Test一覧はallを付けたときだけ終わったタスクを含める(t *testing.T) {
	h := newHarness(t)
	h.ids = []string{"00000001", "00000002"}
	assertOK(t, h.run("task", "add", "--title", "a"))
	assertOK(t, h.run("task", "add", "--title", "b"))
	assertOK(t, h.run("task", "done", "00000001", "--reason", "r"))
	idsOf := func(r result) []string {
		var out struct{ Tasks []struct{ ID string } }
		json.Unmarshal([]byte(r.stdout), &out)
		var ids []string
		for _, t := range out.Tasks {
			ids = append(ids, t.ID)
		}
		return ids
	}
	if got := idsOf(h.run("task", "list")); !reflect.DeepEqual(got, []string{"00000002"}) {
		t.Errorf("list = %v", got)
	}
	if got := idsOf(h.run("task", "list", "--all")); !reflect.DeepEqual(got, []string{"00000001", "00000002"}) {
		t.Errorf("list --all = %v", got)
	}
}

func Test詳細はtaskとeventsを返し登録のイベントは変更前と理由がnullになる(t *testing.T) {
	h := newHarness(t)
	h.ids = []string{"0a1b2c3d"}
	assertOK(t, h.run("task", "add", "--title", "t"))
	h.now = h.now.Add(time.Second)
	h.env["QUENTA_SESSION"] = "s2"
	assertOK(t, h.run("task", "cancel", "0a1b2c3d", "--reason", "不要になった"))
	m := assertOK(t, h.run("task", "show", "0a1b2c3d"))
	var evs []json.RawMessage
	json.Unmarshal(m["events"], &evs)
	if len(evs) != 2 {
		t.Fatalf("events = %s", m["events"])
	}
	for _, e := range evs {
		if got := keys(t, e); !reflect.DeepEqual(got, []string{"actor", "at", "from", "reason", "to"}) {
			t.Errorf("イベントのキー = %v", got)
		}
	}
	if got, want := string(evs[0]), `{"actor":"s1","from":null,"to":"todo","reason":null,"at":"2026-10-25T01:02:03.456Z"}`; got != want {
		t.Errorf("登録のイベント = %s, want %s", got, want)
	}
	if got, want := string(evs[1]), `{"actor":"s2","from":"todo","to":"canceled","reason":"不要になった","at":"2026-10-25T01:02:04.456Z"}`; got != want {
		t.Errorf("中止のイベント = %s, want %s", got, want)
	}
}

func Test完了と中止は変えた後のtaskを返す(t *testing.T) {
	for _, c := range []struct{ cmd, status string }{{"done", "done"}, {"cancel", "canceled"}} {
		h := newHarness(t)
		h.ids = []string{"0a1b2c3d"}
		assertOK(t, h.run("task", "add", "--title", "t"))
		m := assertOK(t, h.run("task", c.cmd, "0a1b2c3d", "--reason", "r"))
		var tk map[string]string
		json.Unmarshal(m["task"], &tk)
		if tk["status"] != c.status || tk["id"] != "0a1b2c3d" {
			t.Errorf("%s: %v", c.cmd, tk)
		}
	}
}

func Test誤った入力にはエラーコードを標準エラー出力にJSONで返し終了コードは1になる(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
		code string
	}{
		{"タイトルを指定しない", []string{"task", "add"}, nil, "title_required"},
		{"タイトルが空白だけ", []string{"task", "add", "--title", " "}, nil, "title_required"},
		{"QUENTA_SESSION がない登録", []string{"task", "add", "--title", "t"}, map[string]string{"QUENTA_SESSION": ""}, "session_required"},
		{"QUENTA_SESSION がない完了", []string{"task", "done", "0a1b2c3d", "--reason", "r"}, map[string]string{"QUENTA_SESSION": ""}, "session_required"},
		{"理由を指定しない完了", []string{"task", "done", "0a1b2c3d"}, nil, "reason_required"},
		{"理由を指定しない中止", []string{"task", "cancel", "0a1b2c3d"}, nil, "reason_required"},
		{"存在しない ID の完了", []string{"task", "done", "ffffffff", "--reason", "r"}, nil, "not_found"},
		{"存在しない ID の詳細", []string{"task", "show", "ffffffff"}, nil, "not_found"},
		{"未知のフラグ", []string{"task", "add", "--title", "t", "--bad"}, nil, "invalid_argument"},
		{"未知のサブコマンド", []string{"task", "foo"}, nil, "invalid_argument"},
		{"未知のコマンド", []string{"foo"}, nil, "invalid_argument"},
		{"詳細に ID がない", []string{"task", "show"}, nil, "invalid_argument"},
		{"完了に ID が 2 つ", []string{"task", "done", "a", "b", "--reason", "r"}, nil, "invalid_argument"},
		{"一覧に余分な引数", []string{"task", "list", "x"}, nil, "invalid_argument"},
		{"登録に余分な引数", []string{"task", "add", "--title", "t", "x"}, nil, "invalid_argument"},
		{"フラグの値がない", []string{"task", "add", "--title"}, nil, "invalid_argument"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			h.ids = []string{"0a1b2c3d"}
			assertOK(t, h.run("task", "add", "--title", "既存"))
			for k, v := range c.env {
				h.env[k] = v
			}
			assertError(t, h.run(c.args...), c.code)
		})
	}
}

func Test終わったタスクを変えようとするとalready_finishedを返す(t *testing.T) {
	h := newHarness(t)
	h.ids = []string{"0a1b2c3d"}
	assertOK(t, h.run("task", "add", "--title", "t"))
	assertOK(t, h.run("task", "done", "0a1b2c3d", "--reason", "r"))
	assertError(t, h.run("task", "cancel", "0a1b2c3d", "--reason", "r"), "already_finished")
	assertError(t, h.run("task", "done", "0a1b2c3d", "--reason", "r"), "already_finished")
}

func TestデータベースのファイルをXDG_DATA_HOMEの下に作れなければinternalを返す(t *testing.T) {
	h := newHarness(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	h.env["XDG_DATA_HOME"] = blocker // ファイルなので、その下にディレクトリを作れない
	assertError(t, h.run("task", "list"), "internal")
}

func Testヘルプは成功しデータベースを作らない(t *testing.T) {
	for _, args := range [][]string{{}, {"--help"}, {"task"}, {"task", "add", "--help"}} {
		h := newHarness(t)
		r := h.run(args...)
		if r.code != 0 || !strings.Contains(r.stdout, "quenta") {
			t.Errorf("%v: code=%d stdout=%q stderr=%q", args, r.code, r.stdout, r.stderr)
		}
		entries, _ := os.ReadDir(h.env["XDG_DATA_HOME"])
		if len(entries) != 0 {
			t.Errorf("%v: データを作った: %v", args, entries)
		}
	}
}

func Test本文とタイトルはJSONの特殊文字やHTMLの記号を含んでもそのまま返す(t *testing.T) {
	h := newHarness(t)
	h.ids = []string{"0a1b2c3d"}
	title := `<a href="x">&</a> "引用符" \ 改行` + "\n" + "タブ\t🎉"
	m := assertOK(t, h.run("task", "add", "--title", title, "--body", title))
	var tk map[string]string
	json.Unmarshal(m["task"], &tk)
	if tk["title"] != title || tk["body"] != title {
		t.Errorf("got %q", tk["title"])
	}
	if out := h.run("task", "show", "0a1b2c3d").stdout; strings.Contains(out, "\\"+"u003c") || !strings.Contains(out, "<a href") {
		t.Error("HTML の記号をエスケープした")
	}
}

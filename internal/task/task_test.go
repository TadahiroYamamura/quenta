package task

import (
	"errors"
	"math"
	"testing"
	"unicode"
	"unicode/utf8"
)

func TestIsBlankはUnicodeの全文字でGoのunicode_IsSpaceと一致する(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		// サロゲートは Dafny の文字の型にも Go の文字列にも入らない
		if !utf8.ValidRune(r) {
			continue
		}
		if got, want := IsBlank(string(r)), unicode.IsSpace(r); got != want {
			t.Errorf("U+%04X: IsBlank=%v, unicode.IsSpace=%v", r, got, want)
		}
	}
}

func TestIsBlankは空文字列と空白だけの文字列を空とみなし文字を含めば空とみなさない(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{" ", true},
		{" \t\r\n　", true}, // 全角の空白を含む
		{"a", false},
		{"  a  ", false},
		{"\u200b", false}, // ゼロ幅スペースは空白ではない
	}
	for _, c := range cases {
		if got := IsBlank(c.in); got != c.want {
			t.Errorf("IsBlank(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestタスクはDafnyの値に変換して戻すと元と同じになる(t *testing.T) {
	statuses := []Status{StatusTodo, StatusDone, StatusCanceled}
	for _, s := range statuses {
		for _, text := range []string{"", "ascii", "日本語のタイトル", "絵文字🎉と\n改行と\"引用符\"", "\x00"} {
			for _, at := range []int64{0, 1, -1, math.MaxInt64, math.MinInt64} {
				in := Task{ID: "0a1b2c3d", Title: text, Body: text, Status: s, CreatedAt: at}
				st, err := toSpecTask(in)
				if err != nil {
					t.Fatal(err)
				}
				out, err := fromSpecTask(st)
				if err != nil {
					t.Fatal(err)
				}
				if out != in {
					t.Errorf("got %+v, want %+v", out, in)
				}
			}
		}
	}
}

func TestイベントはDafnyの値に変換して戻すと元と同じになる(t *testing.T) {
	todo, done := StatusTodo, StatusDone
	reason := "理由🎉"
	cases := []Event{
		{TaskID: "0a1b2c3d", Actor: "s1", From: nil, To: StatusTodo, Reason: nil, At: 5},
		{TaskID: "0a1b2c3d", Actor: "s1", From: &todo, To: StatusDone, Reason: &reason, At: -5},
		{TaskID: "0a1b2c3d", Actor: "s1", From: &done, To: StatusCanceled, Reason: nil, At: math.MaxInt64},
	}
	for _, in := range cases {
		se, err := toSpecEvent(in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := fromSpecEvent(se)
		if err != nil {
			t.Fatal(err)
		}
		if !eventEqual(out, in) {
			t.Errorf("got %+v, want %+v", out, in)
		}
	}
}

func Test未知の状態はDafnyの値に変換できずエラーになる(t *testing.T) {
	if _, err := toSpecTask(Task{Status: "doing"}); err == nil {
		t.Error("エラーにならなかった")
	}
	if _, err := ParseStatus("doing"); err == nil {
		t.Error("ParseStatus がエラーにならなかった")
	}
	for _, s := range []string{"todo", "done", "canceled"} {
		if got, err := ParseStatus(s); err != nil || string(got) != s {
			t.Errorf("ParseStatus(%q) = %q, %v", s, got, err)
		}
	}
}

func Test未知の終わらせ方は完了にも中止にもできずエラーになる(t *testing.T) {
	_, err := Finish(Task{ID: "x", Title: "t", Status: StatusTodo}, Outcome(99), "r", "s", 0)
	if err == nil {
		t.Error("エラーにならなかった")
	}
}

func TestCodeOfはErrorのコードを返しそれ以外のエラーはinternalにしエラーがなければ空を返す(t *testing.T) {
	if got := CodeOf(Errorf(CodeNotFound, "x")); got != CodeNotFound {
		t.Errorf("got %s", got)
	}
	wrapped := errors.Join(errors.New("other"), Errorf(CodeTitleRequired, "x"))
	if got := CodeOf(wrapped); got != CodeTitleRequired {
		t.Errorf("包んだエラー: got %s", got)
	}
	if got := CodeOf(errors.New("plain")); got != CodeInternal {
		t.Errorf("got %s", got)
	}
	// エラーがないときに internal を返すと、internal を確かめるテストがすり抜ける（#17）
	if got := CodeOf(nil); got != "" {
		t.Errorf("nil: got %q", got)
	}
}

func Test登録は仕様の判定どおりのエラーコードを返す(t *testing.T) {
	cases := []struct {
		name         string
		title, actor string
		want         Code
	}{
		{"主体が空", "t", "", CodeSessionRequired},
		{"主体が空白だけ", "t", "  ", CodeSessionRequired},
		{"主体もタイトルも空なら主体のエラーを先に返す", "", "", CodeSessionRequired},
		{"タイトルが空", "", "s", CodeTitleRequired},
		{"タイトルが全角の空白だけ", "　", "s", CodeTitleRequired},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewTask("0a1b2c3d", c.title, "", c.actor, 0)
			if got := CodeOf(err); got != c.want {
				t.Errorf("got %s (%v), want %s", got, err, c.want)
			}
		})
	}
}

func Test登録はtodoのタスクと変更前のない登録のイベントを返す(t *testing.T) {
	c, err := NewTask("0a1b2c3d", " タイトル ", "本文", "s1", 42)
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsNew() || !c.FromSpec() {
		t.Errorf("IsNew=%v FromSpec=%v", c.IsNew(), c.FromSpec())
	}
	tk, ev := c.Task(), c.Event()
	// 前後の空白を削らずに保存する
	if want := (Task{ID: "0a1b2c3d", Title: " タイトル ", Body: "本文", Status: StatusTodo, CreatedAt: 42}); tk != want {
		t.Errorf("task: got %+v, want %+v", tk, want)
	}
	if want := (Event{TaskID: "0a1b2c3d", Actor: "s1", To: StatusTodo, At: 42}); !eventEqual(ev, want) {
		t.Errorf("event: got %+v, want %+v", ev, want)
	}
}

func Test完了と中止は仕様の判定どおりのエラーコードを返す(t *testing.T) {
	todo := Task{ID: "x", Title: "t", Status: StatusTodo}
	cases := []struct {
		name          string
		task          Task
		reason, actor string
		want          Code
	}{
		{"主体が空", todo, "r", "", CodeSessionRequired},
		{"主体も理由も空なら主体のエラーを先に返す", todo, "", "", CodeSessionRequired},
		{"理由が空", todo, "", "s", CodeReasonRequired},
		{"理由が空白だけ", todo, " \n", "s", CodeReasonRequired},
		{"完了したタスク", Task{ID: "x", Title: "t", Status: StatusDone}, "r", "s", CodeAlreadyFinished},
		{"中止したタスク", Task{ID: "x", Title: "t", Status: StatusCanceled}, "r", "s", CodeAlreadyFinished},
		{"終わったタスクで理由が空なら理由のエラーを先に返す", Task{ID: "x", Title: "t", Status: StatusDone}, "", "s", CodeReasonRequired},
	}
	for _, c := range cases {
		for _, o := range []Outcome{OutcomeComplete, OutcomeCancel} {
			t.Run(c.name, func(t *testing.T) {
				_, err := Finish(c.task, o, c.reason, c.actor, 0)
				if got := CodeOf(err); got != c.want {
					t.Errorf("outcome %d: got %s (%v), want %s", o, got, err, c.want)
				}
				if got := CodeOf(FinishArgsError(c.reason, c.actor)); c.want != CodeAlreadyFinished && got != c.want {
					t.Errorf("FinishArgsError: got %s, want %s", got, c.want)
				}
			})
		}
	}
}

func Test完了はdoneに中止はcanceledにしイベントに変更前と理由を残す(t *testing.T) {
	todo := StatusTodo
	reason := "済んだ"
	cases := []struct {
		o    Outcome
		want Status
	}{
		{OutcomeComplete, StatusDone},
		{OutcomeCancel, StatusCanceled},
	}
	for _, c := range cases {
		ch, err := Finish(Task{ID: "x", Title: "t", Body: "b", Status: StatusTodo, CreatedAt: 1}, c.o, reason, "s2", 9)
		if err != nil {
			t.Fatal(err)
		}
		if ch.IsNew() || !ch.FromSpec() {
			t.Errorf("IsNew=%v FromSpec=%v", ch.IsNew(), ch.FromSpec())
		}
		tk, ev := ch.Task(), ch.Event()
		if want := (Task{ID: "x", Title: "t", Body: "b", Status: c.want, CreatedAt: 1}); tk != want {
			t.Errorf("task: got %+v, want %+v", tk, want)
		}
		if want := (Event{TaskID: "x", Actor: "s2", From: &todo, To: c.want, Reason: &reason, At: 9}); !eventEqual(ev, want) {
			t.Errorf("event: got %+v, want %+v", ev, want)
		}
	}
}

func Test一覧には既定で終わっていないタスクだけを出し指定すれば全部を出す(t *testing.T) {
	cases := []struct {
		s                 Status
		def, withFinished bool
	}{
		{StatusTodo, true, true},
		{StatusDone, false, true},
		{StatusCanceled, false, true},
	}
	for _, c := range cases {
		got, err := IsVisible(Task{Title: "t", Status: c.s}, false)
		if err != nil || got != c.def {
			t.Errorf("%s 既定: got %v, %v", c.s, got, err)
		}
		got, err = IsVisible(Task{Title: "t", Status: c.s}, true)
		if err != nil || got != c.withFinished {
			t.Errorf("%s 全部: got %v, %v", c.s, got, err)
		}
	}
}

func eventEqual(a, b Event) bool {
	return a.TaskID == b.TaskID && a.Actor == b.Actor && a.To == b.To && a.At == b.At &&
		ptrEqual(a.From, b.From) && ptrEqual(a.Reason, b.Reason)
}

func ptrEqual[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func Test保存されたタスクが不変条件を満たさなければ仕様の関数に渡さずinternalを返す(t *testing.T) {
	for _, title := range []string{"", " ", "　\n"} {
		broken := Task{ID: "x", Title: title, Status: StatusTodo}
		if _, err := Finish(broken, OutcomeComplete, "r", "s", 0); CodeOf(err) != CodeInternal {
			t.Errorf("Finish(title=%q): %v", title, err)
		}
		if _, err := IsVisible(broken, true); CodeOf(err) != CodeInternal {
			t.Errorf("IsVisible(title=%q): %v", title, err)
		}
	}
}

func Test外で作った状態の変更は仕様の関数が作ったものとして扱われない(t *testing.T) {
	var c Change
	if c.FromSpec() {
		t.Error("ゼロ値が FromSpec を true にした")
	}
}

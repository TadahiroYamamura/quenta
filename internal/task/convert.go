package task

import (
	"fmt"
	"strings"

	spec "github.com/TadahiroYamamura/quenta/internal/spec/Quenta"
	_dafny "github.com/TadahiroYamamura/quenta/internal/spec/dafny"
)

// Dafny の値と Go の値の変換。
// 変換したコードは interface{} で値を返す箇所があるため、型が想定と違えば panic させずにエラーを返す。

func toSeq(s string) _dafny.Sequence { return _dafny.UnicodeSeqOfUtf8Bytes(s) }

func fromSeq(seq _dafny.Sequence) (string, error) {
	var b strings.Builder
	n := seq.Cardinality()
	for i := uint32(0); i < n; i++ {
		c, ok := seq.Select(i).(_dafny.CodePoint)
		if !ok {
			return "", fmt.Errorf("文字列の %d 文字目が CodePoint でない: %T", i, seq.Select(i))
		}
		b.WriteRune(rune(c))
	}
	return b.String(), nil
}

func toSpecStatus(s Status) (spec.Status, error) {
	switch s {
	case StatusTodo:
		return spec.Companion_Status_.Create_Todo_(), nil
	case StatusDone:
		return spec.Companion_Status_.Create_Done_(), nil
	case StatusCanceled:
		return spec.Companion_Status_.Create_Canceled_(), nil
	}
	return spec.Status{}, fmt.Errorf("未知の状態: %q", s)
}

func fromSpecStatus(s spec.Status) (Status, error) {
	switch {
	case s.Is_Todo():
		return StatusTodo, nil
	case s.Is_Done():
		return StatusDone, nil
	case s.Is_Canceled():
		return StatusCanceled, nil
	}
	return "", fmt.Errorf("未知の状態: %v", s)
}

func toSpecOutcome(o Outcome) (spec.Outcome, error) {
	switch o {
	case OutcomeComplete:
		return spec.Companion_Outcome_.Create_Complete_(), nil
	case OutcomeCancel:
		return spec.Companion_Outcome_.Create_Cancel_(), nil
	}
	return spec.Outcome{}, fmt.Errorf("未知の終わらせ方: %d", o)
}

func toSpecTask(t Task) (spec.Task, error) {
	st, err := toSpecStatus(t.Status)
	if err != nil {
		return spec.Task{}, err
	}
	return spec.Companion_Task_.Create_Task_(toSeq(t.ID), toSeq(t.Title), toSeq(t.Body), st, t.CreatedAt), nil
}

func fromSpecTask(t spec.Task) (Task, error) {
	var out Task
	var err error
	if out.ID, err = fromSeq(t.Dtor_id()); err != nil {
		return Task{}, err
	}
	if out.Title, err = fromSeq(t.Dtor_title()); err != nil {
		return Task{}, err
	}
	if out.Body, err = fromSeq(t.Dtor_body()); err != nil {
		return Task{}, err
	}
	if out.Status, err = fromSpecStatus(t.Dtor_status()); err != nil {
		return Task{}, err
	}
	out.CreatedAt = t.Dtor_createdAt()
	return out, nil
}

func fromSpecEvent(e spec.Event) (Event, error) {
	var out Event
	var err error
	if out.TaskID, err = fromSeq(e.Dtor_taskId()); err != nil {
		return Event{}, err
	}
	if out.Actor, err = fromSeq(e.Dtor_actor()); err != nil {
		return Event{}, err
	}
	if from := e.Dtor_from(); from.Is_Some() {
		v, ok := from.Dtor_value().(spec.Status)
		if !ok {
			return Event{}, fmt.Errorf("イベントの変更前が Status でない: %T", from.Dtor_value())
		}
		s, err := fromSpecStatus(v)
		if err != nil {
			return Event{}, err
		}
		out.From = &s
	}
	if out.To, err = fromSpecStatus(e.Dtor_to()); err != nil {
		return Event{}, err
	}
	if reason := e.Dtor_reason(); reason.Is_Some() {
		v, ok := reason.Dtor_value().(_dafny.Sequence)
		if !ok {
			return Event{}, fmt.Errorf("イベントの理由が文字列でない: %T", reason.Dtor_value())
		}
		r, err := fromSeq(v)
		if err != nil {
			return Event{}, err
		}
		out.Reason = &r
	}
	out.At = e.Dtor_at()
	return out, nil
}

func toSpecEvent(e Event) (spec.Event, error) {
	from := spec.Companion_Option_.Create_None_()
	if e.From != nil {
		s, err := toSpecStatus(*e.From)
		if err != nil {
			return spec.Event{}, err
		}
		from = spec.Companion_Option_.Create_Some_(s)
	}
	to, err := toSpecStatus(e.To)
	if err != nil {
		return spec.Event{}, err
	}
	reason := spec.Companion_Option_.Create_None_()
	if e.Reason != nil {
		reason = spec.Companion_Option_.Create_Some_(toSeq(*e.Reason))
	}
	return spec.Companion_Event_.Create_Event_(toSeq(e.TaskID), toSeq(e.Actor), from, to, reason, e.At), nil
}

// fromSpecError は、仕様のエラーを、仕様の ErrorCode が決めたエラーコードのエラーにする
func fromSpecError(e spec.Error) *Error {
	code, err := fromSeq(spec.Companion_Default___.ErrorCode(e))
	if err != nil {
		return Errorf(CodeInternal, "仕様のエラーコードを読めない: %v", err)
	}
	return &Error{Code: Code(code), Message: errorMessages[Code(code)]}
}

// 利用者（AI）に返すエラーの説明。エラーコードは仕様が決め、説明は Go が持つ。
// 仕様のエラーのすべてに説明があることは、テストで確かめる
var errorMessages = map[Code]string{
	CodeSessionRequired: "環境変数 QUENTA_SESSION がない",
	CodeTitleRequired:   "タイトルが空",
	CodeReasonRequired:  "理由が空",
	CodeNotFound:        "タスクがない",
	CodeAlreadyFinished: "終わったタスクの状態は変えられない",
	CodeIDConflict:      "ID が既にある",
}

// taskEventPair は、仕様の Ok((Task, Event)) から値を取り出す
func taskEventPair(r spec.Result) (Task, Event, error) {
	if r.Is_Err() {
		return Task{}, Event{}, fromSpecError(r.Dtor_error())
	}
	tuple, ok := r.Dtor_value().(_dafny.Tuple)
	if !ok {
		return Task{}, Event{}, fmt.Errorf("結果が組でない: %T", r.Dtor_value())
	}
	st, ok := (*tuple.IndexInt(0)).(spec.Task)
	if !ok {
		return Task{}, Event{}, fmt.Errorf("組の 1 つ目が Task でない: %T", *tuple.IndexInt(0))
	}
	se, ok := (*tuple.IndexInt(1)).(spec.Event)
	if !ok {
		return Task{}, Event{}, fmt.Errorf("組の 2 つ目が Event でない: %T", *tuple.IndexInt(1))
	}
	t, err := fromSpecTask(st)
	if err != nil {
		return Task{}, Event{}, err
	}
	e, err := fromSpecEvent(se)
	if err != nil {
		return Task{}, Event{}, err
	}
	return t, e, nil
}

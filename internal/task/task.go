// Package task は、タスクとイベントの値と、登録・完了・中止・一覧の判定を持つ。
// 判定は spec/quenta.dfy の AddDecide・FinishDecide・ListDecide と同じ入力をとり、テストでそれと突き合わせる
package task

import (
	"cmp"
	"slices"
)

type Status string

const (
	Todo     Status = "todo"
	Done     Status = "done"
	Canceled Status = "canceled"
)

// Statuses は、状態のすべての値
var Statuses = []Status{Todo, Done, Canceled}

// IsFinished は、終わった状態を挙げて決める。状態が増えても、挙げていない状態は終わった状態にならない
func (s Status) IsFinished() bool {
	switch s {
	case Done, Canceled:
		return true
	}
	return false
}

type Task struct {
	ID        string
	Title     string
	Body      string
	Status    Status
	CreatedAt int64 // 1970-01-01T00:00:00Z からの秒の数
}

type Event struct {
	TaskID string
	Actor  string
	From   *Status // 登録のイベントでは nil（変更前なし）
	To     Status
	Reason string
	At     int64
}

// Failure は、判定が返すエラーの種類
type Failure int

const (
	SessionRequired Failure = iota + 1
	TitleRequired
	ReasonRequired
	NotFound
	AlreadyFinished
	IDTaken
)

// Failures は、判定が返すエラーの種類のすべて
var Failures = []Failure{SessionRequired, TitleRequired, ReasonRequired, NotFound, AlreadyFinished, IDTaken}

func (f Failure) Error() string {
	switch f {
	case SessionRequired:
		return "主体（QUENTA_SESSION）が空白だけ"
	case TitleRequired:
		return "タイトルが空白だけ"
	case ReasonRequired:
		return "理由が空白だけ"
	case NotFound:
		return "指定した ID のタスクがない"
	case AlreadyFinished:
		return "タスクはもう終わっている"
	case IDTaken:
		return "ID が登録済みのタスクと重なっている"
	}
	return "判定の知らないエラー"
}

// IsWhiteSpace は、Unicode 15.1 で White_Space の性質を持つ文字かを返す（spec/quenta.dfy の IsWhiteSpace と同じ一覧）
func IsWhiteSpace(r rune) bool {
	return (0x0009 <= r && r <= 0x000D) || r == 0x0020 || r == 0x0085 || r == 0x00A0 || r == 0x1680 ||
		(0x2000 <= r && r <= 0x200A) || r == 0x2028 || r == 0x2029 || r == 0x202F || r == 0x205F ||
		r == 0x3000
}

// IsBlank は、文字が 1 つもないか、空白の文字だけでできているかを返す
func IsBlank(s string) bool {
	for _, r := range s {
		if !IsWhiteSpace(r) {
			return false
		}
	}
	return true
}

type AddWrite struct {
	Task  Task
	Event Event
}

// AddDecide は登録の判定。idTaken は、その ID のタスクがもうあるか（DB に問い合わせた事実）
func AddDecide(actor, title, body, id string, now int64, idTaken bool) (AddWrite, error) {
	switch {
	case IsBlank(actor):
		return AddWrite{}, SessionRequired
	case IsBlank(title):
		return AddWrite{}, TitleRequired
	case idTaken:
		return AddWrite{}, IDTaken
	}
	return AddWrite{
		Task:  Task{ID: id, Title: title, Body: body, Status: Todo, CreatedAt: now},
		Event: Event{TaskID: id, Actor: actor, From: nil, To: Todo, Reason: "", At: now},
	}, nil
}

// FinishKind は、完了か中止か
type FinishKind int

const (
	Completing FinishKind = iota + 1
	Canceling
)

// FinishKinds は、FinishKind のすべての値
var FinishKinds = []FinishKind{Completing, Canceling}

// Target は、完了・中止の行き先の状態
func (k FinishKind) Target() (Status, bool) {
	switch k {
	case Completing:
		return Done, true
	case Canceling:
		return Canceled, true
	}
	return "", false
}

type FinishWrite struct {
	ID     string
	Status Status
	Event  Event
}

// errUnknownKind は、FinishKind の知らない値が渡されたこと。Completing・Canceling のほかは作らないので、起きれば実装の誤り
type errUnknownKind struct{}

func (errUnknownKind) Error() string { return "完了か中止かの値が正しくない" }

// FinishDecide は完了・中止の判定。found は、指定した ID のタスク（DB に問い合わせた事実。なければ nil）
func FinishDecide(actor, id, reason string, k FinishKind, now int64, found *Task) (FinishWrite, error) {
	to, ok := k.Target()
	if !ok {
		return FinishWrite{}, errUnknownKind{}
	}
	switch {
	case IsBlank(actor):
		return FinishWrite{}, SessionRequired
	case IsBlank(reason):
		return FinishWrite{}, ReasonRequired
	case found == nil:
		return FinishWrite{}, NotFound
	case found.Status.IsFinished():
		return FinishWrite{}, AlreadyFinished
	}
	from := found.Status
	return FinishWrite{
		ID:     id,
		Status: to,
		Event:  Event{TaskID: id, Actor: actor, From: &from, To: to, Reason: reason, At: now},
	}, nil
}

// ListDecide は一覧の判定。tasks は DB に問い合わせたタスクの集まり。作成日時の古い順、同じなら ID の昇順に並べる
func ListDecide(actor string, includeFinished bool, tasks []Task) ([]Task, error) {
	if IsBlank(actor) {
		return nil, SessionRequired
	}
	r := make([]Task, 0, len(tasks))
	for _, t := range tasks {
		if includeFinished || !t.Status.IsFinished() {
			r = append(r, t)
		}
	}
	// 正しい UTF-8 の文字列では、バイトで比べた順は、仕様の IdLess（符号位置の辞書式の順）と同じ
	slices.SortFunc(r, func(a, b Task) int {
		return cmp.Or(cmp.Compare(a.CreatedAt, b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	return r, nil
}

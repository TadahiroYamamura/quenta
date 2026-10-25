package task

import (
	spec "github.com/TadahiroYamamura/quenta/internal/spec/Quenta"
)

// 判定と遷移は、仕様（spec/task.dfy）から変換した関数に任せる。ここは型を変換して呼ぶだけ

// Change は、仕様の NewTask・Finish が返したタスクとイベントの組（状態の変更）。
// 保存の層はこの型でしか書き込めない。フィールドを非公開にし、このパッケージの NewTask・Finish の結果からしか作れないようにして、
// 仕様が返したものでない組や、タスクとイベントの片方だけを保存する経路を作れなくする（#1 の仕様のレビューで人が求めた）
type Change struct {
	task  Task
	event Event
	isNew bool
	// 外で Change{} と書いた値を見分けるため。NewTask・Finish だけが true にする
	fromSpec bool
}

func (c Change) Task() Task   { return c.task }
func (c Change) Event() Event { return c.event }

// IsNew は、登録（新しいタスクを足す）か。false なら完了・中止（既存のタスクの状態を変える）
func (c Change) IsNew() bool { return c.isNew }

// FromSpec は、仕様の NewTask・Finish が作った値か
func (c Change) FromSpec() bool { return c.fromSpec }

func NewTask(id, title, body, actor string, now int64) (Change, error) {
	r := spec.Companion_Default___.NewTask(toSeq(id), toSeq(title), toSeq(body), toSeq(actor), now)
	t, e, err := taskEventPair(r)
	if err != nil {
		return Change{}, err
	}
	return Change{task: t, event: e, isNew: true, fromSpec: true}, nil
}

func FinishArgsError(reason, actor string) error {
	r := spec.Companion_Default___.FinishArgsError(toSeq(reason), toSeq(actor))
	if r.Is_None() {
		return nil
	}
	e, ok := r.Dtor_value().(spec.Error)
	if !ok {
		return Errorf(CodeInternal, "仕様の判定の結果が Error でない: %T", r.Dtor_value())
	}
	return fromSpecError(e)
}

func Finish(t Task, o Outcome, reason, actor string, now int64) (Change, error) {
	st, err := toSpecTask(t)
	if err != nil {
		return Change{}, err
	}
	so, err := toSpecOutcome(o)
	if err != nil {
		return Change{}, err
	}
	r := spec.Companion_Default___.Finish(st, so, toSeq(reason), toSeq(actor), now)
	nt, e, err := taskEventPair(r)
	if err != nil {
		return Change{}, err
	}
	return Change{task: nt, event: e, isNew: false, fromSpec: true}, nil
}

func IsVisible(t Task, includeFinished bool) (bool, error) {
	st, err := toSpecTask(t)
	if err != nil {
		return false, err
	}
	return spec.Companion_Default___.IsVisible(st, includeFinished), nil
}

func IsBlank(s string) bool {
	return spec.Companion_Default___.IsBlank(toSeq(s))
}

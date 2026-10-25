package task

import (
	spec "github.com/TadahiroYamamura/quenta/internal/spec/Quenta"
)

// 判定と遷移は、仕様（spec/task.dfy）から変換した関数に任せる。ここは型を変換して呼ぶだけ

func NewTask(id, title, body, actor string, now int64) (Task, Event, error) {
	r := spec.Companion_Default___.NewTask(toSeq(id), toSeq(title), toSeq(body), toSeq(actor), now)
	return taskEventPair(r)
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

func Finish(t Task, o Outcome, reason, actor string, now int64) (Task, Event, error) {
	st, err := toSpecTask(t)
	if err != nil {
		return Task{}, Event{}, err
	}
	so, err := toSpecOutcome(o)
	if err != nil {
		return Task{}, Event{}, err
	}
	r := spec.Companion_Default___.Finish(st, so, toSeq(reason), toSeq(actor), now)
	return taskEventPair(r)
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

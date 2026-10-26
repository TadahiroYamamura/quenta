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

// NewTask は、登録の状態の変更を作る。idTaken は、DB で調べた「その ID のタスクが既にあるか」
func NewTask(id string, idTaken bool, title, body, actor string, now int64) (Change, error) {
	r := spec.Companion_Default___.NewTask(toSeq(id), idTaken, toSeq(title), toSeq(body), toSeq(actor), now)
	t, e, err := taskEventPair(r)
	if err != nil {
		return Change{}, err
	}
	return Change{task: t, event: e, isNew: true, fromSpec: true}, nil
}

// Finish は、完了・中止の状態の変更を作る。found は、DB で ID のタスクを探した結果（なければ nil）。
// found が仕様の不変条件（ValidTask）を満たさなければ internal
func Finish(found *Task, o Outcome, reason, actor string, now int64) (Change, error) {
	sf, err := toValidSpecFound(found)
	if err != nil {
		return Change{}, err
	}
	so, err := toSpecOutcome(o)
	if err != nil {
		return Change{}, err
	}
	r := spec.Companion_Default___.Finish(sf, so, toSeq(reason), toSeq(actor), now)
	nt, e, err := taskEventPair(r)
	if err != nil {
		return Change{}, err
	}
	return Change{task: nt, event: e, isNew: false, fromSpec: true}, nil
}

// ShowTask は、詳細に出すタスクを返す。found は、DB で ID のタスクを探した結果（なければ nil）。
// found が仕様の不変条件（ValidTask）を満たさなければ internal
func ShowTask(found *Task) (Task, error) {
	sf, err := toValidSpecFound(found)
	if err != nil {
		return Task{}, err
	}
	r := spec.Companion_Default___.ShowTask(sf)
	if r.Is_Err() {
		return Task{}, fromSpecError(r.Dtor_error())
	}
	st, ok := r.Dtor_value().(spec.Task)
	if !ok {
		return Task{}, Errorf(CodeInternal, "詳細の結果が Task でない: %T", r.Dtor_value())
	}
	return fromSpecTask(st)
}

// toValidSpecFound は、DB で探した結果を仕様の Option<Task> にする。タスクがあれば、仕様の不変条件を満たすか確かめる
func toValidSpecFound(found *Task) (spec.Option, error) {
	if found == nil {
		return spec.Companion_Option_.Create_None_(), nil
	}
	st, err := toValidSpecTask(*found)
	if err != nil {
		return spec.Option{}, err
	}
	return spec.Companion_Option_.Create_Some_(st), nil
}

// IsVisible は、DB から読んだタスク t を一覧に出すか。t が仕様の不変条件（ValidTask）を満たさなければ internal
func IsVisible(t Task, includeFinished bool) (bool, error) {
	st, err := toValidSpecTask(t)
	if err != nil {
		return false, err
	}
	return spec.Companion_Default___.IsVisible(st, includeFinished), nil
}

// toValidSpecTask は、DB から読んだタスクを仕様の値にし、仕様の不変条件を満たすか確かめる。
// 仕様の証明は、不変条件を満たすタスクを前提にしているため
func toValidSpecTask(t Task) (spec.Task, error) {
	st, err := toSpecTask(t)
	if err != nil {
		return spec.Task{}, err
	}
	if !spec.Companion_Default___.ValidTask(st) {
		return spec.Task{}, Errorf(CodeInternal, "保存されたタスク %s が仕様の不変条件（タイトルが空でない）を満たさない", t.ID)
	}
	return st, nil
}

func IsBlank(s string) bool {
	return spec.Companion_Default___.IsBlank(toSeq(s))
}

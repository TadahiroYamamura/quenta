package task

import (
	"fmt"

	spec "github.com/TadahiroYamamura/quenta/internal/spec/Quenta"
	_dafny "github.com/TadahiroYamamura/quenta/internal/spec/dafny"
)

// Model は、仕様の全体の状態（State）とその操作。
// 実装はデータベースで同じことをするので、Model は使わない。テストで、実装の結果をこの手本と突き合わせるために使う
type Model struct {
	st spec.State
}

func NewModel() *Model {
	return &Model{st: spec.Companion_Default___.Empty()}
}

func (m *Model) Add(id, title, body, actor string, now int64) error {
	return m.apply(spec.Companion_Default___.Add(m.st, toSeq(id), toSeq(title), toSeq(body), toSeq(actor), now))
}

func (m *Model) Finish(id string, o Outcome, reason, actor string, now int64) error {
	so, err := toSpecOutcome(o)
	if err != nil {
		return err
	}
	return m.apply(spec.Companion_Default___.FinishTask(m.st, toSeq(id), so, toSeq(reason), toSeq(actor), now))
}

func (m *Model) apply(r spec.Result) error {
	if r.Is_Err() {
		return fromSpecError(r.Dtor_error())
	}
	st, ok := r.Dtor_value().(spec.State)
	if !ok {
		return fmt.Errorf("結果が State でない: %T", r.Dtor_value())
	}
	m.st = st
	return nil
}

func (m *Model) Show(id string) (Task, []Event, error) {
	r := spec.Companion_Default___.Show(m.st, toSeq(id))
	if r.Is_Err() {
		return Task{}, nil, fromSpecError(r.Dtor_error())
	}
	tuple, ok := r.Dtor_value().(_dafny.Tuple)
	if !ok {
		return Task{}, nil, fmt.Errorf("結果が組でない: %T", r.Dtor_value())
	}
	st, ok := (*tuple.IndexInt(0)).(spec.Task)
	if !ok {
		return Task{}, nil, fmt.Errorf("組の 1 つ目が Task でない: %T", *tuple.IndexInt(0))
	}
	seq, ok := (*tuple.IndexInt(1)).(_dafny.Sequence)
	if !ok {
		return Task{}, nil, fmt.Errorf("組の 2 つ目が列でない: %T", *tuple.IndexInt(1))
	}
	t, err := fromSpecTask(st)
	if err != nil {
		return Task{}, nil, err
	}
	evs, err := eventsFromSeq(seq)
	if err != nil {
		return Task{}, nil, err
	}
	return t, evs, nil
}

func (m *Model) List(includeFinished bool) ([]Task, error) {
	return tasksFromSeq(spec.Companion_Default___.List(m.st, includeFinished))
}

func (m *Model) Tasks() ([]Task, error) { return tasksFromSeq(m.st.Dtor_tasks()) }

func (m *Model) Events() ([]Event, error) { return eventsFromSeq(m.st.Dtor_events()) }

// Valid は、仕様の不変条件が成り立つか
func (m *Model) Valid() bool { return spec.Companion_Default___.Valid(m.st) }

// ValidState は、実装から読み出したタスクとイベントで、仕様の不変条件が成り立つか
func ValidState(tasks []Task, events []Event) (bool, error) {
	ts := make([]any, len(tasks))
	for i, t := range tasks {
		st, err := toSpecTask(t)
		if err != nil {
			return false, err
		}
		ts[i] = st
	}
	es := make([]any, len(events))
	for i, e := range events {
		se, err := toSpecEvent(e)
		if err != nil {
			return false, err
		}
		es[i] = se
	}
	st := spec.Companion_State_.Create_State_(_dafny.SeqOf(ts...), _dafny.SeqOf(es...))
	return spec.Companion_Default___.Valid(st), nil
}

func tasksFromSeq(seq _dafny.Sequence) ([]Task, error) {
	n := seq.Cardinality()
	out := make([]Task, 0, n)
	for i := uint32(0); i < n; i++ {
		st, ok := seq.Select(i).(spec.Task)
		if !ok {
			return nil, fmt.Errorf("列の %d 番目が Task でない: %T", i, seq.Select(i))
		}
		t, err := fromSpecTask(st)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func eventsFromSeq(seq _dafny.Sequence) ([]Event, error) {
	n := seq.Cardinality()
	out := make([]Event, 0, n)
	for i := uint32(0); i < n; i++ {
		se, ok := seq.Select(i).(spec.Event)
		if !ok {
			return nil, fmt.Errorf("列の %d 番目が Event でない: %T", i, seq.Select(i))
		}
		e, err := fromSpecEvent(se)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

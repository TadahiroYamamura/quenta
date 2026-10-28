// Package ops は、登録・一覧・表示・完了・中止を、問い合わせ → 判定 → 書き込みの順につなぐ。
// spec/quenta.dfy の Add・List・Complete・Cancel に当たる。1 つの操作は、1 つの書き込みのトランザクションの中で行う
package ops

import (
	"errors"
	"fmt"
	"io"

	"github.com/TadahiroYamamura/quenta/internal/store"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

// MaxIDAttempts は、ID が重なったときに作り直す回数の上限
const MaxIDAttempts = 10

// ErrIDConflict は、ID を作り直しても重なり続け、上限に達したこと
var ErrIDConflict = errors.New("ID を作り直しても、登録済みの ID と重なり続けた")

// ErrCheckFailed は、実行時の確認が、書き込もうとしている内容の誤りを見つけたこと
var ErrCheckFailed = errors.New("実行時の確認が、書き込む内容の誤りを見つけた")

type Ops struct {
	Store  *store.Store
	Random io.Reader
	Now    func() int64
}

// Add は、ID を作って登録する。ID が重なれば、作り直す
func (o *Ops) Add(actor, title, body string) (task.Task, error) {
	for range MaxIDAttempts {
		id, err := task.NewID(o.Random)
		if err != nil {
			return task.Task{}, err
		}
		var added task.Task
		err = o.Store.Write(func(tx *store.Tx) error {
			taken, err := tx.IDTaken(id)
			if err != nil {
				return err
			}
			w, err := task.AddDecide(actor, title, body, id, o.Now(), taken)
			if err != nil {
				return err
			}
			if err := checkAddWrite(w); err != nil {
				return err
			}
			added = w.Task
			return tx.ApplyAdd(w)
		})
		if errors.Is(err, task.IDTaken) {
			continue
		}
		return added, err
	}
	return task.Task{}, ErrIDConflict
}

// Finish は、完了か中止にする
func (o *Ops) Finish(actor, id, reason string, k task.FinishKind) (task.Task, error) {
	var changed task.Task
	err := o.Store.Write(func(tx *store.Tx) error {
		found, err := tx.FindTask(id)
		if err != nil {
			return err
		}
		w, err := task.FinishDecide(actor, id, reason, k, o.Now(), found)
		if err != nil {
			return err
		}
		if err := checkFinishWrite(w); err != nil {
			return err
		}
		changed = *found
		changed.Status = w.Status
		return tx.ApplyFinish(w)
	})
	return changed, err
}

// List は、一覧を返す
func (o *Ops) List(actor string, includeFinished bool) ([]task.Task, error) {
	var r []task.Task
	err := o.Store.Write(func(tx *store.Tx) error {
		ts, err := tx.Tasks()
		if err != nil {
			return err
		}
		r, err = task.ListDecide(actor, includeFinished, ts)
		return err
	})
	return r, err
}

// Show は、タスクとそのイベントを返す。見つからなければ task.NotFound
func (o *Ops) Show(id string) (task.Task, []task.Event, error) {
	var t *task.Task
	var es []task.Event
	err := o.Store.Write(func(tx *store.Tx) error {
		var err error
		if t, err = tx.FindTask(id); err != nil || t == nil {
			return err
		}
		es, err = tx.EventsOf(id)
		return err
	})
	if err != nil {
		return task.Task{}, nil, err
	}
	if t == nil {
		return task.Task{}, nil, task.NotFound
	}
	return *t, es, nil
}

// checkAddWrite は、コミット前に、書き込もうとしているタスクの状態が、そのタスクの最後のイベント（この登録のイベント）の変更後と一致するかを、メモリから確かめる
// @assurance:check status-matches-last-event
func checkAddWrite(w task.AddWrite) error {
	if w.Event.TaskID != w.Task.ID || w.Event.To != w.Task.Status {
		return fmt.Errorf("%w: 登録のタスクの状態 %q とイベントの変更後 %q が一致しない", ErrCheckFailed, w.Task.Status, w.Event.To)
	}
	return nil
}

// checkFinishWrite は、コミット前に、書き込もうとしているタスクの状態が、そのタスクの最後のイベント（この完了・中止のイベント）の変更後と一致するかを、メモリから確かめる
// @assurance:check status-matches-last-event
func checkFinishWrite(w task.FinishWrite) error {
	if w.Event.TaskID != w.ID || w.Event.To != w.Status {
		return fmt.Errorf("%w: 完了・中止の状態 %q とイベントの変更後 %q が一致しない", ErrCheckFailed, w.Status, w.Event.To)
	}
	return nil
}

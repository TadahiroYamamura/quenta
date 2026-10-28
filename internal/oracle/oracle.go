// Package oracle は、Dafny から生成したモデル（internal/spec）と実装の値を相互に変換する。テストからだけ使う
package oracle

import (
	"database/sql"
	"fmt"
	"strings"

	Q "github.com/TadahiroYamamura/quenta/internal/spec/Quenta"
	_dafny "github.com/TadahiroYamamura/quenta/internal/spec/dafny"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

// M は、モデルの関数を呼ぶ入り口
var M = Q.Companion_Default___

func Str(s string) _dafny.Sequence { return _dafny.UnicodeSeqOfUtf8Bytes(s) }

func GoStr(s _dafny.Sequence) (string, error) {
	var b strings.Builder
	for i := uint32(0); i < s.Cardinality(); i++ {
		cp, ok := s.Select(i).(_dafny.CodePoint)
		if !ok {
			return "", fmt.Errorf("文字列の %d 文字目が CodePoint でない: %T", i, s.Select(i))
		}
		b.WriteRune(rune(cp))
	}
	return b.String(), nil
}

func Int(i int64) _dafny.Int { return _dafny.IntOfInt64(i) }

func Status(s task.Status) (Q.Status, error) {
	switch s {
	case task.Todo:
		return Q.Companion_Status_.Create_Todo_(), nil
	case task.Done:
		return Q.Companion_Status_.Create_Done_(), nil
	case task.Canceled:
		return Q.Companion_Status_.Create_Canceled_(), nil
	}
	return Q.Status{}, fmt.Errorf("知らない状態: %q", s)
}

func GoStatus(s Q.Status) (task.Status, error) {
	switch {
	case s.Is_Todo():
		return task.Todo, nil
	case s.Is_Done():
		return task.Done, nil
	case s.Is_Canceled():
		return task.Canceled, nil
	}
	return "", fmt.Errorf("モデルの知らない状態: %v", s)
}

func Kind(k task.FinishKind) (Q.FinishKind, error) {
	switch k {
	case task.Completing:
		return Q.Companion_FinishKind_.Create_Completing_(), nil
	case task.Canceling:
		return Q.Companion_FinishKind_.Create_Canceling_(), nil
	}
	return Q.FinishKind{}, fmt.Errorf("知らない FinishKind: %d", k)
}

func Task(t task.Task) (Q.Task, error) {
	st, err := Status(t.Status)
	if err != nil {
		return Q.Task{}, err
	}
	return Q.Companion_Task_.Create_Task_(Str(t.ID), Str(t.Title), Str(t.Body), st, Int(t.CreatedAt)), nil
}

func GoTask(t Q.Task) (task.Task, error) {
	var r task.Task
	var err error
	if r.ID, err = GoStr(t.Dtor_id()); err != nil {
		return r, err
	}
	if r.Title, err = GoStr(t.Dtor_title()); err != nil {
		return r, err
	}
	if r.Body, err = GoStr(t.Dtor_body()); err != nil {
		return r, err
	}
	if r.Status, err = GoStatus(t.Dtor_status()); err != nil {
		return r, err
	}
	r.CreatedAt = t.Dtor_createdAt().Int64()
	return r, nil
}

func Event(e task.Event) (Q.Event, error) {
	from := Q.Companion_Option_.Create_None_()
	if e.From != nil {
		st, err := Status(*e.From)
		if err != nil {
			return Q.Event{}, err
		}
		from = Q.Companion_Option_.Create_Some_(st)
	}
	to, err := Status(e.To)
	if err != nil {
		return Q.Event{}, err
	}
	return Q.Companion_Event_.Create_Event_(Str(e.TaskID), Str(e.Actor), from, to, Str(e.Reason), Int(e.At)), nil
}

func GoEvent(e Q.Event) (task.Event, error) {
	var r task.Event
	var err error
	if r.TaskID, err = GoStr(e.Dtor_taskId()); err != nil {
		return r, err
	}
	if r.Actor, err = GoStr(e.Dtor_actor()); err != nil {
		return r, err
	}
	if f := e.Dtor_from(); f.Is_Some() {
		st, ok := f.Dtor_value().(Q.Status)
		if !ok {
			return r, fmt.Errorf("イベントの変更前が状態でない: %T", f.Dtor_value())
		}
		s, err := GoStatus(st)
		if err != nil {
			return r, err
		}
		r.From = &s
	}
	if r.To, err = GoStatus(e.Dtor_to()); err != nil {
		return r, err
	}
	if r.Reason, err = GoStr(e.Dtor_reason()); err != nil {
		return r, err
	}
	r.At = e.Dtor_at().Int64()
	return r, nil
}

func Tasks(ts []task.Task) (_dafny.Sequence, error) {
	xs := make([]interface{}, 0, len(ts))
	for _, t := range ts {
		m, err := Task(t)
		if err != nil {
			return nil, err
		}
		xs = append(xs, m)
	}
	return _dafny.SeqOf(xs...), nil
}

func GoTasks(s _dafny.Sequence) ([]task.Task, error) {
	r := []task.Task{}
	for i := uint32(0); i < s.Cardinality(); i++ {
		m, ok := s.Select(i).(Q.Task)
		if !ok {
			return nil, fmt.Errorf("列の %d 番目がタスクでない: %T", i, s.Select(i))
		}
		t, err := GoTask(m)
		if err != nil {
			return nil, err
		}
		r = append(r, t)
	}
	return r, nil
}

func GoEvents(s _dafny.Sequence) ([]task.Event, error) {
	r := []task.Event{}
	for i := uint32(0); i < s.Cardinality(); i++ {
		m, ok := s.Select(i).(Q.Event)
		if !ok {
			return nil, fmt.Errorf("列の %d 番目がイベントでない: %T", i, s.Select(i))
		}
		e, err := GoEvent(m)
		if err != nil {
			return nil, err
		}
		r = append(r, e)
	}
	return r, nil
}

// State は、実装の DB の中身をモデルの状態に写したもの
type State struct {
	Tasks  []task.Task
	Events []task.Event
}

func (s State) Model() (Q.State, error) {
	ts, err := Tasks(s.Tasks)
	if err != nil {
		return Q.State{}, err
	}
	es := make([]interface{}, 0, len(s.Events))
	for _, e := range s.Events {
		m, err := Event(e)
		if err != nil {
			return Q.State{}, err
		}
		es = append(es, m)
	}
	return Q.Companion_State_.Create_State_(ts, _dafny.SeqOf(es...)), nil
}

func GoState(s Q.State) (State, error) {
	ts, err := GoTasks(s.Dtor_tasks())
	if err != nil {
		return State{}, err
	}
	es, err := GoEvents(s.Dtor_events())
	if err != nil {
		return State{}, err
	}
	return State{Tasks: ts, Events: es}, nil
}

// ReadState は、データのファイルの中身を、モデルの状態の並び（タスクは書き込んだ順、イベントは記録の順）で読む
func ReadState(db *sql.DB) (State, error) {
	s := State{Tasks: []task.Task{}, Events: []task.Event{}}
	rows, err := db.Query("SELECT id, title, body, status, created_at FROM tasks ORDER BY rowid")
	if err != nil {
		return s, err
	}
	for rows.Next() {
		var t task.Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Body, &t.Status, &t.CreatedAt); err != nil {
			rows.Close()
			return s, err
		}
		s.Tasks = append(s.Tasks, t)
	}
	rows.Close()
	rows, err = db.Query("SELECT task_id, actor, from_status, to_status, reason, at FROM events ORDER BY seq")
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var e task.Event
		var from sql.NullString
		if err := rows.Scan(&e.TaskID, &e.Actor, &from, &e.To, &e.Reason, &e.At); err != nil {
			return s, err
		}
		if from.Valid {
			f := task.Status(from.String)
			e.From = &f
		}
		s.Events = append(s.Events, e)
	}
	return s, rows.Err()
}

// Failure は、モデルのエラーを、実装の判定のエラーに写す
func Failure(e Q.Error) (task.Failure, error) {
	switch {
	case e.Is_SessionRequired():
		return task.SessionRequired, nil
	case e.Is_TitleRequired():
		return task.TitleRequired, nil
	case e.Is_ReasonRequired():
		return task.ReasonRequired, nil
	case e.Is_NotFound():
		return task.NotFound, nil
	case e.Is_AlreadyFinished():
		return task.AlreadyFinished, nil
	case e.Is_IdTaken():
		return task.IDTaken, nil
	}
	return 0, fmt.Errorf("モデルの知らないエラー: %v", e)
}

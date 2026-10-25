package task

import (
	"reflect"
	"testing"
)

// ValidState は、プロパティベースのテストで実装の状態を確かめる判定に使う。
// 常に true を返すように壊れると、そのテストは何も確かめなくなるので、不変条件を破る状態を見分けられることをここで確かめる

func validFixture() ([]Task, []Event) {
	todo, done := StatusTodo, StatusDone
	reason := "r"
	tasks := []Task{
		{ID: "00000001", Title: "a", Status: StatusTodo, CreatedAt: 1},
		{ID: "00000002", Title: "b", Status: StatusDone, CreatedAt: 2},
	}
	events := []Event{
		{TaskID: "00000001", Actor: "s", To: StatusTodo, At: 1},
		{TaskID: "00000002", Actor: "s", To: StatusTodo, At: 2},
		{TaskID: "00000002", Actor: "s", From: &todo, To: done, Reason: &reason, At: 3},
	}
	return tasks, events
}

func TestValidStateは不変条件を満たす状態をtrueにする(t *testing.T) {
	tasks, events := validFixture()
	ok, err := ValidState(tasks, events)
	if err != nil || !ok {
		t.Errorf("got %v, %v", ok, err)
	}
	ok, err = ValidState(nil, nil)
	if err != nil || !ok {
		t.Errorf("空の状態: got %v, %v", ok, err)
	}
}

func TestValidStateは不変条件を破る状態をfalseにする(t *testing.T) {
	todo, canceled := StatusTodo, StatusCanceled
	blank := " "
	cases := []struct {
		name   string
		breaks func(tasks []Task, events []Event) ([]Task, []Event)
	}{
		{"ID が重なる", func(ts []Task, es []Event) ([]Task, []Event) {
			// 同じ ID・同じ作成日時の todo のタスクが 2 つあり、登録のイベントが 1 件だけある。
			// どちらのタスクから見ても経緯は正しいので、ID の重なりの条件だけが破れる
			dup := ts[0]
			dup.Title = "別のタイトル"
			return append(ts, dup), es
		}},
		{"タイトルが空白だけ", func(ts []Task, es []Event) ([]Task, []Event) {
			ts[0].Title = "　"
			return ts, es
		}},
		{"イベントの主体が空", func(ts []Task, es []Event) ([]Task, []Event) {
			es[0].Actor = ""
			return ts, es
		}},
		{"イベントのないタスクがある", func(ts []Task, es []Event) ([]Task, []Event) {
			return ts, es[1:]
		}},
		{"存在しないタスクのイベントがある", func(ts []Task, es []Event) ([]Task, []Event) {
			return ts, append(es, Event{TaskID: "ffffffff", Actor: "s", To: StatusTodo, At: 9})
		}},
		{"タスクの状態が最後のイベントの変更後と違う", func(ts []Task, es []Event) ([]Task, []Event) {
			ts[1].Status = StatusCanceled
			return ts, es
		}},
		{"登録のイベントの日時が作成日時と違う", func(ts []Task, es []Event) ([]Task, []Event) {
			es[0].At = 99
			return ts, es
		}},
		{"登録のイベントの変更後が todo でない", func(ts []Task, es []Event) ([]Task, []Event) {
			// 登録で done になったタスク。最後の変更後とタスクの状態は合う
			ts[0].Status = StatusDone
			es[0].To = StatusDone
			return ts, es
		}},
		{"最初のイベントが登録でない", func(ts []Task, es []Event) ([]Task, []Event) {
			// 00000002 の登録のイベントを消し、todo から done への変更のイベントだけにする。
			// 日時を作成日時にそろえ、ほかの条件では弾かれないようにする
			es[2].At = ts[1].CreatedAt
			return ts, []Event{es[0], es[2]}
		}},
		{"完了のイベントに理由がない", func(ts []Task, es []Event) ([]Task, []Event) {
			es[2].Reason = nil
			return ts, es
		}},
		{"完了のイベントの理由が空白だけ", func(ts []Task, es []Event) ([]Task, []Event) {
			es[2].Reason = &blank
			return ts, es
		}},
		{"登録のイベントに理由がある", func(ts []Task, es []Event) ([]Task, []Event) {
			es[0].Reason = &blank
			return ts, es
		}},
		{"終わった状態からの変更のイベントがある", func(ts []Task, es []Event) ([]Task, []Event) {
			done := StatusDone
			r := "r"
			ts[1].Status = StatusCanceled
			return ts, append(es, Event{TaskID: "00000002", Actor: "s", From: &done, To: canceled, Reason: &r, At: 4})
		}},
		{"終わっていない状態への変更のイベントがある", func(ts []Task, es []Event) ([]Task, []Event) {
			r := "r"
			return ts, append(es, Event{TaskID: "00000001", Actor: "s", From: &todo, To: StatusTodo, Reason: &r, At: 4})
		}},
		{"変更のイベントの変更前が直前の変更後と違う", func(ts []Task, es []Event) ([]Task, []Event) {
			r := "r"
			// 00000002 は done になった後なのに、todo から canceled へのイベントを足す。
			// 1 件ずつ見ればイベントの形は正しく、最後の変更後もタスクの状態と合うので、この条件だけが破れる
			ts[1].Status = StatusCanceled
			return ts, append(es, Event{TaskID: "00000002", Actor: "s", From: &todo, To: canceled, Reason: &r, At: 4})
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tasks, events := c.breaks(validFixture())
			ok, err := ValidState(tasks, events)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				t.Error("不変条件を破る状態を true にした")
			}
		})
	}
}

func TestModelは登録と完了と中止の結果を詳細と一覧で返す(t *testing.T) {
	m := NewModel()
	if err := m.Add("00000001", "a", "本文", "s", 5); err != nil {
		t.Fatal(err)
	}
	if err := m.Add("00000002", "b", "", "s", 3); err != nil {
		t.Fatal(err)
	}
	if err := m.Add("00000001", "c", "", "s", 1); CodeOf(err) != CodeIDConflict {
		t.Errorf("重なる ID: %v", err)
	}
	if err := m.Finish("00000002", OutcomeCancel, "r", "s2", 7); err != nil {
		t.Fatal(err)
	}
	if err := m.Finish("ffffffff", OutcomeComplete, "r", "s", 8); CodeOf(err) != CodeNotFound {
		t.Errorf("存在しない ID: %v", err)
	}

	tk, evs, err := m.Show("00000002")
	if err != nil {
		t.Fatal(err)
	}
	if want := (Task{ID: "00000002", Title: "b", Status: StatusCanceled, CreatedAt: 3}); tk != want {
		t.Errorf("task: %+v", tk)
	}
	if len(evs) != 2 || evs[1].Actor != "s2" || evs[1].To != StatusCanceled {
		t.Errorf("events: %+v", evs)
	}
	if _, _, err := m.Show("ffffffff"); CodeOf(err) != CodeNotFound {
		t.Errorf("存在しない ID の詳細: %v", err)
	}

	open, _ := m.List(false)
	all, _ := m.List(true)
	if got := taskIDs(open); !reflect.DeepEqual(got, []string{"00000001"}) {
		t.Errorf("一覧: %v", got)
	}
	// 作成日時の古い順（登録した順とは逆）
	if got := taskIDs(all); !reflect.DeepEqual(got, []string{"00000002", "00000001"}) {
		t.Errorf("全部の一覧: %v", got)
	}
	tasks, _ := m.Tasks()
	if got := taskIDs(tasks); !reflect.DeepEqual(got, []string{"00000001", "00000002"}) {
		t.Errorf("登録した順: %v", got)
	}
	events, _ := m.Events()
	if len(events) != 3 {
		t.Errorf("events: %+v", events)
	}
	if !m.Valid() {
		t.Error("手本の状態で不変条件が成り立たない")
	}
}

func taskIDs(ts []Task) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

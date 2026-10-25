package app

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/TadahiroYamamura/quenta/internal/store"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

func newTestApp(t *testing.T, opts ...Option) (*App, *store.Store) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "quenta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return New(s, opts...), s
}

// ids は、呼ばれるたびに順に ID を返す
func ids(list ...string) func() (string, error) {
	i := 0
	return func() (string, error) {
		id := list[i%len(list)]
		i++
		return id, nil
	}
}

func TestRandomIDは16進8桁を返す(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}$`)
	seen := map[string]bool{}
	for range 1000 {
		id, err := RandomID()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(id) {
			t.Fatalf("形が違う: %q", id)
		}
		seen[id] = true
	}
	// 1000 回で重なりがほとんどないこと（偏った作り方なら重なる）
	if len(seen) < 990 {
		t.Errorf("1000 回中 %d 種類しかない", len(seen))
	}
}

func Test作ったIDが既にあれば作り直して登録し既存のタスクは変わらない(t *testing.T) {
	ctx := context.Background()
	a, s := newTestApp(t, WithIDGenerator(ids("0000000a", "0000000a", "0000000a", "0000000b")))
	first, err := a.Add(ctx, "最初", "本文", "s1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Add(ctx, "次", "", "s2")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != "0000000a" || second.ID != "0000000b" {
		t.Errorf("IDs: %s, %s", first.ID, second.ID)
	}
	got, _ := s.GetTask(ctx, "0000000a")
	if got != first {
		t.Errorf("既存のタスクが変わった: %+v", got)
	}
	evs, _ := s.AllEvents(ctx)
	if len(evs) != 2 {
		t.Errorf("イベントが %d 件", len(evs))
	}
}

func TestIDが既にあるものしか作れなければinternalを返し何も登録しない(t *testing.T) {
	ctx := context.Background()
	a, s := newTestApp(t, WithIDGenerator(ids("0000000a")))
	if _, err := a.Add(ctx, "最初", "", "s1"); err != nil {
		t.Fatal(err)
	}
	_, err := a.Add(ctx, "次", "", "s1")
	if got := task.CodeOf(err); got != task.CodeInternal {
		t.Fatalf("got %s (%v)", got, err)
	}
	all, _ := s.AllTasks(ctx)
	evs, _ := s.AllEvents(ctx)
	if len(all) != 1 || len(evs) != 1 {
		t.Errorf("tasks=%d events=%d", len(all), len(evs))
	}
}

func Test登録できない入力ならIDを作らずエラーを返す(t *testing.T) {
	ctx := context.Background()
	calls := 0
	a, _ := newTestApp(t, WithIDGenerator(func() (string, error) { calls++; return "0000000a", nil }))
	_, err := a.Add(ctx, " ", "", "s1")
	if task.CodeOf(err) != task.CodeTitleRequired {
		t.Fatalf("got %v", err)
	}
	// 判定で落ちたときに、作り直しを繰り返さない
	if calls != 1 {
		t.Errorf("ID を %d 回作った", calls)
	}
}

func Test完了と中止は引数の誤りをタスクを探す前に返す(t *testing.T) {
	ctx := context.Background()
	a, _ := newTestApp(t)
	cases := []struct {
		reason, actor string
		want          task.Code
	}{
		{"", "s1", task.CodeReasonRequired},
		{"r", "", task.CodeSessionRequired},
		{"r", "s1", task.CodeNotFound},
	}
	for _, c := range cases {
		_, err := a.Finish(ctx, "ffffffff", task.OutcomeComplete, c.reason, c.actor)
		if got := task.CodeOf(err); got != c.want {
			t.Errorf("reason=%q actor=%q: got %s, want %s", c.reason, c.actor, got, c.want)
		}
	}
}

func Test登録と完了の日時は現在時刻のミリ秒になる(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 25, 1, 2, 3, 456_789_000, time.UTC)
	a, s := newTestApp(t, WithClock(func() time.Time { return now }))
	tk, err := a.Add(ctx, "t", "", "s1")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-10-25T01:02:03.456Z を手で UNIX 時間のミリ秒にしたもの（20751 日 × 86400 秒 ＋ 3723.456 秒）
	const want = int64(1792890123456)
	if tk.CreatedAt != want {
		t.Errorf("CreatedAt = %d, want %d", tk.CreatedAt, want)
	}
	now = now.Add(time.Second)
	if _, err := a.Finish(ctx, tk.ID, task.OutcomeCancel, "r", "s1"); err != nil {
		t.Fatal(err)
	}
	evs, _ := s.EventsOf(ctx, tk.ID)
	if evs[0].At != want || evs[1].At != want+1000 {
		t.Errorf("events: %+v", evs)
	}
}

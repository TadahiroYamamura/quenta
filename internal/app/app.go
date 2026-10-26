// Package app は、CLI の各操作を、仕様の判定と保存の層を組み合わせて行う。
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/TadahiroYamamura/quenta/internal/store"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

// ID が重なるのは 16 進 8 桁のランダム値がぶつかったときだけなので、続けて重なるならランダム値の作り方が壊れている
const maxIDAttempts = 10

type App struct {
	store *store.Store
	now   func() time.Time
	newID func() (string, error)
}

type Option func(*App)

// WithClock は、現在時刻の取り方を差し替える（テスト用）
func WithClock(now func() time.Time) Option { return func(a *App) { a.now = now } }

// WithIDGenerator は、ID の作り方を差し替える（テスト用）
func WithIDGenerator(newID func() (string, error)) Option { return func(a *App) { a.newID = newID } }

func New(s *store.Store, opts ...Option) *App {
	a := &App{store: s, now: time.Now, newID: RandomID}
	for _, o := range opts {
		o(a)
	}
	return a
}

// RandomID は、16 進 8 桁のランダムな ID を作る
func RandomID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *App) Add(ctx context.Context, title, body, actor string) (task.Task, error) {
	now := a.now().UnixMilli()
	for range maxIDAttempts {
		id, err := a.newID()
		if err != nil {
			return task.Task{}, err
		}
		t, err := a.store.InsertTask(ctx, id, func(idTaken bool) (task.Change, error) {
			return task.NewTask(id, idTaken, title, body, actor, now)
		})
		if task.CodeOf(err) == task.CodeIDConflict {
			continue
		}
		return t, err
	}
	return task.Task{}, task.Errorf(task.CodeInternal, "%d 回続けて既にある ID ができた", maxIDAttempts)
}

func (a *App) Finish(ctx context.Context, id string, o task.Outcome, reason, actor string) (task.Task, error) {
	now := a.now().UnixMilli()
	return a.store.UpdateTask(ctx, id, func(found *task.Task) (task.Change, error) {
		return task.Finish(found, o, reason, actor, now)
	})
}

func (a *App) List(ctx context.Context, includeFinished bool) ([]task.Task, error) {
	all, err := a.store.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	out := []task.Task{}
	for _, t := range all {
		visible, err := task.IsVisible(t, includeFinished)
		if err != nil {
			return nil, err
		}
		if visible {
			out = append(out, t)
		}
	}
	return out, nil
}

func (a *App) Show(ctx context.Context, id string) (task.Task, []task.Event, error) {
	found, err := a.store.FindTask(ctx, id)
	if err != nil {
		return task.Task{}, nil, err
	}
	t, err := task.ShowTask(found)
	if err != nil {
		return task.Task{}, nil, err
	}
	evs, err := a.store.EventsOf(ctx, t.ID)
	if err != nil {
		return task.Task{}, nil, err
	}
	return t, evs, nil
}

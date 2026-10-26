// Package store は、タスクとイベントを SQLite に保存する。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	schemadb "github.com/TadahiroYamamura/quenta/db"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

// DefaultPath は、データベースのファイルの場所を返す。
// XDG Base Directory の仕様に従い、XDG_DATA_HOME が絶対パスでなければ無視する。macOS でも同じ場所にする
func DefaultPath(getenv func(string) string) (string, error) {
	dataHome := getenv("XDG_DATA_HOME")
	if !filepath.IsAbs(dataHome) {
		home := getenv("HOME")
		if home == "" {
			return "", errors.New("XDG_DATA_HOME も HOME もない")
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "quenta", "quenta.db"), nil
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("データのディレクトリを作れない: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	// 上書き（INSERT OR REPLACE）による削除でも、削除を拒むトリガーを起動させる
	q.Add("_pragma", "recursive_triggers(1)")
	// 読んでから書くトランザクション（完了・中止）で、読んだ後に別の書き手に割り込まれないようにする
	q.Add("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	ms, err := loadMigrations(schemadb.Migrations, "migrations")
	if err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(context.Background(), db, ms); err != nil {
		db.Close()
		return nil, fmt.Errorf("データベースを用意できない: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// InsertTask は、id のタスクが既にあるかを調べて build に渡し、build が返した登録の状態の変更を保存する。
// build がエラーを返せば何も保存しない。調べた後に別の書き手が同じ ID を保存していれば、一意の制約が拒み id_conflict を返す
func (s *Store) InsertTask(ctx context.Context, id string, build func(idTaken bool) (task.Change, error)) (task.Task, error) {
	var added task.Task
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		found, err := findTask(ctx, tx, id)
		if err != nil {
			return err
		}
		c, err := build(found != nil)
		if err != nil {
			return err
		}
		if !c.IsNew() || c.Task().ID != id {
			return task.Errorf(task.CodeInternal, "タスク %s の登録として、別の状態の変更を保存しようとした", id)
		}
		if err := save(ctx, tx, c); err != nil {
			return err
		}
		added = c.Task()
		return nil
	})
	return added, err
}

// UpdateTask は、id のタスクを探して change に渡し（なければ nil）、change が返した状態の変更を保存する。
// change がエラーを返せば何も保存しない
func (s *Store) UpdateTask(ctx context.Context, id string, change func(found *task.Task) (task.Change, error)) (task.Task, error) {
	var updated task.Task
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		found, err := findTask(ctx, tx, id)
		if err != nil {
			return err
		}
		c, err := change(found)
		if err != nil {
			return err
		}
		if c.IsNew() || c.Task().ID != id {
			return task.Errorf(task.CodeInternal, "タスク %s の変更として、別の状態の変更を保存しようとした", id)
		}
		if err := save(ctx, tx, c); err != nil {
			return err
		}
		updated = c.Task()
		return nil
	})
	return updated, err
}

// save は、状態の変更を保存する。tasks と events に書き込むのは、この関数だけ。
// タスクとイベントを同じトランザクションの中で続けて書き、片方だけが残らないようにする
func save(ctx context.Context, tx *sql.Tx, c task.Change) error {
	if !c.FromSpec() {
		return task.Errorf(task.CodeInternal, "仕様の関数が作っていない状態の変更を保存しようとした")
	}
	t, e := c.Task(), c.Event()
	if c.IsNew() {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO tasks (id, title, body, status, created_at) VALUES (?, ?, ?, ?, ?)`,
			t.ID, t.Title, t.Body, string(t.Status), t.CreatedAt)
		if isUniqueViolation(err) {
			return task.Errorf(task.CodeIDConflict, "ID %s が既にある", t.ID)
		}
		if err != nil {
			return err
		}
	} else {
		// 書き換えるのは状態だけ。仕様の Finish は状態以外を変えない（FinishResult）
		res, err := tx.ExecContext(ctx, `UPDATE tasks SET status = ? WHERE id = ?`, string(t.Status), t.ID)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return fmt.Errorf("タスク %s の更新が %d 行に及んだ", t.ID, n)
		}
	}
	var from, reason sql.NullString
	if e.From != nil {
		from = sql.NullString{String: string(*e.From), Valid: true}
	}
	if e.Reason != nil {
		reason = sql.NullString{String: *e.Reason, Valid: true}
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO events (task_id, actor, from_status, to_status, reason, at) VALUES (?, ?, ?, ?, ?, ?)`,
		e.TaskID, e.Actor, from, string(e.To), reason, e.At)
	return err
}

// FindTask は、id のタスクを返す。なければ nil
func (s *Store) FindTask(ctx context.Context, id string) (*task.Task, error) {
	return findTask(ctx, s.db, id)
}

// ListTasks は、すべてのタスクを、作成日時の古い順（同じなら登録した順）に返す
func (s *Store) ListTasks(ctx context.Context) ([]task.Task, error) {
	return s.queryTasks(ctx, `ORDER BY created_at, seq`)
}

// AllTasks は、すべてのタスクを登録した順に返す
func (s *Store) AllTasks(ctx context.Context) ([]task.Task, error) {
	return s.queryTasks(ctx, `ORDER BY seq`)
}

func (s *Store) queryTasks(ctx context.Context, orderBy string) ([]task.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, title, body, status, created_at FROM tasks `+orderBy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []task.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// EventsOf は、id のタスクのイベントを記録した順に返す
func (s *Store) EventsOf(ctx context.Context, id string) ([]task.Event, error) {
	return s.queryEvents(ctx, `WHERE task_id = ?`, id)
}

// AllEvents は、すべてのイベントを記録した順に返す
func (s *Store) AllEvents(ctx context.Context) ([]task.Event, error) {
	return s.queryEvents(ctx, ``)
}

func (s *Store) queryEvents(ctx context.Context, where string, args ...any) ([]task.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT task_id, actor, from_status, to_status, reason, at FROM events `+where+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []task.Event
	for rows.Next() {
		var e task.Event
		var from, reason sql.NullString
		var to string
		if err := rows.Scan(&e.TaskID, &e.Actor, &from, &to, &reason, &e.At); err != nil {
			return nil, err
		}
		if from.Valid {
			s, err := task.ParseStatus(from.String)
			if err != nil {
				return nil, err
			}
			e.From = &s
		}
		if e.To, err = task.ParseStatus(to); err != nil {
			return nil, err
		}
		if reason.Valid {
			e.Reason = &reason.String
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// テストで、別の行を返す問い合わせに差し替え、ID の確かめがはたらくことを確かめる
var findTaskQuery = `SELECT id, title, body, status, created_at FROM tasks WHERE id = ?`

// findTask は、id のタスクを返す。なければ nil。
// 返すタスクの ID が id と違えば internal にする。探し違えて別のタスクを仕様の関数に渡すと、そのタスクを完了・中止にしてしまうため
func findTask(ctx context.Context, q queryer, id string) (*task.Task, error) {
	t, err := scanTask(q.QueryRowContext(ctx, findTaskQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if t.ID != id {
		return nil, task.Errorf(task.CodeInternal, "タスク %s を探して、別のタスク %s を読んだ", id, t.ID)
	}
	return &t, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTask(r scanner) (task.Task, error) {
	var t task.Task
	var status string
	if err := r.Scan(&t.ID, &t.Title, &t.Body, &status, &t.CreatedAt); err != nil {
		return task.Task{}, err
	}
	s, err := task.ParseStatus(status)
	if err != nil {
		return task.Task{}, err
	}
	t.Status = s
	return t, nil
}

func isUniqueViolation(err error) bool {
	if e, ok := errors.AsType[*sqlite.Error](err); ok {
		return e.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
	}
	return false
}

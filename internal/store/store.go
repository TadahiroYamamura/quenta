// Package store は、SQLite のデータのファイルを開き、移行を当て、タスクとイベントを読み書きする。
// 問い合わせは spec/quenta.dfy の QueryIdTaken・QueryTask・QueryTasks に、書き込みは ApplyAdd・ApplyFinish に当たる
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/TadahiroYamamura/quenta/db"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

// BusyTimeout は、ほかのセッションの書き込みが終わるのを待つ時間
const BusyTimeout = 5 * time.Second

var (
	// ErrUnsupportedVersion は、この quenta の知らない移行が当たったデータのファイル（user_version の移行の番号が、知っている最大の番号より大きい）
	ErrUnsupportedVersion = errors.New("データのファイルに、この quenta の知らない移行が当たっている")
	// ErrBusy は、ほかのセッションの書き込みを待ちきれなかったこと
	ErrBusy = errors.New("ほかのセッションの書き込みを待ちきれなかった")
)

type Store struct {
	db *sql.DB
}

// Open は、path のデータのファイルを開く。ファイルとディレクトリがなければ作り、移行を当てる
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("データのディレクトリを作れない: %w", err)
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout("+strconv.FormatInt(BusyTimeout.Milliseconds(), 10)+")")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_txlock", "immediate")
	conn, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("データのファイルを開けない: %w", err)
	}
	s := &Store{db: conn}
	if err := s.migrate(); err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

type migration struct {
	version int
	sql     string
}

func migrations() ([]migration, error) {
	entries, err := fs.ReadDir(db.Migrations, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		n, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			return nil, fmt.Errorf("移行のファイルの名前が番号で始まらない: %s", e.Name())
		}
		b, err := fs.ReadFile(db.Migrations, "migrations/"+e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: n, sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("移行の番号が 1 から欠けなく続いていない: %d", m.version)
		}
	}
	return ms, nil
}

// migrate は、当てていない移行を、1 つの書き込みのトランザクションの中で順に当てる（複数のセッションが同時に開いても、二重に当てないため）
func (s *Store) migrate() error {
	ms, err := migrations()
	if err != nil {
		return err
	}
	return s.inTx(func(tx *sql.Tx) error {
		var v int
		if err := tx.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
			return err
		}
		if v > len(ms) {
			return ErrUnsupportedVersion
		}
		for _, m := range ms[v:] {
			if _, err := tx.Exec(m.sql); err != nil {
				return fmt.Errorf("移行 %d を当てられない: %w", m.version, err)
			}
		}
		if v < len(ms) {
			if _, err := tx.Exec("PRAGMA user_version = " + strconv.Itoa(len(ms))); err != nil {
				return err
			}
		}
		return nil
	})
}

// Tx は、1 つの書き込みのトランザクション。判定に使う事実を読むのも、書き込むのも、この中で行う
type Tx struct {
	tx *sql.Tx
}

// Write は、書き込みのトランザクションを始めてから f を呼ぶ。f がエラーを返せば、書き込みをすべて取り消す
func (s *Store) Write(f func(*Tx) error) error {
	return s.inTx(func(tx *sql.Tx) error { return f(&Tx{tx: tx}) })
}

func (s *Store) inTx(f func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return wrapBusy(err)
	}
	if err := f(tx); err != nil {
		return wrapBusy(errors.Join(err, tx.Rollback()))
	}
	return wrapBusy(tx.Commit())
}

func wrapBusy(err error) error {
	var se *sqlite.Error
	if errors.As(err, &se) && (se.Code()&0xff) == sqlite3.SQLITE_BUSY {
		return fmt.Errorf("%w: %v", ErrBusy, err)
	}
	return err
}

// IDTaken は、その ID のタスクがもうあるかを返す
func (t *Tx) IDTaken(id string) (bool, error) {
	var n int
	err := t.tx.QueryRow("SELECT count(*) FROM tasks WHERE id = ?", id).Scan(&n)
	return n > 0, err
}

// FindTask は、その ID のタスクを返す。なければ nil
func (t *Tx) FindTask(id string) (*task.Task, error) {
	rows, err := t.tx.Query("SELECT id, title, body, status, created_at FROM tasks WHERE id = ?", id)
	if err != nil {
		return nil, err
	}
	ts, err := scanTasks(rows)
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	return &ts[0], nil
}

// Tasks は、タスクの全体を返す。並びは決めない（一覧の判定が並べ替える）
func (t *Tx) Tasks() ([]task.Task, error) {
	rows, err := t.tx.Query("SELECT id, title, body, status, created_at FROM tasks")
	if err != nil {
		return nil, err
	}
	return scanTasks(rows)
}

// EventsOf は、その ID のタスクのイベントを、記録の順に返す
func (t *Tx) EventsOf(id string) (es []task.Event, err error) {
	rows, err := t.tx.Query("SELECT task_id, actor, from_status, to_status, reason, at FROM events WHERE task_id = ? ORDER BY seq", id)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var e task.Event
		var from sql.NullString
		var to string
		if err := rows.Scan(&e.TaskID, &e.Actor, &from, &to, &e.Reason, &e.At); err != nil {
			return nil, err
		}
		if e.To, err = parseStatus(to); err != nil {
			return nil, err
		}
		if from.Valid {
			f, err := parseStatus(from.String)
			if err != nil {
				return nil, err
			}
			e.From = &f
		}
		es = append(es, e)
	}
	return es, rows.Err()
}

// ApplyAdd は、タスクとイベントを 1 つずつ足す
func (t *Tx) ApplyAdd(w task.AddWrite) error {
	if _, err := t.tx.Exec("INSERT INTO tasks (id, title, body, status, created_at) VALUES (?, ?, ?, ?, ?)",
		w.Task.ID, w.Task.Title, w.Task.Body, string(w.Task.Status), w.Task.CreatedAt); err != nil {
		return err
	}
	return t.insertEvent(w.Event)
}

// ApplyFinish は、指定したタスクの状態を変え、イベントを 1 件足す
func (t *Tx) ApplyFinish(w task.FinishWrite) error {
	res, err := t.tx.Exec("UPDATE tasks SET status = ? WHERE id = ?", string(w.Status), w.ID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return fmt.Errorf("状態を変えるタスクが 1 つでない: %d", n)
	}
	return t.insertEvent(w.Event)
}

func (t *Tx) insertEvent(e task.Event) error {
	var from any
	if e.From != nil {
		from = string(*e.From)
	}
	_, err := t.tx.Exec("INSERT INTO events (task_id, actor, from_status, to_status, reason, at) VALUES (?, ?, ?, ?, ?, ?)",
		e.TaskID, e.Actor, from, string(e.To), e.Reason, e.At)
	return err
}

func scanTasks(rows *sql.Rows) (ts []task.Task, err error) {
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var tk task.Task
		var st string
		if err := rows.Scan(&tk.ID, &tk.Title, &tk.Body, &st, &tk.CreatedAt); err != nil {
			return nil, err
		}
		var err error
		if tk.Status, err = parseStatus(st); err != nil {
			return nil, err
		}
		ts = append(ts, tk)
	}
	return ts, rows.Err()
}

// parseStatus は、DB の状態の値を読む。知らない値は、DB の制約（tasks_status_known・events_status_known）が入れさせないが、読み込みの側でも拒む
func parseStatus(s string) (task.Status, error) {
	for _, st := range task.Statuses {
		if string(st) == s {
			return st, nil
		}
	}
	return "", fmt.Errorf("DB に知らない状態の値がある: %q", s)
}

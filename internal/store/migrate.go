package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"

	"github.com/TadahiroYamamura/quenta/internal/task"
)

// 移行の決まりは CLAUDE.md の「DB の構造の移行」

type migration struct {
	version int
	name    string
	sql     string
}

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// loadMigrations は、dir の移行を番号の順に返す。番号が 1 から欠けなく続いていなければエラー
func loadMigrations(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("移行のファイルの名前が決まりに合わない: %s", e.Name())
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, err
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: v, name: e.Name(), sql: string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("移行の番号が 1 から欠けなく続いていない: %d 番目が %s", i+1, m.name)
		}
	}
	if len(ms) == 0 {
		return nil, errors.New("移行がない")
	}
	return ms, nil
}

// migrate は、データベースに、まだ当たっていない移行を当てる。
// 新しい版が作ったデータベース（知らない移行まで当たっている）なら unsupported_db_version
func migrate(ctx context.Context, db *sql.DB, ms []migration) error {
	latest := len(ms)
	// 当て終わっていれば、書き込みのトランザクションを始めない（開くたびにほかのセッションの書き込みを待たせないため）
	v, err := userVersion(ctx, db)
	if err != nil {
		return err
	}
	if v == latest {
		return nil
	}
	if v > latest {
		return unsupportedVersion(v, latest)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := applyMigrations(ctx, tx, ms); err != nil {
		return errors.Join(err, tx.Rollback())
	}
	return tx.Commit()
}

func applyMigrations(ctx context.Context, tx *sql.Tx, ms []migration) error {
	latest := len(ms)
	// ほかのセッションが先に当てたかもしれないので、書き込みのトランザクションの中で読み直す
	v, err := userVersion(ctx, tx)
	if err != nil {
		return err
	}
	if v > latest {
		return unsupportedVersion(v, latest)
	}
	for _, m := range ms[v:] {
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("移行 %s を当てられない: %w", m.name, err)
		}
	}
	// PRAGMA には値を ? で渡せない。latest は移行の数で、利用者の入力ではない
	_, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", latest))
	return err
}

func userVersion(ctx context.Context, q queryer) (int, error) {
	var v int
	err := q.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v)
	return v, err
}

func unsupportedVersion(v, latest int) error {
	return task.Errorf(task.CodeUnsupportedDBVersion,
		"データベースは移行 %d まで当たっているが、この版の quenta が知っているのは移行 %d まで。新しい版の quenta を使う", v, latest)
}

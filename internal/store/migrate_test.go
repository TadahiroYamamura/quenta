package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	schemadb "github.com/TadahiroYamamura/quenta/db"
	"github.com/TadahiroYamamura/quenta/internal/task"
)

func embeddedMigrations(t *testing.T) []migration {
	t.Helper()
	ms, err := loadMigrations(schemadb.Migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// schemaObjects は、データベースの表・索引・トリガーの定義を、名前の順に、空白の違いを除いて返す
func schemaObjects(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	space := regexp.MustCompile(`\s+`)
	var out []string
	for rows.Next() {
		var typ, name string
		var def sql.NullString
		if err := rows.Scan(&typ, &name, &def); err != nil {
			t.Fatal(err)
		}
		out = append(out, typ+" "+name+": "+space.ReplaceAllString(strings.TrimSpace(def.String), " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func rawOpen(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestDB_schema_sqlは移行を全部当てた構造と一致する(t *testing.T) {
	s, _ := openTemp(t)
	fromSchema := rawOpen(t, filepath.Join(t.TempDir(), "schema.db"))
	if _, err := fromSchema.Exec(schemadb.Schema); err != nil {
		t.Fatal(err)
	}
	got, want := schemaObjects(t, s.db), schemaObjects(t, fromSchema)
	if !equalStrings(got, want) {
		t.Errorf("移行を全部当てた構造が db/schema.sql と違う:\n移行   %v\nschema %v", got, want)
	}
	if len(want) == 0 {
		t.Error("db/schema.sql から何も作られなかった")
	}
}

// 守る: empty-is-valid
func Test新しく作ったデータベースはタスクもイベントもなく移行を全部当てた番号を持つ(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	tasks, err := s.AllTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.AllEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 || len(events) != 0 {
		t.Errorf("tasks=%+v events=%+v", tasks, events)
	}
	v, err := userVersion(ctx, s.db)
	if err != nil {
		t.Fatal(err)
	}
	if want := len(embeddedMigrations(t)); v != want {
		t.Errorf("user_version = %d, want %d", v, want)
	}
}

func Test移行は1から欠けなく続く番号で決まりどおりの名前でなければ読み込まない(t *testing.T) {
	file := func(sql string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(sql)} }
	cases := []struct {
		name string
		fsys fstest.MapFS
		ok   bool
	}{
		{"1 と 2", fstest.MapFS{"m/0001_init.sql": file("a"), "m/0002_next.sql": file("b")}, true},
		{"1 が欠ける", fstest.MapFS{"m/0002_next.sql": file("b")}, false},
		{"間が欠ける", fstest.MapFS{"m/0001_init.sql": file("a"), "m/0003_x.sql": file("c")}, false},
		{"同じ番号が 2 つ", fstest.MapFS{"m/0001_init.sql": file("a"), "m/0001_dup.sql": file("b")}, false},
		{"番号が 4 桁でない", fstest.MapFS{"m/1_init.sql": file("a")}, false},
		{"SQL でないファイル", fstest.MapFS{"m/0001_init.sql": file("a"), "m/README.md": file("x")}, false},
		{"移行がない", fstest.MapFS{"m/.keep": nil}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ms, err := loadMigrations(c.fsys, "m")
			if (err == nil) != c.ok {
				t.Fatalf("err = %v", err)
			}
			if c.ok && (len(ms) != 2 || ms[0].sql != "a" || ms[1].sql != "b") {
				t.Errorf("got %+v", ms)
			}
		})
	}
}

func Test古いデータベースにはまだ当たっていない移行だけを当てて番号を上げる(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db := rawOpen(t, path)
	v1 := []migration{{1, "0001_a.sql", `CREATE TABLE a (x INTEGER);`}}
	if err := migrate(ctx, db, v1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO a VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	// 1 をもう一度当てると、表が既にあって失敗する。当て直していないことは、それが起きないことで分かる
	v2 := append(v1, migration{2, "0002_b.sql", `ALTER TABLE a ADD COLUMN y TEXT NOT NULL DEFAULT 'y'; CREATE TABLE b (z INTEGER);`})
	if err := migrate(ctx, db, v2); err != nil {
		t.Fatal(err)
	}
	var x int
	var y string
	if err := db.QueryRow(`SELECT x, y FROM a`).Scan(&x, &y); err != nil || x != 1 || y != "y" {
		t.Errorf("既存の行: x=%d y=%q err=%v", x, y, err)
	}
	if v, err := userVersion(ctx, db); err != nil || v != 2 {
		t.Errorf("user_version = %d, %v", v, err)
	}
	// 当て終わっていれば、何もしない
	if err := migrate(ctx, db, v2); err != nil {
		t.Errorf("2 回目: %v", err)
	}
}

func Test移行の途中で失敗すると前の移行も番号も戻る(t *testing.T) {
	ctx := context.Background()
	db := rawOpen(t, filepath.Join(t.TempDir(), "fail.db"))
	ms := []migration{
		{1, "0001_a.sql", `CREATE TABLE a (x INTEGER);`},
		{2, "0002_bad.sql", `CREATE TABLE a (x INTEGER);`},
	}
	if err := migrate(ctx, db, ms); err == nil {
		t.Fatal("エラーにならなかった")
	}
	if v, err := userVersion(ctx, db); err != nil || v != 0 {
		t.Errorf("user_version = %d, %v", v, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'a'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("1 番目の移行が残った: %d, %v", n, err)
	}
}

func Test新しい版が作ったデータベースは開かずにunsupported_db_versionを返し何も変えない(t *testing.T) {
	s, path := openTemp(t)
	add(t, s, "0000000a", "a", "", "s", 1)
	before := snapshotRows(t, s)
	newer := len(embeddedMigrations(t)) + 1
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, newer)); err != nil {
		t.Fatal(err)
	}
	s.Close()

	if _, err := Open(path); task.CodeOf(err) != task.CodeUnsupportedDBVersion {
		t.Fatalf("got %v", err)
	}
	// 番号を戻して開き、行が変わっていないことを確かめる
	if _, err := rawOpen(t, path).Exec(fmt.Sprintf(`PRAGMA user_version = %d`, newer-1)); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if after := snapshotRows(t, s2); !equalStrings(after, before) {
		t.Errorf("行が変わった:\n前 %v\n後 %v", before, after)
	}
}

func Test複数のセッションが新しいデータベースを同時に開いても移行を一度だけ当てる(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quenta", "quenta.db")
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	stores := make([]*Store, n)
	start := make(chan struct{})
	for i := range n {
		wg.Go(func() {
			<-start
			stores[i], errs[i] = Open(path)
		})
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("%d: %v", i, err)
		}
		if stores[i] != nil {
			stores[i].Close()
		}
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := userVersion(context.Background(), s.db)
	if err != nil || v != len(embeddedMigrations(t)) {
		t.Errorf("user_version = %d, %v", v, err)
	}
}

func Test書き込みを待つあいだにほかのセッションが移行を当て終えていたら何もしない(t *testing.T) {
	ctx := context.Background()
	db := rawOpen(t, filepath.Join(t.TempDir(), "raced.db"))
	ms := []migration{{1, "0001_a.sql", `CREATE TABLE a (x INTEGER);`}}
	// 先に開いたセッションが当て終えた状態。後のセッションは、当てる前の番号を読んだあとでトランザクションに入る
	if err := migrate(ctx, db, ms); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// もう一度当てると、表が既にあって失敗する
	if err := applyMigrations(ctx, tx, ms); err != nil {
		t.Errorf("got %v", err)
	}
}

// lockExclusive は、ほかのセッションが書き込み中の状態を作る。返す関数でロックを放す
func lockExclusive(t *testing.T, path string) func() {
	t.Helper()
	conn, err := rawOpen(t, path).Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	return func() {
		conn.ExecContext(context.Background(), `COMMIT`)
		conn.Close()
	}
}

func TestWALへの切り替えはほかのセッションのロックが放されるまで待ってやり直す(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	release := lockExclusive(t, path)
	time.AfterFunc(200*time.Millisecond, release)
	if err := useWAL(context.Background(), rawOpen(t, path), 5*time.Second); err != nil {
		t.Fatalf("got %v", err)
	}
	var mode string
	if err := rawOpen(t, path).QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q, %v", mode, err)
	}
}

func TestWALへの切り替えは待つ時間を過ぎてもロックが放されなければエラーを返す(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	release := lockExclusive(t, path)
	defer release()
	start := time.Now()
	err := useWAL(context.Background(), rawOpen(t, path), 300*time.Millisecond)
	if !isBusy(err) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d < 300*time.Millisecond {
		t.Errorf("待つ時間より早く諦めた: %v", d)
	}
}

func TestWALにできないデータベースはエラーにする(t *testing.T) {
	// メモリ上のデータベースは WAL にできず、journal_mode が memory のまま返る
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := useWAL(context.Background(), db, time.Second); err == nil {
		t.Error("エラーにならなかった")
	}
}

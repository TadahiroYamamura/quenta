-- quenta のデータベースの構造の、今の全体（#1）。
-- 人がレビューする。ここに書いた制約やトリガーの名前は、spec/guarantees.toml の constraints から指す。
-- データベースに当てるのは db/migrations/ の移行で、このファイルは実行しない。移行を全部当てた構造がこのファイルと一致することを、テストで確かめる。

-- タスク。seq は登録した順。状態（status）のほかの列は、登録した後は変わらない
CREATE TABLE tasks (
	seq        INTEGER PRIMARY KEY AUTOINCREMENT,
	id         TEXT    NOT NULL CONSTRAINT tasks_id_unique UNIQUE,
	title      TEXT    NOT NULL,
	body       TEXT    NOT NULL,
	status     TEXT    NOT NULL,
	created_at INTEGER NOT NULL -- UNIX 時間のミリ秒
);

-- 状態の変更の経緯。seq は記録した順。from_status が NULL なら登録のイベント。
-- SQLite の参照の制約（events_task_fk）は、接続ごとに foreign_keys を有効にしないとはたらかない。データベースは foreign_keys を有効にして開く
CREATE TABLE events (
	seq         INTEGER PRIMARY KEY AUTOINCREMENT,
	task_id     TEXT    NOT NULL CONSTRAINT events_task_fk REFERENCES tasks(id),
	actor       TEXT    NOT NULL,
	from_status TEXT,
	to_status   TEXT    NOT NULL,
	reason      TEXT,
	at          INTEGER NOT NULL -- UNIX 時間のミリ秒
);
CREATE INDEX events_task_id ON events(task_id);

-- タスクは消えない。データベースは recursive_triggers を有効にして開くので、上書き（INSERT OR REPLACE）による削除でも起動する
CREATE TRIGGER tasks_no_delete BEFORE DELETE ON tasks
BEGIN SELECT RAISE(ABORT, 'tasks の行は削除できない'); END;

-- 書き換えてよい列は状態だけ。値を比べるのは、状態以外の列を同じ値で書く UPDATE を妨げないため。列を足したら、ここにも足す
CREATE TRIGGER tasks_only_status_changes BEFORE UPDATE ON tasks
WHEN NEW.seq IS NOT OLD.seq OR NEW.id IS NOT OLD.id OR NEW.title IS NOT OLD.title
  OR NEW.body IS NOT OLD.body OR NEW.created_at IS NOT OLD.created_at
BEGIN SELECT RAISE(ABORT, 'tasks の状態以外の列は書き換えられない'); END;

-- 終わったタスクは変わらない。仕様の IsFinished が終わったとする状態を列挙している。状態を足したら、ここも見直す
CREATE TRIGGER tasks_finished_frozen BEFORE UPDATE ON tasks
WHEN OLD.status IN ('done', 'canceled')
BEGIN SELECT RAISE(ABORT, '終わったタスクは書き換えられない'); END;

-- イベントは追記だけ（上書きによる削除も、recursive_triggers により events_no_delete が拒む）
CREATE TRIGGER events_no_update BEFORE UPDATE ON events
BEGIN SELECT RAISE(ABORT, 'events の行は書き換えられない'); END;
CREATE TRIGGER events_no_delete BEFORE DELETE ON events
BEGIN SELECT RAISE(ABORT, 'events の行は削除できない'); END;

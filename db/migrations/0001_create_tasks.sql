-- タスクとイベントの表を作る（#24）

-- タスク。状態の値は todo・done・canceled のどれか
CREATE TABLE tasks (
  id         TEXT    NOT NULL,
  title      TEXT    NOT NULL,
  body       TEXT    NOT NULL,
  status     TEXT    NOT NULL,
  created_at INTEGER NOT NULL, -- 1970-01-01T00:00:00Z からの秒の数
  CONSTRAINT tasks_id_unique PRIMARY KEY (id),
  CONSTRAINT tasks_status_known CHECK (status IN ('todo', 'done', 'canceled'))
);

-- イベント。seq が記録の順。どのタスクのイベントも、この 1 つの表に記録の順に並ぶ
CREATE TABLE events (
  seq         INTEGER NOT NULL,
  task_id     TEXT    NOT NULL,
  actor       TEXT    NOT NULL,
  from_status TEXT,             -- 登録のイベントでは NULL（変更前なし）
  to_status   TEXT    NOT NULL,
  reason      TEXT    NOT NULL, -- 登録のイベントでは空の文字列
  at          INTEGER NOT NULL, -- 1970-01-01T00:00:00Z からの秒の数
  CONSTRAINT events_record_order PRIMARY KEY (seq AUTOINCREMENT),
  CONSTRAINT events_status_known CHECK (
    (from_status IS NULL OR from_status IN ('todo', 'done', 'canceled'))
    AND to_status IN ('todo', 'done', 'canceled')
  )
);

-- 終わった状態（done・canceled）のタスクの状態は、UPDATE での書き換えを拒む（台帳の db-finished-unchangeable）
CREATE TRIGGER tasks_finished_unchangeable
BEFORE UPDATE OF status ON tasks
WHEN OLD.status IN ('done', 'canceled')
BEGIN
  SELECT RAISE(ABORT, 'tasks_finished_unchangeable');
END;

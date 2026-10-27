import pathlib
import sys
import tempfile
import textwrap
import unittest

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import build  # noqa: E402

LEDGER = """
[[term]]
id = "task"
name = "タスク"
definition = "AI が取り組む作業の単位"

[[term]]
id = "task-id"
name = "タスクの ID"
definition = "タスクを見分ける値"
part_of = "task"

[[feature]]
id = "task-add"
name = "タスクの登録"
definition = "タスクを台帳に加える"

[[claim]]
id = "task-id-unique"
subjects = ["task-id", "task-add"]
statement = "タスクの ID に重複がない"

[[claim.evidence]]
stage = "実装"
means = "DB の制約"
detail = "tasks.id の一意の制約"

[[claim]]
id = "finished-stays"
subjects = ["task"]
statement = "終わったタスクは戻らない"
condition = "終わったタスクについて"
"""

SPEC = """
// @assurance:subject task
datatype Task = Task(id: string)

// @assurance:subject task-id
// @assurance:claim task-id-unique
predicate UniqueIds(ts: seq<Task>) { true }

// @assurance:internal 仕様の中だけで使う、値があるかないかを表す型
datatype Option<T> = None | Some(value: T)
"""

TEST = """
package store

// @assurance:evidence task-id-unique
func TestIDが重なったら作り直す(t *testing.T) {}
"""


def make_repo(files):
    root = pathlib.Path(tempfile.mkdtemp())
    for name, content in files.items():
        p = root / name
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(textwrap.dedent(content), encoding="utf-8")
    return root


def run(files):
    return build.build(make_repo(files))


class TestBuild(unittest.TestCase):
    def ok(self, files):
        md, errors = run(files)
        self.assertEqual(errors, [])
        return md

    def fails(self, files, fragment):
        md, errors = run(files)
        self.assertIsNone(md)
        self.assertTrue(any(fragment in e for e in errors), errors)

    def base(self, **override):
        files = {"spec/assurance.toml": LEDGER, "spec/task.dfy": SPEC, "internal/store/store_test.go": TEST}
        files.update(override)
        return files

    def test_台帳と印から対象ごとに主張と段階ごとの裏付けを並べる(self):
        md = self.ok(self.base())
        self.assertIn("### タスク {#task}", md)
        self.assertIn("#### タスクの ID に重複がない {#task-id-unique}", md)
        self.assertIn("| 実装 | DB の制約: tasks.id の一意の制約 |", md)
        self.assertIn("| 計画 | — |", md)

    def test_テストの印をテストの段階の裏付けとして集める(self):
        md = self.ok(self.base())
        self.assertIn("| テスト | テスト: `TestIDが重なったら作り直す`（`internal/store/store_test.go`） |", md)

    def test_仕様の中の場所を印の次の宣言から集める(self):
        md = self.ok(self.base())
        self.assertIn("- 仕様の中の場所: `spec/task.dfy` の `Task`", md)
        self.assertIn("- 書き方: 仕様（`spec/task.dfy` の `UniqueIds`）", md)

    def test_一部である対象と一部になっている対象を互いに示す(self):
        md = self.ok(self.base())
        self.assertIn("- 一部である対象: [タスク](#task)", md)
        self.assertIn("- 一部になっている対象: [タスクの ID](#task-id)", md)

    def test_複数の対象に関わる主張はほかの対象の頁からもリンクする(self):
        md = self.ok(self.base(**{"spec/assurance.toml": LEDGER.replace('subjects = ["task"]', 'subjects = ["task", "task-id"]')}))
        self.assertIn("- ほかの対象の頁に書いた主張: [終わったタスクは戻らない](#finished-stays)", md)
        self.assertEqual(md.count("{#finished-stays}"), 1)
        self.assertIn("（タスク、タスクの ID）", md)

    def test_subjectsが空なら失敗する(self):
        self.fails(self.base(**{"spec/assurance.toml": LEDGER.replace('subjects = ["task"]', 'subjects = []')}), "1 つ以上")

    def test_用語と機能を別の節に分ける(self):
        md = self.ok(self.base())
        terms, features = md.split("## 用語", 1)[1].split("## 機能", 1)
        self.assertIn("### タスク {#task}", terms)
        self.assertNotIn("{#task-add}", terms)
        self.assertIn("### タスクの登録 {#task-add}", features)

    def test_仕様に書くと決めた機能の主張に仕様の印がなければ失敗する(self):
        ledger = LEDGER.replace('definition = "タスクを台帳に加える"', 'definition = "タスクを台帳に加える"\nformal = true')
        ledger = ledger.replace('subjects = ["task"]', 'subjects = ["task", "task-add"]')
        self.fails(self.base(**{"spec/assurance.toml": ledger}), "claim 'finished-stays': 機能 'task-add' は仕様に書くと決めた")

    def test_仕様に書くと決めた機能の主張がすべて仕様にあれば通る(self):
        ledger = LEDGER.replace('definition = "タスクを台帳に加える"', 'definition = "タスクを台帳に加える"\nformal = true')
        md = self.ok(self.base(**{"spec/assurance.toml": ledger}))
        self.assertIn("| [タスクの登録](#task-add) | タスクを台帳に加える | はい | 1 | 1 |", md)

    def test_formalが真偽値でなければ失敗する(self):
        ledger = LEDGER.replace('definition = "タスクを台帳に加える"', 'definition = "タスクを台帳に加える"\nformal = "yes"')
        self.fails(self.base(**{"spec/assurance.toml": ledger}), "'formal' は true か false")

    def test_型の宣言に機能の印を付けると失敗する(self):
        self.fails(self.base(**{"spec/x.dfy": "// @assurance:subject task-add\ndatatype Add = Add\n"}), "機能 'task-add' の印")

    def test_機能の印は操作の宣言には付けられる(self):
        md = self.ok(self.base(**{"spec/x.dfy": "// @assurance:subject task-add\nmethod Add() {}\n"}))
        self.assertIn("`spec/x.dfy` の `Add`", md)

    def test_機能にpart_ofは書けない(self):
        ledger = LEDGER.replace('definition = "タスクを台帳に加える"', 'definition = "タスクを台帳に加える"\npart_of = "task"')
        self.fails(self.base(**{"spec/assurance.toml": ledger}), "'part_of'")

    def test_part_ofが機能を指すと失敗する(self):
        self.fails(self.base(**{"spec/assurance.toml": LEDGER.replace('part_of = "task"', 'part_of = "task-add"')}), "part_of 'task-add' は台帳にない用語")

    def test_用語と機能でIDが重なると失敗する(self):
        self.fails(self.base(**{"spec/assurance.toml": LEDGER.replace('id = "task-add"', 'id = "task"')}), "ID 'task'")

    def test_条件を示す(self):
        md = self.ok(self.base())
        self.assertIn("- 条件: 終わったタスクについて", md)

    def test_裏付けのない主張を一覧にする(self):
        md = self.ok(self.base())
        section = md.split("## 裏付けのない主張", 1)[1]
        self.assertIn("終わったタスクは戻らない", section)
        self.assertNotIn("タスクの ID に重複がない", section)

    def test_主張ごとに仕様に書いたか自然言語だけかを示す(self):
        md = self.ok(self.base())
        self.assertIn("- 書き方: 仕様（`spec/task.dfy` の `UniqueIds`）", md)
        self.assertIn("- 書き方: 自然言語だけ（仕様に書いていない）", md)

    def test_一覧に対象ごとの主張の数と仕様に書いた主張の数を出す(self):
        md = self.ok(self.base())
        self.assertIn("| [タスク](#task) | AI が取り組む作業の単位 | 1 | 0 |", md)
        self.assertIn("| [タスクの ID](#task-id) | タスクを見分ける値 | 1 | 1 |", md)
        self.assertIn("| [タスクの登録](#task-add) | タスクを台帳に加える | いいえ | 1 | 1 |", md)

    def test_台帳がなくても失敗せず対象がないことを示す(self):
        md = self.ok({})
        self.assertIn("台帳に、対象がまだない。", md)

    def test_台帳にない対象の印は失敗する(self):
        self.fails(self.base(**{"spec/x.dfy": "// @assurance:subject unknown\ndatatype X = X\n"}), "'unknown'")

    def test_台帳にない主張を指すテストの印は失敗する(self):
        self.fails(self.base(**{"a_test.go": "// @assurance:evidence nope\nfunc TestA(t *testing.T) {}\n"}), "'nope'")

    def test_印のない型は失敗する(self):
        self.fails(self.base(**{"spec/x.dfy": "datatype Status = Todo | Done\n"}), "型 Status に @assurance:subject の印がない")

    def test_Quintの印のない型も失敗する(self):
        self.fails(self.base(**{"spec/x.qnt": "module m {\n  type Task = { id: str }\n}\n"}), "型 Task に")

    def test_型の別名の宣言も印がなければ失敗する(self):
        self.fails(self.base(**{"spec/x.dfy": "type SessionId = string\n"}), "型 SessionId に")

    def test_理由のない補助の型の印は失敗する(self):
        self.fails(self.base(**{"spec/x.dfy": "// @assurance:internal\ndatatype X = X\n"}), "理由を書く")

    def test_決まりにない段階は失敗する(self):
        self.fails(self.base(**{"spec/assurance.toml": LEDGER.replace('stage = "実装"', 'stage = "本番"')}), "stage '本番'")

    def test_決まりにない守り方は失敗する(self):
        self.fails(self.base(**{"spec/assurance.toml": LEDGER.replace('means = "DB の制約"', 'means = "目視"')}), "means '目視'")

    def test_決まりにない項目は失敗する(self):
        ledger = LEDGER.replace('definition = "AI が取り組む作業の単位"', 'definition = "AI が取り組む作業の単位"\ndefinitoin = "x"')
        self.fails(self.base(**{"spec/assurance.toml": ledger}), "'definitoin'")

    def test_必須の項目がなければ失敗する(self):
        self.fails(self.base(**{"spec/assurance.toml": LEDGER.replace('statement = "終わったタスクは戻らない"', "")}), "'statement' がない")

    def test_台帳にない対象を指す主張は失敗する(self):
        self.fails(self.base(**{"spec/assurance.toml": LEDGER.replace('subjects = ["task"]', 'subjects = ["task", "nope"]')}), "subjects の 'nope'")

    def test_台帳にない対象を指すpart_ofは失敗する(self):
        self.fails(self.base(**{"spec/assurance.toml": LEDGER.replace('part_of = "task"', 'part_of = "nope"')}), "part_of 'nope'")

    def test_part_ofの循環は失敗する(self):
        ledger = LEDGER.replace('definition = "AI が取り組む作業の単位"', 'definition = "AI が取り組む作業の単位"\npart_of = "task-id"')
        self.fails(self.base(**{"spec/assurance.toml": ledger}), "循環")

    def test_IDの重なりは失敗する(self):
        self.fails(self.base(**{"spec/assurance.toml": LEDGER.replace('id = "finished-stays"', 'id = "task"')}), "ID 'task'")

    def test_形の正しくないIDは失敗する(self):
        self.fails(self.base(**{"spec/assurance.toml": LEDGER.replace('id = "finished-stays"', 'id = "Finished_Stays"')}), "ID は英小文字")

    def test_印の直後がテスト関数でなければ失敗する(self):
        self.fails(self.base(**{"a_test.go": "// @assurance:evidence task-id-unique\nvar x = 1\n"}), "直後がテスト関数")

    def test_宣言のない印は失敗する(self):
        self.fails(self.base(**{"spec/x.dfy": "// @assurance:claim task-id-unique\n"}), "宣言がない")

    def test_印に似た普通のコメントは印として扱わない(self):
        md = self.ok(self.base(**{"spec/x.dfy": "// 用語: task\n// @assurances are listed elsewhere\nfunction F(): int { 1 }\n"}))
        self.assertNotIn("x.dfy", md)

    def test_TOMLとして読めなければ失敗する(self):
        self.fails(self.base(**{"spec/assurance.toml": "[[subject]\n"}), "TOML として読めない")


if __name__ == "__main__":
    unittest.main()

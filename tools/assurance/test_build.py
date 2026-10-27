import pathlib
import subprocess
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
    md, errors, _ = build.build(make_repo(files))
    return md, errors


def git(root, *args):
    subprocess.run(
        ["git", "-C", str(root), "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false",
         "-c", "core.hooksPath=/dev/null", *args],
        check=True, capture_output=True,
    )


def commit_all(root, message):
    git(root, "add", "-A")
    git(root, "commit", "-q", "-m", message)


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

    def test_理由を示す(self):
        ledger = LEDGER.replace('statement = "終わったタスクは戻らない"', 'statement = "終わったタスクは戻らない"\nrationale = "終わった記録を後から書き換えられないようにするため"')
        md = self.ok(self.base(**{"spec/assurance.toml": ledger}))
        self.assertIn("- 理由: 終わった記録を後から書き換えられないようにするため", md)

    def test_E2Eの台本の印をテストの段階の裏付けとして集める(self):
        md = self.ok(self.base(**{"e2e/testdata/finished.txtar": "# @assurance:evidence finished-stays\nexec quenta list\n"}))
        self.assertIn("| テスト | E2E の台本: `e2e/testdata/finished.txtar` |", md)

    def test_E2Eの台本で証拠以外の印は失敗する(self):
        self.fails(self.base(**{"e2e/testdata/x.txtar": "# @assurance:claim finished-stays\n"}), "E2E の台本の中で使える印")

    def test_台帳にない主張を指す台本の印は失敗する(self):
        self.fails(self.base(**{"e2e/testdata/x.txtar": "# @assurance:evidence nope\n"}), "'nope'")

    def test_gitの外では由来を示さない(self):
        md = self.ok(self.base())
        self.assertNotIn("由来", md)

    def test_由来として主張を作ったIssueと変えたIssueを示す(self):
        root = make_repo(self.base())
        git(root, "init", "-q")
        commit_all(root, "(feat) タスクを足す\n\n#1 のため")
        ledger = (root / "spec/assurance.toml").read_text(encoding="utf-8")
        (root / "spec/assurance.toml").write_text(ledger.replace('condition = "終わったタスクについて"', 'condition = "完了したタスクについて"'), encoding="utf-8")
        commit_all(root, "(fix) 条件を直す #5（#1 の続き）")
        md, errors, warnings = build.build(root, "https://example.com/issues/")
        self.assertEqual((errors, warnings), ([], []))
        self.assertIn("- 由来: [#1](https://example.com/issues/1)（作成）、[#5](https://example.com/issues/5)（変更）\n", md)
        self.assertIn("- 由来: [#1](https://example.com/issues/1)（作成）\n", md)

    def test_後ろに主張を足しても前の主張の由来は変わらない(self):
        root = make_repo(self.base())
        git(root, "init", "-q")
        commit_all(root, "#1")
        with (root / "spec/assurance.toml").open("a", encoding="utf-8") as f:
            f.write('\n[[claim]]\nid = "new-claim"\nsubjects = ["task"]\nstatement = "新しい主張"\n')
        commit_all(root, "#7")
        md, errors, _ = build.build(root)
        self.assertEqual(errors, [])
        self.assertIn("#### 終わったタスクは戻らない {#finished-stays}\n\n- 由来: #1（作成）\n", md)
        self.assertIn("- 由来: #7（作成）", md)

    def test_コミットしていない主張はそう示す(self):
        root = make_repo(self.base())
        git(root, "init", "-q")
        commit_all(root, "#1")
        ledger = (root / "spec/assurance.toml").read_text(encoding="utf-8")
        (root / "spec/assurance.toml").write_text('[[claim]]\nid = "new-claim"\nsubjects = ["task"]\nstatement = "新しい主張"\n\n' + ledger, encoding="utf-8")
        md, errors, _ = build.build(root)
        self.assertEqual(errors, [])
        self.assertIn("#### 新しい主張 {#new-claim}\n\n- 由来: まだコミットしていない", md)
        self.assertIn("- 由来: #1（作成）", md)

    def test_Issueの番号のない台帳のコミットは警告する(self):
        root = make_repo(self.base())
        git(root, "init", "-q")
        commit_all(root, "(feat) タスクを足す")
        md, errors, warnings = build.build(root)
        self.assertEqual(errors, [])
        self.assertTrue(any("Issue の番号" in w for w in warnings), warnings)
        self.assertIn("- 由来: Issue の番号のあるコミットがない", md)

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


STORY_LEDGER = """
[[term]]
id = "task"
name = "タスク"
definition = "AI が取り組む作業の単位"

[[claim]]
id = "planned-all"
subjects = ["task"]
statement = "予定がそろった主張"

[[claim.evidence]]
stage = "計画"
means = "証明"
detail = "UniqueIds"

[[claim.evidence]]
stage = "実装"
means = "DB の制約"
detail = "db/migrations/0001_init.sql の tasks.id の PRIMARY KEY"

[[claim.evidence]]
stage = "テスト"
means = "テスト"
detail = "ID が重なったら作り直す"

[[claim.evidence]]
stage = "リリース"
means = "人のレビュー"
detail = "リリースの前に PO が確かめる"

[[claim]]
id = "planned-none"
subjects = ["task"]
statement = "裏付けの予定がない主張"

[[claim]]
id = "planned-lacking"
subjects = ["task"]
statement = "裏付けが欠けた主張"

[[claim.evidence]]
stage = "計画"
means = "証明"
detail = "まだない"

[[claim.evidence]]
stage = "実装"
means = "DB の制約"
detail = "db/migrations/0002_missing.sql の制約"

[[claim.evidence]]
stage = "テスト"
means = "オラクルとの突き合わせ"
detail = "まだない"
"""


class TestStoryReport(unittest.TestCase):
    def repo(self, message="(feat) 主張を足す #3", extra=None):
        files = {
            "spec/assurance.toml": STORY_LEDGER,
            "spec/task.dfy": "// @assurance:claim planned-all\npredicate UniqueIds() { true }\n",
            "internal/store/store_test.go": "// @assurance:evidence planned-all\nfunc TestA(t *testing.T) {}\n",
            "db/migrations/0001_init.sql": "CREATE TABLE tasks (id TEXT PRIMARY KEY);\n",
        }
        files.update(extra or {})
        root = make_repo(files)
        git(root, "init", "-q")
        commit_all(root, message)
        return root

    def row(self, md, statement, means):
        return next(line for line in md.splitlines() if line.startswith(f"| {statement} |") and f"| {means} |" in line)

    def test_予定どおりの裏付けは手段ごとの確かめ方を示す(self):
        md, _, errors, _ = build.story_report(self.repo(), 3)
        self.assertEqual(errors, [])
        self.assertIn("仕様の印あり（`spec/task.dfy` の `UniqueIds`）", self.row(md, "予定がそろった主張", "証明"))
        self.assertIn("PO が `db/migrations/0001_init.sql` を読んで確かめる", self.row(md, "予定がそろった主張", "DB の制約"))
        self.assertIn("印あり（`TestA`）", self.row(md, "予定がそろった主張", "テスト"))
        self.assertIn("PO が確かめる", self.row(md, "予定がそろった主張", "人のレビュー"))

    def test_欠けた裏付けを数える(self):
        md, missing, errors, _ = build.story_report(self.repo(), 3)
        self.assertEqual(errors, [])
        self.assertEqual(missing, 4)
        self.assertIn("欠け: 裏付けの予定がない", md)
        self.assertIn("欠け: 仕様に主張の印がない", self.row(md, "裏付けが欠けた主張", "証明"))
        self.assertIn("欠け: detail の先頭のファイル `db/migrations/0002_missing.sql` がない", md)
        self.assertIn("欠け: この主張の印が付いたテスト・台本がない", self.row(md, "裏付けが欠けた主張", "オラクルとの突き合わせ"))

    def test_E2Eの台本の印も実際の裏付けに数える(self):
        root = self.repo(extra={"e2e/testdata/lacking.txtar": "# @assurance:evidence planned-lacking\n"})
        md, missing, _, _ = build.story_report(root, 3)
        self.assertIn("印あり（`e2e/testdata/lacking.txtar`）", self.row(md, "裏付けが欠けた主張", "オラクルとの突き合わせ"))
        self.assertEqual(missing, 3)

    def test_ほかのIssueの主張は含めない(self):
        md, missing, errors, _ = build.story_report(self.repo(), 4)
        self.assertEqual((missing, errors), (0, []))
        self.assertIn("#4 で作った・変えた主張がない。", md)

    def test_gitの外では失敗する(self):
        md, _, errors, _ = build.story_report(make_repo({"spec/assurance.toml": STORY_LEDGER}), 3)
        self.assertIsNone(md)
        self.assertTrue(any("git の外" in e for e in errors), errors)

    def test_コミットしていない主張は含めずに警告する(self):
        root = self.repo()
        ledger = (root / "spec/assurance.toml").read_text(encoding="utf-8")
        (root / "spec/assurance.toml").write_text(ledger + '\n[[claim]]\nid = "new-claim"\nsubjects = ["task"]\nstatement = "新しい主張"\n', encoding="utf-8")
        md, _, _, warnings = build.story_report(root, 3)
        self.assertNotIn("新しい主張", md)
        self.assertTrue(any("new-claim" in w for w in warnings), warnings)


if __name__ == "__main__":
    unittest.main()

"""保証の台帳（spec/assurance.toml）と、仕様・テストの中の印から、用語集の Markdown を作る。

使い方: python3 -I tools/assurance/build.py --root <リポジトリ> --out <出力する Markdown> [--issue-url <Issue の URL の前半>]
       python3 -I tools/assurance/build.py --root <リポジトリ> --story <Issue の番号>

--story では、その Issue で作った・変えた主張について、台帳に書いた裏付けの予定と、実際の裏付けを突き合わせた表を標準出力に出す。
欠けている裏付けがあれば、終了コード 1 で終わる。

台帳や印が決まりに反していれば、理由を標準エラー出力に並べ、終了コード 1 で終わる。
主張ごとの由来（どの Issue で作られ、変わったか）は、台帳の git の履歴から集める。git の外で動かしたときは出さない。
"""

import argparse
import pathlib
import re
import subprocess
import sys
import tomllib

STAGES = ["計画", "実装", "テスト", "リリース", "運用"]
TEST_KINDS = ["モデルベーステスト", "プロパティベーステスト", "E2E テスト", "単体テスト", "DB のインターフェースのテスト", "CLI のインターフェースのテスト", "同時実行のテスト"]
MEANS = ["証明", *TEST_KINDS, "DB の制約", "実行時の確認", "監視", "人のレビュー"]
ID_PATTERN = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")

# 対象（主張が関わるもの）は、用語と機能の 2 種類。表を分けるのは、種類ごとに書ける欄が違うため
KINDS = {"term": "用語", "feature": "機能"}
SUBJECT_KEYS = {
    "term": {"id": True, "name": True, "definition": True, "part_of": False},
    "feature": {"id": True, "name": True, "definition": True, "formal": False, "rationale": False},
}
CLAIM_KEYS = {"id": True, "subjects": True, "statement": True, "rationale": False, "condition": False, "evidence": False}
EVIDENCE_KEYS = {"stage": True, "means": True, "detail": True}

MARKER = re.compile(r"^\s*//+\s*@assurance:(\w+)\b[ \t]*(.*?)\s*$")
# 仕様の中で、印を付けるべき型の宣言。Dafny の datatype・codatatype・newtype・type と、Quint の type
TYPE_DECL = re.compile(r"^\s*(?:(?:datatype|codatatype|newtype)\s+|type\s+(?=\w+\s*(?:<[^>]*>)?\s*=))(\w+)")
DECL_NAME = re.compile(
    r"^\s*(?:(?:ghost|static|opaque|pure|nondet|abstract|twostate|least|greatest)\s+)*"
    r"(?:module|datatype|codatatype|newtype|type|class|trait|predicate|function|method|lemma|const|var|val|def|action|run|temporal)\s+(\w+)"
)
TEST_FUNC = re.compile(r"^\s*func\s+(Test\w+)\s*\(")
GO_FUNC = re.compile(r"^\s*func\s+(?:\([^)]*\)\s*)?(\w+)")
# E2E の台本（testscript の .txtar）の印。台本は # で注釈を書くので、Go と印の形が違う。印は台本のファイル全体に掛かる
SCRIPT_MARKER = re.compile(r"^\s*#\s*@assurance:(\w+)\b[ \t]*(.*?)\s*$")
LEDGER = "spec/assurance.toml"
LEDGER_HEADER = re.compile(r"^\s*\[\[(term|feature|claim)\]\]")
LEDGER_ID = re.compile(r'^\s*id\s*=\s*"([^"]*)"')
ISSUE_REF = re.compile(r"(?<![\w/])#(\d+)\b")
SPEC_SUFFIXES = (".dfy", ".qnt")


class Errors(list):
    def add(self, message):
        self.append(message)


def check_keys(table, allowed, where, errors):
    for key in table:
        if key not in allowed:
            errors.add(f"{where}: 決まりにない項目 {key!r} がある")
    for key, required in allowed.items():
        if required and not str(table.get(key, "")).strip():
            errors.add(f"{where}: {key!r} がない")


def load_ledger(path, errors):
    """台帳を読み、(対象の一覧, 主張の一覧) を返す。対象には、種類を "kind" として持たせる"""
    if not path.exists():
        return [], []
    try:
        data = tomllib.loads(path.read_text(encoding="utf-8"))
    except tomllib.TOMLDecodeError as e:
        errors.add(f"{path.name}: TOML として読めない: {e}")
        return [], []
    for key in data:
        if key not in (*KINDS, "claim"):
            errors.add(f"{path.name}: 決まりにない表 {key!r} がある（term・feature・claim だけを書く）")
    subjects = []
    for kind in KINDS:
        for i, s in enumerate(data.get(kind, [])):
            where = f"{path.name} の {i + 1} 番目の {kind}"
            check_keys(s, SUBJECT_KEYS[kind], where, errors)
            if "formal" in s and not isinstance(s["formal"], bool):
                errors.add(f"{where}: 'formal' は true か false にする")
            subjects.append({**s, "kind": kind})
    claims = data.get("claim", [])
    for i, c in enumerate(claims):
        where = f"{path.name} の {i + 1} 番目の claim（{c.get('id', '?')}）"
        check_keys(c, CLAIM_KEYS, where, errors)
        subs = c.get("subjects")
        if subs is not None and (not isinstance(subs, list) or not subs or not all(isinstance(x, str) for x in subs)):
            errors.add(f"{where}: 'subjects' は対象の ID を 1 つ以上並べた配列にする")
            c["subjects"] = []
        for j, e in enumerate(c.get("evidence", [])):
            ew = f"{where} の {j + 1} 番目の evidence"
            check_keys(e, EVIDENCE_KEYS, ew, errors)
            if e.get("stage") and e["stage"] not in STAGES:
                errors.add(f"{ew}: stage {e['stage']!r} は {'・'.join(STAGES)} のどれでもない")
            if e.get("means") and e["means"] not in MEANS:
                errors.add(f"{ew}: means {e['means']!r} は {'・'.join(MEANS)} のどれでもない")
    return subjects, claims


def check_ids(subjects, claims, errors):
    seen = {}
    for item in subjects + claims:
        kind = item.get("kind", "claim")
        i = item.get("id", "")
        if not i:
            continue
        if not ID_PATTERN.match(i):
            errors.add(f"{kind} {i!r}: ID は英小文字・数字をハイフンでつないだ形にする（例: task-id-unique）")
        if i in seen:
            errors.add(f"ID {i!r} が、{seen[i]} と {kind} で重なっている")
        seen[i] = kind
    subject_ids = {s.get("id") for s in subjects}
    term_ids = {s.get("id") for s in subjects if s["kind"] == "term"}
    for s in subjects:
        p = s.get("part_of")
        if p and p not in term_ids:
            errors.add(f"term {s.get('id')!r}: part_of {p!r} は台帳にない用語")
    parents = {x.get("id"): x.get("part_of") for x in subjects}
    for s in subjects:
        chain, cur = [], s.get("id")
        # 循環を見落としたときに止まらなくならないよう、たどる回数を対象の数で打ち切る
        for _ in range(len(parents) + 1):
            if not cur:
                break
            if cur in chain:
                errors.add(f"term {s.get('id')!r}: part_of が循環している（{' → '.join(chain + [cur])}）")
                break
            chain.append(cur)
            cur = parents.get(cur)
    for c in claims:
        for x in c.get("subjects", []):
            if x not in subject_ids:
                errors.add(f"claim {c.get('id')!r}: subjects の {x!r} は台帳にない対象")


def files_under(root, predicate):
    for p in sorted(root.rglob("*")):
        rel = p.relative_to(root)
        if any(part.startswith(".") for part in rel.parts) or rel.parts[:1] in (("site",), ("vendor",)):
            continue
        if p.is_file() and predicate(p):
            yield p


def scan_specs(root, errors):
    """仕様の中の印を集める。返すのは ({(kind, id): [(ファイル, 宣言の名前)]}, 型の宣言に付いた subject の印の [(id, 場所)])"""
    marks, type_marks = {}, []
    for path in files_under(root, lambda p: p.suffix in SPEC_SUFFIXES):
        rel = path.relative_to(root).as_posix()
        pending = []  # 宣言を待っている印
        for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            m = MARKER.match(line)
            if m:
                kind, arg = m.group(1), m.group(2)
                if kind not in ("subject", "claim", "internal"):
                    errors.add(f"{rel}:{lineno}: 仕様の中で使える印は @assurance:subject・claim・internal だけ（{kind!r} がある）")
                elif kind != "internal" and not ID_PATTERN.match(arg):
                    errors.add(f"{rel}:{lineno}: @assurance:{kind} の後に ID がない、または形が正しくない")
                elif kind == "internal" and not arg:
                    errors.add(f"{rel}:{lineno}: @assurance:internal には理由を書く")
                pending.append((kind, arg, lineno))
                continue
            stripped = line.strip()
            if not stripped or stripped.startswith("//"):
                continue
            decl = DECL_NAME.match(line)
            name = decl.group(1) if decl else stripped[:40]
            type_decl = TYPE_DECL.match(line)
            if type_decl and not any(k in ("subject", "internal") for k, _, _ in pending):
                errors.add(
                    f"{rel}:{lineno}: 型 {type_decl.group(1)} に @assurance:subject の印がない"
                    "（仕様の中だけで使う補助の型なら、@assurance:internal <理由> を付ける）"
                )
            for kind, arg, _ in pending:
                if kind in ("subject", "claim"):
                    marks.setdefault((kind, arg), []).append((rel, name))
                if kind == "subject" and type_decl:
                    type_marks.append((arg, f"{rel}:{lineno}"))
            pending = []
        for kind, _, lineno in pending:
            errors.add(f"{rel}:{lineno}: @assurance:{kind} の後に宣言がない")
    return marks, type_marks


def scan_tests(root, errors):
    """テストの中の印を集める。返すのは {claim の id: [(ファイル, テスト関数の名前。E2E の台本なら None)]}"""
    evidence = {}
    for path in files_under(root, lambda p: p.suffix == ".txtar"):
        rel = path.relative_to(root).as_posix()
        for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            m = SCRIPT_MARKER.match(line)
            if not m:
                continue
            kind, arg = m.group(1), m.group(2)
            if kind != "evidence":
                errors.add(f"{rel}:{lineno}: E2E の台本の中で使える印は @assurance:evidence だけ（{kind!r} がある）")
            elif not ID_PATTERN.match(arg):
                errors.add(f"{rel}:{lineno}: @assurance:evidence の後に主張の ID がない、または形が正しくない")
            else:
                evidence.setdefault(arg, []).append((rel, None))
    for path in files_under(root, lambda p: p.name.endswith("_test.go")):
        rel = path.relative_to(root).as_posix()
        pending = []
        for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            m = MARKER.match(line)
            if m:
                kind, arg = m.group(1), m.group(2)
                if kind != "evidence":
                    errors.add(f"{rel}:{lineno}: テストの中で使える印は @assurance:evidence だけ（{kind!r} がある）")
                elif not ID_PATTERN.match(arg):
                    errors.add(f"{rel}:{lineno}: @assurance:evidence の後に主張の ID がない、または形が正しくない")
                else:
                    pending.append((arg, lineno))
                continue
            if not pending:
                continue
            f = TEST_FUNC.match(line)
            if f:
                for arg, _ in pending:
                    evidence.setdefault(arg, []).append((rel, f.group(1)))
                pending = []
            elif line.strip() and not line.strip().startswith("//"):
                for _, ln in pending:
                    errors.add(f"{rel}:{ln}: @assurance:evidence の直後がテスト関数（func Test...）でない")
                pending = []
    return evidence


def scan_checks(root, errors):
    """本番の Go のコードの中の、実行時の確認の印を集める。返すのは {claim の id: [(ファイル, 関数の名前)]}"""
    checks = {}
    for path in files_under(root, lambda p: p.suffix == ".go" and not p.name.endswith("_test.go")):
        rel = path.relative_to(root).as_posix()
        pending = []
        for lineno, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            m = MARKER.match(line)
            if m:
                kind, arg = m.group(1), m.group(2)
                if kind != "check":
                    errors.add(f"{rel}:{lineno}: テストでない Go のコードの中で使える印は @assurance:check だけ（{kind!r} がある）")
                elif not ID_PATTERN.match(arg):
                    errors.add(f"{rel}:{lineno}: @assurance:check の後に主張の ID がない、または形が正しくない")
                else:
                    pending.append((arg, lineno))
                continue
            if not pending or not line.strip() or line.strip().startswith("//"):
                continue
            f = GO_FUNC.match(line)
            for arg, ln in pending:
                if f:
                    checks.setdefault(arg, []).append((rel, f.group(1)))
                else:
                    errors.add(f"{rel}:{ln}: @assurance:check の直後が関数の宣言でない")
            pending = []
        for _, ln in pending:
            errors.add(f"{rel}:{ln}: @assurance:check の後に関数の宣言がない")
    return checks


def check_marks(subjects, claims, spec_marks, type_marks, test_evidence, checks, errors):
    subject_ids = {s.get("id") for s in subjects}
    claim_ids = {c.get("id") for c in claims}
    for (kind, i), places in spec_marks.items():
        known = subject_ids if kind == "subject" else claim_ids
        if i not in known:
            where = "、".join(f"{f} の {n}" for f, n in places)
            errors.add(f"@assurance:{kind} {i!r}（{where}）は台帳にない。台帳に足すか、印を直す")
    for kind, marks in (("evidence", test_evidence), ("check", checks)):
        for i, places in marks.items():
            if i not in claim_ids:
                where = "、".join(f"{f} の {n}" if n else f for f, n in places)
                errors.add(f"@assurance:{kind} {i!r}（{where}）は台帳の主張にない")
    feature_ids = {s["id"] for s in subjects if s["kind"] == "feature"}
    for i, where in type_marks:
        if i in feature_ids:
            errors.add(f"{where}: 型の宣言に、機能 {i!r} の印が付いている。型には用語の印を付ける")
    formal = {s["id"] for s in subjects if s["kind"] == "feature" and s.get("formal")}
    for c in claims:
        for x in c.get("subjects", []):
            if x in formal and not spec_marks.get(("claim", c.get("id"))):
                errors.add(f"claim {c.get('id')!r}: 機能 {x!r} は仕様に書くと決めたが、この主張に仕様の印がない")


def git(root, *args):
    """git を root で動かし、標準出力を返す。git がない、または失敗したら None"""
    try:
        r = subprocess.run(["git", "-C", str(root), *args], capture_output=True, text=True, encoding="utf-8")
    except OSError:
        return None
    return r.stdout if r.returncode == 0 else None


def commits(log):
    """git log --format=%x1e%h%x1f%B の出力を、[(短い ID, メッセージ)] にする（新しい順）"""
    return [tuple(rec.split("\x1f", 1)) for rec in log.split("\x1e") if "\x1f" in rec]


def collect_origins(root, warnings):
    """主張ごとの由来を、台帳の git の履歴から集める。返すのは {claim の id: [(Issue の番号, 作成か)]}。
    git の外なら None。コミットしていない主張は含まない"""
    if git(root, "rev-parse", "--is-inside-work-tree") is None:
        return None
    committed = git(root, "show", f"HEAD:{LEDGER}")
    if committed is None:
        return {}
    for h, message in commits(git(root, "log", "--format=%x1e%h%x1f%B", "--", LEDGER) or ""):
        if not ISSUE_REF.search(message):
            warnings.append(f"コミット {h}（{message.splitlines()[0] if message else ''}）は台帳を変えたが、Issue の番号（#5 など）がない")
    # 主張ごとの行の範囲を、コミットした版の台帳から求める。作業中の台帳とは行がずれうるため
    lines = committed.splitlines()
    starts = [i for i, line in enumerate(lines) if LEDGER_HEADER.match(line)] + [len(lines)]
    origins = {}
    for start, end in zip(starts, starts[1:]):
        if LEDGER_HEADER.match(lines[start]).group(1) != "claim":
            continue
        claim_id = next((m.group(1) for line in lines[start:end] if (m := LEDGER_ID.match(line))), None)
        while end > start + 1 and not lines[end - 1].strip():
            end -= 1
        log = git(root, "log", "-s", "--format=%x1e%h%x1f%B", "-L", f"{start + 1},{end}:{LEDGER}")
        found = []
        for n, (_, message) in enumerate(reversed(commits(log or ""))):
            for num in ISSUE_REF.findall(message):
                if all(num != x for x, _ in found):
                    found.append((num, n == 0))
        origins[claim_id] = found
    return origins


def render_origin(claim_id, origins, issue_url):
    if origins is None:
        return []
    if claim_id not in origins:
        return ["- 由来: まだコミットしていない"]
    if not origins[claim_id]:
        return ["- 由来: Issue の番号のあるコミットがない"]
    refs = [(f"[#{n}]({issue_url}{n})" if issue_url else f"#{n}") + ("（作成）" if first else "（変更）") for n, first in origins[claim_id]]
    return ["- 由来: " + "、".join(refs)]


def cell(entries):
    return "<br>".join(entries) if entries else "—"


def render(subjects, claims, spec_marks, test_evidence, checks, origins=None, issue_url=None):
    out = [
        "# 用語集",
        "",
        "保証の台帳（`spec/assurance.toml`）と、仕様・テストの中の印から、`make assurance` が作る頁。手で直さない。",
        "対象（用語と機能）ごとに、主張（成り立つと主張すること）と、それを裏付けるものを、開発の段階ごとに並べる。裏付けのない段階は「—」。",
        "",
    ]
    if not subjects:
        out += ["台帳に、対象がまだない。", ""]
        return "\n".join(out)
    # 主張の本文は最初の対象の頁にだけ書き、ほかの対象の頁からはリンクする（頁の中のアンカーを重ねないため）
    by_subject, linked = {}, {}
    for c in claims:
        first, *rest = c["subjects"]
        by_subject.setdefault(first, []).append(c)
        for x in rest:
            linked.setdefault(x, []).append(c)
    names = {s.get("id"): s.get("name") for s in subjects}
    unsupported = []
    for kind, label in KINDS.items():
        group = [s for s in subjects if s["kind"] == kind]
        out += [f"## {label}", ""]
        if not group:
            out += [f"台帳に、{label}がまだない。", ""]
            continue
        if kind == "feature":
            out += [f"| {label} | 定義 | 仕様に書く | 主張の数 | うち仕様に書いた主張の数 |", "|---|---|---|---|---|"]
        else:
            out += [f"| {label} | 定義 | 主張の数 | うち仕様に書いた主張の数 |", "|---|---|---|---|"]
        for s in group:
            related = by_subject.get(s["id"], []) + linked.get(s["id"], [])
            formal = [c for c in related if spec_marks.get(("claim", c["id"]))]
            extra = f" {'はい' if s.get('formal') else 'いいえ'} |" if kind == "feature" else ""
            out.append(f"| [{s['name']}](#{s['id']}) | {s['definition']} |{extra} {len(related)} | {len(formal)} |")
        out.append("")
        out += render_subjects(group, subjects, names, by_subject, linked, spec_marks, test_evidence, checks, unsupported, origins, issue_url)
    out += ["## 裏付けのない主張", ""]
    if unsupported:
        out += [f"- [{c['statement']}](#{c['id']})（{'、'.join(names.get(x, x) for x in c['subjects'])}）" for c in unsupported]
    else:
        out += ["なし"]
    out.append("")
    return "\n".join(out)


def render_subjects(group, subjects, names, by_subject, linked, spec_marks, test_evidence, checks, unsupported, origins, issue_url):
    out = []
    for s in group:
        out += [f"### {s['name']} {{#{s['id']}}}", "", s["definition"], ""]
        if s.get("rationale"):
            out += [f"- 仕様に書くと決めた理由: {s['rationale']}" if s.get("formal") else f"- 仕様に書かないと決めた理由: {s['rationale']}"]
        if s.get("part_of"):
            out += [f"- 一部である対象: [{names.get(s['part_of'], s['part_of'])}](#{s['part_of']})"]
        children = [x for x in subjects if x.get("part_of") == s["id"]]
        if children:
            out += ["- 一部になっている対象: " + "、".join(f"[{x['name']}](#{x['id']})" for x in children)]
        places = spec_marks.get(("subject", s["id"]), [])
        out += ["- 仕様の中の場所: " + ("、".join(f"`{f}` の `{n}`" for f, n in places) if places else "—")]
        if s["id"] in linked:
            out += ["- ほかの対象の頁に書いた主張: " + "、".join(f"[{c['statement']}](#{c['id']})" for c in linked[s["id"]])]
        out.append("")
        for c in by_subject.get(s["id"], []):
            out += [f"#### {c['statement']} {{#{c['id']}}}", ""]
            if c.get("rationale"):
                out += [f"- 理由: {c['rationale']}"]
            out += render_origin(c["id"], origins, issue_url)
            if c.get("condition"):
                out += [f"- 条件: {c['condition']}"]
            places = spec_marks.get(("claim", c["id"]), [])
            if places:
                out += ["- 書き方: 仕様（" + "、".join(f"`{f}` の `{n}`" for f, n in places) + "）", ""]
            else:
                out += ["- 書き方: 自然言語だけ（仕様に書いていない）", ""]
            stages = {st: [] for st in STAGES}
            for e in c.get("evidence", []):
                if e.get("stage") in stages:
                    stages[e["stage"]].append(f"{e['means']}: {e['detail']}")
            for f, n in test_evidence.get(c["id"], []):
                stages["テスト"].append(f"テスト: `{n}`（`{f}`）" if n else f"E2E の台本: `{f}`")
            for f, n in checks.get(c["id"], []):
                stages["運用"].append(f"実行時の確認: `{n}`（`{f}`）")
            if not any(stages.values()):
                unsupported.append(c)
            out += ["| 段階 | 裏付け |", "|---|---|"]
            out += [f"| {st} | {cell(stages[st])} |" for st in STAGES]
            out.append("")
    return out


# 裏付けの手段ごとの、実際にあるかの確かめ方
MARKED_MEANS = tuple(TEST_KINDS)  # テストか E2E の台本の印で確かめる
CHECK_MEANS = ("実行時の確認",)  # 本番のコードの中の、確認を行う関数の印で確かめる
FILE_MEANS = ("DB の制約",)  # detail の先頭に書いたファイルを、PO が読んで確かめる
PO_MEANS = ("人のレビュー", "監視")  # 印でもファイルでも確かめられないので、PO が確かめる


def check_evidence(root, claim, e, spec_marks, test_evidence, checks):
    """裏付けの予定 1 つについて、(欠けているか, 表に出す説明) を返す"""
    means = e.get("means")
    if means == "証明":
        places = spec_marks.get(("claim", claim["id"]), [])
        if not places:
            return True, "欠け: 仕様に主張の印がない"
        return False, "仕様の印あり（" + "、".join(f"`{f}` の `{n}`" for f, n in places) + "）。証明が通るかは `make check` で確かめる"
    if means in MARKED_MEANS:
        found = test_evidence.get(claim["id"], [])
        if not found:
            return True, "欠け: この主張の印が付いたテスト・台本がない"
        return False, "印あり（" + "、".join(f"`{n}`" if n else f"`{f}`" for f, n in found) + "）"
    if means in CHECK_MEANS:
        found = checks.get(claim["id"], [])
        if not found:
            return True, "欠け: この主張の @assurance:check の印が付いた確認の関数がない"
        return False, "印あり（" + "、".join(f"`{n}`" for _, n in found) + "）"
    if means in FILE_MEANS:
        path = (e.get("detail") or "").split(" ", 1)[0]
        if not path or not (root / path).is_file():
            return True, f"欠け: detail の先頭のファイル `{path}` がない"
        return False, f"PO が `{path}` を読んで確かめる"
    return False, "PO が確かめる"


def analyze(root, errors, warnings):
    subjects, claims = load_ledger(root / LEDGER, errors)
    check_ids(subjects, claims, errors)
    spec_marks, type_marks = scan_specs(root, errors)
    test_evidence = scan_tests(root, errors)
    checks = scan_checks(root, errors)
    check_marks(subjects, claims, spec_marks, type_marks, test_evidence, checks, errors)
    return subjects, claims, spec_marks, test_evidence, checks


def build(root, issue_url=None):
    """root の台帳・仕様・テストから、(Markdown, 決まりに反した点の一覧, 警告の一覧) を返す"""
    errors, warnings = Errors(), []
    subjects, claims, spec_marks, test_evidence, checks = analyze(root, errors, warnings)
    if errors:
        return None, list(errors), warnings
    origins = collect_origins(root, warnings)
    return render(subjects, claims, spec_marks, test_evidence, checks, origins, issue_url), [], warnings


def story_report(root, issue):
    """Issue で作った・変えた主張の、裏付けの予定と実際の突き合わせ。(Markdown, 欠けの数, 決まりに反した点, 警告) を返す"""
    errors, warnings = Errors(), []
    subjects, claims, spec_marks, test_evidence, checks = analyze(root, errors, warnings)
    if errors:
        return None, 0, list(errors), warnings
    origins = collect_origins(root, warnings)
    if origins is None:
        return None, 0, ["git の外では、Issue で作った・変えた主張を見分けられない"], warnings
    pending = [c["id"] for c in claims if c["id"] not in origins]
    if pending:
        warnings.append("コミットしていない主張は、表に含めていない: " + "、".join(pending))
    targets = [c for c in claims if any(n == str(issue) for n, _ in origins.get(c["id"], []))]
    out = [f"# #{issue} の主張の裏付け", "", "| 主張 | 段階 | 手段 | 予定 | 実際 |", "|---|---|---|---|---|"]
    missing = 0
    for c in targets:
        planned = c.get("evidence", [])
        if not planned:
            missing += 1
            out.append(f"| {c['statement']} | — | — | — | 欠け: 裏付けの予定がない |")
        for e in planned:
            lacking, actual = check_evidence(root, c, e, spec_marks, test_evidence, checks)
            missing += lacking
            out.append(f"| {c['statement']} | {e['stage']} | {e['means']} | {e['detail']} | {actual} |")
    if not targets:
        out = [f"# #{issue} の主張の裏付け", "", f"#{issue} で作った・変えた主張がない。"]
    out.append("")
    return "\n".join(out), missing, [], warnings


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", default=".")
    parser.add_argument("--out")
    parser.add_argument("--issue-url", help="Issue の番号の前に付けて、Issue へのリンクにする URL")
    parser.add_argument("--story", type=int, help="この Issue の主張の裏付けを突き合わせる")
    args = parser.parse_args()
    root = pathlib.Path(args.root).resolve()
    if args.story is not None:
        report, missing, errors, warnings = story_report(root, args.story)
        for w in warnings:
            print(f"警告: {w}", file=sys.stderr)
        for e in errors:
            print(e, file=sys.stderr)
        if report:
            print(report)
        return 1 if errors or missing else 0
    if not args.out:
        parser.error("--out か --story を指定する")
    markdown, errors, warnings = build(root, args.issue_url)
    for w in warnings:
        print(f"警告: {w}", file=sys.stderr)
    if errors:
        for e in errors:
            print(e, file=sys.stderr)
        return 1
    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(markdown, encoding="utf-8")
    return 0


if __name__ == "__main__":
    sys.exit(main())

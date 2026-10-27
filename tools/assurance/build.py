"""保証の台帳（spec/assurance.toml）と、仕様・テストの中の印から、用語集の Markdown を作る。

使い方: python3 -I tools/assurance/build.py --root <リポジトリ> --out <出力する Markdown>

台帳や印が決まりに反していれば、理由を標準エラー出力に並べ、終了コード 1 で終わる。
"""

import argparse
import pathlib
import re
import sys
import tomllib

STAGES = ["計画", "実装", "テスト", "リリース", "運用"]
MEANS = ["証明", "オラクルとの突き合わせ", "DB の制約", "実行時の確認", "テスト", "監視", "人のレビュー"]
ID_PATTERN = re.compile(r"^[a-z0-9]+(-[a-z0-9]+)*$")

SUBJECT_KEYS = {"id": True, "name": True, "definition": True, "part_of": False}
CLAIM_KEYS = {"id": True, "subject": True, "statement": True, "condition": False, "evidence": False}
EVIDENCE_KEYS = {"stage": True, "means": True, "detail": True}

MARKER = re.compile(r"^\s*//+\s*@assurance:(\w+)\b[ \t]*(.*?)\s*$")
# 仕様の中で、印を付けるべき型の宣言。Dafny の datatype・codatatype・newtype・type と、Quint の type
TYPE_DECL = re.compile(r"^\s*(?:(?:datatype|codatatype|newtype)\s+|type\s+(?=\w+\s*(?:<[^>]*>)?\s*=))(\w+)")
DECL_NAME = re.compile(
    r"^\s*(?:(?:ghost|static|opaque|pure|nondet|abstract|twostate|least|greatest)\s+)*"
    r"(?:module|datatype|codatatype|newtype|type|class|trait|predicate|function|method|lemma|const|var|val|def|action|run|temporal)\s+(\w+)"
)
TEST_FUNC = re.compile(r"^\s*func\s+(Test\w+)\s*\(")
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
    if not path.exists():
        return [], []
    try:
        data = tomllib.loads(path.read_text(encoding="utf-8"))
    except tomllib.TOMLDecodeError as e:
        errors.add(f"{path.name}: TOML として読めない: {e}")
        return [], []
    for key in data:
        if key not in ("subject", "claim"):
            errors.add(f"{path.name}: 決まりにない表 {key!r} がある（subject と claim だけを書く）")
    subjects = data.get("subject", [])
    claims = data.get("claim", [])
    for i, s in enumerate(subjects):
        check_keys(s, SUBJECT_KEYS, f"{path.name} の {i + 1} 番目の subject", errors)
    for i, c in enumerate(claims):
        where = f"{path.name} の {i + 1} 番目の claim（{c.get('id', '?')}）"
        check_keys(c, CLAIM_KEYS, where, errors)
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
    for kind, items in (("subject", subjects), ("claim", claims)):
        for item in items:
            i = item.get("id", "")
            if not i:
                continue
            if not ID_PATTERN.match(i):
                errors.add(f"{kind} {i!r}: ID は英小文字・数字をハイフンでつないだ形にする（例: task-id-unique）")
            if i in seen:
                errors.add(f"ID {i!r} が、{seen[i]} と {kind} で重なっている")
            seen[i] = kind
    subject_ids = {s.get("id") for s in subjects}
    for s in subjects:
        p = s.get("part_of")
        if p and p not in subject_ids:
            errors.add(f"subject {s.get('id')!r}: part_of {p!r} は台帳にない対象")
    parents = {x.get("id"): x.get("part_of") for x in subjects}
    for s in subjects:
        chain, cur = [], s.get("id")
        # 循環を見落としたときに止まらなくならないよう、たどる回数を対象の数で打ち切る
        for _ in range(len(parents) + 1):
            if not cur:
                break
            if cur in chain:
                errors.add(f"subject {s.get('id')!r}: part_of が循環している（{' → '.join(chain + [cur])}）")
                break
            chain.append(cur)
            cur = parents.get(cur)
    for c in claims:
        if c.get("subject") and c["subject"] not in subject_ids:
            errors.add(f"claim {c.get('id')!r}: subject {c['subject']!r} は台帳にない対象")


def files_under(root, predicate):
    for p in sorted(root.rglob("*")):
        rel = p.relative_to(root)
        if any(part.startswith(".") for part in rel.parts) or rel.parts[:1] in (("site",), ("vendor",)):
            continue
        if p.is_file() and predicate(p):
            yield p


def scan_specs(root, errors):
    """仕様の中の印を集める。返すのは {(kind, id): [(ファイル, 宣言の名前)]}"""
    marks = {}
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
            pending = []
        for kind, _, lineno in pending:
            errors.add(f"{rel}:{lineno}: @assurance:{kind} の後に宣言がない")
    return marks


def scan_tests(root, errors):
    """テストの中の印を集める。返すのは {claim の id: [(ファイル, テスト関数の名前)]}"""
    evidence = {}
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


def check_marks(subjects, claims, spec_marks, test_evidence, errors):
    subject_ids = {s.get("id") for s in subjects}
    claim_ids = {c.get("id") for c in claims}
    for (kind, i), places in spec_marks.items():
        known = subject_ids if kind == "subject" else claim_ids
        if i not in known:
            where = "、".join(f"{f} の {n}" for f, n in places)
            errors.add(f"@assurance:{kind} {i!r}（{where}）は台帳にない。台帳に足すか、印を直す")
    for i, places in test_evidence.items():
        if i not in claim_ids:
            where = "、".join(f"{f} の {n}" for f, n in places)
            errors.add(f"@assurance:evidence {i!r}（{where}）は台帳の主張にない")


def cell(entries):
    return "<br>".join(entries) if entries else "—"


def render(subjects, claims, spec_marks, test_evidence):
    out = [
        "# 用語集",
        "",
        "保証の台帳（`spec/assurance.toml`）と、仕様・テストの中の印から、`make assurance` が作る頁。手で直さない。",
        "対象（用語）ごとに、主張（成り立つと主張すること）と、それを裏付けるものを、開発の段階ごとに並べる。裏付けのない段階は「—」。",
        "",
    ]
    if not subjects:
        out += ["台帳に、対象がまだない。", ""]
        return "\n".join(out)
    by_subject = {}
    for c in claims:
        by_subject.setdefault(c.get("subject"), []).append(c)
    out += ["## 一覧", "", "| 対象 | 定義 | 主張の数 |", "|---|---|---|"]
    for s in subjects:
        out.append(f"| [{s['name']}](#{s['id']}) | {s['definition']} | {len(by_subject.get(s['id'], []))} |")
    out.append("")
    names = {s.get("id"): s.get("name") for s in subjects}
    unsupported = []
    for s in subjects:
        out += [f"## {s['name']} {{#{s['id']}}}", "", s["definition"], ""]
        if s.get("part_of"):
            out += [f"- 一部である対象: [{names.get(s['part_of'], s['part_of'])}](#{s['part_of']})"]
        children = [x for x in subjects if x.get("part_of") == s["id"]]
        if children:
            out += ["- 一部になっている対象: " + "、".join(f"[{x['name']}](#{x['id']})" for x in children)]
        places = spec_marks.get(("subject", s["id"]), [])
        out += ["- 仕様の中の場所: " + ("、".join(f"`{f}` の `{n}`" for f, n in places) if places else "—"), ""]
        for c in by_subject.get(s["id"], []):
            out += [f"### {c['statement']} {{#{c['id']}}}", ""]
            if c.get("condition"):
                out += [f"- 条件: {c['condition']}"]
            places = spec_marks.get(("claim", c["id"]), [])
            out += ["- 仕様の中の場所: " + ("、".join(f"`{f}` の `{n}`" for f, n in places) if places else "—"), ""]
            stages = {st: [] for st in STAGES}
            for e in c.get("evidence", []):
                if e.get("stage") in stages:
                    stages[e["stage"]].append(f"{e['means']}: {e['detail']}")
            for f, n in test_evidence.get(c["id"], []):
                stages["テスト"].append(f"テスト: `{n}`（`{f}`）")
            if not any(stages.values()):
                unsupported.append(c)
            out += ["| 段階 | 裏付け |", "|---|---|"]
            out += [f"| {st} | {cell(stages[st])} |" for st in STAGES]
            out.append("")
    out += ["## 裏付けのない主張", ""]
    if unsupported:
        out += [f"- [{c['statement']}](#{c['id']})（{names.get(c.get('subject'), c.get('subject'))}）" for c in unsupported]
    else:
        out += ["なし"]
    out.append("")
    return "\n".join(out)


def build(root):
    """root の台帳・仕様・テストから、(Markdown, 決まりに反した点の一覧) を返す"""
    errors = Errors()
    subjects, claims = load_ledger(root / "spec" / "assurance.toml", errors)
    check_ids(subjects, claims, errors)
    spec_marks = scan_specs(root, errors)
    test_evidence = scan_tests(root, errors)
    check_marks(subjects, claims, spec_marks, test_evidence, errors)
    if errors:
        return None, list(errors)
    return render(subjects, claims, spec_marks, test_evidence), []


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", default=".")
    parser.add_argument("--out", required=True)
    args = parser.parse_args()
    markdown, errors = build(pathlib.Path(args.root).resolve())
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

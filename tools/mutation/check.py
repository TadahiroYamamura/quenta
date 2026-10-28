"""Gremlins の結果（JSON）を、変異テストの除外の一覧（spec/mutation-exclusions.toml）と突き合わせる。

使い方: python3 -I tools/mutation/check.py --root <リポジトリ> <パッケージのディレクトリ>=<結果の JSON> ...

生き残った変異（LIVED）と、テストが通らない変異（NOT COVERED）のうち、除外の一覧にないものが 1 つでもあれば、終了コード 1 で終わる。
除外の一覧の行は、場所・変異の種類・結果の種類（status）まで一致したものにだけ当てる。ソースの文が今の行と一致しないもの、結果に現れないもの（もう要らない除外か、結果の種類が変わったもの）も、誤りとして挙げる。
時間切れ（TIMED OUT）は、テストが変異で止まらなくなったことを捕まえたものとして、殺した変異と同じに扱う。
"""

import argparse
import json
import pathlib
import sys
import tomllib

FAILING = ("LIVED", "NOT COVERED")


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--root", default=".")
    parser.add_argument("results", nargs="+", help="<パッケージのディレクトリ>=<結果の JSON>")
    args = parser.parse_args()
    root = pathlib.Path(args.root)
    exclusions = tomllib.loads((root / "spec/mutation-exclusions.toml").read_text(encoding="utf-8")).get("exclusion", [])
    errors = []
    for e in exclusions:
        lines = (root / e["file"]).read_text(encoding="utf-8").splitlines()
        if not (1 <= e["line"] <= len(lines)) or lines[e["line"] - 1].strip() != e["code"]:
            errors.append(f"除外 {e['file']}:{e['line']} の code が、今のソースの行と一致しない（行がずれたか、ソースが変わった）")
    used = set()
    for arg in args.results:
        pkg, result = arg.split("=", 1)
        data = json.loads(pathlib.Path(result).read_text(encoding="utf-8"))
        for f in data.get("files", []):
            path = f"{pkg}/{f['file_name']}"
            for m in f.get("mutations", []):
                if m["status"] not in FAILING:
                    continue
                key = (path, m["line"], m["column"], m["type"], m["status"])
                match = [i for i, e in enumerate(exclusions) if (e["file"], e["line"], e["column"], e["type"], e["status"]) == key]
                if match:
                    used.update(match)
                else:
                    errors.append(f"{m['status']} {m['type']} at {path}:{m['line']}:{m['column']}（除外の一覧にない）")
    for i, e in enumerate(exclusions):
        if i not in used:
            errors.append(f"除外 {e['file']}:{e['line']}:{e['column']} {e['type']} {e['status']} は、結果に現れない（もう要らない除外か、結果の種類が変わった）")
    for e in errors:
        print(e, file=sys.stderr)
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())

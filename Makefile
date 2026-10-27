.PHONY: assurance assurance-test docs docs-serve

PYTHON := python3 -I

# 保証の台帳と仕様・テストの印から用語集を作り、決まりに反していないかを確かめる
assurance:
	$(PYTHON) tools/assurance/build.py --root . --out docs/glossary.md --issue-url https://github.com/TadahiroYamamura/quenta/issues/

assurance-test:
	$(PYTHON) tools/assurance/test_build.py

# 手元で見るための HTML を site/ に作る（公開はしない）
docs: assurance
	mkdocs build --strict

# 手元で見る。表示された URL をブラウザで開く
docs-serve: assurance
	mkdocs serve

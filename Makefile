.PHONY: assurance assurance-test story-evidence docs docs-serve build install verify gen test mutation check check-power

PYTHON := python3 -I

# 保証の台帳と仕様・テストの印から用語集を作り、決まりに反していないかを確かめる
assurance:
	$(PYTHON) tools/assurance/build.py --root . --out docs/glossary.md --issue-url https://github.com/TadahiroYamamura/quenta/issues/

# ストーリーの完了の判断に使う。ISSUE の Issue で作った・変えた主張について、裏付けの予定と実際を突き合わせる
story-evidence:
	@test -n "$(ISSUE)" || { echo "ISSUE=<Issue の番号> を指定する" >&2; exit 1; }
	$(PYTHON) tools/assurance/build.py --root . --story $(ISSUE)

assurance-test:
	$(PYTHON) tools/assurance/test_build.py

# 手元で見るための HTML を site/ に作る（公開はしない）
docs: assurance
	mkdocs build --strict

# 手元で見る。表示された URL をブラウザで開く
docs-serve: assurance
	mkdocs serve

PREFIX ?= $(HOME)/.local
SPEC := spec/quenta.dfy
# Dafny から生成する Go。テストからだけ使い、コミットしない
GEN := internal/spec
GEN_MODULE := github.com/TadahiroYamamura/quenta/$(GEN)

build:
	go build -o bin/quenta ./cmd/quenta

install:
	mkdir -p $(PREFIX)/bin
	go build -o $(PREFIX)/bin/quenta ./cmd/quenta

# 仕様の証明
verify:
	dafny verify $(SPEC)

# 生成した System_ は、同梱したランタイムを "dafny" という名前で読み込もうとするので、同梱した場所に置き換える（読み込み先だけを変え、処理の中身には手を入れない）
gen: $(GEN)/Quenta/Quenta.go

$(GEN)/Quenta/Quenta.go: $(SPEC)
	rm -rf $(GEN) $(GEN)-go
	dafny translate go $(SPEC) --no-verify --include-runtime --output $(GEN) --go-module-name $(GEN_MODULE)
	mkdir -p $(GEN)
	mv $(GEN)-go/Quenta $(GEN)-go/System_ $(GEN)-go/dafny $(GEN)/
	rm -rf $(GEN)-go
	sed -i.bak 's#_dafny "dafny"#_dafny "$(GEN_MODULE)/dafny"#' $(GEN)/System_/System_.go && rm $(GEN)/System_/System_.go.bak

test: gen
	go test ./...

# 実装の Go の変異テスト。生き残った変異と、テストの通らない変異のうち、除外の一覧（spec/mutation-exclusions.toml）にないものが 1 つでもあれば失敗する
# Dafny から生成したコードと、テストの補助（internal/oracle）は対象にしない
MUTATION_PKGS := internal/task internal/ops internal/store internal/cli
MUTATION_OUT := .mutation

mutation: gen
	rm -rf $(MUTATION_OUT) && mkdir -p $(MUTATION_OUT)
	for p in $(MUTATION_PKGS); do go tool gremlins unleash --silent --timeout-coefficient 100 -o $(MUTATION_OUT)/$$(basename $$p).json ./$$p || exit 1; done
	$(PYTHON) tools/mutation/check.py --root . $(foreach p,$(MUTATION_PKGS),$(p)=$(MUTATION_OUT)/$(notdir $(p)).json)

check: assurance assurance-test verify test mutation

# 実行時の確認の検出力を測る（完了の判断で PO に示す。make check には入れない）
check-power: gen
	tools/mutation/check_power.sh

SHELL := bash
.SHELLFLAGS := -eu -o pipefail -c

PREFIX ?= $(HOME)/.local
MODULE := github.com/TadahiroYamamura/quenta
SPEC := spec/task.dfy
GEN_DIR := internal/spec
GEN_STAMP := $(GEN_DIR)/.generated
BIN := bin/quenta

# go.mod より新しい Go を勝手に取得させない（道具の版は flake.lock で固定している）
export GOTOOLCHAIN := local

GO_SOURCES := $(shell find cmd internal db -name '*.go' -not -path '$(GEN_DIR)/*' 2>/dev/null) db/schema.sql go.mod go.sum

.PHONY: all generate build install verify guarantees test check mutation clean

all: build

# 証明は verify で行うので、変換では省く
$(GEN_STAMP): $(SPEC)
	rm -rf $(GEN_DIR)
	tmp=$$(mktemp -d); \
	dafny translate go $(SPEC) --no-verify --include-runtime \
	  --go-module-name $(MODULE)/$(GEN_DIR) --output $$tmp/task; \
	mkdir -p $(GEN_DIR); \
	mv $$tmp/task-go/Quenta $$tmp/task-go/System_ $$tmp/task-go/dafny $(GEN_DIR)/; \
	rm -rf $$tmp
	@# Dafny 4.11 は、ランタイムに同梱の System_ にだけ --go-module-name を当てず、"dafny" を素の名前で import する
	sed -e 's|_dafny "dafny"|_dafny "$(MODULE)/$(GEN_DIR)/dafny"|' $(GEN_DIR)/System_/System_.go > $(GEN_DIR)/System_/System_.go.tmp
	mv $(GEN_DIR)/System_/System_.go.tmp $(GEN_DIR)/System_/System_.go
	touch $@

generate: $(GEN_STAMP)

$(BIN): $(GEN_STAMP) $(GO_SOURCES)
	go build -o $(BIN) ./cmd/quenta

build: $(BIN)

install: $(GEN_STAMP)
	mkdir -p $(PREFIX)/bin
	go build -o $(PREFIX)/bin/quenta ./cmd/quenta

verify:
	dafny verify $(SPEC)

test: $(GEN_STAMP)
	@unformatted=$$(gofmt -l cmd internal e2e db tools | grep -v '^$(GEN_DIR)/' || true); \
	if [ -n "$$unformatted" ]; then echo "gofmt で整形されていない: $$unformatted"; exit 1; fi
	go vet $$(go list ./... | grep -v '/$(GEN_DIR)/')
	go test ./...

# 保証の対応表（spec/guarantees.toml）を、仕様・DB の構造・テストの印と突き合わせ、人が読む表を出す
guarantees:
	go run ./tools/guarantees check

check: verify guarantees test mutation

# 保証の対応表で決めた範囲（重要度が高の行と前提の行のうち、テスト・DB の制約・実行時の確認で守る行の code）を対象にする。
# -i で、壊した箇所のパッケージだけでなく E2E を含む全テストを実行する。生き残りは spec/mutation-exclusions.toml と突き合わせる
mutation: $(GEN_STAMP)
	out=$$(mktemp); \
	go tool gremlins unleash -i --timeout-coefficient 10 --exclude-files '^$(GEN_DIR)/' \
	  $$(go run ./tools/guarantees mutation-targets) -o $$out > /dev/null; \
	go run ./tools/guarantees mutation-check $$out; \
	status=$$?; rm -f $$out; exit $$status

clean:
	rm -rf $(GEN_DIR) bin

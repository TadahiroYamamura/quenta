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

GO_SOURCES := $(shell find cmd internal -name '*.go' -not -path '$(GEN_DIR)/*' 2>/dev/null) go.mod go.sum

.PHONY: all generate build install verify test check mutation clean

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
	go vet $$(go list ./... | grep -v '/$(GEN_DIR)/')
	go test ./...

check: verify test

# AI が書いた Go を対象にする。Dafny から変換したコードとそのランタイムは外す
mutation: $(GEN_STAMP)
	go tool gremlins unleash --exclude-files '^$(GEN_DIR)/' ./...

clean:
	rm -rf $(GEN_DIR) bin

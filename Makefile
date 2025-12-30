.PHONY: build test clean dev release status help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X 'main.Version=$(VERSION)' -X 'main.Commit=$(COMMIT)' -X 'main.BuildDate=$(DATE)'

build:
	@CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -o llm-mux ./cmd/server/

test:
	@go test ./...

clean:
	@rm -rf llm-mux dist/

dev:
	@./scripts/release.sh dev --yes

status:
	@./scripts/release.sh status

release-%:
	@./scripts/release.sh release $* --yes

help:
	@echo "make build          Build binary"
	@echo "make test           Run tests"
	@echo "make clean          Remove artifacts"
	@echo "make status         Show version info"
	@echo "make dev            Dev release (Docker only)"
	@echo "make release-vX.Y.Z Full release"

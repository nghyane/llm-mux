.PHONY: build test clean release help

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

release:
	@./scripts/release.sh help || true

help:
	@echo "make build   - Build binary"
	@echo "make test    - Run tests"
	@echo "make clean   - Remove artifacts"
	@echo "make release - Show release options"

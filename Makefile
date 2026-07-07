.PHONY: all build run clean dev install version tag test cover

BIN ?= sterm
GO ?= go
LDFLAGS ?= -s -w -X main.version=$(shell grep 'version =' version.go | cut -d'"' -f2) -X main.commit=$(shell git rev-parse --short HEAD 2>/dev/null || echo none) -X main.date=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PREFIX ?= /usr/local

all: build

build:
	CGO_ENABLED=0 $(GO) build -ldflags="$(LDFLAGS)" -o $(BIN) .

run: build
	./$(BIN)

clean:
	rm -f $(BIN)

dev:
	$(GO) run .

install: build
	sudo cp $(BIN) $(PREFIX)/bin/$(BIN)

version:
	@echo "sterm $(shell cat version.go | grep 'version =' | cut -d'"' -f2)"
	@echo "commit: $(shell git rev-parse --short HEAD 2>/dev/null || echo none)"
	@echo "go: $(shell go version)"

tag:
	@if [ -z "$$(git status --porcelain)" ]; then \
		echo "staging all changes..."; \
		git add -A; \
		if [ -n "$$1" ]; then \
			V=$$1; \
		else \
			V=$$(grep 'version =' version.go | cut -d'"' -f2 | sed 's/^v//'); \
		fi; \
		git commit --allow-empty -m "release v$$V"; \
		git tag -a "v$$V" -m "Release v$$V"; \
		echo "Tagged v$$V. Run: git push --follow-tags"; \
	else \
		echo "Working tree is dirty. Commit or stash changes first."; \
		exit 1; \
	fi

test:
	$(GO) test -count=1 ./...

cover:
	$(GO) test -coverprofile=coverage.out ./... && $(GO) tool cover -html=coverage.out

integration:
	$(GO) test -count=1 -tags=integration -v ./pkg/k8s/ -run '^TestIntegration'

.PHONY: all build run clean dev

BIN ?= sterm
GO ?= go
LDFLAGS ?= -s -w

all: build

build:
	$(GO) build -ldflags="$(LDFLAGS)" -o $(BIN) .

run: build
	./$(BIN)

clean:
	rm -f $(BIN)

dev:
	$(GO) run .

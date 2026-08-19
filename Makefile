MODULE  := github.com/pressatojump/mrok
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
SERVER  ?=
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.defaultServer=$(SERVER)
PREFIX  ?= /usr/local

.PHONY: build test tidy dist install clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/mrok .

test:
	go test ./...

tidy:
	go mod tidy

install: build
	install -m 0755 bin/mrok $(PREFIX)/bin/mrok

dist: tidy
	mkdir -p dist
	@for pair in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do \
		os=$${pair%/*}; arch=$${pair#*/}; \
		ext=""; [ "$$os" = windows ] && ext=".exe"; \
		out=dist/mrok_$${os}_$${arch}$$ext; \
		echo "$$out"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $$out .; \
	done

clean:
	rm -rf bin dist

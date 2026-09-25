BINARY := bin/simpool
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/strv/simpool/internal/cli.Version=$(VERSION)

.PHONY: build install test lint fmt clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/simpool

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/simpool

test:
	go test -race ./...

lint:
	@test -z "$$(gofmt -l cmd internal)" || (gofmt -l cmd internal; echo "run make fmt"; exit 1)
	go vet ./...

fmt:
	gofmt -w cmd internal

clean:
	rm -rf bin

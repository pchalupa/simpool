BINARY := bin/simpool
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/pchalupa/simpool/internal/cli.Version=$(VERSION)

.PHONY: build install test lint fmt clean eval eval-race

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

eval-race: build
	./evals/race.sh

eval: build
	./evals/run.sh $(EVAL_ARGS)

clean:
	rm -rf bin evals/runs

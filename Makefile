BINARY := bin/simpool
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X github.com/pchalupa/simpool/internal/cli.Version=$(VERSION)

.PHONY: build install test lint fmt clean eval eval-race npm npm-pack release

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

# npm package: one CGO-free binary per macOS architecture, versioned from
# npm/package.json.
NPM_VERSION = $(shell node -p "require('./npm/package.json').version")
NPM_LDFLAGS = -X github.com/pchalupa/simpool/internal/cli.Version=v$(NPM_VERSION)

npm:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w $(NPM_LDFLAGS)" -o npm/dist/simpool-darwin-arm64 ./cmd/simpool
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w $(NPM_LDFLAGS)" -o npm/dist/simpool-darwin-x64 ./cmd/simpool

npm-pack: npm
	cd npm && npm pack

# release-it bumps npm/package.json, rebuilds the binaries, tags and publishes.
release:
	cd npm && npx release-it

clean:
	rm -rf bin evals/runs npm/dist npm/*.tgz

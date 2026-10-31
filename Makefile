.PHONY: help build install test vet fmt check tidy clean

BINARY  := bin/pikactl
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/orshih6/pikactl/internal/cli.version=$(VERSION)

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-10s\033[0m %s\n", $$1, $$2}'

build: ## Compile pikactl into bin/
	@mkdir -p bin
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/pikactl

install: ## Install pikactl into GOBIN
	go install -ldflags "$(LDFLAGS)" ./cmd/pikactl

test: ## Run the tests with the race detector
	go test ./... -race -count=1

vet: ## Run go vet
	go vet ./...

fmt: ## Format the code
	gofmt -w .

check: ## gofmt check + vet + tests - run before finishing work
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "not gofmt-clean:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	go test ./... -race -count=1

tidy: ## Tidy go.mod
	go mod tidy

clean: ## Remove build output
	rm -rf bin dist

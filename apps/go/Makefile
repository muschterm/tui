export GOTOOLCHAIN := go1.27.1

.PHONY: build test check fmt

build:
	go build -o bin/tui-go ./cmd/tui-go

test:
	go test -race ./...

check:
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; exit 1; }
	go vet ./...
	go test -race ./...
	go build -o bin/tui-go ./cmd/tui-go

fmt:
	gofmt -w cmd internal

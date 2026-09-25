export GOTOOLCHAIN := go1.27.1

BIN := bin/tui-go
PTY_HARNESSES := pty_smoke pty_navigation pty_small_screen pty_sidebar_settings pty_steering pty_path_completion pty_colors pty_terminal

.PHONY: build test check fmt fmt-check vet lint vuln pty clean

build:
	go build -o $(BIN) ./cmd/tui-go

test:
	go test -race ./...

fmt:
	gofmt -w cmd internal

fmt-check:
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; exit 1; }

vet:
	go vet ./...

# staticcheck is pinned through the go.mod tool directive.
lint: vet
	go tool staticcheck ./...

# govulncheck is pinned the same way but fetches the vulnerability database,
# so it is not part of check.
vuln:
	go tool govulncheck ./...

check: fmt-check lint test build

# OS-PTY harnesses drive the built binary in isolated application homes; see
# scripts/README.md. Artifacts go to PTY_ARTIFACTS or a fresh temporary directory.
pty: build
	@dir=$${PTY_ARTIFACTS:-$$(mktemp -d /tmp/tui-go-pty.XXXXXX)}; echo "PTY artifacts: $$dir"; \
	for harness in $(PTY_HARNESSES); do \
		echo "== $$harness"; python3 scripts/$$harness.py --binary $(BIN) --artifacts "$$dir/$$harness" || exit 1; \
	done

clean:
	rm -rf bin

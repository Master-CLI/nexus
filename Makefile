COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
NOW     := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -X main.Commit=$(COMMIT) -X main.BuildTime=$(NOW)
BINARY  := nexus

.PHONY: all build build-headless frontend clean test run run-headless

all: frontend build

# Build the full desktop app (requires Wails v3 CLI)
build: frontend
	wails3 build -ldflags "$(LDFLAGS)"

# Build headless-only binary (no GUI, no Wails dependency needed for MCP-only mode)
build-headless:
	go build -tags headless -ldflags "$(LDFLAGS)" -o $(BINARY) .

# Build frontend assets
frontend:
	cd frontend && npm install && npm run build
	@mkdir -p internal/embed/assets
	cp -r frontend/dist/* internal/embed/

# Run tests
test:
	go test ./...

# Run in headless mode (MCP-only, no GUI)
run-headless: build-headless
	./$(BINARY) --headless

clean:
	rm -rf $(BINARY) $(BINARY).exe build/
	rm -rf frontend/node_modules frontend/dist
	rm -rf internal/embed/assets internal/embed/build-meta.json

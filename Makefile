# ffm — Foxmayn Frappe Manager

BINARY    := ffm
BINARY_DIR := bin
CMD_PATH  := ./cmd/ffm

# Build-time version injection
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "none")
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/nasroykh/foxmayn_frappe_manager/internal/version.Version=$(VERSION) \
	-X github.com/nasroykh/foxmayn_frappe_manager/internal/version.Commit=$(COMMIT) \
	-X github.com/nasroykh/foxmayn_frappe_manager/internal/version.Date=$(DATE)

.DEFAULT_GOAL := ship

.PHONY: build install ship clean test tidy vet fmt help dashboard-css \
        skills-init skills-init-claude skills-init-cursor skills-init-agent

## build: compile binary to ./bin/ffm
build:
	@mkdir -p $(BINARY_DIR)
	go build -ldflags "$(LDFLAGS)" -o ./$(BINARY_DIR)/$(BINARY) $(CMD_PATH)

## install: install binary to $GOPATH/bin
install:
	go install -ldflags "$(LDFLAGS)" $(CMD_PATH)
	@mkdir -p ~/.config/ffm/

## ship: tidy deps, build, and install in one step
ship: tidy build install

## test: run all tests
test:
	go test ./...

## tidy: tidy and verify module dependencies
tidy:
	go mod tidy
	go mod verify

## vet: run go vet
vet:
	go vet ./...

## fmt: format all Go files
fmt:
	gofmt -w .

## clean: remove compiled binary
clean:
	rm -f ./$(BINARY_DIR)/$(BINARY)

## help: print this help
help:
	@sed -n 's/^##//p' $(MAKEFILE_LIST) | column -t -s ':' | sed -e 's/^/ /'

# Dashboard stylesheet. The dashboard ships a committed, minified Tailwind build
# (internal/dashboard/static/tailwind.css) instead of the Play CDN script: the
# CDN needs script-src 'unsafe-inline' and a third-party origin, and the page
# would break offline. The standalone CLI is pinned and sha256-checked; the
# digests are the release's sha256sums.txt. CI re-runs this and fails on a diff.
TAILWIND_VERSION := v3.4.19
TAILWIND_OS      := $(shell uname -s | tr '[:upper:]' '[:lower:]' | sed 's/darwin/macos/')
TAILWIND_ARCH    := $(shell uname -m | sed 's/x86_64/x64/; s/aarch64/arm64/')
TAILWIND_BIN     := $(BINARY_DIR)/tailwindcss-$(TAILWIND_VERSION)-$(TAILWIND_OS)-$(TAILWIND_ARCH)
TAILWIND_SHA_linux_x64   := 4af3198c015616ea7d6617974ec3d70d987ecc00c1ca8463b0a30fd65cc7c06e
TAILWIND_SHA_linux_arm64 := e5b2d27694daa80cc52ec29553ba2c6bd43d86bd51a9d633ed24058b9c05a676
TAILWIND_SHA_macos_x64   := a597f407e0f1f03535731f5b42f1576a8152cb5fffc2f38e754722bc0c280045
TAILWIND_SHA_macos_arm64 := 7fdeb00818b6214a337383063282b2361ecb08bbc08f8c8a7ba97ee1e2eaa4fe
TAILWIND_SHA     := $(TAILWIND_SHA_$(TAILWIND_OS)_$(TAILWIND_ARCH))
TAILWIND_URL     := https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$(TAILWIND_OS)-$(TAILWIND_ARCH)

$(TAILWIND_BIN):
	@test -n "$(TAILWIND_SHA)" || { echo "no pinned Tailwind CLI for $(TAILWIND_OS)/$(TAILWIND_ARCH)"; exit 1; }
	@mkdir -p $(BINARY_DIR)
	curl -fsSL -o $@.tmp $(TAILWIND_URL)
	@if command -v sha256sum >/dev/null 2>&1; then got=$$(sha256sum $@.tmp | cut -d' ' -f1); else got=$$(shasum -a 256 $@.tmp | cut -d' ' -f1); fi; \
	  if [ "$$got" != "$(TAILWIND_SHA)" ]; then echo "sha256 mismatch for $(TAILWIND_URL): got $$got, want $(TAILWIND_SHA)"; rm -f $@.tmp; exit 1; fi
	@chmod +x $@.tmp && mv $@.tmp $@

## dashboard-css: regenerate internal/dashboard/static/tailwind.css (pinned Tailwind CLI)
dashboard-css: $(TAILWIND_BIN)
	$(TAILWIND_BIN) -c internal/dashboard/tailwind.config.js -i internal/dashboard/tailwind.in.css -o internal/dashboard/static/tailwind.css --minify

## skills-init: initialise skills symlinks for all AI agents
skills-init:
	$(MAKE) skills-init-claude skills-init-cursor skills-init-agent

## skills-init-claude: initialise skills for Claude
skills-init-claude:
	mkdir -p .claude/skills/ && rm -rf .claude/skills/* && cd .claude/skills/ && ln -s ../../.agents/skills/*/ .
	@echo "Skills initialised for Claude"

## skills-init-cursor: initialise skills for Cursor
skills-init-cursor:
	mkdir -p .cursor/skills/ && rm -rf .cursor/skills/* && cd .cursor/skills/ && ln -s ../../.agents/skills/*/ .
	@echo "Skills initialised for Cursor"

## skills-init-agent: initialise skills for other agents (Antigravity, Gemini CLI, Codex, …)
skills-init-agent:
	mkdir -p .agent/skills/ && rm -rf .agent/skills/* && cd .agent/skills/ && ln -s ../../.agents/skills/*/ .
	@echo "Skills initialised for agent"

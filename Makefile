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

.PHONY: build install ship clean test tidy vet fmt help \
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

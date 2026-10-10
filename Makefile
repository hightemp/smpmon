SHELL := /bin/bash

BINARY         := smpmon
PKG            := github.com/hightemp/smpmon
VERSION        := $(shell cat VERSION)
TAG            := v$(VERSION)
COMMIT         := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE           := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
RELEASE_BRANCH ?= main
REMOTE         ?= origin

GOLANGCI_LINT_VERSION ?= v2.14.0
GOLANGCI_LINT         ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

LDFLAGS := -s -w \
	-X $(PKG)/internal/buildinfo.Version=$(VERSION) \
	-X $(PKG)/internal/buildinfo.Commit=$(COMMIT) \
	-X $(PKG)/internal/buildinfo.Date=$(DATE)

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

export CGO_ENABLED := 0

.DEFAULT_GOAL := build

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build binary for the host platform into bin/
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY)$(shell go env GOEXE) ./cmd/smpmon

.PHONY: build-all
build-all: ## Cross-compile for all supported platforms into bin/
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=; [ "$$os" = windows ] && ext=.exe; \
		echo "==> $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' \
			-o bin/$(BINARY)-$$os-$$arch$$ext ./cmd/smpmon; \
	done

.PHONY: run
run: build ## Build and run (pass args with ARGS="...")
	./bin/$(BINARY) $(ARGS)

.PHONY: test
test: ## Run tests
	go test ./...

.PHONY: test-race
test-race: ## Run tests with the race detector
	CGO_ENABLED=1 go test -race ./...

.PHONY: cover
cover: ## Run tests with coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: lint
lint: ## Run golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: fmt
fmt: ## Format code
	gofmt -s -w .
	go mod tidy

.PHONY: tidy
tidy: ## Tidy go.mod
	go mod tidy

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin dist coverage.out

.PHONY: version
version: ## Print version from VERSION file
	@echo $(VERSION)

.PHONY: release
release: ## Commit VERSION, tag v$(VERSION) and push (triggers the GitHub release workflow)
	@set -e; \
	if ! [[ "$(VERSION)" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$ ]]; then \
		echo "VERSION '$(VERSION)' is not valid semver"; exit 1; fi; \
	branch=$$(git rev-parse --abbrev-ref HEAD); \
	if [ "$$branch" != "$(RELEASE_BRANCH)" ]; then \
		echo "releases must be made from '$(RELEASE_BRANCH)' (current: $$branch)"; exit 1; fi; \
	if [ -n "$$(git status --porcelain -- . ':(exclude)VERSION' ':(exclude)release-notes')" ]; then \
		echo "working tree has uncommitted changes besides VERSION and release notes"; exit 1; fi; \
	if [ ! -f "release-notes/$(TAG).md" ]; then \
		echo "write release-notes/$(TAG).md first (it becomes the GitHub release description)"; exit 1; fi; \
	if git rev-parse -q --verify "refs/tags/$(TAG)" >/dev/null; then \
		echo "tag $(TAG) already exists locally"; exit 1; fi; \
	if git ls-remote --exit-code --tags $(REMOTE) "refs/tags/$(TAG)" >/dev/null 2>&1; then \
		echo "tag $(TAG) already exists on $(REMOTE)"; exit 1; fi; \
	git add VERSION "release-notes/$(TAG).md"; \
	if ! git diff --cached --quiet; then git commit -m "Release $(TAG)"; fi; \
	git tag -a "$(TAG)" -m "Release $(TAG)"; \
	git push $(REMOTE) "$(RELEASE_BRANCH)"; \
	git push $(REMOTE) "$(TAG)"; \
	echo "pushed $(TAG)"

.PHONY: screenshot
screenshot: build ## Regenerate docs/screenshot.svg from a live frame (takes ~1 min)
	./bin/$(BINARY) frame --width 160 --height 48 --interval 500ms --wait 60s | scripts/ansi2svg.py > docs/screenshot.svg

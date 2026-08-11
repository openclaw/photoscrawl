BINARY ?= bin/photoscrawl
VERSION ?= dev

.DEFAULT_GOAL := help

.PHONY: help build test fmt lint tidy-check smoke check snapshot release verify

help:
	@printf '%s\n' \
		'Available targets:' \
		'  help      Print available targets (default).' \
		'  build     Build the CLI into $(BINARY).' \
		'  test      Run the full Go test suite.' \
		'  fmt       Check Go formatting.' \
		'  lint      Run vet and dead-code checks.' \
		'  check     Run every local gate enforced by CI.' \
		'  snapshot  Build credential-free release artifacts.' \
		'  release   Refuse local publishing and print the official CI command.' \
		'  verify    Alias for check.'

build:
	@binary="$(BINARY)"; mkdir -p "$$(dirname -- "$$binary")"
	GOWORK=off go build -trimpath -ldflags "-X main.version=$(VERSION)" -o "$(BINARY)" ./cmd/photoscrawl

test:
	GOWORK=off go test -count=1 ./...

fmt:
	@set -e; \
	changed="$$(gofmt -l .)"; \
	if [ -n "$$changed" ]; then printf 'gofmt wants changes in:\n%s\n' "$$changed"; exit 1; fi

lint:
	GOWORK=off go vet ./...
	@set -e; \
	output_file="$$(mktemp)"; \
	trap 'rm -f "$$output_file"' EXIT; \
	if ! GOWORK=off go run golang.org/x/tools/cmd/deadcode@v0.45.0 -test ./... > "$$output_file"; then cat "$$output_file"; exit 1; fi; \
	if [ -s "$$output_file" ]; then cat "$$output_file"; exit 1; fi

tidy-check:
	GOWORK=off go mod verify
	GOWORK=off go mod tidy -diff

smoke:
	$(MAKE) build VERSION=ci
	@test "$$("$(BINARY)" --version)" = ci

check: tidy-check fmt lint test smoke

snapshot:
	GOWORK=off $${GORELEASER:-goreleaser} release --snapshot --clean --skip=publish

release:
	@printf '%s\n' 'local releases are disabled; official releases must use: gh workflow run release-unified.yml --repo openclaw/photoscrawl -f version=X.Y.Z' >&2
	@false

verify: check

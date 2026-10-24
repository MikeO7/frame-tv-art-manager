.PHONY: all test race e2e coverage coverage-check lint precommit vuln actionlint fmt tidy build docker bench fix agent-fix check tools clean

GOLANGCI_LINT_VERSION := 2.15.0
GOVULNCHECK_VERSION := 1.9.0
ACTIONLINT_VERSION := 1.7.12
PRE_COMMIT_VERSION := 4.7.0
COVERAGE_THRESHOLD ?= 90
GO_TEST_FLAGS ?= -shuffle=on

GOLANGCI_LINT := $(or $(wildcard $(shell go env GOPATH)/bin/golangci-lint),$(shell command -v golangci-lint 2>/dev/null),golangci-lint)
PRE_COMMIT := $(or $(shell command -v pre-commit 2>/dev/null),python3 -m pre_commit)

all: check build

test:
	go test $(GO_TEST_FLAGS) -coverprofile=coverage.out ./...

race:
	go test -race -count=1 -shuffle=on ./...

e2e:
	pnpm test:e2e

coverage: test
	go tool cover -html=coverage.out

coverage-check: test
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {sub("%", "", $$3); print $$3}'); \
	echo "Total coverage: $$total% (floor $(COVERAGE_THRESHOLD)%)"; \
	awk "BEGIN { exit !($$total >= $(COVERAGE_THRESHOLD)) }" || { echo "❌ Coverage is below the floor"; exit 1; }

lint:
	@$(GOLANGCI_LINT) version 2>/dev/null | grep -qF "version $(GOLANGCI_LINT_VERSION)" || \
		{ echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required. Run 'make tools'."; exit 1; }
	$(GOLANGCI_LINT) run --timeout 5m

precommit:
	$(PRE_COMMIT) run --all-files

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v$(GOVULNCHECK_VERSION) ./...

actionlint:
	go run github.com/rhysd/actionlint/cmd/actionlint@v$(ACTIONLINT_VERSION)

fmt:
	go fmt ./...

tidy:
	go mod tidy

build:
	go build -o frame-tv-art-manager ./cmd/frame-tv-art-manager

docker:
	docker build -t frame-tv-art-manager:local .

bench:
	go test ./internal/optimize -run '^$$' -bench . -benchmem

fix: tidy fmt
	-$(GOLANGCI_LINT) run --fix --timeout 5m

agent-fix:
	./scripts/agent-loop.sh

# Cheap structural checks run before the slower tests and vulnerability scan.
check: tidy fmt actionlint lint precommit coverage-check vuln
	@echo "✅ All local checks passed!"

# golangci-lint is built with the toolchain go.mod selects so it can load the
# module's Go version; govulncheck and actionlint run pinned via `go run`.
tools:
	GOTOOLCHAIN=$$(go env GOVERSION) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_VERSION)
	command -v pre-commit >/dev/null 2>&1 || pip install pre-commit==$(PRE_COMMIT_VERSION)

clean:
	rm -f frame-tv-art-manager coverage.out coverage.html

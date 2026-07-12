.PHONY: all test lint build docker check clean tools fmt vuln actionlint tidy coverage coverage-check precommit fix agent-fix shell-test tool-versions benchmark-optimize
.NOTPARALLEL: tidy fmt # These should run sequentially to avoid conflicts

PRE_COMMIT := $(shell command -v pre-commit 2>/dev/null)
ifeq ($(PRE_COMMIT),)
PRE_COMMIT := $(shell python3 -m pre_commit --version >/dev/null 2>&1 && echo "python3 -m pre_commit")
endif
GOLANGCI_LINT := $(shell command -v golangci-lint 2>/dev/null)
GOVULNCHECK := $(shell command -v govulncheck 2>/dev/null)

ifeq ($(GOLANGCI_LINT),)
GOLANGCI_LINT := $(shell go env GOPATH)/bin/golangci-lint
endif

ifeq ($(GOVULNCHECK),)
GOVULNCHECK := $(shell go env GOPATH)/bin/govulncheck
endif

GO_TEST_FLAGS ?= -shuffle=on
GO_TEST_VERBOSE ?= -v
GOLANGCI_LINT_VERSION := 2.12.2
GOVULNCHECK_VERSION := 1.1.4
ACTIONLINT_VERSION := 1.7.12
PRE_COMMIT_VERSION := 4.6.0
COVERAGE_THRESHOLD ?= 97

all: check build


test:
	@echo "🔍 Running tests..."
	go test $(GO_TEST_FLAGS) $(GO_TEST_VERBOSE) -coverprofile=coverage.out ./...

coverage: test
	@echo "📊 Generating coverage report..."
	go tool cover -html=coverage.out

coverage-check: test
	@echo "📈 Checking coverage threshold ($(COVERAGE_THRESHOLD)%)..."
	@TOTAL_COVERAGE=$$(go tool cover -func=coverage.out | awk '/^total:/ {sub("%", "", $$3); print $$3}'); \
	echo "Total coverage: $$TOTAL_COVERAGE%"; \
	if awk "BEGIN { exit !($$TOTAL_COVERAGE < $(COVERAGE_THRESHOLD)) }"; then \
		echo "❌ Coverage is below $(COVERAGE_THRESHOLD)%"; \
		exit 1; \
	fi
	@echo "✅ Coverage check passed!"

tool-versions:
	@version="$$($(GOLANGCI_LINT) version 2>/dev/null | head -n 1)"; \
	case "$$version" in \
		*"version $(GOLANGCI_LINT_VERSION)"*) ;; \
		*) echo "golangci-lint $(GOLANGCI_LINT_VERSION) is required; found: $${version:-not installed}. Run 'make tools'."; exit 1 ;; \
	esac

lint: tool-versions
	@echo "✨ Running linter..."
	$(GOLANGCI_LINT) run --timeout 5m

PRE_COMMIT_VENV := .venv
PRE_COMMIT_BIN := $(PRE_COMMIT_VENV)/bin/pre-commit

precommit:
	@echo "🎨 Running all pre-commit hooks..."
	@if [ -n "$(PRE_COMMIT)" ]; then \
		$(PRE_COMMIT) run --all-files; \
	elif [ -x "$(PRE_COMMIT_BIN)" ]; then \
		$(PRE_COMMIT_BIN) run --all-files; \
	else \
		echo "📦 Bootstrapping pre-commit in $(PRE_COMMIT_VENV)..."; \
		python3 -m venv $(PRE_COMMIT_VENV) && \
		$(PRE_COMMIT_VENV)/bin/pip install -q pre-commit==$(PRE_COMMIT_VERSION) && \
		$(PRE_COMMIT_BIN) run --all-files; \
	fi

vuln:
	@echo "🛡️  Checking for vulnerabilities..."
	$(GOVULNCHECK) ./...

actionlint:
	@echo "🤖 Checking GitHub Actions..."
	@command -v actionlint >/dev/null 2>&1 || { echo "actionlint $(ACTIONLINT_VERSION) is required. Run 'make tools'."; exit 1; }
	@actionlint -version | grep -F "$(ACTIONLINT_VERSION)" >/dev/null || { echo "actionlint $(ACTIONLINT_VERSION) is required. Run 'make tools'."; exit 1; }
	actionlint

shell-test:
	@bash scripts/agent-loop_test.sh

fmt:
	@echo "🧹 Formatting code..."
	go fmt ./...

tidy:
	@echo "📦 Tidying modules..."
	go mod tidy

build:
	@echo "🔨 Building binary..."
	go build -o frame-tv-art-manager ./cmd/frame-tv-art-manager

docker:
	@echo "🐳 Building Docker image (local)..."
	docker build -t frame-tv-art-manager:local .

benchmark-optimize:
	@bash scripts/benchmark-optimize.sh

fix: tidy fmt
	@echo "🔧 Auto-fixing linter issues..."
	-$(GOLANGCI_LINT) run --fix --timeout 5m

agent-fix:
	@chmod +x scripts/agent-loop.sh
	./scripts/agent-loop.sh

# Ordered for fast feedback: cheap/structural checks and the most common
# failures (formatting, actionlint, lint, anti-slop) run before the slow
# test suite and govulncheck, so failures surface as early as possible.
check: tidy fmt actionlint lint shell-test precommit coverage-check vuln
	@echo "✅ All local checks passed!"

tools:
	@echo "🛠️  Installing development tools..."
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_LINT_VERSION)
	go install golang.org/x/vuln/cmd/govulncheck@v$(GOVULNCHECK_VERSION)
	@if ! command -v actionlint &> /dev/null; then \
		if [[ "$$OSTYPE" == "darwin"* ]]; then \
			brew install actionlint; \
		else \
			go install github.com/rhysd/actionlint/cmd/actionlint@v$(ACTIONLINT_VERSION); \
		fi \
	fi
	@if [ -z "$(PRE_COMMIT)" ]; then \
		pip install pre-commit==$(PRE_COMMIT_VERSION); \
	fi

clean:
	rm -f frame-tv-art-manager
	rm -f coverage.out
	rm -f coverage.html

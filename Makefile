.PHONY: all test lint build docker check clean tools fmt vuln actionlint tidy coverage coverage-check precommit fix agent-fix
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

all: check build


test:
	@echo "🔍 Running tests..."
	go test $(GO_TEST_FLAGS) -v -coverprofile=coverage.out ./...

coverage: test
	@echo "📊 Generating coverage report..."
	go tool cover -html=coverage.out

coverage-check: test
	@echo "📈 Checking coverage threshold (50%)..."
	@TOTAL_COVERAGE=$$(go tool cover -func=coverage.out | grep total | awk '{print substr($$3, 1, length($$3)-1)}'); \
	echo "Total coverage: $$TOTAL_COVERAGE%"; \
	if [ $$(echo "$$TOTAL_COVERAGE < 50" | bc) -eq 1 ]; then \
		echo "❌ Coverage is below 50%"; \
		exit 1; \
	fi
	@echo "✅ Coverage check passed!"

lint:
	@echo "✨ Running linter..."
	$(GOLANGCI_LINT) run --timeout 5m

precommit:
	@echo "🎨 Running all pre-commit hooks..."
	@if [ -n "$(PRE_COMMIT)" ]; then \
		$(PRE_COMMIT) run --all-files; \
	else \
		echo "❌ pre-commit not found; install with: pip install pre-commit && pre-commit install"; \
		exit 1; \
	fi

vuln:
	@echo "🛡️  Checking for vulnerabilities..."
	$(GOVULNCHECK) ./...

actionlint:
	@echo "🤖 Checking GitHub Actions..."
	@if command -v actionlint &> /dev/null; then \
		actionlint; \
	else \
		echo "actionlint not found, skipping..."; \
	fi

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

fix: tidy fmt
	@echo "🔧 Auto-fixing linter issues..."
	-$(GOLANGCI_LINT) run --fix --timeout 5m

agent-fix:
	@chmod +x scripts/agent-loop.sh
	./scripts/agent-loop.sh

check: tidy fmt lint vuln actionlint coverage-check precommit
	@echo "✅ All local checks passed!"

tools:
	@echo "🛠️  Installing development tools..."
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest
	@if ! command -v actionlint &> /dev/null; then \
		if [[ "$$OSTYPE" == "darwin"* ]]; then \
			brew install actionlint; \
		else \
			go install github.com/rhysd/actionlint/cmd/actionlint@latest; \
		fi \
	fi
	@if [ -z "$(PRE_COMMIT)" ]; then \
		pip install pre-commit; \
	fi

clean:
	rm -f frame-tv-art-manager
	rm -f coverage.out

.PHONY: all test lint build docker check clean tools fmt vuln actionlint tidy
.NOTPARALLEL: tidy fmt # These should run sequentially to avoid conflicts

all: check build

test:
	@echo "🔍 Running tests..."
	go test -v -count=1 ./...

lint:
	@echo "✨ Running linter..."
	@if command -v golangci-lint &> /dev/null; then \
		golangci-lint run --timeout 5m; \
	else \
		$$(go env GOPATH)/bin/golangci-lint run --timeout 5m; \
	fi

vuln:
	@echo "🛡️  Checking for vulnerabilities..."
	@if command -v govulncheck &> /dev/null; then \
		govulncheck ./...; \
	else \
		$$(go env GOPATH)/bin/govulncheck ./...; \
	fi

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

# The 'check' target now runs test, lint, vuln, and actionlint in parallel 
# when you run 'make -j check'.
check: tidy fmt
	@$(MAKE) -j4 test lint vuln actionlint
	@echo "✅ All local checks passed!"

tools:
	@echo "🛠️  Installing development tools..."
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest
	@if ! command -v actionlint &> /dev/null; then \
		if [[ "$$OSTYPE" == "darwin"* ]]; then \
			brew install actionlint; \
		else \
			go install github.com/rhysd/actionlint/cmd/actionlint@latest; \
		fi \
	fi

clean:
	rm -f frame-tv-art-manager
	rm -f coverage.out

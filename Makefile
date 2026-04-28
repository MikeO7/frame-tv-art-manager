.PHONY: all test lint build docker check clean tools fmt vuln actionlint

all: check build

test:
	@echo "🔍 Running tests..."
	go test -v ./...

lint:
	@echo "✨ Running linter..."
	@if command -v golangci-lint &> /dev/null; then \
		golangci-lint run; \
	else \
		$$(go env GOPATH)/bin/golangci-lint run; \
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

check: tidy fmt test lint vuln actionlint
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

clean:
	rm -f frame-tv-art-manager
	rm -f coverage.out

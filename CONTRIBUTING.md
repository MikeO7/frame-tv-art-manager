# Contributing to frame-tv-art-manager

Thank you for contributing! This project maintains high standards to ensure reliability and "museum-grade" visual quality for Samsung Frame TVs.

## Development Workflow

1. **Install Tools**: Run `make tools` to install required linters and scanners.
2. **Format Code**: Run `make fmt` before committing.
3. **Run Tests**: Run `make test` to ensure everything works.
4. **Check Coverage**: Run `make coverage-check`. We require at least **50% code coverage**.
5. **Full Audit**: Run `make check` to run all tests, linters, and security scanners.

## Engineering Standards

- **Go Version**: 1.22+
- **Linting**: We use `golangci-lint` with the configuration in `.golangci.yml`.
- **Security**: Authentication tokens must be handled with restricted permissions (`0600`).
- **Dependencies**: Minimize external dependencies. We prefer the Go standard library.
- **Image Processing**: We use a custom high-fidelity pipeline. New filters should be implemented in `internal/optimize`.

## Commit Guidelines

- Use descriptive, imperative commit messages.
- Reference issue numbers if applicable.

## AI / LLM Instructions

If you are an AI assistant helping with this repository, please refer to [AI.md](./AI.md) for specific engineering directives. This file contains the master rules for this project.

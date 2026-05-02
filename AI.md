# AI Engineering & Contribution Rules

This repository follows strict production-grade engineering standards for Tizen 8.0+ Samsung Frame TV integration. Any AI assistant or contributor modifying this codebase must adhere to the following rules.

## Core Directives
1. **Always run `make check`** before considering a task complete. This runs linting (`golangci-lint`), vulnerability checks (`govulncheck`), and test coverage verification.
2. **Pre-commit Hooks**: We use `pre-commit`. Ensure hooks are installed and passing before pushing.
3. **Zero-Dependency Core**: Maintain the "zero external dependencies" philosophy for the core engine. The Go standard library is preferred.
4. **Safety First**: Never modify security-sensitive code (auth, token handling) without ensuring `make vuln` passes and permissions are strictly `0600` for tokens.

## Go Best Practices
1. **Explicit Error Handling**: Check every error. Use `%w` to wrap errors for context: `fmt.Errorf("doing thing: %w", err)`.
2. **Context Propagation**: Always accept `context.Context` as the first argument in functions performing I/O. Honor `ctx.Done()`.
3. **Structured Logging**: Use `slog`. Prefer passing key-value pairs: `logger.Info("message", "key", value)`.
4. **Interfaces for Testing**: Define small interfaces to decouple components and allow easy mocking.
5. **Table-Driven Tests**: Use table-driven testing for complex logic and edge cases.
6. **Resource Management**: Always use `defer` to close resources (e.g., `resp.Body.Close()`) immediately after successful acquisition.
7. **No Global State**: Pass dependencies (loggers, clients, config) through constructors (`New...` functions).

## Performance Patterns
- **Batch I/O**: Extract disk operations outside of loops (see `.jules/bolt.md`).
- **Parallel Processing**: Use `sync.WaitGroup` and worker pools for heavy image processing tasks in `internal/optimize`.
- **Lock Contention**: Use batch methods for mutex-protected maps to reduce lock overhead.

## Testing & Coverage
1. **Mandatory Tests**: Every new feature or bug fix MUST include unit tests.
2. **Coverage Threshold**: The repository enforces a **50% minimum code coverage**.
3. **Mocks**: Use `httptest` for network simulations (Samsung TV WebSockets, APIs).

## Technical Context
- **Resolution**: Target resolution is always 3840x2160 (4K).
- **Protocol**: Target Tizen 8.0+ (LS03D models) which requires specific WebSocket handshake sequences and "ready" events.
- **Image Processing**: Use 64-bit linear space for high-fidelity museum-grade results.

## Workflow
- **Commit Messages**: Use descriptive, imperative commit messages.
- **README**: Keep `README.md` updated with any new configuration variables.

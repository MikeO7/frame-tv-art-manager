1. **Refactor `Open` in `internal/samsung/connection.go` (Step 1)**
   - Extract the dial logic and initial handshake into a helper method `dialAndHandshake(ctx context.Context, wsURL string) (*websocket.Conn, error)` on the `connection` struct.
2. **Refactor `Open` in `internal/samsung/connection.go` (Step 2)**
   - Extract the `com.samsung.art-app` specific channel ready check into a `waitForChannelReady(conn *websocket.Conn) error` method on the `connection` struct.
3. **Refactor `Open` in `internal/samsung/connection.go` (Step 3)**
   - Update `Open` to call these new helper methods.
   - Remove the `//nolint:gocyclo,gocognit,nestif,funlen` line above `Open` since it will no longer be necessary.
4. **Verify Refactor**
   - Run `go build ./internal/samsung/...` to ensure the package still builds successfully.
   - Run `go test ./internal/samsung/...` to run the existing unit tests and ensure they still pass.
5. **Complete pre-commit steps to ensure proper testing, verification, review, and reflection are done.**
6. **Submit the change.**

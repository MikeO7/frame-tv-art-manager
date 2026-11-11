package health

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

// mcpEndpoint guards the Model Context Protocol handler. A disabled endpoint
// is indistinguishable from an unknown path. Browser cross-origin requests are
// refused before credentials are checked, and only a request bearing MCP_TOKEN
// reaches the handler. The correct token is always accepted; wrong tokens are
// counted per client address and answered 429 once the budget is spent.
func (s *server) mcpEndpoint() http.Handler {
	authenticated := http.NotFoundHandler()
	if s.mcp != nil {
		authenticated = auth.RequireBearerToken(s.verifyMCPToken, &auth.RequireBearerTokenOptions{
			// The token is a static operator secret, not an issued credential.
			AllowMissingExpiration: true,
		})(s.mcp)
	}
	guarded := http.NewCrossOriginProtection().Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token, presented := bearerToken(r); presented && !s.mcpTokenMatches(token) {
			client := clientAddress(r)
			if wait := s.authFailures.failure(client); wait > 0 {
				writeAuthChallenge(w, authLimited, wait)
				return
			}
			s.logger.Warn("rejected MCP bearer token", "client", client, "path", r.URL.Path)
		}
		authenticated.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg == nil || !s.cfg.MCPEnabled || s.mcp == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		guarded.ServeHTTP(w, r)
	})
}

// bearerToken extracts the presented bearer token the same way the MCP SDK's
// middleware does, so both always judge the same value.
func bearerToken(request *http.Request) (string, bool) {
	fields := strings.Fields(request.Header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "bearer") {
		return "", false
	}
	return fields[1], true
}

// mcpTokenMatches compares token with MCP_TOKEN in constant time. An unset
// MCP_TOKEN matches nothing.
func (s *server) mcpTokenMatches(token string) bool {
	return s.cfg.MCPToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.MCPToken)) == 1
}

// verifyMCPToken is the SDK middleware's verdict on the presented token.
// Failures are counted by mcpEndpoint before the middleware runs; the token
// itself is never logged.
func (s *server) verifyMCPToken(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	if s.mcpTokenMatches(token) {
		return &auth.TokenInfo{}, nil
	}
	return nil, auth.ErrInvalidToken
}

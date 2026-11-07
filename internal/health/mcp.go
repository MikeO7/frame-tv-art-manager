package health

import (
	"context"
	"crypto/subtle"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

// mcpEndpoint guards the Model Context Protocol handler. A disabled endpoint
// is indistinguishable from an unknown path. Browser cross-origin requests are
// refused before credentials are checked, a client over its failed-sign-in
// budget is refused before its token is compared, and only a request bearing
// MCP_TOKEN reaches the handler.
func (s *Server) mcpEndpoint() http.Handler {
	authenticated := http.NotFoundHandler()
	if s.mcp != nil {
		authenticated = auth.RequireBearerToken(s.verifyMCPToken, &auth.RequireBearerTokenOptions{
			// The token is a static operator secret, not an issued credential.
			AllowMissingExpiration: true,
		})(s.mcp)
	}
	guarded := http.NewCrossOriginProtection().Handler(authenticated)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg == nil || !s.cfg.MCPEnabled || s.mcp == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if wait := s.authFailures.retryAfter(clientAddress(r)); wait > 0 {
			writeAuthChallenge(w, authLimited, wait)
			return
		}
		guarded.ServeHTTP(w, r)
	})
}

// verifyMCPToken compares the presented bearer token with MCP_TOKEN in
// constant time. A wrong token counts against the client's failure budget;
// the token itself is never logged.
func (s *Server) verifyMCPToken(_ context.Context, token string, request *http.Request) (*auth.TokenInfo, error) {
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.MCPToken)) == 1 {
		return &auth.TokenInfo{}, nil
	}
	client := clientAddress(request)
	s.authFailures.recordFailure(client)
	s.logger.Warn("rejected MCP bearer token", "client", client, "path", request.URL.Path)
	return nil, auth.ErrInvalidToken
}

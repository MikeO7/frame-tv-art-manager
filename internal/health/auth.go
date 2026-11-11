package health

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	// authFailureLimit is how many wrong credentials one client address may
	// present per authFailureWindow before further wrong credentials are
	// answered 429. It bounds online guessing of UPLOAD_TOKEN and MCP_TOKEN.
	authFailureLimit  = 10
	authFailureWindow = time.Minute
	// authTrackedClients bounds limiter memory; expired windows are pruned
	// first, and a flood of distinct addresses evicts the oldest window.
	authTrackedClients = 4096
	uploadUsername     = "frame"
)

// authResult classifies the credentials a request presented.
type authResult int

const (
	// authAbsent means no Basic credentials were sent. It is not a failure:
	// browsers probe without credentials before showing their prompt.
	authAbsent authResult = iota
	authValid
	authInvalid
	authLimited
)

type failureWindow struct {
	start time.Time
	count int
}

// authLimiter counts wrong credentials per client address in a fixed window.
type authLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	failures map[string]failureWindow
}

func newAuthLimiter(now func() time.Time) *authLimiter {
	return &authLimiter{now: now, failures: make(map[string]failureWindow)}
}

// failure charges one wrong credential to client in a single locked step, so
// parallel guesses cannot all slip under the budget. It returns zero when the
// failure fit in the budget, or how long the client must wait when the budget
// for this window is already spent; a refused guess is not counted again.
func (l *authLimiter) failure(client string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	window, ok := l.failures[client]
	if !ok || now.Sub(window.start) >= authFailureWindow {
		if !ok && len(l.failures) >= authTrackedClients {
			l.pruneLocked(now)
		}
		window = failureWindow{start: now}
	}
	if window.count >= authFailureLimit {
		return authFailureWindow - now.Sub(window.start)
	}
	window.count++
	l.failures[client] = window
	return 0
}

func (l *authLimiter) pruneLocked(now time.Time) {
	oldestClient, oldest, found := "", now, false
	for client, window := range l.failures {
		if now.Sub(window.start) >= authFailureWindow {
			delete(l.failures, client)
			continue
		}
		if !found || window.start.Before(oldest) {
			oldestClient, oldest, found = client, window.start, true
		}
	}
	if found && len(l.failures) >= authTrackedClients {
		delete(l.failures, oldestClient)
	}
}

// authenticate checks the request's Basic credentials against UPLOAD_TOKEN.
// The credentials are compared first: correct ones are always accepted, so
// wrong guesses from the same address (a shared proxy or NAT) cannot lock the
// operator out. Only wrong credentials are counted, and a client that has
// spent its failure budget gets authLimited for further wrong guesses.
func (s *server) authenticate(request *http.Request) (authResult, time.Duration) {
	username, password, ok := request.BasicAuth()
	if !ok {
		return authAbsent, 0
	}
	userMatch := subtle.ConstantTimeCompare([]byte(username), []byte(uploadUsername))
	tokenMatch := subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.UploadToken))
	if s.cfg.UploadToken != "" && userMatch&tokenMatch == 1 {
		return authValid, 0
	}
	client := clientAddress(request)
	if wait := s.authFailures.failure(client); wait > 0 {
		return authLimited, wait
	}
	s.logger.Warn("rejected HTTP credentials", "client", client, "path", request.URL.Path)
	return authInvalid, 0
}

// writeAuthChallenge answers a request that must authenticate.
func writeAuthChallenge(w http.ResponseWriter, result authResult, wait time.Duration) {
	if result == authLimited {
		seconds := int((wait + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(seconds, 1)))
		writeJSONError(w, http.StatusTooManyRequests, "Too many failed sign-in attempts; try again later")
		return
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="Frame TV artwork upload", charset="UTF-8"`)
	writeJSONError(w, http.StatusUnauthorized, "Authentication required")
}

// clientAddress is the connection's peer IP. Forwarding headers are ignored
// because they are client-controlled; behind a reverse proxy every client
// shares the proxy's budget.
func clientAddress(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}

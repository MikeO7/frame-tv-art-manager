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
	// present per authFailureWindow before every authenticated route answers
	// 429. It bounds online guessing of UPLOAD_TOKEN.
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

// retryAfter reports how long client must wait, or zero when it may try.
func (l *authLimiter) retryAfter(client string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	window, ok := l.failures[client]
	if !ok {
		return 0
	}
	elapsed := l.now().Sub(window.start)
	if elapsed >= authFailureWindow {
		delete(l.failures, client)
		return 0
	}
	if window.count < authFailureLimit {
		return 0
	}
	return authFailureWindow - elapsed
}

func (l *authLimiter) recordFailure(client string) {
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
	window.count++
	l.failures[client] = window
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
// A client over its failure budget is refused before the token is compared,
// so a correct guess cannot be confirmed while rate limited.
func (s *Server) authenticate(request *http.Request) (authResult, time.Duration) {
	client := clientAddress(request)
	if wait := s.authFailures.retryAfter(client); wait > 0 {
		return authLimited, wait
	}
	username, password, ok := request.BasicAuth()
	if !ok {
		return authAbsent, 0
	}
	userMatch := subtle.ConstantTimeCompare([]byte(username), []byte(uploadUsername))
	tokenMatch := subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.UploadToken))
	if userMatch&tokenMatch == 1 {
		return authValid, 0
	}
	s.authFailures.recordFailure(client)
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

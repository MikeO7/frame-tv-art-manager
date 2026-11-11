package assistant

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	gosync "sync"
	"testing"
)

// mapResolver answers from a fixed table and counts lookups per host.
type mapResolver struct {
	mu      gosync.Mutex
	answers map[string][]string
	lookups map[string]int
}

func (resolver *mapResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if resolver.lookups == nil {
		resolver.lookups = map[string]int{}
	}
	resolver.lookups[host]++
	answers, ok := resolver.answers[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	addresses := make([]netip.Addr, 0, len(answers))
	for _, answer := range answers {
		addresses = append(addresses, netip.MustParseAddr(answer))
	}
	return addresses, nil
}

// loopbackOrPublic admits the httptest server while keeping every other
// private range blocked, so tests exercise the real policy.
func loopbackOrPublic(addr netip.Addr) bool {
	return addr.IsLoopback() || publicAddress(addr)
}

type imageServer struct {
	server *httptest.Server
	port   string
	pool   *x509.CertPool
}

func newImageServer(t *testing.T, routes map[string]http.HandlerFunc) imageServer {
	t.Helper()
	mux := http.NewServeMux()
	for pattern, handler := range routes {
		mux.HandleFunc(pattern, handler)
	}
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	return imageServer{server: server, port: parsed.Port(), pool: pool}
}

// link names the test server by example.com, which its certificate covers.
func (server imageServer) link(path string) string {
	return "https://example.com:" + server.port + path
}

func TestPublicAddress(t *testing.T) {
	t.Parallel()
	for address, want := range map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.20": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "255.255.255.255": false,
		"224.0.0.1": false, "192.0.2.1": false, "198.18.0.1": false, "240.0.0.1": false,
		"::1": false, "::": false, "fe80::1": false, "fc00::1": false, "ff02::1": false,
		"::ffff:10.0.0.1": false, "::ffff:8.8.8.8": true, "64:ff9b::a00:1": false, "2002:a00:1::1": false,
		"2001:db8::1": false,
		// IPv4-compatible and site-local IPv6 are not public either.
		"::10.0.0.1": false, "::8.8.8.8": false, "::a9fe:a9fe": false, "fec0::1": false, "feff::1": false,
	} {
		if got := publicAddress(netip.MustParseAddr(address)); got != want {
			t.Fatalf("PublicAddress(%s) = %t, want %t", address, got, want)
		}
	}
}

func TestFetcherDownloadsImagesOverHTTPS(t *testing.T) {
	t.Parallel()
	jpegBytes := paintedJPEG(t, 32, 18, 1)
	server := newImageServer(t, map[string]http.HandlerFunc{
		"/art.jpg": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(jpegBytes)
		},
		"/hop": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/art.jpg", http.StatusFound)
		},
	})
	resolver := &mapResolver{answers: map[string][]string{"example.com": {"127.0.0.1"}}}
	fetcher := newFetcher(fetcherOptions{Resolver: resolver, Allow: loopbackOrPublic, RootCAs: server.pool})
	for _, path := range []string{"/art.jpg", "/hop"} {
		data, err := fetcher.Fetch(context.Background(), server.link(path), 1<<20)
		if err != nil || string(data) != string(jpegBytes) {
			t.Fatalf("Fetch(%s) = %d bytes, %v", path, len(data), err)
		}
	}
}

func TestFetcherRejectsUnsafeLinks(t *testing.T) {
	t.Parallel()
	jpegBytes := paintedJPEG(t, 32, 18, 2)
	server := newImageServer(t, map[string]http.HandlerFunc{
		"/art.jpg": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(jpegBytes) },
		"/to-private": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://internal.test:"+r.URL.Port()+"/art.jpg", http.StatusFound)
		},
		"/to-http": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://example.com/art.jpg", http.StatusFound)
		},
		"/loop": func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) },
		"/html": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html></html>"))
		},
		"/disguised": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("<!doctype html><script></script>"))
		},
		"/gif": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/gif")
			_, _ = w.Write([]byte("GIF89a"))
		},
		"/missing": http.NotFound,
	})
	resolver := &mapResolver{answers: map[string][]string{
		"example.com":   {"127.0.0.1"},
		"internal.test": {"10.0.0.8"},
		"metadata.test": {"169.254.169.254"},
		"mixed.test":    {"8.8.8.8", "192.168.1.1"},
		"rebind.test":   {"127.0.0.1"},
	}}
	fetcher := newFetcher(fetcherOptions{Resolver: resolver, Allow: loopbackOrPublic, RootCAs: server.pool})
	tests := []struct {
		name string
		link string
		want string
	}{
		{"plain http", "http://example.com/art.jpg", "only https"},
		{"credentials", "https://user:pass@example.com/art.jpg", "credentials"},
		{"no host", "https:///art.jpg", "no host"},
		{"private by name", "https://internal.test:" + server.port + "/art.jpg", "private or reserved"},
		{"cloud metadata", "https://metadata.test/latest", "private or reserved"},
		{"private literal", "https://10.0.0.1/art.jpg", "private or reserved"},
		{"mixed public and private answers", "https://mixed.test/art.jpg", "private or reserved"},
		{"redirect to a private host", server.link("/to-private"), "private or reserved"},
		{"redirect to http", server.link("/to-http"), "only https"},
		{"redirect loop", server.link("/loop"), "redirects"},
		{"html page", server.link("/html"), "only JPEG and PNG"},
		{"html disguised as JPEG", server.link("/disguised"), "not a JPEG or PNG"},
		{"gif", server.link("/gif"), "only JPEG and PNG"},
		{"not found", server.link("/missing"), "HTTP 404"},
		{"oversized", server.link("/art.jpg"), "limit"},
		{"overlong link", "https://example.com/" + strings.Repeat("a", maxImageURLBytes), "longer than"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			limit := int64(1 << 20)
			if test.name == "oversized" {
				limit = 64
			}
			_, err := fetcher.Fetch(context.Background(), test.link, limit)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Fetch(%s) error = %v, want %q", test.link, err, test.want)
			}
		})
	}
}

func TestFetchErrorOmitsTheRedirectTargetPathAndQuery(t *testing.T) {
	t.Parallel()
	server := newImageServer(t, map[string]http.HandlerFunc{
		"/hop": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://example.com/private/art.jpg?signature=signed-secret", http.StatusFound)
		},
	})
	resolver := &mapResolver{answers: map[string][]string{"example.com": {"127.0.0.1"}}}
	fetcher := newFetcher(fetcherOptions{Resolver: resolver, Allow: loopbackOrPublic, RootCAs: server.pool})
	_, err := fetcher.Fetch(context.Background(), server.link("/hop"), 1<<20)
	if !errors.Is(err, errUnsafeURL) {
		t.Fatalf("Fetch() error = %v, want errUnsafeURL", err)
	}
	for _, secret := range []string{"signed-secret", "/private/", "/hop"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("Fetch() error exposes %q: %v", secret, err)
		}
	}
}

func TestFetcherChecksTheDialedAddressNotJustTheName(t *testing.T) {
	t.Parallel()
	server := newImageServer(t, map[string]http.HandlerFunc{
		"/art.jpg": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(paintedJPEG(t, 8, 8, 3)) },
	})
	// The name resolves to a public address, but the connection lands on a
	// loopback listener, as a rebinding or hijacked route would.
	resolver := &mapResolver{answers: map[string][]string{"example.com": {"8.8.8.8"}}}
	dialer := &net.Dialer{}
	fetcher := newFetcher(fetcherOptions{
		Resolver: resolver, RootCAs: server.pool,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, server.server.Listener.Addr().String())
		},
	})
	_, err := fetcher.Fetch(context.Background(), server.link("/art.jpg"), 1<<20)
	if !errors.Is(err, errUnsafeURL) || !strings.Contains(err.Error(), "connected address") {
		t.Fatalf("Fetch() error = %v, want the connected address rejected", err)
	}
}

func TestAddArtworkFromURLAndSSRFRejection(t *testing.T) {
	t.Parallel()
	jpegBytes := paintedJPEG(t, 96, 54, 4)
	server := newImageServer(t, map[string]http.HandlerFunc{
		"/generated/winter.jpg": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(jpegBytes)
		},
	})
	resolver := &mapResolver{answers: map[string][]string{"example.com": {"127.0.0.1"}, "router.test": {"192.168.0.1"}}}
	fixture := newFixture(t, fixtureOptions{
		fetcher: newFetcher(fetcherOptions{Resolver: resolver, Allow: loopbackOrPublic, RootCAs: server.pool}),
	})
	var added addArtworkOutput
	fixture.call(t, "add_artwork", map[string]any{
		"collection": "Christmas", "image_url": server.link("/generated/winter.jpg"), "title": "Winter",
	}, &added)
	if added.Width != 96 || added.Height != 54 || !strings.HasPrefix(added.Filename, "winter") {
		t.Fatalf("add_artwork(url) = %+v", added)
	}
	text := fixture.callError(t, "add_artwork", map[string]any{
		"collection": "Christmas", "image_url": "https://router.test/admin.jpg",
	})
	if !strings.Contains(text, "private or reserved") {
		t.Fatalf("SSRF add_artwork error = %q", text)
	}
}

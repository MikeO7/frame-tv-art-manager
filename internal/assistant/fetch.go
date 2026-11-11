package assistant

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const (
	defaultFetchTimeout = 60 * time.Second
	maxFetchRedirects   = 5
	maxImageURLBytes    = 4096
	mediaTypeJPEG       = "image/jpeg"
	mediaTypePNG        = "image/png"
)

// errUnsafeURL classifies image links refused before any byte is fetched.
var errUnsafeURL = errors.New("image URL is not allowed")

// resolver looks up the addresses of a host name.
type resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// fetcherOptions configures how image links are downloaded. Zero values use
// the system resolver and dialer and allow only public internet addresses.
type fetcherOptions struct {
	Resolver resolver
	Dial     func(ctx context.Context, network, address string) (net.Conn, error)
	// Allow decides whether an address may be dialed. It defaults to
	// PublicAddress.
	Allow func(netip.Addr) bool
	// RootCAs overrides the system trust store.
	RootCAs *x509.CertPool
	Timeout time.Duration
}

// fetcher downloads images from https links without reaching private
// networks. Every address is checked at dial time, after DNS resolution and
// for every redirect, so DNS rebinding cannot steer it to an internal host.
type fetcher struct {
	resolver resolver
	dial     func(ctx context.Context, network, address string) (net.Conn, error)
	allow    func(netip.Addr) bool
	client   *http.Client
}

// newFetcher returns a Fetcher configured by options.
func newFetcher(options fetcherOptions) *fetcher {
	fetcher := &fetcher{resolver: options.Resolver, dial: options.Dial, allow: options.Allow}
	if fetcher.resolver == nil {
		fetcher.resolver = net.DefaultResolver
	}
	if fetcher.dial == nil {
		fetcher.dial = (&net.Dialer{Timeout: 15 * time.Second}).DialContext
	}
	if fetcher.allow == nil {
		fetcher.allow = publicAddress
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = defaultFetchTimeout
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            fetcher.dialChecked,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: options.RootCAs},
		TLSHandshakeTimeout:    15 * time.Second,
		ResponseHeaderTimeout:  30 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           4,
		IdleConnTimeout:        30 * time.Second,
	}
	fetcher.client = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= maxFetchRedirects {
				return fmt.Errorf("%w: more than %d redirects", errUnsafeURL, maxFetchRedirects)
			}
			return validateImageURL(request.URL)
		},
	}
	return fetcher
}

// Fetch downloads at most maxBytes from rawURL and returns the bytes only
// when they are a JPEG or PNG image.
func (fetcher *fetcher) Fetch(ctx context.Context, rawURL string, maxBytes int64) ([]byte, error) {
	if len(rawURL) > maxImageURLBytes {
		return nil, fmt.Errorf("%w: link is longer than %d characters", errUnsafeURL, maxImageURLBytes)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: link cannot be parsed", errUnsafeURL)
	}
	if err := validateImageURL(parsed); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create image request: %w", err)
	}
	request.Header.Set("Accept", "image/png, image/jpeg")
	request.Header.Set("User-Agent", "frame-tv-art-manager")
	response, err := fetcher.client.Do(request)
	if err != nil {
		// The client's error quotes the whole link, and after a redirect the
		// whole redirect target. Only the cause is safe to report.
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		return nil, fmt.Errorf("download image from %s: %w", safeHost(parsed), err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download image from %s: HTTP %d", safeHost(parsed), response.StatusCode)
	}
	if err := validateImageContentType(response.Header.Get("Content-Type")); err != nil {
		return nil, err
	}
	if response.ContentLength > maxBytes {
		return nil, fmt.Errorf("image is %d bytes; the limit is %d bytes", response.ContentLength, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download image from %s: %w", safeHost(parsed), err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("image exceeds the %d-byte limit", maxBytes)
	}
	if err := sniffImage(data); err != nil {
		return nil, err
	}
	return data, nil
}

// dialChecked resolves the host itself and dials only an allowed address, so
// the checked address is exactly the one connected to.
func (fetcher *fetcher) dialChecked(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid address", errUnsafeURL)
	}
	var candidates []netip.Addr
	if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
		candidates = []netip.Addr{literal}
	} else {
		candidates, err = fetcher.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("resolve %s: no addresses", host)
	}
	for _, candidate := range candidates {
		if !fetcher.allow(candidate.Unmap()) {
			return nil, fmt.Errorf("%w: %s resolves to a private or reserved address", errUnsafeURL, host)
		}
	}
	var dialErrors []error
	for _, candidate := range candidates {
		conn, dialErr := fetcher.dial(ctx, network, net.JoinHostPort(candidate.Unmap().String(), port))
		if dialErr != nil {
			dialErrors = append(dialErrors, dialErr)
			continue
		}
		if !fetcher.remoteAllowed(conn) {
			_ = conn.Close()
			return nil, fmt.Errorf("%w: connected address is private or reserved", errUnsafeURL)
		}
		return conn, nil
	}
	return nil, fmt.Errorf("connect to %s: %w", host, errors.Join(dialErrors...))
}

func (fetcher *fetcher) remoteAllowed(conn net.Conn) bool {
	remote, err := netip.ParseAddrPort(conn.RemoteAddr().String())
	return err == nil && fetcher.allow(remote.Addr().Unmap())
}

func validateImageURL(parsed *url.URL) error {
	if parsed.Scheme != "https" {
		return fmt.Errorf("%w: only https links are accepted", errUnsafeURL)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: links with credentials are not accepted", errUnsafeURL)
	}
	if parsed.Hostname() == "" {
		return fmt.Errorf("%w: link has no host", errUnsafeURL)
	}
	return nil
}

func validateImageContentType(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return fmt.Errorf("image link returned an unreadable content type: %w", err)
	}
	switch mediaType {
	case mediaTypeJPEG, mediaTypePNG, "application/octet-stream", "binary/octet-stream":
		return nil
	default:
		return fmt.Errorf("image link returned %q; only JPEG and PNG images are accepted", mediaType)
	}
}

func sniffImage(data []byte) error {
	switch http.DetectContentType(data) {
	case mediaTypeJPEG, mediaTypePNG:
		return nil
	default:
		return errors.New("the data is not a JPEG or PNG image")
	}
}

// safeHost names a link's host without its path or query, which may carry
// signed tokens.
func safeHost(parsed *url.URL) string {
	return parsed.Hostname()
}

// reservedPrefixes are non-public ranges that netip's predicates do not cover.
var reservedPrefixes = []netip.Prefix{ //nolint:gochecknoglobals // immutable address policy table
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

// publicAddress reports whether addr is a globally routable unicast address,
// rejecting loopback, private, link-local, multicast, unspecified, carrier
// NAT, documentation, translation, and reserved ranges.
func publicAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range reservedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

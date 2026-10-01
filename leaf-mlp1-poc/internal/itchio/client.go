package itchio

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"

	"leaf-mlp1-poc/internal/logger"
)

const (
	// browserUserAgent preserves the upstream browser identity used to avoid Cloudflare bot-protection
	// responses (which would return HTML instead of the expected XML/JSON payloads).
	browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	productName      = "Leaf-Itchio-Pak"

	dialTimeout           = 10 * time.Second
	keepAlive             = 30 * time.Second
	responseHeaderTimeout = 15 * time.Second
	metadataTimeout       = 30 * time.Second

	apiItchIO = "https://api.itch.io"
)

// errH1Negotiated is returned by dialTLS when the server selects http/1.1
// via ALPN. h2FallbackTransport catches it to route the request (and all
// future requests to that host) through the h1 transport instead.
var errH1Negotiated = errors.New("server negotiated http/1.1")

// uaTransport injects browser-compatible headers on every outbound request
// that does not already have them, then delegates to the wrapped RoundTripper.
type uaTransport struct {
	wrapped   http.RoundTripper
	userAgent string
}

func setDefaultHeader(req *http.Request, key, value string) {
	if req.Header.Get(key) == "" {
		req.Header.Set(key, value)
	}
}

func (t *uaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	setDefaultHeader(req, "User-Agent", t.userAgent)
	setDefaultHeader(req, "Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	setDefaultHeader(req, "Accept-Language", "en-US,en;q=0.9")
	setDefaultHeader(req, "sec-ch-ua", `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`)
	setDefaultHeader(req, "sec-ch-ua-mobile", "?0")
	setDefaultHeader(req, "sec-ch-ua-platform", `"Windows"`)
	setDefaultHeader(req, "Sec-Fetch-Dest", "document")
	setDefaultHeader(req, "Sec-Fetch-Mode", "navigate")
	setDefaultHeader(req, "Sec-Fetch-Site", "none")
	setDefaultHeader(req, "Sec-Fetch-User", "?1")
	setDefaultHeader(req, "Cache-Control", "max-age=0")
	return t.wrapped.RoundTrip(req)
}

// dialTLS dials a TLS connection using the Chrome ClientHello fingerprint via
// utls, advertising ["h2", "http/1.1"] ALPN. If the server selects h2 the
// conn is returned to http2.Transport. If it selects http/1.1, the conn is
// closed and errH1Negotiated is returned so h2FallbackTransport can retry
// over the h1 transport. The cfg parameter satisfies http2.Transport's
// DialTLSContext signature but is ignored — we build our own utls config.
func dialTLS(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	conn, err := dialPreferIPv4(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	uconn := utls.UClient(conn, &utls.Config{ServerName: host}, utls.HelloChrome_Auto)
	if err := uconn.BuildHandshakeState(); err != nil {
		conn.Close()
		return nil, err
	}
	for _, ext := range uconn.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"h2", "http/1.1"}
			break
		}
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	proto := uconn.ConnectionState().NegotiatedProtocol
	logger.Debug("client: TLS addr=%s proto=%s", addr, proto)
	if proto != "h2" {
		uconn.Close()
		return nil, errH1Negotiated
	}
	return uconn, nil
}

// dialTLSH1 is the http.Transport-compatible dialer (no *tls.Config param)
// using the Chrome utls fingerprint with http/1.1-only ALPN, for servers
// that do not support h2 (signed download CDNs, custom game hosting).
func dialTLSH1(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	conn, err := dialPreferIPv4(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	uconn := utls.UClient(conn, &utls.Config{ServerName: host}, utls.HelloChrome_Auto)
	if err := uconn.BuildHandshakeState(); err != nil {
		conn.Close()
		return nil, err
	}
	for _, ext := range uconn.Extensions {
		if alpn, ok := ext.(*utls.ALPNExtension); ok {
			alpn.AlpnProtocols = []string{"http/1.1"}
			break
		}
	}
	if err := uconn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return uconn, nil
}

// h2FallbackTransport routes HTTPS requests through http2.Transport for h2
// servers (itch.io game pages, API, image CDN) and falls back to an h1
// transport for servers that only negotiate http/1.1 (Cloudflare R2 signed
// download URLs, custom game hosting). Per-host routing is cached so the
// extra handshake only occurs on the first request to each h1-only host.
// Plain HTTP requests (httptest servers in tests) always use the h1 transport.
type h2FallbackTransport struct {
	h2 http.RoundTripper
	h1 http.RoundTripper

	mu      sync.RWMutex
	h1hosts map[string]struct{} // hosts that negotiated http/1.1
}

func (t *h2FallbackTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		return t.h1.RoundTrip(req)
	}

	host := req.URL.Host
	if !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "443")
	}

	t.mu.RLock()
	_, isH1 := t.h1hosts[host]
	t.mu.RUnlock()

	if isH1 {
		return t.h1.RoundTrip(req)
	}

	resp, err := t.h2.RoundTrip(req)
	if errors.Is(err, errH1Negotiated) {
		logger.Info("client: %s negotiates http/1.1, caching as h1-only", host)
		t.mu.Lock()
		t.h1hosts[host] = struct{}{}
		t.mu.Unlock()
		return t.h1.RoundTrip(req)
	}
	return resp, err
}

func productUserAgent(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		version = "dev"
	}
	// Product tokens cannot contain whitespace. Build versions are normally
	// semver, but keep developer overrides safe and deterministic too.
	version = strings.Map(func(value rune) rune {
		if value <= ' ' || value == '/' || value == ';' || value == '(' || value == ')' {
			return '-'
		}
		return value
	}, version)
	return fmt.Sprintf("%s %s/%s", browserUserAgent, productName, version)
}

// safeRequestError keeps credential-bearing request URLs out of UI/crash
// messages while retaining the full failure in the local, redacted debug log.
// Cancellation identity is preserved for transaction rollback logic.
func safeRequestError(operation string, err error) error {
	logger.Debug("%s request failed: %v", operation, err)
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return fmt.Errorf("%s: network timeout", operation)
	}
	return fmt.Errorf("%s: network request failed", operation)
}

func newHTTPClient(version string) *http.Client {
	jar, _ := cookiejar.New(nil)
	h2t := &http2.Transport{
		DialTLSContext:  dialTLS,
		ReadIdleTimeout: responseHeaderTimeout,
		PingTimeout:     dialTimeout,
	}
	h1t := &http.Transport{
		DialTLSContext:        dialTLSH1,
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       keepAlive,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
	}
	return &http.Client{
		Jar:     jar,
		Timeout: metadataTimeout,
		Transport: &uaTransport{
			userAgent: productUserAgent(version),
			wrapped: &h2FallbackTransport{
				h2:      h2t,
				h1:      h1t,
				h1hosts: make(map[string]struct{}),
			},
		},
	}
}

type Client struct {
	http   *http.Client
	base   string // itch.io/api/1/... base URL
	butler string // api.itch.io base URL (butler-style endpoints)

	// Background API key validation state (atomic, written once per session).
	apiKeyStatus   int32 // stores APIKeyStatus constants
	apiKeyChecking int32 // 0 = not started, 1 = started (CAS gate)
}

func NewClient() *Client {
	return NewClientWithVersion("dev")
}

// NewClientWithVersion builds the production client while preserving the
// upstream browser fingerprint and appending the Leaf product/version token.
func NewClientWithVersion(version string) *Client {
	return &Client{
		http:   newHTTPClient(version),
		base:   "https://itch.io",
		butler: apiItchIO,
	}
}

func NewClientWithBase(base string) *Client {
	return &Client{
		http:   newHTTPClient("dev"),
		base:   base,
		butler: apiItchIO,
	}
}

// NewClientWithBaseAndButler is used in tests to override both base URLs.
func NewClientWithBaseAndButler(base, butler string) *Client {
	return &Client{
		http:   newHTTPClient("dev"),
		base:   base,
		butler: butler,
	}
}

// HTTPClient returns the underlying *http.Client used for all requests.
func (c *Client) HTTPClient() *http.Client {
	return c.http
}

// DownloadURL streams directly from a pre-resolved CDN URL to dest.
// Use when the CDN URL was already resolved by ResolveFreeURL or ResolveAuthURL.
func (c *Client) DownloadURL(cdnURL, dest string, progress func(int64, int64)) error {
	return c.streamToFile(cdnURL, dest, progress)
}

// dialPreferIPv4 connects over IPv4 when possible. On the device's network
// itch.io's IPv6 route (Cloudflare) keeps resetting connections, IPv4 is
// stable; IPv6 is still used when there is no IPv4 route.
func dialPreferIPv4(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: keepAlive}
	if conn, err := d.DialContext(ctx, "tcp4", addr); err == nil {
		return conn, nil
	}
	return d.DialContext(ctx, network, addr)
}

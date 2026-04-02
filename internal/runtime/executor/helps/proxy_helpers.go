package helps

import (
	"context"
	"crypto/tls"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use auth.ProxyURL if configured (highest priority)
// 2. Use cfg.ProxyURL if auth proxy is not configured
// 3. Use RoundTripper from context if neither are configured
// 4. Use HTTP/2 enabled transport as default
//
// All transports are configured with HTTP/2 support for optimal streaming performance.
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport (HTTP/2 enabled)
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	httpClient := &http.Client{}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	// Priority 1: Use auth.ProxyURL if configured
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}

	// Priority 2: Use cfg.ProxyURL if auth proxy is not configured
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	// If we have a proxy URL configured, set up the transport with HTTP/2 support
	if proxyURL != "" {
		transport := buildProxyTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = transport
			return httpClient
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyURL)
	}

	// Priority 3: Use RoundTripper from context (typically from RoundTripperFor)
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
		return httpClient
	}

	// Priority 4: Use default HTTP/2 enabled transport
	httpClient.Transport = buildHTTP2Transport()

	return httpClient
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols with HTTP/2 enabled.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport with HTTP/2 support, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}

// buildHTTP2Transport creates an HTTP transport with HTTP/2 enabled but no proxy.
// Use this when you need HTTP/2 support without a proxy configuration.
//
// Returns:
//   - *http.Transport: A configured transport with HTTP/2 support
func buildHTTP2Transport() *http.Transport {
	return &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{},
	}
}

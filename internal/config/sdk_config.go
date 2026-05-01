// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

// SDKConfig represents the application's configuration, loaded from a YAML file.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// DisableImageGeneration controls whether the built-in image_generation tool is injected/allowed.
	//
	// Supported values:
	//   - false (default): image_generation is enabled everywhere (normal behavior).
	//   - true: image_generation is disabled everywhere. The server stops injecting it, removes it from request payloads,
	//     and returns 404 for /v1/images/generations and /v1/images/edits.
	//   - "chat": disable image_generation injection for all non-images endpoints (e.g. /v1/responses, /v1/chat/completions),
	//     while keeping /v1/images/generations and /v1/images/edits enabled and preserving image_generation there.
	DisableImageGeneration DisableImageGenerationMode `yaml:"disable-image-generation" json:"disable-image-generation"`

	// EnableGeminiCLIEndpoint controls whether Gemini CLI internal endpoints (/v1internal:*) are enabled.
	// Default is false for safety; when false, /v1internal:* requests are rejected.
	EnableGeminiCLIEndpoint bool `yaml:"enable-gemini-cli-endpoint" json:"enable-gemini-cli-endpoint"`

	// ForceModelPrefix requires explicit model prefixes (e.g., "teamA/gemini-3-pro-preview")
	// to target prefixed credentials. When false, unprefixed model requests may use prefixed
	// credentials as well.
	ForceModelPrefix bool `yaml:"force-model-prefix" json:"force-model-prefix"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// APIKeys is a list of keys for authenticating clients to this proxy server.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`

	// OwnerAPIKeys is a subset of APIKeys that receive full model limits for max_tokens.
	// Non-owner keys are capped at MaxTokensCap.
	OwnerAPIKeys []string `yaml:"owner-api-keys" json:"owner-api-keys"`

	// MaxTokensCap is the max_tokens ceiling for non-owner API keys (default: 16384).
	// Owner keys always get the model's MaxCompletionTokens instead.
	MaxTokensCap int `yaml:"max-tokens-cap" json:"max-tokens-cap"`

	// PassthroughHeaders controls whether upstream response headers are forwarded to downstream clients.
	// Default is false (disabled).
	PassthroughHeaders bool `yaml:"passthrough-headers" json:"passthrough-headers"`

	// Streaming configures server-side streaming behavior (keep-alives and safe bootstrap retries).
	Streaming StreamingConfig `yaml:"streaming" json:"streaming"`

	// NonStreamKeepAliveInterval controls how often blank lines are emitted for non-streaming responses.
	// <= 0 disables keep-alives. Value is in seconds.
	NonStreamKeepAliveInterval int `yaml:"nonstream-keepalive-interval,omitempty" json:"nonstream-keepalive-interval,omitempty"`

	// StickySession pins all requests from the same conversation to a single auth credential.
	// This improves prompt cache hit rates when using multiple OAuth accounts, since
	// Anthropic's prompt cache is scoped per-account.
	StickySession StickySessionConfig `yaml:"sticky-session,omitempty" json:"sticky-session,omitempty"`
}

const defaultMaxTokensCap = 16384

// IsOwnerAPIKey reports whether key is listed in OwnerAPIKeys.
// When OwnerAPIKeys is empty, all keys are treated as non-owner.
func (c *SDKConfig) IsOwnerAPIKey(key string) bool {
	for _, k := range c.OwnerAPIKeys {
		if k == key {
			return true
		}
	}
	return false
}

// GetMaxTokensCap returns the max_tokens ceiling for non-owner API keys.
func (c *SDKConfig) GetMaxTokensCap() int {
	if c.MaxTokensCap > 0 {
		return c.MaxTokensCap
	}
	return defaultMaxTokensCap
}

// StickySessionConfig configures conversation-level auth pinning for better prompt cache utilization.
type StickySessionConfig struct {
	// Enabled activates sticky session routing. Default: false.
	Enabled bool `yaml:"enabled" json:"enabled"`

	// TTLMinutes is how long a conversation→auth mapping is kept alive.
	// Default: 30 minutes.
	TTLMinutes int `yaml:"ttl-minutes,omitempty" json:"ttl-minutes,omitempty"`
}

// StreamingConfig holds server streaming behavior configuration.
type StreamingConfig struct {
	// KeepAliveSeconds controls how often the server emits SSE heartbeats (": keep-alive\n\n").
	// <= 0 disables keep-alives. Default is 0.
	KeepAliveSeconds int `yaml:"keepalive-seconds,omitempty" json:"keepalive-seconds,omitempty"`

	// BootstrapRetries controls how many times the server may retry a streaming request before any bytes are sent,
	// to allow auth rotation / transient recovery.
	// <= 0 disables bootstrap retries. Default is 0.
	BootstrapRetries int `yaml:"bootstrap-retries,omitempty" json:"bootstrap-retries,omitempty"`
}

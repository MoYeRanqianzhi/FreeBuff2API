package app

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration structure for FreeBuff2API.
//
// The struct is flat and human-friendly; every knob currently controllable
// via env variables has a corresponding YAML field. Unspecified fields fall
// back to sensible defaults (see ApplyDefaults).
type Config struct {
	Server    ServerConfig    `yaml:"server"`
	Upstream  UpstreamConfig  `yaml:"upstream"`
	Auth      AuthConfig      `yaml:"auth"`
	Logging   LoggingConfig   `yaml:"logging"`
	Limits    LimitsConfig    `yaml:"limits"`
	Incentive IncentiveConfig `yaml:"incentive"`
	GitHub    GitHubConfig    `yaml:"github"`
}

// GitHubConfig controls the post-auth redirect to a GitHub repository page.
// When Repo is set, the success page shows a "please star" prompt and links
// to the repository.
type GitHubConfig struct {
	Repo string `yaml:"repo"` // e.g. "MoYeRanQianZhi/FreeBuff2API"
}

// IncentiveConfig controls what reward a crowdfunding OAuth login receives.
// The three modes are mutually exclusive:
//
//   - "donor_key": generate an sk-or-v1-<hex> API key bound to the donated
//     upstream account (default; preserves v0.10 behaviour).
//   - "redeem_code": consume one line from the redeem codes file and hand it
//     back to the contributor. The code is deleted on issuance so it cannot
//     be reused. When the pool is empty the login still succeeds but no code
//     is issued (the response simply omits redeem_code + usage).
//   - "none": no reward is issued; the contributor sees a thank-you message.
type IncentiveConfig struct {
	// Mode selects the reward type. Valid values: "donor_key", "redeem_code", "none".
	// Empty string defaults to "donor_key".
	Mode string `yaml:"mode"`
	// RedeemCodesFile is the plain-text file holding one code per line. Lines
	// starting with # and blank lines are ignored. Default: "redeem_codes.txt".
	RedeemCodesFile string `yaml:"redeem_codes_file"`
	// RedeemUsage is a one-line instruction shown next to the redeem code on
	// the success page. Example: "前往 https://example.com/redeem 兑换".
	RedeemUsage string `yaml:"redeem_usage"`
}

const (
	IncentiveModeDonorKey   = "donor_key"
	IncentiveModeRedeemCode = "redeem_code"
	IncentiveModeNone       = "none"
)

// LimitsConfig holds optional multi-tier RPM caps. Zero means unlimited at
// that tier. When a limiter rejects, the proxy returns HTTP 429 immediately —
// no internal queueing.
type LimitsConfig struct {
	// GlobalRPM caps total requests/minute across the entire proxy. 0 = unlimited.
	GlobalRPM int `yaml:"global_rpm"`
	// AccountRPM caps requests/minute against each single upstream key.
	// 0 = unlimited. When a per-account bucket is empty, the proxy skips that
	// account in round-robin (automatic load balancing).
	AccountRPM int `yaml:"account_rpm"`
	// ClientRPM caps requests/minute from each single client Bearer token.
	// 0 = unlimited.
	ClientRPM int `yaml:"client_rpm"`
}

type ServerConfig struct {
	// ListenAddr is the host:port the HTTP server binds to. Default: ":8080".
	ListenAddr string `yaml:"listen"`
	// APIKeys is the list of client Bearer tokens accepted by /v1/* endpoints.
	// Empty list disables downstream auth (unless OpenRouter fallback needs it).
	APIKeys []string `yaml:"api_keys"`
}

type UpstreamConfig struct {
	// BaseURL of the codebuff/FreeBuff backend.
	BaseURL string `yaml:"base_url"`
	// CostMode forwarded to the backend: "free" or "normal".
	CostMode string `yaml:"cost_mode"`
	// DefaultModel used when a request omits the model field.
	DefaultModel string `yaml:"default_model"`
	// OpenRouter configures the sk-or-* fallback path.
	OpenRouter OpenRouterConfig `yaml:"openrouter"`
}

type OpenRouterConfig struct {
	// BaseURL of the OpenRouter API. Default: "https://openrouter.ai/api/v1".
	BaseURL string `yaml:"base_url"`
	// Enabled toggles the fallback path. Default: true (pointer so "unset" vs "false" are distinguishable).
	// When false, sk-or-* tokens are not accepted and FreeBuff outages are not fallen back on.
	Enabled *bool `yaml:"enabled"`
}

// OpenRouterEnabled resolves the tri-state pointer to bool, defaulting to true.
func (c OpenRouterConfig) IsEnabled() bool {
	if c.Enabled == nil {
		return true
	}
	return *c.Enabled
}

type AuthConfig struct {
	// APIKeys is an inline list of FreeBuff authTokens.
	APIKeys []string `yaml:"api_keys"`
	// Dir is the directory scanned for codebuff credentials.json files.
	Dir string `yaml:"dir"`
	// WatchInterval is the poll interval for auth file changes. Default: 15s.
	WatchInterval time.Duration `yaml:"watch_interval"`
	// Breaker holds circuit breaker tuning.
	Breaker BreakerConfig `yaml:"breaker"`
}

type BreakerConfig struct {
	// Threshold is consecutive failures that trip the breaker. Default: 3.
	Threshold int `yaml:"threshold"`
	// Cooldown is how long a tripped breaker stays open. Default: 12h.
	Cooldown time.Duration `yaml:"cooldown"`
}

type LoggingConfig struct {
	// Level is one of debug/info/warn/error. Default: info.
	Level string `yaml:"level"`
}

// DefaultConfigPath is relative to the current working directory.
const DefaultConfigPath = "config.yaml"

// DefaultGitHubRepo is used when config.yaml does not specify github.repo.
const DefaultGitHubRepo = "MoYeRanQianZhi/FreeBuff2API"

// LoadConfig reads a YAML config file from the given path and applies defaults.
// The file must exist; missing or unreadable config is a fatal error.
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		return nil, fmt.Errorf("config path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.ListenAddr == "" {
		c.Server.ListenAddr = ":8080"
	}
	if c.Upstream.BaseURL == "" {
		c.Upstream.BaseURL = "https://www.codebuff.com"
	}
	c.Upstream.BaseURL = strings.TrimRight(c.Upstream.BaseURL, "/")
	if c.Upstream.CostMode == "" {
		c.Upstream.CostMode = "free"
	}
	if c.Upstream.DefaultModel == "" {
		c.Upstream.DefaultModel = "anthropic/claude-sonnet-4"
	}
	if c.Upstream.OpenRouter.BaseURL == "" {
		c.Upstream.OpenRouter.BaseURL = "https://openrouter.ai/api/v1"
	}
	c.Upstream.OpenRouter.BaseURL = strings.TrimRight(c.Upstream.OpenRouter.BaseURL, "/")
	if c.Auth.Dir == "" {
		c.Auth.Dir = "auths"
	}
	if c.Auth.WatchInterval <= 0 {
		c.Auth.WatchInterval = 15 * time.Second
	}
	if c.Auth.Breaker.Threshold <= 0 {
		c.Auth.Breaker.Threshold = 3
	}
	if c.Auth.Breaker.Cooldown <= 0 {
		c.Auth.Breaker.Cooldown = 12 * time.Hour
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Incentive.Mode == "" {
		c.Incentive.Mode = IncentiveModeDonorKey
	}
	if c.Incentive.RedeemCodesFile == "" {
		c.Incentive.RedeemCodesFile = "redeem_codes.txt"
	}
	if c.GitHub.Repo == "" {
		c.GitHub.Repo = DefaultGitHubRepo
	}

	c.Auth.APIKeys = dedupStrings(c.Auth.APIKeys)
	c.Server.APIKeys = dedupStrings(c.Server.APIKeys)
}

func dedupStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, k := range in {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// Validate surfaces errors only when the config is so broken the server can't
// start — missing keys are not a Validate error because auths/ may supply
// them separately (checked in main.go after LoadKeySources).
func (c *Config) Validate() error {
	if c.Upstream.CostMode != "free" && c.Upstream.CostMode != "normal" {
		return fmt.Errorf("upstream.cost_mode must be \"free\" or \"normal\", got %q", c.Upstream.CostMode)
	}
	if c.Upstream.OpenRouter.IsEnabled() {
		u := c.Upstream.OpenRouter.BaseURL
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			return fmt.Errorf("upstream.openrouter.base_url must start with http:// or https://, got %q", u)
		}
	}
	if c.Limits.GlobalRPM < 0 || c.Limits.AccountRPM < 0 || c.Limits.ClientRPM < 0 {
		return fmt.Errorf("limits.*_rpm must be >= 0 (0 = unlimited)")
	}
	switch c.Incentive.Mode {
	case IncentiveModeDonorKey, IncentiveModeRedeemCode, IncentiveModeNone:
		// ok
	default:
		return fmt.Errorf("incentive.mode must be %q, %q or %q, got %q",
			IncentiveModeDonorKey, IncentiveModeRedeemCode, IncentiveModeNone, c.Incentive.Mode)
	}
	return nil
}

func fingerprint(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:6] + "…" + key[len(key)-2:]
}

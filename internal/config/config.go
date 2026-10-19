package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Config represents all configuration settings for GAMS.
type Config struct {
	Data   DataConfig   `toml:"data"`
	Server ServerConfig `toml:"server"`
	MCP    MCPConfig    `toml:"mcp"`
	Limits LimitsConfig `toml:"limits"`
	Auth   AuthConfig   `toml:"auth"`
}

type DataConfig struct {
	Path string `toml:"path"`
}

type ServerConfig struct {
	HTTPPort    int      `toml:"http_port"`
	AdminKey    string   `toml:"admin_key"`
	CORSOrigins []string `toml:"cors_origins"`
}

type MCPConfig struct {
	Transport string `toml:"transport"` // "http" | "stdio" | "both"
}

type LimitsConfig struct {
	MaxClaimSizeBytes   int `toml:"max_claim_size_bytes"`
	MaxEvidencePerClaim int `toml:"max_evidence_per_claim"`
	DefaultRecallLimit  int `toml:"default_recall_limit"`
}

type AuthConfig struct {
	SigningRequired    bool `toml:"signing_required"`
	KeyCacheTTLSeconds int  `toml:"key_cache_ttl_seconds"`
}

// Default returns a Config populated with default values.
func Default() *Config {
	return &Config{
		Data: DataConfig{
			Path: "gams.db",
		},
		Server: ServerConfig{
			HTTPPort:    8080,
			AdminKey:    "",
			CORSOrigins: []string{"*"},
		},
		MCP: MCPConfig{
			Transport: "http",
		},
		Limits: LimitsConfig{
			MaxClaimSizeBytes:   65536,
			MaxEvidencePerClaim: 20,
			DefaultRecallLimit:  10,
		},
		Auth: AuthConfig{
			SigningRequired:    false,
			KeyCacheTTLSeconds: 300,
		},
	}
}

// Load loads configuration from an optional file path, applies environment variable overrides,
// and validates the resulting configuration.
func Load(configPath string) (*Config, error) {
	cfg := Default()

	if configPath != "" {
		data, err := os.ReadFile(configPath)
		if err != nil {
			if !os.IsNotExist(err) || configPath != "gams.toml" {
				return nil, fmt.Errorf("reading config file %q: %w", configPath, err)
			}
			// If default "gams.toml" does not exist, continue with defaults
		} else {
			if err := toml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("parsing TOML config: %w", err)
			}
		}
	}

	applyEnvOverrides(cfg)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if val := os.Getenv("GAMS_DATA_PATH"); val != "" {
		cfg.Data.Path = val
	}
	if val := os.Getenv("GAMS_SERVER_HTTP_PORT"); val != "" {
		if port, err := strconv.Atoi(val); err == nil {
			cfg.Server.HTTPPort = port
		}
	}
	if val := os.Getenv("GAMS_SERVER_ADMIN_KEY"); val != "" {
		cfg.Server.AdminKey = val
	}
	if val := os.Getenv("GAMS_SERVER_CORS_ORIGINS"); val != "" {
		cfg.Server.CORSOrigins = strings.Split(val, ",")
	}
	if val := os.Getenv("GAMS_MCP_TRANSPORT"); val != "" {
		cfg.MCP.Transport = val
	}
	if val := os.Getenv("GAMS_LIMITS_MAX_CLAIM_SIZE_BYTES"); val != "" {
		if size, err := strconv.Atoi(val); err == nil {
			cfg.Limits.MaxClaimSizeBytes = size
		}
	}
	if val := os.Getenv("GAMS_LIMITS_MAX_EVIDENCE_PER_CLAIM"); val != "" {
		if count, err := strconv.Atoi(val); err == nil {
			cfg.Limits.MaxEvidencePerClaim = count
		}
	}
	if val := os.Getenv("GAMS_LIMITS_DEFAULT_RECALL_LIMIT"); val != "" {
		if limit, err := strconv.Atoi(val); err == nil {
			cfg.Limits.DefaultRecallLimit = limit
		}
	}
	if val := os.Getenv("GAMS_AUTH_SIGNING_REQUIRED"); val != "" {
		cfg.Auth.SigningRequired = strings.EqualFold(val, "true") || val == "1"
	}
	if val := os.Getenv("GAMS_AUTH_KEY_CACHE_TTL_SECONDS"); val != "" {
		if ttl, err := strconv.Atoi(val); err == nil {
			cfg.Auth.KeyCacheTTLSeconds = ttl
		}
	}
}

// Validate checks whether the configuration values are within acceptable bounds.
func (c *Config) Validate() error {
	if strings.TrimSpace(c.Data.Path) == "" {
		return errors.New("data.path cannot be empty")
	}
	if c.Server.HTTPPort <= 0 || c.Server.HTTPPort > 65535 {
		return fmt.Errorf("server.http_port must be between 1 and 65535, got %d", c.Server.HTTPPort)
	}
	transport := strings.ToLower(c.MCP.Transport)
	if transport != "http" && transport != "stdio" && transport != "both" {
		return fmt.Errorf("mcp.transport must be 'http', 'stdio', or 'both', got %q", c.MCP.Transport)
	}
	if c.Limits.MaxClaimSizeBytes <= 0 {
		return errors.New("limits.max_claim_size_bytes must be greater than 0")
	}
	if c.Limits.MaxEvidencePerClaim <= 0 {
		return errors.New("limits.max_evidence_per_claim must be greater than 0")
	}
	if c.Limits.DefaultRecallLimit <= 0 {
		return errors.New("limits.default_recall_limit must be greater than 0")
	}
	if c.Auth.KeyCacheTTLSeconds <= 0 {
		return errors.New("auth.key_cache_ttl_seconds must be greater than 0")
	}
	return nil
}

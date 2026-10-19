package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/PDK1744/gams/internal/config"
)

func TestDefaultConfig(t *testing.T) {
	cfg := config.Default()

	if cfg.Data.Path != "gams.db" {
		t.Errorf("expected data.path 'gams.db', got %q", cfg.Data.Path)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("expected server.host '127.0.0.1', got %q", cfg.Server.Host)
	}
	if cfg.Server.HTTPPort != 8080 {
		t.Errorf("expected server.http_port 8080, got %d", cfg.Server.HTTPPort)
	}
	if len(cfg.Server.CORSOrigins) != 0 {
		t.Errorf("expected empty server.cors_origins by default, got %v", cfg.Server.CORSOrigins)
	}
	if cfg.MCP.Transport != "http" {
		t.Errorf("expected mcp.transport 'http', got %q", cfg.MCP.Transport)
	}
	if cfg.Limits.MaxClaimSizeBytes != 65536 {
		t.Errorf("expected max_claim_size_bytes 65536, got %d", cfg.Limits.MaxClaimSizeBytes)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("default config should be valid: %v", err)
	}
}

func TestLoadWithFile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "test_config.toml")

	tomlData := `
[data]
path = "custom_test.db"

[server]
host = "0.0.0.0"
http_port = 9090
admin_key = "secret123"
cors_origins = ["http://localhost:3000"]

[mcp]
transport = "both"

[limits]
max_claim_size_bytes = 1024
max_evidence_per_claim = 5
default_recall_limit = 20

[auth]
signing_required = true
key_cache_ttl_seconds = 600
`
	if err := os.WriteFile(configPath, []byte(tomlData), 0644); err != nil {
		t.Fatalf("failed to write test config: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Data.Path != "custom_test.db" {
		t.Errorf("expected path 'custom_test.db', got %q", cfg.Data.Path)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected host '0.0.0.0', got %q", cfg.Server.Host)
	}
	if cfg.Server.HTTPPort != 9090 {
		t.Errorf("expected port 9090, got %d", cfg.Server.HTTPPort)
	}
	if cfg.Server.AdminKey != "secret123" {
		t.Errorf("expected admin key 'secret123', got %q", cfg.Server.AdminKey)
	}
	if len(cfg.Server.CORSOrigins) != 1 || cfg.Server.CORSOrigins[0] != "http://localhost:3000" {
		t.Errorf("expected cors_origins ['http://localhost:3000'], got %v", cfg.Server.CORSOrigins)
	}
	if cfg.MCP.Transport != "both" {
		t.Errorf("expected transport 'both', got %q", cfg.MCP.Transport)
	}
	if !cfg.Auth.SigningRequired {
		t.Errorf("expected signing_required true")
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("GAMS_DATA_PATH", "env_override.db")
	t.Setenv("GAMS_SERVER_HOST", "0.0.0.0")
	t.Setenv("GAMS_SERVER_HTTP_PORT", "7070")
	t.Setenv("GAMS_MCP_TRANSPORT", "stdio")
	t.Setenv("GAMS_AUTH_SIGNING_REQUIRED", "true")

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if cfg.Data.Path != "env_override.db" {
		t.Errorf("expected data.path 'env_override.db', got %q", cfg.Data.Path)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("expected server.host '0.0.0.0', got %q", cfg.Server.Host)
	}
	if cfg.Server.HTTPPort != 7070 {
		t.Errorf("expected port 7070, got %d", cfg.Server.HTTPPort)
	}
	if cfg.MCP.Transport != "stdio" {
		t.Errorf("expected transport 'stdio', got %q", cfg.MCP.Transport)
	}
	if !cfg.Auth.SigningRequired {
		t.Errorf("expected auth signing true")
	}
}

func TestEnvOverrideErrors(t *testing.T) {
	t.Run("invalid port", func(t *testing.T) {
		t.Setenv("GAMS_SERVER_HTTP_PORT", "invalid_number")
		_, err := config.Load("")
		if err == nil {
			t.Errorf("expected error for invalid GAMS_SERVER_HTTP_PORT, got nil")
		}
	})

	t.Run("invalid boolean", func(t *testing.T) {
		t.Setenv("GAMS_AUTH_SIGNING_REQUIRED", "not_a_bool")
		_, err := config.Load("")
		if err == nil {
			t.Errorf("expected error for invalid GAMS_AUTH_SIGNING_REQUIRED, got nil")
		}
	})
}


func TestValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(c *config.Config)
		wantErr bool
	}{
		{
			name: "empty data path",
			mutate: func(c *config.Config) {
				c.Data.Path = ""
			},
			wantErr: true,
		},
		{
			name: "invalid port 0",
			mutate: func(c *config.Config) {
				c.Server.HTTPPort = 0
			},
			wantErr: true,
		},
		{
			name: "invalid port 70000",
			mutate: func(c *config.Config) {
				c.Server.HTTPPort = 70000
			},
			wantErr: true,
		},
		{
			name: "invalid transport",
			mutate: func(c *config.Config) {
				c.MCP.Transport = "grpc"
			},
			wantErr: true,
		},
		{
			name: "negative claim size",
			mutate: func(c *config.Config) {
				c.Limits.MaxClaimSizeBytes = -1
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			tt.mutate(cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

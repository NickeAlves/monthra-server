package config

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("MONGO_URI", "mongodb://localhost:27017")
	t.Setenv("REDIS_ADDR", "localhost:6379")
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, EnvLocal, cfg.AppEnv)
	assert.Equal(t, slog.LevelInfo, cfg.LogLevel)
	assert.Equal(t, ":8080", cfg.HTTP.Addr)
	assert.Equal(t, ":9464", cfg.HTTP.MetricsAddr)
	assert.Equal(t, 5*time.Second, cfg.HTTP.ReadHeaderTimeout)
	assert.Equal(t, int64(1<<20), cfg.HTTP.MaxBodyBytes)
	assert.Equal(t, "monthra", cfg.Mongo.Database)
	assert.Equal(t, 365*24*time.Hour, cfg.AuditLogTTL)
}

func TestLoadRequiresConnectionSettings(t *testing.T) {
	t.Setenv("MONGO_URI", "")
	t.Setenv("REDIS_ADDR", "")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MONGO_URI")
	assert.Contains(t, err.Error(), "REDIS_ADDR")
}

func TestLoadRejectsUnknownEnv(t *testing.T) {
	setRequired(t)
	t.Setenv("APP_ENV", "prod")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid APP_ENV")
}

func TestLoadParsesLogLevel(t *testing.T) {
	setRequired(t)
	t.Setenv("LOG_LEVEL", "debug")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, slog.LevelDebug, cfg.LogLevel)
}

func TestValidate(t *testing.T) {
	base := func() Config {
		setRequired(t)
		cfg, err := Load()
		require.NoError(t, err)
		return cfg
	}

	tests := map[string]struct {
		mutate  func(*Config)
		wantErr string
	}{
		"redis password required outside local": {
			mutate:  func(c *Config) { c.AppEnv = EnvStaging },
			wantErr: "REDIS_PASSWORD is required in staging",
		},
		"redis password set in production": {
			mutate: func(c *Config) { c.AppEnv = EnvProduction; c.Redis.Password = "s3cret" },
		},
		"metrics must not share the public port": {
			mutate:  func(c *Config) { c.HTTP.MetricsAddr = c.HTTP.Addr },
			wantErr: "METRICS_ADDR must differ",
		},
		"timeouts must be positive": {
			mutate:  func(c *Config) { c.HTTP.WriteTimeout = 0 },
			wantErr: "HTTP_WRITE_TIMEOUT must be greater than zero",
		},
		"body limit must be positive": {
			mutate:  func(c *Config) { c.HTTP.MaxBodyBytes = 0 },
			wantErr: "HTTP_MAX_BODY_BYTES",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := base()
			tc.mutate(&cfg)
			err := cfg.Validate()
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestEnvHelpers(t *testing.T) {
	assert.True(t, EnvLocal.IsLocal())
	assert.False(t, EnvStaging.IsLocal())
	assert.True(t, EnvProduction.IsProduction())
	assert.False(t, EnvStaging.IsProduction())
}

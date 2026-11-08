package config

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
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
	t.Setenv("CSRF_SECRET", testSecret)
}

// testSecret is built at run time so no key-looking literal enters git.
var testSecret = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))

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
	assert.Empty(t, cfg.Security.AllowedOrigins)
	assert.Empty(t, cfg.Security.TrustedProxies)
	assert.GreaterOrEqual(t, len(cfg.Security.CSRFSecret), 32)
	assert.Equal(t, Rate{Limit: 300, Period: time.Minute}, cfg.RateLimits.APIPerIP)
}

func TestLoadParsesSecuritySettings(t *testing.T) {
	setRequired(t)
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://App.Monthra.example:443, http://localhost:3000")
	t.Setenv("HTTP_TRUSTED_PROXIES", "10.0.0.0/8,172.18.0.7, fd00::/8")
	t.Setenv("RATE_LIMIT_API_IP", "20/s")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []string{"https://app.monthra.example", "http://localhost:3000"}, cfg.Security.Origins())
	prefixes := cfg.Security.Prefixes()
	require.Len(t, prefixes, 3)
	assert.Equal(t, "10.0.0.0/8", prefixes[0].String())
	assert.Equal(t, "172.18.0.7/32", prefixes[1].String())
	assert.Equal(t, "fd00::/8", prefixes[2].String())
	assert.Equal(t, Rate{Limit: 20, Period: time.Second}, cfg.RateLimits.APIPerIP)
}

func TestLoadAcceptsEmptyLists(t *testing.T) {
	setRequired(t)
	// docker compose passes "VAR=" lines from .env as empty values.
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	t.Setenv("HTTP_TRUSTED_PROXIES", "")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Empty(t, cfg.Security.AllowedOrigins)
	assert.Empty(t, cfg.Security.TrustedProxies)
}

func TestLoadRejectsBadSecuritySettings(t *testing.T) {
	for name, tc := range map[string]struct{ key, value, wantErr string }{
		"wildcard origin":       {"CORS_ALLOWED_ORIGINS", "*", "explicit http(s) origin"},
		"origin with a path":    {"CORS_ALLOWED_ORIGINS", "https://app.example/login", "scheme://host[:port] only"},
		"origin with wildcards": {"CORS_ALLOWED_ORIGINS", "https://*.example", "wildcards"},
		"null origin":           {"CORS_ALLOWED_ORIGINS", "null", "explicit http(s) origin"},
		"non-http origin":       {"CORS_ALLOWED_ORIGINS", "ftp://app.example", "scheme must be http or https"},
		"bad proxy":             {"HTTP_TRUSTED_PROXIES", "10.0.0.0/33", "invalid trusted proxy"},
		"secret not base64":     {"CSRF_SECRET", "change-me!", "must be base64"},
		"secret too short":      {"CSRF_SECRET", "c2hvcnQ=", "at least 32 bytes"},
		"secret missing":        {"CSRF_SECRET", "", "CSRF_SECRET"},
		"rate without unit":     {"RATE_LIMIT_API_IP", "300", "want <n>/s"},
		"rate with a bad unit":  {"RATE_LIMIT_API_IP", "300/d", "unit must be s, m or h"},
		"rate of zero":          {"RATE_LIMIT_API_IP", "0/m", "between 1 and"},
	} {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestSecretAndRateFormatting(t *testing.T) {
	s := Secret("super secret value")
	assert.Equal(t, "[redacted]", s.String())
	assert.Equal(t, "[redacted]", fmt.Sprintf("%v", s))
	assert.NotContains(t, fmt.Sprintf("%#v", s), "super")

	assert.Equal(t, "300/m", Rate{Limit: 300, Period: time.Minute}.String())
	assert.Equal(t, "5/s", Rate{Limit: 5, Period: time.Second}.String())
	assert.Equal(t, "9/h", Rate{Limit: 9, Period: time.Hour}.String())
}

func TestOriginOf(t *testing.T) {
	o, err := OriginOf("https://App.example:443/path?q=1#frag")
	require.NoError(t, err)
	assert.Equal(t, Origin("https://app.example"), o)

	o, err = OriginOf("http://[::1]:3000/x")
	require.NoError(t, err)
	assert.Equal(t, Origin("http://[::1]:3000"), o)

	_, err = OriginOf("/relative/path")
	assert.Error(t, err)
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
			mutate: func(c *Config) {
				c.AppEnv = EnvProduction
				c.Redis.Password = rand.Text()
				c.Security.AllowedOrigins = []Origin{"https://app.monthra.example"}
			},
		},
		"origins required outside local": {
			mutate:  func(c *Config) { c.AppEnv = EnvStaging },
			wantErr: "CORS_ALLOWED_ORIGINS is required in staging",
		},
		"origins must use https outside local": {
			mutate: func(c *Config) {
				c.AppEnv = EnvProduction
				c.Security.AllowedOrigins = []Origin{"http://app.monthra.example"}
			},
			wantErr: "must use https in production",
		},
		"plain http origins are fine locally": {
			mutate: func(c *Config) { c.Security.AllowedOrigins = []Origin{"http://localhost:3000"} },
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

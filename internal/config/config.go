// Package config loads and validates the service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
)

// Env is the deployment environment. It drives environment-specific behavior
// such as HSTS, API docs exposure and strictness of secret checks.
type Env string

// Supported environments.
const (
	EnvLocal      Env = "local"
	EnvStaging    Env = "staging"
	EnvProduction Env = "production"
)

// UnmarshalText rejects anything other than the supported environments.
func (e *Env) UnmarshalText(text []byte) error {
	switch v := Env(text); v {
	case EnvLocal, EnvStaging, EnvProduction:
		*e = v
		return nil
	default:
		return fmt.Errorf("invalid APP_ENV %q: want local, staging or production", string(text))
	}
}

// IsLocal reports whether the service runs on a developer machine.
func (e Env) IsLocal() bool { return e == EnvLocal }

// IsProduction reports whether the service runs in production.
func (e Env) IsProduction() bool { return e == EnvProduction }

// Config is the complete service configuration.
type Config struct {
	AppEnv      Env        `env:"APP_ENV" envDefault:"local"`
	ServiceName string     `env:"SERVICE_NAME" envDefault:"monthra-server"`
	LogLevel    slog.Level `env:"LOG_LEVEL" envDefault:"info"`

	HTTP       HTTP
	Security   Security
	RateLimits RateLimits
	Auth       Auth
	SMTP       SMTP
	Captcha    Captcha
	Mongo      Mongo
	Redis      Redis

	// AuditLogTTL is how long audit log entries are kept before MongoDB expires them.
	AuditLogTTL time.Duration `env:"AUDIT_LOG_TTL" envDefault:"8760h"`

	// OTLPEndpoint enables trace export when set. The OTLP exporter reads the
	// standard OTEL_EXPORTER_OTLP_* variables itself; this field only switches it on.
	OTLPEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
}

// HTTP holds the public API server and the internal metrics server settings.
type HTTP struct {
	Addr              string        `env:"HTTP_ADDR" envDefault:":8080"`
	MetricsAddr       string        `env:"METRICS_ADDR" envDefault:":9464"`
	ReadHeaderTimeout time.Duration `env:"HTTP_READ_HEADER_TIMEOUT" envDefault:"5s"`
	ReadTimeout       time.Duration `env:"HTTP_READ_TIMEOUT" envDefault:"15s"`
	WriteTimeout      time.Duration `env:"HTTP_WRITE_TIMEOUT" envDefault:"30s"`
	IdleTimeout       time.Duration `env:"HTTP_IDLE_TIMEOUT" envDefault:"60s"`
	ShutdownTimeout   time.Duration `env:"HTTP_SHUTDOWN_TIMEOUT" envDefault:"20s"`
	MaxBodyBytes      int64         `env:"HTTP_MAX_BODY_BYTES" envDefault:"1048576"`
}

// Security holds the browser-facing protections: CORS, CSRF and proxy trust.
type Security struct {
	// AllowedOrigins are the web app origins allowed to call the API from a
	// browser (CORS with credentials, and the Origin check of the CSRF guard).
	// Include the app origin even when it is served from the API's own origin:
	// browsers send Origin on every POST.
	AllowedOrigins []Origin `env:"CORS_ALLOWED_ORIGINS"`
	// TrustedProxies are the reverse proxies whose X-Forwarded-For is believed.
	// Empty means the TCP peer is always the client.
	TrustedProxies []Prefix `env:"HTTP_TRUSTED_PROXIES"`
	// CSRFSecret signs the CSRF tokens (base64, at least 32 bytes).
	CSRFSecret Secret `env:"CSRF_SECRET,notEmpty"`
}

// RateLimits holds the request budgets enforced in Redis.
type RateLimits struct {
	// APIPerIP is the baseline budget of every /api/v1 request, by client IP.
	APIPerIP Rate `env:"RATE_LIMIT_API_IP" envDefault:"300/m"`
	// APIPerUser is the budget of authenticated requests, by user.
	APIPerUser Rate `env:"RATE_LIMIT_API_USER" envDefault:"100/m"`
	// AuthPerIP covers the credential endpoints (register, login, email
	// verification) together, by client IP.
	AuthPerIP Rate `env:"RATE_LIMIT_AUTH_IP" envDefault:"5/m"`
	// AuthPerEmail covers the same endpoints by target email address.
	AuthPerEmail Rate `env:"RATE_LIMIT_AUTH_EMAIL" envDefault:"5/m"`
	// RefreshPerIP is the budget of token refreshes, by client IP.
	RefreshPerIP Rate `env:"RATE_LIMIT_REFRESH_IP" envDefault:"30/m"`
}

// Auth holds the token, password and verification settings (§6.1, §6.2).
type Auth struct {
	// SigningKeys are the Ed25519 seeds (32 bytes each) that sign access tokens.
	SigningKeys KeyRing `env:"JWT_SIGNING_KEYS,notEmpty"`
	// ActiveKID picks the key that signs new tokens; the others only verify.
	ActiveKID       string        `env:"JWT_ACTIVE_KID,notEmpty"`
	AccessTokenTTL  time.Duration `env:"ACCESS_TOKEN_TTL" envDefault:"15m"`
	RefreshTokenTTL time.Duration `env:"REFRESH_TOKEN_TTL" envDefault:"720h"`
	// RefreshReuseGrace is how long a just-rotated refresh token answers 409
	// (another tab refreshed first) instead of revoking the session as reuse.
	RefreshReuseGrace time.Duration `env:"REFRESH_REUSE_GRACE" envDefault:"10s"`
	// EmailTokenTTL is the lifetime of email verification links.
	EmailTokenTTL time.Duration `env:"EMAIL_TOKEN_TTL" envDefault:"30m"`
	// PasswordHashConcurrency bounds parallel Argon2id runs (64 MiB each).
	PasswordHashConcurrency int `env:"PASSWORD_HASH_CONCURRENCY" envDefault:"4"`
	// HIBPEnabled checks new passwords against Have I Been Pwned (k-anonymity).
	HIBPEnabled bool `env:"HIBP_ENABLED" envDefault:"true"`
	// AppPublicURL is the web app base URL used in email links.
	AppPublicURL PublicURL `env:"APP_PUBLIC_URL" envDefault:"http://localhost:3000"`
}

// SMTP holds the transactional email relay settings.
type SMTP struct {
	Host     string   `env:"SMTP_HOST,notEmpty"`
	Port     int      `env:"SMTP_PORT" envDefault:"587"`
	Username string   `env:"SMTP_USERNAME"`
	Password Redacted `env:"SMTP_PASSWORD"`
	// From is the sender, "Monthra <no-reply@example.com>" or a bare address.
	From    string        `env:"SMTP_FROM,notEmpty"`
	TLS     SMTPTLS       `env:"SMTP_TLS" envDefault:"starttls"`
	Timeout time.Duration `env:"SMTP_TIMEOUT" envDefault:"10s"`
	// AllowInsecure permits SMTP_TLS=none in staging, for the local
	// homologation stack that sends to Mailpit. Never set it on a deployment.
	AllowInsecure bool `env:"SMTP_ALLOW_INSECURE"`
}

// Captcha holds the Cloudflare Turnstile settings. An empty secret disables
// the challenge.
type Captcha struct {
	TurnstileSecret Redacted `env:"TURNSTILE_SECRET"`
}

// Enabled reports whether a captcha is verified after repeated failures.
func (c Captcha) Enabled() bool { return c.TurnstileSecret != "" }

// Issuer is the iss claim of access tokens. It differs per environment, so a
// staging token is never accepted in production even if keys leaked across.
func (c Config) Issuer() string { return "monthra-server:" + string(c.AppEnv) }

// Mongo holds the MongoDB connection settings.
type Mongo struct {
	URI            string        `env:"MONGO_URI,notEmpty"`
	Database       string        `env:"MONGO_DATABASE" envDefault:"monthra"`
	ConnectTimeout time.Duration `env:"MONGO_CONNECT_TIMEOUT" envDefault:"10s"`
	MaxPoolSize    uint64        `env:"MONGO_MAX_POOL_SIZE" envDefault:"100"`
}

// Redis holds the Redis connection settings.
type Redis struct {
	Addr     string `env:"REDIS_ADDR,notEmpty"`
	Password string `env:"REDIS_PASSWORD"`
	DB       int    `env:"REDIS_DB" envDefault:"0"`
}

// Load reads the configuration from the environment and validates it.
func Load() (Config, error) {
	cfg, err := env.ParseAs[Config]()
	if err != nil {
		return Config{}, fmt.Errorf("parse environment: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate checks rules that struct tags cannot express.
func (c Config) Validate() error {
	var errs []error
	positive := map[string]time.Duration{
		"HTTP_READ_HEADER_TIMEOUT": c.HTTP.ReadHeaderTimeout,
		"HTTP_READ_TIMEOUT":        c.HTTP.ReadTimeout,
		"HTTP_WRITE_TIMEOUT":       c.HTTP.WriteTimeout,
		"HTTP_IDLE_TIMEOUT":        c.HTTP.IdleTimeout,
		"HTTP_SHUTDOWN_TIMEOUT":    c.HTTP.ShutdownTimeout,
		"MONGO_CONNECT_TIMEOUT":    c.Mongo.ConnectTimeout,
		"AUDIT_LOG_TTL":            c.AuditLogTTL,
		"SMTP_TIMEOUT":             c.SMTP.Timeout,
	}
	for name, d := range positive {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be greater than zero", name))
		}
	}
	if c.HTTP.MaxBodyBytes <= 0 {
		errs = append(errs, errors.New("HTTP_MAX_BODY_BYTES must be greater than zero"))
	}
	if c.HTTP.Addr == c.HTTP.MetricsAddr {
		errs = append(errs, errors.New("METRICS_ADDR must differ from HTTP_ADDR so /metrics stays off the public port"))
	}
	if c.Mongo.Database == "" {
		errs = append(errs, errors.New("MONGO_DATABASE must not be empty"))
	}
	if !c.AppEnv.IsLocal() && c.Redis.Password == "" {
		errs = append(errs, fmt.Errorf("REDIS_PASSWORD is required in %s", c.AppEnv))
	}
	errs = append(errs, c.Security.validate(c.AppEnv)...)
	errs = append(errs, c.Auth.validate(c.AppEnv)...)
	errs = append(errs, c.SMTP.validate(c.AppEnv)...)
	return errors.Join(errs...)
}

// Bounds of the auth settings.
const (
	ed25519SeedBytes   = 32
	minAccessTokenTTL  = time.Minute
	maxAccessTokenTTL  = 15 * time.Minute
	minRefreshTokenTTL = time.Hour
	maxRefreshTokenTTL = 90 * 24 * time.Hour
	minEmailTokenTTL   = 5 * time.Minute
	maxEmailTokenTTL   = time.Hour
	maxHashConcurrency = 64
	maxRefreshGrace    = time.Minute
)

func (a Auth) validate(appEnv Env) []error {
	var errs []error
	if _, ok := a.SigningKeys.Key(a.ActiveKID); !ok {
		errs = append(errs, fmt.Errorf("JWT_ACTIVE_KID %q is not in JWT_SIGNING_KEYS", a.ActiveKID))
	}
	for _, kid := range a.SigningKeys.KIDs() {
		if key, _ := a.SigningKeys.Key(kid); len(key) != ed25519SeedBytes {
			errs = append(errs, fmt.Errorf("JWT_SIGNING_KEYS key %q must decode to %d bytes (an Ed25519 seed)", kid, ed25519SeedBytes))
		}
	}
	if a.AccessTokenTTL < minAccessTokenTTL || a.AccessTokenTTL > maxAccessTokenTTL {
		errs = append(errs, fmt.Errorf("ACCESS_TOKEN_TTL must be between %s and %s", minAccessTokenTTL, maxAccessTokenTTL))
	}
	if a.RefreshTokenTTL < minRefreshTokenTTL || a.RefreshTokenTTL > maxRefreshTokenTTL {
		errs = append(errs, fmt.Errorf("REFRESH_TOKEN_TTL must be between %s and %s", minRefreshTokenTTL, maxRefreshTokenTTL))
	}
	if a.RefreshReuseGrace < 0 || a.RefreshReuseGrace > maxRefreshGrace {
		errs = append(errs, fmt.Errorf("REFRESH_REUSE_GRACE must be between 0s and %s", maxRefreshGrace))
	}
	if a.EmailTokenTTL < minEmailTokenTTL || a.EmailTokenTTL > maxEmailTokenTTL {
		errs = append(errs, fmt.Errorf("EMAIL_TOKEN_TTL must be between %s and %s", minEmailTokenTTL, maxEmailTokenTTL))
	}
	if a.PasswordHashConcurrency < 1 || a.PasswordHashConcurrency > maxHashConcurrency {
		errs = append(errs, fmt.Errorf("PASSWORD_HASH_CONCURRENCY must be between 1 and %d", maxHashConcurrency))
	}
	if !appEnv.IsLocal() && !strings.HasPrefix(string(a.AppPublicURL), "https://") {
		errs = append(errs, fmt.Errorf("APP_PUBLIC_URL must use https in %s", appEnv))
	}
	return errs
}

func (s SMTP) validate(appEnv Env) []error {
	var errs []error
	if s.Port < 1 || s.Port > 65535 {
		errs = append(errs, errors.New("SMTP_PORT must be between 1 and 65535"))
	}
	if addr, err := mail.ParseAddress(s.From); err != nil || addr.Address == "" {
		errs = append(errs, fmt.Errorf("SMTP_FROM is not a valid address: %q", s.From))
	}
	if s.TLS == SMTPTLSNone {
		switch {
		case s.Username != "":
			errs = append(errs, errors.New("SMTP_USERNAME requires SMTP_TLS=starttls or tls: credentials never travel in clear text"))
		case appEnv.IsProduction():
			errs = append(errs, errors.New("SMTP_TLS=none is not allowed in production"))
		case appEnv == EnvStaging && !s.AllowInsecure:
			errs = append(errs, errors.New("SMTP_TLS=none in staging requires SMTP_ALLOW_INSECURE=true (local homologation with Mailpit only)"))
		}
	}
	if s.AllowInsecure && appEnv.IsProduction() {
		errs = append(errs, errors.New("SMTP_ALLOW_INSECURE must not be set in production"))
	}
	if s.Username != "" && s.Password == "" {
		errs = append(errs, errors.New("SMTP_PASSWORD is required when SMTP_USERNAME is set"))
	}
	return errs
}

func (s Security) validate(appEnv Env) []error {
	var errs []error
	if len(s.CSRFSecret) < minSecretBytes {
		errs = append(errs, fmt.Errorf("CSRF_SECRET must decode to at least %d bytes", minSecretBytes))
	}
	if !appEnv.IsLocal() {
		if len(s.AllowedOrigins) == 0 {
			errs = append(errs, fmt.Errorf("CORS_ALLOWED_ORIGINS is required in %s", appEnv))
		}
		for _, o := range s.AllowedOrigins {
			if !strings.HasPrefix(string(o), "https://") {
				errs = append(errs, fmt.Errorf("CORS_ALLOWED_ORIGINS must use https in %s: %s", appEnv, o))
			}
		}
	}
	return errs
}

// Origins returns the allowed origins as plain strings.
func (s Security) Origins() []string {
	out := make([]string, len(s.AllowedOrigins))
	for i, o := range s.AllowedOrigins {
		out[i] = string(o)
	}
	return out
}

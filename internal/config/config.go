// Package config loads and validates the service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
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

	HTTP  HTTP
	Mongo Mongo
	Redis Redis

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
	return errors.Join(errs...)
}

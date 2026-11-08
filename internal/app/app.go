// Package app wires configuration, dependencies and servers, and runs the
// service until its context is cancelled.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/csrf"
	apihttp "github.com/NickeAlves/monthra-server/internal/http"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/handler"
	"github.com/NickeAlves/monthra-server/internal/observability"
	"github.com/NickeAlves/monthra-server/internal/ratelimit"
	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
	"github.com/NickeAlves/monthra-server/migrations"
)

// readinessTimeout bounds the dependency checks of /readyz.
const readinessTimeout = 2 * time.Second

// Run starts the API and the metrics server and blocks until ctx is cancelled
// (SIGINT/SIGTERM) or a server fails; then it drains connections within the
// shutdown timeout, flushes traces and closes the database clients.
func Run(ctx context.Context, cfg config.Config, logger *slog.Logger, version string) (err error) {
	shutdownTracing, err := observability.SetupTracing(ctx, observability.TracingOptions{
		Service: cfg.ServiceName,
		Version: version,
		Env:     string(cfg.AppEnv),
		Enabled: cfg.OTLPEndpoint != "",
	})
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, shutdownTracing(flushCtx))
	}()

	mongoClient, err := repomongo.Connect(ctx, cfg.Mongo, cfg.ServiceName)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err = errors.Join(err, mongoClient.Disconnect(closeCtx))
	}()

	if err := migrations.Apply(ctx, mongoClient.Database(cfg.Mongo.Database), migrations.Options{AuditLogTTL: cfg.AuditLogTTL}); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	logger.InfoContext(ctx, "mongo indexes ensured", slog.String("database", cfg.Mongo.Database))

	cache.SetLogger(logger)
	redisClient, err := cache.Connect(ctx, cfg.Redis)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, redisClient.Close()) }()

	csrfSigner, err := csrf.NewSigner(cfg.Security.CSRFSecret)
	if err != nil {
		return err
	}

	metrics := observability.NewMetrics(version, string(cfg.AppEnv))
	// Outside local, TLS terminates at the monthra-infra proxy.
	cookies := cookie.Policy{Secure: !cfg.AppEnv.IsLocal()}
	keys := cache.NewKeys(cfg.AppEnv)
	limiter := ratelimit.NewRedis(redisClient)

	mailer, err := newMailer(cfg, logger, metrics)
	if err != nil {
		return err
	}
	defer func() {
		// Runs after the servers stopped, so no request can enqueue anymore.
		drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.HTTP.ShutdownTimeout)
		defer cancel()
		err = errors.Join(err, mailer.Close(drainCtx))
	}()

	authn, err := newAuth(cfg, logger, metrics, authDeps{
		db: mongoClient.Database(cfg.Mongo.Database), redis: redisClient, keys: keys,
		limiter: limiter, mailer: mailer, cookies: cookies,
	})
	if err != nil {
		return err
	}

	health := handler.NewHealth(logger, readinessTimeout, map[string]handler.Pinger{
		"mongo": repomongo.Pinger{Client: mongoClient},
		"redis": cache.Pinger{Client: redisClient},
	})
	router := apihttp.NewRouter(apihttp.RouterOptions{
		Logger:       logger,
		Metrics:      metrics,
		Health:       health,
		MaxBodyBytes: cfg.HTTP.MaxBodyBytes,
		HSTS:         !cfg.AppEnv.IsLocal(),
		ServeDocs:    !cfg.AppEnv.IsProduction(),

		AllowedOrigins: cfg.Security.Origins(),
		TrustedProxies: cfg.Security.Prefixes(),
		Cookies:        cookies,
		CSRFSigner:     csrfSigner,

		Limiter:     limiter,
		Keys:        keys,
		APIRateByIP: cfg.RateLimits.APIPerIP,

		Auth:            authn.handler,
		Authenticator:   authn.authenticator,
		AuthRateByIP:    cfg.RateLimits.AuthPerIP,
		RefreshRateByIP: cfg.RateLimits.RefreshPerIP,
		APIRateByUser:   cfg.RateLimits.APIPerUser,
	})

	api := apihttp.NewServer(cfg.HTTP.Addr, router, cfg.HTTP, logger)
	metricsSrv := apihttp.NewServer(cfg.HTTP.MetricsAddr, apihttp.NewMetricsHandler(metrics.Handler()), cfg.HTTP, logger)
	return serve(ctx, logger, cfg.HTTP.ShutdownTimeout, api, metricsSrv)
}

// serve runs every server until ctx ends or one fails, then shuts them all down.
func serve(ctx context.Context, logger *slog.Logger, shutdownTimeout time.Duration, servers ...*http.Server) error {
	// Bind every port first, so a taken port fails before anything is served.
	listeners := make([]net.Listener, 0, len(servers))
	var lc net.ListenConfig
	for _, srv := range servers {
		ln, err := lc.Listen(ctx, "tcp", srv.Addr)
		if err != nil {
			for _, open := range listeners {
				_ = open.Close()
			}
			return fmt.Errorf("listen on %s: %w", srv.Addr, err)
		}
		listeners = append(listeners, ln)
	}

	g, gctx := errgroup.WithContext(ctx)
	for i, srv := range servers {
		ln := listeners[i]
		logger.InfoContext(ctx, "listening", slog.String("addr", ln.Addr().String()))
		g.Go(func() error {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("serve %s: %w", srv.Addr, err)
			}
			return nil
		})
	}
	g.Go(func() error {
		<-gctx.Done()
		logger.InfoContext(ctx, "shutting down", slog.Duration("timeout", shutdownTimeout))
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()
		var errs []error
		for _, srv := range servers {
			if err := srv.Shutdown(shutdownCtx); err != nil {
				errs = append(errs, fmt.Errorf("shutdown %s: %w", srv.Addr, err))
			}
		}
		return errors.Join(errs...)
	})
	return g.Wait()
}

package app

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/NickeAlves/monthra-server/internal/audit"
	"github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/cache"
	"github.com/NickeAlves/monthra-server/internal/captcha"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/crypto/aead"
	"github.com/NickeAlves/monthra-server/internal/email"
	"github.com/NickeAlves/monthra-server/internal/http/cookie"
	"github.com/NickeAlves/monthra-server/internal/http/handler"
	"github.com/NickeAlves/monthra-server/internal/http/middleware"
	"github.com/NickeAlves/monthra-server/internal/observability"
	"github.com/NickeAlves/monthra-server/internal/ratelimit"
	repomongo "github.com/NickeAlves/monthra-server/internal/repository/mongo"
	authuc "github.com/NickeAlves/monthra-server/internal/usecase/auth"
	"github.com/NickeAlves/monthra-server/internal/usecase/profile"
)

// refreshGrace maps the configured grace to the use case option, where zero
// means "default" and a negative value disables the window.
func refreshGrace(d time.Duration) time.Duration {
	if d == 0 {
		return -1
	}
	return d
}

// newMailer builds the asynchronous email dispatcher on the SMTP relay.
func newMailer(cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) (*email.Dispatcher, error) {
	sender, err := email.NewSMTP(cfg.SMTP)
	if err != nil {
		return nil, err
	}
	renderer, err := email.NewRenderer()
	if err != nil {
		return nil, err
	}
	return email.NewDispatcher(email.DispatcherOptions{Sender: sender, Renderer: renderer, Logger: logger, Metrics: metrics}), nil
}

type authDeps struct {
	db      *mongo.Database
	redis   *redis.Client
	keys    cache.Keys
	limiter ratelimit.Limiter
	mailer  authuc.Mailer
	cookies cookie.Policy
}

type authComponents struct {
	handler       *handler.Auth
	me            *handler.Me
	locales       middleware.LocaleResolver
	authenticator middleware.AuthOptions
}

// newAuth wires the authentication use case, its stores and its handlers.
func newAuth(cfg config.Config, logger *slog.Logger, metrics *observability.Metrics, d authDeps) (authComponents, error) {
	hasher, err := auth.NewHasher(auth.DefaultParams, cfg.Auth.PasswordHashConcurrency)
	if err != nil {
		return authComponents{}, err
	}
	seeds := map[string][]byte{}
	for _, kid := range cfg.Auth.SigningKeys.KIDs() {
		seeds[kid], _ = cfg.Auth.SigningKeys.Key(kid)
	}
	tokens, err := auth.NewJWT(auth.JWTOptions{Seeds: seeds, ActiveKID: cfg.Auth.ActiveKID, Issuer: cfg.Issuer(), TTL: cfg.Auth.AccessTokenTTL})
	if err != nil {
		return authComponents{}, fmt.Errorf("access tokens: %w", err)
	}

	var breach auth.BreachChecker = auth.NoBreachCheck{}
	if cfg.Auth.HIBPEnabled {
		breach = auth.NewHIBP(nil, auth.HIBPRangeURL)
	}
	var verifier captcha.Verifier = captcha.Disabled{}
	if cfg.Captcha.Enabled() {
		verifier = captcha.NewTurnstile(cfg.Captcha.TurnstileSecret.Reveal(), captcha.TurnstileURL, nil)
	}
	revocations := cache.NewRevocations(d.redis, d.keys)
	encryptionKeys := map[string][]byte{}
	for _, kid := range cfg.Encryption.Keys.KIDs() {
		encryptionKeys[kid], _ = cfg.Encryption.Keys.Key(kid)
	}
	sealer, err := aead.New(encryptionKeys, cfg.Encryption.ActiveKID)
	if err != nil {
		return authComponents{}, fmt.Errorf("encryption keys: %w", err)
	}

	users := repomongo.NewUsers(d.db)
	locales := cache.NewUserLocales(d.redis, d.keys, cache.DefaultLocaleTTL)
	service := authuc.NewService(authuc.Deps{
		Users:         users,
		Sessions:      repomongo.NewSessions(d.db),
		RefreshTokens: repomongo.NewRefreshTokens(d.db),
		AuthTokens:    repomongo.NewAuthTokens(d.db),
		Revocations:   revocations,
		Lockout:       cache.NewLockout(d.redis, d.keys, auth.DefaultLockoutPolicy),
		MFAChallenges: cache.NewMFAChallenges(d.redis, d.keys),
		Sealer:        sealer,
		EmailLimit: ratelimit.Bucket{Limiter: d.limiter, Keys: d.keys, Policy: ratelimit.Policy{
			Name: authuc.PolicyAuthByEmail, Rate: cfg.RateLimits.AuthPerEmail,
		}},
		MailLimit: ratelimit.Bucket{Limiter: d.limiter, Keys: d.keys, Policy: ratelimit.Policy{
			Name: authuc.PolicyAuthMail, Rate: cfg.RateLimits.AuthMailPerEmail,
		}},
		Captcha: verifier,
		Breach:  breach,
		Hasher:  hasher,
		Tokens:  tokens,
		Mailer:  d.mailer,
		Auditor: audit.NewRecorder(repomongo.NewAuditLogs(d.db), logger),
		Metrics: metrics,
	}, authuc.Options{
		RefreshTTL:    cfg.Auth.RefreshTokenTTL,
		EmailTokenTTL: cfg.Auth.EmailTokenTTL,
		ResetTokenTTL: cfg.Auth.ResetTokenTTL,
		AppURL:        cfg.Auth.AppPublicURL,
		MFAIssuer:     cfg.MFAIssuer(),
		Lockout:       auth.DefaultLockoutPolicy,
		RefreshGrace:  refreshGrace(cfg.Auth.RefreshReuseGrace),
		Logger:        logger,
	})

	profiles := profile.NewService(users, locales, profile.Options{Logger: logger})

	return authComponents{
		handler:       &handler.Auth{Service: service, Cookies: d.cookies, Logger: logger},
		me:            &handler.Me{Profile: profiles, Cookies: d.cookies, Logger: logger},
		locales:       profiles,
		authenticator: middleware.AuthOptions{Tokens: tokens, Revocations: revocations, Cookies: d.cookies, Logger: logger},
	}, nil
}

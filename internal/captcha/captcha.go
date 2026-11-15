// Package captcha verifies human challenges (Cloudflare Turnstile) that the
// API asks for after repeated login or sign-up attempts (§6.4).
package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Actions name the form a challenge was solved on. The client widget sends
// the one of its form (`action`), and siteverify echoes it back.
const (
	ActionLogin    = "login"
	ActionRegister = "register"
)

// Challenge is a token to check, with the form it must come from.
type Challenge struct {
	Token string
	// Action is the form the token must have been solved on (ActionLogin...).
	Action string
	// RemoteIP is the client address, passed on to the provider.
	RemoteIP string
}

// Verifier checks a challenge token produced by the client widget.
type Verifier interface {
	// Enabled reports whether challenges are asked for at all.
	Enabled() bool
	// Verify reports whether c is a valid, unused challenge answer.
	// An error means the provider could not be asked.
	Verify(ctx context.Context, c Challenge) (bool, error)
}

// Disabled is the Verifier used without TURNSTILE_SECRET: no challenge is asked.
type Disabled struct{}

// Enabled reports false.
func (Disabled) Enabled() bool { return false }

// Verify accepts everything; it is never called while disabled.
func (Disabled) Verify(context.Context, Challenge) (bool, error) { return true, nil }

// TurnstileURL is the Turnstile siteverify endpoint.
const TurnstileURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

const (
	turnstileTimeout = 5 * time.Second
	// maxTokenLength is the longest token Turnstile issues.
	maxTokenLength = 2048
)

// TurnstileOptions configures a Turnstile verifier.
type TurnstileOptions struct {
	Secret string
	// URL is the siteverify endpoint (TurnstileURL outside tests).
	URL string
	// Hostnames, when set, are the only pages a token may come from, and the
	// token must also carry the Challenge's Action. Empty skips both checks:
	// Cloudflare's testing keys answer with hostname example.com and no action.
	Hostnames []string
	// Client defaults to one with a 5 s timeout.
	Client *http.Client
	// Logger defaults to slog.Default().
	Logger *slog.Logger
}

// Turnstile verifies tokens with Cloudflare's siteverify API.
type Turnstile struct {
	secret    string
	url       string
	hostnames []string
	client    *http.Client
	logger    *slog.Logger
}

// NewTurnstile returns a verifier for o.
func NewTurnstile(o TurnstileOptions) *Turnstile {
	if o.Client == nil {
		o.Client = &http.Client{Timeout: turnstileTimeout}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return &Turnstile{secret: o.Secret, url: o.URL, hostnames: o.Hostnames, client: o.Client, logger: o.Logger}
}

// Enabled reports true.
func (*Turnstile) Enabled() bool { return true }

// siteverifyAnswer is the part of the siteverify answer the API reads.
type siteverifyAnswer struct {
	Success    bool     `json:"success"`
	Hostname   string   `json:"hostname"`
	Action     string   `json:"action"`
	ErrorCodes []string `json:"error-codes"`
}

// Verify asks siteverify about c.Token. With Hostnames configured, a token
// solved on another page or another form is refused even when it is valid.
func (t *Turnstile) Verify(ctx context.Context, c Challenge) (bool, error) {
	if c.Token == "" || len(c.Token) > maxTokenLength {
		return false, nil
	}
	form := url.Values{"secret": {t.secret}, "response": {c.Token}}
	if c.RemoteIP != "" {
		form.Set("remoteip", c.RemoteIP)
	}
	ctx, cancel := context.WithTimeout(ctx, turnstileTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, strings.NewReader(form.Encode()))
	if err != nil {
		return false, fmt.Errorf("turnstile request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("turnstile request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("turnstile answered %d", resp.StatusCode)
	}
	var out siteverifyAnswer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return false, fmt.Errorf("decode turnstile answer: %w", err)
	}
	if !out.Success {
		t.logger.InfoContext(ctx, "captcha token refused", slog.String("action", c.Action), slog.Any("error_codes", out.ErrorCodes))
		return false, nil
	}
	if len(t.hostnames) == 0 {
		return true, nil
	}
	if !slices.Contains(t.hostnames, strings.ToLower(out.Hostname)) {
		t.logger.WarnContext(ctx, "captcha token from another hostname", slog.String("hostname", out.Hostname), slog.String("action", c.Action))
		return false, nil
	}
	if out.Action != c.Action {
		t.logger.WarnContext(ctx, "captcha token from another form", slog.String("got", out.Action), slog.String("want", c.Action))
		return false, nil
	}
	return true, nil
}

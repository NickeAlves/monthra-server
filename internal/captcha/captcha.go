// Package captcha verifies human challenges (Cloudflare Turnstile) that the
// API asks for after repeated login or sign-up attempts (§6.4).
package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Verifier checks a challenge token produced by the client widget.
type Verifier interface {
	// Enabled reports whether challenges are asked for at all.
	Enabled() bool
	// Verify reports whether token is a valid, unused challenge answer.
	// An error means the provider could not be asked.
	Verify(ctx context.Context, token, remoteIP string) (bool, error)
}

// Disabled is the Verifier used without TURNSTILE_SECRET: no challenge is asked.
type Disabled struct{}

// Enabled reports false.
func (Disabled) Enabled() bool { return false }

// Verify accepts everything; it is never called while disabled.
func (Disabled) Verify(context.Context, string, string) (bool, error) { return true, nil }

// TurnstileURL is the Turnstile siteverify endpoint.
const TurnstileURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

const (
	turnstileTimeout = 5 * time.Second
	// maxTokenLength is the longest token Turnstile issues.
	maxTokenLength = 2048
)

// Turnstile verifies tokens with Cloudflare's siteverify API.
type Turnstile struct {
	secret string
	url    string
	client *http.Client
}

// NewTurnstile returns a verifier for secret on url (TurnstileURL in production).
func NewTurnstile(secret, url string, client *http.Client) *Turnstile {
	if client == nil {
		client = &http.Client{Timeout: turnstileTimeout}
	}
	return &Turnstile{secret: secret, url: url, client: client}
}

// Enabled reports true.
func (*Turnstile) Enabled() bool { return true }

// Verify asks siteverify about token.
func (t *Turnstile) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	if token == "" || len(token) > maxTokenLength {
		return false, nil
	}
	form := url.Values{"secret": {t.secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
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
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return false, fmt.Errorf("decode turnstile answer: %w", err)
	}
	return out.Success, nil
}

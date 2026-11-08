package auth

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // G505: the Pwned Passwords range API is keyed by SHA-1; nothing is protected by it
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HIBPRangeURL is the Pwned Passwords k-anonymity range API.
const HIBPRangeURL = "https://api.pwnedpasswords.com/range/"

const (
	hibpTimeout = 2 * time.Second
	// maxRangeBody bounds the response (a padded range is ~30 KB).
	maxRangeBody = 1 << 20
)

// BreachChecker tells whether a password appeared in a known data breach.
type BreachChecker interface {
	Breached(ctx context.Context, password string) (bool, error)
}

// HIBP checks passwords against Have I Been Pwned with k-anonymity: only the
// first 5 hex characters of the SHA-1 leave the server, and Add-Padding hides
// the real size of the answer.
type HIBP struct {
	client  *http.Client
	baseURL string
}

// NewHIBP returns a checker on baseURL (HIBPRangeURL in production).
func NewHIBP(client *http.Client, baseURL string) *HIBP {
	if client == nil {
		client = &http.Client{Timeout: hibpTimeout}
	}
	return &HIBP{client: client, baseURL: baseURL}
}

// Breached reports whether password is in the Pwned Passwords corpus.
func (h *HIBP) Breached(ctx context.Context, password string) (bool, error) {
	sum := sha1.Sum([]byte(password)) //nolint:gosec // G401: required by the range API, see above
	digest := strings.ToUpper(hex.EncodeToString(sum[:]))
	prefix, suffix := digest[:5], digest[5:]

	ctx, cancel := context.WithTimeout(ctx, hibpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.baseURL+prefix, nil)
	if err != nil {
		return false, fmt.Errorf("hibp request: %w", err)
	}
	req.Header.Set("Add-Padding", "true")
	req.Header.Set("User-Agent", "monthra-server")
	resp, err := h.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("hibp request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("hibp answered %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, maxRangeBody))
	for scanner.Scan() {
		hashSuffix, count, ok := strings.Cut(strings.TrimSpace(scanner.Text()), ":")
		// Padding entries have a count of 0.
		if ok && strings.EqualFold(hashSuffix, suffix) && count != "0" {
			return true, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("read hibp answer: %w", err)
	}
	return false, nil
}

// NoBreachCheck is the BreachChecker used when HIBP_ENABLED is false.
type NoBreachCheck struct{}

// Breached always reports false.
func (NoBreachCheck) Breached(context.Context, string) (bool, error) { return false, nil }

package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// minSecretBytes is the minimum decoded length of HMAC and encryption keys.
const minSecretBytes = 32

// Origin is a normalized web origin: lowercase scheme://host[:port], without
// the default port, path, query or credentials. It is what browsers send in
// the Origin header.
type Origin string

// UnmarshalText parses and normalizes an origin. Wildcards and "null" are rejected.
func (o *Origin) UnmarshalText(text []byte) error {
	v, err := ParseOrigin(string(text))
	if err != nil {
		return err
	}
	*o = v
	return nil
}

// ParseOrigin normalizes raw into an Origin, or fails when it is not a plain
// http(s) origin. Request Origin and Referer values go through it too, so
// they compare equal to the configured allowlist.
func ParseOrigin(raw string) (Origin, error) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "*" || s == "null" {
		return "", fmt.Errorf("invalid origin %q: an explicit http(s) origin is required", raw)
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid origin %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("invalid origin %q: scheme must be http or https", raw)
	}
	if u.Host == "" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("invalid origin %q: want scheme://host[:port] only", raw)
	}
	if strings.Contains(u.Host, "*") {
		return "", fmt.Errorf("invalid origin %q: wildcards are not allowed", raw)
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // IPv6 literal
	}
	if port != "" {
		host += ":" + port
	}
	return Origin(scheme + "://" + host), nil
}

// OriginOf returns the origin of an absolute URL such as a Referer header.
func OriginOf(rawURL string) (Origin, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid URL %q", rawURL)
	}
	return ParseOrigin(u.Scheme + "://" + u.Host)
}

// Prefix is a CIDR range. A bare address means that single host.
type Prefix struct{ netip.Prefix }

// UnmarshalText parses "10.0.0.0/8", "fd00::/8" or a bare address.
func (p *Prefix) UnmarshalText(text []byte) error {
	s := strings.TrimSpace(string(text))
	if !strings.Contains(s, "/") {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			return fmt.Errorf("invalid trusted proxy %q: %w", s, err)
		}
		addr = addr.Unmap()
		p.Prefix = netip.PrefixFrom(addr, addr.BitLen())
		return nil
	}
	prefix, err := netip.ParsePrefix(s)
	if err != nil {
		return fmt.Errorf("invalid trusted proxy %q: %w", s, err)
	}
	p.Prefix = prefix.Masked()
	return nil
}

// Prefixes returns the trusted proxy ranges.
func (s Security) Prefixes() []netip.Prefix {
	out := make([]netip.Prefix, len(s.TrustedProxies))
	for i, p := range s.TrustedProxies {
		out[i] = p.Prefix
	}
	return out
}

// Secret is a base64-encoded key. It never prints its value.
type Secret []byte

// UnmarshalText decodes standard or URL-safe base64, padded or not.
func (s *Secret) UnmarshalText(text []byte) error {
	raw := strings.TrimSpace(string(text))
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(raw); err == nil {
			*s = b
			return nil
		}
	}
	return errors.New("secret must be base64 (generate one with: make secrets)")
}

// String redacts the secret.
func (s Secret) String() string { return "[redacted]" }

// GoString redacts the secret in %#v.
func (s Secret) GoString() string { return "config.Secret([redacted])" }

// Rate is a request budget such as 300/m: Limit requests per Period.
type Rate struct {
	Limit  int
	Period time.Duration
}

// maxRateLimit bounds configured budgets to sane values.
const maxRateLimit = 1_000_000

// UnmarshalText parses "<n>/s", "<n>/m" or "<n>/h".
func (r *Rate) UnmarshalText(text []byte) error {
	s := strings.TrimSpace(string(text))
	count, unit, ok := strings.Cut(s, "/")
	if !ok {
		return fmt.Errorf("invalid rate %q: want <n>/s, <n>/m or <n>/h", s)
	}
	n, err := strconv.Atoi(count)
	if err != nil || n < 1 || n > maxRateLimit {
		return fmt.Errorf("invalid rate %q: the count must be between 1 and %d", s, maxRateLimit)
	}
	var period time.Duration
	switch unit {
	case "s":
		period = time.Second
	case "m":
		period = time.Minute
	case "h":
		period = time.Hour
	default:
		return fmt.Errorf("invalid rate %q: the unit must be s, m or h", s)
	}
	*r = Rate{Limit: n, Period: period}
	return nil
}

// String formats the rate as it is configured.
func (r Rate) String() string {
	unit := r.Period.String()
	switch r.Period {
	case time.Second:
		unit = "s"
	case time.Minute:
		unit = "m"
	case time.Hour:
		unit = "h"
	}
	return strconv.Itoa(r.Limit) + "/" + unit
}

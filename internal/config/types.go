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

// KeyRing is a set of named keys, "kid:base64,kid2:base64", so keys can be
// rotated: new material signs or encrypts under a new kid while the old kids
// keep verifying or decrypting. It never prints the key material.
type KeyRing struct {
	keys map[string][]byte
	kids []string
}

// maxKIDLen bounds key ids, which travel in JWT headers and ciphertexts.
const maxKIDLen = 32

// UnmarshalText parses comma-separated kid:base64 pairs.
func (k *KeyRing) UnmarshalText(text []byte) error {
	ring := KeyRing{keys: map[string][]byte{}}
	for entry := range strings.SplitSeq(string(text), ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		kid, material, ok := strings.Cut(entry, ":")
		if !ok {
			return errors.New("invalid key ring entry: want kid:base64 (generate with: make secrets)")
		}
		kid = strings.TrimSpace(kid)
		if !validKID(kid) {
			return fmt.Errorf("invalid key id %q: use 1 to %d letters, digits, '-' or '_'", kid, maxKIDLen)
		}
		if _, dup := ring.keys[kid]; dup {
			return fmt.Errorf("duplicate key id %q", kid)
		}
		var secret Secret
		if err := secret.UnmarshalText([]byte(material)); err != nil {
			return fmt.Errorf("key %q: %w", kid, err)
		}
		ring.keys[kid] = secret
		ring.kids = append(ring.kids, kid)
	}
	if len(ring.kids) == 0 {
		return errors.New("the key ring is empty")
	}
	*k = ring
	return nil
}

func validKID(kid string) bool {
	if kid == "" || len(kid) > maxKIDLen {
		return false
	}
	for _, c := range kid {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !letter && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// Key returns a copy of the key material of kid.
func (k KeyRing) Key(kid string) ([]byte, bool) {
	b, ok := k.keys[kid]
	if !ok {
		return nil, false
	}
	return append([]byte(nil), b...), true
}

// KIDs returns the key ids in configuration order.
func (k KeyRing) KIDs() []string { return append([]string(nil), k.kids...) }

// String lists the key ids only.
func (k KeyRing) String() string { return "KeyRing[" + strings.Join(k.kids, ",") + "]" }

// GoString redacts the key material in %#v.
func (k KeyRing) GoString() string { return "config." + k.String() }

// Redacted is a plain-text secret (a password or an API secret). It never prints its value.
type Redacted string

// String redacts the value.
func (r Redacted) String() string {
	if r == "" {
		return ""
	}
	return "[redacted]"
}

// GoString redacts the value in %#v.
func (r Redacted) GoString() string { return "config.Redacted(" + r.String() + ")" }

// Reveal returns the secret value, for the one place that must send it.
func (r Redacted) Reveal() string { return string(r) }

// SMTPTLS is how the SMTP connection is encrypted.
type SMTPTLS string

// SMTP encryption modes.
const (
	// SMTPTLSNone sends in clear text (local Mailpit only).
	SMTPTLSNone SMTPTLS = "none"
	// SMTPTLSStartTLS upgrades a plain connection and fails when the server cannot (port 587).
	SMTPTLSStartTLS SMTPTLS = "starttls"
	// SMTPTLSImplicit connects with TLS from the start (port 465).
	SMTPTLSImplicit SMTPTLS = "tls"
)

// UnmarshalText accepts none, starttls or tls.
func (t *SMTPTLS) UnmarshalText(text []byte) error {
	switch v := SMTPTLS(strings.ToLower(strings.TrimSpace(string(text)))); v {
	case SMTPTLSNone, SMTPTLSStartTLS, SMTPTLSImplicit:
		*t = v
		return nil
	default:
		return fmt.Errorf("invalid SMTP_TLS %q: want none, starttls or tls", string(text))
	}
}

// PublicURL is the absolute base URL of the web app, without a trailing
// slash, used to build the links sent by email.
type PublicURL string

// UnmarshalText accepts an absolute http(s) URL without query or fragment.
func (p *PublicURL) UnmarshalText(text []byte) error {
	s := strings.TrimRight(strings.TrimSpace(string(text)), "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid APP_PUBLIC_URL %q: want an absolute http(s) URL without query or fragment", string(text))
	}
	*p = PublicURL(s)
	return nil
}

// Join appends path (which starts with /) to the base URL.
func (p PublicURL) Join(path string) string { return string(p) + path }

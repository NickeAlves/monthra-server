package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NickeAlves/monthra-server/internal/audit"
	core "github.com/NickeAlves/monthra-server/internal/auth"
	"github.com/NickeAlves/monthra-server/internal/config"
	"github.com/NickeAlves/monthra-server/internal/crypto/aead"
	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/email"
	"github.com/NickeAlves/monthra-server/internal/ratelimit"
)

var errDown = errors.New("store down")

type clock struct{ t time.Time }

func newClock() *clock                   { return &clock{t: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)} }
func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func ptr[T any](v T) *T { return &v }

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

func idFor(prefix string, n int) string { return fmt.Sprintf("%s%023d", prefix, n) }

type memUsers struct {
	mu        sync.Mutex
	byID      map[string]domain.User
	err       error
	deleteErr error
	seq       int
	hashes    []string
}

func (m *memUsers) Create(_ context.Context, u domain.User) (domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.User{}, m.err
	}
	for _, existing := range m.byID {
		if existing.Email == u.Email {
			return domain.User{}, domain.ErrEmailTaken
		}
	}
	m.seq++
	u.ID = idFor("u", m.seq)
	if m.byID == nil {
		m.byID = map[string]domain.User{}
	}
	m.byID[u.ID] = u
	return u, nil
}

func (m *memUsers) ByEmail(_ context.Context, addr string) (domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.User{}, m.err
	}
	for _, u := range m.byID {
		if u.Email == addr {
			return u, nil
		}
	}
	return domain.User{}, domain.ErrNotFound
}

func (m *memUsers) ByID(_ context.Context, id string) (domain.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.User{}, m.err
	}
	u, ok := m.byID[id]
	if !ok {
		return domain.User{}, domain.ErrNotFound
	}
	return u, nil
}

func (m *memUsers) ResetPassword(_ context.Context, id, hash string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	u, ok := m.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	u.PasswordHash = hash
	if u.EmailVerifiedAt == nil {
		u.EmailVerifiedAt = ptr(at)
	}
	m.byID[id] = u
	return nil
}

// update applies f to the user id under the lock when cond holds, like the
// compare-and-set updates of the MongoDB repository.
func (m *memUsers) update(id string, cond func(domain.User) bool, f func(*domain.User)) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return false, m.err
	}
	u, ok := m.byID[id]
	if !ok {
		return false, domain.ErrNotFound
	}
	if !cond(u) {
		return false, nil
	}
	f(&u)
	m.byID[id] = u
	return true, nil
}

func (m *memUsers) SetPendingMFA(_ context.Context, id, sealed string, expiresAt, _ time.Time) (bool, error) {
	return m.update(id, func(u domain.User) bool { return !u.MFAEnabled() }, func(u *domain.User) {
		u.MFA.PendingSecret, u.MFA.PendingExpiresAt = sealed, ptr(expiresAt)
	})
}

func (m *memUsers) EnableMFA(_ context.Context, id, pending string, hashes []string, step int64, at time.Time) (bool, error) {
	return m.update(id, func(u domain.User) bool {
		return !u.MFAEnabled() && u.MFA.PendingSecret == pending && u.MFA.PendingActive(at)
	}, func(u *domain.User) {
		u.MFA = domain.MFA{Secret: pending, EnabledAt: ptr(at), RecoveryCodes: append([]string(nil), hashes...), LastStep: step}
	})
}

func (m *memUsers) UseTOTPStep(_ context.Context, id string, step int64) (bool, error) {
	return m.update(id, func(u domain.User) bool { return u.MFAEnabled() && u.MFA.LastStep < step }, func(u *domain.User) {
		u.MFA.LastStep = step
	})
}

func (m *memUsers) UseRecoveryCode(_ context.Context, id, hash string) (bool, error) {
	return m.update(id, func(u domain.User) bool { return u.MFAEnabled() && slices.Contains(u.MFA.RecoveryCodes, hash) }, func(u *domain.User) {
		u.MFA.RecoveryCodes = slices.DeleteFunc(slices.Clone(u.MFA.RecoveryCodes), func(h string) bool { return h == hash })
	})
}

func (m *memUsers) ReplaceMFASecret(_ context.Context, id, oldSealed, newSealed string) (bool, error) {
	return m.update(id, func(u domain.User) bool { return u.MFAEnabled() && u.MFA.Secret == oldSealed }, func(u *domain.User) {
		u.MFA.Secret = newSealed
	})
}

func (m *memUsers) DisableMFA(_ context.Context, id string, _ time.Time) (bool, error) {
	return m.update(id, func(u domain.User) bool { return u.MFAEnabled() }, func(u *domain.User) { u.MFA = domain.MFA{} })
}

func (m *memUsers) MarkEmailVerified(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	if u.EmailVerifiedAt == nil {
		u.EmailVerifiedAt = ptr(at)
	}
	m.byID[id] = u
	return nil
}

func (m *memUsers) UpdatePasswordHash(_ context.Context, id, hash string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	u.PasswordHash = hash
	m.byID[id] = u
	m.hashes = append(m.hashes, hash)
	return nil
}

func (m *memUsers) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.byID, id)
	return nil
}

func (m *memUsers) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byID)
}

func (m *memUsers) only() domain.User {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.byID {
		return u
	}
	return domain.User{}
}

type memSessions struct {
	mu   sync.Mutex
	byID map[string]domain.Session
	seq  int
	err  error
}

func (m *memSessions) Create(_ context.Context, s domain.Session) (domain.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.Session{}, m.err
	}
	m.seq++
	s.ID = idFor("s", m.seq)
	if m.byID == nil {
		m.byID = map[string]domain.Session{}
	}
	m.byID[s.ID] = s
	return s, nil
}

func (m *memSessions) ByID(_ context.Context, id string) (domain.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.Session{}, m.err
	}
	s, ok := m.byID[id]
	if !ok {
		return domain.Session{}, domain.ErrNotFound
	}
	s.RevokedAt = cloneTime(s.RevokedAt)
	return s, nil
}

func (m *memSessions) Touch(_ context.Context, id string, lastUsed, exp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[id]
	if ok && s.RevokedAt == nil {
		s.LastUsedAt, s.ExpiresAt = lastUsed, exp
		m.byID[id] = s
	}
	return nil
}

func (m *memSessions) Revoke(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	s, ok := m.byID[id]
	if !ok {
		return domain.ErrNotFound
	}
	if s.RevokedAt == nil {
		s.RevokedAt = ptr(at)
	}
	m.byID[id] = s
	return nil
}

func (m *memSessions) RevokeAllForUser(_ context.Context, userID, except string, at time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	var ids []string
	for id, s := range m.byID {
		if s.UserID == userID && id != except && s.Active(at) {
			s.RevokedAt = ptr(at)
			m.byID[id] = s
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (m *memSessions) ActiveIDs(_ context.Context, userID, except string, now time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	var ids []string
	for id, s := range m.byID {
		if s.UserID == userID && id != except && s.Active(now) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (m *memSessions) ListActive(_ context.Context, userID string, now time.Time, limit int) ([]domain.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	var out []domain.Session
	for _, s := range m.byID {
		if s.UserID == userID && s.Active(now) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b domain.Session) int { return b.LastUsedAt.Compare(a.LastUsedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memSessions) RevokeForUser(_ context.Context, userID, id string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return false, m.err
	}
	s, ok := m.byID[id]
	if !ok || s.UserID != userID || !at.Before(s.ExpiresAt) {
		return false, nil
	}
	if s.RevokedAt == nil {
		s.RevokedAt = ptr(at)
	}
	m.byID[id] = s
	return true, nil
}

func (m *memSessions) active(userID string, at time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, s := range m.byID {
		if s.UserID == userID && s.Active(at) {
			n++
		}
	}
	return n
}

type memRefresh struct {
	mu     sync.Mutex
	byHash map[string]domain.RefreshToken
	err    error
}

func (m *memRefresh) Create(_ context.Context, t domain.RefreshToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if m.byHash == nil {
		m.byHash = map[string]domain.RefreshToken{}
	}
	m.byHash[t.TokenHash] = t
	return nil
}

func (m *memRefresh) Consume(_ context.Context, hash string, now time.Time) (domain.RefreshToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byHash[hash]
	if !ok || t.UsedAt != nil || !now.Before(t.ExpiresAt) {
		return domain.RefreshToken{}, domain.ErrNotFound
	}
	t.UsedAt = ptr(now)
	m.byHash[hash] = t
	return t, nil
}

func (m *memRefresh) ByHash(_ context.Context, hash string) (domain.RefreshToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byHash[hash]
	if !ok {
		return domain.RefreshToken{}, domain.ErrNotFound
	}
	return t, nil
}

type memAuthTokens struct {
	mu     sync.Mutex
	byHash map[string]domain.AuthToken
	err    error
}

func (m *memAuthTokens) Create(_ context.Context, t domain.AuthToken) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if m.byHash == nil {
		m.byHash = map[string]domain.AuthToken{}
	}
	m.byHash[t.TokenHash] = t
	return nil
}

func (m *memAuthTokens) Consume(_ context.Context, hash string, purpose domain.TokenPurpose, now time.Time) (domain.AuthToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.AuthToken{}, m.err
	}
	t, ok := m.byHash[hash]
	if !ok || t.Purpose != purpose || t.UsedAt != nil || !now.Before(t.ExpiresAt) {
		return domain.AuthToken{}, domain.ErrNotFound
	}
	t.UsedAt = ptr(now)
	m.byHash[hash] = t
	return t, nil
}

func (m *memAuthTokens) InvalidateFor(_ context.Context, userID string, purpose domain.TokenPurpose, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for h, t := range m.byHash {
		if t.UserID == userID && t.Purpose == purpose && t.UsedAt == nil {
			t.UsedAt = ptr(now)
			m.byHash[h] = t
		}
	}
	return nil
}

func (m *memAuthTokens) pending() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, t := range m.byHash {
		if t.UsedAt == nil {
			n++
		}
	}
	return n
}

type memRevocations struct {
	mu       sync.Mutex
	tokens   map[string]time.Duration
	sessions map[string]time.Duration
	err      error
}

func (m *memRevocations) DenyToken(_ context.Context, jti string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if m.tokens == nil {
		m.tokens = map[string]time.Duration{}
	}
	m.tokens[jti] = ttl
	return nil
}

func (m *memRevocations) RevokeSession(_ context.Context, sid string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if m.sessions == nil {
		m.sessions = map[string]time.Duration{}
	}
	m.sessions[sid] = ttl
	return nil
}

func (m *memRevocations) RevokeSessions(ctx context.Context, sids []string, ttl time.Duration) error {
	for _, sid := range sids {
		if err := m.RevokeSession(ctx, sid, ttl); err != nil {
			return err
		}
	}
	return nil
}

func (m *memRevocations) sessionRevoked(sid string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[sid]
	return ok
}

// memLockout follows the real policy without expiries.
type memLockout struct {
	policy        core.LockoutPolicy
	fails         map[string]int
	locks         map[string]time.Duration
	registrations int
	err           error
	resets        int
}

func (m *memLockout) Check(_ context.Context, addr, ip string) (core.LoginState, error) {
	if m.err != nil {
		return core.LoginState{}, m.err
	}
	return core.LoginState{
		RetryAfter:    max(m.locks["e:"+addr], m.locks["i:"+ip]),
		EmailFailures: m.fails["e:"+addr],
		IPFailures:    m.fails["i:"+ip],
	}, nil
}

func (m *memLockout) RecordFailure(_ context.Context, addr, ip string) (core.LoginState, error) {
	if m.fails == nil {
		m.fails, m.locks = map[string]int{}, map[string]time.Duration{}
	}
	m.fails["e:"+addr]++
	m.fails["i:"+ip]++
	if d := m.policy.LockFor(m.fails["e:"+addr], m.policy.EmailLockAfter); d > 0 {
		m.locks["e:"+addr] = d
	}
	return core.LoginState{EmailFailures: m.fails["e:"+addr], IPFailures: m.fails["i:"+ip]}, nil
}

func (m *memLockout) Reset(_ context.Context, addr string) error {
	m.resets++
	delete(m.fails, "e:"+addr)
	delete(m.locks, "e:"+addr)
	return nil
}

func (m *memLockout) CountRegistration(context.Context, string) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	m.registrations++
	return m.registrations, nil
}

type memChallenges struct {
	mu     sync.Mutex
	byHash map[string]core.MFAChallenge
	ttls   map[string]time.Duration
	err    error
}

func (m *memChallenges) Create(_ context.Context, hash string, c core.MFAChallenge, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	if m.byHash == nil {
		m.byHash, m.ttls = map[string]core.MFAChallenge{}, map[string]time.Duration{}
	}
	m.byHash[hash], m.ttls[hash] = c, ttl
	return nil
}

func (m *memChallenges) Attempt(_ context.Context, hash string) (core.MFAChallenge, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return core.MFAChallenge{}, false, m.err
	}
	c, ok := m.byHash[hash]
	if !ok {
		return core.MFAChallenge{}, false, nil
	}
	c.Attempts++
	m.byHash[hash] = c
	return c, true, nil
}

func (m *memChallenges) Consume(_ context.Context, hash string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return false, m.err
	}
	_, ok := m.byHash[hash]
	delete(m.byHash, hash)
	return ok, nil
}

func (m *memChallenges) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byHash)
}

type memLimiter struct {
	limit    int
	used     map[string]int
	err      error
	subjects []string
}

func (m *memLimiter) Take(_ context.Context, subject string) (ratelimit.Result, error) {
	if m.err != nil {
		return ratelimit.Result{}, m.err
	}
	if m.used == nil {
		m.used = map[string]int{}
	}
	m.subjects = append(m.subjects, subject)
	if m.limit > 0 && m.used[subject] >= m.limit {
		return ratelimit.Result{RetryAfter: 42 * time.Second}, nil
	}
	m.used[subject]++
	return ratelimit.Result{Allowed: true}, nil
}

type fakeCaptcha struct {
	enabled bool
	err     error
	tokens  []string
}

func (f *fakeCaptcha) Enabled() bool { return f.enabled }
func (f *fakeCaptcha) Verify(_ context.Context, token, _ string) (bool, error) {
	f.tokens = append(f.tokens, token)
	return token == "human", f.err
}

type fakeBreach struct {
	breached bool
	err      error
}

func (f fakeBreach) Breached(context.Context, string) (bool, error) { return f.breached, f.err }

// fakeHasher stores "v1:<password>"; "v0:" hashes verify but need a rehash.
type fakeHasher struct {
	dummies int
	hashes  int
	err     error
}

func (f *fakeHasher) Hash(_ context.Context, pw string) (string, error) {
	f.hashes++
	return "v1:" + pw, f.err
}

func (f *fakeHasher) Verify(_ context.Context, pw, encoded string) (bool, bool, error) {
	if f.err != nil {
		return false, false, f.err
	}
	switch {
	case strings.HasPrefix(encoded, "v1:"):
		return strings.TrimPrefix(encoded, "v1:") == pw, false, nil
	case strings.HasPrefix(encoded, "v0:"):
		ok := strings.TrimPrefix(encoded, "v0:") == pw
		return ok, ok, nil
	}
	return false, false, core.ErrMalformedHash
}

func (f *fakeHasher) VerifyDummy(context.Context, string) { f.dummies++ }

type fakeTokens struct {
	clock *clock
	seq   int
	err   error
}

func (f *fakeTokens) Issue(userID, sessionID string) (core.AccessToken, error) {
	f.seq++
	id := fmt.Sprintf("jti-%d", f.seq)
	return core.AccessToken{Token: "at." + userID + "." + sessionID + "." + id, ID: id, ExpiresAt: f.clock.now().Add(f.TTL())}, f.err
}

func (f *fakeTokens) TTL() time.Duration { return 15 * time.Minute }

type mailbox struct {
	mu   sync.Mutex
	sent []email.Message
}

func (m *mailbox) Send(_ context.Context, msg email.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
}

type auditTrail struct{ entries []audit.Entry }

func (a *auditTrail) Record(_ context.Context, e audit.Entry) { a.entries = append(a.entries, e) }

func (a *auditTrail) events() []audit.Event {
	out := make([]audit.Event, len(a.entries))
	for i, e := range a.entries {
		out[i] = e.Event
	}
	return out
}

type counters struct {
	logins      map[string]int
	rateLimited map[string]int
	mfa         map[string]int
	reuse       int
	resets      int
}

func (c *counters) MFA(event string) {
	if c.mfa == nil {
		c.mfa = map[string]int{}
	}
	c.mfa[event]++
}

func (c *counters) Login(result string) {
	if c.logins == nil {
		c.logins = map[string]int{}
	}
	c.logins[result]++
}
func (c *counters) RefreshReuse()  { c.reuse++ }
func (c *counters) PasswordReset() { c.resets++ }
func (c *counters) RateLimitRejected(policy string) {
	if c.rateLimited == nil {
		c.rateLimited = map[string]int{}
	}
	c.rateLimited[policy]++
}

// testKeys are the AES keys of the test sealers, by kid.
var testKeys = map[string][]byte{"e1": bytes.Repeat([]byte{1}, aead.KeyBytes), "e2": bytes.Repeat([]byte{2}, aead.KeyBytes)}

func mustSealer(t interface{ Fatal(...any) }, keys map[string][]byte, active string) *aead.Sealer {
	s, err := aead.New(keys, active)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// newSealer is a real AES-GCM sealer on testKeys that seals under active.
func newSealer(active string) *aead.Sealer {
	s, err := aead.New(testKeys, active)
	if err != nil {
		panic(err)
	}
	return s
}

// eraseLog records the order in which erasers ran, and can fail one.
type eraseLog struct {
	mu   sync.Mutex
	ran  []string
	fail string
}

type namedEraser struct {
	name string
	log  *eraseLog
}

func (e namedEraser) EraseUserData(_ context.Context, userID string) error {
	e.log.mu.Lock()
	defer e.log.mu.Unlock()
	if e.log.fail == e.name {
		return errors.New(e.name + " down")
	}
	e.log.ran = append(e.log.ran, e.name+":"+userID)
	return nil
}

// harness wires a Service to in-memory fakes.
type harness struct {
	*Service
	clock       *clock
	users       *memUsers
	sessions    *memSessions
	refresh     *memRefresh
	authTokens  *memAuthTokens
	revocations *memRevocations
	lockout     *memLockout
	challenges  *memChallenges
	limiter     *memLimiter
	mailLimiter *memLimiter
	captcha     *fakeCaptcha
	breach      *fakeBreach
	hasher      *fakeHasher
	tokens      *fakeTokens
	mail        *mailbox
	audit       *auditTrail
	metrics     *counters
	erased      *eraseLog
}

func newHarness() *harness {
	c := newClock()
	h := &harness{
		clock: c, users: &memUsers{}, sessions: &memSessions{}, refresh: &memRefresh{}, authTokens: &memAuthTokens{},
		revocations: &memRevocations{}, lockout: &memLockout{policy: core.DefaultLockoutPolicy}, challenges: &memChallenges{}, limiter: &memLimiter{}, mailLimiter: &memLimiter{},
		captcha: &fakeCaptcha{}, breach: &fakeBreach{}, hasher: &fakeHasher{}, tokens: &fakeTokens{clock: c},
		mail: &mailbox{}, audit: &auditTrail{}, metrics: &counters{}, erased: &eraseLog{},
	}
	h.Service = NewService(Deps{
		Users: h.users, Sessions: h.sessions, RefreshTokens: h.refresh, AuthTokens: h.authTokens,
		Revocations: h.revocations, Lockout: h.lockout, MFAChallenges: h.challenges, Sealer: newSealer("e1"), EmailLimit: h.limiter, MailLimit: h.mailLimiter, Captcha: h.captcha,
		Breach: h.breach, Hasher: h.hasher, Tokens: h.tokens, Mailer: h.mail, Auditor: h.audit, Metrics: h.metrics,
		Erasers: []UserDataEraser{namedEraser{"transactions", h.erased}, namedEraser{"sessions", h.erased}},
	}, Options{
		RefreshTTL: 30 * 24 * time.Hour, EmailTokenTTL: 30 * time.Minute, ResetTokenTTL: 15 * time.Minute,
		AppURL: config.PublicURL("https://app.monthra.example"), MFAIssuer: "Monthra (test)", Lockout: core.DefaultLockoutPolicy,
		Logger: slog.New(slog.DiscardHandler), Now: c.now,
	})
	return h
}

package dto

import (
	"time"

	"github.com/NickeAlves/monthra-server/internal/audit"
	"github.com/NickeAlves/monthra-server/internal/domain"
)

// ExportDataRequest is the body of POST /me/export. Code is required when
// MFA is on.
type ExportDataRequest struct {
	Password string `json:"password" validate:"required,max=4096"`
	Code     string `json:"code" validate:"max=64"`
}

// SessionRecord is a stored session in the data export: an active, expired
// or revoked one, until the store removes it.
type SessionRecord struct {
	ID         string     `json:"id"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt time.Time  `json:"lastUsedAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	UserAgent  string     `json:"userAgent"`
	IP         string     `json:"ip"`
}

// NewSessionRecord maps a session to its record.
func NewSessionRecord(s domain.Session) SessionRecord {
	out := SessionRecord{
		ID: s.ID, CreatedAt: s.CreatedAt.UTC(), LastUsedAt: s.LastUsedAt.UTC(), ExpiresAt: s.ExpiresAt.UTC(),
		UserAgent: s.UserAgent, IP: s.IP,
	}
	if s.RevokedAt != nil {
		at := s.RevokedAt.UTC()
		out.RevokedAt = &at
	}
	return out
}

// AuditEntry is one security event of the user in the data export. Empty
// fields are null.
type AuditEntry struct {
	Event     string    `json:"event"`
	SessionID *string   `json:"sessionId"`
	IP        *string   `json:"ip"`
	UserAgent *string   `json:"userAgent"`
	Reason    *string   `json:"reason"`
	CreatedAt time.Time `json:"createdAt"`
}

// NewAuditEntry maps an audit log entry; the user id stays out.
func NewAuditEntry(e audit.Entry) AuditEntry {
	return AuditEntry{
		Event: string(e.Event), SessionID: orNull(e.SessionID), IP: orNull(e.IP), UserAgent: orNull(e.UserAgent),
		Reason: orNull(e.Reason), CreatedAt: e.CreatedAt.UTC(),
	}
}

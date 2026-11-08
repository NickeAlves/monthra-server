package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/NickeAlves/monthra-server/internal/audit"
)

// AuditLogs stores audit entries; a TTL index on created_at expires them (AUDIT_LOG_TTL).
type AuditLogs struct{ coll *mongo.Collection }

// NewAuditLogs returns the audit log store on db.
func NewAuditLogs(db *mongo.Database) *AuditLogs {
	return &AuditLogs{coll: db.Collection(CollAuditLogs)}
}

type auditDoc struct {
	Event     string         `bson:"event"`
	UserID    *bson.ObjectID `bson:"user_id,omitempty"`
	SessionID *bson.ObjectID `bson:"session_id,omitempty"`
	IP        string         `bson:"ip,omitempty"`
	UserAgent string         `bson:"user_agent,omitempty"`
	Reason    string         `bson:"reason,omitempty"`
	CreatedAt time.Time      `bson:"created_at"`
}

// Insert stores e.
func (a *AuditLogs) Insert(ctx context.Context, e audit.Entry) error {
	doc := auditDoc{Event: string(e.Event), IP: e.IP, UserAgent: e.UserAgent, Reason: e.Reason, CreatedAt: e.CreatedAt}
	if oid, err := bson.ObjectIDFromHex(e.UserID); err == nil {
		doc.UserID = &oid
	}
	if oid, err := bson.ObjectIDFromHex(e.SessionID); err == nil {
		doc.SessionID = &oid
	}
	if _, err := a.coll.InsertOne(ctx, doc); err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

package mongo

import (
	"context"
	"fmt"
	"iter"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

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

func (d auditDoc) toEntry() audit.Entry {
	e := audit.Entry{Event: audit.Event(d.Event), IP: d.IP, UserAgent: d.UserAgent, Reason: d.Reason, CreatedAt: d.CreatedAt}
	if d.UserID != nil {
		e.UserID = d.UserID.Hex()
	}
	if d.SessionID != nil {
		e.SessionID = d.SessionID.Hex()
	}
	return e
}

// ForUser yields the entries of userID still kept, the newest first (index
// audit_logs_user_created), for the data export. An id that cannot exist
// owns none.
func (a *AuditLogs) ForUser(ctx context.Context, userID string) iter.Seq2[audit.Entry, error] {
	uid, err := objectID(userID)
	if err != nil {
		return none[audit.Entry]()
	}
	opts := options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}})
	return stream(ctx, a.coll, bson.D{{Key: "user_id", Value: uid}}, opts,
		func(d auditDoc) (audit.Entry, error) { return d.toEntry(), nil })
}

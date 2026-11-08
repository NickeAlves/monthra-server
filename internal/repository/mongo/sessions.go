package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Sessions stores signed-in devices in the sessions collection. A TTL index
// on expires_at removes them once they can no longer be refreshed.
type Sessions struct{ coll *mongo.Collection }

// NewSessions returns the sessions repository on db.
func NewSessions(db *mongo.Database) *Sessions { return &Sessions{coll: db.Collection(CollSessions)} }

type sessionDoc struct {
	ID         bson.ObjectID `bson:"_id"`
	UserID     bson.ObjectID `bson:"user_id"`
	UserAgent  string        `bson:"user_agent"`
	IP         string        `bson:"ip"`
	CreatedAt  time.Time     `bson:"created_at"`
	LastUsedAt time.Time     `bson:"last_used_at"`
	ExpiresAt  time.Time     `bson:"expires_at"`
	RevokedAt  *time.Time    `bson:"revoked_at"`
}

func (d sessionDoc) toDomain() domain.Session {
	return domain.Session{
		ID: d.ID.Hex(), UserID: d.UserID.Hex(), UserAgent: d.UserAgent, IP: d.IP,
		CreatedAt: d.CreatedAt, LastUsedAt: d.LastUsedAt, ExpiresAt: d.ExpiresAt, RevokedAt: d.RevokedAt,
	}
}

// Create inserts s with a new id.
func (r *Sessions) Create(ctx context.Context, s domain.Session) (domain.Session, error) {
	uid, err := objectID(s.UserID)
	if err != nil {
		return domain.Session{}, err
	}
	doc := sessionDoc{
		ID: bson.NewObjectID(), UserID: uid, UserAgent: s.UserAgent, IP: s.IP,
		CreatedAt: s.CreatedAt.UTC(), LastUsedAt: s.LastUsedAt.UTC(), ExpiresAt: s.ExpiresAt.UTC(), RevokedAt: s.RevokedAt,
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		return domain.Session{}, fmt.Errorf("insert session: %w", err)
	}
	return doc.toDomain(), nil
}

// ByID finds a session.
func (r *Sessions) ByID(ctx context.Context, id string) (domain.Session, error) {
	oid, err := objectID(id)
	if err != nil {
		return domain.Session{}, err
	}
	var doc sessionDoc
	if err := r.coll.FindOne(ctx, bson.D{{Key: "_id", Value: oid}}).Decode(&doc); err != nil {
		return domain.Session{}, notFound(err)
	}
	return doc.toDomain(), nil
}

// Touch records a refresh: the session was used at lastUsed and now lives until expiresAt.
func (r *Sessions) Touch(ctx context.Context, id string, lastUsed, expiresAt time.Time) error {
	oid, err := objectID(id)
	if err != nil {
		return err
	}
	_, err = r.coll.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: oid}, {Key: "revoked_at", Value: nil}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "last_used_at", Value: lastUsed.UTC()}, {Key: "expires_at", Value: expiresAt.UTC()}}}},
	)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// Revoke ends a session. Revoking twice keeps the first date.
func (r *Sessions) Revoke(ctx context.Context, id string, at time.Time) error {
	oid, err := objectID(id)
	if err != nil {
		return err
	}
	_, err = r.coll.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: oid}, {Key: "revoked_at", Value: nil}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "revoked_at", Value: at.UTC()}}}},
	)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

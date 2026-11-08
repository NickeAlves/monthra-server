package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

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

// ListActive returns up to limit sessions of userID that can still be
// refreshed at now, the most recently used first.
func (r *Sessions) ListActive(ctx context.Context, userID string, now time.Time, limit int) ([]domain.Session, error) {
	uid, err := objectID(userID)
	if err != nil {
		return nil, err
	}
	cur, err := r.coll.Find(ctx, activeFor(uid, now),
		options.Find().
			SetSort(bson.D{{Key: "last_used_at", Value: -1}, {Key: "_id", Value: -1}}).
			SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, fmt.Errorf("find sessions: %w", err)
	}
	var docs []sessionDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	out := make([]domain.Session, len(docs))
	for i, d := range docs {
		out[i] = d.toDomain()
	}
	return out, nil
}

// RevokeForUser ends the session id only if it belongs to userID and is still
// active at at. It reports false otherwise, so one user cannot learn about or
// end another user's sessions (IDOR).
func (r *Sessions) RevokeForUser(ctx context.Context, userID, id string, at time.Time) (bool, error) {
	uid, err := objectID(userID)
	if err != nil {
		return false, nil //nolint:nilerr // an id that cannot exist matches nothing
	}
	sid, err := objectID(id)
	if err != nil {
		return false, nil //nolint:nilerr // an id that cannot exist matches nothing
	}
	filter := append(bson.D{{Key: "_id", Value: sid}}, activeFor(uid, at)...)
	res, err := r.coll.UpdateOne(ctx, filter, bson.D{{Key: "$set", Value: bson.D{{Key: "revoked_at", Value: at.UTC()}}}})
	if err != nil {
		return false, fmt.Errorf("revoke session: %w", err)
	}
	return res.MatchedCount == 1, nil
}

// activeFor matches the sessions of uid that are neither revoked nor expired at now.
func activeFor(uid bson.ObjectID, now time.Time) bson.D {
	return bson.D{
		{Key: "user_id", Value: uid},
		{Key: "revoked_at", Value: nil},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now.UTC()}}},
	}
}

// RevokeAllForUser ends every active session of userID except the one with
// id except (empty for none), and returns the ids it revoked so their access
// tokens can be denied too.
func (r *Sessions) RevokeAllForUser(ctx context.Context, userID, except string, at time.Time) ([]string, error) {
	uid, err := objectID(userID)
	if err != nil {
		return nil, err
	}
	at = at.UTC()
	filter := activeFor(uid, at)
	if except != "" {
		eid, err := objectID(except)
		if err != nil {
			return nil, err
		}
		filter = append(filter, bson.E{Key: "_id", Value: bson.D{{Key: "$ne", Value: eid}}})
	}
	cur, err := r.coll.Find(ctx, filter, options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("find sessions: %w", err)
	}
	var docs []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("read sessions: %w", err)
	}
	if len(docs) == 0 {
		return nil, nil
	}
	oids := make(bson.A, len(docs))
	ids := make([]string, len(docs))
	for i, d := range docs {
		oids[i], ids[i] = d.ID, d.ID.Hex()
	}
	_, err = r.coll.UpdateMany(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: oids}}}, {Key: "revoked_at", Value: nil}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "revoked_at", Value: at}}}},
	)
	if err != nil {
		return nil, fmt.Errorf("revoke sessions: %w", err)
	}
	return ids, nil
}

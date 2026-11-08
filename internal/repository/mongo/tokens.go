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

// RefreshTokens stores the rotation chain of each session, by token hash.
type RefreshTokens struct{ coll *mongo.Collection }

// NewRefreshTokens returns the refresh token repository on db.
func NewRefreshTokens(db *mongo.Database) *RefreshTokens {
	return &RefreshTokens{coll: db.Collection(CollRefreshTokens)}
}

type refreshTokenDoc struct {
	TokenHash string        `bson:"token_hash"`
	SessionID bson.ObjectID `bson:"session_id"`
	UserID    bson.ObjectID `bson:"user_id"`
	CreatedAt time.Time     `bson:"created_at"`
	ExpiresAt time.Time     `bson:"expires_at"`
	UsedAt    *time.Time    `bson:"used_at"`
}

func (d refreshTokenDoc) toDomain() domain.RefreshToken {
	return domain.RefreshToken{
		TokenHash: d.TokenHash, SessionID: d.SessionID.Hex(), UserID: d.UserID.Hex(),
		CreatedAt: d.CreatedAt, ExpiresAt: d.ExpiresAt, UsedAt: d.UsedAt,
	}
}

// Create stores t.
func (r *RefreshTokens) Create(ctx context.Context, t domain.RefreshToken) error {
	sid, err := objectID(t.SessionID)
	if err != nil {
		return err
	}
	uid, err := objectID(t.UserID)
	if err != nil {
		return err
	}
	doc := refreshTokenDoc{TokenHash: t.TokenHash, SessionID: sid, UserID: uid, CreatedAt: t.CreatedAt.UTC(), ExpiresAt: t.ExpiresAt.UTC(), UsedAt: t.UsedAt}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		return fmt.Errorf("insert refresh token: %w", err)
	}
	return nil
}

// Consume marks an unused, unexpired token as used and returns it. The
// filter on used_at makes it atomic: of two concurrent refreshes with the same
// token, exactly one wins. Any other case returns domain.ErrNotFound.
func (r *RefreshTokens) Consume(ctx context.Context, hash string, now time.Time) (domain.RefreshToken, error) {
	return consume[refreshTokenDoc](ctx, r.coll, bson.D{
		{Key: "token_hash", Value: hash},
		{Key: "used_at", Value: nil},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now.UTC()}}},
	}, now, refreshTokenDoc.toDomain)
}

// ByHash finds a token whatever its state, to tell a reuse from an unknown token.
func (r *RefreshTokens) ByHash(ctx context.Context, hash string) (domain.RefreshToken, error) {
	var doc refreshTokenDoc
	if err := r.coll.FindOne(ctx, bson.D{{Key: "token_hash", Value: hash}}).Decode(&doc); err != nil {
		return domain.RefreshToken{}, notFound(err)
	}
	return doc.toDomain(), nil
}

// AuthTokens stores the single-use tokens sent by email.
type AuthTokens struct{ coll *mongo.Collection }

// NewAuthTokens returns the email token repository on db.
func NewAuthTokens(db *mongo.Database) *AuthTokens {
	return &AuthTokens{coll: db.Collection(CollAuthTokens)}
}

type authTokenDoc struct {
	TokenHash string        `bson:"token_hash"`
	UserID    bson.ObjectID `bson:"user_id"`
	Purpose   string        `bson:"purpose"`
	CreatedAt time.Time     `bson:"created_at"`
	ExpiresAt time.Time     `bson:"expires_at"`
	UsedAt    *time.Time    `bson:"used_at"`
}

func (d authTokenDoc) toDomain() domain.AuthToken {
	return domain.AuthToken{
		TokenHash: d.TokenHash, UserID: d.UserID.Hex(), Purpose: domain.TokenPurpose(d.Purpose),
		CreatedAt: d.CreatedAt, ExpiresAt: d.ExpiresAt, UsedAt: d.UsedAt,
	}
}

// Create stores t.
func (r *AuthTokens) Create(ctx context.Context, t domain.AuthToken) error {
	uid, err := objectID(t.UserID)
	if err != nil {
		return err
	}
	doc := authTokenDoc{TokenHash: t.TokenHash, UserID: uid, Purpose: string(t.Purpose), CreatedAt: t.CreatedAt.UTC(), ExpiresAt: t.ExpiresAt.UTC(), UsedAt: t.UsedAt}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		return fmt.Errorf("insert auth token: %w", err)
	}
	return nil
}

// Consume marks an unused, unexpired token of purpose as used and returns it,
// atomically. Any other case returns domain.ErrNotFound.
func (r *AuthTokens) Consume(ctx context.Context, hash string, purpose domain.TokenPurpose, now time.Time) (domain.AuthToken, error) {
	return consume[authTokenDoc](ctx, r.coll, bson.D{
		{Key: "token_hash", Value: hash},
		{Key: "purpose", Value: string(purpose)},
		{Key: "used_at", Value: nil},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: now.UTC()}}},
	}, now, authTokenDoc.toDomain)
}

// InvalidateFor marks every pending token of purpose for userID as used, so
// only the newest link sent works.
func (r *AuthTokens) InvalidateFor(ctx context.Context, userID string, purpose domain.TokenPurpose, now time.Time) error {
	uid, err := objectID(userID)
	if err != nil {
		return err
	}
	_, err = r.coll.UpdateMany(ctx,
		bson.D{{Key: "user_id", Value: uid}, {Key: "purpose", Value: string(purpose)}, {Key: "used_at", Value: nil}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "used_at", Value: now.UTC()}}}},
	)
	if err != nil {
		return fmt.Errorf("invalidate auth tokens: %w", err)
	}
	return nil
}

func consume[D any, T any](ctx context.Context, coll *mongo.Collection, filter bson.D, now time.Time, conv func(D) T) (T, error) {
	var doc D
	var zero T
	err := coll.FindOneAndUpdate(ctx, filter,
		bson.D{{Key: "$set", Value: bson.D{{Key: "used_at", Value: now.UTC()}}}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&doc)
	if err != nil {
		return zero, notFound(err)
	}
	return conv(doc), nil
}

package mongo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// Users stores accounts in the users collection.
type Users struct{ coll *mongo.Collection }

// NewUsers returns the users repository on db.
func NewUsers(db *mongo.Database) *Users { return &Users{coll: db.Collection(CollUsers)} }

type preferencesDoc struct {
	Locale   string `bson:"locale"`
	Theme    string `bson:"theme"`
	Currency string `bson:"currency"`
}

type userDoc struct {
	ID              bson.ObjectID  `bson:"_id"`
	Name            string         `bson:"name"`
	Email           string         `bson:"email"`
	PasswordHash    string         `bson:"password_hash"`
	EmailVerifiedAt *time.Time     `bson:"email_verified_at"`
	Preferences     preferencesDoc `bson:"preferences"`
	CreatedAt       time.Time      `bson:"created_at"`
	UpdatedAt       time.Time      `bson:"updated_at"`
}

func (d userDoc) toDomain() domain.User {
	return domain.User{
		ID:              d.ID.Hex(),
		Name:            d.Name,
		Email:           d.Email,
		PasswordHash:    d.PasswordHash,
		EmailVerifiedAt: d.EmailVerifiedAt,
		Preferences: domain.Preferences{
			Locale:   i18n.Locale(d.Preferences.Locale),
			Theme:    domain.Theme(d.Preferences.Theme),
			Currency: d.Preferences.Currency,
		},
		CreatedAt: d.CreatedAt,
		UpdatedAt: d.UpdatedAt,
	}
}

// Create inserts u with a new id. A taken email returns domain.ErrEmailTaken.
func (r *Users) Create(ctx context.Context, u domain.User) (domain.User, error) {
	doc := userDoc{
		ID:              bson.NewObjectID(),
		Name:            u.Name,
		Email:           u.Email,
		PasswordHash:    u.PasswordHash,
		EmailVerifiedAt: u.EmailVerifiedAt,
		Preferences: preferencesDoc{
			Locale:   string(u.Preferences.Locale),
			Theme:    string(u.Preferences.Theme),
			Currency: u.Preferences.Currency,
		},
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return domain.User{}, domain.ErrEmailTaken
		}
		return domain.User{}, fmt.Errorf("insert user: %w", err)
	}
	return doc.toDomain(), nil
}

// ByEmail finds the account of a normalized email.
func (r *Users) ByEmail(ctx context.Context, email string) (domain.User, error) {
	var doc userDoc
	if err := r.coll.FindOne(ctx, bson.D{{Key: "email", Value: email}}).Decode(&doc); err != nil {
		return domain.User{}, notFound(err)
	}
	return doc.toDomain(), nil
}

// ByID finds an account.
func (r *Users) ByID(ctx context.Context, id string) (domain.User, error) {
	oid, err := objectID(id)
	if err != nil {
		return domain.User{}, err
	}
	var doc userDoc
	if err := r.coll.FindOne(ctx, bson.D{{Key: "_id", Value: oid}}).Decode(&doc); err != nil {
		return domain.User{}, notFound(err)
	}
	return doc.toDomain(), nil
}

// MarkEmailVerified records that the owner confirmed the address. Verifying
// twice keeps the first date.
func (r *Users) MarkEmailVerified(ctx context.Context, id string, at time.Time) error {
	oid, err := objectID(id)
	if err != nil {
		return err
	}
	res, err := r.coll.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: oid}, {Key: "email_verified_at", Value: nil}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "email_verified_at", Value: at.UTC()}, {Key: "updated_at", Value: at.UTC()}}}},
	)
	if err != nil {
		return fmt.Errorf("mark email verified: %w", err)
	}
	if res.MatchedCount == 0 {
		// Either verified already (fine) or gone.
		if _, err := r.ByID(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// UpdatePasswordHash replaces the stored hash.
func (r *Users) UpdatePasswordHash(ctx context.Context, id, hash string, at time.Time) error {
	oid, err := objectID(id)
	if err != nil {
		return err
	}
	res, err := r.coll.UpdateOne(ctx,
		bson.D{{Key: "_id", Value: oid}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "password_hash", Value: hash}, {Key: "updated_at", Value: at.UTC()}}}},
	)
	if err != nil {
		return fmt.Errorf("update password hash: %w", err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

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

// mfaDoc is the mfa subdocument. It is absent until a setup starts and
// removed when MFA is turned off.
type mfaDoc struct {
	Secret           string     `bson:"secret,omitempty"`
	EnabledAt        *time.Time `bson:"enabled_at,omitempty"`
	PendingSecret    string     `bson:"pending_secret,omitempty"`
	PendingExpiresAt *time.Time `bson:"pending_expires_at,omitempty"`
	RecoveryCodes    []string   `bson:"recovery_codes,omitempty"`
	LastStep         int64      `bson:"last_step,omitempty"`
}

type userDoc struct {
	ID              bson.ObjectID  `bson:"_id"`
	Name            string         `bson:"name"`
	Email           string         `bson:"email"`
	PasswordHash    string         `bson:"password_hash"`
	EmailVerifiedAt *time.Time     `bson:"email_verified_at"`
	Preferences     preferencesDoc `bson:"preferences"`
	MFA             *mfaDoc        `bson:"mfa,omitempty"`
	CreatedAt       time.Time      `bson:"created_at"`
	UpdatedAt       time.Time      `bson:"updated_at"`
}

func (d userDoc) toDomain() domain.User {
	var mfa domain.MFA
	if d.MFA != nil {
		mfa = domain.MFA{
			Secret: d.MFA.Secret, EnabledAt: d.MFA.EnabledAt,
			PendingSecret: d.MFA.PendingSecret, PendingExpiresAt: d.MFA.PendingExpiresAt,
			RecoveryCodes: d.MFA.RecoveryCodes, LastStep: d.MFA.LastStep,
		}
	}
	return domain.User{
		MFA:             mfa,
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

// ResetPassword replaces the password hash and, since following the emailed
// link proved control of the inbox, marks the address verified if it was not.
// One pipeline update keeps both changes atomic.
func (r *Users) ResetPassword(ctx context.Context, id, hash string, at time.Time) error {
	oid, err := objectID(id)
	if err != nil {
		return err
	}
	at = at.UTC()
	// In a pipeline a string starting with "$" is a field path, and PHC
	// hashes start with "$argon2id$": $literal keeps the hash a value.
	res, err := r.coll.UpdateOne(ctx, bson.D{{Key: "_id", Value: oid}}, mongo.Pipeline{
		{{Key: "$set", Value: bson.D{
			{Key: "password_hash", Value: bson.D{{Key: "$literal", Value: hash}}},
			{Key: "updated_at", Value: at},
			{Key: "email_verified_at", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$email_verified_at", at}}}},
		}}},
	})
	if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// mfaEnabled matches accounts with MFA on.
var mfaEnabled = bson.E{Key: "mfa.enabled_at", Value: bson.D{{Key: "$ne", Value: nil}}}

// mfaDisabled matches accounts with MFA off (the field is missing or null).
var mfaDisabled = bson.E{Key: "mfa.enabled_at", Value: nil}

// SetPendingMFA stores a sealed secret waiting for its first code, replacing
// any earlier setup. It reports false when MFA is already enabled.
func (r *Users) SetPendingMFA(ctx context.Context, id, sealed string, expiresAt, at time.Time) (bool, error) {
	return r.updateMFA(ctx, id, bson.D{mfaDisabled}, bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "mfa.pending_secret", Value: sealed},
			{Key: "mfa.pending_expires_at", Value: expiresAt.UTC()},
			{Key: "updated_at", Value: at.UTC()},
		}},
	})
}

// EnableMFA promotes the pending secret, records the step of the code that
// confirmed it and stores the recovery code hashes. The filter is a
// compare-and-set on the pending secret, so a concurrent new setup or a
// second enable makes it report false.
func (r *Users) EnableMFA(ctx context.Context, id, pendingSealed string, recoveryHashes []string, step int64, at time.Time) (bool, error) {
	at = at.UTC()
	return r.updateMFA(ctx, id, bson.D{
		mfaDisabled,
		{Key: "mfa.pending_secret", Value: pendingSealed},
		{Key: "mfa.pending_expires_at", Value: bson.D{{Key: "$gt", Value: at}}},
	}, bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "mfa.secret", Value: pendingSealed},
			{Key: "mfa.enabled_at", Value: at},
			{Key: "mfa.recovery_codes", Value: recoveryHashes},
			{Key: "mfa.last_step", Value: step},
			{Key: "updated_at", Value: at},
		}},
		{Key: "$unset", Value: bson.D{{Key: "mfa.pending_secret", Value: ""}, {Key: "mfa.pending_expires_at", Value: ""}}},
	})
}

// UseTOTPStep records step as the last accepted TOTP step. It reports false
// when that step, or a later one, was already used (a replayed code).
func (r *Users) UseTOTPStep(ctx context.Context, id string, step int64) (bool, error) {
	return r.updateMFA(ctx, id, bson.D{
		mfaEnabled,
		{Key: "mfa.last_step", Value: bson.D{{Key: "$lt", Value: step}}},
	}, bson.D{{Key: "$set", Value: bson.D{{Key: "mfa.last_step", Value: step}}}})
}

// UseRecoveryCode removes one recovery code hash. It reports false when the
// code is not (or no longer) there, so each code works once.
func (r *Users) UseRecoveryCode(ctx context.Context, id, hash string) (bool, error) {
	return r.updateMFA(ctx, id, bson.D{
		mfaEnabled,
		{Key: "mfa.recovery_codes", Value: hash},
	}, bson.D{{Key: "$pull", Value: bson.D{{Key: "mfa.recovery_codes", Value: hash}}}})
}

// ReplaceMFASecret swaps a sealed secret for the same secret sealed again
// (key rotation). It reports false when the stored value changed meanwhile.
func (r *Users) ReplaceMFASecret(ctx context.Context, id, oldSealed, newSealed string) (bool, error) {
	return r.updateMFA(ctx, id, bson.D{
		mfaEnabled,
		{Key: "mfa.secret", Value: oldSealed},
	}, bson.D{{Key: "$set", Value: bson.D{{Key: "mfa.secret", Value: newSealed}}}})
}

// DisableMFA removes the secret, the recovery codes and any pending setup.
// It reports false when MFA was not enabled.
func (r *Users) DisableMFA(ctx context.Context, id string, at time.Time) (bool, error) {
	return r.updateMFA(ctx, id, bson.D{mfaEnabled}, bson.D{
		{Key: "$unset", Value: bson.D{{Key: "mfa", Value: ""}}},
		{Key: "$set", Value: bson.D{{Key: "updated_at", Value: at.UTC()}}},
	})
}

// updateMFA applies update to the user id when it also matches cond, and
// reports whether it did.
func (r *Users) updateMFA(ctx context.Context, id string, cond, update bson.D) (bool, error) {
	oid, err := objectID(id)
	if err != nil {
		return false, err
	}
	filter := append(bson.D{{Key: "_id", Value: oid}}, cond...)
	res, err := r.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, fmt.Errorf("update mfa: %w", err)
	}
	return res.MatchedCount == 1, nil
}

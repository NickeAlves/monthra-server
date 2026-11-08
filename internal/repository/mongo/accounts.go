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

// maxAccountsListed bounds one listing, well above the per-user cap of the
// use case, so a bug can never load an unbounded result.
const maxAccountsListed = 1000

// Accounts stores the money accounts of each user. Every query filters by
// user_id, so one user can never reach another's account (IDOR).
type Accounts struct{ coll *mongo.Collection }

// NewAccounts returns the accounts repository on db.
func NewAccounts(db *mongo.Database) *Accounts { return &Accounts{coll: db.Collection(CollAccounts)} }

type cardDoc struct {
	ClosingDay int    `bson:"closing_day"`
	DueDay     int    `bson:"due_day"`
	Limit      *int64 `bson:"limit"`
}

type accountDoc struct {
	ID             bson.ObjectID `bson:"_id"`
	UserID         bson.ObjectID `bson:"user_id"`
	Name           string        `bson:"name"`
	Type           string        `bson:"type"`
	Currency       string        `bson:"currency"`
	InitialBalance int64         `bson:"initial_balance"`
	Card           *cardDoc      `bson:"card"`
	Source         string        `bson:"source"`
	ArchivedAt     *time.Time    `bson:"archived_at"`
	CreatedAt      time.Time     `bson:"created_at"`
	UpdatedAt      time.Time     `bson:"updated_at"`
}

func newCardDoc(c *domain.CardSettings) *cardDoc {
	if c == nil {
		return nil
	}
	return &cardDoc{ClosingDay: c.ClosingDay, DueDay: c.DueDay, Limit: c.Limit}
}

func (d accountDoc) toDomain() domain.Account {
	a := domain.Account{
		ID: d.ID.Hex(), UserID: d.UserID.Hex(), Name: d.Name,
		Type: domain.AccountType(d.Type), Currency: d.Currency, InitialBalance: d.InitialBalance,
		Source: domain.Source(d.Source), ArchivedAt: d.ArchivedAt, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if d.Card != nil {
		a.Card = &domain.CardSettings{ClosingDay: d.Card.ClosingDay, DueDay: d.Card.DueDay, Limit: d.Card.Limit}
	}
	return a
}

// ownedBy is the filter of one record of one user.
func ownedBy(userID, id string) (bson.D, error) {
	uid, err := objectID(userID)
	if err != nil {
		return nil, err
	}
	oid, err := objectID(id)
	if err != nil {
		return nil, err
	}
	return bson.D{{Key: "_id", Value: oid}, {Key: "user_id", Value: uid}}, nil
}

// List returns the accounts of userID, oldest first; archived ones only
// when includeArchived.
func (r *Accounts) List(ctx context.Context, userID string, includeArchived bool) ([]domain.Account, error) {
	uid, err := objectID(userID)
	if err != nil {
		return nil, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	filter := bson.D{{Key: "user_id", Value: uid}}
	if !includeArchived {
		filter = append(filter, bson.E{Key: "archived_at", Value: nil})
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}).
		SetLimit(maxAccountsListed)
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("find accounts: %w", err)
	}
	var docs []accountDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode accounts: %w", err)
	}
	out := make([]domain.Account, len(docs))
	for i, d := range docs {
		out[i] = d.toDomain()
	}
	return out, nil
}

// Count returns how many accounts userID has, archived ones included.
func (r *Accounts) Count(ctx context.Context, userID string) (int, error) {
	uid, err := objectID(userID)
	if err != nil {
		return 0, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	n, err := r.coll.CountDocuments(ctx, bson.D{{Key: "user_id", Value: uid}})
	if err != nil {
		return 0, fmt.Errorf("count accounts: %w", err)
	}
	return int(n), nil
}

// Insert stores a with a new id and returns it.
func (r *Accounts) Insert(ctx context.Context, a domain.Account) (domain.Account, error) {
	uid, err := objectID(a.UserID)
	if err != nil {
		return domain.Account{}, err
	}
	doc := accountDoc{
		ID: bson.NewObjectID(), UserID: uid, Name: a.Name, Type: string(a.Type), Currency: a.Currency,
		InitialBalance: a.InitialBalance, Card: newCardDoc(a.Card), Source: string(a.Source),
		ArchivedAt: a.ArchivedAt, CreatedAt: a.CreatedAt.UTC(), UpdatedAt: a.UpdatedAt.UTC(),
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		return domain.Account{}, fmt.Errorf("insert account: %w", err)
	}
	return doc.toDomain(), nil
}

// ByID returns account id of userID, or domain.ErrNotFound.
func (r *Accounts) ByID(ctx context.Context, userID, id string) (domain.Account, error) {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return domain.Account{}, err
	}
	var doc accountDoc
	if err := r.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		return domain.Account{}, notFound(err)
	}
	return doc.toDomain(), nil
}

// Update stores the editable fields of a (name, initial balance, card,
// archive date); type, currency, source and creation never change.
func (r *Accounts) Update(ctx context.Context, a domain.Account) error {
	filter, err := ownedBy(a.UserID, a.ID)
	if err != nil {
		return err
	}
	res, err := r.coll.UpdateOne(ctx, filter, bson.D{{Key: "$set", Value: bson.D{
		{Key: "name", Value: a.Name},
		{Key: "initial_balance", Value: a.InitialBalance},
		{Key: "card", Value: newCardDoc(a.Card)},
		{Key: "archived_at", Value: a.ArchivedAt},
		{Key: "updated_at", Value: a.UpdatedAt.UTC()},
	}}})
	if err != nil {
		return fmt.Errorf("update account: %w", err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Delete removes account id of userID, or answers domain.ErrNotFound.
func (r *Accounts) Delete(ctx context.Context, userID, id string) error {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return err
	}
	res, err := r.coll.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if res.DeletedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

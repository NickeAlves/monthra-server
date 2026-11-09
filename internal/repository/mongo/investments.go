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

// maxInvestmentsListed bounds one listing, well above domain.MaxInvestments,
// so a bug can never load an unbounded result.
const maxInvestmentsListed = 1000

// Investments stores the assets of each user's portfolio. Every query
// filters by user_id, so one user can never reach another's investment
// (IDOR); investments_user serves them all.
type Investments struct{ coll *mongo.Collection }

// NewInvestments returns the investments repository on db.
func NewInvestments(db *mongo.Database) *Investments {
	return &Investments{coll: db.Collection(CollInvestments)}
}

// investmentDoc is one investment; ticker and account_id are absent when
// unset.
type investmentDoc struct {
	ID        bson.ObjectID  `bson:"_id"`
	UserID    bson.ObjectID  `bson:"user_id"`
	Name      string         `bson:"name"`
	Type      string         `bson:"type"`
	Mode      string         `bson:"mode"`
	Currency  string         `bson:"currency"`
	Ticker    string         `bson:"ticker,omitempty"`
	AccountID *bson.ObjectID `bson:"account_id,omitempty"`
	Source    string         `bson:"source"`
	CreatedAt time.Time      `bson:"created_at"`
	UpdatedAt time.Time      `bson:"updated_at"`
}

func newInvestmentDoc(inv domain.Investment) (investmentDoc, error) {
	uid, err := objectID(inv.UserID)
	if err != nil {
		return investmentDoc{}, err
	}
	account, err := optionalID(inv.AccountID)
	if err != nil {
		return investmentDoc{}, err
	}
	return investmentDoc{
		ID: bson.NewObjectID(), UserID: uid, Name: inv.Name, Type: string(inv.Type), Mode: string(inv.Mode),
		Currency: inv.Currency, Ticker: inv.Ticker, AccountID: account, Source: string(inv.Source),
		CreatedAt: inv.CreatedAt.UTC(), UpdatedAt: inv.UpdatedAt.UTC(),
	}, nil
}

func (d investmentDoc) toDomain() domain.Investment {
	inv := domain.Investment{
		ID: d.ID.Hex(), UserID: d.UserID.Hex(), Name: d.Name, Type: domain.InvestmentType(d.Type),
		Mode: domain.InvestmentMode(d.Mode), Currency: d.Currency, Ticker: d.Ticker,
		Source: domain.Source(d.Source), CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if d.AccountID != nil {
		inv.AccountID = d.AccountID.Hex()
	}
	return inv
}

// List returns the investments of userID, oldest first.
func (r *Investments) List(ctx context.Context, userID string) ([]domain.Investment, error) {
	uid, err := objectID(userID)
	if err != nil {
		return []domain.Investment{}, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	opts := options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(maxInvestmentsListed)
	cur, err := r.coll.Find(ctx, bson.D{{Key: "user_id", Value: uid}}, opts)
	if err != nil {
		return nil, fmt.Errorf("find investments: %w", err)
	}
	var docs []investmentDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode investments: %w", err)
	}
	out := make([]domain.Investment, len(docs))
	for i, d := range docs {
		out[i] = d.toDomain()
	}
	return out, nil
}

// Count returns how many investments userID has.
func (r *Investments) Count(ctx context.Context, userID string) (int, error) {
	uid, err := objectID(userID)
	if err != nil {
		return 0, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	n, err := r.coll.CountDocuments(ctx, bson.D{{Key: "user_id", Value: uid}})
	if err != nil {
		return 0, fmt.Errorf("count investments: %w", err)
	}
	return int(n), nil
}

// Insert stores inv with a new id and returns it.
func (r *Investments) Insert(ctx context.Context, inv domain.Investment) (domain.Investment, error) {
	doc, err := newInvestmentDoc(inv)
	if err != nil {
		return domain.Investment{}, err
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		return domain.Investment{}, fmt.Errorf("insert investment: %w", err)
	}
	return doc.toDomain(), nil
}

// ByID returns investment id of userID, or domain.ErrNotFound.
func (r *Investments) ByID(ctx context.Context, userID, id string) (domain.Investment, error) {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return domain.Investment{}, err
	}
	var doc investmentDoc
	if err := r.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		return domain.Investment{}, notFound(err)
	}
	return doc.toDomain(), nil
}

// Update stores the editable fields of inv (name, type, ticker, account);
// the mode and the currency never change.
func (r *Investments) Update(ctx context.Context, inv domain.Investment) error {
	filter, err := ownedBy(inv.UserID, inv.ID)
	if err != nil {
		return err
	}
	account, err := optionalID(inv.AccountID)
	if err != nil {
		return err
	}
	set := bson.D{
		{Key: "name", Value: inv.Name},
		{Key: "type", Value: string(inv.Type)},
		{Key: "updated_at", Value: inv.UpdatedAt.UTC()},
	}
	var unset bson.D
	if inv.Ticker != "" {
		set = append(set, bson.E{Key: "ticker", Value: inv.Ticker})
	} else {
		unset = append(unset, bson.E{Key: "ticker", Value: ""})
	}
	if account != nil {
		set = append(set, bson.E{Key: "account_id", Value: *account})
	} else {
		unset = append(unset, bson.E{Key: "account_id", Value: ""})
	}
	update := bson.D{{Key: "$set", Value: set}}
	if len(unset) > 0 {
		update = append(update, bson.E{Key: "$unset", Value: unset})
	}
	res, err := r.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update investment: %w", err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Delete removes investment id of userID, or answers domain.ErrNotFound.
func (r *Investments) Delete(ctx context.Context, userID, id string) error {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return err
	}
	res, err := r.coll.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("delete investment: %w", err)
	}
	if res.DeletedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// UnlinkAccount removes account accountID of userID from the investments
// kept at it, at at. The investments of a user are few (at most
// domain.MaxInvestments), so investments_user serves it.
func (r *Investments) UnlinkAccount(ctx context.Context, userID, accountID string, at time.Time) error {
	uid, err := objectID(userID)
	if err != nil {
		return nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	account, err := objectID(accountID)
	if err != nil {
		return nil //nolint:nilerr // an id that cannot exist names nothing
	}
	_, err = r.coll.UpdateMany(ctx,
		bson.D{{Key: "user_id", Value: uid}, {Key: "account_id", Value: account}},
		bson.D{
			{Key: "$unset", Value: bson.D{{Key: "account_id", Value: ""}}},
			{Key: "$set", Value: bson.D{{Key: "updated_at", Value: at.UTC()}}},
		})
	if err != nil {
		return fmt.Errorf("unlink investments: %w", err)
	}
	return nil
}

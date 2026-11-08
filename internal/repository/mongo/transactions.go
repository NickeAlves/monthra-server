package mongo

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Transactions stores the expenses, income and transfers of each user.
// Every query filters by user_id, so one user can never reach another's
// transaction (IDOR).
type Transactions struct{ coll *mongo.Collection }

// NewTransactions returns the transactions repository on db.
func NewTransactions(db *mongo.Database) *Transactions {
	return &Transactions{coll: db.Collection(CollTransactions)}
}

// transactionDoc is one transaction. category_id is absent on transfers;
// the to_* fields are present on transfers only. date is the civil day at
// midnight UTC. description_search is the folded description that text
// search matches; it never leaves the store.
type transactionDoc struct {
	ID                bson.ObjectID  `bson:"_id"`
	UserID            bson.ObjectID  `bson:"user_id"`
	Kind              string         `bson:"kind"`
	AccountID         bson.ObjectID  `bson:"account_id"`
	Currency          string         `bson:"currency"`
	CategoryID        *bson.ObjectID `bson:"category_id,omitempty"`
	Amount            int64          `bson:"amount"`
	ToAccountID       *bson.ObjectID `bson:"to_account_id,omitempty"`
	ToCurrency        string         `bson:"to_currency,omitempty"`
	ToAmount          int64          `bson:"to_amount,omitempty"`
	Date              time.Time      `bson:"date"`
	Description       string         `bson:"description"`
	DescriptionSearch string         `bson:"description_search"`
	Tags              []string       `bson:"tags"`
	Source            string         `bson:"source"`
	CreatedAt         time.Time      `bson:"created_at"`
	UpdatedAt         time.Time      `bson:"updated_at"`
}

// optionalID parses an id that may be absent ("" is nil).
func optionalID(id string) (*bson.ObjectID, error) {
	if id == "" {
		return nil, nil
	}
	oid, err := objectID(id)
	if err != nil {
		return nil, err
	}
	return &oid, nil
}

func newTransactionDoc(t domain.Transaction) (transactionDoc, error) {
	uid, err := objectID(t.UserID)
	if err != nil {
		return transactionDoc{}, err
	}
	doc := transactionDoc{
		ID: bson.NewObjectID(), UserID: uid, Kind: string(t.Kind), Source: string(t.Source),
		CreatedAt: t.CreatedAt.UTC(), UpdatedAt: t.UpdatedAt.UTC(),
	}
	if err := doc.setEditable(t); err != nil {
		return transactionDoc{}, err
	}
	return doc, nil
}

// setEditable copies the fields a patch may change.
func (d *transactionDoc) setEditable(t domain.Transaction) error {
	account, err := objectID(t.AccountID)
	if err != nil {
		return err
	}
	category, err := optionalID(t.CategoryID)
	if err != nil {
		return err
	}
	to, err := optionalID(t.ToAccountID)
	if err != nil {
		return err
	}
	tags := t.Tags
	if tags == nil {
		tags = []string{}
	}
	d.AccountID, d.Currency, d.CategoryID, d.Amount = account, t.Currency, category, t.Amount
	d.ToAccountID, d.ToCurrency, d.ToAmount = to, t.ToCurrency, t.ToAmount
	d.Date, d.Description, d.DescriptionSearch, d.Tags = t.Date.Time(), t.Description, domain.FoldText(t.Description), tags
	return nil
}

func (d transactionDoc) toDomain() domain.Transaction {
	t := domain.Transaction{
		ID: d.ID.Hex(), UserID: d.UserID.Hex(), Kind: domain.TransactionKind(d.Kind),
		AccountID: d.AccountID.Hex(), Currency: d.Currency, Amount: d.Amount,
		ToCurrency: d.ToCurrency, ToAmount: d.ToAmount,
		Date: domain.DateOf(d.Date.UTC()), Description: d.Description, Tags: d.Tags,
		Source: domain.Source(d.Source), CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if d.CategoryID != nil {
		t.CategoryID = d.CategoryID.Hex()
	}
	if d.ToAccountID != nil {
		t.ToAccountID = d.ToAccountID.Hex()
	}
	if t.Tags == nil {
		t.Tags = []string{}
	}
	return t
}

// Insert stores t with a new id and returns it.
func (r *Transactions) Insert(ctx context.Context, t domain.Transaction) (domain.Transaction, error) {
	doc, err := newTransactionDoc(t)
	if err != nil {
		return domain.Transaction{}, err
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		return domain.Transaction{}, fmt.Errorf("insert transaction: %w", err)
	}
	return doc.toDomain(), nil
}

// ByID returns transaction id of userID, or domain.ErrNotFound.
func (r *Transactions) ByID(ctx context.Context, userID, id string) (domain.Transaction, error) {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return domain.Transaction{}, err
	}
	var doc transactionDoc
	if err := r.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		return domain.Transaction{}, notFound(err)
	}
	return doc.toDomain(), nil
}

// Update stores the editable fields of t; the kind, the source and the
// creation never change.
func (r *Transactions) Update(ctx context.Context, t domain.Transaction) error {
	filter, err := ownedBy(t.UserID, t.ID)
	if err != nil {
		return err
	}
	var doc transactionDoc
	if err := doc.setEditable(t); err != nil {
		return err
	}
	set := bson.D{
		{Key: "account_id", Value: doc.AccountID},
		{Key: "currency", Value: doc.Currency},
		{Key: "amount", Value: doc.Amount},
		{Key: "date", Value: doc.Date},
		{Key: "description", Value: doc.Description},
		{Key: "description_search", Value: doc.DescriptionSearch},
		{Key: "tags", Value: doc.Tags},
		{Key: "updated_at", Value: t.UpdatedAt.UTC()},
	}
	// The kind never changes, so each kind keeps the same shape.
	if t.IsTransfer() {
		set = append(set,
			bson.E{Key: "to_account_id", Value: doc.ToAccountID},
			bson.E{Key: "to_currency", Value: doc.ToCurrency},
			bson.E{Key: "to_amount", Value: doc.ToAmount})
	} else {
		set = append(set, bson.E{Key: "category_id", Value: doc.CategoryID})
	}
	res, err := r.coll.UpdateOne(ctx, filter, bson.D{{Key: "$set", Value: set}})
	if err != nil {
		return fmt.Errorf("update transaction: %w", err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Delete removes transaction id of userID, or answers domain.ErrNotFound.
func (r *Transactions) Delete(ctx context.Context, userID, id string) error {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return err
	}
	res, err := r.coll.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("delete transaction: %w", err)
	}
	if res.DeletedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// List returns up to limit transactions of userID that match f, newest
// date first and then newest id, starting after the cursor when there is
// one. An id in f or in the cursor that cannot exist matches nothing.
func (r *Transactions) List(ctx context.Context, userID string, f domain.TransactionFilter, after *domain.TransactionCursor, limit int) ([]domain.Transaction, error) {
	filter, err := listFilter(userID, f, after)
	if err != nil {
		return []domain.Transaction{}, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "date", Value: -1}, {Key: "_id", Value: -1}}).
		SetLimit(int64(limit))
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("find transactions: %w", err)
	}
	var docs []transactionDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode transactions: %w", err)
	}
	out := make([]domain.Transaction, len(docs))
	for i, d := range docs {
		out[i] = d.toDomain()
	}
	return out, nil
}

// listFilter is the query of List. Each alternative ($or) goes in one
// $and, since a document holds one $or key.
func listFilter(userID string, f domain.TransactionFilter, after *domain.TransactionCursor) (bson.D, error) {
	uid, err := objectID(userID)
	if err != nil {
		return nil, err
	}
	filter := bson.D{{Key: "user_id", Value: uid}}
	var alternatives bson.A

	if !f.From.IsZero() || !f.To.IsZero() {
		var date bson.D
		if !f.From.IsZero() {
			date = append(date, bson.E{Key: "$gte", Value: f.From.Time()})
		}
		if !f.To.IsZero() {
			date = append(date, bson.E{Key: "$lte", Value: f.To.Time()})
		}
		filter = append(filter, bson.E{Key: "date", Value: date})
	}
	if f.AccountID != "" {
		account, err := objectID(f.AccountID)
		if err != nil {
			return nil, err
		}
		alternatives = append(alternatives, bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "account_id", Value: account}},
			bson.D{{Key: "to_account_id", Value: account}},
		}}})
	}
	if f.CategoryID != "" {
		category, err := objectID(f.CategoryID)
		if err != nil {
			return nil, err
		}
		filter = append(filter, bson.E{Key: "category_id", Value: category})
	}
	if f.Kind != "" {
		filter = append(filter, bson.E{Key: "kind", Value: string(f.Kind)})
	}
	if f.Tag != "" {
		filter = append(filter, bson.E{Key: "tags", Value: f.Tag})
	}
	if f.Search != "" {
		// A literal match: the reader's text never acts as a pattern.
		filter = append(filter, bson.E{Key: "description_search", Value: bson.Regex{Pattern: regexp.QuoteMeta(f.Search)}})
	}
	if after != nil {
		last, err := objectID(after.ID)
		if err != nil {
			return nil, err
		}
		day := after.Date.Time()
		alternatives = append(alternatives, bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "date", Value: bson.D{{Key: "$lt", Value: day}}}},
			bson.D{{Key: "date", Value: day}, {Key: "_id", Value: bson.D{{Key: "$lt", Value: last}}}},
		}}})
	}
	if len(alternatives) > 0 {
		filter = append(filter, bson.E{Key: "$and", Value: alternatives})
	}
	return filter, nil
}

// balanceDoc is the sum of the transactions of one account.
type balanceDoc struct {
	Account   bson.ObjectID `bson:"_id"`
	Current   int64         `bson:"current"`
	Projected int64         `bson:"projected"`
}

// Balances sums the transactions of userID by account, in minor units of
// each account's currency: Current up to asOf (included), Projected all of
// them. The initial balances are the caller's to add. Only the accounts of
// accountIDs are summed, unless it is nil; an account without transactions
// is missing from the map.
func (r *Transactions) Balances(ctx context.Context, userID string, accountIDs []string, asOf domain.Date) (map[string]domain.Balance, error) {
	out := map[string]domain.Balance{}
	uid, err := objectID(userID)
	if err != nil {
		return out, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	match := bson.D{{Key: "user_id", Value: uid}}
	legMatch := bson.D{{Key: "legs.account", Value: bson.D{{Key: "$ne", Value: nil}}}}
	if accountIDs != nil {
		ids := make(bson.A, 0, len(accountIDs))
		for _, id := range accountIDs {
			if oid, err := objectID(id); err == nil {
				ids = append(ids, oid)
			}
		}
		if len(ids) == 0 {
			return out, nil
		}
		in := bson.D{{Key: "$in", Value: ids}}
		match = append(match, bson.E{Key: "$or", Value: bson.A{
			bson.D{{Key: "account_id", Value: in}},
			bson.D{{Key: "to_account_id", Value: in}},
		}})
		legMatch = bson.D{{Key: "legs.account", Value: in}}
	}
	// Each transaction moves its account (income in, expenses and transfers
	// out) and, on a transfer, its destination (to_amount in).
	signed := bson.D{{Key: "$cond", Value: bson.A{
		bson.D{{Key: "$eq", Value: bson.A{"$kind", string(domain.TransactionIncome)}}},
		"$amount",
		bson.D{{Key: "$multiply", Value: bson.A{"$amount", -1}}},
	}}}
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: match}},
		{{Key: "$project", Value: bson.D{
			{Key: "date", Value: 1},
			{Key: "legs", Value: bson.A{
				bson.D{{Key: "account", Value: "$account_id"}, {Key: "value", Value: signed}},
				bson.D{{Key: "account", Value: "$to_account_id"}, {Key: "value", Value: "$to_amount"}},
			}},
		}}},
		{{Key: "$unwind", Value: "$legs"}},
		{{Key: "$match", Value: legMatch}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$legs.account"},
			{Key: "projected", Value: bson.D{{Key: "$sum", Value: "$legs.value"}}},
			{Key: "current", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$cond", Value: bson.A{
				bson.D{{Key: "$lte", Value: bson.A{"$date", asOf.Time()}}}, "$legs.value", 0,
			}}}}}},
		}}},
	}
	cur, err := r.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("sum balances: %w", err)
	}
	var docs []balanceDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode balances: %w", err)
	}
	for _, d := range docs {
		out[d.Account.Hex()] = domain.Balance{Current: d.Current, Projected: d.Projected}
	}
	return out, nil
}

// AccountInUse reports whether account id of userID has transactions, as
// their account or as the destination of a transfer.
func (r *Transactions) AccountInUse(ctx context.Context, userID, id string) (bool, error) {
	uid, err := objectID(userID)
	if err != nil {
		return false, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	account, err := objectID(id)
	if err != nil {
		return false, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	return r.exists(ctx, bson.D{{Key: "user_id", Value: uid}, {Key: "$or", Value: bson.A{
		bson.D{{Key: "account_id", Value: account}},
		bson.D{{Key: "to_account_id", Value: account}},
	}}})
}

// CategoryInUse reports whether category id of userID has transactions.
func (r *Transactions) CategoryInUse(ctx context.Context, userID, id string) (bool, error) {
	uid, err := objectID(userID)
	if err != nil {
		return false, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	category, err := objectID(id)
	if err != nil {
		return false, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	return r.exists(ctx, bson.D{{Key: "user_id", Value: uid}, {Key: "category_id", Value: category}})
}

func (r *Transactions) exists(ctx context.Context, filter bson.D) (bool, error) {
	err := r.coll.FindOne(ctx, filter, options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Err()
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, mongo.ErrNoDocuments):
		return false, nil
	default:
		return false, fmt.Errorf("find transaction: %w", err)
	}
}

// ReassignCategory moves the transactions of userID from category from to
// category to; the caller checked both are the owner's, of one kind.
func (r *Transactions) ReassignCategory(ctx context.Context, userID, from, to string, at time.Time) error {
	uid, err := objectID(userID)
	if err != nil {
		return err
	}
	src, err := objectID(from)
	if err != nil {
		return err
	}
	dst, err := objectID(to)
	if err != nil {
		return err
	}
	_, err = r.coll.UpdateMany(ctx,
		bson.D{{Key: "user_id", Value: uid}, {Key: "category_id", Value: src}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "category_id", Value: dst}, {Key: "updated_at", Value: at.UTC()}}}})
	if err != nil {
		return fmt.Errorf("reassign transactions: %w", err)
	}
	return nil
}

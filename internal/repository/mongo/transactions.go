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
// midnight UTC. invoice_month ("YYYY-MM") is present when account_id is a
// credit card, to_invoice_month when to_account_id is; neither is ever null,
// so the partial invoice indexes hold exactly the card legs.
// description_search is the folded description that text search matches;
// it never leaves the store. installment is present on the installments of
// a purchase only, recurrence_id and occurrence_date (the day the rule gave)
// on the occurrences of a recurrence only; neither ever changes.
type transactionDoc struct {
	ID                bson.ObjectID   `bson:"_id"`
	UserID            bson.ObjectID   `bson:"user_id"`
	Kind              string          `bson:"kind"`
	AccountID         bson.ObjectID   `bson:"account_id"`
	Currency          string          `bson:"currency"`
	CategoryID        *bson.ObjectID  `bson:"category_id,omitempty"`
	Amount            int64           `bson:"amount"`
	ToAccountID       *bson.ObjectID  `bson:"to_account_id,omitempty"`
	ToCurrency        string          `bson:"to_currency,omitempty"`
	ToAmount          int64           `bson:"to_amount,omitempty"`
	Date              time.Time       `bson:"date"`
	InvoiceMonth      string          `bson:"invoice_month,omitempty"`
	ToInvoiceMonth    string          `bson:"to_invoice_month,omitempty"`
	Description       string          `bson:"description"`
	DescriptionSearch string          `bson:"description_search"`
	Tags              []string        `bson:"tags"`
	Installment       *installmentDoc `bson:"installment,omitempty"`
	RecurrenceID      *bson.ObjectID  `bson:"recurrence_id,omitempty"`
	OccurrenceDate    *time.Time      `bson:"occurrence_date,omitempty"`
	Source            string          `bson:"source"`
	CreatedAt         time.Time       `bson:"created_at"`
	UpdatedAt         time.Time       `bson:"updated_at"`
}

// installmentDoc places a transaction in an installment purchase.
type installmentDoc struct {
	GroupID bson.ObjectID `bson:"group_id"`
	Number  int           `bson:"number"`
	Count   int           `bson:"count"`
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
	if t.Installment != nil {
		group, err := objectID(t.Installment.GroupID)
		if err != nil {
			return transactionDoc{}, err
		}
		doc.Installment = &installmentDoc{GroupID: group, Number: t.Installment.Number, Count: t.Installment.Count}
	}
	if t.Recurrence != nil {
		rec, err := objectID(t.Recurrence.ID)
		if err != nil {
			return transactionDoc{}, err
		}
		doc.RecurrenceID, doc.OccurrenceDate = &rec, optionalDay(t.Recurrence.Date)
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
	d.InvoiceMonth, d.ToInvoiceMonth = t.InvoiceMonth.String(), t.ToInvoiceMonth.String()
	d.Date, d.Description, d.DescriptionSearch, d.Tags = t.Date.Time(), t.Description, domain.FoldText(t.Description), tags
	return nil
}

func (d transactionDoc) toDomain() domain.Transaction {
	t := domain.Transaction{
		ID: d.ID.Hex(), UserID: d.UserID.Hex(), Kind: domain.TransactionKind(d.Kind),
		AccountID: d.AccountID.Hex(), Currency: d.Currency, Amount: d.Amount,
		ToCurrency: d.ToCurrency, ToAmount: d.ToAmount,
		Date: domain.DateOf(d.Date.UTC()), InvoiceMonth: storedMonth(d.InvoiceMonth),
		ToInvoiceMonth: storedMonth(d.ToInvoiceMonth), Description: d.Description, Tags: d.Tags,
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
	if d.Installment != nil {
		t.Installment = &domain.Installment{GroupID: d.Installment.GroupID.Hex(), Number: d.Installment.Number, Count: d.Installment.Count}
	}
	if d.RecurrenceID != nil {
		t.Recurrence = &domain.RecurrenceLink{ID: d.RecurrenceID.Hex(), Date: storedDay(d.OccurrenceDate)}
	}
	return t
}

// storedMonth reads a month the store wrote ("YYYY-MM"); "" is no month.
// It takes any month the domain computed, beyond the ones clients may name.
func storedMonth(s string) domain.Month {
	t, err := time.Parse("2006-01", s)
	if err != nil {
		return domain.Month{}
	}
	return domain.NewMonth(t.Year(), t.Month())
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

// cleanupTimeout bounds the removal of a purchase whose insert failed.
const cleanupTimeout = 5 * time.Second

// InsertInstallments stores the installments of one purchase under a new
// group id and returns them, in order. MongoDB runs standalone (no
// multi-document transactions), so when the insert fails midway the
// installments already stored are deleted once, even if the request is
// gone, and the error is returned.
func (r *Transactions) InsertInstallments(ctx context.Context, ts []domain.Transaction) ([]domain.Transaction, error) {
	group := bson.NewObjectID()
	docs := make([]any, len(ts))
	out := make([]domain.Transaction, len(ts))
	for i, t := range ts {
		if t.Installment == nil {
			return nil, errors.New("insert installments: not an installment")
		}
		inst := *t.Installment
		inst.GroupID = group.Hex()
		t.Installment = &inst
		doc, err := newTransactionDoc(t)
		if err != nil {
			return nil, err
		}
		docs[i] = doc
		out[i] = doc.toDomain()
	}
	if _, err := r.coll.InsertMany(ctx, docs); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		uid, _ := objectID(ts[0].UserID)
		_, _ = r.coll.DeleteMany(cleanup, inGroup(uid, group, 0))
		return nil, fmt.Errorf("insert installments: %w", err)
	}
	return out, nil
}

// inGroup matches the installments of group of uid numbered from (0 or 1
// for all of them).
func inGroup(uid, group bson.ObjectID, from int) bson.D {
	f := bson.D{{Key: "user_id", Value: uid}, {Key: "installment.group_id", Value: group}}
	if from > 1 {
		f = append(f, bson.E{Key: "installment.number", Value: bson.D{{Key: "$gte", Value: from}}})
	}
	return f
}

// groupFilter is inGroup for ids as the domain holds them; an id that
// cannot exist is an error, which owns nothing.
func groupFilter(userID, groupID string, from int) (bson.D, error) {
	uid, err := objectID(userID)
	if err != nil {
		return nil, err
	}
	group, err := objectID(groupID)
	if err != nil {
		return nil, err
	}
	return inGroup(uid, group, from), nil
}

// Installments returns the installments of group groupID of userID
// numbered from on (1 for all of them), in their order.
func (r *Transactions) Installments(ctx context.Context, userID, groupID string, from int) ([]domain.Transaction, error) {
	filter, err := groupFilter(userID, groupID, from)
	if err != nil {
		return []domain.Transaction{}, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	cur, err := r.coll.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "installment.number", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("find installments: %w", err)
	}
	var docs []transactionDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode installments: %w", err)
	}
	out := make([]domain.Transaction, len(docs))
	for i, d := range docs {
		out[i] = d.toDomain()
	}
	return out, nil
}

// DeleteInstallments removes the installments of group groupID of userID
// numbered from on (1 for all of them); deleting them again is a no-op.
func (r *Transactions) DeleteInstallments(ctx context.Context, userID, groupID string, from int) error {
	filter, err := groupFilter(userID, groupID, from)
	if err != nil {
		return nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	if _, err := r.coll.DeleteMany(ctx, filter); err != nil {
		return fmt.Errorf("delete installments: %w", err)
	}
	return nil
}

// InsertOccurrences stores the occurrences ts of recurrences and returns
// how many were new. An occurrence already stored (a concurrent reader
// created it first, or a pass interrupted before its recurrence advanced)
// is refused by the unique index on (recurrence_id, occurrence_date) and
// skipped; any other failure is returned.
func (r *Transactions) InsertOccurrences(ctx context.Context, ts []domain.Transaction) (int, error) {
	if len(ts) == 0 {
		return 0, nil
	}
	docs := make([]any, len(ts))
	for i, t := range ts {
		if t.Recurrence == nil {
			return 0, errors.New("insert occurrences: not an occurrence")
		}
		doc, err := newTransactionDoc(t)
		if err != nil {
			return 0, err
		}
		docs[i] = doc
	}
	_, err := r.coll.InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	if err == nil {
		return len(docs), nil
	}
	var bulk mongo.BulkWriteException
	if !errors.As(err, &bulk) || bulk.WriteConcernError != nil {
		return 0, fmt.Errorf("insert occurrences: %w", err)
	}
	for _, we := range bulk.WriteErrors {
		if we.Code != duplicateKey {
			return 0, fmt.Errorf("insert occurrences: %w", err)
		}
	}
	return len(docs) - len(bulk.WriteErrors), nil
}

// duplicateKey is the server's error code for a unique index violation.
const duplicateKey = 11000

// inRecurrence matches the occurrences of recurrence rec of uid whose rule
// day is from or later (all of them when from is zero).
func inRecurrence(userID, recurrenceID string, from domain.Date) (bson.D, error) {
	uid, err := objectID(userID)
	if err != nil {
		return nil, err
	}
	rec, err := objectID(recurrenceID)
	if err != nil {
		return nil, err
	}
	f := bson.D{{Key: "recurrence_id", Value: rec}, {Key: "user_id", Value: uid}}
	if !from.IsZero() {
		f = append(f, bson.E{Key: "occurrence_date", Value: bson.D{{Key: "$gte", Value: from.Time()}}})
	}
	return f, nil
}

// Occurrences returns the occurrences of recurrence recurrenceID of userID
// whose rule day is from or later (all of them when from is zero), in the
// order of those days.
func (r *Transactions) Occurrences(ctx context.Context, userID, recurrenceID string, from domain.Date) ([]domain.Transaction, error) {
	filter, err := inRecurrence(userID, recurrenceID, from)
	if err != nil {
		return []domain.Transaction{}, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	cur, err := r.coll.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "occurrence_date", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("find occurrences: %w", err)
	}
	var docs []transactionDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode occurrences: %w", err)
	}
	out := make([]domain.Transaction, len(docs))
	for i, d := range docs {
		out[i] = d.toDomain()
	}
	return out, nil
}

// HasOccurrencesBefore reports whether recurrence recurrenceID of userID has
// occurrences whose rule day comes before day.
func (r *Transactions) HasOccurrencesBefore(ctx context.Context, userID, recurrenceID string, day domain.Date) (bool, error) {
	filter, err := inRecurrence(userID, recurrenceID, domain.Date{})
	if err != nil {
		return false, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	filter = append(filter, bson.E{Key: "occurrence_date", Value: bson.D{{Key: "$lt", Value: day.Time()}}})
	return r.exists(ctx, filter)
}

// DeleteOccurrences removes the occurrences of recurrence recurrenceID of
// userID whose rule day is from or later (all of them when from is zero);
// deleting them again is a no-op.
func (r *Transactions) DeleteOccurrences(ctx context.Context, userID, recurrenceID string, from domain.Date) error {
	filter, err := inRecurrence(userID, recurrenceID, from)
	if err != nil {
		return nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	if _, err := r.coll.DeleteMany(ctx, filter); err != nil {
		return fmt.Errorf("delete occurrences: %w", err)
	}
	return nil
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

// Update stores the editable fields of t; the kind, the source, the
// installment and the creation never change.
func (r *Transactions) Update(ctx context.Context, t domain.Transaction) error {
	filter, update, err := updateOf(t)
	if err != nil {
		return err
	}
	res, err := r.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update transaction: %w", err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// UpdateMany stores the editable fields of each of ts, as Update does, in
// one round trip. The writes are independent (no multi-document
// transactions): after a failure some may be stored, and writing the same
// values again completes them.
func (r *Transactions) UpdateMany(ctx context.Context, ts []domain.Transaction) error {
	if len(ts) == 0 {
		return nil
	}
	models := make([]mongo.WriteModel, len(ts))
	for i, t := range ts {
		filter, update, err := updateOf(t)
		if err != nil {
			return err
		}
		models[i] = mongo.NewUpdateOneModel().SetFilter(filter).SetUpdate(update)
	}
	if _, err := r.coll.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false)); err != nil {
		return fmt.Errorf("update transactions: %w", err)
	}
	return nil
}

// updateOf is the filter and the update that store the editable fields of t.
func updateOf(t domain.Transaction) (bson.D, bson.D, error) {
	filter, err := ownedBy(t.UserID, t.ID)
	if err != nil {
		return nil, nil, err
	}
	var doc transactionDoc
	if err := doc.setEditable(t); err != nil {
		return nil, nil, err
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
	// An invoice a leg no longer has is removed, never stored as null.
	var unset bson.D
	invoices := []bson.E{{Key: "invoice_month", Value: doc.InvoiceMonth}}
	if t.IsTransfer() {
		invoices = append(invoices, bson.E{Key: "to_invoice_month", Value: doc.ToInvoiceMonth})
	}
	for _, inv := range invoices {
		if inv.Value == "" {
			unset = append(unset, bson.E{Key: inv.Key, Value: ""})
		} else {
			set = append(set, inv)
		}
	}
	update := bson.D{{Key: "$set", Value: set}}
	if len(unset) > 0 {
		update = append(update, bson.E{Key: "$unset", Value: unset})
	}
	return filter, update, nil
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

// listFilter is the query of List. A filter by account (or by invoice)
// matches either leg of a transaction, each through its own index (see
// eitherLeg). The cursor bounds the date, so a later page starts reading
// where the previous one ended.
func listFilter(userID string, f domain.TransactionFilter, after *domain.TransactionCursor) (bson.D, error) {
	uid, err := objectID(userID)
	if err != nil {
		return nil, err
	}
	var rest bson.D
	upper := f.To
	if after != nil && (upper.IsZero() || after.Date.Before(upper)) {
		upper = after.Date
	}
	if !f.From.IsZero() || !upper.IsZero() {
		var date bson.D
		if !f.From.IsZero() {
			date = append(date, bson.E{Key: "$gte", Value: f.From.Time()})
		}
		if !upper.IsZero() {
			date = append(date, bson.E{Key: "$lte", Value: upper.Time()})
		}
		rest = append(rest, bson.E{Key: "date", Value: date})
	}
	if f.CategoryID != "" {
		category, err := objectID(f.CategoryID)
		if err != nil {
			return nil, err
		}
		rest = append(rest, bson.E{Key: "category_id", Value: category})
	}
	if f.Kind != "" {
		rest = append(rest, bson.E{Key: "kind", Value: string(f.Kind)})
	}
	if f.Tag != "" {
		rest = append(rest, bson.E{Key: "tags", Value: f.Tag})
	}
	if f.InstallmentGroupID != "" {
		group, err := objectID(f.InstallmentGroupID)
		if err != nil {
			return nil, err
		}
		rest = append(rest, bson.E{Key: "installment.group_id", Value: group})
	}
	if f.RecurrenceID != "" {
		rec, err := objectID(f.RecurrenceID)
		if err != nil {
			return nil, err
		}
		rest = append(rest, bson.E{Key: "recurrence_id", Value: rec})
	}
	if f.Search != "" {
		// A literal match: the reader's text never acts as a pattern.
		rest = append(rest, bson.E{Key: "description_search", Value: bson.Regex{Pattern: regexp.QuoteMeta(f.Search)}})
	}
	if after != nil {
		last, err := objectID(after.ID)
		if err != nil {
			return nil, err
		}
		// Within the date bound above: the days before the cursor's, and on
		// its day the ids before its.
		rest = append(rest, bson.E{Key: "$or", Value: bson.A{
			bson.D{{Key: "date", Value: bson.D{{Key: "$lt", Value: after.Date.Time()}}}},
			bson.D{{Key: "_id", Value: bson.D{{Key: "$lt", Value: last}}}},
		}})
	}

	// Each leg of a transaction: its account and, on a transfer, its
	// destination, with the invoice of that leg when f names one.
	var source, destination bson.D
	if f.AccountID != "" {
		account, err := objectID(f.AccountID)
		if err != nil {
			return nil, err
		}
		source = bson.D{{Key: "account_id", Value: account}}
		destination = bson.D{{Key: "to_account_id", Value: account}}
	}
	if !f.Invoice.IsZero() {
		source = append(source, bson.E{Key: "invoice_month", Value: f.Invoice.String()})
		destination = append(destination, bson.E{Key: "to_invoice_month", Value: f.Invoice.String()})
	}
	if source == nil {
		return append(bson.D{{Key: "user_id", Value: uid}}, rest...), nil
	}
	return eitherLeg(uid, source, destination, rest), nil
}

// eitherLeg matches the transactions of uid by their account leg (source)
// or by their destination leg (destination), each with the conditions of
// rest. The $or is the root of the filter, with user_id inside each branch,
// so each branch reads its own index; beside a top-level user_id, MongoDB
// reads every transaction of the user instead.
func eitherLeg(uid bson.ObjectID, source, destination, rest bson.D) bson.D {
	branch := func(leg bson.D) bson.D {
		b := append(bson.D{{Key: "user_id", Value: uid}}, leg...)
		return append(b, rest...)
	}
	return bson.D{{Key: "$or", Value: bson.A{branch(source), branch(destination)}}}
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
		match = eitherLeg(uid, bson.D{{Key: "account_id", Value: in}}, bson.D{{Key: "to_account_id", Value: in}}, nil)
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

// invoiceSumsDoc is the sum of one invoice of a card.
type invoiceSumsDoc struct {
	Month    string `bson:"_id"`
	Charges  int64  `bson:"charges"`
	Credits  int64  `bson:"credits"`
	Payments int64  `bson:"payments"`
}

// InvoiceSums sums the transactions of card accountID of userID by invoice,
// for every invoice up to through (included), oldest first: the charges
// (expenses and transfers out), the credits (income) and the payments
// (what transfers to the card brought). An invoice without transactions is
// missing.
func (r *Transactions) InvoiceSums(ctx context.Context, userID, accountID string, through domain.Month) ([]domain.InvoiceSums, error) {
	uid, err := objectID(userID)
	if err != nil {
		return []domain.InvoiceSums{}, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	card, err := objectID(accountID)
	if err != nil {
		return []domain.InvoiceSums{}, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	upTo := bson.D{{Key: "$lte", Value: through.String()}}
	income := bson.D{{Key: "$eq", Value: bson.A{"$kind", string(domain.TransactionIncome)}}}
	// legOf is the invoice of a leg when that leg is the card, and null
	// otherwise.
	legOf := func(account, invoice string) bson.D {
		return bson.D{{Key: "$cond", Value: bson.A{
			bson.D{{Key: "$eq", Value: bson.A{account, card}}}, invoice, nil,
		}}}
	}
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: eitherLeg(uid,
			bson.D{{Key: "account_id", Value: card}, {Key: "invoice_month", Value: upTo}},
			bson.D{{Key: "to_account_id", Value: card}, {Key: "to_invoice_month", Value: upTo}}, nil)}},
		{{Key: "$project", Value: bson.D{{Key: "legs", Value: bson.A{
			bson.D{
				{Key: "month", Value: legOf("$account_id", "$invoice_month")},
				{Key: "charge", Value: bson.D{{Key: "$cond", Value: bson.A{income, 0, "$amount"}}}},
				{Key: "credit", Value: bson.D{{Key: "$cond", Value: bson.A{income, "$amount", 0}}}},
				{Key: "payment", Value: 0},
			},
			bson.D{
				{Key: "month", Value: legOf("$to_account_id", "$to_invoice_month")},
				{Key: "charge", Value: 0},
				{Key: "credit", Value: 0},
				{Key: "payment", Value: "$to_amount"},
			},
		}}}}},
		{{Key: "$unwind", Value: "$legs"}},
		// Only the legs on the card, within the invoices asked for: a month
		// compares as a string, so a null one never matches.
		{{Key: "$match", Value: bson.D{{Key: "legs.month", Value: upTo}}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$legs.month"},
			{Key: "charges", Value: bson.D{{Key: "$sum", Value: "$legs.charge"}}},
			{Key: "credits", Value: bson.D{{Key: "$sum", Value: "$legs.credit"}}},
			{Key: "payments", Value: bson.D{{Key: "$sum", Value: "$legs.payment"}}},
		}}},
		{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	}
	cur, err := r.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("sum invoices: %w", err)
	}
	var docs []invoiceSumsDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode invoices: %w", err)
	}
	out := make([]domain.InvoiceSums, len(docs))
	for i, d := range docs {
		out[i] = domain.InvoiceSums{Month: storedMonth(d.Month), Charges: d.Charges, Credits: d.Credits, Payments: d.Payments}
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
	return r.exists(ctx, eitherLeg(uid, bson.D{{Key: "account_id", Value: account}}, bson.D{{Key: "to_account_id", Value: account}}, nil))
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

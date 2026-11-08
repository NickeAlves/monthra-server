package mongo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// maxRecurrencesListed bounds one listing, well above the per-user cap of
// the use case, so a bug can never load an unbounded result.
const maxRecurrencesListed = 1000

// Recurrences stores the recurrences of each user. Every query filters by
// user_id, so one user can never reach another's recurrence (IDOR).
type Recurrences struct{ coll *mongo.Collection }

// NewRecurrences returns the recurrences repository on db.
func NewRecurrences(db *mongo.Database) *Recurrences {
	return &Recurrences{coll: db.Collection(CollRecurrences)}
}

// recurrenceDoc is one recurrence: the template of its occurrences, with
// the shape of a transaction (category_id absent on transfers, the to_*
// fields on transfers only), and its rule. Days are civil days at midnight
// UTC. end_date, next_date and last_date are absent when unset, never null,
// so the partial index on next_date holds exactly the active recurrences.
type recurrenceDoc struct {
	ID          bson.ObjectID  `bson:"_id"`
	UserID      bson.ObjectID  `bson:"user_id"`
	Kind        string         `bson:"kind"`
	AccountID   bson.ObjectID  `bson:"account_id"`
	Currency    string         `bson:"currency"`
	CategoryID  *bson.ObjectID `bson:"category_id,omitempty"`
	Amount      int64          `bson:"amount"`
	ToAccountID *bson.ObjectID `bson:"to_account_id,omitempty"`
	ToCurrency  string         `bson:"to_currency,omitempty"`
	ToAmount    int64          `bson:"to_amount,omitempty"`
	Description string         `bson:"description"`
	Tags        []string       `bson:"tags"`
	Frequency   string         `bson:"frequency"`
	Interval    int            `bson:"interval"`
	StartDate   time.Time      `bson:"start_date"`
	EndDate     *time.Time     `bson:"end_date,omitempty"`
	NextDate    *time.Time     `bson:"next_date,omitempty"`
	LastDate    *time.Time     `bson:"last_date,omitempty"`
	Source      string         `bson:"source"`
	CreatedAt   time.Time      `bson:"created_at"`
	UpdatedAt   time.Time      `bson:"updated_at"`
}

// optionalDay is a day that may be unset (nil).
func optionalDay(d domain.Date) *time.Time {
	if d.IsZero() {
		return nil
	}
	t := d.Time()
	return &t
}

// storedDay reads a day the store wrote; nil is no day.
func storedDay(t *time.Time) domain.Date {
	if t == nil {
		return domain.Date{}
	}
	return domain.DateOf(t.UTC())
}

func newRecurrenceDoc(r domain.Recurrence) (recurrenceDoc, error) {
	uid, err := objectID(r.UserID)
	if err != nil {
		return recurrenceDoc{}, err
	}
	doc := recurrenceDoc{
		ID: bson.NewObjectID(), UserID: uid, Kind: string(r.Kind), LastDate: optionalDay(r.Last),
		Source: string(r.Source), CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
	if err := doc.setEditable(r); err != nil {
		return recurrenceDoc{}, err
	}
	return doc, nil
}

// setEditable copies the fields a patch may change.
func (d *recurrenceDoc) setEditable(r domain.Recurrence) error {
	account, err := objectID(r.AccountID)
	if err != nil {
		return err
	}
	category, err := optionalID(r.CategoryID)
	if err != nil {
		return err
	}
	to, err := optionalID(r.ToAccountID)
	if err != nil {
		return err
	}
	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	d.AccountID, d.Currency, d.CategoryID, d.Amount = account, r.Currency, category, r.Amount
	d.ToAccountID, d.ToCurrency, d.ToAmount = to, r.ToCurrency, r.ToAmount
	d.Description, d.Tags = r.Description, tags
	d.Frequency, d.Interval, d.StartDate = string(r.Rule.Frequency), r.Rule.Interval, r.Rule.Start.Time()
	d.EndDate, d.NextDate = optionalDay(r.Rule.End), optionalDay(r.Next)
	return nil
}

func (d recurrenceDoc) toDomain() domain.Recurrence {
	r := domain.Recurrence{
		ID: d.ID.Hex(), UserID: d.UserID.Hex(), Kind: domain.TransactionKind(d.Kind),
		AccountID: d.AccountID.Hex(), Currency: d.Currency, Amount: d.Amount,
		ToCurrency: d.ToCurrency, ToAmount: d.ToAmount, Description: d.Description, Tags: d.Tags,
		Rule: domain.RecurrenceRule{
			Frequency: domain.Frequency(d.Frequency), Interval: d.Interval,
			Start: domain.DateOf(d.StartDate.UTC()), End: storedDay(d.EndDate),
		},
		Next: storedDay(d.NextDate), Last: storedDay(d.LastDate),
		Source: domain.Source(d.Source), CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if d.CategoryID != nil {
		r.CategoryID = d.CategoryID.Hex()
	}
	if d.ToAccountID != nil {
		r.ToAccountID = d.ToAccountID.Hex()
	}
	if r.Tags == nil {
		r.Tags = []string{}
	}
	return r
}

// find returns the recurrences matching filter, sorted by sort.
func (r *Recurrences) find(ctx context.Context, filter, sort bson.D, limit int64) ([]domain.Recurrence, error) {
	cur, err := r.coll.Find(ctx, filter, options.Find().SetSort(sort).SetLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("find recurrences: %w", err)
	}
	var docs []recurrenceDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode recurrences: %w", err)
	}
	out := make([]domain.Recurrence, len(docs))
	for i, d := range docs {
		out[i] = d.toDomain()
	}
	return out, nil
}

// List returns the recurrences of userID, oldest first.
func (r *Recurrences) List(ctx context.Context, userID string) ([]domain.Recurrence, error) {
	uid, err := objectID(userID)
	if err != nil {
		return []domain.Recurrence{}, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	return r.find(ctx, bson.D{{Key: "user_id", Value: uid}},
		bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}, maxRecurrencesListed)
}

// Due returns up to limit active recurrences of userID whose next
// occurrence falls on or before through, the earliest first.
func (r *Recurrences) Due(ctx context.Context, userID string, through domain.Date, limit int) ([]domain.Recurrence, error) {
	uid, err := objectID(userID)
	if err != nil {
		return []domain.Recurrence{}, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	return r.find(ctx,
		bson.D{{Key: "user_id", Value: uid}, {Key: "next_date", Value: bson.D{{Key: "$lte", Value: through.Time()}}}},
		bson.D{{Key: "next_date", Value: 1}, {Key: "_id", Value: 1}}, int64(limit))
}

// Count returns how many recurrences userID has, ended ones included.
func (r *Recurrences) Count(ctx context.Context, userID string) (int, error) {
	uid, err := objectID(userID)
	if err != nil {
		return 0, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	n, err := r.coll.CountDocuments(ctx, bson.D{{Key: "user_id", Value: uid}})
	if err != nil {
		return 0, fmt.Errorf("count recurrences: %w", err)
	}
	return int(n), nil
}

// Insert stores rec with a new id and returns it.
func (r *Recurrences) Insert(ctx context.Context, rec domain.Recurrence) (domain.Recurrence, error) {
	doc, err := newRecurrenceDoc(rec)
	if err != nil {
		return domain.Recurrence{}, err
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		return domain.Recurrence{}, fmt.Errorf("insert recurrence: %w", err)
	}
	return doc.toDomain(), nil
}

// ByID returns recurrence id of userID, or domain.ErrNotFound.
func (r *Recurrences) ByID(ctx context.Context, userID, id string) (domain.Recurrence, error) {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return domain.Recurrence{}, err
	}
	var doc recurrenceDoc
	if err := r.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		return domain.Recurrence{}, notFound(err)
	}
	return doc.toDomain(), nil
}

// Update stores the template, the rule and the next occurrence of rec; the
// kind, the last occurrence (Advance moves it), the source and the creation
// never change here.
func (r *Recurrences) Update(ctx context.Context, rec domain.Recurrence) error {
	filter, err := ownedBy(rec.UserID, rec.ID)
	if err != nil {
		return err
	}
	var doc recurrenceDoc
	if err := doc.setEditable(rec); err != nil {
		return err
	}
	set := bson.D{
		{Key: "account_id", Value: doc.AccountID},
		{Key: "currency", Value: doc.Currency},
		{Key: "amount", Value: doc.Amount},
		{Key: "description", Value: doc.Description},
		{Key: "tags", Value: doc.Tags},
		{Key: "frequency", Value: doc.Frequency},
		{Key: "interval", Value: doc.Interval},
		{Key: "start_date", Value: doc.StartDate},
		{Key: "updated_at", Value: rec.UpdatedAt.UTC()},
	}
	if rec.Kind == domain.TransactionTransfer {
		set = append(set,
			bson.E{Key: "to_account_id", Value: doc.ToAccountID},
			bson.E{Key: "to_currency", Value: doc.ToCurrency},
			bson.E{Key: "to_amount", Value: doc.ToAmount})
	} else {
		set = append(set, bson.E{Key: "category_id", Value: doc.CategoryID})
	}
	var unset bson.D
	for _, day := range []struct {
		key   string
		value *time.Time
	}{{"end_date", doc.EndDate}, {"next_date", doc.NextDate}} {
		if day.value == nil {
			unset = append(unset, bson.E{Key: day.key, Value: ""})
		} else {
			set = append(set, bson.E{Key: day.key, Value: *day.value})
		}
	}
	update := bson.D{{Key: "$set", Value: set}}
	if len(unset) > 0 {
		update = append(update, bson.E{Key: "$unset", Value: unset})
	}
	res, err := r.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update recurrence: %w", err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Advance records that the occurrences of recurrence id of userID were
// created up to last, the next one being next (zero once it ended), if its
// next occurrence is still from: a compare-and-set, so a reader or an edit
// that moved it first wins and Advance changes nothing.
func (r *Recurrences) Advance(ctx context.Context, userID, id string, from, next, last domain.Date) error {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return err
	}
	filter = append(filter, bson.E{Key: "next_date", Value: from.Time()})
	set := bson.D{{Key: "last_date", Value: last.Time()}}
	update := bson.D{{Key: "$set", Value: append(set, bson.E{Key: "next_date", Value: next.Time()})}}
	if next.IsZero() {
		update = bson.D{{Key: "$set", Value: set}, {Key: "$unset", Value: bson.D{{Key: "next_date", Value: ""}}}}
	}
	if _, err := r.coll.UpdateOne(ctx, filter, update); err != nil {
		return fmt.Errorf("advance recurrence: %w", err)
	}
	return nil
}

// Delete removes recurrence id of userID, or answers domain.ErrNotFound.
func (r *Recurrences) Delete(ctx context.Context, userID, id string) error {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return err
	}
	res, err := r.coll.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("delete recurrence: %w", err)
	}
	if res.DeletedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// AccountUsage reports whether a recurrence of userID names account id (as
// its account or as a transfer's destination), and whether one of those is
// still active.
func (r *Recurrences) AccountUsage(ctx context.Context, userID, id string) (used, active bool, err error) {
	uid, err := objectID(userID)
	if err != nil {
		return false, false, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	account, err := objectID(id)
	if err != nil {
		return false, false, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	// Active ones first: a missing next_date sorts last when descending.
	var doc struct {
		NextDate *time.Time `bson:"next_date"`
	}
	err = r.coll.FindOne(ctx,
		eitherLeg(uid, bson.D{{Key: "account_id", Value: account}}, bson.D{{Key: "to_account_id", Value: account}}, nil),
		options.FindOne().SetSort(bson.D{{Key: "next_date", Value: -1}}).SetProjection(bson.D{{Key: "next_date", Value: 1}}),
	).Decode(&doc)
	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		return false, false, nil
	case err != nil:
		return false, false, fmt.Errorf("find recurrence: %w", err)
	}
	return true, doc.NextDate != nil, nil
}

// CategoryInUse reports whether a recurrence of userID names category id.
func (r *Recurrences) CategoryInUse(ctx context.Context, userID, id string) (bool, error) {
	uid, err := objectID(userID)
	if err != nil {
		return false, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	category, err := objectID(id)
	if err != nil {
		return false, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	err = r.coll.FindOne(ctx, bson.D{{Key: "user_id", Value: uid}, {Key: "category_id", Value: category}},
		options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Err()
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, mongo.ErrNoDocuments):
		return false, nil
	default:
		return false, fmt.Errorf("find recurrence: %w", err)
	}
}

// ReassignCategory moves the recurrences of userID from category from to
// category to; the caller checked both are the owner's, of one kind.
func (r *Recurrences) ReassignCategory(ctx context.Context, userID, from, to string, at time.Time) error {
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
		return fmt.Errorf("reassign recurrences: %w", err)
	}
	return nil
}

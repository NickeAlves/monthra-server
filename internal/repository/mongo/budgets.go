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

// maxBudgetsListed bounds one listing, well above the budgets a month can
// hold (one per expense category, at most 200), so a bug can never load an
// unbounded result.
const maxBudgetsListed = 1000

// Budgets stores the monthly budgets of each user. Every query filters by
// user_id, so one user can never reach another's budget (IDOR). The unique
// index budgets_user_month_category_unique holds one budget per category
// and month, and serves every query by month.
type Budgets struct{ coll *mongo.Collection }

// NewBudgets returns the budgets repository on db.
func NewBudgets(db *mongo.Database) *Budgets {
	return &Budgets{coll: db.Collection(CollBudgets)}
}

// budgetDoc is one budget. warned_at and exceeded_at are absent until the
// alert they mark goes out (or needs none).
type budgetDoc struct {
	ID         bson.ObjectID `bson:"_id"`
	UserID     bson.ObjectID `bson:"user_id"`
	CategoryID bson.ObjectID `bson:"category_id"`
	Month      string        `bson:"month"`
	Currency   string        `bson:"currency"`
	Limit      int64         `bson:"limit"`
	WarnAt     int           `bson:"warn_at"`
	WarnedAt   *time.Time    `bson:"warned_at,omitempty"`
	ExceededAt *time.Time    `bson:"exceeded_at,omitempty"`
	CreatedAt  time.Time     `bson:"created_at"`
	UpdatedAt  time.Time     `bson:"updated_at"`
}

func newBudgetDoc(b domain.Budget) (budgetDoc, error) {
	uid, err := objectID(b.UserID)
	if err != nil {
		return budgetDoc{}, err
	}
	category, err := objectID(b.CategoryID)
	if err != nil {
		return budgetDoc{}, err
	}
	return budgetDoc{
		ID: bson.NewObjectID(), UserID: uid, CategoryID: category, Month: b.Month.String(),
		Currency: b.Currency, Limit: b.Limit, WarnAt: b.WarnAt,
		WarnedAt: utcOrNil(b.WarnedAt), ExceededAt: utcOrNil(b.ExceededAt),
		CreatedAt: b.CreatedAt.UTC(), UpdatedAt: b.UpdatedAt.UTC(),
	}, nil
}

func (d budgetDoc) toDomain() domain.Budget {
	return domain.Budget{
		ID: d.ID.Hex(), UserID: d.UserID.Hex(), CategoryID: d.CategoryID.Hex(), Month: storedMonth(d.Month),
		Currency: d.Currency, Limit: d.Limit, WarnAt: d.WarnAt, WarnedAt: d.WarnedAt, ExceededAt: d.ExceededAt,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

func utcOrNil(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// inMonth is the filter of the budgets of userID in month.
func inMonth(userID string, month domain.Month) (bson.D, bool) {
	uid, err := objectID(userID)
	if err != nil {
		return nil, false
	}
	return bson.D{{Key: "user_id", Value: uid}, {Key: "month", Value: month.String()}}, true
}

// List returns the budgets of userID in month, in the order they were
// created (a month carried over keeps the order of the one before).
func (r *Budgets) List(ctx context.Context, userID string, month domain.Month) ([]domain.Budget, error) {
	filter, ok := inMonth(userID, month)
	if !ok {
		return nil, nil // an id that cannot exist owns nothing
	}
	return r.find(ctx, filter)
}

// Pending returns the budgets of userID in month that may still give an
// alert: those not exceeded yet.
func (r *Budgets) Pending(ctx context.Context, userID string, month domain.Month) ([]domain.Budget, error) {
	filter, ok := inMonth(userID, month)
	if !ok {
		return nil, nil // an id that cannot exist owns nothing
	}
	filter = append(filter, bson.E{Key: "exceeded_at", Value: bson.D{{Key: "$exists", Value: false}}})
	return r.find(ctx, filter)
}

func (r *Budgets) find(ctx context.Context, filter bson.D) ([]domain.Budget, error) {
	opts := options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(maxBudgetsListed)
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("find budgets: %w", err)
	}
	var docs []budgetDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode budgets: %w", err)
	}
	out := make([]domain.Budget, len(docs))
	for i, d := range docs {
		out[i] = d.toDomain()
	}
	return out, nil
}

// ByID returns budget id of userID, or domain.ErrNotFound.
func (r *Budgets) ByID(ctx context.Context, userID, id string) (domain.Budget, error) {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return domain.Budget{}, err
	}
	var doc budgetDoc
	if err := r.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		return domain.Budget{}, notFound(err)
	}
	return doc.toDomain(), nil
}

// Insert stores b with a new id and returns it, or answers
// domain.ErrBudgetExists when its category already has a budget in the
// month.
func (r *Budgets) Insert(ctx context.Context, b domain.Budget) (domain.Budget, error) {
	doc, err := newBudgetDoc(b)
	if err != nil {
		return domain.Budget{}, err
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return domain.Budget{}, domain.ErrBudgetExists
		}
		return domain.Budget{}, fmt.Errorf("insert budget: %w", err)
	}
	return doc.toDomain(), nil
}

// InsertRolled stores the budgets a month carried over in one unordered
// write. The unique index rejects a category that already has a budget in
// the month, and that rejection is the expected outcome of a repeated or
// concurrent rollover; any other failure is returned.
func (r *Budgets) InsertRolled(ctx context.Context, list []domain.Budget) error {
	if len(list) == 0 {
		return nil
	}
	docs := make([]budgetDoc, len(list))
	for i, b := range list {
		doc, err := newBudgetDoc(b)
		if err != nil {
			return err
		}
		docs[i] = doc
	}
	_, err := r.coll.InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	if err == nil || onlyDuplicateKeys(err) {
		return nil
	}
	return fmt.Errorf("insert rolled budgets: %w", err)
}

// Update stores the editable fields of b (limit, share) and its alert
// marks; the category, the month and the currency never change.
func (r *Budgets) Update(ctx context.Context, b domain.Budget) error {
	filter, err := ownedBy(b.UserID, b.ID)
	if err != nil {
		return err
	}
	set := bson.D{
		{Key: "limit", Value: b.Limit},
		{Key: "warn_at", Value: b.WarnAt},
		{Key: "updated_at", Value: b.UpdatedAt.UTC()},
	}
	var unset bson.D
	for _, m := range []struct {
		field string
		at    *time.Time
	}{{"warned_at", b.WarnedAt}, {"exceeded_at", b.ExceededAt}} {
		if m.at != nil {
			set = append(set, bson.E{Key: m.field, Value: m.at.UTC()})
		} else {
			unset = append(unset, bson.E{Key: m.field, Value: ""})
		}
	}
	update := bson.D{{Key: "$set", Value: set}}
	if len(unset) > 0 {
		update = append(update, bson.E{Key: "$unset", Value: unset})
	}
	res, err := r.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update budget: %w", err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ClaimAlert marks the alert of status (warning or exceeded) on budget id
// of userID as given at at, and reports whether this call marked it: a
// compare-and-set on the mark being absent, so concurrent checks give each
// alert once. Exceeding marks the warning too, when unset.
func (r *Budgets) ClaimAlert(ctx context.Context, userID, id string, status domain.BudgetStatus, at time.Time) (bool, error) {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return false, nil //nolint:nilerr // an id that cannot exist has no alert to give
	}
	at = at.UTC()
	var update bson.D
	switch status {
	case domain.BudgetWarning:
		filter = append(filter, bson.E{Key: "warned_at", Value: bson.D{{Key: "$exists", Value: false}}})
		update = bson.D{{Key: "$set", Value: bson.D{{Key: "warned_at", Value: at}}}}
	case domain.BudgetExceeded:
		filter = append(filter, bson.E{Key: "exceeded_at", Value: bson.D{{Key: "$exists", Value: false}}})
		update = bson.D{
			{Key: "$set", Value: bson.D{{Key: "exceeded_at", Value: at}}},
			// $min sets a missing field and keeps an earlier warning.
			{Key: "$min", Value: bson.D{{Key: "warned_at", Value: at}}},
		}
	default:
		return false, fmt.Errorf("claim budget alert: no alert for status %q", status)
	}
	res, err := r.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, fmt.Errorf("claim budget alert: %w", err)
	}
	return res.ModifiedCount == 1, nil
}

// Delete removes budget id of userID, or answers domain.ErrNotFound.
func (r *Budgets) Delete(ctx context.Context, userID, id string) error {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return err
	}
	res, err := r.coll.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("delete budget: %w", err)
	}
	if res.DeletedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// DeleteByCategory removes the budgets of category categoryID of userID in
// every month. Deleting none is not an error, so a category deletion that
// was interrupted can run again.
func (r *Budgets) DeleteByCategory(ctx context.Context, userID, categoryID string) error {
	uid, err := objectID(userID)
	if err != nil {
		return nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	category, err := objectID(categoryID)
	if err != nil {
		return nil //nolint:nilerr // no budget names an id that cannot exist
	}
	if _, err := r.coll.DeleteMany(ctx, bson.D{{Key: "user_id", Value: uid}, {Key: "category_id", Value: category}}); err != nil {
		return fmt.Errorf("delete budgets of category: %w", err)
	}
	return nil
}

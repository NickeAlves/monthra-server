package mongo

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// maxMovementsRead bounds what one read of an investment's movements loads:
// twice domain.MaxMovementsPerInvestment, so the few a race lets past the
// cap still count, and a bug can never load an unbounded result.
const maxMovementsRead = 2 * domain.MaxMovementsPerInvestment

// InvestmentMovements stores the movements of each user's investments.
// Every query filters by user_id, so one user can never reach another's
// movement (IDOR), and by investment_id where it names one, so a movement
// is reached only through its own investment. Their indexes:
// investment_movements_user_investment_date_id (an investment's history,
// newest first), the partial investment_movements_user_income_date (the
// income of the dashboard) and investment_movements_user_category.
type InvestmentMovements struct{ coll *mongo.Collection }

// NewInvestmentMovements returns the movements repository on db.
func NewInvestmentMovements(db *mongo.Database) *InvestmentMovements {
	return &InvestmentMovements{coll: db.Collection(CollInvestmentMovements)}
}

// movementDoc is one movement. It holds only the fields its type takes on
// its mode (domain.ShapeOf); quantity and price are Decimal128.
type movementDoc struct {
	ID           bson.ObjectID    `bson:"_id"`
	UserID       bson.ObjectID    `bson:"user_id"`
	InvestmentID bson.ObjectID    `bson:"investment_id"`
	Type         string           `bson:"type"`
	Mode         string           `bson:"mode"`
	Currency     string           `bson:"currency"`
	Date         time.Time        `bson:"date"`
	Quantity     *bson.Decimal128 `bson:"quantity,omitempty"`
	Price        *bson.Decimal128 `bson:"price,omitempty"`
	Amount       *int64           `bson:"amount,omitempty"`
	Fees         *int64           `bson:"fees,omitempty"`
	CategoryID   *bson.ObjectID   `bson:"category_id,omitempty"`
	CreatedAt    time.Time        `bson:"created_at"`
	UpdatedAt    time.Time        `bson:"updated_at"`
}

// fixedDecimal writes a fixed-point number (domain.Quantity, domain.Price)
// as a Decimal128 with 8 decimal places.
func fixedDecimal(v int64) *bson.Decimal128 {
	d, _ := bson.ParseDecimal128FromBigInt(big.NewInt(v), -domain.FixedDecimals)
	return &d
}

// errStoredDecimal is a stored quantity or price that is not a fixed-point
// number of 8 decimal places, which only a hand-edited document holds.
var errStoredDecimal = errors.New("stored decimal out of the fixed-point scale")

// decimalFixed reads a Decimal128 back as a fixed-point number.
func decimalFixed(d *bson.Decimal128) (int64, error) {
	if d == nil {
		return 0, nil
	}
	n, exp, err := d.BigInt()
	if err != nil {
		return 0, fmt.Errorf("read decimal: %w", err)
	}
	shift := exp + domain.FixedDecimals
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(shift))), nil)
	switch {
	case shift > 0:
		n.Mul(n, scale)
	case shift < 0:
		var rem big.Int
		n.QuoRem(n, scale, &rem)
		if rem.Sign() != 0 {
			return 0, errStoredDecimal
		}
	}
	if !n.IsInt64() {
		return 0, errStoredDecimal
	}
	return n.Int64(), nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// setShape fills the fields m's shape takes.
func (d *movementDoc) setShape(m domain.Movement) error {
	shape := domain.ShapeOf(m.Type, m.Mode)
	d.Quantity, d.Price, d.Amount, d.Fees, d.CategoryID = nil, nil, nil, nil, nil
	if shape.Quantity {
		d.Quantity = fixedDecimal(int64(m.Quantity))
	}
	if shape.Price {
		d.Price = fixedDecimal(int64(m.Price))
	}
	if shape.Amount {
		amount := m.Amount
		d.Amount = &amount
	}
	if shape.Fees {
		fees := m.Fees
		d.Fees = &fees
	}
	if shape.Category {
		category, err := objectID(m.CategoryID)
		if err != nil {
			return err
		}
		d.CategoryID = &category
	}
	return nil
}

func newMovementDoc(m domain.Movement) (movementDoc, error) {
	uid, err := objectID(m.UserID)
	if err != nil {
		return movementDoc{}, err
	}
	inv, err := objectID(m.InvestmentID)
	if err != nil {
		return movementDoc{}, err
	}
	doc := movementDoc{
		ID: bson.NewObjectID(), UserID: uid, InvestmentID: inv, Type: string(m.Type), Mode: string(m.Mode),
		Currency: m.Currency, Date: m.Date.Time(), CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.UpdatedAt.UTC(),
	}
	if err := doc.setShape(m); err != nil {
		return movementDoc{}, err
	}
	return doc, nil
}

func (d movementDoc) toDomain() (domain.Movement, error) {
	q, err := decimalFixed(d.Quantity)
	if err != nil {
		return domain.Movement{}, fmt.Errorf("movement %s quantity: %w", d.ID.Hex(), err)
	}
	p, err := decimalFixed(d.Price)
	if err != nil {
		return domain.Movement{}, fmt.Errorf("movement %s price: %w", d.ID.Hex(), err)
	}
	m := domain.Movement{
		ID: d.ID.Hex(), UserID: d.UserID.Hex(), InvestmentID: d.InvestmentID.Hex(),
		Type: domain.MovementType(d.Type), Mode: domain.InvestmentMode(d.Mode), Currency: d.Currency,
		Date: domain.DateOf(d.Date), Quantity: domain.Quantity(q), Price: domain.Price(p),
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
	if d.Amount != nil {
		m.Amount = *d.Amount
	}
	if d.Fees != nil {
		m.Fees = *d.Fees
	}
	if d.CategoryID != nil {
		m.CategoryID = d.CategoryID.Hex()
	}
	return m, nil
}

// ofInvestment is the filter of the movements of investment invID of
// userID.
func ofInvestment(userID, invID string) (bson.D, error) {
	uid, err := objectID(userID)
	if err != nil {
		return nil, err
	}
	inv, err := objectID(invID)
	if err != nil {
		return nil, err
	}
	return bson.D{{Key: "user_id", Value: uid}, {Key: "investment_id", Value: inv}}, nil
}

// find decodes the movements filter matches, in the order of opts.
func (r *InvestmentMovements) find(ctx context.Context, filter bson.D, opts *options.FindOptionsBuilder) ([]domain.Movement, error) {
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("find movements: %w", err)
	}
	var docs []movementDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode movements: %w", err)
	}
	out := make([]domain.Movement, len(docs))
	for i, d := range docs {
		if out[i], err = d.toDomain(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Of returns every movement of investment invID of userID, newest first
// (domain.BuildPosition sorts them its own way).
func (r *InvestmentMovements) Of(ctx context.Context, userID, invID string) ([]domain.Movement, error) {
	filter, err := ofInvestment(userID, invID)
	if err != nil {
		return []domain.Movement{}, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	opts := options.Find().SetSort(bson.D{{Key: "date", Value: -1}, {Key: "_id", Value: -1}}).SetLimit(maxMovementsRead)
	return r.find(ctx, filter, opts)
}

// All returns every movement of userID by investment id.
func (r *InvestmentMovements) All(ctx context.Context, userID string) (map[string][]domain.Movement, error) {
	out := map[string][]domain.Movement{}
	uid, err := objectID(userID)
	if err != nil {
		return out, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	opts := options.Find().SetLimit(int64(domain.MaxInvestments) * maxMovementsRead)
	list, err := r.find(ctx, bson.D{{Key: "user_id", Value: uid}}, opts)
	if err != nil {
		return nil, err
	}
	for _, m := range list {
		out[m.InvestmentID] = append(out[m.InvestmentID], m)
	}
	return out, nil
}

// Page returns up to limit movements of investment invID of userID that
// match f, newest date first and then newest id, starting after the cursor
// when there is one. An id in the cursor that cannot exist matches nothing.
func (r *InvestmentMovements) Page(ctx context.Context, userID, invID string, f domain.MovementFilter, after *domain.MovementCursor, limit int) ([]domain.Movement, error) {
	filter, err := ofInvestment(userID, invID)
	if err != nil {
		return []domain.Movement{}, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	if f.Type != "" {
		filter = append(filter, bson.E{Key: "type", Value: string(f.Type)})
	}
	if after != nil {
		last, err := objectID(after.ID)
		if err != nil {
			return []domain.Movement{}, nil //nolint:nilerr // a cursor that cannot exist ends nothing
		}
		// The days up to the cursor's (the index bound), and on its day the
		// ids before its.
		filter = append(filter,
			bson.E{Key: "date", Value: bson.D{{Key: "$lte", Value: after.Date.Time()}}},
			bson.E{Key: "$or", Value: bson.A{
				bson.D{{Key: "date", Value: bson.D{{Key: "$lt", Value: after.Date.Time()}}}},
				bson.D{{Key: "_id", Value: bson.D{{Key: "$lt", Value: last}}}},
			}})
	}
	opts := options.Find().SetSort(bson.D{{Key: "date", Value: -1}, {Key: "_id", Value: -1}}).SetLimit(int64(limit))
	return r.find(ctx, filter, opts)
}

// ByID returns movement id of investment invID of userID, or
// domain.ErrNotFound.
func (r *InvestmentMovements) ByID(ctx context.Context, userID, invID, id string) (domain.Movement, error) {
	filter, err := r.owned(userID, invID, id)
	if err != nil {
		return domain.Movement{}, err
	}
	var doc movementDoc
	if err := r.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		return domain.Movement{}, notFound(err)
	}
	return doc.toDomain()
}

// owned is the filter of movement id of investment invID of userID.
func (r *InvestmentMovements) owned(userID, invID, id string) (bson.D, error) {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return nil, err
	}
	inv, err := objectID(invID)
	if err != nil {
		return nil, err
	}
	return append(filter, bson.E{Key: "investment_id", Value: inv}), nil
}

// Count returns how many movements investment invID of userID has.
func (r *InvestmentMovements) Count(ctx context.Context, userID, invID string) (int, error) {
	filter, err := ofInvestment(userID, invID)
	if err != nil {
		return 0, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	n, err := r.coll.CountDocuments(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("count movements: %w", err)
	}
	return int(n), nil
}

// Insert stores m with a new id and returns it.
func (r *InvestmentMovements) Insert(ctx context.Context, m domain.Movement) (domain.Movement, error) {
	doc, err := newMovementDoc(m)
	if err != nil {
		return domain.Movement{}, err
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		return domain.Movement{}, fmt.Errorf("insert movement: %w", err)
	}
	return doc.toDomain()
}

// Update stores the editable fields of m (its day and the fields of its
// shape); the type, the mode and the currency never change.
func (r *InvestmentMovements) Update(ctx context.Context, m domain.Movement) error {
	filter, err := r.owned(m.UserID, m.InvestmentID, m.ID)
	if err != nil {
		return err
	}
	var doc movementDoc
	if err := doc.setShape(m); err != nil {
		return err
	}
	set := bson.D{{Key: "date", Value: m.Date.Time()}, {Key: "updated_at", Value: m.UpdatedAt.UTC()}}
	for _, f := range []struct {
		key   string
		value any
		ok    bool
	}{
		{"quantity", doc.Quantity, doc.Quantity != nil},
		{"price", doc.Price, doc.Price != nil},
		{"amount", doc.Amount, doc.Amount != nil},
		{"fees", doc.Fees, doc.Fees != nil},
		{"category_id", doc.CategoryID, doc.CategoryID != nil},
	} {
		if f.ok {
			set = append(set, bson.E{Key: f.key, Value: f.value})
		}
	}
	res, err := r.coll.UpdateOne(ctx, filter, bson.D{{Key: "$set", Value: set}})
	if err != nil {
		return fmt.Errorf("update movement: %w", err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Delete removes movement id of investment invID of userID, or answers
// domain.ErrNotFound.
func (r *InvestmentMovements) Delete(ctx context.Context, userID, invID, id string) error {
	filter, err := r.owned(userID, invID, id)
	if err != nil {
		return err
	}
	res, err := r.coll.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("delete movement: %w", err)
	}
	if res.DeletedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// DeleteOf removes every movement of investment invID of userID.
func (r *InvestmentMovements) DeleteOf(ctx context.Context, userID, invID string) error {
	filter, err := ofInvestment(userID, invID)
	if err != nil {
		return nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	if _, err := r.coll.DeleteMany(ctx, filter); err != nil {
		return fmt.Errorf("delete movements: %w", err)
	}
	return nil
}

// incomeFlowDoc is one group of IncomeFlows.
type incomeFlowDoc struct {
	ID struct {
		Month    string        `bson:"month"`
		Currency string        `bson:"currency"`
		Category bson.ObjectID `bson:"category"`
	} `bson:"_id"`
	Amount int64 `bson:"amount"`
	Count  int   `bson:"count"`
}

// IncomeFlows sums what the investments of userID paid from day from to day
// to (both included) by month, currency and category, as income flows of
// the dashboard (domain.FlowSum of kind income). It reads the income of
// those days only (investment_movements_user_income_date).
func (r *InvestmentMovements) IncomeFlows(ctx context.Context, userID string, from, to domain.Date) ([]domain.FlowSum, error) {
	uid, err := objectID(userID)
	if err != nil {
		return []domain.FlowSum{}, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{
			{Key: "user_id", Value: uid},
			{Key: "type", Value: string(domain.MovementIncome)},
			{Key: "date", Value: bson.D{{Key: "$gte", Value: from.Time()}, {Key: "$lte", Value: to.Time()}}},
		}}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: "month", Value: monthOfDate},
				{Key: "currency", Value: "$currency"},
				{Key: "category", Value: "$category_id"},
			}},
			{Key: "amount", Value: bson.D{{Key: "$sum", Value: "$amount"}}},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	}
	cur, err := r.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("sum investment income: %w", err)
	}
	var docs []incomeFlowDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode investment income: %w", err)
	}
	out := make([]domain.FlowSum, len(docs))
	for i, d := range docs {
		out[i] = domain.FlowSum{
			Month: storedMonth(d.ID.Month), Currency: d.ID.Currency, Kind: domain.TransactionIncome,
			CategoryID: d.ID.Category.Hex(), Amount: d.Amount, Count: d.Count,
		}
	}
	return out, nil
}

// CategoryInUse reports whether an income movement of userID counts under
// category id.
func (r *InvestmentMovements) CategoryInUse(ctx context.Context, userID, id string) (bool, error) {
	uid, err := objectID(userID)
	if err != nil {
		return false, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	category, err := objectID(id)
	if err != nil {
		return false, nil //nolint:nilerr // an id that cannot exist names nothing
	}
	opts := options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})
	err = r.coll.FindOne(ctx, bson.D{{Key: "user_id", Value: uid}, {Key: "category_id", Value: category}}, opts).Err()
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, mongo.ErrNoDocuments):
		return false, nil
	default:
		return false, fmt.Errorf("find movement: %w", err)
	}
}

// ReassignCategory moves the income movements of userID from category from
// to category to; the caller checked both are the owner's income
// categories.
func (r *InvestmentMovements) ReassignCategory(ctx context.Context, userID, from, to string, at time.Time) error {
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
		return fmt.Errorf("reassign movements: %w", err)
	}
	return nil
}

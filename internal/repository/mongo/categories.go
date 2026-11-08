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

// maxCategoriesListed bounds one listing, well above the per-user cap of
// the use case, so a bug can never load an unbounded result.
const maxCategoriesListed = 1000

// duplicateKeyCode is the server error of a write that breaks a unique index.
const duplicateKeyCode = 11000

// Categories stores the categories of each user. Every query filters by
// user_id, so one user can never reach another's category (IDOR).
type Categories struct{ coll *mongo.Collection }

// NewCategories returns the categories repository on db.
func NewCategories(db *mongo.Database) *Categories {
	return &Categories{coll: db.Collection(CollCategories)}
}

// categoryDoc is one category. default_key is absent on the categories the
// owner created: the unique index on {user_id, default_key} is partial on
// it, so it dedupes the defaults only.
type categoryDoc struct {
	ID         bson.ObjectID `bson:"_id"`
	UserID     bson.ObjectID `bson:"user_id"`
	Name       string        `bson:"name"`
	DefaultKey string        `bson:"default_key,omitempty"`
	Kind       string        `bson:"kind"`
	Icon       string        `bson:"icon"`
	Color      string        `bson:"color"`
	CreatedAt  time.Time     `bson:"created_at"`
	UpdatedAt  time.Time     `bson:"updated_at"`
}

func newCategoryDoc(c domain.Category) (categoryDoc, error) {
	uid, err := objectID(c.UserID)
	if err != nil {
		return categoryDoc{}, err
	}
	return categoryDoc{
		ID: bson.NewObjectID(), UserID: uid, Name: c.Name, DefaultKey: c.DefaultKey,
		Kind: string(c.Kind), Icon: c.Icon, Color: c.Color,
		CreatedAt: c.CreatedAt.UTC(), UpdatedAt: c.UpdatedAt.UTC(),
	}, nil
}

func (d categoryDoc) toDomain() domain.Category {
	return domain.Category{
		ID: d.ID.Hex(), UserID: d.UserID.Hex(), Name: d.Name, DefaultKey: d.DefaultKey,
		Kind: domain.CategoryKind(d.Kind), Icon: d.Icon, Color: d.Color,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

// List returns the categories of userID, oldest first (_id breaks the ties
// of a seeded set, in its insertion order); only those of kind, unless kind
// is empty.
func (r *Categories) List(ctx context.Context, userID string, kind domain.CategoryKind) ([]domain.Category, error) {
	uid, err := objectID(userID)
	if err != nil {
		return nil, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	filter := bson.D{{Key: "user_id", Value: uid}}
	if kind != "" {
		filter = append(filter, bson.E{Key: "kind", Value: string(kind)})
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}).
		SetLimit(maxCategoriesListed)
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("find categories: %w", err)
	}
	var docs []categoryDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode categories: %w", err)
	}
	out := make([]domain.Category, len(docs))
	for i, d := range docs {
		out[i] = d.toDomain()
	}
	return out, nil
}

// Count returns how many categories userID has.
func (r *Categories) Count(ctx context.Context, userID string) (int, error) {
	uid, err := objectID(userID)
	if err != nil {
		return 0, nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	n, err := r.coll.CountDocuments(ctx, bson.D{{Key: "user_id", Value: uid}})
	if err != nil {
		return 0, fmt.Errorf("count categories: %w", err)
	}
	return int(n), nil
}

// Insert stores c with a new id and returns it.
func (r *Categories) Insert(ctx context.Context, c domain.Category) (domain.Category, error) {
	doc, err := newCategoryDoc(c)
	if err != nil {
		return domain.Category{}, err
	}
	if _, err := r.coll.InsertOne(ctx, doc); err != nil {
		return domain.Category{}, fmt.Errorf("insert category: %w", err)
	}
	return doc.toDomain(), nil
}

// InsertDefaults stores the default categories of list in one unordered
// write. The unique index on {user_id, default_key} rejects a default the
// owner already holds, and that rejection is the expected outcome of a
// repeated or concurrent seeding; any other failure is returned.
func (r *Categories) InsertDefaults(ctx context.Context, list []domain.Category) error {
	if len(list) == 0 {
		return nil
	}
	docs := make([]categoryDoc, len(list))
	for i, c := range list {
		if c.DefaultKey == "" {
			return errors.New("insert default categories: a category without a default key")
		}
		doc, err := newCategoryDoc(c)
		if err != nil {
			return err
		}
		docs[i] = doc
	}
	_, err := r.coll.InsertMany(ctx, docs, options.InsertMany().SetOrdered(false))
	if err == nil || onlyDuplicateKeys(err) {
		return nil
	}
	return fmt.Errorf("insert default categories: %w", err)
}

// onlyDuplicateKeys reports whether err is a bulk write whose every failure
// is a duplicate key, with no write concern error.
func onlyDuplicateKeys(err error) bool {
	var bulk mongo.BulkWriteException
	if !errors.As(err, &bulk) || bulk.WriteConcernError != nil || len(bulk.WriteErrors) == 0 {
		return false
	}
	for _, we := range bulk.WriteErrors {
		if we.Code != duplicateKeyCode {
			return false
		}
	}
	return true
}

// ByID returns category id of userID, or domain.ErrNotFound.
func (r *Categories) ByID(ctx context.Context, userID, id string) (domain.Category, error) {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return domain.Category{}, err
	}
	var doc categoryDoc
	if err := r.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		return domain.Category{}, notFound(err)
	}
	return doc.toDomain(), nil
}

// Update stores the editable fields of c (name, icon, color); the kind, the
// default key and the creation never change.
func (r *Categories) Update(ctx context.Context, c domain.Category) error {
	filter, err := ownedBy(c.UserID, c.ID)
	if err != nil {
		return err
	}
	res, err := r.coll.UpdateOne(ctx, filter, bson.D{{Key: "$set", Value: bson.D{
		{Key: "name", Value: c.Name},
		{Key: "icon", Value: c.Icon},
		{Key: "color", Value: c.Color},
		{Key: "updated_at", Value: c.UpdatedAt.UTC()},
	}}})
	if err != nil {
		return fmt.Errorf("update category: %w", err)
	}
	if res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Delete removes category id of userID, or answers domain.ErrNotFound.
func (r *Categories) Delete(ctx context.Context, userID, id string) error {
	filter, err := ownedBy(userID, id)
	if err != nil {
		return err
	}
	res, err := r.coll.DeleteOne(ctx, filter)
	if err != nil {
		return fmt.Errorf("delete category: %w", err)
	}
	if res.DeletedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

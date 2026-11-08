package mongo

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// UserEraser deletes every document of one collection that belongs to a
// user (by user_id), for account deletion. Running it twice is harmless.
type UserEraser struct{ coll *mongo.Collection }

// NewUserEraser returns the eraser of collection coll on db.
func NewUserEraser(db *mongo.Database, coll string) UserEraser {
	return UserEraser{coll: db.Collection(coll)}
}

// EraseUserData deletes the documents of userID.
func (e UserEraser) EraseUserData(ctx context.Context, userID string) error {
	uid, err := objectID(userID)
	if err != nil {
		return nil //nolint:nilerr // an id that cannot exist owns nothing
	}
	if _, err := e.coll.DeleteMany(ctx, bson.D{{Key: "user_id", Value: uid}}); err != nil {
		return fmt.Errorf("erase %s: %w", e.coll.Name(), err)
	}
	return nil
}

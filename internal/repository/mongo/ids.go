package mongo

import (
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// objectID parses a domain id. An id that is not an ObjectID cannot exist,
// so it maps to domain.ErrNotFound.
func objectID(id string) (bson.ObjectID, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return bson.ObjectID{}, domain.ErrNotFound
	}
	return oid, nil
}

// notFound maps the driver's no-document error to domain.ErrNotFound.
func notFound(err error) error {
	if errors.Is(err, mongo.ErrNoDocuments) {
		return domain.ErrNotFound
	}
	return err
}

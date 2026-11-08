package mongo

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func bulkError(codes ...int) mongo.BulkWriteException {
	out := mongo.BulkWriteException{}
	for i, c := range codes {
		out.WriteErrors = append(out.WriteErrors, mongo.BulkWriteError{WriteError: mongo.WriteError{Index: i, Code: c}})
	}
	return out
}

func TestOnlyDuplicateKeys(t *testing.T) {
	assert.True(t, onlyDuplicateKeys(bulkError(duplicateKeyCode)))
	assert.True(t, onlyDuplicateKeys(bulkError(duplicateKeyCode, duplicateKeyCode)))
	assert.True(t, onlyDuplicateKeys(fmt.Errorf("wrapped: %w", bulkError(duplicateKeyCode))))

	// Seeding must never hide a real failure behind the expected duplicates.
	assert.False(t, onlyDuplicateKeys(bulkError(duplicateKeyCode, 121)), "a document failed validation too")
	assert.False(t, onlyDuplicateKeys(bulkError()), "no write error at all")
	concern := bulkError(duplicateKeyCode)
	concern.WriteConcernError = &mongo.WriteConcernError{Code: 64}
	assert.False(t, onlyDuplicateKeys(concern), "the write concern failed")
	assert.False(t, onlyDuplicateKeys(errors.New("connection reset")))
	assert.False(t, onlyDuplicateKeys(nil))
}

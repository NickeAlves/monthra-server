package mongo

import (
	"context"
	"fmt"
	"iter"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// stream finds the documents of coll that filter matches, in the order of
// opts, when it is ranged over, and yields each one decoded as D and mapped
// by conv, so a read of any size holds one document at a time. A failed
// read yields the error and ends; the cursor is closed either way.
func stream[D, T any](ctx context.Context, coll *mongo.Collection, filter bson.D, opts *options.FindOptionsBuilder, conv func(D) (T, error)) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		cur, err := coll.Find(ctx, filter, opts)
		if err != nil {
			yield(zero, fmt.Errorf("find %s: %w", coll.Name(), err))
			return
		}
		defer func() { _ = cur.Close(context.WithoutCancel(ctx)) }()
		for cur.Next(ctx) {
			var d D
			if err := cur.Decode(&d); err != nil {
				yield(zero, fmt.Errorf("decode %s: %w", coll.Name(), err))
				return
			}
			t, err := conv(d)
			if err != nil {
				yield(zero, err)
				return
			}
			if !yield(t, nil) {
				return
			}
		}
		if err := cur.Err(); err != nil {
			yield(zero, fmt.Errorf("read %s: %w", coll.Name(), err))
		}
	}
}

// none yields nothing: what an id that cannot exist owns.
func none[T any]() iter.Seq2[T, error] {
	return func(func(T, error) bool) {}
}

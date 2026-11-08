// Package accounts implements the money accounts of a user (§1 item 4):
// bank accounts, savings, credit cards, cash and brokerages. Every method
// takes the caller's user id from the access token, never from the body.
package accounts

import (
	"context"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// Store keeps the accounts. Every method is scoped to one user: a record
// of another user answers domain.ErrNotFound, like one that does not exist.
type Store interface {
	List(ctx context.Context, userID string, includeArchived bool) ([]domain.Account, error)
	Count(ctx context.Context, userID string) (int, error)
	Insert(ctx context.Context, a domain.Account) (domain.Account, error)
	ByID(ctx context.Context, userID, id string) (domain.Account, error)
	// Update stores the editable fields of a, matching a.ID and a.UserID.
	Update(ctx context.Context, a domain.Account) error
	Delete(ctx context.Context, userID, id string) error
}

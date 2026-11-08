package dto

import (
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
)

// CardSettings are the statement rules of a credit card account. Amounts
// are integers in minor units (cents) of the account currency.
type CardSettings struct {
	ClosingDay int    `json:"closingDay"`
	DueDay     int    `json:"dueDay"`
	Limit      *int64 `json:"limit"`
}

func (c *CardSettings) toDomain() *domain.CardSettings {
	if c == nil {
		return nil
	}
	return &domain.CardSettings{ClosingDay: c.ClosingDay, DueDay: c.DueDay, Limit: c.Limit}
}

// CreateAccountRequest is the body of POST /accounts. The domain checks
// every field, so missing ones are reported as required there.
type CreateAccountRequest struct {
	Name           string        `json:"name"`
	Type           string        `json:"type"`
	Currency       string        `json:"currency"`
	InitialBalance int64         `json:"initialBalance"`
	Card           *CardSettings `json:"card"`
}

// Input maps the request to the domain input.
func (r CreateAccountRequest) Input() domain.AccountInput {
	return domain.AccountInput{
		Name: r.Name, Type: r.Type, Currency: r.Currency, InitialBalance: r.InitialBalance, Card: r.Card.toDomain(),
	}
}

// UpdateAccountRequest is the body of PATCH /accounts/{id}; absent fields
// stay as they are. Type and currency are not fields: they never change.
type UpdateAccountRequest struct {
	Name           *string       `json:"name"`
	InitialBalance *int64        `json:"initialBalance"`
	Card           *CardSettings `json:"card"`
	Archived       *bool         `json:"archived"`
}

// Patch maps the request to the domain patch.
func (r UpdateAccountRequest) Patch() domain.AccountPatch {
	return domain.AccountPatch{Name: r.Name, InitialBalance: r.InitialBalance, Card: r.Card.toDomain(), Archived: r.Archived}
}

// Account is the public view of an account.
type Account struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Type           string        `json:"type"`
	Currency       string        `json:"currency"`
	InitialBalance int64         `json:"initialBalance"`
	Card           *CardSettings `json:"card"`
	Source         string        `json:"source"`
	Archived       bool          `json:"archived"`
	CreatedAt      time.Time     `json:"createdAt"`
	UpdatedAt      time.Time     `json:"updatedAt"`
}

// NewAccount maps an account to its public view; the owner id stays out.
func NewAccount(a domain.Account) Account {
	out := Account{
		ID: a.ID, Name: a.Name, Type: string(a.Type), Currency: a.Currency, InitialBalance: a.InitialBalance,
		Source: string(a.Source), Archived: a.Archived(), CreatedAt: a.CreatedAt.UTC(), UpdatedAt: a.UpdatedAt.UTC(),
	}
	if a.Card != nil {
		out.Card = &CardSettings{ClosingDay: a.Card.ClosingDay, DueDay: a.Card.DueDay, Limit: a.Card.Limit}
	}
	return out
}

// AccountsResponse is the body of GET /accounts.
type AccountsResponse struct {
	Accounts []Account `json:"accounts"`
}

// NewAccounts maps a list of accounts; an empty list is [], never null.
func NewAccounts(list []domain.Account) AccountsResponse {
	out := AccountsResponse{Accounts: make([]Account, len(list))}
	for i, a := range list {
		out.Accounts[i] = NewAccount(a)
	}
	return out
}

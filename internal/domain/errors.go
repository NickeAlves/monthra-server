// Package domain holds the entities and the rules that need no I/O.
package domain

import (
	"errors"
	"strings"
)

// Errors that repositories return and use cases switch on.
var (
	// ErrNotFound means no record matched.
	ErrNotFound = errors.New("not found")
	// ErrEmailTaken means another user already has the email address.
	ErrEmailTaken = errors.New("email already registered")
	// ErrLimitReached means the user already has as many records of a kind
	// as one account may hold.
	ErrLimitReached = errors.New("limit reached")
	// ErrAccountInUse means an account still has transactions, so it can
	// only be archived.
	ErrAccountInUse = errors.New("account in use")
	// ErrCategoryInUse means a category still has transactions; they must
	// move to another category first.
	ErrCategoryInUse = errors.New("category in use")
	// ErrUsedByRecurrence means a recurrence names an account or a
	// category: it cannot be deleted (or an account archived, while the
	// recurrence is active) until the recurrence ends or moves.
	ErrUsedByRecurrence = errors.New("used by a recurrence")
	// ErrBudgetExists means the category already has a budget in the month.
	ErrBudgetExists = errors.New("budget exists")
	// ErrExceedsPosition means the movements of an investment would sell
	// more units than it held on the day of a sale (or pass the bounds of
	// a position).
	ErrExceedsPosition = errors.New("exceeds position")
)

// Field error codes, stable for clients to switch on.
const (
	CodeRequired = "required"
	CodeInvalid  = "invalid"
	CodeTooShort = "too_short"
	CodeTooLong  = "too_long"
	CodeBreached = "breached"
	// CodeOutOfRange is a number outside its bounds (an amount, a day).
	CodeOutOfRange = "out_of_range"
	// CodeArchived is a reference to an archived account, which takes no
	// new transactions.
	CodeArchived = "archived"
	// CodeKindMismatch is a reference to a category of the other kind (an
	// income category on an expense).
	CodeKindMismatch = "kind_mismatch"
	// CodeCurrencyMismatch is a reference to an account in another currency
	// (an investment kept at an account).
	CodeCurrencyMismatch = "currency_mismatch"
	// CodeExceedsPosition is a sale of more units than held on its day.
	CodeExceedsPosition = "exceeds_position"
)

// FieldError is one invalid input field.
type FieldError struct {
	Field string
	Code  string
}

// ValidationError lists every invalid field of one input.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		parts[i] = f.Field + ": " + f.Code
	}
	return "invalid input (" + strings.Join(parts, ", ") + ")"
}

// Validation collects field errors.
type Validation struct{ fields []FieldError }

// Add records an invalid field.
func (v *Validation) Add(field, code string) {
	v.fields = append(v.fields, FieldError{Field: field, Code: code})
}

// Check records err under field. Errors from the validators of this package
// keep their code; any other error is recorded as invalid.
func (v *Validation) Check(field string, err error) {
	if err == nil {
		return
	}
	var fe *fieldErr
	if errors.As(err, &fe) {
		v.Add(field, fe.code)
		return
	}
	v.Add(field, CodeInvalid)
}

// Err returns a *ValidationError, or nil when every field was valid.
func (v *Validation) Err() error {
	if len(v.fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: v.fields}
}

// fieldErr is what the single-field validators return; the field name is
// given by the caller, since the same rule serves several inputs.
type fieldErr struct{ code string }

func (e *fieldErr) Error() string { return e.code }

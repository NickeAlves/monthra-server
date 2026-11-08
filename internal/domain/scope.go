package domain

// EditScope is how far an edit or a deletion of one transaction of a series
// reaches: the installments of a purchase, and later the occurrences of a
// recurrence. On a transaction outside a series every scope reaches that
// transaction alone.
type EditScope string

// Edit scopes.
const (
	// ScopeThis reaches the addressed transaction only.
	ScopeThis EditScope = "this"
	// ScopeFuture reaches the addressed transaction and the ones after it in
	// its series.
	ScopeFuture EditScope = "future"
	// ScopeAll reaches the whole series.
	ScopeAll EditScope = "all"
)

// ParseEditScope reads a scope; an empty one is ScopeThis.
func ParseEditScope(s string) (EditScope, error) {
	switch scope := EditScope(s); scope {
	case "":
		return ScopeThis, nil
	case ScopeThis, ScopeFuture, ScopeAll:
		return scope, nil
	default:
		return "", &fieldErr{CodeInvalid}
	}
}

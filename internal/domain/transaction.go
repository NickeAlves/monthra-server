package domain

import (
	"slices"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Limits of the transaction fields.
const (
	MaxTransactionDescriptionLength = 200
	MaxTransactionTags              = 10
	MaxTagLength                    = 30
	MaxSearchLength                 = 100
)

// TransactionKind tells whether money left an account, entered it, or moved
// between two of the owner's accounts.
type TransactionKind string

// Supported transaction kinds.
const (
	TransactionExpense  TransactionKind = "expense"
	TransactionIncome   TransactionKind = "income"
	TransactionTransfer TransactionKind = "transfer"
)

// TransactionKinds lists the supported transaction kinds.
var TransactionKinds = []TransactionKind{TransactionExpense, TransactionIncome, TransactionTransfer}

// ParseTransactionKind returns the transaction kind named s, if it is
// supported.
func ParseTransactionKind(s string) (TransactionKind, bool) {
	for _, k := range TransactionKinds {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}

// categoryKind is the kind of category a transaction of kind k takes; a
// transfer takes none.
func (k TransactionKind) categoryKind() (CategoryKind, bool) {
	switch k {
	case TransactionExpense:
		return CategoryExpense, true
	case TransactionIncome:
		return CategoryIncome, true
	default:
		return "", false
	}
}

// Transaction is one expense, income or transfer of the owner. Amounts are
// positive minor units: the kind tells the direction.
type Transaction struct {
	ID     string
	UserID string
	// Kind never changes: an expense and a transfer have different shapes.
	Kind TransactionKind
	// AccountID is where an expense left from or an income arrived to; for a
	// transfer, the account the money left.
	AccountID string
	// Currency is the currency of AccountID, copied because an account never
	// changes currency, so totals by currency need no lookup.
	Currency string
	// CategoryID is set on expenses and income (a category of the same kind)
	// and never on transfers.
	CategoryID string
	Amount     int64
	// ToAccountID, ToCurrency and ToAmount are set on transfers only: where
	// the money arrived, and how much in that account's currency (equal to
	// Amount unless the currencies differ).
	ToAccountID string
	ToCurrency  string
	ToAmount    int64
	Date        Date
	Description string
	// Tags are lower case, unique, in the order the owner gave them.
	Tags      []string
	Source    Source
	CreatedAt time.Time
	UpdatedAt time.Time
}

// IsTransfer reports whether t moves money between two accounts.
func (t Transaction) IsTransfer() bool { return t.Kind == TransactionTransfer }

// TransactionInput is a new transaction as the owner describes it.
type TransactionInput struct {
	Kind        string
	AccountID   string
	CategoryID  string
	ToAccountID string
	Amount      int64
	// ToAmount is required on a transfer between two currencies, and
	// optional (equal to Amount) between accounts of one currency.
	ToAmount    *int64
	Date        string
	Description string
	Tags        []string
}

// TransactionRefs are the records a transaction names, as the caller's
// stores found them: nil when the id names none of the owner's records.
type TransactionRefs struct {
	Account   *Account
	ToAccount *Account
	Category  *Category
}

// NewTransaction validates in against the records it names and returns the
// manual transaction of userID, or a *ValidationError naming every invalid
// field. A reference to a record the owner does not hold is invalid, the
// same as an id that does not exist, so the answer reveals nothing; an
// archived account takes no new transactions.
func NewTransaction(userID string, in TransactionInput, refs TransactionRefs, now time.Time) (Transaction, error) {
	var v Validation
	kind, ok := ParseTransactionKind(in.Kind)
	switch {
	case in.Kind == "":
		v.Add("kind", CodeRequired)
	case !ok:
		v.Add("kind", CodeInvalid)
	}

	t := Transaction{UserID: userID, Kind: kind, Amount: in.Amount, Source: SourceManual}
	v.Check("amount", checkPositiveAmount(in.Amount))
	var err error
	t.Date, err = ParseDate(in.Date)
	v.Check("date", err)
	t.Description, err = normalizeOptionalText(in.Description, MaxTransactionDescriptionLength)
	v.Check("description", err)
	t.Tags, err = normalizeTags(in.Tags)
	v.Check("tags", err)

	if kind != "" {
		t.AccountID = in.AccountID
		refsOK := true
		if t.Currency, ok = accountRef(&v, "accountId", in.AccountID, refs.Account); !ok {
			refsOK = false
		}
		t.CategoryID = in.CategoryID
		categoryRef(&v, kind, in.CategoryID, refs.Category)
		if kind == TransactionTransfer {
			t.ToAccountID = in.ToAccountID
			if t.ToCurrency, ok = accountRef(&v, "toAccountId", in.ToAccountID, refs.ToAccount); !ok {
				refsOK = false
			}
			if refsOK && t.ToAccountID == t.AccountID {
				v.Add("toAccountId", CodeInvalid)
				refsOK = false
			}
			if refsOK {
				transferAmount(&v, &t, in.ToAmount, nil)
			}
		} else {
			noTransferFields(&v, in.ToAccountID != "", in.ToAmount != nil)
		}
	}
	if err := v.Err(); err != nil {
		return Transaction{}, err
	}

	now = now.UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	return t, nil
}

// TransactionPatch changes some fields of a transaction; nil fields stay as
// they are. The kind is not a field: it never changes. An empty Description
// clears it, an empty Tags clears them; Tags replaces the whole list.
type TransactionPatch struct {
	AccountID   *string
	CategoryID  *string
	ToAccountID *string
	Amount      *int64
	ToAmount    *int64
	Date        *string
	Description *string
	Tags        *[]string
}

// Apply checks the patch and returns current with the changes, whether
// anything changed (UpdatedAt moves only then), or a *ValidationError
// naming each invalid field. refs holds the records the patch moves the
// transaction to; only those are checked, so a transaction of an account
// archived since can still be corrected.
func (p TransactionPatch) Apply(current Transaction, refs TransactionRefs, now time.Time) (Transaction, bool, error) {
	var v Validation
	next := current
	next.Tags = slices.Clone(current.Tags)
	var err error
	if p.Amount != nil {
		v.Check("amount", checkPositiveAmount(*p.Amount))
		next.Amount = *p.Amount
	}
	if p.Date != nil {
		next.Date, err = ParseDate(*p.Date)
		v.Check("date", err)
	}
	if p.Description != nil {
		next.Description, err = normalizeOptionalText(*p.Description, MaxTransactionDescriptionLength)
		v.Check("description", err)
	}
	if p.Tags != nil {
		next.Tags, err = normalizeTags(*p.Tags)
		v.Check("tags", err)
	}

	refsOK := true
	if p.AccountID != nil && *p.AccountID != current.AccountID {
		next.AccountID = *p.AccountID
		var ok bool
		if next.Currency, ok = accountRef(&v, "accountId", next.AccountID, refs.Account); !ok {
			refsOK = false
		}
	}
	if p.CategoryID != nil && (current.IsTransfer() || *p.CategoryID != current.CategoryID) {
		next.CategoryID = *p.CategoryID
		categoryRef(&v, current.Kind, next.CategoryID, refs.Category)
	}
	if current.IsTransfer() {
		if p.ToAccountID != nil && *p.ToAccountID != current.ToAccountID {
			next.ToAccountID = *p.ToAccountID
			var ok bool
			if next.ToCurrency, ok = accountRef(&v, "toAccountId", next.ToAccountID, refs.ToAccount); !ok {
				refsOK = false
			}
		}
		if refsOK && next.ToAccountID == next.AccountID {
			v.Add("toAccountId", CodeInvalid)
			refsOK = false
		}
		if refsOK {
			transferAmount(&v, &next, p.ToAmount, &current)
		}
	} else {
		noTransferFields(&v, p.ToAccountID != nil, p.ToAmount != nil)
	}
	if err := v.Err(); err != nil {
		return Transaction{}, false, err
	}

	if next.equal(current) {
		return current, false, nil
	}
	next.UpdatedAt = now.UTC()
	return next, true, nil
}

// equal reports whether t and o hold the same editable values.
func (t Transaction) equal(o Transaction) bool {
	return t.AccountID == o.AccountID && t.Currency == o.Currency && t.CategoryID == o.CategoryID &&
		t.Amount == o.Amount && t.ToAccountID == o.ToAccountID && t.ToCurrency == o.ToCurrency &&
		t.ToAmount == o.ToAmount && t.Date.Equal(o.Date) && t.Description == o.Description &&
		slices.Equal(t.Tags, o.Tags)
}

// accountRef checks the account named by id in field (acc is what the
// owner's store found for it) and returns its currency.
func accountRef(v *Validation, field, id string, acc *Account) (string, bool) {
	switch {
	case id == "":
		v.Add(field, CodeRequired)
	case acc == nil || acc.ID != id:
		v.Add(field, CodeInvalid)
	case acc.Archived():
		v.Add(field, CodeArchived)
	default:
		return acc.Currency, true
	}
	return "", false
}

// categoryRef checks the category named by id (cat is what the owner's
// store found for it): required on expenses and income, of the same kind,
// and refused on transfers.
func categoryRef(v *Validation, kind TransactionKind, id string, cat *Category) {
	want, takes := kind.categoryKind()
	switch {
	case !takes:
		if id != "" {
			v.Add("categoryId", CodeInvalid)
		}
	case id == "":
		v.Add("categoryId", CodeRequired)
	case cat == nil || cat.ID != id:
		v.Add("categoryId", CodeInvalid)
	case cat.Kind != want:
		v.Add("categoryId", CodeKindMismatch)
	}
}

// noTransferFields refuses the destination of a transfer on an expense or
// an income.
func noTransferFields(v *Validation, toAccount, toAmount bool) {
	if toAccount {
		v.Add("toAccountId", CodeInvalid)
	}
	if toAmount {
		v.Add("toAmount", CodeInvalid)
	}
}

// transferAmount sets what a transfer adds to its destination. Between
// accounts of one currency it is the amount sent, and a different toAmount
// is invalid. Between two currencies it must be given, unless prev (the
// transfer before a patch) already crossed currencies and keeps its value.
func transferAmount(v *Validation, t *Transaction, toAmount *int64, prev *Transaction) {
	if t.Currency == t.ToCurrency {
		if toAmount != nil && *toAmount != t.Amount {
			v.Add("toAmount", CodeInvalid)
			return
		}
		t.ToAmount = t.Amount
		return
	}
	switch {
	case toAmount != nil:
		v.Check("toAmount", checkPositiveAmount(*toAmount))
		t.ToAmount = *toAmount
	case prev != nil && prev.Currency != prev.ToCurrency:
		// Still two currencies: the amount received stays as it was.
	default:
		v.Add("toAmount", CodeRequired)
	}
}

// checkPositiveAmount validates the amount of a transaction: 1 to
// MaxAmount minor units, since the kind gives the direction.
func checkPositiveAmount(v int64) error {
	if v < 1 {
		return &fieldErr{CodeOutOfRange}
	}
	return CheckAmount(v, false)
}

// normalizeOptionalText is normalizeText where an empty text is allowed.
func normalizeOptionalText(raw string, maxRunes int) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	return normalizeText(raw, maxRunes)
}

// normalizeTags trims each tag, lowers its case and drops repeats, keeping
// the first-seen order: at most MaxTransactionTags of 1 to MaxTagLength
// printable characters.
func normalizeTags(raw []string) ([]string, error) {
	if len(raw) > MaxTransactionTags {
		return nil, &fieldErr{CodeTooLong}
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		tag, err := NormalizeTag(r)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, tag) {
			out = append(out, tag)
		}
	}
	return out, nil
}

// NormalizeTag trims a tag and lowers its case, as tags are stored: 1 to
// MaxTagLength printable characters.
func NormalizeTag(raw string) (string, error) {
	tag, err := normalizeText(raw, MaxTagLength)
	if err != nil {
		return "", err
	}
	return strings.ToLower(tag), nil
}

// NormalizeSearch turns the text a reader searches descriptions for into
// what the store compares (FoldText), or "" when there is none: at most
// MaxSearchLength printable characters.
func NormalizeSearch(raw string) (string, error) {
	s, err := normalizeOptionalText(raw, MaxSearchLength)
	if err != nil {
		return "", err
	}
	return FoldText(s), nil
}

// TransactionFilter selects some transactions of one owner; zero fields
// select them all.
type TransactionFilter struct {
	// From and To bound the date, both days included.
	From, To Date
	// AccountID matches the account of a transaction and the destination of
	// a transfer, so a statement shows the money that arrived too.
	AccountID  string
	CategoryID string
	Kind       TransactionKind
	// Tag is one tag, as NormalizeTag returns it.
	Tag string
	// Search is text the description contains, as NormalizeSearch returns it.
	Search string
}

// TransactionCursor is where a page of transactions ended, in their order:
// newest date first, then the newest id.
type TransactionCursor struct {
	Date Date
	ID   string
}

// FoldText is s as text search compares it: lower case with the accents
// removed, so "cafe" finds "Café".
func FoldText(s string) string {
	// A transformer keeps state, so each call builds its own.
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	folded, _, err := transform.String(t, s)
	if err != nil {
		folded = s
	}
	return strings.ToLower(folded)
}

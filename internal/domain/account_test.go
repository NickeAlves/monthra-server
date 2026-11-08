package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var accountNow = time.Date(2026, 11, 8, 15, 0, 0, 0, time.FixedZone("BRT", -3*3600))

// fieldsOf returns the field codes of a *ValidationError.
func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	out := map[string]string{}
	for _, f := range ve.Fields {
		out[f.Field] = f.Code
	}
	return out
}

func card(closing, due int, limit *int64) *CardSettings {
	return &CardSettings{ClosingDay: closing, DueDay: due, Limit: limit}
}

func TestCheckAmount(t *testing.T) {
	assert.NoError(t, CheckAmount(0, false))
	assert.NoError(t, CheckAmount(MaxAmount, false))
	assert.NoError(t, CheckAmount(-MaxAmount, true))
	for name, tc := range map[string]struct {
		v        int64
		negative bool
	}{
		"negative when not allowed": {-1, false},
		"over the bound":            {MaxAmount + 1, true},
		"under the bound":           {-MaxAmount - 1, true},
	} {
		assert.Equal(t, CodeOutOfRange, codeOf(t, CheckAmount(tc.v, tc.negative)), name)
	}
}

func TestNewAccount(t *testing.T) {
	for _, typ := range AccountTypes {
		in := AccountInput{Name: "  Main  ", Type: string(typ), Currency: "BRL", InitialBalance: -1500}
		if typ == AccountCreditCard {
			in.Card = card(3, 10, nil)
		}
		a, err := NewAccount("u1", in, accountNow)
		require.NoError(t, err, typ)
		assert.Equal(t, "Main", a.Name, "trimmed")
		assert.Equal(t, typ, a.Type)
		assert.Equal(t, SourceManual, a.Source)
		assert.Equal(t, int64(-1500), a.InitialBalance, "an overdraft or a card's debt")
		assert.Equal(t, time.UTC, a.CreatedAt.Location())
		assert.Equal(t, a.CreatedAt, a.UpdatedAt)
		assert.False(t, a.Archived())
	}
}

func TestNewAccountKeepsItsOwnCard(t *testing.T) {
	in := AccountInput{Name: "Card", Type: "credit_card", Currency: "USD", Card: card(28, 5, ptr(int64(500000)))}
	a, err := NewAccount("u1", in, accountNow)
	require.NoError(t, err)
	*in.Card.Limit = 1
	in.Card.DueDay = 9
	assert.Equal(t, CardSettings{ClosingDay: 28, DueDay: 5, Limit: ptr(int64(500000))}, *a.Card)
}

func TestNewAccountValidation(t *testing.T) {
	for name, tc := range map[string]struct {
		in   AccountInput
		want map[string]string
	}{
		"everything missing": {AccountInput{}, map[string]string{"name": CodeRequired, "type": CodeRequired, "currency": CodeRequired}},
		"unknown values": {
			AccountInput{Name: "A", Type: "loan", Currency: "brl"},
			map[string]string{"type": CodeInvalid, "currency": CodeInvalid},
		},
		"name too long": {
			AccountInput{Name: strings.Repeat("a", MaxAccountNameLength+1), Type: "cash", Currency: "BRL"},
			map[string]string{"name": CodeTooLong},
		},
		"control character": {
			AccountInput{Name: "A\u202eB", Type: "cash", Currency: "BRL"},
			map[string]string{"name": CodeInvalid},
		},
		"balance over the bound": {
			AccountInput{Name: "A", Type: "cash", Currency: "BRL", InitialBalance: MaxAmount + 1},
			map[string]string{"initialBalance": CodeOutOfRange},
		},
		"card without settings": {
			AccountInput{Name: "A", Type: "credit_card", Currency: "BRL"},
			map[string]string{"card": CodeRequired},
		},
		"settings on another type": {
			AccountInput{Name: "A", Type: "checking", Currency: "BRL", Card: card(1, 10, nil)},
			map[string]string{"card": CodeInvalid},
		},
		"days out of range": {
			AccountInput{Name: "A", Type: "credit_card", Currency: "BRL", Card: card(0, 32, nil)},
			map[string]string{"card.closingDay": CodeOutOfRange, "card.dueDay": CodeOutOfRange},
		},
		"negative limit": {
			AccountInput{Name: "A", Type: "credit_card", Currency: "BRL", Card: card(31, 1, ptr(int64(-1)))},
			map[string]string{"card.limit": CodeOutOfRange},
		},
	} {
		_, err := NewAccount("u1", tc.in, accountNow)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}
}

func sampleAccount(t *testing.T, typ AccountType) Account {
	t.Helper()
	in := AccountInput{Name: "Main", Type: string(typ), Currency: "BRL", InitialBalance: 100}
	if typ == AccountCreditCard {
		in.Card = card(3, 10, ptr(int64(1000)))
	}
	a, err := NewAccount("u1", in, accountNow.Add(-time.Hour))
	require.NoError(t, err)
	a.ID = "a1"
	return a
}

func TestAccountPatchApply(t *testing.T) {
	current := sampleAccount(t, AccountCreditCard)

	next, changed, err := AccountPatch{
		Name: ptr(" Travel card "), InitialBalance: ptr(int64(-250)), Card: card(5, 12, nil),
	}.Apply(current, accountNow)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "Travel card", next.Name)
	assert.Equal(t, int64(-250), next.InitialBalance)
	assert.Equal(t, CardSettings{ClosingDay: 5, DueDay: 12}, *next.Card, "the settings are replaced, limit included")
	assert.Equal(t, accountNow.UTC(), next.UpdatedAt)
	assert.Equal(t, current.CreatedAt, next.CreatedAt)
	assert.Equal(t, current.Type, next.Type)
	assert.Equal(t, current.Currency, next.Currency)
}

func TestAccountPatchWithoutChanges(t *testing.T) {
	current := sampleAccount(t, AccountCreditCard)
	for name, p := range map[string]AccountPatch{
		"empty":              {},
		"same values":        {Name: ptr("Main"), InitialBalance: ptr(int64(100)), Card: card(3, 10, ptr(int64(1000)))},
		"already unarchived": {Archived: ptr(false)},
	} {
		next, changed, err := p.Apply(current, accountNow)
		require.NoError(t, err, name)
		assert.False(t, changed, name)
		assert.Equal(t, current, next, name)
	}
}

func TestAccountPatchArchives(t *testing.T) {
	current := sampleAccount(t, AccountChecking)

	archived, changed, err := AccountPatch{Archived: ptr(true)}.Apply(current, accountNow)
	require.NoError(t, err)
	assert.True(t, changed)
	require.True(t, archived.Archived())
	assert.Equal(t, accountNow.UTC(), *archived.ArchivedAt)

	again, changed, err := AccountPatch{Archived: ptr(true)}.Apply(archived, accountNow.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, changed, "archiving twice keeps the first date")
	assert.Equal(t, archived, again)

	restored, changed, err := AccountPatch{Archived: ptr(false)}.Apply(archived, accountNow.Add(time.Hour))
	require.NoError(t, err)
	assert.True(t, changed)
	assert.False(t, restored.Archived())
}

func TestAccountPatchValidation(t *testing.T) {
	checking := sampleAccount(t, AccountChecking)
	_, _, err := AccountPatch{Name: ptr(" "), InitialBalance: ptr(-MaxAmount - 1), Card: card(1, 2, nil)}.Apply(checking, accountNow)
	assert.Equal(t, map[string]string{"name": CodeRequired, "initialBalance": CodeOutOfRange, "card": CodeInvalid}, fieldsOf(t, err))

	creditCard := sampleAccount(t, AccountCreditCard)
	_, _, err = AccountPatch{Card: card(32, 0, ptr(MaxAmount+1))}.Apply(creditCard, accountNow)
	assert.Equal(t, map[string]string{
		"card.closingDay": CodeOutOfRange, "card.dueDay": CodeOutOfRange, "card.limit": CodeOutOfRange,
	}, fieldsOf(t, err))
}

func TestParseAccountType(t *testing.T) {
	typ, ok := ParseAccountType("savings")
	assert.True(t, ok)
	assert.Equal(t, AccountSavings, typ)
	_, ok = ParseAccountType("Savings")
	assert.False(t, ok, "case sensitive")
}

func TestCardSettingsEqual(t *testing.T) {
	var none *CardSettings
	assert.True(t, none.equal(nil))
	assert.False(t, none.equal(card(1, 2, nil)))
	assert.True(t, card(1, 2, ptr(int64(3))).equal(card(1, 2, ptr(int64(3)))))
	assert.False(t, card(1, 2, ptr(int64(3))).equal(card(1, 2, nil)))
	assert.False(t, card(1, 2, nil).equal(card(1, 3, nil)))
}

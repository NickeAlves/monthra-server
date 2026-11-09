package handler

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/http/dto"
	"github.com/NickeAlves/monthra-server/internal/usecase/investments"
)

type fakeInvestments struct {
	position   domain.InvestmentPosition
	portfolio  domain.Portfolio
	movement   domain.Movement
	page       investments.MovementPage
	err        error
	userID, id string
	invID      string
	asOf       domain.Date
	input      domain.InvestmentInput
	patch      domain.InvestmentPatch
	mInput     domain.MovementInput
	mPatch     domain.MovementPatch
	query      investments.MovementQuery
	deleted    bool
}

func (f *fakeInvestments) List(_ context.Context, userID string) (domain.Portfolio, error) {
	f.userID = userID
	return f.portfolio, f.err
}

func (f *fakeInvestments) Create(_ context.Context, userID string, in domain.InvestmentInput) (domain.InvestmentPosition, error) {
	f.userID, f.input = userID, in
	return f.position, f.err
}

func (f *fakeInvestments) Get(_ context.Context, userID, id string) (domain.InvestmentPosition, error) {
	f.userID, f.id = userID, id
	return f.position, f.err
}

func (f *fakeInvestments) Update(_ context.Context, userID, id string, p domain.InvestmentPatch) (domain.InvestmentPosition, error) {
	f.userID, f.id, f.patch = userID, id, p
	return f.position, f.err
}

func (f *fakeInvestments) Delete(_ context.Context, userID, id string) error {
	f.userID, f.id, f.deleted = userID, id, f.err == nil
	return f.err
}

func (f *fakeInvestments) ListMovements(_ context.Context, userID, invID string, q investments.MovementQuery) (investments.MovementPage, error) {
	f.userID, f.invID, f.query = userID, invID, q
	return f.page, f.err
}

func (f *fakeInvestments) GetMovement(_ context.Context, userID, invID, id string) (domain.Movement, error) {
	f.userID, f.invID, f.id = userID, invID, id
	return f.movement, f.err
}

func (f *fakeInvestments) CreateMovement(_ context.Context, userID, invID string, in domain.MovementInput, asOf domain.Date) (domain.Movement, error) {
	f.userID, f.invID, f.mInput, f.asOf = userID, invID, in, asOf
	return f.movement, f.err
}

func (f *fakeInvestments) UpdateMovement(_ context.Context, userID, invID, id string, p domain.MovementPatch, asOf domain.Date) (domain.Movement, error) {
	f.userID, f.invID, f.id, f.mPatch, f.asOf = userID, invID, id, p, asOf
	return f.movement, f.err
}

func (f *fakeInvestments) DeleteMovement(_ context.Context, userID, invID, id string) error {
	f.userID, f.invID, f.id, f.deleted = userID, invID, id, f.err == nil
	return f.err
}

// serveInvestments routes r through the investments routes as user u1.
func serveInvestments(svc *fakeInvestments, r *http.Request) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	(&Investments{Service: svc, Logger: slog.New(slog.DiscardHandler)}).Mount(router)
	asUser(r)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func samplePosition(t *testing.T) domain.InvestmentPosition {
	t.Helper()
	price, err := domain.ParsePrice("12.5")
	require.NoError(t, err)
	avg, err := domain.ParsePrice("10.1")
	require.NoError(t, err)
	on, err := domain.ParseDate("2026-11-01")
	require.NoError(t, err)
	return domain.InvestmentPosition{
		Investment: domain.Investment{
			ID: "i1", UserID: "u1", Name: "Stock", Type: domain.InvestmentStock, Mode: domain.ModeUnits, Currency: "BRL",
			Ticker: "ABC3", Source: domain.SourceManual, CreatedAt: t0, UpdatedAt: t0,
		},
		Position: domain.Position{
			Returns:  domain.Returns{Value: 12500, Invested: 10100, Contributed: 10100, Income: 100, Fees: 20},
			Quantity: 1_000_000_000, AveragePrice: &avg, Price: &price, ValuedOn: on,
		},
	}
}

var samplePositionJSON = map[string]any{
	"id": "i1", "name": "Stock", "type": "stock", "mode": "units", "currency": "BRL",
	"ticker": "ABC3", "accountId": nil, "source": "manual",
	"position": map[string]any{
		"quantity": "10", "averagePrice": "10.1", "price": "12.5", "valuedOn": "2026-11-01",
		"value": float64(12500), "invested": float64(10100), "contributed": float64(10100),
		"unrealizedGain": float64(2400), "realizedGain": float64(0), "income": float64(100), "fees": float64(20),
		"totalReturn": float64(2480), "returnBps": float64(2455),
	},
	"createdAt": "2026-11-08T12:00:00Z", "updatedAt": "2026-11-08T12:00:00Z",
}

func TestCreateInvestment(t *testing.T) {
	svc := &fakeInvestments{position: samplePosition(t)}
	w := serveInvestments(svc, request(http.MethodPost, "/investments", `{"name":"Stock","type":"stock","currency":"BRL","ticker":"abc3","accountId":"a1","mode":"units"}`))
	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "/api/v1/investments/i1", w.Header().Get("Location"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "u1", svc.userID, "the owner comes from the token")
	assert.Equal(t, domain.InvestmentInput{Name: "Stock", Type: "stock", Mode: "units", Currency: "BRL", Ticker: "abc3", AccountID: "a1"}, svc.input)
	assert.Equal(t, samplePositionJSON, jsonOf(t, w))

	for name, body := range map[string]string{
		"owner in the body": `{"name":"X","userId":"u2"}`,
		"a source":          `{"name":"X","source":"open_finance"}`,
		"operator key":      `{"name":"X","$set":{"user_id":"u2"}}`,
	} {
		svc := &fakeInvestments{}
		w := serveInvestments(svc, request(http.MethodPost, "/investments", body))
		assert.Equal(t, http.StatusBadRequest, w.Code, name)
		assert.Empty(t, svc.userID, name)
	}

	w = serveInvestments(&fakeInvestments{err: domain.ErrLimitReached}, request(http.MethodPost, "/investments", `{"name":"X"}`))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "limit_reached", problemOf(t, w).Code)
	w = serveInvestments(&fakeInvestments{err: &domain.ValidationError{Fields: []domain.FieldError{{Field: "accountId", Code: domain.CodeCurrencyMismatch}}}},
		request(http.MethodPost, "/investments", `{"name":"X"}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	p := problemOf(t, w)
	require.Len(t, p.Errors, 1)
	assert.Equal(t, "currency_mismatch", p.Errors[0].Code)
}

func TestListInvestments(t *testing.T) {
	ip := samplePosition(t)
	value := ip
	value.Investment.ID, value.Investment.Mode, value.Investment.Type, value.Investment.Ticker, value.Investment.AccountID = "i2", domain.ModeValue, domain.InvestmentFixedIncome, "", "a1"
	value.Position = domain.Position{Returns: domain.Returns{Value: 5000}}
	svc := &fakeInvestments{portfolio: domain.Portfolio{
		Investments: []domain.InvestmentPosition{ip, value},
		Totals: []domain.PortfolioTotal{{
			Currency: "BRL", Returns: domain.Returns{Value: 17500, Invested: 10100, Contributed: 10100},
			ByType: []domain.TypeValue{{Type: domain.InvestmentStock, Value: 12500}, {Type: domain.InvestmentFixedIncome, Value: 5000}},
		}},
	}}
	w := serveInvestments(svc, request(http.MethodGet, "/investments", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	body := jsonOf(t, w)
	list := body["investments"].([]any)
	require.Len(t, list, 2)
	assert.Equal(t, samplePositionJSON, list[0])
	byValue := list[1].(map[string]any)
	assert.Equal(t, "a1", byValue["accountId"])
	assert.Nil(t, byValue["ticker"])
	pos := byValue["position"].(map[string]any)
	assert.Nil(t, pos["quantity"], "no units by value")
	assert.Nil(t, pos["price"])
	assert.Nil(t, pos["averagePrice"])
	assert.Nil(t, pos["valuedOn"])
	assert.Nil(t, pos["returnBps"], "nothing contributed")
	assert.Equal(t, []any{map[string]any{
		"currency": "BRL", "value": float64(17500), "invested": float64(10100), "contributed": float64(10100),
		"unrealizedGain": float64(7400), "realizedGain": float64(0), "income": float64(0), "fees": float64(0),
		"totalReturn": float64(7400), "returnBps": float64(7327),
		"byType": []any{
			map[string]any{"type": "stock", "value": float64(12500)},
			map[string]any{"type": "fixed_income", "value": float64(5000)},
		},
	}}, body["totals"])

	w = serveInvestments(&fakeInvestments{}, request(http.MethodGet, "/investments", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, map[string]any{"investments": []any{}, "totals": []any{}}, jsonOf(t, w), "empty lists, never null")
}

func TestGetUpdateDeleteInvestment(t *testing.T) {
	svc := &fakeInvestments{position: samplePosition(t)}
	w := serveInvestments(svc, request(http.MethodGet, "/investments/i1", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "i1", svc.id)

	w = serveInvestments(&fakeInvestments{err: domain.ErrNotFound}, request(http.MethodGet, "/investments/i9", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)

	svc = &fakeInvestments{position: samplePosition(t)}
	w = serveInvestments(svc, request(http.MethodPatch, "/investments/i1", `{"name":"New","ticker":"","accountId":""}`))
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, svc.patch.Name)
	assert.Equal(t, "New", *svc.patch.Name)
	require.NotNil(t, svc.patch.Ticker)
	assert.Empty(t, *svc.patch.Ticker, "clears it")
	require.NotNil(t, svc.patch.AccountID)
	assert.Nil(t, svc.patch.Type)
	for _, body := range []string{`{"mode":"value"}`, `{"currency":"USD"}`} {
		svc := &fakeInvestments{}
		w := serveInvestments(svc, request(http.MethodPatch, "/investments/i1", body))
		assert.Equal(t, http.StatusBadRequest, w.Code, body)
		assert.Empty(t, svc.userID, body)
	}

	svc = &fakeInvestments{}
	w = serveInvestments(svc, request(http.MethodDelete, "/investments/i1", ""))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.True(t, svc.deleted)
}

func sampleMovement(t *testing.T) domain.Movement {
	t.Helper()
	d, err := domain.ParseDate("2026-11-01")
	require.NoError(t, err)
	return domain.Movement{
		ID: "m1", UserID: "u1", InvestmentID: "i1", Type: domain.MovementBuy, Mode: domain.ModeUnits, Currency: "BRL",
		Date: d, Quantity: 50_000_000, Amount: 1000, Fees: 0, CreatedAt: t0, UpdatedAt: t0,
	}
}

func TestCreateMovementHandler(t *testing.T) {
	svc := &fakeInvestments{movement: sampleMovement(t)}
	w := serveInvestments(svc, request(http.MethodPost, "/investments/i1/movements?asOf=2026-11-09",
		`{"type":"buy","date":"2026-11-01","quantity":"0.5","amount":1000}`))
	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "/api/v1/investments/i1/movements/m1", w.Header().Get("Location"))
	assert.Equal(t, "i1", svc.invID)
	assert.Equal(t, "2026-11-09", svc.asOf.String())
	assert.Equal(t, domain.MovementInput{Type: "buy", Date: "2026-11-01", Quantity: "0.5", Amount: ptrTo(int64(1000))}, svc.mInput)
	assert.Equal(t, map[string]any{
		"id": "m1", "investmentId": "i1", "type": "buy", "date": "2026-11-01",
		"quantity": "0.5", "price": nil, "amount": float64(1000), "fees": float64(0), "categoryId": nil,
		"createdAt": "2026-11-08T12:00:00Z", "updatedAt": "2026-11-08T12:00:00Z",
	}, jsonOf(t, w))

	w = serveInvestments(&fakeInvestments{}, request(http.MethodPost, "/investments/i1/movements", `{"type":"buy","quantity":0.5}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code, "a quantity is a string, never a number")
	w = serveInvestments(&fakeInvestments{}, request(http.MethodPost, "/investments/i1/movements?asOf=x", `{}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)

	svc = &fakeInvestments{err: &domain.ValidationError{Fields: []domain.FieldError{{Field: "quantity", Code: domain.CodeExceedsPosition}}}}
	w = serveInvestments(svc, request(http.MethodPost, "/investments/i1/movements", `{"type":"sell"}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, "exceeds_position", problemOf(t, w).Errors[0].Code)
}

func TestMovementViews(t *testing.T) {
	income := sampleMovement(t)
	income.Type, income.Quantity, income.CategoryID = domain.MovementIncome, 0, "c1"
	assert.Equal(t, dto.Movement{
		ID: "m1", InvestmentID: "i1", Type: "income", Date: "2026-11-01", Amount: ptrTo(int64(1000)), CategoryID: ptrTo("c1"),
		CreatedAt: t0, UpdatedAt: t0,
	}, dto.NewMovement(income))
	quote := sampleMovement(t)
	quote.Type, quote.Price, quote.Quantity, quote.Amount = domain.MovementValuation, 0, 0, 0
	assert.Equal(t, ptrTo("0"), dto.NewMovement(quote).Price, "a worthless asset is a zero price, not null")
	value := sampleMovement(t)
	value.Type, value.Mode, value.Quantity, value.Amount = domain.MovementValuation, domain.ModeValue, 0, 0
	view := dto.NewMovement(value)
	assert.Nil(t, view.Price)
	assert.Equal(t, ptrTo(int64(0)), view.Amount, "a position read as worthless")
}

func TestListMovementsHandler(t *testing.T) {
	m := sampleMovement(t)
	svc := &fakeInvestments{page: investments.MovementPage{Items: []domain.Movement{m}, Next: &domain.MovementCursor{Date: m.Date, ID: m.ID}}}
	cursor := dto.EncodeCursor(domain.MovementCursor{Date: m.Date, ID: "m9"})
	w := serveInvestments(svc, request(http.MethodGet, "/investments/i1/movements?type=buy&limit=10&cursor="+cursor, ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "i1", svc.invID)
	assert.Equal(t, 10, svc.query.Limit)
	assert.Equal(t, domain.MovementBuy, svc.query.Filter.Type)
	require.NotNil(t, svc.query.After)
	assert.Equal(t, "m9", svc.query.After.ID)
	body := jsonOf(t, w)
	assert.Len(t, body["movements"], 1)
	assert.Equal(t, dto.EncodeCursor(domain.MovementCursor{Date: m.Date, ID: m.ID}), body["nextCursor"])

	for _, q := range []string{"type=split", "limit=x", "cursor=!!!"} {
		svc := &fakeInvestments{}
		w := serveInvestments(svc, request(http.MethodGet, "/investments/i1/movements?"+q, ""))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code, q)
		assert.Empty(t, svc.userID, q)
	}
	w = serveInvestments(&fakeInvestments{}, request(http.MethodGet, "/investments/i1/movements", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, map[string]any{"movements": []any{}, "nextCursor": nil}, jsonOf(t, w))
}

func TestGetUpdateDeleteMovement(t *testing.T) {
	svc := &fakeInvestments{movement: sampleMovement(t)}
	w := serveInvestments(svc, request(http.MethodGet, "/investments/i1/movements/m1", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "i1", svc.invID)
	assert.Equal(t, "m1", svc.id)

	svc = &fakeInvestments{movement: sampleMovement(t)}
	w = serveInvestments(svc, request(http.MethodPatch, "/investments/i1/movements/m1?asOf=2026-11-09", `{"quantity":"1","fees":5}`))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, domain.MovementPatch{Quantity: ptrTo("1"), Fees: ptrTo(int64(5))}, svc.mPatch)
	assert.Equal(t, "2026-11-09", svc.asOf.String())
	w = serveInvestments(&fakeInvestments{}, request(http.MethodPatch, "/investments/i1/movements/m1", `{"type":"sell"}`))
	assert.Equal(t, http.StatusBadRequest, w.Code, "the type never changes")
	w = serveInvestments(&fakeInvestments{}, request(http.MethodPatch, "/investments/i1/movements/m1?asOf=x", `{}`))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)

	svc = &fakeInvestments{}
	w = serveInvestments(svc, request(http.MethodDelete, "/investments/i1/movements/m1", ""))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.True(t, svc.deleted)
	w = serveInvestments(&fakeInvestments{err: domain.ErrExceedsPosition}, request(http.MethodDelete, "/investments/i1/movements/m1", ""))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "exceeds_position", problemOf(t, w).Code)
	w = serveInvestments(&fakeInvestments{err: domain.ErrNotFound}, request(http.MethodGet, "/investments/i2/movements/m1", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func ptrTo[T any](v T) *T { return &v }

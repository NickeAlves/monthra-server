package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

type fakeCategories struct {
	categories []domain.Category
	err        error
	userID, id string
	kind       domain.CategoryKind
	input      domain.CategoryInput
	patch      domain.CategoryPatch
	deleted    bool
	reassignTo string
}

func (f *fakeCategories) first() domain.Category {
	if len(f.categories) == 0 {
		return domain.Category{}
	}
	return f.categories[0]
}

func (f *fakeCategories) List(_ context.Context, userID string, kind domain.CategoryKind) ([]domain.Category, error) {
	f.userID, f.kind = userID, kind
	return f.categories, f.err
}

func (f *fakeCategories) Create(_ context.Context, userID string, in domain.CategoryInput) (domain.Category, error) {
	f.userID, f.input = userID, in
	return f.first(), f.err
}

func (f *fakeCategories) Get(_ context.Context, userID, id string) (domain.Category, error) {
	f.userID, f.id = userID, id
	return f.first(), f.err
}

func (f *fakeCategories) Update(_ context.Context, userID, id string, p domain.CategoryPatch) (domain.Category, error) {
	f.userID, f.id, f.patch = userID, id, p
	return f.first(), f.err
}

func (f *fakeCategories) Delete(_ context.Context, userID, id, reassignTo string) error {
	f.userID, f.id, f.reassignTo, f.deleted = userID, id, reassignTo, f.err == nil
	return f.err
}

func sampleCategory() domain.Category {
	return domain.Category{
		ID: "c1", UserID: "u1", Name: "Food", Kind: domain.CategoryExpense, Icon: "utensils", Color: "#AA0000",
		CreatedAt: t0, UpdatedAt: t0,
	}
}

func sampleDefault() domain.Category {
	return domain.Category{
		ID: "c2", UserID: "u1", DefaultKey: "groceries", Kind: domain.CategoryExpense, Icon: "shopping-cart", Color: "#22C55E",
		CreatedAt: t0, UpdatedAt: t0,
	}
}

// inLocale sets the response locale, as the UserLocale middleware does.
func inLocale(l i18n.Locale) func(*http.Request) {
	return func(r *http.Request) { *r = *r.WithContext(i18n.WithLocale(r.Context(), l)) }
}

// serveCategories routes r through the categories routes as user u1.
func serveCategories(svc *fakeCategories, r *http.Request) *httptest.ResponseRecorder {
	router := chi.NewRouter()
	(&Categories{Service: svc, Logger: slog.New(slog.DiscardHandler)}).Mount(router)
	asUser(r)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestCreateCategory(t *testing.T) {
	svc := &fakeCategories{categories: []domain.Category{sampleCategory()}}
	w := serveCategories(svc, request(http.MethodPost, "/categories", `{"name":"Food","kind":"expense","icon":"utensils","color":"#aa0000"}`))

	require.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "/api/v1/categories/c1", w.Header().Get("Location"))
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Equal(t, "u1", svc.userID, "the owner comes from the token")
	assert.Equal(t, domain.CategoryInput{Name: "Food", Kind: "expense", Icon: "utensils", Color: "#aa0000"}, svc.input)
	assert.Equal(t, map[string]any{
		"id": "c1", "name": "Food", "kind": "expense", "icon": "utensils", "color": "#AA0000", "defaultKey": nil,
		"createdAt": "2026-11-08T12:00:00Z", "updatedAt": "2026-11-08T12:00:00Z",
	}, jsonOf(t, w), "no owner id in the answer")
}

func TestCreateCategoryRejectsUnsafeBodies(t *testing.T) {
	for name, body := range map[string]string{
		"owner in the body":  `{"name":"A","kind":"expense","icon":"tag","color":"#000000","userId":"u2"}`,
		"default key forged": `{"name":"A","kind":"expense","icon":"tag","color":"#000000","defaultKey":"salary"}`,
		"operator key":       `{"name":"A","kind":"expense","icon":"tag","color":"#000000","$set":{"user_id":"u2"}}`,
		"not json":           `name=A`,
	} {
		svc := &fakeCategories{}
		w := serveCategories(svc, request(http.MethodPost, "/categories", body))
		assert.Equal(t, http.StatusBadRequest, w.Code, name)
		assert.Equal(t, "bad_request", problemOf(t, w).Code, name)
		assert.Empty(t, svc.userID, "%s: the use case is never reached", name)
	}
}

func TestCreateCategoryErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"invalid": {&domain.ValidationError{Fields: []domain.FieldError{{Field: "color", Code: domain.CodeInvalid}}}, http.StatusUnprocessableEntity, "validation_failed"},
		"limit":   {domain.ErrLimitReached, http.StatusConflict, "limit_reached"},
		"down":    {errors.New("mongo down"), http.StatusInternalServerError, "internal_error"},
	} {
		w := serveCategories(&fakeCategories{err: tc.err}, request(http.MethodPost, "/categories", `{"name":"A","kind":"expense","icon":"tag","color":"#12345"}`))
		assert.Equal(t, tc.status, w.Code, name)
		p := problemOf(t, w)
		assert.Equal(t, tc.code, p.Code, name)
		if tc.status == http.StatusUnprocessableEntity {
			require.Len(t, p.Errors, 1, name)
			assert.Equal(t, "color", p.Errors[0].Field)
		}
	}
}

func TestListCategoriesNamesDefaultsInTheRequestLocale(t *testing.T) {
	svc := &fakeCategories{categories: []domain.Category{sampleDefault(), sampleCategory()}}
	for l, want := range map[i18n.Locale]string{i18n.PtBR: "Mercado", i18n.EsES: "Supermercado", i18n.EnUS: "Groceries"} {
		w := serveCategories(svc, request(http.MethodGet, "/categories", "", inLocale(l)))
		require.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		list, _ := jsonOf(t, w)["categories"].([]any)
		require.Len(t, list, 2)
		def, _ := list[0].(map[string]any)
		own, _ := list[1].(map[string]any)
		assert.Equal(t, want, def["name"], l)
		assert.Equal(t, "groceries", def["defaultKey"])
		assert.Equal(t, "Food", own["name"], "the owner's name in every language")
		assert.Nil(t, own["defaultKey"])
	}
	assert.Empty(t, svc.kind)
}

func TestListCategoriesByKind(t *testing.T) {
	svc := &fakeCategories{}
	w := serveCategories(svc, request(http.MethodGet, "/categories?kind=income", ""))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, domain.CategoryIncome, svc.kind)
	assert.JSONEq(t, `{"categories":[]}`, w.Body.String(), "an empty list is [], never null")

	for _, bad := range []string{"transfer", "INCOME"} {
		svc := &fakeCategories{}
		w = serveCategories(svc, request(http.MethodGet, "/categories?kind="+bad, ""))
		assert.Equal(t, http.StatusUnprocessableEntity, w.Code, bad)
		p := problemOf(t, w)
		require.Len(t, p.Errors, 1)
		assert.Equal(t, "kind", p.Errors[0].Field)
		assert.Empty(t, svc.userID, "%s: the use case is never reached", bad)
	}

	w = serveCategories(&fakeCategories{err: errors.New("mongo down")}, request(http.MethodGet, "/categories", ""))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestGetCategory(t *testing.T) {
	svc := &fakeCategories{categories: []domain.Category{sampleDefault()}}
	w := serveCategories(svc, request(http.MethodGet, "/categories/c2", "", inLocale(i18n.EnUS)))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "c2", svc.id)
	assert.Equal(t, "Groceries", jsonOf(t, w)["name"])

	w = serveCategories(&fakeCategories{err: domain.ErrNotFound}, request(http.MethodGet, "/categories/other", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "not_found", problemOf(t, w).Code)
}

func TestUpdateCategory(t *testing.T) {
	svc := &fakeCategories{categories: []domain.Category{sampleCategory()}}
	w := serveCategories(svc, request(http.MethodPatch, "/categories/c1", `{"name":"Meals","color":"#00AA00"}`))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "c1", svc.id)
	require.NotNil(t, svc.patch.Name)
	assert.Equal(t, "Meals", *svc.patch.Name)
	require.NotNil(t, svc.patch.Color)
	assert.Equal(t, "#00AA00", *svc.patch.Color)
	assert.Nil(t, svc.patch.Icon)

	for name, body := range map[string]string{
		"kind never changes":     `{"kind":"income"}`,
		"default key never set":  `{"defaultKey":"salary"}`,
		"owner is not editable":  `{"userId":"u2"}`,
		"operator key":           `{"$set":{"user_id":"u2"}}`,
		"operator key in nested": `{"name":{"$ne":null}}`,
	} {
		svc := &fakeCategories{categories: []domain.Category{sampleCategory()}}
		w := serveCategories(svc, request(http.MethodPatch, "/categories/c1", body))
		assert.Contains(t, []int{http.StatusBadRequest, http.StatusUnprocessableEntity}, w.Code, name)
		assert.Empty(t, svc.id, name)
	}

	w = serveCategories(&fakeCategories{err: domain.ErrNotFound}, request(http.MethodPatch, "/categories/c9", `{"name":"x"}`))
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestDeleteCategory(t *testing.T) {
	svc := &fakeCategories{}
	w := serveCategories(svc, request(http.MethodDelete, "/categories/c1", ""))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.True(t, svc.deleted)
	assert.Equal(t, "u1", svc.userID)
	assert.Empty(t, svc.reassignTo)

	w = serveCategories(svc, request(http.MethodDelete, "/categories/c1?reassignTo=c2", ""))
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "c2", svc.reassignTo)

	w = serveCategories(&fakeCategories{err: domain.ErrNotFound}, request(http.MethodDelete, "/categories/c1", ""))
	assert.Equal(t, http.StatusNotFound, w.Code)

	w = serveCategories(&fakeCategories{err: domain.ErrCategoryInUse}, request(http.MethodDelete, "/categories/c1", ""))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Equal(t, "category_in_use", problemOf(t, w).Code)

	invalid := &domain.ValidationError{Fields: []domain.FieldError{{Field: "reassignTo", Code: domain.CodeKindMismatch}}}
	w = serveCategories(&fakeCategories{err: invalid}, request(http.MethodDelete, "/categories/c1?reassignTo=c3", ""))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	p := problemOf(t, w)
	require.Len(t, p.Errors, 1)
	assert.Equal(t, "kind_mismatch", p.Errors[0].Code)
}

func TestCategoriesNeedAPrincipal(t *testing.T) {
	h := &Categories{Service: &fakeCategories{}, Logger: slog.New(slog.DiscardHandler)}
	for name, fn := range map[string]http.HandlerFunc{
		"list": h.List, "create": h.Create, "get": h.Get, "update": h.Update, "delete": h.Delete,
	} {
		w := serve(fn, request(http.MethodGet, "/categories", "{}"))
		assert.Equal(t, http.StatusUnauthorized, w.Code, name)
	}
}

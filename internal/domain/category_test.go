package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

var categoryNow = time.Date(2026, 11, 8, 18, 0, 0, 0, time.FixedZone("BRT", -3*3600))

func TestNewCategory(t *testing.T) {
	for _, kind := range CategoryKinds {
		c, err := NewCategory("u1", CategoryInput{Name: "  Café  ", Kind: string(kind), Icon: "coffee", Color: "#1e6b52"}, categoryNow)
		require.NoError(t, err, kind)
		assert.Equal(t, "Café", c.Name, "trimmed")
		assert.Equal(t, kind, c.Kind)
		assert.Equal(t, "coffee", c.Icon)
		assert.Equal(t, "#1E6B52", c.Color, "stored in upper case")
		assert.Empty(t, c.DefaultKey, "created by the owner")
		assert.Equal(t, "u1", c.UserID)
		assert.Equal(t, time.UTC, c.CreatedAt.Location())
		assert.Equal(t, c.CreatedAt, c.UpdatedAt)
	}
}

func TestNewCategoryValidation(t *testing.T) {
	valid := CategoryInput{Name: "Food", Kind: "expense", Icon: "utensils", Color: "#AABBCC"}
	with := func(f func(*CategoryInput)) CategoryInput {
		in := valid
		f(&in)
		return in
	}
	for name, tc := range map[string]struct {
		in   CategoryInput
		want map[string]string
	}{
		"everything missing": {CategoryInput{}, map[string]string{
			"name": CodeRequired, "kind": CodeRequired, "icon": CodeRequired, "color": CodeRequired,
		}},
		"blank name":            {with(func(in *CategoryInput) { in.Name = "   " }), map[string]string{"name": CodeRequired}},
		"long name":             {with(func(in *CategoryInput) { in.Name = strings.Repeat("a", 51) }), map[string]string{"name": CodeTooLong}},
		"control character":     {with(func(in *CategoryInput) { in.Name = "Fo\u0007od" }), map[string]string{"name": CodeInvalid}},
		"bidi override":         {with(func(in *CategoryInput) { in.Name = "Fo\u202eod" }), map[string]string{"name": CodeInvalid}},
		"unknown kind":          {with(func(in *CategoryInput) { in.Kind = "transfer" }), map[string]string{"kind": CodeInvalid}},
		"kind in upper case":    {with(func(in *CategoryInput) { in.Kind = "EXPENSE" }), map[string]string{"kind": CodeInvalid}},
		"icon in upper case":    {with(func(in *CategoryInput) { in.Icon = "ShoppingCart" }), map[string]string{"icon": CodeInvalid}},
		"icon with a space":     {with(func(in *CategoryInput) { in.Icon = "shopping cart" }), map[string]string{"icon": CodeInvalid}},
		"icon with edge hyphen": {with(func(in *CategoryInput) { in.Icon = "-car" }), map[string]string{"icon": CodeInvalid}},
		"icon with two hyphens": {with(func(in *CategoryInput) { in.Icon = "shopping--cart" }), map[string]string{"icon": CodeInvalid}},
		"icon as a path":        {with(func(in *CategoryInput) { in.Icon = "../car" }), map[string]string{"icon": CodeInvalid}},
		"long icon":             {with(func(in *CategoryInput) { in.Icon = strings.Repeat("a", 65) }), map[string]string{"icon": CodeTooLong}},
		"color without #":       {with(func(in *CategoryInput) { in.Color = "AABBCC" }), map[string]string{"color": CodeInvalid}},
		"short color":           {with(func(in *CategoryInput) { in.Color = "#ABC" }), map[string]string{"color": CodeInvalid}},
		"color with alpha":      {with(func(in *CategoryInput) { in.Color = "#AABBCCDD" }), map[string]string{"color": CodeInvalid}},
		"color not hex":         {with(func(in *CategoryInput) { in.Color = "#GGHHII" }), map[string]string{"color": CodeInvalid}},
		"color by name":         {with(func(in *CategoryInput) { in.Color = "red" }), map[string]string{"color": CodeInvalid}},
	} {
		_, err := NewCategory("u1", tc.in, categoryNow)
		assert.Equal(t, tc.want, fieldsOf(t, err), name)
	}

	_, err := NewCategory("u1", with(func(in *CategoryInput) { in.Icon = strings.Repeat("a", 64) }), categoryNow)
	assert.NoError(t, err, "64 characters is the longest icon name")
	_, err = NewCategory("u1", with(func(in *CategoryInput) { in.Name = strings.Repeat("é", 50) }), categoryNow)
	assert.NoError(t, err, "the limit counts characters, not bytes")
}

func TestCategoryPatch(t *testing.T) {
	later := categoryNow.Add(time.Hour)
	current := NewDefaultCategories("u1", categoryNow)[1] // groceries
	require.Equal(t, "groceries", current.DefaultKey)

	next, changed, err := CategoryPatch{}.Apply(current, later)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, current, next)

	next, changed, err = CategoryPatch{Icon: ptr(current.Icon), Color: ptr(strings.ToLower(current.Color))}.Apply(current, later)
	require.NoError(t, err)
	assert.False(t, changed, "the same values, the color in another case")
	assert.Equal(t, current.UpdatedAt, next.UpdatedAt)

	next, changed, err = CategoryPatch{Name: ptr(" Supermarket "), Icon: ptr("store"), Color: ptr("#00aa00")}.Apply(current, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "Supermarket", next.Name)
	assert.Equal(t, "store", next.Icon)
	assert.Equal(t, "#00AA00", next.Color)
	assert.Equal(t, "groceries", next.DefaultKey, "a renamed default keeps its key")
	assert.Equal(t, CategoryExpense, next.Kind)
	assert.Equal(t, later.UTC(), next.UpdatedAt)
	assert.Equal(t, current.CreatedAt, next.CreatedAt)
	for _, l := range i18n.Supported {
		assert.Equal(t, "Supermarket", next.DisplayName(l), "a renamed default no longer follows the language")
	}

	// Naming a default with its translated name still fixes it.
	next, changed, err = CategoryPatch{Name: ptr("Mercado")}.Apply(current, later)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, "Mercado", next.DisplayName(i18n.EnUS))

	_, _, err = CategoryPatch{Name: ptr(""), Icon: ptr("Car"), Color: ptr("#12345")}.Apply(current, later)
	assert.Equal(t, map[string]string{"name": CodeRequired, "icon": CodeInvalid, "color": CodeInvalid}, fieldsOf(t, err))
}

func TestCategoryDisplayName(t *testing.T) {
	salary := Category{DefaultKey: "salary"}
	assert.Equal(t, "Salário", salary.DisplayName(i18n.PtBR))
	assert.Equal(t, "Salario", salary.DisplayName(i18n.EsES))
	assert.Equal(t, "Salary", salary.DisplayName(i18n.EnUS))
	assert.Equal(t, "Salário", salary.DisplayName("fr-FR"), "pt-BR for an unsupported locale")

	assert.Equal(t, "Mine", Category{Name: "Mine"}.DisplayName(i18n.EnUS))
	assert.Equal(t, "retired_key", Category{DefaultKey: "retired_key"}.DisplayName(i18n.EnUS),
		"a key no longer in the set shows as itself, never blank")
}

func TestDefaultCategories(t *testing.T) {
	list := NewDefaultCategories("u1", categoryNow)
	require.Len(t, list, len(defaultCategories))
	assert.Len(t, list, 22)

	keys := map[string]bool{}
	kinds := map[CategoryKind]int{}
	for i, c := range list {
		d := defaultCategories[i]
		assert.False(t, keys[c.DefaultKey], "unique key %s", c.DefaultKey)
		keys[c.DefaultKey] = true
		kinds[c.Kind]++

		assert.Equal(t, d.Key, c.DefaultKey)
		assert.Empty(t, c.Name, "%s follows the language", d.Key)
		assert.Equal(t, "u1", c.UserID)
		assert.Equal(t, categoryNow.UTC(), c.CreatedAt)
		_, ok := ParseCategoryKind(string(c.Kind))
		assert.True(t, ok, d.Key)
		_, err := checkIcon(c.Icon)
		assert.NoError(t, err, d.Key)
		color, err := normalizeColor(c.Color)
		assert.NoError(t, err, d.Key)
		assert.Equal(t, color, c.Color, "%s color in upper case", d.Key)

		assert.Len(t, d.Names, len(i18n.Supported), d.Key)
		for _, l := range i18n.Supported {
			name, err := normalizeText(d.Names[l], MaxCategoryNameLength)
			assert.NoError(t, err, "%s in %s", d.Key, l)
			assert.Equal(t, name, c.DisplayName(l), "%s in %s", d.Key, l)
		}
	}
	assert.Equal(t, 16, kinds[CategoryExpense])
	assert.Equal(t, 6, kinds[CategoryIncome])

	// Every expense comes before every income: the list order is the seed order.
	for i := 1; i < len(list); i++ {
		assert.False(t, list[i-1].Kind == CategoryIncome && list[i].Kind == CategoryExpense, "expenses first")
	}
}

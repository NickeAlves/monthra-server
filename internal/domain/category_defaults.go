package domain

import (
	"time"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// defaultCategory is one category every user starts with. Its name is
// translated until the owner renames it. TestDefaultCategories fails when
// one misses a locale or holds an invalid icon or color.
type defaultCategory struct {
	Key   string
	Kind  CategoryKind
	Icon  string
	Color string
	Names map[i18n.Locale]string
}

// defaultCategories is the starting set, in the order clients list it:
// expenses, then income. Keys are stable: they are stored, and they will map
// the categories of Open Finance providers too. Colors are mid tones that
// read on light and dark backgrounds; salary uses the brand accent.
var defaultCategories = []defaultCategory{
	{"housing", CategoryExpense, "house", "#8B5CF6", names("Moradia", "Vivienda", "Housing")},
	{"groceries", CategoryExpense, "shopping-cart", "#22C55E", names("Mercado", "Supermercado", "Groceries")},
	{"dining", CategoryExpense, "utensils", "#F97316", names("Restaurantes", "Restaurantes", "Dining out")},
	{"transport", CategoryExpense, "car", "#3B82F6", names("Transporte", "Transporte", "Transportation")},
	{"health", CategoryExpense, "heart-pulse", "#EF4444", names("Saúde", "Salud", "Health")},
	{"education", CategoryExpense, "graduation-cap", "#0EA5E9", names("Educação", "Educación", "Education")},
	{"leisure", CategoryExpense, "party-popper", "#EC4899", names("Lazer", "Ocio", "Leisure")},
	{"shopping", CategoryExpense, "shopping-bag", "#D946EF", names("Compras", "Compras", "Shopping")},
	{"utilities", CategoryExpense, "zap", "#EAB308", names("Contas da casa", "Servicios del hogar", "Utilities")},
	{"subscriptions", CategoryExpense, "repeat", "#14B8A6", names("Assinaturas", "Suscripciones", "Subscriptions")},
	{"personal_care", CategoryExpense, "sparkles", "#F43F5E", names("Cuidados pessoais", "Cuidado personal", "Personal care")},
	{"pets", CategoryExpense, "paw-print", "#B45309", names("Pets", "Mascotas", "Pets")},
	{"travel", CategoryExpense, "plane", "#06B6D4", names("Viagens", "Viajes", "Travel")},
	{"taxes_fees", CategoryExpense, "landmark", "#64748B", names("Impostos e taxas", "Impuestos y tasas", "Taxes and fees")},
	{"gifts_donations", CategoryExpense, "gift", "#A855F7", names("Presentes e doações", "Regalos y donaciones", "Gifts and donations")},
	{"other_expense", CategoryExpense, "circle-ellipsis", "#78716C", names("Outros gastos", "Otros gastos", "Other expenses")},
	{"salary", CategoryIncome, "briefcase", "#1E6B52", names("Salário", "Salario", "Salary")},
	{"side_income", CategoryIncome, "laptop", "#10B981", names("Renda extra", "Ingresos extra", "Side income")},
	{"investment_income", CategoryIncome, "trending-up", "#84CC16", names("Rendimentos", "Rendimientos", "Investment income")},
	{"refunds", CategoryIncome, "rotate-ccw", "#6366F1", names("Reembolsos", "Reembolsos", "Refunds")},
	{"gifts_received", CategoryIncome, "hand-heart", "#F59E0B", names("Presentes recebidos", "Regalos recibidos", "Gifts received")},
	{"other_income", CategoryIncome, "circle-plus", "#737373", names("Outras receitas", "Otros ingresos", "Other income")},
}

func names(ptBR, esES, enUS string) map[i18n.Locale]string {
	return map[i18n.Locale]string{i18n.PtBR: ptBR, i18n.EsES: esES, i18n.EnUS: enUS}
}

// defaultCategoryByKey indexes defaultCategories.
var defaultCategoryByKey = func() map[string]defaultCategory {
	out := make(map[string]defaultCategory, len(defaultCategories))
	for _, d := range defaultCategories {
		out[d.Key] = d
	}
	return out
}()

// DefaultCategoryName is the name of default category key in locale l, in
// pt-BR when l is not supported, or key itself when no default has it.
func DefaultCategoryName(key string, l i18n.Locale) string {
	d, ok := defaultCategoryByKey[key]
	if !ok {
		return key
	}
	if n, ok := d.Names[l]; ok {
		return n
	}
	return d.Names[i18n.Default]
}

// NewDefaultCategories returns the starting categories of userID, unnamed so
// their names follow the reader's language.
func NewDefaultCategories(userID string, now time.Time) []Category {
	now = now.UTC()
	out := make([]Category, len(defaultCategories))
	for i, d := range defaultCategories {
		out[i] = Category{
			UserID: userID, DefaultKey: d.Key, Kind: d.Kind, Icon: d.Icon, Color: d.Color,
			CreatedAt: now, UpdatedAt: now,
		}
	}
	return out
}

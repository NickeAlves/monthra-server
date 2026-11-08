package domain

import (
	"regexp"
	"strings"
	"time"

	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// Limits of the category fields.
const (
	MaxCategoryNameLength = 50
	MaxCategoryIconLength = 64
)

// CategoryKind tells whether a category groups expenses or income.
type CategoryKind string

// Supported category kinds.
const (
	CategoryExpense CategoryKind = "expense"
	CategoryIncome  CategoryKind = "income"
)

// CategoryKinds lists the supported category kinds.
var CategoryKinds = []CategoryKind{CategoryExpense, CategoryIncome}

// ParseCategoryKind returns the category kind named s, if it is supported.
func ParseCategoryKind(s string) (CategoryKind, bool) {
	for _, k := range CategoryKinds {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}

var (
	// iconPattern is a Lucide icon name: kebab-case, as lucide.dev writes it
	// ("shopping-cart"). The set of names is not closed here: clients show a
	// generic icon for a name they do not know.
	iconPattern  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	colorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

// Category groups the transactions of one kind (food, salary, ...).
type Category struct {
	ID     string
	UserID string
	// Name is the owner's name for the category. A default category keeps it
	// empty until renamed, and is shown as its DefaultKey in the reader's
	// language instead.
	Name string
	// DefaultKey names the default category this one was seeded from
	// ("groceries"), and stays after a rename; it is empty on the categories
	// the owner created.
	DefaultKey string
	// Kind never changes: transactions must match the kind of their category.
	Kind CategoryKind
	// Icon is a Lucide icon name, Color a "#RRGGBB" color in upper case.
	Icon      string
	Color     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DisplayName is the name to show in locale l: the owner's name, or the
// translated name of the default category while it was never renamed.
func (c Category) DisplayName(l i18n.Locale) string {
	if c.Name != "" {
		return c.Name
	}
	return DefaultCategoryName(c.DefaultKey, l)
}

// CategoryInput is a new category as the owner describes it.
type CategoryInput struct {
	Name  string
	Kind  string
	Icon  string
	Color string
}

// NewCategory validates in and returns the category of userID, or a
// *ValidationError naming every invalid field.
func NewCategory(userID string, in CategoryInput, now time.Time) (Category, error) {
	var v Validation
	name, err := normalizeText(in.Name, MaxCategoryNameLength)
	v.Check("name", err)

	kind, ok := ParseCategoryKind(in.Kind)
	switch {
	case in.Kind == "":
		v.Add("kind", CodeRequired)
	case !ok:
		v.Add("kind", CodeInvalid)
	}

	icon, err := checkIcon(in.Icon)
	v.Check("icon", err)
	color, err := normalizeColor(in.Color)
	v.Check("color", err)
	if err := v.Err(); err != nil {
		return Category{}, err
	}

	now = now.UTC()
	return Category{
		UserID: userID, Name: name, Kind: kind, Icon: icon, Color: color,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

// checkIcon validates a Lucide icon name.
func checkIcon(raw string) (string, error) {
	switch {
	case raw == "":
		return "", &fieldErr{CodeRequired}
	case len(raw) > MaxCategoryIconLength:
		return "", &fieldErr{CodeTooLong}
	case !iconPattern.MatchString(raw):
		return "", &fieldErr{CodeInvalid}
	}
	return raw, nil
}

// normalizeColor validates a "#RRGGBB" color and returns it in upper case.
func normalizeColor(raw string) (string, error) {
	switch {
	case raw == "":
		return "", &fieldErr{CodeRequired}
	case !colorPattern.MatchString(raw):
		return "", &fieldErr{CodeInvalid}
	}
	return strings.ToUpper(raw), nil
}

// CategoryPatch changes some fields of a category; nil fields stay as they
// are. The kind is not a field: it never changes.
type CategoryPatch struct {
	Name  *string
	Icon  *string
	Color *string
}

// Apply checks the patch and returns current with the changes, whether
// anything changed (UpdatedAt moves only then), or a *ValidationError naming
// each invalid field. Naming a default category fixes its name: from then on
// it no longer follows the reader's language.
func (p CategoryPatch) Apply(current Category, now time.Time) (Category, bool, error) {
	var v Validation
	next := current
	if p.Name != nil {
		name, err := normalizeText(*p.Name, MaxCategoryNameLength)
		v.Check("name", err)
		next.Name = name
	}
	if p.Icon != nil {
		icon, err := checkIcon(*p.Icon)
		v.Check("icon", err)
		next.Icon = icon
	}
	if p.Color != nil {
		color, err := normalizeColor(*p.Color)
		v.Check("color", err)
		next.Color = color
	}
	if err := v.Err(); err != nil {
		return Category{}, false, err
	}

	if next.Name == current.Name && next.Icon == current.Icon && next.Color == current.Color {
		return current, false, nil
	}
	next.UpdatedAt = now.UTC()
	return next, true, nil
}

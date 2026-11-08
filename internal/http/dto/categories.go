package dto

import (
	"time"

	"github.com/NickeAlves/monthra-server/internal/domain"
	"github.com/NickeAlves/monthra-server/internal/i18n"
)

// CreateCategoryRequest is the body of POST /categories. The domain checks
// every field, so missing ones are reported as required there.
type CreateCategoryRequest struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Icon  string `json:"icon"`
	Color string `json:"color"`
}

// Input maps the request to the domain input.
func (r CreateCategoryRequest) Input() domain.CategoryInput {
	return domain.CategoryInput{Name: r.Name, Kind: r.Kind, Icon: r.Icon, Color: r.Color}
}

// UpdateCategoryRequest is the body of PATCH /categories/{id}; absent
// fields stay as they are. The kind and the default key are not fields:
// they never change.
type UpdateCategoryRequest struct {
	Name  *string `json:"name"`
	Icon  *string `json:"icon"`
	Color *string `json:"color"`
}

// Patch maps the request to the domain patch.
func (r UpdateCategoryRequest) Patch() domain.CategoryPatch {
	return domain.CategoryPatch{Name: r.Name, Icon: r.Icon, Color: r.Color}
}

// Category is the public view of a category.
type Category struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Icon string `json:"icon"`
	// Color is "#RRGGBB" in upper case.
	Color string `json:"color"`
	// DefaultKey names the default category this one was seeded from, or is
	// null on the categories the owner created.
	DefaultKey *string   `json:"defaultKey"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// NewCategory maps a category to its public view in locale l: a default
// category that was never renamed is named in l. The owner id stays out.
func NewCategory(c domain.Category, l i18n.Locale) Category {
	out := Category{
		ID: c.ID, Name: c.DisplayName(l), Kind: string(c.Kind), Icon: c.Icon, Color: c.Color,
		CreatedAt: c.CreatedAt.UTC(), UpdatedAt: c.UpdatedAt.UTC(),
	}
	if c.DefaultKey != "" {
		key := c.DefaultKey
		out.DefaultKey = &key
	}
	return out
}

// CategoriesResponse is the body of GET /categories.
type CategoriesResponse struct {
	Categories []Category `json:"categories"`
}

// NewCategories maps a list of categories in locale l; an empty list is [],
// never null.
func NewCategories(list []domain.Category, l i18n.Locale) CategoriesResponse {
	out := CategoriesResponse{Categories: make([]Category, len(list))}
	for i, c := range list {
		out.Categories[i] = NewCategory(c, l)
	}
	return out
}

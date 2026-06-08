package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	EnvTableName                  = "CATALOG_TABLE_NAME"
	EnvSlugIndexName              = "CATALOG_SLUG_INDEX_NAME"
	EnvPublicIndexName            = "CATALOG_PUBLIC_INDEX_NAME"
	EnvRecentIndexName            = "CATALOG_RECENT_INDEX_NAME"
	EnvEntityIndexName            = "CATALOG_ENTITY_INDEX_NAME"
	EnvProductImagePlaceholderURL = "PRODUCT_IMAGE_PLACEHOLDER_URL"

	DefaultSlugIndexName   = "slug-index"
	DefaultPublicIndexName = "public-index"
	DefaultRecentIndexName = "recent-index"
	DefaultEntityIndexName = "entity-index"

	DefaultProductImagePlaceholderURL = "/images/placeholder-product.jpg"
)

type Status string

const (
	StatusActive   Status = "active"
	StatusDraft    Status = "draft"
	StatusArchived Status = "archived"
)

type Product struct {
	ID            string
	Slug          string
	Name          string
	Description   string
	PriceCents    int
	ImageURL      string
	Status        Status
	SortOrder     int
	StockQuantity int
	Version       int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CategorySlugs []string
	Variants      []ProductVariant
}

type ProductVariant struct {
	ID            string `dynamodbav:"id,omitempty"`
	Label         string `dynamodbav:"label,omitempty"`
	StockQuantity int    `dynamodbav:"stock_quantity"`
	Status        Status `dynamodbav:"status,omitempty"`
	SortOrder     int    `dynamodbav:"sort_order"`
}

func (p Product) OutOfStock() bool {
	return p.TotalAvailableStock() <= 0
}

func (p Product) UsesVariants() bool {
	return len(p.Variants) > 0
}

func (p Product) ActiveVariants() []ProductVariant {
	variants := make([]ProductVariant, 0, len(p.Variants))
	for _, variant := range p.Variants {
		if variant.Status == StatusActive {
			variants = append(variants, variant)
		}
	}
	sort.SliceStable(variants, func(i int, j int) bool {
		return variants[i].SortOrder < variants[j].SortOrder
	})

	return variants
}

func (p Product) AvailableStockForVariant(variantID string) (int, bool) {
	if !p.UsesVariants() {
		if variantID == "" {
			return p.StockQuantity, true
		}
		return 0, false
	}

	for _, variant := range p.Variants {
		if variant.ID == variantID && variant.Status == StatusActive {
			return variant.StockQuantity, true
		}
	}

	return 0, false
}

func (p Product) TotalAvailableStock() int {
	if !p.UsesVariants() {
		return p.StockQuantity
	}

	total := 0
	for _, variant := range p.Variants {
		if variant.Status == StatusActive {
			total += variant.StockQuantity
		}
	}

	return total
}

func (p Product) Validate() error {
	activeLabels := make([]string, 0, len(p.Variants))
	activeVariantIDs := map[string]string{}
	for _, variant := range p.Variants {
		if variant.Status != StatusActive {
			continue
		}

		label := strings.TrimSpace(variant.Label)
		if label == "" {
			return fmt.Errorf("active variant label is required")
		}

		for _, activeLabel := range activeLabels {
			if strings.EqualFold(activeLabel, label) {
				return fmt.Errorf("duplicate active variant label %q", label)
			}
		}
		activeLabels = append(activeLabels, label)

		variantID := strings.TrimSpace(variant.ID)
		if variantID == "" {
			return fmt.Errorf("active variant ID is required for %q", label)
		}
		if existingLabel, exists := activeVariantIDs[variantID]; exists {
			return fmt.Errorf("duplicate active variant ID %q for %q and %q", variantID, existingLabel, label)
		}
		activeVariantIDs[variantID] = label
	}

	return nil
}

func ValidRouteSlug(slug string) bool {
	if slug == "" {
		return false
	}
	for _, r := range slug {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func (p Product) DisplayImageURL(fallback string) string {
	if strings.TrimSpace(p.ImageURL) != "" {
		return p.ImageURL
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}

	return DefaultProductImagePlaceholderURL
}

type Category struct {
	Slug        string
	Name        string
	Description string
	Status      Status
	SortOrder   int
	Version     int
	UpdatedAt   time.Time
}

type Store interface {
	ListActiveProducts(ctx context.Context, limit int) ([]Product, error)
	ListRecentlyAddedProducts(ctx context.Context, limit int) ([]Product, error)
	GetProductBySlug(ctx context.Context, slug string) (Product, bool, error)
	ListActiveCategories(ctx context.Context) ([]Category, error)
	ListActiveProductsByCategory(ctx context.Context, categorySlug string, limit int) ([]Product, error)
}

type AdminStore interface {
	ListProducts(ctx context.Context) ([]Product, error)
	GetProductByID(ctx context.Context, productID string) (Product, bool, error)
	ListCategories(ctx context.Context) ([]Category, error)
	CreateProduct(ctx context.Context, product Product) (Product, error)
	UpdateProduct(ctx context.Context, previous Product, product Product) (Product, error)
	ArchiveProduct(ctx context.Context, product Product) (Product, error)
	CreateCategory(ctx context.Context, category Category) (Category, error)
	UpdateCategory(ctx context.Context, category Category, expectedVersion int) (Category, error)
	ArchiveCategory(ctx context.Context, category Category) (Category, error)
}

type EmptyStore struct{}

func (EmptyStore) ListActiveProducts(ctx context.Context, limit int) ([]Product, error) {
	return []Product{}, nil
}

func (EmptyStore) ListRecentlyAddedProducts(ctx context.Context, limit int) ([]Product, error) {
	return []Product{}, nil
}

func (EmptyStore) GetProductBySlug(ctx context.Context, slug string) (Product, bool, error) {
	return Product{}, false, nil
}

func (EmptyStore) ListActiveCategories(ctx context.Context) ([]Category, error) {
	return []Category{}, nil
}

func (EmptyStore) ListActiveProductsByCategory(ctx context.Context, categorySlug string, limit int) ([]Product, error) {
	return []Product{}, nil
}

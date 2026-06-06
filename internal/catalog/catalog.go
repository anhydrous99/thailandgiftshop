package catalog

import (
	"context"
	"strings"
	"time"
)

const (
	EnvTableName                  = "CATALOG_TABLE_NAME"
	EnvSlugIndexName              = "CATALOG_SLUG_INDEX_NAME"
	EnvPublicIndexName            = "CATALOG_PUBLIC_INDEX_NAME"
	EnvProductImagePlaceholderURL = "PRODUCT_IMAGE_PLACEHOLDER_URL"

	DefaultSlugIndexName   = "slug-index"
	DefaultPublicIndexName = "public-index"

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
	UpdatedAt     time.Time
	CategorySlugs []string
}

func (p Product) OutOfStock() bool {
	return p.StockQuantity <= 0
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
	UpdatedAt   time.Time
}

type Store interface {
	ListActiveProducts(ctx context.Context, limit int) ([]Product, error)
	GetProductBySlug(ctx context.Context, slug string) (Product, bool, error)
	ListActiveCategories(ctx context.Context) ([]Category, error)
	ListActiveProductsByCategory(ctx context.Context, categorySlug string, limit int) ([]Product, error)
}

type EmptyStore struct{}

func (EmptyStore) ListActiveProducts(ctx context.Context, limit int) ([]Product, error) {
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

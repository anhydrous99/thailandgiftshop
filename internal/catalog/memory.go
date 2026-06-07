package catalog

import (
	"context"
	"slices"
	"sort"
	"time"
)

var _ Store = (*MemoryStore)(nil)

type MemoryStore struct {
	products   []Product
	categories []Category
}

func NewMemoryStore(products []Product, categories []Category) *MemoryStore {
	return &MemoryStore{
		products:   cloneProducts(products),
		categories: slices.Clone(categories),
	}
}

func NewDemoStore() Store {
	return NewMemoryStore(DemoCatalogProducts(), DemoCatalogCategories())
}

func (s *MemoryStore) ListActiveProducts(ctx context.Context, limit int) ([]Product, error) {
	products := make([]Product, 0, len(s.products))
	for _, product := range s.products {
		if product.Status != StatusActive {
			continue
		}
		products = append(products, cloneProduct(product))
		if positiveLimitReached(products, limit) {
			return products, nil
		}
	}

	return products, nil
}

func (s *MemoryStore) ListRecentlyAddedProducts(ctx context.Context, limit int) ([]Product, error) {
	products := make([]Product, 0, len(s.products))
	for _, product := range s.products {
		if product.Status == StatusActive {
			products = append(products, cloneProduct(product))
		}
	}
	sort.SliceStable(products, func(i int, j int) bool {
		return memoryProductRecentTime(products[i]).After(memoryProductRecentTime(products[j]))
	})

	return applyProductLimit(products, limit), nil
}

func (s *MemoryStore) GetProductBySlug(ctx context.Context, slug string) (Product, bool, error) {
	for _, product := range s.products {
		if product.Slug == slug {
			return cloneProduct(product), true, nil
		}
	}

	return Product{}, false, nil
}

func (s *MemoryStore) ListActiveCategories(ctx context.Context) ([]Category, error) {
	categories := make([]Category, 0, len(s.categories))
	for _, category := range s.categories {
		if category.Status == StatusActive {
			categories = append(categories, category)
		}
	}

	return categories, nil
}

func (s *MemoryStore) ListActiveProductsByCategory(ctx context.Context, categorySlug string, limit int) ([]Product, error) {
	products := make([]Product, 0, len(s.products))
	for _, product := range s.products {
		if product.Status != StatusActive || !slices.Contains(product.CategorySlugs, categorySlug) {
			continue
		}
		products = append(products, cloneProduct(product))
		if positiveLimitReached(products, limit) {
			return products, nil
		}
	}

	return products, nil
}

func cloneProducts(products []Product) []Product {
	cloned := make([]Product, len(products))
	for i, product := range products {
		cloned[i] = cloneProduct(product)
	}
	return cloned
}

func cloneProduct(product Product) Product {
	product.CategorySlugs = slices.Clone(product.CategorySlugs)
	return product
}

func memoryProductRecentTime(product Product) time.Time {
	if !product.CreatedAt.IsZero() {
		return product.CreatedAt
	}
	return product.UpdatedAt
}

func applyProductLimit(products []Product, limit int) []Product {
	if limit > 0 && len(products) > limit {
		return products[:limit]
	}
	return products
}

func positiveLimitReached[T any](items []T, limit int) bool {
	return limit > 0 && len(items) >= limit
}

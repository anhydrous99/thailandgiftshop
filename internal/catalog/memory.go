package catalog

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

var _ Store = (*MemoryStore)(nil)
var _ AdminStore = (*MemoryStore)(nil)

type MemoryStore struct {
	mu         sync.RWMutex
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
	s.mu.RLock()
	defer s.mu.RUnlock()

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
	s.mu.RLock()
	defer s.mu.RUnlock()

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
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, product := range s.products {
		if product.Slug == slug {
			return cloneProduct(product), true, nil
		}
	}

	return Product{}, false, nil
}

func (s *MemoryStore) ListActiveCategories(ctx context.Context) ([]Category, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	categories := make([]Category, 0, len(s.categories))
	for _, category := range s.categories {
		if category.Status == StatusActive {
			categories = append(categories, category)
		}
	}
	sort.SliceStable(categories, func(i int, j int) bool {
		if categories[i].SortOrder == categories[j].SortOrder {
			return categories[i].Name < categories[j].Name
		}
		return categories[i].SortOrder < categories[j].SortOrder
	})

	return categories, nil
}

func (s *MemoryStore) ListActiveProductsByCategory(ctx context.Context, categorySlug string, limit int) ([]Product, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

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

func (s *MemoryStore) ListProducts(ctx context.Context) ([]Product, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	products := cloneProducts(s.products)
	sort.SliceStable(products, func(i int, j int) bool {
		if products[i].SortOrder == products[j].SortOrder {
			return products[i].Name < products[j].Name
		}
		return products[i].SortOrder < products[j].SortOrder
	})
	return products, nil
}

func (s *MemoryStore) GetProductByID(ctx context.Context, productID string) (Product, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, product := range s.products {
		if product.ID == productID {
			return cloneProduct(product), true, nil
		}
	}
	return Product{}, false, nil
}

func (s *MemoryStore) ListCategories(ctx context.Context) ([]Category, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	categories := slices.Clone(s.categories)
	sort.SliceStable(categories, func(i int, j int) bool {
		if categories[i].SortOrder == categories[j].SortOrder {
			return categories[i].Name < categories[j].Name
		}
		return categories[i].SortOrder < categories[j].SortOrder
	})
	return categories, nil
}

func (s *MemoryStore) CreateProduct(ctx context.Context, product Product) (Product, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := product.Validate(); err != nil {
		return Product{}, err
	}
	for _, existing := range s.products {
		if strings.EqualFold(existing.Slug, product.Slug) {
			return Product{}, ErrSlugConflict
		}
	}
	product.Version = 1
	s.products = append(s.products, cloneProduct(product))
	return cloneProduct(product), nil
}

func (s *MemoryStore) UpdateProduct(ctx context.Context, previous Product, product Product) (Product, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := product.Validate(); err != nil {
		return Product{}, err
	}
	index := -1
	for i, existing := range s.products {
		if existing.ID == previous.ID {
			index = i
			if existing.Version != previous.Version {
				return Product{}, ErrVersionConflict
			}
			continue
		}
		if strings.EqualFold(existing.Slug, product.Slug) {
			return Product{}, ErrSlugConflict
		}
	}
	if index == -1 {
		return Product{}, errors.New("catalog product not found")
	}
	product.Version = previous.Version + 1
	if product.CreatedAt.IsZero() {
		product.CreatedAt = previous.CreatedAt
	}
	s.products[index] = cloneProduct(product)
	return cloneProduct(product), nil
}

func (s *MemoryStore) ArchiveProduct(ctx context.Context, product Product) (Product, error) {
	archived := product
	archived.Status = StatusArchived
	return s.UpdateProduct(ctx, product, archived)
}

func (s *MemoryStore) CreateCategory(ctx context.Context, category Category) (Category, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.categories {
		if strings.EqualFold(existing.Slug, category.Slug) {
			return Category{}, ErrSlugConflict
		}
	}
	category.Version = 1
	s.categories = append(s.categories, category)
	return category, nil
}

func (s *MemoryStore) UpdateCategory(ctx context.Context, category Category, expectedVersion int) (Category, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, existing := range s.categories {
		if existing.Slug == category.Slug {
			if existing.Version != expectedVersion {
				return Category{}, ErrVersionConflict
			}
			category.Version = expectedVersion + 1
			s.categories[i] = category
			return category, nil
		}
	}
	return Category{}, errors.New("catalog category not found")
}

func (s *MemoryStore) ArchiveCategory(ctx context.Context, category Category) (Category, error) {
	archived := category
	archived.Status = StatusArchived
	return s.UpdateCategory(ctx, archived, category.Version)
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
	product.Variants = slices.Clone(product.Variants)
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

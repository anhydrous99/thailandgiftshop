package catalog

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const DemoSeedGroup = "demo"

var demoUpdatedAt = time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

func demoProductCreatedAt(daysBeforeUpdate int) time.Time {
	return demoUpdatedAt.AddDate(0, 0, -daysBeforeUpdate)
}

type DemoSeedCounts struct {
	Categories          int
	Products            int
	ProductSlugLockRows int
	CategoryProductRows int
	Items               int
}

func DemoCatalogCategories() []Category {
	return []Category{
		{
			Slug:        "market-finds",
			Name:        "Bangkok Market Finds",
			Description: "Popular Thai snacks, keepsakes, pantry items, and compact travel finds.",
			Status:      StatusActive,
			SortOrder:   10,
			UpdatedAt:   demoUpdatedAt,
		},
		{
			Slug:        "pantry",
			Name:        "Thai Pantry",
			Description: "Sauces, curry staples, tea, snacks, and shelf-stable pantry favorites.",
			Status:      StatusActive,
			SortOrder:   20,
			UpdatedAt:   demoUpdatedAt,
		},
		{
			Slug:        "snacks-sweets",
			Name:        "Snacks & Sweets",
			Description: "Thai tea treats, coconut sweets, fruit candies, and market snacks.",
			Status:      StatusActive,
			SortOrder:   30,
			UpdatedAt:   demoUpdatedAt,
		},
		{
			Slug:        "home-decor",
			Name:        "Home Decor",
			Description: "Small decorative pieces, tableware, candles, and handmade home accents.",
			Status:      StatusActive,
			SortOrder:   40,
			UpdatedAt:   demoUpdatedAt,
		},
		{
			Slug:        "textiles",
			Name:        "Textiles",
			Description: "Scarves, pouches, cotton goods, and easy-to-pack woven pieces.",
			Status:      StatusActive,
			SortOrder:   50,
			UpdatedAt:   demoUpdatedAt,
		},
		{
			Slug:        "wellness",
			Name:        "Wellness",
			Description: "Lemongrass, jasmine, spa, and aromatherapy finds inspired by Thai markets.",
			Status:      StatusActive,
			SortOrder:   60,
			UpdatedAt:   demoUpdatedAt,
		},
		{
			Slug:        "souvenirs",
			Name:        "Souvenirs",
			Description: "Lightweight keepsakes and travel-inspired souvenirs for easy shipping.",
			Status:      StatusActive,
			SortOrder:   70,
			UpdatedAt:   demoUpdatedAt,
		},
	}
}

func DemoCatalogProducts() []Product {
	return []Product{
		{
			ID:            "prod_001",
			Slug:          "mango-sticky-rice-kit",
			Name:          "Mango Sticky Rice Treats",
			Description:   "Coconut cream, sweet rice, mango candy, and a printed recipe card inspired by a classic Thai dessert.",
			PriceCents:    2899,
			ImageURL:      "/images/products/mango-sticky-rice-kit.jpg",
			Status:        StatusActive,
			SortOrder:     10,
			StockQuantity: 18,
			CreatedAt:     demoProductCreatedAt(18),
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"market-finds", "snacks-sweets"},
		},
		{
			ID:            "prod_002",
			Slug:          "thai-tea-sampler",
			Name:          "Thai Tea Selection",
			Description:   "Loose leaf Thai tea, condensed milk sweets, and two spice-forward blends for iced tea at home.",
			PriceCents:    2199,
			ImageURL:      "/images/products/thai-tea-sampler.jpg",
			Status:        StatusActive,
			SortOrder:     20,
			StockQuantity: 31,
			CreatedAt:     demoProductCreatedAt(16),
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"market-finds", "pantry", "snacks-sweets"},
		},
		{
			ID:            "prod_003",
			Slug:          "teak-elephant-carving",
			Name:          "Teak Elephant Carving",
			Description:   "A compact hand-carved teak accent sized for desks, bookshelves, and travel-friendly displays.",
			PriceCents:    3999,
			ImageURL:      "/images/products/teak-elephant-carving.jpg",
			Status:        StatusActive,
			SortOrder:     30,
			StockQuantity: 9,
			CreatedAt:     demoProductCreatedAt(14),
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"home-decor", "souvenirs"},
		},
		{
			ID:            "prod_004",
			Slug:          "lemongrass-spa-bundle",
			Name:          "Lemongrass Spa Essentials",
			Description:   "Soap, body oil, and a small incense pack with bright lemongrass and kaffir lime notes.",
			PriceCents:    3499,
			ImageURL:      "/images/products/lemongrass-spa-bundle.jpg",
			Status:        StatusActive,
			SortOrder:     40,
			StockQuantity: 22,
			CreatedAt:     demoProductCreatedAt(12),
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"market-finds", "wellness"},
		},
		{
			ID:            "prod_005",
			Slug:          "handwoven-indigo-scarf",
			Name:          "Handwoven Indigo Scarf",
			Description:   "A soft cotton scarf with a deep indigo pattern inspired by northern Thai weaving traditions.",
			PriceCents:    4599,
			ImageURL:      "/images/products/handwoven-indigo-scarf.jpg",
			Status:        StatusActive,
			SortOrder:     50,
			StockQuantity: 3,
			CreatedAt:     demoProductCreatedAt(10),
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"textiles"},
			Variants: []ProductVariant{
				{ID: "var_005_s", Label: "S", StockQuantity: 1, Status: StatusActive, SortOrder: 10},
				{ID: "var_005_m", Label: "M", StockQuantity: 2, Status: StatusActive, SortOrder: 20},
				{ID: "var_005_xl", Label: "XL", StockQuantity: 0, Status: StatusActive, SortOrder: 30},
			},
		},
		{
			ID:            "prod_006",
			Slug:          "coconut-curry-pantry-box",
			Name:          "Coconut Curry Pantry Picks",
			Description:   "Panang curry paste, coconut milk powder, rice noodles, and finishing aromatics for quick dinners.",
			PriceCents:    3299,
			ImageURL:      "/images/products/coconut-curry-pantry-box.jpg",
			Status:        StatusActive,
			SortOrder:     60,
			StockQuantity: 25,
			CreatedAt:     demoProductCreatedAt(8),
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"pantry"},
		},
		{
			ID:            "prod_007",
			Slug:          "benjarong-cup-set",
			Name:          "Benjarong Tea Cups",
			Description:   "Two patterned cups with jewel-toned details for tea, espresso, or a small shelf display.",
			PriceCents:    5299,
			ImageURL:      "/images/products/benjarong-cup-set.jpg",
			Status:        StatusActive,
			SortOrder:     70,
			StockQuantity: 6,
			CreatedAt:     demoProductCreatedAt(6),
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"home-decor"},
		},
		{
			ID:            "prod_008",
			Slug:          "ceramic-tuk-tuk-magnet-set",
			Name:          "Ceramic Tuk Tuk Magnets",
			Description:   "A trio of glazed tuk tuk magnets in market-bright colors for souvenir bags.",
			PriceCents:    1499,
			ImageURL:      "/images/products/ceramic-tuk-tuk-magnet-set.jpg",
			Status:        StatusActive,
			SortOrder:     80,
			StockQuantity: 40,
			CreatedAt:     demoProductCreatedAt(5),
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"home-decor", "souvenirs"},
		},
		{
			ID:            "prod_009",
			Slug:          "jasmine-rice-candle",
			Name:          "Jasmine Rice Candle",
			Description:   "A small soy candle with jasmine, steamed rice, and white floral notes.",
			PriceCents:    1899,
			ImageURL:      "/images/products/jasmine-rice-candle.jpg",
			Status:        StatusActive,
			SortOrder:     90,
			StockQuantity: 16,
			CreatedAt:     demoProductCreatedAt(4),
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"home-decor", "wellness"},
		},
		{
			ID:            "prod_010",
			Slug:          "nam-prik-tasting-flight",
			Name:          "Nam Prik Tasting Flight",
			Description:   "Three chili relishes with crackers and a guide to pairing with rice, vegetables, and grilled snacks.",
			PriceCents:    2799,
			ImageURL:      "/images/products/nam-prik-tasting-flight.jpg",
			Status:        StatusActive,
			SortOrder:     100,
			StockQuantity: 0,
			CreatedAt:     demoProductCreatedAt(3),
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"pantry", "snacks-sweets"},
		},
		{
			ID:            "prod_011",
			Slug:          "elephant-pouch-set",
			Name:          "Elephant Cotton Pouches",
			Description:   "Two cotton zipper pouches with elephant linework for cards, cables, or small souvenirs.",
			PriceCents:    2499,
			ImageURL:      "/images/products/elephant-pouch-set.jpg",
			Status:        StatusActive,
			SortOrder:     110,
			StockQuantity: 27,
			CreatedAt:     demoProductCreatedAt(1),
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"textiles", "souvenirs"},
		},
		{
			ID:            "prod_012",
			Slug:          "brass-temple-bell",
			Name:          "Brass Temple Bell",
			Description:   "A small brass bell with a warm tone, currently held as a draft item for layout testing.",
			PriceCents:    3799,
			ImageURL:      "/images/products/brass-temple-bell.jpg",
			Status:        StatusDraft,
			SortOrder:     120,
			StockQuantity: 4,
			CreatedAt:     demoProductCreatedAt(0),
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"home-decor"},
		},
	}
}

func DemoCatalogSeedItems() ([]map[string]types.AttributeValue, error) {
	categories := DemoCatalogCategories()
	products := DemoCatalogProducts()
	counts := DemoCatalogSeedCounts()
	items := make([]map[string]types.AttributeValue, 0, counts.Items)

	for _, category := range categories {
		item, err := categoryItem(category)
		if err != nil {
			return nil, err
		}
		items = append(items, demoSeedItem(item))
	}

	for _, product := range products {
		item, err := productItem(product)
		if err != nil {
			return nil, err
		}
		items = append(items, demoSeedItem(item))

		item, err = productSlugLockItem(product)
		if err != nil {
			return nil, err
		}
		items = append(items, demoSeedItem(item))

		for _, categorySlug := range product.CategorySlugs {
			item, err := categoryProductItem(categorySlug, product)
			if err != nil {
				return nil, err
			}
			items = append(items, demoSeedItem(item))
		}
	}

	return items, nil
}

func DemoCatalogSeedCounts() DemoSeedCounts {
	products := DemoCatalogProducts()
	categoryProductRows := 0
	for _, product := range products {
		categoryProductRows += len(product.CategorySlugs)
	}

	categories := DemoCatalogCategories()
	return DemoSeedCounts{
		Categories:          len(categories),
		Products:            len(products),
		ProductSlugLockRows: len(products),
		CategoryProductRows: categoryProductRows,
		Items:               len(categories) + len(products)*2 + categoryProductRows,
	}
}

func demoSeedItem(item map[string]types.AttributeValue) map[string]types.AttributeValue {
	item["seed_group"] = &types.AttributeValueMemberS{Value: DemoSeedGroup}
	return item
}

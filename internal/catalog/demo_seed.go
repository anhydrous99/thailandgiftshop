package catalog

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const DemoSeedGroup = "demo"

var demoUpdatedAt = time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

type DemoSeedCounts struct {
	Categories          int
	Products            int
	CategoryProductRows int
	Items               int
}

func DemoCatalogCategories() []Category {
	return []Category{
		{
			Slug:        "gift-sets",
			Name:        "Gift Sets",
			Description: "Ready-to-wrap collections built around Thai pantry, spa, and souvenir favorites.",
			Status:      StatusActive,
			SortOrder:   10,
			UpdatedAt:   demoUpdatedAt,
		},
		{
			Slug:        "pantry",
			Name:        "Thai Pantry",
			Description: "Sauces, spice kits, tea, curry staples, and shelf-stable cooking gifts.",
			Status:      StatusActive,
			SortOrder:   20,
			UpdatedAt:   demoUpdatedAt,
		},
		{
			Slug:        "snacks-sweets",
			Name:        "Snacks & Sweets",
			Description: "Thai tea treats, coconut sweets, fruit candies, and snackable tasting boxes.",
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
			Description: "Scarves, pouches, cotton goods, and easy-to-gift woven pieces.",
			Status:      StatusActive,
			SortOrder:   50,
			UpdatedAt:   demoUpdatedAt,
		},
		{
			Slug:        "wellness",
			Name:        "Wellness",
			Description: "Lemongrass, jasmine, spa, and aromatherapy gifts inspired by Thai markets.",
			Status:      StatusActive,
			SortOrder:   60,
			UpdatedAt:   demoUpdatedAt,
		},
		{
			Slug:        "souvenirs",
			Name:        "Souvenirs",
			Description: "Lightweight keepsakes and travel-inspired gifts for easy shipping.",
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
			Name:          "Mango Sticky Rice Kit",
			Description:   "Coconut cream, sweet rice, mango candy, and a printed recipe card for a classic Thai dessert night.",
			PriceCents:    2899,
			ImageURL:      "/static/products/mango-sticky-rice-kit.jpg",
			Status:        StatusActive,
			SortOrder:     10,
			StockQuantity: 18,
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"gift-sets", "snacks-sweets"},
		},
		{
			ID:            "prod_002",
			Slug:          "thai-tea-sampler",
			Name:          "Thai Tea Sampler",
			Description:   "Loose leaf Thai tea, condensed milk sweets, and two spice-forward blends for iced tea testing.",
			PriceCents:    2199,
			ImageURL:      "/static/products/thai-tea-sampler.jpg",
			Status:        StatusActive,
			SortOrder:     20,
			StockQuantity: 31,
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"gift-sets", "pantry", "snacks-sweets"},
		},
		{
			ID:            "prod_003",
			Slug:          "teak-elephant-carving",
			Name:          "Teak Elephant Carving",
			Description:   "A compact hand-carved teak accent sized for desks, bookshelves, and travel gift boxes.",
			PriceCents:    3999,
			ImageURL:      "/static/products/teak-elephant-carving.jpg",
			Status:        StatusActive,
			SortOrder:     30,
			StockQuantity: 9,
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"home-decor", "souvenirs"},
		},
		{
			ID:            "prod_004",
			Slug:          "lemongrass-spa-bundle",
			Name:          "Lemongrass Spa Bundle",
			Description:   "Soap, body oil, and a small incense pack with bright lemongrass and kaffir lime notes.",
			PriceCents:    3499,
			ImageURL:      "/static/products/lemongrass-spa-bundle.jpg",
			Status:        StatusActive,
			SortOrder:     40,
			StockQuantity: 22,
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"gift-sets", "wellness"},
		},
		{
			ID:            "prod_005",
			Slug:          "handwoven-indigo-scarf",
			Name:          "Handwoven Indigo Scarf",
			Description:   "A soft cotton scarf with a deep indigo pattern inspired by northern Thai weaving traditions.",
			PriceCents:    4599,
			ImageURL:      "/static/products/handwoven-indigo-scarf.jpg",
			Status:        StatusActive,
			SortOrder:     50,
			StockQuantity: 14,
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"textiles"},
		},
		{
			ID:            "prod_006",
			Slug:          "coconut-curry-pantry-box",
			Name:          "Coconut Curry Pantry Box",
			Description:   "Panang curry paste, coconut milk powder, rice noodles, and finishing aromatics for quick dinners.",
			PriceCents:    3299,
			ImageURL:      "/static/products/coconut-curry-pantry-box.jpg",
			Status:        StatusActive,
			SortOrder:     60,
			StockQuantity: 25,
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"pantry"},
		},
		{
			ID:            "prod_007",
			Slug:          "benjarong-cup-set",
			Name:          "Benjarong Cup Set",
			Description:   "Two patterned cups with jewel-toned details for tea, espresso, or a small shelf display.",
			PriceCents:    5299,
			ImageURL:      "/static/products/benjarong-cup-set.jpg",
			Status:        StatusActive,
			SortOrder:     70,
			StockQuantity: 6,
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"home-decor"},
		},
		{
			ID:            "prod_008",
			Slug:          "ceramic-tuk-tuk-magnet-set",
			Name:          "Ceramic Tuk Tuk Magnet Set",
			Description:   "A trio of glazed tuk tuk magnets in market-bright colors for travel-inspired gift bags.",
			PriceCents:    1499,
			ImageURL:      "/static/products/ceramic-tuk-tuk-magnet-set.jpg",
			Status:        StatusActive,
			SortOrder:     80,
			StockQuantity: 40,
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"home-decor", "souvenirs"},
		},
		{
			ID:            "prod_009",
			Slug:          "jasmine-rice-candle",
			Name:          "Jasmine Rice Candle",
			Description:   "A small soy candle with jasmine, steamed rice, and white floral notes.",
			PriceCents:    1899,
			ImageURL:      "/static/products/jasmine-rice-candle.jpg",
			Status:        StatusActive,
			SortOrder:     90,
			StockQuantity: 16,
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"home-decor", "wellness"},
		},
		{
			ID:            "prod_010",
			Slug:          "nam-prik-tasting-flight",
			Name:          "Nam Prik Tasting Flight",
			Description:   "Three chili relishes with crackers and a guide to pairing with rice, vegetables, and grilled snacks.",
			PriceCents:    2799,
			ImageURL:      "/static/products/nam-prik-tasting-flight.jpg",
			Status:        StatusActive,
			SortOrder:     100,
			StockQuantity: 0,
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"pantry", "snacks-sweets"},
		},
		{
			ID:            "prod_011",
			Slug:          "elephant-pouch-set",
			Name:          "Elephant Pouch Set",
			Description:   "Two cotton zipper pouches with elephant linework for cards, cables, or small souvenirs.",
			PriceCents:    2499,
			ImageURL:      "/static/products/elephant-pouch-set.jpg",
			Status:        StatusActive,
			SortOrder:     110,
			StockQuantity: 27,
			UpdatedAt:     demoUpdatedAt,
			CategorySlugs: []string{"textiles", "souvenirs"},
		},
		{
			ID:            "prod_012",
			Slug:          "brass-temple-bell",
			Name:          "Brass Temple Bell",
			Description:   "A small brass bell with a warm tone, currently held as a draft item for layout testing.",
			PriceCents:    3799,
			ImageURL:      "/static/products/brass-temple-bell.jpg",
			Status:        StatusDraft,
			SortOrder:     120,
			StockQuantity: 4,
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
		CategoryProductRows: categoryProductRows,
		Items:               len(categories) + len(products) + categoryProductRows,
	}
}

func demoSeedItem(item map[string]types.AttributeValue) map[string]types.AttributeValue {
	item["seed_group"] = &types.AttributeValueMemberS{Value: DemoSeedGroup}
	return item
}

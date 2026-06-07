package admin

import (
	"context"
	"strings"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
)

type adminDashboardViewModel struct {
	CSRFValue        string
	TotalProducts    int
	ActiveProducts   int
	DraftProducts    int
	ArchivedProducts int
	Categories       int
	LowStockVariants int
	UploadFailures   int
}

func (h *Handler) dashboardViewModel(ctx context.Context, csrfValue string) (adminDashboardViewModel, error) {
	store := h.catalogStore()
	products, err := store.ListProducts(ctx)
	if err != nil {
		return adminDashboardViewModel{}, err
	}
	categories, err := store.ListCategories(ctx)
	if err != nil {
		return adminDashboardViewModel{}, err
	}

	vm := adminDashboardViewModel{CSRFValue: csrfValue, TotalProducts: len(products), Categories: len(categories)}
	for _, product := range products {
		switch product.Status {
		case catalog.StatusActive:
			vm.ActiveProducts++
		case catalog.StatusDraft:
			vm.DraftProducts++
		case catalog.StatusArchived:
			vm.ArchivedProducts++
		}
		if product.Status == catalog.StatusArchived {
			continue
		}
		for _, variant := range product.Variants {
			if variant.Status == catalog.StatusActive && variant.StockQuantity <= 2 {
				vm.LowStockVariants++
			}
		}
	}
	return vm, nil
}

func renderAdminDashboard(ctx context.Context, vm adminDashboardViewModel) (string, error) {
	var body strings.Builder
	if err := adminDashboardPage(vm).Render(ctx, &body); err != nil {
		return "", err
	}
	return body.String(), nil
}

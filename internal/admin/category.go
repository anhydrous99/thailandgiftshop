package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/aws/aws-lambda-go/events"
)

const adminCategoryAllowedMethods = http.MethodGet + ", " + http.MethodHead + ", " + http.MethodPost

type adminCategoryListViewModel struct {
	CSRFValue  string
	Categories []catalog.Category
	Filters    adminCategoryFilters
	Flash      string
}

type adminCategoryFormViewModel struct {
	CSRFValue      string
	Mode           string
	Action         string
	Title          string
	SubmitText     string
	Category       catalog.Category
	Products       []catalog.Product
	MemberProducts []string
	Errors         []string
	Flash          string
}

type adminCategoryFilters struct {
	Status string
	Search string
}

func isAdminCategoryPath(path string) bool {
	if path == "/admin/categories" || path == "/admin/categories/new" {
		return true
	}
	return strings.HasPrefix(path, "/admin/categories/")
}

func (h *Handler) handleAdminCategories(ctx context.Context, path string, request events.APIGatewayV2HTTPRequest, session adminSession) events.APIGatewayV2HTTPResponse {
	method := requestMethod(request)
	csrfValue, cookies, err := h.csrfForProtectedResponse(ctx, request, session)
	if err != nil {
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	store := h.catalogStore()

	if path == "/admin/categories" {
		if method == http.MethodGet || method == http.MethodHead {
			categories, err := store.ListCategories(ctx)
			if err != nil {
				return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
			}
			filters := categoryFilters(request)
			body, err := renderAdminCategoryList(ctx, adminCategoryListViewModel{CSRFValue: csrfValue, Categories: filterAdminCategories(categories, filters), Filters: filters, Flash: request.QueryStringParameters["saved"]})
			if err != nil {
				return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
			}
			if method == http.MethodHead {
				body = ""
			}
			return adminHTMLResponse(http.StatusOK, body, nil, cookies)
		}
		if method == http.MethodPost {
			return h.createCategory(ctx, request, csrfValue, cookies)
		}
	}

	if path == "/admin/categories/new" {
		if method != http.MethodGet && method != http.MethodHead {
			return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": adminCategoryAllowedMethods}, nil)
		}
		body, err := h.categoryForm(ctx, csrfValue, newCategoryFormViewModel(csrfValue, nil))
		if err != nil {
			return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
		}
		if method == http.MethodHead {
			body = ""
		}
		return adminHTMLResponse(http.StatusOK, body, nil, cookies)
	}

	slug, action, ok := adminCategorySlugAction(path)
	if !ok {
		return adminHTMLResponse(http.StatusNotFound, "Not found", nil, nil)
	}
	category, found, err := adminCategoryBySlug(ctx, store, slug)
	if err != nil {
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	if !found {
		return adminHTMLResponse(http.StatusNotFound, "Not found", nil, nil)
	}

	if action == "edit" {
		if method == http.MethodGet || method == http.MethodHead {
			vm, err := h.editCategoryFormViewModel(ctx, csrfValue, category, nil, request.QueryStringParameters["saved"])
			if err != nil {
				return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
			}
			body, err := h.categoryForm(ctx, csrfValue, vm)
			if err != nil {
				return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
			}
			if method == http.MethodHead {
				body = ""
			}
			return adminHTMLResponse(http.StatusOK, body, nil, cookies)
		}
	}
	if action == "save" && method == http.MethodPost {
		return h.updateCategory(ctx, request, category, csrfValue, cookies)
	}
	if action == "archive" && method == http.MethodPost {
		return h.archiveCategory(ctx, category)
	}

	return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": adminCategoryAllowedMethods}, nil)
}

func adminCategorySlugAction(path string) (string, string, bool) {
	remainder, ok := strings.CutPrefix(path, "/admin/categories/")
	if !ok || remainder == "" {
		return "", "", false
	}
	slug, action, found := strings.Cut(remainder, "/")
	if !found {
		return slug, "save", slug != ""
	}
	if slug == "" || strings.Contains(action, "/") {
		return "", "", false
	}
	return slug, action, action == "edit" || action == "archive"
}

func (h *Handler) createCategory(ctx context.Context, request events.APIGatewayV2HTTPRequest, csrfValue string, cookies []string) events.APIGatewayV2HTTPResponse {
	values, err := formValues(request)
	if err != nil {
		h.recordCatalogWrite(metricEntityCategory, metricOperationCreate, metricOutcomeValidationError)
		return adminHTMLResponse(http.StatusBadRequest, "Invalid form", nil, nil)
	}
	category, selectedProductIDs, errorsList := categoryFromForm(values, catalog.Category{}, h.currentTime().UTC(), true)
	if len(errorsList) > 0 {
		h.recordCatalogWrite(metricEntityCategory, metricOperationCreate, metricOutcomeValidationError)
		vm := newCategoryFormViewModel(csrfValue, errorsList).withCategory(category, selectedProductIDs)
		return h.categoryFormResponse(ctx, csrfValue, cookies, vm, http.StatusBadRequest)
	}
	written, err := h.catalogStore().CreateCategory(ctx, category)
	if err != nil {
		h.recordCatalogWrite(metricEntityCategory, metricOperationCreate, catalogWriteOutcome(err))
		vm := newCategoryFormViewModel(csrfValue, nil).withCategory(category, selectedProductIDs)
		return h.categoryWriteError(ctx, csrfValue, cookies, vm, err)
	}
	if err := h.syncCategoryMemberships(ctx, written.Slug, selectedProductIDs); err != nil {
		h.recordCatalogWrite(metricEntityCategory, metricOperationCreate, catalogWriteOutcome(err))
		vm := newCategoryFormViewModel(csrfValue, nil).withCategory(written, selectedProductIDs)
		return h.categoryWriteError(ctx, csrfValue, cookies, vm, err)
	}
	h.recordCatalogWrite(metricEntityCategory, metricOperationCreate, metricOutcomeSuccess)
	return adminRedirectResponse(http.StatusSeeOther, "/admin/categories/"+url.PathEscape(written.Slug)+"/edit?saved=created", nil)
}

func (h *Handler) updateCategory(ctx context.Context, request events.APIGatewayV2HTTPRequest, previous catalog.Category, csrfValue string, cookies []string) events.APIGatewayV2HTTPResponse {
	values, err := formValues(request)
	if err != nil {
		h.recordCatalogWrite(metricEntityCategory, metricOperationUpdate, metricOutcomeValidationError)
		return adminHTMLResponse(http.StatusBadRequest, "Invalid form", nil, nil)
	}
	if strconv.Itoa(previous.Version) != strings.TrimSpace(values.Get("version")) {
		h.recordCatalogWrite(metricEntityCategory, metricOperationUpdate, metricOutcomeConflict)
		vm, err := h.editCategoryFormViewModel(ctx, csrfValue, previous, []string{"This category changed while you were editing. Reload and try again."}, "")
		if err != nil {
			return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
		}
		return h.categoryFormResponse(ctx, csrfValue, cookies, vm, http.StatusConflict)
	}
	category, selectedProductIDs, errorsList := categoryFromForm(values, previous, h.currentTime().UTC(), false)
	if len(errorsList) > 0 {
		h.recordCatalogWrite(metricEntityCategory, metricOperationUpdate, metricOutcomeValidationError)
		vm := editCategoryFormViewModel(csrfValue, category, selectedProductIDs, errorsList, "")
		return h.categoryFormResponse(ctx, csrfValue, cookies, vm, http.StatusBadRequest)
	}
	written, err := h.catalogStore().UpdateCategory(ctx, category, previous.Version)
	if err != nil {
		h.recordCatalogWrite(metricEntityCategory, metricOperationUpdate, catalogWriteOutcome(err))
		vm := editCategoryFormViewModel(csrfValue, category, selectedProductIDs, nil, "")
		return h.categoryWriteError(ctx, csrfValue, cookies, vm, err)
	}
	if err := h.syncCategoryMemberships(ctx, written.Slug, selectedProductIDs); err != nil {
		h.recordCatalogWrite(metricEntityCategory, metricOperationUpdate, catalogWriteOutcome(err))
		vm := editCategoryFormViewModel(csrfValue, written, selectedProductIDs, nil, "")
		return h.categoryWriteError(ctx, csrfValue, cookies, vm, err)
	}
	h.recordCatalogWrite(metricEntityCategory, metricOperationUpdate, metricOutcomeSuccess)
	return adminRedirectResponse(http.StatusSeeOther, "/admin/categories/"+url.PathEscape(written.Slug)+"/edit?saved=updated", nil)
}

func (h *Handler) archiveCategory(ctx context.Context, category catalog.Category) events.APIGatewayV2HTTPResponse {
	_, err := h.catalogStore().ArchiveCategory(ctx, category)
	if err != nil {
		h.recordCatalogWrite(metricEntityCategory, metricOperationArchive, catalogWriteOutcome(err))
		if errors.Is(err, catalog.ErrVersionConflict) {
			return adminHTMLResponse(http.StatusConflict, "Version conflict", nil, nil)
		}
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	h.recordCatalogWrite(metricEntityCategory, metricOperationArchive, metricOutcomeSuccess)
	return adminRedirectResponse(http.StatusSeeOther, "/admin/categories?status=archived&saved=archived", nil)
}

func (h *Handler) syncCategoryMemberships(ctx context.Context, categorySlug string, selectedProductIDs []string) error {
	products, err := h.catalogStore().ListProducts(ctx)
	if err != nil {
		return err
	}
	selected := make(map[string]bool, len(selectedProductIDs))
	for _, productID := range selectedProductIDs {
		selected[productID] = true
	}
	for _, product := range products {
		hasCategory := slices.Contains(product.CategorySlugs, categorySlug)
		wantsCategory := selected[product.ID]
		if hasCategory == wantsCategory {
			continue
		}
		updated := product
		if wantsCategory {
			updated.CategorySlugs = append(cleanStrings(updated.CategorySlugs), categorySlug)
		} else {
			updated.CategorySlugs = removeString(cleanStrings(updated.CategorySlugs), categorySlug)
		}
		if _, err := h.catalogStore().UpdateProduct(ctx, product, updated); err != nil {
			return err
		}
	}
	return nil
}

func adminCategoryBySlug(ctx context.Context, store catalog.AdminStore, slug string) (catalog.Category, bool, error) {
	categories, err := store.ListCategories(ctx)
	if err != nil {
		return catalog.Category{}, false, err
	}
	for _, category := range categories {
		if category.Slug == slug {
			return category, true, nil
		}
	}
	return catalog.Category{}, false, nil
}

func (h *Handler) categoryWriteError(ctx context.Context, csrfValue string, cookies []string, vm adminCategoryFormViewModel, err error) events.APIGatewayV2HTTPResponse {
	status := http.StatusBadRequest
	message := "Category could not be saved."
	if errors.Is(err, catalog.ErrSlugConflict) {
		message = "Slug is already used by another category."
	} else if errors.Is(err, catalog.ErrVersionConflict) {
		status = http.StatusConflict
		message = "This category changed while you were editing. Reload and try again."
	} else {
		status = http.StatusInternalServerError
		message = "Internal server error"
	}
	vm.Errors = append(vm.Errors, message)
	return h.categoryFormResponse(ctx, csrfValue, cookies, vm, status)
}

func (h *Handler) categoryFormResponse(ctx context.Context, csrfValue string, cookies []string, vm adminCategoryFormViewModel, status int) events.APIGatewayV2HTTPResponse {
	body, err := h.categoryForm(ctx, csrfValue, vm)
	if err != nil {
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	return adminHTMLResponse(status, body, nil, cookies)
}

func (h *Handler) categoryForm(ctx context.Context, csrfValue string, vm adminCategoryFormViewModel) (string, error) {
	products, err := h.catalogStore().ListProducts(ctx)
	if err != nil {
		return "", err
	}
	vm.CSRFValue = csrfValue
	vm.Products = products
	return renderAdminCategoryForm(ctx, vm)
}

func categoryFromForm(values url.Values, previous catalog.Category, now time.Time, create bool) (catalog.Category, []string, []string) {
	category := previous
	if create {
		category = catalog.Category{Version: 0}
	}
	category.Name = strings.TrimSpace(values.Get("name"))
	category.Description = strings.TrimSpace(values.Get("description"))
	category.Status = catalog.Status(strings.TrimSpace(values.Get("status")))
	category.SortOrder = intFromForm(values.Get("sort_order"))
	submittedSlug := strings.TrimSpace(values.Get("slug"))
	if create {
		category.Slug = submittedSlug
	} else if submittedSlug == previous.Slug {
		category.Slug = previous.Slug
	}
	category.UpdatedAt = now.UTC()

	errorsList := validateCategoryForm(category)
	if !create {
		if submittedSlug == "" {
			errorsList = append(errorsList, "Slug is required.")
		} else if submittedSlug != previous.Slug {
			if !catalog.ValidRouteSlug(submittedSlug) {
				errorsList = append(errorsList, "Slug must use only lowercase letters, numbers, and hyphens.")
			}
			errorsList = append(errorsList, "Category slug cannot be changed.")
		}
	}
	return category, cleanStrings(values["product_ids"]), errorsList
}

func validateCategoryForm(category catalog.Category) []string {
	var errorsList []string
	if category.Name == "" {
		errorsList = append(errorsList, "Name is required.")
	}
	if category.Slug == "" {
		errorsList = append(errorsList, "Slug is required.")
	} else if !catalog.ValidRouteSlug(category.Slug) {
		errorsList = append(errorsList, "Slug must use only lowercase letters, numbers, and hyphens.")
	}
	if category.Description == "" {
		errorsList = append(errorsList, "Description is required.")
	}
	if category.Status != catalog.StatusActive && category.Status != catalog.StatusDraft && category.Status != catalog.StatusArchived {
		errorsList = append(errorsList, "Choose active, draft, or archived status.")
	}
	return errorsList
}

func newCategoryFormViewModel(csrfValue string, errorsList []string) adminCategoryFormViewModel {
	return adminCategoryFormViewModel{CSRFValue: csrfValue, Mode: "new", Action: "/admin/categories", Title: "New category", SubmitText: "Create category", Category: catalog.Category{Status: catalog.StatusDraft}, Errors: errorsList}
}

func editCategoryFormViewModel(csrfValue string, category catalog.Category, selectedProductIDs []string, errorsList []string, flash string) adminCategoryFormViewModel {
	return adminCategoryFormViewModel{CSRFValue: csrfValue, Mode: "edit", Action: "/admin/categories/" + url.PathEscape(category.Slug), Title: "Edit category", SubmitText: "Save category", Category: category, MemberProducts: selectedProductIDs, Errors: errorsList, Flash: flash}
}

func (h *Handler) editCategoryFormViewModel(ctx context.Context, csrfValue string, category catalog.Category, errorsList []string, flash string) (adminCategoryFormViewModel, error) {
	products, err := h.catalogStore().ListProducts(ctx)
	if err != nil {
		return adminCategoryFormViewModel{}, err
	}
	selected := make([]string, 0, len(products))
	for _, product := range products {
		if slices.Contains(product.CategorySlugs, category.Slug) {
			selected = append(selected, product.ID)
		}
	}
	return editCategoryFormViewModel(csrfValue, category, selected, errorsList, flash), nil
}

func (vm adminCategoryFormViewModel) withCategory(category catalog.Category, selectedProductIDs []string) adminCategoryFormViewModel {
	vm.Category = category
	vm.MemberProducts = selectedProductIDs
	if vm.Mode == "" || vm.Mode == "new" {
		vm.Mode = "new"
		vm.Action = "/admin/categories"
		vm.Title = "New category"
		vm.SubmitText = "Create category"
	} else {
		vm.Action = "/admin/categories/" + url.PathEscape(category.Slug)
	}
	return vm
}

func categoryFilters(request events.APIGatewayV2HTTPRequest) adminCategoryFilters {
	return adminCategoryFilters{Status: strings.TrimSpace(request.QueryStringParameters["status"]), Search: strings.TrimSpace(request.QueryStringParameters["q"])}
}

func filterAdminCategories(categories []catalog.Category, filters adminCategoryFilters) []catalog.Category {
	filtered := make([]catalog.Category, 0, len(categories))
	search := strings.ToLower(filters.Search)
	for _, category := range categories {
		if filters.Status != "" && string(category.Status) != filters.Status {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(category.Name+" "+category.Slug), search) {
			continue
		}
		filtered = append(filtered, category)
	}
	return filtered
}

func categoryProductChecked(vm adminCategoryFormViewModel, productID string) bool {
	return slices.Contains(vm.MemberProducts, productID)
}

func removeString(values []string, target string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if value != target {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func renderAdminCategoryList(ctx context.Context, vm adminCategoryListViewModel) (string, error) {
	var body strings.Builder
	if err := adminCategoryListPage(vm).Render(ctx, &body); err != nil {
		return "", err
	}
	return body.String(), nil
}

func renderAdminCategoryForm(ctx context.Context, vm adminCategoryFormViewModel) (string, error) {
	var body strings.Builder
	if err := adminCategoryFormPage(vm).Render(ctx, &body); err != nil {
		return "", err
	}
	return body.String(), nil
}

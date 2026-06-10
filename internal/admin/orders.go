package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/checkout"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/aws/aws-lambda-go/events"
)

const adminOrderActionAllowedMethods = http.MethodPost

// adminOrderListLimit caps the newest-first admin order list query (gsi2).
const adminOrderListLimit = 100

// adminOrderOverdueAfter flags pending_payment orders that should have been
// resolved long ago: Stripe Checkout sessions expire after 30 minutes, so a
// pending order older than 45 minutes usually means a missed expiry webhook
// is stranding a stock reservation and needs manual cleanup.
const adminOrderOverdueAfter = 45 * time.Minute

const adminOrderFieldMaxLength = 100

type adminOrderListViewModel struct {
	CSRFValue string
	Orders    []adminOrderRowViewModel
}

type adminOrderRowViewModel struct {
	Order   commerce.Order
	Overdue bool
}

type adminOrderDetailViewModel struct {
	CSRFValue        string
	Order            commerce.Order
	Overdue          bool
	AdvanceTargets   []commerce.OrderStatus
	ShowTrackingForm bool
	ShowCancelForm   bool
	CancelFromPaid   bool
	Flash            string
	RefundNotice     bool
	StockWarning     bool
	Errors           []string
}

func isAdminOrderPath(path string) bool {
	return path == "/admin/orders" || strings.HasPrefix(path, "/admin/orders/")
}

// adminOrderIDAction splits /admin/orders/{id}[/{action}] paths. Order IDs
// are 26-character lowercase Crockford base32 ([a-z0-9]{26}); anything else
// is a 404 before the store is consulted.
func adminOrderIDAction(path string) (string, string, bool) {
	remainder, ok := strings.CutPrefix(path, "/admin/orders/")
	if !ok || remainder == "" {
		return "", "", false
	}
	orderID, action, hasAction := strings.Cut(remainder, "/")
	if !validAdminOrderID(orderID) {
		return "", "", false
	}
	if !hasAction {
		return orderID, "", true
	}
	if action != "advance" && action != "tracking" && action != "cancel" {
		return "", "", false
	}
	return orderID, action, true
}

func validAdminOrderID(orderID string) bool {
	if len(orderID) != 26 {
		return false
	}
	for _, r := range orderID {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func (h *Handler) commerceStore() commerce.Store {
	if h.commerce == nil {
		h.commerce = commerce.NewMemoryStore()
	}
	return h.commerce
}

func (h *Handler) handleAdminOrders(ctx context.Context, path string, request events.APIGatewayV2HTTPRequest, session adminSession) events.APIGatewayV2HTTPResponse {
	method := httpapi.Method(request)
	csrfValue, cookies, err := h.csrfForProtectedResponse(ctx, request, session)
	if err != nil {
		logAdminError("orders: issue csrf token", err)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}

	if path == "/admin/orders" {
		if method != http.MethodGet && method != http.MethodHead {
			return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": adminAllowedMethods}, nil)
		}
		return h.adminOrderListResponse(ctx, method, csrfValue, cookies)
	}

	orderID, action, ok := adminOrderIDAction(path)
	if !ok {
		return adminHTMLResponse(http.StatusNotFound, "Not found", nil, nil)
	}
	order, found, err := h.commerceStore().GetOrder(ctx, orderID)
	if err != nil {
		logAdminError("orders: load order", err)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	if !found {
		return adminHTMLResponse(http.StatusNotFound, "Not found", nil, nil)
	}

	if action == "" {
		if method != http.MethodGet && method != http.MethodHead {
			return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": adminAllowedMethods}, nil)
		}
		vm := h.adminOrderDetailView(order, csrfValue)
		vm.Flash = adminOrderFlashMessage(request.QueryStringParameters["saved"])
		vm.RefundNotice = request.QueryStringParameters["refund"] == "manual"
		vm.StockWarning = request.QueryStringParameters["stock"] == "error"
		body, err := renderAdminOrderDetail(ctx, vm)
		if err != nil {
			logAdminError("orders: render detail", err)
			return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
		}
		if method == http.MethodHead {
			body = ""
		}
		return adminHTMLResponse(http.StatusOK, body, nil, cookies)
	}

	if method != http.MethodPost {
		return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": adminOrderActionAllowedMethods}, nil)
	}
	switch action {
	case "advance":
		return h.advanceOrder(ctx, request, order, csrfValue, cookies)
	case "tracking":
		return h.setOrderTracking(ctx, request, order, csrfValue, cookies)
	case "cancel":
		return h.cancelOrder(ctx, order, csrfValue, cookies)
	}
	return adminHTMLResponse(http.StatusNotFound, "Not found", nil, nil)
}

func (h *Handler) adminOrderListResponse(ctx context.Context, method string, csrfValue string, cookies []string) events.APIGatewayV2HTTPResponse {
	orders, err := h.commerceStore().ListOrders(ctx, adminOrderListLimit)
	if err != nil {
		logAdminError("orders: list", err)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	now := h.currentTime()
	rows := make([]adminOrderRowViewModel, 0, len(orders))
	for _, order := range orders {
		rows = append(rows, adminOrderRowViewModel{Order: order, Overdue: adminOrderOverdue(order, now)})
	}
	body, err := renderAdminOrderList(ctx, adminOrderListViewModel{CSRFValue: csrfValue, Orders: rows})
	if err != nil {
		logAdminError("orders: render list", err)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	if method == http.MethodHead {
		body = ""
	}
	return adminHTMLResponse(http.StatusOK, body, nil, cookies)
}

func (h *Handler) advanceOrder(ctx context.Context, request events.APIGatewayV2HTTPRequest, order commerce.Order, csrfValue string, cookies []string) events.APIGatewayV2HTTPResponse {
	values, err := httpapi.FormValues(request)
	if err != nil {
		return adminHTMLResponse(http.StatusBadRequest, "Invalid form", nil, nil)
	}
	target := commerce.OrderStatus(strings.TrimSpace(values.Get("to_status")))
	if !commerce.AllowedOrderTransition(order.Status, target) || !adminAdvanceTarget(target) {
		return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies, []string{"Choose a valid next status for this order."}, http.StatusBadRequest)
	}
	now := h.currentTime().UTC()
	patch := commerce.OrderPatch{Actor: commerce.OrderActorAdmin}
	switch target {
	case commerce.OrderStatusShipped:
		patch.ShippedAt = &now
	case commerce.OrderStatusDelivered:
		patch.DeliveredAt = &now
	}
	if _, err := h.commerceStore().TransitionOrder(ctx, order.ID, order.Status, target, patch); err != nil {
		return h.orderWriteError(ctx, order, csrfValue, cookies, "orders: advance", err)
	}
	return adminRedirectResponse(http.StatusSeeOther, adminOrderDetailPath(order.ID)+"?saved=advanced", nil)
}

func (h *Handler) setOrderTracking(ctx context.Context, request events.APIGatewayV2HTTPRequest, order commerce.Order, csrfValue string, cookies []string) events.APIGatewayV2HTTPResponse {
	values, err := httpapi.FormValues(request)
	if err != nil {
		return adminHTMLResponse(http.StatusBadRequest, "Invalid form", nil, nil)
	}
	if order.Status != commerce.OrderStatusPaid {
		return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies, []string{"Tracking can only be added to paid orders."}, http.StatusConflict)
	}
	carrier := strings.TrimSpace(values.Get("carrier"))
	trackingNumber := strings.TrimSpace(values.Get("tracking_number"))
	var errorsList []string
	if carrier == "" || len(carrier) > adminOrderFieldMaxLength {
		errorsList = append(errorsList, "Carrier is required and must be 100 characters or fewer.")
	}
	if trackingNumber == "" || len(trackingNumber) > adminOrderFieldMaxLength {
		errorsList = append(errorsList, "Tracking number is required and must be 100 characters or fewer.")
	}
	if len(errorsList) > 0 {
		return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies, errorsList, http.StatusBadRequest)
	}
	now := h.currentTime().UTC()
	patch := commerce.OrderPatch{
		Actor:           commerce.OrderActorAdmin,
		TrackingCarrier: &carrier,
		TrackingNumber:  &trackingNumber,
		ShippedAt:       &now,
	}
	if _, err := h.commerceStore().TransitionOrder(ctx, order.ID, commerce.OrderStatusPaid, commerce.OrderStatusShipped, patch); err != nil {
		return h.orderWriteError(ctx, order, csrfValue, cookies, "orders: save tracking", err)
	}
	return adminRedirectResponse(http.StatusSeeOther, adminOrderDetailPath(order.ID)+"?saved=shipped", nil)
}

// checkoutService assembles the shared checkout orchestration over the admin
// handler's stores so admin cancels reuse the session-expiry and
// stock-release-claim protocols. A nil payments provider (the v1 admin
// Lambda has no Stripe credentials) degrades to skipping session expiry.
func (h *Handler) checkoutService() *checkout.Service {
	return &checkout.Service{
		Commerce: h.commerceStore(),
		Payments: h.payments,
		Stock:    h.stock,
		Metrics:  h.metrics,
		Now:      h.now,
	}
}

func (h *Handler) cancelOrder(ctx context.Context, order commerce.Order, csrfValue string, cookies []string) events.APIGatewayV2HTTPResponse {
	if order.Status != commerce.OrderStatusPendingPayment && order.Status != commerce.OrderStatusPaid {
		return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies, []string{"Only pending or paid orders can be canceled."}, http.StatusConflict)
	}
	location := adminOrderDetailPath(order.ID) + "?saved=canceled"

	if order.Status == commerce.OrderStatusPaid {
		canceled, err := h.commerceStore().TransitionOrder(ctx, order.ID, commerce.OrderStatusPaid, commerce.OrderStatusCanceled, commerce.OrderPatch{Actor: commerce.OrderActorAdmin})
		if err != nil {
			return h.orderWriteError(ctx, order, csrfValue, cookies, "orders: cancel", err)
		}
		if err := h.checkoutService().ClaimAndReleaseOrderStock(ctx, canceled.ID); err != nil {
			logAdminError("orders: release stock after cancel", err)
			location += "&stock=error"
		}
		location += "&refund=manual"
		return adminRedirectResponse(http.StatusSeeOther, location, nil)
	}

	// pending_payment: the session-safe cancel expires the checkout session
	// first so the shopper cannot pay a canceled order from an open tab.
	switch err := h.checkoutService().CancelPendingOrderAs(ctx, order, commerce.OrderActorAdmin); {
	case err == nil:
	case errors.Is(err, checkout.ErrStockReleaseFailed):
		logAdminError("orders: release stock after cancel", err)
		location += "&stock=error"
	case errors.Is(err, checkout.ErrOrderPaidNotCanceled):
		return h.refreshedOrderConflictResponse(ctx, order, csrfValue, cookies,
			"This order's payment completed while you were canceling, so it is now paid instead of canceled.")
	case errors.Is(err, checkout.ErrPaymentSessionInFlight):
		return h.refreshedOrderConflictResponse(ctx, order, csrfValue, cookies,
			"A payment for this order is still processing. Wait for it to settle, then try again.")
	default:
		return h.orderWriteError(ctx, order, csrfValue, cookies, "orders: cancel", err)
	}
	return adminRedirectResponse(http.StatusSeeOther, location, nil)
}

func (h *Handler) refreshedOrderConflictResponse(ctx context.Context, order commerce.Order, csrfValue string, cookies []string, message string) events.APIGatewayV2HTTPResponse {
	refreshed, found, err := h.commerceStore().GetOrder(ctx, order.ID)
	if err == nil && found {
		order = refreshed
	}
	return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies, []string{message}, http.StatusConflict)
}

func (h *Handler) orderWriteError(ctx context.Context, order commerce.Order, csrfValue string, cookies []string, operation string, err error) events.APIGatewayV2HTTPResponse {
	if errors.Is(err, commerce.ErrOrderTransitionConflict) {
		refreshed, found, loadErr := h.commerceStore().GetOrder(ctx, order.ID)
		if loadErr == nil && found {
			order = refreshed
		}
		return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies, []string{"This order changed while you were working. Review its current status and try again."}, http.StatusConflict)
	}
	logAdminError(operation, err)
	return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
}

func (h *Handler) orderDetailErrorResponse(ctx context.Context, order commerce.Order, csrfValue string, cookies []string, errorsList []string, statusCode int) events.APIGatewayV2HTTPResponse {
	vm := h.adminOrderDetailView(order, csrfValue)
	vm.Errors = errorsList
	body, err := renderAdminOrderDetail(ctx, vm)
	if err != nil {
		logAdminError("orders: render detail", err)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	return adminHTMLResponse(statusCode, body, nil, cookies)
}

func (h *Handler) adminOrderDetailView(order commerce.Order, csrfValue string) adminOrderDetailViewModel {
	return adminOrderDetailViewModel{
		CSRFValue:        csrfValue,
		Order:            order,
		Overdue:          adminOrderOverdue(order, h.currentTime()),
		AdvanceTargets:   adminAdvanceTargets(order.Status),
		ShowTrackingForm: order.Status == commerce.OrderStatusPaid,
		ShowCancelForm:   order.Status == commerce.OrderStatusPendingPayment || order.Status == commerce.OrderStatusPaid,
		CancelFromPaid:   order.Status == commerce.OrderStatusPaid,
	}
}

func adminOrderDetailPath(orderID string) string {
	return "/admin/orders/" + url.PathEscape(orderID)
}

// adminAdvanceTargets lists the to_status values the advance form offers for
// an order's current status: the transition-map entries restricted to the
// fulfillment chain (paid -> shipped -> delivered).
func adminAdvanceTargets(status commerce.OrderStatus) []commerce.OrderStatus {
	targets := make([]commerce.OrderStatus, 0, 2)
	for _, target := range commerce.OrderTransitions[status] {
		if adminAdvanceTarget(target) {
			targets = append(targets, target)
		}
	}
	return targets
}

// adminAdvanceTarget keeps the advance form on the fulfillment chain. The
// stock-releasing transitions (canceled, expired, payment_failed) have
// dedicated flows that also release the reservation, and payment transitions
// come from Stripe, never from this form.
func adminAdvanceTarget(target commerce.OrderStatus) bool {
	return target == commerce.OrderStatusShipped || target == commerce.OrderStatusDelivered
}

func adminOrderOverdue(order commerce.Order, now time.Time) bool {
	return order.Status == commerce.OrderStatusPendingPayment && order.CreatedAt.Add(adminOrderOverdueAfter).Before(now.UTC())
}

func adminOrderFlashMessage(saved string) string {
	switch saved {
	case "advanced":
		return "Order status updated."
	case "shipped":
		return "Tracking saved and order marked shipped."
	case "canceled":
		return "Order canceled."
	}
	return ""
}

func adminOrderStatusLabel(status commerce.OrderStatus) string {
	switch status {
	case commerce.OrderStatusPendingPayment:
		return "Pending payment"
	case commerce.OrderStatusPaid:
		return "Paid"
	case commerce.OrderStatusShipped:
		return "Shipped"
	case commerce.OrderStatusDelivered:
		return "Delivered"
	case commerce.OrderStatusPaymentFailed:
		return "Payment failed"
	case commerce.OrderStatusExpired:
		return "Expired"
	case commerce.OrderStatusCanceled:
		return "Canceled"
	}
	return string(status)
}

func adminOrderStatusChipClass(status commerce.OrderStatus) string {
	const base = "inline-flex px-2.5 py-1 text-xs font-semibold uppercase tracking-eyebrow "
	switch status {
	case commerce.OrderStatusPendingPayment:
		return base + "bg-flag-white text-flag-blue"
	case commerce.OrderStatusPaid:
		return base + "bg-flag-blue text-white"
	case commerce.OrderStatusShipped:
		return base + "bg-gold text-ink"
	case commerce.OrderStatusDelivered:
		return base + "bg-ink text-white"
	}
	return base + "bg-flag-red/10 text-flag-red"
}

func adminOrderItemCount(order commerce.Order) int {
	count := 0
	for _, line := range order.Lines {
		count += line.Quantity
	}
	return count
}

func formatAdminOrderTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format("Jan 2, 2006 15:04 UTC")
}

func renderAdminOrderList(ctx context.Context, vm adminOrderListViewModel) (string, error) {
	var body strings.Builder
	if err := adminOrderListPage(vm).Render(ctx, &body); err != nil {
		return "", err
	}
	return body.String(), nil
}

func renderAdminOrderDetail(ctx context.Context, vm adminOrderDetailViewModel) (string, error) {
	var body strings.Builder
	if err := adminOrderDetailPage(vm).Render(ctx, &body); err != nil {
		return "", err
	}
	return body.String(), nil
}

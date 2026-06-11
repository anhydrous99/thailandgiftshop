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
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
	"github.com/aws/aws-lambda-go/events"
)

const adminOrderActionAllowedMethods = http.MethodPost

// adminOrderListLimit is the page size of the newest-first admin order list
// (gsi2 orders-index); older pages continue via the ?after=<orderID> cursor.
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
	// NextCursor is the order ID the next-older page resumes after; empty on
	// the last page. The detail handler re-reads the order for its CreatedAt.
	NextCursor string
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
	ShowCancelForm   bool // pending_payment only
	ShowRefundForm   bool // paid | shipped | delivered | refund_failed
	RefundRetry      bool // refund_failed (button copy "Retry refund")
	RefundFromPaid   bool // paid (copy mentions stock return)
	Flash            string
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
	if action != "advance" && action != "tracking" && action != "cancel" && action != "refund" {
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
		return h.adminOrderListResponse(ctx, request, method, csrfValue, cookies)
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
		// Reconcile-on-render: the missed-webhook backstop for refund
		// settlement and the re-driver for a failed stock release.
		// ReconcileRefund self-gates (non-refund-family orders are returned
		// untouched), so no status check is needed here.
		order = h.checkoutService().ReconcileRefund(ctx, order)
		vm := h.adminOrderDetailView(order, csrfValue)
		vm.Flash = adminOrderFlashMessage(request.QueryStringParameters["saved"])
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
	case "refund":
		return h.refundOrder(ctx, order, csrfValue, cookies)
	}
	return adminHTMLResponse(http.StatusNotFound, "Not found", nil, nil)
}

func (h *Handler) adminOrderListResponse(ctx context.Context, request events.APIGatewayV2HTTPRequest, method string, csrfValue string, cookies []string) events.APIGatewayV2HTTPResponse {
	cursor := h.adminOrdersCursor(ctx, request.QueryStringParameters["after"])
	page, err := h.commerceStore().ListOrders(ctx, adminOrderListLimit, cursor)
	if err != nil {
		logAdminError("orders: list", err)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	now := h.currentTime()
	rows := make([]adminOrderRowViewModel, 0, len(page.Orders))
	for _, order := range page.Orders {
		rows = append(rows, adminOrderRowViewModel{Order: order, Overdue: adminOrderOverdue(order, now)})
	}
	body, err := renderAdminOrderList(ctx, adminOrderListViewModel{CSRFValue: csrfValue, Orders: rows, NextCursor: page.NextCursor.OrderID})
	if err != nil {
		logAdminError("orders: render list", err)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	if method == http.MethodHead {
		body = ""
	}
	return adminHTMLResponse(http.StatusOK, body, nil, cookies)
}

// adminOrdersCursor resolves ?after=<orderID> by re-reading the order so the
// URL never carries a sort-key timestamp. Malformed, unknown, or unreadable
// IDs restart at the first page (the customer-page fallback convention).
func (h *Handler) adminOrdersCursor(ctx context.Context, after string) commerce.OrderCursor {
	if !validAdminOrderID(after) {
		return commerce.OrderCursor{}
	}
	order, found, err := h.commerceStore().GetOrder(ctx, after)
	if err != nil {
		logAdminError("orders: resolve cursor", err)
		return commerce.OrderCursor{}
	}
	if !found {
		return commerce.OrderCursor{}
	}
	return commerce.OrderCursor{OrderID: order.ID, CreatedAt: order.CreatedAt}
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
	updated, err := h.commerceStore().TransitionOrder(ctx, order.ID, order.Status, target, patch)
	if err != nil {
		return h.orderWriteError(ctx, order, csrfValue, cookies, "orders: advance", err)
	}
	h.checkoutService().NotifyOrderStatusChanged(ctx, updated, order.Status, target)
	return adminRedirectResponse(http.StatusSeeOther, adminOrderDetailPath(order.ID)+"?saved=advanced", nil)
}

func (h *Handler) setOrderTracking(ctx context.Context, request events.APIGatewayV2HTTPRequest, order commerce.Order, csrfValue string, cookies []string) events.APIGatewayV2HTTPResponse {
	values, err := httpapi.FormValues(request)
	if err != nil {
		return adminHTMLResponse(http.StatusBadRequest, "Invalid form", nil, nil)
	}
	if order.Status != commerce.OrderStatusPaid && order.Status != commerce.OrderStatusShipped {
		return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies, []string{"Tracking can only be added to paid or shipped orders."}, http.StatusConflict)
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
	if order.TrackingCarrier == carrier && order.TrackingNumber == trackingNumber {
		return adminRedirectResponse(http.StatusSeeOther, adminOrderDetailPath(order.ID)+"?saved=shipped", nil)
	}
	now := h.currentTime().UTC()
	patch := commerce.OrderPatch{Actor: commerce.OrderActorAdmin, TrackingCarrier: &carrier, TrackingNumber: &trackingNumber}
	var updated commerce.Order
	var writeErr error
	if order.Status == commerce.OrderStatusPaid {
		patch.ShippedAt = &now
		updated, writeErr = h.commerceStore().TransitionOrder(ctx, order.ID, commerce.OrderStatusPaid, commerce.OrderStatusShipped, patch)
	} else {
		updated, writeErr = h.commerceStore().PatchOrder(ctx, order.ID, commerce.OrderStatusShipped, order.Version, patch)
	}
	if writeErr != nil {
		return h.orderWriteError(ctx, order, csrfValue, cookies, "orders: save tracking", writeErr)
	}
	h.checkoutService().NotifyTrackingUpdated(ctx, updated)
	return adminRedirectResponse(http.StatusSeeOther, adminOrderDetailPath(order.ID)+"?saved=shipped", nil)
}

// checkoutService assembles the shared checkout orchestration over the admin
// handler's stores so admin cancels and refunds reuse the session-expiry,
// refund, and stock-release-claim protocols. The admin Lambda carries Stripe
// credentials; should the provider still be nil (degraded wiring), cancels
// skip session expiry while refunds refuse to proceed
// (checkout.ErrRefundProviderUnavailable — refunds move money).
func (h *Handler) checkoutService() *checkout.Service {
	return &checkout.Service{
		Commerce:    h.commerceStore(),
		Payments:    h.payments,
		Stock:       h.stock,
		Metrics:     h.metrics,
		EmailSender: h.emailSender,
		BaseURL:     payments.PublicBaseURLFromEnvironment(),
		Now:         h.now,
	}
}

func (h *Handler) cancelOrder(ctx context.Context, order commerce.Order, csrfValue string, cookies []string) events.APIGatewayV2HTTPResponse {
	if order.Status != commerce.OrderStatusPendingPayment {
		return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies, []string{"Only pending orders can be canceled. Refund paid orders instead."}, http.StatusConflict)
	}
	location := adminOrderDetailPath(order.ID) + "?saved=canceled"

	// pending_payment: the session-safe cancel expires the checkout session
	// first so the shopper cannot pay a canceled order from an open tab.
	switch err := h.checkoutService().CancelPendingOrderAs(ctx, order, commerce.OrderActorAdmin); {
	case err == nil:
	case errors.Is(err, checkout.ErrStockReleaseFailed):
		logAdminError("orders: release stock after cancel", err)
		location += "&stock=error"
	case errors.Is(err, checkout.ErrOrderPaidNotCanceled):
		return h.refreshedOrderConflictResponse(ctx, order, csrfValue, cookies,
			"This order's payment completed while you were canceling, so it is now paid instead of canceled.", http.StatusConflict)
	case errors.Is(err, checkout.ErrPaymentSessionInFlight):
		return h.refreshedOrderConflictResponse(ctx, order, csrfValue, cookies,
			"A payment for this order is still processing. Wait for it to settle, then try again.", http.StatusConflict)
	default:
		return h.orderWriteError(ctx, order, csrfValue, cookies, "orders: cancel", err)
	}
	return adminRedirectResponse(http.StatusSeeOther, location, nil)
}

// refundOrder issues an automatic Stripe refund for a paid, shipped, or
// delivered order (or retries a failed one). The checkout service owns the
// protocol; this handler translates its results.
func (h *Handler) refundOrder(ctx context.Context, order commerce.Order, csrfValue string, cookies []string) events.APIGatewayV2HTTPResponse {
	switch order.Status {
	case commerce.OrderStatusPaid, commerce.OrderStatusShipped, commerce.OrderStatusDelivered, commerce.OrderStatusRefundFailed:
	default:
		return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies,
			[]string{"Only paid, shipped, or delivered orders can be refunded."}, http.StatusConflict)
	}
	updated, err := h.checkoutService().RefundOrderAs(ctx, order, commerce.OrderActorAdmin)
	location := adminOrderDetailPath(order.ID)
	switch {
	case err == nil:
	case errors.Is(err, checkout.ErrStockReleaseFailed):
		logAdminError("orders: release stock after refund", err)
		// The refund committed; the stock release is re-driven by webhook
		// redelivery and the order-page reconcile.
		return adminRedirectResponse(http.StatusSeeOther, location+refundSavedQuery(updated)+"&stock=error", nil)
	case errors.Is(err, checkout.ErrRefundProviderUnavailable):
		return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies,
			[]string{"The payments provider is unavailable, so the refund was not issued. Try again shortly, or refund in the Stripe dashboard; the order is unchanged."}, http.StatusBadGateway)
	case errors.Is(err, checkout.ErrRefundNotIssuable):
		return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies,
			[]string{"This order has no payment intent on file, so it must be refunded in the Stripe dashboard."}, http.StatusConflict)
	case errors.Is(err, payments.ErrChargeAlreadyRefunded):
		// Post-idempotency-window orphan: Stripe holds a full refund this
		// order does not know about. Retrying can never succeed.
		return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies,
			[]string{"Stripe reports this payment as already fully refunded, but no refund is recorded on this order. Reconcile it in the Stripe dashboard."}, http.StatusConflict)
	case errors.Is(err, commerce.ErrOrderTransitionConflict):
		return h.orderWriteError(ctx, order, csrfValue, cookies, "orders: refund", err)
	default:
		// Provider/API failure: order unchanged; the same idempotency key
		// applies on a retry.
		logAdminError("orders: refund", err)
		return h.refreshedOrderConflictResponse(ctx, order, csrfValue, cookies,
			"Stripe could not process the refund, so the order is unchanged. Try again, or refund in the Stripe dashboard.", http.StatusBadGateway)
	}
	return adminRedirectResponse(http.StatusSeeOther, location+refundSavedQuery(updated), nil)
}

// refundSavedQuery maps the post-refund order status onto the flash key: a
// synchronously settled refund lands on refunded, a synchronous provider
// failure on refund_failed, and an asynchronous one on refund_pending.
func refundSavedQuery(order commerce.Order) string {
	switch order.Status {
	case commerce.OrderStatusRefunded:
		return "?saved=refunded"
	case commerce.OrderStatusRefundFailed:
		return "?saved=refund_failed"
	}
	return "?saved=refund_pending"
}

func (h *Handler) refreshedOrderConflictResponse(ctx context.Context, order commerce.Order, csrfValue string, cookies []string, message string, statusCode int) events.APIGatewayV2HTTPResponse {
	refreshed, found, err := h.commerceStore().GetOrder(ctx, order.ID)
	if err == nil && found {
		order = refreshed
	}
	return h.orderDetailErrorResponse(ctx, order, csrfValue, cookies, []string{message}, statusCode)
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
		ShowTrackingForm: order.Status == commerce.OrderStatusPaid || order.Status == commerce.OrderStatusShipped,
		ShowCancelForm:   order.Status == commerce.OrderStatusPendingPayment,
		ShowRefundForm:   adminOrderRefundable(order.Status),
		RefundRetry:      order.Status == commerce.OrderStatusRefundFailed,
		RefundFromPaid:   order.Status == commerce.OrderStatusPaid,
	}
}

// adminOrderRefundable mirrors the refundOrder action gate: the Refund form
// is offered exactly where a POST would be accepted.
func adminOrderRefundable(status commerce.OrderStatus) bool {
	switch status {
	case commerce.OrderStatusPaid, commerce.OrderStatusShipped, commerce.OrderStatusDelivered, commerce.OrderStatusRefundFailed:
		return true
	}
	return false
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
	case "refund_pending":
		return "Refund issued. Stripe is processing it; the status updates when it settles."
	case "refunded":
		return "Refund issued and confirmed."
	case "refund_failed":
		return "Stripe reported the refund as failed. See the failure details below."
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
	case commerce.OrderStatusRefundPending:
		return "Refund pending"
	case commerce.OrderStatusRefunded:
		return "Refunded"
	case commerce.OrderStatusRefundFailed:
		return "Refund failed"
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
	case commerce.OrderStatusRefundPending:
		return base + "bg-gold text-ink"
	case commerce.OrderStatusRefunded:
		return base + "bg-ink text-white"
	}
	// refund_failed shares the red terminal fallback.
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

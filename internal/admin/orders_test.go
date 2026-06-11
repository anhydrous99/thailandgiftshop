package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/payments"
	"github.com/aws/aws-lambda-go/events"
)

const testOrderProductID = "prod_thai_tea"
const testOrderVariantID = "var_box12"
const testOrderVariantStock = 5
const testOrderLineQuantity = 2

func TestAdminOrdersListRendersNewestFirstWithStatusChipsAndOverdueFlag(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	now := *currentTime

	overduePending := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPendingPayment, now.Add(-46*time.Minute)))
	boundaryPending := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(2), commerce.OrderStatusPendingPayment, now.Add(-45*time.Minute)))
	paid := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(3), commerce.OrderStatusPaid, now.Add(-1*time.Minute)))

	response := authenticatedProductGet(t, handler, "/admin/orders")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertNoStore(t, response)

	if got := strings.Count(response.Body, `data-testid="admin-order-row"`); got != 3 {
		t.Fatalf("admin-order-row count = %d, want 3; body = %q", got, response.Body)
	}
	if got := strings.Count(response.Body, `data-testid="admin-order-overdue"`); got != 1 {
		t.Fatalf("admin-order-overdue count = %d, want exactly 1 for the 46-minute pending order", got)
	}
	for _, want := range []string{"Pending payment", "Paid", "shopper@example.test", "$30.00", "2 items"} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("body missing %q", want)
		}
	}

	paidIndex := strings.Index(response.Body, paid.ID)
	boundaryIndex := strings.Index(response.Body, boundaryPending.ID)
	overdueIndex := strings.Index(response.Body, overduePending.ID)
	if paidIndex == -1 || boundaryIndex == -1 || overdueIndex == -1 {
		t.Fatalf("body missing order IDs: paid=%d boundary=%d overdue=%d", paidIndex, boundaryIndex, overdueIndex)
	}
	if !(paidIndex < boundaryIndex && boundaryIndex < overdueIndex) {
		t.Fatalf("orders not newest-first: paid=%d boundary=%d overdue=%d", paidIndex, boundaryIndex, overdueIndex)
	}

	overdueRowStart := strings.LastIndex(response.Body[:overdueIndex], `data-testid="admin-order-row"`)
	if !strings.Contains(response.Body[overdueRowStart:], "Overdue pending") {
		t.Fatalf("overdue pending order row missing overdue flag")
	}
	if strings.Contains(response.Body, `data-testid="admin-orders-next-page"`) {
		t.Fatalf("3 orders fit on one page; the older-orders link must not render")
	}
}

func TestAdminOrdersListPagesWithCursor(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	now := *currentTime

	// adminOrderListLimit+1 orders, distinct creation times: the list shows
	// the newest 100 and links the next-older page after the 100th row.
	orderIDs := make([]string, 0, adminOrderListLimit+1)
	for i := 1; i <= adminOrderListLimit+1; i++ {
		order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(i), commerce.OrderStatusPaid, now.Add(-time.Duration(i)*time.Minute)))
		orderIDs = append(orderIDs, order.ID)
	}
	oldestID := orderIDs[adminOrderListLimit]
	cursorID := orderIDs[adminOrderListLimit-1] // 100th-newest order

	response := authenticatedProductGet(t, handler, "/admin/orders")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if got := strings.Count(response.Body, `data-testid="admin-order-row"`); got != adminOrderListLimit {
		t.Fatalf("admin-order-row count = %d, want %d", got, adminOrderListLimit)
	}
	if strings.Contains(response.Body, `href="/admin/orders/`+oldestID+`"`) {
		t.Fatalf("first page must not contain the oldest order")
	}
	if !strings.Contains(response.Body, `data-testid="admin-orders-next-page"`) {
		t.Fatalf("first page is missing the older-orders link")
	}
	if !strings.Contains(response.Body, `href="/admin/orders?after=`+cursorID+`"`) {
		t.Fatalf("older-orders link should resume after the 100th order %s", cursorID)
	}

	secondPage := authenticatedProductGet(t, handler, "/admin/orders?after="+cursorID)
	if secondPage.StatusCode != http.StatusOK {
		t.Fatalf("page 2 status = %d, want %d", secondPage.StatusCode, http.StatusOK)
	}
	if got := strings.Count(secondPage.Body, `data-testid="admin-order-row"`); got != 1 {
		t.Fatalf("page 2 admin-order-row count = %d, want 1", got)
	}
	if !strings.Contains(secondPage.Body, `href="/admin/orders/`+oldestID+`"`) {
		t.Fatalf("page 2 is missing the oldest order")
	}
	if strings.Contains(secondPage.Body, `data-testid="admin-orders-next-page"`) {
		t.Fatalf("the last page must not offer another page")
	}
}

func TestAdminOrdersListUnknownCursorRestartsAtFirstPage(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	now := *currentTime

	for i := 1; i <= 3; i++ {
		createTestOrder(t, commerceStore, adminTestOrder(testOrderID(i), commerce.OrderStatusPaid, now.Add(-time.Duration(i)*time.Minute)))
	}

	// No t.Run subtests: the login helper derives the admin password from
	// t.Name(), so authenticated requests must run under the parent test.
	for _, after := range []string{strings.Repeat("z", 26), "abc"} {
		response := authenticatedProductGet(t, handler, "/admin/orders?after="+after)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("after=%q status = %d, want %d", after, response.StatusCode, http.StatusOK)
		}
		if got := strings.Count(response.Body, `data-testid="admin-order-row"`); got != 3 {
			t.Fatalf("after=%q admin-order-row count = %d, want the full first page of 3", after, got)
		}
	}
}

func TestAdminOrdersListShowsEmptyState(t *testing.T) {
	handler, _, _, _ := newOrdersTestHandler(t)

	response := authenticatedProductGet(t, handler, "/admin/orders")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if !strings.Contains(response.Body, `data-testid="admin-orders-empty"`) || !strings.Contains(response.Body, "No orders yet.") {
		t.Fatalf("body missing empty state: %q", response.Body)
	}
}

func TestAdminOrderDetailRendersSnapshotAddressHistoryAndPaymentIntent(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, currentTime.Add(-10*time.Minute)))

	response := authenticatedProductGet(t, handler, "/admin/orders/"+order.ID)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	assertNoStore(t, response)

	for _, want := range []string{
		"Order " + order.ID,
		// Frozen snapshot line, not live catalog data.
		"Thai Tea Sampler",
		"Box of 12",
		"/products/thai-tea",
		"$15.00",
		"$30.00",
		// Shipping address snapshot.
		`data-testid="admin-order-address"`,
		"Ploy Shopper",
		"99 Sukhumvit Road",
		"Bangkok",
		// Status history and payment intent.
		`data-testid="admin-order-history"`,
		`data-testid="admin-order-payment-intent"`,
		"pi_test_admin_123",
		// Action forms for a paid order: advance, tracking, and refund.
		`data-testid="admin-order-advance"`,
		`data-testid="admin-order-tracking"`,
		`data-testid="admin-order-refund"`,
		"Cancel and refund",
		"returns the full payment to the shopper through Stripe and returns the reserved stock to the shop",
	} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("detail body missing %q", want)
		}
	}
	if strings.Contains(response.Body, `data-testid="admin-order-cancel"`) {
		t.Fatalf("paid order must not offer the cancel form; refunds replace paid cancels")
	}
	if !strings.Contains(response.Body, `value="shipped"`) {
		t.Fatalf("advance form missing shipped option for a paid order")
	}
	if strings.Contains(response.Body, `value="canceled"`) {
		t.Fatalf("advance form must not offer canceled; cancel has a dedicated stock-releasing flow")
	}

	head := adminRequest(http.MethodHead, "/admin/orders/"+order.ID)
	login := loginResponse(t, handler)
	head.Cookies = []string{cookiePair(t, responseCookie(t, login, adminSessionCookieName)), cookiePair(t, responseCookie(t, login, adminCSRFCookieName))}
	headResponse, err := handler.Handle(context.Background(), head)
	if err != nil {
		t.Fatalf("Handle HEAD returned error: %v", err)
	}
	if headResponse.StatusCode != http.StatusOK || headResponse.Body != "" {
		t.Fatalf("HEAD status = %d body = %q, want 200 with empty body", headResponse.StatusCode, headResponse.Body)
	}
}

func TestAdminOrderPendingDetailHidesFulfillmentFormsAndShowsCancel(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPendingPayment, currentTime.Add(-5*time.Minute)))

	response := authenticatedProductGet(t, handler, "/admin/orders/"+order.ID)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if strings.Contains(response.Body, `data-testid="admin-order-advance"`) {
		t.Fatalf("pending order must not offer the advance form")
	}
	if strings.Contains(response.Body, `data-testid="admin-order-tracking"`) {
		t.Fatalf("pending order must not offer the tracking form")
	}
	if !strings.Contains(response.Body, `data-testid="admin-order-cancel"`) {
		t.Fatalf("pending order must offer the cancel form")
	}
	if !strings.Contains(response.Body, "No payment has been captured for this order.") {
		t.Fatalf("pending cancel form missing the no-payment note")
	}
}

func TestAdminOrderPathsRejectInvalidAndUnknownIDs(t *testing.T) {
	// {orderID} is the path of an order that exists in every subtest store.
	tests := []struct {
		name string
		path string
	}{
		{name: "trailing slash", path: "/admin/orders/"},
		{name: "too short", path: "/admin/orders/abc123"},
		{name: "too long", path: "/admin/orders/" + strings.Repeat("a", 27)},
		{name: "uppercase", path: "/admin/orders/" + strings.Repeat("A", 26)},
		{name: "unknown valid format", path: "/admin/orders/" + strings.Repeat("z", 26)},
		{name: "unknown action", path: "/admin/orders/{orderID}/archive"},
		{name: "nested action", path: "/admin/orders/{orderID}/advance/extra"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
			order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, *currentTime))
			path := strings.ReplaceAll(test.path, "{orderID}", order.ID)
			response := authenticatedProductGet(t, handler, path)
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("GET %s status = %d, want %d", path, response.StatusCode, http.StatusNotFound)
			}
		})
	}
}

func TestAdminOrdersMethodNotAllowed(t *testing.T) {
	// {orderID} is the path of an order that exists in every subtest store.
	tests := []struct {
		name      string
		method    string
		path      string
		wantAllow string
	}{
		{name: "POST list", method: http.MethodPost, path: "/admin/orders", wantAllow: adminAllowedMethods},
		{name: "POST detail", method: http.MethodPost, path: "/admin/orders/{orderID}", wantAllow: adminAllowedMethods},
		{name: "GET advance", method: http.MethodGet, path: "/admin/orders/{orderID}/advance", wantAllow: adminOrderActionAllowedMethods},
		{name: "GET tracking", method: http.MethodGet, path: "/admin/orders/{orderID}/tracking", wantAllow: adminOrderActionAllowedMethods},
		{name: "GET cancel", method: http.MethodGet, path: "/admin/orders/{orderID}/cancel", wantAllow: adminOrderActionAllowedMethods},
		{name: "GET refund", method: http.MethodGet, path: "/admin/orders/{orderID}/refund", wantAllow: adminOrderActionAllowedMethods},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
			order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, *currentTime))
			path := strings.ReplaceAll(test.path, "{orderID}", order.ID)
			var response events.APIGatewayV2HTTPResponse
			if test.method == http.MethodPost {
				response = authenticatedProductPost(t, handler, path, url.Values{})
			} else {
				response = authenticatedProductGet(t, handler, path)
			}
			if response.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s status = %d, want %d", test.method, path, response.StatusCode, http.StatusMethodNotAllowed)
			}
			if response.Headers["Allow"] != test.wantAllow {
				t.Fatalf("%s %s Allow = %q, want %q", test.method, path, response.Headers["Allow"], test.wantAllow)
			}
		})
	}
}

func TestAdminOrderAdvanceMovesPaidThroughShippedToDelivered(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, currentTime.Add(-10*time.Minute)))

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/advance", url.Values{"to_status": {"shipped"}})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("advance status = %d, want %d; body = %q", response.StatusCode, http.StatusSeeOther, response.Body)
	}
	if response.Headers["Location"] != "/admin/orders/"+order.ID+"?saved=advanced" {
		t.Fatalf("Location = %q, want detail redirect with saved=advanced", response.Headers["Location"])
	}
	shipped := getTestOrder(t, commerceStore, order.ID)
	if shipped.Status != commerce.OrderStatusShipped {
		t.Fatalf("status = %q, want shipped", shipped.Status)
	}
	if !shipped.ShippedAt.Equal(currentTime.UTC()) {
		t.Fatalf("ShippedAt = %v, want %v", shipped.ShippedAt, currentTime.UTC())
	}
	lastEvent := shipped.StatusHistory[len(shipped.StatusHistory)-1]
	if lastEvent.Status != commerce.OrderStatusShipped || lastEvent.Actor != commerce.OrderActorAdmin {
		t.Fatalf("history tail = %+v, want shipped by admin", lastEvent)
	}

	response = authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/advance", url.Values{"to_status": {"delivered"}})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("advance to delivered status = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	delivered := getTestOrder(t, commerceStore, order.ID)
	if delivered.Status != commerce.OrderStatusDelivered {
		t.Fatalf("status = %q, want delivered", delivered.Status)
	}
	if !delivered.DeliveredAt.Equal(currentTime.UTC()) {
		t.Fatalf("DeliveredAt = %v, want %v", delivered.DeliveredAt, currentTime.UTC())
	}
}

func TestAdminOrderAdvanceRejectsIllegalTransitions(t *testing.T) {
	tests := []struct {
		name     string
		from     commerce.OrderStatus
		toStatus string
	}{
		{name: "pending to shipped", from: commerce.OrderStatusPendingPayment, toStatus: "shipped"},
		{name: "pending to delivered", from: commerce.OrderStatusPendingPayment, toStatus: "delivered"},
		{name: "pending to paid is stripe's job", from: commerce.OrderStatusPendingPayment, toStatus: "paid"},
		{name: "paid to delivered skips shipped", from: commerce.OrderStatusPaid, toStatus: "delivered"},
		{name: "paid to canceled must use cancel flow", from: commerce.OrderStatusPaid, toStatus: "canceled"},
		{name: "paid to expired", from: commerce.OrderStatusPaid, toStatus: "expired"},
		{name: "shipped to shipped", from: commerce.OrderStatusShipped, toStatus: "shipped"},
		{name: "delivered is terminal", from: commerce.OrderStatusDelivered, toStatus: "shipped"},
		{name: "canceled is terminal", from: commerce.OrderStatusCanceled, toStatus: "shipped"},
		{name: "unknown status", from: commerce.OrderStatusPaid, toStatus: "refunded"},
		{name: "empty status", from: commerce.OrderStatusPaid, toStatus: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
			order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), test.from, *currentTime))

			response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/advance", url.Values{"to_status": {test.toStatus}})
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusBadRequest)
			}
			if !strings.Contains(response.Body, "Choose a valid next status for this order.") {
				t.Fatalf("body missing advance validation error: %q", response.Body)
			}
			unchanged := getTestOrder(t, commerceStore, order.ID)
			if unchanged.Status != test.from {
				t.Fatalf("status = %q, want unchanged %q", unchanged.Status, test.from)
			}
			if len(unchanged.StatusHistory) != 1 {
				t.Fatalf("status history length = %d, want 1 (no transition recorded)", len(unchanged.StatusHistory))
			}
		})
	}
}

func TestAdminOrderTrackingTransitionsPaidToShipped(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, currentTime.Add(-10*time.Minute)))

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/tracking", url.Values{
		"carrier":         {" Thailand Post "},
		"tracking_number": {" TH1234567890 "},
	})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusSeeOther, response.Body)
	}
	if response.Headers["Location"] != "/admin/orders/"+order.ID+"?saved=shipped" {
		t.Fatalf("Location = %q, want detail redirect with saved=shipped", response.Headers["Location"])
	}

	shipped := getTestOrder(t, commerceStore, order.ID)
	if shipped.Status != commerce.OrderStatusShipped {
		t.Fatalf("status = %q, want shipped", shipped.Status)
	}
	if shipped.TrackingCarrier != "Thailand Post" || shipped.TrackingNumber != "TH1234567890" {
		t.Fatalf("tracking = %q %q, want trimmed carrier and number", shipped.TrackingCarrier, shipped.TrackingNumber)
	}
	if !shipped.ShippedAt.Equal(currentTime.UTC()) {
		t.Fatalf("ShippedAt = %v, want %v", shipped.ShippedAt, currentTime.UTC())
	}
	lastEvent := shipped.StatusHistory[len(shipped.StatusHistory)-1]
	if lastEvent.Status != commerce.OrderStatusShipped || lastEvent.Actor != commerce.OrderActorAdmin {
		t.Fatalf("history tail = %+v, want shipped by admin", lastEvent)
	}

	detail := authenticatedProductGet(t, handler, "/admin/orders/"+order.ID+"?saved=shipped")
	if !strings.Contains(detail.Body, "Tracking saved and order marked shipped.") {
		t.Fatalf("detail body missing shipped flash")
	}
	if !strings.Contains(detail.Body, "Thailand Post") || !strings.Contains(detail.Body, "TH1234567890") {
		t.Fatalf("detail body missing tracking values")
	}
}

func TestAdminOrderTrackingValidatesFieldsAndStatus(t *testing.T) {
	tests := []struct {
		name       string
		from       commerce.OrderStatus
		carrier    string
		number     string
		wantStatus int
		wantError  string
	}{
		{name: "missing carrier", from: commerce.OrderStatusPaid, carrier: "", number: "TH123", wantStatus: http.StatusBadRequest, wantError: "Carrier is required"},
		{name: "missing number", from: commerce.OrderStatusPaid, carrier: "Thailand Post", number: "", wantStatus: http.StatusBadRequest, wantError: "Tracking number is required"},
		{name: "carrier too long", from: commerce.OrderStatusPaid, carrier: strings.Repeat("x", 101), number: "TH123", wantStatus: http.StatusBadRequest, wantError: "Carrier is required"},
		{name: "pending order", from: commerce.OrderStatusPendingPayment, carrier: "Thailand Post", number: "TH123", wantStatus: http.StatusConflict, wantError: "Tracking can only be added to paid orders."},
		{name: "delivered order", from: commerce.OrderStatusDelivered, carrier: "Thailand Post", number: "TH123", wantStatus: http.StatusConflict, wantError: "Tracking can only be added to paid orders."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
			order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), test.from, *currentTime))

			response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/tracking", url.Values{
				"carrier":         {test.carrier},
				"tracking_number": {test.number},
			})
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.wantStatus)
			}
			if !strings.Contains(response.Body, test.wantError) {
				t.Fatalf("body missing %q: %q", test.wantError, response.Body)
			}
			unchanged := getTestOrder(t, commerceStore, order.ID)
			if unchanged.Status != test.from || unchanged.TrackingCarrier != "" || unchanged.TrackingNumber != "" {
				t.Fatalf("order mutated: status=%q carrier=%q number=%q", unchanged.Status, unchanged.TrackingCarrier, unchanged.TrackingNumber)
			}
		})
	}
}

func TestAdminOrderCancelFromPendingReleasesStock(t *testing.T) {
	handler, commerceStore, catalogStore, currentTime := newOrdersTestHandler(t)
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPendingPayment, *currentTime))

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/cancel", url.Values{})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusSeeOther, response.Body)
	}
	if response.Headers["Location"] != "/admin/orders/"+order.ID+"?saved=canceled" {
		t.Fatalf("Location = %q, want saved=canceled without refund notice", response.Headers["Location"])
	}

	canceled := getTestOrder(t, commerceStore, order.ID)
	if canceled.Status != commerce.OrderStatusCanceled {
		t.Fatalf("status = %q, want canceled", canceled.Status)
	}
	lastEvent := canceled.StatusHistory[len(canceled.StatusHistory)-1]
	if lastEvent.Status != commerce.OrderStatusCanceled || lastEvent.Actor != commerce.OrderActorAdmin {
		t.Fatalf("history tail = %+v, want canceled by admin", lastEvent)
	}
	assertVariantStock(t, catalogStore, testOrderVariantStock+testOrderLineQuantity)
}

func TestAdminOrderCancelFromPaidIsRefused(t *testing.T) {
	handler, commerceStore, catalogStore, currentTime := newOrdersTestHandler(t)
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, *currentTime))

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/cancel", url.Values{})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusConflict, response.Body)
	}
	if !strings.Contains(response.Body, "Refund paid orders instead.") {
		t.Fatalf("body missing the refund-instead guidance: %q", response.Body)
	}

	unchanged := getTestOrder(t, commerceStore, order.ID)
	if unchanged.Status != commerce.OrderStatusPaid {
		t.Fatalf("status = %q, want paid untouched", unchanged.Status)
	}
	assertVariantStock(t, catalogStore, testOrderVariantStock)
}

func TestAdminOrderCancelPendingExpiresCheckoutSession(t *testing.T) {
	handler, commerceStore, catalogStore, currentTime := newOrdersTestHandler(t)
	provider := payments.NewFakeProvider()
	handler.payments = provider

	order := adminTestOrder(testOrderID(1), commerce.OrderStatusPendingPayment, *currentTime)
	session := createTestPaymentSession(t, provider, order)
	order.StripeCheckoutSessionID = session.ID
	order = createTestOrder(t, commerceStore, order)

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/cancel", url.Values{})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusSeeOther, response.Body)
	}
	if response.Headers["Location"] != "/admin/orders/"+order.ID+"?saved=canceled" {
		t.Fatalf("Location = %q, want saved=canceled", response.Headers["Location"])
	}

	canceled := getTestOrder(t, commerceStore, order.ID)
	if canceled.Status != commerce.OrderStatusCanceled {
		t.Fatalf("status = %q, want canceled", canceled.Status)
	}
	lastEvent := canceled.StatusHistory[len(canceled.StatusHistory)-1]
	if lastEvent.Status != commerce.OrderStatusCanceled || lastEvent.Actor != commerce.OrderActorAdmin {
		t.Fatalf("history tail = %+v, want canceled by admin", lastEvent)
	}
	if canceled.StockReleasedAt.IsZero() {
		t.Fatalf("StockReleasedAt is zero, want the release claim recorded")
	}
	assertVariantStock(t, catalogStore, testOrderVariantStock+testOrderLineQuantity)

	expired, err := provider.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if expired.Status != payments.SessionStatusExpired {
		t.Fatalf("session status = %q, want %q", expired.Status, payments.SessionStatusExpired)
	}
	// A stale checkout tab can no longer pay the canceled order.
	if _, err := provider.MarkSessionPaid(session.ID, false); !errors.Is(err, payments.ErrSessionExpired) {
		t.Fatalf("MarkSessionPaid on the canceled order's session error = %v, want %v", err, payments.ErrSessionExpired)
	}
}

// TestLocalDemoHandlerCancelExpiresFakeSession pins the devserver demo wiring:
// NewLocalDemoHandler carries the shared fake payment provider, so demo-mode
// admin cancels run the same session-expiry guard as production instead of
// silently skipping it with a nil provider.
func TestLocalDemoHandlerCancelExpiresFakeSession(t *testing.T) {
	provider := payments.NewFakeProvider()
	catalogStore := newOrdersTestCatalogStore()
	commerceStore := commerce.NewMemoryStore()
	handler := NewLocalDemoHandler(Credentials{
		PasswordHash:  testPasswordHash(t),
		SessionSecret: testSessionSecret,
	}, catalogStore, commerceStore, catalogStore, provider)

	order := adminTestOrder(testOrderID(1), commerce.OrderStatusPendingPayment, time.Now())
	session := createTestPaymentSession(t, provider, order)
	order.StripeCheckoutSessionID = session.ID
	order = createTestOrder(t, commerceStore, order)

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/cancel", url.Values{})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusSeeOther, response.Body)
	}
	if response.Headers["Location"] != "/admin/orders/"+order.ID+"?saved=canceled" {
		t.Fatalf("Location = %q, want saved=canceled", response.Headers["Location"])
	}

	expired, err := provider.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if expired.Status != payments.SessionStatusExpired {
		t.Fatalf("session status = %q, want %q (demo cancels must expire fake sessions)", expired.Status, payments.SessionStatusExpired)
	}
}

func TestAdminOrderCancelPendingPaidSessionFinalizesInstead(t *testing.T) {
	handler, commerceStore, catalogStore, currentTime := newOrdersTestHandler(t)
	provider := payments.NewFakeProvider()
	handler.payments = provider

	order := adminTestOrder(testOrderID(1), commerce.OrderStatusPendingPayment, *currentTime)
	session := createTestPaymentSession(t, provider, order)
	order.StripeCheckoutSessionID = session.ID
	order = createTestOrder(t, commerceStore, order)
	if _, err := provider.MarkSessionPaid(session.ID, false); err != nil {
		t.Fatalf("MarkSessionPaid returned error: %v", err)
	}

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/cancel", url.Values{})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusConflict, response.Body)
	}
	if !strings.Contains(response.Body, "payment completed while you were canceling") {
		t.Fatalf("body missing the paid-not-canceled explanation: %q", response.Body)
	}

	paid := getTestOrder(t, commerceStore, order.ID)
	if paid.Status != commerce.OrderStatusPaid {
		t.Fatalf("status = %q, want paid (never cancel paid-for goods)", paid.Status)
	}
	if paid.StripePaymentIntentID != "pi_fake_"+order.ID {
		t.Fatalf("payment intent id = %q, want the finalize patch applied", paid.StripePaymentIntentID)
	}
	// The paid order keeps its reservation.
	assertVariantStock(t, catalogStore, testOrderVariantStock)
}

func TestAdminOrderCancelRejectsTerminalAndShippedStatuses(t *testing.T) {
	for _, status := range []commerce.OrderStatus{
		commerce.OrderStatusShipped,
		commerce.OrderStatusDelivered,
		commerce.OrderStatusCanceled,
		commerce.OrderStatusExpired,
		commerce.OrderStatusPaymentFailed,
	} {
		t.Run(string(status), func(t *testing.T) {
			handler, commerceStore, catalogStore, currentTime := newOrdersTestHandler(t)
			order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), status, *currentTime))

			response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/cancel", url.Values{})
			if response.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusConflict)
			}
			if !strings.Contains(response.Body, "Only pending orders can be canceled. Refund paid orders instead.") {
				t.Fatalf("body missing cancel guard error: %q", response.Body)
			}
			unchanged := getTestOrder(t, commerceStore, order.ID)
			if unchanged.Status != status {
				t.Fatalf("status = %q, want unchanged %q", unchanged.Status, status)
			}
			assertVariantStock(t, catalogStore, testOrderVariantStock)
		})
	}
}

func TestAdminOrderCancelStockReleaseFailureFlagsWarning(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	handler.stock = nil
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPendingPayment, *currentTime))

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/cancel", url.Values{})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	if response.Headers["Location"] != "/admin/orders/"+order.ID+"?saved=canceled&stock=error" {
		t.Fatalf("Location = %q, want saved=canceled&stock=error", response.Headers["Location"])
	}
	canceled := getTestOrder(t, commerceStore, order.ID)
	if canceled.Status != commerce.OrderStatusCanceled {
		t.Fatalf("status = %q, want canceled despite the release failure", canceled.Status)
	}

	detail := authenticatedProductGet(t, handler, "/admin/orders/"+order.ID+"?saved=canceled&stock=error")
	if !strings.Contains(detail.Body, `data-testid="admin-order-stock-warning"`) {
		t.Fatalf("detail body missing stock release warning")
	}
	if !strings.Contains(detail.Body, "Adjust the affected product stock manually.") {
		t.Fatalf("detail body missing manual stock repair copy")
	}
}

func TestAdminOrderActionsRequireCSRF(t *testing.T) {
	handler, commerceStore, catalogStore, currentTime := newOrdersTestHandler(t)
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPendingPayment, *currentTime))

	login := loginResponse(t, handler)
	request := adminFormRequest(http.MethodPost, "/admin/orders/"+order.ID+"/cancel", url.Values{})
	request.Cookies = []string{cookiePair(t, responseCookie(t, login, adminSessionCookieName))}
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	unchanged := getTestOrder(t, commerceStore, order.ID)
	if unchanged.Status != commerce.OrderStatusPendingPayment {
		t.Fatalf("status = %q, want pending_payment after rejected CSRF", unchanged.Status)
	}
	assertVariantStock(t, catalogStore, testOrderVariantStock)
}

func TestAdminOrdersRequireSession(t *testing.T) {
	handler, _, _, _ := newOrdersTestHandler(t)

	response, err := handler.Handle(context.Background(), adminRequest(http.MethodGet, "/admin/orders"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther || response.Headers["Location"] != "/admin/login" {
		t.Fatalf("status = %d Location = %q, want login redirect", response.StatusCode, response.Headers["Location"])
	}
}

func newOrdersTestHandler(t *testing.T) (*Handler, *commerce.MemoryStore, *catalog.MemoryStore, *time.Time) {
	t.Helper()
	handler, currentTime := newAuthTestHandler(t)
	catalogStore := newOrdersTestCatalogStore()
	commerceStore := commerce.NewMemoryStoreWithClock(func() time.Time { return *currentTime })
	handler.catalog = catalogStore
	handler.commerce = commerceStore
	handler.stock = catalogStore
	return handler, commerceStore, catalogStore, currentTime
}

func newOrdersTestCatalogStore() *catalog.MemoryStore {
	return catalog.NewMemoryStore([]catalog.Product{{
		ID:          testOrderProductID,
		Slug:        "thai-tea",
		Name:        "Thai Tea Sampler",
		Description: "A frozen-snapshot fixture product for admin order tests.",
		PriceCents:  1500,
		Status:      catalog.StatusActive,
		SortOrder:   10,
		Version:     1,
		Variants: []catalog.ProductVariant{{
			ID:            testOrderVariantID,
			Label:         "Box of 12",
			StockQuantity: testOrderVariantStock,
			Status:        catalog.StatusActive,
			SortOrder:     10,
		}},
	}}, nil)
}

// testOrderID returns a deterministic ID matching the ^[a-z0-9]{26}$ route
// contract for order, customer, and address identifiers.
func testOrderID(n int) string {
	return fmt.Sprintf("%026d", n)
}

func adminTestOrder(id string, status commerce.OrderStatus, createdAt time.Time) commerce.Order {
	order := commerce.Order{
		ID:         id,
		CustomerID: testOrderID(900),
		Email:      "shopper@example.test",
		Status:     status,
		Lines: []commerce.OrderLine{{
			Slug:           "thai-tea",
			ProductID:      testOrderProductID,
			Name:           "Thai Tea Sampler",
			VariantID:      testOrderVariantID,
			VariantLabel:   "Box of 12",
			UnitPriceCents: 1500,
			Quantity:       testOrderLineQuantity,
			LineTotalCents: 3000,
		}},
		SubtotalCents: 3000,
		ShippingCents: 0,
		TaxCents:      0,
		TotalCents:    3000,
		Currency:      "usd",
		ShippingAddress: commerce.OrderAddress{
			FullName:   "Ploy Shopper",
			Line1:      "99 Sukhumvit Road",
			City:       "Bangkok",
			Region:     "NY",
			PostalCode: "10110",
			Country:    "US",
		},
		CreatedAt: createdAt.UTC(),
		UpdatedAt: createdAt.UTC(),
	}
	if status != commerce.OrderStatusPendingPayment {
		order.StripePaymentIntentID = "pi_test_admin_123"
		order.StripeCheckoutSessionID = "cs_test_admin_123"
	}
	return order
}

// createTestPaymentSession registers the order's hosted checkout session with
// the fake provider, mirroring the place-order step the customer flow runs.
func createTestPaymentSession(t *testing.T, provider *payments.FakeProvider, order commerce.Order) payments.Session {
	t.Helper()
	session, err := provider.CreatePaymentSession(context.Background(), payments.PaymentSessionInput{
		OrderID:    order.ID,
		CustomerID: order.CustomerID,
		TotalCents: order.TotalCents,
		SuccessURL: "http://127.0.0.1:8080/checkout/confirm?session_id={CHECKOUT_SESSION_ID}",
		CancelURL:  "http://127.0.0.1:8080/checkout?canceled=1",
	})
	if err != nil {
		t.Fatalf("CreatePaymentSession returned error: %v", err)
	}
	return session
}

func createTestOrder(t *testing.T, store *commerce.MemoryStore, order commerce.Order) commerce.Order {
	t.Helper()
	created, err := store.CreateOrder(context.Background(), order)
	if err != nil {
		t.Fatalf("CreateOrder returned error: %v", err)
	}
	return created
}

func getTestOrder(t *testing.T, store *commerce.MemoryStore, orderID string) commerce.Order {
	t.Helper()
	order, found, err := store.GetOrder(context.Background(), orderID)
	if err != nil {
		t.Fatalf("GetOrder returned error: %v", err)
	}
	if !found {
		t.Fatalf("order %q not found", orderID)
	}
	return order
}

func assertVariantStock(t *testing.T, store *catalog.MemoryStore, want int) {
	t.Helper()
	product, found, err := store.GetProductByID(context.Background(), testOrderProductID)
	if err != nil {
		t.Fatalf("GetProductByID returned error: %v", err)
	}
	if !found {
		t.Fatalf("product %q not found", testOrderProductID)
	}
	stock, ok := product.AvailableStockForVariant(testOrderVariantID)
	if !ok {
		t.Fatalf("variant %q not found on product %q", testOrderVariantID, testOrderProductID)
	}
	if stock != want {
		t.Fatalf("variant stock = %d, want %d", stock, want)
	}
}

// refundStubProvider injects CreateRefund failures over the fake provider for
// the admin error-path tests.
type refundStubProvider struct {
	*payments.FakeProvider
	createRefundErr error
}

func (p *refundStubProvider) CreateRefund(ctx context.Context, input payments.RefundInput) (payments.Refund, error) {
	if p.createRefundErr != nil {
		return payments.Refund{}, p.createRefundErr
	}
	return p.FakeProvider.CreateRefund(ctx, input)
}

func TestAdminOrderRefundFromPaidSettlesAndReleasesStock(t *testing.T) {
	handler, commerceStore, catalogStore, currentTime := newOrdersTestHandler(t)
	handler.payments = payments.NewFakeProvider()
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, *currentTime))

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/refund", url.Values{})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusSeeOther, response.Body)
	}
	if response.Headers["Location"] != "/admin/orders/"+order.ID+"?saved=refunded" {
		t.Fatalf("Location = %q, want saved=refunded", response.Headers["Location"])
	}

	refunded := getTestOrder(t, commerceStore, order.ID)
	if refunded.Status != commerce.OrderStatusRefunded {
		t.Fatalf("status = %q, want refunded", refunded.Status)
	}
	if refunded.StripeRefundID != "re_fake_"+order.ID+"_1" || refunded.RefundAttempt != 1 || refunded.RefundedAt.IsZero() {
		t.Fatalf("refund fields = %#v, want attempt-1 settled refund", refunded)
	}
	history := refunded.StatusHistory
	if len(history) != 3 || history[1].Status != commerce.OrderStatusRefundPending || history[1].Actor != commerce.OrderActorAdmin || history[2].Status != commerce.OrderStatusRefunded || history[2].Actor != commerce.OrderActorStripe {
		t.Fatalf("history = %#v, want refund_pending(admin) then refunded(stripe)", history)
	}
	assertVariantStock(t, catalogStore, testOrderVariantStock+testOrderLineQuantity)

	detail := authenticatedProductGet(t, handler, "/admin/orders/"+order.ID+"?saved=refunded")
	for _, want := range []string{
		"Refund issued and confirmed.",
		`data-testid="admin-order-refund-id"`,
		"re_fake_" + order.ID + "_1",
		">Refunded</span>",
	} {
		if !strings.Contains(detail.Body, want) {
			t.Fatalf("detail body missing %q", want)
		}
	}
	for _, gone := range []string{`data-testid="admin-order-refund"`, `data-testid="admin-order-cancel"`} {
		if strings.Contains(detail.Body, gone+`"`) || strings.Contains(detail.Body, gone+" ") {
			t.Fatalf("detail body still offers %q for a refunded order", gone)
		}
	}
	if strings.Contains(detail.Body, `data-testid="admin-order-refund-button"`) {
		t.Fatalf("refunded order must not offer the refund form")
	}
}

func TestAdminOrderRefundFromShippedKeepsStock(t *testing.T) {
	handler, commerceStore, catalogStore, currentTime := newOrdersTestHandler(t)
	handler.payments = payments.NewFakeProvider()
	shippedAt := currentTime.UTC()
	seed := adminTestOrder(testOrderID(1), commerce.OrderStatusShipped, *currentTime)
	seed.ShippedAt = shippedAt
	order := createTestOrder(t, commerceStore, seed)

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/refund", url.Values{})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusSeeOther, response.Body)
	}
	if response.Headers["Location"] != "/admin/orders/"+order.ID+"?saved=refunded" {
		t.Fatalf("Location = %q, want saved=refunded", response.Headers["Location"])
	}
	refunded := getTestOrder(t, commerceStore, order.ID)
	if refunded.Status != commerce.OrderStatusRefunded {
		t.Fatalf("status = %q, want refunded", refunded.Status)
	}
	// Shipped goods are never restocked.
	assertVariantStock(t, catalogStore, testOrderVariantStock)
	if !refunded.StockReleasedAt.IsZero() {
		t.Fatalf("StockReleasedAt = %v, want zero for a shipped refund", refunded.StockReleasedAt)
	}
}

func TestAdminOrderRefundPendingThenReconcileOnRender(t *testing.T) {
	handler, commerceStore, catalogStore, currentTime := newOrdersTestHandler(t)
	provider := payments.NewFakeProvider()
	handler.payments = provider
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, *currentTime))
	provider.SetNextRefundOutcome(payments.RefundStatusPending, "")

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/refund", url.Values{})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusSeeOther, response.Body)
	}
	if response.Headers["Location"] != "/admin/orders/"+order.ID+"?saved=refund_pending" {
		t.Fatalf("Location = %q, want saved=refund_pending", response.Headers["Location"])
	}

	detail := authenticatedProductGet(t, handler, "/admin/orders/"+order.ID+"?saved=refund_pending")
	if !strings.Contains(detail.Body, "Refund issued. Stripe is processing it; the status updates when it settles.") {
		t.Fatalf("detail body missing the refund_pending flash")
	}
	if strings.Contains(detail.Body, `data-testid="admin-order-refund-button"`) {
		t.Fatalf("refund_pending order must not offer the refund form")
	}

	// The refund settles at Stripe; opening the order page reconciles it.
	if err := provider.SettleRefund("re_fake_"+order.ID+"_1", payments.RefundStatusSucceeded, ""); err != nil {
		t.Fatalf("SettleRefund returned error: %v", err)
	}
	settledDetail := authenticatedProductGet(t, handler, "/admin/orders/"+order.ID)
	if !strings.Contains(settledDetail.Body, ">Refunded</span>") {
		t.Fatalf("detail body missing the Refunded chip after reconcile")
	}
	settled := getTestOrder(t, commerceStore, order.ID)
	if settled.Status != commerce.OrderStatusRefunded || settled.RefundedAt.IsZero() {
		t.Fatalf("order = %s refundedAt %v, want reconciled to refunded", settled.Status, settled.RefundedAt)
	}
	assertVariantStock(t, catalogStore, testOrderVariantStock+testOrderLineQuantity)
}

func TestAdminOrderRefundFailedThenRetrySucceeds(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	provider := payments.NewFakeProvider()
	handler.payments = provider
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, *currentTime))
	provider.SetNextRefundOutcome(payments.RefundStatusFailed, "expired_or_canceled_card")

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/refund", url.Values{})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusSeeOther, response.Body)
	}
	if response.Headers["Location"] != "/admin/orders/"+order.ID+"?saved=refund_failed" {
		t.Fatalf("Location = %q, want saved=refund_failed", response.Headers["Location"])
	}

	detail := authenticatedProductGet(t, handler, "/admin/orders/"+order.ID+"?saved=refund_failed")
	for _, want := range []string{
		`data-testid="admin-order-refund-failed"`,
		"expired_or_canceled_card",
		"Retry the refund below, or resolve it in the Stripe dashboard.",
		"Retry refund",
	} {
		if !strings.Contains(detail.Body, want) {
			t.Fatalf("detail body missing %q", want)
		}
	}

	// Retry: a fresh attempt-2 refund settles and clears the banner.
	retry := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/refund", url.Values{})
	if retry.StatusCode != http.StatusSeeOther {
		t.Fatalf("retry status = %d, want %d; body = %q", retry.StatusCode, http.StatusSeeOther, retry.Body)
	}
	if retry.Headers["Location"] != "/admin/orders/"+order.ID+"?saved=refunded" {
		t.Fatalf("retry Location = %q, want saved=refunded", retry.Headers["Location"])
	}
	refunded := getTestOrder(t, commerceStore, order.ID)
	if refunded.StripeRefundID != "re_fake_"+order.ID+"_2" || refunded.RefundAttempt != 2 || refunded.RefundFailureReason != "" {
		t.Fatalf("retried order = %#v, want attempt-2 refund with the reason cleared", refunded)
	}
	afterRetry := authenticatedProductGet(t, handler, "/admin/orders/"+order.ID)
	if strings.Contains(afterRetry.Body, `data-testid="admin-order-refund-failed"`) {
		t.Fatalf("refund-failed banner still renders after a successful retry")
	}
}

func TestAdminOrderTerminalAutoRefundFailureSurface(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	seed := adminTestOrder(testOrderID(1), commerce.OrderStatusCanceled, *currentTime)
	seed.StripeRefundID = "re_test_admin_1"
	seed.RefundAttempt = 1
	seed.RefundFailureReason = "expired_or_canceled_card"
	order := createTestOrder(t, commerceStore, seed)

	detail := authenticatedProductGet(t, handler, "/admin/orders/"+order.ID)
	if detail.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", detail.StatusCode, http.StatusOK)
	}
	for _, want := range []string{
		`data-testid="admin-order-refund-failed"`,
		"expired_or_canceled_card",
		"Resolve it in the Stripe dashboard.",
	} {
		if !strings.Contains(detail.Body, want) {
			t.Fatalf("detail body missing %q", want)
		}
	}
	// Terminal orders never get an in-app retry.
	if strings.Contains(detail.Body, `data-testid="admin-order-refund"`) {
		t.Fatalf("terminal order must not offer the refund form")
	}
	if strings.Contains(detail.Body, "Retry the refund below") {
		t.Fatalf("terminal order banner must use the no-retry copy")
	}
}

func TestAdminOrderRefundProviderErrorLeavesOrderUnchanged(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	handler.payments = &refundStubProvider{FakeProvider: payments.NewFakeProvider(), createRefundErr: errors.New("stripe api down")}
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, *currentTime))

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/refund", url.Values{})
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusBadGateway, response.Body)
	}
	if !strings.Contains(response.Body, "the order is unchanged") {
		t.Fatalf("body missing the unchanged-order copy: %q", response.Body)
	}
	if got := getTestOrder(t, commerceStore, order.ID).Status; got != commerce.OrderStatusPaid {
		t.Fatalf("status = %q, want paid untouched", got)
	}
}

func TestAdminOrderRefundAlreadyRefundedDirectsToDashboard(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	handler.payments = &refundStubProvider{FakeProvider: payments.NewFakeProvider(), createRefundErr: fmt.Errorf("%w: payment intent pi_test_admin_123", payments.ErrChargeAlreadyRefunded)}
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, *currentTime))

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/refund", url.Values{})
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusConflict, response.Body)
	}
	if !strings.Contains(response.Body, "Reconcile it in the Stripe dashboard.") {
		t.Fatalf("body missing the dashboard reconciliation copy: %q", response.Body)
	}
	if got := getTestOrder(t, commerceStore, order.ID).Status; got != commerce.OrderStatusPaid {
		t.Fatalf("status = %q, want paid untouched", got)
	}
}

func TestAdminOrderRefundRejectsNonRefundableStatuses(t *testing.T) {
	for _, status := range []commerce.OrderStatus{
		commerce.OrderStatusPendingPayment,
		commerce.OrderStatusCanceled,
		commerce.OrderStatusExpired,
		commerce.OrderStatusPaymentFailed,
	} {
		t.Run(string(status), func(t *testing.T) {
			handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
			handler.payments = payments.NewFakeProvider()
			order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), status, *currentTime))

			response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/refund", url.Values{})
			if response.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusConflict, response.Body)
			}
			if !strings.Contains(response.Body, "Only paid, shipped, or delivered orders can be refunded.") {
				t.Fatalf("body missing refund guard error: %q", response.Body)
			}
			if got := getTestOrder(t, commerceStore, order.ID).Status; got != status {
				t.Fatalf("status = %q, want unchanged %q", got, status)
			}
		})
	}
}

func TestAdminOrderRefundRequiresCSRF(t *testing.T) {
	handler, commerceStore, catalogStore, currentTime := newOrdersTestHandler(t)
	handler.payments = payments.NewFakeProvider()
	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, *currentTime))

	login := loginResponse(t, handler)
	request := adminFormRequest(http.MethodPost, "/admin/orders/"+order.ID+"/refund", url.Values{})
	request.Cookies = []string{cookiePair(t, responseCookie(t, login, adminSessionCookieName))}
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	unchanged := getTestOrder(t, commerceStore, order.ID)
	if unchanged.Status != commerce.OrderStatusPaid || unchanged.StripeRefundID != "" {
		t.Fatalf("order = %#v, want paid untouched after rejected CSRF", unchanged)
	}
	assertVariantStock(t, catalogStore, testOrderVariantStock)
}

// TestLocalDemoHandlerRefundsEndToEnd pins the devserver demo wiring: the
// shared fake payment provider refunds a paid order end-to-end, so the demo
// loop (pay via fake-pay, refund from the admin desk) works offline.
func TestLocalDemoHandlerRefundsEndToEnd(t *testing.T) {
	provider := payments.NewFakeProvider()
	catalogStore := newOrdersTestCatalogStore()
	commerceStore := commerce.NewMemoryStore()
	handler := NewLocalDemoHandler(Credentials{
		PasswordHash:  testPasswordHash(t),
		SessionSecret: testSessionSecret,
	}, catalogStore, commerceStore, catalogStore, provider)

	order := createTestOrder(t, commerceStore, adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, time.Now()))

	response := authenticatedProductPost(t, handler, "/admin/orders/"+order.ID+"/refund", url.Values{})
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusSeeOther, response.Body)
	}
	if response.Headers["Location"] != "/admin/orders/"+order.ID+"?saved=refunded" {
		t.Fatalf("Location = %q, want saved=refunded", response.Headers["Location"])
	}
	refunded := getTestOrder(t, commerceStore, order.ID)
	if refunded.Status != commerce.OrderStatusRefunded || refunded.StripeRefundID == "" {
		t.Fatalf("order = %#v, want a refunded order carrying its fake refund ID", refunded)
	}
	assertVariantStock(t, catalogStore, testOrderVariantStock+testOrderLineQuantity)
}

// adminGuestTestOrder is the adminTestOrder fixture with the guest marker:
// CustomerID == "" and a guest contact email.
func adminGuestTestOrder(id string, status commerce.OrderStatus, createdAt time.Time) commerce.Order {
	order := adminTestOrder(id, status, createdAt)
	order.CustomerID = ""
	order.Email = "guest@example.test"
	return order
}

// TestAdminGuestOrderLifecycle pins that customer-less orders ride the full
// refund-era admin lifecycle: list + detail render (with the guest markers),
// pending cancel releases stock, paid cancel is refused toward Refund, the
// fulfillment chain works, and a paid guest refund settles end-to-end with the
// stock released. Flat test on purpose: authenticated admin requests must not
// run inside t.Run subtests (the test password derives from t.Name()).
func TestAdminGuestOrderLifecycle(t *testing.T) {
	handler, commerceStore, catalogStore, currentTime := newOrdersTestHandler(t)
	handler.payments = payments.NewFakeProvider()

	pendingGuest := createTestOrder(t, commerceStore, adminGuestTestOrder(testOrderID(1), commerce.OrderStatusPendingPayment, currentTime.Add(-3*time.Minute)))
	fulfillGuest := createTestOrder(t, commerceStore, adminGuestTestOrder(testOrderID(2), commerce.OrderStatusPaid, currentTime.Add(-2*time.Minute)))
	refundGuest := createTestOrder(t, commerceStore, adminGuestTestOrder(testOrderID(3), commerce.OrderStatusPaid, currentTime.Add(-1*time.Minute)))

	// List: guest rows render the email plus the Guest suffix.
	list := authenticatedProductGet(t, handler, "/admin/orders")
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want %d", list.StatusCode, http.StatusOK)
	}
	if got := strings.Count(list.Body, `data-testid="admin-order-row"`); got != 3 {
		t.Fatalf("admin-order-row count = %d, want 3", got)
	}
	if !strings.Contains(list.Body, "guest@example.test") {
		t.Fatalf("list body missing the guest email")
	}
	if got := strings.Count(list.Body, "· Guest"); got != 3 {
		t.Fatalf("list Guest suffix count = %d, want 3", got)
	}

	// Detail: guest chip next to the status chip.
	detail := authenticatedProductGet(t, handler, "/admin/orders/"+pendingGuest.ID)
	if detail.StatusCode != http.StatusOK {
		t.Fatalf("detail status = %d, want %d", detail.StatusCode, http.StatusOK)
	}
	for _, want := range []string{`data-testid="admin-order-guest"`, "Guest checkout", "guest@example.test"} {
		if !strings.Contains(detail.Body, want) {
			t.Fatalf("detail body missing %q", want)
		}
	}

	// Pending cancel releases the reservation.
	cancel := authenticatedProductPost(t, handler, "/admin/orders/"+pendingGuest.ID+"/cancel", url.Values{})
	if cancel.StatusCode != http.StatusSeeOther || cancel.Headers["Location"] != "/admin/orders/"+pendingGuest.ID+"?saved=canceled" {
		t.Fatalf("pending cancel = %d %q, want 303 saved=canceled", cancel.StatusCode, cancel.Headers["Location"])
	}
	if got := getTestOrder(t, commerceStore, pendingGuest.ID).Status; got != commerce.OrderStatusCanceled {
		t.Fatalf("pending guest order status = %q, want canceled", got)
	}
	assertVariantStock(t, catalogStore, testOrderVariantStock+testOrderLineQuantity)

	// Paid cancel is refused toward the Refund action.
	paidCancel := authenticatedProductPost(t, handler, "/admin/orders/"+fulfillGuest.ID+"/cancel", url.Values{})
	if paidCancel.StatusCode != http.StatusConflict {
		t.Fatalf("paid cancel status = %d, want %d", paidCancel.StatusCode, http.StatusConflict)
	}
	if !strings.Contains(paidCancel.Body, "Only pending orders can be canceled. Refund paid orders instead.") {
		t.Fatalf("paid cancel body missing the refund-instead guidance: %q", paidCancel.Body)
	}

	// Fulfillment chain: tracking ships, advance delivers.
	tracking := authenticatedProductPost(t, handler, "/admin/orders/"+fulfillGuest.ID+"/tracking", url.Values{
		"carrier":         {"Thailand Post"},
		"tracking_number": {"TH1234567890"},
	})
	if tracking.StatusCode != http.StatusSeeOther || tracking.Headers["Location"] != "/admin/orders/"+fulfillGuest.ID+"?saved=shipped" {
		t.Fatalf("tracking = %d %q, want 303 saved=shipped", tracking.StatusCode, tracking.Headers["Location"])
	}
	if got := getTestOrder(t, commerceStore, fulfillGuest.ID).Status; got != commerce.OrderStatusShipped {
		t.Fatalf("guest order status = %q, want shipped", got)
	}
	advance := authenticatedProductPost(t, handler, "/admin/orders/"+fulfillGuest.ID+"/advance", url.Values{"to_status": {"delivered"}})
	if advance.StatusCode != http.StatusSeeOther {
		t.Fatalf("advance status = %d, want %d", advance.StatusCode, http.StatusSeeOther)
	}
	if got := getTestOrder(t, commerceStore, fulfillGuest.ID).Status; got != commerce.OrderStatusDelivered {
		t.Fatalf("guest order status = %q, want delivered", got)
	}

	// Refund of a paid guest order settles end-to-end through the shared fake
	// provider; stock comes back because the order never shipped.
	refund := authenticatedProductPost(t, handler, "/admin/orders/"+refundGuest.ID+"/refund", url.Values{})
	if refund.StatusCode != http.StatusSeeOther || refund.Headers["Location"] != "/admin/orders/"+refundGuest.ID+"?saved=refunded" {
		t.Fatalf("refund = %d %q, want 303 saved=refunded", refund.StatusCode, refund.Headers["Location"])
	}
	refunded := getTestOrder(t, commerceStore, refundGuest.ID)
	if refunded.Status != commerce.OrderStatusRefunded {
		t.Fatalf("refunded guest order status = %q, want refunded", refunded.Status)
	}
	if refunded.StripeRefundID != "re_fake_"+refundGuest.ID+"_1" {
		t.Fatalf("refund id = %q, want the fake provider refund", refunded.StripeRefundID)
	}
	assertVariantStock(t, catalogStore, testOrderVariantStock+2*testOrderLineQuantity)

	refundedDetail := authenticatedProductGet(t, handler, "/admin/orders/"+refundGuest.ID+"?saved=refunded")
	for _, want := range []string{"Refund issued and confirmed.", `data-testid="admin-order-guest"`, ">Refunded</span>"} {
		if !strings.Contains(refundedDetail.Body, want) {
			t.Fatalf("refunded detail body missing %q", want)
		}
	}
}

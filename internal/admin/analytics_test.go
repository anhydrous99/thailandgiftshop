package admin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/aws/aws-lambda-go/events"
)

func analyticsDay(day int, hour int) time.Time {
	return time.Date(2026, 6, day, hour, 0, 0, 0, time.UTC)
}

// analyticsFixtureOrders returns a mixed set of orders inside a window anchored
// at 2026-06-15: paid, shipped, delivered, refunded, and unpaid-pending.
func analyticsFixtureOrders() []commerce.Order {
	return []commerce.Order{
		{
			ID: "a", Status: commerce.OrderStatusPaid, TotalCents: 3000, Currency: "usd",
			CreatedAt: analyticsDay(10, 9), PaidAt: analyticsDay(10, 10),
			Lines: []commerce.OrderLine{{ProductID: "p1", Name: "Tea", Quantity: 2, LineTotalCents: 3000}},
		},
		{
			ID: "b", Status: commerce.OrderStatusShipped, TotalCents: 5000, Currency: "usd",
			CreatedAt: analyticsDay(12, 9), PaidAt: analyticsDay(12, 10), ShippedAt: analyticsDay(13, 10),
			Lines: []commerce.OrderLine{{ProductID: "p2", Name: "Scarf", Quantity: 1, LineTotalCents: 5000}},
		},
		{
			ID: "c", Status: commerce.OrderStatusDelivered, TotalCents: 2000, Currency: "usd",
			CreatedAt: analyticsDay(5, 9), PaidAt: analyticsDay(5, 10), ShippedAt: analyticsDay(6, 10), DeliveredAt: analyticsDay(8, 10),
			Lines: []commerce.OrderLine{{ProductID: "p1", Name: "Tea", Quantity: 1, LineTotalCents: 2000}},
		},
		{
			ID: "d", Status: commerce.OrderStatusRefunded, TotalCents: 4000, Currency: "usd",
			CreatedAt: analyticsDay(8, 9), PaidAt: analyticsDay(8, 10), RefundedAt: analyticsDay(9, 10),
			Lines: []commerce.OrderLine{{ProductID: "p3", Name: "Mug", Quantity: 1, LineTotalCents: 4000}},
		},
		{
			ID: "e", Status: commerce.OrderStatusPendingPayment, TotalCents: 1500, Currency: "usd",
			CreatedAt: analyticsDay(9, 9),
			Lines:     []commerce.OrderLine{{ProductID: "p1", Name: "Tea", Quantity: 1, LineTotalCents: 1500}},
		},
	}
}

func TestComputeAnalyticsRevenueAndOrders(t *testing.T) {
	now := analyticsDay(15, 12)
	window := parseAnalyticsRange("30d", now)
	summary := computeAnalytics(analyticsFixtureOrders(), now, window)

	if summary.TotalOrders != 5 {
		t.Fatalf("TotalOrders = %d, want 5", summary.TotalOrders)
	}
	if summary.PaidOrders != 4 {
		t.Fatalf("PaidOrders = %d, want 4", summary.PaidOrders)
	}
	if summary.GrossRevenueCents != 14000 {
		t.Fatalf("GrossRevenueCents = %d, want 14000", summary.GrossRevenueCents)
	}
	if summary.RefundedCents != 4000 {
		t.Fatalf("RefundedCents = %d, want 4000", summary.RefundedCents)
	}
	if summary.NetRevenueCents != 10000 {
		t.Fatalf("NetRevenueCents = %d, want 10000", summary.NetRevenueCents)
	}
	if summary.AOVCents != 3500 {
		t.Fatalf("AOVCents = %d, want 3500", summary.AOVCents)
	}
}

func TestComputeAnalyticsStatusBreakdownOrdered(t *testing.T) {
	now := analyticsDay(15, 12)
	summary := computeAnalytics(analyticsFixtureOrders(), now, parseAnalyticsRange("30d", now))

	want := []analyticsStatusCount{
		{Status: commerce.OrderStatusPendingPayment, Count: 1},
		{Status: commerce.OrderStatusPaid, Count: 1},
		{Status: commerce.OrderStatusShipped, Count: 1},
		{Status: commerce.OrderStatusDelivered, Count: 1},
		{Status: commerce.OrderStatusRefunded, Count: 1},
	}
	if len(summary.StatusCounts) != len(want) {
		t.Fatalf("StatusCounts = %#v, want %d entries", summary.StatusCounts, len(want))
	}
	for i, expected := range want {
		if summary.StatusCounts[i] != expected {
			t.Fatalf("StatusCounts[%d] = %#v, want %#v", i, summary.StatusCounts[i], expected)
		}
	}
}

func TestComputeAnalyticsTopProductsRankedByRevenueThenUnits(t *testing.T) {
	now := analyticsDay(15, 12)
	summary := computeAnalytics(analyticsFixtureOrders(), now, parseAnalyticsRange("30d", now))

	if len(summary.TopProducts) != 3 {
		t.Fatalf("TopProducts = %#v, want 3", summary.TopProducts)
	}
	// p1 (Tea): units 2+1=3, revenue 5000 from paid orders only (the unpaid
	// pending order's line is excluded). p2 (Scarf): 5000 ties on revenue but
	// fewer units, so it ranks below Tea. p3 (Mug): 4000.
	if summary.TopProducts[0].Name != "Tea" || summary.TopProducts[0].Units != 3 || summary.TopProducts[0].RevenueCents != 5000 {
		t.Fatalf("TopProducts[0] = %#v, want Tea units 3 revenue 5000", summary.TopProducts[0])
	}
	if summary.TopProducts[1].Name != "Scarf" || summary.TopProducts[1].RevenueCents != 5000 {
		t.Fatalf("TopProducts[1] = %#v, want Scarf revenue 5000", summary.TopProducts[1])
	}
	if summary.TopProducts[2].Name != "Mug" || summary.TopProducts[2].RevenueCents != 4000 {
		t.Fatalf("TopProducts[2] = %#v, want Mug revenue 4000", summary.TopProducts[2])
	}
}

func TestComputeAnalyticsTopProductsSeparatesVariants(t *testing.T) {
	now := analyticsDay(15, 12)
	orders := []commerce.Order{
		{
			ID: "small", Status: commerce.OrderStatusPaid, TotalCents: 1000, Currency: "usd",
			CreatedAt: analyticsDay(14, 9), PaidAt: analyticsDay(14, 10),
			Lines: []commerce.OrderLine{{ProductID: "prod_scarf", Name: "Scarf", VariantID: "small", VariantLabel: "Small", Quantity: 1, LineTotalCents: 1000}},
		},
		{
			ID: "medium", Status: commerce.OrderStatusPaid, TotalCents: 2000, Currency: "usd",
			CreatedAt: analyticsDay(14, 11), PaidAt: analyticsDay(14, 12),
			Lines: []commerce.OrderLine{{ProductID: "prod_scarf", Name: "Scarf", VariantID: "medium", VariantLabel: "Medium", Quantity: 2, LineTotalCents: 2000}},
		},
	}

	summary := computeAnalytics(orders, now, parseAnalyticsRange("30d", now))
	if len(summary.TopProducts) != 2 {
		t.Fatalf("TopProducts = %#v, want separate rows for each variant", summary.TopProducts)
	}
	if summary.TopProducts[0].VariantLabel != "Medium" || summary.TopProducts[0].RevenueCents != 2000 || summary.TopProducts[0].Units != 2 {
		t.Fatalf("TopProducts[0] = %#v, want Medium variant revenue 2000 units 2", summary.TopProducts[0])
	}
	if summary.TopProducts[1].VariantLabel != "Small" || summary.TopProducts[1].RevenueCents != 1000 || summary.TopProducts[1].Units != 1 {
		t.Fatalf("TopProducts[1] = %#v, want Small variant revenue 1000 units 1", summary.TopProducts[1])
	}
}

func TestComputeAnalyticsBacklogAndLatency(t *testing.T) {
	now := analyticsDay(15, 12)
	summary := computeAnalytics(analyticsFixtureOrders(), now, parseAnalyticsRange("30d", now))

	if summary.AwaitingFulfillment != 1 {
		t.Fatalf("AwaitingFulfillment = %d, want 1", summary.AwaitingFulfillment)
	}
	if summary.InTransit != 1 {
		t.Fatalf("InTransit = %d, want 1", summary.InTransit)
	}
	// Oldest awaiting = now - order A PaidAt (Jun 10 10:00) = 5d 2h.
	if !summary.HasOldestAwaiting || summary.OldestAwaitingAge != 5*24*time.Hour+2*time.Hour {
		t.Fatalf("OldestAwaitingAge = %v (has=%v), want 122h", summary.OldestAwaitingAge, summary.HasOldestAwaiting)
	}
	if !summary.HasPaidToShipped || summary.AvgPaidToShipped != 24*time.Hour {
		t.Fatalf("AvgPaidToShipped = %v (has=%v), want 24h", summary.AvgPaidToShipped, summary.HasPaidToShipped)
	}
	if !summary.HasShippedToDelivered || summary.AvgShippedToDelivered != 48*time.Hour {
		t.Fatalf("AvgShippedToDelivered = %v (has=%v), want 48h", summary.AvgShippedToDelivered, summary.HasShippedToDelivered)
	}
}

func TestComputeAnalyticsDailyBucketsAttributeByCreatedAt(t *testing.T) {
	now := analyticsDay(15, 12)
	window := parseAnalyticsRange("30d", now)
	summary := computeAnalytics(analyticsFixtureOrders(), now, window)

	if len(summary.Buckets) != 30 {
		t.Fatalf("Buckets = %d, want 30 daily buckets", len(summary.Buckets))
	}
	// Window starts 2026-05-17; order A (created Jun 10, paid 3000) lands in
	// bucket index 24.
	jun10 := summary.Buckets[24]
	if !jun10.Start.Equal(analyticsDay(10, 0)) {
		t.Fatalf("Buckets[24].Start = %s, want 2026-06-10", jun10.Start)
	}
	if jun10.RevenueCents != 3000 || jun10.Orders != 1 {
		t.Fatalf("Buckets[24] = %#v, want revenue 3000 / 1 order", jun10)
	}
	// The unpaid pending order (Jun 9) counts toward order volume but not
	// revenue.
	jun9 := summary.Buckets[23]
	if jun9.Orders != 1 || jun9.RevenueCents != 0 {
		t.Fatalf("Buckets[23] = %#v, want 1 order / 0 revenue", jun9)
	}
}

func TestComputeAnalyticsWeeklyBucketsForLongWindow(t *testing.T) {
	now := analyticsDay(15, 12)
	window := parseAnalyticsRange("12m", now)
	if !window.Weekly {
		t.Fatalf("12m window Weekly = false, want true")
	}
	summary := computeAnalytics(nil, now, window)
	// ceil(365 / 7) = 53 weekly buckets.
	if len(summary.Buckets) != 53 {
		t.Fatalf("Buckets = %d, want 53 weekly buckets", len(summary.Buckets))
	}
}

func TestParseAnalyticsRange(t *testing.T) {
	now := analyticsDay(15, 12)
	tests := []struct {
		value      string
		wantKey    string
		wantDays   int
		wantWeekly bool
	}{
		{"", "30d", 30, false},
		{"7d", "7d", 7, false},
		{"30d", "30d", 30, false},
		{"90d", "90d", 90, false},
		{"12m", "12m", 365, true},
		{"bogus", "30d", 30, false},
	}
	for _, test := range tests {
		window := parseAnalyticsRange(test.value, now)
		if window.Key != test.wantKey || window.Days != test.wantDays || window.Weekly != test.wantWeekly {
			t.Fatalf("parseAnalyticsRange(%q) = key %q days %d weekly %v, want %q/%d/%v", test.value, window.Key, window.Days, window.Weekly, test.wantKey, test.wantDays, test.wantWeekly)
		}
	}

	window := parseAnalyticsRange("30d", now)
	if !window.Start.Equal(time.Date(2026, 5, 17, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("30d Start = %s, want 2026-05-17 00:00 UTC", window.Start)
	}
	if !window.End.Equal(now.UTC()) {
		t.Fatalf("30d End = %s, want %s", window.End, now.UTC())
	}
}

func TestFormatAnalyticsMoney(t *testing.T) {
	tests := []struct {
		cents int
		want  string
	}{
		{0, "$0.00"},
		{5, "$0.05"},
		{1599, "$15.99"},
		{100000, "$1,000.00"},
		{1234567, "$12,345.67"},
		{-550, "-$5.50"},
	}
	for _, test := range tests {
		if got := formatAnalyticsMoney(test.cents); got != test.want {
			t.Fatalf("formatAnalyticsMoney(%d) = %q, want %q", test.cents, got, test.want)
		}
	}
}

func TestAnalyticsPercentText(t *testing.T) {
	tests := []struct {
		value int
		total int
		want  string
	}{
		{0, 0, "0%"},
		{1, 0, "0%"},
		{1, 4, "25%"},
		{3, 5, "60%"},
	}
	for _, test := range tests {
		if got := analyticsPercentText(test.value, test.total); got != test.want {
			t.Fatalf("analyticsPercentText(%d, %d) = %q, want %q", test.value, test.total, got, test.want)
		}
	}
}

func TestAnalyticsDurationLabel(t *testing.T) {
	tests := []struct {
		d    time.Duration
		ok   bool
		want string
	}{
		{0, false, "—"},
		{30 * time.Second, true, "<1m"},
		{45 * time.Minute, true, "45m"},
		{90 * time.Minute, true, "1h 30m"},
		{2 * time.Hour, true, "2h"},
		{26 * time.Hour, true, "1d 2h"},
		{48 * time.Hour, true, "2d"},
	}
	for _, test := range tests {
		if got := analyticsDurationLabel(test.d, test.ok); got != test.want {
			t.Fatalf("analyticsDurationLabel(%v, %v) = %q, want %q", test.d, test.ok, got, test.want)
		}
	}
}

// --- Handler integration ---

func authenticatedAdminRequest(t *testing.T, handler *Handler, method string, path string) events.APIGatewayV2HTTPResponse {
	t.Helper()
	login := loginResponse(t, handler)
	requestPath := path
	query := ""
	if before, after, found := strings.Cut(path, "?"); found {
		requestPath = before
		query = after
	}
	request := adminRequest(method, requestPath)
	request.RawQueryString = query
	request.QueryStringParameters = map[string]string{}
	if values, err := url.ParseQuery(query); err == nil {
		for key, vals := range values {
			if len(vals) > 0 {
				request.QueryStringParameters[key] = vals[0]
			}
		}
	}
	request.Cookies = []string{
		cookiePair(t, responseCookie(t, login, adminSessionCookieName)),
		cookiePair(t, responseCookie(t, login, adminCSRFCookieName)),
	}
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle %s %s returned error: %v", method, path, err)
	}
	return response
}

func seedAnalyticsHandlerOrders(t *testing.T, handler *Handler, commerceStore *commerce.MemoryStore, currentTime time.Time) {
	t.Helper()
	paid := adminTestOrder(testOrderID(1), commerce.OrderStatusPaid, currentTime.Add(-48*time.Hour))
	paid.PaidAt = currentTime.Add(-47 * time.Hour)
	createTestOrder(t, commerceStore, paid)

	refunded := adminTestOrder(testOrderID(2), commerce.OrderStatusRefunded, currentTime.Add(-24*time.Hour))
	refunded.PaidAt = currentTime.Add(-23 * time.Hour)
	refunded.RefundedAt = currentTime.Add(-12 * time.Hour)
	createTestOrder(t, commerceStore, refunded)
}

func TestAnalyticsPageRendersMetrics(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	seedAnalyticsHandlerOrders(t, handler, commerceStore, *currentTime)

	response := authenticatedProductGet(t, handler, "/admin/analytics")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %q", response.StatusCode, http.StatusOK, response.Body)
	}
	for _, want := range []string{
		`data-testid="admin-nav-analytics" aria-current="page"`,
		`data-testid="admin-analytics-range"`,
		`data-testid="admin-analytics-range-30d" aria-current="page"`,
		`data-testid="admin-analytics-gross-revenue"`,
		`data-testid="admin-analytics-net-revenue"`,
		`data-testid="admin-analytics-aov"`,
		`data-testid="admin-analytics-orders"`,
		`data-testid="admin-analytics-awaiting"`,
		`data-testid="admin-analytics-product-row"`,
		`Thai Tea Sampler`,
		`>$60.00</p>`, // gross revenue = two paid orders at $30.00
		`<svg`,
	} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("analytics page missing %q: %q", want, response.Body)
		}
	}
	assertNoStore(t, response)
}

func TestAnalyticsRangeSwitchMarksActiveOption(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	seedAnalyticsHandlerOrders(t, handler, commerceStore, *currentTime)

	response := authenticatedProductGet(t, handler, "/admin/analytics?range=7d")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if !strings.Contains(response.Body, `data-testid="admin-analytics-range-7d" aria-current="page"`) {
		t.Fatalf("range=7d not marked active: %q", response.Body)
	}
	if strings.Contains(response.Body, `data-testid="admin-analytics-range-30d" aria-current="page"`) {
		t.Fatalf("range=30d should not be active when range=7d selected: %q", response.Body)
	}
}

func TestAnalyticsEmptyState(t *testing.T) {
	handler, _, _, _ := newOrdersTestHandler(t)

	response := authenticatedProductGet(t, handler, "/admin/analytics")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if !strings.Contains(response.Body, `data-testid="admin-analytics-empty"`) {
		t.Fatalf("empty analytics page missing empty state: %q", response.Body)
	}
	if strings.Contains(response.Body, `data-testid="admin-analytics-product-row"`) {
		t.Fatalf("empty analytics page rendered product rows: %q", response.Body)
	}
}

func TestAnalyticsUnauthenticatedRedirectsToLogin(t *testing.T) {
	handler, _, _, _ := newOrdersTestHandler(t)

	response, err := handler.Handle(context.Background(), adminRequest(http.MethodGet, "/admin/analytics"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusSeeOther)
	}
	if response.Headers["Location"] != "/admin/login" {
		t.Fatalf("Location = %q, want /admin/login", response.Headers["Location"])
	}
}

func TestAnalyticsMethodNotAllowed(t *testing.T) {
	handler, _, _, _ := newOrdersTestHandler(t)

	response := authenticatedAdminRequest(t, handler, http.MethodPut, "/admin/analytics")
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusMethodNotAllowed)
	}
	if response.Headers["Allow"] != adminAllowedMethods {
		t.Fatalf("Allow = %q, want %q", response.Headers["Allow"], adminAllowedMethods)
	}
}

func TestAnalyticsHeadBlanksBody(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	seedAnalyticsHandlerOrders(t, handler, commerceStore, *currentTime)

	response := authenticatedAdminRequest(t, handler, http.MethodHead, "/admin/analytics")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if response.Body != "" {
		t.Fatalf("HEAD body = %q, want empty", response.Body)
	}
}

// TestAnalyticsHasNoInlineScriptOrStyle guards the strict CSP: the analytics
// page (including its server-rendered SVG charts) must never emit an inline
// <script>, <style>, or style="" attribute.
func TestAnalyticsHasNoInlineScriptOrStyle(t *testing.T) {
	handler, commerceStore, _, currentTime := newOrdersTestHandler(t)
	seedAnalyticsHandlerOrders(t, handler, commerceStore, *currentTime)

	response := authenticatedProductGet(t, handler, "/admin/analytics")
	for _, forbidden := range []string{"<script", "<style", "style=\""} {
		if strings.Contains(response.Body, forbidden) {
			t.Fatalf("analytics page contains CSP-violating %q: %q", forbidden, response.Body)
		}
	}
}

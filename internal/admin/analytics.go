package admin

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anhydrous99/thailandgiftshop/internal/commerce"
	"github.com/anhydrous99/thailandgiftshop/internal/httpapi"
	"github.com/aws/aws-lambda-go/events"
)

const (
	// analyticsStorePageSize is the page size used to walk ListOrdersInRange.
	analyticsStorePageSize = 500
	// analyticsMaxOrders caps how many orders one analytics render aggregates,
	// bounding Lambda time and memory even if a window is unexpectedly dense.
	// When the cap stops the walk early the view flags the figures as partial.
	//
	// Keep this an exact multiple of analyticsStorePageSize: loadAnalyticsOrders
	// relies on the cap landing on a page boundary so its truncated flag (the
	// next cursor being non-zero) accurately reflects whether more orders
	// remained. A non-multiple could stop mid-page and under-report truncation.
	analyticsMaxOrders = 5000
	// analyticsTopProductLimit is how many best sellers to list.
	analyticsTopProductLimit = 10
	// analyticsDailyBucketMaxDays switches the time series from daily to weekly
	// buckets once the window grows beyond this many days.
	analyticsDailyBucketMaxDays = 92
	// defaultAnalyticsRangeKey is the window used when ?range= is absent or
	// unrecognized.
	defaultAnalyticsRangeKey = "30d"
)

// analyticsRangeOption is a selectable reporting window. Days drives both the
// date-bounded order query and the time-series bucketing.
type analyticsRangeOption struct {
	Key   string
	Label string
	Days  int
}

var analyticsRangeOptions = []analyticsRangeOption{
	{Key: "7d", Label: "7 days", Days: 7},
	{Key: "30d", Label: "30 days", Days: 30},
	{Key: "90d", Label: "90 days", Days: 90},
	{Key: "12m", Label: "12 months", Days: 365},
}

// analyticsWindow is a resolved reporting window: the inclusive [Start, End]
// CreatedAt bounds passed to the store plus the bucket granularity.
type analyticsWindow struct {
	Key    string
	Label  string
	Days   int
	Start  time.Time
	End    time.Time
	Weekly bool
}

// analyticsBucket is one time-series cell (a UTC day, or a 7-day span for long
// windows).
type analyticsBucket struct {
	Start        time.Time
	Label        string
	RevenueCents int
	Orders       int
}

type analyticsStatusCount struct {
	Status commerce.OrderStatus
	Count  int
}

type analyticsProductStat struct {
	Key          string
	Name         string
	VariantLabel string
	Units        int
	RevenueCents int
}

// analyticsSummary is the pure aggregation result over a window's orders.
type analyticsSummary struct {
	TotalOrders       int
	PaidOrders        int
	GrossRevenueCents int
	RefundedCents     int
	NetRevenueCents   int
	AOVCents          int

	AwaitingFulfillment   int
	InTransit             int
	OldestAwaitingAge     time.Duration
	HasOldestAwaiting     bool
	AvgPaidToShipped      time.Duration
	HasPaidToShipped      bool
	AvgShippedToDelivered time.Duration
	HasShippedToDelivered bool

	StatusCounts []analyticsStatusCount
	TopProducts  []analyticsProductStat
	Buckets      []analyticsBucket
}

// analyticsStatusOrder is the lifecycle-ordered status sequence for the
// orders-by-status breakdown; only non-zero statuses render.
var analyticsStatusOrder = []commerce.OrderStatus{
	commerce.OrderStatusPendingPayment,
	commerce.OrderStatusPaid,
	commerce.OrderStatusShipped,
	commerce.OrderStatusDelivered,
	commerce.OrderStatusRefundPending,
	commerce.OrderStatusRefunded,
	commerce.OrderStatusRefundFailed,
	commerce.OrderStatusPaymentFailed,
	commerce.OrderStatusCanceled,
	commerce.OrderStatusExpired,
}

func startOfUTCDay(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

// parseAnalyticsRange resolves a ?range= value into a concrete window anchored
// at now. The window spans Days whole UTC days ending today: Start is the
// midnight that begins the oldest covered day, End is now.
func parseAnalyticsRange(value string, now time.Time) analyticsWindow {
	option := analyticsRangeOptions[1]
	for _, candidate := range analyticsRangeOptions {
		if candidate.Key == defaultAnalyticsRangeKey {
			option = candidate
			break
		}
	}
	trimmed := strings.TrimSpace(value)
	for _, candidate := range analyticsRangeOptions {
		if candidate.Key == trimmed {
			option = candidate
			break
		}
	}

	end := now.UTC()
	start := startOfUTCDay(end).AddDate(0, 0, -(option.Days - 1))
	return analyticsWindow{
		Key:    option.Key,
		Label:  option.Label,
		Days:   option.Days,
		Start:  start,
		End:    end,
		Weekly: option.Days > analyticsDailyBucketMaxDays,
	}
}

func analyticsBucketLabel(start time.Time) string {
	return start.UTC().Format("Jan 2")
}

// bucketIndex returns the time-series bucket a CreatedAt falls into, or -1 when
// it predates the window. Both ends are normalized to UTC midnight so the day
// math is exact.
func bucketIndex(bucketStart time.Time, created time.Time, bucketDays int) int {
	diff := startOfUTCDay(created).Sub(bucketStart)
	if diff < 0 {
		return -1
	}
	return int(diff/(24*time.Hour)) / bucketDays
}

// computeAnalytics aggregates a window's orders into business metrics. It is a
// pure function (no store, no clock beyond the passed now) so it is exercised
// directly in unit tests. Revenue is attributed to order-placement date
// (CreatedAt) and recognized once an order has been paid (PaidAt set); refunds
// subtract the full order total (no partial-refund amount is persisted).
func computeAnalytics(orders []commerce.Order, now time.Time, window analyticsWindow) analyticsSummary {
	summary := analyticsSummary{TotalOrders: len(orders)}

	bucketDays := 1
	if window.Weekly {
		bucketDays = 7
	}
	bucketStart := startOfUTCDay(window.Start)
	bucketCount := (window.Days + bucketDays - 1) / bucketDays
	if bucketCount < 1 {
		bucketCount = 1
	}
	buckets := make([]analyticsBucket, bucketCount)
	for i := range buckets {
		start := bucketStart.AddDate(0, 0, i*bucketDays)
		buckets[i] = analyticsBucket{Start: start, Label: analyticsBucketLabel(start)}
	}

	statusCounts := map[commerce.OrderStatus]int{}
	productStats := map[string]*analyticsProductStat{}
	productOrder := make([]string, 0)

	var oldestAwaiting time.Time
	var paidToShippedTotal time.Duration
	paidToShippedCount := 0
	var shippedToDeliveredTotal time.Duration
	shippedToDeliveredCount := 0

	for _, order := range orders {
		statusCounts[order.Status]++

		paid := !order.PaidAt.IsZero()
		if paid {
			summary.PaidOrders++
			summary.GrossRevenueCents += order.TotalCents
		}
		if order.Status == commerce.OrderStatusRefunded {
			summary.RefundedCents += order.TotalCents
		}

		if idx := bucketIndex(bucketStart, order.CreatedAt, bucketDays); idx >= 0 && idx < len(buckets) {
			buckets[idx].Orders++
			if paid {
				buckets[idx].RevenueCents += order.TotalCents
			}
		}

		if paid {
			for _, line := range order.Lines {
				key := analyticsProductKey(line)
				stat, ok := productStats[key]
				if !ok {
					stat = &analyticsProductStat{Key: key, Name: line.Name, VariantLabel: line.VariantLabel}
					productStats[key] = stat
					productOrder = append(productOrder, key)
				}
				stat.Units += line.Quantity
				stat.RevenueCents += line.LineTotalCents
			}
		}

		switch order.Status {
		case commerce.OrderStatusPaid:
			summary.AwaitingFulfillment++
			if paid && (oldestAwaiting.IsZero() || order.PaidAt.Before(oldestAwaiting)) {
				oldestAwaiting = order.PaidAt
			}
		case commerce.OrderStatusShipped:
			summary.InTransit++
		}

		if paid && !order.ShippedAt.IsZero() && order.ShippedAt.After(order.PaidAt) {
			paidToShippedTotal += order.ShippedAt.Sub(order.PaidAt)
			paidToShippedCount++
		}
		if !order.ShippedAt.IsZero() && !order.DeliveredAt.IsZero() && order.DeliveredAt.After(order.ShippedAt) {
			shippedToDeliveredTotal += order.DeliveredAt.Sub(order.ShippedAt)
			shippedToDeliveredCount++
		}
	}

	summary.NetRevenueCents = summary.GrossRevenueCents - summary.RefundedCents
	if summary.PaidOrders > 0 {
		summary.AOVCents = summary.GrossRevenueCents / summary.PaidOrders
	}
	if !oldestAwaiting.IsZero() {
		summary.OldestAwaitingAge = now.Sub(oldestAwaiting)
		summary.HasOldestAwaiting = true
	}
	if paidToShippedCount > 0 {
		summary.AvgPaidToShipped = paidToShippedTotal / time.Duration(paidToShippedCount)
		summary.HasPaidToShipped = true
	}
	if shippedToDeliveredCount > 0 {
		summary.AvgShippedToDelivered = shippedToDeliveredTotal / time.Duration(shippedToDeliveredCount)
		summary.HasShippedToDelivered = true
	}

	summary.Buckets = buckets
	summary.StatusCounts = analyticsStatusBreakdown(statusCounts)
	summary.TopProducts = analyticsTopProducts(productStats, productOrder)
	return summary
}

func analyticsProductKey(line commerce.OrderLine) string {
	productKey := line.ProductID
	if productKey == "" {
		productKey = line.Name
	}
	return productKey + "\x00" + line.VariantID + "\x00" + line.VariantLabel
}

func analyticsStatusBreakdown(counts map[commerce.OrderStatus]int) []analyticsStatusCount {
	out := make([]analyticsStatusCount, 0, len(counts))
	for _, status := range analyticsStatusOrder {
		if count := counts[status]; count > 0 {
			out = append(out, analyticsStatusCount{Status: status, Count: count})
		}
	}
	return out
}

func analyticsTopProducts(stats map[string]*analyticsProductStat, order []string) []analyticsProductStat {
	out := make([]analyticsProductStat, 0, len(order))
	for _, key := range order {
		out = append(out, *stats[key])
	}
	sort.SliceStable(out, func(i int, j int) bool {
		if out[i].RevenueCents != out[j].RevenueCents {
			return out[i].RevenueCents > out[j].RevenueCents
		}
		if out[i].Units != out[j].Units {
			return out[i].Units > out[j].Units
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > analyticsTopProductLimit {
		out = out[:analyticsTopProductLimit]
	}
	return out
}

// --- View model ---

type analyticsRangeViewOption struct {
	Key    string
	Label  string
	Href   string
	Active bool
}

type analyticsStatusRowViewModel struct {
	Label     string
	ChipClass string
	Count     string
	Percent   string
}

type analyticsProductRowViewModel struct {
	Rank    string
	Name    string
	Variant string
	Units   string
	Revenue string
}

type analyticsViewModel struct {
	CSRFValue string

	RangeKey     string
	RangeOptions []analyticsRangeViewOption
	WindowLabel  string

	TotalOrders int
	Truncated   bool
	HasOrders   bool

	GrossRevenue string
	NetRevenue   string
	Refunded     string
	AOV          string
	PaidOrders   string
	OrdersCount  string

	AwaitingFulfillment string
	InTransit           string
	OldestAwaiting      string
	PaidToShipped       string
	ShippedToDelivered  string

	StatusRows  []analyticsStatusRowViewModel
	TopProducts []analyticsProductRowViewModel

	Chart analyticsChart
}

func isAdminAnalyticsPath(path string) bool {
	return path == "/admin/analytics"
}

func (h *Handler) handleAdminAnalytics(ctx context.Context, request events.APIGatewayV2HTTPRequest, session adminSession) events.APIGatewayV2HTTPResponse {
	method := httpapi.Method(request)
	if method != http.MethodGet && method != http.MethodHead {
		return adminHTMLResponse(http.StatusMethodNotAllowed, "Method not allowed", map[string]string{"Allow": adminAllowedMethods}, nil)
	}

	csrfValue, cookies, err := h.csrfForProtectedResponse(ctx, request, session)
	if err != nil {
		logAdminError("analytics: issue csrf token", err)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	vm, err := h.analyticsViewModel(ctx, csrfValue, request)
	if err != nil {
		logAdminError("analytics: build view model", err)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	body, err := renderAdminAnalytics(ctx, vm)
	if err != nil {
		logAdminError("analytics: render", err)
		return adminHTMLResponse(http.StatusInternalServerError, "Internal server error", nil, nil)
	}
	if method == http.MethodHead {
		body = ""
	}
	return adminHTMLResponse(http.StatusOK, body, nil, cookies)
}

func (h *Handler) analyticsViewModel(ctx context.Context, csrfValue string, request events.APIGatewayV2HTTPRequest) (analyticsViewModel, error) {
	now := h.currentTime()
	window := parseAnalyticsRange(request.QueryStringParameters["range"], now)

	orders, truncated, err := h.loadAnalyticsOrders(ctx, window)
	if err != nil {
		return analyticsViewModel{}, err
	}
	summary := computeAnalytics(orders, now, window)

	vm := analyticsViewModel{
		CSRFValue:   csrfValue,
		RangeKey:    window.Key,
		WindowLabel: analyticsWindowLabel(window),
		TotalOrders: summary.TotalOrders,
		Truncated:   truncated,
		HasOrders:   summary.TotalOrders > 0,

		GrossRevenue: formatAnalyticsMoney(summary.GrossRevenueCents),
		NetRevenue:   formatAnalyticsMoney(summary.NetRevenueCents),
		Refunded:     formatAnalyticsMoney(summary.RefundedCents),
		AOV:          formatAnalyticsMoney(summary.AOVCents),
		PaidOrders:   strconv.Itoa(summary.PaidOrders),
		OrdersCount:  strconv.Itoa(summary.TotalOrders),

		AwaitingFulfillment: strconv.Itoa(summary.AwaitingFulfillment),
		InTransit:           strconv.Itoa(summary.InTransit),
		OldestAwaiting:      analyticsDurationLabel(summary.OldestAwaitingAge, summary.HasOldestAwaiting),
		PaidToShipped:       analyticsDurationLabel(summary.AvgPaidToShipped, summary.HasPaidToShipped),
		ShippedToDelivered:  analyticsDurationLabel(summary.AvgShippedToDelivered, summary.HasShippedToDelivered),

		Chart: buildAnalyticsChart(summary.Buckets, formatAnalyticsMoney),
	}

	for _, option := range analyticsRangeOptions {
		vm.RangeOptions = append(vm.RangeOptions, analyticsRangeViewOption{
			Key:    option.Key,
			Label:  option.Label,
			Href:   "/admin/analytics?range=" + option.Key,
			Active: option.Key == window.Key,
		})
	}

	for _, status := range summary.StatusCounts {
		vm.StatusRows = append(vm.StatusRows, analyticsStatusRowViewModel{
			Label:     adminOrderStatusLabel(status.Status),
			ChipClass: adminOrderStatusChipClass(status.Status),
			Count:     strconv.Itoa(status.Count),
			Percent:   analyticsPercentText(status.Count, summary.TotalOrders),
		})
	}

	for i, product := range summary.TopProducts {
		vm.TopProducts = append(vm.TopProducts, analyticsProductRowViewModel{
			Rank:    strconv.Itoa(i + 1),
			Name:    product.Name,
			Variant: product.VariantLabel,
			Units:   strconv.Itoa(product.Units),
			Revenue: formatAnalyticsMoney(product.RevenueCents),
		})
	}

	return vm, nil
}

// loadAnalyticsOrders pages the date-bounded order query, stopping at the
// safety cap. truncated reports that the cap halted the walk before the window
// was exhausted.
func (h *Handler) loadAnalyticsOrders(ctx context.Context, window analyticsWindow) ([]commerce.Order, bool, error) {
	store := h.commerceStore()
	orders := make([]commerce.Order, 0, analyticsStorePageSize)
	cursor := commerce.OrderCursor{}
	for {
		page, err := store.ListOrdersInRange(ctx, window.Start, window.End, analyticsStorePageSize, cursor)
		if err != nil {
			return nil, false, err
		}
		for _, order := range page.Orders {
			orders = append(orders, order)
			if len(orders) >= analyticsMaxOrders {
				return orders, !page.NextCursor.IsZero(), nil
			}
		}
		if page.NextCursor.IsZero() {
			return orders, false, nil
		}
		cursor = page.NextCursor
	}
}

func renderAdminAnalytics(ctx context.Context, vm analyticsViewModel) (string, error) {
	var body strings.Builder
	if err := adminAnalyticsPage(vm).Render(ctx, &body); err != nil {
		return "", err
	}
	return body.String(), nil
}

func analyticsWindowLabel(window analyticsWindow) string {
	start := startOfUTCDay(window.Start)
	end := window.End.UTC()
	return start.Format("Jan 2") + " – " + end.Format("Jan 2, 2006") + " UTC"
}

// formatAnalyticsMoney renders integer cents as "$1,234.56" with thousands
// separators, handling negatives (net revenue can dip below zero when refunds
// in the window exceed new paid revenue).
func formatAnalyticsMoney(cents int) string {
	negative := cents < 0
	if negative {
		cents = -cents
	}
	whole := strconv.Itoa(cents / 100)
	frac := cents % 100

	var b strings.Builder
	if negative {
		b.WriteByte('-')
	}
	b.WriteByte('$')
	n := len(whole)
	for i := 0; i < n; i++ {
		if i > 0 && (n-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(whole[i])
	}
	b.WriteByte('.')
	if frac < 10 {
		b.WriteByte('0')
	}
	b.WriteString(strconv.Itoa(frac))
	return b.String()
}

func analyticsPercentText(value int, total int) string {
	if total <= 0 || value <= 0 {
		return "0%"
	}
	return strconv.Itoa(value*100/total) + "%"
}

func analyticsDurationLabel(d time.Duration, ok bool) string {
	if !ok {
		return "—"
	}
	if d < time.Minute {
		return "<1m"
	}
	days := int(d / (24 * time.Hour))
	hours := int((d % (24 * time.Hour)) / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	switch {
	case days > 0:
		if hours > 0 {
			return fmt.Sprintf("%dd %dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	case hours > 0:
		if minutes > 0 {
			return fmt.Sprintf("%dh %dm", hours, minutes)
		}
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

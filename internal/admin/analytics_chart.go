package admin

import (
	"math"
	"strconv"
	"strings"
)

// Chart geometry is expressed in a fixed SVG viewBox coordinate space. The
// templ renders the <svg> with viewBox="0 0 width height" and class="w-full
// h-auto" so it scales responsively. All positioning rides on SVG geometry
// attributes (x/y/width/height/points) and Tailwind color classes — never
// inline style — to stay within the strict CSP (style-src 'self').
const (
	analyticsChartWidth     = 1000.0
	analyticsChartHeight    = 280.0
	analyticsChartPadTop    = 18.0
	analyticsChartPadBottom = 30.0
	// analyticsChartLabelTarget caps how many x-axis labels render so dense
	// windows (90 daily bars) stay legible.
	analyticsChartLabelTarget = 12
)

// analyticsBarGeometry is one revenue bar plus its x-axis label position.
type analyticsBarGeometry struct {
	X         float64
	Y         float64
	Width     float64
	Height    float64
	Label     string
	ShowLabel bool
	LabelX    float64
	// Aria is the accessible per-bar description (revenue + order count).
	Aria string
}

// analyticsChart is the fully computed revenue-and-orders time series geometry.
type analyticsChart struct {
	Width           float64
	Height          float64
	BaselineY       float64
	Bars            []analyticsBarGeometry
	LinePoints      string
	HasData         bool
	MaxRevenueLabel string
	MaxOrders       int
}

// buildAnalyticsChart turns time-series buckets into SVG geometry: revenue
// bars scaled to the busiest bucket, plus an order-count overlay polyline
// scaled independently. moneyLabel formats cents for the y-axis and per-bar
// aria text.
func buildAnalyticsChart(buckets []analyticsBucket, moneyLabel func(int) string) analyticsChart {
	chart := analyticsChart{
		Width:     analyticsChartWidth,
		Height:    analyticsChartHeight,
		BaselineY: analyticsChartHeight - analyticsChartPadBottom,
	}
	if len(buckets) == 0 {
		chart.MaxRevenueLabel = moneyLabel(0)
		return chart
	}

	maxRevenue := 0
	maxOrders := 0
	for _, bucket := range buckets {
		if bucket.RevenueCents > maxRevenue {
			maxRevenue = bucket.RevenueCents
		}
		if bucket.Orders > maxOrders {
			maxOrders = bucket.Orders
		}
	}
	chart.MaxOrders = maxOrders
	chart.MaxRevenueLabel = moneyLabel(maxRevenue)
	chart.HasData = maxRevenue > 0 || maxOrders > 0

	plotHeight := chart.BaselineY - analyticsChartPadTop
	count := len(buckets)
	slot := chart.Width / float64(count)
	barWidth := slot * 0.62
	gap := (slot - barWidth) / 2

	labelStride := 1
	if count > analyticsChartLabelTarget {
		labelStride = (count + analyticsChartLabelTarget - 1) / analyticsChartLabelTarget
	}

	points := make([]string, 0, count)
	for i, bucket := range buckets {
		x := float64(i)*slot + gap

		height := 0.0
		if maxRevenue > 0 {
			height = plotHeight * float64(bucket.RevenueCents) / float64(maxRevenue)
		}
		chart.Bars = append(chart.Bars, analyticsBarGeometry{
			X:         round2(x),
			Y:         round2(chart.BaselineY - height),
			Width:     round2(barWidth),
			Height:    round2(height),
			Label:     bucket.Label,
			ShowLabel: i%labelStride == 0,
			LabelX:    round2(x + barWidth/2),
			Aria:      bucket.Label + ": " + moneyLabel(bucket.RevenueCents) + ", " + strconv.Itoa(bucket.Orders) + " orders",
		})

		orderY := chart.BaselineY
		if maxOrders > 0 {
			orderY = chart.BaselineY - plotHeight*float64(bucket.Orders)/float64(maxOrders)
		}
		points = append(points, fmtCoord(round2(x+barWidth/2))+","+fmtCoord(round2(orderY)))
	}
	chart.LinePoints = strings.Join(points, " ")
	return chart
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}

// fmtCoord renders a coordinate as the shortest exact decimal for an SVG
// attribute (e.g. 12.5 rather than 12.500000).
func fmtCoord(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

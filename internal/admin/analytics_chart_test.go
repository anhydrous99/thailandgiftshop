package admin

import (
	"strings"
	"testing"
)

func TestBuildAnalyticsChartEmptyBuckets(t *testing.T) {
	chart := buildAnalyticsChart(nil, formatAnalyticsMoney)
	if chart.HasData {
		t.Fatalf("HasData = true, want false for no buckets")
	}
	if len(chart.Bars) != 0 {
		t.Fatalf("Bars = %d, want 0", len(chart.Bars))
	}
	if chart.LinePoints != "" {
		t.Fatalf("LinePoints = %q, want empty", chart.LinePoints)
	}
	if chart.MaxRevenueLabel != "$0.00" {
		t.Fatalf("MaxRevenueLabel = %q, want $0.00", chart.MaxRevenueLabel)
	}
}

func TestBuildAnalyticsChartScalesBarsToPeak(t *testing.T) {
	buckets := []analyticsBucket{
		{Label: "Jun 1", RevenueCents: 0, Orders: 0},
		{Label: "Jun 2", RevenueCents: 5000, Orders: 2},
		{Label: "Jun 3", RevenueCents: 2500, Orders: 1},
	}
	chart := buildAnalyticsChart(buckets, formatAnalyticsMoney)

	if !chart.HasData {
		t.Fatalf("HasData = false, want true")
	}
	if len(chart.Bars) != 3 {
		t.Fatalf("Bars = %d, want 3", len(chart.Bars))
	}
	if chart.MaxOrders != 2 {
		t.Fatalf("MaxOrders = %d, want 2", chart.MaxOrders)
	}
	if chart.MaxRevenueLabel != "$50.00" {
		t.Fatalf("MaxRevenueLabel = %q, want $50.00", chart.MaxRevenueLabel)
	}

	// plotHeight = (height - padBottom) - padTop = (280 - 30) - 18 = 232.
	if chart.Bars[0].Height != 0 {
		t.Fatalf("empty bar Height = %v, want 0", chart.Bars[0].Height)
	}
	if chart.Bars[0].Y != chart.BaselineY {
		t.Fatalf("empty bar Y = %v, want baseline %v", chart.Bars[0].Y, chart.BaselineY)
	}
	if chart.Bars[1].Height != 232 {
		t.Fatalf("peak bar Height = %v, want 232 (full plot height)", chart.Bars[1].Height)
	}
	if chart.Bars[1].Y != 18 {
		t.Fatalf("peak bar Y = %v, want 18 (top of plot)", chart.Bars[1].Y)
	}
	if chart.Bars[2].Height != 116 {
		t.Fatalf("half bar Height = %v, want 116", chart.Bars[2].Height)
	}

	// One polyline vertex per bucket.
	if got := len(strings.Fields(chart.LinePoints)); got != 3 {
		t.Fatalf("LinePoints vertices = %d, want 3 (%q)", got, chart.LinePoints)
	}

	// Per-bar aria text carries both the money and the order count.
	if !strings.Contains(chart.Bars[1].Aria, "$50.00") || !strings.Contains(chart.Bars[1].Aria, "2 orders") {
		t.Fatalf("bar aria = %q, want money + order count", chart.Bars[1].Aria)
	}
}

func TestBuildAnalyticsChartThinsLabelsWhenDense(t *testing.T) {
	buckets := make([]analyticsBucket, 30)
	for i := range buckets {
		buckets[i] = analyticsBucket{Label: "d", RevenueCents: (i + 1) * 100, Orders: i}
	}
	chart := buildAnalyticsChart(buckets, formatAnalyticsMoney)

	// 30 buckets > 12 target -> stride 3: indices 0,3,6,... show labels.
	if !chart.Bars[0].ShowLabel {
		t.Fatalf("bar 0 ShowLabel = false, want true")
	}
	if chart.Bars[1].ShowLabel || chart.Bars[2].ShowLabel {
		t.Fatalf("bars 1/2 ShowLabel = true, want thinned out")
	}
	if !chart.Bars[3].ShowLabel {
		t.Fatalf("bar 3 ShowLabel = false, want true at stride 3")
	}

	shown := 0
	for _, bar := range chart.Bars {
		if bar.ShowLabel {
			shown++
		}
	}
	if shown > 12 {
		t.Fatalf("shown labels = %d, want <= 12", shown)
	}
}

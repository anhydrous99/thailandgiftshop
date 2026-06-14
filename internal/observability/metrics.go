package observability

import (
	"encoding/json"
	"io"
	"maps"
	"time"
)

const Namespace = "ThailandGiftshop/App"

const (
	MetricAddressValidation   = "AddressValidation"
	MetricAdminLoginAttempt   = "AdminLoginAttempt"
	MetricAdminOriginRejected = "AdminOriginRejected"
	MetricCatalogOperation    = "CatalogOperation"
	MetricCatalogOperationMs  = "CatalogOperationMs"
	MetricCatalogWrite        = "CatalogWrite"
	MetricCheckoutPayment     = "CheckoutPayment"
	MetricCheckoutRefund      = "CheckoutRefund"
	MetricCommerceOperation   = "CommerceOperation"
	MetricCommerceOperationMs = "CommerceOperationMs"
	MetricCustomerAuth        = "CustomerAuth"
	MetricOrderEmail          = "OrderEmail"
	MetricOrderTransition     = "OrderTransition"
	MetricProductImageUpload  = "ProductImageUpload"
	MetricRouteColdStart      = "RouteColdStart"
	MetricRouteDurationMs     = "RouteDurationMs"
	MetricStockAdjust         = "StockAdjust"
	MetricStripeWebhook       = "StripeWebhook"
)

const UnitCount = "Count"
const UnitMilliseconds = "Milliseconds"

type Dimension struct {
	Name  string
	Value string
}

type Metric struct {
	Name       string
	Unit       string
	Value      float64
	Dimensions []Dimension
}

type Recorder interface {
	Record(metric Metric)
}

type NoopRecorder struct{}

func (NoopRecorder) Record(metric Metric) {}

type EMFRecorder struct {
	writer io.Writer
	now    func() time.Time
}

func NewEMFRecorder(writer io.Writer) *EMFRecorder {
	return &EMFRecorder{writer: writer, now: time.Now}
}

func NewTestEMFRecorder(writer io.Writer, now func() time.Time) *EMFRecorder {
	return &EMFRecorder{writer: writer, now: now}
}

func Count(name string, dimensions ...Dimension) Metric {
	return Metric{
		Name:       name,
		Unit:       UnitCount,
		Value:      1,
		Dimensions: dimensions,
	}
}

func Duration(name string, duration time.Duration, dimensions ...Dimension) Metric {
	return Metric{
		Name:       name,
		Unit:       UnitMilliseconds,
		Value:      float64(duration.Milliseconds()),
		Dimensions: dimensions,
	}
}

func Dim(name string, value string) Dimension {
	return Dimension{Name: name, Value: value}
}

func (r *EMFRecorder) Record(metric Metric) {
	if r == nil || r.writer == nil || metric.Name == "" {
		return
	}

	now := time.Now
	if r.now != nil {
		now = r.now
	}

	dimensionNames := make([]string, 0, len(metric.Dimensions))
	dimensionValues := make(map[string]any, len(metric.Dimensions))
	hasService := false
	hasOutcome := false
	for _, dimension := range metric.Dimensions {
		if dimension.Name == "" {
			continue
		}
		dimensionNames = append(dimensionNames, dimension.Name)
		dimensionValues[dimension.Name] = dimension.Value
		if dimension.Name == "Service" {
			hasService = true
		}
		if dimension.Name == "Outcome" {
			hasOutcome = true
		}
	}
	dimensionSets := [][]string{dimensionNames}
	if hasService && hasOutcome && len(dimensionNames) > 2 {
		dimensionSets = append(dimensionSets, []string{"Service", "Outcome"})
	}

	event := map[string]any{
		"_aws": map[string]any{
			"Timestamp": now().UnixMilli(),
			"CloudWatchMetrics": []map[string]any{
				{
					"Namespace":  Namespace,
					"Dimensions": dimensionSets,
					"Metrics": []map[string]string{
						{
							"Name": metric.Name,
							"Unit": metric.Unit,
						},
					},
				},
			},
		},
		metric.Name: metric.Value,
	}
	maps.Copy(event, dimensionValues)

	_ = json.NewEncoder(r.writer).Encode(event)
}

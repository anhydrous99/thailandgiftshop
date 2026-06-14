package admin

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/anhydrous99/thailandgiftshop/internal/observability"
)

func TestAdminMetricsRecordLoginOutcomes(t *testing.T) {
	handler, currentTime := newAuthTestHandler(t)
	recorder := &testMetricRecorder{}
	handler.metrics = recorder

	success := loginResponse(t, handler)
	if success.StatusCode != http.StatusSeeOther {
		t.Fatalf("success login status = %d, want %d", success.StatusCode, http.StatusSeeOther)
	}
	invalid, err := handler.Handle(context.Background(), adminFormRequest(http.MethodPost, "/admin/login", url.Values{
		"password": {string(testWrongPassword(t))},
	}))
	if err != nil {
		t.Fatalf("Handle invalid login returned error: %v", err)
	}
	if invalid.StatusCode != http.StatusUnauthorized {
		t.Fatalf("invalid login status = %d, want %d", invalid.StatusCode, http.StatusUnauthorized)
	}

	throttle := newTestAdminLoginThrottle()
	throttle.lockClient(unknownAdminLoginClient, currentTime.Add(adminLoginLockout))
	handler.loginThrottle = throttle
	throttled, err := handler.Handle(context.Background(), adminFormRequest(http.MethodPost, "/admin/login", url.Values{
		"password": {string(testPassword(t))},
	}))
	if err != nil {
		t.Fatalf("Handle throttled login returned error: %v", err)
	}
	if throttled.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("throttled login status = %d, want %d", throttled.StatusCode, http.StatusTooManyRequests)
	}

	assertRecordedMetric(t, recorder, observability.MetricAdminLoginAttempt, map[string]string{
		"Service":   metricServiceAdmin,
		"Operation": metricOperationLogin,
		"Outcome":   metricOutcomeSuccess,
	})
	assertRecordedMetric(t, recorder, observability.MetricAdminLoginAttempt, map[string]string{
		"Service":   metricServiceAdmin,
		"Operation": metricOperationLogin,
		"Outcome":   metricOutcomeInvalid,
	})
	assertRecordedMetric(t, recorder, observability.MetricAdminLoginAttempt, map[string]string{
		"Service":   metricServiceAdmin,
		"Operation": metricOperationLogin,
		"Outcome":   metricOutcomeThrottled,
	})
}

func TestAdminMetricsRecordOriginRejection(t *testing.T) {
	t.Setenv(EnvAdminOriginHeaderSecret, "expected-origin-secret")
	handler, _ := newAuthTestHandler(t)
	recorder := &testMetricRecorder{}
	handler.metrics = recorder

	response, err := handler.Handle(context.Background(), adminRequest(http.MethodGet, "/admin"))
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
	assertRecordedMetric(t, recorder, observability.MetricAdminOriginRejected, map[string]string{
		"Service":   metricServiceAdmin,
		"Operation": metricOperationOrigin,
		"Outcome":   metricOutcomeRejected,
	})
	assertRecordedMetricWithUnit(t, recorder, observability.MetricRouteDurationMs, observability.UnitMilliseconds, map[string]string{
		"Service": metricServiceAdmin,
		"Route":   "origin_rejected",
		"Method":  http.MethodGet,
		"Status":  "403",
	})
}

func TestAdminMetricsRecordCatalogWriteOutcomes(t *testing.T) {
	handler, _ := newProductTestHandler(t)
	recorder := &testMetricRecorder{}
	handler.metrics = recorder

	created := authenticatedProductPost(t, handler, "/admin/products", validProductForm(t, handler))
	if created.StatusCode != http.StatusSeeOther {
		t.Fatalf("created status = %d, want %d", created.StatusCode, http.StatusSeeOther)
	}

	invalidValues := validProductForm(t, handler)
	invalidValues.Set("name", "")
	invalid := authenticatedProductPost(t, handler, "/admin/products", invalidValues)
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, want %d", invalid.StatusCode, http.StatusBadRequest)
	}

	assertRecordedMetric(t, recorder, observability.MetricCatalogWrite, map[string]string{
		"Service":   metricServiceAdmin,
		"Entity":    metricEntityProduct,
		"Operation": metricOperationCreate,
		"Outcome":   metricOutcomeSuccess,
	})
	assertRecordedMetric(t, recorder, observability.MetricCatalogWrite, map[string]string{
		"Service":   metricServiceAdmin,
		"Entity":    metricEntityProduct,
		"Operation": metricOperationCreate,
		"Outcome":   metricOutcomeValidationError,
	})
}

func TestAdminMetricsRecordProductImageUploadOutcome(t *testing.T) {
	handler, _ := newAuthTestHandler(t)
	recorder := &testMetricRecorder{}
	handler.metrics = recorder
	handler.uploads = &fakeProductImageUploads{confirmURL: "/images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original.webp"}

	request := authenticatedUploadRequest(t, handler, productImageConfirmPath, map[string]any{
		"key":          "images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111/original.webp",
		"content_type": "image/webp",
		"size_bytes":   1024,
	}, validUploadCSRF)
	response, err := handler.Handle(context.Background(), request)
	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	assertRecordedMetric(t, recorder, observability.MetricProductImageUpload, map[string]string{
		"Service": metricServiceAdmin,
		"Step":    metricUploadStepConfirm,
		"Outcome": metricOutcomeSuccess,
	})
}

func TestAdminMetricsRecordRouteDurationAndColdStartWithoutChangingResponse(t *testing.T) {
	adminColdStartRecorded.Store(false)
	plainHandler, _ := newAuthTestHandler(t)
	plainResponse, err := plainHandler.Handle(context.Background(), adminRequest(http.MethodGet, "/admin/login"))
	if err != nil {
		t.Fatalf("plain Handle returned error: %v", err)
	}

	handler, _ := newAuthTestHandler(t)
	recorder := &testMetricRecorder{}
	handler.metrics = recorder
	instrumentedResponse, err := handler.Handle(context.Background(), adminRequest(http.MethodGet, "/admin/login"))
	if err != nil {
		t.Fatalf("instrumented Handle returned error: %v", err)
	}
	if instrumentedResponse.StatusCode != plainResponse.StatusCode || instrumentedResponse.Body != plainResponse.Body {
		t.Fatalf("instrumented response changed: got status %d body %q, want status %d body %q", instrumentedResponse.StatusCode, instrumentedResponse.Body, plainResponse.StatusCode, plainResponse.Body)
	}

	assertRecordedMetricWithUnit(t, recorder, observability.MetricRouteColdStart, observability.UnitCount, map[string]string{
		"Service": metricServiceAdmin,
	})
	assertRecordedMetricWithUnit(t, recorder, observability.MetricRouteDurationMs, observability.UnitMilliseconds, map[string]string{
		"Service": metricServiceAdmin,
		"Route":   "login",
		"Method":  http.MethodGet,
		"Status":  "200",
	})

	_, err = handler.Handle(context.Background(), adminRequest(http.MethodGet, "/products"))
	if err != nil {
		t.Fatalf("second Handle returned error: %v", err)
	}
	if got := recordedMetricCount(recorder, observability.MetricRouteColdStart); got != 1 {
		t.Fatalf("cold-start metric count = %d, want 1", got)
	}
	assertRecordedMetricWithUnit(t, recorder, observability.MetricRouteDurationMs, observability.UnitMilliseconds, map[string]string{
		"Service": metricServiceAdmin,
		"Route":   "not_found",
		"Method":  http.MethodGet,
		"Status":  "404",
	})
}

type testMetricRecorder struct {
	metrics []observability.Metric
}

func (r *testMetricRecorder) Record(metric observability.Metric) {
	r.metrics = append(r.metrics, metric)
}

func assertRecordedMetric(t *testing.T, recorder *testMetricRecorder, name string, dimensions map[string]string) {
	t.Helper()
	assertRecordedMetricWithUnit(t, recorder, name, "", dimensions)
}

func assertRecordedMetricWithUnit(t *testing.T, recorder *testMetricRecorder, name string, unit string, dimensions map[string]string) {
	t.Helper()
	for _, metric := range recorder.metrics {
		if metric.Name != name {
			continue
		}
		if unit != "" && metric.Unit != unit {
			continue
		}
		if metricDimensionsMatch(metric, dimensions) {
			return
		}
	}
	t.Fatalf("metric %q with unit %q and dimensions %#v not recorded; got %#v", name, unit, dimensions, recorder.metrics)
}

func recordedMetricCount(recorder *testMetricRecorder, name string) int {
	count := 0
	for _, metric := range recorder.metrics {
		if metric.Name == name {
			count++
		}
	}
	return count
}

func metricDimensionsMatch(metric observability.Metric, dimensions map[string]string) bool {
	if len(metric.Dimensions) != len(dimensions) {
		return false
	}
	for _, dimension := range metric.Dimensions {
		if dimensions[dimension.Name] != dimension.Value {
			return false
		}
	}
	return true
}

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
	handler.uploads = &fakeProductImageUploads{confirmURL: "/images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111.webp"}

	request := authenticatedUploadRequest(t, handler, productImageConfirmPath, map[string]any{
		"key":          "images/products/uploads/2026/06/11111111-1111-4111-8111-111111111111.webp",
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

type testMetricRecorder struct {
	metrics []observability.Metric
}

func (r *testMetricRecorder) Record(metric observability.Metric) {
	r.metrics = append(r.metrics, metric)
}

func assertRecordedMetric(t *testing.T, recorder *testMetricRecorder, name string, dimensions map[string]string) {
	t.Helper()
	for _, metric := range recorder.metrics {
		if metric.Name != name {
			continue
		}
		if metricDimensionsMatch(metric, dimensions) {
			return
		}
	}
	t.Fatalf("metric %q with dimensions %#v not recorded; got %#v", name, dimensions, recorder.metrics)
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

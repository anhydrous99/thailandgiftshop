package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestEMFRecorderWritesCloudWatchEmbeddedMetric(t *testing.T) {
	var output bytes.Buffer
	recorder := NewTestEMFRecorder(&output, func() time.Time {
		return time.Date(2026, 6, 8, 1, 2, 3, 4_000_000, time.UTC)
	})

	recorder.Record(Count(
		MetricAdminLoginAttempt,
		Dim("Service", "admin"),
		Dim("Operation", "login"),
		Dim("Outcome", "success"),
	))

	var event map[string]any
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatalf("unmarshal EMF event: %v\n%s", err, output.String())
	}
	if event[MetricAdminLoginAttempt] != float64(1) {
		t.Fatalf("%s = %#v, want 1", MetricAdminLoginAttempt, event[MetricAdminLoginAttempt])
	}
	for key, want := range map[string]string{"Service": "admin", "Operation": "login", "Outcome": "success"} {
		if event[key] != want {
			t.Fatalf("%s = %#v, want %q", key, event[key], want)
		}
	}

	aws, ok := event["_aws"].(map[string]any)
	if !ok {
		t.Fatalf("_aws = %#v, want object", event["_aws"])
	}
	if aws["Timestamp"] != float64(1780880523004) {
		t.Fatalf("Timestamp = %#v, want fixed unix millis", aws["Timestamp"])
	}
	metrics := aws["CloudWatchMetrics"].([]any)
	directive := metrics[0].(map[string]any)
	if directive["Namespace"] != Namespace {
		t.Fatalf("Namespace = %#v, want %q", directive["Namespace"], Namespace)
	}
	dimensions := directive["Dimensions"].([]any)[0].([]any)
	for index, want := range []string{"Service", "Operation", "Outcome"} {
		if dimensions[index] != want {
			t.Fatalf("dimension[%d] = %#v, want %q", index, dimensions[index], want)
		}
	}
	aggregateDimensions := directive["Dimensions"].([]any)[1].([]any)
	for index, want := range []string{"Service", "Outcome"} {
		if aggregateDimensions[index] != want {
			t.Fatalf("aggregate dimension[%d] = %#v, want %q", index, aggregateDimensions[index], want)
		}
	}
	metricDirectives := directive["Metrics"].([]any)
	metricDirective := metricDirectives[0].(map[string]any)
	if metricDirective["Name"] != MetricAdminLoginAttempt || metricDirective["Unit"] != UnitCount {
		t.Fatalf("metric directive = %#v", metricDirective)
	}
}

func TestEMFRecorderDropsWriterErrors(t *testing.T) {
	recorder := NewTestEMFRecorder(failingWriter{}, func() time.Time { return time.Unix(0, 0).UTC() })

	recorder.Record(Count(MetricCatalogWrite, Dim("Service", "admin")))
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

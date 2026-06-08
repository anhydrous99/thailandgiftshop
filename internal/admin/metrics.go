package admin

import (
	"errors"

	"github.com/anhydrous99/thailandgiftshop/internal/catalog"
	"github.com/anhydrous99/thailandgiftshop/internal/observability"
)

const (
	metricServiceAdmin = "admin"

	metricOperationLogin   = "login"
	metricOperationOrigin  = "origin"
	metricOperationCreate  = "create"
	metricOperationUpdate  = "update"
	metricOperationArchive = "archive"

	metricEntityProduct  = "product"
	metricEntityCategory = "category"

	metricUploadStepPresign = "presign"
	metricUploadStepConfirm = "confirm"
	metricUploadStepUnknown = "unknown"

	metricOutcomeSuccess         = "success"
	metricOutcomeInvalid         = "invalid"
	metricOutcomeRejected        = "rejected"
	metricOutcomeThrottled       = "throttled"
	metricOutcomeConflict        = "conflict"
	metricOutcomeValidationError = "validation_error"
	metricOutcomeNotFound        = "not_found"
	metricOutcomeError           = "error"
)

func (h *Handler) recordAdminLoginAttempt(outcome string) {
	h.recordMetric(observability.Count(
		observability.MetricAdminLoginAttempt,
		observability.Dim("Service", metricServiceAdmin),
		observability.Dim("Operation", metricOperationLogin),
		observability.Dim("Outcome", outcome),
	))
}

func (h *Handler) recordAdminOriginRejected() {
	h.recordMetric(observability.Count(
		observability.MetricAdminOriginRejected,
		observability.Dim("Service", metricServiceAdmin),
		observability.Dim("Operation", metricOperationOrigin),
		observability.Dim("Outcome", metricOutcomeRejected),
	))
}

func (h *Handler) recordCatalogWrite(entity string, operation string, outcome string) {
	h.recordMetric(observability.Count(
		observability.MetricCatalogWrite,
		observability.Dim("Service", metricServiceAdmin),
		observability.Dim("Entity", entity),
		observability.Dim("Operation", operation),
		observability.Dim("Outcome", outcome),
	))
}

func (h *Handler) recordProductImageUpload(step string, outcome string) {
	h.recordMetric(observability.Count(
		observability.MetricProductImageUpload,
		observability.Dim("Service", metricServiceAdmin),
		observability.Dim("Step", step),
		observability.Dim("Outcome", outcome),
	))
}

func (h *Handler) recordMetric(metric observability.Metric) {
	if h.metrics == nil {
		return
	}
	h.metrics.Record(metric)
}

func catalogWriteOutcome(err error) string {
	if errors.Is(err, catalog.ErrSlugConflict) || errors.Is(err, catalog.ErrVersionConflict) {
		return metricOutcomeConflict
	}
	return metricOutcomeError
}

func productImageUploadStep(path string) string {
	switch path {
	case productImagePresignPath:
		return metricUploadStepPresign
	case productImageConfirmPath:
		return metricUploadStepConfirm
	default:
		return metricUploadStepUnknown
	}
}

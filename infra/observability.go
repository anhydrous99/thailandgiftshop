package main

import (
	appobservability "github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudwatch"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssns"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssnssubscriptions"
	"github.com/aws/jsii-runtime-go"
)

func addObservability(stack awscdk.Stack, resources observabilityResources) {
	period5m := awscdk.Duration_Minutes(jsii.Number(5))
	period1d := awscdk.Duration_Days(jsii.Number(1))
	operationsAlarmTopic := addOperationsAlarmTopic(stack)

	apiRequests := resources.httpAPI.MetricCount(sumMetric("HTTP API requests", period5m, awscloudwatch.Unit_COUNT))
	api5xx := resources.httpAPI.MetricServerError(sumMetric("HTTP API 5xx", period5m, awscloudwatch.Unit_COUNT))
	apiLatencyP95 := resources.httpAPI.MetricLatency(percentileMetric("HTTP API p95 latency", period5m, awscloudwatch.Unit_MILLISECONDS, 95))
	apiIntegrationLatencyP95 := resources.httpAPI.MetricIntegrationLatency(percentileMetric("HTTP API integration p95 latency", period5m, awscloudwatch.Unit_MILLISECONDS, 95))

	cfRequests := resources.distribution.MetricRequests(sumMetric("CloudFront requests", period5m, awscloudwatch.Unit_COUNT))
	cf4xxRate := resources.distribution.Metric4xxErrorRate(avgMetric("CloudFront 4xx rate", period5m, awscloudwatch.Unit_PERCENT))
	cf5xxRate := resources.distribution.Metric5xxErrorRate(avgMetric("CloudFront 5xx rate", period5m, awscloudwatch.Unit_PERCENT))

	ssrInvocations := resources.ssrFunction.MetricInvocations(sumMetric("SSR invocations", period5m, awscloudwatch.Unit_COUNT))
	ssrErrors := resources.ssrFunction.MetricErrors(sumMetric("SSR errors", period5m, awscloudwatch.Unit_COUNT))
	ssrThrottles := resources.ssrFunction.MetricThrottles(sumMetric("SSR throttles", period5m, awscloudwatch.Unit_COUNT))
	ssrDurationP95 := resources.ssrFunction.MetricDuration(percentileMetric("SSR p95 duration", period5m, awscloudwatch.Unit_MILLISECONDS, 95))
	ssrDurationMax := resources.ssrFunction.MetricDuration(maxMetric("SSR max duration", period5m, awscloudwatch.Unit_MILLISECONDS))
	ssrMemoryUsedMax := lambdaMaxMemoryUsedMetric(stack, "SsrLambdaMaxMemoryUsedMetricFilter", resources.ssrLogGroup, "SsrLambdaMaxMemoryUsedMB", "SSR max memory used", period5m)
	adminInvocations := resources.adminFunction.MetricInvocations(sumMetric("Admin invocations", period5m, awscloudwatch.Unit_COUNT))
	adminErrors := resources.adminFunction.MetricErrors(sumMetric("Admin errors", period5m, awscloudwatch.Unit_COUNT))
	adminThrottles := resources.adminFunction.MetricThrottles(sumMetric("Admin throttles", period5m, awscloudwatch.Unit_COUNT))
	adminDurationP95 := resources.adminFunction.MetricDuration(percentileMetric("Admin p95 duration", period5m, awscloudwatch.Unit_MILLISECONDS, 95))
	adminDurationMax := resources.adminFunction.MetricDuration(maxMetric("Admin max duration", period5m, awscloudwatch.Unit_MILLISECONDS))
	adminMemoryUsedMax := lambdaMaxMemoryUsedMetric(stack, "AdminLambdaMaxMemoryUsedMetricFilter", resources.adminLogGroup, "AdminLambdaMaxMemoryUsedMB", "Admin max memory used", period5m)

	catalogDynamoOperations := []dynamoMetricOperation{
		{idSuffix: "get", dimension: "GetItem"},
		{idSuffix: "batchget", dimension: "BatchGetItem"},
		{idSuffix: "query", dimension: "Query"},
		{idSuffix: "scan", dimension: "Scan"},
		{idSuffix: "put", dimension: "PutItem"},
		{idSuffix: "delete", dimension: "DeleteItem"},
		{idSuffix: "update", dimension: "UpdateItem"},
		{idSuffix: "batchwrite", dimension: "BatchWriteItem"},
		{idSuffix: "transactwrite", dimension: "TransactWriteItems"},
	}
	adminLoginThrottleDynamoOperations := []dynamoMetricOperation{
		{idSuffix: "get", dimension: "GetItem"},
		{idSuffix: "update", dimension: "UpdateItem"},
		{idSuffix: "delete", dimension: "DeleteItem"},
	}
	commerceDynamoOperations := []dynamoMetricOperation{
		{idSuffix: "get", dimension: "GetItem"},
		{idSuffix: "query", dimension: "Query"},
		{idSuffix: "put", dimension: "PutItem"},
		{idSuffix: "delete", dimension: "DeleteItem"},
		{idSuffix: "update", dimension: "UpdateItem"},
		{idSuffix: "batchwrite", dimension: "BatchWriteItem"},
		{idSuffix: "transactwrite", dimension: "TransactWriteItems"},
	}
	catalogReadCapacity := resources.catalogTable.MetricConsumedReadCapacityUnits(sumMetric("Catalog read capacity", period5m, awscloudwatch.Unit_COUNT))
	catalogWriteCapacity := resources.catalogTable.MetricConsumedWriteCapacityUnits(sumMetric("Catalog write capacity", period5m, awscloudwatch.Unit_COUNT))
	catalogThrottles := dynamoOperationSumMetric(resources.catalogTable, "ThrottledRequests", "Catalog throttles", period5m, awscloudwatch.Unit_COUNT, "ct", catalogDynamoOperations)
	catalogSystemErrors := dynamoOperationSumMetric(resources.catalogTable, "SystemErrors", "Catalog system errors", period5m, awscloudwatch.Unit_COUNT, "cs", catalogDynamoOperations)
	adminLoginThrottleTableThrottles := dynamoOperationSumMetric(resources.adminLoginAttemptsTable, "ThrottledRequests", "Admin login throttle table throttles", period5m, awscloudwatch.Unit_COUNT, "alt", adminLoginThrottleDynamoOperations)
	commerceReadCapacity := resources.commerceTable.MetricConsumedReadCapacityUnits(sumMetric("Commerce read capacity", period5m, awscloudwatch.Unit_COUNT))
	commerceWriteCapacity := resources.commerceTable.MetricConsumedWriteCapacityUnits(sumMetric("Commerce write capacity", period5m, awscloudwatch.Unit_COUNT))
	commerceThrottles := dynamoOperationSumMetric(resources.commerceTable, "ThrottledRequests", "Commerce throttles", period5m, awscloudwatch.Unit_COUNT, "cmt", commerceDynamoOperations)
	commerceSystemErrors := dynamoOperationSumMetric(resources.commerceTable, "SystemErrors", "Commerce system errors", period5m, awscloudwatch.Unit_COUNT, "cme", commerceDynamoOperations)

	wafAllowed := wafMetric("AllowedRequests", "ALL", "WAF allowed requests", period5m)
	wafBlocked := wafMetric("BlockedRequests", "ALL", "WAF blocked requests", period5m)
	wafLoginBlocked := wafMetric("BlockedRequests", "ThailandGiftshopAdminCloudFrontLoginPost", "WAF login blocks", period5m)
	wafCustomerAuthBlocked := wafMetric("BlockedRequests", "ThailandGiftshopAdminCloudFrontCustomerAuthPost", "WAF customer auth blocks", period5m)

	adminLoginSuccess := appMetric(appobservability.MetricAdminLoginAttempt, "Admin login success", period5m, map[string]*string{"Service": jsii.String("admin"), "Operation": jsii.String("login"), "Outcome": jsii.String("success")})
	adminLoginInvalid := appMetric(appobservability.MetricAdminLoginAttempt, "Admin login invalid", period5m, map[string]*string{"Service": jsii.String("admin"), "Operation": jsii.String("login"), "Outcome": jsii.String("invalid")})
	adminLoginThrottled := appMetric(appobservability.MetricAdminLoginAttempt, "Admin login throttled", period5m, map[string]*string{"Service": jsii.String("admin"), "Operation": jsii.String("login"), "Outcome": jsii.String("throttled")})
	adminOriginRejected := appMetric(appobservability.MetricAdminOriginRejected, "Admin origin rejected", period5m, map[string]*string{"Service": jsii.String("admin"), "Operation": jsii.String("origin"), "Outcome": jsii.String("rejected")})
	catalogWriteSuccess := appMetric(appobservability.MetricCatalogWrite, "Catalog write success", period5m, map[string]*string{"Service": jsii.String("admin"), "Outcome": jsii.String("success")})
	catalogWriteErrors := appMetric(appobservability.MetricCatalogWrite, "Catalog write errors", period5m, map[string]*string{"Service": jsii.String("admin"), "Outcome": jsii.String("error")})
	imageUploadSuccess := appMetric(appobservability.MetricProductImageUpload, "Image upload success", period5m, map[string]*string{"Service": jsii.String("admin"), "Outcome": jsii.String("success")})
	imageUploadErrors := appMetric(appobservability.MetricProductImageUpload, "Image upload errors", period5m, map[string]*string{"Service": jsii.String("admin"), "Outcome": jsii.String("error")})

	// Customer auth, checkout, webhook, order email, and stock metrics query the EMF
	// {Service, Outcome} rollup dimension set. Service values are part of the
	// emitting packages' contract: customer auth and webhook handling record
	// Service=ssr; checkout.Service records checkout and order email outcomes.
	customerAuthSuccess := appMetric(appobservability.MetricCustomerAuth, "Customer auth success", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("success")})
	customerAuthInvalid := appMetric(appobservability.MetricCustomerAuth, "Customer auth invalid", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("invalid")})
	customerAuthThrottled := appMetric(appobservability.MetricCustomerAuth, "Customer auth throttled", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("throttled")})
	customerAuthError := appMetric(appobservability.MetricCustomerAuth, "Customer auth errors", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("error")})
	addressValidationUnavailable := appMetric(appobservability.MetricAddressValidation, "Address validation unavailable", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("unavailable")})
	checkoutPaymentSuccess := appMetric(appobservability.MetricCheckoutPayment, "Checkout payment success", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("success")})
	checkoutPaymentInsufficientStock := appMetric(appobservability.MetricCheckoutPayment, "Checkout insufficient stock", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("insufficient_stock")})
	checkoutPaymentProviderError := appMetric(appobservability.MetricCheckoutPayment, "Checkout provider errors", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("provider_error")})
	checkoutPaymentError := appMetric(appobservability.MetricCheckoutPayment, "Checkout errors", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("error")})
	checkoutPaymentErrors := appOutcomeSumMetric(appobservability.MetricCheckoutPayment, "Checkout payment errors", period5m, metricServiceCheckout, "cp", []string{"amount_mismatch", "provider_error", "error"})
	checkoutRefundIssued := appMetric(appobservability.MetricCheckoutRefund, "Refunds issued", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("issued")})
	checkoutRefundIssuedAuto := appMetric(appobservability.MetricCheckoutRefund, "Refunds auto-issued", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("issued_auto")})
	checkoutRefundSettled := appMetric(appobservability.MetricCheckoutRefund, "Refunds settled", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("settled")})
	checkoutRefundErrors := appOutcomeSumMetric(appobservability.MetricCheckoutRefund, "Checkout refund errors", period5m, metricServiceCheckout, "crf", []string{"failed", "provider_error", "error"})
	orderEmailSent := appMetric(appobservability.MetricOrderEmail, "Order emails sent", period5m, map[string]*string{"Service": jsii.String(metricServiceCheckout), "Outcome": jsii.String("sent")})
	orderEmailErrors := appOutcomeSumMetric(appobservability.MetricOrderEmail, "Order email errors", period5m, metricServiceCheckout, "oe", []string{"reserve_error", "send_error", "mark_failed_error", "mark_sent_error"})
	stripeWebhookProcessed := appMetric(appobservability.MetricStripeWebhook, "Stripe webhook processed", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("processed")})
	stripeWebhookIgnored := appMetric(appobservability.MetricStripeWebhook, "Stripe webhook ignored", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("ignored")})
	stripeWebhookInvalidSignatures := appMetric(appobservability.MetricStripeWebhook, "Stripe webhook invalid signatures", period5m, map[string]*string{"Service": jsii.String(metricServiceSsr), "Outcome": jsii.String("invalid_signature")})
	stripeWebhookErrors := appOutcomeSumMetric(appobservability.MetricStripeWebhook, "Stripe webhook critical errors", period5m, metricServiceSsr, "sw", []string{"amount_mismatch", "refund_mismatch", "paid_after_terminal", "error"})
	// StockAdjust is emitted by internal/catalog's AdjustStock (Service=catalog)
	// regardless of which Lambda triggered the adjustment.
	stockAdjustReserve := appMetric(appobservability.MetricStockAdjust, "Stock reservations", period5m, map[string]*string{"Service": jsii.String(metricServiceCatalog), "Outcome": jsii.String("reserve")})
	stockAdjustRelease := appMetric(appobservability.MetricStockAdjust, "Stock releases", period5m, map[string]*string{"Service": jsii.String(metricServiceCatalog), "Outcome": jsii.String("release")})
	stockAdjustConflict := appMetric(appobservability.MetricStockAdjust, "Stock adjust conflicts", period5m, map[string]*string{"Service": jsii.String(metricServiceCatalog), "Outcome": jsii.String("conflict")})
	stockAdjustRollbackErrors := appMetric(appobservability.MetricStockAdjust, "Stock adjust rollback errors", period5m, map[string]*string{"Service": jsii.String(metricServiceCatalog), "Outcome": jsii.String("rollback_error")})
	// OrderTransition and CommerceOperation carry no Outcome dimension, so
	// they have no {Service, Outcome} rollup; SEARCH expressions graph every
	// emitted dimension combination instead.
	orderTransitions := appSearchMetric(appobservability.MetricOrderTransition, "Order transitions", period5m, []string{"Actor", "From", "Service", "To"}, "Sum")
	commerceOperations := appSearchMetric(appobservability.MetricCommerceOperation, "Commerce store operations", period5m, []string{"Method", "Operation", "Service"}, "Sum")
	commerceOperationLatency := appSearchMetric(appobservability.MetricCommerceOperationMs, "Commerce store latency", period5m, []string{"Method", "Operation", "Service"}, "Average")

	productImagesBucketSize := s3StorageMetric(resources.productImagesBucket, "BucketSizeBytes", "Product image bytes", period1d, awscloudwatch.Unit_BYTES, "StandardStorage")
	productImagesObjectCount := s3StorageMetric(resources.productImagesBucket, "NumberOfObjects", "Product image objects", period1d, awscloudwatch.Unit_COUNT, "AllStorageTypes")

	alarms := []awscloudwatch.Alarm{
		addCriticalAlarm(stack, operationsAlarmTopic, "CloudFront5xxRateAlarm", "ThailandGiftshop-CloudFront-5xxRate-High", cf5xxRate, 5, 2, "CloudFront 5xx error rate is above 5%."),
		addCriticalAlarm(stack, operationsAlarmTopic, "HttpApi5xxAlarm", "ThailandGiftshop-HttpApi-5xx-High", api5xx, 5, 2, "HTTP API is returning elevated 5xx responses."),
		addAlarm(stack, "HttpApiLatencyAlarm", "ThailandGiftshop-HttpApi-LatencyP95-High", apiLatencyP95, 3000, 3, "HTTP API p95 latency is above 3 seconds."),
		addCriticalAlarm(stack, operationsAlarmTopic, "SsrLambdaErrorsAlarm", "ThailandGiftshop-SsrLambda-Errors", ssrErrors, 0, 2, "SSR Lambda has errors."),
		addCriticalAlarm(stack, operationsAlarmTopic, "AdminLambdaErrorsAlarm", "ThailandGiftshop-AdminLambda-Errors", adminErrors, 0, 2, "Admin Lambda has errors."),
		addAlarm(stack, "SsrLambdaMemoryUsedHighAlarm", "ThailandGiftshop-SsrLambda-MemoryUsed-High", ssrMemoryUsedMax, lambdaHighMemoryUsedMB, 1, "SSR Lambda max memory used is above 100 MB after the 128 MB memory cutover."),
		addAlarm(stack, "AdminLambdaMemoryUsedHighAlarm", "ThailandGiftshop-AdminLambda-MemoryUsed-High", adminMemoryUsedMax, lambdaHighMemoryUsedMB, 1, "Admin Lambda max memory used is above 100 MB after the 128 MB memory cutover."),
		addAlarm(stack, "SsrLambdaDurationMaxNearTimeoutAlarm", "ThailandGiftshop-SsrLambda-DurationMax-NearTimeout", ssrDurationMax, lambdaNearTimeoutDurationMs, 1, "SSR Lambda max duration is above 8 seconds and close to the 10 second timeout."),
		addAlarm(stack, "AdminLambdaDurationMaxNearTimeoutAlarm", "ThailandGiftshop-AdminLambda-DurationMax-NearTimeout", adminDurationMax, lambdaNearTimeoutDurationMs, 1, "Admin Lambda max duration is above 8 seconds and close to the 10 second timeout."),
		addAlarm(stack, "SsrLambdaThrottlesAlarm", "ThailandGiftshop-SsrLambda-Throttles", ssrThrottles, 0, 1, "SSR Lambda is throttling."),
		addAlarm(stack, "AdminLambdaThrottlesAlarm", "ThailandGiftshop-AdminLambda-Throttles", adminThrottles, 0, 1, "Admin Lambda is throttling."),
		addAlarm(stack, "CatalogThrottlesAlarm", "ThailandGiftshop-CatalogTable-Throttles", catalogThrottles, 0, 1, "Catalog DynamoDB table is throttling."),
		addAlarm(stack, "CatalogSystemErrorsAlarm", "ThailandGiftshop-CatalogTable-SystemErrors", catalogSystemErrors, 0, 1, "Catalog DynamoDB table has system errors."),
		addAlarm(stack, "CommerceThrottlesAlarm", "ThailandGiftshop-CommerceTable-Throttles", commerceThrottles, 0, 1, "Commerce DynamoDB table is throttling."),
		addAlarm(stack, "CommerceSystemErrorsAlarm", "ThailandGiftshop-CommerceTable-SystemErrors", commerceSystemErrors, 0, 1, "Commerce DynamoDB table has system errors."),
		// The alarm ID and name predate the customer-auth rate rule; both are
		// kept stable so the alarm resource is not replaced on deploy. The
		// Rule=ALL metric covers every edge rule in the web ACL.
		addAlarm(stack, "WafAdminBlocksAlarm", "ThailandGiftshop-WAF-AdminBlocks", wafBlocked, 10, 1, "WAF blocked requests across all edge rules (admin login, admin path, and customer auth) exceeded the normal operating threshold."),
		addAlarm(stack, "AdminOriginRejectedAlarm", "ThailandGiftshop-Admin-OriginRejected", adminOriginRejected, 0, 1, "Admin origin header rejections were observed."),
		addAlarm(stack, "AdminLoginInvalidAlarm", "ThailandGiftshop-Admin-InvalidLogins", adminLoginInvalid, 10, 1, "Admin invalid login attempts exceeded the normal operating threshold."),
		addAlarm(stack, "AdminLoginThrottledAlarm", "ThailandGiftshop-Admin-ThrottledLogins", adminLoginThrottled, 0, 1, "Admin login throttling occurred."),
		addAlarm(stack, "CatalogWriteErrorsAlarm", "ThailandGiftshop-CatalogWrite-Errors", catalogWriteErrors, 0, 1, "Admin catalog write errors occurred."),
		addAlarm(stack, "ImageUploadErrorsAlarm", "ThailandGiftshop-ProductImageUpload-Errors", imageUploadErrors, 0, 1, "Product image upload errors occurred."),
		addAlarm(stack, "AdminLoginThrottleTableThrottlesAlarm", "ThailandGiftshop-AdminLoginThrottleTable-Throttles", adminLoginThrottleTableThrottles, 0, 1, "Admin login throttle DynamoDB table is throttling."),
		addAlarm(stack, "CustomerAuthErrorsAlarm", "ThailandGiftshop-CustomerAuth-Errors", customerAuthError, 0, 1, "Customer account authentication flow errors occurred."),
		addAlarm(stack, "AddressValidationUnavailableAlarm", "ThailandGiftshop-AddressValidation-Unavailable", addressValidationUnavailable, 0, 1, "Address validation was unavailable and failed open."),
		addAlarm(stack, "OrderEmailErrorsAlarm", "ThailandGiftshop-OrderEmail-Errors", orderEmailErrors, 0, 1, "Order lifecycle email send or tracking errors occurred."),
		addAlarm(stack, "StripeWebhookInvalidSignaturesAlarm", "ThailandGiftshop-StripeWebhook-InvalidSignatures", stripeWebhookInvalidSignatures, 5, 1, "Stripe webhook invalid signatures exceeded the normal operating threshold."),
		addCriticalAlarm(stack, operationsAlarmTopic, "StripeWebhookErrorsAlarm", "ThailandGiftshop-StripeWebhook-Errors", stripeWebhookErrors, 0, 1, "Stripe webhook amount or refund mismatches, payments captured for terminal orders with auto-refund issued, or processing errors occurred."),
		addCriticalAlarm(stack, operationsAlarmTopic, "CheckoutPaymentErrorsAlarm", "ThailandGiftshop-CheckoutPayment-Errors", checkoutPaymentErrors, 0, 1, "Checkout payment provider or processing errors occurred."),
		addCriticalAlarm(stack, operationsAlarmTopic, "CheckoutRefundErrorsAlarm", "ThailandGiftshop-CheckoutRefund-Errors", checkoutRefundErrors, 0, 1, "A Stripe refund failed, could not be issued, or hit a processing error; the affected order page in /admin/orders names the refund and failure reason."),
		addCriticalAlarm(stack, operationsAlarmTopic, "StockAdjustRollbackErrorsAlarm", "ThailandGiftshop-StockAdjust-RollbackErrors", stockAdjustRollbackErrors, 0, 1, "A stock reservation rollback failed; product stock may need manual correction."),
	}

	dashboard := awscloudwatch.NewDashboard(stack, jsii.String("OperationsDashboard"), &awscloudwatch.DashboardProps{
		DashboardName:   jsii.String(operationsDashboardName),
		DefaultInterval: awscdk.Duration_Hours(jsii.Number(6)),
	})
	dashboard.AddWidgets(awscloudwatch.NewTextWidget(&awscloudwatch.TextWidgetProps{
		Markdown: jsii.String("# Thailand Gift Shop operations\nProduction service metrics for https://" + siteDomainName + " in us-east-1. Critical alarm actions publish to SNS; watchlist alarms remain dashboard-only."),
		Width:    jsii.Number(24),
		Height:   jsii.Number(2),
	}))
	dashboard.AddWidgets(
		awscloudwatch.NewSingleValueWidget(&awscloudwatch.SingleValueWidgetProps{
			Title:   jsii.String("Current volume and errors"),
			Width:   jsii.Number(8),
			Height:  jsii.Number(4),
			Metrics: cwMetrics(cfRequests, apiRequests, cf5xxRate, api5xx),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Public traffic and edge errors"),
			Width: jsii.Number(16),
			Left:  cwMetrics(cfRequests, apiRequests),
			Right: cwMetrics(cf4xxRate, cf5xxRate),
		}),
	)
	dashboard.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("API and Lambda latency"),
			Width: jsii.Number(12),
			Left:  cwMetrics(apiLatencyP95, apiIntegrationLatencyP95, ssrDurationP95, adminDurationP95, ssrDurationMax, adminDurationMax),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Lambda health"),
			Width: jsii.Number(12),
			Left:  cwMetrics(ssrInvocations, adminInvocations),
			Right: cwMetrics(ssrErrors, adminErrors, ssrThrottles, adminThrottles, ssrMemoryUsedMax, adminMemoryUsedMax),
		}),
	)
	dashboard.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Admin and WAF security"),
			Width: jsii.Number(12),
			Left:  cwMetrics(wafAllowed, wafBlocked, wafLoginBlocked, wafCustomerAuthBlocked),
			Right: cwMetrics(adminLoginSuccess, adminLoginInvalid, adminLoginThrottled, adminOriginRejected),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Catalog and DynamoDB health"),
			Width: jsii.Number(12),
			Left:  cwMetrics(catalogWriteSuccess, catalogWriteErrors, catalogReadCapacity, catalogWriteCapacity),
			Right: cwMetrics(catalogThrottles, catalogSystemErrors, adminLoginThrottleTableThrottles),
		}),
	)
	dashboard.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Customer accounts and checkout"),
			Width: jsii.Number(12),
			Left:  cwMetrics(customerAuthSuccess, customerAuthInvalid, customerAuthThrottled, customerAuthError, addressValidationUnavailable),
			Right: cwMetrics(checkoutPaymentSuccess, checkoutPaymentInsufficientStock, checkoutPaymentProviderError, checkoutPaymentError, checkoutRefundIssued, checkoutRefundIssuedAuto, checkoutRefundSettled, checkoutRefundErrors, orderEmailSent, orderEmailErrors),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Stripe webhooks and stock adjustments"),
			Width: jsii.Number(12),
			Left:  cwMetrics(stripeWebhookProcessed, stripeWebhookIgnored, stripeWebhookInvalidSignatures, stripeWebhookErrors),
			Right: cwMetrics(stockAdjustReserve, stockAdjustRelease, stockAdjustConflict, stockAdjustRollbackErrors),
		}),
	)
	dashboard.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Commerce table and order activity"),
			Width: jsii.Number(12),
			Left:  cwMetrics(commerceReadCapacity, commerceWriteCapacity, commerceThrottles, commerceSystemErrors),
			Right: cwMetrics(orderTransitions),
		}),
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Commerce store operations"),
			Width: jsii.Number(12),
			Left:  cwMetrics(commerceOperations),
			Right: cwMetrics(commerceOperationLatency),
		}),
	)
	dashboard.AddWidgets(
		awscloudwatch.NewGraphWidget(&awscloudwatch.GraphWidgetProps{
			Title: jsii.String("Product image uploads and storage"),
			Width: jsii.Number(12),
			Left:  cwMetrics(imageUploadSuccess, imageUploadErrors),
			Right: cwMetrics(productImagesBucketSize, productImagesObjectCount),
		}),
		awscloudwatch.NewSingleValueWidget(&awscloudwatch.SingleValueWidgetProps{
			Title:   jsii.String("Alarm watchlist"),
			Width:   jsii.Number(12),
			Height:  jsii.Number(4),
			Metrics: cwMetrics(alarmMetrics(alarms...)...),
		}),
	)
}

func addOperationsAlarmTopic(stack awscdk.Stack) awssns.Topic {
	alarmNotificationEmail := awscdk.NewCfnParameter(stack, jsii.String("AlarmNotificationEmail"), &awscdk.CfnParameterProps{
		Description: jsii.String("Email address subscribed to critical operations alarms"),
		Type:        jsii.String("String"),
	})
	topic := awssns.NewTopic(stack, jsii.String("OperationsAlarmTopic"), &awssns.TopicProps{
		TopicName: jsii.String(operationsAlarmTopicName),
	})
	topic.AddSubscription(awssnssubscriptions.NewEmailSubscription(alarmNotificationEmail.ValueAsString(), nil))

	return topic
}

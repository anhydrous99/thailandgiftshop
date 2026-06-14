package main

import (
	appobservability "github.com/anhydrous99/thailandgiftshop/internal/observability"
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudwatch"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudwatchactions"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsdynamodb"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslogs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssns"
	"github.com/aws/jsii-runtime-go"
	"strings"
)

func sumMetric(label string, period awscdk.Duration, unit awscloudwatch.Unit) *awscloudwatch.MetricOptions {
	return metricOptions(label, period, awscloudwatch.Stats_SUM(), unit)
}

func avgMetric(label string, period awscdk.Duration, unit awscloudwatch.Unit) *awscloudwatch.MetricOptions {
	return metricOptions(label, period, awscloudwatch.Stats_AVERAGE(), unit)
}

func maxMetric(label string, period awscdk.Duration, unit awscloudwatch.Unit) *awscloudwatch.MetricOptions {
	return metricOptions(label, period, awscloudwatch.Stats_MAXIMUM(), unit)
}

func percentileMetric(label string, period awscdk.Duration, unit awscloudwatch.Unit, percentile float64) *awscloudwatch.MetricOptions {
	return metricOptions(label, period, awscloudwatch.Stats_P(jsii.Number(percentile)), unit)
}

func metricOptions(label string, period awscdk.Duration, statistic *string, unit awscloudwatch.Unit) *awscloudwatch.MetricOptions {
	return &awscloudwatch.MetricOptions{
		Label:     jsii.String(label),
		Period:    period,
		Statistic: statistic,
		Unit:      unit,
	}
}

func dynamoOperationSumMetric(table awsdynamodb.ITable, metricName string, label string, period awscdk.Duration, unit awscloudwatch.Unit, idPrefix string, operations []dynamoMetricOperation) awscloudwatch.MathExpression {
	usingMetrics := make(map[string]awscloudwatch.IMetric, len(operations))
	expressionTerms := make([]string, 0, len(operations))
	for _, operation := range operations {
		id := idPrefix + operation.idSuffix
		expressionTerms = append(expressionTerms, id)
		usingMetrics[id] = awscloudwatch.NewMetric(&awscloudwatch.MetricProps{
			Namespace:  jsii.String("AWS/DynamoDB"),
			MetricName: jsii.String(metricName),
			DimensionsMap: &map[string]*string{
				"TableName": table.TableName(),
				"Operation": jsii.String(operation.dimension),
			},
			Label:     jsii.String(operation.dimension),
			Period:    period,
			Statistic: awscloudwatch.Stats_SUM(),
			Unit:      unit,
		})
	}

	return awscloudwatch.NewMathExpression(&awscloudwatch.MathExpressionProps{
		Expression:   jsii.String(strings.Join(expressionTerms, "+")),
		Label:        jsii.String(label),
		Period:       period,
		UsingMetrics: &usingMetrics,
	})
}

func appMetric(metricName string, label string, period awscdk.Duration, dimensions map[string]*string) awscloudwatch.Metric {
	return awscloudwatch.NewMetric(&awscloudwatch.MetricProps{
		Namespace:     jsii.String(appobservability.Namespace),
		MetricName:    jsii.String(metricName),
		DimensionsMap: &dimensions,
		Label:         jsii.String(label),
		Period:        period,
		Statistic:     awscloudwatch.Stats_SUM(),
		Unit:          awscloudwatch.Unit_COUNT,
	})
}

// appOutcomeSumMetric sums one app metric across several Outcome dimension
// values for a single Service — the alarmable equivalent of graphing each
// outcome separately (CloudWatch metrics cannot OR dimension values).
func appOutcomeSumMetric(metricName string, label string, period awscdk.Duration, service string, idPrefix string, outcomes []string) awscloudwatch.MathExpression {
	usingMetrics := make(map[string]awscloudwatch.IMetric, len(outcomes))
	expressionTerms := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		id := idPrefix + outcome
		expressionTerms = append(expressionTerms, id)
		usingMetrics[id] = appMetric(metricName, metricName+" "+outcome, period, map[string]*string{
			"Service": jsii.String(service),
			"Outcome": jsii.String(outcome),
		})
	}

	return awscloudwatch.NewMathExpression(&awscloudwatch.MathExpressionProps{
		Expression:   jsii.String(strings.Join(expressionTerms, "+")),
		Label:        jsii.String(label),
		Period:       period,
		UsingMetrics: &usingMetrics,
	})
}

// appSearchMetric graphs every emitted dimension combination of an app metric
// whose full EMF dimension set carries no {Service, Outcome} rollup. SEARCH
// expressions are dashboard-only — they cannot back alarms.
func appSearchMetric(metricName string, label string, period awscdk.Duration, dimensionNames []string, statistic string) awscloudwatch.MathExpression {
	schemaTerms := make([]string, 0, len(dimensionNames)+1)
	schemaTerms = append(schemaTerms, appobservability.Namespace)
	for _, dimensionName := range dimensionNames {
		schemaTerms = append(schemaTerms, dimensionName)
	}

	schema := strings.Join(schemaTerms, ",")
	return awscloudwatch.NewMathExpression(&awscloudwatch.MathExpressionProps{
		Expression: jsii.String(`SEARCH('{` + schema + `} MetricName="` + metricName + `"', '` + statistic + `', 300)`),
		Label:      jsii.String(label),
		Period:     period,
	})
}

func wafMetric(metricName string, rule string, label string, period awscdk.Duration) awscloudwatch.Metric {
	return awscloudwatch.NewMetric(&awscloudwatch.MetricProps{
		Namespace:  jsii.String("AWS/WAFV2"),
		MetricName: jsii.String(metricName),
		DimensionsMap: &map[string]*string{
			"Region": jsii.String("Global"),
			"Rule":   jsii.String(rule),
			"WebACL": jsii.String("ThailandGiftshopAdminCloudFront"),
		},
		Label:     jsii.String(label),
		Period:    period,
		Statistic: awscloudwatch.Stats_SUM(),
		Unit:      awscloudwatch.Unit_COUNT,
		Region:    jsii.String("us-east-1"),
	})
}

func s3StorageMetric(bucket awss3.IBucket, metricName string, label string, period awscdk.Duration, unit awscloudwatch.Unit, storageType string) awscloudwatch.Metric {
	return awscloudwatch.NewMetric(&awscloudwatch.MetricProps{
		Namespace:  jsii.String("AWS/S3"),
		MetricName: jsii.String(metricName),
		DimensionsMap: &map[string]*string{
			"BucketName":  bucket.BucketName(),
			"StorageType": jsii.String(storageType),
		},
		Label:     jsii.String(label),
		Period:    period,
		Statistic: awscloudwatch.Stats_AVERAGE(),
		Unit:      unit,
	})
}

func lambdaMaxMemoryUsedMetric(stack awscdk.Stack, id string, logGroup awslogs.ILogGroup, metricName string, label string, period awscdk.Duration) awscloudwatch.Metric {
	pattern := awslogs.FilterPattern_SpaceDelimited(
		jsii.String("report"),
		jsii.String("requestIdLabel"),
		jsii.String("requestId"),
		jsii.String("durationLabel"),
		jsii.String("duration"),
		jsii.String("durationUnit"),
		jsii.String("billedLabel"),
		jsii.String("billedDurationLabel"),
		jsii.String("billedDuration"),
		jsii.String("billedDurationUnit"),
		jsii.String("memoryLabel"),
		jsii.String("sizeLabel"),
		jsii.String("memorySize"),
		jsii.String("memorySizeUnit"),
		jsii.String("maxLabel"),
		jsii.String("maxMemoryLabel"),
		jsii.String("usedLabel"),
		jsii.String("maxMemoryUsed"),
		jsii.String("maxMemoryUsedUnit"),
		jsii.String("..."),
	).WhereString(jsii.String("report"), jsii.String("="), jsii.String("REPORT")).WhereString(jsii.String("maxMemoryUsedUnit"), jsii.String("="), jsii.String("MB"))

	filter := awslogs.NewMetricFilter(stack, jsii.String(id), &awslogs.MetricFilterProps{
		FilterName:      jsii.String(metricName),
		FilterPattern:   pattern,
		LogGroup:        logGroup,
		MetricName:      jsii.String(metricName),
		MetricNamespace: jsii.String(lambdaReportMetricNamespace),
		MetricValue:     jsii.String("$maxMemoryUsed"),
		Unit:            awscloudwatch.Unit_MEGABYTES,
	})

	return filter.Metric(maxMetric(label, period, awscloudwatch.Unit_MEGABYTES))
}

func addAlarm(stack awscdk.Stack, id string, name string, metric awscloudwatch.IMetric, threshold float64, evaluationPeriods float64, description string) awscloudwatch.Alarm {
	return newAlarm(stack, id, name, metric, threshold, evaluationPeriods, description, false)
}

func addCriticalAlarm(stack awscdk.Stack, operationsAlarmTopic awssns.ITopic, id string, name string, metric awscloudwatch.IMetric, threshold float64, evaluationPeriods float64, description string) awscloudwatch.Alarm {
	alarm := newAlarm(stack, id, name, metric, threshold, evaluationPeriods, description, true)
	alarm.AddAlarmAction(awscloudwatchactions.NewSnsAction(operationsAlarmTopic))

	return alarm
}

func newAlarm(stack awscdk.Stack, id string, name string, metric awscloudwatch.IMetric, threshold float64, evaluationPeriods float64, description string, actionsEnabled bool) awscloudwatch.Alarm {
	return awscloudwatch.NewAlarm(stack, jsii.String(id), &awscloudwatch.AlarmProps{
		ActionsEnabled:     jsii.Bool(actionsEnabled),
		AlarmDescription:   jsii.String(description),
		AlarmName:          jsii.String(name),
		ComparisonOperator: awscloudwatch.ComparisonOperator_GREATER_THAN_THRESHOLD,
		EvaluationPeriods:  jsii.Number(evaluationPeriods),
		Metric:             metric,
		Threshold:          jsii.Number(threshold),
		TreatMissingData:   awscloudwatch.TreatMissingData_NOT_BREACHING,
	})
}

func cwMetrics(metrics ...awscloudwatch.IMetric) *[]awscloudwatch.IMetric {
	return &metrics
}

func alarmMetrics(alarms ...awscloudwatch.Alarm) []awscloudwatch.IMetric {
	metrics := make([]awscloudwatch.IMetric, 0, len(alarms))
	for _, alarm := range alarms {
		metrics = append(metrics, alarm.Metric())
	}
	return metrics
}

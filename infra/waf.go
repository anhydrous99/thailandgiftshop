package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awswafv2"
	"github.com/aws/jsii-runtime-go"
)

func addAdminCloudFrontWebACL(stack awscdk.Stack) awswafv2.CfnWebACL {
	return adminRateLimitWebACL(stack, "AdminCloudFrontWebACL", "thailandgiftshop-admin-cloudfront", "CLOUDFRONT", "ThailandGiftshopAdminCloudFront")
}

func adminRateLimitWebACL(stack awscdk.Stack, id string, name string, scope string, metricName string) awswafv2.CfnWebACL {
	return awswafv2.NewCfnWebACL(stack, jsii.String(id), &awswafv2.CfnWebACLProps{
		Name:  jsii.String(name),
		Scope: jsii.String(scope),
		DefaultAction: &awswafv2.CfnWebACL_DefaultActionProperty{
			Allow: &awswafv2.CfnWebACL_AllowActionProperty{},
		},
		VisibilityConfig: wafVisibility(metricName),
		Rules: []any{
			adminRateLimitRule("AdminLoginPostRateLimit", 0, 100, loginPostStatement(), metricName+"LoginPost"),
			adminRateLimitRule("AdminPathRateLimit", 1, 500, adminPathStatement(), metricName+"Path"),
			adminRateLimitRule("CustomerAuthPostRateLimit", 2, 100, customerAuthPostStatement(), metricName+"CustomerAuthPost"),
			adminRateLimitRule("CheckoutPlaceOrderRateLimit", 3, 100, checkoutPlaceOrderPostStatement(), metricName+"CheckoutPlaceOrderPost"),
		},
	})
}

func adminRateLimitRule(name string, priority int, limit int, statement any, metricName string) *awswafv2.CfnWebACL_RuleProperty {
	return &awswafv2.CfnWebACL_RuleProperty{
		Name:     jsii.String(name),
		Priority: jsii.Number(priority),
		Action: &awswafv2.CfnWebACL_RuleActionProperty{
			Block: &awswafv2.CfnWebACL_BlockActionProperty{
				CustomResponse: &awswafv2.CfnWebACL_CustomResponseProperty{
					ResponseCode: jsii.Number(429),
				},
			},
		},
		Statement: &awswafv2.CfnWebACL_StatementProperty{
			RateBasedStatement: &awswafv2.CfnWebACL_RateBasedStatementProperty{
				AggregateKeyType:    jsii.String("IP"),
				Limit:               jsii.Number(limit),
				EvaluationWindowSec: jsii.Number(300),
				ScopeDownStatement:  statement,
			},
		},
		VisibilityConfig: wafVisibility(metricName),
	}
}

func loginPostStatement() *awswafv2.CfnWebACL_StatementProperty {
	return &awswafv2.CfnWebACL_StatementProperty{
		AndStatement: &awswafv2.CfnWebACL_AndStatementProperty{
			Statements: []any{
				urlDecodedPathStatement("/admin/login", "EXACTLY"),
				byteMatchStatement(&awswafv2.CfnWebACL_FieldToMatchProperty{Method: map[string]any{}}, "POST", "EXACTLY"),
			},
		},
	}
}

// customerAuthPostStatement scopes the customer sign-in/sign-up rate rule to
// POSTs against the two pre-session auth endpoints. The Stripe webhook path is
// deliberately not rate-limited here: Stripe burst-retries, and the origin
// secret plus webhook signature already gate it.
func customerAuthPostStatement() *awswafv2.CfnWebACL_StatementProperty {
	return &awswafv2.CfnWebACL_StatementProperty{
		AndStatement: &awswafv2.CfnWebACL_AndStatementProperty{
			Statements: []any{
				&awswafv2.CfnWebACL_StatementProperty{
					OrStatement: &awswafv2.CfnWebACL_OrStatementProperty{
						Statements: []any{
							urlDecodedPathStatement("/account/sign-in", "EXACTLY"),
							urlDecodedPathStatement("/account/sign-up", "EXACTLY"),
						},
					},
				},
				byteMatchStatement(&awswafv2.CfnWebACL_FieldToMatchProperty{Method: map[string]any{}}, "POST", "EXACTLY"),
			},
		},
	}
}

// checkoutPlaceOrderPostStatement rate-limits the one unauthenticated POST
// that reserves stock. Guest checkout removes the account gate, so the edge
// throttle takes its place; 100 place-order POSTs per 5 minutes per IP is far
// beyond any human checkout cadence.
func checkoutPlaceOrderPostStatement() *awswafv2.CfnWebACL_StatementProperty {
	return &awswafv2.CfnWebACL_StatementProperty{
		AndStatement: &awswafv2.CfnWebACL_AndStatementProperty{
			Statements: []any{
				urlDecodedPathStatement("/checkout/place-order", "EXACTLY"),
				byteMatchStatement(&awswafv2.CfnWebACL_FieldToMatchProperty{Method: map[string]any{}}, "POST", "EXACTLY"),
			},
		},
	}
}

func adminPathStatement() *awswafv2.CfnWebACL_StatementProperty {
	return &awswafv2.CfnWebACL_StatementProperty{
		OrStatement: &awswafv2.CfnWebACL_OrStatementProperty{
			Statements: []any{
				byteMatchStatement(&awswafv2.CfnWebACL_FieldToMatchProperty{UriPath: map[string]any{}}, "/admin", "EXACTLY"),
				byteMatchStatement(&awswafv2.CfnWebACL_FieldToMatchProperty{UriPath: map[string]any{}}, "/admin/", "STARTS_WITH"),
			},
		},
	}
}

func byteMatchStatement(fieldToMatch any, search string, positionalConstraint string) *awswafv2.CfnWebACL_StatementProperty {
	return transformedByteMatchStatement(fieldToMatch, search, positionalConstraint, []string{"NONE"})
}

// urlDecodedPathStatement matches a URI path with URL_DECODE applied before
// the literal comparison, so percent-encoded spellings of a rate-limited path
// (for example /account/sign%2Din) still count toward the edge rate rules.
// WAF applies TextTransformations in priority order and inspects the final
// value, so decoding leaves canonical paths byte-identical to a NONE match.
func urlDecodedPathStatement(search string, positionalConstraint string) *awswafv2.CfnWebACL_StatementProperty {
	return transformedByteMatchStatement(&awswafv2.CfnWebACL_FieldToMatchProperty{UriPath: map[string]any{}}, search, positionalConstraint, []string{"URL_DECODE", "NONE"})
}

func transformedByteMatchStatement(fieldToMatch any, search string, positionalConstraint string, transformations []string) *awswafv2.CfnWebACL_StatementProperty {
	textTransformations := make([]any, 0, len(transformations))
	for priority, transformation := range transformations {
		textTransformations = append(textTransformations, &awswafv2.CfnWebACL_TextTransformationProperty{
			Priority: jsii.Number(priority),
			Type:     jsii.String(transformation),
		})
	}

	return &awswafv2.CfnWebACL_StatementProperty{
		ByteMatchStatement: &awswafv2.CfnWebACL_ByteMatchStatementProperty{
			FieldToMatch:         fieldToMatch,
			SearchString:         jsii.String(search),
			PositionalConstraint: jsii.String(positionalConstraint),
			TextTransformations:  textTransformations,
		},
	}
}

func wafVisibility(metricName string) *awswafv2.CfnWebACL_VisibilityConfigProperty {
	return &awswafv2.CfnWebACL_VisibilityConfigProperty{
		CloudWatchMetricsEnabled: jsii.Bool(true),
		MetricName:               jsii.String(metricName),
		SampledRequestsEnabled:   jsii.Bool(false),
	}
}

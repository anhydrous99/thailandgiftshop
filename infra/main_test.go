package main

import (
	"testing"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/assertions"
	"github.com/aws/jsii-runtime-go"
)

func TestStackSynthesizes(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)

	if stack == nil {
		t.Fatal("expected stack to be created")
	}
}

func TestStackIncludesObservabilityResources(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.ResourceCountIs(jsii.String("AWS::Logs::LogGroup"), jsii.Number(3))
	template.AllResourcesProperties(jsii.String("AWS::Logs::LogGroup"), map[string]interface{}{
		"RetentionInDays": 90,
	})

	template.HasResourceProperties(jsii.String("AWS::Logs::LogGroup"), map[string]interface{}{
		"LogGroupName":    "/aws/lambda/thailandgiftshop-ssr",
		"RetentionInDays": 90,
	})

	template.HasResourceProperties(jsii.String("AWS::Logs::LogGroup"), map[string]interface{}{
		"LogGroupName":    "/aws/apigateway/thailandgiftshop-ssr",
		"RetentionInDays": 90,
	})

	template.HasResourceProperties(jsii.String("AWS::Logs::LogGroup"), map[string]interface{}{
		"LogGroupName":    "/aws/lambda/thailandgiftshop-static-assets-deployment",
		"RetentionInDays": 90,
	})

	template.HasResourceProperties(jsii.String("AWS::Lambda::Function"), map[string]interface{}{
		"FunctionName": "thailandgiftshop-ssr",
		"LoggingConfig": map[string]interface{}{
			"LogGroup": map[string]interface{}{
				"Ref": "SsrLambdaLogGroup5806F2F2",
			},
		},
		"TracingConfig": map[string]interface{}{
			"Mode": "Active",
		},
	})

	template.HasResourceProperties(jsii.String("AWS::Lambda::Function"), map[string]interface{}{
		"Handler": "index.handler",
		"LoggingConfig": map[string]interface{}{
			"LogGroup": assertions.Match_ObjectLike(&map[string]interface{}{
				"Ref": assertions.Match_StringLikeRegexp(jsii.String("StaticAssetsDeploymentLogGroup")),
			}),
		},
	})

	template.HasResourceProperties(jsii.String("AWS::IAM::Policy"), map[string]interface{}{
		"PolicyDocument": map[string]interface{}{
			"Statement": assertions.Match_ArrayWith(&[]interface{}{
				assertions.Match_ObjectLike(&map[string]interface{}{
					"Action": assertions.Match_ArrayWith(&[]interface{}{
						"xray:PutTraceSegments",
						"xray:PutTelemetryRecords",
					}),
					"Effect":   "Allow",
					"Resource": "*",
				}),
			}),
		},
	})

	template.HasResourceProperties(jsii.String("AWS::ApiGatewayV2::Stage"), map[string]interface{}{
		"AccessLogSettings": map[string]interface{}{
			"DestinationArn": assertions.Match_AnyValue(),
			"Format":         assertions.Match_StringLikeRegexp(jsii.String("requestId")),
		},
		"DefaultRouteSettings": map[string]interface{}{
			"DetailedMetricsEnabled": true,
		},
	})
}

func TestStackIncludesCatalogResources(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.HasResource(jsii.String("AWS::DynamoDB::Table"), map[string]interface{}{
		"DeletionPolicy":      "Retain",
		"UpdateReplacePolicy": "Retain",
		"Properties": assertions.Match_ObjectLike(&map[string]interface{}{
			"AttributeDefinitions": assertions.Match_ArrayWith(&[]interface{}{
				map[string]interface{}{
					"AttributeName": catalogPartitionKeyName,
					"AttributeType": "S",
				},
				map[string]interface{}{
					"AttributeName": catalogSortKeyName,
					"AttributeType": "S",
				},
				map[string]interface{}{
					"AttributeName": catalogSlugIndexPKName,
					"AttributeType": "S",
				},
				map[string]interface{}{
					"AttributeName": catalogSlugIndexSKName,
					"AttributeType": "S",
				},
				map[string]interface{}{
					"AttributeName": catalogPublicIndexPKName,
					"AttributeType": "S",
				},
				map[string]interface{}{
					"AttributeName": catalogPublicIndexSKName,
					"AttributeType": "S",
				},
			}),
			"BillingMode": "PAY_PER_REQUEST",
			"GlobalSecondaryIndexes": assertions.Match_ArrayWith(&[]interface{}{
				assertions.Match_ObjectLike(&map[string]interface{}{
					"IndexName": "slug-index",
					"KeySchema": assertions.Match_ArrayWith(&[]interface{}{
						map[string]interface{}{
							"AttributeName": catalogSlugIndexPKName,
							"KeyType":       "HASH",
						},
						map[string]interface{}{
							"AttributeName": catalogSlugIndexSKName,
							"KeyType":       "RANGE",
						},
					}),
					"Projection": map[string]interface{}{
						"ProjectionType": "ALL",
					},
				}),
				assertions.Match_ObjectLike(&map[string]interface{}{
					"IndexName": "public-index",
					"KeySchema": assertions.Match_ArrayWith(&[]interface{}{
						map[string]interface{}{
							"AttributeName": catalogPublicIndexPKName,
							"KeyType":       "HASH",
						},
						map[string]interface{}{
							"AttributeName": catalogPublicIndexSKName,
							"KeyType":       "RANGE",
						},
					}),
					"Projection": map[string]interface{}{
						"ProjectionType": "ALL",
					},
				}),
			}),
			"KeySchema": assertions.Match_ArrayWith(&[]interface{}{
				map[string]interface{}{
					"AttributeName": catalogPartitionKeyName,
					"KeyType":       "HASH",
				},
				map[string]interface{}{
					"AttributeName": catalogSortKeyName,
					"KeyType":       "RANGE",
				},
			}),
			"PointInTimeRecoverySpecification": map[string]interface{}{
				"PointInTimeRecoveryEnabled": true,
			},
			"SSESpecification": map[string]interface{}{
				"SSEEnabled": true,
			},
			"TableName": catalogTableName,
		}),
	})

	template.HasResourceProperties(jsii.String("AWS::Lambda::Function"), map[string]interface{}{
		"FunctionName": "thailandgiftshop-ssr",
		"Environment": map[string]interface{}{
			"Variables": map[string]interface{}{
				"CATALOG_TABLE_NAME":        assertions.Match_AnyValue(),
				"CATALOG_SLUG_INDEX_NAME":   "slug-index",
				"CATALOG_PUBLIC_INDEX_NAME": "public-index",
			},
		},
	})

	template.HasResourceProperties(jsii.String("AWS::IAM::Policy"), map[string]interface{}{
		"PolicyDocument": map[string]interface{}{
			"Statement": assertions.Match_ArrayWith(&[]interface{}{
				assertions.Match_ObjectLike(&map[string]interface{}{
					"Action": assertions.Match_ArrayWith(&[]interface{}{
						"dynamodb:BatchGetItem",
						"dynamodb:Query",
						"dynamodb:GetItem",
					}),
					"Effect":   "Allow",
					"Resource": assertions.Match_AnyValue(),
				}),
			}),
		},
	})

	template.HasOutput(jsii.String("CatalogTableName"), map[string]interface{}{
		"Description": "DynamoDB table name for products and categories",
		"Value":       assertions.Match_AnyValue(),
	})
	template.HasOutput(jsii.String("CatalogTableArn"), map[string]interface{}{
		"Description": "DynamoDB table ARN for products and categories",
		"Value":       assertions.Match_AnyValue(),
	})
}

func TestStackIncludesStaticAssetsDistribution(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopStack(app, "TestStack", nil)
	template := assertions.Template_FromStack(stack, nil)

	template.HasParameter(jsii.String("HostedZoneId"), map[string]interface{}{
		"Type":        "String",
		"Description": "Route 53 public hosted zone ID for thailandgiftshop.com",
	})

	template.HasResourceProperties(jsii.String("AWS::S3::Bucket"), map[string]interface{}{
		"BucketEncryption": map[string]interface{}{
			"ServerSideEncryptionConfiguration": assertions.Match_ArrayWith(&[]interface{}{
				assertions.Match_ObjectLike(&map[string]interface{}{
					"ServerSideEncryptionByDefault": map[string]interface{}{
						"SSEAlgorithm": "AES256",
					},
				}),
			}),
		},
		"OwnershipControls": map[string]interface{}{
			"Rules": assertions.Match_ArrayWith(&[]interface{}{
				map[string]interface{}{
					"ObjectOwnership": "BucketOwnerEnforced",
				},
			}),
		},
		"PublicAccessBlockConfiguration": map[string]interface{}{
			"BlockPublicAcls":       true,
			"BlockPublicPolicy":     true,
			"IgnorePublicAcls":      true,
			"RestrictPublicBuckets": true,
		},
	})

	template.HasResourceProperties(jsii.String("AWS::S3::BucketPolicy"), map[string]interface{}{
		"PolicyDocument": map[string]interface{}{
			"Statement": assertions.Match_ArrayWith(&[]interface{}{
				assertions.Match_ObjectLike(&map[string]interface{}{
					"Action": "s3:*",
					"Condition": map[string]interface{}{
						"Bool": map[string]interface{}{
							"aws:SecureTransport": "false",
						},
					},
					"Effect": "Deny",
				}),
				assertions.Match_ObjectLike(&map[string]interface{}{
					"Action": "s3:GetObject",
					"Condition": map[string]interface{}{
						"StringEquals": map[string]interface{}{
							"AWS:SourceArn": assertions.Match_AnyValue(),
						},
					},
					"Effect": "Allow",
					"Principal": map[string]interface{}{
						"Service": "cloudfront.amazonaws.com",
					},
				}),
			}),
		},
	})

	template.HasResourceProperties(jsii.String("AWS::CloudFront::OriginAccessControl"), map[string]interface{}{
		"OriginAccessControlConfig": map[string]interface{}{
			"OriginAccessControlOriginType": "s3",
			"SigningBehavior":               "always",
			"SigningProtocol":               "sigv4",
		},
	})

	template.HasResourceProperties(jsii.String("AWS::CertificateManager::Certificate"), map[string]interface{}{
		"DomainName": siteDomainName,
		"SubjectAlternativeNames": assertions.Match_ArrayWith(&[]interface{}{
			wwwDomainName,
		}),
		"ValidationMethod": "DNS",
	})

	template.HasResourceProperties(jsii.String("AWS::CloudFront::ResponseHeadersPolicy"), map[string]interface{}{
		"ResponseHeadersPolicyConfig": map[string]interface{}{
			"SecurityHeadersConfig": map[string]interface{}{
				"ContentSecurityPolicy": map[string]interface{}{
					"ContentSecurityPolicy": assertions.Match_StringLikeRegexp(jsii.String("default-src 'self'")),
					"Override":              true,
				},
				"ContentTypeOptions": map[string]interface{}{
					"Override": true,
				},
				"FrameOptions": map[string]interface{}{
					"FrameOption": "DENY",
					"Override":    true,
				},
				"ReferrerPolicy": map[string]interface{}{
					"Override":       true,
					"ReferrerPolicy": assertions.Match_StringLikeRegexp(jsii.String("strict-origin")),
				},
				"StrictTransportSecurity": map[string]interface{}{
					"AccessControlMaxAgeSec": 31536000,
					"Override":               true,
					"Preload":                false,
				},
			},
		},
	})

	template.HasResourceProperties(jsii.String("AWS::CloudFront::Distribution"), map[string]interface{}{
		"DistributionConfig": map[string]interface{}{
			"Aliases": assertions.Match_ArrayWith(&[]interface{}{
				siteDomainName,
				wwwDomainName,
			}),
			"CacheBehaviors": assertions.Match_ArrayWith(&[]interface{}{
				assertions.Match_ObjectLike(&map[string]interface{}{
					"AllowedMethods": assertions.Match_ArrayEquals(&[]interface{}{
						"GET",
						"HEAD",
					}),
					"CachePolicyId":           "658327ea-f89d-4fab-a63d-7e88639e58f6",
					"Compress":                true,
					"PathPattern":             "static/*",
					"ResponseHeadersPolicyId": assertions.Match_AnyValue(),
					"ViewerProtocolPolicy":    "redirect-to-https",
				}),
			}),
			"DefaultCacheBehavior": map[string]interface{}{
				"AllowedMethods": assertions.Match_ArrayEquals(&[]interface{}{
					"GET",
					"HEAD",
				}),
				"CachePolicyId":           "4135ea2d-6df8-44a3-9df3-4b5a84be39ad",
				"Compress":                true,
				"OriginRequestPolicyId":   "b689b0a8-53d0-40ab-baf2-68738e2966ac",
				"ResponseHeadersPolicyId": assertions.Match_AnyValue(),
				"ViewerProtocolPolicy":    "redirect-to-https",
			},
			"Origins": assertions.Match_ArrayWith(&[]interface{}{
				assertions.Match_ObjectLike(&map[string]interface{}{
					"CustomOriginConfig": map[string]interface{}{
						"OriginProtocolPolicy": "https-only",
					},
					"DomainName": map[string]interface{}{
						"Fn::Join": assertions.Match_ArrayWith(&[]interface{}{
							"",
							assertions.Match_ArrayWith(&[]interface{}{
								".execute-api.",
							}),
						}),
					},
				}),
				assertions.Match_ObjectLike(&map[string]interface{}{
					"OriginAccessControlId": assertions.Match_AnyValue(),
					"S3OriginConfig": map[string]interface{}{
						"OriginAccessIdentity": "",
					},
				}),
			}),
			"ViewerCertificate": assertions.Match_ObjectLike(&map[string]interface{}{
				"SslSupportMethod": "sni-only",
			}),
		},
	})

	for _, record := range []struct {
		name       string
		recordType string
	}{
		{name: siteDomainName + ".", recordType: "A"},
		{name: siteDomainName + ".", recordType: "AAAA"},
		{name: wwwDomainName + ".", recordType: "A"},
		{name: wwwDomainName + ".", recordType: "AAAA"},
	} {
		template.HasResourceProperties(jsii.String("AWS::Route53::RecordSet"), map[string]interface{}{
			"AliasTarget": assertions.Match_ObjectLike(&map[string]interface{}{
				"DNSName": assertions.Match_AnyValue(),
			}),
			"Name": record.name,
			"Type": record.recordType,
		})
	}

	template.HasResourceProperties(jsii.String("Custom::CDKBucketDeployment"), map[string]interface{}{
		"DestinationBucketKeyPrefix": "static",
		"DistributionPaths": assertions.Match_ArrayWith(&[]interface{}{
			"/static/*",
		}),
		"Prune": true,
		"SystemMetadata": map[string]interface{}{
			"cache-control": "max-age=3600",
		},
	})

	template.HasOutput(jsii.String("SiteDistributionDomainName"), map[string]interface{}{
		"Description": "CloudFront domain name for thailandgiftshop.com",
		"Value":       assertions.Match_AnyValue(),
	})
	template.HasOutput(jsii.String("SiteUrl"), map[string]interface{}{
		"Description": "CloudFront URL for thailandgiftshop.com",
		"Value":       "https://" + siteDomainName,
	})
}

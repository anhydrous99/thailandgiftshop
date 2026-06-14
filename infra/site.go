package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscertificatemanager"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfront"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfrontorigins"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslogs"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsroute53"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsroute53targets"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3deployment"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssecretsmanager"
	"github.com/aws/jsii-runtime-go"
)

func ssrOriginRequestPolicy(stack awscdk.Stack) awscloudfront.OriginRequestPolicy {
	return awscloudfront.NewOriginRequestPolicy(stack, jsii.String("SsrOriginRequestPolicy"), &awscloudfront.OriginRequestPolicyProps{
		OriginRequestPolicyName: jsii.String(ssrOriginRequestPolicyName),
		Comment:                 jsii.String("Headers, cookies, and query strings for thailandgiftshop.com SSR origin"),
		CookieBehavior:          awscloudfront.OriginRequestCookieBehavior_All(),
		QueryStringBehavior:     awscloudfront.OriginRequestQueryStringBehavior_All(),
		HeaderBehavior: awscloudfront.OriginRequestHeaderBehavior_AllowList(
			jsii.String("CloudFront-Viewer-Address"),
			jsii.String("CloudFront-Forwarded-Proto"),
			jsii.String("Content-Type"),
			jsii.String("HX-Request"),
			jsii.String("X-CSRF-Token"),
			jsii.String("Stripe-Signature"),
			// Origin and Referer back the cart-mutation CSRF host check; they
			// are forwarded to the origin only (not in the cache key), so they
			// never fragment the shared public edge cache.
			jsii.String("Origin"),
			jsii.String("Referer"),
		),
	})
}

// ssrCachePolicy caches only responses that opt in with an explicit cacheable
// Cache-Control header (DefaultTtl is zero). Cart, checkout, account, and admin
// responses send no-store and stay uncached; origin forwarding still carries
// cookies and query strings for those private routes, but public cache keys stay
// shared so tracking parameters and cart/session cookies do not fragment them.
func ssrCachePolicy(stack awscdk.Stack) awscloudfront.CachePolicy {
	return awscloudfront.NewCachePolicy(stack, jsii.String("SsrCachePolicy"), &awscloudfront.CachePolicyProps{
		CachePolicyName:            jsii.String(ssrCachePolicyName),
		Comment:                    jsii.String("Cache opt-in SSR responses for thailandgiftshop.com with shared public cache keys"),
		CookieBehavior:             awscloudfront.CacheCookieBehavior_None(),
		QueryStringBehavior:        awscloudfront.CacheQueryStringBehavior_None(),
		HeaderBehavior:             awscloudfront.CacheHeaderBehavior_None(),
		MinTtl:                     awscdk.Duration_Seconds(jsii.Number(0)),
		DefaultTtl:                 awscdk.Duration_Seconds(jsii.Number(0)),
		MaxTtl:                     awscdk.Duration_Days(jsii.Number(1)),
		EnableAcceptEncodingGzip:   jsii.Bool(true),
		EnableAcceptEncodingBrotli: jsii.Bool(true),
	})
}

func addCanonicalHostRedirectFunction(stack awscdk.Stack) awscloudfront.Function {
	return awscloudfront.NewFunction(stack, jsii.String("CanonicalHostRedirectFunction"), &awscloudfront.FunctionProps{
		Code: awscloudfront.FunctionCode_FromInline(jsii.String(`function handler(event) {
    var request = event.request;
    var host = request.headers.host.value.toLowerCase();

    if (host !== "www.thailandgiftshop.com") {
        return request;
    }

    var location = "https://thailandgiftshop.com" + request.uri;
    var querystring = request.querystring;
    var queryParts = [];

    for (var name in querystring) {
        if (!Object.prototype.hasOwnProperty.call(querystring, name)) {
            continue;
        }

        var parameter = querystring[name];
        if (parameter.multiValue) {
            for (var index = 0; index < parameter.multiValue.length; index++) {
                queryParts.push(name + "=" + parameter.multiValue[index].value);
            }
            continue;
        }

        queryParts.push(name + "=" + parameter.value);
    }

    if (queryParts.length > 0) {
        location += "?" + queryParts.join("&");
    }

    return {
        statusCode: 308,
        statusDescription: "Permanent Redirect",
        headers: {
            location: {
                value: location
            }
        }
    };
}`)),
		Comment:      jsii.String("Redirect www.thailandgiftshop.com requests to the apex host"),
		FunctionName: jsii.String("thailandgiftshop-www-to-apex"),
		Runtime:      awscloudfront.FunctionRuntime_JS_2_0(),
	})
}

func canonicalHostRedirectAssociations(function awscloudfront.Function) *[]*awscloudfront.FunctionAssociation {
	return &[]*awscloudfront.FunctionAssociation{
		{
			EventType: awscloudfront.FunctionEventType_VIEWER_REQUEST,
			Function:  function,
		},
	}
}

func addSite(stack awscdk.Stack, httpAPI awsapigatewayv2.HttpApi, productImagesBucket awss3.IBucket, adminOriginHeaderSecret awssecretsmanager.ISecret, hostedZone awsroute53.IPublicHostedZone) siteResources {
	certificate := awscertificatemanager.NewCertificate(stack, jsii.String("SiteCertificate"), &awscertificatemanager.CertificateProps{
		DomainName: jsii.String(siteDomainName),
		SubjectAlternativeNames: &[]*string{
			jsii.String(wwwDomainName),
		},
		Validation: awscertificatemanager.CertificateValidation_FromDns(hostedZone),
	})
	securityHeadersPolicy := siteSecurityHeaders(stack, productImagesBucket)
	adminWebACL := addAdminCloudFrontWebACL(stack)
	originRequestPolicy := ssrOriginRequestPolicy(stack)
	cachePolicy := ssrCachePolicy(stack)
	canonicalHostRedirectFunction := addCanonicalHostRedirectFunction(stack)
	canonicalHostRedirectFunctionAssociations := canonicalHostRedirectAssociations(canonicalHostRedirectFunction)

	staticBucket := awss3.NewBucket(stack, jsii.String("StaticAssetsBucket"), &awss3.BucketProps{
		BlockPublicAccess: awss3.BlockPublicAccess_BLOCK_ALL(),
		Encryption:        awss3.BucketEncryption_S3_MANAGED,
		EnforceSSL:        jsii.Bool(true),
		ObjectOwnership:   awss3.ObjectOwnership_BUCKET_OWNER_ENFORCED,
		RemovalPolicy:     awscdk.RemovalPolicy_RETAIN,
	})
	staticAssetsDeploymentLogGroup := awslogs.NewLogGroup(stack, jsii.String("StaticAssetsDeploymentLogGroup"), &awslogs.LogGroupProps{
		LogGroupName: jsii.String("/aws/lambda/thailandgiftshop-static-assets-deployment"),
		Retention:    awslogs.RetentionDays_THREE_MONTHS,
	})

	distribution := awscloudfront.NewDistribution(stack, jsii.String("SiteDistribution"), &awscloudfront.DistributionProps{
		Certificate: certificate,
		Comment:     jsii.String("CloudFront distribution for thailandgiftshop.com SSR and static assets"),
		DomainNames: &[]*string{
			jsii.String(siteDomainName),
			jsii.String(wwwDomainName),
		},
		PriceClass: awscloudfront.PriceClass_PRICE_CLASS_200,
		DefaultBehavior: &awscloudfront.BehaviorOptions{
			AllowedMethods:        awscloudfront.AllowedMethods_ALLOW_ALL(),
			CachePolicy:           cachePolicy,
			Compress:              jsii.Bool(true),
			Origin:                ssrOrigin(httpAPI, adminOriginHeaderSecret),
			OriginRequestPolicy:   originRequestPolicy,
			ResponseHeadersPolicy: securityHeadersPolicy,
			ViewerProtocolPolicy:  awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
			FunctionAssociations:  canonicalHostRedirectFunctionAssociations,
		},
		AdditionalBehaviors: &map[string]*awscloudfront.BehaviorOptions{
			"static/*": {
				AllowedMethods:        awscloudfront.AllowedMethods_ALLOW_GET_HEAD(),
				CachePolicy:           awscloudfront.CachePolicy_CACHING_OPTIMIZED(),
				Compress:              jsii.Bool(true),
				Origin:                awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(staticBucket, nil),
				ResponseHeadersPolicy: securityHeadersPolicy,
				ViewerProtocolPolicy:  awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
				FunctionAssociations:  canonicalHostRedirectFunctionAssociations,
			},
			"images/*": {
				AllowedMethods:        awscloudfront.AllowedMethods_ALLOW_GET_HEAD(),
				CachePolicy:           awscloudfront.CachePolicy_CACHING_OPTIMIZED(),
				Compress:              jsii.Bool(true),
				Origin:                awscloudfrontorigins.S3BucketOrigin_WithOriginAccessControl(productImagesBucket, nil),
				ResponseHeadersPolicy: securityHeadersPolicy,
				ViewerProtocolPolicy:  awscloudfront.ViewerProtocolPolicy_REDIRECT_TO_HTTPS,
				FunctionAssociations:  canonicalHostRedirectFunctionAssociations,
			},
		},
		WebAclId: adminWebACL.AttrArn(),
	})

	addSiteAliasRecords(stack, hostedZone, distribution)

	staticAssetsDeployment := awss3deployment.NewBucketDeployment(stack, jsii.String("StaticAssetsDeployment"), &awss3deployment.BucketDeploymentProps{
		CacheControl: &[]awss3deployment.CacheControl{
			awss3deployment.CacheControl_MaxAge(awscdk.Duration_Hours(jsii.Number(1))),
		},
		DestinationBucket:    staticBucket,
		DestinationKeyPrefix: jsii.String(staticAssetsKeyPrefix),
		Distribution:         distribution,
		DistributionPaths: &[]*string{
			jsii.String("/static/*"),
		},
		LogGroup: staticAssetsDeploymentLogGroup,
		Prune:    jsii.Bool(false),
		Sources: &[]awss3deployment.ISource{
			awss3deployment.Source_Asset(jsii.String("../web/static"), nil),
		},
	})
	immutableStaticAssetsDeployment := awss3deployment.NewBucketDeployment(stack, jsii.String("ImmutableStaticAssetsDeployment"), &awss3deployment.BucketDeploymentProps{
		CacheControl: &[]awss3deployment.CacheControl{
			awss3deployment.CacheControl_MaxAge(awscdk.Duration_Days(jsii.Number(365))),
			awss3deployment.CacheControl_Immutable(),
		},
		DestinationBucket:    staticBucket,
		DestinationKeyPrefix: jsii.String(staticAssetsKeyPrefix + "/assets"),
		LogGroup:             staticAssetsDeploymentLogGroup,
		Prune:                jsii.Bool(false),
		Sources: &[]awss3deployment.ISource{
			awss3deployment.Source_Asset(jsii.String("../web/static/assets"), nil),
		},
	})
	immutableStaticAssetsDeployment.Node().AddDependency(staticAssetsDeployment)
	immutableStaticScriptsDeployment := awss3deployment.NewBucketDeployment(stack, jsii.String("ImmutableStaticScriptsDeployment"), &awss3deployment.BucketDeploymentProps{
		CacheControl: &[]awss3deployment.CacheControl{
			awss3deployment.CacheControl_MaxAge(awscdk.Duration_Days(jsii.Number(365))),
			awss3deployment.CacheControl_Immutable(),
		},
		DestinationBucket:    staticBucket,
		DestinationKeyPrefix: jsii.String(staticAssetsKeyPrefix + "/js"),
		LogGroup:             staticAssetsDeploymentLogGroup,
		Prune:                jsii.Bool(false),
		Sources: &[]awss3deployment.ISource{
			awss3deployment.Source_Asset(jsii.String("../web/static/js"), nil),
		},
	})
	immutableStaticScriptsDeployment.Node().AddDependency(staticAssetsDeployment)
	immutableStaticFontsDeployment := awss3deployment.NewBucketDeployment(stack, jsii.String("ImmutableStaticFontsDeployment"), &awss3deployment.BucketDeploymentProps{
		CacheControl: &[]awss3deployment.CacheControl{
			awss3deployment.CacheControl_MaxAge(awscdk.Duration_Days(jsii.Number(365))),
			awss3deployment.CacheControl_Immutable(),
		},
		DestinationBucket:    staticBucket,
		DestinationKeyPrefix: jsii.String(staticAssetsKeyPrefix + "/fonts"),
		LogGroup:             staticAssetsDeploymentLogGroup,
		Prune:                jsii.Bool(false),
		Sources: &[]awss3deployment.ISource{
			awss3deployment.Source_Asset(jsii.String("../web/static/fonts"), nil),
		},
	})
	immutableStaticFontsDeployment.Node().AddDependency(staticAssetsDeployment)
	awss3deployment.NewBucketDeployment(stack, jsii.String("ProductImagesDeployment"), &awss3deployment.BucketDeploymentProps{
		CacheControl: &[]awss3deployment.CacheControl{
			awss3deployment.CacheControl_MaxAge(awscdk.Duration_Hours(jsii.Number(1))),
		},
		DestinationBucket:    productImagesBucket,
		DestinationKeyPrefix: jsii.String(productImagesKeyPrefix),
		Distribution:         distribution,
		DistributionPaths: &[]*string{
			jsii.String("/images/*"),
		},
		LogGroup: staticAssetsDeploymentLogGroup,
		Prune:    jsii.Bool(false),
		Sources: &[]awss3deployment.ISource{
			awss3deployment.Source_Asset(jsii.String("../web/product-images"), nil),
		},
	})

	awscdk.NewCfnOutput(stack, jsii.String("SiteDistributionDomainName"), &awscdk.CfnOutputProps{
		Description: jsii.String("CloudFront domain name for thailandgiftshop.com"),
		Value:       distribution.DistributionDomainName(),
	})

	awscdk.NewCfnOutput(stack, jsii.String("SiteUrl"), &awscdk.CfnOutputProps{
		Description: jsii.String("CloudFront URL for thailandgiftshop.com"),
		Value:       jsii.String("https://" + siteDomainName),
	})
	awscdk.NewCfnOutput(stack, jsii.String("ProductImagesBucketName"), &awscdk.CfnOutputProps{
		Description: jsii.String("S3 bucket name for product image objects"),
		Value:       productImagesBucket.BucketName(),
	})
	awscdk.NewCfnOutput(stack, jsii.String("ProductImagesBaseUrl"), &awscdk.CfnOutputProps{
		Description: jsii.String("CloudFront base URL path for product images"),
		Value:       jsii.String("https://" + siteDomainName + "/" + productImagesKeyPrefix + "/"),
	})

	return siteResources{distribution: distribution, adminWebACL: adminWebACL}
}

func siteHostedZone(stack awscdk.Stack) awsroute53.IPublicHostedZone {
	hostedZoneID := awscdk.NewCfnParameter(stack, jsii.String("HostedZoneId"), &awscdk.CfnParameterProps{
		Description: jsii.String("Route 53 public hosted zone ID for thailandgiftshop.com"),
		Type:        jsii.String("String"),
	})

	return awsroute53.PublicHostedZone_FromPublicHostedZoneAttributes(stack, jsii.String("SiteHostedZone"), &awsroute53.PublicHostedZoneAttributes{
		HostedZoneId: hostedZoneID.ValueAsString(),
		ZoneName:     jsii.String(siteDomainName),
	})
}

func siteSecurityHeaders(stack awscdk.Stack, productImagesBucket awss3.IBucket) awscloudfront.ResponseHeadersPolicy {
	productImagesBucketDomain := awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), productImagesBucket.BucketDomainName()})
	productImagesBucketRegionalDomain := awscdk.Fn_Join(jsii.String(""), &[]*string{jsii.String("https://"), productImagesBucket.BucketRegionalDomainName()})
	contentSecurityPolicy := awscdk.Fn_Join(jsii.String(""), &[]*string{
		jsii.String("default-src 'self'; base-uri 'self'; connect-src 'self' "),
		productImagesBucketDomain,
		jsii.String(" "),
		productImagesBucketRegionalDomain,
		jsii.String("; frame-ancestors 'none'; form-action 'self' "),
		productImagesBucketDomain,
		jsii.String(" "),
		productImagesBucketRegionalDomain,
		jsii.String(" https://checkout.stripe.com; img-src 'self' data:; object-src 'none'; script-src 'self'; style-src 'self' " + skipLinkStyleCSPHash + "; manifest-src 'self'"),
	})

	return awscloudfront.NewResponseHeadersPolicy(stack, jsii.String("SiteSecurityHeadersPolicy"), &awscloudfront.ResponseHeadersPolicyProps{
		Comment: jsii.String("Security headers for thailandgiftshop.com"),
		CustomHeadersBehavior: &awscloudfront.ResponseCustomHeadersBehavior{
			CustomHeaders: &[]*awscloudfront.ResponseCustomHeader{
				{
					Header:   jsii.String("Cross-Origin-Opener-Policy"),
					Value:    jsii.String("same-origin"),
					Override: jsii.Bool(true),
				},
				{
					Header:   jsii.String("Permissions-Policy"),
					Value:    jsii.String("accelerometer=(), autoplay=(), camera=(), clipboard-read=(), clipboard-write=(), display-capture=(), document-domain=(), encrypted-media=(), fullscreen=(), geolocation=(), gyroscope=(), magnetometer=(), microphone=(), midi=(), payment=(), picture-in-picture=(), publickey-credentials-get=(), screen-wake-lock=(), usb=(), web-share=(), xr-spatial-tracking=()"),
					Override: jsii.Bool(true),
				},
			},
		},
		SecurityHeadersBehavior: &awscloudfront.ResponseSecurityHeadersBehavior{
			ContentSecurityPolicy: &awscloudfront.ResponseHeadersContentSecurityPolicy{
				ContentSecurityPolicy: contentSecurityPolicy,
				Override:              jsii.Bool(true),
			},
			ContentTypeOptions: &awscloudfront.ResponseHeadersContentTypeOptions{
				Override: jsii.Bool(true),
			},
			FrameOptions: &awscloudfront.ResponseHeadersFrameOptions{
				FrameOption: awscloudfront.HeadersFrameOption_DENY,
				Override:    jsii.Bool(true),
			},
			ReferrerPolicy: &awscloudfront.ResponseHeadersReferrerPolicy{
				Override:       jsii.Bool(true),
				ReferrerPolicy: awscloudfront.HeadersReferrerPolicy_STRICT_ORIGIN_WHEN_CROSS_ORIGIN,
			},
			StrictTransportSecurity: &awscloudfront.ResponseHeadersStrictTransportSecurity{
				AccessControlMaxAge: awscdk.Duration_Days(jsii.Number(365)),
				IncludeSubdomains:   jsii.Bool(true),
				Override:            jsii.Bool(true),
				// Preload is a near-irreversible commitment; enable after a soak.
				Preload: jsii.Bool(false),
			},
		},
	})
}

func addSiteAliasRecords(stack awscdk.Stack, hostedZone awsroute53.IHostedZone, distribution awscloudfront.IDistribution) {
	awsroute53.NewARecord(stack, jsii.String("SiteARecord"), &awsroute53.ARecordProps{
		Target: awsroute53.RecordTarget_FromAlias(awsroute53targets.NewCloudFrontTarget(distribution)),
		Zone:   hostedZone,
	})
	awsroute53.NewAaaaRecord(stack, jsii.String("SiteAAAARecord"), &awsroute53.AaaaRecordProps{
		Target: awsroute53.RecordTarget_FromAlias(awsroute53targets.NewCloudFrontTarget(distribution)),
		Zone:   hostedZone,
	})
	awsroute53.NewARecord(stack, jsii.String("WwwARecord"), &awsroute53.ARecordProps{
		RecordName: jsii.String("www"),
		Target:     awsroute53.RecordTarget_FromAlias(awsroute53targets.NewCloudFrontTarget(distribution)),
		Zone:       hostedZone,
	})
	awsroute53.NewAaaaRecord(stack, jsii.String("WwwAAAARecord"), &awsroute53.AaaaRecordProps{
		RecordName: jsii.String("www"),
		Target:     awsroute53.RecordTarget_FromAlias(awsroute53targets.NewCloudFrontTarget(distribution)),
		Zone:       hostedZone,
	})
}

func ssrOrigin(httpAPI awsapigatewayv2.HttpApi, adminOriginHeaderSecret awssecretsmanager.ISecret) awscloudfront.IOrigin {
	return awscloudfrontorigins.NewHttpOrigin(apiGatewayDomainName(httpAPI), &awscloudfrontorigins.HttpOriginProps{
		CustomHeaders: &map[string]*string{
			"X-TGS-Origin-Secret": adminOriginHeaderSecretReference(adminOriginHeaderSecret),
		},
		ProtocolPolicy: awscloudfront.OriginProtocolPolicy_HTTPS_ONLY,
	})
}

func apiGatewayDomainName(httpAPI awsapigatewayv2.HttpApi) *string {
	return awscdk.Fn_Join(jsii.String(""), &[]*string{
		httpAPI.HttpApiId(),
		jsii.String(".execute-api."),
		awscdk.Aws_REGION(),
		jsii.String("."),
		awscdk.Aws_URL_SUFFIX(),
	})
}

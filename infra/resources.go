package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsapigatewayv2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awscloudfront"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsdynamodb"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/aws-cdk-go/awscdk/v2/awss3"
	"github.com/aws/aws-cdk-go/awscdk/v2/awswafv2"
)

type ThailandGiftshopStackProps struct {
	awscdk.StackProps
}

type ssrResources struct {
	httpAPI  awsapigatewayv2.HttpApi
	function awslambda.Function
	admin    adminResources
}

type adminResources struct {
	routes   []awsapigatewayv2.HttpRoute
	function awslambda.Function
}

type siteResources struct {
	distribution awscloudfront.Distribution
	adminWebACL  awswafv2.CfnWebACL
}

type observabilityResources struct {
	catalogTable            awsdynamodb.Table
	commerceTable           awsdynamodb.Table
	productImagesBucket     awss3.IBucket
	adminLoginAttemptsTable awsdynamodb.Table
	ssrFunction             awslambda.IFunction
	adminFunction           awslambda.IFunction
	httpAPI                 awsapigatewayv2.IHttpApi
	distribution            awscloudfront.Distribution
	adminWebACL             awswafv2.CfnWebACL
}

type dynamoMetricOperation struct {
	idSuffix  string
	dimension string
}

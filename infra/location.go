package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2/awsiam"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/jsii-runtime-go"
)

// grantAddressValidation lets a Lambda call the Amazon Location Service
// standalone Places Geocode API. The geo-places API is resource-less, so the
// policy targets "*" (there is no Place Index ARN to scope to).
func grantAddressValidation(lambdaFunction awslambda.Function) {
	lambdaFunction.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: &[]*string{
			jsii.String("geo-places:Geocode"),
		},
		Resources: &[]*string{
			jsii.String("*"),
		},
	}))
}

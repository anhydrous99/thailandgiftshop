package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsiam"
	"github.com/aws/aws-cdk-go/awscdk/v2/awslambda"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsroute53"
	"github.com/aws/aws-cdk-go/awscdk/v2/awsses"
	"github.com/aws/jsii-runtime-go"
)

func addEmailIdentity(stack awscdk.Stack, hostedZone awsroute53.IPublicHostedZone) awsses.EmailIdentity {
	return awsses.NewEmailIdentity(stack, jsii.String("SiteEmailIdentity"), &awsses.EmailIdentityProps{
		Identity: awsses.Identity_PublicHostedZone(hostedZone),
		DkimIdentity: awsses.DkimIdentity_EasyDkim(
			awsses.EasyDkimSigningKeyLength_RSA_2048_BIT,
		),
	})
}

func grantEmailSend(lambdaFunction awslambda.Function, identity awsses.IEmailIdentity) {
	lambdaFunction.AddToRolePolicy(awsiam.NewPolicyStatement(&awsiam.PolicyStatementProps{
		Actions: &[]*string{
			jsii.String("ses:SendEmail"),
		},
		Resources: &[]*string{
			identity.EmailIdentityArn(),
		},
	}))
}

package main

import (
	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/aws-cdk-go/awscdk/v2/awssecretsmanager"
	"github.com/aws/jsii-runtime-go"
)

func adminCredentialsSecret(stack awscdk.Stack) awssecretsmanager.ISecret {
	return awssecretsmanager.Secret_FromSecretNameV2(stack, jsii.String("AdminCredentialsSecret"), jsii.String(adminCredentialsSecretName))
}

func addAdminOriginHeaderSecret(stack awscdk.Stack) awssecretsmanager.Secret {
	return awssecretsmanager.NewSecret(stack, jsii.String("AdminOriginHeaderSecret"), &awssecretsmanager.SecretProps{
		Description: jsii.String("Shared origin header secret for thailandgiftshop.com admin requests through CloudFront"),
		GenerateSecretString: &awssecretsmanager.SecretStringGenerator{
			ExcludePunctuation: jsii.Bool(true),
			PasswordLength:     jsii.Number(64),
		},
	})
}

func adminPreviousOriginHeaderSecretValue(stack awscdk.Stack) *string {
	previousOriginSecret := awscdk.NewCfnParameter(stack, jsii.String(adminPreviousOriginHeaderSecretParameterName), &awscdk.CfnParameterProps{
		Default:     jsii.String(""),
		Description: jsii.String("Previous X-TGS-Origin-Secret value accepted during CloudFront origin header rotations. Also update AdminOriginHeaderVersion when changing this value."),
		NoEcho:      jsii.Bool(true),
		Type:        jsii.String("String"),
	})
	return previousOriginSecret.ValueAsString()
}

func adminOriginHeaderVersionValue(stack awscdk.Stack) *string {
	originHeaderVersion := awscdk.NewCfnParameter(stack, jsii.String(adminOriginHeaderVersionParameterName), &awscdk.CfnParameterProps{
		Default:     jsii.String("1"),
		Description: jsii.String("Non-secret marker to force publishing a new SSR Lambda version when origin header runtime configuration changes"),
		Type:        jsii.String("String"),
	})
	return originHeaderVersion.ValueAsString()
}

func adminOriginHeaderSecretReference(secret awssecretsmanager.ISecret) *string {
	return awscdk.NewCfnDynamicReference(
		awscdk.CfnDynamicReferenceService_SECRETS_MANAGER,
		secret.CfnDynamicReferenceKey(nil),
	).ToString()
}

func addCartCookieSecret(stack awscdk.Stack) awssecretsmanager.Secret {
	return awssecretsmanager.NewSecret(stack, jsii.String("CartCookieSecret"), &awssecretsmanager.SecretProps{
		Description: jsii.String("Signing secret for thailandgiftshop.com cart cookies"),
		GenerateSecretString: &awssecretsmanager.SecretStringGenerator{
			ExcludePunctuation: jsii.Bool(true),
			PasswordLength:     jsii.Number(64),
		},
	})
}

func cartCookieSecretReference(secret awssecretsmanager.ISecret) *string {
	return awscdk.NewCfnDynamicReference(
		awscdk.CfnDynamicReferenceService_SECRETS_MANAGER,
		secret.CfnDynamicReferenceKey(nil),
	).ToString()
}

func addCustomerSessionSecret(stack awscdk.Stack) awssecretsmanager.Secret {
	return awssecretsmanager.NewSecret(stack, jsii.String("CustomerSessionSecret"), &awssecretsmanager.SecretProps{
		Description: jsii.String("Signing secret for thailandgiftshop.com customer session cookies"),
		GenerateSecretString: &awssecretsmanager.SecretStringGenerator{
			ExcludePunctuation: jsii.Bool(true),
			PasswordLength:     jsii.Number(64),
		},
	})
}

func customerSessionSecretReference(secret awssecretsmanager.ISecret) *string {
	return awscdk.NewCfnDynamicReference(
		awscdk.CfnDynamicReferenceService_SECRETS_MANAGER,
		secret.CfnDynamicReferenceKey(nil),
	).ToString()
}

// stripeCredentialsSecret references the manually pre-created Stripe secret
// (JSON {"secret_key","webhook_signing_secret"}) in us-east-1 — the
// adminCredentialsSecret precedent. It must exist before the first deploy.
func stripeCredentialsSecret(stack awscdk.Stack) awssecretsmanager.ISecret {
	return awssecretsmanager.Secret_FromSecretNameV2(stack, jsii.String("StripeCredentialsSecret"), jsii.String(stripeCredentialsSecretName))
}

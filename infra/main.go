package main

import (
	"os"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/constructs-go/constructs/v10"
	"github.com/aws/jsii-runtime-go"
)

type ThailandGiftshopInfraStackProps struct {
	awscdk.StackProps
}

func NewThailandGiftshopInfraStack(scope constructs.Construct, id string, props *ThailandGiftshopInfraStackProps) awscdk.Stack {
	var stackProps awscdk.StackProps
	if props != nil {
		stackProps = props.StackProps
	}

	stack := awscdk.NewStack(scope, &id, &stackProps)

	awscdk.Tags_Of(stack).Add(jsii.String("Project"), jsii.String("thailandgiftshop"), nil)
	awscdk.Tags_Of(stack).Add(jsii.String("ManagedBy"), jsii.String("aws-cdk"), nil)

	return stack
}

func main() {
	defer jsii.Close()

	app := awscdk.NewApp(nil)

	NewThailandGiftshopInfraStack(app, "ThailandGiftshopInfraStack", &ThailandGiftshopInfraStackProps{
		StackProps: awscdk.StackProps{
			Description: jsii.String("Infrastructure for thailandgiftshop.com"),
			Env:         env(),
		},
	})

	app.Synth(nil)
}

func env() *awscdk.Environment {
	account := os.Getenv("CDK_DEFAULT_ACCOUNT")
	region := os.Getenv("CDK_DEFAULT_REGION")

	if account == "" && region == "" {
		return nil
	}

	return &awscdk.Environment{
		Account: stringOrNil(account),
		Region:  stringOrNil(region),
	}
}

func stringOrNil(value string) *string {
	if value == "" {
		return nil
	}

	return jsii.String(value)
}

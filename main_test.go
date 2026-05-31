package main

import (
	"testing"

	"github.com/aws/aws-cdk-go/awscdk/v2"
	"github.com/aws/jsii-runtime-go"
)

func TestStackSynthesizes(t *testing.T) {
	defer jsii.Close()

	app := awscdk.NewApp(nil)
	stack := NewThailandGiftshopInfraStack(app, "TestStack", nil)

	if stack == nil {
		t.Fatal("expected stack to be created")
	}
}

package main

import (
	"github.com/anhydrous99/thailandgiftshop/internal/ssr"
	"github.com/aws/aws-lambda-go/lambda"
)

func main() {
	lambda.Start(ssr.Handle)
}

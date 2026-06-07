package main

import (
	"context"
	"log"

	"github.com/anhydrous99/thailandgiftshop/internal/admin"
	"github.com/aws/aws-lambda-go/lambda"
)

func main() {
	handler, err := admin.NewHandlerFromEnvironment(context.Background())
	if err != nil {
		log.Fatalf("load admin handler: %v", err)
	}

	lambda.Start(handler.Handle)
}

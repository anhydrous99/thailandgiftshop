package main

import (
	"context"
	"log"

	"github.com/anhydrous99/thailandgiftshop/internal/ssr"
	"github.com/aws/aws-lambda-go/lambda"
)

func main() {
	handler, err := ssr.NewHandlerFromEnvironment(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	lambda.Start(handler.Handle)
}

# thailandgiftshop-infra

AWS CDK v2 infrastructure for `thailandgiftshop.com`, written in Go.

This repository currently contains a deployable CDK skeleton and GitHub Actions CI/CD. Website resources will be added in a later stack change.

## Prerequisites

- Go 1.25 or newer
- Node.js 22 or newer
- AWS credentials for local bootstrap/deploy

## Local Setup

Install dependencies:

```sh
npm install
go mod download
```

Synthesize the CloudFormation template:

```sh
npx cdk synth
```

Run tests:

```sh
go test .
```

Check Go formatting:

```sh
gofmt -l main.go main_test.go
```

## Bootstrap

Before deploying to an AWS account/region for the first time, bootstrap CDK:

```sh
npx cdk bootstrap aws://ACCOUNT_ID/us-east-1
```

Replace `ACCOUNT_ID` and the region as needed.

## Deploy

Deploy locally:

```sh
npx cdk deploy --all
```

## GitHub Actions

CI runs on pull requests and pushes to `main`:

- `go test .`
- `gofmt` check
- `npx cdk synth`

Deployment runs after the `CI` workflow completes successfully on `main`, and can also be run manually with workflow dispatch.

Configure these GitHub settings before the first deployment:

- Repository or environment secret: `AWS_ROLE_TO_ASSUME`
- Repository or environment variable: `AWS_REGION` set to the target AWS region, for example `us-east-1`

The deployment workflow uses GitHub OIDC through `aws-actions/configure-aws-credentials`, so long-lived AWS access keys are not required.

The AWS role used by `AWS_ROLE_TO_ASSUME` must trust GitHub's OIDC provider and should be scoped to this repository's `production` GitHub environment, for example `repo:OWNER/thailandgiftshop-infra:environment:production`.

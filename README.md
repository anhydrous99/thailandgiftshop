# thailandgiftshop

Monorepo for `thailandgiftshop.com`.

The AWS CDK v2 infrastructure package lives in `infra/` and is written in Go. Go Lambda entrypoints, shared Go packages, HTMX templates, Tailwind CSS, and static asset code should live outside `infra/` so application code can evolve separately from deployment code.

The deployed CDK stack ID is `ThailandGiftshopStack`.

Static assets belong in `web/static/`. CDK deploys that folder to a private S3 bucket behind the site CloudFront distribution and serves it under `/static/`, so `web/static/logo.svg` is available as `/static/logo.svg`.

## Prerequisites

- Go 1.25 or newer
- Node.js 24 LTS
- AWS credentials for local bootstrap/deploy

## Local Setup

Install dependencies:

```sh
go mod download
npm --prefix infra ci
```

Synthesize the CloudFormation template:

```sh
npm --prefix infra run synth
```

Run tests:

```sh
go test $(git ls-files '*.go' | xargs -n1 dirname | sort -u | sed 's#^#./#')
```

Check Go formatting:

```sh
gofmt -l $(git ls-files '*.go')
```

## Observability

The CDK stack provisions the SSR Lambda with its own CloudWatch Logs group at `/aws/lambda/thailandgiftshop-ssr` and 3-month retention. The HTTP API stage writes access logs to `/aws/apigateway/thailandgiftshop-ssr`, also with 3-month retention. Static asset deployment helper logs are written to `/aws/lambda/thailandgiftshop-static-assets-deployment` with the same 3-month retention policy.

Lambda emits AWS-managed CloudWatch metrics automatically, and the HTTP API default stage has detailed metrics enabled. Lambda X-Ray tracing is active and the Lambda role includes the X-Ray write permissions required to publish trace data.

## Bootstrap

Before deploying to an AWS account/region for the first time, bootstrap CDK:

```sh
cd infra
npx cdk bootstrap aws://ACCOUNT_ID/us-east-1
```

Replace `ACCOUNT_ID` and the region as needed.

## Deploy

Deploy locally:

```sh
cd infra
npx cdk deploy --all
```

The stack outputs `SiteUrl` and `SiteDistributionDomainName` for the CloudFront entrypoint. The existing `SsrHttpApiUrl` output remains available for direct API Gateway access while CloudFront handles normal site traffic.

## GitHub Actions

CI runs on pull requests and pushes to `main`:

- Go tests for tracked Go package directories
- `gofmt` check
- `npx cdk synth`

The CI workflow is path-aware and runs for Go, infrastructure, static asset, or workflow changes.

Deployment runs after the `CI` workflow completes successfully on `main`, and can also be run manually with workflow dispatch.

Configure these GitHub settings before the first deployment:

- Repository or environment secret: `AWS_ROLE_TO_ASSUME`
- Repository or environment variable: `AWS_REGION` set to the target AWS region, for example `us-east-1`

The deployment workflow uses GitHub OIDC through `aws-actions/configure-aws-credentials`, so long-lived AWS access keys are not required.

The AWS role used by `AWS_ROLE_TO_ASSUME` must trust GitHub's OIDC provider and should be scoped to this repository's `production` GitHub environment, for example `repo:anhydrous99/thailandgiftshop:environment:production`.

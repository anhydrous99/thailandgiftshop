# thailandgiftshop

Monorepo for `thailandgiftshop.com`.

The AWS CDK v2 infrastructure package lives in `infra/` and is written in Go. Go Lambda entrypoints, shared Go packages, HTMX templates, Tailwind CSS, and static asset code should live outside `infra/` so application code can evolve separately from deployment code.

The deployed CDK stack ID is `ThailandGiftshopStack`. Production is deployed in `us-east-1` so the CloudFront certificate and site infrastructure are managed by the same stack.

Static assets belong in `web/static/`. CDK deploys that folder to a private S3 bucket behind the site CloudFront distribution and serves it under `/static/`, so `web/static/logo.svg` is available as `/static/logo.svg`.

## Prerequisites

- Go 1.25 or newer
- Node.js 24 LTS for frontend asset builds, browser tests, and CDK commands
- AWS credentials for local bootstrap/deploy

## Local Run

Serve the checked-in local site with the Go devserver:

```sh
go run ./cmd/devserver
```

Open `http://127.0.0.1:8080`. The leading `./` is intentional because `cmd/devserver` is a local Go package path.

The devserver uses `web/static/` by default and does not install Node dependencies or rebuild frontend assets. Override the bind address or static asset directory when needed:

```sh
PORT=8081 go run ./cmd/devserver
HOST=0.0.0.0 go run ./cmd/devserver
STATIC_DIR=/path/to/static go run ./cmd/devserver
```

## Local Setup

Install dependencies for frontend builds, browser tests, CDK commands, and explicit Go dependency prefetching:

```sh
go mod download
npm --prefix infra ci
npm --prefix web ci
```

Generate Go code after editing templ files:

```sh
go tool templ generate
```

Build frontend assets after editing Tailwind, HTMX, or template files:

```sh
npm --prefix web run build
```

Synthesize the CloudFormation template after frontend assets have been built:

```sh
npm --prefix infra run synth
```

For first-time local browser test runs, install Playwright Chromium:

```sh
npm --prefix web run test:install
```

Run tests:

```sh
go test $(sh scripts/go-packages.sh)
npm --prefix web test
```

Check Go formatting:

```sh
gofmt -l $(git ls-files '*.go')
```

## Observability

The CDK stack provisions the SSR Lambda with its own CloudWatch Logs group at `/aws/lambda/thailandgiftshop-ssr` and 3-month retention. The HTTP API stage writes access logs to `/aws/apigateway/thailandgiftshop-ssr`, also with 3-month retention. Static asset deployment helper logs are written to `/aws/lambda/thailandgiftshop-static-assets-deployment` with the same 3-month retention policy.

Lambda emits AWS-managed CloudWatch metrics automatically, and the HTTP API default stage has detailed metrics enabled. Lambda X-Ray tracing is active and the Lambda role includes the X-Ray write permissions required to publish trace data.

## Bootstrap

Before deploying to an AWS account/region for the first time, bootstrap CDK in `us-east-1`:

```sh
cd infra
npx cdk bootstrap aws://ACCOUNT_ID/us-east-1
```

Replace `ACCOUNT_ID` as needed. Production deploys intentionally fail outside `us-east-1`.

## Deploy

Local deployers should build frontend assets before synthesizing or deploying so `web/static/` contains the generated CSS and vendored HTMX files used by CDK:

```sh
npm --prefix web run build
npm --prefix infra run synth
```

Deploy locally:

```sh
cd infra
AWS_REGION=us-east-1 npx cdk deploy ThailandGiftshopStack --parameters HostedZoneId=ROUTE53_HOSTED_ZONE_ID
```

Replace `ROUTE53_HOSTED_ZONE_ID` with the public Route 53 hosted zone ID for `thailandgiftshop.com`.

The stack outputs `SiteUrl` as `https://thailandgiftshop.com` and also outputs `SiteDistributionDomainName` for the underlying CloudFront distribution. The existing `SsrHttpApiUrl` output remains available for direct API Gateway access while CloudFront handles normal site traffic.

After the `us-east-1` site has been deployed and verified, manually destroy the old regional stack if it is still present:

```sh
cd infra
AWS_REGION=us-east-2 npx cdk destroy ThailandGiftshopStack
```

Retained resources, such as retained buckets or log groups, may remain after stack destruction and should be reviewed before manual deletion.

## GitHub Actions

CI runs on pull requests and pushes to `main`:

- Go tests for tracked Go package directories
- Frontend asset build and browser tests
- `gofmt` check
- `npx cdk synth`

The CI workflow is path-aware and runs for Go, infrastructure, static asset, or workflow changes.

Deployment runs after the `CI` workflow completes successfully on `main`, and can also be run manually with workflow dispatch.

Configure these GitHub settings before the first deployment:

- Repository or environment secret: `AWS_ROLE_TO_ASSUME`
- Repository or environment variable: `AWS_REGION` set to `us-east-1`
- Repository or environment variable: `ROUTE53_HOSTED_ZONE_ID` set to the public Route 53 hosted zone ID for `thailandgiftshop.com`

The deployment workflow uses GitHub OIDC through `aws-actions/configure-aws-credentials`, so long-lived AWS access keys are not required.

The AWS role used by `AWS_ROLE_TO_ASSUME` must trust GitHub's OIDC provider and should be scoped to this repository's `production` GitHub environment, for example `repo:anhydrous99/thailandgiftshop:environment:production`.

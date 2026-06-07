# thailandgiftshop

Monorepo for `thailandgiftshop.com`.

The AWS CDK v2 infrastructure package lives in `infra/` and is written in Go. Go Lambda entrypoints, shared Go packages, HTMX templates, Tailwind CSS, and static asset code should live outside `infra/` so application code can evolve separately from deployment code.

The deployed CDK stack ID is `ThailandGiftshopStack`. Production is deployed in `us-east-1` so the CloudFront certificate and site infrastructure are managed by the same stack.

Static assets belong in `web/static/`. CDK deploys that folder to a private S3 bucket behind the site CloudFront distribution and serves it under `/static/`, so `web/static/logo.svg` is available as `/static/logo.svg`.

Product image seed assets belong in `web/product-images/`. CDK deploys that folder to a separate private retained S3 bucket behind the same CloudFront distribution and serves it under `/images/`, so `web/product-images/products/mango-sticky-rice-kit.jpg` is available as `/images/products/mango-sticky-rice-kit.jpg`. This bucket is intentionally separate from the pruned static asset deployment so future uploaded product images are not removed by site deploys.

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

The devserver uses `web/static/` and `web/product-images/` by default and does not install Node dependencies or rebuild frontend assets. Override the bind address, static asset directory, or product image directory when needed:

```sh
PORT=8081 go run ./cmd/devserver
HOST=0.0.0.0 go run ./cmd/devserver
STATIC_DIR=/path/to/static go run ./cmd/devserver
IMAGE_DIR=/path/to/product-images go run ./cmd/devserver
```

If `CATALOG_TABLE_NAME` is unset, the devserver uses an empty catalog store. To read the deployed DynamoDB catalog locally, run the devserver with an AWS profile that already has access to the catalog table:

```sh
AWS_PROFILE=default AWS_REGION=us-east-1 CATALOG_TABLE_NAME=thailandgiftshop-catalog go run ./cmd/devserver
```

`CATALOG_SLUG_INDEX_NAME`, `CATALOG_PUBLIC_INDEX_NAME`, and `CATALOG_RECENT_INDEX_NAME` default to `slug-index`, `public-index`, and `recent-index`, which match the CDK-provisioned table. The Home page displays latest active products from the recent index, and product cards use `image_url` from DynamoDB, falling back to `PRODUCT_IMAGE_PLACEHOLDER_URL` or `/images/placeholder-product.jpg` when the field is empty.

Set `CART_COOKIE_SECRET` to a stable local-only value when exercising cart cookies locally. Production receives this value from a CDK-managed generated Secrets Manager secret, so no production cookie signing secret is stored in this repository.

Use deterministic demo data for local browser tests or admin/public E2E work by opting into the in-memory catalog store:

```sh
CATALOG_DEMO_STORE=1 CART_COOKIE_SECRET=<cart-cookie-secret> go run ./cmd/devserver
```

When `CATALOG_DEMO_STORE=1` is set, the devserver skips DynamoDB and serves the checked-in demo catalog from memory. The demo catalog includes an active variant product, `handwoven-indigo-scarf`, with sizes `S=1`, `M=2`, and `XL=0` for stable admin and public E2E coverage. The default devserver path still uses environment-backed catalog loading, so local Go tests and Playwright tests can run without a live AWS account.

For local admin work, set placeholder-only admin environment variables before starting the admin Lambda or any local wrapper that loads admin credentials from the environment:

```sh
export ADMIN_PASSWORD_HASH=<bcrypt-hash>
export ADMIN_SESSION_SECRET=<session-secret>
```

Create the bcrypt hash and session secret outside the repository, then keep the real values in your shell, local secret manager, or CI secret store. Do not commit a plaintext admin password, generated bcrypt hash, or generated session secret. Local and dev code reads `ADMIN_PASSWORD_HASH` and `ADMIN_SESSION_SECRET` as a fallback; production reads `ADMIN_CREDENTIALS_SECRET_JSON` from the Lambda environment.

## Seed Catalog

Seed the deployed DynamoDB catalog with demo categories, products, and category-product rows:

```sh
AWS_REGION=us-east-1 go run ./cmd/seedcatalog
```

The command defaults to the CDK table name, `thailandgiftshop-catalog`. Override it when needed:

```sh
AWS_REGION=us-east-1 go run ./cmd/seedcatalog -table CUSTOM_TABLE_NAME
```

Preview the row count without writing:

```sh
AWS_REGION=us-east-1 go run ./cmd/seedcatalog -dry-run
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
AWS_REGION=us-east-1 CDK_DEFAULT_REGION=us-east-1 npm --prefix infra run synth
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

The CDK stack provisions the SSR Lambda with its own CloudWatch Logs group at `/aws/lambda/thailandgiftshop-ssr` and 3-month retention. The HTTP API stage writes access logs to `/aws/apigateway/thailandgiftshop-ssr`, also with 3-month retention. Static and product image deployment helper logs are written to `/aws/lambda/thailandgiftshop-static-assets-deployment` with the same 3-month retention policy.

Lambda emits AWS-managed CloudWatch metrics automatically, and the HTTP API default stage has detailed metrics enabled. Lambda X-Ray tracing is active and the Lambda role includes the X-Ray write permissions required to publish trace data.

## Bootstrap

Before deploying to an AWS account/region for the first time, bootstrap CDK in `us-east-1`:

```sh
cd infra
npx cdk bootstrap aws://ACCOUNT_ID/us-east-1
```

Replace `ACCOUNT_ID` as needed. Production deploys intentionally fail outside `us-east-1`.

Bootstrap the production admin credentials as a manually managed Secrets Manager JSON secret named `thailandgiftshop/admin/credentials` before deploying the admin Lambda wiring:

```sh
aws secretsmanager create-secret \
  --region us-east-1 \
  --name thailandgiftshop/admin/credentials \
  --secret-string '{"password_hash": "<bcrypt-hash>", "session_secret": "<session-secret>"}'
```

Rotate the same named secret by writing a new JSON value with the same fields:

```sh
aws secretsmanager put-secret-value \
  --region us-east-1 \
  --secret-id thailandgiftshop/admin/credentials \
  --secret-string '{"password_hash": "<bcrypt-hash>", "session_secret": "<session-secret>"}'
```

The CDK stack imports that name and passes the secret string to the admin Lambda through the `ADMIN_CREDENTIALS_SECRET_JSON` dynamic reference. The JSON fields are `password_hash` and `session_secret`; the examples above are placeholders only.

## Deploy

Local deployers should build frontend assets before synthesizing or deploying so `web/static/` contains the generated CSS and vendored HTMX files used by CDK:

```sh
npm --prefix web run build
AWS_REGION=us-east-1 CDK_DEFAULT_REGION=us-east-1 npm --prefix infra run synth
```

Deploy locally:

```sh
cd infra
AWS_REGION=us-east-1 npx cdk deploy ThailandGiftshopStack --parameters HostedZoneId=ROUTE53_HOSTED_ZONE_ID
```

Replace `ROUTE53_HOSTED_ZONE_ID` with the public Route 53 hosted zone ID for `thailandgiftshop.com`.

The stack outputs `SiteUrl` as `https://thailandgiftshop.com` and also outputs `SiteDistributionDomainName` for the underlying CloudFront distribution. The existing `SsrHttpApiUrl` output remains available for direct API Gateway access while CloudFront handles normal site traffic. Catalog infrastructure outputs include `CatalogTableName` and `CatalogTableArn`; product image infrastructure outputs include `ProductImagesBucketName` and `ProductImagesBaseUrl`.

After changing seeded image URLs, product creation dates, or catalog indexes, reseed the catalog so DynamoDB points at `/images/products/...` and populates the latest-products access pattern:

```sh
AWS_REGION=us-east-1 go run ./cmd/seedcatalog
```

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

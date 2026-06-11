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

`CATALOG_SLUG_INDEX_NAME`, `CATALOG_PUBLIC_INDEX_NAME`, `CATALOG_RECENT_INDEX_NAME`, and `CATALOG_ENTITY_INDEX_NAME` default to `slug-index`, `public-index`, `recent-index`, and `entity-index`, which match the CDK-provisioned table. The Home page displays latest active products from the recent index, admin product/category lists read the entity index, and product cards use `image_url` from DynamoDB, falling back to `PRODUCT_IMAGE_PLACEHOLDER_URL` or `/images/placeholder-product.jpg` when the field is empty.

Set `CART_COOKIE_SECRET` to a stable local-only value when exercising cart cookies locally. Set `CUSTOMER_SESSION_SECRET` the same way when exercising customer accounts, sign-in, or checkout locally — it signs the customer session, CSRF, and login-throttle keys. Production receives both values from CDK-managed generated Secrets Manager secrets, so no production signing secret is stored in this repository.

Customer accounts, carts, addresses, and orders live in the `thailandgiftshop-commerce` DynamoDB table. `COMMERCE_TABLE_NAME` selects it; `COMMERCE_CUSTOMER_ORDERS_INDEX_NAME` and `COMMERCE_ORDERS_INDEX_NAME` default to `customer-orders-index` and `orders-index`, which match the CDK-provisioned table. When `COMMERCE_TABLE_NAME` is unset outside production, the devserver uses an in-memory commerce store.

Payments are selected by environment. With Stripe credentials present the Stripe-hosted checkout is used; otherwise non-production runs fall back to the in-memory fake provider and production fails closed:

```sh
export STRIPE_CREDENTIALS_SECRET_JSON='{"secret_key":"<sk-test-key>","webhook_signing_secret":"<whsec>"}'
export PAYMENTS_PROVIDER=stripe   # optional; "fake" is rejected in production
export PUBLIC_BASE_URL=http://127.0.0.1:8080   # success/cancel/webhook URL base; defaults to https://thailandgiftshop.com
```

Keep real Stripe keys outside the repository. Card entry happens only on Stripe's hosted checkout page; the site never sees or stores card numbers.

Use deterministic demo data for local browser tests or admin/public E2E work by opting into the in-memory catalog store:

```sh
CATALOG_DEMO_STORE=1 CART_COOKIE_SECRET=<cart-cookie-secret> CUSTOMER_SESSION_SECRET=<customer-session-secret> go run ./cmd/devserver
```

When `CATALOG_DEMO_STORE=1` is set, the devserver skips DynamoDB and serves the checked-in demo catalog from memory, alongside one shared in-memory commerce store and the fake payment provider for both the storefront and admin handlers. The whole checkout journey then runs on-site with no Stripe keys and no network: placing an order redirects to the `/checkout/fake-pay` demo payment page, whose Pay action drives the real `/checkout/confirm` reconcile path, and orders placed on the storefront appear in the admin order desk at `/admin/orders`. The demo catalog includes an active variant product, `handwoven-indigo-scarf`, with sizes `S=1`, `M=2`, and `XL=0` for stable admin and public E2E coverage. The default devserver path still uses environment-backed catalog loading, so local Go tests and Playwright tests can run without a live AWS account.

For local admin work, set placeholder-only admin environment variables before starting the admin Lambda or any local wrapper that loads admin credentials from the environment:

```sh
export ADMIN_PASSWORD_HASH=<bcrypt-hash>
export ADMIN_SESSION_SECRET=<session-secret>
```

Create the bcrypt hash and session secret outside the repository, then keep the real values in your shell, local secret manager, or CI secret store. Do not commit a plaintext admin password, generated bcrypt hash, or generated session secret. Local and dev code reads `ADMIN_PASSWORD_HASH` and `ADMIN_SESSION_SECRET` as a fallback; production loads the `thailandgiftshop/admin/credentials` secret at runtime from the name in `ADMIN_CREDENTIALS_SECRET_NAME` (an inline `ADMIN_CREDENTIALS_SECRET_JSON` blob is still honored ahead of it for local overrides).

Leave `ADMIN_ORIGIN_HEADER_SECRET` unset for local direct `/admin` work. In production, CDK generates this secret, configures CloudFront to send it as `X-TGS-Origin-Secret`, and configures the admin Lambda to reject `/admin` requests that do not include the matching header.

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

After deploying catalog index changes, reseed or backfill existing DynamoDB product and category rows so they include the entity-index attributes used by admin lists. Category-product membership rows and product slug-lock rows should not receive those entity-index attributes.

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

Lambda emits AWS-managed CloudWatch metrics automatically, and the HTTP API default stage has detailed metrics enabled. Lambda X-Ray tracing is active and the Lambda role includes the X-Ray write permissions required to publish trace data. CloudFront, WAF, S3, DynamoDB, API Gateway, and Lambda metrics are collected into a CloudWatch dashboard named `ThailandGiftshop-Operations`.

The admin Lambda emits custom embedded metric format (EMF) events to the `ThailandGiftshop/App` namespace for admin login attempts, origin-header rejections, catalog writes, and product image uploads. The stack also provisions CloudWatch alarms for the dashboard watchlist. Critical alarm actions publish to the `thailandgiftshop-operations-alarms` SNS topic, while watchlist alarms remain dashboard-only. Email subscriptions require confirmation before notifications flow.

## Bootstrap

Before deploying to an AWS account/region for the first time, bootstrap CDK in `us-east-1`:

```sh
cd infra
npx cdk bootstrap aws://ACCOUNT_ID/us-east-1
```

Replace `ACCOUNT_ID` as needed. Production deploys intentionally fail outside `us-east-1`.

Bootstrap (and later rotate) the production admin credentials — a manually managed Secrets Manager JSON secret named `thailandgiftshop/admin/credentials` — in one step. The same command creates the secret on first run and rotates it afterwards; just give it the password:

```sh
printf '%s' 'YOUR_ADMIN_PASSWORD' | \
  go run ./scripts/generate-admin-secret.go -push -password-stdin -yes
```

`-region` defaults to `us-east-1` and `-secret-id` to `thailandgiftshop/admin/credentials`. Supply the password via stdin (`-password-stdin`) or the `ADMIN_PASSWORD` env var rather than `-password`, which is visible in shell history. Omit both and a strong password is generated and printed once as `ADMIN_PASSWORD=…`. Drop `-yes` to confirm interactively, or add `-dry-run` to preview the action without writing.

> **Each rotation regenerates the session secret**, so it logs out all active admin sessions and invalidates outstanding CSRF tokens.

The secret JSON shape:

```json
{"password_hash": "<bcrypt-hash>", "session_secret": "<session-secret>"}
```

**Manual fallback.** Write the JSON to a file with `-out` (now optional) and push it with the AWS CLI yourself:

```sh
go run ./scripts/generate-admin-secret.go -out /tmp/thailandgiftshop-admin-secret.json

# First time:
aws secretsmanager create-secret \
  --region us-east-1 \
  --name thailandgiftshop/admin/credentials \
  --secret-string file:///tmp/thailandgiftshop-admin-secret.json

# Rotation:
aws secretsmanager put-secret-value \
  --region us-east-1 \
  --secret-id thailandgiftshop/admin/credentials \
  --secret-string file:///tmp/thailandgiftshop-admin-secret.json
```

The CDK stack passes the secret's name to the admin Lambda as `ADMIN_CREDENTIALS_SECRET_NAME` and grants it read access; the Lambda fetches the JSON from Secrets Manager at runtime and caches it briefly, so a rotation takes effect within a few minutes without a redeploy. (It is not injected via a deploy-time `ADMIN_CREDENTIALS_SECRET_JSON` dynamic reference, which would require a redeploy to pick up a rotation.)

The stack also creates a DynamoDB table for admin login attempts and passes its generated name to the admin Lambda as `ADMIN_LOGIN_ATTEMPTS_TABLE_NAME`. Failed admin logins are tracked per client, locked after 8 failures in 15 minutes, and receive a generic `429` with `Retry-After` during the 15-minute lockout.

### Stripe credentials and webhook

Before enabling checkout, bootstrap the Stripe credentials as a manually managed Secrets Manager JSON secret named `thailandgiftshop/stripe/credentials` in `us-east-1`. The webhook signing secret is not known until the endpoint is registered, so start with a placeholder value:

```sh
aws secretsmanager create-secret \
  --region us-east-1 \
  --name thailandgiftshop/stripe/credentials \
  --secret-string '{"secret_key":"<sk-live-or-test-key>","webhook_signing_secret":"<whsec-placeholder>"}'
```

After the first deploy, register the webhook endpoint in the Stripe dashboard as `https://thailandgiftshop.com/webhooks/stripe` — use the apex domain, because the `www` host issues a `308` redirect and Stripe does not follow redirects — subscribed to the four `checkout.session.*` events: `checkout.session.completed`, `checkout.session.async_payment_succeeded`, `checkout.session.async_payment_failed`, and `checkout.session.expired`. Then write the real `whsec_` value into the same secret:

```sh
aws secretsmanager put-secret-value \
  --region us-east-1 \
  --secret-id thailandgiftshop/stripe/credentials \
  --secret-string '{"secret_key":"<sk-live-or-test-key>","webhook_signing_secret":"<whsec>"}'
```

The CDK stack passes the secret name to both the SSR and admin Lambdas as `STRIPE_CREDENTIALS_SECRET_NAME` and grants `secretsmanager:GetSecretValue` on that name. Deployments do not resolve the secret value, so a missing Stripe secret will not roll back CloudFormation; checkout remains unavailable until the secret exists and contains valid JSON. Refunds for canceled paid orders are performed manually in the Stripe dashboard.

CloudFront has an AWS WAF web ACL with rate limits for `POST /admin/login`, `/admin*`, and customer-auth `POST /account/sign-in` and `POST /account/sign-up` requests. Direct API Gateway access through the `SsrHttpApiUrl` output remains useful for public SSR checks, but production admin access must use `https://thailandgiftshop.com/admin` so CloudFront can apply WAF rules and inject the admin origin header.

## Deploy

Local deployers should build frontend assets before synthesizing or deploying so `web/static/` contains the generated CSS and vendored HTMX files used by CDK:

```sh
npm --prefix web run build
AWS_REGION=us-east-1 CDK_DEFAULT_REGION=us-east-1 npm --prefix infra run synth
```

Deploy locally:

```sh
cd infra
AWS_REGION=us-east-1 npx cdk deploy ThailandGiftshopStack --parameters HostedZoneId=ROUTE53_HOSTED_ZONE_ID --parameters AlarmNotificationEmail=ops@example.com
```

Replace `ROUTE53_HOSTED_ZONE_ID` with the public Route 53 hosted zone ID for `thailandgiftshop.com` and replace `ops@example.com` with the operations email address that should receive critical alarm notifications.

The stack outputs `SiteUrl` as `https://thailandgiftshop.com` and also outputs `SiteDistributionDomainName` for the underlying CloudFront distribution. The existing `SsrHttpApiUrl` output remains available for direct public API Gateway checks while CloudFront handles normal site traffic and all production admin traffic. Catalog infrastructure outputs include `CatalogTableName` and `CatalogTableArn`; product image infrastructure outputs include `ProductImagesBucketName` and `ProductImagesBaseUrl`.

After changing seeded image URLs, product creation dates, or catalog indexes, reseed or backfill the catalog so DynamoDB points at `/images/products/...` and populates the latest-products and admin entity-index access patterns:

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
- Repository or environment variable: `ALARM_NOTIFICATION_EMAIL` set to the operations email address subscribed to critical alarms

The deployment workflow uses GitHub OIDC through `aws-actions/configure-aws-credentials`, so long-lived AWS access keys are not required.

The AWS role used by `AWS_ROLE_TO_ASSUME` must trust GitHub's OIDC provider and should be scoped to this repository's `production` GitHub environment, for example `repo:anhydrous99/thailandgiftshop:environment:production`.

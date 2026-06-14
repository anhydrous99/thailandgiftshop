# thailandgiftshop

Monorepo for `thailandgiftshop.com`.

The AWS CDK v2 infrastructure package lives in `infra/` and is written in Go. Go Lambda entrypoints, shared Go packages, templ templates, Tailwind CSS, and static asset code should live outside `infra/` so application code can evolve separately from deployment code.

The deployed CDK stack ID is `ThailandGiftshopStack`. Production is deployed in `us-east-1` so the CloudFront certificate and site infrastructure are managed by the same stack.

Static assets belong in `web/static/`. CDK deploys that folder to a private S3 bucket behind the site CloudFront distribution and serves it under `/static/`, so `web/static/logo.svg` is available as `/static/logo.svg`.

Product image seed assets belong in `web/product-images/`. CDK deploys that folder to a separate private retained S3 bucket behind the same CloudFront distribution and serves it under `/images/`, so `web/product-images/products/mango-sticky-rice-kit.jpg` is available as `/images/products/mango-sticky-rice-kit.jpg`. This bucket is intentionally separate from the pruned static asset deployment so future uploaded product images are not removed by site deploys.

## Prerequisites

- Go 1.25 or newer
- Node.js 24 LTS for frontend asset builds, browser tests, and CDK commands
- AWS credentials for local bootstrap/deploy

## Local Run

Create local-only admin credentials first. The devserver initializes both the public storefront and `/admin`, so it needs admin credentials even when you only plan to browse the storefront. If you do not provide `ADMIN_PASSWORD`, the generator prints a one-time `ADMIN_PASSWORD=...` value for local sign-in:

```sh
go run ./scripts/generate-admin-secret.go -out /tmp/tgs-admin-secret.json
export ADMIN_CREDENTIALS_SECRET_JSON="$(cat /tmp/tgs-admin-secret.json)"
```

Then serve the checked-in local site with the Go devserver:

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

Set `CART_COOKIE_SECRET` to a stable local-only value when exercising cart cookies locally. Set `CUSTOMER_SESSION_SECRET` the same way when exercising customer accounts, sign-in, or checkout locally — it signs the customer session, CSRF, login-throttle keys, the orders `?after` cursor tokens, and the guest checkout values (the `__Host-tgs_guest_order` pointer cookie and the guest order `?access=` links). Production receives both values from CDK-managed generated Secrets Manager secrets, so no production signing secret is stored in this repository. Rotating `CUSTOMER_SESSION_SECRET` invalidates every customer session, CSRF token, orders cursor link (they self-heal to page 1), guest pointer cookie, and outstanding guest order access link simultaneously. Caveat: anyone still holding a guest order's `/checkout/confirm?session_id=…` URL can re-mint an access token under the new secret while inside that order's 30-days-from-payment window — rotation kills leaked *links*, not confirm-URL holders.

Customer accounts, carts, addresses, and orders live in the `thailandgiftshop-commerce` DynamoDB table. `COMMERCE_TABLE_NAME` selects it; `COMMERCE_CUSTOMER_ORDERS_INDEX_NAME` and `COMMERCE_ORDERS_INDEX_NAME` default to `customer-orders-index` and `orders-index`, which match the CDK-provisioned table. When `COMMERCE_TABLE_NAME` is unset outside production, the devserver uses an in-memory commerce store.

Payments are selected by environment. With Stripe credentials present the Stripe-hosted checkout is used; otherwise non-production runs fall back to the in-memory fake provider and production fails closed:

```sh
export STRIPE_CREDENTIALS_SECRET_JSON='{"secret_key":"<sk-test-key>","webhook_signing_secret":"<whsec>"}'
export PAYMENTS_PROVIDER=stripe   # optional; "fake" is rejected in production
export PUBLIC_BASE_URL=http://127.0.0.1:8080   # success/cancel/webhook URL base; defaults to https://thailandgiftshop.com
```

Keep real Stripe keys outside the repository. Card entry happens only on Stripe's hosted checkout page; the site never sees or stores card numbers.

Transactional email is selected by environment. Local, demo, and test runs use fake capture and never call AWS SES. Production uses AWS SESv2 simple text and HTML transactional mail through the verified `thailandgiftshop.com` identity and must fail closed when sender config is missing, invalid, or set to fake mode:

```sh
export EMAIL_SENDER_MODE=fake
export PUBLIC_BASE_URL=http://127.0.0.1:8080

export EMAIL_SENDER_MODE=ses
export EMAIL_FROM_ADDRESS=noreply@thailandgiftshop.com
export EMAIL_SES_REGION=us-east-1
export PUBLIC_BASE_URL=https://thailandgiftshop.com
```

`PUBLIC_BASE_URL` builds password-reset and order links. Local defaults to `http://127.0.0.1:8080`; production defaults to `https://thailandgiftshop.com`. The app does not build these links from request `Host` headers.

When `EMAIL_SENDER_MODE=fake` is enabled in the local devserver and `APP_ENV` is not production, browser tests can inspect captured mail with `GET /__test/emails` and clear it with `POST /__test/emails/clear`. These endpoints are devserver-only and are not registered by the SSR or admin Lambda handlers.

Password reset is customer-only; admin password reset is out of scope. Reset request responses are enumeration-safe, links expire after 30 minutes, reset links are single-use, a successful reset invalidates customer sessions, and raw reset tokens are not stored at rest.

Order emails use the order email snapshot, `Order.Email`, for both guest and signed-in orders. Payment finalization sends one `order_placed` email when an order moves from `pending_payment` to `paid`; later lifecycle transitions send status emails; admin tracking changes send tracking-update emails. Email send failures are best-effort metadata and do not roll back checkout, payment, refund, or admin order mutations.

Bounce and complaint handling, queues, marketing email, SES templates, attachments, and preference centers are out of scope for this phase.

Use deterministic demo data for local browser tests or admin/public E2E work by opting into the in-memory catalog store:

```sh
CATALOG_DEMO_STORE=1 \
  ADMIN_CREDENTIALS_SECRET_JSON="$(cat /tmp/tgs-admin-secret.json)" \
  CART_COOKIE_SECRET=<cart-cookie-secret> \
  CUSTOMER_SESSION_SECRET=<customer-session-secret> \
  go run ./cmd/devserver
```

When `CATALOG_DEMO_STORE=1` is set, the devserver skips DynamoDB and serves the checked-in demo catalog from memory, alongside one shared in-memory commerce store and the fake payment provider for both the storefront and admin handlers. The whole checkout journey then runs on-site with no Stripe keys and no network: placing an order redirects to the `/checkout/fake-pay` demo payment page, whose Pay action drives the real `/checkout/confirm` reconcile path, and orders placed on the storefront appear in the admin order desk at `/admin/orders`. The demo catalog includes an active variant product, `handwoven-indigo-scarf`, with sizes `S=1`, `M=2`, and `XL=0` for stable admin and public E2E coverage. The default devserver path still uses environment-backed catalog loading, so local Go tests and Playwright tests can run without a live AWS account.

Guest checkout works end-to-end in demo mode too: an anonymous shopper with a cookie cart gets the guest layout on `/checkout` (contact email + shipping address on one form), pays on the same fake-pay page, and lands on a tokenized order page. Guests get no order history; their record is a signed `?access=` status link (valid for 30 days from payment, anchored to the payment time so confirm replays never extend it) plus Stripe's receipt email in live mode. Stranded guest pending orders release their stock via the guaranteed 30-minute `checkout.session.expired` webhook in production; signed checkout-cancel returns release their own reservation before the cart is normalized.

For local admin work, prefer the generated local-only `ADMIN_CREDENTIALS_SECRET_JSON` shown above. If you already have a bcrypt hash and session secret from another local secret manager, the lower-level fallback is:

```sh
export ADMIN_PASSWORD_HASH=<bcrypt-hash>
export ADMIN_SESSION_SECRET=<session-secret>
```

Create the bcrypt hash and session secret outside the repository, then keep the real values in your shell, local secret manager, or CI secret store. Do not commit a plaintext admin password, generated bcrypt hash, or generated session secret. Local and dev code reads `ADMIN_PASSWORD_HASH` and `ADMIN_SESSION_SECRET` as a fallback; production loads the `thailandgiftshop/admin/credentials` secret at runtime from the name in `ADMIN_CREDENTIALS_SECRET_NAME` (an inline `ADMIN_CREDENTIALS_SECRET_JSON` blob is still honored ahead of it for local overrides).

Leave `ADMIN_ORIGIN_HEADER_SECRET` unset for local direct `/admin` work. In production, CDK generates this secret, configures CloudFront to send it as `X-TGS-Origin-Secret`, and configures the SSR and admin Lambdas to reject requests that do not include the matching header. During an origin-header rotation, pass the old header value as the no-echo `AdminOriginHeaderPreviousSecret` deploy parameter until CloudFront has propagated the new origin custom header everywhere; leave it blank otherwise.

## Seed Catalog

Seed missing deployed DynamoDB catalog rows with demo categories, products, product slug-lock rows, and category-product rows:

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

The seed command is insert-only: it writes rows that are absent and skips rows that already exist. After changing seeded image URLs, product creation dates, or catalog index projections, run a purpose-built backfill or migration for existing rows; do not rely on `cmd/seedcatalog` to overwrite production catalog data. Category-product membership rows and product slug-lock rows should not receive entity-index attributes.

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

Build frontend assets after editing Tailwind or template files:

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

CDK infrastructure tests compare the synthesized stack against snapshots in `infra/testdata/`. When an intended infrastructure change alters the synthesized CloudFormation template, update the fixtures and review the snapshot diff before committing:

```sh
UPDATE_SNAPSHOTS=1 go test ./infra
```

Check Go formatting:

```sh
gofmt -l $(git ls-files '*.go')
```

## Observability

The CDK stack provisions the SSR Lambda with its own CloudWatch Logs group at `/aws/lambda/thailandgiftshop-ssr` and 3-month retention. The HTTP API stage writes access logs to `/aws/apigateway/thailandgiftshop-ssr`, also with 3-month retention. Static and product image deployment helper logs are written to `/aws/lambda/thailandgiftshop-static-assets-deployment` with the same 3-month retention policy.

Three edge-log invariants protect the guest order access tokens, which ride in URL query strings: the API Gateway access-log **format string** (in `infra/main.go`) emits `$context.routeKey` and contains no raw path or query field — it must never gain `$context.path`, `$context.requestPath`, or any raw-query field; CloudFront standard logging is off and must stay off (or strip query strings) for the same reason; and WAF sampled requests are disabled because sampled-request inspection can expose request URLs. Application code likewise never logs request paths or query strings (`logAccountError`/`logHandlerError`/`logHandlerWarn` carry no query data).

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

After the first deploy, register the webhook endpoint in the Stripe dashboard as `https://thailandgiftshop.com/webhooks/stripe` — use the apex domain, because the `www` host issues a `308` redirect and Stripe does not follow redirects — subscribed to **seven** events: the four `checkout.session.*` events (`checkout.session.completed`, `checkout.session.async_payment_succeeded`, `checkout.session.async_payment_failed`, and `checkout.session.expired`) plus the three refund events (`refund.created`, `refund.updated`, and `refund.failed`). Existing deployments must edit the webhook endpoint in the Stripe dashboard to add the three `refund.*` events before relying on automatic refund settlement; until then, settlement is healed by the admin order page's reconcile when the order is opened. Then write the real `whsec_` value into the same secret:

```sh
aws secretsmanager put-secret-value \
  --region us-east-1 \
  --secret-id thailandgiftshop/stripe/credentials \
  --secret-string '{"secret_key":"<sk-live-or-test-key>","webhook_signing_secret":"<whsec>"}'
```

The CDK stack passes the secret name to both the SSR and admin Lambdas as `STRIPE_CREDENTIALS_SECRET_NAME` and grants `secretsmanager:GetSecretValue` on that name. Deployments do not resolve the secret value, so a missing Stripe secret will not roll back CloudFormation; checkout remains unavailable until the secret exists and contains valid JSON.

### Transactional email

Before enabling production email delivery, make sure AWS SES is ready in `us-east-1`. The CDK stack creates a Route53-backed SES identity for `thailandgiftshop.com` with Easy DKIM and grants the SSR and admin Lambdas `ses:SendEmail` for transactional mail. SES identity verification, DKIM DNS, and SES production access must all be complete before production deliveries are expected to leave the account. Accounts still in the SES sandbox can only send to verified recipients.

Production sender config should be:

```sh
EMAIL_SENDER_MODE=ses
EMAIL_FROM_ADDRESS=noreply@thailandgiftshop.com
EMAIL_SES_REGION=us-east-1
PUBLIC_BASE_URL=https://thailandgiftshop.com
```

Do not run production with `EMAIL_SENDER_MODE=fake`. Startup and config validation should reject fake mode and incomplete SES sender settings in production. For local, demo, and Playwright runs, keep `EMAIL_SENDER_MODE=fake` so tests use the fake capture outbox and never call SES.

Refunds are issued automatically from the admin order page: the Refund action returns the full payment to the shopper's original payment method through Stripe (paid orders also return their reserved stock; shipped/delivered orders do not), and the order settles to `refunded` via the `refund.*` webhooks or the admin page's reconcile-on-render. Payments captured for orders that already reached a terminal status (`paid_after_terminal`) are also refunded automatically and unattended; the existing `StripeWebhook` alarm's meaning for that outcome therefore changes from "go issue a refund" to "a refund was already issued unattended — verify in Stripe that it is legitimate (not a pay-then-expire abuse pattern, which burns non-returnable processing fees and can serve as card-testing cover; the WAF rate limit in front of checkout bounds it) and that it settles" — the dashboard's auto-issued refund series is the volume watch point. The Stripe dashboard remains the manual fallback only for orders without a payment-intent ID, refunds that fail again after a retry, failed terminal-order auto-refunds (those orders have no in-app retry), and the already-fully-refunded reconciliation case that appears when a refund is retried after Stripe's ~24h idempotency-key window. Refunds issued directly in the Stripe dashboard carry no order metadata and do not update order status.

CloudFront has an AWS WAF web ACL with rate limits for `POST /admin/login`, `/admin*`, customer-auth `POST /account/sign-in` and `POST /account/sign-up`, and `POST /checkout/place-order` requests (guest checkout removes the account gate from place-order, so the edge throttle takes its place; the rule bounds request count, not units reserved). Production traffic, including public SSR checks, must use `https://thailandgiftshop.com/` so CloudFront can apply WAF rules and inject the origin header accepted by the Lambdas.

## Deploy

Local deployers should build frontend assets before synthesizing or deploying so `web/static/` contains the generated CSS used by CDK:

```sh
npm --prefix web run build
AWS_REGION=us-east-1 CDK_DEFAULT_REGION=us-east-1 npm --prefix infra run synth
```

Run Go and browser tests before deploying email, reset, or checkout behavior. Rebuild frontend assets when templates or static assets changed:

```sh
go test $(sh scripts/go-packages.sh)
npm --prefix web test
npm --prefix web run build
AWS_REGION=us-east-1 CDK_DEFAULT_REGION=us-east-1 npm --prefix infra run synth
```

Deploy locally:

```sh
cd infra
AWS_REGION=us-east-1 npx cdk deploy ThailandGiftshopStack --parameters HostedZoneId=ROUTE53_HOSTED_ZONE_ID --parameters AlarmNotificationEmail=ops@example.com
```

Replace `ROUTE53_HOSTED_ZONE_ID` with the public Route 53 hosted zone ID for `thailandgiftshop.com` and replace `ops@example.com` with the operations email address that should receive critical alarm notifications. For an origin-header rotation only, add `--parameters AdminOriginHeaderPreviousSecret=OLD_HEADER_VALUE`, deploy the new generated secret, wait for CloudFront propagation, then deploy again with the parameter omitted or blank.

The stack outputs `SiteUrl` as `https://thailandgiftshop.com` and also outputs `SiteDistributionDomainName` for the underlying CloudFront distribution. The `SsrHttpApiUrl` output identifies the private API Gateway origin behind CloudFront; production requests to it are expected to fail without the CloudFront-injected origin header. Catalog infrastructure outputs include `CatalogTableName` and `CatalogTableArn`; product image infrastructure outputs include `ProductImagesBucketName` and `ProductImagesBaseUrl`.

After changing seeded image URLs, product creation dates, or catalog indexes, backfill existing catalog rows so DynamoDB points at `/images/products/...` and populates the latest-products and admin entity-index access patterns. Use `cmd/seedcatalog` only to insert rows that are currently missing:

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
- `npm audit --omit=dev --audit-level=high` for web and infra production dependencies
- `gofmt` check
- `go vet` for tracked Go package directories
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

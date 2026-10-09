# Development

Local development workflow for `thailandgiftshop.com`: setup, running the devserver, demo mode, seeding the catalog, codegen, asset builds, and tests. For the full environment-variable reference, see [`configuration.md`](configuration.md).

## Prerequisites

- Go 1.27.1 or newer (see `go.mod`)
- Node.js 24 LTS (`.nvmrc` pins `24`) for frontend asset builds, browser tests, and CDK commands
- AWS credentials only if you intend to read deployed data or deploy

## One-time setup

Install dependencies for frontend builds, browser tests, CDK commands, and explicit Go dependency prefetching:

```sh
go mod download
npm --prefix infra ci
npm --prefix web ci
```

Install Playwright Chromium once for browser tests:

```sh
npm --prefix web run test:install
```

## Running the devserver

The devserver initializes both the public storefront and `/admin`, so it needs admin credentials even when you only plan to browse the storefront. Create local-only admin credentials first. If you do not provide `ADMIN_PASSWORD`, the generator prints a one-time `ADMIN_PASSWORD=...` value for local sign-in:

```sh
go run ./scripts/generate-admin-secret.go -out /tmp/tgs-admin-secret.json
export ADMIN_CREDENTIALS_SECRET_JSON="$(cat /tmp/tgs-admin-secret.json)"
```

Then serve the checked-in local site:

```sh
go run ./cmd/devserver
```

Open `http://127.0.0.1:8080`. The leading `./` is intentional because `cmd/devserver` is a local Go package path.

The devserver uses `web/static/` and `web/product-images/` by default and does not install Node dependencies or rebuild frontend assets. Override the bind address or asset directories when needed:

```sh
PORT=8081 go run ./cmd/devserver
HOST=0.0.0.0 go run ./cmd/devserver
STATIC_DIR=/path/to/static go run ./cmd/devserver
IMAGE_DIR=/path/to/product-images go run ./cmd/devserver
```

If `CATALOG_TABLE_NAME` is unset, the devserver uses an empty catalog store.

### Reading the deployed catalog locally

To read the deployed DynamoDB catalog locally, run the devserver with an AWS profile that already has access to the catalog table:

```sh
AWS_PROFILE=default AWS_REGION=us-east-1 CATALOG_TABLE_NAME=thailandgiftshop-catalog go run ./cmd/devserver
```

The catalog index names (`CATALOG_SLUG_INDEX_NAME`, `CATALOG_PUBLIC_INDEX_NAME`, `CATALOG_RECENT_INDEX_NAME`, `CATALOG_ENTITY_INDEX_NAME`) default to the CDK-provisioned values. The home page displays latest active products from the recent index, admin product/category lists read the entity index, and product cards use `image_url` from DynamoDB, falling back to `PRODUCT_IMAGE_PLACEHOLDER_URL` or `/images/placeholder-product.jpg` when the field is empty.

## Demo mode (no AWS, no Stripe)

Use deterministic demo data for local browser tests or admin/public E2E work by opting into the in-memory catalog store:

```sh
CATALOG_DEMO_STORE=1 \
  ADMIN_CREDENTIALS_SECRET_JSON="$(cat /tmp/tgs-admin-secret.json)" \
  CART_COOKIE_SECRET=<cart-cookie-secret> \
  CUSTOMER_SESSION_SECRET=<customer-session-secret> \
  go run ./cmd/devserver
```

When `CATALOG_DEMO_STORE=1` is set, the devserver skips DynamoDB and serves the checked-in demo catalog from memory, alongside one shared in-memory commerce store and the fake payment provider for both the storefront and admin handlers. The whole checkout journey then runs on-site with no Stripe keys and no network: placing an order redirects to the `/checkout/fake-pay` demo payment page, whose Pay action drives the real `/checkout/confirm` reconcile path, and orders placed on the storefront appear in the admin order desk at `/admin/orders`.

The demo catalog includes an active variant product, `handwoven-indigo-scarf`, with sizes `S=1`, `M=2`, and `XL=0` for stable admin and public E2E coverage. The default devserver path still uses environment-backed catalog loading, so local Go tests and Playwright tests can run without a live AWS account.

Guest checkout works end-to-end in demo mode too: an anonymous shopper with a cookie cart gets the guest layout on `/checkout` (contact email + shipping address on one form), pays on the same fake-pay page, and lands on a tokenized order page. Guests get no order history; their record is a signed `?access=` status link (valid for 30 days from payment, anchored to the payment time so confirm replays never extend it) plus Stripe's receipt email in live mode.

## Seeding the catalog

Seed missing deployed DynamoDB catalog rows with demo categories, products, product slug-lock rows, and category-product rows:

```sh
AWS_REGION=us-east-1 go run ./cmd/seedcatalog
```

The command defaults to the CDK table name, `thailandgiftshop-catalog`. Override it or preview without writing:

```sh
AWS_REGION=us-east-1 go run ./cmd/seedcatalog -table CUSTOM_TABLE_NAME
AWS_REGION=us-east-1 go run ./cmd/seedcatalog -dry-run
```

The seed command is insert-only: it writes rows that are absent and skips rows that already exist. After changing seeded image URLs, product creation dates, or catalog index projections, run a purpose-built backfill or migration for existing rows; do not rely on `cmd/seedcatalog` to overwrite production catalog data. Category-product membership rows and product slug-lock rows should not receive entity-index attributes.

## Code generation

Regenerate `*_templ.go` after editing any `.templ` file, and commit the generated output alongside the `.templ` source:

```sh
go tool templ generate
```

## Frontend assets

Build Tailwind CSS and responsive product image variants after editing Tailwind or template files. Output lands in `web/static/`:

```sh
npm --prefix web run build
```

The build runs three steps: `build:images` (generates `-320w` through `-1200w` JPEG/WebP variants with sharp), `build:css` (Tailwind v4, minified, into `web/static/assets/app.css`), and `fingerprint` (asset fingerprinting). Keep CSS source in `web/src/styles/app.css`; built output belongs in `web/static/assets/app.css`.

## Testing

Run Go unit tests and the Playwright browser suite:

```sh
go test $(sh scripts/go-packages.sh)
npm --prefix web test
```

`npm --prefix web test` builds assets, starts a fresh demo devserver on `http://127.0.0.1:18080`, and runs Playwright (no production credentials or external payment/email services). Playwright manages its own devserver through `web/playwright.config.ts` and never reuses an existing server: an occupied test port fails explicitly rather than testing an unrelated process. The managed environment overrides production mode, secret-name/inline credential overrides, origin protection headers, and service selection with local demo/fake settings. Use Node 24 LTS and a credential-free shell for validation. Browser specs live in `web/tests/*.spec.ts`; stock-sensitive checkout and paging specs create their own products via the local admin flow.

Run a single Go package or test:

```sh
go test ./internal/ssr/...
go test ./internal/ssr/ -run TestName
```

Run a single browser spec from `web/` so Playwright loads its managed-server configuration:

```sh
(cd web && npm exec -- playwright test tests/pages.spec.ts)
```

CDK infrastructure tests compare the synthesized stack against snapshots in `infra/testdata/`. When an intended infrastructure change alters the synthesized CloudFormation template, update the fixtures and review the snapshot diff before committing:

```sh
UPDATE_SNAPSHOTS=1 go test ./infra
```

## Lint and format

CI enforces formatting and vetting. Run both before pushing changes that touch Go behavior or infrastructure:

```sh
gofmt -l $(git ls-files '*.go')
go vet $(sh scripts/go-packages.sh)
```

`gofmt -l` should print nothing; CI fails if it lists files. `scripts/go-packages.sh` lists tracked and untracked Go package directories, so prefer it over `./...` to keep codegen artifacts and infra handled consistently.

## Synthesizing infrastructure locally

Build frontend assets first so `web/static/` contains the generated CSS used by CDK, then synthesize the CloudFormation template:

```sh
npm --prefix web run build
AWS_REGION=us-east-1 CDK_DEFAULT_REGION=us-east-1 npm --prefix infra run synth
```

See [`deployment.md`](deployment.md) for bootstrap and deploy steps.

## Local email and webhook testing

For exercising transactional email and Stripe webhooks against the local devserver, see [`configuration.md`](configuration.md#transactional-email) and [`operations.md`](operations.md#stripe-webhooks). In short: keep `EMAIL_SENDER_MODE=fake` locally (browser tests inspect captured mail at `GET /__test/emails`), and forward real Stripe test-mode webhooks with the Stripe CLI rather than registering a tunnel URL in the dashboard.

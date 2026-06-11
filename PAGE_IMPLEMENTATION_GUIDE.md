# Page Implementation Guide

This guide is for AI agents implementing the pages implied by the home page. Keep the site positioned as an online Bangkok-style gift shop: snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes. Do not reintroduce gift-set, bundle, or ready-to-wrap framing.

## Current State

- The SSR app routes the catalog pages plus customer accounts, server-backed carts, a real Stripe-hosted checkout, order history with tracking, and the Stripe webhook endpoint.
- Customers sign up and sign in under `/account`; sessions are server-side rows in the commerce table with HMAC-signed cookies. Signed-in carts are stored server-side, and the `tgs_cart` cookie becomes a write-through mirror so the header cart label stays cookie-only and catalog pages stay edge-cacheable.
- Checkout requires sign-in. Card entry happens exclusively on Stripe's hosted checkout page (SAQ-A); the site never sees or stores card numbers and persists only Stripe opaque IDs plus brand/last4 for display. Local demo runs without Stripe credentials use the in-memory fake provider, which keeps the payment step on-site at `/checkout/fake-pay`.
- The admin Lambda adds an order desk under `/admin/orders` for adding tracking, advancing paid orders through shipped/delivered, and canceling with automatic stock release.
- `internal/ssr/home.templ` renders the full home page.
- `internal/ssr/handler.go` owns page routing and should keep unknown paths returning `404`.
- Catalog data already exposes the required page data through `catalog.Store`:
  - `ListRecentlyAddedProducts(ctx, limit)` for home.
  - `ListActiveProducts(ctx, limit)` for product listing.
  - `GetProductBySlug(ctx, slug)` for product detail.
  - `ListActiveCategories(ctx)` for category navigation and category index.
  - `ListActiveProductsByCategory(ctx, categorySlug, limit)` for category detail.
- Commerce data (customers, sessions, addresses, carts, orders) lives in `internal/commerce` backed by the `thailandgiftshop-commerce` DynamoDB table; checkout orchestration is `internal/checkout`; payment providers (Stripe and the demo fake) are `internal/payments`; stock reservations go through `catalog.StockStore`.

## Pages To Implement

Implement these routes as server-rendered pages using templ. Preserve existing asset paths and layout language.

| Page | Route | Data Source | Empty / Missing State |
| --- | --- | --- | --- |
| Home | `/` | Recent products and active categories | Existing empty product state |
| Product listing | `/products` | `ListActiveProducts(ctx, 0)` | Show a calm empty catalog message |
| Product detail | `/products/{slug}` | `GetProductBySlug(ctx, slug)` | `404` when not found |
| Category index | `/categories` | `ListActiveCategories(ctx)` | Show no-categories message |
| Category detail | `/categories/{slug}` | `ListActiveCategories(ctx)` plus `ListActiveProductsByCategory(ctx, slug, 0)` | `404` when slug is not in active categories; empty product grid when category exists but has no products |
| Story / About | `/story` | Static content | No data dependency |
| Cart | `/cart` | Anonymous: `tgs_cart` cookie; signed-in: server cart row | Show an empty cart message when no cart items are present |
| Sign up | `/account/sign-up` (GET/POST) | Guest CSRF + `commerce.Store` | Signed-in visitors 303 to `/account` |
| Sign in | `/account/sign-in` (GET/POST) | Guest CSRF + `commerce.Store` | `?return_to=` validated; reset-deferral copy |
| Sign out | `POST /account/sign-out` | Session + CSRF | 303 `/` clearing session, CSRF, and cart-mirror cookies |
| Account overview | `/account` | Email, default address, recent 3 orders, password form | Anonymous visitors 303 to sign-in with `return_to` |
| Password change | `POST /account/password` | Session + CSRF, re-verified current password | Revokes all other sessions |
| Addresses | `/account/addresses` (GET/POST), `/account/addresses/{id}/edit`, `POST .../{id}/update`, `POST .../{id}/remove`, `POST .../{id}/default` | `commerce.Store` | Cap 10 per customer; empty state invites the first address |
| Saved cards | `/account/payment-methods`, `POST .../add`, `POST .../{pmID}/remove` | Live Stripe payment-method list (brand, last4, expiry only) | "No saved cards yet." when none |
| Checkout | `/checkout` | Server cart + saved addresses | Empty/unavailable cart 303 to `/cart`; anonymous 303 to sign-in with `return_to=/checkout`; inline address form when none exist |
| Place order | `POST /checkout/place-order` | `checkout.Service.PlaceOrder` | 303 to the provider's hosted payment page (or `/checkout/fake-pay` in demo runs) |
| Checkout confirm | `/checkout/confirm` | Server-side provider session reconcile | Paid 303 to `/orders/{id}?placed=1`; unpaid renders the no-JS processing page |
| Demo payment | `/checkout/fake-pay` (GET/POST) | Fake provider only | `404` unless the fake provider is wired (never in production) |
| Orders | `/orders` | `ListOrdersByCustomer`, newest-first, 20 per page, signed `?after` cursor pages the full history | Empty state invites shopping |
| Order detail | `/orders/{orderID}` | Frozen order snapshot (names/prices at purchase) | Foreign or missing order returns `404`, never `403` |
| Stripe webhook | `POST /webhooks/stripe` | Signature-verified provider events | Invalid signature returns `400` |

All account, checkout, and order pages send `Cache-Control: private, no-store` plus `X-Robots-Tag: noindex, follow`, stay out of the sitemap, and get trailing-slash `308` redirects. Form POSTs follow POST-redirect-GET.

Admin order desk routes (admin Lambda): `/admin/orders`, `/admin/orders/{id}`, `POST /admin/orders/{id}/advance`, `POST /admin/orders/{id}/tracking`, `POST /admin/orders/{id}/cancel`.

Optional aliases may redirect, not duplicate:

- `/shop` -> `/products`
- `/about` -> `/story`

## Category Rules

Category pages and category navigation must be derived from catalog data.

- Do not hard-code category routes such as `/categories/souvenirs` in templates.
- Add a shared helper or view model that loads `ListActiveCategories(ctx)` and passes those categories into pages that render category navigation.
- The home page `Shop categories` section should render from active categories instead of fixed cards.
- Each category card/link should use `category.Slug`, `category.Name`, and `category.Description`.
- Category detail must verify that the requested slug exists in `ListActiveCategories(ctx)` before querying or rendering the detail page.
- If a category is inactive or missing, return `404`.
- Use `/categories/{slug}` as the canonical category URL.

## Product Rules

- Product cards on listing and category pages should link to `/products/{product.Slug}`.
- Product detail pages should show name, image with `DisplayImageURL`, price from `formatPrice`, description, and stock state.
- If `product.OutOfStock()` is true, show `Out of stock`; otherwise show `In stock`.
- Cart and checkout collect only what an order needs: the server-cart lines and a shipping address chosen from (or added to) the customer's saved addresses. Card entry happens exclusively on Stripe's hosted checkout page — never collect or store card numbers on-site. Orders persist frozen name/price snapshots so later catalog edits never change a placed order.

## Deferred Scope

Keep these features out of the current release unless a later guide explicitly adds them:

- Search, filter, and sort controls for catalog browsing.
- Guest checkout (sign-in is required to place an order).
- Email verification and self-service password reset (both need SES or equivalent email infrastructure).
- Refunds and a `refunded` order status (refunds for canceled paid orders are manual in the Stripe dashboard).
- Asynchronous payment methods beyond cards.

## Implementation Notes

- Keep routing in `internal/ssr/handler.go`; parse paths with small explicit helpers.
- Prefer view-model structs in `internal/ssr` over passing many separate slices/strings into templates.
- Add new templ files by page or shared partial:
  - `layout.templ` for common shell/header/footer if needed.
  - `product_card.templ` for repeated product cards.
  - `category_card.templ` for repeated category cards.
  - Page templates for product listing, product detail, category index, category detail, and story.
- If common layout is introduced, migrate home without changing the rendered visual behavior.
- After changing templ files, run `go tool templ generate`.
- Keep URLs lowercase and slug-based. Do not expose product IDs in public URLs.

## Tests

Update and add tests before considering the implementation complete.

- SSR unit tests:
  - `/` still renders the home page.
  - `/products` renders active products and links to detail pages.
  - `/products/{slug}` renders the matching product.
  - missing product slug returns `404`.
  - `/categories` renders categories returned by the store.
  - `/categories/{slug}` renders products for that category.
  - missing or inactive category slug returns `404`.
  - category links are generated from fake store data, proving they are not hard-coded.
  - account, checkout, and order routes: route/method matrices, anonymous 303s with validated `return_to`, CSRF rejection before dispatch, throttled sign-in 429s, merge-on-login, frozen order snapshots, webhook signature rejection, and `private, no-store` + noindex headers per kind.
- Playwright tests (demo mode, no Stripe keys, no network):
  - Home category links navigate to `/categories/{slug}`.
  - Product cards navigate to `/products/{slug}`.
  - Product listing, product detail, category index, category detail, and story pages render without JavaScript.
  - `account.spec.ts`: sign-up, sign-out, sign-in, generic wrong-password error, address CRUD plus default selection — all without JavaScript.
  - `checkout.spec.ts`: anonymous cart hand-off to sign-in, sign-up cart merge, address capture, place order, fake-pay, order history, cancel paths, and last-unit stock release.
  - `payment-methods.spec.ts`: setup flow saves the demo card, lists it, removes it.
  - `orders-admin.spec.ts`: admin order desk advances a paid order through tracking, shipped, and delivered.
- Verification commands:
  - `go tool templ generate`
  - `go test $(sh scripts/go-packages.sh)`
  - `npm test` from `web/`

## Acceptance Criteria

- All implied pages above return `200` for valid data-backed paths; account, checkout, and order pages return `200` for signed-in customers and `303` to sign-in for anonymous visitors.
- Missing product and category slugs return `404`; foreign or missing orders, addresses, and payment methods return `404`, never `403`.
- Category pages and category links are driven by `catalog.Store` data, not template constants.
- Order pages render from the frozen order snapshot, never live catalog data.
- No card numbers, CVCs, or expiry fields are ever collected or stored on-site; payment happens on Stripe's hosted page.
- The home page remains aligned with the Bangkok gift-shop positioning.
- Existing static assets and product image fallback behavior continue to work.

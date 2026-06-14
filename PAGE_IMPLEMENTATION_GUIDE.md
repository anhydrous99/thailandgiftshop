# Page Implementation Guide

This guide is for AI agents implementing the pages implied by the home page. Keep the site positioned as an online Bangkok-style gift shop: snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes. Do not reintroduce gift-set, bundle, or ready-to-wrap framing.

## Current State

- The SSR app routes the catalog pages plus customer accounts, server-backed carts, a real Stripe-hosted checkout, order history with tracking, and the Stripe webhook endpoint.
- Customers sign up and sign in under `/account`; sessions are server-side rows in the commerce table with HMAC-signed cookies. Signed-in carts are stored server-side, and the `__Host-tgs_cart` cookie becomes a write-through mirror so the header cart label stays cookie-only and catalog pages stay edge-cacheable.
- Checkout works signed-in or as a guest. Card entry happens exclusively on Stripe's hosted checkout page (SAQ-A); the site never sees or stores card numbers and persists only Stripe opaque IDs plus brand/last4 for display. Guest orders carry `CustomerID == ""`, ride the cookie cart, use the guest double-submit CSRF pair, and hand the shopper a signed, expiring `?access=` link to their order instead of order history. Local demo runs without Stripe credentials use the in-memory fake provider, which keeps the payment step on-site at `/checkout/fake-pay`.
- The admin Lambda adds an order desk under `/admin/orders` for adding tracking, advancing paid orders through shipped/delivered, canceling pending orders with automatic stock release, and issuing automatic Stripe refunds for paid/shipped/delivered orders (full amount; unshipped refunds also return their reserved stock).
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
| Cart | `/cart` | Anonymous: `__Host-tgs_cart` cookie; signed-in: server cart row | Show an empty cart message when no cart items are present |
| Sign up | `/account/sign-up` (GET/POST) | Guest CSRF + `commerce.Store` | Signed-in visitors 303 to `/account` |
| Sign in | `/account/sign-in` (GET/POST) | Guest CSRF + `commerce.Store` | `?return_to=` validated; password-reset link |
| Sign out | `POST /account/sign-out` | Session + CSRF | 303 `/` clearing session, CSRF, and cart-mirror cookies |
| Account overview | `/account` | Email, default address, recent 3 orders, password form | Anonymous visitors 303 to sign-in with `return_to` |
| Password change | `POST /account/password` | Session + CSRF, re-verified current password | Revokes all other sessions |
| Addresses | `/account/addresses` (GET/POST), `/account/addresses/{id}/edit`, `POST .../{id}/update`, `POST .../{id}/remove`, `POST .../{id}/default` | `commerce.Store` | Cap 10 per customer; empty state invites the first address |
| Saved cards | `/account/payment-methods`, `POST .../add`, `POST .../{pmID}/remove` | Live Stripe payment-method list (brand, last4, expiry only) | "No saved cards yet." when none |
| Checkout | `/checkout` | Signed-in: server cart + saved addresses. Anonymous: guest layout (email + address entry, guest CSRF) over the cookie cart | Empty/unavailable cart 303 to `/cart` (before any auth branch); inline address form when a signed-in customer has none |
| Place order | `POST /checkout/place-order` | `checkout.Service.PlaceOrder` (guest branch validates email + address, carries the signed `__Host-tgs_guest_order` pointer cookie) | 303 to the provider's hosted payment page (or `/checkout/fake-pay` in demo runs) |
| Checkout confirm | `/checkout/confirm` | Server-side provider session reconcile; guest orders are authorized by possession of the session id | Paid 303 to `/orders/{id}?placed=1` (guest: `?access=<token>&placed=1`); unpaid renders the no-JS processing page; anonymous without a session id `404`s |
| Demo payment | `/checkout/fake-pay` (GET/POST) | Fake provider only; the guest branch additionally requires the pointer cookie to name the session's order | `404` unless the fake provider is wired (never in production) |
| Orders | `/orders` | `ListOrdersByCustomer`, newest-first, 20 per page, signed `?after` cursor pages the full history (signed-in only — guests get the tokenized link instead) | Empty state invites shopping |
| Order detail | `/orders/{orderID}` | Frozen order snapshot (names/prices at purchase). A valid `?access=` token (HMAC-signed, expires 30 days after payment) grants a read-only render of exactly one guest order | Foreign or missing order returns `404`, never `403`; invalid/expired tokens behave as absent |
| Stripe webhook | `POST /webhooks/stripe` | Signature-verified provider events | Invalid signature returns `400` |

All account, checkout, and order pages send `Cache-Control: private, no-store` plus `X-Robots-Tag: noindex, follow`, stay out of the sitemap, and get trailing-slash `308` redirects. Form POSTs follow POST-redirect-GET.

Admin order desk routes (admin Lambda): `/admin/orders`, `/admin/orders/{id}`, `POST /admin/orders/{id}/advance`, `POST /admin/orders/{id}/tracking`, `POST /admin/orders/{id}/cancel`, `POST /admin/orders/{id}/refund`.

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
- Guest-order adoption and cart merge for a guest who registers later (revisit with email verification; unverified-email adoption is an account-takeover vector).
- Email verification.
- Partial refunds (the admin Refund action always refunds the full amount).
- Syncing dashboard-issued refunds (refunds created directly in the Stripe dashboard carry no order metadata and never update order status).
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
  - `checkout.spec.ts`: anonymous cart hand-off through the guest checkout's sign-in link, sign-up cart merge, address capture, place order, fake-pay, order history, cancel paths, and last-unit stock release.
  - `guest-checkout.spec.ts`: guest end-to-end purchase, the tokenized access link surviving a cookie wipe, tampered-token rejection, cancel keeping the cart, pointer-cookie session resume, and the admin guest chip. New testids: `guest-checkout-form`, `guest-email-input`, `checkout-sign-in-link`, `guest-order-link-notice`, `guest-signup-upsell`, `admin-order-guest`.
  - `payment-methods.spec.ts`: setup flow saves the demo card, lists it, removes it.
  - `orders-admin.spec.ts`: admin order desk advances a paid order through tracking, shipped, and delivered.
- Verification commands:
  - `go tool templ generate`
  - `go test $(sh scripts/go-packages.sh)`
  - `npm test` from `web/`

## Acceptance Criteria

- All implied pages above return `200` for valid data-backed paths; account and order pages return `200` for signed-in customers and `303` to sign-in for anonymous visitors (order detail also `200`s for a valid guest `?access=` token); `/checkout` renders the guest layout for anonymous shoppers with a cart.
- Missing product and category slugs return `404`; foreign or missing orders, addresses, and payment methods return `404`, never `403`.
- Category pages and category links are driven by `catalog.Store` data, not template constants.
- Order pages render from the frozen order snapshot, never live catalog data.
- No card numbers, CVCs, or expiry fields are ever collected or stored on-site; payment happens on Stripe's hosted page.
- The home page remains aligned with the Bangkok gift-shop positioning.
- Existing static assets and product image fallback behavior continue to work.

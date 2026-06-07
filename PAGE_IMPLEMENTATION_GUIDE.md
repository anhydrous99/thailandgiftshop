# Page Implementation Guide

This guide is for AI agents implementing the pages implied by the home page. Keep the site positioned as an online Bangkok-style gift shop: snacks, souvenirs, textiles, pantry items, decor, wellness, and small keepsakes. Do not reintroduce gift-set, bundle, or ready-to-wrap framing.

## Current State

- The SSR app currently routes `/` and the internal HTMX-only `/hello-fragment` path.
- `internal/ssr/home.templ` renders the full home page.
- `internal/ssr/handler.go` currently returns `404` for all paths except `/` and `/hello-fragment`.
- Catalog data already exposes the required page data through `catalog.Store`:
  - `ListRecentlyAddedProducts(ctx, limit)` for home.
  - `ListActiveProducts(ctx, limit)` for product listing.
  - `GetProductBySlug(ctx, slug)` for product detail.
  - `ListActiveCategories(ctx)` for category navigation and category index.
  - `ListActiveProductsByCategory(ctx, categorySlug, limit)` for category detail.

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
- Do not invent checkout, cart, account, or search behavior as part of this page pass.

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
- Playwright tests:
  - Home category links navigate to `/categories/{slug}`.
  - Product cards navigate to `/products/{slug}`.
  - Product listing, product detail, category index, category detail, and story pages render without JavaScript.
- Verification commands:
  - `go tool templ generate`
  - `go test $(sh scripts/go-packages.sh)`
  - `npm test` from `web/`

## Acceptance Criteria

- All implied pages above return `200` for valid data-backed paths.
- Missing product and category slugs return `404`.
- Category pages and category links are driven by `catalog.Store` data, not template constants.
- The home page remains aligned with the Bangkok gift-shop positioning.
- Existing static assets and product image fallback behavior continue to work.

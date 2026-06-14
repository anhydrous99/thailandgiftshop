# Launch Readiness Tracker

Tracks the pre-launch audit findings for `thailandgiftshop.com`: what has shipped,
the operational follow-ups tied to those changes, and the audit findings that are
still open. Update the checkboxes as items land.

Legend: `[x]` done · `[ ]` open · **P0** launch blocker · **P1** before/just-after
launch · **P2/P3** hardening & polish.

---

## 1. Shipped (audit remediation pass)

Twelve findings, one focused commit each.

- [x] **Payments integrity** — assert paid amount matches the order total in
  `FinalizePayment` (closes the `/checkout/confirm` reconcile path; new
  `amount_mismatch` metric feeds the existing critical alarm). `056728a`
- [x] **Perf** — reuse the resolved session on the signed-in checkout render
  (drops 2 redundant DynamoDB reads). `bf09016`
- [x] **UX** — notify shoppers when the cart is auto-adjusted (out-of-stock
  removed / quantities reduced). `69c2def`
- [x] **Security** — verify request `Origin`/`Referer` on cart mutations; forward
  those headers from CloudFront. `4d431ee`
- [x] **Security/CSP** — externalize the admin inline scripts so the production
  CSP no longer breaks admin image upload. `5803a3e`
- [x] **Security** — equalize password-reset request timing (constant-time
  floor) to close the user-enumeration oracle. `1e751e9`
- [x] **Perf/scale** — bound product listing (96) and sitemap (5000) reads.
  `8d3ddf7`
- [x] **Trust/legal** — add shipping, returns, privacy, terms, and contact pages
  + footer links + global support email. `bc2cded`
- [x] **Infra** — HSTS `includeSubDomains`. `837b84d`
- [x] **Infra** — DynamoDB deletion protection on catalog + commerce. `7a3488f`
- [x] **Infra** — encrypt the operations alarm SNS topic with a customer-managed
  KMS key (CloudWatch-publish grant included). `f7baa22`
- [x] **Infra** — AWS managed WAF rule groups (IP-reputation + known-bad-inputs
  blocking; common rule set in Count). `f953f21`

---

## 2. Direct follow-ups for the shipped items

Operational toggles created by the pass above; revisit on the noted trigger.

- [ ] **P1 — Flip WAF CommonRuleSet to Block.** It ships in **Count**. Review
  the per-rule CloudWatch metrics for false positives, then change
  `wafOverrideCount()` → `wafOverrideNone()` for `CommonRuleSet` in
  `infra/waf.go`. Re-check the `WafAdminBlocksAlarm` threshold afterward.
- [ ] **P0 (legal) — Replace Privacy & Terms draft copy.** `/privacy` and
  `/terms` render clearly-marked drafts and are `noindex`. After counsel review,
  update the content in `internal/ssr/policy.go`, drop the `Draft` flag, switch
  their metadata from `noindexMetadata` to `metadataForPath` in
  `internal/ssr/seo.go`, and add them to `sitemapXML`.
- [ ] **P1 — Enable HSTS preload.** `Preload` is intentionally off
  (`infra/site.go`). After a soak confirming every subdomain is HTTPS-only, set
  `Preload: true` and submit the apex to the HSTS preload list (one-way door).
- [ ] **P2 — Cursor pagination** for `/products` and `/categories/{slug}` (the
  successor to the bounded caps). Mirror the orders cursor pattern and allowlist
  the page param in the CloudFront cache key.

Deploy notes: WAF managed groups add ~925 WCU (under the 1500 default); the SNS
KMS key adds ~$1/mo.

---

## 3. Open audit findings (not in this pass)

### P0 — block release
- [ ] **Home "This page is being built" banner** shows to every visitor.
  Unconditional in `internal/ssr/home.templ:12-24`; also pinned by
  `web/tests/hello.spec.ts:13-17` (remove/gate the banner and update the test).
- [ ] **Lambda concurrency cap = 10** (account quota). Request a Service Quotas
  increase for "Concurrent executions" before launch, then set a reserved floor
  and re-enable `ssrProvisionedConcurrency` (`infra/config.go:43`,
  `infra/runtime.go:104-107`). Also promote the Lambda/DynamoDB throttle alarms
  to paging (`infra/observability.go:138-143`).

### P1 — before / just after launch
- [ ] **Raise Lambda memory** from 128 MB (CPU-starved; bcrypt ~2-3s).
  `infra/config.go:50` — Power-Tune toward 512-1024 MB.
- [ ] **Share one `aws.Config`/HTTP client** across the 5 service constructors
  (cold-start + connection reuse).
- [ ] **Product-images S3 bucket versioning** (retained bucket holds admin
  uploads not in source control). `infra/storage.go:161-184`.
- [ ] **Replace developer-credit footer copy** ("Built with Go… find me on
  GitHub") with customer-facing content. `internal/ssr/layout.templ` (footer
  bottom row).
- [ ] **Add `Permissions-Policy` + COOP** response headers. `infra/site.go:294`.
- [ ] **Tighten CSP `connect-src`/`form-action`** off the global
  `*.s3.amazonaws.com` wildcard. `infra/site.go:296`.

### P2 — accessibility & correctness
- [ ] Order-detail section labels are `<p>`, not headings (h1→h3 skip).
  `internal/ssr/order_detail.templ:48,66,81,92,105`.
- [ ] Checkout saved-address radios lack `<fieldset>`/`<legend>`.
  `internal/ssr/checkout.templ:101-124`.
- [ ] Address-form errors lack per-field `aria-invalid`/`aria-describedby`;
  State is free-text. `internal/ssr/addresses.templ:23-52`.
- [ ] Payment-processing page uses a 3s `<meta refresh>` (re-announces for AT).
  `internal/ssr/checkout.templ:236`.
- [ ] DynamoDB deletion protection covered; consider CloudFront
  `PriceClass_200` for cost. `infra/site.go`.

### P3 — copy & nits
- [ ] "Gift bag" vs "Cart" inconsistency (`internal/ssr/cart.templ:14,27`).
- [ ] "Remove unavailable **sizes**" hardcoded for non-variant items
  (`internal/ssr/cart.templ:87`).
- [ ] Checkout totals hardcode "Free"/"$0.00" instead of the view model
  (`internal/ssr/checkout.templ:180-181`).
- [ ] `formatPrice` has no thousands separator (`internal/ssr/handler.go`).
- [ ] No "continue shopping" link on the placed-order view
  (`internal/ssr/order_detail.templ:30-44`).
- [ ] Destructive actions (clear cart, remove address/card) have no confirm.
- [ ] Password `maxlength="72"` not enforced client-side.
- [ ] Reset-token hash compared with `==` (not constant-time) on the validate
  path (`internal/commerce/dynamo.go:1659`).
- [ ] `tgs_cart` cookie lacks `__Host-` prefix / conditional `Secure`
  (`internal/ssr/cart_state.go`).
- [ ] Responsive `srcset`/WebP for product images when real photography lands.

# Configuration

Environment-variable reference for `thailandgiftshop.com`, grouped by concern. Production fetches admin and Stripe credentials from AWS Secrets Manager at runtime; cart and customer-session signing secrets are resolved into Lambda environment variables at deployment. URL defaults depend on the execution path, as described below. See [`development.md`](development.md) for local usage and [`deployment.md`](deployment.md) for production bootstrap.

> **Never commit** real Stripe keys, admin password hashes, session/cookie secrets, AWS credentials, or generated secret JSON. Keep real values in your shell, a local secret manager, or a CI secret store.

## Server

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | Devserver bind port. |
| `HOST` | `127.0.0.1` | Devserver bind address. |
| `STATIC_DIR` | `web/static` | Static asset directory. |
| `IMAGE_DIR` | `web/product-images` | Product image directory. |
| `APP_ENV` | unset | Set to `production` to enable production detection and fail-closed behavior. |
| `PUBLIC_BASE_URL` | Depends on the execution path; see below. | Base used to build password-reset and order links, and payment success/cancel URLs. |

### Public base URL

When `PUBLIC_BASE_URL` is unset or blank, the defaults are:

| Execution path | Default |
| --- | --- |
| Non-demo checkout, including local Stripe checkout | `https://thailandgiftshop.com`, regardless of environment. |
| Demo checkout and password-reset links (`CATALOG_DEMO_STORE=1`) | `http://127.0.0.1:8080`. |
| Password-reset links outside demo mode | `http://127.0.0.1:8080` outside production; `https://thailandgiftshop.com` when production is detected. |

An explicit `PUBLIC_BASE_URL` overrides these defaults. **Local Stripe checkout requires `PUBLIC_BASE_URL=http://127.0.0.1:8080`** (or your actual local origin); otherwise success/cancel redirects target production. Set a matching value when using a custom local host or port, including in demo mode. The app does not infer this origin from request `Host` headers or the devserver's `HOST`/`PORT` settings.

This variable does not configure Stripe webhook endpoints. Register or forward those separately; see [Stripe webhooks](operations.md#stripe-webhooks).

## Catalog

| Variable | Default | Purpose |
| --- | --- | --- |
| `CATALOG_TABLE_NAME` | unset (empty store) | DynamoDB catalog table. Unset uses an empty catalog store. |
| `CATALOG_DEMO_STORE` | unset | Set to `1` to serve the in-memory demo catalog and skip DynamoDB. |
| `CATALOG_SLUG_INDEX_NAME` | `slug-index` | Product-by-slug GSI. |
| `CATALOG_PUBLIC_INDEX_NAME` | `public-index` | Active/public product GSI. |
| `CATALOG_RECENT_INDEX_NAME` | `recent-index` | Latest active products GSI (home page). |
| `CATALOG_ENTITY_INDEX_NAME` | `entity-index` | Admin product/category list GSI. |
| `PRODUCT_IMAGE_PLACEHOLDER_URL` | `/images/placeholder-product.jpg` | Fallback when a product's `image_url` is empty. |

The defaults match the CDK-provisioned table.

## Commerce

| Variable | Default | Purpose |
| --- | --- | --- |
| `COMMERCE_TABLE_NAME` | unset (in-memory outside production) | DynamoDB table for customers, carts, addresses, and orders. |
| `COMMERCE_CUSTOMER_ORDERS_INDEX_NAME` | `customer-orders-index` | Orders-by-customer GSI. |
| `COMMERCE_ORDERS_INDEX_NAME` | `orders-index` | Orders GSI. |

When `COMMERCE_TABLE_NAME` is unset outside production, the devserver uses an in-memory commerce store.

## Signing secrets

| Variable | Purpose |
| --- | --- |
| `CART_COOKIE_SECRET` | Signs the cookie cart. Set a stable local-only value when exercising cart cookies. |
| `CUSTOMER_SESSION_SECRET` | Signs the customer session, CSRF, login-throttle keys, the orders `?after` cursor tokens, and the guest checkout values (the `__Host-tgs_guest_order` pointer cookie and the guest order `?access=` links). Set the same way locally for customer accounts, sign-in, or checkout. |

Production stores both values in CDK-managed generated Secrets Manager secrets. CloudFormation resolves dynamic references into Lambda environment variables, and handlers read those variables at initialization. `CART_COOKIE_SECRET` is consumed by SSR; `CUSTOMER_SESSION_SECRET` is consumed by both SSR and admin.

Changing a stored secret value alone leaves the deployed signing value unchanged. To apply a rotation:

1. Rotate the value in Secrets Manager.
2. Deploy an actual update to the consuming Lambda environment configuration through CDK/CloudFormation so the dynamic reference is resolved again: SSR for the cart secret, and both SSR and admin for the customer-session secret. A no-op deployment is insufficient; see [CloudFormation dynamic references](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/dynamic-references-secretsmanager.html).
3. Publish a new SSR Lambda version with the updated configuration and move its `live` alias to that version. Published versions retain their configuration; publishing a version without refreshing the environment first also retains the old signing value. See [Lambda versions](https://docs.aws.amazon.com/lambda/latest/dg/configuration-versions.html).
4. Verify that all affected consumers and the serving SSR version have adopted the new configuration before treating invalidation as complete.

> **After the rollout completes**, rotating `CART_COOKIE_SECRET` invalidates existing cart cookies. Rotating `CUSTOMER_SESSION_SECRET` invalidates every customer session, CSRF token, orders cursor link (they self-heal to page 1), guest pointer cookie, and outstanding guest order access link.
>
> **Caveat:** anyone still holding a guest order's `/checkout/confirm?session_id=...` URL can re-mint an access token under the new secret while inside that order's 30-days-from-payment window. Rotation kills leaked *links*, not confirm-URL holders.

## Payments (Stripe)

| Variable | Purpose |
| --- | --- |
| `STRIPE_CREDENTIALS_SECRET_JSON` | Inline Stripe credentials JSON: `{"secret_key":"<sk-test-key>","webhook_signing_secret":"<whsec>"}`. Keep real keys outside the repository. |
| `STRIPE_CREDENTIALS_SECRET_NAME` | Production: name of the Secrets Manager secret fetched when each Lambda initializes its payment provider. Warm providers retain those credentials; they do not periodically refresh them. |
| `PAYMENTS_PROVIDER` | Optional. `stripe` selects Stripe; `fake` is rejected in production. |

With Stripe credentials present, the Stripe-hosted checkout is used; otherwise non-production runs fall back to the in-memory fake provider and production fails closed. Card entry happens only on Stripe's hosted checkout page; the site never sees or stores card numbers.

For local Stripe checkout, explicitly set the return origin along with test credentials:

```sh
export STRIPE_CREDENTIALS_SECRET_JSON='{"secret_key":"<sk-test-key>","webhook_signing_secret":"<whsec>"}'
export PAYMENTS_PROVIDER=stripe
export PUBLIC_BASE_URL=http://127.0.0.1:8080
```

For forwarding real Stripe test-mode webhooks to the local devserver with the Stripe CLI, see [`operations.md`](operations.md#stripe-webhooks).

## Transactional email

| Variable | Purpose |
| --- | --- |
| `EMAIL_SENDER_MODE` | `fake` (local/demo/test capture, never calls SES) or `ses` (production). |
| `EMAIL_FROM_ADDRESS` | Sender address, e.g. `noreply@thailandgiftshop.com`. |
| `EMAIL_SES_REGION` | SES region, `us-east-1`. |

```sh
# Local / demo / test
export EMAIL_SENDER_MODE=fake
export PUBLIC_BASE_URL=http://127.0.0.1:8080

# Production
export EMAIL_SENDER_MODE=ses
export EMAIL_FROM_ADDRESS=noreply@thailandgiftshop.com
export EMAIL_SES_REGION=us-east-1
export PUBLIC_BASE_URL=https://thailandgiftshop.com
```

Local, demo, and test runs use fake capture and never call AWS SES. Production uses AWS SES v2 simple text and HTML transactional mail through the verified `thailandgiftshop.com` identity and must fail closed when sender config is missing, invalid, or set to fake mode.

When `EMAIL_SENDER_MODE=fake` is enabled in the local devserver and `APP_ENV` is not production, browser tests can inspect captured mail with `GET /__test/emails` and clear it with `POST /__test/emails/clear`. These endpoints are devserver-only and are not registered by the SSR or admin Lambda handlers.

**Email behavior:**

- Password reset is customer-only (admin password reset is out of scope). Reset request responses are enumeration-safe, links expire after 30 minutes, reset links are single-use, a successful reset invalidates customer sessions, and raw reset tokens are not stored at rest.
- Order emails use the order email snapshot, `Order.Email`, for both guest and signed-in orders. Payment finalization sends one `order_placed` email when an order moves from `pending_payment` to `paid`; later lifecycle transitions send status emails; admin tracking changes send tracking-update emails.
- Email send failures are best-effort metadata and do not roll back checkout, payment, refund, or admin order mutations.
- Bounce and complaint handling, queues, marketing email, SES templates, attachments, and preference centers are out of scope for this phase.

## Admin

| Variable | Purpose |
| --- | --- |
| `ADMIN_CREDENTIALS_SECRET_JSON` | Inline admin credentials JSON (local override): `{"password_hash":"<bcrypt-hash>","session_secret":"<session-secret>"}`. Honored ahead of the Secrets Manager name. |
| `ADMIN_CREDENTIALS_SECRET_NAME` | Production: name of the Secrets Manager secret (`thailandgiftshop/admin/credentials`) the admin Lambda fetches at runtime. Credentials are cached for five minutes before a subsequent request attempts a refresh; a failed refresh retains the last good credentials. |
| `ADMIN_PASSWORD_HASH` | Local/dev fallback: bcrypt password hash. |
| `ADMIN_SESSION_SECRET` | Local/dev fallback: session secret. |
| `ADMIN_PASSWORD` | Optional input to the secret generator; if omitted, a one-time value is printed. |
| `ADMIN_ORIGIN_HEADER_SECRET` | Leave unset for local direct `/admin` work. In production, CloudFront sends it as `X-TGS-Origin-Secret` and the Lambdas reject requests without the matching header. |

For local admin work, prefer the generated local-only `ADMIN_CREDENTIALS_SECRET_JSON` (see [`development.md`](development.md#running-the-devserver)). If you already have a bcrypt hash and session secret from another local secret manager, the lower-level fallback is:

```sh
export ADMIN_PASSWORD_HASH=<bcrypt-hash>
export ADMIN_SESSION_SECRET=<session-secret>
```

Create the bcrypt hash and session secret outside the repository. Do not commit a plaintext admin password, generated bcrypt hash, or generated session secret. Production loads the `thailandgiftshop/admin/credentials` secret at runtime from the name in `ADMIN_CREDENTIALS_SECRET_NAME` (an inline `ADMIN_CREDENTIALS_SECRET_JSON` blob is still honored ahead of it for local overrides).

During an origin-header rotation, pass the old header value as the no-echo `AdminOriginHeaderPreviousSecret` deploy parameter until CloudFront has propagated the new origin custom header everywhere; leave it blank otherwise. See [`deployment.md`](deployment.md#origin-header-rotation).

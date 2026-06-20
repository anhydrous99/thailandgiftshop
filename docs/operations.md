# Operations

Day-2 runbook for `thailandgiftshop.com`: observability, edge-log invariants, refunds, the WAF, and Stripe webhook handling. For first-time setup and deploys, see [`deployment.md`](deployment.md).

## Observability

The CDK stack provisions the SSR Lambda with its own CloudWatch Logs group at `/aws/lambda/thailandgiftshop-ssr` and 3-month retention. The HTTP API stage writes access logs to `/aws/apigateway/thailandgiftshop-ssr`, also with 3-month retention. Static and product image deployment helper logs are written to `/aws/lambda/thailandgiftshop-static-assets-deployment` with the same 3-month retention policy.

Lambda emits AWS-managed CloudWatch metrics automatically, and the HTTP API default stage has detailed metrics enabled. Lambda X-Ray tracing is active and the Lambda role includes the X-Ray write permissions required to publish trace data. CloudFront, WAF, S3, DynamoDB, API Gateway, Lambda, and app EMF metrics are collected into a CloudWatch dashboard named `ThailandGiftshop-Operations`.

The app emits custom embedded metric format (EMF) events to the `ThailandGiftshop/App` namespace for route timing and cold starts, admin login/origin/catalog/image flows, customer auth, address validation, checkout payment/refund/webhook/order-email outcomes, stock adjustments, catalog and commerce store operations, and order transitions.

Critical alarms publish to the `thailandgiftshop-operations-alarms` SNS topic. Watchlist alarms remain dashboard-only with alarm actions disabled; they cover security noise, capacity warnings, invalid Stripe signatures, customer auth/address/email issues, and admin-only workflow failures. Email subscriptions require confirmation before critical notifications flow.

## Edge-log invariants

Three edge-log invariants protect the guest order access tokens, which ride in URL query strings:

1. The API Gateway access-log **format string** (in `infra/runtime.go`) emits `$context.routeKey` and contains no raw path or query field. It must never gain `$context.path`, `$context.requestPath`, or any raw-query field.
2. CloudFront standard logging is off and must stay off (or strip query strings) for the same reason.
3. WAF sampled requests are disabled because sampled-request inspection can expose request URLs.

Application code likewise never logs request paths or query strings (`logAccountError`/`logHandlerError`/`logHandlerWarn` carry no query data). Preserve this invariant in any change that touches logging.

## AWS WAF

CloudFront has an AWS WAF web ACL with rate limits for:

- `POST /admin/login`
- `/admin*`
- customer-auth `POST /account/sign-in` and `POST /account/sign-up`
- `POST /checkout/place-order`

Guest checkout removes the account gate from place-order, so the edge throttle takes its place; the rule bounds request count, not units reserved. Production traffic, including public SSR checks, must use `https://thailandgiftshop.com/` so CloudFront can apply WAF rules and inject the origin header accepted by the Lambdas.

## Stripe webhooks

The app serves webhooks at `/webhooks/stripe`, never `/stripe/events`. Invalid signatures return `400`. The dashboard webhook endpoint is only for deployed environments (see [`deployment.md`](deployment.md#stripe-credentials-and-webhook)).

### Local webhook forwarding

To exercise real Stripe (test mode) webhooks against the local devserver, forward them with the Stripe CLI rather than registering a tunnel URL (for example ngrok) as a webhook endpoint in the Stripe dashboard — ephemeral tunnel URLs go stale and leave a failing dashboard endpoint behind that retries and emails for days. Install the CLI (`brew install stripe/stripe-cli/stripe`), run `stripe login` once, then forward to the app's webhook path:

```sh
stripe listen \
  --forward-to http://127.0.0.1:8080/webhooks/stripe \
  --events checkout.session.completed,checkout.session.async_payment_succeeded,checkout.session.async_payment_failed,checkout.session.expired,refund.created,refund.updated,refund.failed
```

`stripe listen` prints `Your webhook signing secret is whsec_...`; copy that value into the `webhook_signing_secret` field of `STRIPE_CREDENTIALS_SECRET_JSON` and restart the devserver, otherwise signature verification rejects every delivery with `400`. The CLI's signing secret is distinct from any dashboard endpoint's secret, and `stripe listen` stays in test mode unless `--live` is passed.

### Stranded guest pending orders

Stranded guest pending orders release their stock via the guaranteed 30-minute `checkout.session.expired` webhook in production; signed checkout-cancel returns release their own reservation before the cart is normalized.

## Refunds

Refunds are issued automatically from the admin order page. The Refund action returns the full payment to the shopper's original payment method through Stripe (paid orders also return their reserved stock; shipped/delivered orders do not), and the order settles to `refunded` via the `refund.*` webhooks or the admin page's reconcile-on-render. The admin Refund action always refunds the full amount; partial refunds are out of scope.

Payments captured for orders that already reached a terminal status (`paid_after_terminal`) are also refunded automatically and unattended. The existing `StripeWebhook` alarm's meaning for that outcome therefore changes from "go issue a refund" to **"a refund was already issued unattended — verify in Stripe that it is legitimate"** (not a pay-then-expire abuse pattern, which burns non-returnable processing fees and can serve as card-testing cover; the WAF rate limit in front of checkout bounds it) and that it settles. The dashboard's auto-issued refund series is the volume watch point.

The Stripe dashboard remains the manual fallback only for:

- orders without a payment-intent ID,
- refunds that fail again after a retry,
- failed terminal-order auto-refunds (those orders have no in-app retry), and
- the already-fully-refunded reconciliation case that appears when a refund is retried after Stripe's ~24h idempotency-key window.

Refunds issued directly in the Stripe dashboard carry no order metadata and do not update order status.

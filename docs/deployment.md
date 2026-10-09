# Deployment

Bootstrap, secrets, and deploy procedures for `thailandgiftshop.com`. Production deploys only to `us-east-1`; deploys intentionally fail elsewhere because the CloudFront certificate lives in the same stack. The deployed stack ID is `ThailandGiftshopStack`.

For day-2 runbook topics (observability, refunds, webhook reconciliation), see [`operations.md`](operations.md). For environment variables, see [`configuration.md`](configuration.md).

## Bootstrap CDK

Before deploying to an AWS account/region for the first time, bootstrap CDK in `us-east-1`:

```sh
cd infra
npx cdk bootstrap aws://ACCOUNT_ID/us-east-1
```

Replace `ACCOUNT_ID` as needed. Production deploys intentionally fail outside `us-east-1`.

## Admin credentials secret

Bootstrap (and later rotate) the production admin credentials — a manually managed Secrets Manager JSON secret named `thailandgiftshop/admin/credentials` — in one step. The same command creates the secret on first run and rotates it afterwards; just give it the password:

```sh
printf '%s' 'YOUR_ADMIN_PASSWORD' | \
  go run ./scripts/generate-admin-secret.go -push -password-stdin -yes
```

`-region` defaults to `us-east-1` and `-secret-id` to `thailandgiftshop/admin/credentials`. Supply the password via stdin (`-password-stdin`) or the `ADMIN_PASSWORD` env var rather than `-password`, which is visible in shell history. Omit both and a strong password is generated and printed once as `ADMIN_PASSWORD=...`. Drop `-yes` to confirm interactively, or add `-dry-run` to preview the action without writing.

> **Each rotation regenerates the session secret**, so it logs out all active admin sessions and invalidates outstanding CSRF tokens.

The secret JSON shape:

```json
{"password_hash": "<bcrypt-hash>", "session_secret": "<session-secret>"}
```

The CDK stack passes the secret's name to the admin Lambda as `ADMIN_CREDENTIALS_SECRET_NAME` and grants it read access; the Lambda fetches the JSON from Secrets Manager at runtime and caches it briefly, so a rotation takes effect within a few minutes without a redeploy. (It is not injected via a deploy-time `ADMIN_CREDENTIALS_SECRET_JSON` dynamic reference, which would require a redeploy to pick up a rotation.)

The stack also creates a DynamoDB table for admin login attempts and passes its generated name to the admin Lambda as `ADMIN_LOGIN_ATTEMPTS_TABLE_NAME`. Failed admin logins are tracked per client, locked after 8 failures in 15 minutes, and receive a generic `429` with `Retry-After` during the 15-minute lockout.

### Manual fallback

Write the JSON to a file with `-out` (optional) and push it with the AWS CLI yourself:

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

## Stripe credentials and webhook

Before enabling checkout, bootstrap the Stripe credentials as a manually managed Secrets Manager JSON secret named `thailandgiftshop/stripe/credentials` in `us-east-1`. The webhook signing secret is not known until the endpoint is registered, so start with a placeholder value:

```sh
aws secretsmanager create-secret \
  --region us-east-1 \
  --name thailandgiftshop/stripe/credentials \
  --secret-string '{"secret_key":"<sk-live-or-test-key>","webhook_signing_secret":"<whsec-placeholder>"}'
```

After the first deploy, register the webhook endpoint in the Stripe dashboard as `https://thailandgiftshop.com/webhooks/stripe` — use the apex domain, because the `www` host issues a `308` redirect and Stripe does not follow redirects — subscribed to **seven** events:

- `checkout.session.completed`
- `checkout.session.async_payment_succeeded`
- `checkout.session.async_payment_failed`
- `checkout.session.expired`
- `refund.created`
- `refund.updated`
- `refund.failed`

Existing deployments must edit the webhook endpoint in the Stripe dashboard to add the three `refund.*` events before relying on automatic refund settlement; until then, settlement is healed by the admin order page's reconcile when the order is opened. Then write the real `whsec_` value into the same secret:

```sh
aws secretsmanager put-secret-value \
  --region us-east-1 \
  --secret-id thailandgiftshop/stripe/credentials \
  --secret-string '{"secret_key":"<sk-live-or-test-key>","webhook_signing_secret":"<whsec>"}'
```

The CDK stack passes the secret name to both the SSR and admin Lambdas as `STRIPE_CREDENTIALS_SECRET_NAME` and grants `secretsmanager:GetSecretValue` on that name. Deployments do not resolve the secret value, so a missing Stripe secret will not roll back CloudFormation; production SSR and admin startup fail closed until the secret exists and contains valid JSON.

## Transactional email (SES)

Before enabling production email delivery, make sure AWS SES is ready in `us-east-1`. The CDK stack creates a Route53-backed SES identity for `thailandgiftshop.com` with Easy DKIM and grants the SSR and admin Lambdas `ses:SendEmail` for transactional mail. SES identity verification, DKIM DNS, and SES production access must all be complete before production deliveries are expected to leave the account. Accounts still in the SES sandbox can only send to verified recipients.

Production sender config should be:

```sh
EMAIL_SENDER_MODE=ses
EMAIL_FROM_ADDRESS=noreply@thailandgiftshop.com
EMAIL_SES_REGION=us-east-1
PUBLIC_BASE_URL=https://thailandgiftshop.com
```

Do not run production with `EMAIL_SENDER_MODE=fake`. Startup and config validation should reject fake mode and incomplete SES sender settings in production.

## Build, synth, and deploy

Build frontend assets before synthesizing or deploying so `web/static/` contains the generated CSS used by CDK. Run Go and browser tests before deploying email, reset, or checkout behavior:

```sh
go test $(sh scripts/go-packages.sh)
npm --prefix web test
npm --prefix web run build
AWS_REGION=us-east-1 CDK_DEFAULT_REGION=us-east-1 npm --prefix infra run synth
```

Deploy locally:

```sh
cd infra
AWS_REGION=us-east-1 npx cdk deploy ThailandGiftshopStack \
  --parameters HostedZoneId=ROUTE53_HOSTED_ZONE_ID \
  --parameters AlarmNotificationEmail=ops@example.com
```

Replace `ROUTE53_HOSTED_ZONE_ID` with the public Route 53 hosted zone ID for `thailandgiftshop.com` and replace `ops@example.com` with the operations email address that should receive critical alarm notifications.

### Stack outputs

| Output | Meaning |
| --- | --- |
| `SiteUrl` | `https://thailandgiftshop.com`. |
| `SiteDistributionDomainName` | Underlying CloudFront distribution domain. |
| `SsrHttpApiUrl` | Private API Gateway origin behind CloudFront; production requests to it are expected to fail without the CloudFront-injected origin header. |
| `CatalogTableName`, `CatalogTableArn` | Catalog DynamoDB table. |
| `ProductImagesBucketName`, `ProductImagesBaseUrl` | Product image bucket. |

### Origin-header rotation

For an origin-header rotation only, add `--parameters AdminOriginHeaderPreviousSecret=OLD_HEADER_VALUE`, deploy the new generated secret, wait for CloudFront propagation, then deploy again with the parameter omitted or blank.

### Catalog backfill

After changing seeded image URLs, product creation dates, or catalog indexes, backfill existing catalog rows so DynamoDB points at `/images/products/...` and populates the latest-products and admin entity-index access patterns. Use `cmd/seedcatalog` only to insert rows that are currently missing:

```sh
AWS_REGION=us-east-1 go run ./cmd/seedcatalog
```

### Decommissioning an old regional stack

After the `us-east-1` site has been deployed and verified, manually destroy the old regional stack if it is still present:

```sh
cd infra
AWS_REGION=us-east-2 npx cdk destroy ThailandGiftshopStack
```

Retained resources, such as retained buckets or log groups, may remain after stack destruction and should be reviewed before manual deletion.

## CI/CD (GitHub Actions)

CI runs on pull requests and pushes to `main`:

- Go tests for tracked Go package directories
- Frontend asset build and browser tests
- `npm audit --omit=dev --audit-level=high` for web and infra production dependencies
- `gofmt` check
- `go vet` for tracked Go package directories
- `npx cdk synth`

The CI workflow is path-aware and runs for Go, infrastructure, static asset, or workflow changes. The deployment workflow is triggered after the `CI` workflow completes successfully on `main`, and can also be run manually with workflow dispatch (which must run from `main`). Both triggers enter a deploy job targeting the `production` GitHub environment; its required-reviewer gate must be approved before any job steps run. After approval and validation, the job runs `cdk diff` then `cdk deploy ThailandGiftshopStack --require-approval never`. That CDK flag disables CDK's interactive confirmation, not GitHub's environment approval gate.

### Production approval gate

The approval gate is GitHub repository configuration under **Settings → Environments → production**, not a resource created by CDK or the workflow YAML. The workflow's existing `environment: production` declaration binds the deploy job to these settings:

| Setting | Configuration |
| --- | --- |
| Required reviewer | `@anhydrous99` |
| Administrator bypass | Disabled |
| Prevent self-review | Disabled, so the designated reviewer can approve deployments from their own pushes |

To approve a waiting deployment, open **Actions → Deploy → the waiting run → Review deployments**, verify the intended source commit, select `production`, and choose **Approve and deploy**. Reject the deployment if its source or validation is not acceptable. Configure another eligible reviewer before preventing self-review, otherwise the sole reviewer cannot approve their own deployment runs.

Environment protection is effective as soon as the repository settings are saved and applies to both automatic and manually dispatched deploy jobs. It does not gate local `cdk deploy` commands made with direct AWS credentials; those remain subject to AWS permissions and operator procedures.

Configure these GitHub settings before the first deployment:

| Setting | Type | Value |
| --- | --- | --- |
| `AWS_ROLE_TO_ASSUME` | Secret | IAM role ARN trusting GitHub's OIDC provider. |
| `AWS_REGION` | Variable | `us-east-1`. |
| `ROUTE53_HOSTED_ZONE_ID` | Variable | Public Route 53 hosted zone ID for `thailandgiftshop.com`. |
| `ALARM_NOTIFICATION_EMAIL` | Variable | Operations email subscribed to critical alarms. |

The deployment workflow uses GitHub OIDC through `aws-actions/configure-aws-credentials`, so long-lived AWS access keys are not required. The AWS role used by `AWS_ROLE_TO_ASSUME` must trust GitHub's OIDC provider and should be scoped to this repository's `production` GitHub environment, for example `repo:anhydrous99/thailandgiftshop:environment:production`.

# Server payment configuration

New purchases are disabled unless `POINTS_NEW_ORDERS_ENABLED=true`. This
switch only blocks new checkouts. Keep a provider's `*_CONFIGURED` flag and
server credentials available while that provider has historical orders, so
signed notifications, customer status checks, and inbox recovery can continue.

Points billing must also be enabled and migrated. The application rejects new
orders unless the requested package is active and at least one model in the
customer's group has an active published price and an enabled upstream channel
that supports the current OpenAI text relay path. Payment credentials are not
read from client requests, stored in the database, or returned from APIs.

WeChat Pay reads `WECHAT_PAY_CONFIGURED`, `WECHAT_PAY_MERCHANT_ID`,
`WECHAT_PAY_APP_ID`, `WECHAT_PAY_MERCHANT_SERIAL`,
`WECHAT_PAY_PLATFORM_SERIAL`, `WECHAT_PAY_PRIVATE_KEY_FILE`,
`WECHAT_PAY_PLATFORM_KEY_FILE`, `WECHAT_PAY_API_V3_KEY`, and
`WECHAT_PAY_NOTIFY_URL`; the independent refund callback uses
`WECHAT_PAY_REFUND_NOTIFY_URL`. Alipay reads `ALIPAY_CONFIGURED`, `ALIPAY_APP_ID`,
`ALIPAY_SELLER_ID`, `ALIPAY_PRIVATE_KEY_FILE`, `ALIPAY_PUBLIC_KEY_FILE`,
`ALIPAY_NOTIFY_URL`, and optional `ALIPAY_RETURN_URL`.

Key files must be readable by the gateway process and must not be placed under
the public website root. The adapters use fixed official provider endpoints;
no endpoint or callback URL is accepted from a customer. The enabled lanes are
WeChat Native and Alipay desktop page-pay. H5, JSAPI, Alipay mobile web, and
real merchant acceptance remain
separate work. Refund HTTP routes are available behind
`POINTS_REFUND_OPERATIONS_ENABLED`; keep this switch false until merchant
configuration, a published package, an eligible priced model/channel, and the
provider's refund contract have all been reviewed. No real merchant refund has
been performed as part of local acceptance.

Automatic refund recovery is disabled by default. Set
`POINTS_REFUND_RECOVERY_ENABLED=true` to start the internal background worker.
It processes approved refund operations and durable verified inbox records
with persisted keyset cursors, same-refund-number retries, provider-specific
backoff, and a fixed maximum of five provider calls per batch. It does not
enable new refunds or accept refund requests. Keep provider configuration
available for historical orders even when new purchases are paused. On
shutdown, the worker is cancelled and awaited before the database is closed.

## Implemented HTTP entry points

These routes require the current points schema (version 13), applied explicitly with
`--migrate-points`.
Back up and stop the production service before an approved migration; startup
does not apply the points migration automatically. Legacy orders with missing
merchant/application snapshots remain unchanged and are quarantined if new
payment evidence cannot be matched safely.

- `GET /api/payments/packages`: active package terms.
- `GET /api/payments/csrf`: session-bound payment operation token, available even
  while points billing is paused.
- `POST /api/payments/orders`: authenticated customer, same-origin CSRF token and
  `Idempotency-Key`; body contains only `package_id` and `channel`.
- `GET /api/payments/orders` and `GET /api/payments/orders/:key`: current user's
  orders. Administrator status does not bypass ownership on these routes.
- `POST /api/payments/orders/:key/query` and `/close`: current user plus CSRF;
  unknown provider results preserve the order for later verification.
- `POST /api/payments/notify/wechat` and `/alipay`: provider signature verification,
  durable notification inbox and atomic credit, independent of new-order and
  points billing switches.
- `POST /api/admin/points/payments/packages`: current administrator plus CSRF,
  immutable package publication with business key and audit.
- `POST /api/admin/points/payments/recover?after_id=0&limit=100`: current
  administrator plus CSRF; process already verified, durable inbox events.
- `GET /api/admin/orders`, `/api/admin/orders/:key` and `GET /api/admin/audit`:
  current administrator; paginated, filtered order/audit review with sensitive
  provider and credential fields omitted.

Recovery reports scanned, attempted, processed, quarantined, failed, skipped,
`next_after_id` and `has_more`. Continue with the returned cursor while
`has_more=true`. A completed scan is not proof that failed/skipped events were
resolved: retain their records, fix the cause and start another scan from zero.
The HTTP payment recovery route only replays verified inbox records and makes
no provider network calls. Refund worker operations are internal and preserve
the exact refund number, amount, original transaction and merchant snapshot.
Unknown outcomes keep the original points frozen; automatic recovery never
creates a new refund number or releases a hold based on an absent query result.

## Refund request and review routes

Customer refund routes require the current login session and points schema.
Historical reads remain available while new refund operations are paused:

- `GET /api/payments/orders/:key/refund-quote`: a best-effort preview; the
  request transaction rechecks the order, previous refunds, purchase and bonus
  lots, and available balance.
- `POST /api/payments/orders/:key/refund-requests`: same-origin CSRF and a
  customer idempotency key; only integer `amount_fen`, reason and
  `idempotency_key` are accepted. Refund number, provider identity and frozen
  lots are selected by the server.
- `GET /api/payments/orders/:key/refunds` and
  `GET /api/payments/refunds/:key`: current-customer progress, with strict
  ownership checks.
- `POST /api/payments/refunds/notify/wechat`: independent WeChat refund
  notification URL; verifies signature and encrypted resource, persists the
  verified inbox before acknowledging, then attempts inbox processing. A
  processing error leaves the durable event available for recovery.
- `GET /api/admin/refunds` and `GET /api/admin/refunds/:key`: current
  `refund.read` capability, paginated safe projections.
- `POST /api/admin/refunds/:key/approve` and `/reject`: current
  `refund.review`; a scoped password recheck ticket is consumed in the same
  transaction as the decision and audit. Approval moves to `review_approved`
  and does not call a provider.
- `POST /api/admin/refunds/:key/submit`: current `refund.submit`; a separate
  ticket commits the submit intent before provider I/O. Replays of that exact
  business decision resume the durable operation without a second ticket.
- `POST /api/admin/refunds/:key/reconcile`: current `refund.reconcile`; checks
  existing provider state and preserves frozen points for unknown outcomes.
- `POST /api/refund-auth/step-up`: password recheck for action-bound approve,
  reject or submit tickets. Tickets are short-lived, one-use and session-bound.

Role 100 receives platform refund capabilities; role 10 has no refund
capability by default. Delegated capabilities are checked against current
database identity on every request. Refund action tickets are never exposed as
a standalone consume operation. The `review_approved` state is deliberately
not eligible for recovery dispatch; only a separate submit action can create
the provider submission intent. Older `approved` records retain their prior
recovery behavior.

Provider reconciliation is available to root and users granted the independent
`reconciliation.read`, `reconciliation.import`, and `reconciliation.note`
capabilities. Role 10 does not receive those permissions by default, and refund
capabilities do not grant reconciliation access. Import requires a configured
payment provider and an independent source encryption key. It remains available
while new purchases and points billing are paused; existing batch metadata can
be read without source encryption being configured. Imports are limited to 30
per user per hour and serialized within one gateway process.

Set `POINTS_RECONCILIATION_SOURCE_KEY_ID` and
`POINTS_RECONCILIATION_SOURCE_KEY_BASE64` to enable encrypted source retention.
The latter must be standard padded Base64 decoding to exactly 32 bytes. Use a
dedicated stable server secret, separate from login/session and payment keys;
do not generate it at startup or expose it through environment/status APIs.
Missing or malformed configuration disables import without disabling metadata
reads. There is no plaintext fallback or raw-source export route. Historical
key retention and rotation operations still need an operational procedure.

The admin API is rooted at `/api/admin/reconciliation`: status, paginated batch,
row, difference, action and import-attempt reads; `POST /import` accepts only
provider and bill date; and `POST /differences/:id/actions` appends an operator
note/query/reference. All routes require the current session and current
capability checks; writes additionally require same-origin CSRF. Provider
identity and URLs come from server configuration. WeChat ALL produces parsed
rows; Alipay is retained as an `unsupported_format` opaque batch. Neither
imports nor notes modify point balances or mark findings resolved.

Points refunds and reconciliation are currently restricted to the verified
single-instance SQLite configuration; other database backends are not enabled.

## Refund authorization foundation

Refund authorization tables use the normal database startup migration and are
separate from the explicitly applied points/payment schema. Refund actions stay
closed unless `POINTS_REFUND_OPERATIONS_ENABLED=true`. With that switch off,
the exact role-100 root account may prepare or revoke delegated refund
capabilities after local-password step-up, but no refund action ticket can be
issued. Role 10 receives no financial capability by default. Only root can
manage grants; delegated grants are limited to `refund.read`, `refund.review`,
`refund.submit`, `refund.reconcile`, and `refund.audit`.

Sensitive verification is a short-lived one-time ticket bound to the actor,
current login session, action and exact request scope. The database stores only
its digest. Each account needs an existing local password; this is password
reverification, not MFA. OAuth-only accounts without a local password cannot
perform these sensitive actions. The current email/reset flow does not persist
a durable verified-email marker, so it is not used to enable a local password
for an OAuth-only account. No recovery shortcut is provided here. Step-up
issuance is limited to five attempts per user per minute, independently of
the IP-based limit.

Capability grants are bound to the recipient's current credential identity and
auth epoch. Password, email, OAuth identity or account-state changes invalidate
the grant until root explicitly grants it again. Grant and revoke records keep
actor, target, capability set, reason and time; they never contain passwords,
step-up tickets, provider payloads or credential fingerprints.

The authorization foundation exposes `GET /api/refund-auth/self` and `/csrf`,
`POST /api/refund-auth/step-up` for root grant/revoke and scoped refund-action
re-verification, `GET /api/admin/refund-auth/users/:id/grants`, and
`POST /api/admin/refund-auth/grants`. Refund action tickets are consumed only
inside the matching refund business transaction.

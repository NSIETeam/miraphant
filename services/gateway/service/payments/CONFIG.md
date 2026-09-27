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
`WECHAT_PAY_NOTIFY_URL`. Alipay reads `ALIPAY_CONFIGURED`, `ALIPAY_APP_ID`,
`ALIPAY_SELLER_ID`, `ALIPAY_PRIVATE_KEY_FILE`, `ALIPAY_PUBLIC_KEY_FILE`,
`ALIPAY_NOTIFY_URL`, and optional `ALIPAY_RETURN_URL`.

Key files must be readable by the gateway process and must not be placed under
the public website root. The adapters use fixed official provider endpoints;
no endpoint or callback URL is accepted from a customer. The enabled lanes are
WeChat Native and Alipay desktop page-pay. H5, JSAPI, Alipay mobile web,
refunds, reconciliation, and real merchant acceptance remain separate work.

## Implemented HTTP entry points

These routes require schema version 8, applied explicitly with `--migrate-points`.
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

Recovery reports scanned, attempted, processed, quarantined, failed, skipped,
`next_after_id` and `has_more`. Continue with the returned cursor while
`has_more=true`. A completed scan is not proof that failed/skipped events were
resolved: retain their records, fix the cause and start another scan from zero.
Recovery performs no provider network calls and does not manufacture payment
evidence. Administrator order search, provider reconciliation, payment status
management and customer checkout pages are subsequent integration work.

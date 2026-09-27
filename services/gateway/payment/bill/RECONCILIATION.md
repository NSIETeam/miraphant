# Reconciliation storage and matching boundary

This document describes the SQLite reconciliation store and its authorized
admin API. It does not enable automatic point changes or declare a batch
reconciled merely because it was downloaded and imported.

## Batch evidence

Each import is keyed by provider, configured merchant and app, bill date,
statement type, format version, and SHA-256 of the exact source bytes. Replaying
that identity returns the original batch. A different source hash for the same
scope/date/format creates a new import version; earlier batches and findings
remain unchanged. Batch, normalized rows, and initial findings commit in one
SQLite transaction.

The original bill is encrypted with AES-256-GCM using the explicit source key
ID and 32-byte server key configured by `POINTS_RECONCILIATION_SOURCE_KEY_ID`
and `POINTS_RECONCILIATION_SOURCE_KEY_BASE64`. The batch identity is
authenticated as additional data. The key is separate from login/session
secrets and is never generated at startup. An absent or invalid key prevents
imports. There is no plaintext fallback. Metadata, list, detail and HTTP
responses omit ciphertext. No raw-file export route is provided. Historical
source decryption after key rotation, retention, and deletion still need an
operational policy.

Normalized line records retain source line number and digest, provider IDs,
status, currency, and separate gross, settlement, discount, refund, fee, and
optional net amounts in integer fen. Payer IDs, product descriptions, and other
free-form provider text are not copied into normalized records. Duplicate lines
remain individual source rows and receive a duplicate finding.

## Matching behavior

Matching requires exact provider, merchant, app, order or refund identity,
currency, integer amount, and provider status. Payment amount comparison uses
the order gross amount; settlement, fees, discounts, and net settlement are
never substituted for it. Rows from another app or merchant remain evidence in
the batch and are not attributed to a customer order.

For payments, a local match additionally requires the transaction ownership
record and the exact verified, processed payment event named by the order's
`PaidEventKey`. A `missing_provider` payment finding is produced only from that
same exact event and its normalized `provider_occurred_at` falling within the
statement's timezone and bill date. Order creation/update times and isolated,
quarantined, or unverified events do not determine a bill date.

The current refund model has no immutable provider refund application time.
Refund inbox occurrence time usually records a later query or completion, so it
is not used to assert that a refund is missing from a particular daily bill.
Refund lines are matched by the merchant refund key and provider refund ID; if
both are present, they must resolve to the same local refund. A WeChat
`PROCESSING` line is retained as informational historical evidence. It is not
automatically considered an error if a later notification reports success,
because the exported daily bill is a historical snapshot.

The classifications are fixed findings: `matched`, `other_scope`,
`missing_local`, `missing_provider`, `duplicate`, `identity_mismatch`,
`amount_mismatch`, `state_mismatch`, and `historical_processing`. Importing or
reviewing a batch never credits, debits, refunds, settles, or releases points.
Operator notes and query/reference actions are separate append-only records and
do not mark a discrepancy resolved. Any balance change must go through the
existing verified payment/refund workflow.

For a WeChat import, candidate local orders are currently scanned with one
verified-event lookup per order inside the SQLite import transaction. This is
not yet indexed or paged by provider event date; do not run imports concurrently
or treat this first node as suitable for unbounded historical backfills. A
later service should narrow that query using the verified event's persisted
normalized date without using order create/update timestamps.

## Authorized admin API

The routes are mounted below `/api/admin/reconciliation`. They use the actual
login session and current database capability check, independently of the
legacy role-10 administrator gate and the points billing switch. Root role 100
receives `reconciliation.read`, `reconciliation.import`, and
`reconciliation.note`. Other users need those exact delegated capabilities;
refund permissions do not imply reconciliation access.

- `GET /status`, `/batches`, `/batches/:key`, `/batches/:key/rows`,
  `/batches/:key/differences`, `/differences/:id/actions`, and
  `/import-attempts` require `reconciliation.read`.
- `POST /import` requires `reconciliation.import`, same-origin CSRF, an 8 KiB
  request limit, and a 30-per-hour per-user limit. The body accepts only
  `provider` and `bill_date`. The URL, merchant, app, bill rows and verification
  flags always come from the server's configured adapter. A single process
  import gate and a 90-second request context bound work.
- `POST /differences/:id/actions` requires `reconciliation.note`, CSRF and the
  same bounded request size. It appends a note/query/reference record only;
  replaying an action key with different actor, finding, action, reason or
  business reference conflicts.

Accepted import attempts first persist an actor/provider/date `started` audit
row before provider I/O. Successful import and its final audit row commit in
the same transaction as the batch and findings. Failed attempts append a
sanitized fixed error code using a separate bounded database context. A failed
database write never returns an import success response. Provider errors,
download URLs, keys, and raw source bytes are not returned or stored in the
attempt audit. Imported Alipay files are explicitly `unsupported_format` until
an authoritative parser is available.

## Supported formats

WeChat `ALL` trade bills use the verified `wechat-all-27-column-v1` parser and
the provider's signed metadata plus raw-file SHA-1 check. Bill dates use
`Asia/Shanghai`, as defined by that provider adapter.

Alipay trade downloads currently enter as `unsupported_format` opaque batches.
The original downloaded bytes and SHA-256 can be preserved, but no rows are
parsed and no result is described as reconciled. A current ordinary merchant
trade-file format/version is required before enabling Alipay row matching.

An absent bill, failed download, invalid signature/hash, or incomplete statement
is an error, not a zero-row day. This layer is explicitly limited to trade bill
matching; settlement account statements and fee/settlement reconciliation are
separate work.

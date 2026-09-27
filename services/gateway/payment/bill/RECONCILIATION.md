# Reconciliation storage and matching boundary

This document describes the first SQLite reconciliation storage layer. It does
not enable bill downloads, imports through HTTP, automatic point changes, or
merchant operations.

## Batch evidence

Each import is keyed by provider, configured merchant and app, bill date,
statement type, format version, and SHA-256 of the exact source bytes. Replaying
that identity returns the original batch. A different source hash for the same
scope/date/format creates a new import version; earlier batches and findings
remain unchanged. Batch, normalized rows, and initial findings commit in one
SQLite transaction.

The original bill is encrypted with AES-256-GCM using a caller-supplied,
explicit source key ID and 32-byte server key. The batch identity is
authenticated as additional data. The key must be separate from login/session
secrets and must not be generated at startup. An absent or invalid key prevents
import. There is no plaintext fallback. This node does not yet wire a runtime
configuration loader or HTTP import route. Metadata and list queries omit
ciphertext. Internal decryption verifies the key ID and source SHA-256; no
public raw-file access route is part of this node. Key rotation, authorized
raw-file access, retention, and deletion policy still require operational
design.

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

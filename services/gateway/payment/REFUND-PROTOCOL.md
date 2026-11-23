# Refund protocol adapter contract

This document describes the protocol-only adapter checkpoint. It does not enable refunds, call live merchant accounts, persist evidence, or release ledger holds. Service orchestration must retain the refund freeze for `accepted`, `unknown`, and `abnormal` outcomes.

## Shared adapter input and outcomes

`payment.RefundRequest` is constructed from immutable server-side refund and paid-order records. `RefundKey`, the provider-specific stable refund number, original merchant order number, provider transaction ID, configured merchant/app identity, integer fen amounts, currency, and reason are bound together. Clients cannot supply these values to a provider adapter.

`PriorRefundedFen` is the amount of earlier *successful* refunds for this original order, calculated by the trusted refund service from completed refund records and checked against the frozen order amount. It is never accepted from a customer, browser, or UI. The service permits only one nonterminal refund per original order, so the amount used for a new request is a stable snapshot. If the local completed amount and verified provider cumulative amount disagree, the adapter returns `unknown` with an error; it does not adjust the requested amount.

Adapter outcomes are:

- `accepted`: provider accepted or may have accepted the request; keep the ledger freeze.
- `succeeded`: evidence proves this exact refund completed and matches the frozen order/refund amounts.
- `definite_failed`: verified provider state proves the refund closed/failed. Current WeChat query/notification support maps `CLOSED` to this outcome. Alipay refund query currently exposes success but no documented terminal failure state, so it stays `unknown` until separately reconciled.
- `unknown`: timeout, HTTP/API error, signature or identity mismatch, missing field, ambiguous response, or unknown status. Keep funds frozen.
- `abnormal`: a verified WeChat `ABNORMAL` state. Keep funds frozen for manual handling.

Network, HTTP, verification, or malformed-response errors return an unknown outcome and an error. The caller must not interpret the error alone as proof that the channel did not process an earlier retry. Untrusted responses do not return partially normalized identities.

## WeChat Pay API v3

The implementation targets the Native domestic refund API:

- Apply: `POST /v3/refund/domestic/refunds`
- Query: `GET /v3/refund/domestic/refunds/{out_refund_no}`
- Callback: the encrypted refund-result notification envelope.

See the [official Native refund application](https://pay.wechatpay.cn/doc/v3/merchant/4012791883), [refund query](https://pay.wechatpay.cn/doc/v3/merchant/4012791884), and [refund result notification](https://pay.wechatpay.cn/doc/v3/merchant/4012791886) documentation.

Apply sends the frozen provider transaction ID, stable `out_refund_no`, integer `amount.refund`, original `amount.total`, `CNY`, and the separately configured `RefundNotifyURL` when present. The API requires either `transaction_id` or `out_trade_no`; this adapter sends `transaction_id` and validates the response's original order number against the frozen `OrderKey`. `out_refund_no` is limited to 64 bytes and uses the provider's allowed ASCII characters. `reason` is limited to 80 UTF-8 bytes. The payment notification URL is never reused for refund notifications.

A successful signed apply response means only that the request was accepted. This adapter always normalizes an apply response to `accepted`; it does not finalize from that response. Query requires and matches `refund_id`, `out_refund_no`, `transaction_id`, `out_trade_no`, `status`, and `amount.refund/total/currency`. Only the configured platform key/serial and the request's frozen merchant/app context bind identity: the documented query response does not return `mchid` or `appid`.

Refund notifications have top-level `resource_type: "encrypt-resource"`, `event_type`, `id`, `summary`, and an encrypted `resource` with `original_type: "refund"`. The adapter verifies the raw-body WeChat signature and timestamp first, decrypts AES-256-GCM with the APIv3 key, requires the actual payload `mchid`, `out_trade_no`, `transaction_id`, `out_refund_no`, `refund_id`, `refund_status`, and `amount.total/refund/currency`, then binds app identity to the configured merchant context. It rejects a missing `refund_id`. Notification event types must agree with their payload status: `REFUND.SUCCESS`/`SUCCESS`, `REFUND.CLOSED`/`CLOSED`, or `REFUND.ABNORMAL`/`ABNORMAL`.

For verified query/callback states, `SUCCESS` means succeeded, `CLOSED` means definite failure, `PROCESSING` means accepted, and `ABNORMAL` means manual review. Unknown status strings fail closed. An unsigned non-2xx response, 5xx, timeout, redirect, or other transport error remains unknown; this adapter does not interpret an unverified error body as a final rejection.

## Alipay

The implementation targets the signed OpenAPI methods `alipay.trade.refund` and `alipay.trade.fastpay.refund.query`; the API endpoint remains fixed to the configured Alipay gateway and redirects are disabled.

See the [official refund API](https://aipay.alipay.com/docs/vibe-pay/ai-web-app-payment-qianyi/api-list/alipay-trade-refund.html) and [official refund query API](https://aipay.alipay.com/docs/vibe-pay/ai-web-app-payment-qianyi/api-list/alipay-trade-fastpay-refund-query.html).

Apply sends frozen `out_trade_no`, `trade_no`, the current integer-fen amount formatted as a decimal string, and stable `out_request_no`. Signed response `out_trade_no` and `trade_no` must match the frozen order. The response `refund_fee` is the transaction's **cumulative successful refund amount**, not this request's amount. For `fund_change: "Y"`, it must equal `PriorRefundedFen + AmountFen`; only then does the response prove this refund succeeded. For `fund_change: "N"` or absent, it must equal either the previous cumulative value or that value plus this request, but the result remains `accepted` and must be queried. This preserves recovery when a prior timed-out attempt may already have changed funds. A signed business error after a possible earlier attempt is also `unknown`; the stable refund number must be queried before any release decision.

Query sends and verifies `out_request_no`, original order and transaction IDs, `total_amount`, and this request's `refund_amount` with strict decimal-to-fen parsing. A signed, identity-matched `refund_status: "REFUND_SUCCESS"` is success. Missing status, future status, provider error, malformed amount, identity mismatch, or transport/verification error remains unknown. The documented query response does not return `refund_fee`; its `refund_amount` is the current refund request amount. The configured app/merchant identity is bound by the fixed provider instance and verified response signature rather than requiring response fields the API does not promise.

This adapter stage does not implement Alipay refund callbacks because the selected refund flow is reconciled using the official query API. No protocol test sends requests outside local `httptest` servers.

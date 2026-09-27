# Payment trade-bill adapter boundary

This package is a protocol/parser checkpoint only. It does not import bills into a database, match rows to orders, create reconciliation cases, or expose an admin route. No production bill request was made in tests.

No new environment variables or merchant secrets were added. Both adapters use the existing server-side provider configuration; callers cannot supply a URL. The methods are not wired to a public or admin route. A WeChat raw bill contains payer identifiers and custom merchant text, so a later persistence layer must set access and retention rules before storing raw bytes.

## WeChat Pay

`wechat.Provider.DownloadTradeBill` requests the fixed `ALL` bill for a completed China calendar day. It verifies the signed API response, accepts only the documented bill download paths on the configured API hostname, signs the download GET, limits the response to 32 MiB, rejects redirects/non-public DNS results/compressed responses, and compares the API-provided SHA-1. The returned statement also carries the raw bytes, a local SHA-256, requested merchant/app identity, API hash metadata, and per-row source line/hash.

The parser accepts only the current 27-column `ALL` header and its seven-column summary. It requires the summary; an absent file or summary is an error, never zero activity. Zero activity is represented only by an explicit summary with count zero and zero totals. Legacy headers, unknown columns/statuses, invalid currency, bad dates, mismatched totals, oversized input, and over 250,000 rows fail closed. Duplicate rows remain duplicate rows. The app ID in each row is retained even when it differs from the requested app, so other applications under a shared merchant are not silently attributed to this app.

The official specification says the transaction time is payment success time for payments and refund initiation time for refunds, not refund success time. Refund rows have zero order amount; payment fees are nonnegative and refund/reversal fees are nonpositive. `PROCESSING` is preserved as a point-in-time historical value: the exported refund status does not update later, so a newer successful query/notification is not by itself a bill discrepancy. Gross order amount, settlement amount, discount, refund amounts, and fee stay in separate integer-fen fields.

The file uses comma separators and one leading backtick marker on fields. Provider-defined text escaping replaces embedded commas with backslash plus space and escapes a quote as `\"`; parsing therefore does not use RFC CSV quote rules. Text columns are not decoded into customer-facing data; source evidence remains available through the raw file and digest.

Official sources:

- [交易账单详细说明](https://pay.wechatpay.cn/doc/v3/merchant/4013071246) — current columns, summary sums, row dates/statuses, amount semantics, and escaping.
- [申请交易账单 API](https://pay.wechatpay.cn/doc/v3/merchant/4013070395) — signed metadata response and SHA-1 hash.
- [下载账单开发指引](https://pay.wechatpay.cn/doc/v3/merchant/4013071218) — date availability and temporary download URL.

## Alipay

`alipay.Provider.DownloadTradeBill` calls the fixed `alipay.data.dataservice.bill.downloadurl.query` method with `bill_type=trade` and a completed `Asia/Shanghai` date. The existing gateway adapter verifies the API response signature over the original response node. The returned URL must use HTTPS and the observed official host `dwbillcenter.alipay.com`; the safe downloader applies DNS/public-IP checks, no redirects, a size limit, and rejects HTTP content-encoding compression. It does not unpack archives. The raw result includes a local SHA-256, bill date, format marker, and configured merchant/app identity. The API response is signed; the downloaded file has no independent provider hash in the verified contract, so the local SHA-256 is an evidence digest, not a provider integrity signature.

The ordinary merchant `trade` file's current column schema, encoding, compression, row states, summary, and date semantics are not implemented. The official bill URL API proves how to request and download a trade bill, but the accessible current API reference does not specify that file layout. An older bank-interconnect data dictionary is a different product and is not used as a merchant parser. Until a current official normal-merchant format or validated merchant sample is available, bytes remain opaque; do not normalize them or claim Alipay reconciliation support.

References:

- [Alipay bill download URL API](https://opendocs.alipay.com/apis/api_15/alipay.data.dataservice.bill.downloadurl.query) — method and response URL.
- [Older generic reconciliation article](https://developer.alibaba.com/docs/doc.htm?articleId=106262&docType=1&source=search&treeId=193) — a format research lead only; not sufficient evidence for a current parser.
- [Bank-interconnect data dictionary](https://doc.open.alipay.com/docs/doc.htm?articleId=106431&docType=1) — explicitly excluded from the normal merchant parser.

## Remaining reconciliation work

The next phase must persist immutable source batches (provider, date, merchant/app scope, format/version, source digest and appropriate raw-file retention), detect duplicate imports without deleting duplicate rows, match payment/refund evidence by explicit identifiers, and create auditable differences. Fee/net settlement reconciliation is separate from this transaction-bill parser. Human resolution must not rewrite source evidence or directly alter points.

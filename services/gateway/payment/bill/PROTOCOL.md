# Payment trade-bill adapter boundary

This package contains the bill protocol and parser adapters; database import, matching, audit, and admin HTTP/UI integration are provided by `service/payments`, `controller/admin_reconciliation.go`, and the default reconciliation page. This is local implementation evidence, not a claim of production availability. No production bill request was made in tests. See [`RECONCILIATION.md`](RECONCILIATION.md) for the end-to-end repository boundary.

The adapters use existing server-side provider configuration; callers cannot supply a URL. The integration is limited to the repository's authorized admin reconciliation flow. A WeChat raw bill contains payer identifiers and custom merchant text, so stored source access and retention remain governed by the reconciliation safeguards described below.

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

`alipay.Provider.DownloadTradeBill` calls the fixed `alipay.data.dataservice.bill.downloadurl.query` method with `bill_type=trade`, a completed `Asia/Shanghai` date, and `secure=true`. The official v2 Java SDK documents `secure` as a string and states that only `true` returns an HTTPS bill URL; it is included in the signed request. The existing gateway adapter verifies the API response signature over the original response node. The returned URL must use HTTPS and the observed official host `dwbillcenter.alipay.com`; the safe downloader applies DNS/public-IP checks, no redirects, a size limit, and rejects HTTP content-encoding compression. It does not unpack archives. The raw result includes a local SHA-256, bill date, format marker, and configured merchant/app identity. The API response is signed; the downloaded file has no independent provider hash in the verified contract, so the local SHA-256 is an evidence digest, not a provider integrity signature.

The ordinary merchant `trade` file's current column schema, encoding, compression, row states, summary, and date semantics are not implemented. The official bill URL API proves how to request and download a trade bill, but the accessible current API reference does not specify that file layout. An older bank-interconnect data dictionary is a different product and is not used as a merchant parser. Until a current official normal-merchant format or validated merchant sample is available, bytes remain opaque; do not normalize them or claim Alipay reconciliation support.

References:

- [Alipay bill download URL API](https://opendocs.alipay.com/apis/api_15/alipay.data.dataservice.bill.downloadurl.query) — method and response URL.
- [Alipay Java SDK v2 request model](https://raw.githubusercontent.com/alipay/alipay-sdk-java-all/master/v2/src/main/java/com/alipay/api/domain/AlipayDataDataserviceBillDownloadurlQueryModel.java) — `secure` is a string; `true` selects an HTTPS bill URL.
- [Older generic reconciliation article](https://developer.alibaba.com/docs/doc.htm?articleId=106262&docType=1&source=search&treeId=193) — a format research lead only; not sufficient evidence for a current parser.
- [Bank-interconnect data dictionary](https://doc.open.alipay.com/docs/doc.htm?articleId=106431&docType=1) — explicitly excluded from the normal merchant parser.

## Reconciliation follow-up

The SQLite storage/matching implementation, bounded admin HTTP access, and
default reconciliation page are described in [`RECONCILIATION.md`](RECONCILIATION.md).
Alipay bytes remain explicitly unsupported for row parsing. This repository
implementation is not a claim that production reconciliation is enabled or
has been validated against live merchant accounts. Fee/net settlement-account
matching is separate from this trade-bill phase.

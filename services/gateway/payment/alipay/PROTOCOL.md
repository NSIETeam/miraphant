# Alipay protocol adapter

This package implements the first Alipay desktop website payment lane using
`alipay.trade.page.pay`. It does not imply a merchant account is configured or
that this channel is enabled. The provider constructor fails closed unless
server-owned configuration is complete; production endpoint selection is
fixed in code. Tests inject only local `httptest` endpoints through a private
field.

## Verified protocol behavior

- Page-pay orders use CNY fen converted with integer arithmetic, set
  `time_expire` from the immutable local order expiry, and return the fixed
  official form action separately from the signed fields. Browser return data
  does not credit an order.
- Request signatures use RSA2 over sorted `key=value` parameters, including
  `sign_type`; notification verification excludes `sign` and `sign_type`,
  parses form encoding once, and rejects duplicate fields. The signed fixture
  includes Chinese text, spaces, `+`, `%`, and `&` to check decoded values.
- API responses are RSA2 verified over the exact received bytes of the method
  response JSON object. The code does not decode and reserialize the response
  before signature verification.
- Query binds the signed request to the configured app and seller, requires
  the requested order and a provider trade number in the signed response, and
  distinguishes `ACQ.TRADE_NOT_EXIST` from other unknown API errors. Optional
  app/seller fields are checked if Alipay returns them; common response shapes
  that omit them use the configured signed request identity. Query payment
  time is read from `send_pay_date`.
- Close first performs an authoritative signed query and calls
  `alipay.trade.close` only for `WAIT_BUYER_PAY`. Paid and unknown statuses are
  not closed. The signed close response must match the local order; its
  optional trade number, when present, must match the query.
- Amount strings are parsed directly into integer fen. Scientific notation,
  signs, excess precision, and overflow are rejected.
- HTTP redirects are disabled for server-to-server API requests.

## Scope and remaining lanes

Only the desktop website checkout is implemented. Alipay mobile website
payment still requires a separate `alipay.trade.wap.pay` lane and merchant
product approval. No Alipay merchant credentials, real transactions, refunds,
reconciliation, or production endpoint calls are part of these tests.

Official references:

- [Alipay desktop website payment request](https://developer.alibaba.com/docs/doc.htm?articleId=105901&docType=1&treeId=237)
- [Alipay notification checks and successful states](https://developer.alibaba.com/docs/doc.htm?articleId=106448&docType=1&treeId=193)
- [Alipay trade query](https://developer.alibaba.com/docs/doc.htm?articleId=757&docType=4&treeId=180)
- [Alipay trade close](https://developer.alibaba.com/docs/api.htm?apiId=1058&docType=4)
- [Alipay API response signature verification](https://developer.alibaba.com/docs/doc.htm?articleId=104613&docType=1&treeId=140)
- [Alipay mobile website payment](https://developer.alibaba.com/docs/doc.htm?articleId=107090&docType=1&treeId=193)
- [Alipay SDK parameter signing reference](https://github.com/alipay/alipay-sdk-php-all/blob/master/v2/aop/AopCertClient.php)

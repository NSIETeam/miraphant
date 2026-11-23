# Default 路由剩余源码审计

## 范围与边界

本次以 `services/gateway/web/default/src/App.js` 的实际路由为入口，沿复用组件核对对应后端。下表记录修复前确认的三个问题及修复状态。父任务已补充隔离浏览器验证，详见文末；未做生产环境或外部支付验证。

登录、注册、密码重置和个人绑定页面做了定向源码检查，未发现需列入本次前三项的问题；这不代表每种认证配置和错误分支均已验收。`token` 出现在模型协议/API 字段中属于协议用语；用户密钥页面称“访问密钥”是产品凭证名称；`Brand.js`、页脚与“其他设置”中的 One API/MIT 来源说明属于开源署名，均不作为品牌问题。

## 已确认问题

| 优先级 | 可达位置 / 角色 | 修复前源码证据 | 建议 | 当前工作树状态 |
| --- | --- | --- | --- | --- |
| P1 | API：`/dashboard/billing/subscription`、`/v1/dashboard/billing/subscription`、`/dashboard/billing/usage`、`/v1/dashboard/billing/usage`；持有有效模型访问密钥的客户 | [router/dashboard.go:10-20](../../services/gateway/router/dashboard.go#L10-L20) 将四条路径挂在 `TokenAuth` 下，[router/main.go:14-17](../../services/gateway/router/main.go#L14-L17) 确认路由已注册。修复前，[billing.go](../../services/gateway/controller/billing.go) 的两个处理器读取 `User.Quota` 或 `Token.RemainQuota/UsedQuota`，并返回美元语义字段，没有积分模式分支。 | 积分模式下拒绝旧 API，返回稳定 `legacy_billing_disabled` 错误，不输出 USD 或旧 quota；旧模式响应保持原契约。 | 已在 [billing.go:13-27](../../services/gateway/controller/billing.go#L13-L27) 与 [billing.go:81-84](../../services/gateway/controller/billing.go#L81-L84) 加积分模式拒绝。新增 [legacy_billing_http_test.go:20-127](../../services/gateway/controller/legacy_billing_http_test.go#L20-L127) 覆盖四 URL、401、积分模式拒绝和旧模式响应结构。定向 HTTP 测试通过。 |
| P1 | `/admin/users/edit/:id`、旧别名 `/user/edit/:id`；管理员 | 路由见 [App.js:113-115](../../services/gateway/web/default/src/App.js#L113-L115) 和 [App.js:190-196](../../services/gateway/web/default/src/App.js#L190-L196)。修复前 `EditUser.js` 无条件显示“剩余额度”；后端 [user.go:415-417](../../services/gateway/controller/user.go#L415-L417) 对积分模式 quota 变更明确拒绝。 | 积分模式隐藏旧额度输入、保留载入的旧 quota 作为兼容投影，并提供 `/admin/users` 积分入口；按服务端 `points_billing_enabled` 判断，读取失败时不显示可提交默认表单。 | 已在 [EditUser.js:39-75](../../services/gateway/web/default/src/pages/User/EditUser.js#L39-L75) 读取状态并丢弃过期路由响应，在 [EditUser.js:88-104](../../services/gateway/web/default/src/pages/User/EditUser.js#L88-L104) 校验加载用户与当前路由并保留 quota；[EditUser.js:167-190](../../services/gateway/web/default/src/pages/User/EditUser.js#L167-L190) 按计费模式隐藏旧额度并给出积分入口。默认主题构建通过。 |
| P2 | `/admin/setting`、旧别名 `/setting` 的运营设置；管理员 | [App.js:113](../../services/gateway/web/default/src/App.js#L113) 与 [App.js:263-271](../../services/gateway/web/default/src/App.js#L263-L271) 注册入口。修复前 `OperationSetting.js` 在积分模式仍显示两个旧计费开关；[model/option.go:20-25](../../services/gateway/model/option.go#L20-L25) 将它们列入旧计费设置，[controller/option.go:48-50](../../services/gateway/controller/option.go#L48-L50) 写入时返回 409。 | 积分模式隐藏“Billing 显示令牌额度”和“近似估算 token”开关，并说明旧设置只读；服务端拒绝保护保留。 | 已在 [OperationSetting.js:225-238](../../services/gateway/web/default/src/components/OperationSetting.js#L225-L238) 仅旧计费模式渲染两项。默认主题构建通过。 |

## 本次未列为问题的边界

- `/console/keys` 中模型访问凭证的“访问密钥”名称与用途相符；不要将其与 `PersonalSetting` 中明确标注为系统管理用途的旧系统令牌混为一谈。
- `/about`、页脚及品牌状态/版本页中的上游与 MIT 声明是许可证归属说明，不属于残留品牌错误。
- `/topup` 已重定向到积分钱包；兑换入口明确标为尚未迁移并关闭，未把它视为可用充值功能。

## 父任务复核（2026-09-28）

定向 controller race 检查两次通过。隔离合成浏览器验证了积分模式隐藏字段、修改显示名称后历史 quota=123456 和积分账户/流水不变、资料请求 500 后失败页及重试恢复、旧模式重新显示额度和两个开关。路由 ID 并发响应保护仅完成源码审核，本节点未做移动端或所有认证配置遍历。构建及范围证据见 [审核记录](REVIEW-GATES.md)。

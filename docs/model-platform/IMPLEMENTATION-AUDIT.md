# Miraphant 网关源码纳管与实现审计

## 当前阅读指引（2026-09-28，提交 301721b）

本文保留首次源码审计及各阶段的历史判断，下面的“待审核”“尚未实现”“未提交”等表述以所在阶段为准。当前审核结论以 [REVIEW-GATES.md](REVIEW-GATES.md) 为准，页面实测范围见 [ROUTE-ACCEPTANCE.md](ROUTE-ACCEPTANCE.md)。

| 范围 | 当前仓库状态 | 尚未完成的边界 |
|---|---|---|
| 源码与构建 | 固定上游基线、三主题锁文件、静态发布隔离与 Linux 构建已提交并通过 CI | 新版本未部署生产 |
| 品牌与客户入口 | 官方 Miraphant SVG、默认主题、积分总览／钱包／用量／密钥与部分管理页面已实现，部分实际浏览器检查通过 | 全路由、剩余管理页及完整移动场景待验收 |
| 积分账本与模型扣费 | 整数微积分账本、价格快照、冻结／结算／待核实、管理裁决、受限纯文本 relay 与离线冻结恢复已实现 | 真实上游模型、其他模态、历史余额换算与生产切换待完成 |
| 充值基础 | 套餐／商户身份快照、双渠道桌面协议、配置装载、本人订单 HTTP、通知入箱与原子到账、游标恢复、客户充值／收银台／订单页面已提交 | 真实商户、小额实付及移动支付待完成 |
| 运营能力 | 管理积分调整、价格发布、待核实请求处理、套餐版本管理、脱敏支付准备页及管理查单／审计已交付；退款纯账本、双渠道协议、持久 claim 与 inbox、显式恢复函数已交付；v11 同号续发、默认关闭的自动恢复及主程序启停已提交并通过 CI；退款能力授权与密码再确认基础已提交并通过 CI | 退款业务 HTTP 已提交；退款页面本地构建和首轮浏览器验收通过，完整异常场景、支付对账及其余管理界面仍待完成 |
| 真实收款 | 新充值默认关闭，未提供真实商户凭证 | 商户及产品开通、真实小额付款／退款／对账验收后才可开放 |

最近已确认成功构建：[301721b / CI 36349389962](https://github.com/NSIETeam/miraphant/actions/runs/36349389962)。它覆盖定向后端检查、支付与退款协议及持久服务、三主题和 Linux 网关构建；不代表商户联调或完整产品验收。退款业务、页面和终态重放提示修正已提交，c10b280 的 CI 36348118107 已通过；首轮及补充浏览器验收范围见 ROUTE-ACCEPTANCE.md。

2026-09-28 源码复核：默认主题 App.js 的 `/admin/refunds` 已接真实业务页面，`/admin/reconciliation`、`/admin/brand` 仍使用占位组件；router/api.go 已注册客户充值与管理查询，b600aa7 已增加客户退款、财务操作及微信退款通知路由；对应操作页面已接线并完成首轮浏览器验收，详细边界见 ROUTE-ACCEPTANCE.md。后续执行顺序为退款恢复与运行生命周期、退款 HTTP／权限／客户及管理界面、对账、剩余品牌与移动端路线，再做商户／模型／迁移／发布验收。商户进件资料准备可同步进行；未开通不得开放真实收款。

## 首次 P0 审计记录

审计日期：2026-09-27
状态：P0 源码纳管及本地构建完成；等待主 agent 审核。生产切换和积分/支付功能仍未实施。
范围：核对规格与 OneAPI v0.6.10 上游代码，确定源码治理方式、功能改造落点和阻塞。本文不代表网关已接入、支付已开通或任何功能已上线。

## 核验结论

- 当前 `NSIETeam/miraphant` 仓库（分支 `docs/miraphant-points-payments`，HEAD `6e99183`）是静态官网和产品规格，没有 Go 服务源码、支付 SDK、网关部署定义或数据库迁移。仓库内未发现 `AGENTS.md`。
- 上游 `songquanpeng/one-api` 的 `v0.6.10` 标签可复现，指向 commit `3915ce9814b8261a1ab13ed93adec58b463cd75c`。本阶段已将完整源码基线纳入本仓库 `services/gateway/`，保留 LICENSE 并在 `services/gateway/UPSTREAM.md` 记录出处及差异。
- OneAPI 的模型中转、现有用户会话、模型渠道和访问密钥可复用；品牌替换、独立客户钱包、可靠积分结算、微信／支付宝订单与退款需要新增服务端和数据层，不能只靠配置或改静态页面完成。
- `/api/topup` 是管理员 API：`controller/user.go::AdminTopUp` 直接增加用户 quota，再另写充值日志；它没有支付单号、幂等键、回调校验或事务性账本。客户已有 `/api/user/topup` 是兑换码兑换，不能借名承载支付回调或自动重试。
- 现有模型调用先通过 Redis 和 DB 分别扣用户与令牌 quota，最终结算由 goroutine 异步执行；`usage == nil` 时不结算也不释放冻结额度。它不满足规格要求的预扣／结算幂等、数据库单一事务或积分单一账本。
- 支付商户未开通。微信和支付宝实现必须是真实渠道适配，配置与收款总开关默认关闭；没有商户凭证时只可开发关闭状态、校验路径和人工构造的签名测试，不得伪造成功通知或虚构实付结果。

## P0 纳管和构建执行状态

- `services/gateway/` 已纳入上游 tag 的 545 个文件，逐个做 SHA-256 比对，内容差异为 0；上游 LICENSE 与 tag 字节一致。导入源码排除 `.git`、node_modules、构建输出、数据库、日志、缓存和运行数据；构建后 `node_modules` 已清理，`web/build/` 仅保留为 Git 忽略的本地 embed 构建输出。三主题 `package.json` 保持原样；因上游 tag 无 npm 锁文件，按其依赖声明生成并纳入三个 lockfile，后续构建走 `npm ci`。
- [services/gateway/BUILDING.md](../../services/gateway/BUILDING.md) 和 `services/gateway/scripts/build-local.sh` 提供独立构建方式。实际构建使用 Go `1.25.1 darwin/arm64`、Node `24.20.0`、npm `11.19.0`；default、berry、air 均显示 `Compiled successfully`，Go 网关编译成功，`--version` 返回 `miraphant-v0.6.10+3915ce9`。本地二进制 SHA-256：`d25d46429c9541124e21f194f2cb8816b3f4e41f58583e6d439efe3253cabdb2`，产物写入仓库外 `work/miraphant-gateway-build/`；前端嵌入资源位于被 Git 忽略的 `services/gateway/web/build/`。这只证明 macOS/arm64 本地构建，CI 的 Linux/amd64 构建尚未运行，不能作为生产目标架构验收。
- npm 安装输出含上游旧依赖弃用提示、安装脚本提示；三个主题仍然全部通过编译。air 主题另有约 989 KB gzip 前主 JS bundle 的 CRA 体积提示，未阻断构建。
- `scripts/build-pages.py` 将 51 个跟踪中的官网页面和依赖资源复制到仓库外的干净 staging 目录。构建两次均通过；临时夹验证了仓库祖先、未标记目录、输出 symlink、指向仓库的父 symlink 和 allowlist 中的源 symlink 均拒绝删除/复制，旧的标记产物可安全刷新。新建 `.github/workflows/build-check.yml` 只做静态 staging 和 Go/前端构建检查，不部署，也未改线上 Pages 配置。
- 线上实际部署、数据库和一致性备份见 [PRODUCTION-BASELINE.md](PRODUCTION-BASELINE.md)。当前运行版本字符串是 `v0.6.10`，但生产二进制哈希尚未与上游 release 文件逐字节比对；生产架构为 Linux/x86_64，本地成功不证明生产可运行。主 agent 的隔离恢复验证不构成新版本部署或切流验收。

## 可行性与来源治理建议

建议在当前 `NSIETeam/miraphant` 仓库新增 `services/gateway/`，将 OneAPI v0.6.10 源码保持完整放在该目录，保留 `.git` 之外的项目源码布局、上游 `LICENSE`、版权和所用版本要求的署名，并在 `services/gateway/UPSTREAM.md` 记录上游地址、tag、完整基线 commit、Miraphant 补丁范围和获取方式。服务构建、镜像和部署流程与官网发布分开。这样可在现有仓库纳管源码和审阅改动，同时不要求未经用户选择另建仓库。

当前 checkout 没有 Pages 构建工作流，官网已有静态文件位于仓库根目录；将 `services/gateway/` 放入仓库前必须建立独立 webroot/明确发布 allowlist。建议新增 `scripts/build-pages.sh` 和 `.github/workflows/pages.yml`，仅把官网入口 HTML、现有页面目录、`css/`、`js/`、`images/`、`products/`、官网所需下载产物、favicon 及其明确引用资源复制到干净 staging 目录，然后只部署该 staging 目录。明确排除 `services/`、`docs/` 中的内部部署资料、`.env*`、数据库/日志、构建缓存和仓库元数据。发布流程不得直接把 checkout 根目录作为 webroot；加入一个断言检查打包清单中没有 `services/gateway` 或敏感运行数据。网关用独立容器/主机流程构建发布，不能由 Pages 提供静态访问。

建立 gateway 仓库前，先从线上部署方取得实际镜像 digest、启动参数、环境配置名、容器编排、代理规则、数据库类型／版本及备份恢复流程，并将它们与上游 tag 做源码／构建比对。已知线上 `/api/status` 报告 `v0.6.10` 只证明版本字符串，不能证明该镜像未打补丁或由该 tag 构建。生产状态与凭据不在本审计范围；不得把任何凭据复制到仓库或文档。

实际标志源 `/Users/king/Desktop/Miraphant.svg` 存在（1838 字节）。品牌衍生图在 `services/gateway/web/default/public/` 生成并记录源文件哈希；不把工作站绝对路径写入服务运行配置。

## 上游实际结构与复用边界

检出基线：`v0.6.10` / `3915ce9814b8261a1ab13ed93adec58b463cd75c`。

| 能力 | v0.6.10 代码证据 | 复用判断 |
|---|---|---|
| API 路由与身份 | `router/api.go`、`router/relay.go`、`middleware/auth.go` | 复用 `/v1` relay、会话和模型访问密钥校验。新增客户 API 取当前登录身份；回调单独做签名认证；后台接口逐条做服务端角色校验。 |
| 旧充值 | `controller/user.go::TopUp`、`AdminTopUp`；`model/redemption.go::Redeem` | 兑换码可保留为旧兼容入口并单独分类入账。管理充值仅是直接 quota 增量；禁止 payment 回调调用它作为可重试的发放路径。 |
| 余额和令牌限额 | `model/user.go`、`model/token.go`、`model/cache.go` | 必须重做预扣／结算边界；用户钱包 ledger/hold 为唯一账本。令牌自身限额仍保留为另一项访问约束，不等于客户钱包。 |
| 数据库 | `model/main.go::migrateDB`、`model/log.go` | 当前主库和日志库走 GORM `AutoMigrate`；支付、账本和迁移应使用可审查、有版本、有回滚/前滚说明的 SQL migration，不能依赖启动时自动猜测生产 schema。 |
| 客户与后台 UI | `web/default/src/App.js`、`web/default/src/index.js`、`web/default/src/components/Header.js`、`web/default/src/pages/TopUp/index.js` | 以默认主题完整覆盖规格路由、身份和状态；其他主题 (`berry`, `air`) 未证明与默认主题功能同步。首期应固定并验收默认主题，或同步实现所有启用主题。 |
| 品牌运行配置 | `common/config/config.go`、`controller/misc.go::GetStatus`、`web/default/src/App.js` | 状态接口把系统名、Logo、footer 和 quota 显示配置送到前端；它不足以覆盖 SEO、分享图、邮件或所有遗留文字。还须改静态模板、图标、邮件文案并搜残留。 |
| 前端构建和后端打包 | `web/default/package.json`、`web/build.sh`、`Dockerfile`、`main.go` | Docker 构建三个主题再将 `web/build` 嵌入 Go 二进制；每次品牌或路由 UI 变更都要重建镜像。只改数据库里的名称／Logo 不会更换前端资源和所有标题。 |
| 容器部署样例 | `docker-compose.yml`、`.env.example`、`one-api.service` | 是上游示例而非线上部署证据；含默认口令/`latest` 镜像等示范值，不能作为生产配置直接套用。生产模板要锁定镜像 digest、密钥外置、持久卷和恢复步骤。 |

### 余额路径为何不能原样沿用

文本请求在 `relay/controller/helper.go::preConsumeQuota` 中经 Redis 缓存检查余额并减少缓存，随后在 `model/token.go::PreConsumeTokenQuota` 单独更新令牌和用户 DB。成功响应后 `relay/controller/text.go` 以 goroutine 调用 `postConsumeQuota`；`model/token.go::PostConsumeTokenQuota` 又分开更新用户／令牌，`relay/billing/billing.go` 同时刷新缓存、异步写用量日志和汇总。不同请求路径（图片、音频等）有各自实现。数据库事务未覆盖整个预扣／结算生命周期，调用重复、进程重启、错误分支与缺失 usage 需要新的请求级幂等键和可恢复 hold 记录。

新账本落地后只能有一个可用余额权威来源。推荐在迁移期让现有 `quota` 成为 ledger 汇总投影，定义单向更新、缓存失效和对账；所有旧写入（充值、兑换码、管理员调整、注册赠送、迁移脚本及模型消费）均须先改为有唯一业务键的账本事件，再更新投影。禁止旧 quota 路径和新 points 路径并行扣同一笔请求。

## 文件级实施清单

以下服务端路径相对于当前官网仓库中的 `services/gateway/`，基于上游 v0.6.10。先由主 agent 审阅本清单、生产构建来源与恢复方案，再开始功能改造。

### P0：纳管和基线

- `VERSION`、`common/constants.go`、构建元数据：记录 Miraphant 派生版本、上游 tag 与完整基线 commit；构建产物可追溯至 commit 与前端构建。
- `LICENSE`、`README.md`、`web/README.md`：保留 MIT 文本和要求的上游归属；记录修改与部署方式，不移除所用主题要求的署名。
- `Dockerfile`、`docker-compose.yml`、`.env.example`、`one-api.service`：改成非生产默认值、锁定发布镜像、列出必需/可选配置名与 secret 注入方式、持久化数据和日志；示例不得含可用生产凭证。
- 新建 `migrations/` 及迁移执行入口：记录数据库兼容矩阵、迁移版本、备份／恢复演练和回滚界限。先只新增表，不重解释旧 quota 或批量换算余额。
- 纳入上游基线比较记录与部署来源证据；确认真实线上构建与 tag 差异。未能取得生产部署资料时，P0 标注阻塞，不做切流。

### P1：品牌与客户入口

- `common/config/config.go`、`controller/misc.go`：引入固定 Miraphant 默认品牌值和关闭状态，不靠生产数据库临时值承担品牌基线；`GET /api/status` 保留已有 JSON 字段兼容。
- `web/default/public/index.html`、`web/default/public/favicon.ico` 及新增 `web/default/public/` 品牌衍生资源：接入核验过的 SVG 源、favicon、触屏和分享图片；补齐标题与分享 metadata。
- `web/default/src/App.js`、`web/default/src/index.js`、`web/default/src/components/Header.js`、`Footer.js`、`web/default/src/helpers/render.js`：替换客户导航和显示单位；人民币/积分展示不得通过把 `quota_per_unit` 改数值伪装旧美元 quota。
- `web/default/src/pages/TopUp/index.js`：保留兑换码入口，换成服务端套餐、订单、钱包 API；移除把客户端拼接 `username/user_id/transaction_id` 交给外部充值链接当到账证据的逻辑。
- `web/default/src/App.js` 与新增 `web/default/src/pages/Console/*`、`Pricing/*`、`Checkout/*`、`Orders/*`、`Usage/*`：加入规格的 `/pricing`、`/console/*`、`/checkout/:orderId`、帮助页；订单详情始终通过服务端校验本人或获授权管理员。
- `web/default/src/App.js`、新增 `web/default/src/pages/Admin/*`：管理界面按操作能力区分财务、客服只读、平台管理员；隐藏菜单仅为 UI，不构成授权。
- `web/default/src/components/Footer.js`、邮件模板 `common/message/`、帮助和条款页面：分别处理品牌署名、客服及经营／退款文案，保留上游许可证要求。
- `web/build.sh`、`Dockerfile`：品牌 UI 只作为重建镜像的一部分发布；检查 `berry` 与 `air` 是否仍可选，禁用未覆盖验收的主题。

### P2：价格、钱包、幂等账本和模型用量

- 新增 `model/point_ledger.go`、`model/point_hold.go`、`model/price_version.go`、`model/model_binding.go`：保存微积分整数、唯一业务键、来源类型、请求/订单/价格版本关联、冻结及终态；由数据库唯一索引而非进程内检查保证幂等。
- `migrations/`：增加 ledger、hold、价格版本及审计表；索引覆盖 user、业务键、request id、有效时间；余额与流水初始校验/迁移须单独脚本、批次 ID、总量校验和回滚点。
- 新增 `service/points/reservation.go`、`settlement.go`、`pricing.go`：同一个请求锁定价格版本；原子冻结用户积分并校验访问密钥上限，按 micro-points 最终结算一次，释放余量；待核实 usage 有时限、可恢复状态。价格来自规格草案但未绑定模型，发布前必须完成真实成本审核。
- `relay/controller/helper.go`、`relay/controller/text.go`，并逐项审查 `relay/controller/image.go`、`audio.go`、`relay/billing/billing.go`、各 adaptor 的 `Usage` 转换：让文本主路径经统一 reservation/settlement，显式请求幂等 ID；首期未定价的图像、语音、视频、工具不允许带积分承诺或误扣。移除 `helper.go::preConsumeQuota` 中“余额大于预扣额 100 倍则跳过预扣”的信任优化；每次请求先原子检查用户可用额并扣留预算，同一 reservation 的重试不能重复冻结。
- `model/token.go`、`model/user.go`、`model/cache.go`、`model/utils.go`、`model/redemption.go`、`controller/user.go`：盘点并替换所有余额写入/缓存读写点，包括注册送额、邀请额、兑换码、管理员额度调整和失败返还。兼容 API 保留原有字段和 `/v1` 行为；为历史交易建立账本来源分类。设置单一投影方向及旧版读取兼容，移除直接写余额路径前先完成迁移验证。
- `controller/billing.go`、`controller/log.go`、`model/log.go` 和前端账单 UI：保留 OpenAI `usage` 字段、用户用量记录及管理统计兼容；新增详单存模型、缓存、输入、输出、价格版本和精确积分，不把历史 quota 猜算成实际人民币消费。

**账本事务边界：**新增积分账本作为唯一事实来源，旧 `User.Quota` 只能是按账本更新的兼容投影，不保留一份独立可写的积分余额。对模型消费，将“数据库内开 reservation/hold + 原子占用可用余额 + 唯一请求键”放在一个短事务中；上游模型 HTTP 请求在事务提交后执行；拿到可确认的 usage 后，再以另一个短事务用唯一请求键写最终消费事件、标记 hold 已结算、归还多余冻结额并更新余额投影。无 usage 或状态不明时事务性保留有过期策略的 hold，供查证与恢复，不提前结算或释放。每个状态转换都可安全重放，唯一索引处理跨进程并发。不得让数据库事务跨越上游网络调用，也不得沿用“余额很高就跳过冻结”的优化。

支付到账时，订单／通知状态推进也走可恢复的幂等事务。渠道确认成功后，在同一数据库事务中写充值 ledger event、更新余额投影、将订单标记为 `credited`。任一写入失败即整体回滚；外部支付 HTTP 调用在事务外执行，再凭唯一订单键推进状态。

### P3：支付订单、真实渠道适配与退款

- 新增 `model/payment_order.go`、`payment_event.go`、`refund.go`、`package.go` 与对应迁移：订单号、用户、渠道、整数分金额、积分/套餐快照、状态、过期时间；通知唯一键、渠道流水号和脱敏验签结果；退款及累计金额／积分上限。
- 新增 `payment/provider.go`、`payment/wechat/`、`payment/alipay/`：按正式 API 文档实施下单、验签/解密、查单、关单和退款；Provider 接口不能以返回“成功”的假实现接入生产。支付通道配置和全局充值开关显式默认 `false`，没有 merchant config 时创建真实订单必须失败关闭。
- 新增 `service/payments/orders.go`、`notifications.go`、`reconcile.go`、`refunds.go`：服务端从会话读取 user ID、从已发布套餐确定金额和积分；事务内保存订单/通知和状态转换。支付通知先可靠落库，成功入账走与重试共用的唯一 ledger business key；查单确认后才能关闭超时单；浏览器 return URL 只查询，不增加积分。
- `router/api.go`：增加规格的 `/api/points/*`、`/api/payments/orders*`、`/api/payments/notify/wechat`、`/api/payments/notify/alipay`、`/api/admin/payments/*` 和 `/api/admin/points/*`；保留旧 `/api/user/topup`（兑换码）及 `/api/topup`（管理员）既有语义，拒绝把新支付映射到旧端点。
- `middleware/auth.go` 或新增专用 middleware：订单 API 校验 user ownership；财务／客服／管理员按动作做服务端授权；管理员角色和后台授权须从数据库读取当前状态，不能只依赖 `authHelper` 登录时放入 session 的 role/status（否则撤权后旧 session 仍带旧权限）。回调不读用户 session，只验证平台签名、商户/AppID、订单、币种、金额、状态；写请求加 CSRF、限流及审计。不能直接以现有 `AdminAuth` 将客服与财务合并授权。
- `controller/user.go::AdminTopUp`、`model/user.go::IncreaseUserQuota`：增加强制审计和受控迁移/管理适配，确认上线后人工调整生成 ledger adjustment，不允许绕过账本直改 quota。`model/redemption.go::Redeem` 在兑换码一次性事务成功时也应写唯一账本业务事件。
- `web/default/src/pages/TopUp/index.js` 及 `Console/Wallet`、`Checkout`、`Orders`、后台 `Payments/Orders/Refunds/Reconciliation`：呈现服务端订单状态、已付待入账、关闭和退款进度；通道未开时说明未开放且不创建订单；不得用假二维码或假成功页面。

### P4：迁移、运营审计与切换门

- 新增 `migrations/` 版本化变更、`scripts/audit_legacy_balances.*` 与 `scripts/migrate_points.*`：先只读盘点用户 quota、令牌限额、兑换码、注册/邀请赠送、已用额度、Redis 缓存和进行中请求；输出来源不明项；经业务确认转换比例后才允许带批次、校验和的 dry-run/正式迁移。
- 新增 `model/admin_audit.go`、`controller/admin_*`、`service/reconciliation/` 以及 `/admin/audit` 页面：覆盖价格、套餐、开关、调账、补账、退款、角色变更及查单重试；记录执行人、原因、关联业务键和结果，不写 secret。
- 新增 `docs/operations/backup-restore.md`、`cutover.md`、`payments.md`、`incident-recovery.md`：要求备份校验、暂停新充值与消费、排空在途调用、恢复后按事件对账。发生新交易后不能用旧数据库快照覆盖已付款事件。
- 真实小额双通道实付、到积分、模型消费、原路退款和渠道对账由获得商户权限的公司管理员按正式账户完成；本地 P0/P1/P2 工作无法替代这一验收。

## API 兼容与授权约束

- `/v1/*` 的路径、认证头、请求／响应 schema 和供应商 `usage` 原样兼容；新余额展示 API 不改变 OpenAI 客户端字段。
- `GET /api/status` 保留上游既有字段，旧 `/api/user/topup` 仍只兑换码、旧 `/api/topup` 仍为受控管理员接口，直至有正式迁移窗口与公告。
- 新订单接口只从已认证用户会话取用户身份，只接受 `package_id`、`channel` 和幂等键；币种、金额、积分由服务端套餐版本决定。`orderId` 不作为授权凭证。
- 每个用户订单读写都按当前 user ID 限定；管理员或财务越权访问订单须单独审计。客户角色不能调用 `/api/admin/*`；用户访问密钥认证不得意外获得后台权限。
- Cookie 写操作实施 CSRF 与限流；回调验证官方签名和必要订单字段；通知可重放但只能产生一次入账。支付开关、通道开关初始关闭，无商户凭证时不可被 UI 或配置误开启。

## 阻塞与验收门

1. **生产构建来源与切换待证**：运行状态、数据库、备份和隔离恢复已记录在 [PRODUCTION-BASELINE.md](PRODUCTION-BASELINE.md)；生产运行二进制哈希尚未与官方 release 文件或可复现 Linux/x86_64 构建比对。完成目标架构产物验证和发布审阅前不得切流。源码纳管后须验证 Pages artifact 只含清单内官网静态资源，`services/gateway/`、运行配置、数据库和日志不对公众静态发布。
2. **余额迁移决策待业务确认**：旧 quota 到 CNY/积分的历史换算无默认等价关系；需来源清单和经确认的迁移版本，来源不明项隔离待处理。
3. **支付开通待商户办理**：正式主体、结算账户、API 凭证、域名审核、产品开通状态均需通过商户流程确认；未完成时微信/支付宝与充值全局开关保持关闭。
4. **价格发布待成本验证**：`pricing-draft.json` 是建议值、无模型绑定；每个可售模型需核验真实计量方式和成本后发布价格版本。
5. **按规格执行的验收**：新代码还须覆盖通知并发重放、金额/签名/商户不匹配、服务中断和通知丢失、关单竞态、同一逻辑模型请求重复结算、流式中断、无 usage、余额不足、退款与消费并发、角色越权、价格切换、旧余额迁移和备份恢复。无真实商户账户不能宣称真实收款、真实退款或双渠道验收通过。

## P0 阶段边界

本阶段新增上游源码基线、构建说明、锁文件、独立构建入口、静态站点 allowlist staging 和只做构建检查的 CI。没有实现积分账本、支付功能或生产部署，也没有修改线上服务或线上 Pages 配置；未提交或推送。本地构建为 macOS/arm64，Linux/x86_64 CI 与生产发布检查尚待主 agent 后续审核。线上当前服务、SQLite 备份和隔离恢复证据见 [PRODUCTION-BASELINE.md](PRODUCTION-BASELINE.md)。

## 首次审计阶段边界（历史记录）

首次审计只核对官网仓库、产品规格和公开的 OneAPI v0.6.10 源码，并新增本文件；当时未纳管服务源码。其余实现、生产验证和商户验收边界以后续阶段状态为准。

## G1 基础账本实现状态（待主 agent 复审）

本轮在 `services/gateway/model/points.go` 新增微积分账户投影、来源/有效期 lot、append-only ledger、不可变价格版本与独立激活映射、逻辑请求 hold/lot allocation/attempt、显式 token budget、购买订单、人工裁决记录及显式 schema migration。价格费用使用整数运算并只做一次 half-up；赠送 lot 有效期采用 UTC Unix 秒，避免 SQLite 对带时区 DATETIME 字符串比较产生错误排序。`ReservePoints` 默认受 `POINTS_BILLING_ENABLED=false` 保护，不接入 relay；访问密钥必须已有显式积分预算记录，`Unlimited` 也需明确设定。每个 attempt 要提供请求指纹；估算/缺失 usage 进入 pending，未知结果不按 TTL 自动释放；需要人工或供应商裁决后才可 settle/release。超出冻结预算时维持 needs-review，审核 settle 会在同一事务重新校验余额与 token cap 并额外冻结，不能透支或截断真实 usage。

`--migrate-points` 是单独的显式迁移入口。迁移不包在跨数据库 DDL 事务中，逐表 additive `AutoMigrate` 可在 MySQL 隐式提交/中途失败后重跑；仅全部表完成后写版本记录。SQLite 专项测试覆盖双 GORM 句柄并发冻结、余额不足、token cap 失败回滚、pending 到可信 usage 幂等结算、过期 lot、新冻结/旧 hold 释放、paid order 重复到账以及超冻结预算的审核补冻。当前实际验证为本机 Go 1.25.1：上述 model 专项测试通过，`go build ./...` 通过；全量 `go test ./...` 被既有 `common/image` 的 `TestDecode/Decode:jpeg` 失败阻断（`image: unknown format` 后测试 panic），本轮没有修改该测试或其图片资源。

本轮没有在 MySQL/PostgreSQL 实例上验证，不能据 SQLite 结果宣称跨数据库运行验收；没有改写、转换或投影旧 `User.Quota`，也没有把新账本余额与旧余额并行扣费。relay 各种模态和 quota 增减路径尚未接线，因此新计费必须保持关闭。没有真实微信/支付宝适配、订单创建/通知验签或商户到账；credit service 只接受数据库中已可靠标记 `paid` 且金额/积分快照符合 1 元=100 积分的订单。未部署、未提交或推送；生产 Linux/amd64 构建还需独立 CI/主 agent 验收。


## 剩余品牌与管理入口复核（2026-09-28，账单节点并行只读盘点）

- `web/default/src/App.js` 的 `/admin/brand` 仍是 UnavailableAdminPage；管理首页卡片却写“统一品牌配置”。下一品牌节点需交付真实页面及持久配置，或把能力状态明确展示；不能以导航存在认定完成。
- `web/default/public/index.html` 已有 Miraphant 名称、SVG favicon 和中文描述，但 noscript 仍是英文，尚无移动收藏图标/manifest 与分享元信息。需用现有官方素材补齐，并检查静态部署路径。
- `/admin/setting` 仍接旧 Setting：OperationSetting 暴露旧额度、倍率和外部充值链接。`controller/option.go` 未对这些键作积分模式限制。下一节点需逐项厘清哪些仍生效、哪些只属历史额度，提供正确的积分价格/套餐/钱包入口；不能让运营人员误以为旧倍率就是当前积分售价。
- Header/Footer 已使用 `/miraphant.svg`。保留应有上游许可署名；后续需对登录、错误页、渠道增改、绑定设置和手机状态逐路由验收，不能由首页品牌推断所有页面已经一致。

本节是源码盘点，不是页面验收；账单实现仍由 Luna 继续，品牌整改在其后交接。

2026-09-28：日账单适配器提交 301721b 的 CI 已通过。微信当前 ALL 格式解析及双渠道受控下载可用作后续内部集成；支付宝文件仍未解析。批次持久化、差异与管理流程正在后续开发，不属于本次通过范围。


### 对账存储节点最新进展

新增 schema v12 的加密原始账单、不可变批次/行/差异与追加处理记录，父稳定节点测试和竞态检查通过（详见 REVIEW-GATES.md）。这替代此前“尚无批次存储”的状态；运行配置、HTTP 和管理页面仍待接线。支付宝原文暂不解析，退款缺账范围受可靠渠道申请时间缺失限制；商户实账、生产迁移与发布未执行。


### 对账管理 HTTP 最新进展

schema v13 新增导入审计，运行配置与 `/api/admin/reconciliation` 已接线：独立查看/导入/备注权限、加密配置状态、批次/行/差异/处理记录与导入尝试查询。导入仅接受渠道和日期，成功批次与终态审计同事务，失败保留已接受请求并追加结果；父稳定复核通过。对账页面仍待实现；合成测试不等于实商户验收。前文“尚无配置/HTTP”的状态由本节替代。

## 2026-09-28：仓库交付与开放收款分别验收

前文“对账页面待实现”的状态已由 `bfe8216` 替代：批次、差异、备注、导入尝试及能力授权页面已接线，父级隔离浏览器证据见 ROUTE-ACCEPTANCE.md，精确提交 CI 36354935368 已通过。不能据此认定整个平台或真实收款完成。

| 用户要求／规格 | 当前证据 | 剩余验收 |
|---|---|---|
| 计划写入公司仓库 | docs/model-platform、services/gateway、PR #2 | 后续节点同步文档与代码 |
| Miraphant 正式 Logo 和全站品牌 | 两份 SVG 校验相同，主要默认主题已使用 | 品牌状态页、派生资产、旧配置入口与全路由收尾正在实施 |
| 客户积分及任务量估算 | points.md、pricing-draft.json、钱包和价格页面；已有账本/请求结算检查 | 实际可售模型和正式成本价格仍待确认，草案不构成保证 |
| 客户微信／支付宝充值入口 | /console/wallet、订单、收银台与支付适配 | 商户未开通，真实支付入口不能开放 |
| 管理界面 | 价格、套餐、订单、审计、退款和对账开发版 | 品牌/旧设置本轮收尾；支付宝逐行账单、运营恢复/调度及完整权限/异常覆盖仍待核对 |
| 商户申请准备 | README.md 的主体、支付产品和资料清单 | 公司管理员在官方流程办理及配置凭证 |
| 生产迁移和真实可用性 | 保留既有备份/隔离证据和关闭开关边界 | 历史余额转换决策、真实模型扣费、小额双渠道支付退款及发布均未完成 |

此次品牌审核的资产不变基准：官方 SVG SHA-256 `5f69642734d961b4616e35bfae63385030688a253763549edeca72babb791e63`；上游 LICENSE SHA-256 `e965b65a52d90708ffeeb9632e1928cab08bc7ae1424d8407e42fc372bce6506`。图标衍生不修改正式矢量源或删除许可。

### 历史余额迁移现状复核（2026-09-28）

当前 `main.go` 的 `--migrate-points` 只调用 `MigratePointsSchema`，model/points.go 的版本为 13，执行新增表/字段升级后退出。它不是 quota 到积分的余额迁移。源码中虽有 migration 批次类别及余额汇总，不足以证明已有历史额度转换工具；当前脚本目录/Go 命令未找到带来源清单、经确认比例、dry-run、迁移批次及校验和的余额转换入口。

品牌节点之后仍可在不接触生产的前提下实现只读历史余额盘点和可审阅转换计划；真正转换比例及生产执行另行确认。不得把 schema 升级成功作为历史余额已迁移的证据。

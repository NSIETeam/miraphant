# 生产基线与备份核验

日期：2026-09-27。主 agent 通过现有授权的阿里云管理通道核验；未重启服务、未迁移余额、未开启收款。

## 服务实证

- 公开域名：`api.miraphant.com`，状态接口报告 `v0.6.10`。
- 主机架构：`x86_64`。
- 服务：systemd `one-api`，状态 `active`，运行用户 `oneapi`。
- 工作目录：`/var/lib/one-api`。
- unit：`/etc/systemd/system/one-api.service`。
- 实际运行二进制：`/opt/one-api/one-api`。
- 二进制 SHA-256：`e891ff7c9502ecf1dfe9c02062a2c945286a05b01134e857c2343d7f0db2ca18`。
- 数据库：`/var/lib/one-api/one-api.db`，SQLite，文件大小 151552 字节；核验工具 SQLite 3.40.1。
- 只读统计：2 个用户、0 个模型渠道、2 个访问密钥、3 条兑换码记录、2 条日志。

未读取或输出模型密钥、用户凭证、兑换码内容或客户个人信息。模型渠道为零已经由数据库只读统计确认，不能开放“充值后即可使用”的承诺。

## 一致性备份与隔离恢复检查

备份时间：`2026-09-27T10:21:04Z`。

- 服务器备份目录：`/var/backups/miraphant/gateway-20260927T102104Z`（仅运维可读）。
- 使用 SQLite backup API 生成一致性快照，未直接复制正在写入的数据库文件。
- 数据库备份 SHA-256：`10346aa51293a04cfb1071f81c34954cc4dc5e042e00aed217f5a635dbe75d7c`。
- 同步保留 systemd unit 和对应 nginx 站点配置；内容留在服务器受限目录，不进入仓库。
- 将快照复制到临时目录，独立打开后执行 `PRAGMA integrity_check`，返回 `ok`。
- 恢复副本各表计数与基线一致；临时验证副本已清理，正式备份及 manifest 保留。

## 尚未证明的事项

已进一步用恢复副本和当前生产二进制，在 systemd PrivateNetwork 隔离网络内启动临时网关：`/api/status` 返回 HTTP 200、success=true、v0.6.10，首页返回 HTTP 200。检查后终止临时进程并清理临时数据库；未请求上游模型或支付服务，也未重启生产服务。

这证明当前快照能以原二进制启动，不等于新版迁移兼容、生产切换或发生新交易后的恢复验证通过。

主 agent 随后下载 [上游 v0.6.10 的 Linux 资产 one-api](https://github.com/songquanpeng/one-api/releases/download/v0.6.10/one-api) 到隔离工作目录，仅计算哈希并读取 Go 构建元数据，没有执行下载文件。文件大小 67948352 字节，SHA-256 为 `e891ff7c9502ecf1dfe9c02062a2c945286a05b01134e857c2343d7f0db2ca18`，与生产二进制完全相同。构建元数据报告 Linux/amd64、`vcs.revision=3915ce9814b8261a1ab13ed93adec58b463cd75c`，对应纳管的上游提交。

发布资产同时标记 `vcs.modified=true`：已确认生产运行文件等同官方发布资产、基准 Git 提交一致；不据此宣称纯净源码重建能够得到逐字节相同的原始二进制。新 Miraphant 产物仍须记录自身提交、未提交改动状态和哈希。

本地为 Apple Silicon；本地编译成功不能证明 Linux x86_64 发布包可用。正式替换前必须构建并在隔离环境验证目标架构包、记录其源码提交与 SHA-256。

商户未开通、上游模型渠道未配置、历史余额转换比例未批准。当前备份不是永久可用的迁移恢复点；实际切换前必须在排空在途业务后重新备份并核对。

# Bingdi Bots MVP 开发进展

> 开发基线 `87c1021`，分支 `codex/mvp`；产品合同为 [MVP 设计](MVP设计.md)，执行依赖为 [Chronotope 应用接入契约](../../chronotope/docs/应用接入契约.md)。本轮从 HTTP 骨架开始实现，不以参考 fixture 代替 Bots 产品实现。

| 阶段 | 状态 | 完成证据 |
|---|---|---|
| G0：确定性知识、提醒与恢复闭环 | 开发中 | 待验证：真实 PG 事务、五工具、签名入站、唯一 sender、Chronotope run/timer、重复与故障窗口 |
| G1：真实模型与 Slack 私有安装 | 未开始 | 待验证：指定测试频道真实 message ts、等待中重启、知识跨 session、启停与审计；目前尚无 Slack 安装配置 |
| G2：最小管理与一次 SOP 改进 | 未开始 | 待验证：管理页、固定模型与独立 fixture 的候选对照、人工发布与新 session 反馈 |

## 开发记录

- 2026-10-19：核对两仓库当前源码。Chronotope 运行时已经实现；Bots 尚无知识、任务、Slack 或业务数据库。采用一个 Bots Go 服务、同 PG 服务独立 schema、小型 HTTP client、固定服务认证及单次 timer。
- 本地和进程环境未发现 Slack token/signing secret/workspace/测试频道配置。先推进 G0 和其余独立实现；真实 Slack 验收的外部配置与发送授权单独确认，不能把 sink 的 message ts 记作真实 Slack 通过。
- 每次功能变更配套适用验证并按职责原子提交。故障恢复保留 PG/Restate 数据卷；真实模型、Slack sink 和真实 Slack 的证据分开记录。

## 完成审计

完成前逐项核对主合同 §6 的验收矩阵、G0/G1/G2、当前代码与运行证据。缺外部配置时继续完成独立工作，保留未验证状态；不将部分骨架或 fake 通过改写为完整 MVP 发布。

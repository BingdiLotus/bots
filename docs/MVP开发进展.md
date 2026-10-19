# Bingdi Bots MVP 开发进展

> 开发基线 `87c1021`，分支 `codex/mvp`；产品合同为 [MVP 设计](MVP设计.md)，执行依赖为 [Chronotope 应用接入契约](../../chronotope/docs/应用接入契约.md)。本轮从 HTTP 骨架开始实现，不以参考 fixture 代替 Bots 产品实现。

| 阶段 | 状态 | 完成证据 |
|---|---|---|
| G0：确定性知识、提醒与恢复闭环 | 通过 | 实际 PG tests/race、五工具、签名入站、唯一 sender、真实 Bots/Chronotope/Restate 进程联调；[G0 证据](evidence/MVP-G0-2026-10-19.md) |
| G1：真实模型与 Slack 私有安装 | 部分通过，待外部安装 | [真实模型＋sink](evidence/MVP-真实模型与Sink-2026-10-19.md)通过五分钟等待、重启、确定性发送及新 session recall；用户确认沿用 Slack 免费自建 Bot，真实 workspace/频道/凭据引用尚待配置，未发送真实 Slack 消息 |
| G2：最小管理与一次 SOP 改进 | 未开始 | 待验证：管理页、固定模型与独立 fixture 的候选对照、人工发布与新 session 反馈 |

## 开发记录

- 2026-10-19：核对两仓库当前源码。Chronotope 运行时已经实现；Bots 尚无知识、任务、Slack 或业务数据库。采用一个 Bots Go 服务、同 PG 服务独立 schema、小型 HTTP client、固定服务认证及单次 timer。
- 本地和进程环境未发现 Slack token/signing secret/workspace/测试频道配置。先推进 G0 和其余独立实现；真实 Slack 验收的外部配置与发送授权单独确认，不能把 sink 的 message ts 记作真实 Slack 通过。
- 每次功能变更配套适用验证并按职责原子提交。故障恢复保留 PG/Restate 数据卷；真实模型、Slack sink 和真实 Slack 的证据分开记录。
- 2026-10-19：实现 `internal/chronotope` 薄 HTTP client、`internal/slack` 签名/事件/单次发送、原生 domain/store 八类记录和五工具、HTTP 管理/内部回调及 durable dispatcher；不复制 Chronotope 的模型历史或计时调度。PG 成为首导入后的配置权威。
- 首次正式 G0 及最终 G0 均通过；最终版额外严格检查五工具 schema/description、所有 call/result IDs、随机知识 ID、实际 Restate Sleep 和运行时 LLM 调用账本。45 秒等待中 SIGKILL Bots、重启 worker，暂停期间接纳 fire，恢复后确定性发送；新 session recall 相同知识。三条 sink 回执均 confirmed，不能记作真实 Slack 通过。
- 补齐真实 PG 故障测试：四类 RPC 接纳响应丢失后稳定 key 修复、工具回调先于 run handle、Task/outbox 原子回滚、CAS/旧 fire、unknown/迟到 receipt、429、来源失效和 tenant/Bot/scope 隔离。单 tenant 租约在 HTTP 开放前取得；专用 PG backend 失效后停止新派发，在途确认仍落账。
- 账号/频道/凭据引用及 bridge/timer 部署绑定固定首导入；人格/SOP、模型、时区及限额可在线 CAS 升版，已有 thread 冻结原配置，新 thread 使用新版本。修复 Slack `message` 与 `app_mention` 的同消息跨订阅去重。
- 真实模型第一次使用 90 秒测试等待，第一轮模型约 49 秒，第二轮尚在途时被测试清理中断，该次**未通过**。保留其 dispatched/failed 证据；未自动重派旧模型调用。改为 210 秒验收等待和至少 300 秒提醒，使用新隔离场景重验。
- 扩大窗口后的真实 `claude-sonnet-4-6`＋sink 已通过：五分钟实际 Restate Sleep、Bots/worker 重启、暂停到点再恢复发送、不同 session recall；等待前/暂停到点后/恢复发送后 LLM call count 均为 2，recall 后为 4，三个实际 sink TS 均已 confirmed。两次 run 上报总计 3,862 tokens，非真实 Slack 验收。
- `make check`、带隔离 `bots_test` 的 `go test -race ./...` 通过；CI 增加 PostgreSQL 16 服务，防止只跑跳过 PG 的单元测试。尚未推送本轮提交、未取得本轮 GitHub CI 结果；容器镜像构建/运行仍未验证。
- 原子提交已完成：`4f962c1` HTTP client、`a048e68` Slack adapter、`360a311` 原生账本、`cc8106f` 完整编排与管理 API、`be1c391` 数据库启动错误脱敏、`2910b2d` 事务故障/进程联调及 CI；运行文档与证据另作一次文档提交。

## 当前部署与下一步

运行配置、Slack 免费 App 权限/订阅、管理 API 与独立 G0/真实模型＋sink 入口见[运行与验收](运行与验收.md)。其它渠道的免费条件见[免费测试渠道](research/免费测试渠道.md)，当前不扩适配器。

下一发布门需要指定 Slack workspace、Bot user、测试频道，以及本地 token/signing secret 的环境引用。真实发送前明确测试内容和频道；只有 Slack API 返回的真实 message ts 与任务/审计一同落账，才能关闭 G1。G2 管理页及一次人工批准/拒绝的 SOP 改进仍未实施，待真实轨迹后推进；没有将 G0 当作完整 MVP 发布。

## 完成审计

完成前逐项核对主合同 §6 的验收矩阵、G0/G1/G2、当前代码与运行证据。缺外部配置时继续完成独立工作，保留未验证状态；不将部分骨架或 fake 通过改写为完整 MVP 发布。

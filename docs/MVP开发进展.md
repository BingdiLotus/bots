# Bingdi Bots MVP 开发进展

> 开发基线 `87c1021`，分支 `codex/mvp`；产品合同为 [MVP 设计](MVP设计.md)，执行依赖为 [Chronotope 应用接入契约](../../chronotope/docs/应用接入契约.md)。本轮从 HTTP 骨架开始实现，不以参考 fixture 代替 Bots 产品实现。

| 阶段 | 状态 | 完成证据 |
|---|---|---|
| G0：确定性知识、提醒与恢复闭环 | 通过 | 实际 PG tests/race、五工具、签名入站、唯一 sender、真实 Bots/Chronotope/Restate 进程联调；[G0 证据](evidence/MVP-G0-2026-10-19.md)与[提交 timer 前实际 SIGKILL 补验](evidence/MVP-管理页与崩溃恢复-2026-10-19.md) |
| G1：真实模型与 Slack 私有安装 | 部分通过，待外部安装 | [真实模型＋sink](evidence/MVP-真实模型与Sink-2026-10-19.md)通过五分钟等待、重启、确定性发送及新 session recall；用户确认沿用 Slack 免费自建 Bot，真实 workspace/频道/凭据引用尚待配置，未发送真实 Slack 消息 |
| G2：最小管理与一次 SOP 改进 | 管理页通过，改进闭环待来源 | [管理页实际 PG/浏览器验收](evidence/MVP-管理页与崩溃恢复-2026-10-19.md)通过；真实纠正轨迹、固定模型/独立 fixture 对照、人工决议及新 session 反馈尚缺，[操作合同](SOP改进验收.md)已明确 |

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
- `make check`、带隔离 `bots_test` 的 `go test -race ./...` 通过；CI 增加 PostgreSQL 16 服务，防止只跑跳过 PG 的单元测试。尚未推送本轮提交、未取得本轮 GitHub CI 结果；容器当前证据范围见下方补验。
- 原子提交已完成：`4f962c1` HTTP client、`a048e68` Slack adapter、`360a311` 原生账本、`cc8106f` 完整编排与管理 API、`be1c391` 数据库启动错误脱敏、`2910b2d` 事务故障/进程联调及 CI；运行文档与证据另作一次文档提交。
- 续轮逐条审计发现：旧 G0 已证明等待中重启，但“Task 提交后、timer 接纳前”只有事务/dispatcher 回归，没有指定断点的真实 OS kill。本轮新增透明测试代理，观察原 Task pending_schedule、实际 runtime 404/零转发后 SIGKILL（-9），同 PG 重启后原 timer/revision/hash 修复为实际 handle，LLM calls 仍为 2；再完成原等待中第二次重启和新 thread recall。没有改产品调度逻辑或增加测试开关。
- `/admin` 已以 Go embed 实现，无新前端构建链。实际 PG、Chrome 155/Playwright 的桌面 1440×1000 和窄屏 390×844 通过：登录、配置保存、并发 CAS 冲突保留输入、Bot/账号启停、scope 过滤、知识停用/审计、三条回执及键盘标签。凭据仅内存，退出/刷新清理私有 DOM，无持久存储或 URL/cookie 凭据；除刻意触发的 401/409 外无运行错误。独立只读审查未发现越权/XSS/CAS/stale-response 问题。
- 容器构建器固定为已核查的 Go 1.26.9 digest。默认模块代理在 Docker 网络 TLS 超时，标准 GOPROXY build arg 后构建通过；非 root scratch 镜像含 CA，实际容器启动、PG 管理查询、已有 confirmed 记录和管理页资源 smoke 通过。该容器没有重做模型/timer 全链路，不能据此扩大运行时或真实 Slack 验收范围。
- 管理页测试只修改隔离 fixture 的测试配置并停用测试知识；没有生成真实 SOP 候选、替人批准、把 fake 分数当改进收益，或更改真实 Bot。一次改进需真实来源及人工决议，先记录现有 API 的发布/拒绝/新 revision 回退操作合同，不新增提案/评测平台。
- 续轮原子提交：`64633d7` 内嵌管理页，`af68c66` 提交 timer 前真实进程崩溃与交互 QA 入口，`445a50e` 固定容器构建器/模块缓存。最终 `make check`、实际 PG 全仓 race、完整 fake E2E/浏览器 QA 与镜像构建/只读 smoke 通过；临时服务/容器已停止，原 Chronotope 栈与数据卷保留，截图和浏览器中间产物移至 `/tmp`。

## 当前部署与下一步

运行配置、Slack 免费 App 权限/订阅、管理 API 与独立 G0/真实模型＋sink 入口见[运行与验收](运行与验收.md)。其它渠道的免费条件见[免费测试渠道](research/免费测试渠道.md)，当前不扩适配器。

下一发布门需要指定 Slack workspace、Bot user、测试频道，以及本地 token/signing secret 的环境引用。续轮只读核查仍未发现这两项凭据，且默认 seed/.env 尚未配置；这是同一外部依赖。真实发送前明确测试内容和频道；只有 Slack API 返回的真实 message ts 与任务/审计一同落账，并完成现场权限撤回验证，才能关闭 G1。G2 管理页已通过，一次人工批准/拒绝的 SOP 改进仍待真实纠正轨迹、独立对照和人工决议；没有将部分完成当作完整 MVP 发布。

## 完成审计

完成前逐项核对主合同 §6 的验收矩阵、G0/G1/G2、当前代码与运行证据。缺外部配置时继续完成独立工作，保留未验证状态；不将部分骨架或 fake 通过改写为完整 MVP 发布。

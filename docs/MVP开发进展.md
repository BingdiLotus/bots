# Bingdi Bots MVP 开发进展

> 开发基线 `87c1021`，分支 `codex/mvp`；产品合同为 [MVP 设计](MVP设计.md)，执行依赖为 [Chronotope 应用接入契约](../../chronotope/docs/应用接入契约.md)。2026-10-20 的真实 Slack G1 验收使用 `b4e8ee3`，随后以 `29d1426`、`30b0dfa` 修复验收中发现的 recall 与 run binding 缺陷。本轮从 HTTP 骨架开始实现，不以参考 fixture 代替 Bots 产品实现。

| 阶段 | 状态 | 完成证据 |
|---|---|---|
| G0：确定性知识、提醒与恢复闭环 | 通过 | 实际 PG tests/race、五工具、签名入站、唯一 sender、真实 Bots/Chronotope/Restate 进程联调；[G0 证据](evidence/MVP-G0-2026-10-19.md)与[提交 timer 前实际 SIGKILL 补验](evidence/MVP-管理页与崩溃恢复-2026-10-19.md) |
| G1：真实模型与 Slack 私有安装 | 通过 | [真实 Slack Socket Mode、知识/提醒、等待中 SIGKILL 重启与跨会话 recall](evidence/MVP-真实Slack与Socket-2026-10-20.md)：真实 `claude-sonnet-4-6`、6 条 confirmed Slack ts、2 条有效知识/2 个完成任务；首次 recall 空结果与后续 binding 滞留均保留并修复后重验 |
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
- 2026-10-20：新增 Socket Mode 入口（`b4e8ee3`）。内部 App 使用 app-level token 建立真实 WebSocket，不再要求公网签名 webhook；`event_transport` 与 `app_token_env` 固定首导入，Socket-only 安装的 webhook 路由返回 404。两种入口复用同一 installation 绑定、事件归一化和持久 inbox，先持久接纳再 ACK，接纳错误/2 秒超时保持未 ACK。SDK 连接/reconnect、认证失败、原始 payload 和 ACK 语义有独立回归；固定 `slack-go/slack v0.30.1`。
- 2026-10-20：`806f621` 修复重启时重复执行 `CREATE INDEX IF NOT EXISTS` 引发的表锁死锁风险；schema readiness 从 DDL 自省，缺失才修复，Bootstrap 回归在独立临时数据库运行。`make check`、实际 PG 全仓 race 通过；fake E2E 七项全过（含 timer 接纳前 SIGKILL、等待中重启、暂停 fire、恢复发送和新 session recall），报告哈希在真实 Slack 证据文件中。
- 2026-10-20：真实 G1 通过。真实成员在私有测试频道 @Bot，真实模型一轮执行 `remember + create_reminder`（2 次 LLM 调用，2,174 tokens），保存知识和原生提醒；最终回复与到点提醒分别取得真实 Slack ts。随后在 5 分钟等待中实际 SIGKILL Bots PID 16133，重启后原 task/timer/revision/handle 不变，到点仍发出提醒，等待与发送期间无新 LLM 调用；新 thread/session 在进程重启后 recall 到原知识、来源和日期。共 6 条 confirmed delivery、2 条有效知识、2 个完成任务；管理员 API 启停与审计复验。
- 2026-10-20：真实验收发现并修复两个缺陷，失败轨迹保留。`29d1426`：`recall` 原先把整段 query 当子串，模型给出多个自然关键词时返回空；改为有界关键词匹配、命中数排序和字面 `strpos`。`30b0dfa`：dispatcher 用当前二进制重算 immutable agent 的完整工具元数据，工具描述变化后已完成的 canonical run 被拒绝收集；改为比较治理配置并保留工具 ref/可信上下文校验。修复后同一 canonical run 在重启后补发真实 Slack 最终回复，证明不重放模型。
- 2026-10-20：真实 Slack 证据、6 条 delivery、重启 PID/timer 和两次缺陷的失败/修复哈希见[真实 Slack 与 Socket 验收](evidence/MVP-真实Slack与Socket-2026-10-20.md)。真实验收结束后停止临时 Bots 进程，保留 Chronotope 栈和 PG/Restate 数据卷；没有删除失败 recall 的实际 Slack 消息。

## 当前部署与下一步

运行配置、Socket Mode/webhook 两种 Slack 入口、管理 API 与分层验证见[运行与验收](运行与验收.md)；真实 G1 结果、失败轨迹和边界见[真实 Slack 与 Socket 验收](evidence/MVP-真实Slack与Socket-2026-10-20.md)。真实 workspace、测试频道和凭据引用只保存在本地 gitignored `configs/bot.yaml` 与 `.env*`，不写入仓库；更换 workspace/频道/账号需要新 Bot 或明确的开发重建。

G1 已关闭。G2 仍待一次真实、可由 SOP 改善的纠正轨迹：从该轨迹冻结 baseline、生成一条 instructions 候选及可检验假设，在隔离身份/初始知识/固定模型与 sink 上做 baseline/candidate 配对，再由人批准或拒绝并在新 thread 验证、记录反馈和回退；操作合同见[SOP 改进验收](SOP改进验收.md)。本轮 recall 空结果属于代码缺陷，已由代码和 PG 回归修复，不作为 SOP 改进收益。完成这些之前不宣布完整 MVP 发布。

## 完成审计

完成前逐项核对主合同 §6 的验收矩阵、G0/G1/G2、当前代码与运行证据。G1 已通过真实 Slack 证据关闭；G2 未完成。不将部分骨架、fake 通过或 G1 结果改写为完整 MVP 发布。

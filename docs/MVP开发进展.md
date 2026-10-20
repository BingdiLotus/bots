# Bingdi Bots MVP 开发进展

> 开发基线 `87c1021`，分支 `codex/mvp`；产品合同为 [MVP 设计](MVP设计.md)，执行依赖为 [Chronotope 应用接入契约](../../chronotope/docs/应用接入契约.md)。2026-10-20 的真实 Slack G1 验收使用 `b4e8ee3`，随后以 `29d1426`、`30b0dfa` 修复验收中发现的 recall 与 run binding 缺陷；同日完成一次 G2 SOP 改进并发布真实 Bot revision 2。本轮从 HTTP 骨架开始实现，不以参考 fixture 代替 Bots 产品实现。

| 阶段 | 状态 | 完成证据 |
|---|---|---|
| G0：确定性知识、提醒与恢复闭环 | 通过 | 实际 PG tests/race、五工具、签名入站、唯一 sender、真实 Bots/Chronotope/Restate 进程联调；[G0 证据](evidence/MVP-G0-2026-10-19.md)与[提交 timer 前实际 SIGKILL 补验](evidence/MVP-管理页与崩溃恢复-2026-10-19.md) |
| G1：真实模型与 Slack 私有安装 | 通过 | [真实 Slack Socket Mode、知识/提醒、等待中 SIGKILL 重启与跨会话 recall](evidence/MVP-真实Slack与Socket-2026-10-20.md)：真实 `claude-sonnet-4-6`、6 条 confirmed Slack ts、2 条有效知识/2 个完成任务；首次 recall 空结果与后续 binding 滞留均保留并修复后重验 |
| G2：最小管理与一次 SOP 改进 | 通过 | [管理页实际 PG/浏览器验收](evidence/MVP-管理页与崩溃恢复-2026-10-19.md)；[一次真实 SOP 改进](evidence/MVP-SOP改进-2026-10-20.md)：真实纠正、冻结 fixture、真实模型配对、用户批准、revision 2 发布及真实新 thread 验证 |

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
- 2026-10-20：G1 判定口径：真实 Slack 覆盖新知识、原生提醒、真实 message ts、等待中崩溃重启、新 session recall、管理员启停/审计；十次投递去重、unknown/迟到 receipt、改期/取消竞态、执行限额和跨 tenant/bot/scope 仍由 G0 的真实 PG/fake provider 回归覆盖，没有在真实 Slack 上逐行注入故障。真实与 fake 证据分开记录，不把 fake 行算作真实渠道通过。
- 2026-10-20：真实验收发现并修复两个缺陷，失败轨迹保留。`29d1426`：`recall` 原先把整段 query 当子串，模型给出多个自然关键词时返回空；改为有界关键词匹配、命中数排序和字面 `strpos`。`30b0dfa`：dispatcher 用当前二进制重算 immutable agent 的完整工具元数据，工具描述变化后已完成的 canonical run 被拒绝收集；改为比较治理配置并保留工具 ref/可信上下文校验。修复后同一 canonical run 在重启后补发真实 Slack 最终回复，证明不重放模型。
- 2026-10-20：真实 Slack 证据、6 条 delivery、重启 PID/timer 和两次缺陷的失败/修复哈希见[真实 Slack 与 Socket 验收](evidence/MVP-真实Slack与Socket-2026-10-20.md)。真实验收结束后停止临时 Bots 进程，保留 Chronotope 栈和 PG/Restate 数据卷；没有删除失败 recall 的实际 Slack 消息。
- 2026-10-20：G2 完成一次真实 SOP 改进。来源是 G1 新 thread recall 回复主动展开交叉记录；用户确认纠正“只引用最相关一条来源，不主动展开其它记录”。真实模型生成候选 patch（run `r_986950cb…`），独立 5-case fixture/checker 在候选 v2 前冻结；第一次 v1 fixture 未复现 baseline 差异，作为 null 结果保留并改用 v2。
- 2026-10-20：隔离配对使用两个 test tenant、相同初始知识和真实 `claude-sonnet-4-6`，只连接计数 sink。目标 case：baseline 列出已废弃 `2026-10-18` 初版记录，candidate 只返回 `2026-10-20`、`b4e8ee3` 和一条来源；无 candidate-only guard regression。两处 checker 误报（baseline 日期格式、两臂共同的提醒措辞）原始保留。成本 baseline 9 calls/9,277 tokens、candidate 9/9,525、proposal 827；sink 回执全部 confirmed。
- 2026-10-20：用户于 09:23:44 批准；通过管理 API CAS 发布 revision 2（agent `a_e1497669…`，仅 instructions 变化）。真实 Slack 新 thread 验证：session revision 2、run `r_32013c8d…`、2 次 LLM 调用/2,265 tokens，最终只返回当前记录/来源，真实 ts `1792459570.858859`，旧 thread 仍为 revision 1；反馈 pass，无回退。证据见[一次 SOP 改进](evidence/MVP-SOP改进-2026-10-20.md)。
- 2026-10-20：P0 上线准备第一部分（`f61ec45`）：新增 `/readyz`，检查 PG、dispatcher 租约、最近 60 秒成功 Step 和当前 Socket 连接；新增 `bots readyz` 二进制探针与 Docker HEALTHCHECK。真实进程 `/readyz` 200、二进制探针退出 0；新增 HTTP/Engine/Socket/探针回归，`make check` 与实际 PG race 通过。镜像 `bingdi-bots:p0` 构建成功，healthcheck 配置已核对；未在容器内重做完整模型/timer 链路。
- 2026-10-20：P0 上线准备第二部分（`35cc9bf`）：加入 systemd/compose 模板、Slack scope allowlist 检查和 Bots schema 备份/恢复脚本。容器 PG16 恢复演练通过（`bots.bots` 17/17、`audit_events` 273/273）；宿主机 pg_dump 17 对 PG16 的版本不匹配已被脚本拦截，`pg_restore --schema` 丢失 `CREATE SCHEMA` 的问题已修正。当时实际 Slack token 仍含 `im:*`、`reactions:*`、`users:read`、`assistant:write`、`channels:read` 等多余 scopes，scope 检查退出 1；该项已在同日晚些时候完成，见下一条。
- 2026-10-20：Slack 最小权限已完成。CLI 登录（A）后可读远端 manifest；`admin.apps:read/write`（B）位于 user scopes，导致 `apps.developerInstall` 返回 `restricted_scopes`，已确认移除全部 user scopes。最终 manifest 只保留 4 个 bot scopes，移除 bot_optional、未使用 user/channel/message.im events 和 Agent View；before/final SHA-256 `bc432d0a…`/`10a2bdb2…`。uninstall/install 轮换 token，旧 token `invalid_auth`；新 bot/app-level token 写回本地 env（0600），`scripts/check-slack-scopes.sh` 退出 0，Bot 重新邀请入频道，真实 smoke run `r_349e76f2…`、ts `1792479946.186979`、2,268 tokens。
- 2026-10-20：unknown 人工处置完成（`e6a078c`）。管理 API 只接受 `unknown`；confirmed 必须带真实 provider message ID，rejected 必须带理由；行锁更新 delivery 和匹配 task（completed/canceled），写 `delivery_resolved` 审计，不调用 provider、不重试。PG/HTTP 回归覆盖状态、重复解析、跨 Bot 与非法输入。
- 2026-10-20：部署/备份继续核查。`docker compose config` 对 `deploy/compose.bots.yml` 静态解析通过（healthcheck、127.0.0.1 绑定、只读 rootfs、restart、外部网络）；systemd 未在 Linux 执行。Chronotope 已有 D1 PITR、D3 Restate 卷恢复、D2 RustFS 回放脚本；Bots schema 与 Chronotope 同库，但本轮未执行破坏性全栈演练，联合 RTO/RPO 仍未证明。
- 2026-10-20：Chronotope D1 PG PITR 隔离演练 6/6 通过（WAL 归档、basebackup、目标时刻回卷、目标后数据消失），未触碰现有 PG/Restate/Bots 数据，临时容器/卷已清理。D3 Restate 卷恢复与 D2 RustFS 未执行；Bots + Chronotope + Restate 联合 RTO/RPO 仍未证明。
- 2026-10-20：真实权限撤回演练通过。成员创建 5 分钟提醒；到点前将 Bot 移出私有频道，Slack 返回 `channel_not_found`，delivery `rejected`、task `blocked`、attempts 1，12 秒后无重试；Bot 重新邀请后 members=2、`/readyz` 200。演练留下 blocked task 和测试知识，待试用数据清理。
- 2026-10-20：Docker 容器实跑通过。`bingdi-bots:p0` 以只读 rootfs、65532、`no-new-privileges`、127.0.0.1:8090 启动，healthcheck healthy、`/readyz` 200、Socket connected；真实 recall 经 Chronotope bridge→宿主→容器的工具回调并 confirmed（run `r_70081bcc…`，ts `1792484302.646889`）。
- 2026-10-20：Restate D3 全栈恢复演练通过 5/5（卷快照、清空、恢复、worker 重注册、awakeable resolve），总墙钟 130.4s；API auth 临时关闭后恢复 on，harness fake 恢复 0，健康均 200，无残留。D2 RustFS 未执行；D1+D3 仍不是完整联合 RTO。
- 2026-10-20：P0 **尚未完成**。systemd 目标主机未执行；D2/联合 RTO 未完成；新试用频道/Bot binding 与数据清理未做。见[P0 上线准备证据](evidence/P0-上线准备-2026-10-20.md)。

## 当前部署与下一步

运行配置、Socket Mode/webhook 两种 Slack 入口、管理 API 与分层验证见[运行与验收](运行与验收.md)；真实 G1 结果、失败轨迹和边界见[真实 Slack 与 Socket 验收](evidence/MVP-真实Slack与Socket-2026-10-20.md)。真实 workspace、测试频道和凭据引用只保存在本地 gitignored `configs/bot.yaml` 与 `.env*`，不写入仓库；更换 workspace/频道/账号需要新 Bot 或明确的开发重建。

G1/G2 的代码与真实纵向门禁已通过，但**受控试用尚未开始**：P0 上线准备仍在进行。Slack scopes、unknown 人工处置、权限撤回演练、Docker 容器和 D1/D3 恢复已完成；完成前仍需在目标主机验证 systemd、补 D2/联合 RTO，并为新试用频道建立新的 Bot binding/清理数据。清单与未完成项见[部署与试用](部署与试用.md)和[P0 证据](evidence/P0-上线准备-2026-10-20.md)。

真实 Bot 仍是一个 workspace/测试频道、一个模型和一个渠道；SOP 只在一条目标 case 和四个 guard 上验证，且有两处 checker 误报已如实保留。后续真实使用反馈若显示候选造成回退，按新 revision 发布 baseline 内容（不倒退 revision）并记录；未来改进继续按[SOP 改进验收](SOP改进验收.md)冻结来源、fixture 和人工决议，不建设自动提案/评测平台。

## 完成审计

完成前逐项核对主合同 §6 的验收矩阵、G0/G1/G2、当前代码与运行证据。G0 使用真实 PG/fake provider 覆盖故障与隔离矩阵；G1 使用真实 Slack 纵向路径；G2 完成一次真实来源、隔离配对、人工批准和真实 thread 复验。G1 的十次投递、unknown/迟到 receipt、改期/取消竞态、限额和跨 tenant 行仍由 G0/PG/fake 回归覆盖，未在真实 Slack 逐项注入；G2 的 checker 误报和单次改进边界保留在证据中。P0 上线准备仍有 systemd 目标主机、D2/联合 RTO 和新试用频道/数据清理阻塞项；Slack scopes、unknown 处置、权限撤回演练、Docker 容器和 D1/D3 已完成。不将部分骨架、fake 通过、单次 SOP 改进、G1 结果或部分 P0 工作改写为完整生产发布。

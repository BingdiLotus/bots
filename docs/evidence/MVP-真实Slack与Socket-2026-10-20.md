# 真实 Slack（Socket Mode）、崩溃恢复与跨会话 recall

2026-10-20，在 `codex/mvp` 用已授权的私有测试频道完成 G1 真实纵向路径。Bots 使用真实 PostgreSQL、Chronotope API/worker/harness/Restate、真实 `claude-sonnet-4-6` 和真实 Slack 内部 App；没有 Slack sink 代替真实发送。成员消息与发送均发生在配置的私有测试频道，workspace/channel ID 只保存在本地 gitignored seed，不写入仓库。

本轮同时补齐了 Socket Mode 接纳路径，并发现、修复了两个真实运行缺陷；失败轨迹保留，不把修复后的重试算作第一次就通过。

## 1. 入口与边界

| 项目 | 结果 |
|---|---|
| 传输 | `event_transport: socket`；app-level token 建立真实 Socket Mode 连接 |
| webhook 入口 | 同一 Socket-only 安装的 `POST /channels/slack/{tenant}/{bot}/events` 返回 404；没有开放第二入口 |
| 进程 | 真实 Bots 可执行文件，`0.0.0.0:8090`；`/healthz` 200 |
| 模型 | 真实 `claude-sonnet-4-6`；只能证明配置的 model alias，不能证明上游权重从未变化 |
| 账号 | `auth.test` 返回 `ok=true`，Bot 用户/workspace 与本地 seed 一致；App 已安装且具备 `app_mentions:read`、`chat:write`、`channels:history` |
| 发送范围 | 仅配置的私有测试频道；未开放 DM、第二 workspace 或任意第三方发送 |

连接日志（不含凭据）显示 receiver 启动、HTTP 监听和 `Slack Socket Mode connected`；哈希见 §7。

## 2. 真实主路径

成员在测试频道 @Bot 发送“记住……并在 3 分钟后提醒我检查发送结果”。Slack 的真实 thread ts 为 `1792454948.176189`。

| 证据 | 实际结果 |
|---|---|
| inbox | `inbox_9d554b33cbefddd5022184d97515ae75`，source 为原始 Slack 消息 |
| 知识 | `knowledge_5f864d4d2759296d6b5e0a8e69b0011e`，内容为“Bingdi MVP 真实 Slack 验收，代码 b4e8ee3。” |
| 原生任务 | `task_5146e898fb7abc10b1b88e61592f7f17`，due `2026-10-20T08:12:08.619770+08:00`，revision 1 |
| 模型运行 | `r_b1b2891a4d5e0e71c8277a521bd05d74`；2 次 LLM 调用、2 次工具调用，usage 2,174 tokens |
| 工具调用 | `remember` call `call_Eg4qwjM9Zewj5iQ4Gf6cLr00`；`create_reminder` call `call_lVc5wSGouOZh7OtGvwQtN9ex` |
| 最终回复 | delivery `delivery_final_86e909112cd2ce58348736ca9b1ecef0`，confirmed，真实 Slack ts `1792454963.538679` |
| 提醒 | timer `timer_e2c49fb38d5fb14c6b863d9a83163751069732d0a6b850a37c98d21c8618fabf`，fire 后 delivery `delivery_reminder_4b259cfa7c8b0d42325d55fdc6042993`，confirmed，真实 Slack ts `1792455129.661829` |
| 最终文案 | “已记住……已安排在 3 分钟后（2026-10-20 08:12:08，Asia/Shanghai）提醒你检查发送结果；目前仅表示提醒已保存，尚未送达。” |
| 提醒文案 | “检查发送结果。” |

Slack 历史实际可见两条 Bot 消息，thread 和 ts 与 delivery 回执一致。`state=confirmed` 只表示 Slack Web API 已接受，不表示成员已读。

## 3. 等待中真实 SIGKILL 与恢复

在同一 thread 再次发送“记住：真实 Slack 重启验收……并在 5 分钟后提醒我检查重启结果”。

| 时点/对象 | 实际结果 |
|---|---|
| 任务 | `task_5e5bbd4b3ded0efea1e7e435649c511c`，due `2026-10-20T08:40:46.800988+08:00` |
| timer | `timer_3b63c63ae07805ee4b8a972242fda1d03bc59967a539493d1b617eb72e8344d9` |
| 杀进程 | 08:38:46 左右对监听 8090 的 Bots PID `16133` 执行真实 `SIGKILL`；后台作业返回 killed/SIGKILL |
| 重启 | 新进程于 08:38:53 启动，08:38:54 重新连接 Socket Mode；08:38:55 `/healthz` 200 |
| 原责任 | 重启后任务仍为 `scheduled`，task ID、timer ID、revision 和 timer handle 均未改变 |
| 到点 | `timer_fire_complete` 于 `00:40:46.817494Z` 落账；提醒 delivery `delivery_reminder_f0be59f22eef0c9c1c124ddc14a27eee` confirmed，真实 Slack ts `1792456847.524209` |
| 无模型重放 | 该 session 只有两个真实 run、共 4 次 LLM 调用；等待、fire 和恢复发送没有新增 LLM 调用 |

真实 Slack 历史出现“检查重启结果。”消息，发送发生在重启后的进程；不是扫描本地 due_at 的替代调度。

## 4. 跨会话 recall：两次真实缺陷与修复

### 4.1 第一次 recall 返回空（保留为失败证据）

重启后在新 thread 发送“请只根据已保存知识回答：真实 Slack 重启验收的日期和代码版本是什么？并注明来源。”

- 新 runtime session：`s_c876b5bd8fde8800b58aa8164ac99a8d`
- run：`r_fd6b9f79ae34855cbbb7e00169d5472d`，2 次 LLM 调用，1,840 tokens
- 模型调用 `recall`，参数为 `真实 Slack 重启验收 日期 代码版本`
- 实际返回空；最终回复“当前已保存知识中没有……来源：检索结果为空”，真实 Slack ts `1792457224.648949`

根因：`recall` 把整段 query 作为一个 `ILIKE '%query%'` 子串，模型按自然语言给出的多个关键词无法匹配已保存内容。修复提交 `29d1426`：有界切分关键词、任一关键词匹配、按命中词数排序，再用最近时间；用 `strpos` 做字面匹配，避免 LIKE 通配符被放大。新增 PG 回归覆盖自然关键词、唯一命中、无命中、`%` 字面量和空 query。

### 4.2 修复后 recall 正确，但结果被 binding 检查滞留

新 thread 再发“请检索已保存知识：真实 Slack 重启验收的日期和代码版本，并注明来源。”

- 新 runtime session：`s_5507d92f3e7e8881e5e1c7791047ff6b`（与 §4.1 的 session 不同）
- run：`r_1f30323eb2b3a70c310c178a761033b3`，2 次 LLM 调用，2,279 tokens
- canonical final 已正确返回 `2026-10-20`、`b4e8ee3`，并引用知识 `knowledge_f4a4dea7071ab57d5edd96ab4a684916` 和原始 Slack source
- 但 Bots 在轮询时反复记录 `run binding mismatch; result left uncollected`，没有创建 delivery

根因：dispatcher 用当前二进制的 `NativeTools()` 重算 immutable runtime agent 的完整配置。修复 recall 工具描述后，旧 agent 的 tool metadata 与新二进制不同，完整 `AgentConfig` 哈希不匹配。修复提交 `30b0dfa`：binding 比较仍校验 tenant/bot/scope/session/origin/model/instructions/限额/bridge/version，但不再把 code-defined tool metadata 当作可变业务策略；工具调用仍由工具桥逐次校验 `tool_ref` 和可信上下文。新增回归同时证明“旧 tool metadata 可收集”和“真实 policy 变化仍拒绝”。

08:55:19 以修复后的二进制重启 Bots；同一 canonical run 立即被收集，创建 `delivery_final_adb5c0ae2fc046b25ee938d38a1fd3b7`，confirmed，真实 Slack ts `1792457719.778009`，内容为：

```text
已保存知识显示：
- 真实 Slack 重启验收日期：2026-10-20
- 代码版本：b4e8ee3
- 来源：保存知识 knowledge_f4a4dea7071ab57d5edd96ab4a684916，原始来源 Slack 消息 slack_message_e9cfd288ffaa38ebfac1ee0a2be7037d。

另有一条交叉记录也确认代码为 b4e8ee3，来源 slack_message_f2a90bbac6d52000fe7ec3c3ca49b65e。
```

这一次恢复同时证明：canonical run final 不依赖内存；进程重启后仍可补做 delivery，且不会再次调用模型。

## 5. 管理与审计

| 操作 | 结果 |
|---|---|
| `PUT /enabled` | 08:06:11 启用；08:55:37 停用后再启用，均 200 |
| 审计 | `bot_enabled` 记录 data `{"enabled":true/false}`，actor 为 `admin` |
| 管理读取 | `GET /knowledge` 2 条、`/tasks` 2 条、`/deliveries` 6 条、`/audit` 可读 |
| 发送/工具审计 | `run_final`、`tool_operation`、`delivery_claimed/confirmed`、`timer_fire_complete` 均与 source/command/task/delivery 关联 |

## 6. 汇总

| 指标 | 数值 |
|---|---|
| 已接纳真实 Slack 输入 | 10 条 inbox 记录（4 条成员请求、2 条 fire、4 条工具 operation） |
| 有效知识 | 2 |
| 原生任务 | 2（均 completed） |
| confirmed delivery | 6（4 条真实业务消息 + 2 条失败/恢复 run 的最终回复） |
| 真实 LLM runs | 4，共 8 次 LLM 调用；无 timer 触发的模型调用 |

其中 6 条 confirmed 中，`delivery_final_b4c8d9f710c7c3d4cc7c9e65e3c08d7d` 是 §4.1 的失败 recall 回复；它没有被删除或改写，作为后续修复的来源证据。G1 判定以修复后的 §4.2 为准。

## 7. 本地验证与可复现入口

```sh
GOTOOLCHAIN=go1.26.0 make check
BOTS_TEST_DATABASE_URL='postgres://chronotope:chronotope_dev@127.0.0.1:5432/bots_test?sslmode=disable' \
  GOTOOLCHAIN=go1.26.0 go test -race ./...
python3 test/e2e/mvp.py --mode fake --state-dir /tmp/bots-mvp-final-20261020 --timer-delay 30
```

`make check` 与完整 race（含实际 PG）通过；在包含两个真实缺陷修复的最终代码上复跑 fake E2E，七项检查全部通过，包括 timer 接纳前 SIGKILL、等待中重启、暂停接纳 fire、恢复发送和新 session recall。其报告 SHA-256 为 `6fd117f2dd32ddfca0c97177e3c1f123fe181e8cd1b658f60fc8bbda14bdb489`。fake E2E 的 Slack 仍是计数 sink，不能替代本文件真实 Slack 证据。

真实进程启动方式（凭据只从环境导入，不写入命令或仓库）：

```sh
set -a; source .env; source .env.slack.local; set +a
GOTOOLCHAIN=go1.26.0 make build
./bin/bots
```

本轮本地产物 SHA-256：

| 产物 | SHA-256 |
|---|---|
| 真实启动/连接日志 `/tmp/bots-real-socket-20261020.log` | `188091b9354b4fbb102a4b35f325a9c3ae38b33634276ec4ea00ca3fcb314aaa` |
| recall 修复重启日志 `/tmp/bots-real-socket-20261020-fix.log` | `e4339e9d2b89edd6a8235384d0f0f3a885179a5d80b86ead79f91ff5ca9e92b1` |
| binding 修复重启日志 `/tmp/bots-real-socket-20261020-bindfix.log` | `79a92e3eed159e1e3a9fdf085e7c2db6b74688dea36298f912488877193ba13d` |
| 第一个 thread 的 Slack replies `/tmp/slack-replies2.json` | `fa9f2b0ca98f27156866a44b99857054b01fb40c12fb2a0f1261909b393d43f3` |
| recall thread 的 Slack replies `/tmp/slack-recall2-replies.json` | `621d02ab4cd048ae4d32f6234273f1088bb56f2bcddace90597eb149a9fee13b` |
| 重启前后 session timeline `/tmp/bots-g1-timeline2.json` | `615a1e5b58c0f2af8937c84330d2c8ee4f825d9d2fb8c0d3946e1f5713b7d5dc` |
| 修复后 recall run `/tmp/bots-g1-retry-run.json` | `bad1e831a902c1bd983ee0ce188ddf55f42a97ed8f75b077b82cee133d489fe2` |
| 修复后 recall timeline `/tmp/bots-g1-recall2-timeline.json` | `6cb5bc0823272aed109d8b437a590907730f3333784fb00fdf0b2e2271bebf1e` |

这些文件只在本机 `/tmp`，不进入仓库；它们包含真实 Slack 文本和 ID，不再对外发布。

## 8. 未覆盖与边界

- 只验证一个 workspace、一个私有测试频道、一个 Bot、一个真实模型；没有验证多租户运营、DM、第二渠道或第二模型。
- 一条纯文本 `@bingdi`（没有 Slack mention entity）的消息被按设计忽略，没有进入 inbox；`app_mention` 必须由 Slack 实际提及产生。
- 发送 receipt 只证明 Slack API 接受，不证明已读、已处理或外部承诺已履行。
- 真实 unknown/迟到回执、旧 timer 竞态和跨账号切换仍由 fake PG/E2E 回归覆盖，没有在真实 Slack 上制造这些故障。
- G2 的一次 SOP 改进仍未完成：缺真实纠正轨迹、冻结 fixture、独立对照和人工决议；本文件不把真实 G1 通过当作 G2 通过。

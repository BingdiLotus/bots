# Bots MVP：知识、原生提醒与企业治理

> 2026-10-19。状态：设计待实施；本地 Git 已初始化，remote 尚未绑定。当前只有设计文档，未运行产品或清空数据。
> 本期完整执行合同以[Chronotope 期 8](../../chronotope/docs/期8-细化落地方案.md)为准。它按 G0/G1/G2 取代旧 A–F 全量前置；本文件只定义 Bots 需要实现的部分。

## 1. 首个成功场景

管理员预置一个组织 Bot、Slack private app 和一份 SOP。成员在测试频道 @Bot：“记住例会在下午三点，十分钟后提醒我准备材料。”

真实模型通过 `remember` 和 `create_reminder` 保存知识及原生任务，返回 task ID。原请求结束，Chronotope 持久等待；重启 Bots 和 worker 后仍能触发。Bots 向原 thread 发一条提醒，保存 Slack message ts，原生任务据此完成，管理员能查询整条链。

再开一个新 session，Bot 调用 `recall` 使用此前知识及来源，证明是自己的持久知识资产。原生提醒无需先在外部 calendar/issue 建对象；提醒完成不等于会议已举办。

## 2. 固定范围

- 一个组织/管理员/Bot、一个 Slack workspace/应用账号、一个测试频道；成员不使用 Bots 聊天 UI。
- 一个模型、一种 harness、一套运行历史；复用 Chronotope Go loop/Python provider。不要再接 Agents SDK、托管 Agents API 或第二个 workflow 引擎。
- 原生知识、任务及其验收归 Bots。首版直接建 PG 表，与 Chronotope 共用 PG 服务但分 schema/DAO，不先造通用存储平台。
- 单次、固定绝对时间提醒；支持显式改期/取消。组织 timezone 明确，歧义时间先询问；周期 cron、自动跟随日历后置。
- G0/G1 通过 YAML/受权 API 配置与启停；G2 再做一页管理视图和一次人工 SOP 改进。

## 3. 一个服务与八类持久记录

首版用 Go HTTP 服务，进程内分 api、slack、tools、store、dispatcher；数据库使各后台 poller 可恢复。没有独立 scheduler、队列服务或多个业务微服务。

| 记录 | 必须保留的字段/责任 |
|---|---|
| bots | org_id/bot_id、enabled、人格/SOP/工具能力/预算配置、revision/hash、runtime_agent_id、Slack workspace/bot identity 与 secret refs；PG 是导入后配置权威，账号先嵌配置 |
| runtime_sessions | tenant/bot/installation/channel/thread 唯一，不含 revision；首次冻结 revision/runtime agent，后续输入沿实际映射版，创建幂等，不复制模型 history |
| inbox | namespace/source ID 唯一，输入/hash/来源/knowledge scope、处置、实际 session revision、command/run handle；timer fire 幂等接纳，写工具还保存 operation hash/原结果 |
| knowledge | bot/scope、内容、kind（知识/经验）、来源、revision、有效状态；真实内化资产，先按 scope+最近/关键词检索 |
| tasks | 原生提醒目标、文本/时间、简单计划与责任、验收条件、status/revision、origin_command_id/reminder_slot、timer/fire/delivery 关联；外部 task ref 可空 |
| command_outbox | run/timer 等待提交责任、稳定命令身份、请求摘要、接纳状态/handle、重试信息；修复事务提交后尚未调到 Chronotope 的窗口 |
| deliveries | 唯一 operation ID、源 command 或 task/revision、可信 target、payload hash、pending/dispatching/confirmed/unknown、Slack ts/error |
| audit_events | 配置/启停、知识、任务、接纳和发送的 actor、ID、revision、时间、结果/引用；保存不可变配置 revision→runtime agent 映射，G1 不另建发布框架 |

Plan、Schedule、BotCommitment 的原生语义先放在 Task 字段中。出现复杂依赖、一个任务多个调度或独立责任生命周期后再拆表；不要为概念名称先建三套引擎。

knowledge scope 默认是 tenant/bot/installation/channel，独立于 thread/session；所以同频道新 thread 可 recall，跨 channel/tenant 不自动共享。DM/更复杂知识共享策略后置。

## 4. 内置工具与可信上下文

| 模型工具 | 行为 |
|---|---|
| remember(content, kind) | 保存有权知识/经验与当前来源；返回 knowledge ID/revision |
| recall(query) | 只查当前 tenant/bot/scope 的有效资产，返回内容和来源 |
| create_reminder(due_at, text) | 保存 Task+待调度命令后返回；不等到点。每原始输入 MVP 只建一个提醒槽 |
| update_reminder(task_id, expected_revision, due_at, text) | 校验归属/状态/CAS，产生新 timer revision；旧 fire 不发送 |
| cancel_reminder(task_id, expected_revision) | 阻止尚未取得派发权的提醒；已有在途或 unknown 仍待对账 |

这些名称是本期建议接口，尚未实现。只使用一个 typed HTTP bridge，不同时扩 HTTP/MCP 两套协议。模型只提供业务参数；Chronotope 从已绑定 run 注入 tenant/bot/scope、origin/operation/call IDs 和服务认证，接收方仍校验它们。目标 URL、Slack target、账号和 secret 不由模型自报。

写工具把 operation ID、request/context hash、原返回值与业务修改/审计同事务保存，复用 inbox 的 tool-operation 命名空间。重试先查 receipt，同请求回原结果、异请求冲突；提醒槽唯一键另外防止模型以新 call 重建同一逻辑提醒。update/cancel 与 sender claim 锁同一 task/delivery；已 claim/unknown 拒绝改期/取消，不自动建替代 timer。

MVP 不开放 shell、浏览器、任意 HTTP 或自动生成工具。Slack token/signing secret 先由环境/受限配置提供引用，只有 Bots adapter 解析，不能进入模型、harness 请求或审计内容。

普通记忆积累、建任务和设闹钟是预授权日常能力，不逐条做发布审批。行为方法/skill/SOP 修改在 G2 用固定 fixture 对照后人工切配置；正式知识或技能不能因来自私有输入就自动扩大可见范围。

## 5. 接纳、等待、发送

1. Slack 验签后事务写 inbox，再 ACK；同 event 重投不重复。@mention 后只在原 thread 续接；不做全频道 ambient。
2. inbox 同 thread 串行派发。Chronotope 忙时留队列重试，不能丢弃输入。run 使用稳定幂等键异步接纳，Bots 保存 handle 并查询终态。
3. Bots 创建唯一最终回复 delivery。模型没有额外 `slack_send` 工具，避免 final 自动回复与工具回复各发一遍。
4. create_reminder 的事务保存 Task、审计和 timer outbox。提交失败不得向模型说已创建；timer 暂不可用则保留待提交责任。
5. Chronotope 的通用 durable timer 到点只回调 opaque action ref。Bots 先持久接纳 fire，再 ACK；不在 Bots 另建扫描 due task 并直接发送的第二调度器。
6. fire 接纳创建唯一 delivery。sender 新 claim 前事务检查 Bot enabled、当前 task revision/status、账号和可信 target；过期 fire 记 ignored。
7. Slack `ok=true + ts`、匹配的 task 完成及审计同一事务提交；迟到 receipt 保留，但只在 operation/摘要/task revision/delivery 匹配时推进状态。确定未接受才有限重试，未知不盲重发。本地已知停用阻断 claim，远端尚未同步的权限变化仍可能在调用时被 Slack 拒绝。

run 的技术完成、原生 task 完成、平台接受消息、外部会议状态分别记录。暂停后阻断新接纳/claim，已在途结果仍记账。原生修改或取消不修改外部会议。

## 6. 最小管理、版本与恢复

管理员能配置、启停、查询知识/任务/运行/receipt。G1 用 API 即可；G2 管理页面复用这些 API，默认 metadata/引用，不因管理员身份自动读取成员私态。

YAML 只作首次导入；之后 PG 是配置唯一权威，重启不覆盖 API 修改。配置快照/revision→runtime agent 映射不可变，创建未确认不切 active revision。agent/session 创建带稳定客户端键和请求摘要，同键同请求回原 ID、异请求冲突；outbox 修复远端创建成功/本地映射尚未保存的窗口。

新 thread 映射选当前批准版，已有映射固定原版，inbox 冻结实际映射版本；G2 用新 session 测试，不做热迁移/cohort/跨 provider 发布。回退配置不删除知识、任务或效果。每 run 限模型输出 token、请求/steps 与绝对 deadline，每 Bot 一次模型 run 并发；未完提醒数量在创建事务中限额，unknown/blocked 仍占用。事后 usage 不是硬预算门。

进程重启恢复必须保留 Bots PG、Chronotope PG/Restate；outbox、pending inbox/fire/delivery 都可重建处理。允许开发全栈 `down -v` 重置，但那是新测试代次，不是崩溃恢复；无需旧 API/schema/数据兼容。

## 7. 实施与验收

G0 先接确定性 fixture/可计数假 Slack sink，验证多工具回喂、持久知识、任务→timer 窗口、改期/取消、暂停及 unknown。G1 再接真实模型和真实测试 Slack；等待中重启后取得一个真实 message ts。验收清单、Chronotope 修复点与官方实践依据见[期 8 §6–§9](../../chronotope/docs/期8-细化落地方案.md#6-三个小阶段及主路径验收)。

G1 是发布门；第二连接器/模型、DM 私人连接器、多 Bot/子 Agent、Plan DAG、自动 RSI、向量知识平台、全量来源修复、HA 和旧版兼容均不是前置。G2 选一条 G1 失败/人工纠正轨迹，让 Bot 提一个 SOP 补丁与假设，再用固定模型、未参与改稿的 fixture 和隔离 sink 对照；管理员批准或拒绝，新 session 验证并记录反馈/回退。一份报告记录全链即可，不建提案表或评测服务。

第一轮代码可按 `cmd/bots`、`internal/{api,slack,tools,store,dispatcher}`、`migrations`、`test` 组织；本轮不预建空包、不引入跨仓库 internal 依赖或远程发布流程。

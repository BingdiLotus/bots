# 一次 SOP 改进的操作合同

本文件细化 [MVP G2](MVP设计.md) 的一次人工治理闭环。它是操作与报告约定；2026-10-20 已按本文件完成一次真实实例（[证据](evidence/MVP-SOP改进-2026-10-20.md)），下文的冻结、隔离对照、人工决议和回退要求继续适用于后续改进。不会用计时器网络超时替代可由 SOP 改善的问题，也不会用 fake 模型的固定行为证明收益。

## 1. 先保存来源和基线

从一条真实 G1 轨迹选择具体问题，例如时间歧义下擅自创建提醒。保留获授权的输入/纠正、来源 message ID、command/run/tool ID、期望与实际行为，明确其中哪些数据可以进入改稿上下文。source trace、知识和评测数据仍遵守原 tenant/Bot/scope 范围。

从现有管理 API 导出当前 Bot 为 `baseline.json`，保留 `revision/config/runtime_agent_id/config_hash`。生成候选时只允许改变 `config.instructions` 中的一条 SOP；固定模型、工具、限额、知识初态和其它配置。候选输出包含补丁和可检验假设，不能自行修改当前 approved 配置。

独立验收 case/checker 在改稿前冻结并计算 hash，候选生成的上下文不包含该集合，且不开放文件读取工具。每个 case 明确预期的工具调用、原生记录/来源、提醒目标/时间以及允许的外部效果；不用模型自评分作为通过判据。

## 2. 隔离比较，先证明执行证据

baseline 与 candidate 逐题配对，分别使用隔离测试身份和等价的初始知识/任务状态。运行绑定只能连接测试 sink，不能持有真实 Slack 凭据。使用相同 Chronotope/Python harness、五工具 schema、模型路由和预算，仅 SOP 不同。已有 `test/e2e/mvp.py` 是固定主路径 smoke，不是任意 SOP 的评测器。

比较读取实际知识/任务/delivery 和 Chronotope run/calls，核对原 call ID、调用数、`dispatch_seq`、usage、超时、unknown 和 sink 收据。失败、未确认、未取得计量均单独保留，不过滤失败样本后再报告分数；测试产生的 native 资产不写回组织 Bot。

报告至少保存以下信息，可使用一份本地 JSON，无需新数据库表或评测服务：

| 字段 | 必需内容 |
|---|---|
| source_trace | 真实来源、纠正及相关命令/运行 ID，获授权的可复查引用 |
| baseline | Bots revision、配置/SOP hash、runtime agent ID/spec digest |
| candidate | SOP diff/hash、假设、生成 run ID；候选冻结时刻 |
| environment | 代码版本、harness/工具 schema hash、模型 alias、provider 路由及可证的版本；不能证明的实际上游版本标 unknown |
| fixtures | manifest/checker hash、改稿前冻结证据与初始状态约定 |
| results | baseline/candidate 逐题判据、原生状态、run/session/agent ID、sink 回执与失败原因 |
| cost | LLM 调用数、tokens、耗时、usage_unknown，不将 tokens 冒充已结算账单 |
| decision | 初始为 null；人工批准/拒绝后记录操作者、时间、理由及所批准的 candidate hash |
| publish | 初始为 null；实际 CAS 请求/结果、批准后 Bots revision 和 runtime agent ID |
| feedback/rollback | 新 thread 验证、后续反馈；如回退，记录恢复的内容及新 revision |

每个新 runtime agent 的配置版本目前均为 1；**Bots revision 与 runtime agent version 是两个不同字段**。模型 alias 固定也不能证明上游模型权重或服务实现从未改变。

## 3. 人工发布、拒绝与回退

人工决议前，报告中的 `decision/publish` 保持 null，产品配置不变。管理员审核具体 diff、来源、独立对照结果、成本和限制；只能把实际批准或拒绝写成决议，不能由评测程序替人签字。

批准后调用已有 `PUT /api/tenants/{tenant}/bots/{bot}/config`，提交 `expected_revision` 和完整配置：保留原部署绑定及其它字段，只替换已批准的 instructions。远端不可变 agent 确认后，Bots 才以 CAS 激活。若 revision 冲突，停止该发布，读取当前版本重新核对；不自动把旧补丁覆盖到新配置上。

在外部应用的**新 thread**验证新 revision，保留新 runtime session/run.bound 证据；旧 thread 仍使用旧映射。检查原生知识/任务延续及真实渠道 receipt，避免只凭最终回复判断成功。

拒绝时不调用配置更新接口，保留拒绝理由。回退时将 baseline 的 SOP 内容作为新的 approved revision 发布：`r → r+1（候选）→ r+2（恢复内容）`，不把 revision 数字倒退，也不清空知识、任务或已经发生的外部效果。

## 当前状态

配置 CAS、不可变 runtime agent、新旧 thread 绑定及回退所需底层机制已实现并有 PG 测试。管理页按同一 API 操作。

2026-10-20 已完成一次实例：真实 G1 recall 轨迹 → 用户确认纠正 → 真实模型候选 → 冻结 5-case fixture/checker → 两隔离 test tenant 配对 → 用户批准 → CAS 发布真实 Bot revision 2（agent `a_e1497669…`，仅 instructions 变化）→ 真实 Slack 新 thread 验证通过（session revision 2，run `r_32013c8d…`，真实 ts `1792459570.858859`，无交叉记录）；反馈 pass，rollback=null。期间 v1 fixture 未复现 baseline 差异，作为 null 结果保留；最终 v2 fixture 与两处 checker 误报均在[证据](evidence/MVP-SOP改进-2026-10-20.md)中说明。该实例不建立自动提案/评测平台，也不代表后续 SOP 改动会自动批准。

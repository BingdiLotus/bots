# M1/M2 Slack smoke（2026-10-21）

## 环境

- 镜像：`bingdi-bots:m1m2-20261021`（main `44d57ae` 构建）
- Bot：trial tenant / `bingdi-trial`，revision **2**（新 agent，9 个工具）
- 容器：`bots-trial-m1m2`，read-only、65532、8090 loopback；Slack Socket Mode connected
- 频道：现有 private trial 频道
- 真实模型：`claude-sonnet-4-6`

## 结果

### 1. 任务可见性

用户在新顶层 thread 请求：

```text
请在 10 分钟后提醒我“M1M2 smoke 提醒”，然后用 list_tasks 列出我当前的待办和提醒。
```

观测到工具序列：

```text
create_reminder -> list_tasks
```

- run `r_299dcc00…` completed，3 次 LLM call；
- task `task_ce2a668f…` 创建为 `scheduled`，due `2026-10-21 13:54:56 Asia/Shanghai`；
- final 明确列出 scheduled 提醒；
- final delivery **confirmed**，真实 ts `1792561510.560899`；
- 到点 reminder delivery **confirmed**，真实 ts `1792562097.745569`，task `completed`。

### 2. Memory 查询

用户在新顶层 thread 请求：

```text
记住：我的 M1M2 测试代号是 ORANGE-7。然后用 list_memories 列出你记得我什么。
```

观测到工具序列：

```text
remember -> list_memories
```

- run `r_a320e907…` completed；
- final 返回 `ORANGE-7`，delivery **confirmed**，ts `1792561530.546379`；
- 新 thread 是 revision 2，跨 thread 读取的是显式 `remember` 的 scope 知识。

### 3. Memory 停用

用户在新顶层 thread 明确请求：

```text
忘掉 M1M2 测试代号 ORANGE-7 的那条记忆。
```

观测到工具序列：

```text
list_memories -> forget_memory(confirm=true, expected_revision=1)
```

- run `r_66bdffda…` completed；
- final “已忘掉 M1M2 测试代号 ORANGE-7 的那条记忆。”，delivery **confirmed**，ts `1792561563.553359`；
- `knowledge_forgotten` audit 写入，revision 2；
- 当前 active knowledge = **0**；
- read-only `list_tasks`/`list_memories` 没有写 `tool_operation` receipt/audit；`remember`/`forget_memory` 有 mutation receipt/audit。

## 边界

- `list_reminders` 的真实 Slack 未单独调用；它与 `list_tasks` 共用同一只读 handler，已有 PG 回归，仍建议下一次 smoke 覆盖别名。
- 本次 forget 的确认来自用户明确的“忘掉…”请求；模型据此传 `confirm=true`，没有二次 UI/服务端确认。若要防止模型在模糊语句下直接停用，后续应增加两步确认或服务端 confirmation token。
- 这是真实 Slack smoke，不是正式团队 trial；数据写入现有 trial tenant，未新建频道/Bot 组织。
- smoke 容器在提醒到点后停止（`exited`，8090 free）；trial 业务数据保留。

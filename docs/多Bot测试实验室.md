# 多 Bot 测试实验室（test-only）

这不是 MVP 产品能力。`docs/MVP设计.md` 的范围仍是一个 Bot、一个 Slack 账号、一个模型；本实验室只用于验证多 Bot/多 harness 的隔离、路由和并发边界，不能作为生产多 Bot 承诺。

## 2-Bot 形态

| 角色 | Slack 身份 | Bots tenant/bot | 端口 | Slack App |
|---|---|---|---|---|
| Lab A（现有） | `bingdi` | `bots-lab-a` / `bingdi-a` | 8091 | 现有 `REDACTED_APP_A` |
| Lab B（新增） | `bingdi-b` | `bots-lab-b` / `bingdi-b` | 8092 | 新建 test-only app，manifest 见 `deploy/slack-lab-b.manifest.json` |

两个 Bot 同一私有频道 `#bingdi-bots-lab`，各自一个 Bots 容器、tenant key、Chronotope bridge/timer。Trial 的 `bots-trial` 与 Lab A 使用同一个 Slack app token，不能同时运行；跑 Lab 时暂停 trial，结束后恢复。Lab B 使用独立 app/token。

## 关键边界

- 一个 Slack App 只有一个 bot user；两个真实 bot 需要两个 app 安装。
- Bots 现在会过滤 bot 消息（`isBotMessage`）。**初始实验由操作者/AI 驱动器以人类用户身份发消息**；如果要让两个 bot 互相 @ 对话，需要另加 test-only bot sender allowlist，不能默认放宽生产。
- 每个 harness 配一个 bot 的含义是：独立 Slack app/bot user + 独立 Bots tenant + 独立 Chronotope agent/harness；不是给同一个 bot 配多个模型。
- AI 小分队可以覆盖路由、隔离、并发、提醒、配额和故障；不能覆盖真人信任、社交歧义、管理员权限边界和采纳意愿。

## 准备步骤

1. 新建私有频道 `#bingdi-bots-lab`，邀请现有 `bingdi`。
2. 用 `deploy/slack-lab-b.manifest.json` 创建并安装新 app；把 app ID 提供给维护者。bot token/app token 不粘贴到聊天，写入本地 0600 文件或由已登录 Slack CLI 取回。
3. 邀请新 app 的 bot user `bingdi-b` 入频道。
4. 维护者创建两个 lab tenant/seed、tenant key、Chronotope bridge/timer，启动两个容器（8091/8092），并暂停 trial。
5. 操作者/AI 驱动器发消息，记录 inbox/delivery/audit 和两个 bot 的回复；结束停 Lab A、恢复 trial。

## 首批场景

1. `@bingdi-a`/`@bingdi-b` 分别被 @，验证路由和身份不串。
2. 同一频道并发创建提醒，验证 quota/tenant 隔离与未知/拒绝处理。
3. 一个 bot 的配置/凭证错误，另一个仍正常。
4. 两 bot 同频道噪声下，mention-only 和 thread 归属。
5. 重启/权限撤回时两 tenant 的交付互不影响。

## 清理

- 停 Lab 容器；恢复 trial 容器；Lab tenant 数据保留或单独删除，不写入 trial。
- 新 app 可保留为 test-only，或测试后卸载；不把新 app 的 token 提交到仓库。

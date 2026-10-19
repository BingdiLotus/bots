# Bingdi Bots

面向企业、组织和团队的 Bot 原生能力与治理平台。管理员配置人格、SOP、应用账号和运行范围；Bot 可以沉淀业务知识/经验、自己创建任务和提醒。成员通过 Slack 等外部应用交互，管理台用于配置、启停和审计。

当前只有设计文档，没有产品代码。本地 Git 已初始化，尚未绑定远程；规划仓库地址为 [BingdiLotus/bots](https://github.com/BingdiLotus/bots)。

下一里程碑只验证：**一个 Bot、一个 Slack 账号、一个 harness，记住信息、自建提醒、跨重启发送并保存真实回执。**

- [MVP 设计与实施入口](./docs/MVP设计.md)
- [本地 Chronotope 期 8 方案](../chronotope/docs/期8-细化落地方案.md)

允许重建开发数据和破坏性接口调整。第一版不建设旧数据迁移、协议兼容、多 Agent、应用市场或完整 RSI 发布平台。

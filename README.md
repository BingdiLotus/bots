# Bingdi Bots

面向企业、组织和团队的 Bot 原生能力与治理平台。管理员管理人格、SOP、应用账号和运行范围；Bot 可以沉淀业务知识/经验，自建任务与提醒。成员通过 Slack 等外部应用交互，Bots 管理台负责配置、启停和审计。

当前已建立 Go HTTP 项目骨架：环境配置、JSON 日志、进程存活检查和优雅退出。知识、任务、Slack 集成与 Chronotope 联调仍待实施。仓库：[BingdiLotus/bots](https://github.com/BingdiLotus/bots)，本地 origin 已配置。

## 本地运行

需要 Go 1.26.0 或更高版本。

```sh
make run
```

另一个终端检查进程：

```sh
curl -fsS http://127.0.0.1:8090/healthz
```

响应为 `{"status":"alive"}`。探针仅表示进程存活，不表示 PG、Slack 或业务能力已就绪；当前无业务 API/readyz 占位接口。

```sh
make check
make build
./bin/bots
```

`make check` 执行格式检查、vet、测试和构建。运行时收到 SIGINT/SIGTERM 后，在配置期限内优雅退出。

## 配置

| 环境变量 | 默认值 | 含义 |
|---|---|---|
| BOTS_HTTP_ADDR | 127.0.0.1:8090 | HTTP 监听地址；容器默认 :8090 |
| BOTS_SHUTDOWN_TIMEOUT | 10s | 正数的优雅退出期限 |

参见 [.env.example](./.env.example)。服务不自动读取 `.env`，需由 shell/部署环境导出；例如 `BOTS_HTTP_ADDR=127.0.0.1:8091 make run`。

已提供 [Dockerfile](./Dockerfile) 和 [CI](./.github/workflows/ci.yml)。本轮本机 Go 检查及真实 HTTP/退出验证通过；Docker 构建因基础镜像元数据获取停滞而中断，容器构建/运行尚未验证。

## 代码与设计入口

```text
cmd/bots/               HTTP 服务入口
internal/config/        环境配置与校验
internal/httpapi/       当前仅 healthz
.github/workflows/      Go CI
docs/                  产品设计与研究
```

下一里程碑只验证一个组织 Bot、一个 Slack 账号、一个 harness：**记住信息 → 自建提醒 → 等待中重启 → 发送并保存真实回执**。

| 文档 | 权威范围 |
|---|---|
| [MVP 实施方案](./docs/MVP设计.md) | 唯一产品主路径、八类记录、五个工具、G0/G1/G2 与实施验收 |
| [Chronotope 集成与职责边界](./docs/Chronotope与Bingdi-Bots职责边界.md) | Bots、执行 infra 与外部应用逐对象分权 |
| [持久身份与非模型 RSI](./docs/持久Agent身份与非模型RSI设计.md) | 原生资产/工作与后续改进能力，非 MVP 全量前置 |
| [承诺原语](./docs/承诺原语设计.md) | 原生责任的历史依据/后续设计；MVP 先用 Task 字段 |
| [工业产品调研](./docs/工业产品调研-持久Agent与托管运行平台-2026-10.md) | Tag、Managed Agents、Agents API/SDK、dots、Grok、Muse、Cue |
| [前沿研究](./docs/前沿调研-持久Agent与非模型自改进-2026-10.md) | 知识/技能、协作与非模型改进的证据和限制 |

文档已从 Chronotope 迁入本仓库，不再在两边保存完整产品设计。Chronotope 仅维护[运行时期 8](../chronotope/docs/期8-细化落地方案.md)和[应用接入契约](../chronotope/docs/应用接入契约.md)。跨仓库相对链接使用本地 `BingdiLotus/{bots,chronotope}` 并列布局；线上从 [Chronotope 仓库](https://github.com/BingdiLotus/chronotope)导航。

两个项目均未上生产，允许破坏性 API/schema 调整与开发全栈重建。初始化不包含成员聊天产品、完整业务功能或生产上线。

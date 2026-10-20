# Bingdi Bots

面向企业、组织和团队的 Bot 原生能力与治理平台。管理员管理人格、SOP、应用账号和运行范围；Bot 可以沉淀业务知识/经验，自建任务与提醒。成员通过 Slack 等外部应用交互，Bots 管理台负责配置、启停和审计。

当前实现了知识与提醒的 MVP 主链：认证入站（签名 webhook 或内部 App 的 Socket Mode）、持久 inbox、五个原生工具、Chronotope 有限 run/耐久 timer、唯一 sender、回执/unknown 台账及管理员 API。G0 已使用真实 Bots 进程、PG 与 Chronotope 栈验证；G1 已在私有测试频道通过真实 Slack Socket Mode、真实模型、知识/提醒、等待中崩溃重启与跨会话 recall；G2 已用真实纠正轨迹、冻结 fixture、隔离配对和人工批准完成一次 SOP 改进并发布真实 Bot revision 2。仓库：[BingdiLotus/bots](https://github.com/BingdiLotus/bots)。阶段状态与证据见[开发进展](docs/MVP开发进展.md)；受控试用上线准备见[部署与试用](docs/部署与试用.md)；独立 private 新频道和 Docker trial Bot 已上线并通过 smoke；Slack scopes、unknown 人工处置、D1/D3 恢复已验证。D2/联合 RTO 仍未完成，systemd 为可选项，试用成员/指标待确认。

管理员可通过 `/admin` 管理工作配置、启停、知识、任务、回执和审计。页面直接嵌入 Go 服务；成员仍在 Slack 中与 Bot 交互。管理页与一次 SOP 改进的完整验收分别记录，尚未用界面可用替代实际改进结果。

## 本地运行

需要 Go 1.26.0 或更高版本，以及已启用租户认证的 Chronotope 栈。先按[运行与验收](docs/运行与验收.md)配置 seed、PG、服务认证和 Slack 免费自建 App；普通 Bot 不依赖 Slack 内置 AI 订阅。

```sh
cp configs/bot.example.yaml configs/bot.yaml
# 编辑配置并从本地环境导出所需凭据后启动
GOTOOLCHAIN=go1.26.0 make run
```

另一个终端检查进程与依赖就绪：

```sh
curl -fsS http://127.0.0.1:8090/healthz   # 仅进程存活
curl -fsS http://127.0.0.1:8090/readyz    # PG、dispatcher 租约/Step、Socket 连接
./bin/bots readyz                         # scratch 容器/服务管理器可用的二进制探针
```

`healthz` 只表示进程存活；`readyz` 失败时返回 503，且不输出依赖错误细节。受控试用的 systemd/容器模板、最小 Slack scopes、备份恢复演练和 unknown 处置见[部署与试用](docs/部署与试用.md)。

```sh
make check
make build
./bin/bots
```

`make check` 执行格式检查、vet、测试、构建、Shell 脚本语法和 G2 Python 编译检查。运行时收到 SIGINT/SIGTERM 后，在配置期限内优雅退出。

## 配置

| 环境变量 | 默认值 | 含义 |
|---|---|---|
| BOTS_HTTP_ADDR | 127.0.0.1:8090 | HTTP 监听地址；容器默认 :8090 |
| BOTS_SHUTDOWN_TIMEOUT | 10s | 正数的优雅退出期限 |
| BOTS_DATABASE_URL | 必填 | PG 连接，应用仅拥有 `bots` schema |
| BOTS_SEED_PATH | configs/bot.yaml | 首次导入配置；PG 保存后续管理修改 |
| BOTS_CHRONOTOPE_URL | http://127.0.0.1:8080 | 运行时 API，不跨仓库导入内部实现 |
| BOTS_POLL_INTERVAL | 1s | durable inbox/outbox/result 的推进间隔 |
| BOTS_RUNTIME_TOKEN_ENV | BOTS_CHRONOTOPE_TOKEN | Chronotope tenant key 的环境变量名 |
| BOTS_ADMIN_TOKEN_ENV | BOTS_ADMIN_TOKEN | 管理员 key 的环境变量名 |
| BOTS_INTERNAL_TOKEN_ENV | APPLICATION_SERVICE_TOKEN | 工具/timer 服务认证的环境变量名 |

参见 [.env.example](./.env.example)。服务不自动读取 `.env`，需由 shell/部署环境导出。seed 仅存凭据引用；后台 sender 才解析实际渠道 token，模型不能指定频道或回调地址。

已提供 [Dockerfile](./Dockerfile) 和含 PostgreSQL 事务测试的 [CI](./.github/workflows/ci.yml)。本机 `make check`、G0、真实模型＋sink、管理页浏览器 QA 及容器构建/只读 smoke 通过；真实 Slack 与完整 SOP 改进仍待验收，详见开发进展。

## 代码与设计入口

```text
cmd/bots/               HTTP + 单 tenant dispatcher 入口
internal/config/        环境配置与校验
internal/httpapi/       管理 API、验签 Slack、工具/timer 回调
internal/domain/        Bot、来源、知识、任务、命令及回执类型
internal/store/         PG 事务、outbox、幂等、CAS、审计
internal/chronotope/    无状态 HTTP client 与固定工具 schema
internal/slack/         渠道事件归一化、验签及单次发送
internal/service/       durable 命令推进、结果收集和唯一 sender
test/                   实际进程联调，只有模型和渠道 provider 使用替身
.github/workflows/      Go CI
docs/                  产品设计与研究
```

下一发布门仍是一个组织 Bot、一个 Slack 账号、一个 harness：**记住信息 → 自建提醒 → 等待中重启 → 发送并保存真实 Slack 回执**。本地 sink 的确认只验证技术路径，不能替代真实渠道。

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

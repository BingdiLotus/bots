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

- [MVP 设计与实施入口](./docs/MVP设计.md)
- [本地 Chronotope 期 8 方案](../chronotope/docs/期8-细化落地方案.md)

允许重建开发数据和破坏性接口调整。第一版不建设旧数据迁移、协议兼容、多 Agent、应用市场或完整 RSI 发布平台。

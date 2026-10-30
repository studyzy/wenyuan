# wenyuan（文渊）

参考实现的智能体记忆 HTTP API 及其 PostgreSQL 存储层的 Go 实现。

> **文渊** —— 取「文之渊薮」之意，指汇聚、保存典籍学问之处。名字正对应本服务的职责：
> 一处智能体可以写入、也能从中唤回记忆的持久记忆库。

本项目实现的是 **兼容参考实现 API** 的版本：公开 HTTP 表面（路由、JSON 字段名、状态码、
请求头）与 PostgreSQL schema（表、列、枚举）逐项保留，因此现有客户端无需修改即可对接。
实现语言为 Go，优先使用标准库。

[English README](README.md)

## 已实现的功能

- **知识库（bank）生命周期** —— 创建 / 列表 / 整体替换 / 局部更新 / 删除；别名
  （列出 / 新增 / 提升为主 / 删除）；按库配置；退役路由的响应。
- **写入（retain）** —— 同步与异步记忆写入、带附件的文件上传、分块 / 实体 / 记忆持久化、
  异步操作跟踪（列表 / 查询 / 取消 / 重试），并有真正的队列消费者（`cmd/worker`：
  租约、退避、重试）。
- **检索（recall）** —— 多路召回，接真实向量检索（pgvector），融合重排可选接
  交叉编码器重排器，token 预算筛选、标签过滤与作用域、从查询文本解析的时间窗口。
- **反思（reflect）** —— 有界「检索-综合」工具循环，带引用证据、执行轨迹、结构化输出、
  指令（directive）。
- **记忆子面** —— 记忆列表 / 查询 / 更新、单条记忆的历史与观察、干跑抽取、prompt 预览。
- **心智模型（mental model）** —— CRUD、刷新触发器（cron、tags-match、手动）、
  经 worker 的计划与随写刷新、刷新历史、干跑刷新。
- **知识库页面（knowledge base）** —— 目录树 / 页面、文本 + 向量混合检索、导出。
- **指令（directive）** —— CRUD，并注入反思循环。
- **读面** —— 文档、分块、标签、实体，以及记忆图与实体图。
- **观察与整合** —— 整合批次、策略预览、失败恢复、过期观察清理。
- **可观测** —— 库统计、记忆时间序列、审计轨迹、模型请求追踪、OTel 追踪、
  Prometheus 风格指标。
- **Webhook** —— 注册 CRUD，外加带重试与投递记录的投递。
- **传输（transfer）** —— HTTP 与 `bankctl` 两种整库导出 / 导入、文档传输、整库克隆、
  附件往返。
- **MCP** —— `/mcp` 上的 JSON-RPC 端点（特性开关控制）。
- **模型提供方** —— 对话 / 向量 / 重排三个端口，适配 OpenAI 兼容端点、Anthropic、
  Cohere / Gemini / TEI 向量等；未配置时使用确定性的无模型选择。
- **后台维护** —— 保留期清理、搁浅工作恢复、心智模型计划刷新；租户 / 扩展 schema 支持。
- **对象存储** —— S3 兼容后端。
- **前端 UI** —— `frontend/` 下的 Next.js 前端（服务 API 与 `wenyuan-admin ui` 均可驱动）。
- **命令行** —— `bankctl`（管理：迁移、备份恢复、导出导入、修复）与 `wenyuan-admin`
  （客户端：全部 API 命令族、文件系统镜像、explore TUI）。

**有意不做：** OAuth provider 集成与 GCS / Azure 对象存储（S3 兼容后端已覆盖对象存储）。

## 环境要求

- Go 1.25 或更高
- PostgreSQL 并启用 `pgvector` 与全文检索扩展（仅在使用 PostgreSQL 存储时需要；
  默认的内存存储可离线运行）

## 构建与测试

```sh
make build     # go build ./...
make test      # go test -count=1 ./...
make vet       # go vet ./...
make pgx       # go build -tags pgx ./...   （编译 PostgreSQL 驱动路径）
make check     # 以上全部 + lint
make install   # 构建 server、worker、bankctl 与 wenyuan-admin 到 bin/（带 pgx tag）
```

默认构建仅依赖标准库、不访问网络。PostgreSQL 驱动（`pgx`）隔离在 `pgx` 构建标签之后，
因此默认构建保持依赖精简且可离线。`make check` 与 CI 门禁一致；`make test-pg` 针对
真实数据库跑 PostgreSQL 套件（`WENYUAN_TEST_DATABASE_URL`）；`make parity` 用上游
系统测试故事驱动本服务。

## 运行服务

```sh
make db-up                              # 本项目专用的 PostgreSQL，127.0.0.1:15432
export WENYUAN_API_DATABASE_URL=postgresql://wenyuan:wenyuan@127.0.0.1:15432/wenyuan
go run -tags pgx ./cmd/server           # 使用配置的监听地址启动
go run -tags pgx ./cmd/server --host 0.0.0.0 --port 18888
go run -tags pgx ./cmd/worker           # 异步操作消费者
```

配置了 `database_url` 时 `pgx` tag 是必需的：不带 tag 的二进制没有
PostgreSQL 驱动，会拒绝启动。

或使用 Docker 只跑依赖服务 —— PostgreSQL 与两个 TEI 模型 sidecar；
server、worker 与 bankctl 跑在宿主机：

```sh
make db-up                              # PostgreSQL，127.0.0.1:15432
make tei-up                             # TEI 向量（18080）与重排（18081）
```

TEI 首次启动会把模型下载进 compose 卷，之后复用。

### 控制台前端

```sh
make frontend-build                     # frontend/ 的生产构建
make frontend-run                       # 19999 端口（Next.js 生产模式）
```

前端通过 BFF 路由访问 API，BFF 读取 `WENYUAN_CP_DATAPLANE_API_URL`
（默认 `http://127.0.0.1:8000`；请指向 wenyuan API，例如
`http://127.0.0.1:18888`）。请使用生产构建：本环境下 dev 模式的 HMR
会挂起带 Origin 的 WebSocket 升级，dashboard 会一直加载不出数据。

控制台没有独立口令：日常用数据 key（配置里的 `tenant_api_key`）登录，同一把
key 既能访问控制台也能访问 API；需要管理租户时用运维 key（`admin_api_key`）
登录，只有该会话能调用 `/api/admin/**`。`tenant_mode: multi` 的部署不认数据
key，用它会登录失败，各租户改用自己的 `wyk_...` key 登录。

前端自己的环境文件由 `make service-env` 从同一份 `etc/wenyuan.yaml` 生成
（`make service-restart` 会先跑这一步），所以 key 只需要在一个地方配置。

### 服务端参数

| 参数 | 作用 |
| --- | --- |
| `--host` | 监听地址 |
| `--port` | 监听端口 |
| `--workers` | worker 并发度 |
| `--access-log` / `--no-access-log` | 开启 / 关闭每请求访问日志 |
| `--proxy-headers` | 信任转发 / 代理头 |
| `--forwarded-allow-ips` | 信任代理头的 IP 白名单（逗号分隔） |
| `--daemon` | 以守护模式运行 |
| `--log-level` | 日志级别 |
| `--ssl-certfile`、`--ssl-keyfile` | TLS 证书与私钥 |

`--reload` 与 `--idle-timeout` 仅为兼容而接受，在本构建中是空操作。

### 管理命令行

```sh
bankctl bank rename --from OLD --to NEW [--dry-run]
bankctl bank export-bank --bank ID --output FILE [--include-history]
bankctl bank import-bank --archive FILE [--target-bank ID] [--include-history]
bankctl bank backup [--output] FILE
bankctl bank restore [--input] FILE [--yes]
bankctl bank run-db-migration [--embedding-dimension N] [--skip-extension-reconcile]
bankctl bank repair-bank (--bank ID | --all) [--dry-run]
bankctl bank worker-status
bankctl bank decommission-worker [--worker] ID [--yes]
bankctl bank decommission-workers [--yes]
bankctl bank migrate (--archive FILE | --source-url URL --source-bank ID) [--bank-id ID] [--replace|--append|--incremental] [--dry-run]
```

所有数据库命令都可用 `--schema/-s` 指向配置之外的 schema；不带 `--schema` 的迁移
覆盖基础 schema 及租户扩展报告的每个 schema。

## 配置

优先级：命令行参数 > 环境变量 > 配置文件 > 默认值。

```sh
wenyuan                                  # 依次读取 ./.env、./etc/wenyuan.yaml
WENYUAN_API_CONFIG_FILE=/etc/wenyuan/prod.yaml wenyuan   # 指定文件，必须存在
```

配置文件为 YAML，默认从工作目录下的 `etc/wenyuan.yaml` 读取（容器镜像内即
`/etc/wenyuan.yaml`）。它能提供环境变量能提供的任何键，写法是去掉 `WENYUAN_API_`
前缀并转为小写；嵌套键会在启动时报错而不会被静默忽略——`llm: {provider: openai}`
是错误写法，`llm_provider: openai` 才是该键。仓库自带的 `etc/wenyuan.yaml` 是全部
注释掉的模板：取消某行注释，该键即以此文件作为环境变量之后的兜底。由
`WENYUAN_API_CONFIG_FILE` 指定的文件必须存在且可解析；`etc/wenyuan.yaml` 不存在
时什么都不改变。

| 环境变量 | 作用 |
| --- | --- |
| `WENYUAN_API_CONFIG_FILE` | YAML 配置文件路径（默认 `etc/wenyuan.yaml`） |
| `WENYUAN_API_HOST` | 监听地址 |
| `WENYUAN_API_PORT` | 监听端口 |
| `WENYUAN_API_DATABASE_URL` | PostgreSQL 连接串 |
| `WENYUAN_API_DATABASE_SCHEMA` | 数据库 schema 名 |
| `WENYUAN_API_VECTOR_EXTENSION` | 向量扩展名 |
| `WENYUAN_API_TEXT_SEARCH_EXTENSION` | 全文检索扩展名 |
| `WENYUAN_API_RUN_MIGRATIONS_ON_STARTUP` | 启动时执行迁移 |
| `WENYUAN_API_MCP_ENABLED` | 启用 MCP 命名空间 |
| `WENYUAN_API_ENABLE_BANK_CONFIG_API` | 开放按库配置写入路由 |
| `WENYUAN_API_GZIP_MIN_SIZE` | 响应体 gzip 体积下限（负值关闭） |
| `WENYUAN_API_RETAIN_BATCH_ENABLED` | 开启批量写入闸门 |
| `WENYUAN_API_RECALL_STRATEGY_BOOSTS` | 各召回路策略加权 |
| `WENYUAN_API_REFLECT_SEARCH_OBSERVATIONS_INCLUDE_ENTITIES` | 反思观察默认值 |
| `WENYUAN_API_REFLECT_SEARCH_OBSERVATIONS_MAX_TOKENS` | 反思观察 token 默认值 |

> 环境变量名保持不变，属于参考实现 API 的**公开兼容面**。

### 模型提供方

模型访问按作用域配置，环境变量沿用参考实现的命名，上游文档可直接套用：

- 对话：`WENYUAN_API_RETAIN_LLM_*`、`WENYUAN_API_REFLECT_LLM_*`、
  `WENYUAN_API_MENTAL_MODEL_REFRESH_LLM_*`（`_PROVIDER`、`_API_KEY`、`_MODEL`、
  `_BASE_URL`、重试 / 退避参数等）。
- 向量：`WENYUAN_API_EMBEDDINGS_<VENDOR>_*`（openai、cohere、gemini、tei、onnx 等）。
- 重排：`WENYUAN_API_RERANKER_*`（cohere、openrouter 等）。

不配置即使用确定性的无模型选择，记忆写入与反思照常离线工作。完整键名清单见
`internal/config/provider_env_keys.go`。

## HTTP API

所有按库路由都在 `/v1/default/banks` 之下。凡是接受库 id 的地方都可传入别名；
响应始终报告规范（canonical）库 id。所服务路由面由测试钉死
（`internal/app/route_coverage_test.go`）并与参考实现 100/100 一致；机器可读契约在
`internal/httpx/openapi.json`。

| 命令族 | 路由（代表性） |
| --- | --- |
| 监控 | `GET /health`、`/health/live`、`/health/ready`、`/version`、`/metrics` |
| 知识库（bank） | `GET/POST /v1/default/banks`、`GET/PUT/PATCH/DELETE .../{bank_id}`、别名、配置（`GET/PATCH/DELETE`） |
| 记忆操作 | `POST .../memories`、`DELETE .../memories`（清空）、`POST .../files/retain`、`POST .../memories/recall`、`POST .../reflect` |
| 记忆子面 | `GET .../memories/list`、`GET/PATCH .../memories/{id}`、`.../history`、`DELETE .../memories/{id}/observations`、`POST .../memories/dry-run-extract`、`POST .../prompts/preview` |
| 异步操作 | `GET .../operations`、`GET/DELETE .../operations/{id}`、`DELETE .../operations/{id}/delete`、`POST .../operations/{id}/retry` |
| 心智模型 | `GET/POST .../mental-models`、`GET/PATCH/DELETE .../mental-models/{id}`、`.../history`、`.../refresh`、`.../dry-run-refresh`、`.../clear` |
| 知识库页面 | `GET .../knowledge-base/tree`、`POST .../folders`、`POST .../pages`、`GET .../export`、`GET .../search`、节点的 `GET/PATCH/DELETE` |
| 指令 | `GET/POST .../directives`、`GET/PATCH/DELETE .../directives/{id}` |
| 读面 | `GET/PATCH/DELETE .../documents/{id}`、`GET .../documents/{id}/chunks`、`POST .../documents/{id}/reprocess`、`GET .../tags`、`GET .../graph`、`GET .../entities`、`GET .../entities/graph`、`POST .../entities/{id}/regenerate`、`GET /v1/default/chunks/{id}` |
| 观察与整合 | `DELETE .../observations`、`GET .../observations/scopes`、`POST .../consolidate`、`POST .../consolidation/recover`、`POST .../consolidation-strategies/preview` |
| 报表 | `GET .../stats`、`GET .../stats/memories-timeseries`、`POST .../health/llm`、`GET .../audit-logs`（+`/stats`）、`GET .../llm-requests`（+`/stats`） |
| Webhook | `GET/POST .../webhooks`、`GET/PATCH/DELETE .../webhooks/{id}`、`GET .../webhooks/{id}/deliveries` |
| 传输 | `POST .../import`、`GET .../export`、`POST .../transfer/export`、`POST .../transfer/import`、`POST .../document-transfer`（+`/export`）、`POST .../clone`、`GET .../attachments/{id}`、`GET /v1/default/files/download/{...}` |
| MCP | `POST /mcp`（JSON-RPC，`WENYUAN_API_MCP_ENABLED` 控制） |
| 契约 | `GET /v1/bank-template-schema`、`GET /v1/version` |

`GET/PUT /v1/default/banks/{bank_id}/profile` 与
`POST /v1/default/banks/{bank_id}/background` 已退役，返回 `410 Gone`，
并在消息中指出 config 读写路由作为替代。

## 存储

PostgreSQL schema 位于 `migrations/`（同一批文件在 `internal/storage/embedded/` 下被
`go:embed` 内嵌）。迁移序列是**生成**的——从参考数据库自身目录导出，共五个文件，
`0001_extensions` 到 `0005_reference_functions`——与上游 schema 逐对象一致：
24 张表、234 列、75 个索引、191 个约束、236 个函数（`docs/SCHEMA-ALIGNMENT.md`
是常备比对，差异为零）。可以直接对接参考实现已有的数据库。

## 目录结构

```
cmd/server         进程入口（HTTP API）
cmd/worker         异步操作消费者入口
cmd/bankctl        知识库管理命令行
cmd/wenyuan        客户端命令行入口（wenyuan-admin 二进制）
cmd/schematool     迁移生成器的 schema 导出辅助工具
internal/app       应用组装、worker 处理器、端到端 parity 测试
internal/bankapi   知识库生命周期处理器与路由
internal/bankadmin bankctl 共用的重命名 / 归档命令
internal/cli       客户端命令行实现（registry、HTTP client、命令族）
internal/config    参数 > 环境变量 > 默认值 的解析
internal/httpx     路由、中间件、JSON 与错误信封、OpenAPI
internal/memoryops retain / recall / reflect 处理器与模型
internal/monitoring 健康、就绪、版本、指标
internal/provider  模型提供方端口 + 厂商适配器（openai、anthropic、
                   embed、rerank、none、mock）
internal/storage   存储接口、内存与 PostgreSQL 实现
migrations         PostgreSQL schema 迁移
```

完整包地图与架构边界见 `AGENTS.md`。

## 工程文件

| 文件 | 用途 |
| --- | --- |
| `Makefile` | 构建、测试、vet、pgx 构建、lint 与 parity 目标 |
| `CHANGELOG.md` | 各版本重要变更（Keep a Changelog） |
| `CONTRIBUTING.md` | 如何提交改动 |
| `CODE_OF_CONDUCT.md` | Contributor Covenant v2.1 |
| `SECURITY.md` | 如何私密报告安全问题 |
| `Dockerfile`、`docker-compose.yml` | server + worker 镜像；compose 依赖服务（pgvector、TEI 模型 sidecar） |
| `.github/workflows/ci.yml` | 每次 push / PR 的构建、测试、vet、gofmt |
| `.github/ISSUE_TEMPLATE/` | Bug 与功能需求表单 |
| `docs/ROADMAP.md` | 未完成工作与已知局限（交接文档） |
| `docs/GAPS.md`、`docs/FUNCTIONAL-GAPS.md` | 功能 / 契约缺口台账（带证据） |
| `docs/PARITY-PLAN.md`、`docs/PARITY-SUITE.md` | parity 策略与可执行套件 |
| `docs/SCHEMA-ALIGNMENT.md` | 与参考实现的常备 schema 比对（必须保持为零差异） |
| `docs/MIGRATION.md` | 从参考实现迁移已有记忆到 Wenyuan |
| `docs/ARCHITECTURE-REVIEW.md` | 架构评审：分层、耦合、发现项 |

## 参与贡献

见 [CONTRIBUTING.md](CONTRIBUTING.md)。提交 PR 前请先运行 `make check`。
安全问题请按 [SECURITY.md](SECURITY.md) 私密报告。

## 许可证

MIT —— 见 [LICENSE](LICENSE)。

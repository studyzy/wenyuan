# wenyuan（文渊）

Hindsight 智能体记忆 HTTP API 及其 PostgreSQL 存储层的 Go 实现。

> **文渊** —— 取「文之渊薮」之意，指汇聚、保存典籍学问之处。名字正对应本服务的职责：
> 一处智能体可以写入、也能从中唤回记忆的持久记忆库。

本项目实现的是 **兼容 Hindsight API** 的版本：公开 HTTP 表面（路由、JSON 字段名、状态码、
请求头）与 PostgreSQL schema（表、列、枚举）逐项保留，因此现有客户端无需修改即可对接。
实现语言为 Go，优先使用标准库。

[English README](README.md)

## 实现范围

本次切片已实现：

- **知识库（bank）生命周期** —— 创建 / 列表 / 整体替换 / 局部更新 / 删除；别名
  （列出 / 新增 / 提升为主 / 删除）；按库配置；退役路由的响应。
- **写入（retain）** —— 同步与异步记忆写入、文件上传、分块 / 实体 / 记忆持久化、
  异步操作跟踪（列表 / 查询 / 取消 / 重试）。
- **检索（recall）** —— 多路召回、融合重排、token 预算筛选、标签过滤、时间窗口、辅助通道。
- **反思（reflect）** —— 综合答案与引用证据、执行轨迹、结构化输出、指令（directive）、
  工具循环错误分类。

非目标：worker / 队列消费者、控制台 UI、部署打包、首片之外的端点、性能类新特性。

## 环境要求

- Go 1.24 或更高
- PostgreSQL 并启用 `pgvector` 与全文检索扩展（仅在使用 PostgreSQL 存储时需要；
  默认的内存存储可离线运行）

## 构建与测试

```sh
make build     # go build ./...
make test      # go test -count=1 ./...
make vet       # go vet ./...
make pgx       # go build -tags pgx ./...   （编译 PostgreSQL 驱动路径）
make check     # 以上全部
```

默认构建仅依赖标准库、不访问网络。PostgreSQL 驱动（`pgx`）隔离在 `pgx` 构建标签之后，
因此默认构建保持依赖精简且可离线。

## 运行服务

```sh
go run ./cmd/server                     # 使用配置的监听地址启动
go run ./cmd/server --host 0.0.0.0 --port 8888
```

### 服务端参数

| 参数 | 作用 |
| --- | --- |
| `--host` | 监听地址 |
| `--port` | 监听端口 |
| `--workers` | worker 并发度 |
| `--access-log` | 开启每请求访问日志 |
| `--proxy-headers` | 信任转发 / 代理头 |
| `--daemon` | 以守护模式运行 |
| `--log-level` | 日志级别 |
| `--ssl-certfile`、`--ssl-keyfile` | TLS 证书与私钥 |

### 管理命令行

```sh
bankctl bank rename --source OLD --to NEW [--dry-run]
bankctl bank archive export --archive FILE [--target ID]
bankctl bank import  --archive FILE [--target ID]
```

## 配置

优先级：命令行参数 > 环境变量 > 默认值。

| 环境变量 | 作用 |
| --- | --- |
| `HINDSIGHT_API_HOST` | 监听地址 |
| `HINDSIGHT_API_PORT` | 监听端口 |
| `HINDSIGHT_API_DATABASE_URL` | PostgreSQL 连接串 |
| `HINDSIGHT_API_DATABASE_SCHEMA` | 数据库 schema 名 |
| `HINDSIGHT_API_VECTOR_EXTENSION` | 向量扩展名 |
| `HINDSIGHT_API_TEXT_SEARCH_EXTENSION` | 全文检索扩展名 |
| `HINDSIGHT_API_RUN_MIGRATIONS_ON_STARTUP` | 启动时执行迁移 |
| `HINDSIGHT_API_MCP_ENABLED` | 启用 MCP 命名空间 |
| `HINDSIGHT_API_ENABLE_BANK_CONFIG_API` | 开放按库配置写入路由 |
| `HINDSIGHT_API_GZIP_MIN_SIZE` | 响应体 gzip 体积下限（负值关闭） |
| `HINDSIGHT_API_DROP_NULL_FIELDS` | 响应中省略 null 字段 |
| `HINDSIGHT_API_RETAIN_BATCH_ENABLED` | 开启批量写入闸门 |
| `HINDSIGHT_API_RECALL_STRATEGY_BOOSTS` | 各召回路策略加权 |
| `HINDSIGHT_API_REFLECT_SEARCH_OBSERVATIONS_INCLUDE_ENTITIES` | 反思观察默认值 |
| `HINDSIGHT_API_REFLECT_SEARCH_OBSERVATIONS_MAX_TOKENS` | 反思观察 token 默认值 |

> 环境变量名保持不变，属于 Hindsight API 的**公开兼容面**。

## HTTP API

所有按库路由都在 `/v1/default/banks` 之下。凡是接受库 id 的地方都可传入别名；
响应始终报告规范（canonical）库 id。

### 监控

| 方法 | 路径 |
| --- | --- |
| GET | `/health` |
| GET | `/health/live` |
| GET | `/health/ready` |
| GET | `/version` |
| GET | `/metrics` |

### 知识库（bank）

| 方法 | 路径 |
| --- | --- |
| GET | `/v1/default/banks` |
| POST | `/v1/default/banks` |
| PUT | `/v1/default/banks/{bank_id}` |
| PATCH | `/v1/default/banks/{bank_id}` |
| DELETE | `/v1/default/banks/{bank_id}` |
| GET | `/v1/default/banks/{bank_id}/aliases` |
| POST | `/v1/default/banks/{bank_id}/aliases` |
| PATCH | `/v1/default/banks/{bank_id}/aliases/{alias}` |
| DELETE | `/v1/default/banks/{bank_id}/aliases/{alias}` |
| GET | `/v1/default/banks/{bank_id}/config` |
| PATCH | `/v1/default/banks/{bank_id}/config` |

`GET/PUT /v1/default/banks/{bank_id}/profile` 与
`POST /v1/default/banks/{bank_id}/background` 已退役，返回 `410 Gone`，
并在消息中指出 config 读写路由作为替代。

### 记忆操作

| 方法 | 路径 |
| --- | --- |
| POST | `/v1/default/banks/{bank_id}/memories` |
| POST | `/v1/default/banks/{bank_id}/files/retain` |
| POST | `/v1/default/banks/{bank_id}/memories/recall` |
| POST | `/v1/default/banks/{bank_id}/reflect` |
| GET | `/v1/default/banks/{bank_id}/operations` |
| GET | `/v1/default/banks/{bank_id}/operations/{operation_id}` |
| DELETE | `/v1/default/banks/{bank_id}/operations/{operation_id}` |
| POST | `/v1/default/banks/{bank_id}/operations/{operation_id}/retry` |

## 存储

PostgreSQL schema 位于 `migrations/`；同一批文件在 `internal/storage/embedded/` 下被
`go:embed` 内嵌。表：`banks`、`bank_aliases`、`documents`、`chunks`、`memory_units`、
`entities`、`unit_entities`、`memory_links`、`entity_cooccurrences`、`async_operations`。

## 目录结构

```
cmd/server         进程入口（HTTP API）
cmd/bankctl        知识库管理命令行
internal/app       应用组装与中间件
internal/bankapi   知识库生命周期处理器与路由
internal/bankadmin 知识库重命名 / 归档命令
internal/config    参数 > 环境变量 > 默认值 的解析
internal/httpx     路由、中间件、JSON 与错误信封
internal/memoryops retain / recall / reflect 处理器与模型
internal/monitoring 健康、就绪、版本、指标
internal/storage   存储接口、内存与 PostgreSQL 实现
migrations         PostgreSQL schema 迁移
```

## 工程文件

| 文件 | 用途 |
| --- | --- |
| `Makefile` | 构建、测试、vet、pgx 构建目标 |
| `CHANGELOG.md` | 各版本重要变更（Keep a Changelog） |
| `CONTRIBUTING.md` | 如何提交改动 |
| `CODE_OF_CONDUCT.md` | Contributor Covenant v2.1 |
| `SECURITY.md` | 如何私密报告安全问题 |
| `.github/workflows/ci.yml` | 每次 push / PR 的构建、测试、vet、gofmt |
| `.github/ISSUE_TEMPLATE/` | Bug 与功能需求表单 |
| `docs/ROADMAP.md` | 未完成工作与已知局限（交接文档） |
| `docs/MIGRATION.md` | 从 Hindsight 迁移已有记忆到 Wenyuan |

## 参与贡献

见 [CONTRIBUTING.md](CONTRIBUTING.md)。提交 PR 前请先运行 `make check`。
安全问题请按 [SECURITY.md](SECURITY.md) 私密报告。

## 许可证

MIT —— 见 [LICENSE](LICENSE)。
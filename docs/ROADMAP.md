# Wenyuan 未完成工作与待完善清单（交接文档）

> 本文档写给后续接手的人。它列出**当前已经完成的内容**、**明确未完成的工作**、
> **已知的取舍与局限**，以及**每一项的建议处理方式**。文中每条都标了证据位置
> （文件:行号、测试名），可以据此直接定位。
>
> 现状基线（编写时）：277 个测试全绿，`go build` / `go vet` / `gofmt` / `go build -tags pgx`
> 全部通过；CI（`.github/workflows/ci.yml`）在 push 与 PR 上运行同样的四项检查。

---

## 0. 一句话现状

Wenyuan 是一个**兼容 Hindsight HTTP API** 的 Go 服务：**公开 HTTP 契约面（路由、JSON 字段、
状态码、请求头）已实现并有测试覆盖**；**存储层与上游 Hindsight 不兼容**（不能直接指向已有
Hindsight 数据库），因此提供了一条**通过官方导出格式迁移数据**的路径。

因此「平替」的含义是：
- ✅ 客户端无需改代码即可对接。
- ✅ 已有 Hindsight 记忆可通过 `bankctl bank migrate` 导入。
- ❌ 不能把 Wenyuan 直接指向 Hindsight 的 PostgreSQL 库。

---

## 1. 未完成的 API 能力

### 1.1 语义检索（向量）是占位实现 —— 优先级：高

**现状**：召回中的 semantic / keyword 打分是确定性占位算法，**不是真实的向量相似度**。

- 证据：`internal/storage/memory_recall_arms.go:167-169`
  ```go
  // semanticScore is a deterministic stand-in for vector similarity: shared word ...
  func semanticScore(m RecallMatch, term string) float64
  ```
- 同文件 `:187` keywordScore 同样是占位。
- 数据库中**没有任何向量列**：`migrations/` 全系列无 `vector(N)`；
  `internal/storage/schema.go` 里曾有的 `embedding` 是幽灵列，已在本次修复中移除。

**影响**：recall 的排序质量与上游 Hindsight 存在实质差距；对"语义相近但用词不同"的查询会漏召回。

**建议实现路径**：
1. 新增迁移 `0007_memory_unit_embedding.sql` 加入 `embedding vector(384)`（维度须与部署一致，
   上游为 384；见 `config.go` 的 `DefaultVectorExtension`）。
2. `CREATE EXTENSION IF NOT EXISTS vector` + HNSW/IVFFlat 索引。
3. 在 retain 写入路径生成 embedding（需要接入一个 embedding provider；当前项目**没有任何
   LLM/provider 集成**——见 §1.4）。
4. 在 `internal/storage/postgres_recall.go` 的 semantic arm 用 `<=>` 距离查询替换占位评分。

**注意**：这需要引入外部模型调用，是当前项目**最大的一块缺失能力**。

### 1.2 异步操作不是真正后台执行 —— 优先级：中

**现状**：`POST .../memories` 的 `async=true` 会**接受并返回 operation 记录**，但没有后台 worker
真正消费；也没有独立的 worker 进程入口。

- 证据：`internal/memoryops/retain.go` 中无 `go func` / goroutine 派发；
  `cmd/` 下只有 `server` 与 `bankctl`，无 worker 入口。
- 上游 Hindsight 有独立的 worker 服务（消费 PG 任务队列）。

**影响**：异步写入的"完成信号"可用，但实际处理是同步完成的（当前语义上等价，只是没有
真正的异步队列/重试调度）。

**建议**：新增 `cmd/worker`，实现 `async_operations` 表的领取（claim）→ 处理 → 完成/重试循环，
复用 `storage` 的 `AdvanceOperation` / `ListOperations`。

### 1.3 导出 / 导入端点未实现 —— 优先级：中

**现状**：Wenyuan **没有** `/v1/default/banks/{id}/export` 与 `/import` 端点
（这两个端点在 spec 中的 `public_surface` 也未被列入本切片）。

- 证据：`internal/memoryops/routes.go`、`internal/bankapi/routes.go` 中无 export/import 路由。
- 但**迁移工具已实现了上游导出格式的解析与导入**（`internal/hindsightarchive` +
  `internal/migration/hindsight`），所以补端点主要是把它接到 HTTP 层。

**建议**：加 `POST /v1/default/banks/{id}/export`（产出同格式 ZIP）与 `/import`（消费 ZIP），
复用现成的解析/映射代码。

### 1.4 无 LLM / Provider 集成 —— 优先级：高（与 §1.1 耦合）

**现状**：reflect 的"综合答案"、retain 的"实体抽取/事实抽取"目前都是**确定性的本地实现**，
不调用任何真实模型。

- 证据：`internal/storage/memory_reflect.go`（合成逻辑在本地）、
  `internal/memoryops/reflect_synthesis.go`。
- 上游 Hindsight 通过 provider 抽象接入多家 LLM。

**影响**：reflect 的答案质量、retain 的抽取质量与上游差距最大。

**建议**：定义 `Provider` 接口（完成/embedding），先接一家（如 OpenAI 兼容端点），
再让 retain/reflect 走该接口；`internal/config` 增加 provider 配置项。

### 1.5 MCP 传输未实现 —— 优先级：低

**现状**：`/mcp` 命名空间已挂载但返回 422 "not implemented"。
- 证据：`internal/httpx/router.go:123`。

### 1.6 `files/retain` 多部分上传 —— 优先级：低（部分实现）

**现状**：路由已绑定、异步语义、请求级字段拒绝已实现（`internal/memoryops/file_retain.go`），
但附件内容落盘/存储链路未完整（上游有 `file_storage`/`attachments` 表）。

---

## 2. 存储层：与上游 Hindsight 的差异（不能直接复用已有库）

**结论：Wenyuan 的 schema 与 Hindsight 不同，无法直接指向已有 Hindsight 数据库。**

已核实的差异（这些是**设计事实**，不是 bug）：

| 维度 | Hindsight | Wenyuan |
|---|---|---|
| 迁移体系 | 112 个 Alembic 迁移 + `alembic_version` 表 | 6 个手写 SQL，**无版本表**，每次启动重跑（靠 `IF NOT EXISTS` 幂等） |
| `banks` 主键 | `bank_id` | `id` |
| `banks` 画像列 | `disposition JSONB`、`mission`、`config`、`internal_id` | `name`、`disposition_skepticism/literalism/empathy`、`reflect_mission` |
| `memory_units` | 含 `embedding vector(384)`、`context`、`event_date`、`fact_type`、`confidence_score`、`access_count` | `type`（非 `fact_type`）、`chunk_id`、`tags`、`source_fact_ids`；`context`/`event_date`/`confidence_score`/`access_count` 已由 `0006` 补齐 |
| `entities` 名称列 | `canonical_name` | `name` |
| 向量 | `vector(384)` + HNSW/IVFFlat | 无 |

### 2.1 迁移工具（已实现）—— 使用方式

```sh
# 1) 从 Hindsight 导出整库
curl -X POST http://<hindsight>:8888/v1/default/banks/<bank_id>/export -o bank.zip

# 2) 导入 Wenyuan
bankctl bank migrate --archive bank.zip --replace          # 幂等重建（默认）
bankctl bank migrate --archive bank.zip --append           # 追加合并
bankctl bank migrate --archive bank.zip --dry-run          # 只报告不写库
bankctl bank migrate --archive bank.zip --bank-id TARGET   # 指定目标 bank id
```

代码位置：
- 归档解析：`internal/hindsightarchive/`（`archive.go` 类型、`zip.go` 解压）
- 映射与导入：`internal/migration/hindsight/`（`map.go` 映射、`run.go` 导入）
- CLI：`internal/bankadmin/migrate.go`（解析）、`cmd/bankctl/main.go`（分发）

### 2.2 迁移工具的已知局限 —— 优先级：中

1. **依赖上游导出格式**：迁移读的是 Hindsight `SCHEMA_VERSION = 1` 的 ZIP。
   如果上游升级格式版本，`internal/hindsightarchive/archive.go:24` 的
   `SupportedSchemaVersion` 需同步，并扩展类型。
2. **实体 ID 是派生的**：`map.go` 的 `entityID()` 用 `bank_id + name` 哈希生成实体 id
   （因为导出只带实体**名**，不带 id）。若同一 bank 内存在同名但语义不同的实体，会合并。
3. **关系目标用序号定位**：`CausalRelation.TargetFactIndex` 是文档内 fact 的序号
   （上游设计如此），导入时按序号映射；若上游改变该语义需同步。
4. **无增量同步**：迁移是"快照式"的。迁移期间若旧实例仍在写入，新增数据会丢失。
   **建议：导出后停止旧实例写入，再执行迁移。**
5. **未映射字段**：迁移会在结束时打印"无目标列的字段"清单（见 `map.go` 的
   `unmappedFields()`），当前包括 `source_id`、`observation_scopes`、
   `consolidated_at`/`consolidation_failed_at`、`document.retain_params`。
   如需保留，需先给 Wenyuan 补列。

### 2.3 未实现的存储能力 —— 优先级：低

- **跨进程缓存失效**：per-process 缓存按时间失效，跨 worker 进程的一致性保证未定义。
- **索引/约束的精确对齐**：Wenyuan 只有 3 个显式索引（`idx_bank_aliases_bank`、
  `idx_bank_aliases_one_primary`、`idx_documents_retain_params`），上游有一整套
  （trigram、GIN、向量）。若有性能要求需补。
- **`async_operations.error` 是遗留列**：`0001` 建了 `error`，`0005` 又加了 `error_message`；
  代码只用后者，`error` 是死列。可择机清理。

---

## 3. 契约面待确认项（来自规格的 deferred 清单）

以下 14 项是**规格层面未钉死的公开契约边界**。当前实现取了合理默认值，但**未与上游逐一核对**。
如果要求"逐字节兼容"，需要逐条确认。

| # | 待确认项 | 当前实现 | 影响面 |
|---|---|---|---|
| 1 | 公开路由 → operation identity 的权威映射，以及哪些 operation identity 属于公开兼容面 | 按路由 path+method 处理，未暴露 operation identity | 生成式客户端 / tracing |
| 2 | 哪些废弃请求字段别名需永久接受、哪些有移除期限 | 接受了已知别名（如 `background`/`reflect_mission`） | 请求兼容 |
| 3 | 非校验类错误体（not-found/conflict/gone/unavailable/timeout）的精确形状 | 统一 `{"detail": ...}` 信封 | 客户端错误处理 |
| 4 | disposition traits 的确切默认值 | 中性值 3 | 行为兼容 |
| 5 | recall 的 trace 对象是否为公开面及其字段集 | 未实现 trace 对象的完整字段 | 客户端依赖 |
| 6 | 每-bank 配置覆盖键的完整权威集合 | 仅 disposition traits + reflect mission | 配置 API |
| 7 | 跨进程缓存失效保证 | 仅时间失效 | 分布式一致性 |
| 8 | 非校验类写错误体的精确形状（是否都带 item 列表） | 仅 blocked-write 带 violations | 客户端错误处理 |
| 9 | 部署依赖的索引/约束精确集合；entity-kind→索引选择是否公开面 | 未实现该选择逻辑 | 性能/存储兼容 |
| 10 | 写路径中视为公开的配置名边界 | 仅单个 batch gate | 配置兼容 |
| 11 | 自动 document-id 生成是否需匹配确切形式，还是仅分组行为 | 仅分组行为 | 写语义 |
| 12 | processed-content-token 计数是否为公开响应字段 | 未作为公开字段暴露 | 响应兼容 |
| 13 | reflection loop 需要 per-bank perspectives/directives 的公开读取契约 | 未实现该读取路径 | reflect 能力 |
| 14 | 未知 **body** 字段未在 `X-Ignored-Params` 上报（只上报 query 参数） | 仅 query 参数上报 | 前向兼容 |

> #14 的实现要点：`internal/httpx/middleware.go` 的 `IgnoredParamsMiddleware` 目前只处理
> 查询参数（`Store.queryparams.go`）。上游同时上报未知 body 字段，需要按路由定义
> "接受的 body 字段集"才能补齐。

---

## 4. 工程质量与运维待完善项

### 4.1 明确排除的目标（设计如此，无需实现）

以下在规格中已声明为**非目标**，除非需求变化，否则不需要做：
- Control Plane UI（端口 9999）
- docker/compose/pip 部署形态与安装器
- 首片之外的 API 端点
- 性能类新特性

### 4.2 建议补充的工程内容 —— 优先级：低

| 项 | 现状 | 建议 |
|---|---|---|
| 端到端集成测试 | 有 277 个单元/包测试；`internal/app/parity_integration_test.go` 有 8 个移植自上游的 HTTP 契约用例 | 补一个真正起服务 + 连 PG 的 e2e（当前沙箱禁 `net.Listen`，需在 CI 或本地跑） |
| PostgreSQL 路径测试 | 大部分测试用内存 store；`-tags pgx` 只验证编译 | 加 testcontainers 或 CI 里的 PG 服务，跑通 `postgres_*` 的实际 SQL |
| 基准测试 | 无 | 为 recall 的多路融合、迁移导入加 benchmark |
| 覆盖率门禁 | 无 | CI 加 `go test -cover` 阈值 |
| 容器化 | 无 Dockerfile | 如需部署，补多阶段 Dockerfile |
| 版本与发布 | `CHANGELOG.md` 有 `[Unreleased]`；无 tag、无 release | 打 `v0.1.0` tag 并走 release 流程 |
| 依赖升级 | `go.mod` 固定 pgx v5.5.4 等 | 定期 `go get -u` + 回归 |

### 4.3 已知的代码层小问题 —— 优先级：低

1. **`schema.go` 的列清单与真实 DDL 曾不一致**：`MemoryUnitsColumns` 曾列 `embedding`
   （不存在），`EntitiesColumns` 列 `metadata`（不存在）。`embedding` 已修；
   **`entities.metadata` 仍是幽灵列**，建议一并核对清理。
   - 证据：`internal/storage/schema.go` 的 `EntitiesColumns`。
   - 相关测试只校验"清单里有没有某名字"，不比对真实 DDL，所以能通过 CI。
2. **`async_operations.error` 死列**（见 §2.3）。
3. **`documents` 的 `metadata`/`retain_params` 从不被写入**：`postgres_retain.go` 的
   `PutDocument` INSERT 未包含这两列，虽然 Go 结构体里有值。若要用需补写入。

---

## 5. 建议的接手顺序

如果要继续把 Wenyuan 往"完整平替"推进，建议顺序：

1. **补 PG 端到端测试**（§4.2）——先建立"存储层真的对"的信心，再动 schema。
2. **实现 provider 接口 + embedding**（§1.4 → §1.1）——这是收益最大的一块。
3. **async worker 进程**（§1.2）——让异步语义名副其实。
4. **export/import HTTP 端点**（§1.3）——迁移工具已就绪，接线即可。
5. **逐条核对 14 项契约待确认项**（§3）——只在要求"逐字节兼容"时才需要。
6. **清理幽灵列与死列**（§4.3）。

---

## 6. 相关代码地图

```
cmd/server            进程入口（HTTP API）
cmd/bankctl           管理命令：bank rename / archive / migrate
internal/app          应用组装、中间件、认证
internal/bankapi      知识库生命周期 + 别名 + 配置 + 退役路由
internal/bankadmin    rename / archive / migrate 命令实现
internal/config       参数 > 环境变量 > 默认值
internal/httpx        路由、中间件、JSON/错误信封、忽略参数上报
internal/hindsightarchive  Hindsight 导出 ZIP 解析（§2.1）
internal/memoryops    retain / recall / reflect 处理器与请求响应模型
internal/migration/hindsight  导出→Wenyuan 映射与导入（§2.1）
internal/monitoring   health / live / ready / version / metrics
internal/storage      存储接口 + 内存实现 + PostgreSQL 实现 + 迁移
migrations/           PostgreSQL schema（0001..0006）
```

**迁移系列约束**：`internal/storage/migrate.go` 要求版本号从 1 连续到 N，新增迁移必须是
`0007_*.sql` 且同时在 `internal/storage/embedded/` 放一份（`go:embed` 用）。

---

## 7. 如何验证当前状态

```sh
make check                 # build + test + vet + pgx build
gofmt -l .                 # 应无输出
go test -count=1 ./...     # 277 个测试
gh run list --repo studyzy/wenyuan   # CI 状态
```
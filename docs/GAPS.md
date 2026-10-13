# Wenyuan 功能缺口与待办清单（净室重写交接文档）

> 本文档是**实现清单**，供后续净室重写 AI 逐项实现。
> 只覆盖**服务后端**；前端 / Control Plane（9999）不在范围内。
>
> 参照基线：Hindsight 服务端 `hindsight-api-slim/hindsight_api/`，
> 公开 HTTP 路由 **99 条 + MCP 命名空间**。
> Wenyuan 当前实际挂载 **22 条路由**（含 3 条 retired 返回 410）。
>
> **重要**：本文所述"现状"均已对 `wenyuan` 当前代码逐条核对；
> `docs/ROADMAP.md` 中的部分描述写于更早基线，与代码有出入处以本文为准。

---

## 0. 图例与约定

| 标记 | 含义 |
|---|---|
| ✅ | Wenyuan 已实现且已核对 |
| ❌ | Wenyuan 完全缺失 |
| ⚠️ | 部分实现 / 为占位实现 |
| 🔁 | 已有底层能力，仅需接线到 HTTP |
| 🚫 | 主动 retired（返回 410），**非遗漏**，不要重写 |

参照实现的路径均相对 `hindsight-api-slim/hindsight_api/`。

---

## 1. 当前已达成的兼容面（重写时不要破坏）

**已实现的路由（22 条）：**

```
GET    /health  /health/live  /health/ready  /version  /metrics
GET    /v1/default/banks
PUT    /v1/default/banks/{bank_id}
PATCH  /v1/default/banks/{bank_id}
DELETE /v1/default/banks/{bank_id}
GET    /v1/default/banks/{bank_id}/aliases
POST   /v1/default/banks/{bank_id}/aliases
PATCH  /v1/default/banks/{bank_id}/aliases/{alias}
DELETE /v1/default/banks/{bank_id}/aliases/{alias}
GET    /v1/default/banks/{bank_id}/config
PATCH  /v1/default/banks/{bank_id}/config
POST   /v1/default/banks/{bank_id}/memories        （retain，含 async 受理）
POST   /v1/default/banks/{bank_id}/files/retain    （multipart，见 A6）
POST   /v1/default/banks/{bank_id}/memories/recall
POST   /v1/default/banks/{bank_id}/reflect
GET    /v1/default/banks/{bank_id}/operations
GET    /v1/default/banks/{bank_id}/operations/{operation_id}
DELETE /v1/default/banks/{bank_id}/operations/{operation_id}
POST   /v1/default/banks/{bank_id}/operations/{operation_id}/retry
```

**主动 retired（🚫 不要重写，保持 410）：**

```
GET  /v1/default/banks/{bank_id}/profile
PUT  /v1/default/banks/{bank_id}/profile
POST /v1/default/banks/{bank_id}/background
```

证据：`internal/bankapi/retired_routes.go:34-38`。

**并存约束（AGENTS.md，重写时必须遵守）：**

- 公开面冻结：路由、JSON 字段名、状态码、响应头、PG 表名 / 列名 / 枚举值必须逐字保留。
- 标准库优先：`net/http`、`encoding/json`、`database/sql`。
- 无 LLM 依赖的默认构建须可离线编译；PG 驱动仅在 `-tags pgx` 下编译（`internal/storage/driver_pgx.go`）。**`make build` 与 `make pgx` 必须都过。**
- 存储接口任何新方法须在 **memory 与 postgres 两个后端**同时实现（`internal/storage/store.go`）。
- 不得在 `internal/bankapi` / `internal/memoryops` 中写 SQL；只能经 `internal/storage`。边界由 `internal/storage/boundary_test.go` 强制。
- 归属表固定为：`banks, bank_aliases, documents, chunks, entities, memory_units, unit_entities, memory_links, entity_cooccurrences, async_operations`。
- 迁移文件**两处副本**须同步：`migrations/` 与 `internal/storage/embedded/`；编号必须从 1 连续到 N，新迁移为 `0007_*.sql`。
- 单文件 ≤500 行，测试文件 ≤800 行。

---

## 2. A 类：核心引擎能力缺失（阻塞性，最高优先级）

### A1. 真实向量语义检索 — ❌ 高

**现状**：`semanticScore` / `keywordScore` 为确定性占位算法，非向量相似度。

- 证据：`internal/storage/memory_recall_arms.go:167`（`semanticScore`）、`:187`（`keywordScore`）。
- `migrations/` 全系列无 `vector(N)` 列。

**参照**：`engine/search/retrieval.py`、`reranking.py`、`cross_encoder.py`、`embeddings.py`、`engine/jina_mlx_reranker.py`

**实现要点**：
1. 新增迁移 `0007_memory_unit_embedding.sql`：`embedding vector(384)`（维度须与部署一致，上游为 384）+ `CREATE EXTENSION IF NOT EXISTS vector` + HNSW / IVFFlat 索引。
2. 在 retain 写路径生成 embedding（依赖 A2）。
3. 将 `postgres_recall.go` 的 semantic arm 换成 `<=>` 距离查询。
4. `internal/storage/schema.go` 补列声明。

---

### A2. LLM Provider 层 — ❌ 高（**所有质量差距的根因**）

**现状**：项目**任何位置都没有 LLM 调用**。reflect 的"综合答案"、retain 的"实体/事实抽取"均为本地确定性实现。

- 证据：`internal/storage/memory_reflect.go`、`internal/memoryops/reflect_synthesis.go`。

**参照**：`engine/providers/`（**23 个适配器**）+ `engine/llm_interface.py`、`llm_wrapper.py`、`multi_llm.py`、`structured_output.py`、`llm_transport.py`、`provider_auth.py`

**参照适配器清单**（择需实现，建议先做 OpenAI 兼容 + Anthropic）：
`anthropic_llm`、`openai_compatible_llm`、`openai_responses_llm`、`gemini_llm`、`litellm_llm`、`litellm_router_llm`、`claude_code_llm`、`codex_llm`、`cursor_llm`、`github_copilot_llm`、`xai_oauth_llm`、`nous_llm`、`llamacpp_llm`、`fireworks_llm`、`none_llm`、`mock_llm` 等；OAuth 鉴权见 `codex_auth.py`、`xai_oauth_auth.py`、`nous_auth.py`。

**实现要点**：
1. 定义 `Provider` 接口（completion / embedding）。
2. 先集成一个厂商（OpenAI 兼容端点）。
3. 在 `internal/config` 增加 provider 配置项（遵循 `internal/config/` 的 flags > env > .env > defaults 优先级）。
4. retain / reflect 经该接口路由。

---

### A3. retain 事实与实体抽取 — ❌ 高（依赖 A2）

**现状**：无 LLM 驱动抽取。

**参照**：`engine/retain/fact_extraction.py`、`entity_processing.py`、`entity_labels.py`、`orchestrator.py`、`link_creation.py`、`fold.py`、`memory_budget.py`

**对照 Wenyuan 已有**：`internal/storage/memory_retain.go`、`postgres_retain.go`、`internal/memoryops/retain.go`

---

### A4. reflect agentic loop — ⚠️ 高（依赖 A2）

**现状**：有路由与本地 synthesis，但**无工具调用循环**。

**参照**：`engine/reflect/agent.py`（agentic loop，**≤10 轮**；工具优先级 `search_mental_models → search_observations → recall → expand → done`）、`tools.py`、`tools_schema.py`、`observations.py`、`retractions.py`、`delta_ops.py`、`structured_doc.py`、`presentation.py`

**对照 Wenyuan 已有**：`internal/memoryops/reflect.go`、`reflect_synthesis.go`、`reflect_validation.go`、`storage/reflect_tools.go`、`memory_reflect.go`

---

### A5. 后台 Worker 进程 — ❌ 高

**现状**：`async=true` 受理并返回 operation 记录，但**没有 worker 消费队列**，写入实际同步完成；`cmd/` 下只有 `server` 和 `bankctl`。

- 证据：`internal/memoryops/retain.go` 无 goroutine 派发；`cmd/` 无 worker 入口。

**参照**：`worker/main.py`、`poller.py`（PG 队列轮询）、`stage.py`、`backpressure.py`、`exceptions.py`；上游 worker 与 API **同镜像不同入口** `hindsight-worker`，metrics 端口 8889。

**实现要点**：
1. 新增 `cmd/worker`。
2. 实现 claim → process → complete/retry 循环，消费 `async_operations` 表。
3. 复用 `storage` 层已有 `AdvanceOperation` / `ListOperations`。

---

### A6. `files/retain` 附件持久化 — ⚠️ 低

**现状**：路由已绑定，async 语义与请求级字段拒绝已实现；但**附件内容持久化路径不完整**（上游有 `file_storage` / `attachments` 表）。

- 证据：`internal/memoryops/file_retain.go`。

**参照**：`engine/retain/attachment_content.py`、`attachment_store.py`；`engine/storage/`（`s3.py` / `gcs.py` / `azure.py` / `postgresql.py`）

---

### A7. Consolidation（巩固）与 Mental Model 刷新 — ❌ 中

**参照**：`engine/consolidation/consolidator.py`、`prompts.py`、`engine/mental_model_refresh.py`

---

## 3. B 类：HTTP 路由缺失（按参照模块分组，便于逐块重写）

> 以下所有路径前缀均为 `/v1/default/banks/{bank_id}`，除另行标注。

### B1. Mental Models — ❌ 9 条

```
GET    /mental-models
GET    /mental-models/{mental_model_id}
GET    /mental-models/{mental_model_id}/history
POST   /mental-models
POST   /mental-models/{mental_model_id}/refresh
POST   /mental-models/{mental_model_id}/dry-run-refresh
POST   /mental-models/{mental_model_id}/clear
PATCH  /mental-models/{mental_model_id}
DELETE /mental-models/{mental_model_id}
```

参照：`engine/mental_model_refresh.py`、`engine/response_models.py:718`（`ConsolidationStrategySpec`）

---

### B2. Knowledge Base — ❌ 8 条

```
GET    /knowledge-base/tree
POST   /knowledge-base/folders
POST   /knowledge-base/pages
GET    /knowledge-base/export
GET    /knowledge-base/search
GET    /knowledge-base/pages/{page_id}
PATCH  /knowledge-base/nodes/{node_id}
DELETE /knowledge-base/nodes/{node_id}
```

---

### B3. Directives — ❌ 5 条

```
GET    /directives
GET    /directives/{directive_id}
POST   /directives
PATCH  /directives/{directive_id}
DELETE /directives/{directive_id}
```

参照：`engine/directives/models.py`（Wenyuan 仅一处注释引用）

---

### B4. 导出 / 导入 / 克隆 / 附件下载 — 🔁 8 条（**底层已就绪，仅需接线**）

```
POST /import
GET  /export
POST /document-transfer/export
POST /document-transfer
POST /transfer/export
POST /transfer/import
POST /clone
GET  /attachments/{attachment_id}
GET  /v1/default/files/download/{key:path}     ← 注意：非 bank 前缀
```

**🔁 现成基建**：
- 归档解析：`internal/hindsightarchive/`（`archive.go` 类型、`zip.go` 解压）
- 映射与导入：`internal/migration/hindsight/`（`map.go`、`run.go`）
- CLI：`internal/bankadmin/migrate.go`、`cmd/bankctl/main.go`

参照：`engine/transfer/export.py`、`importer.py`、`stream_archive.py`、`schema.py`

---

### B5. Documents / Chunks / Tags / Entities / Graph — ❌ 13 条

```
GET    /documents
GET    /documents/{document_id:path}/chunks
POST   /documents/{document_id:path}/reprocess
GET    /documents/{document_id:path}
PATCH  /documents/{document_id:path}
DELETE /documents/{document_id:path}
GET    /tags
GET    /v1/default/chunks/{chunk_id:path}      ← 非 bank 前缀
GET    /graph
GET    /entities
GET    /entities/graph
GET    /entities/{entity_id}
POST   /entities/{entity_id}/regenerate
```

参照：`engine/retain/chunk_storage.py`、`entity_processing.py`、`engine/graph_maintenance.py`、`engine/search/tags.py`、`tag_resolution.py`

---

### B6. Memories 子路由 — ❌ 6 条

```
GET    /memories/list
GET    /memories/{memory_id}
PATCH  /memories/{memory_id}
GET    /memories/{memory_id}/history
POST   /memories/dry-run-extract
POST   /prompts/preview
DELETE /memories                              ← 批量删除（Wenyuan 仅有 POST /memories）
```

参照：`engine/prompt_preview.py`

---

### B7. 审计 / 可观测性 — ❌ 8 条

```
GET  /stats
GET  /stats/memories-timeseries
POST /health/llm
GET  /audit-logs
GET  /audit-logs/stats
GET  /llm-requests
GET  /llm-requests/stats
```

参照：`engine/audit.py`（`AuditEntry` / `AuditLogger`）、`engine/llm_trace.py`、`engine/metrics.py`、`metrics_multiworker.py`

---

### B8. Webhooks — ❌ 5 条

```
POST   /webhooks
GET    /webhooks
DELETE /webhooks/{webhook_id}
PATCH  /webhooks/{webhook_id}
GET    /webhooks/{webhook_id}/deliveries
```

参照：`webhooks/manager.py`、`url_guard.py`（**SSRF 防护：必须实现 URL 校验 / allowlist**）、`models.py`

---

### B9. Observations / Consolidation — ❌ 6 条

```
DELETE /observations
GET    /observations/scopes
POST   /consolidation-strategies/preview
POST   /consolidation/recover
DELETE /memories/{memory_id}/observations
POST   /consolidate
```

参照：`engine/reflect/observations.py`、`engine/consolidation/`

---

### B10. 其他契约项 — ❌

```
POST   /ext/*                                ← 扩展命名空间；参照 extensions/（bank_tables, tenant, memory_defense, operation_validator, http, mcp）
POST   /mcp                                  ← Wenyuan 已挂载但返回 422「not implemented」
                                               证据：internal/httpx/router.go:122
GET    /v1/bank-template-schema
GET    /v1/version                           ← Wenyuan 仅有 /version
DELETE /config                               ← Wenyuan 仅有 GET / PATCH
POST   /operations/{operation_id}/delete     ← Wenyuan 仅有 DELETE /operations/{id}
```

参照：`api/mcp.py`（MCP 传输）、`hindsight_api/extensions/`、`internal/bankapi/config_keys.go`（config 键扩展）

---

## 4. C 类：存储层 / 数据契约缺口

| # | 缺口 | 现状证据 | 参照 |
|---|---|---|---|
| C1 | `documents.metadata` / `retain_params` 从未写入 | `postgres_retain.go` 的 `PutDocument` INSERT 未含这两列，尽管 Go 结构体有值 | `PutDocument` 全量字段 |
| C2 | 迁移丢失字段 | `unmappedFields()`（`migration/hindsight/map.go`）报告 `source_id`、`observation_scopes`、`consolidated_at`、`consolidation_failed_at`、`document.retain_params` 无目标列 | 需先加列 |
| C3 | 索引 / 约束不足 | 仅 3 个显式索引（`idx_bank_aliases_bank`、`idx_bank_aliases_one_primary`、`idx_documents_retain_params`）；参照有 trigram / GIN / HNSW / IVFFlat 全套 | `alembic/` |
| C4 | 无法直连 Hindsight 库 | 表结构差异：`banks.id` vs `bank_id`；`entities.name` vs `canonical_name`；`memory_units.type` vs `fact_type`；无 `embedding` | 设计取舍，经导出格式迁移 |
| C5 | 无版本表 | `migrations/` 6 个手写 SQL，无 `alembic_version` 等价物，每次启动重跑（`IF NOT EXISTS` 幂等） | `alembic/` 112 迁移 |
| C6 | 跨进程缓存失效 | `bank_cache.go` 仅时间过期，多 worker 进程一致性未定义 | — |
| C7 | 实体 ID 派生 | `entityID()` 以 `bank_id + name` 哈希生成；同名不同实体将被合并 | 导出仅带实体名 |
| C8 | 无增量同步 | 迁移为快照式；导出后旧实例继续写入会丢数据 | — |

---

## 5. D 类：工程与运维缺口

| # | 缺口 | 现状 |
|---|---|---|
| D1 | 无 PG 端到端测试 | `-tags pgx` 仅验证编译，真实 SQL 未执行 |
| D2 | 无真实 e2e | 仅 `internal/app/parity_integration_test.go` 8 个 HTTP 契约用例 |
| D3 | 无基准测试 | recall 多路融合、migration 导入无 benchmark |
| D4 | 无容器化 / 部署形态 | 参照有 `docker/standalone` 单容器、docker-compose 14 变体、helm chart、`pip install hindsight-api`、嵌入式 `hindsight-all`（含 `pg0`） |
| D5 | 无 Release / Tag | `CHANGELOG.md` 停在 `[Unreleased]` |
| D6 | `entities.metadata` 幽灵列 | `internal/storage/schema.go` 的 `EntitiesColumns` 列出但 DDL 无此列 |
| D7 | `async_operations.error` 死列 | `0001` 建 `error`，`0005` 加 `error_message`，代码只用后者 |
| D8 | 无覆盖率门槛 | CI 无 `go test -cover` 阈值 |

---

## 6. 契约待确认项（14 条，来自早期规范）

以下为**规范未钉死的公开契约边界**。当前实现取了合理默认值，但**未逐条与上游核对**。若要求逐字节兼容，需逐项确认。

| # | 待确认项 | 当前实现 |
|---|---|---|
| 1 | 公开路由 → operation 身份的权威映射 | 仅按路由 path+method，operation 身份不外露 |
| 2 | 哪些废弃请求字段别名须永久接受 | 已知别名接受（如 `background` / `reflect_mission`） |
| 3 | 非校验类错误体形状（not-found/conflict/gone/unavailable/timeout） | 统一 `{"detail": ...}` 信封 |
| 4 | disposition 特质默认值 | 中性值 3 |
| 5 | recall trace 对象是否为公开面及其字段集 | **未实现完整 trace 字段** |
| 6 | 每 bank 配置覆盖键的权威全集 | 仅 disposition 特质 + reflect mission |
| 7 | 跨进程缓存失效保证 | 仅时间驱动 |
| 8 | 非校验类写错误体形状（是否都带 item 列表） | 仅 blocked-write 带 violations |
| 9 | 部署依赖的精确索引 / 约束集 | **未实现** entity-kind → 索引选择逻辑 |
| 10 | 写路径上哪些配置名属公开面 | 仅单一 batch gate |
| 11 | 文档 ID 自动生成是否须精确形式 | 仅保证分组行为 |
| 12 | 已处理内容 token 数是否为公开响应字段 | 未作为公开字段暴露 |
| 13 | reflect 循环需要每 bank perspectives/directives 的公开读契约 | **该读路径未实现** |
| 14 | 未知 **body** 字段是否上报 `X-Ignored-Params` | 仅查询参数上报（`internal/httpx/middleware.go` 的 `IgnoredParamsMiddleware` 只处理 query） |

---

## 7. 建议重写顺序

1. **A2 LLM Provider 层** — 是所有质量差距的根因（A1 / A3 / A4 均依赖它）。
2. **A1 向量检索** — `0007_memory_unit_embedding.sql` + `vector` 扩展 + HNSW 索引。
3. **A5 Worker 进程** — `cmd/worker`，消费 `async_operations`，复用 `AdvanceOperation` / `ListOperations`。
4. **B4 导出 / 导入接线** — 🔁 底层 `hindsightarchive` + `migration/hindsight` 已就绪，纯接 HTTP。
5. **B1 Mental Models → B3 Directives → B9 Observations/Consolidation** — 均为 A7 巩固的前置。
6. **B5 / B6 Documents·Memories 子路由 → B7 审计 → B8 Webhooks**。
7. **C1 / C2 存储字段补齐 → D1 PG e2e → 剩余 C / D 项**。

---

## 8. 验证命令

```sh
make check                 # build + test + vet + pgx build + lint
make pgx                   # CGO_ENABLED=0 go build -tags pgx ./...   ← 不可跳过
gofmt -l .                 # 应无输出
go test -count=1 ./...     # 全量测试
gh run list --repo studyzy/wenyuan   # CI 状态
```

---

## 9. 已排除的目标（无需实现）

按早期规范为**非目标**：

- Control Plane UI（端口 9999）
- docker / compose / pip 部署形态与安装器
- 首页切片之外的 API 端点（本清单即为其展开）
- 新的性能相关特性
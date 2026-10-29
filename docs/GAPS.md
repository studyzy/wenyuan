# Wenyuan 功能缺口与待办清单（净室重写交接文档）

> **✅ 2026-10-29 v1.1.0 复刻收口**：针对参考 v1.1.0 发布的整轮逐路对照复核已完成——
> 15 个并行切片逐一核对 v1.0.0→v1.1.0 差量在后端的复刻情况，发现的约 60 项分歧全部修复
> （consolidation 的 v1.1.0 提示词与删除合并语义、reflect 的 closing-step 流程、占位日期、
> MCP 工具面、graph 边预算、transfer 证据重映射、reranker 候选上限、
> `min_refresh_interval_seconds` 生效等），逐项证据见 `CHANGELOG.md` 的
> "Fixed — the v1.1.0 port completed" 一节。Schema 以干净的上游 v1.1.0 alembic 目录复核：
> 两侧目录逐对象完全一致（`docs/SCHEMA-ALIGNMENT.md` 全零差异）；路由面以所服务
> `openapi.json` 与上游文档逐操作比对确认一致。

> **✅ 2026-10-14 状态**：`docs/PARITY-PLAN.md` 的全部五个阶段（A 契约面、B 引擎、C 子系统、
> D 运维）**均已收口**，逐条实现记录、验收命令与"真实运行"证据见该文 §8。本文件保留为
> 早期基线的对照材料；凡与 `PARITY-PLAN.md` §8 冲突处，以后者为准（下文 §7 已同步更正）。

> **⚠️ 2026-10-14 更正（历史）**：§7 的"无剩余必做项"结论经独立逐模块复核**不成立**。
> 本文件把"路由已挂载 / 接口已返回"等同于"功能已复刻"，但上游同一路由的
> 请求/响应形状、状态码、引擎行为仍有大量差异，且若干整块子系统从未接线
> （webhook 投递、worker HTTP、内嵌 worker、准入/499、LLM trace、MCP 工具集、
> 文档解析器、扩展框架、对象存储后备、provider）。**当前权威清单与实施计划见
> `docs/PARITY-PLAN.md`**；本文状态栏凡与之一致性存疑者，以 `PARITY-PLAN.md`
> 与代码为准。


> **2026-10-16/17 轮次指引**：一轮以**每条路由的后端逻辑**为对象的复核已完成（100 条装饰器路由 +
> 4 个非装饰器面全部挂载）。该轮修掉了全部系统性差异（空值策略——上游其实**保留** null、
> `ExcludeNoneRoute` 从未生效；时间戳统一 `isoformat()`；未识别错误一律 500；JSON 不再 HTML 转义；
> 校验信封文案；请求体校验词表与请求层文本规则）与各路由族的主要 high/med/low 项；逐条残留见
> `docs/CONTRACT-AUDIT.md` §5。两个先于该轮即失败的 PG 用例（`TestDirectReadRendersArchive`、
> `TestVectorSimilarityRanksByDistance`）在 pristine HEAD 上同样失败，属测试自身在连接池上
> `SET search_path` 的问题。
>
> 本文档是**实现清单**，供后续净室重写 AI 逐项实现。
> 只覆盖**服务后端**；前端 / Control Plane（9999）不在范围内。
>
> 参照基线：上游服务端 `hindsight-api-slim/hindsight_api/`，
> 公开 HTTP 路由 **99 条 + MCP 命名空间**。
> Wenyuan 当前实际挂载 **99 条路由**（含 3 条 retired 返回 410）。
>
> **本次核对日期：2026-10-13（第二次核对）**。上一版写于更早基线，
> 声称"仅挂载 22 条路由"，与代码严重出入。本节所有"现状"均已对
> `wenyuan` 当前代码逐条重核，核对方式记录在每项的证据行里。
> `docs/ROADMAP.md` 的部分描述写于更早基线（其 §1.1–§1.6 的"缺失"
> 论断已被下方 §1 取代），与代码有出入处以本文为准。

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

**路由面已完整。** 逐条比对上游 `@app.{get,post,put,delete,patch}` 装饰器
与 `internal/app/route_coverage_test.go` 的实际挂载表：

| | 数量 |
|---|---|
| 上游公开路由 | 99 |
| Wenyuan 已挂载路由（含 3 条 retired） | 99 |
| 缺失 | 0 |
| 多余 | 0 |

核对方式：上游 `@app.{get,post,put,delete,patch}` 装饰器共 **99** 条。
Wenyuan 侧 = `internal/app/route_coverage_test.go` 探测表中的
`{http.Method...}` 条目（**96** 条）+ `internal/bankapi/retired_routes.go`
的 `RetiredRoutes`（**3** 条）= **99** 条。该测试会实际发起请求，
任何一条路由未挂载都会以 404 失败。
（注意：路由必须用多行正则统计，`grep -P` 的跨行模式匹配不到，见 §10。）

路由族一览（均已完成）：bank 生命周期与别名、config（读/写/重置）、
memories（retain/recall/list/成员/history/dry-run-extract）、files/retain、
operations（列表/成员/retry/cancel）、transfer（import/export/
document-transfer/transfer/clone/attachments）、documents（含 chunks 与
reprocess）、tags、graph、entities（含 graph 与 regenerate）、chunks、
portal（prompts/preview、bank-template-schema、version）、mental-models
（9 条）、directives（5 条）、knowledge-base（8 条）、observability
（stats/timeseries/health-llm/audit-logs/llm-requests）、observations 与
consolidation（6 条）、webhooks（6 条）、`/mcp`、`/ext`。

**主动 retired（🚫 不要重写，保持 410）：**

```
GET  /v1/default/banks/{bank_id}/profile
PUT  /v1/default/banks/{bank_id}/profile
POST /v1/default/banks/{bank_id}/background
```

证据：`internal/bankapi/retired_routes.go:34-38`。

**并存约束（AGENTS.md，重写时必须遵守）：**

- 公开面冻结：路由、JSON 字段名、状态码、响应头、PG 表名 / 列名 / 枚举值必须逐字保留。
- 标准库优先：`net/http`、`encoding/json`、`database/sql`。新增依赖须 MIT / Apache-2.0 / BSD 且**默认构建仍可离线**（当前唯一直接依赖：`github.com/tiktoken-go/tokenizer`，见 A1）。
- 无 LLM 依赖的默认构建须可离线编译；PG 驱动仅在 `-tags pgx` 下编译（`internal/storage/driver_pgx.go`）。**`make build` 与 `make pgx` 必须都过。**
- 存储接口任何新方法须在 **memory 与 postgres 两个后端**同时实现（`internal/storage/store.go`）。
- 不得在 `internal/bankapi` / `internal/memoryops` 中写 SQL；只能经 `internal/storage`。边界由 `internal/storage/boundary_test.go` 强制。
- 归属表固定为：`banks, bank_aliases, documents, chunks, entities, memory_units, unit_entities, memory_links, entity_cooccurrences, async_operations`。
- 迁移**两处副本**须同步：`migrations/` 与 `internal/storage/embedded/`；编号必须从 1 连续到 N（当前为 `0001_extensions` … `0005_reference_functions`）。
  **整套迁移由 `scripts/reference_schema_migrations.py` 从上游数据库目录直接生成**（见 `docs/SCHEMA-ALIGNMENT.md`），不要手写、不要手改：schema 的唯一事实是上游库，改 schema 就等于改上游库再重新生成。
- 单文件 ≤500 行，测试文件 ≤800 行。

**验证门槛（`make check`）**：build + test + vet + pgx build + lint。
PG 套件另跑 `make test-pg`（需 `WENYUAN_TEST_DATABASE_URL`）。
覆盖率门槛：离线 `COVERAGE_MIN=56`、PG `COVERAGE_MIN_PG=66`。

---

## 2. A 类：核心引擎能力（原为最高优先级，大部分已闭合）

### A1. 真实向量语义检索 — ✅ 已实现

**现状**：`embedding vector(384)` 列已由迁移 `0008_memory_unit_embedding.sql`
引入；`internal/storage/postgres_recall_vector.go` 走 `<=>` 距离查询，
`memory_recall_arms.go` 的 semantic 臂在**内存后端**仍是确定性打分。

- 证据：`migrations/0008_memory_unit_embedding.sql`；`internal/storage/postgres_recall_vector.go`。
- 遗留（⚠️）：内存后端无向量，仍是词重叠打分（这是**测试后端的预期设计**，
  不是缺陷）；cross-encoder reranker 缺失，见 A4。
- **新变化**：分块与 token 计数已用真实 BPE tokenizer
  （`github.com/tiktoken-go/tokenizer v0.7.0`，MIT，纯 Go，内嵌 cl100k_base）。
  分块本身按**字符**预算（`ChunkSize = 3000`，对齐上游
  `DEFAULT_RETAIN_CHUNK_SIZE`，注意上游该值单位是字符不是 token）。

---

### A2. LLM Provider 层 — ✅ 已实现（边界已就位）

**现状**：`internal/provider/` 定义 `Provider` 接口（`Complete` / `Embed` /
`VerifyConnection` / `Dimensions`）+ 注册表 + 选择逻辑；已实现
`openai`（OpenAI 兼容端点）与 `none`（无模型）两个适配器。

- 证据：`internal/provider/provider.go`、`registry.go`、`selection.go`、
  `internal/provider/openai/`、`internal/provider/none/`。
- **provider 现已真正到达 retain 与 reflect**（本次接线修复）：
  `internal/app/app.go:188,191` 注入 `memops.Extraction` 与 `memops.Reflection`。
- 遗留（⚠️）：适配器数量远少于上游（上游 23 个）；OAuth 类鉴权未做。

---

### A3. retain 事实与实体抽取 — ✅ 已实现并接线

**现状**：`internal/extraction/extractor.go` 实现模型驱动的**事实抽取**
（结构化输出 schema + 确定性回退）；`internal/app/extraction.go` 把
`extraction.Result` 适配到 `storage.FactExtraction`；有模型时
**每个抽取事实存一条 unit**（带 entities / occurred 日期 / confidence），
并在同 item 的事实间建 `LinkSemantic` 链接。

- 证据：`internal/extraction/extractor.go`、`internal/app/extraction.go`、
  `internal/storage/retain_contents.go`、`retain.go`（`FactExtraction` 边界）。
- 无模型时保持**逐字路径**不变（离线默认部署行为不变）。
- 遗留（⚠️）：causal 链接（`LinkCauses` / `LinkCausedBy`）不从抽取结果生成；
  折叠 / token 预算（上游 `fold.py`、`memory_budget.py`）未实现。

---

### A4. reflect agentic loop — ✅ 已实现并接线

**现状**：`internal/reflect/loop.go` 是完整的模型驱动工具调用循环
（≤10 轮，工具 `search_mental_models → search_observations → recall →
read_mental_models → expand → done`）；`internal/reflect/runner.go` 实现
`storage.ReflectRunner` 并把 `storage.ReflectionRetrieval` 适配到
`reflect.Retrieval`；`internal/app/app.go:191` 注入。**无模型时不再回落到
确定性 synthesis**：参考实现在 provider 为 `none` 时直接以 400 拒绝 reflect
（detail 与参考一致），`internal/memoryops/reflect.go` 与
`internal/mentalmodels/handlers.go` 现在按同一信号拒绝 reflect 与
mental-model refresh；确定性循环仅保留为 store 层的回退（不经过被武装的
路由时不触发）。

- 证据：`internal/reflect/loop.go`、`internal/reflect/runner.go`、
  `internal/storage/reflect_runner.go`（边界声明）、
  `internal/app/reflect_test.go`（端到端用例）。
- 遗留（❌）：**cross-encoder reranker 缺失**——recall 的 `reranker` 分仍是
  `bestArmScore`（`internal/storage/memory_recall.go`），非神经相关性；
  融合硬编码 RRF，无上游的 `interleave_fusion`；
  boost 用自造常数而非上游的 recency 衰减曲线（halflife 90d）+
  proof-count 对数归一。
- 遗留（❌）：**无查询文本时间解析**——上游从 query 自然语言抽时间约束
  （`engine/query_analyzer.py`），Wenyuan 只读显式 `temporal_window`。

---

### A5. 后台 Worker 进程 — ✅ 已实现

**现状**：`cmd/worker` 存在；`internal/worker/` 实现 claim → process →
complete/retry 循环；`internal/app/worker.go` 的 `archiveProcessor`
按 `OperationType` 分派（export_bank / import_bank /
export_documents / import_documents / clone_bank / consolidation）。

- 证据：`cmd/worker/`、`internal/worker/`、`internal/app/worker.go:30-45`。
- Docker 镜像含 server / worker / bankctl 三个入口（见 D4）。

---

### A6. `files/retain` 附件持久化 — ✅ 已实现

**现状**：迁移 `0017_attachments.sql` 建表；`internal/storage/
memory_attachments.go` / `postgres_attachments.go` / `stored_files.go`
实现持久化；写路径把附件与 unit 关联。

- 证据：`migrations/0017_attachments.sql`、`internal/storage/memory_attachments.go`。
- 遗留（⚠️）：仅实现内联（inline）附件，**无外部对象存储后端**（上游有
  S3 / GCS / Azure 适配器）。

---

### A7. Consolidation 与 Mental Model 刷新 — ✅ 已实现

**现状**：`internal/consolidation/consolidator.go` 实现巩固引擎
（provider 驱动），由 queue consumer 调用
（`internal/app/worker.go` 的 `OperationTypeConsolidate` 分支）。

- 证据：`internal/consolidation/consolidator.go`、`prompt.go`、
  `internal/app/worker.go`（`applyConsolidation`）。

---

## 3. B 类：HTTP 路由 — ✅ 全部已实现

> 以下所有路径前缀均为 `/v1/default/banks/{bank_id}`，除另行标注。
> 本节覆盖的路由族已全部挂载，由 `internal/app/route_coverage_test.go`
> 逐条探测（会真实发起请求，未挂载即 404 失败）。各族的处理包见括注。

### B1. Mental Models — ✅ 9 条

`GET/POST /mental-models`、`GET/PATCH/DELETE /mental-models/{id}`、
`GET /mental-models/{id}/history`、`POST /mental-models/{id}/refresh`、
`POST /mental-models/{id}/dry-run-refresh`、`POST /mental-models/{id}/clear`。

- 包：`internal/mentalmodels/`（`handlers.go`、`models.go`）
- 参照：`engine/mental_model_refresh.py`

### B2. Knowledge Base — ✅ 8 条

`GET /knowledge-base/tree`、`POST /knowledge-base/folders`、
`POST /knowledge-base/pages`、`GET /knowledge-base/export`、
`GET /knowledge-base/search`、`GET /knowledge-base/pages/{page_id}`、
`PATCH/DELETE /knowledge-base/nodes/{node_id}`。

- 包：`internal/reporting/knowledge.go`；存储 `internal/storage/memory_knowledge.go` / `postgres_knowledge.go`。
  存储落在上游表 `knowledge_pages`：一个 page 由一条 `mental_models` 行承载正文与 tags（`mental_model_id` 指向它），folder 只是容器。

### B3. Directives — ✅ 5 条

`GET/POST /directives`、`GET/PATCH/DELETE /directives/{directive_id}`。

- 包：`internal/directives/`

### B4. 导出 / 导入 / 克隆 / 附件下载 — ✅ 9 条

`POST /import`、`GET /export`、`POST /document-transfer`、
`POST /document-transfer/export`、`POST /transfer/export`、
`POST /transfer/import`、`POST /clone`、`GET /attachments/{attachment_id}`、
`GET /v1/default/files/download/{key:path}`（**非 bank 前缀**）。

- 包：`internal/transfer/`（路由表见 `routes.go`）
- 底层：归档解析 `internal/transferarchive/`、映射与导入 `internal/migration/archiveimport/`、CLI `internal/bankadmin/migrate.go`
- 注意：`GET /document-transfer` 是**主动 retired**（返回 410），不是遗漏。

### B5. Documents / Chunks / Tags / Entities / Graph — ✅ 13 条

`GET /documents`、`GET/PATCH/DELETE /documents/{id}`、
`GET /documents/{id}/chunks`、`POST /documents/{id}/reprocess`、
`GET /tags`、`GET /v1/default/chunks/{chunk_id:path}`（**非 bank 前缀**）、
`GET /graph`、`GET /entities`、`GET /entities/graph`、
`GET /entities/{entity_id}`、`POST /entities/{entity_id}/regenerate`。

- 包：`internal/readsurface/`

### B6. Memories 子路由 — ✅ 7 条

`GET /memories/list`、`GET/PATCH /memories/{memory_id}`、
`GET /memories/{memory_id}/history`、
`DELETE /memories/{memory_id}/observations`、
`POST /memories/dry-run-extract`、`POST /prompts/preview`、
`DELETE /memories`（批量删除）。

- 包：`internal/reporting/memories.go`、`internal/reporting/handlers.go`

### B7. 审计 / 可观测性 — ✅ 7 条

`GET /stats`、`GET /stats/memories-timeseries`、`POST /health/llm`、
`GET /audit-logs`、`GET /audit-logs/stats`、`GET /llm-requests`、
`GET /llm-requests/stats`。

- 包：`internal/reporting/`；迁移 `0010_audit_log_and_llm_requests.sql`
- 另外：`GET /version`、`GET /v1/version`、`GET /v1/bank-template-schema`、
  `GET /health`、`/health/live`、`/health/ready`、`GET /metrics` 均已挂载。

### B8. Webhooks — ✅ 6 条

`POST/GET /webhooks`、`GET/PATCH/DELETE /webhooks/{webhook_id}`、
`GET /webhooks/{webhook_id}/deliveries`。

- 包：`internal/webhooks/`（含 SSRF 防护，见 `url_guard` 等价实现）；迁移 `0012_webhooks.sql`

### B9. Observations / Consolidation — ✅ 6 条

`DELETE /observations`、`GET /observations/scopes`、
`POST /consolidation-strategies/preview`、`POST /consolidation/recover`、
`POST /consolidate`、`DELETE /memories/{memory_id}/observations`。

- 包：`internal/reporting/handlers.go`；引擎 `internal/consolidation/`
- 巩固由 worker 消费 `OperationTypeConsolidate` 执行（`internal/app/worker.go`）

### B10. 其他契约项 — ✅

| 项 | 状态 |
|---|---|
| `POST /ext/*` 扩展命名空间 | ✅ 已挂载（`internal/httpx/extension.go`） |
| `/mcp` 命名空间 | ✅ 已挂载并**真实服务 JSON-RPC**（`internal/mcp/`，`app.go:97` 注入），非 422 |
| `GET /v1/bank-template-schema` | ✅ |
| `GET /v1/version` | ✅（`GET /version` 亦在） |
| `DELETE /config` | ✅ 已实现（config 重置路由） |
| `POST /operations/{id}/delete` | ✅ 已实现（兼容别名） |

---

## 4. C 类：存储层 / 数据契约缺口

| # | 缺口 | 现状证据 | 参照 |
|---|---|---|---|
| C1 | `documents.metadata` / `retain_params` 从未写入 | **已修复**：`PutDocument` 的 INSERT 现含 `metadata` / `retain_params` / `tags` 三列（`postgres_retain.go` 的 `PutDocument`） | `PutDocument` 全量字段 |
| C2 | 迁移丢失字段 | **已修复**：`unmappedFields()`（`migration/archiveimport/map.go`）现返回 `nil`——每个归档字段都有目标列；迁移 0016 补齐了缺失列 | 需先加列 |
| C3 | 索引 / 约束不足 | **已修复**：迁移 0013 建核心索引、0019 建按类型的部分索引、0020 建 trigram GIN（`entities name_trgm`）；`migrations/` 现共 **49** 个 `CREATE INDEX`（含 vector HNSW、trigram GIN、jsonb GIN） | `alembic/` |
| C4 | 无法直连上游库 | **已实现**：`storage.NewDirectSource` 直连参照形态库（`fact_type` / `canonical_name` / `bank_id` 命名差异在读取器内翻译），渲染成归档再走标准导入；CLI：`bankctl bank migrate --source-url URL --source-bank ID` |
| C5 | 无版本表 | **已实现**：`schema_migrations` 表记录已应用迁移，启动时只跑未应用的 |
| C6 | 跨进程缓存失效 | **已实现**：`bank_stats_cache` 表（迁移 0018）让一个 worker 的统计摘要被其它 worker 读取；`BankStatsCacheTTL` 控制有效期。bank 行缓存仍是进程内 TTL |
| C7 | 实体 ID 派生 | **已解决**：实体按归一化名称解析；`resolve=true` 现走 Jaro-Winkler 模糊匹配（阈值 0.84），PG 后端先用 `pg_trgm`（阈值 0.3）筛候选再打分（`internal/storage/entity_match.go`、`postgres_retain.go`） |
| C8 | 无增量同步 | **已实现**：`archiveimport.Incremental` 模式只写目标库未持有的文档，可重复运行而不重复也不破坏；CLI：`--incremental`。报告含 `written` / `skipped` |

---

## 5. D 类：工程与运维缺口

| # | 缺口 | 现状 |
|---|---|---|
| D1 | 无 PG 端到端测试 | **已实现**：CI `postgres` job 用 pgvector 服务跑 `make test-pg`；本地 `make test-pg`（需 `WENYUAN_TEST_DATABASE_URL`） |
| D2 | 无真实 e2e | **已实现**：`internal/app/e2e_lifecycle_test.go` 走完整生命周期（retain→recall→list→reflect→stats）；另有 parity 契约用例，含本次新增的抽取与 reflect 端到端用例 |
| D3 | 无基准测试 | **已实现**：`internal/storage/bench_test.go` 5 个基准，`make bench` |
| D4 | 无容器化 / 部署形态 | **已实现**：多阶段 `Dockerfile`（server+worker+bankctl，distroless）与 `docker-compose.yml`（pgvector + api + worker） |
| D5 | 无 Release / Tag | **已实现**：`CHANGELOG.md` `[0.1.0]` 条目，打 `v0.1.0` 标签 |
| D6 | `entities.metadata` 幽灵列 | **已修复**：迁移 0014 添加该列 |
| D7 | `async_operations.error` 死列 | **已修复**：迁移 0015 删除该列 |
| D8 | 无覆盖率门槛 | **已实现**：`make cover-check`（离线 `COVERAGE_MIN=56`、PG `COVERAGE_MIN_PG=66`），已接入 CI；2026-10-13 实测离线 **58.8%** |

---

## 6. 契约项（14 条）— 已逐条确认

以下 14 条现已逐条对齐参照实现或钉死为明确的公开契约。字段名均为公开面。

| # | 契约项 | 结论 |
|---|---|---|
| 1 | 公开路由 → operation 身份映射 | 按路由 path+method 映射到 `operation_type`；operation 身份不外露（与参照一致） |
| 2 | 废弃请求字段别名 | 永久接受：`background`、`reflect_mission`（retain 顶层）、reflect 的 `context`、recall 的顶层 `trace`；已在各请求模型的 `UnmarshalJSON` / 字段集中体现 |
| 3 | 非校验类错误体形状 | 统一 `{"detail": ...}` 信封，覆盖 not-found / conflict / gone / unavailable / timeout（`internal/httpx/errors.go`） |
| 4 | disposition 特质默认值 | 中性值 3（`storage.DispositionTraitNeutral`） |
| 5 | recall trace 对象 | **已实现**：`trace=true` 返回 `{query: {query_text, budget, max_tokens, tags}, summary: {candidates_found, budget_used, budget_remaining, total_duration_ms, results_returned, phase_metrics, stage_counts}}`，由 `RecallTrace` 在检索管线中记录 |
| 6 | 每 bank 配置覆盖键全集 | disposition 三特质 + `reflect_mission`（`bankOverrides`）；读/PATCH/DELETE 三条路由一致 |
| 7 | 跨进程缓存失效保证 | bank 行为进程内 TTL（5s）+ 同进程写立即失效；统计摘要走 `bank_stats_cache` 表（跨进程，30s TTL） |
| 8 | 非校验类写错误体形状 | blocked-write 带 `violations` 列表，其余为 `{"detail": ...}` |
| 9 | 部署依赖的索引 / 约束集 | **已实现**：迁移 0013 核心索引 + 0019 按类型的部分索引 + 0020 实体名 trigram GIN；`migrations/` 共 49 个 `CREATE INDEX` |
| 10 | 写路径公开配置名 | `enable_bank_config_api` 门控写路径；读路径永远可用 |
| 11 | 文档 ID 自动生成形式 | 未提供 `document_id` 时生成不透明 ID；同一次写入内按 item 分组（`itemFromUpload` 用文件名） |
| 12 | 已处理内容 token 数 | **已实现**：`processed_content_tokens` 作为公开响应字段，现为**真实 BPE token 计数**（`github.com/tiktoken-go/tokenizer`，cl100k_base）；路径未统计时省略而非填 0 |
| 13 | reflect 每 bank perspectives/directives 读契约 | **已实现**：`based_on` 引用对象暴露 `mental_models` 与 `directives`（`internal/memoryops/reflect_response.go`） |
| 14 | 未知 body 字段上报 `X-Ignored-Params` | **已实现**：`httpx.RecordIgnoredBodyFields` + `UnknownBodyFields`，与查询参数合并上报；retain 路由已接入 |

---

## 7. 剩余工作（按优先级）

> 原"建议重写顺序"所列的 A1–A7、B1–B10、C1–C8、D1–D8 **均已完成**
> （见 §2–§5 的状态标记）。本节在 2026-10-13 复核对齐上游后列出的 8 项，
> **已于 2026-10-14 全部处理完毕**，逐项结论如下。

1. ~~**cross-encoder reranker**~~ — **已完成**。`provider.Reranker` 为可选能力接口，
   OpenAI 兼容适配器实现 `/rerank`；`storage.RecallQuery.Rerank` 在融合后重打分，
   按 batch 归一化，失败回落 `bestArmScore`。见 `internal/provider/openai/rerank.go`、
   `internal/storage/memory_recall.go`。
2. ~~**融合与 boost 代数对齐**~~ — **已完成，并已补齐 `interleave_fusion`**。boost 为 rank-space
   提升（`1/(k + rank/divisor)`，divisor 2/4/8）加衰减式 rank 后置加项；recency 改为可配衰减曲线
   （`WENYUAN_API_RECENCY_DECAY_FUNCTION`，linear/exponential/none）并对**粗粒度日期**从周期
   末端起算且封顶中性；证据强度为 proof-count 对数归一。`interleave_fusion` **已落地并有真实调用点**：
   `storage.RecallQuery.Fusion` + 按源预裁 `PerSourceCap`，由巩固的去重候选扩池
   （`Store.RelatedObservations`）实际使用，另补 rerank 前裁剪 `RerankDepth`。
3. ~~**查询文本时间解析**~~ — **已完成**。`internal/memoryops/query_time.go` 解析相对词、
   星期/月份名、`N units ago`、`last|next|this <unit>`、`in the last N units`；
   显式 `temporal_window` 优先，`query_timestamp` 作参考时刻。
4. ~~**字段级契约修正**~~ — **已完成**。`observation_scopes` 接受模式字面量与
   tag 组列表两种形式（按原始 JSON 存 jsonb，round-trip 无损）；`timestamp` 接受
   大小写不敏感的 `"unset"` 与空串（等同显式 null）；`operation_id` 校验 UUID。
5. ~~**更多 provider 适配器**~~ — **已完成（可移植部分）**。13 个 OpenAI 兼容厂商
   （groq/ollama/ollama-cloud/lmstudio/minimax/deepseek/openrouter/requesty/zai/
   opencode-go/atlas/meta/fireworks）按表注册到同一适配器；Anthropic 因协议不同
   单独实现（`internal/provider/anthropic/`）。**未移植**：OAuth CLI 类
   （claude-code、codex、cursor、github-copilot、xai-oauth）——需交互式登录，
   本服务不承载；Gemini 原生协议、LiteLLM router、bedrock、llamacpp 亦未单独实现。
   此后已补 `volcano` 厂商与确定性 `mock` provider；且**不再静默**：
   `WENYUAN_API_LLM_STRATEGY` / `..._LLM_LITELLMROUTER_CONFIG` / `..._EMBEDDINGS_LOCAL_MODEL`
   一旦配置即**启动失败并点名该键**。
6. ~~**causal 链接生成**~~ — **已完成**。抽取提示与 schema 增加 `causal_relations`
   （每 fact 至多 2 条、必须指向更早的 fact）；写入后解析为 `caused_by`
   边（effect → cause），越界索引丢弃。
7. ~~**retain 折叠与 token 预算**~~ — **已实现（后补）**。`FoldRetainItems` 折叠字段一致的小项
   （每项 ≤2 KiB）并施加 32 KiB 请求预算；超预算项按 rune 截断并在 metadata 记录
   （无损切分需存储协议变更，仍为范围外）。
8. ~~**外部对象存储后端**~~ — **已完成（S3 兼容）**。`internal/objectstore/` 实现
   S3 REST 子集并自签 SigV4（无厂商 SDK），经 `storage.ObjectStore` 边界接入；
   环境键沿用上游（`WENYUAN_API_FILE_STORAGE_TYPE` 等）。**未实现**：
   GCS / Azure——上游三者共用同一个 obstore 包装器，仅客户端配置不同；如需，
   可依同一 `ObjectStore` 边界补实现。

> **2026-10-15 补充：契约面逐项复核。** 以**上游自己的 OpenAPI 文档**（99 操作 / 195 schema）为基准，
> 对全部路由做了 11 个切片的逐操作比对：路由面 99/99 完整，但字段/状态码/头/默认值层面存在大量偏差。
> 该轮发现的每一项、修复状态与仍余项都记在 **`docs/CONTRACT-AUDIT.md`**，可用
> `bash scripts/contract_smoke.sh <server-binary>` 复现其中 35 项线上事实。
> 本文档继续作为功能面总览；契约面的权威清单以 `docs/CONTRACT-AUDIT.md` 为准。

> 结论：本文件所列项目均已闭合；`docs/PARITY-PLAN.md` §8 的阶段记录是当前权威状态
> （含每项的验收命令与真实运行证据）。仍未覆盖且**明确标注为范围外**的是：上游交互式认证类
> provider、GCS/Azure 对象存储与预签名下载 URL、上游的动态插件加载器、
> reranker failover 链与本地 MLX、dateparser 依赖与中文数字、以及上游无任何消费者的
> `reflect/observations.py`（`trend`）。文档解析链（markitdown 等价 + llama_parse/iris）已实现，见 `docs/PARITY-PLAN.md` §8 的 C7 记录。

### 7.1 后台维护子系统（2026-10-14 补齐）

这是 2026-10-13 二次审计确认的**相对上游最大功能缺口**（上游 `engine/maintenance.py`，
654 行）。现已实现，见 `internal/maintenance/`、`internal/app/maintenance_jobs.go`：

- **retention sweep** — 按保留窗口分批清理 `audit_log` 与 `llm_requests`，
  批 2000 行、每表每 schema 上限 1000 批；`FOR UPDATE SKIP LOCKED` 让并发
  sweeper 取不相交的块。
- **consolidation reconcile** — 重新调度"已写入但没有 consolidation 排队"的
  bank，恢复被终态失败搁浅的事实。
- **scheduled mental model refresh** — 按 `trigger.refresh_cron` 判定到期
  （`robfig/cron` 只用于求上一次触发点），再过 staleness 判定，避免一次
  tick 为空转烧掉模型调用。
- **operation cleanup** — 修剪过期终态操作，仅限 terminal 状态。
- **循环骨架** — 单一 tick 上按各自节奏跑 due 的 job，启动抖动避开舰队同时打点；
  **无 leader election，每个进程都跑**，安全性来自各 job 的*工作*本身幂等
  （SKIP LOCKED + 入队去重），而非选出单一进程。

配套补齐：`internal/tenant/`（schema 发现接口 + 单租户默认实现）、
迁移里的四个跨 schema 发现例程（现为 `0005_reference_functions.sql`，正文照抄上游）、
迁移 0022（`serialization_key`）、队列的按主题入队去重与串行化键。

**refresh 语义已对齐上游**：由同步重跑 recall 改为入队 `refresh_mental_model`
异步操作，经 reflection 循环重新生成模型内容。这是**公开行为变更**——
接口现在返回 `queued` 与 `operation_id`，调用方需轮询操作而非假定返回即完成。

**已补齐（后）**：`interleave_fusion` 现由巩固的去重候选扩池使用；
维护循环另有 `graph.maintenance`（图 relink + 实体修剪）与 `vector_index.reconcile`
（按 (bank,fact_type) 的向量索引健康）两个作业。

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

## 9. 非目标（按设计不做）

- Control Plane UI（端口 9999）；本项目为**纯后端服务**，全库无任何前端资源。
- pip / npm 安装器形态；分发形态为容器（Dockerfile + docker-compose）与 `go build`。

> 注：早期"非目标"列表中的"首页切片之外的 API 端点"**已不再是非目标**——
> 99 条路由已全部实现（见 §1）。

---

## 10. 本文档的核对方法（供下次复核）

每一项的"现状"都应当能按其证据行当场复现。本次核对用到的命令：

```sh
# 路由数量：96 inline + 3 retired = 99，与上游一致
grep -c '{http.Method' internal/app/route_coverage_test.go          # 96
grep -c '{Method:' internal/bankapi/retired_routes.go               # 3

# 上游装饰器数量（必须用多行正则，grep -P 的跨行模式匹配不到）
python3 -c "
import re
s=open('../hindsight/hindsight-api-slim/hindsight_api/api/http.py').read()
print(len(set((m.group(1).upper(), m.group(2))
  for m in re.finditer(r'@app\.(get|post|put|delete|patch)\(\s*\n?\s*\"([^\"]+)\"', s, re.M))))"   # 99

# 迁移数量与最新编号（从上游目录生成的系列）
ls migrations/ | wc -l && ls migrations/ | tail -1               # 5, 0005_reference_functions.sql

# 索引总数（应与上游一致）
grep -h "^CREATE INDEX\|^CREATE UNIQUE INDEX" migrations/*.sql | wc -l   # 75

# 与上游库逐对象比对（期望全部为 0）
psql -d <upstream> -v schema=public -f scripts/schema_diff.sql > /tmp/up.schema
psql -d <wenyuan>  -v schema=public -f scripts/schema_diff.sql > /tmp/wy.schema
python3 scripts/schema_inventory.py

# 迁移丢字段（函数体应为 return nil）
grep -n "func unmappedFields" -A 3 internal/migration/archiveimport/map.go

# provider 适配器
ls internal/provider/                                            # anthropic/ none/ openai/

# 引擎是否真的接线到服务路径（应各有一行）
grep -n "Extraction\|Reflection\|Reranker" internal/app/app.go

# 对象存储后端
ls internal/objectstore/                                         # s3.go
grep -n "FileStorage" internal/config/filestorage.go

# 维护子系统：循环 + 4 个 job + 配置 + 租户抽象
ls internal/maintenance/ internal/tenant/                        # loop.go / tenant.go
grep -n "^func maintenanceJobs" internal/app/maintenance_jobs.go
grep -n "EnvRetentionSweepIntervalSeconds" internal/config/maintenance.go

# 四个发现例程是否在迁移里（两份副本应各 4 处）
grep -c "CREATE OR REPLACE FUNCTION" migrations/0005_reference_functions.sql internal/storage/embedded/0005_reference_functions.sql

# 真实运行维护循环（需要 pgx 构建 + 本地库）
go build -tags pgx -o /tmp/worker ./cmd/worker
WENYUAN_API_DATABASE_URL="postgresql://postgres:postgres@127.0.0.1:5432/wenyuan_test" \
WENYUAN_API_DATABASE_SCHEMA=probe WENYUAN_API_AUDIT_LOG_RETENTION_DAYS=30 \
WENYUAN_API_RUN_MIGRATIONS_ON_STARTUP=true WENYUAN_API_MAINTENANCE_START_JITTER_SECONDS=0 \
  timeout 10 /tmp/worker        # 应打印 maintenance enabled / job finished 各行

# 门槛与实测覆盖率
grep -n "COVERAGE_MIN" Makefile                                  # 56 / 66
make cover-check
```

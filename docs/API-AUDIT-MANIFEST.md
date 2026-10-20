# API 逐条复刻审查清单（2026-10-20，第二轮：逐 API 子 Agent）

> 本轮目标：**把每个对外 API 拆成独立工作单元，每个单元一个子 Agent，逐条核对它的逻辑
> 复刻是否与参考实现 100% 一致；不一致的按参考实现改；全部改到一致后编译 + UT + Lint 全绿并提交。**
>
> 本文件是权威清单。每个子 Agent 只负责自己那一节，只在自己的写作用域里改代码，
> 只往 `docs/API-AUDIT-FINDINGS.md` 追加自己那一小节。
>
> 第一轮的按"横向面"切组的记录已归档在 `docs/API-AUDIT-FINDINGS.md` 的历史小节里
> （组 A–Q 与组长复核记录），本轮在其基础上按 API 逐条重审。

## 1. 唯一权威的契约面

**OpenAPI 文档里声明的 99 条 operation 就是全部 API。**

- 文档即契约：`internal/httpx/openapi.json` 逐字节等于参考 `http://127.0.0.1:8888/openapi.json`。
  参考有 100 个路由装饰器 = 99 条 OpenAPI operation + `GET /v1/version`（`include_in_schema=False`）。
- 参考源码入口：`../hindsight/hindsight-api-slim/hindsight_api/api/http.py`。
- 本服务路由表落点：`internal/*/{routes,handlers}.go` 的 `BindRoutes`，以及
  `internal/app/{app,transfer,reporting,read_surface,mental_models,webhooks}.go` 的挂载。
- 复核命令：
  ```sh
  python3 -c "import json;print(len([o for p in json.load(open('internal/httpx/openapi.json'))['paths'].values() for m,o in p.items() if m in ('get','post','put','patch','delete')]))"
  # → 99
  ```

**API 归属规则**：参考实现里以路由装饰器（`@app.{get,post,put,patch,delete}(...)`）声明的算
"API"；被 handler 直接调用、没有独立路由的引擎方法属于调用它的那个 API 的实现细节，不在本表里
单独成行（例如 `engine/memory_engine.py` 的 `delete_bank` 属于 `DELETE /v1/default/banks/{bank_id}`）。

## 2. 判"一致"的标准

按**线上差分**判定，不接受"看起来差不多"：

1. **请求侧契约**：路径参数、查询参数的声明集合与校验顺序；请求体的模型校验错误
   （`422` 的 `detail` 数组：`type`/`loc`/`msg` 三项、条目顺序、`input`）。
2. **响应侧契约**：状态码、`Content-Type`、响应头（`X-Ignored-Params`、`Allow`、
   `Retry-After`、`Content-Disposition` …）、响应体的**键集与键序**、值的类型与形状
   （`null` vs 缺省 vs `[]`）、载荷取值。
3. **副作用**：写库的行、字段与值（表名/列名/枚举值属冻结面），以及响应体里报告的
   （如 `deleted_count`、`total`、`success`）。副作用靠"同请求 → 再读"的往返证明。
4. **错误路径**：404 / 405 / 409 / 422 / 500 以及每一种的字节内容。

**最强证据是线上差分**：同一请求打两侧，逐字节比对。参考在
`http://127.0.0.1:8888`（key `sk-hs-f94653018553250b8ffeff305b86ee0a`），本服务用
`scripts/parity_dev_service.sh <端口>` 起在 `182xx` 端口。

## 3. 对照夹具（必读，避免把夹具差异当缺陷）

- 参考读的是 `127.0.0.1:5432/hindsight` 的 **`user_devin`** schema（46 个 bank）；本服务默认读
  `127.0.0.1:15432/wenyuan`，**不是同一个库**。带计数/聚合的响应（`stats`、`scopes`、
  `list` 的 `total`、列表长度）在那种配置下必然不同，**那是夹具差异，不是缺陷**。
- **可行且有说服力的做法**：在同一个 PostgreSQL 实例上建**自己的兄弟 schema**（把
  `HINDSIGHT_API_CONFIG_FILE` 指到一个 `database_schema: audit_<组>` 的临时 yaml），本服务会正常
  迁移它；把同一份夹具**逐行播到两侧**再比较两份夹具。用完 `DROP SCHEMA … CASCADE`。
  不要把本服务直接指到 `user_devin`：`internal/storage/postgres_adoption.go` 的
  `partialSchemaError` 会拒绝部分匹配的 schema。
- 多个子 Agent 共享这台机器：**每人一个端口**（18210、18211…），结束 `--stop`；**只读**探测用
  既有 bank，任何写操作只碰你自己建的 `audit-<组>-<随机>` bank 并在结束前删除。**绝不改既有 bank。**
- 已知夹具噪声（掩掉即可，不要据此改代码）：参考 worker 自己 consolidate（`consolidated_at`
  被填、可能多出新观测）、`edited_at`/`updated_at` 是各自请求时刻、
  `db_acquire_ms`/`uptime_seconds`/`seconds_since_last_poll` 是活测量、`/version` 的
  `features.worker`（`parity_dev_service.sh` 固定 `HINDSIGHT_API_WORKER_ENABLED=false`）。
- **不要在仓库内建 `git worktree`** 做隔离：`internal/storage/boundary_test.go` 会扫描全仓 `.go`，
  会让它误报。**严禁迁就性修改**：只在"库不同/进程状态不同"的条件下成立的差异不要改代码。

## 4. 工作单元（每节 = 一个子 Agent 的写作用域）

本服务代码分布在 300+ 个 Go 文件里，98 条 business API 的写作用域重叠严重（`internal/reporting/`
一个包就承载了 20+ 个 API）。因此工作单元按**写作用域不重叠**来切：一个单元里可能有多条 API，
但**每一条 API 都必须单独取证、单独给结论**——判定粒度和报告粒度是单 API，不是单单元。

| 单元 | 负责的 API（编号见 §5） | 写作用域 |
|---|---|---|
| `monitor` | 1–5 | `internal/monitoring/`、`internal/app/metrics*.go`、`internal/app/otel.go` |
| `bank-lifecycle` | 6–13 | `internal/bankapi/`（banks/aliases/decode/validation） |
| `bank-config` | 14–20 | `internal/bankapi/config*.go`、`internal/app/background.go`、`internal/banktemplate/` |
| `retain` | 21–25 | `internal/memoryops/`（retain/clear/file_retain）、`internal/extraction/`、`internal/fileparse/` |
| `recall-reflect` | 26–27 | `internal/memoryops/`（recall/reflect）、`internal/reflect/`、`internal/storage/*recall*` |
| `memory-crud` | 28–31 | `internal/reporting/memory*.go` |
| `observations` | 32–37 | `internal/reporting/`（observation/consolidation）、`internal/consolidation/` |
| `mental-models` | 38–46 | `internal/mentalmodels/`、`internal/app/mental_model*.go` |
| `knowledge` | 47–54 | `internal/reporting/knowledge*.go` |
| `directives` | 55–59 | `internal/directives/` |
| `read-surface` | 60–72 | `internal/readsurface/`、`internal/entitylabels/` |
| `operations` | 73–77 | `internal/memoryops/operations*.go`、`internal/worker/` |
| `transfer` | 78–87 | `internal/transfer/`、`internal/hindsightarchive/`、`internal/app/transfer.go`、`internal/app/worker_transfer.go` |
| `webhooks` | 88–92 | `internal/webhooks/`、`internal/app/webhooks.go` |
| `reporting-audit` | 93–99 | `internal/reporting/`（stats/audit/llm）、`internal/audit/`、`internal/app/llm_trace.go` |

**跨单元公共文件**（`internal/app/app.go`、`internal/storage/`、`internal/httpx/`、`internal/config/`）：
单元可以先**读**、可以出结论，但改这些文件的请求必须写进报告交给组长，由组长统一改（或者由组长
指定唯一 owner），避免并行时互相踩。唯一的例外：某单元发现**自己作用域之外**的缺陷时，必须在
报告里给出 `file:line`、参考证据和建议改法，不要自己动手。


## 5. API 编号表（99 条）

下表由 `internal/httpx/openapi.json` 的 99 条 operation 生成（operationId 与参考 `api/http.py` 的
`operation_id=` 逐条对应），"参考装饰器行"是参考源码里该路由 `@app.*(...)` 的行号，同事先核对这一行再往下读。

### 单元 `monitor`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 1 | `GET /health` | `health_endpoint_health_get` | `api/http.py:5650` | 见 §4 的写作用域 |
| 2 | `GET /health/ready` | `get_readiness` | `api/http.py:5667` | 见 §4 的写作用域 |
| 3 | `GET /health/live` | `get_liveness` | `api/http.py:5684` | 见 §4 的写作用域 |
| 4 | `GET /version` | `get_version` | `api/http.py:5703` | 见 §4 的写作用域 |
| 5 | `GET /metrics` | `metrics_endpoint_metrics_get` | `api/http.py:5748` | 见 §4 的写作用域 |

### 单元 `bank-lifecycle`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 6 | `GET /v1/default/banks` | `list_banks` | `api/http.py:6592` | 见 §4 的写作用域 |
| 7 | `GET /v1/default/banks/{bank_id}/aliases` | `list_bank_aliases` | `api/http.py:8476` | 见 §4 的写作用域 |
| 8 | `POST /v1/default/banks/{bank_id}/aliases` | `create_bank_alias` | `api/http.py:8499` | 见 §4 的写作用域 |
| 9 | `PATCH /v1/default/banks/{bank_id}/aliases/{alias}` | `set_bank_alias_primary` | `api/http.py:8530` | 见 §4 的写作用域 |
| 10 | `DELETE /v1/default/banks/{bank_id}/aliases/{alias}` | `delete_bank_alias` | `api/http.py:8565` | 见 §4 的写作用域 |
| 11 | `PUT /v1/default/banks/{bank_id}` | `create_or_update_bank` | `api/http.py:8593` | 见 §4 的写作用域 |
| 12 | `PATCH /v1/default/banks/{bank_id}` | `update_bank` | `api/http.py:8638` | 见 §4 的写作用域 |
| 13 | `DELETE /v1/default/banks/{bank_id}` | `delete_bank` | `api/http.py:8684` | 见 §4 的写作用域 |

### 单元 `bank-config`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 14 | `GET /v1/default/banks/{bank_id}/profile` | `get_bank_profile` | `api/http.py:8411` | 见 §4 的写作用域 |
| 15 | `PUT /v1/default/banks/{bank_id}/profile` | `update_bank_disposition` | `api/http.py:8424` | 见 §4 的写作用域 |
| 16 | `POST /v1/default/banks/{bank_id}/background` | `add_bank_background` | `api/http.py:8439` | 见 §4 的写作用域 |
| 17 | `GET /v1/bank-template-schema` | `get_bank_template_schema` | `api/http.py:9450` | 见 §4 的写作用域 |
| 18 | `GET /v1/default/banks/{bank_id}/config` | `get_bank_config` | `api/http.py:9612` | 见 §4 的写作用域 |
| 19 | `PATCH /v1/default/banks/{bank_id}/config` | `update_bank_config` | `api/http.py:9641` | 见 §4 的写作用域 |
| 20 | `DELETE /v1/default/banks/{bank_id}/config` | `reset_bank_config` | `api/http.py:9678` | 见 §4 的写作用域 |

### 单元 `retain`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 21 | `POST /v1/default/banks/{bank_id}/memories/dry-run-extract` | `dry_run_extract_memories` | `api/http.py:5922` | 见 §4 的写作用域 |
| 22 | `POST /v1/default/banks/{bank_id}/prompts/preview` | `preview_prompt` | `api/http.py:5981` | 见 §4 的写作用域 |
| 23 | `POST /v1/default/banks/{bank_id}/memories` | `retain_memories` | `api/http.py:10052` | 见 §4 的写作用域 |
| 24 | `POST /v1/default/banks/{bank_id}/files/retain` | `file_retain` | `api/http.py:10359` | 见 §4 的写作用域 |
| 25 | `DELETE /v1/default/banks/{bank_id}/memories` | `clear_bank_memories` | `api/http.py:10528` | 见 §4 的写作用域 |

### 单元 `recall-reflect`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 26 | `POST /v1/default/banks/{bank_id}/memories/recall` | `recall_memories` | `api/http.py:6165` | 见 §4 的写作用域 |
| 27 | `POST /v1/default/banks/{bank_id}/reflect` | `reflect` | `api/http.py:6410` | 见 §4 的写作用域 |

### 单元 `memory-crud`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 28 | `GET /v1/default/banks/{bank_id}/memories/list` | `list_memories` | `api/http.py:5815` | 见 §4 的写作用域 |
| 29 | `GET /v1/default/banks/{bank_id}/memories/{memory_id}` | `get_memory` | `api/http.py:6046` | 见 §4 的写作用域 |
| 30 | `PATCH /v1/default/banks/{bank_id}/memories/{memory_id}` | `update_memory` | `api/http.py:6078` | 见 §4 的写作用域 |
| 31 | `GET /v1/default/banks/{bank_id}/memories/{memory_id}/history` | `get_observation_history` | `api/http.py:6134` | 见 §4 的写作用域 |

### 单元 `observations`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 32 | `DELETE /v1/default/banks/{bank_id}/observations` | `clear_observations` | `api/http.py:9462` | 见 §4 的写作用域 |
| 33 | `GET /v1/default/banks/{bank_id}/observations/scopes` | `list_observation_scopes` | `api/http.py:9487` | 见 §4 的写作用域 |
| 34 | `POST /v1/default/banks/{bank_id}/consolidation-strategies/preview` | `preview_consolidation_strategies` | `api/http.py:9521` | 见 §4 的写作用域 |
| 35 | `POST /v1/default/banks/{bank_id}/consolidation/recover` | `recover_consolidation` | `api/http.py:9556` | 见 §4 的写作用域 |
| 36 | `DELETE /v1/default/banks/{bank_id}/memories/{memory_id}/observations` | `clear_memory_observations` | `api/http.py:9581` | 见 §4 的写作用域 |
| 37 | `POST /v1/default/banks/{bank_id}/consolidate` | `trigger_consolidation` | `api/http.py:9705` | 见 §4 的写作用域 |

### 单元 `mental-models`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 38 | `GET /v1/default/banks/{bank_id}/mental-models` | `list_mental_models` | `api/http.py:6920` | 见 §4 的写作用域 |
| 39 | `GET /v1/default/banks/{bank_id}/mental-models/{mental_model_id}` | `get_mental_model` | `api/http.py:6983` | 见 §4 的写作用域 |
| 40 | `GET /v1/default/banks/{bank_id}/mental-models/{mental_model_id}/history` | `get_mental_model_history` | `api/http.py:7021` | 见 §4 的写作用域 |
| 41 | `POST /v1/default/banks/{bank_id}/mental-models` | `create_mental_model` | `api/http.py:7052` | 见 §4 的写作用域 |
| 42 | `POST /v1/default/banks/{bank_id}/mental-models/{mental_model_id}/refresh` | `refresh_mental_model` | `api/http.py:7099` | 见 §4 的写作用域 |
| 43 | `POST /v1/default/banks/{bank_id}/mental-models/{mental_model_id}/dry-run-refresh` | `dry_run_refresh_mental_model` | `api/http.py:7133` | 见 §4 的写作用域 |
| 44 | `POST /v1/default/banks/{bank_id}/mental-models/{mental_model_id}/clear` | `clear_mental_model` | `api/http.py:7183` | 见 §4 的写作用域 |
| 45 | `PATCH /v1/default/banks/{bank_id}/mental-models/{mental_model_id}` | `update_mental_model` | `api/http.py:7219` | 见 §4 的写作用域 |
| 46 | `DELETE /v1/default/banks/{bank_id}/mental-models/{mental_model_id}` | `delete_mental_model` | `api/http.py:7262` | 见 §4 的写作用域 |

### 单元 `knowledge`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 47 | `GET /v1/default/banks/{bank_id}/knowledge-base/tree` | `get_knowledge_base_tree` | `api/http.py:7302` | 见 §4 的写作用域 |
| 48 | `POST /v1/default/banks/{bank_id}/knowledge-base/folders` | `create_knowledge_folder` | `api/http.py:7329` | 见 §4 的写作用域 |
| 49 | `POST /v1/default/banks/{bank_id}/knowledge-base/pages` | `create_knowledge_page` | `api/http.py:7361` | 见 §4 的写作用域 |
| 50 | `GET /v1/default/banks/{bank_id}/knowledge-base/export` | `export_knowledge_base` | `api/http.py:7414` | 见 §4 的写作用域 |
| 51 | `GET /v1/default/banks/{bank_id}/knowledge-base/search` | `search_knowledge_base` | `api/http.py:7457` | 见 §4 的写作用域 |
| 52 | `GET /v1/default/banks/{bank_id}/knowledge-base/pages/{page_id}` | `get_knowledge_page` | `api/http.py:7492` | 见 §4 的写作用域 |
| 53 | `PATCH /v1/default/banks/{bank_id}/knowledge-base/nodes/{node_id}` | `update_knowledge_node` | `api/http.py:7520` | 见 §4 的写作用域 |
| 54 | `DELETE /v1/default/banks/{bank_id}/knowledge-base/nodes/{node_id}` | `delete_knowledge_node` | `api/http.py:7589` | 见 §4 的写作用域 |

### 单元 `directives`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 55 | `GET /v1/default/banks/{bank_id}/directives` | `list_directives` | `api/http.py:7620` | 见 §4 的写作用域 |
| 56 | `GET /v1/default/banks/{bank_id}/directives/{directive_id}` | `get_directive` | `api/http.py:7669` | 见 §4 的写作用域 |
| 57 | `POST /v1/default/banks/{bank_id}/directives` | `create_directive` | `api/http.py:7699` | 见 §4 的写作用域 |
| 58 | `PATCH /v1/default/banks/{bank_id}/directives/{directive_id}` | `update_directive` | `api/http.py:7734` | 见 §4 的写作用域 |
| 59 | `DELETE /v1/default/banks/{bank_id}/directives/{directive_id}` | `delete_directive` | `api/http.py:7771` | 见 §4 的写作用域 |

### 单元 `read-surface`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 60 | `GET /v1/default/banks/{bank_id}/graph` | `get_graph` | `api/http.py:5774` | 见 §4 的写作用域 |
| 61 | `GET /v1/default/banks/{bank_id}/entities` | `list_entities` | `api/http.py:6748` | 见 §4 的写作用域 |
| 62 | `GET /v1/default/banks/{bank_id}/entities/graph` | `get_entity_graph` | `api/http.py:6796` | 见 §4 的写作用域 |
| 63 | `GET /v1/default/banks/{bank_id}/entities/{entity_id}` | `get_entity` | `api/http.py:6838` | 见 §4 的写作用域 |
| 64 | `POST /v1/default/banks/{bank_id}/entities/{entity_id}/regenerate` | `regenerate_entity_observations` | `api/http.py:6895` | 见 §4 的写作用域 |
| 65 | `GET /v1/default/banks/{bank_id}/documents` | `list_documents` | `api/http.py:7801` | 见 §4 的写作用域 |
| 66 | `GET /v1/default/banks/{bank_id}/documents/{document_id}/chunks` | `list_document_chunks` | `api/http.py:7873` | 见 §4 的写作用域 |
| 67 | `POST /v1/default/banks/{bank_id}/documents/{document_id}/reprocess` | `reprocess_document` | `api/http.py:7929` | 见 §4 的写作用域 |
| 68 | `GET /v1/default/banks/{bank_id}/documents/{document_id}` | `get_document` | `api/http.py:7972` | 见 §4 的写作用域 |
| 69 | `GET /v1/default/banks/{bank_id}/tags` | `list_tags` | `api/http.py:8009` | 见 §4 的写作用域 |
| 70 | `GET /v1/default/chunks/{chunk_id}` | `get_chunk` | `api/http.py:8076` | 见 §4 的写作用域 |
| 71 | `PATCH /v1/default/banks/{bank_id}/documents/{document_id}` | `update_document` | `api/http.py:8115` | 见 §4 的写作用域 |
| 72 | `DELETE /v1/default/banks/{bank_id}/documents/{document_id}` | `delete_document` | `api/http.py:8167` | 见 §4 的写作用域 |

### 单元 `operations`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 73 | `GET /v1/default/banks/{bank_id}/operations` | `list_operations` | `api/http.py:8210` | 见 §4 的写作用域 |
| 74 | `GET /v1/default/banks/{bank_id}/operations/{operation_id}` | `get_operation_status` | `api/http.py:8258` | 见 §4 的写作用域 |
| 75 | `DELETE /v1/default/banks/{bank_id}/operations/{operation_id}` | `cancel_operation` | `api/http.py:8296` | 见 §4 的写作用域 |
| 76 | `POST /v1/default/banks/{bank_id}/operations/{operation_id}/retry` | `retry_operation` | `api/http.py:8333` | 见 §4 的写作用域 |
| 77 | `DELETE /v1/default/banks/{bank_id}/operations/{operation_id}/delete` | `delete_operation` | `api/http.py:8363` | 见 §4 的写作用域 |

### 单元 `transfer`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 78 | `POST /v1/default/banks/{bank_id}/import` | `import_bank_template` | `api/http.py:8716` | 见 §4 的写作用域 |
| 79 | `GET /v1/default/banks/{bank_id}/export` | `export_bank_template` | `api/http.py:8803` | 见 §4 的写作用域 |
| 80 | `GET /v1/default/banks/{bank_id}/document-transfer` | `export_documents_sync_removed` | `api/http.py:8894` | 见 §4 的写作用域 |
| 81 | `POST /v1/default/banks/{bank_id}/document-transfer/export` | `export_documents` | `api/http.py:8924` | 见 §4 的写作用域 |
| 82 | `POST /v1/default/banks/{bank_id}/document-transfer` | `import_documents` | `api/http.py:8988` | 见 §4 的写作用域 |
| 83 | `POST /v1/default/banks/{bank_id}/transfer/export` | `export_bank_transfer` | `api/http.py:9062` | 见 §4 的写作用域 |
| 84 | `POST /v1/default/banks/{bank_id}/transfer/import` | `import_bank_transfer` | `api/http.py:9146` | 见 §4 的写作用域 |
| 85 | `POST /v1/default/banks/{bank_id}/clone` | `clone_bank` | `api/http.py:9252` | 见 §4 的写作用域 |
| 86 | `GET /v1/default/banks/{bank_id}/attachments/{attachment_id}` | `get_bank_attachment` | `api/http.py:9335` | 见 §4 的写作用域 |
| 87 | `GET /v1/default/files/download/{key}` | `download_file` | `api/http.py:9389` | 见 §4 的写作用域 |

### 单元 `webhooks`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 88 | `POST /v1/default/banks/{bank_id}/webhooks` | `create_webhook` | `api/http.py:9768` | 见 §4 的写作用域 |
| 89 | `GET /v1/default/banks/{bank_id}/webhooks` | `list_webhooks` | `api/http.py:9829` | 见 §4 的写作用域 |
| 90 | `DELETE /v1/default/banks/{bank_id}/webhooks/{webhook_id}` | `delete_webhook` | `api/http.py:9892` | 见 §4 的写作用域 |
| 91 | `PATCH /v1/default/banks/{bank_id}/webhooks/{webhook_id}` | `update_webhook` | `api/http.py:9923` | 见 §4 的写作用域 |
| 92 | `GET /v1/default/banks/{bank_id}/webhooks/{webhook_id}/deliveries` | `list_webhook_deliveries` | `api/http.py:10006` | 见 §4 的写作用域 |

### 单元 `reporting-audit`

| # | API | operationId | 参考装饰器行 | 本服务落点 |
|---|---|---|---|---|
| 93 | `GET /v1/default/banks/{bank_id}/stats` | `get_agent_stats` | `api/http.py:6620` | 见 §4 的写作用域 |
| 94 | `POST /v1/default/banks/{bank_id}/health/llm` | `test_bank_llm` | `api/http.py:6678` | 见 §4 的写作用域 |
| 95 | `GET /v1/default/banks/{bank_id}/stats/memories-timeseries` | `get_memories_timeseries` | `api/http.py:6713` | 见 §4 的写作用域 |
| 96 | `GET /v1/default/banks/{bank_id}/audit-logs` | `list_audit_logs` | `api/http.py:10574` | 见 §4 的写作用域 |
| 97 | `GET /v1/default/banks/{bank_id}/audit-logs/stats` | `audit_log_stats` | `api/http.py:10615` | 见 §4 的写作用域 |
| 98 | `GET /v1/default/banks/{bank_id}/llm-requests` | `list_llm_requests` | `api/http.py:10648` | 见 §4 的写作用域 |
| 99 | `GET /v1/default/banks/{bank_id}/llm-requests/stats` | `llm_request_stats` | `api/http.py:10706` | 见 §4 的写作用域 |

## 6. 子 Agent 输出约定


每个子 Agent 只往 `docs/API-AUDIT-FINDINGS.md` 的**自己的小节**追加（不改别人的小节），
小节标题固定为 `## 第二轮 <单元名>`：

```
## 第二轮 monitor

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET /health | ✅一致 | `api/http.py:5650` | `internal/monitoring/health.go:86` | 无 | `parity_check ...` → SAME |
```

结论只用三种：

- `✅一致`——有证据（参考 `file:line` + 本服务 `file:line` + 至少一个线上差分或往返验证）。
- `❌已修`——已改代码，且给出**证明修复的用例名**与差分结果。
- `⚠️存疑`——有证据但无法判定（参考行为依赖外部状态、需组长裁决、或属"已知不修"记录项）。

附加要求：

- **每处行为修改必须带一个"改回去就 FAIL"的用例**（仓库既有的表驱动风格），并说明证伪方式。
- 修完后在自己作用域内跑通：
  ```sh
  go test -count=1 ./internal/<包>/...
  go vet ./internal/<包>/...
  gofmt -l internal/<包>
  golangci-lint run ./internal/<包>/...
  ```
  **lint / build 缓存已由会话环境重定向**：`GOCACHE=/tmp/wc/gobuild`、
  `GOLANGCI_LINT_CACHE=/tmp/wc/lint`（默认的 `~/.cache/go-build` 与 `~/.cache/golangci-lint`
  在本机只读）。若你的 shell 里这两个变量为空，显式加上，否则 `golangci-lint` 会报
  `no go files to analyze`。
- 验收前再跑一次全仓 `go build ./...`、`CGO_ENABLED=0 go build -tags pgx ./...`（`pgx` 标签不可省）。
- 不提交 git（组长统一提交）；不改 `go.mod`；不新增依赖；不碰别人的写作用域。
- 报告末尾给出：本单元的结论计数（`✅ x / ❌ y / ⚠️ z`，按 API 计）与需要组长裁决的事项。
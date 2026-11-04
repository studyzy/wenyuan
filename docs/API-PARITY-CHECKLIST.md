# API 与后台任务复刻清单（第五轮：全量重审，2026-11-03）

> 本轮目标：**把对外 API 与后台定时任务拆成独立工作单元，逐条按 `../hindsight` 源码
> 重审逻辑复刻是否 100% 一致；不一致的按参考实现改；改到一致后标记完成。**
>
> 本文件是权威 checklist。勾选 `[x]` 表示该条已有证据结论并落在
> `docs/API-AUDIT-FINDINGS.md` 的 `## 第五轮 <单元名>` 小节。
>
> 这不是从零开始：仓库已有第四轮（[API-AUDIT-MANIFEST.md](API-AUDIT-MANIFEST.md) /
> [API-AUDIT-FINDINGS.md](API-AUDIT-FINDINGS.md) / [parity-round/ROUND4.md](parity-round/ROUND4.md)）。
> **本轮不信任旧结论**——旧结论只作为"优先怀疑哪几行"的线索，每条都要重新取证。

## 1. 契约面（权威）

**101 条 operation = 参考 `api/http.py` 的 101 个 `@app.{get,post,put,patch,delete}` 装饰器。**

- 参考源码：`../hindsight/hindsight-api-slim/hindsight_api/api/http.py`（本机绝对路径见下）。
- 本服务契约文档：`internal/httpx/openapi.json`。
- 复核命令（两侧条数必须都是 101）：

  ```sh
  # 参考
  grep -c '@app\.\(get\|post\|put\|patch\|delete\)(' \
    ../hindsight/hindsight-api-slim/hindsight_api/api/http.py
  # 本服务
  python3 -c "import json;print(sum(1 for p in json.load(open('internal/httpx/openapi.json'))['paths'].values() for m in p if m in ('get','post','put','patch','delete')))"
  ```

2026-11-03 核对结果：**route 集合逐条相同，零缺零多。** 唯一差异是 `document_id` 等路径参数
在参考里声明为 `:path` 转换器（可含 `/`），在本服务 openapi 里是普通段——**OpenAPI 文档两侧
一致（逐字节相等）**，但 `:path` 的匹配语义属于路由行为，归入单元 `read-surface` / `transfer` 的
证据项（`GET /v1/default/files/download/{key}` 的 key 可含 `/`）。

## 2. 判定标准

按线上差分判定，不接受"看起来差不多"：

1. **请求侧契约**：路径参数、查询参数声明集合与校验顺序；请求体模型校验错误（`422` 的 `detail`
   数组：`type`/`loc`/`msg`，条目顺序，`input`）。
2. **响应侧契约**：状态码、`Content-Type`、响应头、响应体**键集与键序**、值的类型与形状
   （`null` vs 缺省 vs `[]`）、载荷取值。
3. **副作用**：写库的行、字段与值，以及响应体里报告的计数（`deleted_count`、`total`、`success`）。
   副作用靠"同请求 → 再读"的往返证明。
4. **错误路径**：400 / 404 / 405 / 409 / 422 / 500 以及每一种的字节内容。
5. **后台任务**：触发条件（cadence、开关、jitter）、每轮做的事、幂等性、多进程并发下的行为、
   写库的行与字段、日志行。

## 3. 环境与差分工具

| 物 | 值 |
|---|---|
| 参考服务 | `http://127.0.0.1:8888`，key `sk-hs-f94653018553250b8ffeff305b86ee0a`，库 `127.0.0.1:5432/hindsight`（schema `user_devin`） |
| 本服务 | `scripts/parity_dev_service.sh <端口>` 起在 `182xx`，库 `127.0.0.1:15432/wenyuan` |
| 参考源码 | `../hindsight/hindsight-api-slim/hindsight_api/` |
| 缓存 | `GOCACHE=/tmp/wc/gobuild`、`GOLANGCI_LINT_CACHE=/tmp/wc/lint`（默认缓存在本机只读） |

**差分纪律**

- **绝不改既有 bank。** 写操作只碰自己建的 `audit-<单元>-<随机>` bank，结束前删除。
- 一个子 Agent 一个端口（18220、18221、18222…），用完 `--stop`。
- 夹具噪声（掩掉即可，**不要据此改代码**）：参考 worker 自行 consolidate 造成的
  `consolidated_at` 漂移、`edited_at`/`updated_at` 为各自请求时刻、
  `db_acquire_ms`/`uptime_seconds`/`seconds_since_last_poll` 等活测量、`/version` 的
  `features.worker`（`parity_dev_service.sh` 固定 `WENYUAN_API_WORKER_ENABLED=false`）。
- **严禁迁就性修改**：只在"库不同/进程状态不同"下成立的差异不要改代码。
- 若某条 API 无法线上差分（依赖外部 provider、需要写既有数据），用**源码逐行比对 + 往返用例**
  作为证据，并在报告里写明为什么没有差分。

## 4. 有意增量（不算缺口，比对时排除）

本服务比参考多出这些面，**它们是设计的一部分，不得报为"divergence"**：

| 面 | 说明 |
|---|---|
| `admin` schema 与 `/admin/v1/tenants/**` | 多租户控制面（参考无 HTTP admin 面，`admin/` 只是 CLI） |
| `/admin/v1/teams/**` | team 记忆：team 即 `kind='team'` 的 tenant，project 的 bank 为 `<team>::<name>` |
| `project.merge` 后台任务 | team 记忆的合并作业 |
| `personal.rollup` 后台任务 + `<tenant>::myself` | myself 记忆的聚合作业 |
| `GET /v1/default/banks/{bank_id}/document-transfer` | 参考里已移除（保留 410 语义）；本服务保留同名路由，归 `transfer` 单元核对语义 |
| MCP `/mcp` 命名空间 | 参考有对应实现，**不排除**，归 `mcp` 单元 |

## 5. 工作单元（每个单元 = 一个子 Agent）

写作用域按**不重叠**切；一个单元可含多条 API，但**每条 API 单独取证、单独结论**。

### 5.1 API 单元

- [x] `monitor`（#1–#5）：`/health`、`/health/ready`、`/health/live`、`/version`、`/metrics`
- [x] `bank-lifecycle`（#6–#13）：banks 列表、别名四件套、PUT/PATCH/DELETE bank
- [x] `bank-config`（#14–#20）：profile、background、bank-template-schema、config 三件套
- [x] `retain`（#21–#25）：dry-run-extract、prompts/preview、retain、files/retain、clear memories
- [x] `recall-reflect`（#26–#27）：recall、reflect
- [x] `memory-crud`（#28–#31）：memories/list、get、patch、history
- [x] `observations`（#32–#37）：clear observations、scopes、策略预览、recover、clear memory observations、consolidate
- [x] `mental-models`（#38–#46）：list/get/history/create/refresh/dry-run-refresh/clear/patch/delete
- [x] `knowledge`（#47–#54）：tree/folders/pages/export/search/page/nodes
- [x] `directives`（#55–#59）：CRUD
- [x] `read-surface`（#60–#72）：graph、entities 四件套、documents 六件套、tags、chunks
- [x] `operations`（#73–#77）：list/get/cancel/retry/delete
- [x] `transfer`（#78–#87）：import/export 模板、document-transfer 三件套、transfer 两件套、clone、attachment、download
- [x] `webhooks`（#88–#92）：CRUD + deliveries
- [x] `reporting-audit`（#93–#101）：stats、health/llm、timeseries、audit-logs 三件套、llm-requests 两件套

### 5.2 后台任务单元

- [x] `bg-maintenance`（参考 `engine/maintenance.py` `MaintenanceLoop`）
- [x] `bg-worker`（参考 `worker/poller.py`、`worker/main.py`、`engine/background_writes.py`、
  `engine/retain/embedding_coalescer.py`、`metrics.py` 的 backlog 刷新）
- [x] `bg-engines`（参考 `engine/graph_maintenance.py`、`engine/mental_model_refresh.py`、
  `engine/chinese_temporal_periods.py`、`engine/llm_trace.py` 里的周期/异步副作用）

## 6. API 编号表（101 条）

编号与 operationId 由 `internal/httpx/openapi.json` 与参考 `api/http.py` 装饰器行号生成。
"参考行"是 2026-11-03 的 `api/http.py` 行号；同事先 `sed -n '<行>p'` 核对这一行再往下读。

### 单元 `monitor`

- [x] #1 `GET /health`
- [x] #2 `GET /health/ready`
- [x] #3 `GET /health/live`
- [x] #4 `GET /version`
- [x] #5 `GET /metrics`

### 单元 `bank-lifecycle`

- [x] #6 `GET /v1/default/banks`
- [x] #7 `GET /v1/default/banks/{bank_id}/aliases`
- [x] #8 `POST /v1/default/banks/{bank_id}/aliases`
- [x] #9 `PATCH /v1/default/banks/{bank_id}/aliases/{alias}`
- [x] #10 `DELETE /v1/default/banks/{bank_id}/aliases/{alias}`
- [x] #11 `PUT /v1/default/banks/{bank_id}`
- [x] #12 `PATCH /v1/default/banks/{bank_id}`
- [x] #13 `DELETE /v1/default/banks/{bank_id}`

### 单元 `bank-config`

- [x] #14 `GET /v1/default/banks/{bank_id}/profile`
- [x] #15 `PUT /v1/default/banks/{bank_id}/profile`
- [x] #16 `POST /v1/default/banks/{bank_id}/background`
- [x] #17 `GET /v1/bank-template-schema`
- [x] #18 `GET /v1/default/banks/{bank_id}/config`
- [x] #19 `PATCH /v1/default/banks/{bank_id}/config`
- [x] #20 `DELETE /v1/default/banks/{bank_id}/config`

### 单元 `retain`

- [x] #21 `POST /v1/default/banks/{bank_id}/memories/dry-run-extract` —— ❌已修（4 处：`ctx` 键序、非字符串判别值、被拒块 `input` 书写序、`missing` 的 `input` 书写序）；15 例差分全 SAME，见 `API-AUDIT-FINDINGS.md` `## 第五轮 retain`；`verbatim` 预览维持 `⚠️存疑（参考自身缺陷）`
- [x] #22 `POST /v1/default/banks/{bank_id}/prompts/preview` —— ✅一致（15 例全 SAME，未改代码）
- [x] #23 `POST /v1/default/banks/{bank_id}/memories` —— ✅一致（HTTP 契约面：同步/异步收据、校验、`update_mode`/`operation_id` 约束、字段传递共 20+ 例全 SAME）；`chunks` 行差异定性为 `⚠️存疑（参考同步路径回归，不照抄）`，含 async-vs-sync 决定性对照
- [x] #24 `POST /v1/default/banks/{bank_id}/files/retain` —— ✅一致（部件校验/404/multipart 上传/`file_convert_retain` op 行形状 15 键全同序）；`⚠️1 处存疑`：参考转换后另提一条独立 `retain` op（`operations` 行数 2 vs 1，跨 storage+worker 结构性缺口，不影响 `POST` 响应）
- [x] #25 `DELETE /v1/default/banks/{bank_id}/memories` —— ✅一致（10 例全 SAME：`type` 语义、重复参数末值优先、未知 bank 200 而非 404；未改代码）

### 单元 `recall-reflect`

- [x] #26 `POST /v1/default/banks/{bank_id}/memories/recall` —— ❌已修（3 处：请求体级校验项 `input` 键序、trace 十个块的键序、`retrieval_results` 的 fact_type 顺序跟随请求 `types`）；28 例校验全 SAME、trace 逐块同序；第四轮三处无回归
- [x] #27 `POST /v1/default/banks/{bank_id}/reflect` —— ❌已修（body 级校验项 `input` 键序，与 #26 同源）；15 例校验全 SAME；`⚠️存疑`：成功路径因部署模型端点拦截长会话而恒 500（已逐条排除本服务请求构造，建议列入 PARITY-OPEN-ITEMS）

### 单元 `memory-crud`

- [x] #28 `GET /v1/default/banks/{bank_id}/memories/list`
- [x] #29 `GET /v1/default/banks/{bank_id}/memories/{memory_id}`
- [x] #30 `PATCH /v1/default/banks/{bank_id}/memories/{memory_id}`
- [x] #31 `GET /v1/default/banks/{bank_id}/memories/{memory_id}/history`

### 单元 `observations`

- [x] #32 `DELETE /v1/default/banks/{bank_id}/observations`
- [x] #33 `GET /v1/default/banks/{bank_id}/observations/scopes`
- [x] #34 `POST /v1/default/banks/{bank_id}/consolidation-strategies/preview`
- [x] #35 `POST /v1/default/banks/{bank_id}/consolidation/recover`
- [x] #36 `DELETE /v1/default/banks/{bank_id}/memories/{memory_id}/observations`
- [x] #37 `POST /v1/default/banks/{bank_id}/consolidate`

### 单元 `mental-models`

- [x] #38 `GET /v1/default/banks/{bank_id}/mental-models`
- [x] #39 `GET /v1/default/banks/{bank_id}/mental-models/{mental_model_id}`
- [x] #40 `GET /v1/default/banks/{bank_id}/mental-models/{mental_model_id}/history`
- [x] #41 `POST /v1/default/banks/{bank_id}/mental-models`
- [x] #42 `POST /v1/default/banks/{bank_id}/mental-models/{mental_model_id}/refresh`
- [x] #43 `POST /v1/default/banks/{bank_id}/mental-models/{mental_model_id}/dry-run-refresh`
- [x] #44 `POST /v1/default/banks/{bank_id}/mental-models/{mental_model_id}/clear`
- [x] #45 `PATCH /v1/default/banks/{bank_id}/mental-models/{mental_model_id}`
- [x] #46 `DELETE /v1/default/banks/{bank_id}/mental-models/{mental_model_id}`

### 单元 `knowledge`

- [x] #47 `GET /v1/default/banks/{bank_id}/knowledge-base/tree`
- [x] #48 `POST /v1/default/banks/{bank_id}/knowledge-base/folders`
- [x] #49 `POST /v1/default/banks/{bank_id}/knowledge-base/pages`
- [x] #50 `GET /v1/default/banks/{bank_id}/knowledge-base/export`
- [x] #51 `GET /v1/default/banks/{bank_id}/knowledge-base/search`
- [x] #52 `GET /v1/default/banks/{bank_id}/knowledge-base/pages/{page_id}`
- [x] #53 `PATCH /v1/default/banks/{bank_id}/knowledge-base/nodes/{node_id}`
- [x] #54 `DELETE /v1/default/banks/{bank_id}/knowledge-base/nodes/{node_id}`

### 单元 `directives`

- [x] #55 `GET /v1/default/banks/{bank_id}/directives`
- [x] #56 `GET /v1/default/banks/{bank_id}/directives/{directive_id}`
- [x] #57 `POST /v1/default/banks/{bank_id}/directives`
- [x] #58 `PATCH /v1/default/banks/{bank_id}/directives/{directive_id}`
- [x] #59 `DELETE /v1/default/banks/{bank_id}/directives/{directive_id}`

### 单元 `read-surface`

- [x] #60 `GET /v1/default/banks/{bank_id}/graph`
- [x] #61 `GET /v1/default/banks/{bank_id}/entities`
- [x] #62 `GET /v1/default/banks/{bank_id}/entities/graph`
- [x] #63 `GET /v1/default/banks/{bank_id}/entities/{entity_id}`
- [x] #64 `POST /v1/default/banks/{bank_id}/entities/{entity_id}/regenerate`
- [x] #65 `GET /v1/default/banks/{bank_id}/documents`
- [x] #66 `GET /v1/default/banks/{bank_id}/documents/{document_id}/chunks`
- [x] #67 `POST /v1/default/banks/{bank_id}/documents/{document_id}/reprocess`
- [x] #68 `GET /v1/default/banks/{bank_id}/documents/{document_id}`
- [x] #69 `GET /v1/default/banks/{bank_id}/tags`
- [x] #70 `GET /v1/default/chunks/{chunk_id}`
- [x] #71 `PATCH /v1/default/banks/{bank_id}/documents/{document_id}`
- [x] #72 `DELETE /v1/default/banks/{bank_id}/documents/{document_id}`
- [x] #100 `GET /v1/default/banks/{bank_id}/documents/{document_id}/history`（本轮新增，从未比对过）

### 单元 `operations`

- [x] #73 `GET /v1/default/banks/{bank_id}/operations`
- [x] #74 `GET /v1/default/banks/{bank_id}/operations/{operation_id}`
- [x] #75 `DELETE /v1/default/banks/{bank_id}/operations/{operation_id}`
- [x] #76 `POST /v1/default/banks/{bank_id}/operations/{operation_id}/retry`
- [x] #77 `DELETE /v1/default/banks/{bank_id}/operations/{operation_id}/delete`

### 单元 `transfer`

- [x] #78 `POST /v1/default/banks/{bank_id}/import`
- [x] #79 `GET /v1/default/banks/{bank_id}/export`
- [x] #80 `GET /v1/default/banks/{bank_id}/document-transfer`
- [x] #81 `POST /v1/default/banks/{bank_id}/document-transfer/export`
- [x] #82 `POST /v1/default/banks/{bank_id}/document-transfer`
- [x] #83 `POST /v1/default/banks/{bank_id}/transfer/export`
- [x] #84 `POST /v1/default/banks/{bank_id}/transfer/import`
- [x] #85 `POST /v1/default/banks/{bank_id}/clone`
- [x] #86 `GET /v1/default/banks/{bank_id}/attachments/{attachment_id}`
- [x] #87 `GET /v1/default/files/download/{key}`

### 单元 `webhooks`

- [x] #88 `POST /v1/default/banks/{bank_id}/webhooks`
- [x] #89 `GET /v1/default/banks/{bank_id}/webhooks`
- [x] #90 `DELETE /v1/default/banks/{bank_id}/webhooks/{webhook_id}`
- [x] #91 `PATCH /v1/default/banks/{bank_id}/webhooks/{webhook_id}`
- [x] #92 `GET /v1/default/banks/{bank_id}/webhooks/{webhook_id}/deliveries`

### 单元 `reporting-audit`

- [x] #93 `GET /v1/default/banks/{bank_id}/stats`
- [x] #94 `POST /v1/default/banks/{bank_id}/health/llm`
- [x] #95 `GET /v1/default/banks/{bank_id}/stats/memories-timeseries`
- [x] #96 `GET /v1/default/banks/{bank_id}/audit-logs`
- [x] #97 `GET /v1/default/banks/{bank_id}/audit-logs/stats`
- [x] #98 `GET /v1/default/banks/{bank_id}/llm-requests`
- [x] #99 `GET /v1/default/banks/{bank_id}/llm-requests/stats`
- [x] #101 `GET /v1/default/banks/{bank_id}/audit-logs/{log_id}`（本轮新增，从未比对过）

## 7. 后台任务清单

参考后端的"定时/异步后台面"共 6 组入口（2026-11-03 用 `grep -rn create_task` 全量枚举）：

| 组 | 参考落点 | 本服务落点 | 状态 |
|---|---|---|---|
| 维护循环（4 个作业） | `engine/maintenance.py:123` `MaintenanceLoop` | `internal/maintenance/loop.go` + `internal/app/maintenance_jobs.go` | [x] |
| worker 轮询 + 恢复 | `worker/poller.py:343` `WorkerPoller`（`_run_recovery`:1895） | `internal/worker/` | [x] |
| retain 子批并发 | `engine/memory_engine.py:7674` | `internal/app/worker_retain.go` | [x] |
| embedding 批并发 | `engine/embeddings.py:284` | `internal/provider/` | [x] |
| 后台写（fire-and-forget） | `engine/background_writes.py` | `internal/` 对应处 | [x] |
| llm trace 异步落库 | `engine/llm_trace.py`（缓冲 + `flush_on_shutdown` + memory_ids 补丁） | `internal/app/llm_trace.go` | [x] |
| metrics backlog 刷新 | `metrics.py:1209` | `internal/monitoring/` | [x] |

参考维护循环的 4 个作业（`MaintenanceLoop._tick`，`engine/maintenance.py:225`）：

| 作业 | 开关 | 间隔键 | 本服务对应 | 第五轮 |
|---|---|---|---|---|
| retention（`audit_log` / `llm_requests` 过期清理） | `retention_sweep_interval_seconds > 0` 且各自 retention days | `retention_sweep_interval_seconds` | `retention.audit_log` / `retention.llm_requests` | [x] |
| consolidation reconcile | `consolidation_reconcile_interval_seconds > 0` | 同左 | `consolidation.reconcile` | [x] |
| scheduled mental model refresh | `mental_model_refresh_tick_seconds > 0` | 同左 | `mental_model.scheduled_refresh` | [x] |
| terminal operation cleanup | `operation_cleanup_interval_seconds > 0` 且 `operation_retention_days > 0` | 同左 | `operations.cleanup` | [x] |

本服务另有 `vector_index.reconcile`、`graph.maintenance`、`personal.rollup`、`project.merge` 四个作业——
**必须逐条证明它们对应参考的哪一处**（或在参考里确实没有、属于有意增量），不能默认"多的是特性"。
**这是本轮最容易漏的部分：参考的 graph 维护/向量索引重建是否在别处（如 worker 内联）执行，必须先查清。**

> 2026-11-03 结论（`## 第五轮 bg-maintenance`）：四个参考作业全部存在且开关条件等价；每轮行为共发现并修复
> 13 处偏离（retention 3、reconcile 3、mm refresh 3、operation cleanup 2，外加四作业的例程/过滤口径）。
> `vector_index.reconcile` 与 `graph.maintenance` 在参考里**都不是维护循环作业**（前者是写路径触发的异步
> operation，后者是 worker 认领的 `graph_maintenance` operation），两者判 `⚠️存疑（实现路径不同）` 并保留 —
> 详见 `API-AUDIT-FINDINGS.md` §H/§I。`personal.rollup` / `project.merge` 为有意增量，不影响参考面。

## 8. 子 Agent 输出约定

每个子 Agent：
1. 只勾选本文件的**自己的节**（用 `edit` 精确替换 `- [ ]` → `- [x]`）。
2. 只往 `docs/API-AUDIT-FINDINGS.md` 追加自己的 `## 第五轮 <单元名>` 小节，不动别人的。
3. 只在自己的写作用域里改代码；**共享文件**（`internal/storage/`、`internal/httpx/`、
   `internal/app/app.go`）本轮按用户裁决**允许直接改**，但必须在报告里列出改了哪些共享文件
   与行号，便于组长复核冲突。
4. 每处行为修改必须带一个"改回去就 FAIL"的表驱动用例，并说明证伪方式。
5. 不提交 git；不改 `go.mod`；不新增依赖。

结论只用三种：`✅一致`（有证据）、`❌已修`（给出用例名 + 差分结果）、`⚠️存疑`（给出证据，
并写明需要组长裁决什么）。

验证门（各自作用域内）：

```sh
export GOCACHE=/tmp/wc/gobuild GOLANGCI_LINT_CACHE=/tmp/wc/lint
go test -count=1 ./internal/<包>/...
go vet ./internal/<包>/...
gofmt -l internal/<包>
golangci-lint run ./internal/<包>/...
```

组长在全部单元完成后统一跑 `make check` 并提交。
---

## 9. 收官记录（2026-11-04，第五轮完成）

### 覆盖

**101 条 API 全部有结论 + 6 组后台任务全部有结论。** checklist 119/119 勾选完毕。
`docs/API-AUDIT-FINDINGS.md` 的 22 个 `## 第五轮*` 小节覆盖 1–101 全部编号（已用「节标题 → 该节 #编号」交叉表核验，脚本判据：每个 #N 必须落在其归属单元的小节内）。

**新增的两个从未比对过的面已覆盖**：
- #100 `GET .../documents/{document_id}/history` —— ✅一致，且做了分页语义等价性证明（`total` 计操作数、`items` 计事件数）
- #101 `GET .../audit-logs/{log_id}` —— ✅一致，10 键及键序逐字节相同；非 UUID → 404 而非 400

### 验证门（组长最终执行）

| 门 | 结果 |
|---|---|
| `go build ./...` | PASS |
| `CGO_ENABLED=0 go build -tags pgx ./...` | PASS |
| `go vet ./...` | PASS |
| `gofmt -l internal cmd` | 空 |
| `go test -count=1 ./...` | **5459 passed / 56 packages，0 失败** |
| `golangci-lint run ./...` | **No issues found** |

### 本轮修复的真实缺陷（按影响面）

**契约面（响应字节可见）**
1. `POST .../memories/recall` trace 全部 10 个块的键序（Go map 字母序 → 参考声明序），以及 `retrieval_results` 忽略请求 `types` 顺序
2. `POST .../reflect` body 级校验 `input` 键序
3. `POST .../memories/dry-run-extract` 4 处：`ctx` 键序、非字符串判别值误报 `union_tag_not_found`、被拒块 `input` 书写序、`missing` 的 `input` 层对象
4. `GET .../memories/{id}` / `PATCH` 响应 20 键键序（`httpx.OrderedObject`）
5. `GET .../mental-models/{id}` 的 `reflect_response` 成员序（`reflect_skipped` 与 `mental_models` 位置相反）
6. `GET .../knowledge-base/tree` 的 `trigger` 18 键键序
7. `GET .../documents/{id}` 成员键序 + `nodes_by_fact_type` 内层键序
8. `PUT .../profile` / `POST .../background` 缺 `X-Ignored-Params`
9. `entity_labels: []` 读回形状（裹 envelope vs 裸数组）+ `input_value` 键序（字母序 vs 调用方书写序）
10. bank config 键前缀只认 `WENYUAN_API_*`（参考客户端发 `HINDSIGHT_API_*` 会被拒）
11. 审计行多写 `metadata.error` 与失败路径的 `response`（参考失败行是 `{}` 与 `null`）

**后台任务**
12. maintenance 四个作业未用参考 PL/pgSQL 例程（谓词不同）、单 schema 失败静默跳过其后所有 schema、paused 模型额外写库、缺 tenant 过滤
13. worker wall ceiling 被 lease 300s 截断（参考 3600s/7200s 表全部失效）、超限走 retry 而非终态、缺 idle 可续期上限
14. append-conflict 退避阶梯缺失（参考 `min(2^n·2s,300s)·uniform(0.5,1.5)`）
15. mental model refresh 的 transient 失败被当成永久失败（吞掉重试预算）
16. graph/entity maintenance drain 缺 `FOR UPDATE SKIP LOCKED`（多进程互等）
17. llm_trace 同步逐行写（参考 0.5s/500 行缓冲批量）+ 缺 `flush_on_shutdown` + 缺 `metadata.memory_ids` 补丁
18. `transfer/import` 归档校验时机（参考 `or` 短路：给 `target_bank_id` 就不解析归档）
19. consolidation enqueue 缺 bank 存在性检查（未知 bank 应答 404 而非 200）
20. `bankadmin` 两个死函数（既有问题，使 `make check` 不通过）

### 记录的存疑/不修项（均有证据）

- 参考侧自身异常不复刻：同步 retain 不写 chunks（控制变量实验证明）、`DETAIL: Failing row contains` 的 500 泄露被拒行内容
- 参考非确定性而非契约：bank config 键序（frozenset 随 PYTHONHASHSEED 变）、`Valid configurable fields` 样本
- 夹具噪声：`/version` 的 `features.worker`、worker 开关导致的 `last_full_refreshed_at`、两侧各自 retain 的 LLM 输出差异
- 结构性差距（建议列 `PARITY-OPEN-ITEMS`）：`file_retain` 的独立 `retain` op（参考一条变两条）、reflect 系统提示词过长（11819 字符，部署侧可精简）、`/metrics` 千 bank 时逐 bank 查询可能超时返回空 series

### 流程经验（下一轮直接沿用）

1. **`docs/API-AUDIT-FINDINGS.md` 只能用 `cat >>` 追加**；除 checklist 单行 `edit` 外禁止任何整文件写回（本轮发生过整文件重写冲掉 240 行的事故）
2. **报"一致"前必须先证明有数据**：参考的审计默认关闭，空表 SAME 是零证据力
3. **审计/日志类接口必须成功 + 失败各打一次**：本轮 2 处审计行差异都是失败路径专属
4. **`parity_check.py` 先 `json.loads`，看不到键序**：键序类结论必须用原始字节比对。本轮 11 处修复有 6 处是键序
5. **差分脚本必须 `set -f`**：`"0 3 * * *"` 这类 cron 会被 shell 展开成文件列表，两侧同时污染成"假差异"
6. **参考侧状态会被探测污染**：`consolidate`/`recover`/`clear observations` 会改参考自身状态，探测前须确认无 pending/失败标记
7. **服务进程必须用持久终端托管**：`/tmp` 每次 bash 调用被清空，`nohup` 进程活不过一次调用
8. **启动参数**：`-tags pgx` 不可省；`WENYUAN_API_SKIP_LLM_VERIFICATION=true` 不可省（否则模型 provider 校验超时退出）

# 契约面复核（2026-10-15）：与上游 OpenAPI 逐项对照

> 复核对象：`wenyuan`（Go）对上游 `hindsight-api-slim/hindsight_api`（Python）HTTP 服务的复刻程度。
> 复核方：11 个独立审计切片，全部以**上游 OpenAPI 文档**为契约基准
> （`hindsight-docs/static/openapi.json`，99 个操作 / 195 个 schema，复核时版本 0.10.3，
> 2026-10-16 已随参考 v1.0.0 发布重新捕获为 1.0.0——路径与 schema 未变；
> 已用脚本与上游 pydantic 模型逐类核对，133 个可比类中 130 个完全一致，其余 3 个为
> `async_`/`async` 别名与继承字段，故该文档可用作契约真值）。
>
> 复核结论：**路由面 99/99 完整，但契约的字段/状态码/头/默认值层面并非 100%。**
> 本文件是这次复核的完整清单；`docs/GAPS.md` 仍是当前状态总览，
> `docs/PARITY-PLAN.md` 记录实施与进度。

## 0. 复核方法

| 手段 | 说明 |
|---|---|
| 契约清单 | `scripts/reference_openapi.py` 捕获的参考文档即 `internal/httpx/openapi.json`，逐操作展开成 params / body / responses 的紧凑清单，作为每个切片的比对基准 |
| 空值策略 | 由参考文档机械推导：含「required 且 nullable」字段或 `OpenRowModel` 行的模型**保留** null，其余**丢弃** null（上游 `ExcludeNoneRoute` 的规则），共 29 个保留模型 |
| 逐操作核对 | 每个切片的 agent 对每个操作核对：是否挂载、参数名/必填/enum/默认值、请求体字段是否被接收并生效、响应字段名/类型/必填/空值策略、状态码、响应头、校验错误信封 |
| 证据 | 每条发现都带 wenyuan `file:line` 与上游 `file:line`；无 shell 的切片明确标注「静态证据，未经实跑」，实跑复核由集成方补做 |

## 1. 本轮已修复（集成方直接实施）

| 项 | 内容 | 证据/实现 |
|---|---|---|
| 描述面缺失 | 上游作为 FastAPI 应用**默认**提供 `GET /openapi.json`、`/docs`、`/docs/oauth2-redirect`、`/redoc`（上游自带测试 `tests/test_base_path.py` 断言），wenyuan 此前全部 404 | `internal/httpx/openapi.go`：契约文档取自参考（`scripts/reference_openapi.py` 生成，嵌入 `internal/httpx/openapi.json`），文档页取自上游 FastAPI 生成结果，二者皆按 base path 改写 URL |
| base path | `HINDSIGHT_API_BASE_PATH`（上游 `root_path`）此前完全不支持 | `internal/config`（解析 + 归一化）与 `internal/httpx.BasePathMiddleware`：带前缀与不带前缀**都**可路由（与上游 `get_route_path` 的剥离语义一致）；文档 `servers` 仅在配置前缀时注入 |
| `/version` 版本号 | 报 `1.0.0`（wenyuan 自己的版本）；上游报自身发布版本 | `httpx.APIVersion = ReferenceAPIVersion = "1.0.0"`（随参考 v1.0.0 发布同步），并由测试钉住与所服务契约文档 `info.version` 一致 |
| 特性开关默认值 | MCP/银行配置 API/文档导入/文档导出默认 false（上游 true）、audit_log 硬编码 true（上游 false）、llm_trace 默认 false（上游 true）、observations/store_document_text 硬编码 | `internal/config`：默认值对齐上游，新增 `HINDSIGHT_API_ENABLE_OBSERVATIONS`/`_AUDIT_LOG_ENABLED`/`_STORE_DOCUMENT_TEXT`，`FeatureMap` 改为字段驱动 |
| 空值策略 | `HINDSIGHT_API_DROP_NULL_FIELDS` 默认 **false**，导致上游会省略的 null 字段被输出 | 默认改为 true（上游 `ExcludeNoneRoute` 的规则）；保留 null 的模型由 `httpx.WriteJSONOpenRow` 逐响应覆盖 |
| 未匹配路由响应 | 未命中路由/方法返回 Go 的纯文本 `404 page not found`/`405`；上游返回 JSON `{"detail":"Not Found"}`/`{"detail":"Method Not Allowed"}`（405 保留 `Allow` 头） | `internal/httpx/fallback.go`（仅改写路由器自身的拒绝，处理器自己写的 404/405 原样通过） |
| 认证缺失 | 上游可经 `HINDSIGHT_API_TENANT_EXTENSION`+`_TENANT_API_KEY` 对**所有**引擎路由强制 API key（401 `{"detail":"Authentication failed: Invalid API key"}`）；wenyuan 此前只在 3 条 retired 路由上做「Authorization 头是否存在」的假校验 | `internal/config.ResolveTenantAuth`（仅支持内置 `ApiKeyTenantExtension`，其它选择启动即失败而不是静默无认证）+ `httpx.TenantAPIKeyMiddleware`（保护 `/v1/default/*` 与 `/mcp`，豁免 retired 路径） |
| 请求体校验状态码 | 非法/类型错误的 JSON 体返回 400 单句；上游是 422 结构化条目（`json_invalid`/`int_parsing`/`string_type`…） | `httpx.DecodeBody` 生成上游词汇表的 422 条目；`httpx.WriteError`/`WriteValidationFor` 直接渲染 `*ValidationFailure` |
| 校验条目形状 | `loc` 中的列表下标是**字符串**；上游是整数 | `httpx.ValidationError.MarshalJSON` 将全数字段渲染为 JSON 数字 |
| retired 文案 | 三条 retired 路由共用一句文案；上游 profile 与 background 是两句不同文案 | `httpx.DetailRetiredProfile` / `DetailRetiredBackground`（逐字对齐上游） |
| 准入（admission）语义 | 保留车道按「后缀」匹配、**不看方法**，`DELETE /memories`（清空）会占用 retain 车道；上游只对 `POST` 三个写路由设限，且被拒文案不同 | `httpx.AdmissionRule` 增加 `Method`/`Lane`，文案改为上游的 `Server is at capacity for '<lane>' (N concurrent); try again shortly.`，499 文案改为 `client disconnected while queued` |
| 文档成员路由 | `{document_id:path}` 允许文档 id 含 `/`；wenyuan 用单段 `{document_id}`，含斜杠的 id 直接 404 | 改为子树注册 + 手动切分（`SuffixDocumentMember = "/documents/"`），处理器仍读 `path value` |
| 指标名 | **已闭合（本非分歧）**：导出 `hindsight_*`，与上游一致，无需改名 | `internal/monitoring/metrics.go:112` 直接导出 `hindsight_http_requests_total`（及 `hindsight_http_requests_in_progress`、`hindsight_http_duration_seconds`），正是上游 `hindsight_api/metrics.py:527` 的 OTel 名 `hindsight.http.requests.total` 在 Prometheus 下的渲染形式（`.` → `_`）；两侧同名同标签 |
| 画像日志 | `HINDSIGHT_API_PROFILE` 被当作 bool 开关 pprof 端点；上游是 JSON（`{"every":60,"top":20}`），启动即校验并周期输出 `[profile]` 日志 | `internal/config.ResolveProfile`（同上游的校验与报错）+ `monitoring.ProfileEmitter`（`/proc` 逐线程 CPU + Go 运行时摘要）；Go 的 pprof 端点移到独立的 `HINDSIGHT_API_PPROF_ENABLED` |

## 2. 本轮已修复（按包并行实施）

| 包 | 修复内容 |
|---|---|
| `internal/reporting`（knowledge-base） | 建页/建目录 201、`CreateKnowledgePageResponse` 字段、搜索 `snippet`、导出 `index.md`、`UpdateNodeRequest` 全字段、树/搜索的 tag 过滤、搜索 `q` 必填与 `limit` 1..50、页面 `type`/`description`/`body` 空值策略、`/v1/bank-template-schema` 对齐上游 `model_json_schema()` |
| `internal/reporting`（memories/observability） | `fact_type` 字段名、`state`/`consolidation_state` 枚举与拒绝、`tags_match` 默认 `any`、`limit` 默认 100、记忆详情字典（`entities` 为数组等）、`PATCH` 请求体 9 字段、历史接口返回**裸数组**、`POST /consolidate` 200、巩固策略预览形状、`prompts/preview` 的 blocks/run_settings、`dry-run-extract` 的 facts/chunks/usage/content 联合类型与逐次覆盖字段、timeseries 的 period/trunc/time_field 响应字段与补零网格 |
| `internal/mentalmodels` + `internal/directives` | `trigger`/`id` 字段、refresh 返回 `AsyncOperationSubmitResponse`、dry-run 全字段、history 裸数组、未知 bank 惰性创建、请求体校验改 422 |
| `internal/transfer` + `internal/webhooks` | `/import` 与 `/export` 绑回 JSON 模板语义（原来绑到归档 multipart/ZIP）、`transfer/import` 默认 `restore` 与枚举校验、webhook 建 201、`WebhookResponse` 含 `secret`（保留 null）、`http_config` 结构化、投递列表 `{items,next_cursor}` 与上游条目字段 |
| `internal/readsurface` | 实体重建 410、`/graph` 与 `/entities*` 的参数/默认值、`/documents` 的 `q` 过滤文档 id、`tags_match` 默认 `any_strict`、分页默认 100、附件用 `ChunkAttachment`、`/tags` 源自记忆单元 |
| `internal/storage` | 双后端同步：读面过滤/分页、记忆单元 tag 直方图、别名 `primary` 降级、操作查询的 bank 作用域与 `exclude_parents`、子操作 |
| `internal/bankapi` | retired 路由不再要求凭证（无条件 410）、`/config` 返回完整解析配置、`PUT/PATCH bank` 12 个被忽略字段生效、`/banks` 分页默认与边界、config 键集合与 400、别名 `primary` |
| `internal/memoryops` | retain `resolve_entities` 默认 true、宽松 ISO 时间戳、recall `include.entities` 缺省即包含、操作路由 bank 作用域与 `exclude_parents`、非 UUID 操作 id 返回 400、`OperationResponse` 字段（`child_operations`/`task_payload`） |
| `internal/mcp` | 工具清单与参数对齐上游（39 项逐个 JSON 级比对）、`bank_id` 选填、`/mcp/{bank_id}` 单 bank 模式、JSON-RPC 错误码与初始化字段 |
| 集成方收尾 | 时序窗口表移入 `internal/storage`（两后端共用：1h/12h/1d/7d/30d/90d → 桶宽/步长/桶数，未知周期回落 7d）、内存后端插入时给 `created_at` 打戳（否则该轴为空）、删除 recall 请求/响应中上游没有的 `limit`/`offset`/`truncated`、缺失银行改为 `Bank '<id>' not found`、`/entities/{id}/regenerate` 410 文案、实体图节点来自边端点与上游一致的配色/线型 |

## 3. 逐项复核结论（2026-10-15 收口）

> 本节原来的"尚未闭合"清单是**本轮复核结束时**写下的，其中多项已被此后无关工作闭合。收口时对每项
> 重新核验：**先看代码与可执行证据，再决定是否动手**。结果有两种——「已闭合（附证据）」以及
> 「前提本身是反的，真实分歧在别处（已修）」。照单直改会改坏已经正确的东西，这一点值得写下来。

| # | 项 | 结论与证据 |
|---|---|---|
| 1 | `X-Ignored-Params` 的**集合** | **已闭合**。按路由声明集计算并排除同名路径参数：`internal/httpx/queryparams.go:205-236`（`pathParamNames` + `declared`）、31 个 `QueryParams(…)` 调用点、`middleware_test.go:72`、`internal/app/query_declaration_test.go`。上游对应 `api/unknown_params.py:167-175` |
| 2 | 校验条目的 `type` 词汇表与 `ctx`/`url` | **原前提是反的**。上游**从不发** `url`：FastAPI 用 `exc.errors(include_url=False)`（参考 venv 的 `fastapi/_compat/v2.py:187`），其契约 `ValidationError` 只声明 `loc/msg/type/input/ctx`、required 三项——是我们多发了一个上游从不发的 `url`，并把 pydantic 版本钉错（2.13 vs lock 里的 2.12）。**已修**：移除 `url`；`type`/`msg` 改为按**目标类型**分派（`list_type`/`dict_type`/`*_parsing` 全句/非字符串 `*_type`/小数 `int_from_float`——此前先看输入类型，`{}` 送进 `int` 会被命名为 `dict_type`）；空 body = `missing` 且保留 `input:null`；recall/reflect 的 `input` 具名。证据：18 例**走真实路由器**的 served 表（含"不存在 `url`"断言）+ `internal/app/parity_integration_test.go` |
| 3 | 银行 id 形状校验 | **原前提是反的**。上游**确实**校验，只是**只在创建**：空 / 超 192 UTF-8 字节 / 控制字符 → 400（`engine/retain/bank_utils.py:341-352`，文档字符串明写 *"Existing banks are never re-checked"*），由 `_ensure_bank_exists` 触发（为此 `retain` 首次写也会建行）；上游自己的 `test_50_banks_and_config.py` 同时钉住"创建 400"与"未知 bank 404"。**已修两处真分歧**：内存后端 retain 曾接受被拒 id（200 → 400，同步+异步）、`PATCH /banks/{id}/config` 曾吞掉创建错误（404 → 400）。另**新发现并已修**：`DELETE /banks/{id}/config` 对不存在的 bank 上游是 **200**（无条件更新 + 随后读配置，故不创建行），我们曾 404——真服务冒烟已验证 `reset: 200` → 随后 `GET /config: 404` |
| 4 | 审计与 LLM 请求的过滤器 | **已闭合**。两后端都暴露并在 SQL 生效：审计 `action`/`transport`/`start_date`/`end_date`/`limit`/`offset`；LLM 请求另加 `status`/`operation`/`scope`/`provider`/`trace_id`/`document_id`/`memory_id`/`group`（`internal/storage/observability.go:25-36,89-113`、`postgres_observability.go:263-470`、`memory_observability.go:181-360`）。本轮补了"不传过滤"与"组合过滤"两个测试缺口 |
| 5 | 指标标签集与时长直方图 | **已闭合**：三个 instrument 与上游同名同标签（`internal/monitoring/metrics.go:57-133`），桶与上游 `HTTP_DURATION_BUCKETS` 相同。**真残留已修**：`endpoint` 标签曾原样带出 Go 的通配符（`{chunk_id...}`），上游是**转换器剥离后**的模板（`{chunk_id}`，其自有测试 `tests/test_http_observability_middleware.py:200` 钉住）——同一路由在两侧是两个 series。已改为剥离（注册用的 pattern 不动） |
| 6 | 405 的指标 `endpoint` 标签 | **已闭合**：用不带方法段的模式解析，路径匹配/方法不匹配即该路由模板（`internal/httpx/router.go:170-190`、`metrics_test.go:47-51`）。上游 `observability.py:93-115` "首次完全匹配，否则首次仅路径匹配（405）" |
| 7 | `/health` 未初始化态与连接池统计 | **已闭合**，且原前提有误：上游未初始化分支在**读池统计之前**就返回，因此那里**不该**有池键；成功分支才有 `db_acquire_ms`/`db_pool_in_use/max/idle`（`engine/memory_engine.py:6196-6240` ↔ `internal/monitoring/health.go:96-104`）。**残留（无法实现，已记录）**：成功分支的 `db_pool_waiting`（在途 acquire）——Go 的 `database/sql` 不暴露"当前等待数"，上游自己的池统计本身是尽力而为、允许后端省略 |
| 8 | 知识库 `tag_groups` 的 `resolve:"fuzzy"` | **确实开着，已修**。上游把 fuzzy 叶子对**银行的记忆标签词表**解析后改为精确匹配（`engine/memory_engine.py:19488` → `engine/search/tag_resolution.py`，`MIN_SIMILARITY=0.45`、`MAX_VOCABULARY=5000`、`MAX_EXACT_COMBINATIONS=32`）。旧实现是拿叶子和**每个页面自己的标签**比相似度，三个可观测差异：精确命中被放宽到相近标签、**词表为空时近似词仍命中**、两个上限未生效。现为 `internal/storage/tag_resolution.go`（精确大小写不敏感命中即胜；否则同命名空间 trigram ≥0.45；`any`=并、`all`=交、`exact`=去重组合的或且先查上限）+ `internal/reporting/knowledge_tags.go` 经 `ListTags` 取词表，超 5000 用上游的 422 文案 |
| 9 | MCP 传输层 | **部分开着，已修**。上游会在 `Accept` 缺少 `text/event-stream` 时**改写**该头（`api/mcp.py:566-568`，注释明说是为了兼容不发 Accept 的客户端），因此契约是：缺 Accept / `application/json` / `*/*` **都放行**，只有"有 event-stream 而无 json"才 406。我们曾对 `application/json` 与 `*/*` 直接 406——**错拒了正确的客户端**。已实现通配符感知的判定、对齐 SDK 的错误信封（`id:null`、406、精确文案）与 GET 的 SSE 保活帧（`ping - <RFC3339>` + CRLF + `Cache-Control: no-cache, no-transform` + 回显 session） |
| 10 | `HINDSIGHT_API_STORE_DOCUMENT_TEXT=false` | **确实开着，已修**。① 上游存 **NULL** 而非空串（PG 现绑 NULL；内存后端保持 `''` 但读面统一收敛为 null，两后端观测一致）；② 拒绝文案丢掉了设置名，现与上游逐字相同（该句以句号结尾，与 ST1005 冲突，已在声明处标注理由并保留上游原文）；③ `internal/storage/direct_reference.go` 把可空的 `original_text` 扫进 `string`——**一个选择不存文本的库会让读取失败**，已 COALESCE |
| 11 | 非内置 tenant 扩展 | **范围外（刻意）**。上游可加载任意 Python 扩展类，Go 无法加载；选择非内置值**启动即失败并点名**，不是静默无认证 |
| 12 | Go 画像报告的函数级表格 | **有意偏离**。上游是 cProfile 的 `tottime/cumtime/ncalls` 表，Go 标准库无等价汇总；改为逐线程 CPU + 运行时摘要，函数级归因走 `HINDSIGHT_API_PPROF_ENABLED` 的 pprof 端点 |
| 13 | 嵌入契约文档与实现的偏差 | **文档侧已核**：`internal/httpx/openapi.json` 与上游 `hindsight-docs/static/openapi.json` **完全一致**（77 path / 99 操作 / 195 schema / `info` 逐字段相同，两方向差集皆空）。**运行时侧**：`scripts/contract_smoke.sh` 的 35 项线上事实全过（命令见 §4） |

**收口时新发现、不在原清单的一项（已修）**：**recall 的 fuzzy 标签解析**当时仍是旧的"逐行相似度"
读法（每个臂在 SQL 里对行内标签算相似度），而上游对 recall 用的是**同一套词表改写**
（`memory_engine.py:19488` → `15942` `_resolve_fuzzy_tag_groups`，与知识查询同一条路径）。可观测后果：
一个被标为 `name:alexandrio` 的记忆会被 fuzzy 词 `name:alexandria` 召回（相似度 ≥0.45），而上游"精确
命中即胜出"只会解析成 `name:alexandria`；两个上限也没有作用在改写后的过滤上。现已改为
`internal/storage/tag_resolution.go:ResolveRecallTagScope`：两个后端在**派发任何臂之前**解析一次
（`memory_recall.go:24`、`postgres_recall.go:35`），因此各臂一律按字面标签过滤，调用方无法通过换臂
得到两种行为；上限先行校验，超限用上游的两条 422 原文（逐字，含结尾句号）。旧的逐行 fuzzy 求值
（`fuzzyLeafMatch`/`anyTagSimilar`/`fuzzyExactMatch`/`checkTagScopeCaps` 与 `postgres_recall_tags.go`
的渲染器）已删除。tag-scope 的 story 覆盖不到这个方向，故补了先失败后通过的测试：
精确命中不得放宽（内存 + 真库各一）、空词表近似词不得命中、`all` 语义保持、两个上限的拒绝与文案保真。

## 4. 复核手段的可复现命令

```sh
# 契约文档：从参考服务或参考文档重新捕获（99 操作 / 195 schema）
python3 scripts/reference_openapi.py --source <url 或本地 openapi.json>

# 契约冒烟：起服务并逐项断言本轮修复的线上事实（35 项）
# 三个前提缺一不可：story 的探针要走真实 PostgreSQL，所以二进制必须带 -tags pgx
# （默认构建没有数据库驱动，启动即退出）；脚本自己不设数据库，靠调用者的环境；
# 迁移由服务自己跑。用一次性数据库，别指向要保留的库。
go build -tags pgx -o /tmp/wenyuan ./cmd/server
createdb wenyuan_smoke
HINDSIGHT_API_DATABASE_URL="postgresql://$USER@127.0.0.1:5432/wenyuan_smoke" \
HINDSIGHT_API_RUN_MIGRATIONS_ON_STARTUP=true \
  PORT=19500 bash scripts/contract_smoke.sh /tmp/wenyuan

# 全量验证门
make check        # build + test + vet + pgx 构建 + lint + gofmt
```

# Wenyuan 复刻缺口清单与实施计划（净室对照审计）

> 本文档由对上游 `../hindsight/hindsight-api-slim/hindsight_api/`（约 17 万行 Python，
> 330+ 模块）与 wenyuan 当前代码（约 3.5 万行 Go）的**逐模块对照审计**得出。
> 审计只覆盖**服务后端**；前端 / Control Plane（9999）、pip/npm 分发形态不在范围内。
>
> **对照日期：2026-10-14**。
>
> ⚠️ **对 `docs/GAPS.md` 的更正**：`docs/GAPS.md` §7 结论"无剩余必做项"经独立复核
> **不成立**。该文档把"路由已挂载 / 接口已返回"当作"功能已复刻"，但逐条比对上游
> 实现后发现：**路由面确实齐（99/99），接口的存在性也齐，但大量接口的请求/响应形状、
> 状态码、引擎内部行为不一致，且若干整块子系统从未接线**。本文第 1 节给出证据。
> 后续工作以本文为准；`docs/GAPS.md` 状态栏中"A1/A2/A3/A4/A5/B*"等文字凡与本文冲突，
> 以本文与代码为准。

---

## 0. 结论速览

| 类别 | 数量级 | 说明 |
|---|---|---|
| A 线上契约不一致（字段名/信封/状态码/头） | ~70 处 | 破坏"客户端不改代码即可对接"，**最高优先级** |
| B 引擎行为不一致（检索/写入/反思质量） | ~60 处 | 服务能跑，但排序、抽取、反思结果与上游差异大 |
| C 整块子系统缺失（从未接线） | ~12 块 | Webhook 投递、Worker HTTP、内嵌 Worker、准入、LLM trace、MCP 工具集、解析器、扩展框架、对象存储、Provider…… |
| D 运维/配置缺口 | ~20 处 | 指标/追踪/配置键/worker 调度 |

已确认**无缺口**的部分（不要动）：DB 对象面（表/列/索引/约束/函数/枚举，见
`docs/SCHEMA-ALIGNMENT.md` 全 0）；路由挂载面（99/99）；导出/导入归档解析与
`bankctl migrate` 主体。

---

## 1. 关键取证（本文核心论断的复现命令）

下面每条都是"GAPS.md 声称已完成、实际未闭合"的典型，均已在本机复现：

```sh
# 1) 向量检索实际不可达：retain 从不写 embedding，recall 查询向量从不在生产路径设置
grep -rn "QueryEmbedding" internal/ --include=*.go | grep -v _test   # 只有存储层自用，无调用者
sed -n '13,32p' internal/storage/postgres_recall.go                  # 单臂：vector-only 或 ILIKE

# 2) 独立 server 进程不消费队列、不跑维护（上游默认单机内嵌 worker）
grep -n "consumer\|maintenance\|worker" internal/app/entrypoint.go    # 无

# 3) llm_requests 只被清理、从不写入
grep -rn "INSERT INTO.*llm_requests" internal/                        # 无（退出码 1）

# 4) webhook 投递从不入队；worker 无 webhook_delivery 分支
grep -rn "webhook_delivery" internal/ --include=*.go | grep -v _test  # 只有 list/delete

# 5) X-Ignored-Params 头在真实服务里永远不会出现（WriteHeader 之后才 Set）
sed -n '30,42p' internal/httpx/middleware.go

# 6) 校验错误信封键是 errors，上游是 detail
sed -n '23,30p' internal/httpx/errors.go
```

---

## 2. A 类：线上契约不一致（最高优先级，破坏 drop-in 兼容）

> 均为"同一路由、同一方法，但 body/参数/状态码/头不同"。每项都应补齐
> `internal/app/parity_integration_test.go` 的端到端用例。

### A1. 中间件与通用契约

| ID | 缺口 | 证据 | 目标 |
|---|---|---|---|
| A1.1 | 422 校验信封键 `{"errors":[...]}` ≠ 上游 `{"detail":[{type,loc,msg,input,ctx}]}` | `internal/httpx/errors.go:27` | 键改 `detail`，补 `ctx`；`url` 上游不发（见 2026-10-15 记录） |
| A1.2 | `X-Ignored-Params` 在真实服务被丢弃（`Header().Set` 在 `WriteHeader` 之后）；分隔符用 `","` 而非 `", "`；不保序 | `httpx/middleware.go:24-40`、`httpx/queryparams.go:106` | 缓冲头直到 handler 返回；`, ` 分隔、保序 |
| A1.3 | 未知 body 字段上报仅少数 handler 接入 | `httpx/queryparams.go:69-97` | POST/PUT/PATCH JSON 路由默认接入 |
| A1.4 | 每路由"可接受查询参数"白名单只有 8 个全局项，合法参数被误报 | `httpx/api.go:180-190` | 按路由注册允许集合 |
| A1.5 | 逐响应模型 exclude-none 语义缺失（map/graph 行必须保留 null） | `httpx/json.go:34-51` | 加"保留 null"标志 |
| A1.6 | 客户端断开不映射 499；`contextWithCloseNotify` 是空实现 | `httpx/context.go:14-24` | 接 `Request.Context`，映射 499 |
| A1.7 | 准入控制缺失（无 lane/有界等待/503 Retry-After） | 无对应 | 见 C4 |

### A2. 路由族请求/响应形状

> 逐条列出；"上游"列给出上游证据行（`api/http.py` 简称 http）。

| ID | 路由 | 缺口 | 上游证据 |
|---|---|---|---|
| A2.1 | `/knowledge-base/tree` | 信封 `nodes`→`roots`；节点字段 `title`→`name`，缺 `mental_model_id/managed/description/is_stale/last_refresh_failed_at/trigger` | http:3484-3531 |
| A2.2 | `/knowledge-base/pages`(读/建) | 页面体 `{id,name,type,description,tags,timestamp,body,markdown}`；建页请求 `{name,source_query,tags,max_tokens,trigger}`→`{page_id,mental_model_id,operation_id}`；wenyuan 直写文本 | http:3533-3576,3594-3670,7362-7412 |
| A2.3 | `/knowledge-base/export` | 上游 `{files:[{path,content}]}`（markdown 包，`page_markdown.py`）；wenyuan `{bank_id,pages,total}` | http:7415-7455 |
| A2.4 | `/knowledge-base/search` | 上游 `results[]`（`{id,name,mental_model_id,source_query,updated_at,score}`）+ tags 过滤 + 混合排序；wenyuan `hits[]{page,score}` | http:7469-7490,3673-3697 |
| A2.5 | `/knowledge-base/nodes/{id}` PATCH/DELETE | 上游字段 `name,parent_id,source_query,tags,max_tokens,trigger`；DELETE 200+body | http:7520-7613 |
| A2.6 | `/mental-models`(list/get) | 信封 `{items,total,limit,offset}`；字段补 `trigger/last_memory_seen_at/last_refresh_failed_at/reflect_response/is_stale`；`detail=` 参数 | http:6929-7010,3409-3482 |
| A2.7 | `/mental-models`(create/delete/refresh) | create 200 `{mental_model_id,operation_id}`；delete 200 `{"status":"deleted"}`；refresh `{operation_id,status}` | http:3776-3781,7052-7131,7262-7299 |
| A2.8 | `/mental-models/{id}/dry-run-refresh` | 上游返回完整 pipeline 预览；wenyuan 返回单行 reason | http:7134 |
| A2.9 | `/mental-models/{id}/history` | 上游存"上一版内容"`previous_content/previous_reflect_response/changed_at`；wenyuan 存新版 | http:7021-7050 |
| A2.10 | `/directives`(list) | 信封 `{items,total,limit,offset}` + `tags/tags_match/active_only` | http:3148-3169,7629-7667 |
| A2.11 | `/directives`(create/delete) | create 200（wenyuan 201）；delete 200+body（wenyuan 204） | http:7699-7798 |
| A2.12 | `/webhooks`(完整族) | 响应含 `secret/http_config`；create 200；list 信封 `{items,total,limit,offset}`；delivery 列表 `{items,next_cursor}` 与记录字段；delete `DeleteResponse` | http:4882-4994,9768-9921 |
| A2.13 | `/operations` | 列表 `exclude_parents` + 默认 limit 20；单读键 `operation_id` + `child_operations/task_payload`；列表项 `items_count/document_id/filename/mental_model_id/details`；payload 参数 `include_payload` | http:8210-8290,4545-4809 |
| A2.14 | `/documents`(list/get) | list 参数 `tags/tags_match/time_field/start_date/end_date`；detail 补 `nodes_by_fact_type/observation_scopes/attachments`；chunk 补 `attachments` | http:7801-7855,2703-2916 |
| A2.15 | `DELETE /documents/{id}` | 上游 200 `{success,message,document_id,memory_units_deleted}`；wenyuan 204 | http:8167-8206 |
| A2.16 | `/tags` | 参数 `q/source/limit/offset` + 分页；wenyuan 忽略 | http:8009-8060 |
| A2.17 | `/memories/list` | 参数 `type,q,consolidation_state,state,document_id,entity_id,tags,tags_match,time_field,start_date,end_date,limit,offset` | http:5815-5905 |
| A2.18 | 记忆项形状 | 补 `date/entities(string)/proof_count/metadata/consolidated_at/.../state/invalidation_reason/invalidated_at/edited_at/updated_at/source_memory_ids` | http:2366-2432 |
| A2.19 | `PATCH /memories/{id}` | 上游接受 `text,context,occurred_start,occurred_end,fact_type,entities,resolve_entities,state,reason` | http:2777-2865 |
| A2.20 | `/entities`(list/detail) | list 参数 `tags/tags_match/tag_groups`；detail 填充真实 observations | http:6748-6893 |
| A2.21 | `/graph`、`/entities/graph` 负载 | 节点/边键 `source/target`（非 `from/to`）、`label/date/context/color/entityName/lineStyle/mentionCount/linkType/lastCooccurred`；`entities` 为字符串；行含 `created_at` | http:747-784,2191-2295 |
| A2.22 | recall 响应 | `source_facts` 值应为完整结果对象（非 `map[string]string`）；chunk 键 `chunk_index`（非 `index`）+ `attachments`；实体键 `entity_id`（非 `id`）；按 include 通道的 `max_tokens`/`truncated` | http:688-938,484-558 |
| A2.23 | recall trace 对象 | 补 `retrieval_results[]/rrf_merged[]/reranked[]/entry_points[]/visits[]/final_results[]` 与 query 内 `query_embedding/timestamp/tags_match/temporal_constraint` | `engine/search/trace.py:21-149` |
| A2.24 | reflect 请求/响应 | `done` 需 `memory_ids/mental_model_ids/observation_ids/document`；`based_on` 只报"被引用"而非全部证据；`include` 默认与顶层 `trace` 语义 | http:1459-1512；`engine/reflect/models.py:165-176` |
| A2.25 | `/stats` | 补 `links_by_fact_type/links_breakdown` | http:3001-3063 |
| A2.26 | `/stats/memories-timeseries` | 桶应为 `{time,world,experience,observation}` | http:3111-3145 |
| A2.27 | `/health/llm` | 形状 `{operation,ok,status,latency_ms}`；`enable_bank_llm_health=false` 时 404 | http:3066-3108,6678-6696 |
| A2.28 | `/memories/dry-run-extract` | 请求 `content,context,timestamp,agent_name,strategy,retain_mission,...`；响应 `DryRunExtractionResult{facts,chunks,usage}`；`enable_dry_run_extract` 404 门控 | http:2572-2618,5916-5979 |
| A2.29 | `/prompts/preview` | 请求 `{operation,strategy}`；响应 `{messages,strategy,strategies,run_settings,response_schema,skipped_reason}` | http:2433-2570,5980-6044 |
| A2.30 | `/consolidation-strategies/preview` | 请求 `{strategies,sample_limit}` 驱动 | http:2332-2340,9521-9554 |
| A2.31 | `/observations/scopes` | `ObservationScope{tags:[str],count}` | http:2296-2364 |
| A2.32 | `/health`、`/health/ready`、`/health/live` | 补 `database/db_acquire_ms`；状态串 `unhealthy`（非 `not_healthy`）；live 补 `version/uptime_seconds` | `engine/memory_engine.py:6186`、`liveness.py:20-53` |
| A2.33 | `/version`、`/v1/version` | `features` 需 11 键；`/v1/version` 用同一 `VersionResponse` | http:4828-4878,5703-5746 |
| A2.34 | `/banks/{id}/config` | 响应补 `bank_id`；create/update 请求接受履职配置键（retain_mission/…/enable_* 共 12+） | http:2166-2188,1990-2144 |
| A2.35 | `/v1/bank-template-schema` | 上游返回模板清单真实 JSON Schema | http:9450-9460 |
| A2.36 | retain 响应 | `usage` 对 async 为 null、`operation_id(s)` 仅 async；`usage` 为可空 | http:1388-1422 |
| A2.37 | `/files/retain` | 请求 `{parser,files_metadata:[...]}`；响应 `{operation_ids}`；`HINDSIGHT_API_ENABLE_FILE_UPLOAD_API` 门控 + 批量/大小上限 | http:10360-10525,1341-1441 |

---

## 3. B 类：引擎行为不一致

### B1. 写入（retain）

| ID | 缺口 | 证据 |
|---|---|---|
| B1.1 | **retain 从不生成 embedding**，`memory_units.embedding` 恒 NULL → 向量臂/ANN 无数据可排 | `postgres_retain.go:341`；`storage/models.go:360` |
| B1.2 | 无语义相似链接（embedding ANN + 阈值） | `engine/retain/link_creation.py:40-71` |
| B1.3 | 无时间邻近链接 | `link_creation.py:20-37` |
| B1.4 | 相邻链接被错误标记为 `semantic` | `retain_contents.go:170` |
| B1.5 | `update_mode` replace/append 是空操作：重写同 `document_id` 会累积重复 unit，无级联删除 | `postgres_retain.go:74` |
| B1.6 | 无 delta retain（changed-chunk 跳过），`content_hash` 写了不用 | `orchestrator.py:3653` |
| B1.7 | chunk id 用随机 UUID，非上游 `bank_doc_index` 可构造形式 | `chunk_ids.py:52-66`；`postgres_retain.go:114` |
| B1.8 | 分块策略单一固定 3000 字符，无结构化/JSON/JSONL/会话/图片分支与配置键 | `fact_extraction.py:567-834` |
| B1.9 | 抽取 schema 是子集：缺 `where/fact_kind/fact_type(world/assistant)/from_attachments` | `extractor.go:160-215` |
| B1.10 | `context`/`mentioned_at` 从不落库（recall 恒空/NULL） | `retain_contents.go:89-118` |
| B1.11 | `documents.retain_params` 只写 3 键，缺 context/event_date/observation_scopes/tags | `postgres_retain.go:75` |
| B1.12 | 无退化事实过滤、无输出溢出重试/形状修复、无时间推断 | `types.py:414-449`；`fact_extraction.py:319-367,89-126` |
| B1.13 | 实体标签体系（key:value 控制词表）缺失 | `entity_labels.py` |
| B1.14 | 附件多模态规范化/占位符（`⟦hs-att:…⟧`）缺失 | `attachment_content.py` |
| B1.15 | 实体名无 512 字符上限，超长名字会让整次 retain 失败 | `entity_processing.py:70-101` |
| B1.16 | `files/retain` 把原始字节当记忆文本（PDF/DOCX 变二进制噪声） | `file_retain.go:136` |
| B1.17 | 无 batch-retain 折叠 / streaming 折叠 / 字节预算（`fold.py`、`memory_budget.py`） | `fold.py:1-178` |
| B1.18 | 无内存防御筛查/脱敏（`memory_defense`） | `orchestrator.py:169,1624` |
| B1.19 | 无 retain mission / 自定义指令 / 多 strategy 分组 | `fact_extraction.py:1770`；http:10252-10290 |
| B1.20 | `observation_scopes` 只存不生效 | `postgres_retain.go:368` |
| B1.21 | 无 embedding 输入截断 / coalescing（依赖 B1.1） | `embedding_utils.py:56` |

### B2. 检索（recall）

| ID | 缺口 | 证据 |
|---|---|---|
| B2.1 | **Postgres 检索是单臂**：vector-only 或 `ILIKE`，无 BM25、无图、无时间臂、无融合 | `postgres_recall.go:13-72` |
| B2.2 | Postgres 路径丢弃 `Tags/TagGroups/Temporal/StrategyBoosts/Rerank/MinScores/PreferObservations` | `postgres_recall.go:13` |
| B2.3 | 查询文本**从不被向量化**（`QueryEmbedding` 无生产调用者）→ B2.1 的向量臂实际不可达 | 见 §1 取证 1 |
| B2.4 | 无 BM25 / `tsvector` 排序后端；keyword 臂是子串包含 | `search/bm25_term_selection.py`、`_pg_search.py` |
| B2.5 | 融合后最终分公式错：RRF 被乘入而非重排后剔除；stage-2 boost 被 RRF 分缩放（应绝对加） | `memory_recall.go:277-283` vs `reranking.py:174-300`、`recall_boost.py:226` |
| B2.6 | reranker 归一化总是 min-max（应 `[0,1]` 透传/logits sigmoid）；无 partial-timeout、无 passthrough 播种、无 provider failover/本地 MLX | `memory_recall.go:321`；`cross_encoder.py:77` |
| B2.7 | 图臂按词法播种（上游按余弦），无实体链接扩展，因果权重语义相反 | `memory_recall_arms.go:67-149` |
| B2.8 | 时间臂：proximity 二元（应窗口中点渐变）；缺 `temporal_score/temporal_proximity` 拆分 | `memory_recall.go:415-418` |
| B2.9 | 查询时间解析仅英文窄正则；无 dateparser、无中文、无 "in 2019"/拼写数字 | `query_time.go:90` |
| B2.10 | 模糊 tag 用子串（应 pg_trgm/Jaccard），缺 MAX_VOCABULARY/组合上限 422 | `memory_recall_tags.go:92-115` |
| B2.11 | 缺 `interleave_fusion`、按源预融合上限、rerank 前裁剪 | `fusion.py:8,112`、`recall_boost.py:195` |
| B2.12 | 无 recency 衰减函数配置 / 粗粒度日期规则 | `reranking.py:56-123` |
| B2.13 | 无每 (bank,fact_type) 向量索引健康/重建（`vector_index_health.py`、`repair-bank`） | `vector_index_health.py:225` |
| B2.14 | tag `resolve` 值用 `literal`（上游 `exact`）；min_scores 未下推到臂 | `tags.py:307` |
| B2.15 | 无 bank 级 `enable_text_search/enable_temporal_retrieval/enable_graph_retrieval/enable_reranking` 开关与 CE→rrf 降级 | http:2071-2094 |
| B2.16 | temporal_window 请求不校验 `start`/`end` 必填与顺序 | `response_models.py:316-343` |

### B3. 反思（reflect）/ 心智模型 / 巩固 / 知识库

| ID | 缺口 | 证据 |
|---|---|---|
| B3.1 | 工具 schema：`reason` 非必填；缺 `recall.max_chunk_tokens`、`read_mental_models.mental_model_ids/max_tokens`、`expand.depth` | `tools_schema.py:27-165` |
| B3.2 | 无强制层级检索序列（MM→observations→recall）与"新鲜页短路" | `agent.py:1220,1603` |
| B3.3 | 无证据护栏（首次 `done` 空证据应被拒） | `agent.py:1355` |
| B3.4 | 工具失败被吞成空结果（应与空 bank 可区分） | `agent.py:1590`；`loop.go:196-199` |
| B3.5 | 工具预算未 clamp（模型可请求 1e7 token） | `loop.go:203-214` vs `agent.py:1810` |
| B3.6 | 无 presentation/alias 层（重复计费）、无并行工具执行 | `presentation.py`；`agent.py:1450` |
| B3.7 | 无 directive 合规门 / 输出语言注入 | `tools_schema.py:330-386`；`prompts.py:60-119` |
| B3.8 | `max_tokens` 未做后置长度改写 | `agent.py:_rewrite_to_length_budget` |
| B3.9 | 心智模型 refresh：无 delta 模式、无 fact retraction、无 outcome/failure 上报、无 scope/window | `delta_ops.py`、`retractions.py`、`mental_model_refresh.py` |
| B3.10 | 巩固无语义去重、无精确重复守卫、无 tag scope 锁、无 proof_count/history/trend | `consolidator.py` |
| B3.11 | 知识库页面架构不同（内容应存 mental_models，缺 `managed/sort_order` 唯一名约束） | `alembic/…a9b8c7d6e5f4`；`postgres_knowledge.go:79` |
| B3.12 | prompt preview 是桩（从不渲染真实 prompt 块） | `prompt_preview.py`；`reporting/handlers.go:485` |

---

## 4. C 类：整块子系统缺失（从未接线）

| ID | 子系统 | 现状证据 | 目标 |
|---|---|---|---|
| C1 | **Webhook 投递管线** | 从不入队 `webhook_delivery`；worker 无该分支；无 HMAC 签名/重试表/`http_config`/投递时 SSRF 防护 | 入队 + 投递处理器 + `RETRY_DELAYS` + `X-Hindsight-Signature` + 事件枚举对齐 |
| C2 | **Worker HTTP 面** | `cmd/worker` 无监听；`/health /ready /live /metrics /`（8889）缺失；无 worker liveness 字段 | 复用 `internal/monitoring` 挂载 |
| C3 | **API 内嵌 Worker** | `app.Serve` 不启动 consumer/maintenance（上游单机默认内嵌） | `WORKER_ENABLED` 默认开时随 server 启动 |
| C4 | **准入控制 + 499** | 见 A1.6/A1.7 | 每操作 lane + 有界等待 + 503 Retry-After + 断开 499 |
| C5 | **LLM 请求追踪** | `llm_requests` 无 INSERT；`HINDSIGHT_API_LLM_TRACE_ENABLED` 清理空表 | provider 边界写行 |
| C6 | **MCP 工具集** | 9/38，缺 29（document/operation/mental-model/directive/knowledge-base CRUD 等） | 补齐 roster + 默认 bank/auth/filter |
| C7 | **文档解析器** | 无 markitdown/llama_parse/iris、无 `HINDSIGHT_API_FILE_PARSER` 链、无上传门控 | markitdown 等价 + 解析链 + 400/门控 |
| C8 | **扩展框架** | 仅编译期 Go 接口；无 loader/context、OPERATION_VALIDATOR、MEMORY_DEFENSE、MCP/MEMORIES 扩展、tenant 富化、`bank_tables` | 按需扩展点（至少 memory_defense + validator） |
| C9 | **对象存储** | 仅 S3（缺 prefix-delete/streaming/签名 URL）；无 GCS/Azure | 补 S3 能力；GCS/Azure 视需求 |
| C10 | **Provider 适配** | 缺 gemini 原生/litellm/bedrock/llamacpp/mock/openai-responses/volcano；embeddings 仅 OpenAI 兼容；`LLM_STRATEGY`/`LITELLMROUTER_CONFIG`/`EMBEDDINGS_LOCAL_*` 静默忽略 | 补可离线实现者；不可实现者显式报错而非静默 |
| C11 | **图维护队列** | 无 relink/entity-prune 队列与 drain job | 加队列 + 作业 |
| C12 | **管理 CLI** | `bankctl` 仅 rename/archive/migrate；`rename` 会**毁掉**目标数据（建空 bank 后删源）；无 backup/restore/repair-bank/export-bank/import-bank/worker-* | 修复 rename（原地改 `bank_id`）；补命令或明确不支持 |

---

## 5. D 类：运维与配置

| ID | 缺口 | 证据 |
|---|---|---|
| D1 | 指标面只有 2 个计数器；缺操作/LLM/DB 池/async backlog 指标 | `monitoring/metrics.go:1-60` |
| D2 | 无 OTel 追踪、无 `HINDSIGHT_API_OTEL_*`、无 GenAI span | `tracing.py` |
| D3 | 无 CPU profiling 配置键 | `profiling.py` |
| D4 | 无 loop-lag 探针 / stall watchdog / 多 worker metrics 标签 | `loop_lag.py`、`loop_watchdog.py`、`metrics_multiworker.py` |
| D5 | worker 配置键名/单位与上游不同，上游 env 被静默忽略 | `config/config.go:42-54` |
| D6 | 无每类型 slot 预留 / 每类型 wall ceiling / 进度续期 | `worker/poller.py:306-341,74-196` |
| D7 | store backpressure / append-conflict 未分类，会消耗重试预算 | `worker/backpressure.py` |
| D8 | 无跨 bank 公平 cursor / 启动态 stale-claim 预算对账 | `worker/poller.py:425-434,1414` |
| D9 | 无 DB 连接预算 / `db_url` 规范化（`?ssl=` 等） | `engine/db_budget.py`、`db_url.py` |
| D10 | 维护 retention 批间无 pause；provider rate-limit 未映射为不消耗预算的 defer | `maintenance.py:73-84` |
| D11 | 分层配置解析（tenant/bank 作用域）与缓存失效缺失 | `config_resolver.py` |

---

## 6. 实施计划（按阶段）

> 原则：**先修契约（A），再补"存在性断裂"（C 中使功能完全不可用者），最后对齐引擎质量（B）与运维（D）**。
> 每项都按 AGENTS.md 门槛验收：`make check`（build+test+vet+pgx+lint）+ 相关端到端用例；
> 新公开行为进 `internal/app/parity_integration_test.go`；DB 变化只能改上游库再 `python3 scripts/reference_schema_migrations.py` 重新生成。

### 阶段 0 — 止血（1 个迭代，最高价值、低风险）
1. **A1.1** 校验信封改 `detail` + `ctx`（`url` 上游不发，2026-10-15 修正）。
2. **A1.2** 修复 `X-Ignored-Params`（缓冲写头 + `", "` + 保序）。
3. **C3** server 单机启动 worker/maintenance（`WORKER_ENABLED` 语义）。
4. **C5** `llm_requests` 在 provider 边界写行，接通既有清理作业。
5. **B1.1/B2.3** retain 生成 embedding、recall 向量化查询文本（打通已存在但不可达的向量链路）。
6. **C12 rename 修复**：原地 `UPDATE … bank_id`（当前实现会丢数据，属数据破坏性缺陷）。

验收：新增端到端用例覆盖 1–6；`make test-pg` 证明向量臂对真实 PG 生效。

### 阶段 1 — 契约面收敛（2–3 个迭代）
- 完成 **A2.* 全部 37 项**的信封/字段/状态码/参数对齐，按路由族分批：
  知识库 → 心智模型/指令 → webhooks → operations/documents/tags/entities/graph →
  recall/reflect 响应 → stats/health/version/config/template-schema →
  dry-run-extract/prompts-preview/consolidation-preview/files-retain。
- 完成 **A1.3–A1.5**（body 未知字段、白名单、逐响应 null 保留）。
- 验收：`parity_integration_test.go` 用例覆盖每一族，逐字段断言。

### 阶段 2 — 引擎闭环（3–5 个迭代，价值最高、工作量最大）
- **B1.5/B1.6** `update_mode` replace/append + delta retain（正确性，非质量）。
- **B1.10/B1.11/B1.2/B1.3** context/mentioned_at/语义链接/时间链接落库。
- **B1.7/B1.8** chunk id 方案与分块策略对齐。
- **B2.1/B2.2/B2.4** Postgres 多臂 + BM25 + tags/temporal/boosts/reranker 贯通（把内存臂逻辑迁到 SQL 或复用 `scoreCandidates`）。
- **B2.5/B2.6** 融合后评分公式与 reranker 归一化对齐。
- **B2.9** 查询时间解析（至少补齐显式 period 与中文/常见英文形式）。
- ~~**B1.16/C7** 文件保留解析器（markitdown 等价）。~~ 完成（见阶段 3 的 C7 条目）。
- **B3.1–B3.5** reflect 工具 schema + 强制检索 + 证据护栏 + 错误传播 + 预算 clamp。
- **B3.9/B3.10** 心智模型 delta/retraction、巩固去重与作用域锁。
- **B1.9/B1.12/B1.13/B1.14** 抽取 schema 扩展、退化过滤、实体标签、附件占位符。
- 验收：新增对比用例（同一输入下召回集合/顺序、抽取事实数、`based_on` 语义）；bench 记录。

### 阶段 3 — 子系统补全（并行可拆）— **已完成**
- ~~**C1** Webhook 投递管线（含签名/重试/http_config/SSRF）。~~ 完成：投递+HMAC 签名+退避阶梯+http_config+
  SSRF 防护，并补上 `retain.completed` 事件与投递状态回写。
- ~~**C2** Worker HTTP 面 + liveness 字段（与 A2.32 一起）。~~ 完成。
- ~~**C4** 准入控制 + 499。~~ 完成。
- ~~**C6** MCP 工具补至 38。~~ 完成（39）。
- ~~**C11** 图维护队列。~~ 完成。
- ~~**C9/C10** 对象存储与 provider 补齐~~。完成：S3 prefix-delete 与流式读取；provider 补齐 volcano/mock，
  其余不可离线实现者**启动即显式报错**。**明确范围外**：GCS/Azure 后端、预签名下载 URL（上游亦无该路由）。
- ~~**C12** 其余管理 CLI~~。完成：rename 原地搬迁 + export/import/repair/worker-status +
  backup/restore/run-db-migration/decommission-worker(s)。
- ~~**C7** 文档解析器。~~ 完成：`internal/fileparse` 提供解析链（`convert`/`supports`/`name` 接口、按序回退、
  远端 4xx 视为不支持而继续）；本地 markitdown 等价解析器用标准库读取 PDF/DOCX/PPTX/XLSX/HTML/CSV/TSV/
  JSON/JSONL/文本，`llama_parse` 与 `iris` 走各自 HTTP API；`HINDSIGHT_API_FILE_PARSER` 链、
  `..._ALLOWLIST`、`..._CONVERSION_MAX_BATCH_SIZE(_MB)`、`..._DELETE_AFTER_RETAIN` 均已生效；
  上传门控（体/单文件/数量）与不可解析格式的显式 400 保留。OCR（`..._MARKITDOWN_OCR_*`）仍**明确范围外**，
  图片按附件处理。
- **C8** 部分：memory_defense 策略与操作校验器扩展点已落地；上游的**动态插件加载器**（`module:Class`
  字符串装载、跨语言扩展）在本 Go 构建中**明确范围外**，改为编译期注册点。

### 阶段 4 — 质量与非功能 — **已完成**
- **D9/D11** 本轮收口（见 §8）：DB URL 归一化与连接池预算；配置跨进程直读 + process→tenant→bank 分层。
- ~~**B2.11–B2.16** interleave fusion、按源/预 rerank 裁剪、vector index health、衰减配置、bank 开关、temporal 校验。~~ **阶段 2 已全部落地**（见 §8 各条日志）；
  剩余为**已记录的有意分歧/范围外**：B1.13 的 `map` 递归以外的上游细节、B1.17 的无损超长项切分（需存储协议变更）、
  B2.6 的 reranker failover 链与本地 MLX、B2.9 的 dateparser 依赖与中文数字、B3.10 的 `trend`（上游无消费者，判为 N/A）。
- **B3.6–B3.8/B3.11/B3.12** presentation/并行工具、合规门、长度改写、知识库架构、prompt preview 实渲染。
- **D1–D11** 指标/追踪/profiling/worker 调度/配置解析。

### 明确不做（写入 `docs/` 非目标，避免反复）
- 交互式认证类 provider（codex/cursor/claude-code/github-copilot/xai-oauth/nous）。
- 需要联网/本地进程的 provider（litellm router/bedrock/llamacpp）与 embeddings 本地模型，除非提供网关。
- GCS/Azure 对象存储（S3 兼容已够）。
- Control Plane UI / pip/npm 分发。

---

## 7. 验收与防回归

- 每个阶段结束：`make check` 全绿；新增/更新 `parity_integration_test.go` 用例。
- 契约类（A）：逐字段断言，反例是"上游有、我们无"。
- 引擎类（B）：同输入对比召回顺序/抽取结果/反思 `based_on`；用基准记录前后差异。
- 子系统的"接线"必须有真实运行证据（起服务/起 worker，观察入队→处理→产出），不接受"能编译"。
- `docs/GAPS.md` 的状态栏应随本计划推进逐条更新（把"✅"改为"见 PARITY-PLAN #ID"）。

## 8. 进度日志（实现记录）

### 2026-10-16 — 银行删除契约（`DELETE /v1/default/banks/{id}`）

- **契约行为修复**：`DeleteBank` 现在**真正清除银行内容**。记忆域没有指向 `banks` 的外键——
  `memory_units`/`documents`/`entities` 只相互引用，级联只覆盖 async 队列、attachments、aliases、
  directives、knowledge pages、mental models、webhooks——旧的 `DELETE FROM banks` 会把该银行
  全部记忆**孤立**下来并仍可读。两后端现在按上游顺序（documents → 无级联可达的行 → banks 行）
  在**同一事务**内清理，bank id 可立即复用。
- **响应契约对齐**：`deleted_count = memory_units + entities + documents`（旧值为「命中的银行行数」），
  `message` 对所有结果用**同一句**上游文案，包括**从未存在过的银行**（报 0，不报错）。
- 新增测试：`TestMemoryDeletePurgesContent`、`TestPostgresDeleteBankPurgesContent`（`pgx`，
  含邻行不受影响、三表零残留）、`TestPostgresDeleteNeverCreatedBankIsZero`、
  `TestDeleteBankRemovesContent`（HTTP 路由端到端）；均验证「去掉修复即失败」。
  `TestDeleteEnvelope` / `TestMemoryDeleteRemovesAliases` 的期望值随之从 1 改为 0。
- 根因发现于生产迁移：4 个银行首轮导入失败后 delete+重导，残留行未被清除，计数偏少。
- 验证：`make check` 全绿。

### 2026-10-15 — C7 文档解析链（`internal/fileparse`）

- 新增 `internal/fileparse`：解析器接口 `Convert(data, filename) (markdown, error)` /
  `Supports(filename, contentType)` / `Name()`，注册表按注册序自动探测，`ConvertWithFallback`
  按序回退（不支持/空结果/其它错误都继续，链尽时抛出最后一条——与上游 `convert_with_fallback` 一致）。
- 本地 `markitdown` 等价解析器，**零新依赖**（标准库 `archive/zip`、`encoding/xml`、`encoding/csv`、
  `encoding/json`、`compress/zlib`）：PDF 文本抽取（FlateDecode + 文本显示算子 + UTF-16BE 串解码）、
  DOCX/PPTX/XLSX（OOXML 部件）、HTML→markdown、CSV/TSV→表格、JSON/JSONL、文本/markdown。
  读不了的格式（旧版 .doc/.ppt/.xls、图片、音频）报 `UnsupportedFileTypeError`，交由链回退，
  全部失败才拒绝。
- 远端解析器 `llama_parse`、`iris` 走各自 HTTP API（上传/轮询/取结果；4xx 视为不支持、5xx 与网络错误视为硬失败）。
- `HINDSIGHT_API_FILE_PARSER`（链）、`..._ALLOWLIST`、`..._CONVERSION_MAX_BATCH_SIZE(_MB)`、
  `..._DELETE_AFTER_RETAIN` 经 `fileparse.FromEnv` 生效并在 `app.mount` 接线；配置侧改为 `Delegated`
  不再拒绝这些键。`FILE_DELETE_AFTER_RETAIN`（默认开）下，上传字节不做存储落盘（等价于上游"转换后删除原文"）；
  图片仍保留字节，因为其内容就是字节。
- 保留既有的上传门控（体/单文件/数量）与不可解析格式的 400（消息点名格式与文件）。
- 测试：`internal/fileparse/testdata` 提交了 PDF/DOCX/XLSX/PPTX/HTML/CSV 夹具（PDF 取自上游示例），
  断言各格式转出的 markdown；另有链回退、allowlist、批量上限、delete-after-retain 及远端解析器 httptest 用例。

### 2026-10-15 — 契约面逐项复核（以上游 OpenAPI 为基准）

11 个审计切片对 99 个操作逐项比对参数/请求体/响应字段/空值策略/状态码/头/校验信封，
发现并修复了大量字段级偏差（路由面此前已 99/99）。完整清单、每项证据与仍余项见
**`docs/CONTRACT-AUDIT.md`**；可复现事实由 `scripts/contract_smoke.sh`（35 项）与
`make check` 覆盖。实施方式：9 个并行修复 slice（mcp / mentalmodels+directives /
transfer+webhooks / reporting×2 / readsurface / storage / bankapi / memoryops）
+ 集成方直接完成的 httpx/config/monitoring 面（描述面、base path、版本与特性开关、
空值策略、404/405 信封、tenant 认证、请求体 422、准入语义、文档成员路由、指标名、画像）。


### 2026-10-14 — 独立复核及其修复（12 项，含 6 项本会话引入的缺陷）

对本会话全部改动（165 文件）做了一次独立正确性 + 安全复核，**逐项确认并修复**：

| # | 缺陷 | 触发 | 修复 |
|---|---|---|---|
| 1 | PG 召回臂参数位有空洞（`$3` 未被引用）且 `any_strict` 被渲染成**包含**而非**重叠** | PG 上 `tags:["a","b"], tags_match:"any_strict"` 只返回同时带两个标签的行；空洞参数让 PG 无法推断 `$3` 类型 | 参数改为**连续且全被引用**（`$1` bank、`$2` types、`$3/$4` 窗口），标签过滤全部内联为**转义字面量**；`any_strict` 改为 `&&`（与 memories/read-surface 谓词一致） |
| 2 | `observation_scopes` **从未写入 unit**，巩固作用域因此完全空转（本会话 B1.20 宣称"已生效"不成立） | 带 `observation_scopes:"per_tag"` 的写入仍然跨作用域合并 | 两条路径（fact/verbatim）都带上作用域，事实级作用域优先 |
| 3 | `fact_type:"assistant"` 在 `parseFacts` 被**提前压成 world**，`factTypeOrDefault` 的 assistant 分支与 kind 回退皆死代码 | 模型答 `assistant`，落库成 world | 保真透传到存储层再映射 |
| 4 | worker 在**defer 路径丢弃**投递上报的 `last_status_code`（本会话 C1 的"状态回写"对重试场景不成立） | 任何被 defer 的 webhook 投递 | 新增可选能力 `OperationRetryRecorder`，重试时合并上报元数据 |
| 5 | `CREATE INDEX CONCURRENTLY` 失败留下的 `indisvalid=false` 索引被当作**已存在**，且缩水/删除的 bank **永远走不到 DROP 分支** | 一次被取消的并发建索引；bank 行被清空或删除 | 无效索引视为不存在（先 drop 再重建）；候选集并入"持有我们前缀索引但已低于阈值"的 bank；`DeleteBank` 先拆索引 |
| 6 | 图维护 `DELETE … RETURNING` 批量取出后，**首个错误丢掉整批剩余** | 500 目标批次中途一次瞬时错误 | 失败项**重新入队**，其余继续处理，首个错误仍上报 |
| 7 | Anthropic 适配器只在有 response schema 时才发 tools，**reflection 在该 provider 下零检索** | `provider=anthropic` 调反思 | 无 schema 时转发调用方 tools/tool_choice，并解码所有 `tool_use` 块（结构化路径不变） |
| 8 | 强制层级检索**未校验模型是否真的调用了被钉住的那层**就前进 | 忽略/降级 `tool_choice` 的 provider | 仅当该层被真正调用才推进，否则下一轮重新钉住 |
| 9 | 时间分项 `temporal_score/temporal_proximity` **从未进入公开信封**（上游 RecallScores 只有 final/reranker/semantic/keyword） | 任何带窗口的召回 | 删除自造的字段与注释（该信号本就是 combined weight 的输入，非上报项）；保留内部计算 |
| 10 | fuzzy 标签叶子**忽略叶子 match 模式**且按整标签（含命名空间）算相似度 | `match:"all"`/`"exact"` 的 fuzzy 叶子 | 按上游 tag_resolution 规则逐模式改写（any 并集 / all 逐词 AND / exact 集合相等），相似度只比命名空间内去前缀值，两后端一致 |
| 11 | webhook 投递**可被重定向绕过 SSRF 防护**（且注册→投递解析可漂移） | 接收方回 307 指向内网地址 | 拒绝跟随重定向（记为投递失败并记录 3xx）；投递前复查目标；拨号时用 `Dialer.Control` **校验实际连接地址**，关闭解析漂移窗口 |
| 12 | 记忆防御在**银行配置读取失败时 fail-open**（该请求不被筛查即写入） | 配置读瞬时失败 | 写入路径改为**拒绝**（503 + Retry-After，与"未配置策略"可区分） |

- 另修复一项**既有**缺陷（复核附带发现，非本会话引入）：`CancelOperation`/`RetryOperation` 的 `RETURNING` 只返回 10 列而 `scanOperation` 读 15 列 → 在真库上必然报错。现将操作列**只写一次**为 `operationColumns`，
  六处语句共用，并加"列数=15 且首尾列固定"的不变式用例 + pgx 标签下的真库用例。
- **复核另附带发现并修复两处**（非本会话引入，但实际坏了公开行为）：
  - **`GET /banks/{bank}/attachments/{attachment_id}` 永远 404**：实现按 `banks/<bank>/<id>` 猜存储键，
    而保留的附件实际落在 `banks/<bank>/documents/<doc>/attachments/sha256-<hash>`（上游是按**附件行**
    取 `storage_key` 再读 blob）。新增 `storage.GetAttachment`（两后端，按 short id），路由改走该行，
    并以**声明时的 media type** 作答；既有 fixture 已改为种入附件行（原先的测试把错误契约钉住了）。
  - **字面量转义依赖服务器设置**：内联 SQL 字面量（compound tag 谓词、`CREATE INDEX … WHERE bank_id='…'`
    这类无法绑参的 DDL）仅靠引号翻倍转义，只在 `standard_conforming_strings=on` 时正确。现于**打开连接时
    校验该设置**，非 on 则拒绝启动（而不是按另一种转义规则静默出错）。
- **有意保留（上游同样如此）**：`memory_defense` 只筛查 `content`，不覆盖 `context`/`metadata`/实体名等
  兄弟字段——上游 `screen()` 同样只接收 content，故此为上游既有范围，非本实现的分歧；已在此记录，
  不擅自扩大筛查面。
- 加测试：上述每项都有失败先于修复的用例（含服务端端到端与 pgx 标签真库用例）。
- 验证：`make check` 全绿（0 issues）、`go test ./...` 31 包全绿、`go vet -tags pgx` 干净、实起服务 smoke（version/retain/`any_strict` 召回）通过。

### 2026-10-14 — 阶段 4（DB URL/连接预算 D9、配置分层与失效 D11）

- **D9** 两半均落地：
  - `storage.NormalizeDatabaseURL`：SQLAlchemy/asyncpg 形态 URL（`postgresql+asyncpg`/`postgres+asyncpg`/
    `postgres` → `postgresql`、`ssl=` → `sslmode=`）归一化，幂等，非 PG URL 原样返回；**在驱动打开前应用**，
    使从上游拷贝来的 URL 能直接连上而非因 scheme 报错。
  - 连接池预算 `PoolBudget{MaxOpenConns, MaxIdleConns}` 贯通 `BackendConfig` → 驱动注册 →
    `SetMaxOpenConns/SetMaxIdleConns`；配置键 `HINDSIGHT_API_DB_POOL_MAX_SIZE`(100，对齐上游)/`_MIN_SIZE`(5)；
    server/worker、bankctl、schematool（小预算）均已接线。无上限的池会一直扩到数据库拒连，把负载尖峰
    变成对该库所有客户端的故障。
- **D11** 配置**跨进程失效**：新增 `storage.BankConfig`，**绕过进程内银行行缓存**直读配置列；recall 的
  禁用臂、retain 的抽取配置与 memory_defense 策略改走该路径——上游正是每请求重解析以保证多机一致。
  - **分层解析**：`config.ResolveLayers(bank, tenant, process)` 实现 **process → tenant → bank**
    （逐键、后者胜、缺层不参与、未定义的键保持"未设置"而不代填默认值）；tenant 覆盖经
    `tenant.Context.Overrides`/`Tenant.Overrides` 提供，请求路径由可选能力
    `httpx.TenantOverrideSource` + `TenantOverridesMiddleware` 承载（未实现该能力的认证器行为不变），
    memoryops 的 recall 禁用臂 / retain 抽取配置 / memory_defense 策略改走分层解析。
- 验证：`make check` 全绿；新增用例覆盖 URL 归一化（含幂等与非 PG 输入）、预算透传到驱动、配置读取
  的副本语义与"不被缓存遮蔽"。

### 2026-10-14 — 阶段 3（子系统收口：C1/C7/C8/C9/C10/C12）

- **C1** webhook 管线补完：新增 **`retain.completed`** 事件（写入落库后触发，载荷含
  `document_id`/`items_count`/`unit_ids`/`operation_id`）；投递把接收方状态写入 operation 的
  `result_metadata.last_status_code`（传输失败写 `null`，与"对端拒绝"可分）；内存后端改为**从 operation
  派生投递记录**（此前是一个永远为空的切片，与 PG 语义不一致），删除该死字段。
- **C7** 上传门控：新增进程级上限 `HINDSIGHT_API_RETAIN_ATTACHMENT_MAX_SIZE_MB`(20) 与
  `..._MAX_COUNT`(50)——请求体在解析前用 `MaxBytesReader` 封顶、单文件先查声明大小再按 limit 截读
  （谎报长度也越不过）、文件数超限即拒；三者都是 400 且消息点明具体限制。解析链本身（markitdown 等价）
  仍为**范围外**：无法离线实现的格式继续显式 400 提示先转文本（不再当二进制噪声存库）。
- **C8** 扩展点：新增**操作校验器扩展**（`httpx.OperationValidator` + 中间件 + 请求上下文里的
  tag scope；`retain`/`file retain`/`recall` 在边界被校验，拒绝按其状态码与理由返回且操作不执行；
  tag scope 只能收窄不能放宽；未注册校验器时行为逐字节不变）；memory_defense 策略已在上阶段落地。
- **C9** 流式下载：新增 `StoredFileStreamer` 可选能力（`OpenStoredFile`），S3 返回**活的响应体**而不再
  全量 `ReadAll`（用例以字节计数证明返回时零字节已传输），两处字节服务路由优先流式、未知尺寸时省略
  `Content-Length`、头部与缓冲路径逐项一致；无该能力的后端保持原路径。**GCS/Azure 与预签名 URL 明确不做**
  （上游亦无预签名路由）。
- **C10** 不可实现者**显式报错**：`HINDSIGHT_API_LLM_STRATEGY`/`..._LLM_LITELLMROUTER_CONFIG`/
  `..._EMBEDDINGS_LOCAL_MODEL` 一旦配置且本构建无法实现，**启动即失败**并点名该键（此前静默忽略）。
- **C12** 管理 CLI：新增 `backup`/`restore`（zip + 每表 JSON，表集合**从迁移文件派生**并做拓扑排序，
  恢复前先整体校验再按逆序 TRUNCATE；内存后端明确拒绝）、`run-db-migration`、
  `decommission-worker`/`decommission-workers`（走既有 claim release 路径）。
- 端到端验收（**真实起服务与 CLI**）：`bankctl backup` 在内存后端给出明确拒绝、`run-db-migration`
  成功、`decommission-workers` 成功；设置不支持的 provider 键**启动即失败**并点名；2MB 上传在 1MB
  限制下 400（消息含 "2MB … exceeding the 1MB limit"）、小文件 200；注册本地接收方后 `retain.completed`
  真实投递（接收方收到完整载荷），投递记录写到 `status_code=200`，此前不可达接收方记录
  `status_code=null` + 连接错误。
- 验证：`make check` 全绿（PG 运行时语义待 CI `make test-pg`）。

### 2026-10-14 — 阶段 2（并行冲刺：B1.8/B1.10/B1.12/B1.14–B1.19/B2.7–B2.10/B3.2）

> 本批以并行子代理推进互不重叠的文件切片，父级负责接口约定、文档与集成验证。

- **B1.10**（PG）`UpsertUnit` 补齐 `context`/`mentioned_at`/`event_date`/`occurred_end` 与真实
  `metadata`（此前硬编码 `{}`），两后端字段一致；新增 pgx 标签用例（离线跳过）。
- **B1.15** 实体名上限 `EntityNameMaxLength=512`（rune 边界截断）。*注*：上游对超长名是**丢弃**
  mention，此处按任务要求**截断保留**，属**有意分歧**，已在代码注释与本文记录。
- **B1.12** 抽取：输出溢出（`length` 结束原因或 `IsOutputTooLong`）改**二分重试**（深度 3、下限 500 字符、
  因果索引重定基）；新增**时间推断** `InferPeriod`（`in 2015`、`in March 2026`、相对短语），
  粗日期一律给出周期跨度且与 `storage.SpansCalendarPeriod` 自洽。
- **B1.19** 抽取提示词支持 `Options{Mission, Instructions, Strategy, ReferenceTime}`，各自独立标注块，
  未配置时提示词逐字节不变；`PreviewMessages` 同步。
- **B1.8** 分块可配：`storage.SegmentOptions{ChunkSize, StructuredChunkSize}` + `SegmentWith`，
  `Segment` 成为默认包装；`RetainRequest.ChunkSize/StructuredChunkSize` 经两个保留路径贯通到
  chunk 写入与 verbatim 分段。
- **B1.14** 附件占位符 `⟦hs-att:<short_id>⟧`（内容寻址，跨次保留稳定）。
- **B1.16** 文件保留：无法解析的 PDF/DOCX/PPT/XLS 等**拒绝**（按扩展名与 content-type），
  提示先转文本；文本类仍照常，图片走占位符——不再把二进制当记忆文本。
- **B1.17** 保留前**折叠**小项与**字节预算**（2 KiB/项、32 KiB/请求）；超预算项按 rune 截断并在
  metadata 记录 `extraction_truncated`/`extraction_original_bytes`，不做静默改写。
- **B1.18** 记忆防御（**已按上游更正**）：首版误做成"注入筛查 + 自造响应字段 `defense_findings`"，
  已整体替换为上游语义——银行配置 `memory_defense` 策略（`enabled` + `rules[{on,action}]`，
  action ∈ allow/redact/block，非法规则 422），**抽取前**逐项跑 `sensitive_data` 检测器
  （无依赖正则词表，含密钥/PII/私钥/JWT/连接串与**带 Luhn 校验**的卡号），命中即
  `[REDACTED:<type>]` 就地改写或丢弃该项；非 allow 决策入队
  `memory_defense.triggered` webhook（载荷含 document id、detector、matched types 与**指纹化**
  hits，绝不含原文）。默认关闭。自造响应字段已删除。
- **B2.7** 图臂三项：种子取**分数最强的 20 个**（两后端一致，无向量时回落词法）；新增
  **实体共现扩展**（`tanh(shared*0.5)`，无显式链接也可达）；跳权重改**可加**（因果+时序=1.5 而非取最大）；
  顺带修正 PG 图臂的参数位错位（$6 种子、$7 limit）。
- **B2.8/B2.6/B2.10/B2.9** 由并行切片推进（时间邻近渐变与 `temporal_score` 拆分、reranker 部分失败/
  passthrough 种子、词表与组合上限 422、中文与英文拼写数字时间解析）。
- **B3.2** 强制层级检索与"新鲜页短路"由并行切片推进。
- **附**：tag 组**契约修正**——`tag_groups` 为**列表**（非 `{"groups":…}`），约束为空的节点
  （无 tags 且无 and/or/not）现以 422 拒绝（此前会被解码成"不过滤"而**放宽**结果）；
  组内叶子 `match` 缺省为上游的 **`any_strict`**（排除未打标签项），扁平字段缺省仍为 `any`；
  内存与 PG 两侧逐条对齐。
- 父级新增端到端契约用例：召回经服务端同时校验 tag 组收窄、拒绝空约束节点、recency 曲线贯通。
- **B1.13** 实体标签词表（受控 `key:value`）：新增无依赖 `internal/entitylabels`
  （五种组类型 value/multi-values/text/multi-text/map 递归、legacy `free_values`/`multi_value` 迁移、
  非法定义报错），抽取 schema/prompt 仅在配置时注入（未配置**逐字节不变**），抽出的标签经既有实体解析
  存为 `key:value` 实体，`tag:true` 组同时落到 unit tags；银行配置写入时校验（非法 422）。
- **B3.9（收尾）** 刷新**结果上报**：worker 新增可选 `ReportOutcome(ctx, metadata)` 通道，
  consumer 把处理器上报的元数据写入 operation 的 `result_metadata`（新可选能力
  `OperationResultRecorder`，两后端实现且**合并**而非覆盖）；心智模型刷新上报
  `outcome`（content_written/content_unchanged/refresh_failed_error）、`failure_reason`
  与读取**窗口**（`created_after`/`created_before`）。不新增公开 HTTP 字段——operations
  端点本就返回 `result_metadata`。
- **附** PG 侧补齐 tag 词表读取，使词表/组合上限在两后端一致生效。
- 端到端验收（**真实起服务**，非仅测试）：`/v1/version` 正常；retain 200；`tag_groups` 召回 200 且
  非法组 422；prompt preview 渲染真实 consolidation prompt；银行配置 `entity_labels`/
  `retain_chunk_size`/`retain_mission` 接受 200，非法词表 422、零分块 422；文件保留拒绝 PDF
  （400，提示先转文本）而接受文本（200）；配置 `memory_defense` 后新写入的 GitHub token 落库为
  `[REDACTED:github_token]`。
- 验证：`make check` 全绿（PG 运行时语义待 CI `make test-pg`）。

### 2026-10-14 — 阶段 2（recency 可配 / 融合与下推 / bank 开关 / tag 解析 / PG TagGroups）

- **B2.12** recency 衰减可配：新增 `storage.RecencyDecayConfig` 与 `RecencyDecay`/`RecencyForUnit`/
  `SpansCalendarPeriod`；**粗粒度日期**（整月/整年）从**周期末端**起算并封顶于中性——否则
  "2026 峰会"存成 2026-01-01 会读作过期。配置键 `HINDSIGHT_API_RECENCY_DECAY_FUNCTION`
  （linear|exponential|none）+ `..._LINEAR_WINDOW_DAYS` + `..._HALFLIFE_DAYS`，经 app→memoryops→
  `RecallQuery.Recency` 贯通；未识别名回落默认（非法不启动失败）。
- **B2.2**（Postgres 路径）**compound TagGroups 原本被静默忽略**：现渲染为 SQL 条件
  （and/or/not 递归；flat 模式 any/all/any_strict/all_strict/exact 与内存后端逐条对齐；
  fuzzy 叶子走 pg_trgm `similarity`），叶子值经 `sqlTextLiteral` 转义（单引号翻倍），
  且**不占用参数位**（不扰动各臂既有 $N）。
- **B2.14** ① tag `resolve` 取值改为上游的 **`exact`**（原 `literal`）；② 每臂 **min_scores 下推**：
  `prepareArm` 在融合**前**按该臂 floor 过滤再截断，失败 floor 的命中不再占用候选预算。
- **B2.15** `enable_reranking=false` 现真正生效：作为 `ArmReranker` 进入 `Bank.RecallDisabledArms`，
  recall 在合并 bank 配置后**不挂 cross-encoder**（降级为融合序，而非拒绝回答）。
- **B1.10**（Postgres）`UpsertUnit` 此前漏写 `context`/`mentioned_at`/`event_date`/`occurred_end`
  且 `metadata` 硬编码 `{}`；现全部落库，两后端字段一致。
- **B1.15** 实体名上限 `EntityNameMaxLength=512`（rune 边界截断），超长名不再能让整次 retain 失败。
  *注*：上游对超长名是**丢弃**该 mention，此处按任务要求**截断保留**，属已记录的有意分歧。
- 加测试：recency 三种曲线/未来日期钳制/日历周期识别与粗日期取端封顶、tag 谓词渲染与转义、
  受影响 arm 的 floor 与截断顺序、bank reranker 开关、PG 字段往返与超长实体名（含 pgx 标签用例）。
- **B3.10 trend 一项判定为 N/A**：上游 `engine/reflect/observations.py` 的 `Trend`/`compute_trend`
  在**整个上游仓库（含测试）无任何调用者**，实现它等于发明无人消费的面。
- 验证：`make check` 全绿（PG 运行时语义待 CI `make test-pg`；新增 pgx 用例离线跳过）。

### 2026-10-14 — 阶段 2（interleave 融合与按源预裁 B2.11）

- **B2.11** 召回融合补齐：
  - `RecallQuery.Fusion`（空/`rrf` 默认求和、`interleave` 轮转）与 `PerSourceCap`（融合前按臂截断，
    防止单臂挤掉其它臂）。`interleaveFusion` 从已记录的 per-arm rank 还原轮转序（各臂第 1 名、
    再各臂第 2 名…），未入任何臂的候选仍保留在末尾；interleave 下 `Final` 按位置严格递减，
    下游排序即复现该序。
  - **真实调用方**：巩固的去重候选扩池——`Store.RelatedObservations`（可选实现），
    由 `consolidationStore` 以 **interleave 融合 + 按源截断** 逐条 fact 文本召回 observation
    （`Types=[observation]`、`Budget=low`、去重、失败不致命——丢一个合并候选远好于整趟失败）。
  - 配置：`HINDSIGHT_API_RECALL_MAX_CANDIDATES_PER_SOURCE`（默认 0 关闭）。
- 加测试：轮转序、未排名候选保留、按源截断、去重扩池（相关 observation 恰好出现一次、池内不重复）。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（事实撤回 B3.9 剩余）

- **B3.9** 新增 **retraction（撤回）** 检测与消费：
  - 刷新时把 grounded 证据写入 `reflect_response.based_on.memories`（`{id,type,text}`）。
  - 纯函数层：`CitedGrounding`（**白名单** memory_units 事实类型 world/experience/observation，
    绝不用排除法，避免把兄弟文档/指令误判为缺失）、`RetractionOf`（原因不区分：失效/删除/重摄取
    在文档视角是同一件事）、`Unresolvable`（**全部**引用都消失属于断链而非撤回，不得据此让模型撤言）。
  - `IsStale` 在**水印判定后**追加撤回判定——撤回是"行消失"，不抬高水印，原先永远不会触发刷新。
  - 新增 `LiveMemoryIDs`（两后端）单查询判定存活。
  - delta 合并提示词追加 `## RETRACTED EVIDENCE`（引用**记录下来的原文**，因为原行可能已硬删除）。
- 加测试：类型过滤、部分撤回/断链、`IsStale` 因撤回而 stale 且证据回填后恢复、无 grounded 不 stale、
  retracted section 渲染与跳过。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（观察历史 B3.10 剩余）

- **B3.10** 补齐 observation history：`UpdateObservation` 在改写前把**旧文本**快照进
  `observation_history`（PG 单行 JSONB `content`），每观察保留最近
  `ObservationHistoryMaxEntries`(=20) 条（超限裁剪，常被强化的结论不会无限增长）；文本未变则不记。
  内存后端同样记录（`memoryHistory`，Reason=`consolidated`）。
  `GET /memories/{id}/history`（`MemoryHistory`）现把该观察的 `observation_history` 与
  `audit_log` 的更新记录**合并**并按时间倒序返回（此前只看 audit，巩固改写从不出现）。
  `proof_count` 已在上轮核对为实现（`linkObservationSources` 与证据并集同步）。
- 加测试：改写记录旧文本、未变改写不记录且证据仍增长。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（并行工具执行 B3.6 剩余）

- **B3.6** 完成：一轮内模型请求的多个检索是相互独立的，现**并行执行**（有界，最多 4 个并发，
  避免单请求打开过多事务）；结果仍按模型请求的**顺序**记录，后续轮次上下文不依赖谁先返回；
  done 不参与并发；任一失败仍使整轮失败（不静默当作"无结果"）。
- 加测试：并发屏障证明真并行（顺序执行会死锁）+ 结果顺序保持；兄弟调用成功时失败仍使整轮失败。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（长度改写 B3.8）

- **B3.8** reflect 回答落地 `max_tokens` 后置改写：`max_tokens` 是**可见答案**的目标而非
  provider 上限（思考模型的硬上限会被 reasoning token 吃掉并截断答案），故答案生成后按
  `storage.CountTokens` 判定，超预算则再发一次改写调用（system 保持要点与结构、只回改写文本）；
  改写 usage 一并计入；**空改写保留原文**，不把完整答案清空。
- 加测试：超预算改写且计费累计、未超预算不额外调用、空改写保留原文。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（prompt preview 实渲染 B3.12）

- **B3.12** prompt preview 不再返回空桩：三个操作各自调用**真实 prompt builder** 渲染
  （`extraction.PreviewMessages`、`consolidation.PreviewMessages`、`reflect.PreviewMessages`），
  运行期数据用带书名号的占位符（`«…»`）而非调用方输入——预览回答的是"这个 bank 发什么"。
  retain 预览带出 response schema；reflect 预览带 directives 段；`strategies` 报告
  retain/consolidation/reflect；未知操作返回 400（不再编造 prompt）。
- 加测试：三操作均渲染出 system+user 且非空、strategies/schema、未知操作 400。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（合规门与输出语言 B3.7）

- **B3.7** 两半均落地：
  - **directive 合规门**：reflect 开场插入 `## DIRECTIVES (MANDATORY)` 段（"NEVER violate…"、
    "Do NOT explain…"），并在**回答调用的最后**追加编号 `## REMINDER: MANDATORY DIRECTIVES`
    （"Your response will be REJECTED if it violates…"），使后段轮次无法压制规则；规则去重/排序稳定渲染，
    无 directive 时两段均不出现。answer prompt 改为按短别名引用（配合 presentation 层）。
  - **输出语言注入**：新增 `WithOutputLanguage` provider 包裹层，把
    `HINDSIGHT_API_LLM_OUTPUT_LANGUAGE`（此前只解析从不使用）作为指令附加到 system 消息（无 system 则插入），
    覆盖 retain 抽取 / 巩固 / reflect / 心智模型全部产物。
- 加测试：system 追加/插入/未设置不包裹、directive 段与提醒位置及排序、无 directive 不输出。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（reflect presentation 层 B3.6 部分）

- **B3.6(部分)** reflect 新增 **presentation/alias 层**（`internal/reflect/presentation.go`）：
  - 证据按短别名呈现（fact→`f1`、observation→`o1`、perspective→`p1`、chunk→`c1`），
    完整 UUID 不再每轮重发；chunk 来源也以别名标注。
  - 模型回写的别名在**回答文本**与**工具入参**（`ids`/`mental_models_ids`）中解析回真实 id，
    别名绝不外泄给调用方。
  - 回答中模型**从未见过**的 UUID 替换为 `[unverified id]`（伪造/损坏引用），已见 id 原样保留。
- 加测试：prompt 用别名且不含原始 id、回答别名解析、未验证 UUID 洗白、工具入参别名解析、
  别名前缀与复用。
- 剩余：并行工具执行。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（抽取字段生效 B1.9 剩余）

- **B1.9** 抽取字段真正生效：
  - `fact_type`：`assistant` **与** `experience` 均映射到存储 `experience`，`world` 保持；未识别的值
    回退到 `fact_kind == "assistant"` 再默认 world（对齐上游 2242-2252）。schema enum 扩为
    world/assistant/experience，prompt 补 assistant 语义。
  - `from_attachments`：`ExtractedFact.FromAttachments` 新增，按「附件序号（从 1）」或
    「附件标识」解析到 unit 的 `AttachmentIDs`，与 item 自身的附件并集去重；越界/未知项丢弃。
  - prompt 补 `from_attachments` 说明。
- 加测试：fact_type 映射表、附件解析（序号/标识/混合/越界/未知）、单元附件并集。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（向量索引健康 B2.13）

- **B2.13** 新增按 **(bank, fact_type)** 的分区局部向量索引治理：
  - 索引名 `idx_mu_emb_{worl|expr|obsv}_{bank16}`（对齐上游命名与 63 字符上限）。
  - `PlanBankVectorIndexes`：阈值 0 → 不动（索引随 bank 生命周期）；≥ 阈值建、< 阈值拆。
  - DDL 一律 **CONCURRENTLY**（`CREATE INDEX CONCURRENTLY IF NOT EXISTS ... WHERE fact_type/bank_id`、
    `DROP INDEX CONCURRENTLY IF EXISTS`），bank_id 字面量转义；`vector_extension` 决定访问方法。
  - PG 实现 `VectorIndexReconciler`（可选能力接口，内存后端不实现 → 跳过）：状态/候选银行/对账。
  - 配置：`HINDSIGHT_API_VECTOR_INDEX_MIN_ROWS`（默认 0）、`..._RECONCILE_INTERVAL_SECONDS`（默认 900）；
    维护作业 `vector_index.reconcile`；CLI `bankctl bank repair-bank --vector-index-min-rows N`。
- 加测试：命名/规划/DDL（含并发、转义、vchord）、作业门控与内存后端 no-op。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 4（OpenTelemetry 追踪 D2）

- **D2** 新增 `internal/oteltrace`：**无依赖**的 OTLP/HTTP JSON 导出器（信封小、批次发送、
  10s 超时、失败只记录不重试）、**有界队列 + 后台 worker**（满则丢弃并计数，不阻塞模型调用）、
  幂等 `Close` 冲刷。
- provider 包裹层 `TraceSpans` 为每次 completion 发一条 **GenAI span**
  （`gen_ai.system`/`gen_ai.request.max_tokens`/`temperature`/`gen_ai.response.model`/
  `gen_ai.usage.*`/`wenyuan.scope`），错误置 error 状态。
- 配置键对齐上游：`HINDSIGHT_API_OTEL_TRACES_ENABLED`（默认关）、
  `..._OTEL_EXPORTER_OTLP_ENDPOINT`、`..._HEADERS`、`..._SERVICE_NAME`、
  `..._DEPLOYMENT_ENVIRONMENT`；启用但无 endpoint 视作关闭。`Serve` 退出时 `App.Close()`
  冲刷。
- 加测试：导出载荷/属性类型、端点补 `/v1/traces`、队列丢弃、采集端失败、配置解析、
  provider 端到端 span。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 1 结构残留（观察作用域 / chunk 附件 / 重复页名）

- **A2.14** 文档详情：`observation_scopes` 现取 **retain 时配置的 spec**（模式名或 tag 组，保持
  调用方形状，`any`）；`attachments` 复用统一渲染器。顺带修复 PG `GetDocument` 不返回
  `retain_params`/`tags`/`updated_at` 的漏读。
- **A2.14** chunk：`ChunkResponse.attachments` 新增；`ChunkAttachments`（两后端）按 unit 的
  `attachment_ids` 归集到 chunk，list/get 两处填充。
- **B3.11** 页面名唯一：同 folder 内重名 page 创建返回 **409**（上游文案），`asHTTPError` 现
  透传既有 `*httpx.HTTPError`。
- 加测试：chunk 附件、详情 scopes、重名 409。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（巩固作用域锁）

- **B3.10(部分)** 巩固 pass 现以**银行级序列化键** `consolidate:<bank>` 入队（`EnqueueConsolidation`
  与 reconciler 两处），复用既有 serialization-key 互斥：同 bank 的两个 pass 不会并发（否则会读到
  同一批未巩固事实并产生孪生 observation），不同 bank 仍可并行。加测试。
- 剩余：proof_count/history/trend。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 4（按类型预留槽位 D6）

- **D6** worker 新增**按类型预留槽位**：`ClaimNextOperationForTypes`（两后端+接口），consumer
  在某预留类型在飞数低于下限时优先领取该类型，否则领取任意工作。配置
  `HINDSIGHT_API_WORKER_CONSOLIDATION_RESERVED_SLOTS`（默认 2，参照上游）。
  加测试（预留类型优先于更旧的非预留工作）。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（巩固近似去重）

- **B3.10(部分)** 巩固 CREATE 守卫扩展为**近似去重**：规范化文本与既有/本趟已建 observation
  的 **token Jaccard ≥ 0.7** 即视为同一结论而跳过（此前只挡精确重复）。加测试。
- 剩余：作用域锁（并发 pass 串行化）、proof_count/history/trend。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 3（webhook 重试排程）

- **C1(部分)** worker `DeferAt(delay, err)`：可携带显式重试延迟；webhook 投递用上游退避阶梯
  `[5s,5m,30m,2h,5h]`（按 `retry_count` 取档，饱和于末档），transport/5xx/429 走该排程，
  4xx 永久。加测试。
- 剩余：delivery 记录 `last_status_code` 回写、其它事件源。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 3（对象存储 S3 能力）

- **C9(部分)** S3 后端新增 `DeletePrefix`（ListObjectsV2 分页 → 逐个 DELETE），并在
  `PostgresStore.DeleteBank` 时回收该 bank 的对象（经可选 `prefixDeleter` 能力，失败不阻断
  行删除）。加测试（fake S3 支持 list/delete）。
- 剩余：GCS/Azure（有意范围外）、streaming retrieve、预签名下载 URL。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 3（provider 补齐）

- **C10(部分)** 新增 `volcano`（OpenAI 兼容厂商端点）；新增 `mock` provider
  （确定性：complete 回显最后一条 user、embed 由文本摘要生成稳定向量），可离线驱动模型路径。
  加测试。
- 剩余：gemini 原生 / litellm / bedrock / llamacpp / openai-responses；本地 embeddings；
  OAuth 类（有意范围外）。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 4（rate-limit defer / 维护批间 pause）

- **D10** worker 新增 `Defer`：handler 可标记「可延迟」失败（provider 限流经
  `provider.IsRateLimitReset` 映射），consumer 对其延迟重排、不消耗重试预算。retention 分批之间
  加 250ms pause（PG `PurgeExpiredRows`），避免长 sweep 独占锁。加测试。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 4（跨 bank 公平调度）

- **D8** claim 改为 bank-公平：先服务「该 bank 最早已排队工作」最老的候选（PG 用相关子查询
  `MIN(pending.created_at)` 排序；memory 同规则），使一个 bank 的积压不饿死其他 bank，
  行内仍最老优先。加测试（A1,B1,A2 交错）。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（抽取形状修复）

- **B1.9(部分)** 抽取输出不可解析时做**一次自纠正重试**：把上一条回复原样回传并附「只返回
  `{"facts":[...]}`」的修复指令，成功则继续，仍失败才报错。加测试。
- 剩余：输出溢出按 chunk 重试切分、chunks/verbatim 模式、组合文本 `what|when|where|who|why`、
  时间推断。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 4（指标面）

- **D1(部分)** `/metrics` 增加 async 积压 gauge：`wenyuan_async_operations_pending`/
  `_failed`（scrape 时按 bank 汇总 `BankStats`，2s 超时，慢库不阻塞 scrape）。加测试。
- 剩余：操作/LLM/DB 池细粒度指标、多 worker 标签合并。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 4（per-type wall ceiling）

- **D6(部分)** worker 每操作类型 wall ceiling：`HINDSIGHT_API_WORKER_{RETAIN,CONSOLIDATION,REFLECT}_WALL_TIMEOUT_SECONDS`；
  任务预算取 `min(lease, ceiling)`，卡死任务在 ceiling 释放 slot 并重试，而非等到 lease。加测试。
- 剩余：每类型 slot 预留（`WORKER_<TYPE>_RESERVED_SLOTS`）、共享池。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 4（loop-lag 探针）

- **D4** `monitoring.LoopLagProbe`：按 `HINDSIGHT_API_LOOP_LAG_REPORT_SECONDS`（默认 5s）采样，
  对超 `HINDSIGHT_API_LOOP_LAG_STALL_SECONDS`（默认 1s）的调度延迟记 `event loop lag` 日志；
  server 随请求循环启动；过迟的一次 tick 会重同步而非爆发补跑。加测试。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（心智模型 delta 刷新）

- **B3.9(部分)** 心智模型刷新支持 `delta` 模式：当 `trigger.mode=delta`、已有正文且源查询未变时，
  以「保留未变段落、删除过时、追加新证据」的合并提示让模型产出更新文档；失败或无模型时回落到
  完整答案并记 `delta_fallback`。history 记录 `reason=delta|delta_fallback`。加测试。
- 剩余：fact retraction（被引用事实失效后剔除陈旧句）、outcome/failure_reason 上报、scope/window。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 4（worker backpressure 分类）

- **D7** consumer 对 store 背压错误（`ErrWritesUnavailable`/`ErrReadShed`）改为**延迟重排**：
  `ScheduleOperationRetry` 传原 `retry_count`（不递增）后释放 claim，持续 shedding 不会把工作
  推向永久失败。加测试。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 4（worker 配置别名 / profiling）

- **D5** worker 配置接受上游键名别名：`HINDSIGHT_API_WORKER_POLL_INTERVAL_MS`（毫秒）、
  `HINDSIGHT_API_WORKER_MAX_SLOTS`、`HINDSIGHT_API_WORKER_TASK_RETRY_BACKOFF_SECONDS`，
  在本项目自有键未设时生效。加测试。
- **D3** `HINDSIGHT_API_PROFILE=true` 时挂载 `/debug/pprof/*`（默认关闭）。加测试。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 1 结构残留（knowledge managed）

- 页面内容本就存于 `mental_models`（`knowledgeSelect` 联表），补 `managed`/`sort_order`：
  storage `KnowledgeNode` 增字段并在 PG 投影/写入贯通；tree 节点 wire 暴露 `managed`；
  API 创建的页面标 `managed=true`。
- 剩余：页面名唯一约束（schema 已有）、文档详情 `observation_scopes`、chunk 侧 attachments。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 3（图维护队列）

- **C11** 新增图/实体维护队列消费：storage 增 `DrainGraphMaintenance`/`RelinkUnit`/
  `DrainEntityMaintenance`/`PruneEntity`（memory 无队列为空操作、PG 用 `DELETE ... RETURNING`
  认领并按 embedding 重建语义边、按 `unit_entities` 存在性剪除实体）；maintenance 增
  `graph.maintenance` 作业（复用 reconcile 间隔）。加测试。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 3（bankctl 管理命令）

- **C12(部分)** `bankctl` 增 `export-bank`/`import-bank`（复用归档导出/导入）、
  `repair-bank [--bank ID]`（重新调度搁浅的 consolidation，
  `RecoverConsolidations`，幂等）、`worker-status`（按 bank 汇总 pending/failed 操作）。
  加测试。仍未实现：`backup`/`restore`/`run-db-migration`/`decommission-worker(s)`
  （备份/恢复需二进制 COPY 全库；decommission 需跨 worker 的 worker_id 视图）。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 3（准入控制 + 499）

- **C4** `httpx.AdmissionMiddleware`：按路由后缀为 recall/retain/reflect 设并发 lane；
  有界等待（`HINDSIGHT_API_ADMISSION_MAX_WAIT_SECONDS`，默认 5s），超时 → `503` + `Retry-After`；
  等待期间客户端断开 → `499`。规则数由 `HINDSIGHT_API_ADMISSION_{RECALL,RETAIN,REFLECT}_LIMIT`
  （默认 8）控制。接入 router 链。`DisconnectCancelMiddleware` 简化为直通（Go 的
  `Request.Context` 已在断开时取消）。加测试。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 3（MCP 工具补齐）

- **C6** MCP 工具roster 由 9 扩到 39：新增 bank 生命周期、memories/documents 维护、
  operations、tags/stats、mental model get/delete/refresh、directive create/delete、
  knowledge tree/page/search、sync_retain、entities、consolidate、observation scopes、
  webhook list/create/delete。全部映射到 storage 调用（`internal/mcp/tools_more.go`）。加测试。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 3（worker HTTP / webhook 投递）

- **C2** worker 进程健康面：`HINDSIGHT_API_WORKER_HTTP_PORT`（默认 8889）上挂
  `/health` `/health/ready` `/health/live`；`monitoring.WorkerHandler` 的 liveness 载荷含
  `worker_id`/`is_shutdown`/`seconds_since_last_poll`。加测试。
- **C1(部分)** webhook 投递管线：`consolidation.completed` 事件为订阅的 webhook 入队
  `webhook_delivery` 操作；worker `Process` 新增该分支，投递时读取注册、POST 并带
  `X-Hindsight-Signature`/`X-Hub-Signature-256`（HMAC-SHA256 over `ts.body`）；4xx 视为永久失败，
  5xx/传输失败重试；已删除 webhook 为永久 no-op。加测试。
- 待办：投递的重试排程常量、delivery 记录 `last_status_code` 回写、其它事件源。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2 起步

- **B1.5** `update_mode` 生效：`replace` 重建文档的 units/chunks/links，`append` 拼接既有正文
  （memory/postgres 双实现）。
- **B1.7** chunk id 改上游注入式 `bank_doc_index`（转义 `_`/`~`，`BuildChunkID`），
  两后端一致；保留未含分隔符时的朴素形式。
- **B1.10** retain 落库 `context` 与 `mentioned_at`（此前恒空/NULL）。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（PG 多臂检索）

- **B2.1/B2.2/B2.4** `PostgresStore.Recall` 从单臂改写为多臂：语义（pgvector 余弦）+ BM25
  关键词（`ts_rank_cd` over `search_vector`）+ 图扩展（`memory_links`），共用
  `scoreCandidates` 做 RRF 融合、boost、reranker、`filterFloors`/`preferObservations`/cap；
  并贯通 tags（any/all/exact）、temporal 窗口、disabled arms 与空查询列举。
  移除已死的 `vectorRecall`/`embeddingProviderAvailable`。
- 待办：PG 侧 side channel（entities/chunks/source_facts）与时间臂扩散未接线（note）。
- 验证：`make check` 全绿（PG 运行时待 CI `make test-pg`）。

### 2026-10-14 — 阶段 2（语义/时间链接）

- **B1.2/B1.3/B1.4** retain 现在生成真实链接：embedding 相似（余弦 ≥ `SemanticLinkMinSimilarity`
  0.75）的 unit 之间建 `semantic` 边（memory 内存计算 + PG `<=>` SQL，per-unit fan-out 5）；
  同 item 相邻 unit 的边从误标的 `semantic` 改为 `temporal`；新增 `cosineBetween`。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（评分对齐 + reflect 工具/护栏）

- **B2.5/B2.6** `scoreCandidates` 对齐上游：reranker 运行时以归一化 CE 分为基并把 RRF
  从最终分剔除；无 reranker 时基为 `fused × bestArm`（bestArm=0 时取中性 1）；
  stage-2 策略提升改为**绝对**加到最终权重（不再乘 fused）。`normalizeRerankScore`
  改为：批内已在 [0,1] 直接透传，疑似 logits（[-50,50]）走 sigmoid，否则 min-max。
- **B3.1** reflect 工具 schema：每个工具 `required` 含 `reason`；`recall` 增
  `max_chunk_tokens`；`read_mental_models` 用 `mental_model_ids`（`ids` 兼容别名）与
  `max_tokens`；`expand` 增 `depth` 枚举。
- **B3.3/B3.4/B3.5** reflect 循环：证据为空时的首次 `done` 被忽略（不能空证据作答）；
  工具执行失败改为让整次反思失败（不再静默成空结果）；模型给的 token 预算 clamp 到
  [1000,16000]（`max_results` 1–50）。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（抽取 schema / 退化过滤 / retain_params）

- **B1.9** 抽取 schema 与提示扩展：`where`/`fact_kind`/`fact_type`(world|experience)/
  `from_attachments`；`storage.ExtractedFact` 增 `Where`/`Kind`/`Type`；unit 的 `fact_type`
  按抽取结果落库（experience/world），`where`/`fact_kind` 存入 metadata。
- **B1.12(部分)** 退化事实过滤：仅含 <2 个字母/数字的“事实”（纯标点等）被丢弃。
- **B1.11** `documents.retain_params` 补 `context`/`event_date`/`observation_scopes`/`metadata`
  （`retainParamsFor`，两后端共用）。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（分块策略 / fuzzy tag）

- **B1.8** `Segment` 增加结构化分支：JSON 会话数组按“整轮”分块、JSONL 按“整条记录”分块
  （整体贪心打包到字符预算，单条过大才递归切分），不再把一条消息/记录从中间切断。
- **B2.10** 模糊 tag 匹配由子串包含改为 **trigram Jaccard ≥ 0.45**，且命名空间必须一致
  （`user:alice` 不匹配 `run:alice`）。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（巩固去重守卫）

- **B3.10(部分)** 巩固精确重复守卫：CREATE 文本规范化后与既有/本趟已建 observation 相同则
  跳过；UPDATE 只重述自身文本则跳过。`Result` 增 `Skipped`。加测试。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 2（delta retain / bank 开关 / temporal 校验）

- **B1.6** delta retain：re-retain 同一 `document_id` 且内容哈希不变（且未 `force_reextract`、
  非 replace/append）→ 跳过重抽取，返回既有 unit；memory/postgres 双实现；`RetainItem` 增
  `ForceReextract`（wire `force_reextract`）。
- **B2.15** 每 bank 召回臂开关：`banks.config` 读写贯通（memory/postgres），config API 接受
  `enable_text_search`/`enable_temporal_retrieval`/`enable_graph_retrieval`/`enable_reranking`
  （默认 true），recall 时合并为 `DisabledArms`。
- **B2.16** `temporal_window` 校验：start/end 均必填且需 `end >= start`。
- **B2.14** tag `resolve` 值为 `literal` 时 `exact` 亦等同字面（既有行为），已确认。
- 验证：`make check` 全绿。

## 8.5 早期阶段记录

### 2026-10-15 — 校验条目词汇表修订（A1.1 修正）

上游的校验条目**不带** `url`：参考的 FastAPI 校验处理器用
`exc.errors(include_url=False)` 构造条目（`.venv` 内 `fastapi/_compat/v2.py:187`），
契约文档 `openapi.json` 的 `ValidationError` 也只声明
`loc`/`msg`/`type`/`input`/`ctx`（required 仅前三者）。此前按 pydantic 文档补的
`url` 与 2.13 版本号是错的，已移除。同批修正：

- 类型/措辞按上游词汇表：`list_type` = "Input should be a valid array"、
  `dict_type` = "Input should be an object"、`*_parsing` 补上「unable to parse …」、
  非字符串送入标量走 `int_type`/`float_type`/`bool_type`（分数走 `int_from_float`）。
- 空 body 归为 `missing`（参考对空体也报 `missing`/`loc:["body"]`/`input:null`），
  校验条目不再被 null 丢弃规则裁剪（`WriteValidationFailure` 固定保留键）。
- `BodyDecodeFailure` 的 `input` 不再丢：recall/reflect 的 `decodeJSON` 返回读到的
  body 字节（`input` 缺失只影响这两条路由；reporting 的 `decodeBody` 仍未回传 body）。

证据：`internal/httpx/validation_served_test.go`（served router 表驱动）、
`internal/app/parity_integration_test.go:TestParityServedValidationItemShape`。

### 2026-10-14 — 阶段 0 完成

- **A1.1** 校验信封改 `detail` + `ctx`/`url`（`internal/httpx/errors.go`，`errors_test.go`）。
- **A1.2** `X-Ignored-Params` 在提交状态时注入（原实现写在 `WriteHeader` 之后被丢弃）；
  名称按出现顺序、`", "` 连接、去重；body 未知字段进入同一 collector
  （`internal/httpx/{middleware,queryparams}.go`，`middleware_test.go`）。
- **C3** `HINDSIGHT_API_WORKER_ENABLED`（默认 true）在 server 进程内启动 consumer +
  maintenance（`internal/app/background.go`、`config.go`、`entrypoint.go`）。
- **C5** provider 边界写 `llm_requests`（`internal/app/llm_trace.go`、
  `storage.RecordLLMRequest`），`/llm-requests` 不再读空表。
- **B1.1/B2.3** retain 写路径与 recall 查询文本走 embedding（`storage.EmbeddingGenerator`
  + `internal/app/embeddings.go`），打通此前不可达的向量臂。
- **C12** `bankctl rename` 改为 `storage.RenameBank` 原地搬迁，拒绝占用目标
  （`memory_rename.go`、`postgres_rename.go`、`bankadmin/rename.go`）。
- 验证：`make check` 全绿；新增测试覆盖上述每一项。

### 2026-10-14 — 阶段 1 部分

- **A1.3/A1.4** 未知 body 字段经 `decodeJSON` 反射上报（recall/reflect）；接受查询参数
  集合扩展，消除合法参数被误报（`internal/httpx/api.go`、`internal/memoryops/operations.go`）。
  其余 JSON 路由的 body 字段覆盖见待办。
- **A2.32/A2.33** readiness 体补 `database`/`db_acquire_ms`、状态串 `unhealthy`；
  liveness 体补 `version`/`uptime_seconds`；`/version` 与 `/v1/version` 报 11 个
  feature 键（`internal/monitoring/health.go`、`internal/reporting/knowledge.go`、
  `internal/app/reporting.go`）。
- **A2.27（部分）** `/health/llm` 受 `HINDSIGHT_API_ENABLE_BANK_LLM_HEALTH` 门控（默认关→404）；
  文件上传受 `HINDSIGHT_API_ENABLE_FILE_UPLOAD_API` 门控（默认开）。
- **A2.34（部分）** bank config 读/写/重置响应补 `bank_id`。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 1 部分（续）

- **A2.5/A2.7/A2.11/A2.12(部分)/A2.15** 删除路由改为 200+body：mental model / directive /
  knowledge node 返回 `{"status":"deleted"}`；webhook 返回 `DeleteResponse`；document delete
  返回 `{success,message,document_id,memory_units_deleted}`，`storage.DeleteDocument`
  改为返回级联计数。
- **A2.10/A2.11** directive 创建 200；列表 `{items,total,limit,offset}` + `active_only`/
  `tags`/`tags_match` 过滤与分页。
- **A2.22** recall 侧信道字段名对齐：`chunk_index`、`entity_id`；`source_facts` 值为完整结果
  对象（`storage.SourceFactMap` 改持 `RecallMatch`）。
- **A2.12(部分)** webhook 创建 200、默认订阅 `consolidation.completed`、列表信封
  `{items,total,limit,offset}`+分页；事件枚举对齐为 `consolidation.completed`/
  `retain.completed`/`memory_defense.triggered`。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 1 部分（续 2）

- **A2.6/A2.7(部分)** mental model：Model 暴露 `trigger`/`last_memory_seen_at`/
  `last_refresh_failed_at`/`is_stale`/`reflect_response`；list 改 `{items,total,limit,offset}`
  + `tags`/`tags_match`/`detail`/分页（默认 100）；get/list 支持 `detail=metadata|content|full`
  （list 默认 metadata，get 默认 full）；create 改 `200 {mental_model_id, operation_id}`
  并入队刷新操作。
- **A2.1(部分)** knowledge tree 信封改 `{roots:[...]}`；节点字段 `title`→`name`，新增
  `mental_model_id`/`description`/`timestamp`。
- **A2.2–A2.4(部分)** knowledge page 读为 `{id,name,type,tags,timestamp,body,markdown}`；
  页面创建请求接受 `name`（`title` 兼容别名），返回 `200 {page_id,mental_model_id}`；
  目录创建返回 KnowledgeNode 201；export 为 `{files:[{path,content}]}`（markdown+frontmatter）；
  search 为 `{results:[{id,name,mental_model_id,updated_at,score}],total}`。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 1 部分（续 3）

- **A2.13** operations：单读加 `operation_id` 字段与 `include_payload` 参数（`include=payload`
  兼容）；列项补 `items_count`/`document_id`/`filename`/`mental_model_id`；列表默认 limit 20。
- **A2.14/A2.16/A2.20/A2.21** 读面：documents 列表支持 `tags`/`tags_match`/`time_field`/
  `start_date`/`end_date`（含存储层时间窗与 tag 模式）；文档详情补 `nodes_by_fact_type`
  （新存储方法 `DocumentFactCounts`）与 `attachments`；tags 支持 `q` 通配与分页；实体详情
  填真实 observations（新存储方法 `EntityMentions`）；memory graph 节点/边改用上游键名
  （`label`/`date`/`context`/`entities` 字符串、`source`/`target`/`linkType`/`weight`/
  `entityName`/`lineStyle`），表行补 `created_at`；实体图节点补 `mentionCount`。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 1 部分（续 4）

- **A2.25/A2.26** `/stats` 补 `links_by_fact_type`/`links_breakdown`（按源 unit 的 fact_type
  归组，存储层新增计算）；timeseries 桶补 `world`/`experience`/`observation` 分类型计数。
- **A2.27** `/health/llm` 改为真实探测：`{operation, ok, status, latency_ms}`，操作集
  retain/consolidation/reflect，状态 connected/not_configured/auth_failed/unreachable/timeout；
  经 `reporting.LLMProber` 边界接入 provider。
- **A2.35** `/v1/bank-template-schema` 返回真实 manifest JSON Schema（`$defs` +
  `properties` + `required`）。
- **A2.36** retain 响应 `usage` 对 async 写为 null，仅 sync 写给出计数。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 1 部分（续 5）

- **A2.17/A2.18** `/memories/list` 支持 `q`/`document_id`/`entity_id`/`tags`/`tags_match`/
  `state`（别名 `consolidation_state`）/`time_field`/`start_date`/`end_date`；记忆项补
  `date`/`proof_count`/`consolidated_at`/`consolidation_failed_at`/`state`/`updated_at`/
  `edited_at`/`source_memory_ids`（存储层 `MemoryUnit` 增补时间戳列，PG 查询与扫描同步）。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 1 部分（续 6）

- **A2.37** `/files/retain` 响应改 `{operation_ids}`；接受可选 `request` 表单字段
  `{parser, files_metadata}`，parser 走允许表（未知 → 422），per-file
  `document_id/context/metadata/tags/strategy/timestamp` 覆盖到条目。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 1 部分（续 7）

- **A2.28** `/memories/dry-run-extract` 请求接受 `content`(与 `text` 别名)/`context`/`strategy`，
  响应改 `{facts, chunks, usage}`（接入抽取器与分块；无抽取器时 facts 为空）。
- **A2.29** `/prompts/preview` 请求接受 `{operation, strategy}`，响应改
  `{messages, strategy, strategies, run_settings, response_schema, skipped_reason}`
  （prompt 渲染器未接入时以 `skipped_reason` 说明）。
- **A2.31** 观测作用域项改 `{tags:[...], count}`。
- 验证：`make check` 全绿。

### 2026-10-14 — 阶段 1 收尾

- **A1.3** 未知 body 字段上报扩展到 bankapi/directives/mentalmodels/readsurface/reporting/
  webhooks 的 `decodeBody`（统一反射取允许字段）。
- **A1.5** 新增 `httpx.WriteJSONOpenRow`，graph/实体图等开放行响应在 `DROP_NULL_FIELDS=true`
  下仍保留 null。
- **A2.12(余)** webhook 响应/请求补 `http_config`（存储补 `WebhookPatch.HTTPConfig` 与两后端应用）。
- **A2.22(余)** recall 侧信道按通道 `max_tokens` 截断：chunk 标记 `truncated`，source_facts
  设 `source_facts_truncated` 并支持 per-observation 上限。
- **A2.30** `consolidation-strategies/preview` 请求驱动：按 `strategies`/`sample_limit`
  返回每个策略匹配到的现存 scope 采样与计数。
- 验证：`make check` 全绿。

### 待办（阶段 1 余项）

- knowledge 页面内容架构：页面正文应存于 mental_models（当前双写），缺 `managed`/`sort_order`
  与页面名唯一约束；文档详情 `observation_scopes`；chunk 侧 `attachments`。
- 这些依赖存储/迁移变更，归入阶段 2/3 一并处理。

## 9. 复核方法（下次审计用）

见第 1 节命令。补充：

```sh
# 上游路由（多行正则）
python3 - <<'EOF'
import re
s=open('../hindsight/hindsight-api-slim/hindsight_api/api/http.py').read()
print(len(set((m.group(1).upper(),m.group(2)) for m in
  re.finditer(r'@app\.(get|post|put|delete|patch)\(\s*\n?\s*"([^"]+)"', s, re.M))))
EOF
# 只读对比：按族抽查 upstream 的 pydantic 模型字段 vs wenyuan 的 json tag
```
## 10. 参考配置键面：剩余 59 个拒绝键（2026-10-15 三次复核）

`internal/config/reference.go` 的 `referenceRuntimeKeys` 是参考实现运行时环境键的清单，共 243 键
（其中 231 键已解析或委派、12 键仍**拒绝启动**）。拒绝本身是有意策略（"宁可失败，也不静默 no-op"），但对
"直接平替"的部署方而言，把参考 `.env` 整份搬过来就会启动失败——这是本轮复核出的最大剩余面。

四路只读审计（按域切分：召回/写入/反思/巩固、数据库与 PG、worker/指标/进程形状、MCP/文件/扩展/webhook）
给出逐键分类与 Go 侧证据。分类口径：

- **WIRING**：行为已存在，键只是没接到位（加 `Resolve:` + 把值传进既有代码路径）。
- **SMALL**：需要一小段真实实现（几十行量级）。
- **ABSENT / 结构**：Go 侧没有对应机制，或纯 Go 二进制无法实现。

### 已闭合（本轮批次，按批次记）

**第一轮（进程/引擎）**：4 个召回臂开关、retain 分块/上限/mission/指令、巩固 mission/轮数上限/源事实预算/策略/作用域上限/LLM 批与并行、
反思 mission 与源事实预算、默认下限（graph seed、语义链接、每臂 floor、BM25 词数）、固定召回预算档、反思迭代与上下文、
两个历史开关与条数、每通道准入等待、DB statement_timeout / max_parallel_workers / session setup、worker 每类型槽位保留、
事件循环看门狗、dry-run 开关、MCP 三键、实体候选门与上界、抽取补全上限、子批预算、link expansion 上限/超时、银行行缓存上界、
`WORKERS` / `DAEMON_LOG`。

**第二轮**：`DISPOSITION_*`（列表行改报解析值）、`ANN_{ITERATIVE_SCAN,MAX_SCAN_TUPLES}`、`MIGRATION_DATABASE_URL`、
`KNOWLEDGE_PAGE_DEFAULT_TRIGGER`。

**第三轮（并行）**：全局 webhook 四键、指标四键（backlog/bank-id/tenant/lag 直方图）、扩展透传头、
`DEFAULT_BANK_TEMPLATE`（存储层建行钩子，覆盖所有建行路径）、读副本三键、pg_search 两键。

**第四轮（并行）**：`DATABASE_BACKEND`；召回入口三键（query 截断、引擎级并发 permits、BM25 词项选择）+
内部召回三键；反思补全上限与默认 options 两键；写入/实体三键（因果链开关、银行默认策略、实体第二道合并门）；
worker/巩固八键（背压推迟、巩固按银行分级认领、巩固批次/重试/预算/补全、统计缓存上界）。

### 仍拒绝的 12 键（第六轮后的现状）

**需要新的外部能力**

- `FILE_STORAGE_EXTENSION`：Python 类加载式的外部存储后端；本仓库只有内置的 S3/GCS/Azure，没有可加载的 Go 后端注册。
- `REFLECT_PROMPT_CACHE_ENABLED`：上游的增量提示缓存是"provider 能力 + 每次运行的 cache session 创建/回收"（`supports_incremental_prompt_cache()`，只有其 Gemini provider 实现），本实现的 provider 边界没有该能力，开关无处可挂；本轮已把另一件真实差异补上（见下"默认发出的提示缓存标记"）。
- `QUERY_ANALYZER_LANGUAGES`：上游限制 dateparser 的语言集；本实现是自写时间解析，没有对应开关。

**本实现的流水线里没有可生效的位置**

- `RETAIN_{MAX_CONCURRENT,SUBBATCH_CONCURRENCY,STORE_MAX_CONCURRENT,MEMORY_BUDGET_MB,BATCH_DOCUMENT_WRITES}`（5）：写入各阶段的并发/内存上界与分批落盘；本实现是顺序流水线（每个文档一次写入），这些上界没有可绑定的位置。
- `RETAIN_BATCH_POLL_INTERVAL_SECONDS`：LLM Batch API 轮询间隔（无 Batch API 客户端）。
- `RETAIN_ENTITY_RESOLUTION_BATCH_SIZE`：本实现每次只查一个名字的候选，没有批量查询可调。
- `FAIL_ON_EXTRACTION_ERRORS`：参考用它把**批处理**抽取错误升级为失败；本实现的流式路径本来就让抽取失败失败整个操作（上游 `test_62` 钉的就是这条），因此无处可绑。

**`database/sql` 的限制**

- `DB_COMMAND_TIMEOUT`：客户端逐命令超时。忠实实现必须在语句的 `*sql.Rows` 关闭时取消，而 `DB`/`Tx` 返回 `database/sql` 的 opaque 行类型；唯一不改接口的绕法要改 ~287 个调用点。

### 本轮额外补上的"非键"行为

- **提示缓存标记**：Anthropic 的 system 块带 `cache_control: {"type":"ephemeral"}`（工具循环里最后一轮也带），OpenAI 兼容侧按 host 发 `prompt_cache_key`（首条消息的指纹）/`x-grok-conv-id`/`x-opencode-session`，全部按上游默认，运营商配置的头与 extra body 优先。
- **`RETAIN_BATCH_ENABLED` 语义修正**（见 `docs/FUNCTIONAL-GAPS.md` §2.3）。

### 运行配置包测试的注意事项（2026-10-16）

`go test ./internal/config/` 目前会挂在另一个编辑者留下的临时探针文件 `zz_inventory_probe_test.go` 上（它给**任何**以 `_PROVIDER` 结尾的键都回 `openai`，于是参考实现本身就无上限的索引成员扫描永不终止）。绕开它即可跑完整套件，这是键表契约的真实验证：

```sh
go test -count=1 -skip TestDumpKeyInventory ./internal/config/   # 243 行键表 + provider 家族契约全绿
go test -count=1 -skip TestDumpKeyInventory ./...                # 全模块
```

该套件在 2026-10-16 抓出三处真缺陷（均已修）：`DB_POOL_SLOW_ACQUIRE_THRESHOLD_SECONDS` 的参考默认是**小数秒** 0.05（按整数解析会退回 0）、`SKIP_LLM_VERIFICATION` 的探针值选到非布尔字串导致"解析键未改变配置"、以及该键加入 `resolvedProviderKeys` 后在家族契约里缺一个可证明的取值。

### 验收方式（每个键落地的标准）

1. 该键设为非默认值后，`FromEnv(...)` 解析出的值随之改变（`reference_test.go` 的表驱动契约测试已强制
   这一点：解析键必须在探针值下改变解析结果，否则测试失败）。
2. 行为类键必须能观察：能走 HTTP/MCP 的用 `scripts/run_parity_suite.sh` 的 story 或新 story 覆盖，
   进程/运维类的用真实启动观察（如本轮的 `--workers` 用 `ss` 看共享 socket inode、`--daemon` 看父进程
   退出码与日志落盘）。
3. 默认值必须与参考一致，避免"接线即改行为"。

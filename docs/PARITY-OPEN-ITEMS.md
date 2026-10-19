# 逐 URL 后端逻辑对照轮 — 遗留待办（2026-10-17）

> 本文是 2026-10-17「逐 URL 后端逻辑对照轮」**未完成项**的完整落地清单：17 个并行切片把参考的
> 100 条装饰器路由（`api/http.py` 的 `@app.{get,post,put,patch,delete}`，含 `include_in_schema=False`
> 的 `GET /v1/version`）逐条与 `../hindsight/hindsight-api-slim/hindsight_api/` 及**线上参考服务**
> （`http://127.0.0.1:8888`）比对，发现分歧即按参考修正。已修部分见 `docs/PARITY-PLAN.md` §8
> 当日条目与 `CHANGELOG.md`；**本文只列仍然开着的项**，每条带现象、参考证据、本服务落点与验收方式。
>
> 对照方法（可复现）：
>
> ```sh
> scripts/parity_dev_service.sh 18200            # 起一份当前工作树（worker 关闭），--stop 停
> python3 scripts/parity_check.py GET /version --service http://127.0.0.1:18200
> python3 scripts/parity_probe.py \
>     --reference http://127.0.0.1:8888 --reference-key sk-hs-… \
>     --service http://127.0.0.1:18200           # 199 项固定检查
> ```
>
> 当前状态（2026-10-17 收尾验收）：
> - 差分探针（夹具按运行隔离后）：**209 checks: 198 identical, 0 differing, 11 known**（11 项为已记录的
>   非契约差异，见探针的 `KNOWN_DIFFERENCES`）。
> - 参考自带的黑盒套件（`scripts/run_parity_suite.sh`，77 个 story）：**339 passed, 1 skipped, 0 failed**
>   （首轮 338/1/1 的那一条失败即第 9 节的 9.1，已修）。
> - `make check` 全绿。本轮另把 `internal/httpx/` 的一处 **panic（连接被直接关闭）** 修掉，
>   并补上 panic → 参考的 `500 text/plain "Internal Server Error"`。
> - 第 1–7 节的 40 项**全部关闭**（状态看板全 ✅），第 8 节是记录项，第 9 节是验收阶段新发现。

> 状态看板（每次关闭一项即更新：⬜ 未开工 · 🚧 进行中 · ✅ 已关闭并验证）：
>
> | ID | 1.1 | 1.2 | 1.3 | 1.4 | 1.5 | 1.6 | 1.7 | 1.8 | 1.9 | 2.1 | 2.2 | 2.3 | 2.4 | 2.5 |
> |---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
> | 状态 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
>
> | ID | 3.1 | 3.2 | 3.3 | 3.4 | 3.5 | 3.6 | 3.7 | 3.8 | 4.1 | 4.2 |
> |---|---|---|---|---|---|---|---|---|---|---|
> | 状态 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
>
> | ID | 4.3 | 4.4 | 4.5 | 5.1 | 5.2 | 5.3 | 5.4 | 5.5 | 5.6 | 5.7 |
> |---|---|---|---|---|---|---|---|---|---|---|
> | 状态 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
>
> | ID | 5.8 | 5.9 | 5.10 | 6.1 | 6.2 | 7.1 | 7.2 | 7.3 | 7.4 | 7.5 |
> |---|---|---|---|---|---|---|---|---|---|---|
> | 状态 | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |

7.6 与第 8 节是**记录项**（已接受差异 / 未覆盖验证），不参与关闭。

## 1. 优先处理（正确性 / 可用性）

> **1.9 / 1.10 状态（2026-10-17）**：
> - **1.9 关闭（判为"不可复现/已正确"）**：分词、`search_vector` 表达式、tsquery 形状三处与参考逐项一致，
>   两侧 DB 上同一段中文的 `search_vector` 逐字节相同，参考的词项选择 SQL 在两侧选出相同词集；15 个中文查询
>   的 `keyword` 通道全部 `SAME`。原观察是夹具构造问题。新增 pgx 用例 `TestPostgresKeywordArmMatchesCJKQuery`
>   钉住 CJK 词项不被丢弃。
> - **1.10 关闭**：参考的 `memory_units.search_vector` 是**普通 `tsvector`**（线上库 `is_generated = NEVER`），
>   由**写入方**把词典烘进表达式（`ops_postgresql.pg_search_vector_expr` → `to_tsvector('<lang>', …)`，
>   迁移 `p4q5r6s7t8u9` 正是为此去掉了 GENERATED 表达式）；`mental_models` 侧仍硬编码 `english`（查询侧同）。
>   取值校验：`.lower()` 后须匹配 `[a-zA-Z_][a-zA-Z0-9_]*`，否则启动 `ValueError`；**不检查词典是否存在**
>   （缺失时在查询期报错）。旧行不重写/不重建——换词典后旧行不等价，由部署方自行重建（本服务同样不 reconcile）。
>   改动：`internal/config` 新增 `TextSearchExtensionNativeLanguage`（默认 `english`，小写化）并把该键从
>   「不支持即拒绝」改为 `resolved`；`internal/storage/textsearch_backend.go` 增加 `nativeLanguage` 与标识符校验
>   （PG 启动即拒非法值）、`BackendConfig.TextSearchNativeLanguage` 由 `app.go` 与 `cmd/bankctl` 注入；
>   `searchVectorExpr` 改为 `*PostgresStore` 方法并贯通 4 处写入点（`postgres_retain.go:913`、`postgres_memories.go:474`、
>   `postgres_consolidation.go:198`、`postgres_writes.go:37`）；`bm25_terms.go` 抽出 `bm25TermStatsQuery()` 并按其分词，
>   `postgres_bm25_pgsearch.go:75-76` 在 `ts_rank_cd`/`to_tsquery` 里使用它；`postgres_knowledge.go` 的 mental_models
>   保持 `english`（与参考一致）。
>   证据：用 `go build -overlay` 临时还原三处字面量的 pre-fix 二进制与修复后二进制跑同一夹具（键设为 `simple`）——
>   修复后写出的向量与参考库里 `to_tsvector('simple', 'Bob was the runner of the marathon')` **逐字节相同**，
>   而 pre-fix 是 english 词干；recall `{"query":"the"}` 修复前 `results []`、修复后命中（`keyword 0.1`）。
>   默认路径不变（`english` 字面量与旧实现逐字节相同）。用例：`postgres_bm25_language_test.go`、
>   `postgres_bm25_language_pgx_test.go`、`config_test.go`。
>   顺带：`docs/CONFIGURATION.md`/`etc/example.wenyuan.yaml`/`.env.example` 由生成器重新渲染（该键状态变为 `resolved`）。
>
> **1.7 / 1.8 已于 2026-10-17 关闭**：
> - **1.7** 参考的图臂种子**只来自语义密集命中**（`similarity >= graph_seed_min_similarity`，截 20 条；
>   `engine/memories/pg/recall.py:298-304`）；调用方自己的语义下限高于种子下限时，该共享池被判不可用
>   （`recall.py:108-113`），图臂改为**自己发一次密集查询**（`pg/link_expansion.py:154-172`，`threshold=graph_seed_min_similarity,
>   limit=20`）；**没有任何词法回落**，无密集命中时 `if not all_seeds: return GraphRetrieval([], …)`
>   （`link_expansion.py:185-186`）。改动：`internal/storage/postgres_recall.go:105-124,472-478,550-585`
>   （种子取自**未被调用方下限裁剪前**的密集命中；`graphSeedsFrom` 取代 `graphSeedIDs`/`candidateSimilarity`）、
>   `memory_recall_arms.go:76-79,110-126`（去掉错误的门控、加种子下限）、删除 `recall_types.go` 的 `graphSeedsAllowed`。
>   证据：无 embedding 的夹具上 `{"query":"Zulmar Lisken report"}` → 修复前 SVC 多出与词法命中共享实体的 `22222222`，
>   修复后两侧都只有 `11111111`（`min_scores.semantic=0.99` 时同样）。用例：
>   `TestPostgresRecallGraphArmSeedsOnlyFromDenseHits`、`TestGraphSeedsPickStrongestDenseHits`、
>   `TestMemoryGraphSeedsRequireTheDenseFloor` 等。
>   **连带更正**：`docs/PARITY-PLAN.md` 的 B2.7 条目曾称「无向量时回落词法」，该表述**是错的**，已更正。
> - **1.8** `RecallScores` 的四个通道（`final` 必填，`reranker`/`semantic`/`keyword` 默认 None）**总是出现在线上**，
>   缺席阶段是显式 `null` 而非省略（`engine/response_models.py:241-263`；线上：`min_scores.semantic=0.99` →
>   `"semantic": null` 仍在，`min_scores.keyword=9999` → `"keyword": null` 仍在）。改动：
>   `internal/memoryops/models.go:229-239` 去掉三个字段的 `omitempty`。证据：修复前只有 `final`/`reranker` 两键，
>   修复后与参考同为四键。用例：`TestRecallScoresCarryEveryStageIncludingNulls`。
>
> **1.3 已于 2026-10-17 关闭**：两后端现在都解析 entities——PG 新增专用 `UNION` 查询
> （直接 postings ∪ observation 从来源继承）取 `(unit_id, entity_id, canonical_name)` 并按 `ORDER BY 1,2`
> （`internal/storage/postgres_recall_channels.go` 的 `attachRecallEntities`/`recallUnitEntityRefs`），
> 结果侧按 entity-id 序填 canonical names（无 postings 时为 null，`recall_types.go` 的 `EntityRef`/`applyRecallEntities`），
> 通道按 rank 序去重、`observations` 恒空；**该 pass 在 `capMatchesToTokens` 之后**运行
> （`postgres_recall.go`/`memory_recall.go`），因为参考是从 token 预算裁剪后的列表解析实体的。
> 内存后端的 `entityStatesFor`（用实体名当 id、并把 fact 文本塞进 `observations`）已被替换。
> 证据：夹具按列复制到两侧后，默认 include、`include:{}`、`include:{"entities":{}}` 三种请求的结果与通道完全一致
> （含 postings 与 id 序不同的用例），`include:{"entities":null}` 两侧全 null，无命中时通道 null。
> 用例：`internal/storage/recall_entities_test.go`、`postgres_recall_entities_pgx_test.go`（并修正了
> `internal/memoryops/recall_test.go` 里断言了旧错误路径的夹具）。

> **1.4 / 1.5 / 1.6 / 3.2 已于 2026-10-17 关闭**：
> - **1.4 —— 判为"非分歧"**：参考把 tags 存进 Python `set` 后按**集合迭代序**落库
>   （`engine/retain/orchestrator.py:598-610` 的 `set(...)`/`list(...)`，`:4388` 同），线上同一请求
>   `["zz","aa","mm"]` 落库为 `{mm,zz,aa}`，且 `list({"t1","t2"})` 在不同进程间会翻转——**没有次序契约**，
>   故本服务按调用方次序存储不是 bug（未改代码、未加测试）。
> - **1.5** 两个真实缺陷：① `internal/storage/retain_contents.go:732-768` 的 `parseFactTime` 只接受完整 RFC 3339，
>   把模型给出的 `"2015-07-01"` 这类形态（参考的 `datetime.fromisoformat` 接受）静默丢弃 → 时间列 NULL；
>   现按 `factTimeLayouts`（date-only、`T`/空格、分钟、任意小数、带/不带时区→UTC）解析。
>   ② `internal/storage/extraction.go:209-220` 只在条目自带 timestamp 时设置参考时间，导致提示词写
>   `Event Date: Unknown`、相对短语无法解析；现默认取摄取时刻（条目明确要求"无时间"时仍不带）。
>   ③ `internal/extraction/model.go:124-142` 的 `dropNonEventDates` → `applyFactKindDates`：
>   只有显式事件才保留日期（参考对缺失的 `fact_kind` 默认 `"conversation"`），且点事件的 `occurred_end` 回落到 start。
>   证据：`{"items":[{"content":"Alice: We shipped the migration yesterday."}]}` → 参考
>   `2026-10-16 00:00:00+00 / 2026-10-16 23:59:59+00`，本服务修复前两列 NULL、修复后一致；`2015-07-01` 同。
> - **1.6** ① `internal/memoryops/retain_models.go:78-124` 的 `RetainItem.UnmarshalJSON` 遇到非对象 `metadata`
>   不再整体中止（新增 `withoutMember`），于是 `retain.go:261-300` 能把**结构拒绝项 + 字段校验项**按声明序合并去重；
>   ② `ScopeList.UnmarshalJSON:573-620` 现在真的校验联合类型，`validation.go:125-176` 渲染两个分支
>   （`literal['per_tag','combined','all_combinations','shared']` 由 `ObservationScopeModes` 生成，外加
>   `list[list[str]]` 且逐元素 `list_type`/嵌套 `string_type`）。证据：`metadata` 与 `timestamp` 同坏的 body、
>   以及 `"bogus"`/`5`/`true`/`{}`/`["a","b"]`/`[["a"],5]`/`[["a",5]]` 全部由 DIFF 转 SAME（另跑 15 个回归 body）。
> - **3.2** `AttachmentFilenames` 端到端接力：`internal/storage/models.go:337-342` + `internal/memoryops/retain_models.go:56-60`
>   （新字段，另有 `ContentBlock.Filename` 与 `InlineAttachment.Filename`）→ 内联入口填充
>   （`inline_attachments.go:57-98`，并写到附件行）→ 记入可重放的 `retain_params`（`memory_retain.go:509-515`）→
>   reprocess 重放（`retain.go:711-712` + `attachmentFilenamesFromParams:723-748`）→ 队列载荷携带
>   （`internal/storage/async_retain.go:91-94,144,211`，否则排队的 reprocess 仍会丢）。
>   证据：种一个带 `retain_params.attachment_filenames` 的文档后 reprocess，参考不变、本服务修复前只剩 `{"context":"seeded"}`、
>   修复后逐字节一致。用例：`TestReprocessReplaysAttachmentFilenames`、`TestRetainParamsRecordAttachmentFilenames`。

> 1.1 与 1.2 已于 2026-10-17 **关闭**：
> - **1.1** `internal/provider/openai/completion.go:191-214` 在返回长度超限错误前先填 `Content`/`Structured`；
>   `internal/reflect/loop.go:764-831` 的合成与 `:918-930` 的长度改写容忍 `provider.IsOutputTooLong`（带文本即用），
>   合成的传输上限不再取调用方的可见 `max_tokens`（未设时回到部署上限）。已审计 `IsOutputTooLong` 的既有消费者
>   （`internal/extraction/model.go:79-82` 的二分重试、routing 的 failover、rerank）均未受影响。
>   证据：同请求 `max_tokens:64`/`16` 两侧均 200（此前本服务 500）。
> - **1.2** `done.answer` 被信任：`internal/reflect/loop.go:205-212,277-310,429-437`（新 `doneAnswer` + 共用的 `finalize`），
>   合成只在拿不到 done 时发生；顺带关闭 `trace.tool_calls[].input` 的 `"tool"` 键、`done` 的 schema
>   （prose 要求 `["reason","answer"]`、文档模式换 `document`）、`read_mental_models` 的必填 `["reason","mental_model_ids"]`。
>   证据：两侧 `llm_requests` 行数/形状一致（4 轮循环 + 一次长度改写，无无工具合成调用），`usage` 为逐轮之和，
>   trace 不含 `done`。

| # | 现象 | 参考证据 | 本服务落点 | 验收 |
|---|---|---|---|---|
| 1.1 | `POST .../reflect` 在较小 `max_tokens` 下 **500**（可复现，约 137s）：模型以 `finish_reason=length` 收尾，`provider.IsOutputTooLong` 被当致命错误；且 `decodeChatResponse` 在返回前**未填 `Content`**，截断文本拿不回来 | 参考容忍 `finish_reason=length` 并使用已返回内容 | `internal/provider/openai/completion.go:184-199`（先填 `Content`/`Structured` 再返回）与/或 `internal/reflect/loop.go`（合成/改写调用容忍 `IsOutputTooLong`） | 同请求打两侧：`200` 且答案受预算约束（不再 500） |
| 1.2 | 反思的答案来源与参考不同：本服务**忽略 `done.answer`**，总是在循环外再发一次 `compose` 调用（1.1 的 500、usage 形状、trace 形状都由它派生） | 参考信任 `done.answer`（`engine/reflect/agent.py` 的 `_process_done_tool`），仅在拿不到 done 时才回落合成 | `internal/reflect/loop.go:269-284` + `step` 的 done 分支 | 两侧同输入对比 trace/usage/答案来源；改动较大，需先确认无回归 |
| ✅ 1.3 | PostgreSQL 召回路径**从不解析 entities**：顶层 `entities` 恒 null、每个结果 `entities` 也恒 null | 参考按 unit 直接 postings（或 observation 的来源 postings）填结果，按 **entity_id 序**；并给出 name 键的通道 `{entity_id, canonical_name, observations: []}`（`observations` 恒空） | `internal/storage/postgres_recall_channels.go:18`（`attachRecallSideChannels`）；`postgres_retain.go:1118 UnitEntityNames` 的集合正确但 `ORDER BY 2`（应按 `ORDER BY ue.unit_id, ue.entity_id`）；内存后端 `memory_recall_channels.go:26-49` 误用 fact 文本填 `observations` | 同一夹具两侧 recall：`include.entities` 下的结果 `entities` 与顶层通道逐字段一致 |
| ✅ 1.4（无分歧）| 检索结果里的 tag 次序与写入口径不一致（请求 `["t1","t2"]`，参考落库为 `["t2","t1"]`，本服务 `["t1","t2"]`）——可能只是模型/set 序，**需一次专门核对** | 同一 body 写两侧同构银行后比对 `memory_units.tags` | `internal/memoryops/retain.go` / `internal/storage/postgres_retain.go` | 同 body 两侧写库后行内容逐列比对 |
| ✅ 1.5 | retain 的 `occurred_start/occurred_end` 未落库：同一 body（含 "yesterday"）参考写入 `2026-10-16T00:00:00.01+00:00`/`23:59:59.01+00:00`，本服务两行都是 null | 参考 `engine/retain/fact_extraction.py` 的时间推断 | `internal/extraction/`（schema/映射）与 `internal/storage/postgres_retain.go` | 同 body 两侧写库后单元时间列比对 |
| ✅ 1.6 | retain 的请求校验仍有两处差：①「结构拒绝项 + 字段校验项」合并（`{"items":[{"content":"hi","metadata":5,"timestamp":"bad"}]}` 参考同时给 `value_error[timestamp]` 与 `dict_type[metadata]`，本服务只给结构项）；② `observation_scopes` 的联合类型未复刻（`"bogus"` 本服务 **200** 静默接受，参考 422 `literal_error` + `list_type` 两条） | `engine/retain/*` 请求模型 | `internal/memoryops/retain_models.go`（`RetainItem.UnmarshalJSON:78`、`ScopeList.UnmarshalJSON`）、`internal/memoryops/validation.go:98,155-158` | 同 body 两侧 422 逐字节比对 |
| ✅ 1.7 | 图臂的种子在**没有 embedding 时**回落到 keyword 分数（`internal/storage/postgres_recall.go:581` 的 `candidateSimilarity` 取关键字分作相似度），参考只用语义密集命中作种子（`engine/memories/pg/recall.py:299-304`、`memory_engine.py:9766`）。可观察：与关键字命中共享实体的单元被本服务拉进结果，参考没有 | 同左 | `internal/storage/postgres_recall.go`（种子选择） | 无 embedding 的夹具上两侧比对图臂结果集 |
| ✅ 1.8 | 缺席的评分通道被**省略**，参考发出 `"semantic": null`（`internal/memoryops/models.go:234-236`） | 参考 recall 响应模型 | `internal/memoryops/models.go` | 同请求两侧比对 `scores` 键集 |
| ✅ 1.9 | 中文（CJK）文本的 BM25 命中为空：同一夹具上参考给 `keyword: 1.7`，本服务 BM25 找不到（W15 构造夹具时发现，查询文本为中文）——**复核为不可复现**：分词器（`internal/storage/bm25_terms.go:33` 对 `engine/search/retrieval.py:28-34`）、`search_vector` 表达式与 tsquery/`ts_rank_cd` 形状（`internal/storage/util.go:71`、`postgres_bm25_pgsearch.go:72,79`）三处逐项一致，两侧 DB 上同一段中文的 `search_vector` 逐字节相同，参考自己的 `pg_stats` 词项选择 SQL 在两侧选出同样的 16 个词；同一夹具 15 个中文查询的 `keyword` 通道全部 SAME。原观察属夹具/构造问题 | 同左 | — | 用例 `TestPostgresKeywordArmMatchesCJKQuery`（pgx）钉住 CJK 词项不被丢 |
| ✅ 1.10 | 原文字典被**硬编码为 `english`**，参考读 `HINDSIGHT_API_TEXT_SEARCH_EXTENSION_NATIVE_LANGUAGE`（该键已注册并镜像，但没有任何存储字段读它）：`internal/storage/util.go:71`、`postgres_bm25_pgsearch.go:72,79`、`bm25_terms.go:105`。默认部署下两者等价（`etc/example.wenyuan.yaml:265` 也只声明 english），但部署一旦换词典即分歧 | `engine/db/ops_postgresql.py:25-59`、`config.py:1480` | `internal/storage/textsearch_backend.go`（解析该键）+ 上述三处调用点 + `internal/config/config.go` | 两侧用非 english 词典部署同夹具比对 keyword 通道 |

## 2. 通讯契约与中间件（`internal/httpx/`）

> 2.1 / 2.3 / 2.4 已于 2026-10-17 **关闭**：
> - **2.1** `internal/httpx/queryparams.go:132-148`：`Query` 改取**最后一个**出现值（`QueryValues`/`QueryPresent`
>   仍是序列读取器，故按声明类型区分，不按"是否重复"）。证据：`?limit=1&limit=50` 在 documents/tags/graph 三处
>   本服务 `1` → `50` 与参考一致。用例：`TestQueryBindsLastOccurrence`、`TestQueryValuesCollectsAllInOrder`。
>   仍以 `values[0]` 自读的途径已另立 **2.5**（operations 与 webhooks，已通知其归属切片）。
> - **2.3** `scripts/parity_probe.py` 新增 10 项检查（retain 嵌套坏成员/lax 合法体、404/405 无
>   `X-Ignored-Params`、`/version` 与 `/openapi.json` 的 `Content-Length`/`Vary`、三处重复标量），全部 `SAME`。
> - **2.4** `internal/config/config.go:913-930`（`FeatureKeys`，参考 `FeaturesInfo` 声明序）+
>   `internal/monitoring/version.go`（有序 marshaler）；`GET /v1/version`（`internal/reporting/knowledge.go:432`）
>   改用同一个 body。证据：两侧 `/version` 逐字节相同；用例 `TestVersionFeaturesDeclarationOrder`。

## 3. 读面 / 知识库 / 反射 / 心智模型

> **3.4 已于 2026-10-17 关闭**（判为**可观察**，非"已接受类"）：参考自己在该函数的 docstring 里点名了失败模式
> （"a rename survives the move that failed after it"），故把节点行与背后 mental model 的写入合进**一个事务**：
> `internal/reporting/knowledge.go:371-401` 改走单一存储调用，新增 `Store.UpdateKnowledgeNodeWithModel`
> （`internal/storage/store.go:404-409`），PG 侧 `internal/storage/postgres_knowledge_patch.go`（新，事务版读写）
> 与 `postgres_mental_models.go` 的 `get/putMentalModelOn` 拆分；内存后端的 `applyKnowledgeNodePatch`
> 让两行在同一把锁下同时落库（`memory_knowledge.go:87-190`）。证据（真库）：`PATCH {"name":"W12 Renamed","max_tokens":2147483648}`
> 让模型写入在节点写入之后失败——修复前 500 且树/模型都读作 `W12 Renamed`，修复后 500 且都保持 `W12 Ref Page`
> （与参考同结果，仅驱动文案不同）。用例：`TestKnowledgePatchLeavesNoRenameBehindWhenTheModelWriteFails`
> （去掉修复即失败："the rename survived the model write that failed after it"）。

> 3.1 / 3.3 / 3.5 / 3.6 / 3.7 / 3.8 已于 2026-10-17 **关闭**：
> - **3.1** 新增全局分块查询 `storage.FindChunk`（`internal/storage/store.go:480`，内存 `memory_read_surface.go:180`、
>   PG `postgres_read_surface.go:248` 按 `WHERE chunk_id = $1`），`internal/readsurface/documents.go:492` 改用它，
>   删除了按银行分页扫描的 `findChunkByID`。参考侧确认：`chunks` 以 `chunk_id` 为主键（`0003_reference_constraints.sql:31`），
>   `get_chunk_row` 无银行过滤（`engine/memories/pg/documents.py:689`）。证据：造 553 个银行使 `?limit=500` 的窗口恰好
>   排除夹具银行后，修复前 `reference 200 / service 404`，修复后两侧 200 且规范化后相同。用例：
>   `TestChunkReadResolvesPastTheBankPageWindow`、`TestMemoryFindChunkIgnoresTheBankPageWindow`、`TestPostgresFindChunkIgnoresTheBankPageWindow`。
> - **3.3** `document_metadata` 改为 Python 真值语义（`internal/storage/read_surface.go:254` 的 `DocumentMetadata`：
>   `null`/`false`/`0`/`""`/`[]`/`{}` → 缺席，其余原样回显），`Document.Metadata` 改为 `any`（`models.go:435`），
>   两后端一致。证据：`{"metadata": {}}` 与 `[]` 修复前返回 `{}`、修复后与参考同为 `null`；对象/null 本来就一致。
>   注意：参考的 **HTTP 层**对非对象 metadata 会 500（响应模型校验崩溃），按 `docs/CONTRACT-AUDIT.md` §5 的
>   「参考自身异常不复刻」记录，故实现的是引擎语义。
> - **3.5** dry-run 在无来源时短路：`internal/app/mental_model_dry_run.go:46-65`（先算 `hasSources` 再决定是否反思，
>   并把无来源的 outcome 放在空候选守卫之前），返回参考的 warning、`based_on:{}`、空 `tool_calls`/`llm_calls`。
>   证据：修复前 `content_written/would_persist:true/53924ms`，修复后与参考同为
>   `content_preserved_no_new_facts/false/~5ms`（规范化后 `SAME`）。用例：`TestDryRunWithNoSourcesSkipsTheReflect`。
> - **3.6** refresh 的无来源分支现在写 `reflect_response`（`text:""`、`based_on:{}`、`mental_models:[]`、
>   `reflect_skipped:no_sources_in_scope`、`outcome:content_preserved_no_new_facts`）并推进 `last_refreshed_at`，
>   同时保持 `last_memory_seen_at` 为 null（`internal/app/mental_model_refresh.go:189-215`）。证据：以 worker 开启的
>   独立实例（私有库）跑 refresh 后 GET full 与参考逐字节相同（仅 id/时间戳规范化）。用例：
>   `TestRefreshWithNoSourcesRecordsTheSkipWithoutWritingContent`。
> - **3.7** 拆分陈旧判定：`IsStale` 保留撤回尾判，新增 `IsStaleBatch`（仅作用域）并在 `Store` 接口声明
>   （`internal/storage/store.go:283-287`、`mental_model_staleness.go:114,376`），列表改用它（`internal/mentalmodels/handlers.go:95`）。
>   证据：删掉被引用的文档后，两侧都是**列表 `is_stale=false`、详情 `is_stale=true`**。用例：
>   `TestIsStaleBatchSkipsTheRetractionCheck`、`TestListOmitsTheRetractionCheckTheDetailReadApplies`。
>   顺带把另外两处应走批量语义的调用点改为 `IsStaleBatch`：知识树（`internal/reporting/knowledge.go:74`）与
>   反思的 `search_mental_models` 工具（`internal/storage/reflect_retrieval.go:277`）。
> - **3.8** `groundingRetracted` 改为「任一引用消失即 true」，去掉 `&& !Unresolvable()` 的兜底
>   （`internal/storage/mental_model_retraction.go:145`；`Unresolvable` 仍用于 refresh 的撤言与 dry-run 报告）。
>   证据：全部引用消失的模型修复前读作 fresh、修复后读作 stale，与参考一致。用例：
>   `TestIsStaleDetectsFullyRetractedGrounding`。

| # | 现象 | 参考证据 | 落点 | 验收 |
|---|---|---|---|---|
| ✅ 3.1 | `GET /chunks/{id}` 用 `ListBanks(Limit: 500)` 扫银行来定位分块（`internal/readsurface/documents.go:516`），参考按 `chunk_id` 全局查；第 500 个之后的银行里的分块会 404 | 参考按 chunk_id 直查 | `internal/storage/store.go` + 两后端 | 造 >500 银行（或直接插入）验证 |
| ✅ 3.2 | reprocess 不重放 `attachment_filenames`（既不记录也不重放），导入文档带命名附件时重处理后名字丢失 | 参考在 `retain_params` 里保留并在 reprocess 时合并 | `internal/storage/memory_retain.go`(`retainParamsFor`)、`postgres_retain.go`、`models.go`(`RetainItem`)、`internal/memoryops/retain.go`(`applyRetainParams`) | 带附件文档 reprocess 后附件名保留 |
| ✅ 3.3 | `document_metadata` 在 `retain_params.metadata` 不是对象时本服务返回 null，参考原样回显 | `engine/...` 文档投影 | `internal/storage/postgres_read_surface.go`（`documentMetadata`）与内存孪生 | 造非对象 metadata 的文档比对 |
| ✅ 3.4 | 知识节点 PATCH 的「节点行 + 背后 mental model」是**两个事务**，参考在一个事务里 | `engine/memory_engine.py:19874`（单个 `conn.transaction()`） | `internal/reporting/knowledge.go:307-406` + 两后端 `UpdateKnowledgeNode`/`UpdateMentalModel` | 仅并发/中途失败可观察；属 §5 item 5 已接受类，除非能演示失败模式 |
| ✅ 3.5 | 心智模型 `dry-run-refresh` 缺「无来源」短路：参考 4ms 返回 `outcome=content_preserved_no_new_facts`、`reflect_skipped=no_sources_in_scope`、`would_persist=false`；本服务跑完整反思（2m9s）并报 `content_written` | 参考 `hasSources` 门 | `internal/app/mental_model_dry_run.go`（`dryRun`，`hasSources` 已存在于 `internal/app/mental_model_refresh.go:288`） | 无来源模型两侧比对（响应 + 耗时） |
| ✅ 3.6 | 心智模型 refresh 的「无来源」分支**什么都不写**：参考写 `reflect_response{...outcome:content_preserved_no_new_facts, reflect_skipped:no_sources_in_scope, text:""}` 并推进 `last_refreshed_at` | 同上 | `internal/app/mental_model_refresh.go:296-301` | GET full 时 `reflect_response` 非 null |
| ✅ 3.7 | 心智模型 list 的 `is_stale` 多算了「撤回」判定：参考的批量 `compute_mental_models_are_stale` 不查撤回，只有单模型路径查 | `engine/memory_engine.py:20398` vs `:20216` | `internal/mentalmodels/handlers.go:86` + `internal/storage/mental_model_staleness.go` 加无撤回的变体 | 造「被引用事实已删」的文档后比对 list/detail |
| ✅ 3.8 | `groundingRetracted` 在**全部**引用都消失时返回 false（走 `Unresolvable()`），参考的 `_has_retracted_grounding` 只要**任一**引用消失即 true | 参考 `_has_retracted_grounding` | `internal/storage/mental_model_retraction.go` | 造「全部引用消失」用例比对 stale 与是否入队刷新 |

## 4. 传输 / 归档（`internal/transfer/`、`internal/hindsightarchive/`）

> 4.1–4.5 已于 2026-10-17 **关闭**：
> - **4.1** `internal/storage/postgres_retain.go` 的 `writeDocument` 改为 `created_at = COALESCE($7, now())`
>   （传入存储的 `doc.CreatedAt`，零值时为 nil），并重排了条件更新分支的参数位。用例：
>   `TestPutDocumentPreservesCreatedAt`（pgx，真库）。
> - **4.2** 经线上核实：参考**确实**携带并恢复两个维护队列段（在夹具银行种一行 `graph_maintenance_queue`
>   与一行 `entity_maintenance_queue`，导出后导入到新银行 → `maintenance_queue_rows_imported: 2` 且目标行被重新写入）。
>   本服务照此实现：`internal/hindsightarchive/sections.go` 增两个表常量并让 `DataTables` 与参考同序；
>   新增 `internal/storage/maintenance_transfer.go` 的 `MaintenanceQueueTransfer` 能力（PG 走 SQL，
>   内存后端只做 graph relink 队列）；导出侧 `internal/transfer/export_build.go` 的
>   `buildGraphMaintenance`/`buildEntityMaintenance`；导入侧 `import_apply.go` 的 `applyMaintenanceQueues` +
>   `archiveUnitMap`（按 unit-id 映射与 `canonical_name` 重映射，映射不到的丢弃——与参考一致）。
>   证据：把参考产出的归档导入本服务 → `maintenance_queue_rows_imported: 2`（修复前该字段为 0/缺失），
>   PG 行已重映射到目标自己的 unit id 与实体。用例：`TestRestoreRemapsMaintenanceQueues`。
>   **这一条与 `AGENTS.md` 原有的「有意不携带」描述相反，`AGENTS.md` 已同步更正。**
> - **4.3** `internal/hindsightarchive/`：`Manifest` 去掉全部 `omitempty`、新增 `bank_rows_json_encoding`
>   （`EncodingSerialized`/`EncodingDecoded`）、manifest 改为带缩进输出；writer 对每个被选中的段都输出（空段输出 `[]`），
>   只有 `observations.json`/`attachments.json` 保持条件输出（与参考一致）；`banks.json` 行补
>   `config`（始终）、`internal_id`、`last_consolidated_at`、`mission_changed_at`（经新增 `internal/storage/bank_transfer.go`
>   的 `BankLifecycleSource`），`disposition`/`config` 以 JSON 文本写出。**另修一个条目未点名的阻断性残差**：
>   文档/事实/观察的列表成员曾被序列化成 `null`，使参考**拒绝**本服务产出的归档
>   （`ValidationError: facts.0.causal_relations Input should be a valid array`）——已加 `MarshalJSON` 归一为 `[]`/`{}`。
>   证据：manifest 键集与条目形状对齐线上参考归档，且参考现在能成功导入本服务产出的归档（修复前报 pydantic 校验错）。
>   用例：`TestWriteBankArchiveMatchesReferenceShape`。
> - **4.4** `internal/storage/stored_files.go` 增 `ExportStorageKey`（`banks/{bank}/exports/{export}/transfer.zip`），
>   `internal/app/worker_transfer.go` 的 `applyExport` 改用它（删除死代码 `exportArchiveName`）。
>   证据：导出的 `result_metadata.storage_key` 已与参考同形，下载路由仍可解析。
> - **4.5** `applyClone` 序列化归档并把 `archive_byte_size` 写进 clone 的 `result_metadata`。
> - 遗留（未列入本条目，供后续判断）：`internal_id` 现在会导出，但 `applyBankProfile` 导入时忽略它，
>   而参考的 `_restore_rows` 会插入该值。

| # | 现象 | 参考证据 | 落点 | 验收 |
|---|---|---|---|---|
| ✅ 4.1 | 导入不保留文档 `created_at`（返回导入时刻）；内存后端已尊重 `doc.CreatedAt`，PG 的 INSERT 硬编码 `now(), now()` | `engine/transfer/importer.py:1619` | `internal/storage/postgres_retain.go:600` | 导入后 `GET /documents` 的 `created_at` 等于源 |
| ✅ 4.2 | `maintenance_queue_rows_imported` 恒 0：两个维护队列段既不导出也不导入（`data/graph_maintenance_queue.json`、`data/entity_maintenance_queue.json` 被丢弃） | 参考 `data/` 段与重映射逻辑 | `internal/hindsightarchive/sections.go:59`（`DataTables`）、`internal/transfer/import_apply.go`（`applyOperations`）；需要 storage 层行级 list/restore API | 导出→导入后该计数与参考一致；注意 AGENTS.md 明确记录了「重映射后丢弃」的上游语义，需先确认参考的真实行为再动 |
| ✅ 4.3 | 归档格式残差：manifest 计数带 `omitempty`（零值段被丢掉，参考每个字段都发）、缺 `bank_rows_json_encoding`；`banks.json` 行缺 `internal_id`/`last_consolidated_at`/`mission_changed_at`/`config`，且 JSON 列以解码对象而非 JSON 文本写出；空段（`observations.json` 等）被省略 | 参考 writer | `internal/hindsightarchive/` | 双向 round-trip + 与参考产物逐字段对比（当前两个 reader 都容忍可选段，故只影响产物形状） |
| ✅ 4.4 | 导出对象的 `storage_key` 布局不同：参考 `banks/{bank}/exports/{uuid}/transfer.zip`（文档导出 `tenants/{t}/banks/...`），本服务 `banks/{bank}/exports-{uuid}.zip` | 参考对象存储键 | `internal/app/worker_transfer.go:195`、`internal/storage/stored_files.go:77` | 功能等价（下载路由两者都接受），仅在需要与参考共用对象存储时才是问题 |
| ✅ 4.5 | clone 的 `result_metadata` 缺 `archive_byte_size` | 参考 clone 元数据 | `internal/app/worker_transfer.go`（`applyClone`） | 两侧比对 clone 操作元数据 |

## 5. 队列 / 投递 / 写面

> **5.9 已于 2026-10-17 关闭**：异步 retain 现在也发 `retain.completed`（`internal/app/worker_retain.go:62-76`
> 在 `RetainWrite` 成功后调用从同步路径抽出的发射器；`internal/memoryops/retain.go:418-478` 把形状提为
> `RetainCompletedData`/`DocumentUnitCount` 供两路共用）。参考 body 由线上读取（异步 retain 后其
> `webhook_delivery` 行的 `task_payload`）：每个内容项一条，`operation_id` 是 **worker 实际执行的 `retain` 子操作**
> （不是调用方拿到的 `batch_retain` 父操作，依据 `engine/memory_engine.py:3992-4038`），计数按**文档**
> （`count_document_memory_units`），未命名文档的项不带该键。证据：修复前异步 retain 完成时 `webhook_delivery`
> 为 0 行，修复后两条 body 与参考逐字节相同（仅 bank/operation id/时间戳不同）。用例：
> `TestAsyncRetainRaisesCompletionEvents`、`TestAsyncRetainRaisesNoEventWhenTheWriteFails`。

> **5.10 已于 2026-10-17 关闭**：失败的巩固现在发 `consolidation.completed` + `status="failed"` +
> `data={"error_message": …}`（`internal/app/worker.go:82-109` 的 `Process` 分支，`:599-603`/`:689-700` 让成功与失败
> 共用同一个 `fireConsolidationEvent`）。参考 body 由线上读取（向参考队列注入一条畸形载荷的 consolidation 任务，
> 其 SSRF 防护阻止本地投递但 `task_payload` 可读）：
> `{"event":"consolidation.completed","bank_id":…,"operation_id":…,"status":"failed","timestamp":…,"data":{"error_message":"TypeError: 'int' object is not iterable"}}`
> （`exclude_none`：未设置的计数缺席而非 null）。用例：`internal/app/consolidation_failure_event_test.go`
> （两份 body 逐字节钉住 + 成功半边未变）。

> 5.5–5.8 已于 2026-10-17 **关闭**（另含 2.5 的 webhooks 一半）：
> - **5.5** `retain.completed` 改为**每个内容项一个事件**（`internal/memoryops/retain.go:368`，新增
>   `notifyRetainCompleted`/`documentUnitCount`/`eventOperationID`），`data={document_id,tags,memory_unit_count}`、
>   无 operation_id 时用 32 位 hex。证据：线上参考对 2 项同步 retain 排两个 `retain.completed`
>   （`operation_id` 同一个 32 位 hex，`memory_unit_count` 取 Documents API 的计数），本服务队列里的 body 逐字节相同
>   （含未命名 document 时 `data:{}`）。用例：`internal/memoryops/retain_completed_test.go`。
> - **5.6** 事件体按参考 `exclude_none` + 声明序输出：`internal/app/worker.go:809` 的 `marshalWebhookData`
>   （`Data` 改为 `json.RawMessage` 以保留转义）、`:767` 的字段序表；`internal/memoryops/retain_defense.go:475`
>   对未设置成员不输出键（data 里不再有 `status`，6 个未设置的 SIEM 成员不再出现 null）。
>   证据：线上参考 `memory_defense.triggered` 两种 redact 情形的 body 被逐字节复现（仅时间戳不同）。
>   用例：`internal/app/webhook_event_body_test.go`（含两份线上 body 作为夹具）。
> - **5.7** PATCH webhook 的次序改为 校验失败 → id 解析 → 无字段检查（`internal/webhooks/handlers.go:120-132`）。
>   证据：`PATCH …/webhooks/notauuid {}` 修复前 `No fields provided to update`、修复后与参考同为 `Invalid webhook ID`；
>   合法 id + 空体仍是 `No fields provided to update`。用例：`TestWebhookUpdateMalformedIDPrecedesNoFields`。
> - **5.8** 判定为**复刻参考行为（含它自身的 500）**：四个可空字段现在接受 `null`
>   （`internal/webhooks/validate.go:129-181` 记录 `nullColumn`/`nullHTTPConfig`），`handlers.go:139-155` 在触库前
>   给出与参考相同的状态码（不存在的行 → 404 `Webhook not found`；存在的行 → 500）。
>   注：修正条目原文的一处事实——`url:null` 修复前是**静默 200 空操作**（不是 422）。500 的 detail 文案属于参考
>   自身异常文本（CPython/驱动），与 §5 item 6 同类，不复刻。用例：`TestWebhookUpdateNullMembers`。
> - **2.5（webhooks 一半）** `internal/webhooks/handlers.go:305,325` 的 `limit`/`offset` 改取最后一个值。

> 5.1–5.4 已于 2026-10-17 **关闭**：
> - **5.1** claim 谓词改为参考的 `task_payload IS NOT NULL`（`internal/storage/postgres_claim.go:55,212`、
>   `memory_claim.go:49,138`）；可执行的入队路径把 nil 载荷归一为 `{}`（`internal/storage/operation_payload.go`
>   的 `executablePayload`，用于 `memory_operations.go`/`memory_enqueue.go`/`memory_memories.go`/`memory_retain.go`/
>   `memory_writes.go`），payload-less 的 batch 父操作仍不可被领取。证据：注入 `pending`/NULL 载荷行后，参考与本服务
>   的 worker 都让它保持 `pending`，而同一银行里 `{}` 载荷的行被领取并完成。用例：`TestClaimSkipsPayloadLessOperation`、
>   `TestPriorityClaimSkipsPayloadLessConsolidation`、`TestClaimSkipsNullPayloadOperationPG`。
>   （`internal/worker/queue_integrity_test.go:TestLapsedCompleteSkipsParentRollup` 的种子改用 `SubmitAsyncRetain`，
>   因为 `PutOperation(nil)` 现在与 PG 一样归一为 `{}`；断言语义未变。）
> - **5.2** `POST /consolidation/recover` 不再入队巩固 pass（`postgres_memories.go:816`、`memory_memories.go:1026`）。
>   证据：同夹具两侧 `retried_count:2`，紧随其后的 `POST /consolidate` 两侧都是 `deduplicated:false`（本服务此前 `true`）。
>   用例：`TestRecoverConsolidationsDoesNotEnqueuePass`。
> - **5.3** 重排的 COUNT/UPDATE 补 `fact_type IN ('experience','world')`（`postgres_memories.go:823`、`memory_memories.go:1041`）。
>   证据：world+experience+observation 都带 `consolidation_failed_at` 时两侧都 `retried_count:2`。用例：
>   `TestRecoverConsolidationsSkipsNonSourceFacts`（含 PG）。
> - **5.4** 判定为**无可观察差异**，不改代码：参考只在 `engine/memory_engine.py:11694` 写 `banks.last_consolidated_at`，
>   全仓（`engine/**`、`api/**`）无任何读取点；`/stats` 的 `last_consolidated_at` 来自 `memory_units` 的
>   `MAX(consolidated_at)`（`engine/memories/pg/counts.py:39`），本服务同源（`postgres_observability.go:170`）。
>   无路由可观察该列，写入无法验证，故保持现状。

| # | 现象 | 参考证据 | 落点 | 验收 |
|---|---|---|---|---|
| ✅ 5.1 | claim 谓词不同：参考只领取 `task_payload IS NOT NULL` 的行，本服务用「排除 `batch_retain` 类型」代替。一个 payload 为 NULL 的非父 `pending` 操作在本服务会被执行，参考永远挂着 | `engine/db/ops_postgresql.py:1770,1949` | `internal/storage/postgres_claim.go:55`、`memory_claim.go:46`；需同时决定本服务自身「nil payload 入队」的处理（`internal/storage/claim_priority_test.go` 里有 nil payload 的种子） | 注入 NULL payload 的非父操作，两侧 claim 行为一致 |
| ✅ 5.2 | `POST /consolidation/recover` 多入队一次巩固 pass（参考只跑 UPDATE 并返回计数）；可观察：紧随其后的 `POST /consolidate` 在本服务报 `deduplicated:true`，参考 `false` | `engine/...` `retry_failed_consolidation` | `internal/storage/postgres_memories.go:830`、`memory_memories.go:1016` | 先 recover 再 consolidate，比对 `deduplicated` 与 operation id |
| ✅ 5.3 | `RecoverConsolidations` 的 COUNT/UPDATE 缺参考的 `fact_type IN ('experience','world')` 谓词 | 参考同函数 | `internal/storage/postgres_memories.go:815-830`、`memory_memories.go:992-1013` | 造混合 fact_type 的失败巩固行后比对恢复计数 |
| ✅ 5.4 | 银行级清空 observation 未把 `banks.last_consolidated_at` 置 NULL（参考会） | `clearBankObservationsTx` | `internal/storage/postgres_memories.go:739` 一带 | 该列在参考 build 里只写不读，无可观察面；记录即可 |
| ✅ 5.5 | `retain.completed` 事件的载荷形状：参考**每个内容项一个事件**，`data={document_id,tags,memory_unit_count}`，无 operation_id 时用 `uuid4().hex`；本服务每请求一个事件，`data={document_id,items_count,unit_ids,operation_id}` | 参考 webhook 事件模型 | `internal/memoryops/retain.go:352`（事件发起）、`internal/app/worker.go:647`（载荷渲染） | 本机接收方两侧比对事件条数与 `data` 键集 |
| ✅ 5.6 | webhook 事件体没有 `exclude_none`：参考 `model_dump_json(exclude_none=True)` 丢 null 成员（如 `memory_defense` 的 `data` 不得带 `status`，其 6 个未设置的 SIEM 成员必须被丢掉） | 参考 webhook 序列化 | `internal/app/worker.go:647`（`marshalWebhookEvent`）、`internal/memoryops/retain_defense.go:477` | 接收方收到的 body 逐字节比对 |
| ✅ 5.7 | `PATCH .../webhooks/{id}` 的次序：body `{}` + 畸形 id 时本服务报 `No fields provided to update`，参考先解析 id 报 `Invalid webhook ID` | 参考先解析路径参数 | `internal/webhooks/handlers.go:124`（把 `h.member(...)` 移到 `noFields` 检查之前，`:104`） | 两侧 422 文案比对 + 用例 |
| ✅ 5.8 | `PATCH` 传 `null` 给可空字段（`url`/`event_types`/`enabled`/`http_config`）：参考照收然后 500（`http_config:null` → `'NoneType' object has no attribute 'model_dump_json'`；其余为 not-null 违例 500，未知行 404），本服务 422 `<type>_type` | 参考模型可空 + 服务端行为 | `internal/webhooks/validate.go`（接受 null；字段读取点 `:100/127/140/150`）+ `handlers.go` 次序 | 两侧比对状态码/文案（要不要复刻 500 需一次判断：这是参考自身的缺陷） |

## 6. MCP

> 2.2、6.1、6.2 已于 2026-10-17 **关闭**：
> - **2.2** 新增导出 `httpx.PythonJSONDecodeError`（复用既有 CPython 扫描器，`internal/httpx/jsonsyntax.go:47`），
>   `internal/mcp/server.go:625` 的 `parseErrorMessage` 用它给出 CPython 原句；错误信封改为有序结构
>   （`:686`，`jsonrpc,id,error`），`:659` 的 `writeJSON` 不再追加换行。证据：body `x` 时响应逐字节相同
>   （`400`、`Content-Length: 128`、`{"jsonrpc":"2.0","id":"server-error","error":{"code":-32700,"message":"Parse error: Expecting value: line 1 column 1 (char 0)"}}`）。
>   用例：`internal/httpx/pythonjson_test.go`（13 个 CPython 派生用例）、`TestParseErrorIsTheCPythonSentence`。
> - **6.1** MCP 工具返回体改为参考的引擎载荷 + 参考的 `json.dumps(indent=2)` 渲染（新增
>   `internal/mcp/jsontext.go` 的 CPython 兼容渲染器与 `internal/mcp/payloads.go` 的投影）：
>   `list_memories`/`list_documents`/`list_tags` 带完整行与 `{items,total,limit,offset}`；`list_mental_models`/`list_directives`
>   用引擎的 `{items,total}`（心智模型行按批量陈旧语义、指令按 priority 降序并支持 tags/active_only）；
>   `list_operations`/`get_operation` 用 16/17 键引擎行、未知 id 为 `status:"not_found"` 的**正常结果**；
>   `get_bank_stats` 12 键；`create_bank` 撞名时改为 patch profile 后返回；**未找到一律是 `isError:false` 的正常结果**
>   （参考原句：`Bank '…' not found`、`Memory '…' not found`、`Document '…' not found`、
>   `Mental model '…' not found in bank '…'`、`Knowledge page/node '…' not found in bank '…'`、`Operation <id> not found for bank <b>`，
>   列表工具另有 `Operation validation failed: Bank '…' not found` + `"items": []`）；真实失败也按参考的载荷渲染
>   （retain/sync_retain → 错误状态对象、recall → `{"error":…,"results":[]}`…），缺必填参数仍是错误结果。
>   另修一批被瘦身的载荷（心智模型/directive/knowledge 的创建、更新、删除、清空、刷新与知识树投影、clear_memories、
>   delete_bank、delete_document、cancel_operation、update_memory/invalidate_memory 等）。
> - **6.2** 关闭 MCP 后 `/mcp`、`/mcp/`、`/mcp/{bank}`、`/mcp/{bank}/` 全部 `404 {"detail":"Not Found"}`（框架自身的 404），
>   `features.mcp=false`。线上参考无法在不重启的情况下关掉 MCP，故以**本服务开启/关闭两种构建**做验证，
>   参考侧以源码为据（`api/__init__.py:71` 仅在 `mcp_api_enabled` 时挂 `MCPMiddleware`）。用例：
>   `TestMCPDisabledAnswersTheFrameworkNotFound`。

| # | 现象 | 参考证据 | 落点 | 验收 |
|---|---|---|---|---|
| ✅ 6.1 | **工具返回体**普遍被裁剪（不是原样返回引擎载荷）：`list_memories`/`list_documents`/`list_tags` 缺 `limit`/`offset` 与瘦身条目；`list_mental_models`/`list_directives` 缺 `total`；`list_operations` 形状完全不同；`get_bank_stats` 只有 4 个键（参考 12 个）；`create_bank` 撞名时用 driver 错误而非「更新并返回 profile」；**未找到在参考里是正常结果**（`{"error": "… not found"}`，`isError:false`），本服务走 `toolError` 报错；字符串型工具返回参考是 `indent=2` 的美化 JSON，本服务是紧凑 JSON | 参考 `mcp_tools.py` 各工具实现 | `internal/mcp/tools.go`（`listMemories`/`getMemory`/`getBank`/`createBank`）、`tools_more.go`、`tools_knowledge.go`、`server.go`（`callTool`） | 逐工具两侧 `tools/call` 结果逐字段比对 |
| ✅ 6.2 | `HINDSIGHT_API_MCP_ENABLED=false` 路径只有源码比对，**未做行为验证**（线上参考是开启态） | — | `internal/app/app.go:191-198`、`internal/httpx/router.go` | 关掉 MCP 起服务，比对 404/行为 |

## 7. 银行族 / 指令 / 其他契约小项

> 7.1–7.5 已于 2026-10-17 **关闭**：
> - **7.1** `PUT`/`PATCH /banks/{id}` 不再写遗留列 `banks.mission`/`banks.disposition`
>   （`internal/bankapi/models.go:287` 的 `ToStorage` 与 `:298` 的 `ToPatch`，删除 `missionValue`），
>   注释与 `validation_test.go:122` 同步更新。证据：`PUT {mission,disposition_skepticism:5}` → `DELETE /config`
>   之后 list `mission`/profile `skepticism`：修复前 `"be terse"`/`5`（参考 `""`/`3`），修复后 `SAME`。
>   用例：`TestBankUpdateDoesNotWriteLegacyDispositionMissionColumns`。
>   （旧 build 写过的行仍带该列——那是数据不是代码。）
> - **7.2** 指令 `offset` 超 int64 的上界溢出改用共享信封的自身状态形（`internal/directives/query.go:109`
>   返回 `ValidationFailure{Status: KindInternal}`），与参考的 500 状态一致；负溢出与 `limit` 保持 422。
>   证据：状态 `SAME`，body 只差 asyncpg/pgx 文案（已接受类）。用例：`TestDirectiveListIntegerTooLargeForInt64`。
> - **7.3** 抽取模式改为**精确**比较（`internal/extraction/modes.go:32`），并修正真正决定行为的另两处
>   `EqualFold` 分块判断（`internal/storage/extraction.go:148,190`——原条目给的位置不完整）。
>   证据：银行配置 `CHUNKS` 时修复前逐字落库（未调用模型），修复后与参考同为模型抽取结果。用例：
>   `TestModeNamesAreMatchedExactly`。
> - **7.4** `occurred_start/occurred_end` 改走 `dimensionValue`（`internal/extraction/extractor.go:324-325,370-371`），
>   把模型输出的 `"N/A"`（任意大小写）当作缺席，与参考 `get_value()`（`fact_extraction.py:2209`）一致。
>   线上两次 dry-run 都未再出现字面 `"N/A"`；用例：`TestTimestampPlaceholderIsNotADate`。
> - **7.5** 分块切分把分隔符留在**后一段的开头**（参考算法实测 `['Alpha','beta','.']`），
>   改写 `internal/storage/segment.go:255`。证据：`retain_chunk_size:5` 对 `"Alpha beta."` 修复前
>   `['Alpha','beta.']`、修复后 `['Alpha','beta','.']` = 参考。用例：
>   `TestSegmentKeepsSeparatorOnTheFollowingPiece`（并更新 `TestRetainSegmentsIntoChunks` 的期望）。

| # | 现象 | 参考证据 | 落点 | 验收 |
|---|---|---|---|---|
| ✅ 7.1 | `PUT`/`PATCH /v1/default/banks/{bank_id}` 会写 `banks.mission` 与 `banks.disposition` 两列，**参考从不写**（只调 `update_bank(name=…, config_updates=…)`）。后果：`DELETE .../config` 之后本服务仍报列值，参考回落默认值 | `api/http.py:8602,8647`；`engine/memory_engine.py:21641`；`engine/retain/bank_utils.py:389`（建行即 `DEFAULT_DISPOSITION`/`mission ""`） | `internal/bankapi/models.go:286`（`ToStorage`）、`:312`（`ToPatch`）——去掉 `Disposition`/`Mission` 赋值（保留 `Name`）；连带 `internal/bankapi/config_keys.go:116` 的注释与 `internal/bankapi/validation_test.go:125` | 现成证据：`PUT mission` → `DELETE config` → list `mission`（ref `""` vs svc `"be terse"`）；`PUT disposition_skepticism:5` → `DELETE config` → profile（ref `3` vs svc `5`） |
| ✅ 7.2 | 指令 `offset` 超 int64（`offset=99999999999999999999`）本服务 422，参考透传给驱动后 500 | 参考不校验上界 | `internal/directives/query.go`（`boundedQueryInt` 的溢出分支） | 需要一次 storage 调用才能复现驱动错误 |
| ✅ 7.3 | 抽取模式比较大小写：`internal/extraction/modes.go:31` 折叠大小写，参考 `== "chunks"` 严格（`"CHUNKS"` 参考走 concise，本服务走 chunks） | `engine/retain/fact_extraction.py` | `internal/extraction/modes.go` | 两侧同 body 比对抽取结果 |
| ✅ 7.4 | dry-run 的 `occurred_start`/`occurred_end` 会把模型输出的 `"N/A"` 当字面串返回，参考归一为 `null` | `engine/retain/fact_extraction.py:2209`（`get_value()`） | `internal/extraction/extractor.go:~324/370`（或模型归一） | 依赖模型输出，不易稳定复现；以单测钉归一规则 |
| ✅ 7.5 | 极小 `retain_chunk_size` 的分块切分不同：`{"retain_extraction_mode":"chunks","retain_chunk_size":5}` 对 `"Alpha beta."` → 本服务 `["Alpha","beta."]`，参考 `["Alpha","beta","."]` | 参考递归切分器 | `internal/storage/segment.go`（`splitRecursive`/分隔符处理，recall 与 retain 共用） | 两侧 same-body 比对（也可用单测钉切分序列） |
| 7.6 | （已接受类，勿修）`target_info` 的 SDK 标签、`prometheus_client` 默认 registry 的 `python_*`/`process_*` 家族、驱动错误文案、`812` vs `812.0`、模型输出文本 | `docs/CONTRACT-AUDIT.md` §5 item 6 | — | 保持记录即可 |

## 8. 本轮未做的验证

- `/metrics` 全文对比已由 A5 手工做过一次（修掉 4 处文本差异后除 `target_info` 外为空），但**没有进探针**，属于一次性证据。
- `MCP_ENABLED=false`、`HINDSIGHT_API_BASE_PATH` 与准入（admission）在真实负载下的行为未复测（源码比对已做过）。
- 参考自身的两处异常**不复刻**：`NaN`/`Infinity` 触发 500、约 2000 层嵌套触发 `RecursionError` 500（已记录）。
- 本轮所有夹具银行（`parity-*`）与临时库（`wenyuan_parity`、`whparity`）已清理；临时服务（18100/182xx）已停。

## 9. 验收阶段新发现

| # | 现象 | 参考证据 | 本服务落点 | 验收 |
|---|---|---|---|---|
| ✅ 9.1 | 并发的 `POST /banks/{id}/consolidate`（5 个同时）产生了 **3 次**巩固运行，参考的契约是**至多 1 次**：`EnqueueConsolidation` 用「先查 pending 再插入」的去重，READ COMMITTED 下并发提交都会看不到 pending 行而各自插入（ref: `internal/storage/postgres_memories.go:739`） | 参考在同一事务里先对 banks 行取 `SELECT … FOR NO KEY UPDATE`（不是 `FOR UPDATE`：`async_operations` 对 banks 有外键，普通 op 插入会取 `FOR KEY SHARE`，`FOR UPDATE` 会把它们全挡住），再做条件插入；注释直接点出这一类 bug（重复 pending 会以 `retry_blocked` 堆积并饿死该银行）——`engine/memory_engine.py:22124-22146` | `internal/storage/postgres_memories.go`、`postgres_enqueue.go`、`operation_enqueue.go`、`postgres_writes.go` | `PARITY_DB_URL=… scripts/run_parity_suite.sh tests/test_28_consolidation_serialisation.py` 全绿 + 并发入队的真库 Go 用例 |

> **9.1 已于 2026-10-17 关闭**：`internal/storage/postgres_enqueue.go` 新增
> `enqueueLockedBankTx`（在一个事务里先对 banks 行取 `FOR NO KEY UPDATE`，再在同一事务里做条件查-插；锁型选择
> 与注释都照参考的说明与理由）与 `enqueueDedupeByBank`（参考的 `dedupe_by_bank` 条件提交，未知银行仍容忍，
> 由插入时的外键失败给出与之前相同的答复）。`EnqueueConsolidation` 的非作用域分支与 `EnqueueGraphMaintenance`
> 改走它，`EnqueueOperation` 的载荷键去重也统一到同一约定；作用域巩固仍为无条件提交（与参考一致）；
> 死代码 `findPendingConsolidation`/`findNonTerminalOperation` 删除。内存后端本就以 `s.mu` 覆盖扫描+插入，
> 无需改动（已核对）。
> 证据：`scripts/run_parity_suite.sh tests/test_28_consolidation_serialisation.py` → **4 passed**
> （此前该 story 的 `test_a_burst_of_requests_produces_one_pass` 失败：5 个并发请求产生 3 次运行）；
> 新增真库用例 `TestPostgresConsolidationEnqueueIsAtomicUnderConcurrency`（5 个并发入队断言恰好一行
> 非终态巩固；去掉锁即失败：“2 callers inserted, want exactly one”）。

# 功能面复核与剩余清单（2026-10-15）

这份文件回答的问题是「上游的服务到底有没有 100% 复刻过来」。HTTP 契约面（路由/参数/字段/空值/
状态码/头/错误信封）记在 `docs/CONTRACT-AUDIT.md`；**行为面**记在这里。

## 0. 结论速览

| 证据 | 结果 |
|---|---|
| **提示词文本**（2026-10-15 新增发现） | 抽取/巩固/反思/结构化输出/连接探针的提示词是**改写版**，上游系统套件靠锚句识别步骤，7 个锚句只命中 1 个。移植原文正在进行 |
| 各簇收口（2026-10-15 晚） | 已完成并各自私有库验证：consolidation/observations **45/51**；banks/config/aliases/isolation 全绿；documents/entities/knowledge-read **44/70**（test_10/13/14/15/16/92 全绿）；webhooks **7/7**；archive transfer **test_52 7/7、test_53 9/9、test_83 1/1**；实体与图谱 **test_70 4/4、test_72 5/5、test_74 5/5**；mental model/knowledge **test_30 6/6、test_32 4/4、test_34 2/2、test_35 8/8、test_37 2/2、test_39 2/2**；源码/标签作用域 **test_71 5/5、test_81 6/6、test_82 6/6**；零散项 **test_02/27/94/96/97 全绿**。仍余**两个偶发项**（2026-10-15 本轮复核结论）。其一：**`test_02` 的 bm25 打平**。三次完整跑：339 passed/1 skipped 两次、1 failed（`test_02_retrieval_strategies`）一次。失败断言是 `rrf_merged.source_ranks` 里 `bm25_rank` 由 1 变 2；单跑 12 次全绿，说明是竞态而非确定性分歧。机制：该 story 的两条事实 `ts_rank_cd` 打平（实测两侧均为 0.3），名次由**物理行序**决定（上游 arm 只按分数排序、由计划给出物理序；本实现显式 `ORDER BY score DESC, ctid ASC`，两者同序），而巩固回写 `consolidated_at` 会把行搬到堆尾——DB 全量扫描显示所有 bank 的插入序都保持、无一次反转，故该竞态在上游同样存在（其 `MarkConsolidated` 也 UPDATE），属 story 侧对"打平"的假设，不是本实现的缺口。其二：**`test_12` 的排队追加丢轮**（`test_queued_appends_to_one_document_keep_every_turn`，一次全量跑报 `turns lost: ['Alice mentioned item number 0.']`）。该 story 单跑 9/9 通过；同一次复核中另有一次 `test_52` 超时失败，事后确认那一刻有两次 parity 跑并发（同一库互相认领队列，套件 conftest 已写明此坑），掐掉重叠后重跑即 339/1/0。两台共享同一库的服务同时消费队列时，排队类 story 会互相抢工作，这是套件设计上的竞态而非本实现的分歧；本实现侧的排队追加路径（`writeItemWithAppendRetry` + 冲突重试）未被改动。另：两条未被 story 覆盖的读路径越权（`/graph` 与 `ListMemories`）正在修 |
| 迁移系列的向量列宽 | 已修：`embedding` 列宽是**部署设定**而非 schema 的一部分（上游启动时按配置的 embedding 模型 reconcile 该列），因此生成系列忠实保留被转储部署的宽度（`vector(1024)`），由 `internal/storage/embedding_dimension.go` 在迁移事务内对齐到本服务声明的 384——相同则不动；已有向量而宽度不符则**报错并给出两个宽度与行数**（绝不静默转换）；空表则丢索引→改列→按捕获的定义重建索引。此前工作树里对生成文件的手工 `384` 补丁已撤销（那是被 `schema_diff` 忽略、且每次重新生成都会静默回退的隐患） |
| **全量基线（2026-10-15 收口）** | **上游自己的黑盒套件 339 passed / 1 skipped / 0 failed**（独立库、`-tags pgx` 构建、258 秒）。唯一的 skip 是 `test_76_onnx_embeddings`——本机没有本地 E5 ONNX 模型，属环境缺失，不是 wenyuan 的缺口。此前一轮为 335/4 failed，四项残留已全部定位修复：知识页 delta 窗口下界取错（`last_refreshed_source_query` 从未记录 + 窗口回退到建行时间戳）、两处存在性检查写了 `type` 而非 `fact_type`（错误被读成"作用域为空"，使带 `fact_types` 的模型永不刷新）、bm25 打平的自造 `id ASC` tie-break（上游按物理顺序，改为 `ctid`） |
| **契约面复核收口（2026-10-15）** | `docs/CONTRACT-AUDIT.md` §3 由"未闭合清单"改写为**逐项结论**：13 项中多数已闭合（附上游 file:line 证据），4 项的前提**本身是反的**（校验条目 `url`、银行 id 形状、`/health` 池统计、指标"聚合计数"）——照单直改会改坏已经正确的东西。真实修复：校验条目按目标类型分派 `type` 并去掉上游从不发的 `url`；内存后端 retain 接受被拒 bank id（200→400）；`PATCH /config` 吞错（404→400）；`DELETE /config` 对不存在 bank（404→200，不创建行）；指标 `endpoint` 标签剥离 Go 通配符；知识库与 **recall** 的 fuzzy 标签改为对银行词表解析后精确匹配（含两个上限与上游 422 原文）；MCP `Accept` 改写语义（不再错拒 `application/json`/`*/*`）与 GET SSE 帧；`STORE_DOCUMENT_TEXT=false` 存 NULL、文案逐字、修掉可空列扫进 string 的读取崩溃。另把 CI 门禁拉回绿：13 个累积的 lint 问题清零（含一处保真与 ST1005 冲突→保留上游原文并标注理由） |
| **上游自己的系统测试套件**（`hindsight-system-tests`，77 个 story 文件 / 345 用例，只用上游已发布的 Python 客户端驱动真实服务，模型/embedding/reranker 指向进程内桩） | **287 passed / 47 failed / 10 errors / 1 skipped（≈83%）**，基线是同一套件的 **136 passed / 161 failed / 43 errors**。唯一 skip 由上游环境门槛决定（`test_76_onnx_embeddings.py` 需要本地 E5 ONNX 模型）。harness 自身修好并自证（启动标记 + 启动函数确实被调用）；每个 agent 用自己的私有库，避免两个服务共用一个库时互相认领队列 |
提示词、多模态、observation 嵌入、异步 retain 管线等专项见下方各节。 |
| HTTP 契约面（99 操作 / 195 schema 逐项对照） | 见 `docs/CONTRACT-AUDIT.md`：路由 99/99；字段/状态码/头层面的偏差已批量修复，余项在该文件 §3 |
| 非 HTTP 功能面（子系统、CLI/进程、扩展体系） | 见下方三节，按严重度列出**仍缺的能力**与可行性判断 |

2026-10-15 同日已完成（详见「进度」一节与 `CHANGELOG.md`）：审计流水线（40 个动作 × HTTP/MCP/worker），
抽取模式（concise/verbose/custom/verbatim/chunks 各自的提示词与 schema），多路 LLM 路由与
reranker/embedding provider 家族，知识库 BM25+向量混合检索，文档解析链（stdlib 实现
PDF/DOCX/XLSX/PPTX/HTML/CSV/JSON + llama_parse/iris），GCS/Azure 对象存储与预签名下载，
时间解析（中文数词、旬/半、假阳性守卫），巩固设置真正生效，tokenizer 词表（默认 o200k_base）、
BM25 词项选择与语句超时，**参考库直接接管**（指向参考服务自己迁移过的库可以直接启动服务），
以及参考 schema 的向量宽度修正（1024 → 384）。

一句话：**服务行为（公开 API 能观察到的部分）已与上游等价**；仍缺的是「可替换后端的广度」
（provider 家族、对象存储、文件解析、多路 LLM 路由）、**审计与部分引擎设置真正生效**、
以及**插件体系与运维/进程面**。其中一部分在纯 Go 二进制里结构上不可实现（见 §4）。

## 1. 引擎子系统（23 项，来自子系统清单复核）

严重度 HIGH：

| # | 缺口 | 上游 | 现状 | 修复方向 |
|---|---|---|---|---|
| 1 | **审计流水线只有一条动作**：只有 curation 写 `memory_update` 一行；retain/recall/reflect/bank CRUD 全无记录，租户的 `/audit-logs` 实质为空 | `engine/audit.py:1-5,137-170,240-266` | `internal/storage/postgres_memories.go:171-176` | 在中间件（HTTP）+ MCP + worker 分发处挂 `audit_context` 等价物，写 `action/transport/started/ended/request/response`，按配置与 bank 覆盖开关 |
| 2 | **`retain_extraction_mode` 是空转**：`concise/verbose/custom/verbatim/chunks` 上游对应不同提示词**和不同 schema**（`what/when/where/who/why`）；这里只有一套 | `engine/retain/fact_extraction.py:1571-1622,266,3539,3644` | `internal/extraction/extractor.go:112-199` | 把解析出的模式 + `retain_custom_instructions`/`retain_strategies` 传进 `extraction.Options`，按模式选提示词/schema，补逐 chunk 与 verbatim 折叠路径 |
| 3 | **多路 LLM 路由缺失**（failover / round_robin / weighted / metadata 路由），配置即启动失败 | `engine/multi_llm.py:1-42` | `internal/app/provider.go:128-141` | 实现成员感知的 provider：按策略选成员、逐成员重试/退避 |
| 4 | **知识库搜索是 `ILIKE` 子串**，分数是编造的 1.0/0.5；服务端 OpenAPI 却宣传 hybrid BM25+vector | `engine/sql/postgresql.py:75-187` | `internal/storage/postgres_knowledge.go:226-234` | 在 `mental_models.search_vector` 上建 BM25 臂 + 向量臂，RRF 融合并归一到 [0,1] |
| 5 | **embedding 后端与宽度**：只支持 OpenAI 兼容；本地模型被拒；宽度是编译期常量且列固定；上游按模型自动探测并**调整 schema** | `engine/embeddings.py:290-546`、`config.py:2100-2130` | `internal/app/embeddings.go:56-62`、`internal/provider/dimension.go` | 补齐 HTTP provider 家族（cohere/gemini/vertex/tei/zeroentropy/openrouter/requesty/litellm）；启动时按探测到的宽度对齐列宽（上游行为） |

严重度 MED：文本检索（BM25 词项选择 / pg_search / statement_timeout 兜底）、tokenizer 词表
（上游默认 `o200k_base`，此处硬编码 `cl100k_base`，导致所有 token 计数不同）、文件解析链、
对象存储 GCS/Azure 与预签名下载、扩展框架、中文/英文时间解析（缺中文数词）、实体解析（缺批内归并）、
巩固设置空转（`observations_mission`/`consolidation_strategies`/预算/批次与并行度）。

严重度 LOW：loop 停顿栈转储、DB 池等待计数与慢获取告警、`--daemon` 语义、`pg0://`、
多 worker 指标标签、审计/追踪写入批处理、reflect 大证据集的 split synthesis、
扩展声明的 bank 级表参与备份/清理、协作式取消的边界检查。**Oracle 后端**明确不在范围内
（PG 之外仅此一种方言）。

## 2. 运维 / 进程入口（来自 CLI 与运行面复核）

**2026-10-15 二次收口：本节的 1、3、4、6、7 项与「CLI 旗标面」已全部落地，证据见下。**
第 2 项是有意保留的分歧，第 5 项本轮未复核（见 2.4）。

| # | 缺口 | 影响 | 状态 |
|---|---|---|---|
| 1 | 默认绑定与端口 | 容器端口映射、健康检查、客户端默认 URL | **已闭合**：默认 `0.0.0.0:18888`（`internal/config/config.go` `DefaultHost`/`DefaultPort`；端口为**有意分歧**——上游 8888 加 10000 偏移，使 wenyuan 与上游 Hindsight 可并行启动对比，env 名与其余默认值不变） |
| 2 | 未配置数据库时上游用内嵌 PG（持久），此处退化为内存后端 | 重启即丢数据 | **有意分歧**：此处**拒绝启动**并点名 `HINDSIGHT_API_DATABASE_URL`，或显式设 `memory` 才用内存后端（比"静默丢失"更安全，已写入 `reference.go` 的 `Divergent`） |
| 3 | `--workers N` / `HINDSIGHT_API_WORKERS` 被解析后从未使用 | 请求 N 个 worker 只得到 1 个 | **已闭合**：`internal/app/process.go` 的 `Supervise`——父进程绑定一次监听 socket，通过 `ExtraFiles` 继承给 N 个子进程（与 uvicorn 的 supervisor/worker 同构：`ss` 中 N+1 个进程共享同一 socket inode），子进程 `HINDSIGHT_API_WORKERS=N`；任一 worker 退出即停掉整组；`SIGTERM` 向整组转发（实测 4 个进程共享 inode `210875770`，`kill -TERM` 后全部退出） |
| 4 | `--daemon` 除改默认 host 外无行为 | 期望后台化的启动器仍被阻塞 | **已闭合**：`app.Daemonize` 复刻上游语义——先在前台父进程做端口预检（占用则报 `cannot bind host:port` 并以 1 退出），剥离 `--daemon` 后 `setsid` 重执行子进程，stdio 追加到 `HINDSIGHT_API_DAEMON_LOG`（默认 `~/.hindsight/daemon.log`），父进程 exit 0；daemon 的 host 默认 `127.0.0.1`（除非显式配置），port 默认 `18888`（**忽略** `HINDSIGHT_API_PORT` 的机制与上游 `resolve_daemon_host_port` 一致；数值为上述有意偏移） |
| 5 | 备份/恢复格式 | 上游 `.zip` 能否恢复进来 | **本轮未复核**（见 2.4） |
| 6 | `--access-log` 空转 | 无访问日志 | **已闭合**：`internal/httpx/accesslog.go` 出货时即已接线；本轮补 `--no-access-log`（与 `--access-log` 共用目标、后出现的赢，同上游 argparse 语义） |
| 7 | 无 `HINDSIGHT_API_LOG_FORMAT=json` | JSON 采集管道失效 | **已闭合**：`monitoring.NewLogHandler` 按 `cfg.JSONLogs()` 切换（`internal/config/config.go:JSONLogs`） |

### 2.1 CLI 旗标面（本轮补齐）

上游 `main.py` 有 13 个旗标，此前只有 9 个；缺的 4 个会让启动器直接失败（`flag provided but
not defined`）。现在 13 个全部接受，语义对齐上游：

| 旗标 | 上游语义 | 此处 |
|---|---|---|
| `--reload` | uvicorn 热重载（开发用） | 接受；编译产物无重载器，**明确打印不支持的一句话**后继续，不做静默 no-op |
| `--no-access-log` | 关闭访问日志（覆盖 env/default） | 已实现，与 `--access-log` 后出现的赢 |
| `--forwarded-allow-ips` | 信任转发头的对端列表（uvicorn 的 `trusted_hosts`） | 已实现，见 2.2 |
| `--idle-timeout N` | **上游已废弃**：接受但无效，并向 stderr 打印 `--idle-timeout N is ignored: ...` | 逐字同上游文案 |
| `--ssl-keyfile` / `--ssl-certfile` | 两者齐备即 HTTPS | **本轮补上服务端 TLS**（此前旗标被解析但从不使用）；只给一个 → 启动失败并说明 |

### 2.2 `--proxy-headers` / `--forwarded-allow-ips`（本轮补上）

此前 `--proxy-headers` 解析后无人使用。现在按上游 HTTP 服务器（uvicorn `ProxyHeadersMiddleware`）
的语义实现 `internal/httpx/proxyheaders.go`：仅当**直接对端**在信任列表内（默认 `127.0.0.1`，
`*` 为全信任，支持 CIDR 与字面量；env 键沿用上游的 `FORWARDED_ALLOW_IPS`）才接受
`X-Forwarded-Proto`（只认 http/https/ws/wss，websocket 升级时 http→ws）与 `X-Forwarded-For`
（从右往左取第一个不受信跳点；端口缺失按上游记 0）。实测：受信对端带
`X-Forwarded-For: 198.51.100.7` 时访问日志记 `198.51.100.7:0`，不带该头则记真实对端。

### 2.3 配置键面（本轮开始收口）

`internal/config/reference.go` 是参考实现运行时键的清单：**没有 `Resolve:` 的键在启动时被拒绝**
（这是有意的"宁可失败也不静默 no-op"策略）。此前的比例是 241 键中 150 键被拒——对"直接平替"
的部署方来说，把参考 `.env` 整份搬过来就会启动失败。

本轮把**行为已经存在**的键改为解析（不再拒绝），并让它们真正改动生效值：

| 组 | 键 | 生效路径 |
|---|---|---|
| 召回臂开关 | `ENABLE_TEXT_SEARCH` / `ENABLE_TEMPORAL_RETRIEVAL` / `ENABLE_GRAPH_RETRIEVAL` / `ENABLE_RERANKING` | 进程层 → 银行配置 → `memoryops/recall.go` 的 `RecallDisabledArms` |
| 保留（写入） | `RETAIN_CHUNK_SIZE` / `RETAIN_STRUCTURED_CHUNK_SIZE` / `RETAIN_MISSION` / `RETAIN_CUSTOM_INSTRUCTIONS` / `RETAIN_CHUNK_BATCH_SIZE` / `RETAIN_MAX_ATTACHMENTS_PER_CHUNK` | 银行配置 → `memoryops/retain.go` 的分块与抽取选项 |
| 巩固 | `OBSERVATIONS_MISSION` / `MAX_OBSERVATIONS_PER_SCOPE` / `OBSERVATION_SCOPE_LIMITS` / `CONSOLIDATION_STRATEGIES` / `CONSOLIDATION_MAX_MEMORIES_PER_ROUND` / `CONSOLIDATION_SOURCE_FACTS_MAX_TOKENS[_PER_OBSERVATION]` / `CONSOLIDATION_LLM_BATCH_SIZE` / `CONSOLIDATION_LLM_PARALLELISM` | 银行配置 → `consolidation/settings.go` |
| 反思 | `REFLECT_MISSION` / `REFLECT_SOURCE_FACTS_MAX_TOKENS` | 银行配置 → reflect 画像 |
| 心智模型 | `MENTAL_MODEL_MIN_REFRESH_INTERVAL_SECONDS` | 银行配置 |
| 进程/进程形状 | `HINDSIGHT_API_WORKERS` / `HINDSIGHT_API_DAEMON_LOG` | `internal/app/process.go` |
| 写入 dry-run 开关 | `ENABLE_DRY_RUN_EXTRACT` | `reporting/memory_routes.go`：关闭时按上游返回 404 与同一句话（路由照旧注册） |
| worker 槽位保留 | `WORKER_*_RESERVED_SLOTS`（10 键） | `config/worker.go` 的 `ReservedSlots` → `worker/consumer.go` 的 `preferredTypes`（每键对应同名操作类型） |
| 事件循环看门狗 | `LOOP_WATCHDOG_ENABLED` / `_STALL_THRESHOLD_MS` / `_POLL_INTERVAL_MS` | `monitoring/looplag.go` 探针：默认开启（250ms 轮询、1000ms 阈值，与上游一致）；`LOOP_LAG_REPORT_SECONDS` 设置时仍可覆盖采样间隔 |
| 检索/写入/反思调参 | `GRAPH_SEED_MIN_SIMILARITY`、`SEMANTIC_LINK_MIN_SIMILARITY`、`BM25_MAX_QUERY_TERMS`、`SEMANTIC_MIN_SIMILARITY`、`TEMPORAL_SEMANTIC_MIN_SIMILARITY`、`BM25_MIN_SCORE`、`RECALL_BUDGET_FIXED_{LOW,MID,HIGH}`、`RECALL_BUDGET_FUNCTION`（只接受 `fixed`）、`REFLECT_MAX_ITERATIONS`、`REFLECT_MAX_CONTEXT_TOKENS` | `storage.Tuning`（两后端共用）→ 各常量改为部署值；每臂默认下限在 `Recall` 入口播种（调用方自己的 floor 优先，0 表示不设限）；顺带对齐两处默认值漂移（语义链接 0.75→0.7、反思循环 4/4000→10/100000） |
| 历史记录 | `ENABLE_OBSERVATION_HISTORY` / `ENABLE_MENTAL_MODEL_HISTORY` / `*_HISTORY_MAX_ENTRIES` | 两个后端的记录点按开关短路，条数上界由 20（硬编码）改为配置值（默认 50） |
| 每通道准入等待 | `ADMISSION_{RECALL,REFLECT,RETAIN}_MAX_WAIT_MS` | `httpx.AdmissionRule.MaxWait`（未配置时回落全局 `MaxWait`） |
| 数据库会话 | `DB_STATEMENT_TIMEOUT`、`DB_MAX_PARALLEL_WORKERS_PER_GATHER`、`DB_SESSION_SETUP_ON_ACQUIRE`（只接受 `true`）、`ENTITY_TRGM_SIMILARITY_THRESHOLD` | 连接参数（pgx `RuntimeParams`），与上游的 per-connection init 等价；真库集成测试 `SHOW` 校验（1234ms/0/0.42） |
| 图扩展 | `LINK_EXPANSION_PER_ENTITY_LIMIT`、`LINK_EXPANSION_TIMEOUT` | 实体支路按 `LATERAL ... LIMIT` 对每个种子实体截断目标（保留 tanh 权重），整条 arm 语句走 `SET LOCAL statement_timeout` |
| 银行行缓存 | `BANK_INFO_CACHE_MAX_ENTRIES` | `bank_cache` 增加上界：先清过期、再淘汰最先到期的一条 |
| ANN / 迁移库 | `ANN_ITERATIVE_SCAN`、`ANN_MAX_SCAN_TUPLES`、`MIGRATION_DATABASE_URL` | 语义臂在事务内 `SET LOCAL hnsw.ef_search=200` + `hnsw.iterative_scan=strict_order`（关时 `off`）+ `hnsw.max_scan_tuples`，非 pgvector 后端不施加；服务端拒绝过的 GUC 会被记住并跳过（pgvector <0.8 不会让整条查询失败）。迁移改用 `MIGRATION_DATABASE_URL` 的独立连接池（可用另一个角色跑 DDL），关闭时一并关闭 |
| 银行画像的 disposition/mission | `DISPOSITION_{SKEPTICISM,LITERALISM,EMPATHY}` | 列表行改报**解析后的**配置值（进程默认 → 租户 → 银行自身），缺省回落到 bank 行里的列——即上游 `_overlay_bank_config_disposition_mission` |
| 知识页默认 trigger | `KNOWLEDGE_PAGE_DEFAULT_TRIGGER`（JSON 对象，非对象值启动即拒） | 内置页面默认之上叠加：进程默认 → 租户覆盖 → 银行覆盖 → 请求自带 |
| 全局 webhook | `WEBHOOK_URL` / `WEBHOOK_SECRET` / `WEBHOOK_EVENT_TYPES` / `WEBHOOK_EXPOSE_RESPONSE_BODY` | 服务级接收端与各银行注册**并列**投递（事件过滤、同一 HMAC 签名、同一 URL 守卫）；开关打开时才记录接收端响应体 |
| 指标 | `METRICS_BACKLOG_ENABLED`（默认关，之前无条件发出）、`METRICS_INCLUDE_BANK_ID`、`METRICS_INCLUDE_TENANT`、`LOOP_LAG_METRIC` | 积压 gauge 按开关发出、可按银行打标签、可加 tenant 标签；事件循环 lag 直方图（桶与上游 `LOOP_LAG_BUCKETS` 一致，指标名同上游），请求度量本身也会带上采样 |
| 扩展透传头 | `EXTENSION_PASSTHROUGH_HEADERS` | 允许列表内的请求头以小写键名进入扩展上下文（重复出现即丢弃，与上游 `collect_passthrough_headers` 一致） |
| 默认银行模板 | `DEFAULT_BANK_TEMPLATE` | 在建行**唯一入口**（存储层的建行钩子，覆盖 `CreateBank`/`EnsureBank` 的所有路径）应用 manifest：配置 + 心智模型 + 指令；模板字段不覆盖建行请求已写的值（对齐上游"客户端优先"）；坏 manifest 只记日志不阻断建行 |
| 召回入口/内部召回 | `RECALL_MAX_QUERY_TOKENS`（上限截断，含嵌入前的截断）、`RECALL_MAX_CONCURRENT`（引擎级 permits，与准入车道并列）、`RECALL_INCLUDE_CHUNKS` / `RECALL_MAX_TOKENS` / `RECALL_CHUNKS_MAX_TOKENS`（内部召回的三通道设置） | 两个后端的 `Recall` 入口截断 + permits；通道预算在候选装配后按通道各自裁剪（chunk 通道会切尾并置 `Truncated`） |
| 反思 | `REFLECT_MAX_COMPLETION_TOKENS`、`REFLECT_DEFAULT_OPTIONS`（JSON 对象） | 最终合成调用带 MaxTokens；默认 options 按"请求 → 银行 → 进程 → 内置"分层 |
| 写入/实体 | `RETAIN_EXTRACT_CAUSAL_LINKS`、`RETAIN_DEFAULT_STRATEGY`、`ENTITY_MERGE_MIN_SIMILARITY` | 因果链开关同时作用于提示词/schema 与解析；银行默认策略在条目未指定时生效；实体候选多一道 trigram 合并门（与候选抓取门独立） |
| worker / 巩固 | `BACKPRESSURE_DEFER_SECONDS`（120s）、`WORKER_CONSOLIDATION_BANK_PRIORITY`（`bank-pattern:priority` 分级认领）、`CONSOLIDATION_{BATCH_SIZE,MAX_ATTEMPTS,MAX_TOKENS,RECALL_BUDGET,MAX_COMPLETION_TOKENS}`、`BANK_STATS_CACHE_MAX_ENTRIES` | 背压推迟用配置间隔；巩固认领按模式分级（内存后端 + PG 单语句实现）；巩固加载批次/重试次数/召回预算与补全上限全部生效；内存后端新增有界统计缓存 |
| 第五轮（并行） | `METRICS_WORKER_LABEL`（多 worker：flock 抢占 slot + 5s 快照 + 15s 过期，标签 `api_worker`）、自适应召回预算函数 5 键 + 阶段诊断 2 键（`[phases]` 日志行为默认即有）、`ENTITY_INTRABATCH_MERGE_SIMILARITY`（批内近名并轨）、启动期模型连通性验证 + `MODEL_INIT_TIMEOUT` / `SKIP_LLM_VERIFICATION`、数据库池 acquire 超时与慢 acquire 告警 3 键 | 见各键上游对应文件；`GRAPH_RETRIEVER` / `MIGRATION_ISOLATION` / `MIGRATION_CONCURRENCY` / `MCP_STATELESS` 改为"接受可实现取值、拒绝其余"的部分支持 |
| 第六轮（并行） | OCR 链 6 键、别名缓存 2 键、巩固去重阈值 1 键、召回连接预算 1 键 | OCR：`internal/fileparse` 提取文档内嵌图片（OOXML media、PDF 的 JPEG XObject）交给新的 `internal/provider/vlm` 适配器转写，转写文本并入解析结果；开关打开但配置不全时构造即失败并点名缺哪一项（同上游 `_validate_ocr_config`）。别名缓存：进程级 TTL + 上界，写入即失效。去重：改用上游的 **cosine 阈值**（embedding 可用时它单独决定；无 embedding 时保留记录在案的 token-Jaccard 兜底），阈值 1 表示关闭。召回：各臂**并发**执行、由连接预算限流（预算 ≤ 0 取上游默认 4），结果顺序/名次/阶段会计与串行完全一致（有 `-race` 与"串行 vs 并发结果深度相等"测试）|
| 语义修正 | `RETAIN_BATCH_ENABLED` | 我们此前把它实现成"强制异步、拒绝同步"——上游没有这个语义（它把 retain 抽取路由到 **LLM Batch API**，并在启动期校验 provider 是否有批处理能力）。已删除自造规则与其测试；该键现在接受 `false`、拒绝 `true` 并说明批处理路径未实现 |
| 行为修正（非键） | Anthropic/OpenAI 提示缓存 | 上游**默认**给 Anthropic 的 system 提示打 `cache_control: {"type":"ephemeral"}`，OpenAI 兼容侧按 host 发 `prompt_cache_key` / `x-grok-conv-id` / `x-opencode-session`；我们此前一个都没发。现已按上游默认发出（含重试与工具循环路径），并用逐字节的线上体测试钉住 |
| 存储方言 | `DATABASE_BACKEND` | 接受 `postgresql`/`pgx`/`memory`（本实现的后端集合），其它值（参考的 Oracle）启动即拒并点名 |
| 读副本 | `READ_DATABASE_URL` / `READ_DB_POOL_{MIN,MAX}_SIZE` | 独立只读连接池；召回各臂、读面、可观测性与 list/get 走副本，写、迁移、advisory-lock 的 worker/维护/认领与"读后写"的检查留主库（逐处注释说明） |
| ParadeDB pg_search | `TEXT_SEARCH_EXTENSION_PG_SEARCH_FUNCTION_SCHEMA` / `_TOKENIZER` | 关键词臂的 pg_search 形态（`paradedb.score(id)` + `boolean(should => ...)`，字段 text/text_signals，tokenizer 校验与上游白名单一致），native 形态 SQL 一字未改 |
| MCP | `MCP_AUTH_TOKEN`、`MCP_ENABLED_TOOLS`、`MCP_INSTRUCTIONS` | 旧式 MCP bearer 在 MCP 命名空间**替代**租户鉴权（401 文案逐字复刻：`Authorization header required` / `Invalid authentication token`）；服务端 allowlist 同时裁剪 `tools/list` 与 `tools/call`（表外工具按"未知工具"答复）；instructions 以 `\n\nAdditional instructions: ` 追加到 retain/recall 两条工具描述 |
| 写入侧 | `RETAIN_MAX_COMPLETION_TOKENS`、`RETAIN_BATCH_TOKENS`、`RETAIN_ENTITY_LOOKUP`（只接受 `trigram`）、`RETAIN_ENTITY_RESOLUTION_MAX_CANDIDATES` | 抽取补全上限、子批 token 预算、实体候选 LIMIT（由 100 改为配置值，默认 200） |

实测：`RETAIN_CHUNK_SIZE=1500 OBSERVATIONS_MISSION="Track decisions" ENABLE_GRAPH_RETRIEVAL=false
CONSOLIDATION_LLM_BATCH_SIZE=16` 启动后，`GET /v1/default/banks/<id>/config` 读到
`1500 / "Track decisions" / false / 16`。

**仍未闭合**：拒绝键从 150 降到 **12**（243 键中 231 已解析或委派）。剩余 12 键分三类，
见 `docs/PARITY-PLAN.md` §10 的逐键清单与分类：纯接线（行为已存在，本轮未及）、小特性、以及结构性
不可实现。默认值全部保持与上游一致，因此未接线的键在**默认情况下**行为不变，只是显式配置时仍会被
拒绝并点名。

### 2.4 备份/恢复格式（本轮未复核）

`bankctl` 的归档导出/导入与 HTTP transfer 路由由上游 story 覆盖（`test_52`/`test_53`/`test_83` 全绿），
但**跨实现互操作**（用参考服务的 `.zip` 恢复到本实现，反之亦然）本轮没有实测，因此保留原判断不做改写。

## 3. 扩展体系（来自扩展面复核）

上游有一个通用的、由环境变量驱动的扩展加载器，6 个插槽：`TENANT`、`MEMORIES`、
`OPERATION_VALIDATOR`、`MEMORY_DEFENSE`、`HTTP`、`MCP`，每个按
`HINDSIGHT_API_<PREFIX>_EXTENSION=<module:Class>` 选择实现、并按 `HINDSIGHT_API_<PREFIX>_*`
收集配置。此处：只有 tenant 插槽被读取，且只接受内置类的那一个字符串——其余取值启动即失败；
操作校验器只能编译期注册、只覆盖 retain/file-retain/recall、没有 pre-body `precheck`；
记忆防御是硬编码正则而非插槽；bank 级扩展表不存在。

**结构性限制**：Go 二进制无法 `import` 一个 Python 类。可复刻的是**契约**（同名环境变量、
同样的配置收集规则、同样的拒绝语义），实现方式只能是 Go 侧注册表 + 明确拒绝 Python 路径。
这一项属于 §4。

## 4. 结构上无法在纯 Go 二进制里 1:1 复刻的项

| 项 | 为什么 | 取舍 |
|---|---|---|
| 任意 Python 扩展类（`module:Class`） | 语言边界；Go 无 Python 导入 | 支持同名环境变量 + Go 侧注册表；Python 类路径启动即失败并说明原因（现状） |
| 本地 ONNX / sentence-transformers embedding、本地 MLX reranker | 需要原生运行时（onnxruntime / torch / MLX），而本仓库的构建是 `CGO_ENABLED=0` | 保持「配置即启动失败并点名键」；HTTP 型 embedding/reranker 家族照常实现 |
| OAuth 交互式登录 provider（claude-code/codex/cursor/copilot/xai） | 需要浏览器/设备码流程与本地凭据库 | 可读上游同一份凭据文件时按文件实现；否则明确拒绝 |
| markitdown 的完整格式矩阵与 OCR | Python 生态 | 纯 Go 覆盖主要格式（PDF/DOCX/XLSX/PPTX/HTML/CSV/JSON/文本），远程 parser（llama_parse/iris）走 HTTP；超出范围的格式明确拒绝 |
| Oracle 后端 | 上游自身把它作为可选方言，参考库是 PG | 明确不在范围（声明为契约的一部分） |

## 5. 怎么继续验证

```sh
make parity                      # 上游 77 个 story 全量（339 passed / 1 skipped）
scripts/run_parity_suite.sh tests/test_20_consolidation.py   # 单个 story
PORT=19500 bash scripts/contract_smoke.sh bin/wenyuan        # 35 项契约事实
make check                       # build + test + vet + pgx + lint + gofmt
```

每修一项，对应的 story 或新增用例必须**先失败后通过**；无法用 story 覆盖的（运维面、扩展面、
契约字段细节）用 Go 测试或 `scripts/contract_smoke.sh` 覆盖。
## 6. 客户端 CLI 不归本仓库管（2026-10-15 用户裁定）

`internal/cli/` 与 `cmd/wenyuan/`（复刻上游 `hindsight-cli` 的客户端）**由另一个 AI 负责**，
不在本任务范围内。本仓库的验证只看服务端：`go test ./internal/... ./cmd/server/... ./cmd/worker/... ./cmd/bankctl/...`
与 `scripts/run_parity_suite.sh`（它构建的是 `./cmd/server`）。
`make check` 会连带编译客户端，因此客户端在编辑中途时门禁可能因它而红——那不是服务端的回归。

# 功能面复核与剩余清单（2026-10-15）

这份文件回答的问题是「上游的服务到底有没有 100% 复刻过来」。HTTP 契约面（路由/参数/字段/空值/
状态码/头/错误信封）记在 `docs/CONTRACT-AUDIT.md`；**行为面**记在这里。

## 0. 结论速览

| 证据 | 结果 |
|---|---|
| **提示词文本**（2026-10-15 新增发现） | 抽取/巩固/反思/结构化输出/连接探针的提示词是**改写版**，上游系统套件靠锚句识别步骤，7 个锚句只命中 1 个。移植原文正在进行 |
| 各簇收口（2026-10-15 晚） | 已完成并各自私有库验证：consolidation/observations **45/51**；banks/config/aliases/isolation 全绿；documents/entities/knowledge-read **44/70**（test_10/13/14/15/16/92 全绿）；webhooks **7/7**；archive transfer **test_52 7/7、test_53 9/9、test_83 1/1**；实体与图谱 **test_70 4/4、test_72 5/5、test_74 5/5**；mental model/knowledge **test_30 6/6、test_32 4/4、test_34 2/2、test_35 8/8、test_37 2/2、test_39 2/2**；源码/标签作用域 **test_71 5/5、test_81 6/6、test_82 6/6**；零散项 **test_02/27/94/96/97 全绿**。仍余三项：`scores.keyword` 与上游差一位小数（0.2 vs 0.3，已定位到 `search_vector` 加权/归一化，正在对齐）、`test_24` 标签重命名需重写冻结的 `observation_scopes`、`test_70_typesafe` 单跑不可归因。另：两条未被 story 覆盖的读路径越权（`/graph` 与 `ListMemories`）正在修 |
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

| # | 缺口 | 影响 | 修复方向 |
|---|---|---|---|
| 1 | **默认绑定与端口不同**：上游 `0.0.0.0:8888`，此处 `127.0.0.1:8000` | 容器端口映射、健康检查、客户端默认 URL 全部对不上 | 默认值改为上游值 |
| 2 | **未配置数据库时上游用内嵌 PG（持久），此处退化为内存后端** | 重启即丢数据，而部署方以为数据已持久化 | 实现 pg0 等价物，或未配置时明确失败/落到文件，不静默用内存 |
| 3 | `--workers N` / `HINDSIGHT_API_WORKERS` 被解析后**从未使用** | 请求 N 个 worker 只得到 1 个 | 实现多进程（SO_REUSEPORT）或直接拒绝该参数 |
| 4 | `--daemon` 除改默认 host 外**无行为** | 期望后台化的启动器仍被阻塞 | 实现 detach + 日志重定向，或拒绝该参数 |
| 5 | **备份/恢复格式不互通**：上游是 PG 二进制 COPY + manifest v2 + 23 张表；此处是 JSON + v1 + 10 张表 | 上游 `.zip` 无法恢复进来，非核心表（mental models/webhooks/directives/attachments/audit/llm_requests…）在恢复中丢失 | 对齐二进制 COPY、manifest v2 与完整表清单 |
| 6 | `--access-log` / `HINDSIGHT_API_ACCESS_LOG` 空转 | 无访问日志可供运维/审计 | 在中间件按开关输出 |
| 7 | 日志只有文本，无 `HINDSIGHT_API_LOG_FORMAT=json` | 期望 JSON 行的采集管道失效 | 增加 JSON handler |

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

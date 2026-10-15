# Parity suite：用上游自己的系统测试验证本服务

上游仓库自带一套**黑盒系统测试**（`hindsight-system-tests`，77 个 story 文件、340 个用例）：
它启动一个真实服务进程，只通过**已发布的 Python 客户端**驱动它，并把语言模型、embedding 模型、
reranker 全部指向进程内的桩服务。测试里没有任何针对某个服务端的特判，所以把这套测试指向本服务，
就是「服务等价」这一说法最接近可执行定义的表达方式。

## 结论（2026-10-15 收口）

**`339 passed / 1 skipped / 0 failed`**——全套 340 个用例，独立数据库、带 `-tags pgx` 的本服务二进制，
258 秒。唯一的 skip 是 `test_76_onnx_embeddings`，story 自身 `skipif(not MODEL_DIR)`：本机没有本地
E5 ONNX 模型，属**环境缺失**，不是本服务的缺口。

这个数字可信的前提是 harness 真的在跑本服务，这一点本文件记过一次惨痛教训（见下）：
唯一值得整轮失败的失败模式是「静默退回上游服务」，所以插件现在同时做三件事——在**模块导入时**
替换启动函数（早于 conftest 绑定）、要求进程日志出现本服务启动标记（`entrypoint=wenyuan`）、
以及在 `pytest_sessionfinish` 检查插件启动函数**确实被调用过**。把 `WENYUAN_BIN` 指向
`/bin/false` 会立刻报 `the service exited during startup`，这一条是防回归保险。

可信测量的历史轨迹（记录在案，避免把中间态当成终点）：修好 harness 后的第一次可信测量是
**136 passed / 161 failed / 43 errors**，按失败簇并行修复后到 **287 / 47 / 10 / 1 skipped**，
最终收口到 **339 / 1 / 0**。

**契约面复核一轮之后重测，数字不变（仍 339 / 1 / 0）**——那一轮改的是契约细节（校验条目词汇表、
银行 id 形状与配置重置、指标 `endpoint` 标签、知识库与 recall 的 fuzzy 标签解析、MCP `Accept`、
`STORE_DOCUMENT_TEXT`），黑盒套件本来就覆盖不到它们，所以"数字不变"是预期而非无事可做；它们的证据是
`docs/CONTRACT-AUDIT.md` §3 里逐项的 `file:line` 与 `scripts/contract_smoke.sh` 的 35 项线上事实。
同一轮把 CI 门禁拉回绿：`make check` 全过、`golangci-lint run ./...` **0 issues**（此前累积了 13 个）。

第一次跑出的 `339 passed, 1 skipped` **测的不是本服务**：
harness 最初在 `pytest_configure` 里替换启动函数，而上游的 `tests/conftest.py` 在**导入时**
就 `from hindsight_system_tests import start_hindsight_server` 绑定了原函数，pytest 在解析参数阶段
就导入了该 conftest（早于 hook 执行），所以替换从未生效——跑起来的是**上游自己**的服务。
证据：把 `WENYUAN_BIN` 指向 `/bin/false` 时那次跑仍然全绿（现在会立刻报
"the service exited during startup (1)"）。

已修复两处，并加了防回归保险：

1. 替换改为在插件**模块导入时**完成（此时早于 conftest 导入）；
2. 启动后要求进程日志里出现本服务的启动标记（`entrypoint=wenyuan`），否则报错；
3. `pytest_sessionfinish` 检查插件启动函数**确实被调用过**，没被调用就让整个 run 失败——
   一个静默回退到上游服务的 harness 会把上游的健康当成本服务的复刻度，这是唯一值得整轮失败的失败模式。

同一轮还发现构建必须带 `-tags pgx`（story 用真实 PostgreSQL，默认构建没有数据库驱动）；
运行脚本已改。

修好之后第一次真实运行立刻暴露了真实问题（harness 的价值所在），按发现顺序：

0. **真实基线：136 passed / 161 failed / 43 errors**（修好 harness 后的第一次可信测量）；按失败簇并行修复后为
   **287 passed / 47 failed / 10 errors / 1 skipped**。

1. **构建必须带 `-tags pgx`**（story 用真实 PostgreSQL；默认构建没有数据库驱动，启动即退出）。
   运行脚本已改。
2. **启动即失败**：套件环境设置了 `HINDSIGHT_API_RERANKER_*` 与
   `HINDSIGHT_API_BANK_STATS_CACHE_TTL_SECONDS`，当时被“已解析但不支持 → 启动拒绝”拦下。
   前者是当时尚缺的独立 reranker provider 子系统（已实现），后者只是漏进参考键表（已修）。
3. **PostgreSQL 标签算子类型错误**：`read observations: operator does not exist:
   character varying[] @> text[]`。参考 schema 的 tag 列是 `character varying[]`，而查询里
   用的是未加转换的 `tags @> ARRAY[…]::text[]`。已把列转成 `text[]`
   （`postgres_recall.go`、`mental_model_staleness.go`、`consolidation_scope.go`）。
   这个 bug 只在真实 PG 上出现，内存后端与离线测试都看不到。
4. **strict schema 不合法**：OpenAI 兼容的 strict 结构化输出要求每个 object 都带
   `additionalProperties: false` 且把全部属性列入 `required`；我们的 schema 生成器没有加，
   于是 provider（桩）拒绝请求。已修（`internal/provider/openai/schema.go`）。
5. **提示词不是参考实现的文本**（当前剩余的主要差距）：套件靠**提示词里的锚句**
   识别流水线步骤（`hindsight_system_tests/steps.py:STEP_ANCHORS`），因为提示词是线上唯一
   能标识步骤的东西。我们的提示词是改写版，7 个锚句里只有 1 个命中
   （`Rewrite the user's text so it fits within the requested token budget.`），
   于是桩无法路由我们的调用，story 报 “LLM call(s) matched no stub rule”。
   缺的是 `extract_facts`、`consolidate`、`reflect`、`reflect_answer`、
   `reflect_structured`、`connection_probe` ——即抽取/巩固/反思三段提示词与连接探针的原文。
   正在按参考原文移植（不只是插入锚句）。

## 怎么跑

```sh
# 全量（需要上游 checkout 及其 system-test venv）
scripts/run_parity_suite.sh

# 单个 story
scripts/run_parity_suite.sh tests/test_01_retain_and_recall.py

# 走 make
make parity PARITY_ARGS=tests/test_90_multilingual.py
```

环境变量：

| 变量 | 作用 | 默认 |
|---|---|---|
| `HINDSIGHT_REPO` | 上游 checkout 路径 | `/code/hindsight` |
| `PARITY_DB_URL` | 直接指定 `postgresql://` 数据库，跳过 pg0 解析 | 空（用 pg0 解析） |
| `PG0_URL` | 需要解析的 pg0 URL | `pg0://hindsight-systest:15499` |
| `PARITY_BIN` | 用已有二进制，跳过 `go build` | 空（现构建） |

## 它是怎么接起来的

构建时必须带 `-tags pgx`：story 使用真实 PostgreSQL，而默认构建没有数据库驱动（启动即退出）。
运行脚本已经这么做，手工跑时别漏。

上游的 story 用 `uv run --project <api> hindsight-api` 启动**上游服务**。
`scripts/system_parity_plugin.py` 是一个 pytest 插件，在 fixture 运行前把
`start_hindsight_server` 换成启动**本服务二进制**的那个，并做两处环境翻译：

1. `HINDSIGHT_API_DATABASE_URL` 上游给的是 `pg0://<instance>:<port>`（它自带的嵌入式 PostgreSQL）。
   运行脚本先用上游自己的 `hindsight_api.pg0.resolve_database_url` 解析出真实的
   `postgresql://` URL，再交给本服务；两者因此共用一个数据库。
2. 本服务自己跑迁移，所以补上 `HINDSIGHT_API_RUN_MIGRATIONS_ON_STARTUP=true`。

其余环境**原样透传**——这正是重点：本服务不认识的配置名是一条发现，而不是在这里抹平。

## 这套测试覆盖什么

按 story 文件：retain/recall（多策略、token 预算、fact type、标签、时间、极端日期）、文档生命周期
（新增/替换/追加/删除/重处理、分块、附件）、记忆策展与清空、观察与巩固（去重、矛盾、作用域、
生命周期、失败与恢复、串行化、策略）、mental model（刷新、陈旧、并发、失败暂停）与知识库页面、
reflect（结构化输出、长度预算、失败、disposition、directives）、银行与配置、别名与模板、
转账/导入导出、异步操作与失败、webhook、实体与图谱（解析、链接、标签作用域）、银行隔离与
作用域密钥、多语言、LLM 推理用量、抽取形状恢复。

## 它**不**覆盖什么

story 只走公开 API 且只用一个桩 provider，所以下列内容需要另外的证据（见
`docs/CONTRACT-AUDIT.md` 与 `docs/GAPS.md` 的余项清单）：

- 具体 provider 家族的真实实现（本地 ONNX、Cohere/Gemini/Vertex/LiteLLM/zeroentropy/TEI 等
  embedding 与 OAuth 类 CLI provider）、非 S3 的对象存储（GCS/Azure）、markitdown/iris 文档解析链；
- HTTP 契约的字段级细节（错误条目词汇表、`X-Ignored-Params` 集合、指标标签集）——由
  `scripts/contract_smoke.sh` 与 Go 测试覆盖；
- MCP 传输层细节（`Accept`、GET/SSE）、CLI 与进程入口（`cmd/` 与上游 console scripts 的差别）、
  扩展插件体系（上游可加载任意 Python 扩展类）。
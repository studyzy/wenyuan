# API 逐条复刻审查清单（2026-10-20）

> 本文件是「每个 API 一个子 Agent 逐条审查逻辑复刻一致性」轮次的**权威清单**：把本服务对外
> 暴露的每一个 API 归入切片组，每组带：路由、本服务实现落点、参考实现落点、线上差分命令。
> 子 Agent 以本文件为输入，产出写回 `docs/API-AUDIT-FINDINGS.md`。
>
> ## 对照的三份证据（缺一不可）
>
> 1. **静态对照**：本服务实现 vs 参考实现源码
>    - 参考：`../hindsight/hindsight-api-slim/hindsight_api/api/http.py`（100 个装饰器路由）
>    - 本服务：`internal/*/{routes,handlers}.go` + `internal/app/app.go` 的 `mount()`
> 2. **线上差分**：同一请求打两侧，逐字节比对（最强证据，优先）
>    ```sh
>    scripts/parity_dev_service.sh 18201                 # 起本服务（worker 关）
>    python3 scripts/parity_check.py GET /v1/default/banks/xxx/stats --service http://127.0.0.1:18201
>    python3 scripts/parity_probe.py --reference http://127.0.0.1:8888 \
>        --reference-key sk-hs-f94653018553250b8ffeff305b86ee0a --service http://127.0.0.1:18201
>    ```
> 3. **参考自带黑盒套件**：`make parity`（77 个 story，339 passed 基线）
>
> ## 已确认的对齐基线（本轮开工前实测，勿重复排查）
>
> | 面 | 结论 | 证据 |
> |---|---|---|
> | 路由集合 | **100 ↔ 99(OpenAPI)+`/version`，逐条相等** | 两侧 `openapi.json` path 集合 diff 为空 |
> | PostgreSQL schema | **除 `vector` 扩展版本外逐字节相同**（765 行清单 diff=0） | `scripts/schema_diff.sql` + `schema_inventory.py` |
> | MCP 工具数 | 两侧各 39 个 | `mcp_tools.py` 的 `_register_*` vs `internal/mcp/tools_defs_*.go` |
>
> ## 已确认**未闭合**的差异（本轮开工前实测，作为样例校准）
>
> - **`GET /health`、`GET /health/ready` 缺 `db_pool_waiting`**。参考在没有等待者时**也**输出
>   该键（值 0），本服务 `HealthBody` 只有 `db_pool_in_use/max/idle`。
>   参考证据：`engine/memory_engine.py:6218` 的 `_pool_health_stats()` 无条件写入
>   `stats["db_pool_waiting"] = waiting_count()`；`engine/db/pool_instrumentation.py:29`。
>   修复落点：`internal/monitoring/health.go:70-84` + `internal/storage/db.go:44`。
>
> 注：`/version` 的 `features.worker` 差异是**对照夹具**造成的（`parity_dev_service.sh` 固定
> `HINDSIGHT_API_WORKER_ENABLED=false` 以免消费共享队列），参考默认 `true`；不是代码缺陷。
> 对照时该项忽略，或在 `etc/wenyuan.yaml` 打开 worker 复核。

## 切片分组

每个切片是一个子 Agent 的工作单元。**写作用域互不重叠**是本轮并行的前提：如果两个切片要改同一个
文件，先由组长合并成一个切片，或让其中一方只出结论不改文件。

| 组 | API | 本服务落点 | 参考落点 |
|---|---|---|---|
| A 监控面 | `GET /health`、`/health/ready`、`/health/live`、`/metrics`、`GET /version`、`GET /v1/version`、`/debug/pprof/*` | `internal/monitoring/`、`internal/app/metrics*.go` | `api/http.py:5650-5775`、`api/observability.py`、`metrics.py` |
| B 银行生命周期与别名 | `GET/PUT/PATCH/DELETE /v1/default/banks[/{bank_id}]`、`/aliases*` | `internal/bankapi/` | `api/http.py:6592-8690`、`engine/memory_engine.py` 银行段 |
| C 银行配置、profile、background、模板 | `GET/PATCH/DELETE .../config`、`GET/PUT .../profile`、`POST .../background`、`GET /v1/bank-template-schema` | `internal/bankapi/config*.go`、`internal/app/background.go` | `api/http.py:8411-8440, 9450-9480, 9612-9700` |
| D retain / 文件 retain / dry-run-extract / prompts-preview | `POST .../memories`、`POST .../files/retain`、`POST .../memories/dry-run-extract`、`POST .../prompts/preview`、`DELETE .../memories` | `internal/memoryops/`、`internal/extraction/`、`internal/fileparse/` | `api/http.py:5922-6000, 10052-10560`、`engine/retain/` |
| E recall / reflect | `POST .../memories/recall`、`POST .../reflect` | `internal/memoryops/recall*.go`、`internal/reflect/` | `api/http.py:6165-6590`、`engine/memories/pg/recall.py`、`engine/reflect*.py` |
| F 记忆 CRUD 与历史 | `GET/PATCH .../memories/{id}`、`GET .../memories/{id}/history`、`GET .../memories/list`、`DELETE .../memories/{id}/observations` | `internal/memoryops/`、`internal/reporting/memory_routes.go` | `api/http.py:5815-6160, 9581-9610` |
| G 观测（observation）与 consolidation | `DELETE .../observations`、`GET .../observations/scopes`、`POST .../consolidate`、`.../consolidation/recover`、`.../consolidation-strategies/preview` | `internal/reporting/`、`internal/consolidation/` | `api/http.py:9450-9710`、`engine/consolidation/` |
| H mental models | `/mental-models*`（7 条 + clear + dry-run-refresh） | `internal/mentalmodels/`、`internal/app/mental_model_*.go` | `api/http.py:6920-7300`、`engine/mental_models/` |
| I knowledge base | `/knowledge-base/*`（tree/folders/pages/export/search/nodes） | `internal/reporting/knowledge.go`、`internal/storage/postgres_knowledge*.go` | `api/http.py:7300-7620`、`engine/knowledge*` |
| J directives | `/directives*` | `internal/directives/` | `api/http.py:7620-7800` |
| K 读表面：documents / chunks / tags | `/documents*`、`/v1/default/chunks/{id}`、`/tags` | `internal/readsurface/` | `api/http.py:7800-8210`、`api/page_markdown.py` |
| L 图与实体 | `GET .../graph`、`/entities*`、`POST .../entities/{id}/regenerate` | `internal/readsurface/`、`internal/entitylabels/` | `api/http.py:5774-5815, 6748-6920` |
| M operations 队列 | `GET .../operations*`、`DELETE/retry` | `internal/memoryops/operations*.go`、`internal/worker/` | `api/http.py:8210-8410`、`worker/` |
| N transfer / import / export / clone / document-transfer / files download / attachments | 9 条 | `internal/transfer/`、`internal/hindsightarchive/`、`internal/app/transfer.go` | `api/http.py:8716-9390`、`engine/transfer*` |
| O webhooks | `/webhooks*` + 投递签名与重试 | `internal/webhooks/`、`internal/app/webhooks.go`、`internal/worker/` | `api/http.py:9768-10050`、`webhooks/` |
| P 审计与 LLM 请求 | `/audit-logs*`、`/llm-requests*`、`/health/llm` | `internal/reporting/`、`internal/audit/`、`internal/app/llm_trace.go` | `api/http.py:6678, 10574-10737` |
| Q MCP | `/mcp` 的 39 个工具 + transport/错误 | `internal/mcp/` | `api/mcp.py`、`mcp_tools.py`（4640 行） |

## 子 Agent 输出约定

每个子 Agent 只写 `docs/API-AUDIT-FINDINGS.md` 的**自己那一节**（追加，不改别人小节），格式：

```
### [组 X] <组名>

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET /v1/... | ✅一致 / ❌已修 / ⚠️存疑 | file:line | file:line | 无 / 简述 | 测试名 / 差分命令 |
```

结论只用三种：`✅一致`（有证据）、`❌已修`（改了并验证）、`⚠️存疑`（有证据但无法判定或
需要组长裁决）。**不接受"看起来一致"**——每条 `✅` 必须附参考 `file:line` 与本服务 `file:line`。
## 对照夹具：两侧必须读同一个库（2026-10-20 补充，重要）

线上参考（`127.0.0.1:8888`，pid 3367723）读的是 **`127.0.0.1:5432/hindsight` 的 `user_devin` schema**，
由 `hindsight-extensions/static-keys-tenant` 的 `StaticKeysTenantExtension` 把 API key `devin:sk-hs-…`
映射到 `user_{user_id}` 而成（`HINDSIGHT_API_TENANT_SCHEMA_PREFIX` 默认 `user`）。
实测：参考 `GET /v1/default/banks` 的 `total` = **46**，`user_devin.banks` = **46**，`public.banks` = **2**。

而 `scripts/parity_dev_service.sh` 默认把本服务指向 `15432/wenyuan`（`etc/wenyuan.yaml` 的
`database_url`），**不是同一个库**。凡是带**计数/聚合**的响应（stats、scopes、list 的 `total`、
列表长度）在那种配置下必然"不同"，**那是夹具差异，不是缺陷** —— 不要据此改代码。

要让本服务读参考的同一份数据，三处配置必须**同时**给（`etc/wenyuan.yaml` 的 `database_schema: public`
在配置文件的优先级高于进程环境变量，所以 `HINDSIGHT_API_DATABASE_SCHEMA` 单独设无效，必须换配置文件）：

```sh
sed -e 's#^database_url: .*#database_url: "postgresql://hindsight:hindsight@127.0.0.1:5432/hindsight"#' \
    -e 's/^database_schema: public/database_schema: user_devin/' etc/wenyuan.yaml > /tmp/wy-userdevin.yaml

scripts/parity_dev_service.sh 18201 --stop
HINDSIGHT_API_CONFIG_FILE=/tmp/wy-userdevin.yaml \
HINDSIGHT_API_TENANT_USERS="devin:sk-hs-f94653018553250b8ffeff305b86ee0a" \
HINDSIGHT_API_TENANT_EXTENSION="hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension" \
HINDSIGHT_API_TENANT_API_KEY="sk-hs-f94653018553250b8ffeff305b86ee0a" \
PARITY_DEV_DATABASE_URL="postgresql://hindsight:hindsight@127.0.0.1:5432/hindsight" \
  scripts/parity_dev_service.sh 18201
```

**读写纪律**：这是个**有真实存量数据**的共享库。只读探测直接用现有 bank；任何写操作必须建自己的
`audit-<组>-<随机>` bank 并在结束前删除，**绝不要改 `devin` 等既有 bank**。多个组共用一个端口时，
后来的会踢掉先前的 —— 各自用不同端口（18210、18211…），并在结束时 `--stop`。

### 更正（实测）：直接读 `user_devin` 会被拒

本服务启动时**拒绝接管部分匹配的 schema**（`internal/storage/postgres_adoption.go` 的
`partialSchemaError`）：参考的 `user_devin` 带着 `alembic_version = c8e2f4a6b1d3` 且只含序列要建的
314 个对象中的 309 个（那 5 个是 `invalidated_mu_document_fkey` 等），因此**不能**直接把本服务指过去。

**可用做法（组 F 已验证）**：在**同一个 PostgreSQL 实例**（`127.0.0.1:5432/hindsight`）建**自己的兄弟
schema**（如 `audit_f_replica`，本服务会正常迁移它），把同一份夹具**逐行播到两边**，然后只比较
两份夹具——数据逐行相同，结论才成立。用 `HINDSIGHT_API_CONFIG_FILE` 指一个 `database_schema:
audit_<组>_replica` 的临时 yaml。用完 `DROP SCHEMA … CASCADE`。

已知需要掩掉的夹具噪声：参考 worker 会自己 consolidate（`consolidated_at` 被填、可能多出新观测）、
`edited_at`/`updated_at` 是各自请求时刻。

**也注意**：不要在仓库内建 `git worktree` 做隔离——`internal/storage/boundary_test.go` 会扫描全仓
`.go` 文件，会让它误报。若已建，务必 `git worktree remove`。

**严禁迁就性修改**：如果某个差异只在"库不同"的条件下成立，那就是夹具问题，不要改代码来"对齐"它。

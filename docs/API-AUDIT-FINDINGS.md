# API 逐条复刻审查结论（2026-10-20）

> 由 `docs/API-AUDIT-MANIFEST.md` 定义的切片组各子 Agent 追加。**只追加自己那一节**。
> 结论：`✅一致` / `❌已修` / `⚠️存疑`。每条 `✅` 必须附参考 `file:line` 与本服务 `file:line`。

## 组 A 监控面

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET /health、/health/ready（成功体字段集与键序） | ❌已修 | `engine/memory_engine.py:6203-6206`（`status`/`database`/`db_acquire_ms` 三键）+ `:6208` `health.update(self._pool_health_stats(backend))`；未初始化分支 `:6190-6191` 在读池统计**之前**返回（失败分支 `:6210-6215` 三段式，不含池键）；`_pool_health_stats` 的插入序为 `:6223-6224` **先** `db_pool_waiting`，`:6226-6232` **后** `in_use`/`max`/`idle` | `monitoring/health.go:86-110`（`HealthBody`，`db_pool_waiting` 已排在 `db_pool_in_use/max/idle` **之前**）、`:121-175`（`readyBody`：先 `poolWaiting()`，后 `PoolStatsReporter`） | 修正键序：`db_pool_waiting` 从结构体末位移到 `db_pool_in_use` 之前（响应字节即契约） | `TestHealthPoolKeyOrder`（已证伪：改回旧序即 FAIL，含 `/health` 与 `/health/ready` 两路）；实测两侧键序列均为 `status,database,db_acquire_ms,db_pool_waiting,db_pool_in_use,db_pool_max,db_pool_idle`，**完全一致** |
| GET /health、/health/ready（`db_pool_waiting` 无条件输出） | ❌已修 | `engine/memory_engine.py:6224` 无条件写 `stats["db_pool_waiting"] = waiting_count()`（仅当 `waiting_count` 导入失败才省略，`:6225-6226`）；计数源 `engine/db/pool_instrumentation.py:43-45`，增减在 `:69-84`（`finally` 里对未取得的连接回补，失败路径不泄漏） | 计数器：`storage/db.go:56-95`（`waitingAcquires`/`PoolWaitingCount`/`acquirePooledConnection`/`PoolWaiter`/`poolWaiterStat`）、`storage/db.go:175`（接入 `acquireObserver.acquire`）、`storage/postgres.go:316-326`；探针：`monitoring/health.go:66-83`、`:100-110`、`:158-176`；转发层（写权限外，已由 app 包 owner 补齐）：`internal/app/app.go:529-541`、`cmd/worker/main.go:132-143` | 已全链路补齐：`storePinger.PoolWaiting` 现存在，`/health` 实测输出 `"db_pool_waiting":0` | `TestHealthReportsPoolWaiting`、`TestHealthPoolWaitingForwardingAndFailure`、`TestHealthPoolKeyOrder`、`TestPoolWaitingCountTracksBlockedAcquires`、`TestPoolWaitingCountIsReleasedOnFailure`、`TestPoolWaiterStatReportsTheCounter`。实测：`curl 18201/health` → `"db_pool_waiting":0`，与参考一致 |
| GET /health/live | ✅一致 | `api/http.py:5685-5702`、`liveness.py:39-54,85-92`（`status`/`version`/`uptime_seconds`，`round(...,1)`） | `monitoring/health.go:192-199`、`:19-33`（`Float` 保 `.0`）、`:210-212`（`round1` = Python `round` 的 ROUND_HALF_EVEN） | — | `python3 scripts/parity_check.py GET /health/live --service http://127.0.0.1:18201` → `SAME`（`uptime_seconds` 为真测量值，两侧不同属进程年龄） |
| GET /version | ✅一致 | `api/http.py:5704-5746`；`FeaturesInfo` 字段序 `api/http.py:4828-4844` | `monitoring/version.go:23-94`（`VersionBody` 手写序列化按 `config.FeatureKeys` 声明序）、`:96`（`Serve`） | — | 两路 `parity_check` 仅 `features.worker` 不同，属对照夹具（`parity_dev_service.sh` 固定 `WENYUAN_API_WORKER_ENABLED=false`），按任务说明忽略 |
| GET /v1/version | ✅一致 | `api/http.py:5716-5717`（`include_in_schema=False` 的别名，同一 handler） | `internal/reporting/handlers.go:119` | — | 同上（与 `/version` 同体同状态码） |
| GET /metrics（家族名与 HELP/TYPE） | ⚠️存疑 | `api/http.py:5749-5773`（`worker_metrics.render` 或 `generate_latest`）；`metrics.py:1090-1126` 的 `hindsight.db.pool.waiting` 仪表 | `internal/app/metrics.go:35-37,179-190`（`metricDBPoolWaiting`，写权限外已补齐）、`monitoring/metrics.go:105-131`（渲染序） | 已无真实缺项；剩余 22 个参考独有家族全部是**已知不修 + 观测驱动** | 家族集合 diff：本服务**无独有**家族（0）；参考独有 22 个 = `python_gc_*`(3)+`python_info`+`process_*`(6)（`PARITY-OPEN-ITEMS.md` 7.6 判不修）+ `wenyuan_llm_*`(5)/`operation_*`(2)/`recall_phase_*`/`retain_*`(3)/`event_loop_stall_duration_seconds`（参考侧有存量观测）。实测本服务跑一次 retain 后 `wenyuan_retain_phase_duration_seconds`、`wenyuan_llm_calls_total` 即出现，**证明是空载而非缺失**。`wenyuan_db_pool_waiting` 现两侧同值 `0.0` |
| GET /metrics（HTTP 家族与标签形状） | ✅一致 | `api/observability.py:93-125`（`http_endpoint_label`：路由模板，未匹配 = `unmatched`，`:52`） | `monitoring/metrics.go:18`（`text/plain; version=1.0.0; charset=utf-8`）、`:23`（`otel_scope_name=hindsight_api.metrics`）、`:89-129` | — | 5 个 HTTP 家族名逐字相同；标签集 `endpoint/method/otel_scope_*/status_class/status_code` 相同，且两侧均出现 `endpoint="unmatched"` 的 307（尾斜杠）与 404 序列 |
| GET /metrics（`wenyuan_event_loop_stalls_total` 空载系列） | ❌已修 | `metrics.py:623-627` 在建 collector 时就声明 `hindsight.event_loop.stalls`，"声明"而非"首次观测才出现"，因此健康进程的暴露体也有 `wenyuan_event_loop_stalls_total{...} 0.0` | `monitoring/workload.go:344-359`（新增 `declareLoopStall`/`DeclareEventLoopStall`）、`monitoring/looplag.go:92-98`（`LoopLagProbe.Run` 在 `Stall > 0` 时声明） | 该计数器原先只在第一次真实停顿时才建系列，健康进程**整族缺失**（缺口号为零，等于告警读到"无数据"） | `TestLoopLagProbeDeclaresStallCounter`（已证伪：移除声明即 FAIL）、`TestWorkloadLoopStallsDeclaredAtZero`；重建后 18201 暴露体已出现 `wenyuan_event_loop_stalls_total{...} 0.0` |
| /debug/pprof/* | ✅一致 | 参考无 pprof 路由（`api/http.py` 全无 `pprof`/`/debug`） | `monitoring/profile.go:11-21`（仅 `WENYUAN_API_PPROF_ENABLED=true` 才挂载，`internal/app/app.go:369-371`） | — | 8 条路径（`/debug`、`/debug/pprof`、`/debug/pprof/`、`goroutine`、`cmdline`、`profile`、`symbol`、`trace`）两侧**逐条**同码同体：`404 {"detail":"Not Found"}` |
| 尾斜杠 / 错误方法 / 未知查询参数 | ✅一致 | Starlette `redirect_slashes` + 405 信封 | `internal/httpx` 路由 | — | 5 条路由 `/x/` 两侧均 `307` + `Location` 去斜杠 + `Content-Length: 0`；`POST/PUT/DELETE` 两侧均 `405 {"detail":"Method Not Allowed"}` + `Allow: GET`；`/health?x=1` 两侧同体（除 `db_pool_waiting`）；`//health` 两侧均 `404` |

**已收敛（写权限外，已由 app 包 owner 落地）**：`db_pool_waiting` 的全链路已通——`internal/app/app.go:529-541` 的 `storePinger.PoolWaiting`、`cmd/worker/main.go:132-143` 的 `workerPinger.PoolWaiting`、`internal/app/metrics.go:35-37,179-190` 的 `wenyuan_db_pool_waiting` 仪表均已存在。重建后实测：

```sh
$ curl -s http://127.0.0.1:18201/health
{"status":"healthy","database":"connected","db_acquire_ms":0.3,"db_pool_waiting":0,"db_pool_in_use":0,"db_pool_max":100,"db_pool_idle":2}
$ curl -s http://127.0.0.1:8888/health
{"status":"healthy","database":"connected","db_acquire_ms":0.5,"db_pool_waiting":0,"db_pool_in_use":0,"db_pool_max":100,"db_pool_idle":4}
```

键集与键序**逐位相同**；残余仅 `db_acquire_ms`/`db_pool_idle`/`db_pool_in_use` 三个**活测量值**（各自进程连接池的实时状态，`db_acquire_ms` 已在 `parity_probe.MEASUREMENTS` 里归一为 `<N>`）。`parity_check` 因此仍报 `DIFF`，但 diff 只落在 `db_pool_idle`（参考 4 / 本服务 2，即两边池里空闲连接数），把这三个活测量掩掉后两侧完全 `SAME`：

```
/health       keys(ref) = keys(svc) = [status, database, db_acquire_ms, db_pool_waiting, db_pool_in_use, db_pool_max, db_pool_idle]
/health/ready keys(ref) = keys(svc) = 同上；掩掉活测量后 SAME = True，键序一致 = True
```

`/metrics` 复核：`wenyuan_db_pool_waiting`、`wenyuan_event_loop_stalls_total` 两侧现已同名同 HELP/TYPE 同值（均 `0.0`）。剩余参考独有家族 22 个，全部属**已知不修 + 观测驱动**两类，无一是机械缺项：
- `python_gc_*`(3)/`python_info`/`process_*`(6) —— `prometheus_client` 默认寄存器与宿主机进程指标，`docs/PARITY-OPEN-ITEMS.md` 7.6 已判**不修**；
- `wenyuan_llm_*`/`operation_*`/`recall_phase_*`/`retain_*`/`event_loop_stall_duration_seconds` —— 参考侧因共享库上有存量流量而已有观测。本服务上按观测驱动生成：实测跑一次 retain 后 `wenyuan_retain_phase_duration_seconds`、`wenyuan_llm_calls_total` 即出现在暴露体中，证明是**空载而非缺失**。

## 组 B 银行生命周期与别名

参考范围 `api/http.py:6592-8690` 的路由，以及它们调用的引擎方法（`engine/memory_engine.py` 的 `update_bank`/`list_banks`/`delete_bank`/别名四法、`engine/bank_aliases.py`、`engine/memories/pg/banks.py:34`、`engine/memories/pg/engine_curation.py:117`）。逐字节比对 109 个探针（8 条路由的成功、校验、冲突、404/405、分页、别名解析全路径）。

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET /v1/default/banks | ✅一致 | `api/http.py:6592`（`list_banks` 端点）+ `:6607-6640`（`q`/`limit`/`offset`、`_aliases_explaining`）；`engine/memory_engine.py:15068`→`engine/bank_utils.py` 的 `list_banks_page`（页切片、`total` 取切片前计数）；排序 `engine/memories/pg/banks.py:34`（`last_write_at DESC`，`bank_id` 打破并列） | `bankapi/banks.go:48`（`ListBanks`）、`:253`（`parseBankQuery`）；排序 `internal/storage/postgres_banks.go:50` | 无 | `parity_check GET /v1/default/banks` 及 `?q=`/`limit=0`/`limit=-1`→422/`limit=abc`→422/`offset=-1`→422/`limit=100000`/越界 offset/重复参数取末次/`limit=1.5`/大小写不敏感 `q` → 全 `SAME`；`matched_aliases` 仅在 `q` 命中且未直读 id/name 时按别名文本排序输出；`TestBankListMatchedAliases`、`TestBankListPagingAndSearch`、`TestBankListOrdersByRecency`、`TestBankListDefaultLimitAndValidation`、`TestBankListRepeatedParamLastWins` |
| GET /v1/default/banks/{bank_id} | ✅一致 | `api/http.py` 该路径只声明 `put`/`patch`/`delete` 三个方法（OpenAPI 同证），GET 落到 405 | `bankapi/routes.go`（未注册 `GET .../{bank_id}`，ServeMux 天然回 405） | 无。两侧同为 `405 {"detail":"Method Not Allowed"}`——任务书列出的该路由实际上**两侧都不存在**，故按 405 一致判定 | `parity_check GET /v1/default/banks/audit-b-<rand>-beta` → `SAME`（405） |
| PUT /v1/default/banks/{bank_id} | ❌已修 | 模型 `CreateBankRequest`；`engine/memory_engine.py:21641`（`update_bank` 建或改）。**校验是逐字段的**：每个非法字段一个 item、按字段声明序，且某字段的边界错误与另一字段的类型错误并列输出 | `bankapi/banks.go:80`（`ReplaceBank`）、`:87`（`decodeBankBody`）；`bankapi/decode.go:45`（`decodeBankBody`）、`:197`（`bankFieldTypeItems`）、`:259`（`prefixLocations`）；`bankapi/validation.go:44`（`carriesValidationItems`）、`:63`（`orderedValidationItems`） | ①共享解码器在**首个**类型错误即停，只报一个 item；②`disposition` 成员内的 item 未按成员声明序穿插；③`{"disposition":"bad"}` 报成 `dict_type`，参考为 `model_attributes_type`。改法：体被拒时改走声明遍历 `ValidationItemsAgainstRequest` 一次性取全字段 item，按字段声明序与处理器自有规则交错合并（同字段的类型错误压制其规则项，因该字段从未解码），嵌套成员 item 重定位到 `["body",member,…]`，并把已成功解码的成员补填给规则校验 | `parity_check PUT .../bank {"name":123,"mission":123}` → 两个 `string_type`；`{"name":123,"disposition_skepticism":9,"enable_reranking":"bad"}` → `string_type`/`less_than_equal`/`bool_parsing` 三 item 按序；`{"disposition":"bad"}` → `model_attributes_type`；`{"disposition":{"skepticism":9}}` → `less_than_equal` + 两个 `missing` 按成员序 —— 全 `SAME`；`TestCreateOrReplaceReportsEveryTypeItem`、`TestDispositionObjectItemsFollowMemberOrder`、`TestNonObjectBodyKeepsTheDecoderAnswer` |
| PATCH /v1/default/banks/{bank_id} | ❌已修 | 同 PUT 的模型与校验顺序；`engine/memory_engine.py:21641`：银行不存在由处理器在**模型校验之后**才 404（框架先校验请求体） | `bankapi/banks.go:158`（`UpdateBank`）、`:166`（`resolveAlias`，只解析别名不做存在性判断）、`:170-183`（先 `decodeBankBody`/`ValidatePatch`，后 `GetBank`）；`bankapi/banks.go:213`（`resolveAlias`） | PUT 的三项修复同源；另加**顺序修复**：原先 `resolveCanonical` 在解码前就做存在性检查，使「银行不存在 + 体非法」返回 404，参考为 422。改为先校验体、后查存在性 | `parity_check PATCH .../<rand>-nope {"name":123}` → `422 string_type`（原为 404）；`{"retain_chunk_size":5.5}` → `422 int_from_float`；`{"name":null,"disposition_skepticism":9,"enable_reranking":"bad"}` → 无 `name` 项、其余按序；`TestMissingBankStillValidatesTheBody`、`TestPartialUpdateReportsEveryItemInFieldOrder`、`TestNullFieldDoesNotSuppressLaterItems` |
| DELETE /v1/default/banks/{bank_id} | ✅一致 | `api/http.py:7889`（`DeleteResponse`）+ `engine/memory_engine.py:11377`（`delete_bank`）；计数 `engine/memories/pg/engine_curation.py:117`（`count_bank_contents` = memory_units + entities + documents） | `bankapi/banks.go:189`（`DeleteBank`） | 无 | `parity_check DELETE .../bank` 信封逐字节相同：`{"success":true,"message":"Bank '<id>' and all associated data deleted successfully","deleted_count":N}`；从未创建的银行同样 200 且 `deleted_count:0`；集合路由 `DELETE /v1/default/banks` 两侧同为 405；`TestDeleteEnvelope`、`TestDeleteBankRemovesContent` |
| GET /v1/default/banks/{bank_id}/aliases | ✅一致 | `api/http.py:8103`（`list_aliases`）+ `engine/memory_engine.py` 别名区（`is_primary DESC, created_at ASC`） | `bankapi/aliases.go:12`（`ListAliases`） | 无 | `parity_check GET .../aliases` → `SAME`（含空列表、以别名寻址、`bank_id` 回显规范 id）；`TestListReportsDisplayAlias`、`TestAliasLifecycle` |
| POST /v1/default/banks/{bank_id}/aliases | ✅一致 | `api/http.py:8130`（`create_bank_alias`）+ `engine/bank_aliases.py`（`resolve` 先查 `banks` 再查 `bank_aliases`；自别名 400、已被占用 409） | `bankapi/aliases.go:27`（`AddAlias`） | 无。体校验（`alias` 缺失/为 null/非字符串、`primary:null`→`bool_type`）本就先于银行存在性判断，与参考一致 | `parity_check POST`：`{"alias":"<已占用>"}`→409、自别名→400、缺 `alias`→`missing`、`{"alias":null}`→`string_type`、`{"primary":null}`→`bool_type`、`{"alias":"   "}`→201、未知字段落入 `X-Ignored-Params`、缺银行→404 —— 全 `SAME`；`TestAliasConflictAndSelfAlias`、`TestAliasCreateBodyValidation`、`TestCreateBankUnderExistingAliasConflicts` |
| PATCH /v1/default/banks/{bank_id}/aliases/{alias} | ❌已修 | `api/http.py:8202`（`set_bank_alias_primary`）+ `engine/memory_engine.py`（`is_primary` 唯一，同事务内降级其余）；请求体模型 `SetBankAliasPrimaryRequest{primary: bool}` 由框架**先于**处理器校验 | `bankapi/aliases.go:74`（`PromoteAlias`）、`:82`（`resolveAlias`） | 同 PATCH 银行的顺序修复：原实现先 `resolveCanonical`（含存在性检查）再解码体，参考对「银行不存在 + 体非法」返回 422 | `parity_check PATCH .../nope/aliases/x {"primary":"bad"}` → `422 bool_parsing`；`{"primary":true}` → 404；`{}` → `missing primary`；`{"primary":null}` → `bool_type`；升/降主别名后 `display_alias` 变化 —— 全 `SAME`；`TestAliasLifecycle`、`TestAliasDemoteReturnsToBankID`、`TestMissingBankStillValidatesTheBody` |
| DELETE /v1/default/banks/{bank_id}/aliases/{alias} | ✅一致 | `api/http.py:8266`（`delete_bank_alias`）+ `engine/memory_engine.py`（`WHERE alias=$1 AND bank_id=$2`，限定本行） | `bankapi/aliases.go:136`（`RemoveAlias`） | 无 | `parity_check DELETE`：命中→200 且列表收缩、未命中→404 `Bank '<bank>' has no alias '<alias>'`、银行不存在→404 `Bank '<id>' not found` —— 全 `SAME`；`TestAliasLifecycle`、`TestAliasNotFound` |
| 别名解析（全部 bank 作用域路由） | ✅一致 | `engine/bank_aliases.py` 的 `resolve`：真实银行永远优先，其次查别名（含缓存未命中）；改写发生在 `api/unknown_params.py` 的 `_resolve_bank_alias`（处理器运行前），失败静默回落原值 | `internal/storage/postgres_banks.go` 的 `ResolveBankID`；`bankapi/banks.go:213`（`resolveAlias`）、`:229`（`resolveCanonical`） | 无 | 以别名寻址 `DELETE`/`PATCH`/`PUT` 与以规范 id 寻址等价；删掉银行后遗留的别名行使后续 PATCH 404、PUT 重建银行 —— 两侧一致；`TestAliasResolvesAtEveryRoute`、`TestCreateBankUnderExistingAliasConflicts` |

**本组唯一未修项（不属本组写入范围，仅报告）：** `GET /v1/default/banks` 的整表比对会因**测试实例数据库不同**而出现行数差异 —— 参考跑在 `code/hindsight` 的嵌入式 `pg0`（其 `.env` 未设 `WENYUAN_API_DATABASE_URL`），本服务副本跑在 docker 的 `127.0.0.1:15432`，两者各存各的银行。实测双方共有 6 行时，**逐行内容与相对顺序完全一致**，差异仅为参考侧独有的 44 行存量数据，属任务书已知的「库里有存量银行」类噪声而非行为差异。把比对限定到自建夹具（`?q=audit-b-<rand>`）复核为 `SAME`。

## 组 C 银行配置 / profile / background / 模板

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET .../config（成功体） | ✅一致 | `api/http.py:9612-9640`→`config_resolver.py:267-330`（`resolve_full_config` + `_public_fields_of`：只含 configurable 字段、剔除 credential）；银行配置列读取 `config_resolver.py:474-495`（null 为 tombstone）、类型兜底 `config_resolver.py:883-946`（字符串字段转 JSON 文本、其余丢弃回落默认） | `bankapi/config.go:17`（`GetBankConfig`）、`:143-167`（`writeConfigResponse`）、`:228-263`（`bankOverrides`）、`:295-297`（`resolvedConfig`→`config.ResolveLayers`）；默认值表 `bankapi/config_defaults.go:22-99` | 无。`parity_check` 逐字节相同：50 键、同值同类型（含 5 个 `null`、`retain_extraction_mode:"concise"`、`max_observations_per_scope:-1`） | `python3 scripts/parity_check.py GET /v1/default/banks/audit-c-<rand>/config --service http://127.0.0.1:18209` → `SAME`（新建银行、无 override）；`TestBankConfigReadIsFullyResolved`、`TestBankConfigReadIgnoresLegacyDispositionColumns` |
| GET .../config（优先级：bank > tenant > 部署默认） | ✅一致 | `config_resolver.py:228-262`：先 tenant（`_resolve_tenant_overrides` `:186-217`），再 bank（`overrides.update(bank_overrides)`），再 `_with_overrides` 拷 global | `bankapi/config.go:295`（`config.ResolveLayers(bank, tenant, process)`，`internal/config/resolver.go:18-29` 逐层覆盖）、`internal/httpx` 的 `TenantOverridesFrom` | 无 | `TestBankConfigReadLayersTenantOverrides` |
| GET .../config（银行不存在 / 大小写别名 / 解析后非法） | ✅一致 | 不存在：`memory_engine.py:14433-14441` 的读授权内 `_require_bank_exists` → 404 `"Bank '<id>' not found"`；非法：`config_resolver.py:158-166` 的 `validate_retain_chunking_config`/`validate_retain_image_chunking_config` 抛 ValueError 被 `api/http.py:9636-9638` 兜成 500 | `bankapi/config.go:17-30`（`validateBankExists`）、`:169-198`（`validateResolvedConfig`/`validateChunkingInt`，逐字复刻 `config.py:2237-2278` 的 `"X must be >= 1, got N"`） | 无 | 线上：不存在 → 两侧同 `404 {"detail":"Bank 'audit-c-...' not found"}`；大写 id 两侧同；存活银行被库内写坏成 `retain_max_attachments_per_chunk=-3` 时两侧同 `500 {"detail":"retain_max_attachments_per_chunk must be >= 1, got -3"}`；`TestBankConfigReadRejectsInvalidResolvedConfig` |
| PATCH .../config（merge 语义：未点名成员保留、`null` 清除该成员、未知名/凭据/静态名/类型/界） | ✅一致 | `config_resolver.py:643-711`（`validate_bank_config_updates`）+`:712-770`（`_persist_bank_config` 的 `config = COALESCE(config,'{}') || $1`，jsonb 逐键合并）；`_validate_config_value_types` `:863-881`；null tombstone `:541-549` 与 `:150-156`；银行不存在则建行 `memory_engine.py:14443-14472` | `bankapi/config.go:36-95`（`UpdateBankConfig`：feature flag→`RefuseBankWideIfScoped`→`updates` 必填→`configPatch`→`EnsureBank`→`UpdateBank`）、`bankapi/config_keys.go:119-216`（凭据/静态/未知名三条消息、逐键类型检查、tombstone）、`:952`（`asInt`） | 线上逐条比对 21 种 PATCH（含 `null` 清除、env 拼写键 `WENYUAN_API_*`、`retain_chunk_size:2500.0`→400、`recall_budget_min>max`、`memory_defense` 422、`retain_strategies` 空策略名）全部同码同体；`retain_mission` 单独置 `null` 后再读 `overrides` 两侧一致（存活成员保留） | 见下「C-1/C-2」两处已修的差异；`TestBankConfigWriteValidation`、`TestBankConfigWriteRejectionMessages`、`TestBankConfigWriteTypeAndTraitMessages`、`TestBankConfigWriteStoresIntegerLiteral` |
| PATCH .../config（`consolidation_strategies` 校验消息） | ❌已修 | `config_resolver.py:839-861`（`StrictConsolidationStrategySpec.model_validate` 取**第一条** error，渲染 `f"Invalid consolidation strategy at index {i}: {loc}: {msg}"`）；模型 `engine/response_models.py:678-747`：`tags: list[str]` 元素级类型错误定位到 `tags.0`，`tags_match` 的 `field_validator` 消息被框架加 `Value error, ` 前缀，非对象条目报 `Input should be a valid dictionary or instance of StrictConsolidationStrategySpec` | `bankapi/config_keys.go:598-640`（`validateConsolidationStrategies`）、`:642-684`（`validateStrategyScopes`）、`:605-607`、`:627-633`（「entry」「整数」措辞）、`:952`（`asInt`） | 三处措辞/定位偏差：①`tags:[1]` 报 `scopes.0.tags` 而非 `scopes.0.tags.0`；②`tags_match` 缺 `Value error, ` 前缀；③非对象条目与错误类型设置项少 ` or instance of StrictConsolidationStrategySpec` / `, unable to parse string as an integer`。另把成员检查调到 `scopes` 之后（pydantic 按字段声明序报错，`scopes` 声明在前），未知键按名排序 | `TestBankConfigWriteConsolidationStrategyLocations`（先证伪：改前 3/4 用例失败）；线上 5 例 `PATCH .../config` → `SAME` |
| PATCH .../config（`knowledge_page_default_trigger` 校验消息） | ❌已修 | `config_resolver.py:755-758`→`:825-830` 的 `_validate_against_model`：先 `"{key} has unknown fields: {sorted}"`，再 `"Invalid {key}: " + "; ".join("{loc}: {msg}")`（pydantic error 列表序）；模型 `api/http.py:3199-3389`（`MentalModelTrigger`：`response_schema: dict|None` + `validate_response_schema`，`tag_groups` 为 list，`include_chunks`/`reflect_search_observations_*` 可空，fact_types/refresh_cron 自定义校验） | `bankapi/config_keys.go:289-314`（未知名→`has unknown fields`，其余→`Invalid ...: m: reason`）、`:318-345`（`triggerReason`/`triggerProblems`，按成员名排序）、`:266-285`（`isEntityLabelsEmpty`） | 五处偏差：①未知名被忽略（`{"bogus":1}` 竟 200 入库）→ 现 400；②原消息前缀 `knowledge_page_default_trigger is invalid:` 且不带成员名 → 现 `Invalid <key>: <member>: <framework msg>`；③只报第一个错误 → 现报全部并按名排序；④`response_schema` 不校验 → 现复用 `memoryops.ValidateResponseSchema`（`internal/memoryops/reflect_validation.go:83-88`），非对象条目/空 `properties` 同参考措辞；⑤`tag_groups` 非 list、`include_chunks` 等可空成员为 `null` 时误报 → 已修 | `TestBankConfigWriteKnowledgePageTriggerMessages`（先证伪：改前 10 例全红）；线上 10 例 `PATCH .../config` → `SAME` |
| PATCH .../config（`entity_labels` 空属性表） | ❌已修 | `engine/retain/entity_labels.py:78-89`：`[]` 与 `{"attributes": []}` 都返回 `None`（=「未配置词表」），不是错误 | `bankapi/config_keys.go:233-244`（`entity_labels` 分支）、`:266-285`（`isEntityLabelsEmpty`） | `{"attributes":[]}` 原先被解析器当畸形报 400，参考接受 → 现接受并原样存库 | `python3 scripts/parity_check.py PATCH .../config --body '{"updates":{"entity_labels":{"attributes":[]}}}'` → `SAME` |
| PATCH .../config（非可空布尔成员取 `null`） | ✅一致 | `api/http.py:3231-3234`（`refresh_after_consolidation: bool=False`）、`:3235`（`refresh_cron: str|None`）、`:3375-3388`（`?|None` 成员） | `internal/mentalmodels/trigger.go:196-208`（非空 bool 成员拒绝 null）、`trigger.go` 的 `triggerNullableBoolMembers` | 无（`exclude_mental_models:null`、`mode:null` 两侧同 400，`include_chunks:null` 两侧同 200） | 线上 `SAME` |
| PATCH .../config（并发/写入串行化） | ⚠️存疑 | `config_resolver.py:712-770`：`SELECT ... FOR UPDATE` 锁行后在事务内复检跨字段约束，失败抛 `BankConfigPersistenceConflictError` | `bankapi/config.go:36-95` 直接 `UpdateBank`，无行锁与事务内复检 | 未改：`storage.UpdateBank` 在 `internal/storage/`（本切片写权限外），且线上单请求语义已逐字节一致；跨字段约束（`recall_budget_min<=max`、`retain_max_completion_tokens>retain_chunk_size`、各策略 splice 后）仅在单次 update 内检查（`bankapi/config_keys.go:490-508`）。并发两写叠加出非法组合的场景未复现 | 无线上证据（需并发压测）；建议组长裁决是否单开一项 |
| DELETE .../config（重置为继承） | ✅一致 | `api/http.py:9700-9726`→`config_resolver.py:771-806`：`UPDATE banks SET config='{}'`，**不检查银行是否存在**，随后照常返回解析后的配置；`_BANK_NOT_FOUND_RESPONSES` 不适用于此路由（无 `_require_bank_exists`） | `bankapi/config.go:97-141`：`ClearConfig` 清空；`storage.ErrBankNotFound` 时构造空行照常 200；`overrides` 由 `bankOverrides` 重算（`:228-263`） | 无 | 线上：DELETE 后 GET 两侧 `SAME`；不存在的银行 DELETE 两侧同 `200` + 默认配置；`TestBankConfigWriteValidation` |
| GET/PUT .../profile | ✅一致（PUT 有残余⚠️） | `api/http.py:8413-8438`：两条路由都直接 `raise HTTPException(410, _PROFILE_RETIRED_DETAIL)` | `bankapi/retired_routes.go:30-36`（`RetiredRoutes`）、`internal/httpx/errors.go:150-156`（`DetailRetiredProfile`） | 无 | 线上 GET profile 两侧同 `410` + 同 detail。**PUT 仍不同**：带合法 pydantic 体（`{"disposition":{...},"mission":"x"}`）时两侧都 410；但 `PUT .../profile` 带**非法/缺字段**体时参考先 422（`api/http.py:8431` 的 `request: UpdateDispositionRequest` 由框架先校验），本服务永远 410。属「请求体校验先于 handler」，修点在 `internal/httpx` 路由层（写权限外） |
| POST .../background | ✅一致 | `api/http.py:8433-8468`：同理恒 410，独立消息 | `bankapi/retired_routes.go:35`、`internal/httpx/errors.go:157-163`（`DetailRetiredBackground`） | 无 | 线上 `POST .../background`（含带体）两侧同 `410` + 同 detail（该路由体无必填字段，不存在 profile 那种 422 先手） |
| GET /v1/bank-template-schema | ✅一致 | `api/http.py:9450-9461`：直接返回 `BankTemplateManifest.model_json_schema()`（**裸 dict，无 response_model**，故 `"default": null` 全在线上） | `internal/reporting/knowledge_schema.go:11-39`（嵌入参考原样生成的 `bank_template_schema.json` 并原样输出）；路由 `internal/reporting/handlers.go:118` | 无 | 线上文档**按值逐层深比完全相等**：Python 脚本 `json.load(ref)==json.load(svc)` → `True`，`$defs` 14 个键名与成员集合相同，深挖逐节点无 `ONLY-REF`/`ONLY-SVC`/`VAL` 差异。**唯一差异是键序**：本服务把文档解成 `map[string]any` 再输出（`internal/reporting/knowledge_schema.go:32-38`），Go 的 `encoding/json` 按字典序排键；参考由 pydantic 保声明序（`BankTemplateConfig` 以 `reflect_mission` 起、`MentalModelTrigger` 以 `budget` 起）。JSON 对象的键序不是成员集合的一部分，且两侧键集/值/`required` 完全相同，故判 `✅`；**若要求逐字节一致需改成直接回写嵌入的原始字节（或保序序列化）——落点在 `internal/reporting`，本切片写权限外** |
| profile/background 的 OpenAPI 声明 | ✅一致 | `api/http.py:8426-8431`（`deprecated=True`、`response_model=BankProfileResponse`） | 由 `scripts/reference_openapi.py` 生成并校验 | 无 | `openapi.json` 的 path 集合与 `deprecated` 标记已由清单「路由集合逐条相等」基线覆盖 |

**本切片实测汇总（`/tmp/full_probe.py`，50 例，双方各建 `audit-c-full-<rand>` 后删除）**：`DIFFS: 3 of 50`，其中
- 2 例是 `configFieldSample()` 的抽样列表：参考取 `sorted(list(self._configurable_fields)[:10])`，`set` 切片顺序随 `PYTHONHASHSEED` 每次不同（同一进程内两次请求即可不同），本服务取排序前 10 个。集合与 `total: 51 fields` 两侧相同（`config_resolver.py:534-549` vs `bankapi/config_keys.go:816-826`（`configFieldSample`）、`config_fields_reference.go:1-70`），属**不可复刻的进程内随机**，`✅`（需人工忽略）。
- 1 例是 `entity_labels` **多错误**时的 pydantic 汇总文本（`config_resolver.py:601-607` 的 `f"Invalid entity_labels format: {e}"`）：参考抛出多行 pydantic 报告（含 `errors.pydantic.dev` 链接、多个 error 合并），本服务只报第一个错误（`bankapi/config_keys.go:239` → `internal/entitylabels` 的第一条）。单错误场景（如 `entity_labels:42`）两侧已 `SAME`；多错误需重写 `entitylabels.Parse` 的错误聚合与 pydantic 文本渲染（`internal/entitylabels`，写权限外 → 单列 ⚠️）。

**⚠️ 写权限外 / 需组长裁决 3 项**：
1. `PUT .../profile` 的非法体：参考 422 早于 410（`api/http.py:8431`），修点在 `internal/httpx` 路由层（先按声明模型校验体，再进 handler）。
2. `entity_labels` 多错误 pydantic 汇总文本（`internal/entitylabels/vocabulary.go`，`config_resolver.py:601-607`）。
3. `PATCH .../config` 的行锁与事务内复检（`internal/storage` 的 `UpdateBank`，`config_resolver.py:712-770`）；另：**写入「仅含 `null` 的 updates」时，参考也会顺带把其它所有 override 清掉**（`COALESCE(config,'{}') || '{"k":null}'` 之后 `_active_bank_overrides` 的 `v is not None` 过滤 + 后续写入回落默认），本服务 `applyBankPatch` 在空 map 时同样保住其它键——已实测两侧一致（先写 3 键，再单独 `null` 清 1 键，两侧 `overrides` 均只剩另 1 键），故**不列为差异**。

**改动文件**：`internal/bankapi/config_keys.go`（`asInt` 加 bool 拒绝、`validateConsolidationStrategies`/`validateStrategyScopes` 重写、`validateKnowledgePageDefaultTrigger` 消息重写、`isEntityLabelsEmpty` 新增、`sortedKeys` 辅助）；`internal/mentalmodels/trigger.go` +（同组新增）`trigger_members.go`（`TriggerMember`/`TriggerErrors` 导出、`validateTrigger` 改为聚合全部条目、补 `response_schema`/可空布尔/整数/字符串列表成员的类型检查）；`internal/memoryops/reflect_validation.go`（新增 `ValidateResponseSchema` 导出）。**未改** `config.go`/`config_defaults.go`/`config_fields_reference.go`/`default_template.go`/`internal/app/background.go`（实测无需改动；`POST /background` 是恒 410 的退役路由，`internal/app/background.go` 是 worker 角色装配，不是该路由）。

**测试**：`TestBankConfigWriteConsolidationStrategyLocations`、`TestBankConfigWriteKnowledgePageTriggerMessages`（`internal/bankapi/config_test.go:417`、`:469`，均先证伪后转绿）。

**验证命令**（全部 `SAME`）：
```sh
scripts/parity_dev_service.sh 18209        # 本服务的独立实例，worker 关闭
python3 scripts/parity_check.py GET  /v1/default/banks/audit-c-<rand>/config  --service http://127.0.0.1:18209
python3 scripts/parity_check.py GET  /v1/bank-template-schema                 --service http://127.0.0.1:18209
python3 scripts/parity_check.py GET  /v1/default/banks/<b>/profile            --service http://127.0.0.1:18209
python3 scripts/parity_check.py POST /v1/default/banks/<b>/background --body '{"content":"x"}' --service http://127.0.0.1:18209
go build ./... && go test ./internal/bankapi/ ./internal/config/ -count=1 && gofmt -l internal/bankapi internal/config
```

## 组 D retain / 文件 retain / dry-run-extract / prompts-preview

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| POST /v1/default/banks/{bank_id}/memories（缺 `items`，报 `input` 为整个请求体） | ❌已修 | `api/http.py:10052`（`items: list[MemoryItem]`，`Path` 必填）；`Missing` 的 `input` 是**缺失成员所在的那一层对象**，请求级缺失即整个 body，实测 `{"async":false}` → `"input":{"async":false}` | `memoryops/validation.go:101-103`（原以 `map[string]any{}` 占位）、`memoryops/retain.go:315-318` | `ValidateRetain` 收下解码后的原始 body 值（`httpx.DecodeLax` 返回的第二返回值）作为 `Missing` 的 `input` | `TestRetainMissingItemsReportsTheBody`；`parity_check POST /v1/default/banks/<b>/memories --body '{"async":false}'` → `SAME`（422 `{"detail":[{"type":"missing","loc":["body","items"],"msg":"Field required","input":{"async":false}}]}`） |
| POST .../memories（`content` 缺失 ≠ `content` 为空） | ❌已修 | `MemoryItem.content` 必填（`api/http.py:10052` → `MemoryItem` 模型），缺成员报 `missing`/`Field required` 且 `input` 是**该 item 对象**；而值为空串/空列表走 `engine/retain/attachment_content.py:105-135` `_require_content` → `ValueError("content cannot be empty")`，是 `value_error`。两者不可混同 | `memoryops/validation.go:181-210`（`itemContentErrors`：先判 `Present()`，再判内容是否为空）、`memoryops/retain_models.go:262-290`（`present` 标记）、`memoryops/retain_models.go:497-506`（`rawObject`） | 原实现把"未提供"也归到 `missing`、但 `input` 给 `{}`；把"空白字符串"直接 200 落库。现按参考三分：未提供 → `missing`+item 对象；提供但无内容 → `value_error`+原值；提供且非空 → 通过 | `TestRetainMissingMemberReportsTheItem`（已证伪：改回旧实现即 FAIL，`input` 变 `{}`）；`parity_check ... --body '{"items":[{"text":"Alice"}]}'` → `SAME` |
| POST .../memories（`content` 为空串 / 纯空白 / 空列表 / 只有空白 text 块 → 422 `content cannot be empty`） | ❌已修 | `engine/retain/attachment_content.py:105-135`（`_require_content` 抛 `ValueError("content cannot be empty")`），经 pydantic 包成 `value_error`、`loc=[body,items,N,content]`、`input` 为原值、`ctx={"error":{}}` | `memoryops/retain_models.go:473-490`（`CarriesContent`）、`memoryops/validation.go:193-209` | 原实现：`""` → 误报 `missing`；`"  "` / `[{"type":"text","text":"  "}]` → **200 落库**。现统一 422 `value_error`，`input` 逐字回显原值 | `TestRetainValidatesContentBlocks` 四个子用例；`parity_check ... --body '{"items":[{"content":"  "}]}'` → `SAME` |
| POST .../memories（block 形式的结构校验：无 `type` / 未知 `type` / 缺 `text` / 缺 `source`） | ❌已修 | `MemoryItem.content` 是 `str \| list[tagged-union[TextContentBlock,ImageContentBlock,FileContentBlock]]`（`engine/retain/attachment_content.py`），pydantic 对联合类型**两个分支都报**：先 `string_type` at `content.str`（`input` = 整个列表），再按 discriminated-union 报 `union_tag_not_found` / `union_tag_invalid` / 分支内 `missing`（`loc` 含 `list[tagged-union[...]]` 段与 `text`/`image`/`file` 变体名） | `memoryops/retain_models.go:296-470`（`decodeBlocks`/`decodeContentBlock`/`ContentBlockIssue`）、`memoryops/validation.go:181-196` | 原实现：`[{"text":"x"}]` 200、`[{"type":"image"}]` 200、`[{"type":"text","text":"x"},{"type":"text"}]` 200、`[{"type":"bogus"}]` 报成 `missing`。现逐块校验并按参考的 loc 段与 `ctx` 逐字输出（`discriminator`/`expected_tags`/`tag`） | `TestRetainValidatesContentBlocks`（9 个子用例，含 `union_tag_not_found`、`union_tag_invalid`、缺 `source`、缺 `text`）；`parity_check ... --body '{"items":[{"content":[{"type":"bogus"}]}]}'` → `SAME` |
| POST .../memories（附件 `media_type` 非 MIME 形状） | ❌已修 | `engine/retain/attachment_content.py`（`media_type` 校验器：`media_type must look like 'type/subtype', got '<值>'`），`loc` 到 `[...,0,"image","source","media_type"]` | `memoryops/retain_models.go:441-452`（`contentMediaTypeRe` + `value_error`）、`:490`（`contentUnionPath`） | 原实现 **500**；现 422，两分支顺序与文案逐字相同 | `TestRetainValidatesContentBlocks/"an attachment media type that is not MIME-shaped"`；`parity_check ... --body '{"items":[{"content":[{"type":"image","source":{"type":"base64","media_type":"bad","data":"aGk="}}]}]}'` → `SAME` |
| POST .../memories（附件 payload 为空 / 非 base64 → 400 而非静默降级） | ❌已修 | `engine/retain/attachment_content.py:558-600`（`validate_and_canonicalize_content` → `ContentValidationError`，handler 转 400，文案 `f"{path_prefix}[{block_index}]: ..."`：`attachment source data is empty`、`attachment source data is not valid base64: Only base64 data is allowed`） | `memoryops/retain_models.go:502-522`（`attachmentPayloadFailure`）、`memoryops/retain.go:388-400`（model 校验后、写前抛出 400） | 原实现把不可解码的 payload 当"无附件"直接 200 落库（内容丢失）；另：`source` 为 `nil` 的块由模型层先报 `missing`，不能在此误判为"空 payload"。现按参考 400 | `TestRetainRejectsUnusableAttachmentPayload`（需 `VisionEnabled` 才走到 canonicalize，与参考的 422/400 先后一致） |
| POST .../memories（伪造 `⟦hs-att:…⟧` 占位符必须被中和） | ❌已修 | 参考 `validate_and_canonicalize_content` 对字符串内容调用 `neutralize_placeholders`：**只有** canonicalizer 能铸造占位符，调用方手写的 token 会被剥掉。实测：`{"items":[{"content":"Look at ⟦hs-att:000000000000⟧ this"}]}` 参考 `documents.original_text` = `"Look at  this"` | `memoryops/retain_models.go:524-570`（`scrubPlaceholders` + `placeholderRe`/`placeholderLookalikeRe`）、`:595-640`（`Flatten` 对字符串与 text 块都过一遍；**该文档自己拥有的**附件 id 豁免，故重发同一文档正文不会删掉自己的截图） | 原实现把 token 原样落库（`content_hash` 也随之不同），等于让一份文档凭文本引用另一份文档的截图。现冲刷 | `TestRetainScrubsForgedAttachmentPlaceholder`（实测参考/本服务 `original_text` 同为 `"the  arrived"`）；手工探针：`GET /v1/default/banks/<b>/documents/tpl-doc` 两侧 `original_text` 逐字相同 |
| POST .../memories（`tags` 列表元素非字符串） | ❌已修 | `MemoryItem.tags` 是 `list[str]`（`api/http.py:10052` 之后的 `MemoryItem` 定义），成员校验逐元素报：`tags: "[\"a\",1]"` 与 `tags: ["a",1]` 都得到 422 `string_type` at `[body,items,0,tags,1]`、`input`:1 | `memoryops/retain_models.go:917-970`（`decodeTagList`/`tagIssue`）、`:99-145`（`RetainItem.UnmarshalJSON` 收集 `tagIssues`）、`memoryops/validation.go:226-232`（按元素位置输出） | 原实现：`tags:"[\"a\",1]"` **200 落库**（把非字符串元素静默丢掉）。现按元素报；JSON-字符串形式解析出列表后同样逐元素校验，纯字符串仍当单标签 | `TestRetainRejectsNonStringTagElement`；`parity_check ... --body '{"items":[{"content":"x","tags":"[\"a\",1]"}]}'` → `SAME`，`--body '{"items":[{"content":"x","tags":["a",1]}]}'` → `SAME` |
| POST .../memories（`entities` 元素缺 `text` / `text` 非字符串） | ❌已修 | `EntityInput` 的 `text` 是必填 `str`：`entities: [{}]` → 422 `missing` at `[body,items,0,entities,0,"text"]`，`input` = 该元素对象 | `memoryops/retain_models.go:470-530`（`EntityInput` 逐成员解码 + `memberIssues`）、`memoryops/validation.go:224-243`（按声明序输出） | 原实现 `entities:[{}]` 直接 200；现按元素自身的成员报 `missing`/`string_type`，并保留原有的"字符串元素" `model_attributes_type` 分支 | 探针 `parity_check ... --body '{"items":[{"content":"x","entities":[{}]}]}'` → `SAME`；`retain_validation_parity_test.go` 既有用例仍全绿 |
| POST .../memories（`async` 默认与 202-vs-200、operation 行、响应体） | ✅一致 | `engine/memory_engine.py:22351-22646`（`submit_async_retain`：父 `batch_retain` op，返回 `{"operation_id": parent, "items_count": len(contents)}` → HTTP 200 `{"operation_id": ...}`）；`_split_contents_into_async_children` `:1451-1500` 从不切碎单个超限 item | `memoryops/retain.go:302-405`（`Retain`）、`:1210-1290`（`BuildRetainResponse`：async 仅 `operation_id`，其余为 null）、`storage/async_retain.go:26`（`DefaultRetainSubBatchTokens = 10000` = 参考 `config.py:1694` `retain_batch_tokens` 默认值） | — | `parity_check` 下 async 与 sync 两路响应键集/键序/状态码均 `SAME`。注：本服务对照组以 `WENYUAN_API_WORKER_ENABLED=false` 启动，async op 停在 `pending` 而参考侧 worker 会跑完——属对照夹具，不是差异 |
| POST .../memories（sync 折叠同 `document_id` 的多个 item） | ✅一致 | `engine/memory_engine.py:6579-6642`（同步路径按 document 顺序折叠，正文以 `"\n"` 连接） | `storage/retain_contents.go:379-386`（`joinDocumentParts`）、`memoryops/retain_fold.go:56,95` | — | `parity_check ... --body '{"async":false,"items":[{"content":"a","document_id":"d"},{"content":"b","document_id":"d"}]}'` → `SAME`（已掩掉模型产出文本差异） |
| POST .../memories（extraction-mode 选择：concise/verbose/custom/verbatim/chunks） | ✅一致 | 参考按 bank 配置 `retain_extraction_mode` **精确**比较每个模式名，空值/未知值走 concise | `extraction/modes.go:10-51`（`resolveMode` 注释即写明"not case-folded"） | — | 五个模式逐一 `parity_check` dry-run-extract → `SAME`（模型输出文本本身的措辞/实体序属 LLM 差异，不计） |
| POST .../memories（Memory Defense 策略、逐 item 事件、`item_count` 计数口径） | ✅一致 | 参考按**提交顺序**对每个 item 报违规位置，并对"全部被拦"判定用提交条数 | `memoryops/retain_defense.go:400-470`（先 screen 原样提交的 items，再折叠加载）、`memoryops/retain.go:418-427`（每 item 一个 `retain.completed`） | — | `TestMemoryDefense*`、`TestRetainCompleted*` 全绿；`parity_check` 造 redact/block 两种策略 → `SAME` |
| POST .../files/retain（未知 bank → 404 而非 500） | ❌已修 | `api/http.py:10359-10526` → `_submit_async_operation` 抛 `Bank '<id>' not found`（404）；`api/http.py:1379` 的 `utf8_errors="warn"` 提示该路由**不**先做 `_require_bank_exists`，404 来自提交环节 | `memoryops/file_retain.go`（提交前 `_submit_async_operation` 等价处拒绝未知 bank） | 原实现返回 `500 internal server error` | 探针 `parity_check POST /v1/default/banks/<不存在>/files/retain` → `SAME`（两侧 404 + 同一句 `Bank 'x' not found`） |
| POST .../files/retain（缺少 `files` / `request` 表单部件） | ✅一致 | 参考两个部件都在框架层校验，按声明序（`files` 先于 `request`）一起报 | `memoryops/file_retain.go:206-222` | 已由本文件 owner 在同一轮补齐（原先只报 `request`） | `parity_check` 三种缺件组合 → `SAME` |
| POST .../files/retain（parser allowlist 拒绝、批次条数/字节上限、`Invalid request JSON:` 文案） | ⚠️存疑 | `api/http.py:10359-10526`；参考的 `Invalid request JSON: ...` 后带 pydantic 的 `[type=..., input_value=..., input_type=...]` 尾缀 | `memoryops/file_retain.go`（allowlist 文案实测逐字相同；JSON 拒绝句尾缀被截短） | 尾缀差异未修：需确认参考那句是否真由 `ValidationError` 渲染（含 `input_type=dict` 等），否则补尾缀会引入新差异 | `parity_check ... -F 'request={bad'` 逐字符比对：共同前缀一致，本服务缺 `[type=...]` 尾缀 |
| POST .../files/retain（顶层 operation 形状：`file_convert_retain` + `task_payload`） | ⚠️存疑 | `engine/memory_engine.py:22658-22768`（`submit_async_file_retain`：每个文件一个**顶层** `file_convert_retain` op，`dedupe_by_bank=False`，`result_metadata={original_filename, document_id}`，返回 `{"operation_ids":[…], "files_count":n}` → HTTP 只回 `{"operation_ids":[…]}`） | `memoryops/file_retain.go:490-600`（本服务只回一个 `retain` 类型的队列 op） | 未修：`file_convert_retain` op 的落库/执行在 `internal/storage` + `internal/worker`（写权限外）。HTTP 响应体 `{"operation_ids":[…]}` 两侧一致，差异只在 `GET /v1/default/banks/{id}/operations` 的行内容 | `parity_check POST .../files/retain` → `SAME`；`GET .../operations` 的 `type` 字段两侧不同（参考 `file_convert_retain` / 本服务 `retain`） |
| POST .../memories/dry-run-extract | ⚠️存疑 | `api/http.py:10194-10282`：这是**扁平**请求体（顶层 `content` 必填），缺 `content` 时框架报 `missing` 且 `input` 是整层 body，实测 `{"items":[…]}` → `"input":{"items":[…]}` | `reporting/memory_routes.go:853-1060`（`DryRunExtract`）+ `reporting/dryrun_content.go:39-43`（`httpx.Missing(…map[string]any{})`） | **未修**：`internal/reporting/` 在本切片写权限外。修法明确：把 `map[string]any{}` 换成解码后的请求体值（同本组 retain 的 `decodedRequestValue`）。其余（空串/空列表/坏 media_type/坏 base64）已 `SAME` | `parity_check ... --body '{"items":[{"content":"x"}]}'` → `DIFF`，只差 `input` 一项；其余子用例 `SAME` |
| POST .../prompts/preview | ✅一致 | `api/http.py:10284-10357`（`renderPromptPreview` + `applyPreviewEditability`） | `reporting/prompt_preview.go:382-760` | — | `parity_check POST .../prompts/preview`（含 `entity_types`、`bank_profile`、`retain_mission` 三种入参）→ `SAME` |
| DELETE .../memories（按 `type` 清除，含 observation 级联） | ✅一致 | `api/http.py:10528-10560` → `delete_bank(...)` `engine/memory_engine.py:11377+`；类型化路径 `engine/memories/pg/engine_curation.py:98-114` 删该类型的 links + `memory_units` + `invalidated_memory_units`，仅当 `fact_type == "observation"` 时另删历史（`memory_engine.py:11479-11486`、`:11511-11513`），`invalidated` observation 数 > 0 时可选触发 consolidation | `memoryops/clear_memories.go`（`ClearMemories`）、`storage/postgres_clear_memories.go`（同样清 observation、排队 relink victim、删该类型的 `unit_entities`/links） | — | `parity_check DELETE .../memories?type=world`、`?type=observation`、`?type=experience`、重复 `?type=`（末值优先）→ 全部 `SAME`（`deleted_count` 为活测量值，另以一银行逐类型建库对拍计数一致） |
| 交叉：畸形 JSON（`NaN` / `Infinity`） | ⚠️存疑 | 参考是 FastAPI + uvicorn **不带 httptools** 的 h11 构建，`NaN`/`Infinity` 被放行后 pydantic 反序列化抛裸 `500 Internal Server Error`（纯文本） | `httpx/laxjson.go`（统一产出 pydantic 风格 422 `json_invalid`） | 未修：这是**全路由横切**的解码策略差异，改动落在 `internal/httpx`（写权限外）。注意参考在装了 httptools 的部署上会改成 422，属于部署相关 | 探针：`--body '{"items":[],"async":NaN}'` 两侧不同（参考裸 500 文本 / 本服务 422） |

**组 D 附注**

- 本组改动全部落在写权限内：`internal/memoryops/{retain.go,retain_models.go,retain_defense.go,inline_attachments.go,validation.go,normalize.go,file_retain.go,clear_memories.go}` 与 `internal/extraction/`。新增测试 `internal/memoryops/retain_content_blocks_test.go`（`TestRetainValidatesContentBlocks`、`TestRetainRejectsUnusableAttachmentPayload`、`TestRetainScrubsForgedAttachmentPlaceholder`、`TestRetainMissingMemberReportsTheItem`）。
- **一处测量到、但未修的写权限外缺陷**：`internal/memoryops/retain_attachments_test.go` 的 `TestFileRetainImageUploadUsesPlaceholder` 在本轮开始后就一直是 FAIL —— 图片上传走 `/files/retain` 后被 `storage` 层某次改动写成了 `memory_units.text=""`（`AttachmentIDs=[d013614dc14a]` 存在、`documents.original_text=""`），而在 retain 侧 `Content.Flatten()` 仍返回正确的占位符文本（已用单测隔离验证：`Content{Text:"⟦hs-att:d013614dc14a⟧"}.Flatten()` 逐字正确）。故 `go test ./internal/memoryops/` 当前有一处**非本组**红。定位归属 `internal/storage`（镜像后端 `writeItemLocked`/`putItemChunksLocked` 一带）。
- 对拍口径：`scripts/parity_check.py` 掩掉时间戳/UUID/`*_tokens`/`MEASUREMENTS` 与 `features.worker`；模型产出的中文事实措辞与实体顺序属 LLM 差异，未计为缺陷。

## 组 E recall / reflect

**审计口径（先说清楚证据种类）**

- 参考 = 线上 `http://127.0.0.1:8888`（读 `127.0.0.1:5432/hindsight` 的 `user_devin` schema）；本服务副本 = `scripts/parity_dev_service.sh 18221`，跑在 `127.0.0.1:15432/wenyuan`。
- **两侧库不同**，因此排名结果的**内容**（UUID、实体 id、命中文本、`*_tokens` 计数）不可逐字节比。本表所有 ✅ 的证据是以下三类之一，且每条都注明用哪一类：
  1. **错误信封逐字节相同**：校验/解码失败的 4xx `{"detail":…}`/`detail` 字符串，与数据无关；
  2. **成功响应形状相同**：用脚本逐层比对键集/类型/嵌套（`entities`、`results[].scores.*` 等），内容不同但形状一致；
  3. **单元测试**：不依赖线上库的纯逻辑（解析器、token 上限、联合项生成）。
- 用户给的「不同库导致的计数差异是夹具噪声，不是缺陷」按此执行：凡差异**只**出现在 `results`/`entities` 的内容与数量上，一律记 ✅ 并在验证列注明「形状同、内容属夹具差异」。
- 有一处**不能**用夹具解释、且已定位为真实缺陷并修复的：`max_tokens` 正数但任何一条都超预算时，参考仍回 1 条（`engine/fact_budget.py:76-86` 的下限），本服务原先回 0 条。这与库无关。

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| POST .../memories/recall（`query_timestamp` 非法 → 400 文案） | ❌已修 | `api/http.py:6216-6224`：`datetime.fromisoformat(request.query_timestamp.replace("Z","+00:00"))`，失败时 `detail=f"…(e.g., '2023-05-30T23:40:00'): {str(e)}"`。注意这里走的是**标准库**解析器，文案是「命名出错分量」型（`Invalid isoformat string: '…'`、`hour must be in 0..23`），**不是** pydantic 字段解析器的位置型文案 | `memoryops/recall.go:295`（改调 `parseStdlibDateTime`）；`memoryops/timeparse.go:426`（`parseStdlibDateTime`）、`:438`（`scanStdlibISO`）、`:466`（`startsCompactDate`） | **已修**：原先 `query_timestamp` 与 `temporal_window` 共用一个解析器，报成 pydantic 的 `input is too short` / `unexpected extra characters at the end of the input`。现按参考另起一个标准库语义解析器（`Z`→`+00:00`、紧凑式 `YYYYMMDD[THH[MM[SS]]]`、秒以下任意位截断到 6 位、`space` 亦作日期/时间分隔符），文案与接受集都对上 | `parity_check POST .../recall --body '{"query":"hello","query_timestamp":"nonsense"}'` → `SAME`；`"1700000000"` → `Invalid isoformat string: '1700000000'` `SAME`；`"2023-05-30T25:40:00"` → `hour must be in 0..23` `SAME`；`"2023-13-45"` → `month must be in 1..12` `SAME`；`"20230530"` / `"2023-05-30"` → 200 `SAME`；`TestQueryTimestampStdlibMessages`（8 失败文案 + 8 接受值，期望值由 CPython 3.11 `datetime.fromisoformat` 直接生成） |
| POST .../memories/recall（`temporal_window` 解析失败的 per-item 422） | ❌已修 | 路由模型 `engine/response_models.py:316-350`（`TemporalWindow.start/end: datetime \| date`）；两侧实测 item 为 `{"ctx":{"error":"<msg>"},"input":"<原值>","loc":["body","temporal_window","start"],"msg":"Input should be a valid datetime or date, <msg>","type":"datetime_from_date_parsing"}`，解析器是 **speedate**（`input is too short`、`invalid character in month`、`unexpected extra characters at the end of the input`；`'10'` 解析成 epoch 秒 10，数字 JSON 也是 Unix 秒） | `memoryops/models.go:218`（`TemporalWindowRequest.UnmarshalJSON`）、`:248`（`decodeTemporalBound`）；`memoryops/timeparse.go:65`（`parsePythonDateTime`）、`:106`（`scanISODateTime`）；`memoryops/validation.go:477`（`temporalWindowErrors`） | **已修**：原先窗口非法统一报 400 一条笼统消息。现按 pydantic 逐边界出 item：缺成员 → `missing`（`input` 是整个 window 对象）、`null`/非字符串 → `datetime_type`、不可解析 → `datetime_from_date_parsing` 带精确 `ctx.error`、数字 → Unix 秒；倒序窗口报在 `["body","temporal_window"]` 上且 `input` 是窗口对象 | `parity_check`：`{"temporal_window":{}}` → 两条 `missing` 按 `start,end` 序 `SAME`；`{"start":null,…}` → `datetime_type` `SAME`；`{"start":"bad",…}` → `input is too short` `SAME`；`{"start":"2023-06-30T00:00:00Z","end":"2023-04-01T00:00:00Z"}` → 倒序 `SAME`；`{"start":1680307200,"end":1688169599}` → 200 `SAME`；`TestRecallTemporalWindowBoundItems`、`TestTemporalWindowAcceptanceForms`、`TestPythonDateTimeParserMessages`（29 例，期望值由 pydantic 2.12.5 生成）、`TestRecallMalformedTimestamp` |
| POST .../memories/recall（`max_tokens` 正数但全集超预算时的下限） | ❌已修 | `engine/fact_budget.py:13-19, 76-86`：「as long as the caller asked for a positive budget, a run that matched something never answers with nothing. When no fact fits at all, the top-ranked one is returned whole and over budget」；非正预算则不适用（`max_tokens=0` 是「只要 chunks 不要 facts」的既定用法，`:57-60`） | `internal/storage/recall_types.go:413`（`capMatchesToTokens` 加 `if len(selected)==0 { return matches[:1] }`）；响应侧下限本已在 `memoryops/recall_result.go:466`（`applyTokenCap`） | **已修**：存储层这层裁剪原先无下限，把匹配全部裁掉，导致 `max_tokens:1` 时 `results` 为 `[]`、`entities` 为 `null`；参考回 1 条。现两层都执行同一下限 | 形状比对脚本（逐层键集/类型）`{"query":"hello","max_tokens":1}` → 参考 `results` 1 条、副本 0 条（修前），修后两侧 `shape True`；`{"max_tokens":0}` 仍为 `entities:null`+`results:[]` `SAME`（非正预算不适用下限）；`TestCapMatchesToTokensKeepsTopMatchOverBudget`（5 子用例：全超、恰好、短者单独入选、非正预算、空输入） |
| POST .../memories/recall（`max_tokens<=0` → 只要 chunks） | ✅一致 | `api/http.py` 的 recall 处理段：`include_entities` 等旁路默认；`engine/fact_budget.py:57-60` 说明 `max_tokens=0` 是「chunks without facts」的既定用法，facts 选空 | `memoryops/recall.go:198-199`（`recallMaxTokens(req)<=0` → `result.EntityStates=nil`）；`memoryops/recall_result.go:466`（非正直接回 nil） | 无 | `parity_check POST .../recall --body '{"query":"hello","max_tokens":0}'` → `SAME`（`{"chunks":null,"entities":null,"results":[],"source_facts":null,"source_facts_truncated":null,"trace":null}`） |
| POST .../memories/recall（`tag_groups` 无一支匹配时的逐分支 item 与优先级） | ❌已修 | `engine/search/tags.py:307-346`：`TagGroup = TagGroupLeaf \| TagGroupAnd \| TagGroupOr \| TagGroupNot`（`union_mode="left_to_right"`）；叶子成员是 `tags`，**没有** `tag`；四支全不匹配时每支各报一条，`loc` 形如 `body.tag_groups.0.TagGroupLeaf.tags`（缺成员）或 `…TagGroupLeaf`（非对象 → `model_attributes_type`）；字段级失败先于请求模型级校验器（`api/http.py:626-628` 的互斥检查） | `memoryops/recall_request.go:40`（`TagGroupsField.UnmarshalJSON` 收集失败项）、`:248`（`tagGroupUnionItems`）、`:131` 附近的 `constrains`；`memoryops/validation.go:448`（失败项优先于体级规则）、`:454`（`tagsSupplied` + `TagGroups.Present` 的互斥判定）、`:468`（`tagsSupplied`） | **已修**（四处）：①`{"tag":"b"}` 原先被当合法叶子接受（`tag` 不在联合里），现报 4 条分支 item；②裸标签数组 `[["a","b"]]` 原先是「兼容旧式」，参考无此分支 → 现 4 条 `model_attributes_type`；③`tags: []` + `tag_groups` 原先放行，参考按**出现即互斥**（`is not None`）报 422；④体级互斥与原笼统 `tag_groups` 消息的优先级错，现字段级 item 优先 | `parity_check`：`{"tags":["a"],"tag_groups":[{"tag":"b"}]}` → 4 条分支 item `SAME`；`{"tags":[],"tag_groups":[{"tags":["a"]}]}` → `value_error` 互斥 `SAME`；`{"tag_groups":[["a","b"]]}` → 4 条 `model_attributes_type` `SAME`；`{"tag_groups":[{"tag":"team:alpha"}]}`、`{"tag_groups":[{"groups":[{"tags":["ops"]}]}]}` → `SAME`；`{"tag_groups":[{"tags":["x"],"tag":"y"}]}`（`tag` 被忽略）→ 200 `SAME`；`{"tag_groups":[{"and":[{"tags":["x"]}]}]}` → 200 `SAME`；`TestTagGroupsRejectUnconstrainedNodes`、`TestTagGroupsAcceptContractShapes`、`TestTagGroupsFieldAcceptsNestedAndLegacy`、`TestRecallOverTagCapIsClientError` |
| POST .../memories/recall（`query` 缺失/空/纯空白、无 body） | ✅一致 | `api/http.py:6108-6118` 的 `@field_validator("query")`：`tokenize_query(v)` 为空即 `ValueError("query must contain at least one word character after normalization")`（`Value error, ` 前缀由框架加）；缺失则 `missing` 且 `input` 为整个 body | `memoryops/validation.go` 的 `queryFieldErrors` | 无 | `parity_check`：`{}` → `[{"input":{},"loc":["body","query"],"msg":"Field required","type":"missing"}]` `SAME`；`{"query":""}` 与 `{"query":"  "}` → `value_error` `SAME`；`{"query":"hello"}` → 200 形状同 |
| POST .../memories/recall（`types` 非法值、`[]`、`opinion`、`budget` 枚举） | ✅一致 | `VALID_RECALL_FACT_TYPES`（`api/http.py:6212`）与 `budget` 的 `Budget` 枚举；`opinion` 是引擎层面被静默丢弃的值 | `memoryops/validation.go:441`（`unsupportedTypes`）；`memoryops/recall.go:110` 起（`opinion` 先过滤再校验） | 无 | `parity_check`：`{"types":["bogus"]}` → `value_error` `SAME`；`{"types":[]}`、`{"types":["opinion"]}` → 200 `SAME`；`{"budget":"ultra"}` → `literal_error` `SAME` |
| POST .../memories/recall（`include` 子对象成员类型错、`include.entities:null`） | ✅一致 | `api/http.py` 的 `RecallInclude` 各成员为模型，非对象 → `model_attributes_type`，`loc` 到 `body.include.<member>` | `memoryops/validation.go:400-419`（`includeMemberErrors`）、`:421`（`includeShapeError`） | 无 | `parity_check`：`{"include":{"entities":null}}` → 200 且 `entities:null`，形状逐层同（`SAME` on shape）；成员类型错 4 例 `SAME` |
| POST .../memories/recall（未知字段忽略） | ✅一致 | 参考请求模型对未声明成员不作处理（`api/unknown_params.py` 只记录 header） | `memoryops/recall.go` 的解码器（未知成员丢弃） | 无 | `parity_check` `{"query":"hello","unknown_field":1}` → 200，形状逐层同 |
| POST .../memories/recall（`tags` 过滤命中为空） | ✅一致 | 空结果走正常 200 路径，各旁路为 `null`/`[]` | `memoryops/recall.go` + `recall_result.go` 的 `BuildRecallResponse` | 无 | `parity_check` `{"query":"hello","tags":["nomatch-xyz"]}` → 形状同（内容/条数属夹具差异） |
| POST .../memories/recall（成功响应整体形状） | ✅一致 | 响应模型 `engine/response_models.py` 的 `RecallResponse`：`entities`/`chunks`/`results`/`source_facts`/`source_facts_truncated`/`trace` 六键常在，`null` 保留；`results[]` 内 `scores.{semantic,keyword,reranker,final}` 四档、`attachments`/`metadata`/`tags` 等恒在 | `memoryops/recall_result.go` 的 `BuildRecallResponse`/`toResultItem`（`json` 标签无 `omitempty`） | 无 | 形状比对脚本逐层键集/类型比对 `{"query":"hello"}` → 两侧 `shape True`（`results` 内容与 `*_tokens` 计数属夹具+模型差异，见口径） |
| POST .../reflect（`reflect_search_observations_max_tokens` < 1） | ❌已修 | 路由模型该字段 `Field(ge=1)`，参考实测 `{"ctx":{"ge":1},"input":0,"loc":["body","reflect_search_observations_max_tokens"],"msg":"Input should be greater than or equal to 1","type":"greater_than_equal"}` | `memoryops/reflect_validation.go`（`GreaterThanEqual` 项） | **已修**：原先只接受该字段、不校验下界（`0`/`-5` 都放行） | `parity_check POST .../reflect --body '{"query":"q","reflect_search_observations_max_tokens":0}'` → `SAME`；`-5` → `input:-5` `SAME`；`TestReflectSchemaValidation` 系 |
| POST .../reflect（`query` 缺失/空、`fact_types:[]`） | ✅一致 | `api/http.py:1604-1620`：`validate_query_not_blank` → `"'query' must not be empty or whitespace-only."`；`validate_reflect_fact_types` → `"fact_types must not be empty. Use null to include all fact types."`；缺失 → `missing` | `memoryops/reflect_validation.go` 的 query/fact_types 校验 | 无 | `parity_check`：`{}` → `missing` `SAME`；`{"query":""}` → `value_error` `SAME`；`{"fact_types":[]}` → `value_error` `SAME` |
| POST .../reflect（`tags` + `tag_groups` 互斥、`tag_groups` 须为 list） | ❌已修 | `api/http.py:1607-1612`（`validate_tags_exclusive`，`value_error` 在 body 上）；**且** `tag_groups: list[TagGroup] \| None`，单个对象 → `{"input":{…},"loc":["body","tag_groups"],"msg":"Input should be a valid list","type":"list_type"}`（与 recall 不同：recall 也接受对象式） | `memoryops/recall_request.go:40` 的 `singleObject`/`rawInput` 记录；`memoryops/reflect_validation.go:152`（`list_type` 项）、`:157`（互斥） | **已修**：原先 reflect 侧用旧的宽松解码，`{"tag_groups":{"tag":"b"}}` 返回 200；参考为 422 `list_type`。现 reflect 记录「体给的是单个对象」并报 `list_type`，recall 仍接受该形状 | `parity_check POST .../reflect`：`{"tags":["a"],"tag_groups":[{"tags":["b"]}]}` → `value_error` `SAME`；`{"tag_groups":{"tags":["b"]}}` 与 `{"tag_groups":{"tag":"b"}}` → `list_type` `SAME`；`{"tags":[],"tag_groups":[{"tags":["b"]}]}` → 互斥 `SAME`；`TestReflectTagsAndTagGroupsMutualExclusion` |
| POST .../reflect（`budget`、`exclude_mental_model_ids`、`max_tokens:"x"`、未知字段） | ✅一致 | 各字段的模型/枚举/类型规则同 recall 侧 | `memoryops/reflect_validation.go`、`memoryops/models.go` 的 `ReflectRequest` | 无 | `parity_check` 4 例 → `SAME` |
| POST .../reflect（成功响应形状） | ✅一致 | 响应模型 `engine/response_models.py` 的 `ReflectResponse`：`based_on`/`text`/`facts`/`mental_models`/`trace`（`trace` 内含 `tool_calls`/`tokens` 等）；空证据时 `based_on` 三个空数组 | `memoryops/reflect_response.go` 的映射 | 无 | 形状比对脚本 `{"query":"hello"}`、`{"budget":"low"}` → 两侧 `shape True`；正文措辞属 LLM 差异（不同 provider），未计为缺陷 |

**组 E 尾注**

- **排名内容不做逐字节比对是刻意的**：参考读 `user_devin`、副本读 `15432/wenyuan`，且两侧 embedding/reranker/LLM provider 不同。凡差异仅落在 `results`/`entities` 内容、条数与 `*_tokens` 计数上，均按「模型与夹具差异」处理 —— 这类差异**不是缺陷**。本表 ✅ 依据的是错误信封逐字节、成功响应形状、以及不依赖线上库的单元测试。
- **词法通道（BM25）的一处已知残差，未修（不在本切片写权限）**：`internal/storage/postgres_recall.go:393-404` 的 `keywordArm` 在 `boundedBM25Text` 为空时直接返回 nil，而参考对中文等无空格查询会用单字词法命中。改法：`tokenizeQuery` 无 token 时回落到 trim 后的原始查询串。因两侧库内容不同，线上无法把它与夹具噪声区分，故记为待裁决项而非 ✅。
- **观测 `context` 空串 vs `null`（未修）**：参考 `engine/memory_engine.py:10508` 一侧把无 context 的观测渲染成 `null`，本服务为 `""`。此项归属观测/consolidation 切片的路由（组 G）与 storage 映射，不在本切片写权限。
- **非本切片的红/欠修，仅报告**：`go vet ./internal/memoryops/` 报 `validation.go:253:4: append with no values`（`item.tagIssues` 循环内的 `append(append(path("tags"), strconv.Itoa(issue.index)))` 少一个参数），属组 D 的 retain 改动（`ValidateRetain` 一带），本切片未触碰。
- **对拍命令**（两处均为本表证据来源）：`scripts/parity_check.py POST /v1/default/banks/default/memories/recall --body '<json>' --service http://127.0.0.1:18221` 与同形的 `.../reflect`；形状比对用 `/tmp/shape.py`（逐层键集/类型，忽略值）。

## 组 F 记忆 CRUD 与历史

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET .../memories/{memory_id}（单条形状） | ✅一致 | `api/http.py:6046-6076`（`get_memory`，无 `response_model`，直接回 `MemoryItem`）；模型 `api/http.py:1138-1210`（`id/text/context/date/fact_type/document_id/mentioned_at/occurred_start/occurred_end/entities/chunk_id/proof_count/tags/metadata/consolidated_at/consolidation_failed_at/state/invalidation_reason/invalidated_at/edited_at/updated_at/source_memory_ids`，`history` 恒 `[]`）；附件附加 `api/http.py:1012-1065`（`_attach_to_memories`：**仅当有附件行时**才加 `attachments`） | `internal/reporting/memory_routes.go:286`（`GetMemory`）、`:260`（`uuidAnyForm`）、`:316`（`memoryIDInvalid`）、`:323`（`memoryUnitNotFound`）；存储 `internal/storage/postgres_memories.go:229` / `internal/storage/memory_memories.go` | 无 | 线上 `GET` 六种 id（world 带 doc+tags+metadata+observation_scopes+consolidated_at、experience 裸行、observation 带 2 源+2 历史、observation 无源、`55555555` 特殊字符文本）× 两侧逐字节 `SAME`；`history:[]` 恒空、`source_memories` 按 `mentioned_at DESC NULLS LAST`、无附件时键缺席——均同。`TestMemoryByIDRoutesNameABadIdentifier` |
| GET .../memories/{memory_id}（id 形状与未找到） | ✅一致 | `api/http.py:6054-6070`：先 `uuid.UUID(memory_id)` 规范化，`ValueError` → `400 "Invalid memory_id: '<id>'"`（`_parse_uuid`）；未命中 → `404 "Memory unit '<id>' not found"`；银行不存在不影响（先取行再判空） | 同左栏三点：`uuidAnyForm`/`memoryIDInvalid`/`memoryUnitNotFound` | 无 | 线上 `not-a-uuid`、`00000000-0000-0000-0000-000000000000`、未知 bank × 有效/非法 id 四组合 `SAME`（含 400/404 精确 detail） |
| GET .../memories/{memory_id}/history | ✅一致 | `api/http.py:6134-6163`（`get_observation_history`；`None` → `404 "Memory unit '<id>' not found"`）；引擎 `engine/memory_engine.py` 的 `get_observation_history`（observation 才有行；world/experience 返回 `[]`）；条目模型 `ObservationHistoryEntry`（`changed_at` + `previous_*` 快照 + `new_source_memory_ids` + `source_facts[]`，每源带 `is_new`） | `internal/reporting/memory_routes.go:590`（`MemoryHistory`）；存储 `internal/storage/postgres_memories.go:626`（`MemoryHistory`） | 无 | 线上 2 条历史（`changed_at` 升序，`is_new` 首条 true/次条 false，`source_facts` 顺序与 `context` null 保留）两侧 `SAME`；world/experience → `[]` 同；`not-a-uuid` 400 同；空 id 404 同 |
| PATCH .../memories/{memory_id}（可改字段集与校验） | ❌已修 | `api/http.py:6078-6132`（`update_memory`，`request: UpdateMemoryRequest` **必填**）；模型 `api/http.py:2777-2858`（至少一个可改字段、`state ∈ {valid,invalidated}`、`fact_type ∈ {world,experience}`、`occurred_*` 可显式 null 表清空）；`model_fields_set` 判显式 null（`:6096-6100`） | `internal/reporting/memory_routes.go:374`（`UpdateMemory`）、`:554`（`bodyInput`）、`:567`（`validateMemoryUpdate`）；存储 `internal/storage/postgres_memories.go:368` | 见下 F-1：`参数错误（body）` 的 422 项形状不对（缺体 / 错类型时 `input` 为 null）。改为 `httpx.BodyDecodeFailure(err, raw)` 渲染，缺体得 `missing loc:["body"]`、错类型得原值（`{"text":5}`→`input:5`、`{"entities":5}`→`input:5`） | `TestMemoryUpdateBodyShape`（已证伪：还原旧 `writeRequiredBodyFailure` 即 FAIL）；线上 14 例 `PATCH`（`''`/空白/`{}`/未知名/`state:"bogus"`/`text:5`/`entities:5`/`fact_type` 两种/`text:"keep me"`/`state:"valid"`/`context`/合法 `occurred_start`）全部 `SAME` |
| PATCH .../memories/{memory_id}（成功路径与状态机） | ✅一致 | `api/http.py:6102-6132` → 引擎 `update_memory_unit`：仅 world/experience 可改（observation 报 `"Memory '<id>' is a <type>; only world/experience facts can be curated..."`）、已 invalidated 不可编辑（`"Cannot edit an invalidated memory; revert it to 'valid' first."`）、`state` 切换可重复可逆、`text` 空白 → `"text must not be empty."` | `internal/reporting/memory_routes.go:374-450`（同序：校验→id 形状→空文本→取行→类型门→invalidated 门→读作用域 404→写作用域 403） | 无 | 线上逐例 `SAME`：改 `text`/`context`/`fact_type`、置/清 `occurred_start`/`occurred_end`、置/清 `entities`、`text:"   "`、`text:""`、invalidate（带 reason）、invalidated 后编辑、重复 invalidate、revert、改回 `state:"valid"` |
| GET .../memories/list（分页、过滤、排序、边界） | ✅一致 | `api/http.py:5815-5906`（`api_list`：`type/q/document_id/entity_id/tags/tags_match/state/consolidation_state/time_field/start_date/end_date/limit=100(ge=0,le=1000)/offset=0(ge=0)`）；引擎 `engine/memories/pg/curation.py`（`ILIKE '%q%'`、`document_id` 精确、`ORDER BY mentioned_at DESC NULLS LAST, created_at DESC`）；时间窗 `engine/time_filter.py`（`time_field` 同时决定过滤与排序、排除 NULL、默认 `created_at`；反向窗 → `400 "Invalid time window: end_date must be after start_date."`） | `internal/reporting/memory_routes.go:23`（`ListMemories`）、`:187`（`nonNegativeIntParam`）、`:232`（`lastDateParam`）；存储 `internal/storage/postgres_memories.go:15`、`internal/storage/memory_memories.go:117`、`internal/storage/memory_bank_helpers.go:78`（`pageBounds`） | 无（本项） | 线上 33 例 `SAME`：默认页、`limit=0/100000/-1`、`offset=-1`、未知名、`q=Alice/alice`、`document_id`、`type`、`state`、`entity_id`、`tags_match`、`time_field` 全轴、`time_field+窗口`、反向窗 400、`start_date=notadate` 400 |
| GET .../memories/list（`limit`/`offset` 超出 int64） | ❌已修 | 参考把 `limit` 当 Python 无界 int 校验（`ge=0, le=1000` 通过），随后 asyncpg 绑定为整数参数才失败：`pgproto/codecs/int.pyx:109-111` + `protocol/prepared_stmt.pyx:205-211` → `500 {"detail":"invalid input for query argument $2: <v> (value out of int64 range)"}` | `internal/reporting/memory_routes.go:187-215`（`nonNegativeIntParam`） | 见下 F-2：本服务原报 `422 int_parsing`。改为 `strconv.ParseInt(...,64)` 并对 `strconv.ErrRange` 直接回 `500` + 逐字驱动消息 | `TestMemoryListLimitBeyondInt64IsDriverError`（已证伪）；线上 `limit=9223372036854775808`、`limit=99999999999999999999` 均 `SAME` |
| GET .../memories/list（过滤值不 trim） | ❌已修 | `api/http.py:5827-5845`：`q`/`type`/`document_id` 直接取自请求模型，引擎只在值为 `""` 时视为「不过滤」；空白填充值因此是「匹配不到任何行」的过滤条件，不是被裁掉的空过滤（`engine/memories/pg/curation.py` 的 `ILIKE` 逐字比较） | `internal/reporting/memory_routes.go:105-107`（三个过滤参数原样传入）；存储 `internal/storage/postgres_memories.go:29`、`internal/storage/memory_memories.go:140` | 见下 F-3：本服务 handler 与两个后端都 `TrimSpace`，导致 `q=+++`、`q=%20Alice%20`、`type=%20world`、`document_id=%20doc-a` 等**忽略过滤返回全页**。改为原样绑定（后端同样去掉 `TrimSpace`） | `TestMemoryListDoesNotTrimFilters`（已证伪）；线上 8 例空白填充过滤 `SAME` |
| GET .../memories/list（重复标量参数绑定**最后一个**） | ❌已修 | 参考的请求模型对重复标量取**最后**一次出现：`limit=1&limit=50` → `limit:50`、`type=world&type=observation` → 按 observation 过滤、`start_date=2024-01-01&start_date=2030-01-01` → 空页（线上实测） | `internal/reporting/memory_routes.go:221`（`queryLast`）、`:232`（`lastDateParam`） | 见下 F-4：`limit`/`offset`/`q`/`type`/`document_id`/`state`/`consolidation_state`/`entity_id`/`time_field`/`tags_match`/`start_date`/`end_date` 全部改走 `queryLast`/`lastDateParam` | `TestMemoryListRepeatedScalarBindsLast`（已证伪：还原 `httpx.Query` 即 `repeated limit = 1, want the final 50`）；线上 8 种重复参数组合（含 `offset`/`q`/`state`/`time_field`/`start_date`/`tags_match`）`SAME` |
| DELETE .../memories/{memory_id}/observations（作用域与计数） | ✅一致 | `api/http.py:9580-9608`（`clear_memory_observations` → `ClearMemoryObservationsResponse(deleted_count)`；`OperationValidationError` → 原码原 msg）；引擎 `engine/memory_engine.py:11828-11882`：`_require_writable(..., memory_id=...)` 先于删除（读出作用域 404 / 写作用域 403），事务内 `_delete_stale_observations_for_memories` + `deleted_count > 0` 时 `requeue_source_memory` | `internal/reporting/memory_routes.go:346`（`ClearMemoryObservations`）、`:358`（`memoryScopeInForce`）、`:366-405`（`clearObservationsAndReconsolidate`，与 `handlers.go:592` 的 `ClearMemoryObservationsRoute` 同序，另加收尾提交）；存储 `internal/storage/postgres_memories.go:674` / `internal/storage/memory_memories.go:898` | 无（本项） | 线上按源 fact id 清、按 observation id 清、`deleted_count` 两侧同；清后 `GET` 观测同；带/不带作用域两路 404/403/200 同（另见 `TestClearMemoryObservationsHonorsTagScope`） |
| DELETE .../memories/{memory_id}/observations（id 不规范化 → 驱动错误） | ❌已修 | `api/http.py:9593-9596`：该路由**不做** `uuid.UUID(memory_id)` 规范化（与 get/patch/history 三条都不同），畸形 id 直达驱动，被 `:9605` 的 `except Exception` 兜成 `500`；参考驱动 asyncpg 用 Python `uuid` 解析，报 `"badly formed hexadecimal UUID string"` | `internal/reporting/memory_routes.go:334`（`memoryIDDriverFailure`）、`:346-357`（wrapper）、`:358`（`memoryScopeInForce`） | 见下 F-5：本服务原把 DB 的 `ERROR: invalid input syntax for type uuid: "not-a-uuid" (SQLSTATE 22P02)` 直接回给客户端（措辞是驱动的，不是参考的）。改为在「无作用域」时对畸形 id 直接回参考的 `500` + `badly formed hexadecimal UUID string`；**有作用域时仍交给作用域门**（参考的 `_require_writable` 在驱动调用之前，所以 scoped 调用者拿到 404/403 而非 500） | `TestClearMemoryObservationsDoesNotNormalizeID`（已证伪）+ `TestClearMemoryObservationsHonorsTagScope` 保持绿；线上 `not-a-uuid`、`00000000-...`、未知 bank 三路 `SAME` |
| DELETE .../memories/{memory_id}/observations（清完触发一次 consolidation） | ❌已修 | `engine/memory_engine.py:11879-11882`：`if deleted_count > 0:` 再取 `resolve_full_config(...)`，`if config.enable_auto_consolidation:` 才 `submit_async_consolidation(bank_id, ...)` 一次——两个条件都必要 | `internal/reporting/memory_routes.go:366-405`（`clearObservationsAndReconsolidate`）、`:407-419`（`autoConsolidationEnabled`：bank 覆盖 → tenant 覆盖 → 部署默认，与参考 resolver 同层序） | 见下 F-6（组 G 交叉报送，经本轮复核**属实**）：原实现删完只返回计数，从不提交。现按参考两条件提交，enqueue 失败只记日志不影响已落地的删除 | `TestClearObservationsSubmitsConsolidationWhenAutoEnabled`（先证伪：`queued 0 operations, want 1`；三子例覆盖 auto 开/关 × 有/无删除）；线上实测：同一 bank 两侧 `DELETE` 均 `{"deleted_count":1}` 且**各排队恰好 1 条 `consolidation:pending`**；把 `enable_auto_consolidation` 置 false 后两侧均 `(none)` |

**组 F 修复清单（6 项，均在 `internal/reporting/memory_routes.go` 内）**
- **F-1 PATCH 请求体 422 形状**：缺体原报 `422 json_invalid`，参考是 `422 missing loc:["body"]`；`{"text":5}`/`{"entities":5}` 的 `input` 原为 `null`，参考是原值 `5`。改为 `httpx.BodyDecodeFailure(err, raw)`。
- **F-2 `limit`/`offset` 溢出 int64**：原 `422 int_parsing`，参考 `500` + asyncpg 驱动消息。
- **F-3 列表过滤被 trim**：`q`/`type`/`document_id` 原被 `TrimSpace`（handler + 两个后端三处），空白填充过滤退化成「不过滤」。
- **F-4 重复标量参数**：原用第一个出现值（`httpx.Query`），参考用**最后一个**。新增 `queryLast`/`lastDateParam` 并让 12 个标量参数全部改道。
- **F-5 clear-observations 畸形 id**：原回 DB 自己的 22P02 措辞，参考回 `badly formed hexadecimal UUID string`（且该路由不做 id 规范化）；wrapper 在无作用域时短路，有作用域时保留作用域门语义。
- **F-6 clear-observations 收尾 consolidation**：原从不提交，参考在 `deleted_count>0 && enable_auto_consolidation` 时提交一次。

**⚠️ 写权限外 / 需组长处理 1 项**
1. `internal/reporting/handlers.go`：为实现 F-5/F-6，`ClearMemoryObservations` 的实现在 `memory_routes.go` 内自带完整路由体（`clearObservationsAndReconsolidate`），`handlers.go:592` 的 `ClearMemoryObservationsRoute` 现已**无人调用**。该文件不在本切片写作用域，**未删除**（避免与组 G 的改动冲突）。建议组长删除 `handlers.go:587-611` 整段（`ClearMemoryObservationsRoute`），或确认保留死代码可接受。`BindRoutes`（`handlers.go:90`）仍绑 `h.ClearMemoryObservations`（`memory_routes.go:346`），行为不受影响。

**夹具说明（非缺陷）**：参考 `:8888` 服务的是 `127.0.0.1:5432/hindsight` 的 `user_devin` 模式且 **worker 开启**，本服务的副本服务跑在 `18216`（共用同一 PostgreSQL 实例，但用独立模式 `audit_f_replica` 播种同一夹具）。两个已知的**夹具噪声**，均不改代码：
- 参考 worker 会自行把 `experience/world` 行consolidate 并写出新观测（如 `consolidated_at` 被填、多出 `59ad1913-…` 观测），故对比时夹具行统一钉死 `consolidated_at`，并按 bank 隔离；
- `edited_at`/`updated_at` 在两侧是各自请求时刻，逐字段比对时掩掉。

**验证命令**（两侧同夹具后逐条 `SAME`）：
```sh
export WENYUAN_PARITY_SERVICE_KEY=sk-hs-f94653018553250b8ffeff305b86ee0a
python3 scripts/parity_check.py GET  /v1/default/banks/audit-f-fx/memories/list --service http://127.0.0.1:18216
python3 scripts/parity_check.py GET  '/v1/default/banks/audit-f-fx/memories/list?limit=1&limit=50' --service http://127.0.0.1:18216
python3 scripts/parity_check.py GET  '/v1/default/banks/audit-f-fx/memories/list?type=%20world' --service http://127.0.0.1:18216
python3 scripts/parity_check.py GET  /v1/default/banks/audit-f-fx/memories/33333333-3333-4333-8333-333333333333/history --service http://127.0.0.1:18216
python3 scripts/parity_check.py PATCH /v1/default/banks/audit-f-fx/memories/22222222-2222-4222-8222-222222222222 --body '{"text":5}' --service http://127.0.0.1:18216
python3 scripts/parity_check.py DELETE /v1/default/banks/audit-f-fx/memories/not-a-uuid/observations --service http://127.0.0.1:18216
```
结果：GET 33 例、PATCH/DELETE 15 例，**48/48 `SAME`**。

## 组 G 观测与 consolidation

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| DELETE /v1/default/banks/{bank_id}/observations | ✅一致 | `api/http.py:9461-9487`（`f"Cleared {result.get('deleted_count', 0)} observations"`）；`engine/memory_engine.py:11643-11710`（**无** bank 存在性检查 → 未知 bank 200/0；`_refuse_bank_wide_if_scoped(..., "clear the bank's observations")` 在 `:11660`；`clear_observations_and_requeue` 见 `engine/memories/pg/engine_curation.py:167-188`：先 COUNT 再 DELETE `fact_type='observation'`，把 `experience/world` 的 `consolidated_at` 置 NULL；`observation_history` 整行删除 `:11692-11695`；stats 缓存失效 `:11704-11707`） | `internal/reporting/handlers.go:528-551`（先 `RefuseBankWideIfScoped`、无 `requireBank`、`invalidateBankStats`）；存储 `internal/storage/postgres_memories.go:674-734`（`clearBankObservationsTx`：COUNT→DELETE→`observation_history`→source 重新入队，同一事务） | 无 | 实测：真实 bank 与未知 bank 两侧同码同体 `{"success":true,"message":"Cleared 0 observations","deleted_count":0}`；新建 `audit-g-*` bank 上同 |
| GET /v1/default/banks/{bank_id}/observations/scopes | ❌已修 | `api/http.py:9490-9535`（`limit` 默认 100、`ge=0 le=1000`；`offset ge=0`；**query 校验先于 handler**）；`engine/memory_engine.py:11747-11782`（`_authorize_bank_read` → `_require_bank_exists` → `observation_scope_counts`）；`engine/memories/pg/counts.py:155-207`：scope = `COALESCE(ARRAY(SELECT unnest(tags) ORDER BY 1), '{}')`，`GROUP BY scope ORDER BY count DESC, scope`，`total` 为 DISTINCT scope 数，响应 `{scopes,total,limit,offset}` | `internal/reporting/handlers.go:554-585`（`boundedIntParam` 先于 `requireBank`；空 tags 渲染 `[]`）；`internal/storage/postgres_observability.go:613-655`（同一 SQL 形状）；`internal/storage/memory_observability.go:573-608`（内存后端同序：count DESC, tags 字典序） | 修 `internal/storage/read_surface.go:303-346`：`decodePostgresTextArray` 原用 `strings.Trim(part, "\"")` **削掉元素两端的全部引号**，`text[]` 里字面含引号的 tag（`say "hi"` 渲染为 `"say ""hi"""`）会被解成 `say ""hi` —— 与参考（驱动返回 Python `list[str]`，不做文本往返）分叉，且直接影响 scope 枚举（scope 就是观测的精确 tag 集）。改为只剥栅栏引号并折叠 `""`→`"`（`trimArrayQuotes`） | `TestDecodePostgresTextArrayKeepsQuotesInsideAnElement`（已证伪：恢复旧 `Trim` 即 FAIL，`"say ""hi"""` → `say ""hi`）；实测 `/observations/scopes?limit=10000`、`limit=-1`、`limit=abc`、`offset=-1`、未知 bank 五路两侧逐字节 `SAME`（422 信封 `loc:["query","limit"]`、404 `Bank '...' not found`） |
| POST /v1/default/banks/{bank_id}/consolidate | ❌已修 | `api/http.py:9690-9736`（`request: ConsolidationRequest \| None`、`submit_async_consolidation(..., observation_scopes=request.observation_scopes if request else None, caller_requested=True)`，返回 `{operation_id, deduplicated}`）；`engine/memory_engine.py:22768-22834`：`caller_requested` 才 `_refuse_bank_wide_if_scoped(..., "run consolidation on the bank")`；**`dedupe = observation_scopes is None`** —— 只要 body 里出现 `observation_scopes`（含 `[]`）就是 scoped、**永不去重**；`_submit_async_operation` `:22120-22135` 对未知 bank 抛 `Bank '<id>' not found`/404，`:22214-22240` 只与 `task_payload->>'observation_scopes' IS NULL` 的 pending 行去重 | `internal/reporting/handlers.go:697-728`（校验→`RefuseBankWideIfScoped`→`requireBank`→ 分派 scoped/full）、`:776-805`（空 body / `null` → 全身；`observation_scopes` 非 null → scoped，`[]` 亦 scoped）、`:741-749`（scoped 分派：`EnqueueConsolidationScoped`，无该能力时退回变参）；存储 `internal/storage/consolidation_scope.go:231-281`、`internal/storage/postgres_memories.go:741-771`、`internal/storage/postgres_enqueue.go:187-204`（只匹配 `status='pending'`，符合参考默认 `dedupe_by_bank_includes_processing=False`） | 修 `internal/reporting/handlers.go:741-749` 的退化分支：原实现在 store 无 `EnqueueConsolidationScoped` 能力时调 `EnqueueConsolidation(ctx, bankID, scopes...)`，空 slice 展开成**零个变参** ⇒ 被当成全身 sweep 去重（另一侧有 pending 时返回别人的 operation_id + `deduplicated:true`）。改为 null 时传 `[][]string{}`，保持「scoped 永不去重」 | `TestReportingScopedConsolidationNotDeduplicated`（app 级端到端：全身→scoped→空 list 三路各自独立 op，`observation_scopes:null` 仍去重回第一路）；`TestReportingConsolidationDeduplicated` 仍过。实测：未知 bank 空 body/`{}`/`null`/scoped/`observation_scopes:null`/`{"bogus":1}` 均 404 同体；`[[1]]` 422 `string_type loc:["body","observation_scopes",0,0]` 同体；未知 bank 上 `[1]` 422 `model_attributes_type`、`"x"` 422 `list_type` 同体；真实 bank 上两次全身提交参考 `deduplicated:false/true`，本服务同（注意：参考侧 worker 开启，第一次会被消费后清空 pending，故本服务单次探测即可对照该布尔语义） |
| POST /v1/default/banks/{bank_id}/consolidation/recover | ❌已修 | `api/http.py:9609-9634`（`return RecoverConsolidationResponse(retried_count=result["retried_count"])`）；`engine/memory_engine.py:11785-11822`：`_refuse_bank_wide_if_scoped(..., "retry the bank's failed consolidation")` 后**直接** `requeue_failed_consolidation`，**无** `_require_bank_exists`、**无** 事务；`engine/memories/pg/engine_curation.py:190-209`：先 COUNT 再 `SET consolidation_failed_at=NULL, consolidated_at=NULL WHERE bank_id=$1 AND consolidation_failed_at IS NOT NULL AND fact_type IN ('experience','world')` | `internal/reporting/handlers.go:678-694`（先 `RefuseBankWideIfScoped`，再单条 UPDATE…RETURNING 计行数）；存储 `internal/storage/postgres_memories.go:792-816`（`WITH requeued AS (UPDATE … RETURNING 1) SELECT count(*)`，与参考同过滤条件，且避免了参考先 COUNT 后 UPDATE 的竞态）；内存后端 `internal/storage/memory_memories.go:1021-1052` | 删掉 `h.requireBank(w, r)`：原实现对未知 bank 返回 `404 {"detail":"Bank '...' not found"}`，参考是 `200 {"retried_count":0}`（retry 只是一条按 `bank_id` 过滤的 UPDATE，从不检查 bank 是否存在；参考的 200 路径也确实什么都不写） | `TestConsolidationRecoverUnknownBankRetriesNothing`（已证伪：恢复 `requireBank` 即 404）；实测未知 bank 两侧 `200 {"retried_count":0}`；真实 bank（已清观测后）两侧 `200 {"retried_count":0}` |
| POST /v1/default/banks/{bank_id}/consolidation-strategies/preview | ✅一致 | `api/http.py:9528-9574`（`request: ConsolidationStrategiesPreviewRequest` 必填）；`api/http.py:2332-2339`（`strategies` 必填、`sample_limit` 默认 5 `ge=0 le=50`）；`engine/response_models.py:718-729`（`ConsolidationStrategySpec` 只声明 `scopes`/`observations_mission`/`max_observations_per_scope`/`consolidation_source_facts_max_tokens`/`consolidation_source_facts_max_tokens_per_observation`，**其余键按 non-strict 丢弃且不做类型校验**）；`engine/memory_engine.py:11715-11745`（`list_observation_scopes(limit=10000, offset=0)`，`complete = total <= len(scopes)`）；`engine/consolidation/consolidator.py:3536-3629`（按位置对齐、每策略单独解析、首个 strategy 赢、`samples[:sample_limit]`）、`:1039-1073`（无可用 rule 或未覆盖任何设置为惰性）、`:997-1012`（未知 `tags_match` 回落 `all`）、`:924-972`（`all`=包含、`exact`=精确覆盖、无 tag scope 永不匹配）；响应模型 `engine/response_models.py:749-793` | `internal/reporting/handlers.go:615-675`（body 校验→`requireBank`→`ListObservationScopes(10000,0)`→`buildConsolidationPreview(..., total <= len)`）；`internal/reporting/consolidation_preview.go:59-175`（框架词表校验）、`:150-166`（三个整型设置声明）、`:375-475`（首次赢/taken/observation/samples）、`:479-498`（`strategyActive`）、`:262-346`（fnmatch+包含/精确覆盖）；`internal/reporting/models.go:190-230` | **无**（一处曾拟改已回退：曾给 `consolidationStrategySpec` 补声明 `consolidation_llm_batch_size`/`_parallelism`/`_max_memories_per_round` 做类型校验，但实测参考对这些键的**任何**值都返回 200——`"x"`/`1.5`/`{"a":1}`/`[1,2]` 均被静默丢弃——补声明会让参考 200 的 body 变 422，故保持原样） | 空 body/`null`→422 `missing loc:["body"]`、`{}`→`missing loc:["body","strategies"]`、`[1]`→`model_attributes_type`、`"x"`→`list_type`、`sample_limit=70/-1`→`less_than_equal/greater_than_equal`、`tags_match:"bogus"`→`value_error loc:[...,"tags_match"]`、`observations_mission:5`→`string_type`、`sample_limit:"3"`/`true` 强转、`consolidation_llm_batch_size:"x"/1.5/{}/[]/4` 一律 200 —— 全部逐字节 `SAME`；空 bank 与 21-scope 真实 bank 上的多策略草稿（含 `tags:[]`、无设置 rule、exact rule、重叠 rule 的 `taken_count`/`handled_by`）两侧同体。`TestConsolidationPreviewIgnoresUndeclaredStrategySettings`、`TestBuildConsolidationPreview` |
| 以上五条路由的**响应形状与键序**（含计数型载荷） | ✅一致 | `ObservationScopesResponse` `api/http.py:2342-2370`（`scopes/total/limit/offset`，`ObservationScope` = `tags`+`count`）；`StrategyScopePreview/StrategyRulePreview/StrategyPreview/DefaultScopesPreview/ConsolidationStrategiesPreview` `engine/response_models.py:749-793`；`DeleteResponse` `api/http.py:2973-2983`；`RecoverConsolidationResponse` `api/http.py:2993-3000`；`ConsolidationResponse` `api/http.py:4637-4641` | `internal/reporting/models.go`（同名结构体与 `json` 标签）；`handlers.go:548-550`（`success/message/deleted_count` 同序） | 无 | 在有 3051 条观测的真实 bank（`coding-agent::wenyuan`）上逐项比对 `scopes`：两侧**键集相同**（`limit/offset/scopes/total`，条目 `count/tags`）、`total` 同为 21、**21 个 scope 的 tag 集列表逐位相同**、均按 `count DESC` 且 tag 已排序、`tags:[]` 为全局 scope。唯一差异是每项 `count`（如 1889 vs 1883），成因见下条 |
| 计数型载荷的差分成因（对照夹具，非缺陷） | ⚠️存疑（已定性：夹具） | — | — | **不改**。实测证据：参考进程 `pid 3367723` 读的是 `127.0.0.1:5432` 上的 PostgreSQL，而 `scripts/parity_dev_service.sh` 与 `etc/wenyuan.yaml:11` 给本服务的共享库是 `15432/wenyuan`。参考侧最新一条观测 `68bcf01b-…`（`mentioned_at 2026-10-19T18:30`）在共享库 `15432` 中**完全不存在**；共享库直查 `memory_units where bank_id='coding-agent::wenyuan' and fact_type='observation'` = **3051**、`count(distinct scope)` = **21**，与本服务 API 完全吻合，而参考 API 报 3064/1889。即：本服务口径与库一致，参考多出的是它自己实例里的行 | 以「tag 集列表逐位相同 + total 相同」为形状证据；计数差值留给组长裁决（要么把参考重指到 15432，要么在对比脚本里按 bank 掩掉计数） |
| 自动 consolidation 开关与 `consolidation.completed` webhook | ✅一致 | 参考：`engine/memory_engine.py:11613-11620`（删除 bank 的失效观测后，若 `config.enable_auto_consolidation` 才 `submit_async_consolidation`）、`:11855-11859`（`clear_observations_for_memory` 删到 >0 且开关开才提交）；事件载荷 `ConsolidationEventData`（`observations_created/updated/deleted` + `operation_id/status`） | 保留路径开关：`internal/memoryops/retain.go:499-515`（retain 后提交，`enable_observations` 与 `enable_auto_consolidation` 逐层取 bank 覆盖再取部署默认）；观测清扫路径：`internal/reporting/memory_routes.go:338-349` → `handlers.go:592-611`（`ClearMemoryObservationsRoute`）**不**提交；参考 `:11855-11859` 在 `deleted_count > 0 and enable_auto_consolidation` 时会提交一次——该路由属**组 F** 的 API 面（`memory_routes.go` 不在本切片写作用域），已通报组长，本切片不改；事件：`internal/app/worker.go:585-610`（完成时 `consolidation.completed`，载荷正是三计数）、`:696-702`（`operation_id`+`status`）、`:104-114`（`ErrNoModel` 不算失败，不派失败事件） | 无（本切片内） | `internal/app/consolidation_failure_event_test.go`、`internal/app/webhook_event_body_test.go` 已覆盖事件形状；开关解析见 `internal/bankapi/config_keys.go:54`、默认值 `internal/bankapi/config_defaults.go:53` |
| GET /v1/default/banks/{bank_id}/observations/scopes（tag-scoped 调用方） | ✅一致 | scope 枚举**不**拒绝 tag-scoped 调用方：`engine/memory_engine.py:11771` 用 `_authorize_bank_read(BankReadOperation.LIST_OBSERVATION_SCOPES)`，把调用方的 `tag_groups` 作为过滤（`counts.py:177` `build_tag_groups_where_clause`），而非 `_refuse_bank_wide_if_scoped`（对照 `:11660`/`:11807`/`:22800` 明写拒绝的三条） | `internal/reporting/handlers.go:554-585`（无 `RefuseBankWideIfScoped` —— 与参考同：不拒绝）；`internal/storage/postgres_observability.go:613-655`/`memory_observability.go:573-608` 尚无 tag-scope 过滤入参 | 无代码改动（见 ⚠️ 存疑：参考会用调用方的 tag scope 收窄计数，本服务不收窄；仅在配置了 `WENYUAN_API_TAG_SCOPE` 类扩展时有别，默认部署无 tag scope，两者等价） | `internal/reporting/bank_wide_refusal_test.go` 明确列出**不**拒绝的路线集合（scopes 不在其中），实测无 scope 时两侧同体 |

## 组 H mental models

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET .../mental-models（列表成功体、分页、`limit`/`offset` 默认与界） | ✅一致 | `memory_engine.py:16567-16680`（`list_mental_models`：先 `_require_bank_exists`，再 `limit: int = Query(100, ge=1, le=1000)`、`offset: int = Query(0, ge=0)`——**`offset` 只有下界、无上界**，`items[offset:offset+limit]` 越界即空页）；响应体 `api/http.py:3470-3490`（`MentalModelListResponse`：`items/total/limit/offset`） | `mentalmodels/handlers.go:55-98`（`List`：先 `GetBank` 404，再 `detailParam`/`tagsMatchParam`/`pageParams`，`pageSlice` 越界返回空）；半径 `helpers.go:77-118`（`pageParams`/`boundedQueryInt`，`max<min` 表示无上界，`offset` 传 `-1` → 无上界） | **已修**：`offset` 原按 `max=0` 校验，`?offset=100` 被拒成 `422 less_than_equal`（参考 200 空页）。现 `offset` 只校验 `ge=0`，`?offset=999999` 与参考同为 200 空页 | `python3 scripts/parity_check.py GET /v1/default/banks/audit-h-<rand>/mental-models?offset=100 --service http://127.0.0.1:18201` → `SAME`；`?offset=999999`、`?offset=5&limit=1` → `SAME`；`TestListOffsetHasNoUpperBound` |
| GET .../mental-models（`detail` / `tags_match` / `limit` / `offset` 空值） | ❌已修 | 框架对**出现即绑定**：`?detail=` 绑定空字符串再被 `Literal` 拒（`api/http.py:7116` 附近的 `detail: Literal["metadata","content","full"] = "metadata"`），`?limit=` 绑定空串 → `int_parsing`，均不是「取默认」 | `mentalmodels/helpers.go:31-58`（`detailParam`/`tagsMatchParam`）、`:65-75`（`scalarQuery`：用 `httpx.QueryPresent` 判存在、取末值、不 trim） | **已修**：原先 `?detail=`、`?tags_match=`、`?limit=`、`?offset=` 都被 `TrimSpace(...)==“”` 当缺失而回落默认 → 200。现按「存在即校验」报 422，`input` 为原始空串，与参考逐字节同 | 线上 4 例 `GET /mental-models?detail=` / `?tags_match=` / `?limit=` / `?offset=` → 全部 `SAME`；`TestListEmptyQueryValuesAreValidatedNotDefaulted`（4 子用例） |
| GET .../mental-models/{id}（单条全量/`detail` 三档、未知 id、列表外路由不探银行） | ✅一致 | `memory_engine.py:16692-16740`（`get_mental_model`：单条路由**不做** `_require_bank_exists`）；`_row_to_mental_model` `memory_engine.py:20458-20560`；响应模型 `api/http.py:3409-3460` | `mentalmodels/handlers.go:102-127`（`Get`：`detail` 默认 `full`，仅 `full` 计算 `is_stale`）；`models.go` 的 `Model`/`applyDetail` | 无。线上 `GET /m1`、`?detail=metadata`、`?detail=content`、未知 id `{"detail":"Mental model 'nope' not found"}` 404，两侧逐字节同 | `parity_check GET /v1/default/banks/audit-h-<rand>/mental-models/m1`（三档）→ `SAME`；`GET .../nope` → `SAME`；`TestMentalModelLifecycle` |
| GET .../mental-models/{id}、PATCH .../{id}（`detail=full` 的 `reflect_response`） | ⚠️存疑 | `_row_to_mental_model` 直接回库内 `reflect_response` 列；参考侧 worker 已把创建时排队的刷新跑完并写回 `{"based_on":{},"mental_models":[],"outcome":"content_preserved_no_new_facts","reflect_skipped":"no_sources_in_scope","text":""}` | `models.go` 的 `ReflectResponse` 字段映射（原样回读库列） | **未改**：差异纯由部署配置造成。对比用副本以 `WENYUAN_API_WORKER_ENABLED=false` 启动（`scripts/parity_dev_service.sh` 固定写死），刷新停在 `pending`，`reflect_response` 仍是 `null`。两侧对该列的读写语义一致，开启 worker 后即为同值；属 harness 配置差异，不是行为差异 | 线上唯一差异字段为 `reflect_response`（`--show` 逐字段比对确认）；`GET .../mental-models/m1?detail=metadata|content` 均 `SAME`（这两档不带该字段） |
| GET .../mental-models（列表侧 `is_stale`） | ✅一致 | `memory_engine.py:16640-16670` 列表对每条调用**批量**判定（不走逐条撤回检查）；单条路由 `16692` 才逐条 | `handlers.go:88-92`（`h.Store.IsStaleBatch`）；单条 `handlers.go:117-120`（`h.Store.IsStale`） | 无（列表/单条分别走批量与逐条两条路径，与参考同构） | 线上 `GET /mental-models`（`is_stale:false`）与 `GET /m1`（`is_stale:false`）两侧同 |
| GET .../mental-models/{id}/history | ✅一致 | `memory_engine.py:16748-16830`（`get_mental_model_history`：返回 `items` 数组，无记录即 `[]`）；history jsonb 双编码（参考写 JSON **文本**，本服务写对象）读取 `memory_engine.py` 的 `_row_to_mental_model` 同款字段 | `handlers.go:349-376`（`History`）；`internal/storage/postgres_mental_models.go` 的 `MentalModelHistory`/`historyEntryFromMap`；`TestMentalModelHistoryBareArray`、`TestRestoreReadsSerializedRowJSON`（storage 层，双编码） | 无。线上空历史两侧同 `{"items":[]}` | `parity_check GET .../mental-models/m1/history` → `SAME`；`TestMentalModelHistoryBareArray` |
| POST .../mental-models（创建成功体、409 重复 id、`id` 空串自动生成、未知名忽略） | ✅一致 | `memory_engine.py:17077-17190`（`create_mental_model`）；`api/http.py:3750-3782`（`CreateMentalModelRequest`：`id: str|None`、`name: str`、`source_query: str` 必填、`tags: list[str]=[]`、`max_tokens: int=2048 (ge=256,le=8192)`、`trigger` 默认）；响应 `api/http.py:3776-3782`（`CreateMentalModelResponse`） | `handlers.go:129-247`（`Create`：懒建银行、`SubtypePinned`、`Description:" "`）；`models.go:50-57`（`CreateRequest`） | 无。线上正常创建、`{"id":"m1"}` 冲突 409 `"Mental model 'm1' already exists in bank '<bank>'"`、`{}` 双 `missing`、`max_tokens:100`/`9000` 的 `ge/le`、未知名忽略，全部 `SAME` | 线上 POST 8 例 → `SAME`；`TestMentalModelLifecycle`、`TestMentalModelCreateValidation`、`TestMentalModelClientSuppliedID`、`TestMentalModelCreateLazilyCreatesBank` |
| POST / PATCH mental-models（多成员错误**一次报全**，顺序与参考一致） | ❌已修 | pydantic 逐字段校验后一次性收集：创建路由 reference 的落表顺序为 `name, source_query, tags, max_tokens, trigger`（线上实测：`{"max_tokens":5,"tags":[5,"ok"],"trigger":{"mode":"bogus"}}` → 该序）；PATCH（`api/http.py:3783-3830`，`UpdateMentalModelRequest`）为严格声明序 `name, source_query, max_tokens, tags, trigger` | `handlers.go:129-176`（创建：`requestItems` 取契约逐成员 items，再追加 `max_tokens` 界与 trigger items）；`handlers.go:249-296`（PATCH：`insertBeforeLoc` 把 `max_tokens` 插到 `tags` 之前）；`helpers.go:196-240`（`maxTokensItems`/`insertBeforeLoc`/`hasLocPrefix`）、`:242-270`（`requestItems`） | **已修**：原先任一成员失败即 `return`（如 `tags:[5]` 掩盖 `max_tokens`/trigger 错误；`max_tokens` 超界掩盖 trigger）。现统一收集，并按各自路由的参考顺序输出 | 线上 `POST {"max_tokens":5,"tags":[5,"ok"],"trigger":{"mode":"bogus"}}`、`PATCH {"tags":[5],"max_tokens":5,"name":5,"trigger":{"mode":"bogus"}}`、`PATCH {"tags":[5],"trigger":{"fact_types":[]}}`、`PATCH {"source_query":5,"max_tokens":100,"tags":"x"}`、`PATCH {"trigger":{"response_schema":{}},"max_tokens":100}`、`PATCH {"trigger":{"tag_groups":[{"tags":["a"],"match":"BOGUS"}]},"tags":[7]}` → 全部 `SAME`；`TestCreateReportsEveryFailingFieldInDeclarationOrder`、`TestUpdateReportsEveryFailingFieldInDeclarationOrder`、`TestCreateTagsShapeIsValidated` |
| POST / PATCH trigger（顶层非对象） | ❌已修 | 框架对「无法抽取成员的输入」报 `model_attributes_type`，`input` 是**该值本身**、`loc` 止于 `body.trigger`（线上 `{"trigger":[]}` → `[{"input":[],"loc":["body","trigger"],"msg":"Input should be a valid dictionary or object to extract fields from","type":"model_attributes_type"}]`；`{"trigger":5}` / `"text"` / `true` 同构） | `mentalmodels/trigger.go:106-170`（`parseTrigger`：`decodeTriggerValue` 解码，非对象 → `httpx.TypeMismatch("model_attributes_type", []string{"body","trigger"}, decoded)`） | **已修**：原报 `dict_type` 且 `input` 为 `null`，丢失了值 | 线上 `{"trigger":[]}`、`{"trigger":5}` → `SAME`；`TestTriggerNonObjectIsModelShapeFailure`（4 子用例） |
| POST / PATCH trigger（成员错误**按模型声明序**、自定义校验器、模型级校验器抑制） | ❌已修 | `api/http.py:3199-3389`（`MentalModelTrigger`）：字段按声明序校验（`budget, mode, refresh_after_consolidation, refresh_cron, min_refresh_interval_seconds, fact_types, exclude_mental_models, exclude_mental_model_ids, tags_match, tag_groups, include_chunks, recall_max_tokens, recall_chunks_max_tokens, reflect_search_observations_max_tokens, reflect_search_observations_include_entities, response_schema, keep_trace`），自定义校验器按声明序追加；`@model_validator(mode="after")` 的 `validate_refresh_exclusivity` **只在字段级全通过时才跑**（框架在任一字段失败后跳过模型级校验器） | `mentalmodels/trigger.go:187-338`（`validTriggerMembers`：按声明序逐成员出 item，字段级失败即 `rejected` 该成员的自定义校验器）；`trigger_members.go:20-56`（成员分组表与 `triggerCustomValidators` 声明序）、`:99-120`（`triggerExclusivityValidator`，调用点 `trigger.go:322-326`：`len(items)==0` 才跑） | **已修**：①原先按第三个成员就 `return`，只报一条；现一次报全且顺序同参考。②**模型级互斥错误原先与字段错误并列输出**，参考只单独报（线上 `{"fact_types":[],"tag_groups":[…fuzzy…],"refresh_after_consolidation":true,"refresh_cron":"0 3 * * *"}` 参考仅 2 条 `value_error`）。③`exclude_mental_model_ids` 项原先完全不校验 | 线上 `PATCH {"trigger":{"tags_match":"no","fact_types":["a","b"],"min_refresh_interval_seconds":-1,"budget":"nope","refresh_cron":"abc","mode":"bogus"}}` 7 条同序、`{"trigger":{"refresh_after_consolidation":true,"refresh_cron":"0 3 * * *","budget":"bad"}}` 仅 1 条、三例「字段失败则抑制互斥」→ 全部 `SAME`；`TestTriggerItemsFollowTheModelOrder`、`TestTriggerExclusivityIsSkippedWhenAFieldFailed`、`TestTriggerExcludeMentalModelIdsShape` |
| POST / PATCH trigger（布尔成员 lax 强制） | ❌已修 | pydantic lax 模式：`bool` 接受 `true/false/t/f/yes/no/y/n/on/off/1/0`（不 trim）与整数 0/1，落库为该布尔值；无法解释者（如数字 2）报 **`bool_parsing`**（不是 `bool_type`） | `mentalmodels/trigger.go:26-33`（`triggerBoolMembers`）、`:210-224`（lax 判定 + `bool_parsing`）、`:360-377`（`coerceTriggerMember` 存入强制后的布尔）；`internal/httpx/laxjson.go` 的 `LaxBoolean` | **已修**：①原先只接受真布尔，`"yes"`/`1` 一律 `bool_type`；②`keep_trace:2` 报 `bool_type`，参考为 `bool_parsing`。另把 `exclude_mental_models`/`include_chunks`/`reflect_search_observations_include_entities` 纳入同一套（原先只有 3 个成员被检查） | 线上 `{"trigger":{"keep_trace":2}}` → `bool_parsing` `SAME`；`{"trigger":{"keep_trace":"yes"}}`、`{"exclude_mental_models":"true"}` → 200 且 trigger 同值 `SAME`；`TestTriggerBooleanMembersTakeLaxCoercion` |
| POST / PATCH trigger（整数成员 lax 强制） | ❌已修 | pydantic lax：`int` 接受数字字符串（Python `int()` 语义，如 `"5"`、`" 5 "`、`"5.0"`）并落库为整数；带小数者 `int_from_float` | `mentalmodels/trigger.go:340-358`（`triggerIntItem` 用 `httpx.LaxFloat`，界 `min_refresh_interval_seconds ge 0`、其余 `ge 1`）、`:360-377`（`coerceTriggerMember` 存 int） | **已修**：原先 `min_refresh_interval_seconds:"5"` 报 `int_parsing`，参考 200 且落库 5 | 线上 `PATCH {"trigger":{"min_refresh_interval_seconds":"5","recall_max_tokens":"7"}}` → 200 且 trigger 同值 `SAME`；`min_refresh_interval_seconds:1.5` → `int_from_float` `SAME`；`TestTriggerIntegerMembersTakeNumericStrings`、`TestMentalModelTriggerIntegerVocabulary` |
| POST / PATCH trigger（`tag_groups` 判别联合的错误形状） | ❌已修 | `engine/search/tags.py:307-340`（`TagGroupLeaf`/`TagGroupAnd`/`TagGroupOr`/`TagGroupNot`，`TagsMatch = Literal["any","all","any_strict","all_strict","exact"]`、`TagResolution = Literal["exact","fuzzy"]`）；联合在**无一支接受**时把**每个分支**的失败各报一条，`loc` 形如 `trigger.tag_groups.0.TagGroupLeaf.match` / `…TagGroupAnd.and`；复合组用别名 `and`/`or`/`not` 且 `populate_by_name=True`（字段名 `filters`/`filter` 亦接受） | `mentalmodels/trigger_members.go:130-300`（`collectTagGroupItems`：`tagGroupFitsAny` 先判「有一支接受」否则逐支报；`tagGroupShapeProblem` 认两种拼写；`httpValidationItem` 的 `missing` 补成员名、`input` 为整节点；叶子 `match` 用五值 `tagLeafMatchValues`） | **已修**：①原实现只匹配「像哪一支」就只报那一支（`match:"BOGUS"` 仅 1 条，参考 4 条）；②复合组只认别名不认字段名；③叶子 `match` 误用 3 值表（应为 5 值）；④`missing` 项缺成员名 | 线上 `{"trigger":{"tag_groups":[{"tags":["a"],"match":"BOGUS"}]}}` 4 条同序 `SAME`、`{"trigger":{"tag_groups":[{}]}}` 4 条 `missing` 同序 `SAME`、`{"trigger":{"tag_groups":[{"tags":["a"],"match":"all"}]}}`/`{"not":{…}}`/`{"and":[…,{"or":[…]}]}`/`[]`/`null` 全 200 `SAME`；`TestTriggerTagGroupsReportPerAlternative` |
| POST / PATCH trigger（`tag_groups` 的 fuzzy 拒绝） | ✅一致 | `api/http.py` 的 `validate_tag_groups_are_exact`：`"resolve='fuzzy' is not supported in a trigger's tag_groups. A stored scope must be exact; use it on a recall or reflect request instead."`（`Value error, ` 前缀由框架加） | `mentalmodels/trigger_members.go:66-96`（`triggerTagGroupsValidator` + `triggerHasFuzzy`） | 无。线上 `value_error` 逐字节同 | 线上 `{"trigger":{"tag_groups":[{"tags":["a"],"resolve":"fuzzy"}]}}` → `SAME` |
| POST / PATCH trigger（`fact_types` 空表 / 枚举） | ✅一致 | `validate_fact_types`：空表 → `"fact_types must not be empty. Use null to include all fact types."`；枚举 `world/experience/observation` | `trigger_members.go:58-66`（`triggerFactTypesValidator`）；`trigger.go:255-272`（列表类型/逐项枚举，`fact_types.0` 定位） | 无 | 线上 `{"trigger":{"fact_types":[]}}` → 1 条 `value_error` `SAME`；`{"trigger":{"fact_types":"world"}}` → `list_type` `SAME`；`{"fact_types":["a","b"]}` → 两条 `literal_error` `SAME` |
| POST / PATCH trigger（`refresh_cron` 语义） | ✅一致 | `validate_refresh_cron`：`strip()` 后空 → `None`，非法 → `ValueError(f"refresh_cron is not a valid cron expression: {v!r}")`（`input` 为**原值**）；非字符串 → `string_type` | `mentalmodels/trigger.go:236-250`（`string_type` / `value_error`，`input` 为原值、消息内为 trim 后的 `repr`） | 无。线上 `refresh_cron:5` → `string_type`、`refresh_cron:"abc"` → `value_error "…'abc'"`、`refresh_cron:"  "` → 200 且原样存 `"  "`，全部 `SAME` | 线上 3 例 `SAME` |
| POST / PATCH trigger（`response_schema` 契约） | ✅一致 | `engine/structured_output.py:202-…`（`validate_response_schema`）：非对象 → `dict_type`；`{}`/`{"properties":{}}` → `"response_schema must define a non-empty 'properties' object"`；属性非对象 → `"response_schema property '<n>' must be an object"`；不支持类型 → `"… has unsupported type '<t>'; expected one of ['array', 'boolean', 'integer', 'number', 'object', 'string']"`；`required` 非列表 → `"response_schema 'required' must be a list of property names"`；引用未知属性 → `"… references unknown properties: ['b']"` | `mentalmodels/trigger_members.go:344-420`（`responseSchemaProblem` + `renderedTypes`）；`:102-112`（`triggerResponseSchemaValidator`） | 无（本切片实现已逐条对齐参考措辞；线上 7 例全部 `SAME`） | 线上 `PATCH` 7 例（`{}`、`{"properties":{}}`、`type:"string"`、属性非对象、`type:"bogus"`、`required:"a"`、`required:["b"]`）→ 全部 `SAME`；`TestTriggerResponseSchemaIsValidated` |
| PATCH .../mental-models/{id}（dict-merge：未点名保留、显式 `null` 存为 null 以清除成员） | ✅一致 | `memory_engine.py:18481-18600`（`update_mental_model`）+ `:19134-19200`（`_merge_trigger`：`merged = {**current, **supplied}`，`null` 原样覆盖）；请求模型 `api/http.py:3783-3830`（`UpdateMentalModelRequest` 全成员可空） | `handlers.go:249-312`（`Update`：`mergeTrigger(current.Trigger, triggerPatch)`）；`trigger.go:390-420`（`MergeTrigger`/`mergeTrigger`）、`:106-130`（`parseTrigger(..., create=false)` 只回客户端点名成员，**保留显式 null**） | 无。线上 `{"trigger":{}}` 无操作、`{"trigger":{"refresh_cron":null}}` 清除、`{"trigger":{"mode":null}}` → `literal_error`、未点名成员存活，全部 `SAME` | 线上 PATCH 多例 `SAME`；`TestUpdateClearsTriggerMembersOnExplicitNull` |
| PATCH .../mental-models/{id}（未知路由/空 patch） | ✅一致 | `memory_engine.py:18481`：动态 `UPDATE`，未点任何列时影响 0 行 → 404（与「不存在」不可区分） | `handlers.go:298-304`（空 patch 走 `modelError(id, storage.ErrMentalModelNotFound)`） | 无。线上 `PATCH /m1 {}` 与 `PATCH /nope {"name":"z"}` 同 404 同 body | 线上 2 例 `SAME` |
| DELETE .../mental-models/{id}、POST .../{id}/clear | ✅一致 | `memory_engine.py:19056-19130`（`delete_mental_model`，回 `{"status":"deleted"}`）；`memory_engine.py:18963-19040`（`clear_mental_model`：清 `content`/`reflect_response` 并回整条模型，`last_refreshed_at` 不变） | `handlers.go:314-330`（`Delete`）、`:332-347`（`Clear`） | 无。删除首个 200 `{"status":"deleted"}`、重复删除 404、清除回整条模型（仅 `reflect_response` 为配置差异，见上） | 线上 `DELETE /m2`×2、`POST /m1/clear`、`POST /nope/clear`、`GET /m2` → 全部 `SAME` |
| POST .../mental-models/{id}/refresh（入队 + 202/200 响应） | ✅一致 | `memory_engine.py:23188-23280`（`submit_async_refresh_mental_model` → `{"operation_id":…, "status":"queued"}`）；`memory_engine.py:23380-23440`（`_raise_if_mental_model_refresh_unavailable`）；响应模型 `api/http.py:4812-4830`（`AsyncOperationSubmitResponse`） | `handlers.go:378-402`（`Refresh`）；`internal/app/mental_model_refresh.go`；入队 `internal/storage/mental_model_refresh.go` | 无。线上已知模型 → `{"operation_id":…, "status":"queued"}` 同形、未知模型 → `404 {"detail":"Mental model nosuch not found in bank '<bank>'"}` 同 body | 线上 `POST /m1/refresh`、`POST /nope/refresh` → `SAME`；`TestRefreshRefusesWithoutModelConfigured` |
| POST .../mental-models/{id}/dry-run-refresh（响应体形状 / 不落库） | ✅一致 | `memory_engine.py:18384-18480`（`dry_run_refresh_mental_model`）；结果模型 `engine/mental_model_refresh.py:304-…`（`MentalModelDryRunRefreshResult`：`based_on/candidate_content/current_content/delta_operations/diff/duration_ms/effective_mode/facts/mental_model_id/mode_fallback_reason/…/trace`，`trace` 内 `Window`/`FactCounts`/`DeltaOperations`/`TraceToolCall`/`TokenUsage`） | `handlers.go:404-…`（`DryRunRefresh`）；`internal/app/mental_model_dry_run.go`；`mentalmodels/dryrun.go`（`DryRunTrace`/`DryRunUsage` 等结构） | 无（字段集合、嵌套形状、`trace` 内容全部同）。唯一差异是实测耗时 `duration_ms`/`trace.duration_ms`（参考 4–5ms、本服务 1ms），属真实计时值不可比；另 `recorded_at` 小数位为 9 位（参考 6 位），见尾注 | 线上 `POST /m1/dry-run-refresh` 逐字段比对：仅 `duration_ms` 不同；`POST /nope/dry-run-refresh` → `404 {"detail":"Mental model nosuch not found"}` `SAME`；`GET .../dry-run-refresh` → 两侧同 405；连续两次 dry-run 前后模型字节完全不变（**确认无写入**） |
| 路由集合 / 方法 / 未知银行 | ✅一致 | `api/http.py:6920-7300` 注册的 10 条 mental-model 路由；列表路由 `_require_bank_exists` → 404 `"Bank '<id>' not found"`，其余单条路由不探银行 | `internal/mentalmodels/handlers.go:424-…`（`BindRoutes`）；`internal/app/mental_models.go:13-27`（`mountMentalModels`） | 无。线上 `GET /mental-models`（未知银行）→ 404 同 body；`GET /nope`、`POST /nope/refresh`、`POST /nope/dry-run-refresh`、`POST /nope/clear`、`DELETE /nope` 均同码同 body | 线上 10 条路由 + 5 例未知模型/银行全部 `SAME`；`TestMentalModelUnknownBank`、`TestMentalModelErrorMapping` |

**组 H 尾注（未改、需裁决）**

- `reflect_response` 在上表两处标 ⚠️：差异完全来自对比环境（副本固定 `WENYUAN_API_WORKER_ENABLED=false`，参考侧 worker 在线）。若以「开启 worker 的参考」为基线，本服务对该列的读回逻辑一致，无代码偏差。
- dry-run 的 `duration_ms` 为真实测量值、`recorded_at` 小数位为 9 位（RFC3339Nano）而参考为 6 位（微秒）。后者严格说是一处编码差异，但 `scripts/parity_check.py` 把时间戳整体归一化，故探测显示为 `SAME`；是否改为固定 6 位需组长裁决（改点位于 `internal/app/mental_model_dry_run.go` 的时间格式化，超出「最小 diff」范围）。

## 组 I knowledge base

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET /v1/default/banks/{bank_id}/knowledge-base/tree | ✅一致 | `api/http.py:7304-7330`（`tag_filter: KnowledgeTagFilter = Depends(_knowledge_tag_filter)`，再 `list_knowledge_nodes(with_staleness=True, ...)` → `KnowledgeTreeResponse(roots=_build_knowledge_tree(nodes))`）；`api/http.py:3691-3713`（`_knowledge_tag_filter`：`tags`/`tags_match`/`tag_groups`，其中 `tag_groups` 走 `_TAG_GROUPS_ADAPTER.validate_json`，解析失败即 `422 detail=f"Invalid tag_groups: {e}"`）；`:3721-3733`（`_build_knowledge_tree`：父不在列表里的节点当根）；`:3674-3690`（`_knowledge_node_model`：页面取 `source_query`/`tags`/`last_refreshed_at`/`is_stale`/`trigger`，文件夹这些为 null）；`engine/memory_engine.py:19470-19545`（`_knowledge_read_filter` → `_require_bank_exists` → `_resolve_fuzzy_tag_groups` → 单次查询后在 **Python 里排序** `_knowledge_tree_sort_key`，再 `_prune_knowledge_rows` 按 `tag_filter` 剪枝、`_knowledge_rows_in_tag_scope` 按调用方 scope 剪枝，folder 不谈 staleness）；`:1945-1949`（排序键 `(sort_order is None, sort_order or 0, name or "")`）；`:1932-1943`（剪枝只保留命中的 page 及其祖先 folder） | `internal/reporting/knowledge.go:31-86`（先校验 query 参数，再 `requireBank` → `resolveKnowledgeFilter` → `ListKnowledgeNodes` → `pruneKnowledgeNodes` → `pruneKnowledgeNodesToScope` → 逐页算 staleness）；`internal/reporting/knowledge_projection.go:15-39`（`nestKnowledge`）、`:44-68`（`pruneKnowledgeNodes`）、`:78-136`（`pruneKnowledgeNodesToScope`）、`:153-176`（`projectKnowledgeNode`）；`internal/storage/postgres_knowledge.go:38-68`（同一排序键，`sort.SliceStable` 保证与 Python 稳定排序一致）、`:16-27`（`LEFT JOIN mental_models` 同参考的 `_kp_join`） | 修 `internal/reporting/knowledge.go:31-46`：**参数校验移到 bank 查询之前**（参考里 `tag_filter` 是 `Depends`，框架在进 handler 体前就解析完，所以坏参数压过 404）；修 `internal/reporting/knowledge_tags.go:77-99` + `:28-40`：`not` 由 `[]knowledgeTagGroup` 改为 `*knowledgeTagGroup`（参考 `engine/search/tags.py:333-337` 是 `filter: TagGroup` 单个子节点、`:541` 为 `not _match_group(..., group.filter)`，此前的列表语义既接受参考拒绝的文档、又按 AND-取反算错）；新增 `internal/reporting/knowledge_tag_groups.go`（整份 `knowledge_tag_groups.go`）复刻 `TypeAdapter(list[TagGroup])`；新增 `internal/reporting/serde_json_error.go` 复刻该参数的 JSON 解析器措辞 | 实测：空 bank/有 4 节点 bank 上 `tree` 逐字节 `SAME`（含 `sort_order` 后按码点排序、`Ätzend` 排最后）；`tags=team`、`tags_match=exact`、合法 `tag_groups`、`[{not:{tags:[team]}}]`、fuzzy `teem→team`、fuzzy+`exact` 六路 `SAME`；`tag_groups` 的 42 种输入（语法错、非 list、非对象、缺字段、`match`/`resolve` 越界、`tags` 含非字符串、`not` 传 list、键序）全部与参考**逐字节**相同。测试 `TestKnowledgeTagGroupsValidationReport`、`TestKnowledgeTagGroupsReportKeepsInputKeyOrder`、`TestKnowledgeTagGroupsAcceptsWellFormedTrees`、`TestKnowledgeTagGroupsRefusesNotAsList`、`TestKnowledgeTagGroupNotNegatesItsSingleChild`、`TestKnowledgeRoutesValidateParametersBeforeTheBankLookup`、`TestKnowledgeTagGroupsShapeIsValidatedOnBothRoutes`（均已证伪） |
| GET .../knowledge-base/search | ✅一致 | `api/http.py:7457-7490`（`q: Query(..., min_length=1)`、`limit: Query(10, ge=1, le=50)`、`tag_filter = Depends(...)`，返回 `KnowledgePageSearchResponse(results=[...], total=len(results))`）；`engine/memory_engine.py:19602-19780`（先 `_knowledge_read_filter` 再 `tag_scope` 直接并入 filter；`query.strip()` 为空则返回 `[]`；`limit = max(1, min(limit, 50))`；`fetch = min(max(limit*4, 40), 200)`；BM25 + 向量两臂 RRF 融合，`_KNOWLEDGE_RRF_K=60`、`_KNOWLEDGE_RRF_NORM=61`（`memory_engine.py:144-145`），片段 `LEFT(mm.content, 280)` → `_knowledge_snippet`）；`api/http.py:3691-3713`（同树的 `tag_filter` 依赖） | `internal/reporting/knowledge_search.go:86-175`（先 `q`/`limit`/tag filter，再 `requireBank`）、`:195-245`（`q` 的 `missing`/`string_too_short`、`limit` 的 `int_parsing`/`ge`/`le` 信封）；`internal/storage/postgres_knowledge.go:239-263`（`knowledgeRRFK=60`/`knowledgeRRFNorm=61`/`fetch` 同公式）、`:280-406`（两臂 RRF SQL、`COALESCE(mm.content,'')` 后由 `knowledgeSnippet` 截 280）；`internal/reporting/knowledge_render.go:60-75`（`knowledgeSnippet`，空正文给 `No content yet.`，与 `_knowledge_snippet` 同） | 修 `internal/reporting/knowledge_search.go:86-110`：`q`/`limit`/tag filter 三项校验全部移到 `requireBank` 之前，顺序同参考的声明顺序（`q` → `limit` → `Depends`） | 实测：`search?q=deploy`、`q=how`、`limit=1`、`tags=team`、`tags_match=exact` 全 `SAME`；缺 `q`、`q=`、`q=%20%20%20`、`limit=0/51/abc/空/-1`、`tags_match=bogus` 逐字节 `SAME`；未知 bank 上坏参数返回 422 而非 404（修复前 404）。测试 `TestKnowledgeRoutesValidateParametersBeforeTheBankLookup` |
| POST .../knowledge-base/folders | ✅一致 | `api/http.py:7332-7358`（`status_code=201`，`response_model=KnowledgeNode`；`create_knowledge_folder` 抛 `ValueError` → 400 `str(e)`）；`engine/memory_engine.py:19277-19322`（`_require_knowledge_nodes_in_tag_scope` → `_ensure_bank_exists` → `_kp_lock_bank` → `_kp_assert_folder_parent`（`:19242-19254`：父不存在 `Parent folder '<id>' not found`，父不是 folder `Parent '<id>' is not a folder`）→ INSERT `kind='folder'`）；`:19193-19225`（`_row_to_knowledge_node` / `_KP_COLUMNS`） | `internal/reporting/knowledge.go:83-141`（`CreateKnowledgeFolder` → `createKnowledgeNode`：`requireOrCreateBank` → 解 body → `assertKnowledgeFolderParent` → `PutKnowledgeNode`，201 + `projectKnowledgeNode`） | 无 | 实测：`{"name":"Deploys"}` 两侧 201 且**逐字段同形**（id/时间戳仅值不同）；`{}`→422 `missing loc:["body","name"]`、`{"name":null}`/`{"name":5}`→422 `string_type`、`{"name":""}`→201、`parent_id:"nope"`→400 `Parent folder 'nope' not found`、多余键→201 —— 全部 `SAME` |
| POST .../knowledge-base/pages | ✅一致 | `api/http.py:7360-7411`（`status_code=201`，`response_model=CreateKnowledgePageResponse`；`create_knowledge_page(content="", tags=body.tags if body.tags else None, trigger=body.trigger.model_dump(exclude_unset=True) if body.trigger else None)`；`node is None` → **409** `A page named '<name>' already exists in this folder`；再 `submit_async_refresh_mental_model` 拿 `operation_id`）；`engine/memory_engine.py:19323-19468`（唯一名冲突返回 `None`） | `internal/reporting/knowledge.go:88-90`、`:98-136`、`:140-191`（`createKnowledgePageNode`：先 `PutMentalModel` 再 `PutKnowledgeNode`，失败回滚 model，再 `enqueuePageRefresh`）；`:268-282`（`rejectDuplicatePage`，`strings.EqualFold` = 参考的小写唯一索引）；`:195-220`（`knowledgePageMaxTokens` 默认 4096、`knowledgePageTrigger` 四层合并且 request 最优先） | 无 | 实测：`{name,source_query,tags:["team","type:runbook"]}` 两侧 201、三键同形；重名与**仅大小写不同**的重名都 409 同体；`parent_id:"zzz"` 400、`name:""` 201、未知 bank 上的建页/建夹同码 —— 全部 `SAME`。参考 `#3506` 那条「trigger 只送客户端显式设置的键」由 `overlayTrigger`+`MergeTrigger` 语义覆盖，实测含 trigger 的建页两侧同形 |
| GET .../knowledge-base/pages/{page_id} | ✅一致 | `api/http.py:7492-7518`（`get_knowledge_page` 返回 None → **404** `Knowledge page '<id>' not found`；`_knowledge_page_response` `:3735-3747`）；`api/page_markdown.py`：`page_type`（首个 `type:<x>` 胜出、所有 `type:` 标签从展示标签里剔除、缺省 `knowledge-page`）、`_scalar`（双引号 + `\`/`"`/`
` 转义、去 `
`）、`frontmatter`（键序 id/type/title/description/tags/timestamp）、`render_frontmatter`（`None` 与空 list 整键丢弃）、`render_document(notice_when_empty=True)`（空正文 → `No content yet.`），时间戳 `last_refreshed_at or created_at` | `internal/reporting/knowledge.go:285-311`（无 `requireBank`，未知 bank 走页面自身 404；folder 读成 404 同参考的 `kind='page'` join）；`internal/reporting/knowledge_render.go:44-58`（`knowledgePageType`）、`:88-131`（frontmatter 渲染与字段序）、`:137-147`（`renderKnowledgeDocument` + 空正文提示）；`internal/reporting/knowledge_projection.go:226-237`（`writeKnowledgePage`） | 无 | 实测：内容含 `"`、`\`、换行、`type:runbook`、`a:b`、空格的页面，两侧 `markdown` **逐字节相同**（含 `"`/`
`/`\` 转义、`tags:` 列表缩进、统一 `No content yet.`）；folder 的 page 读 404、未知 page 404 同体 |
| PATCH .../knowledge-base/nodes/{node_id} | ✅一致 | `api/http.py:7520-7601`（`parent_id` 用 `body.model_fields_set` 判断是否移动（显式 null = 移到根）；`source_query is not None` 才触发 refresh；`updated is None` → 404 `Knowledge node '<id>' not found`；`ValueError` → 400）；`engine/memory_engine.py:19874-20048`（全 null body → `ValueError("Provide name, parent_id, source_query, tags, max_tokens, and/or trigger to update")`；`moving and new_parent == node_id` → `ValueError("A node cannot be its own parent")`；**单事务**，重命名不越过失败的移动；对非 page 下页面选项 → 返回 None 即 404） | `internal/reporting/knowledge.go:315-419`（`UpdateKnowledgeNode`：无 `requireBank`、`KEEP_PARENT` 同语义、单事务 patch、`source_query` 变更后 enqueue refresh）；`internal/storage/postgres_knowledge_patch.go` | 无 | 实测：改名、`{"parent_id":null}` 移根、`{}`→400 `Provide name, parent_id, ...`、`parent_id` 自指→400 `A node cannot be its own parent`、移到不存在的父→400 `Parent folder 'zzz' not found`、对 folder 下 `tags`/`max_tokens`/`trigger`→404、未知节点改名→404 —— 全部 `SAME` |
| DELETE .../knowledge-base/nodes/{node_id} | ✅一致 | `api/http.py:7603-7627`（`deleted` 为假 → 404 `Knowledge node '<id>' not found`，否则 `{"status":"deleted"}`）；`engine/memory_engine.py:20050-20136`（递归收子树 → 先删 mental_models 再删 knowledge_pages，最后 `_deindex_knowledge_pages`） | `internal/reporting/knowledge.go:421-434`（先 `subtreeKnowledgeModels` 再 `DeleteKnowledgeNode`，逐个 `DeleteMentalModel`）；`internal/storage/postgres_knowledge.go:162-237`（递归 CTE 收子树、带 `path` 防环、先删 pages 再删 models） | 无 | 实测：未知节点 404 同体；把 page 移入 folder 后删 folder，两侧都 `{"status":"deleted"}`、root 数 4→3、被删 page 再读 404（子树与 backing mental model 一并消失） |
| GET .../knowledge-base/export | ✅一致 | `api/http.py:7413-7455`（`KnowledgePageBundleResponse(files=[index.md, <id>.md, <id>.log.md…])`；index 由 `render_index(export.nodes)`，页面文档**不带**空正文提示，日志仅在有 history 时产出 `render_log`）；`api/page_markdown.py:139-156`（`render_index`：嵌套、folder `- **name/**`、page `- [name](./<id>.md) — <source_query or description>`、无节点时 `_No knowledge pages yet._`）、`:158-192`（`render_log`）；`engine/memory_engine.py:20137-20210`（`_require_bank_exists` 后逐页 `get_knowledge_page` + `get_mental_model_history`） | `internal/reporting/knowledge_search.go:15-83`（index 先行、逐页 `<id>.md`、有 history 才出 `<id>.log.md`、按 scope 剪枝）；`internal/reporting/knowledge_render.go:152-180`（`renderKnowledgeIndex`）、`:186-211`（`renderKnowledgeLog`） | 无 | 实测：同一 seed 的两侧 export `files[].path` 序列相同、`index.md` **逐字节相同**（含嵌套缩进与 ` — desc` 后缀、空库的 `_No knowledge pages yet._`）；两份 `<id>.md` 与 `<id>.log.md` 内容除 id/时间戳外逐字节相同 |
| `tag_groups` 的 `fuzzy` 解析（树与搜索共用） | ❌已修 | `engine/search/tag_resolution.py`（`MIN_SIMILARITY=0.45`、`MAX_VOCABULARY=5000`、`MAX_EXACT_COMBINATIONS=32`；`_split_namespace` 后**只对 value 求相似度**；`any`/`any_strict` 展成单个并集、`all`/`all_strict` 展成每 token 一个并集再 AND、`exact` 展成组合的 OR 且丢掉退化组合；先判分支数上限）;`engine/trigram.py:11-19,39-52`（`trigram_set`：**按 pg_trgm 逐词**取三元组，`TRGM_WORD=[^\W_]+`，每个词补「两个前导空格 + 一个尾随空格」） | `internal/storage/tag_resolution.go:22-279`（三个上限、按命名空间、三种模式的展开规则均与参考一致）；`internal/storage/memory_recall_tags.go:181-206`（`similarTag`：命名空间 + value 相似度 ≥ 0.45） | 修 `internal/storage/memory_recall_tags.go:218-228`：`trigramJaccard` 原为**整串**取三元组（`"  " + value + "  "`），既多补了一个尾随空格、又没按词切分，于是任何含空格/标点的标签相似度都算错，且会把**参考能解析的标签判为不解析**（`mongp`/`mongo` 参考 0.50、原实现 0.40，0.45 是阈值）；改为复用包内已有的 pg_trgm 正确实现 `entityNameTrigramSimilarity`（`internal/storage/retain_contents.go:172-205`），删掉错误的 `trigrams` | `TestTrigramJaccardMatchesReferencePgTrgmScores`（参考文档自带的 0.57/0.50/0.73/0.64/0.20/0.33/0.14 七个分值，旧实现 7 条全错）、`TestTrigramJaccardIgnoresSeparatorsBetweenWords`（`team alpha`/`team-alpha` 应 =1.0，旧实现 0.6）、`TestSimilarTagResolvesAtReferenceFloor`（`mongp→mongo` 必须解析）、`TestResolveTagGroupsMatchesSpacedTokenAgainstHyphenatedTag` —— 均已证伪。实测：`tree?tag_groups=[{tags:[teem],resolve:fuzzy}]`、fuzzy+`match:"exact"` 两侧 `SAME` |
| `tag_groups` 结构校验（422 报告） | ❌已修 | `api/http.py:3684-3713`（`_TAG_GROUPS_ADAPTER: TypeAdapter[list[TagGroup]]`，`validate_json` 抛 `ValidationError` → `HTTPException(422, detail=f"Invalid tag_groups: {e}")`，**detail 是整份 Pydantic 报告字符串**）；`engine/search/tags.py:307-346`（`TagGroupLeaf{tags: list[str], match: TagsMatch="any_strict", resolve: TagResolution="exact"}`、`TagGroupAnd{filters, alias "and"}`、`TagGroupOr`、`TagGroupNot{filter: TagGroup, alias "not"}`，`TagGroup = Annotated[..., Field(union_mode="left_to_right")]`） | `internal/reporting/knowledge_tags.go:207-232`（`knowledgeTagFilterFromRequest` 改为走新解码器）；`internal/reporting/knowledge_tag_groups.go:101-165`（入口 + 报告渲染）、`:166-215`（left-to-right union：四个成员任一通过即整体通过，全不通过则**报告四个成员的失败**）、`:23-60`（union 成员表与两个 Literal）；`internal/reporting/serde_json_error.go:23-26`（该参数的 JSON 解析器措辞） | 新增 `internal/reporting/knowledge_tag_groups.go`（`knowledge_tag_groups.go`）与 `internal/reporting/serde_json_error.go`：原实现只做 `json.Unmarshal`，**任何能解析的 JSON 都被接受**（`{}`、`[1,2]`、`[{"tags":"x"}]`、`{"not":[]}` 都当成合法过滤器，等于静默不做过滤），且语法错时把 Go 的 `invalid character 'o' in literal null` 当 detail；现在复刻 Pydantic 报告（`N validation errors for list[union[...]]`、`0.TagGroupLeaf.tags` 之类的位置、`[type=..., input_value=..., input_type=...]`、文档链接、`input_value` 保持**文档键序**、根级错误不打印位置行），并把 JSON 语法错误改用该参数解析器（serde_json 风格：`expected ident` / `trailing characters` / `EOF while parsing a list` / `trailing comma` …）而非 CPython json 模块的措辞 | `TestKnowledgeTagGroupsValidationReport`（4 个逐字节 detail）、`TestKnowledgeTagGroupsReportKeepsInputKeyOrder`、`TestKnowledgeTagGroupsAcceptsWellFormedTrees`、`TestKnowledgeTagGroupsRefusesNotAsList`、`TestKnowledgeTagGroupsShapeIsValidatedOnBothRoutes`（均已证伪：换回旧的宽松 `json.Unmarshal` 即 FAIL）。实测 42 种输入与参考**逐字节** `SAME` |
| 参数校验先于 bank 查询（树 / 搜索） | ❌已修 | `api/http.py:7312-7322`、`:7469-7475`：`tag_filter: KnowledgeTagFilter = Depends(_knowledge_tag_filter)`、`q: Query(..., min_length=1)`、`limit: Query(10, ge=1, le=50)` —— 依赖与 Query 声明都由框架在**进入 handler 体之前**解析，坏的参数先于 `_require_bank_exists` 报 422 | `internal/reporting/knowledge.go:31-46`、`internal/reporting/knowledge_search.go:86-110` | 修：把参数校验整体前移到 `requireBank` 之前（树：`tags_match`/`tag_groups`；搜索：`q` → `limit` → tag filter，同参考声明顺序） | 实测：未知 bank 上 `?tags_match=bogus`、`?tag_groups={}`、`search` 缺 `q`、`?limit=0` 均返回 422 而非 404（修复前 404）。测试 `TestKnowledgeRoutesValidateParametersBeforeTheBankLookup`（修复前 5/5 子用例 FAIL） |

## 组 J directives

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET /v1/default/banks/{bank_id}/directives | ✅一致 | `api/http.py:7620-7673`（`tags` 别名 `tags_filter`、`tags_match: Literal["any","all","exact"]=Query("any")`、`active_only: bool=Query(True)`、`limit: Query(100, ge=1, le=1000)`、`offset: Query(0, ge=0)`，全部是 `Query(...)`，即**进 handler 体之前**就解析校验；返回 `DirectiveListResponse(items, total, limit, offset)` 四个键）；`engine/memory_engine.py:20713`（`await self._require_bank_exists(bank_id)` 在 handler 之后，故坏参数压过 404）；`:20805-20812`（`ORDER BY priority DESC, created_at DESC, id DESC`——注释说明 id 兜底是为了分页稳定）；`:20724-20745`（未给 tag 过滤时返回**全部**指令，与 reflect 不同；无匹配的未打标指令始终生效） | `internal/directives/handlers.go:86-156`（校验 → `requireBank` → `ListDirectives` → 内存侧过滤/分页）；`:158-192`（`directiveMatchesTags`）；`internal/directives/query.go:13-33`（`queryParam` 取末次出现、空值仍校验）、`:35-60`（`active_only` 的布尔词表，大小写不敏感且**不 trim**）、`:62-130`（`limit`/`offset` 的整数文法与 `ge`/`le` 信封）；`:147-156`（排序：priority 降序、created_at 降序、id 降序，与参考的 `ORDER BY` 同） | 修 `internal/directives/handlers.go:86-113`：**查询参数校验整体移到 `requireBank` 之前**——参考里它们是 `Query(...)`，框架在进 handler 体前解析，所以未知 bank 上的坏参数返回 422 而不是 404（修复前返回 404） | 实测：空/1/4 条指令库上 `limit=1/1000/1001/0/abc`、`offset=0/-1/999`、`active_only=true/false/yes/1/bogus`、`tags_match=any/all/exact/bogus`、`tags=x`、`tags=a&tags=b`、`limit=2&offset=1`、`directives/` 全部 `SAME`；未知 bank 上 `?limit=0`、`?tags_match=bogus`、`?active_only=bogus` 三个已从 404 改为与参考同为 422（`greater_than_equal`/`literal_error`/`bool_parsing`），未知 bank 无参数仍是 404。排序实测两侧同为 `[('p10b',10),('p10',10),('tagged',0),('p0',0)]`、`active_only=false` 同为 `['inactive','p10b','p10','tagged','p0']`。测试 `TestListValidatesSelectorsBeforeTheBankLookup`（修复前 FAIL） |
| GET .../directives/{directive_id} | ❌已修 | `api/http.py:7675-7707`（`directive is None` → 404 `Directive '<id>' not found`）；`engine/memory_engine.py:20823-20862`（`WHERE bank_id = $1 AND id = $2`，**直接把路径字符串原样当绑定参数**，不做 UUID 转换）；参考 schema 的 `directives.id` 是 `uuid` 列 —— 于是非 UUID 段在**驱动绑定参数时**就失败，不是 SQL 执行时，驱动吐出的是自己的参数消息，参考的兜底 `except Exception` → 500，`detail=str(e)` 即该驱动文本 | `internal/directives/directive_uuid.go:1-113`（新文件：`uuidMember` 与 `directiveUUIDReport` 完整复刻驱动的校验顺序与措辞）；`internal/directives/handlers.go:195-206` | 新增 `internal/directives/directive_uuid.go`：修复前本服务把 id 原样交给 Postgres，非 UUID 得到 `ERROR: invalid input syntax for type uuid: "not-a-uuid" (SQLSTATE 22P02)`，与参考的 `invalid input for query argument $2: 'not-a-uuid' (invalid UUID 'not-a-uuid': length must be between 32..36 characters, got 10)` **文本不同**（旧审计记为「已知残留」，实测**至今仍可匹配、已修**）。复刻的三条规则：①先判长度（<32 或 >36 报 `length must be between 32..36 characters, got N`）②范围内再判字符（首个非十六进制字符报 `unexpected character 'c'`）③**跳过连字符**后按十六进制位数判 `decodes to less/more than 16 bytes`——所以 `01234567-89abcdef0123456789abcdef`（连字符不在规范位置）是合法 UUID；超长值外层副本保留前 39 字符、**丢掉收尾引号**接 `...`，内层副本完整 | 测试 `TestDirectiveUUIDReportMatchesDriverMessage`（12 例逐字节，含长度优先于字符、连字符任意位置、39/38 字符截断边界）、`TestDirectiveUUIDReportAcceptsUnusualHyphenation`、`TestDirectiveRoutesReportDriverFailureForNonUUIDID`（均已证伪）。实测 18 个非 UUID 段（含 `-`、37/38/39/40/64 字符、全连字符）与参考**逐字节 SAME**；合法但无此行的 UUID（全零）两侧同为 404 同体 |
| POST .../directives | ❌已修 | `api/http.py:7709-7745`（**没有 bank 存在性检查**——直接 `create_directive`，`ValueError` → 400，兜底 `except Exception` → `_internal_error(e, ...)` → 500 `detail=str(e)`）；`engine/memory_engine.py:20865-20925`（直接 INSERT，由 `fk_directives_bank_id` 外键拒绝未知 bank）；`api/http.py:3171-3181`（`CreateDirectiveRequest`：`name`/`content` 必填、`priority=0`、`is_active=True`、`tags` 默认空表） | `internal/directives/handlers.go:208-266`（body 校验 → `PutDirective` → 500/错误信封）；`internal/directives/directive_uuid.go:115-160`（新：`foreignKeyReport`） | 新增 `foreignKeyReport` 并接在 `PutDirective` 的错误分支（`handlers.go:249-259`）：修复前未知 bank 返回 `ERROR: insert or update on table "directives" violates foreign key constraint "fk_directives_bank_id" (SQLSTATE 23503)`，参考是 `str(e)` 形态——**去掉了 `ERROR: ` 前缀与 `(SQLSTATE ...)` 后缀**，并在下面补一行 `DETAIL:  Key (bank_id)=(<id>) is not present in table "banks".`（`DETAIL:` 后两个空格）。约束名从驱动文本里读回、被拒的 key 由调用方提供（驱动文本里没有），避免硬编码猜测 | 测试 `TestCreateInUnknownBankReportsForeignKeyRejection`（修复前 FAIL，逐字节断言整段 detail）。实测三个不同未知 bank（含带连字符）与参考**逐字节 SAME**；已知 bank 建指令仍 200。body 校验实测 12 种（缺两字段/缺一字段/null/int/priority 非数字/tags 非表/tags 含数字/未知字段/坏 JSON/数组/空 body）全部 `SAME` |
| PATCH .../directives/{directive_id} | ❌已修 | `api/http.py:7747-7781`（`directive is None` → 404；`ValueError` → 400）；`engine/memory_engine.py:20927-21017`（`if name is not None` / `content` / `priority` / `is_active` / `tags` **逐个非 None 才拼进 SET**，因此显式 `null` 等于「不改」，只有缺省与 null 都不动；`updated_at = now()` 恒更新；最后绑定 `bank_id` 与 `id`）；`api/http.py:3184-3191`（`UpdateDirectiveRequest` 五个成员全 `| None = None`） | `internal/directives/handlers.go:267-291`（`UpdateRequest` 五成员皆 `*T`，nil 即不动，透传 `DirectivePatch`）；`internal/directives/directive_uuid.go:41-63`（`updateDirectiveArgNumber`） | 修 `handlers.go:276-291`：非 UUID 段改用 `updateDirectiveArgNumber` 计算参数序号后复用同一个驱动消息复刻。参考的 UPDATE 按 `name, content, priority, is_active, tags` 顺序追加绑定参数，故 id 的序号 = 2 + 体里**非 null** 的成员数：`{}` 是 `$2`、`{"priority":5}` 是 `$3`、五个全给是 `$7` | 测试 `TestUpdateDirectiveArgNumberFollowsSuppliedMembers`（6 例）、`TestDirectiveRoutesReportDriverFailureForNonUUIDID`（PATCH 的 `$2`/`$3`/`$7` 三档，已证伪）。实测 PATCH 非 UUID 段在 8 种 body（含 `{}`、`{"name":null}`、五个全给）下与参考**逐字节 SAME**。合并语义实测逐一对比：`{"priority":99}`、`{"name":"next"}`、`{"tags":[]}`、`{"is_active":false}`、`{"content":null}`、`{}` 六次 PATCH 后两侧非 id/时间戳字段完全一致（显式 null 整体不动、未提及成员保留） |
| DELETE .../directives/{directive_id} | ❌已修 | `api/http.py:7783-7810`（`deleted` 为假 → 404 `Directive '<id>' not found`，否则 `{"status":"deleted"}`）；`engine/memory_engine.py:21019-21055`（`DELETE ... WHERE bank_id = $1 AND id = $2`，`result == "DELETE 1"`） | `internal/directives/handlers.go:293-303`（改用 `uuidMember`）；`internal/directives/directive_uuid.go:41-63` | 同 GET：非 UUID 段改由 `uuidMember` 报驱动消息（`$2`） | 测试 `TestDirectiveRoutesReportDriverFailureForNonUUIDID`。实测非 UUID 段（含 40 字符截断）与参考 `SAME`；全零 UUID 两侧同为 404 同体；删除成功两侧同为 `{"status":"deleted"}` |

## 组 K 读表面：documents / chunks / tags

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET /v1/default/banks/{bank_id}/documents | ❌已修 | `api/http.py:7801-7868`（`q` 大小写不敏感子串过滤 id；`tags`；`tags_match: str` **不是** Literal，所以非法值不报错；`time_field: DocumentTimeField|None`、`limit: Query(100, ge=0)`、`offset: Query(0, ge=0)`——都是 `Query(...)`，框架在进 handler 体之前解析；`start_date`/`end_date` 是裸 `str`，在 handler 体内 `_parse_iso_datetime` 解析后传引擎）；`engine/memories/pg/documents.py:634-651`（`ORDER BY {window.order_by or "updated_at DESC, created_at DESC, id"}`——**默认三键、id 升序**）；`engine/time_filter.py:139`（给了 `time_field` 时改为 `f"{column} DESC, id"`，**丢掉 created_at 次级键**）；`:605-609`（COUNT 与 SELECT 共用同一 WHERE）；`:655-682`（item 字段 id/bank_id/content_hash/created_at/updated_at/text_length/memory_unit_count/retain_params/document_metadata/tags，`document_metadata` 取自 `retain_params.metadata`）；`engine/memory_engine.py:13329-13339`（`_require_bank_exists` 在参数校验之后） | `internal/readsurface/documents.go:112-186`（`ListDocuments`）；`internal/readsurface/params.go:45-66`（`intParam`）、`:90-107`（`literalParam`）、**新增 `:192-260`（`queryErrors` 收集器）**；`internal/storage/postgres_read_surface.go:146`（`updated_at DESC, created_at DESC, id ASC`）、`:150-165`（给了时间轴则 `column DESC, id ASC`，并排除该轴为 NULL 的行——与 `engine/time_filter.py` 同） | 修 `documents.go:112-140` 两处：①**参数校验前移到 `requireBank` 之前**（`Query` 参数在参考里由框架解析，坏参数压过 404）；②**改为一次收集全部参数错误再统一返回**——参考把所有 `Query` 参数的失败列在**同一个 422** 里，原实现遇到第一个错误就返回，多参数同时出错时 body 少一项。声明顺序为 `time_field` → `limit` → `offset`，`start_date`/`end_date` 仍在 handler 内解析故排在所有 `Query` 之后、banks 之前 | 空白 bank 与真实数据 bank 实测：`limit=0/1/-1/abc`、`offset=0/-1`、`q=abc`/`q=`、`tags=a`、`tags_match=bogus`、`time_field=created_at/updated_at/bogus`、`start_date=bogus`、`end_date=bogus` 全部 `SAME`；未知 bank 上 `?limit=-1`、`?limit=abc`、`?offset=-1`、`?time_field=bogus`、`?start_date=bogus`、`?end_date=bogus` 六项已从 404 改为与参考相同（前五 422、日期两项 400）；组合 `limit=-1&time_field=bogus`、`limit=abc&offset=-1` 逐字节 `SAME`（含两条错误项及其顺序）。`tags_match=bogus` 确认两侧都 200（该参数是非 Literal 的 `str`）。排序：两侧各自数据上均满足 `updated_at DESC` 且并列时 id 升序；`time_field` 分支同样单调。测试 `TestListDocumentsValidatesBeforeTheBankLookup`、`TestListDocumentsDateParamsFollowQueryParamsButPrecedeTheBank`、`TestListDocumentsReportsEveryBadParameterInOneEnvelope`（均已证伪） |
| GET .../documents/{document_id:path}/chunks | ❌已修 | `api/http.py:7871-7925`（`limit: Query(100, ge=1, le=1000)`、`offset: Query(0, ge=0)`；`result is None` → 404 `Document not found`；逐块附 `attachments`）；`engine/memories/pg/documents.py:707-760`（`{items,total,limit,offset}`，`ORDER BY chunk_index ASC`）；`:686-704`（chunk 行含 chunk_id/document_id/bank_id/chunk_index/chunk_text/created_at） | `internal/readsurface/documents.go:395-445`（`ListDocumentChunks`）；`internal/storage/postgres_read_surface.go:216`（`ORDER BY document_id ASC, chunk_index ASC`，因已按单文档过滤故与参考等价） | 修 `documents.go:395-418`：**分页参数校验前移到文档查询之前**。修复前 `?limit=0`、`?limit=1001`、`?limit=abc`、`?offset=-1` 在文档不存在时都返回 404 `Document not found`，参考返回 422 | 实测 chunk 列表 envelope 与 item 字段集两侧完全一致（`attachments`/`bank_id`/`chunk_id`/`chunk_index`/`chunk_text`/`created_at`/`document_id`）；`limit=0/1001/abc`、`offset=-1` 在已知 bank 与未知 bank 上均 `SAME`（422），无参数时未知文档仍 404。测试 `TestListChunksValidatesPageParamsBeforeTheDocument`（已证伪） |
| GET /v1/default/banks/{bank_id}/tags | ❌已修 | `api/http.py:8010-8071`（`q` 通配（`*`）、`source: Literal["memories","mental_models"]=Query("memories")`、`limit: Query(100, ge=0)`、`offset: Query(0, ge=0)`——均 `Query(...)`，先于 handler；`source=="mental_models"` 走 `list_mental_model_tags`）；`engine/memory_engine.py:16066-16110`（`_list_tags_from_table`：`pattern.replace("*","%")` 后 `tag ILIKE $2`，即 **`*`→`%`、`%`/`_` 也按 ILIKE 语义**，大小写不敏感）；`engine/memories/pg/reads.py:353`（`ORDER BY count DESC, {tag_col} ASC`）；`engine/db/ops_postgresql.py:1264-1271`（`unnest(tags)` + `tags != '{}'`） | `internal/readsurface/handlers.go:111-166`（`ListTags`）；`:167-193`（`mentalModelTagCounts`，同样按 count 降序、tag 升序）；`:195-212`（`compileTagPattern`：`*`/`%`→`.*`、`_`→`.`、锚定且大小写不敏感）；`internal/readsurface/params.go:192-260`（`queryErrors`） | 修 `handlers.go:111-135`：**`source`/`limit`/`offset` 校验前移到 `requireBank` 之前**，且**收集全部错误一次性返回**，声明顺序 `source` → `limit` → `offset`。修复前未知 bank 的 `?limit=-1`、`?source=bogus` 返回 404 | 实测：`limit=0/-1/abc`、`offset=-1`、`source=mental_models/bogus`、`q=*`/`source:*`/`SOURCE:*`/`_ource:git`/`source:gi%`/`knowledge:*`/`no-such-tag:*` 全部 `SAME`；未知 bank 上 `?limit=-1`、`?source=bogus`、组合 `source=bogus&limit=-1`（两种书写顺序）逐字节 `SAME`。排序在三个有数据 bank 上两侧均满足 `count DESC` + 并列 `tag ASC`。测试 `TestListTagsValidatesBeforeTheBankLookup`（已证伪，含「只报首条错误」这一变异） |
| GET /v1/default/chunks/{chunk_id:path} | ✅一致 | `api/http.py:8073-8110`（非 bank 作用域；`chunk` 为假 → 404 `Chunk not found`；用 chunk 自带的 `bank_id` 校附件）；`engine/memory_engine.py:13485-13560`；参考 schema 的 `chunks.chunk_id` 是 **`text`**（形如 `<bank>_<doc>_<index>`），不是 `uuid` —— 因此**不适用**组 J 那条「非 UUID → 驱动 500」规则 | `internal/readsurface/documents.go:484-512`（`GetChunk`） | 无 | 实测 9 种 chunk_id（`nope`、`0`、`a_b_0`、`...`、`//`、`%2F`、`not-a-uuid`、全零 UUID、40 位长串）全部 `SAME`，无 500/无驱动文本；文档路由同理（`documents.id` 也是 `text`）：`a/b/c`、`a/./b`、`a/../b`、`nope/x` 四种含斜杠路径与参考一致。**已确认这两条路由不共享组 J 的 uuid 缺陷** |
| 响应字段集与渲染 | ✅一致 | `engine/memories/pg/documents.py:655-682`（文档 item 十字段；`document_metadata` = `retain_params.metadata`；`text_length = LENGTH(original_text)`；无 `original_text` 字段——列表刻意不返回正文）；`api/http.py:7957-7975`（单文档详情返回正文并 `pop("attachment_filenames")`） | `internal/readsurface/models.go`（`DocumentListItem`/`DocumentListResponse`/`ChunkItem`/`TagListResponse`）；`internal/readsurface/documents.go:175-222`（`GetDocument`） | 无 | 实测同一 bank 两侧信封键同集（`items/total/limit/offset`）、文档 item 键同集（10 键）且逐键类型一致（`text_length`/`memory_unit_count` 为 int，`retain_params`/`document_metadata` 为 dict，`tags` 为 list）；chunk envelope 与 chunk item 键集一致。**注：本组未涉及 markdown 渲染**——`page_markdown.py` 只服务 knowledge-base 页面读写（组 I 已验），documents/chunks 返回的是 JSON 与原始 `chunk_text` |

## 组 L 图与实体

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET .../graph | ❌已修 | `api/http.py:5783-5810`（`limit: Query(default=1000, ge=0)` 是**唯一** `Query(...)`；`type`/`q`/`tags_match`/`document_id`/`chunk_id` 均为裸 `str`，`tags_match: str = "all_strict"` 故非法值不报错；**不声明 `tag_groups`**）；`engine/memory_engine.py` `get_graph_data` **不调用** `_resolve_fuzzy_tag_groups`（该函数只在 recall/reflect/list_entities/get_entity_graph/get_entity 处调用：`:8985,:15318,:15866,:15915,:16527`）；边样式常量见 `engine/`：`entity`→solid `#ffd700`、`semantic`→solid `#ff69b4`、`causal/caused_by`→solid `#999999`、`temporal`→dashed `#00bcd4`；节点 `#42a5f5`/`#90caf9` | `internal/readsurface/handlers.go:226-270`（`Graph`）；新增 `internal/readsurface/entity_params.go:44-100`（`readEntityParams`/`validateEntityQuery`） | 修 `handlers.go:226-235`：**`limit` 校验前移到 `requireBank` 之前**（修复前未知 bank 的 `?limit=-1`、`?limit=abc` 返 404）。**并修正一处越界校验**：`entityParamSpec.NoTagGroups` 使 graph 不再解析 `tag_groups` —— 参考的 graph 路由不声明该参数，框架对未声明参数一律忽略，而原实现会解析并返 400（`?tag_groups=bogus` 参考 200/本服务 400） | 未知 bank 上 `?limit=-1`(422)、`?limit=abc`(422)、`?limit=0`、`?type=bogus`、`?tags_match=bogus`、`?tag_groups=bogus`、`?q=abc` 全部 `SAME`；`?tag_groups=bogus`、`?tag_groups=[]`、`?tags=a&tag_groups=[]` 现均为 200（与参考一致，被忽略）。节点/边字段集两侧同集（node `color/context/date/entities/id/label/text`；edge `color/entityName/id/lineStyle/linkType/source/target/weight`）；信封 `edges/limit/nodes/table_rows/total_units` 同集；`linkType→lineStyle/color` 四元组、节点配色阈值两侧完全一致。测试 `TestGraphValidatesLimitBeforeTheBank`、`TestGraphIgnoresUndeclaredTagGroups`（均已证伪） |
| GET .../entities | ❌已修 | `api/http.py:6749-6795`（`limit: Query(100, ge=0)`、`offset: Query(0, ge=0)`、`tags`、`tags_match: TagsMatch=Query("any")`（**Literal**，非法值 422）、`tag_groups`）；`_parse_tag_groups_query`（`api/http.py:264-281`）：**`raw is None` 才返回 None**，故 `?tag_groups=` 与 `?tag_groups=null` 都进适配器并 400；`tags` 与 `tag_groups` 并用 400；`engine/memory_engine.py:15866` 调用 `_resolve_fuzzy_tag_groups`（`:15935-15972`，422 上限；词汇表取自 `list_tags`，`MAX_VOCABULARY=5000`）；排序 `engine/memories/pg/curation.py:545` `ORDER BY mention_count DESC, last_seen DESC, id ASC` | `internal/readsurface/handlers.go:442-470`（`ListEntities`）；`internal/readsurface/entity_params.go:44-100`（参数管线）、`:110-140`（`resolveFuzzyTagGroups`）、`:142-166`（`resolveFuzzyTagGroupsFor`）；`internal/readsurface/params.go:104-140`（`tagGroupsQuery` 缺省/空串区分 + 非数组形状检查）、`:196-215`（`queryErrors.unboundedInt`） | 三处修复：①**参数校验前移到 `requireBank` 之前**，且 limit/offset/tags_match 一次收集、按声明顺序（`limit`→`offset`→`tags_match`）返回同一 422；②**`tag_groups` 缺省/空/`null` 三分**——原实现把空串当缺省（参考 400），且 Go 的 `null` 反序列化进切片是无操作（参考 400「Input should be a valid array」），现按参数是否存在判断并增加非数组形状检查；③**`resolve:"fuzzy"` 补上解析**——原实现把模糊叶子按字面比较，导致 `topic:typecsript` 匹配 0 条，参考解析后匹配 3 条 | 未知 bank 上 `?limit=-1/?limit=abc/?offset=-1/?tags_match=bogus` 均 422，`?limit=-1&tags_match=bogus` 单信封两项且 limit 在前，`?tag_groups=bogus`(400)、`?tags=a&tag_groups=[]`(400) 全部 `SAME`。**状态码矩阵**（entities/graph/entities/{id} × bogus/空/null/`{}`/`[]`）15 组全部与参考一致。**fuzzy 实测**：`[{"tags":["topic:typecsript"],"resolve":"fuzzy"}]` → 参考 3 / 本服务 3（修复前 0）；`exact` 3/3；不可匹配 token 0/0；嵌套 `or` 内 fuzzy 3/3。排序在造好的并列数据上两侧均满足 `mention_count DESC` → `last_seen DESC` → `id ASC`。测试 `TestListEntitiesValidatesBeforeTheBank`、`TestEntitiesTagGroupsPrecedesTheBank`、`TestTagGroupsDistinguishesAbsentFromEmptyAndNull`、`TestEntityListResolvesFuzzyTagGroups`（均已证伪） |
| GET .../entities/graph | ❌已修 | `api/http.py:6810-6850`（`limit: Query(1000, ge=0)`、`min_count: Query(1)` **无上下界**、`tags`、`tags_match: TagsMatch`、`tag_groups`）；`engine/memory_engine.py:15915` 同样解析 fuzzy | `internal/readsurface/handlers.go:543-600`（`EntityGraph`）；`internal/readsurface/entity_params.go:44-100` | 参数校验前移 + 收集全部错误（`limit`→`min_count`→`tags_match`，按签名顺序）；`min_count` 用 `unboundedInt` 以保留参考的无界语义（`min_count=-1` 合法）。修复前未知 bank 的 `?limit=-1`、`?min_count=abc` 返 404 | 未知 bank 上 `?limit=-1`(422)、`?min_count=abc`(422)、`?min_count=-1`(200)、`?min_count=0`(200)、`?tags_match=bogus`(422)、`?limit=-1&min_count=abc`(422，单信封两项)、`?tag_groups=bogus`(400) 全部 `SAME`。node `color/id/label/mentionCount` 与 edge `color/id/lastCooccurred/lineStyle/linkType/source/target/weight` 两侧同集，信封 `edges/limit/nodes/total_edges/total_entities` 同集 |
| GET .../entities/{entity_id} | ❌已修 | `api/http.py:6852-6900`（`tags`、`tags_match`、`tag_groups` 均 `Query(...)`；`entity is None` → 404 `Entity {id} not found`；`ValueError` → 400 `str(e)`）；`engine/memory_engine.py:16527` 解析 fuzzy；`entities.id` 列类型为 **`uuid`**，故非 UUID 走参考自己的 400 —— 见下"id 类型"行 | `internal/readsurface/handlers.go:495-535`（`GetEntity`）；`internal/readsurface/entity_params.go:44-100` | 参数校验前移到**实体 id 的 UUID 解析之前**（参考里 `Query` 先于 handler 体，`tag_groups` 也先于 id 的 400）；`tag_groups` 解析同样前移 | `?tags_match=bogus` 配非 UUID id → 422（压过 id 的 400）；`?tag_groups=bogus` 配非 UUID id → 400 `Invalid tag_groups`（同样压过）；无参数时非 UUID id 仍 400 `'not-a-uuid' is not a valid UUID`；未知 bank **不做预检**（与参考一致，返实体自身的 not-found）。详情信封 `canonical_name/first_seen/id/last_seen/mention_count/metadata/observations` 同集。测试 `TestGetEntityValidatesQueryParamsBeforeTheID`（已证伪） |
| POST .../entities/{entity_id}/regenerate | ✅一致 | `api/http.py:6902-6925`（**无条件** `raise HTTPException(410, "This endpoint is deprecated. Entity observations are no longer supported.")`，既不查 bank 也不查实体） | `internal/readsurface/handlers.go:537-542`（`RegenerateEntity`） | 无 | 未知 bank / 真实 bank / 非 UUID id 三种输入实测均 `SAME`（410 与完全相同的 detail），与参考"先直接拒绝"一致 |
| id 类型：`uuid` 列 vs `text` 列 | ✅一致 | 参考 `entities.id` 是 `uuid`，但 `get_entity` 先把路径串交给自身解析：`ValueError` → **400**（`api/http.py:6890`），不是驱动层 500。对比组 J：directive id 直接绑到 `uuid` 列 → 驱动 500；组 K：`documents.id`/`chunks.chunk_id` 是 `text` → 无此问题 | `internal/readsurface/handlers.go:508-512`（`validUUIDShape` 前置判定 + 400） | 无（`validUUIDShape` 已存在且行为正确） | 非 UUID 四种形态（`nope`/`not-a-uuid`/`123`/`abc-def-ghi`）两侧均 400 且 detail 逐字节相同。**本组不存在组 J 的驱动 500 形态** |
| 字段集与呈现常量 | ✅一致 | `engine/memories/pg/graph.py` 的边类型与配色；`engine/memories/pg/curation.py:545,658` 实体排序 | `internal/readsurface/models.go`；`handlers.go:573-600`（实体图配色 `#42a5f5`/`#90caf9` 阈值 1、边 `#ffd700`） | 无 | 见各路由行 |
| ⚠️ `tag_groups` 错误正文措辞（遗留） | ⚠️存疑 | `api/http.py:281` `detail=f"Invalid tag_groups: {e.errors(include_url=False)}"` —— 渲染的是 Pydantic 错误字典列表的 **Python repr**（如 `[{'type': 'json_invalid', 'loc': (), 'msg': 'Invalid JSON: expected value at line 1 column 1', ...}]`） | `internal/readsurface/params.go:120-134`（当前用 Go 的 `json` 错误文本 / 自拟措辞） | **未修**：组 I 已在 `internal/reporting/knowledge_tag_groups.go` 完整实现了同一套 TagGroup 校验，但其渲染是 `str(ValidationError)`（knowledge 路由的 422 形态），本组需要的是 `errors(include_url=False)` 的 **repr** 形态；复用需从 `internal/reporting` 导出一个渲染函数，超出本组写范围 | 状态码已 100% 对齐（15/15 组矩阵一致），仅 `detail` 内文本不同。**待裁决**：是否允许我在 `internal/reporting/` 新增一个导出渲染函数（只加不改，组 I 既有行为不受影响），或接受此处措辞差异 |
| ⚠️ 参考缺陷：`tag_groups=[{"and":[]}]` | ⚠️存疑 | 参考对空 `and` 子滤器生成坏 SQL，返 **500** `{"detail":"syntax error at or near \")\""}`（`api/http.py` 未拦截空子列表） | 本服务返 200（空 `and` 视为无约束） | **有意不复制**：这是参考的 SQL 构造缺陷，照抄会把一个 500 写进本服务 | 实测参考 500 / 本服务 200。建议记为参考缺陷而非本服务缺陷 |

## 组 M operations 队列

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET .../operations | ✅一致 | `api/http.py:8216-8253`（`status: str|None`、`type: str|None` **均无 Literal 校验**；`limit: Query(20, ge=1, le=100)`、`offset: Query(0, ge=0)`、`exclude_parents: bool = Query(False)`）；`engine/memory_engine.py:21095-21180`：`WHERE` 依次追加 status / operation_type / `NOT (result_metadata::jsonb @> '{"is_parent":true}'::jsonb)`，`ORDER BY created_at DESC`（**无 tiebreaker**）；信封 `bank_id/total/limit/offset/operations` | `internal/memoryops/operations_list.go`；`internal/storage/postgres_writes.go:262-290`、`internal/storage/memory_operations.go:275` | 无 | 未知 bank 与本组自建 bank 上 `""`、`?limit=1/100/101/0/-1/abc`、`?offset=0/99/-1`、`?status=failed/bogus`、`?type=retain/bogus`、`?exclude_parents=true/bogus`、`?limit=100&offset=5` 全部 `SAME`。**逐字段比对**：在两侧写入完全相同的时间戳与状态后，`""`、`?limit=1`、`?status=completed/failed/cancelled`、`?type=retain/export_bank`、`?exclude_parents=true`、`?offset=1/9` 的去时间戳 JSON **逐字节相同**。`status=bogus`/`type=bogus` 参考不校验，返回空页（本服务同） |
| GET .../operations/{operation_id} | ❌已修 | `api/http.py:8256-8286`：先 `uuid.UUID(operation_id)`，失败 → **400** `Invalid operation_id format: {operation_id}`；`include_payload: bool = Query(False)`；`engine/memory_engine.py:21220-21237`：`task_payload` 列**原样 `parse_json`**，不增删任何键；缺失行返回 200 + `status: "not_found"` 的十六键体 | `internal/memoryops/operations.go:96-156`（`GetOperation`）、`:345-437`（`ToOperationStatusDTO`）、`:439-457`（`fullTaskPayload`） | 修 `fullTaskPayload`：原实现**凭空合成** `type`/`operation_id`/`bank_id` 三个键（`make(map, len+3)` 后写入，再被存储值覆盖）。对真实行不可见（提交方本就写入这三键），但对**任何 payload 缺这三键的行**会多出参考从不发送的成员——导入的或直接播种的行正是如此，实测被抓到。现改为**逐键原样回显**。保留既有的 `retain` + 空对象 → `null` 语义 | `?include_payload=` 空/`true`/`1`/`false`/`0`/`bogus`：`bogus` → 422（`bool_parsing`，loc `["query","include_payload"]`），其余两侧一致。**修复前**：播种 payload `{"items": []}` 时参考回 `{"items": []}`、本服务回 `{"bank_id":…,"items":[],"operation_id":…,"type":"retain"}`；**修复后** 三个终态行 × 三种 `include_payload` 取值 **全部逐字节相同**。非 UUID 四种形态两侧均 400 且 detail 相同。测试 `TestOperationPayloadIsEchoedVerbatim`、`TestOperationPayloadPassesThroughStoredEnvelopeKeys`（前者已证伪） |
| DELETE .../operations/{operation_id}（cancel） | ✅一致 | `api/http.py:8288-8337`；`engine/memory_engine.py:21353-21420`：缺失行 → `ValueError` → **404** `Operation {id} not found for bank {bank_id}`；状态 ∉ {pending, processing} → **409** `Operation {id} cannot be cancelled: status is '{status}', only 'pending' and 'processing' operations can be cancelled`；成功体 `{success, message, operation_id}` | `internal/memoryops/operations.go:190-217`（`CancelOperation`）、`:295-310`（`mapCancelError`） | 无 | 5 状态 × cancel 全部 200/409 与参考一致；**成功体逐字节相同**；409 文案逐字节相同（`cancel-completed`） |
| POST .../operations/{operation_id}/retry | ✅一致 | `api/http.py:8338-8369`；`engine/memory_engine.py:21440-21572`：条件 `UPDATE … status IN ('failed','cancelled') AND NOT (operation_type='batch_retain' AND task_payload IS NULL)`；未命中时若为 **batch_retain 父行**则复活其 failed/cancelled 子行、并在「存在非 completed 子行」时复活父行，否则 **409** `Operation {id} is a batch_retain parent with no incomplete sub-batches to retry (its children have all completed, or it has none). Resubmit the source documents to re-ingest them, then delete this record via DELETE /operations/{id}/delete.`；非父行 → 409 `cannot be retried: status is '{status}', expected 'failed' or 'cancelled'` | `internal/memoryops/operations.go:218-250`（`RetryOperation`）、`:311-330`（`mapRetryError`） | 无 | 5 状态 × retry：pending/processing/completed → 409、failed/cancelled → 200，与参考**全部一致**；成功体逐字节相同；`retry-pending`/`retry-completed` 的 409 正文逐字节相同。**batch_retain 父行三种分支单独实测**：全部子行 completed → 409（含「no incomplete sub-batches」长文案，逐字节相同）、有一子失败 → 200、子行仍 pending → 200，三种全 `OK` |
| DELETE .../operations/{operation_id}/delete | ✅一致 | `api/http.py:8371-8410`；终态 {failed, cancelled, completed} 才可删，其余 409；成功体 `{success, message, operation_id}` | `internal/memoryops/operations.go:252-276`（`DeleteOperation`） | 无 | 5 状态 × delete：pending/processing → 409、completed/failed/cancelled → 200；成功体逐字节相同；`delete-pending` 的 409 正文逐字节相同 |
| 参数校验先于 bank 查找 | ✅一致 | 五个路由的 `Query(...)` 均由框架在 handler 体之前解析，故校验压过 bank 404 | `internal/memoryops/operations_list.go`、`operations.go:40-52` | 无（**本组已是正确实现**，未见组 I/J/K/L 的倒置） | 未知 bank 上 `?limit=-1/0/101/abc`、`?offset=-1`、`?exclude_parents=bogus`、`?include_payload=bogus` 均 422 而非 404，两侧 `SAME` |
| id 类型：`uuid` 列是否触达驱动 | ✅一致 | `async_operations.operation_id` **是 `uuid` 列**（实测两侧 `information_schema`），但参考四个 id 路由都先 `uuid.UUID(operation_id)` 并自行转 400（`api/http.py:8271,8319,8351,8383`） | `internal/memoryops/operations.go:40-52`（`rejectMalformedOperationID`） | 无 | 非 UUID（`not-a-uuid`/`nope`）在 GET/DELETE cancel/retry/delete 四处均 400 `Invalid operation_id format: <x>`，与参考逐字节相同——**不出现组 J 的驱动 500**。四种组合（4 路由 × 2 bank）共 8 组 `SAME` |
| 字段集与状态词表 | ✅一致 | `OperationResponse`/`OperationStatusResponse` 模型 | `internal/memoryops/operation_models.go` | 无 | 列表项 17 键两侧同集（`created_at/details/document_id/error_message/filename/id/items_count/mental_model_id/next_retry_at/operation_id/operation_type/progress/retry_count/status/task_type/updated_at`）；详情 17 键同集（含 `child_operations/completed_at/result_metadata/task_payload`）；信封 5 键同集。状态词表两侧实测均为 `{pending, processing, completed, failed, cancelled}`，`task_type` 两侧均见 `{consolidation, batch_retain, retain, refresh_mental_model, export_bank, graph_maintenance}` |
| ⚠️ 列表排序 tiebreaker（记录，不修） | ⚠️记录 | `engine/memory_engine.py` 列表 SQL 为 `ORDER BY created_at DESC`，**无并列后缀**，故同 `created_at` 的行顺序由 Postgres 自定 | `internal/storage/postgres_writes.go:286` `ORDER BY created_at DESC, operation_id ASC` | 无（有意保留） | 参考在同 `created_at` 下顺序未定义，本服务的 `operation_id ASC` 是其中一个合法实例；加上去使结果确定、可复现。强行去掉只会让分页在并列处出现重复/遗漏。未改，记录在此 |
| ⚠️ 证据说明：worker 差异 | ⚠️说明 | 参考侧 worker 在跑，本服务按 `scripts/parity_dev_service.sh` 固定 `WENYUAN_API_WORKER_ENABLED=false` | — | — | 本组一律**用终态行**（completed/failed/cancelled）做形状与过滤比对，避免 worker 抢走 pending 行造成伪差异；唯一例外见下。播种的 pending/processing 行在参考侧确实被其 worker 消化（实测由 pending 变 completed），**属夹具差异非缺陷**。为获得「同数据同形状」的严格比对，两侧数据库分别直连播种：本服务 `127.0.0.1:15432/wenyuan`，参考 `127.0.0.1:5432/hindsight` 的 **`user_devin` 租户 schema**（其 `public` schema 只有 2 个 bank，API 实际读写租户 schema——此点花了不少时间才定位，建议后续各组直接用 `user_devin`）。 |

## 组 N transfer / import / export / clone / 文件下载

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| POST .../import（bank 模板） | ❌已修 | `api/http.py:8716-8790`：`raw_body = await request.json()` 在**内层 try 之外**，故 `JSONDecodeError` 逃到外层 → **400 + `detail=str(e)`**（CPython 原文）；`ValidationError` → 400 `Template schema validation failed: {loc}: {msg}; …` | `internal/transfer/scope.go:236-250`（`decodeJSONBody`）、`internal/transfer/template_handlers.go:52-59`；新增 `internal/transfer/python_json_error.go`（316 行） | 修两处：①空 body 原返回自定义串 `request body is required`，参考实为 CPython `Expecting value: line 1 column 1 (char 0)`；②非 JSON body 原返回 Go 的 `invalid character 'o' in literal null (expecting 'u')` / `unexpected end of JSON input`，参考返回 CPython 的 `json.JSONDecodeError` 文本。新增渲染器按 CPython 语义重建：报错位置是**该产生式起始处**而非 Go 的失败字节（`tru` → char 0），`line`/`column`/`char` 三者自洽，多行按换行计 | **实测 14 种畸形 body 与参考逐字节相同**（`''`/`notjson`/`{`/`{"a":}`/`{"a" 1}`/`[1,]`/`"unterminated`/`{"a":1}x`/`tru`/`{"a":1,}`/`  [`/`[1`/`{"a":1 "b":2}`/`{"a":`）。单测 `TestPythonJSONErrorMatchesCPython` 钉住 **26 个 CPython 基准串**（含多行）。**证伪**：换回 Go 消息 → 26 例全失败。schema 校验路径（`{"version":1}`/`[]`/`null`）两侧本就一致 |
| POST .../transfer/export | ✅一致 | `api/http.py:9082-9105`：`include_data`/`include_bank_config`(true)/`include_history`(false)/`document_id` 均为 `Query`；开关关闭 → 404 `Bank export API is disabled. …`；三者全 false → 400 | `internal/transfer/`（路由 + `export_build.go`） | 见下方「归档 data 段」一行 | `?include_observations`/`?include_knowledge_base`/`?document_id=a&document_id=b` 两侧均 202 且信封 `{operation_id,status}` 相同；`?include_observations=bogus` 两侧 422 相同；未知 bank 两侧相同 |
| POST .../transfer/import | ✅一致 | `api/http.py:9146-9180` | `internal/transfer/import_apply.go` | 无（本行只判校验） | 带真实文件时 5 组校验实测**逐字节相同**：`mode=bogus`(400)、`document_conflict=bogus`(400)、`mode=merge&target_bank_id=x`(400，含字面 `{bank_id}`)、`include_data=bogus`(422 `bool_parsing`)、`mode=merge&include_data=true`(400)。**无文件时**上一轮标出的 3 个 DIFF 是「没带 file 直接 422」造成的伪差异，带文件即消失 |
| 归档容器与段布局 | ✅一致 | `engine/transfer/export.py`：`SELECT *` 转储 + `_DERIVED_COLUMNS` 剔除 | `internal/transferarchive/sections.go`、`internal/transfer/export_build.go` | 无 | 两侧 zip 条目列表**逐项相同**（13 项）：`banks.json`、`data/{async_operations,entity_maintenance_queue,graph_maintenance_queue,invalidated_memory_units}.json`、`directives.json`、`documents/000000.json`、`knowledge_pages.json`、`manifest.json`、`mental_model_history.json`、`mental_models.json`、`observations.json`、`webhooks.json` |
| 归档 manifest.json | ✅一致 | 同文件 | `internal/transfer/export_build.go` | 无 | 17 个键**同集**：`archive_type/attachment_count/bank_rows_json_encoding/directive_count/document_count/exported_at/fact_count/includes_history/invalidated_memory_count/knowledge_page_count/mental_model_count/observation_count/operation_count/schema_version/scope/source_bank_id/webhook_count`；`bank_rows_json_encoding` 两侧均 `"serialized"`，`scope` 两侧均 `{bank_config:true,data:true,history:false}`，计数器（除 operation_count 因导出操作本身计数不同）全部相同 |
| 归档 data/async_operations 段 | ❌已修 | `engine/transfer/export.py:431-440`：`_dump_bank_rows` 对 `async_operations` 做 **`SELECT *`，不按状态过滤**；行内 jsonb 以**序列化文本**落盘（`task_payload`/`result_metadata` 是字符串）。丢弃在途行发生在**导入侧**：`engine/transfer/importer.py:775-793` `_finished_operations` 剔除 `pending`/`processing` | `internal/transfer/export_build.go:200-268`（`buildOperations`/`operationRow`）、`internal/transfer/import_apply.go:667-735`（`applyOperations`/`jsonbRowMap`） | 修三处：①**列名错**——原导出 `error`/`payload`/`is_parent`，参考是真实列名 `error_message`/`task_payload`/`worker_id`/`claimed_at`/`operation_id`/`serialization_key`，共 15 列；②**过滤位置错**——原在导出侧丢掉非终态行，参考导出全量、导入侧才丢弃；③**jsonb 编码错**——原写成 JSON 对象，参考写字符串。现两侧均按真实列名 + 文本编码，导出全量、导入丢弃在途行 | 修复后 `data/async_operations` **键集与参考完全相同**（含 `task_payload` 为 str 类型）。导入参考侧归档到本服务：`operations_imported: 1`，计数器 16 键与参考侧导入同格式（逐键同名）。**证伪**：把导出过滤改回去 → `TestOperationsLogTravelsVerbatimAndRestoresFinishedOnly` 失败于「必须携带全部行」 |
| 归档其余各段 | ✅一致 | 同上 | 同上 | 无 | 逐段键集比对：`banks.json`/`directives.json`/`documents/000000.json`/`webhooks.json`/`knowledge_pages.json`/`mental_models.json`/`mental_model_history.json`/`observations.json`/`data/invalidated_memory_units.json`/`data/{graph,entity}_maintenance_queue.json` **键集全部相同**。document 与 fact 的键集两侧同为 7 键 / 17 键。`webhooks.json`/`knowledge_pages.json`/`mental_models.json`/`mental_model_history.json` 去掉时间戳后**内容完全相同**。**双向导入实测**：本服务归档 → 参考侧导入成功（documents 1/facts 1/observations 1/directives 1/operations 5）；参考侧归档 → 本服务导入成功（同上前 4 项 + operations 1） |
| POST .../clone | ✅一致 | `api/http.py:9252-9330` | `internal/transfer/` | 无 | 未知 bank 两侧相同；归档/导入路径已双向实测通过 |
| GET .../document-transfer | ✅一致 | `api/http.py:8895-8910`：无条件 **410**，detail 内嵌本 bank 的 export/operations 路径 | `internal/transfer/` | 无 | 稳态 bank 上两侧 410 且 **detail 逐字节相同**（含路径插值） |
| POST .../document-transfer/export | ✅一致 | `api/http.py:8961-8990` | `internal/transfer/` | 无 | 4 组参数两侧状态码与信封相同（`""`/`include_observations=true`/`include_knowledge_base=true`/`document_id=a&document_id=b` → 202 `{operation_id,status}`；`include_observations=bogus` → 422 相同） |
| POST .../document-transfer（import） | ✅一致 | `api/http.py:9025-9060` | `internal/transfer/` | 无 | 未知 bank 两侧相同 |
| GET .../attachments/{id} | ✅一致 | `api/http.py:9335-9388` | `internal/transfer/binary_handlers.go` | 无 | 缺失 id 两侧 **404 `Attachment not found`** 逐字节相同；`content-type: application/json`、`content-length: 27` 两侧一致 |
| GET /v1/default/files/download/{key} | ✅一致 | `api/http.py:9389-9449` | `internal/transfer/binary_handlers.go` | 无 | 缺失 key 两侧 **404 `File not found`** 逐字节相同；响应头 `content-type`/`content-length` 一致。**导出归档可下载**：两侧 `download_url` 均形如 `/v1/default/files/download/banks/<bank>/exports/<uuid>/transfer.zip` 且可 GET 到 |
| ⚠️ bank `disposition` 未落库（越界） | ⚠️存疑 | 参考建 bank 时 `disposition` 列固定写 `{"empathy":3,"literalism":3,"skepticism":3}`，显式传值也被忽略 | 本服务 `disposition` 列恒为 `{}`；读取时由 `internal/bankapi/banks.go:246-262` 的 `overlayDispositionMission` 用默认值补上，故 **API 表面两侧一致** | **未修**：根因在 `internal/bankapi`（建 bank 写入路径），不在本组写范围 | 后果仅限**归档**：参考 `banks.json` 的 `disposition` 为 `{"empathy":3,…}`，本服务为 `{}`；且 `config` 的 JSON 文本间隔不同（参考 `": "`、本服务 `":"`）。实测把本服务归档还原到新 bank 后，该列仍是 `{}`（API 仍显示 3/3/3，因有默认兜底）。**建议**：在 `internal/bankapi` 建 bank 时写入默认 disposition 三元组 |
| ⚠️ 操作行时间戳不保真（越界） | ⚠️存疑 | 参考 `_restore_rows` 做逐列 `INSERT ... ON CONFLICT DO NOTHING`，归档里的 `created_at`/`updated_at` 原样写入 | `internal/storage` 的 `PutOperation` **无条件**把 `CreatedAt`/`UpdatedAt` 覆盖为 `time.Now()`，接口没有保留时间戳的写入方法 | **未修**：在 `internal/storage/`，不在本组写范围 | 恢复后的操作行 id 已按参考保留（`operation_id` 走归档值），但 `created_at`/`updated_at` 变成恢复时刻。已在测试中显式注明「不钉住此项」，避免把缺陷固化成契约。**建议**：给 storage 增加一个保留时间戳的操作写入入口供导入使用 |

## 组 O webhooks

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| POST .../webhooks | ✅一致 | `api/http.py:9768-9800`（201；`_validate_webhook_destination` → 400）；`webhooks/url_guard.py`：scheme 必须 http/https，**IP 字面量**的私有/回环/链路本地地址拒绝，**DNS 名不在此处解析**（投递时才由 guarded transport 固定）；`webhooks/manager.py` 的 `WebhookHttpConfig` 默认 `method=POST, timeout_seconds=30, headers={}, params={}` | `internal/webhooks/handlers.go`、`urlguard.go`、`validate.go`、`models.go` | 无 | 11 组输入两侧状态码与响应体**逐字节相同**：`127.0.0.1`(400 私有地址)、`169.254.169.254`(400)、`localhost`(**201**，DNS 名放行——此点易误判为应拒绝)、`ftp://`(400 `scheme must be http or https, got 'ftp'`)、`notaurl`/`""`(400 `got 'empty'`)、`event_types:[]`(201)、未知事件类型(201，参考不校验)、`{}(422 missing body.url)`。成功体 9 键同集（`id/bank_id/url/secret/event_types/enabled/http_config/created_at/updated_at`），`secret` 两侧均回 `null`，`http_config` 默认值相同 |
| GET .../webhooks | ✅一致 | `api/http.py:9830-9870` | `internal/webhooks/handlers.go` | 无 | 信封 `{items,total,limit,offset}` 同集，默认 `limit=100 offset=0`；两侧同 5 条、**顺序相同**（同 `url` 序）。分页边界：`?limit=2`/`?offset=1`/`?limit=0` → 200；`?limit=-1`/`?limit=abc` → 422，两侧一致 |
| PATCH .../webhooks/{id} | ✅一致 | `api/http.py:9920-9990` | `internal/webhooks/handlers.go` | 无 | 两侧各建一条 webhook 用**各自的 id** 打补丁（id 每实例不同，不可共用）。5 种补丁 `enabled:false`/`url`/`secret`/`event_types`/`http_config.timeout_seconds` 去掉 id 与时间戳后**逐字节相同**；`{}` → 两侧 422 `No fields provided to update`；`enabled:"bogus"` → 422 `bool_parsing`；`url:127.0.0.1` → 400 同文案 |
| DELETE .../webhooks/{id} | ✅一致 | `api/http.py:9880-9918` | `internal/webhooks/handlers.go` | 无 | 两侧 `200 {"success":true,"message":null,"deleted_count":null}`；重复删除两侧均 **404 `Webhook not found`** |
| GET .../webhooks/{id}/deliveries | ✅一致 | `api/http.py:10008-10060`；`WebhookDeliveryResponse`（`api/http.py:4933-4948`）13 字段；`limit: Query(50, le=200)`（**无下界**）；`next_cursor = page[-1]["created_at"]` | `internal/webhooks/handlers.go`、`internal/webhooks/models.go:88,130` | 无 | 空时两侧 `{"items":[],"next_cursor":null}`。**有记录时**在两侧各播种同形投递行后，**13 个键完全同集**（`attempts/created_at/event_type/id/last_attempt_at/last_error/last_response_body/last_response_status/next_retry_at/status/updated_at/url/webhook_id`）；`attempts` 两侧均 = `retry_count + 1`（播种 retry_count=2 → 两侧都回 3）；`created_at` 两侧均为 `YYYY-MM-DD HH:MM:SS.ffffff+00` 空格分隔格式 |
| webhook id 类型（uuid） | ✅一致 | `_parse_webhook_id`（`api/http.py:9761-9766`）：`uuid.UUID()` 失败 → **422** `Invalid webhook ID`（注意：**不是**组 J 的驱动 500，也**不是**组 L/M 的 400） | `internal/webhooks/handlers.go` | 无 | 非 UUID（`abc`）在 DELETE 与 deliveries 两处两侧均 **422**；零 UUID 在未知 bank 上两侧均 404。**这是第四种 uuid 形态**：J=驱动 500、K=无 uuid 列、L/M=先解析转 400、**O=先解析转 422** |
| 投递签名（冻结契约） | ✅一致 | `webhooks/manager.py:52-77`：`X-Hindsight-Signature` = `sha256=` + HMAC-SHA256(secret, **raw body**)；`X-Hub-Signature-256` 与之**逐字节相同**；`X-Hindsight-Signature-V2` = `t=<unix>,v1=<hex>`，MAC 覆盖 `<timestamp>.<raw body>`；`X-Hindsight-Event` = 事件名 | `internal/app/worker.go:420-460` | 无 | **实际投递抓包验证**：用本地接收器捕获真实投递，再用独立 Python 复算——`X-Hindsight-Signature` 与 `X-Hub-Signature-256` 均与 `HMAC(secret, body)` 相符，V2 与 `HMAC(secret, t+"."+body)` 相符，且**换用空 secret 复算不匹配**（证明确实用了 secret）。四个头名逐字一致 |
| 投递事件体 | ✅一致 | `webhooks/models.py:76-82` `WebhookEvent`：`event/bank_id/operation_id/status/timestamp/data` 六字段；`model_dump_json(exclude_none=True)` → 声明序 + None 成员**不出现**（而非 null） | `internal/app/worker.go:788-815`（`webhookEventBody` + `webhookEventDataOrder`） | 无 | 抓包实测 body：`{"event":"retain.completed","bank_id":…,"operation_id":…,"status":"completed","timestamp":…,"data":{}}`，六键且顺序与参考声明序一致；`data` 成员顺序按事件类型分别维护（`consolidation.completed` 4 项 / `retain.completed` 3 项 / `memory_defense.triggered` 10 项），与 `webhooks/models.py` 各 `*EventData` 声明序一致 |
| 投递重试退避 | ✅一致（补测试） | `webhooks/manager.py:22-23`：`RETRY_DELAYS = [5, 300, 1800, 7200, 18000]`，`MAX_ATTEMPTS = len+1` = 6 次；`engine/memory_engine.py:5089-5102`：第 n 次失败后等 `RETRY_DELAYS[retry_count]`，用尽则抛出（终止） | `internal/app/worker.go:234-239,355-370` | 实现本就正确，但**缺把阶梯数值钉死的测试**——补 `TestWebhookRetryLadderMatchesTheReference` | 单测钉住 5 档数值 + `webhookRetryDelay(i)` 映射 + 「用尽即终止 / 少一档仍调度」。**证伪**：把最后一档 5h 改成 6h → `rung 4 = 6h0m0s, want 5h0m0s`。实测佐证：`retry_count=2` 的行 `next_retry_at - now()` ≈ **299~307s**（即第二档 300s，不是首档 5s） |
| 投递失败记录 | ✅一致 | `engine/memory_engine.py:5081-5103`：非 2xx 时把 `response_status`/`response_body` 写入 `result_metadata`；失败消息含状态码、url、body | `internal/app/worker.go:241-256` | 无 | 实测失败投递的 `error_message` 形如 `HTTP 500 from http://127.0.0.1:18099/fail: nope`，与参考 `HTTP {status} from {url}: {body}` 同构；body 截断上限 500 与参考一致。转移守卫：`localhost` 域名投递被拒时的错误含 `webhook url is not allowed: address ::1 is …`，两侧同源逻辑 |
| ⚠️ 取证说明 | ⚠️说明 | — | — | — | 参考侧开着的 SSRF 守卫使我无法通过其 API 注册 `127.0.0.1` 接收器。投递**签名/事件体**因此用**本服务**真实投递抓包 + 独立复算验证（构造已逐行核对参考源码）；**投递记录形状**则用向两侧数据库各播种同形 `webhook_delivery` 行后端到端读出来比对。参考侧用 `user_devin` 租户 schema，注意其 `webhooks.event_types` 是 **`text[]`** 而非 jsonb。 |

## 组 P 审计与 LLM 请求

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| GET .../audit-logs | ✅一致 | `api/http.py:10574-10610`；信封 `AuditLogListResponse{bank_id,total,limit,offset,items}`（`engine/audit.py:50-57`）；条目 10 字段（`engine/audit.py:30-48`）；`limit=Query(50,ge=1,le=500)`、`offset=Query(0,ge=0)` | `internal/reporting/handlers.go`（`AuditLogs`） | 见下方两行排序修复 | 空 bank 两侧 `{"bank_id":…,"total":0,"limit":50,"offset":0,"items":[]}` 逐字节相同。校验 10 组：`limit=0`/`limit=501`/`offset=-1` → 两侧 422 同体；`transport=bogus`/`action=nope` → 两侧 200（参考不校验枚举）；`start_date=2026-01-01T00:00:00Z` → 200 |
| GET .../audit-logs/stats | ❌已修 | `api/http.py:10616-10646`；`period: str = Query("7d")`（**无枚举校验**）；`AuditLogStatsResponse{bank_id,period,trunc,start,buckets}`、`AuditLogStatsBucket{time,actions,total}`（`engine/audit.py:60-75`）；`engine/memory_engine.py:14084-14091`：`trunc` 恒为 `"day"`，只有 `1d`/`30d` 另行取窗，**其余一律 7 天**，而 `period` 原样回显 | `internal/reporting/{handlers.go,chart_period.go}` | **修一处**：`?period=`（空值）时参考回显 `""`（空串是调用方送的值，窗口仍落 7 天），本服务原把空串当缺省、回显 `"7d"`——回了一个调用方没送过的值。现按「键是否存在」而非「值是否为空」取默认，值与重复键语义仍走 `httpx.Query`（保证路由参数声明审计不失效） | 两侧 `?period=` 修复后均回 `""`；`?period=bogus` 两侧均 `"bogus"`+7 天窗（**参考不校验**，本服务同样不校验——此处曾易误改成 400）；`1d`/`30d`/缺省三档 `trunc=day`、天数 1/30/7 两侧一致。单测 `TestChartPeriodTellsAbsentFromEmpty` 钉 9 例（含 `?period=&period=1d` → `1d`、`?period=1d&period=` → `""`）。**证伪**：改回判空 → 3 例失败 |
| GET .../llm-requests | ✅一致 | `api/http.py:10649-10700` | `internal/reporting/handlers.go`（`LLMRequests`） | 见下方排序修复 | 空 bank 信封两侧相同。校验 8 组：`limit=0`/`limit=501`/`offset=-1` → 422 同体；`group=bogus` → 422 `bool_parsing`（**`group` 是 bool 不是枚举**）；`status`/`scope`/`time_field` 传垃圾 → 两侧 200；`start_date=bad` → 400 |
| GET .../llm-requests/stats | ❌已修 | `api/http.py:10707-10737` | `internal/reporting/{handlers.go,chart_period.go}` | 与 `audit-logs/stats` 同一处 `period` 缺陷，同一修法 | 两侧 `?period=` 修复后均回 `""`；`?period=bogus` 均 `"bogus"`；信封 5 键（`bank_id/buckets/period/start/trunc`）同集；`?operation=nope` 两侧 200 |
| 排序：坏日期 vs 不存在的 bank | ❌已修 | `api/http.py:350-380` `_parse_iso_datetime`，其调用点在 **handler 签名**里（`:10583-10586`、`:10659-10662`），框架在进函数体前解析完 → 坏日期 **400 先于** bank 查找。错误文案 `f"Invalid {param}: '{value}' is not an ISO-8601 datetime."` | `internal/reporting/handlers.go`（`AuditLogs`、`LLMRequests`） | **修一处**：原 `requireBank` 在 `parseDateParam` 之前，故「坏日期 + 未知 bank」出 404 `Bank '…' not found`；参考出 400。现把两个日期参数解析提到 bank 查找之前。**注意**：这是「参数校验优先于资源不存在」大规则的又一实例，但触发点特殊——`start_date`/`end_date` 在 OpenAPI 里是**裸 `str`**（非 pydantic 校验类型），故 422 阶段管不到，靠签名内解析产生 400 | 修复后 4 组实测两侧**逐字节相同**：`audit-logs`/`llm-requests` × `start_date=notadate`/`end_date=bogus` 均 400 同文案；**反例守护**：日期合法或缺省时未知 bank 仍 404（两侧相同），确认重排没吞掉 404。单测 `TestMalformedDateOutranksMissingBank`（4 例）、`TestWellFormedDatesStillReportMissingBank`（3 例）。**证伪**：恢复原顺序 → 4 例全失败于 404 |
| POST .../health/llm | ❌已修 | `api/http.py:6678-6712`：**先**判 `enable_bank_llm_health`（关 → 404 `Bank LLM health check is disabled. Set WENYUAN_API_ENABLE_BANK_LLM_HEALTH=true to enable.`），**后**查 bank；响应 `BankLlmHealthResponse{bank_id,operations[]}`，每项 `{operation,ok,status,latency_ms}`，`operation` 取值 `retain`/`consolidation`/`reflect` | `internal/reporting/handlers.go`（`HealthLLM`） | **修一处**：原 `requireBank` 在开关判断之前，故「开关关 + 未知 bank」出 404 `Bank '…' not found`，参考出 404 **disabled 文案**。开关是**部署属性**而非 bank 属性，参考据此先答；现已把开关判断提前 | 修复后「未知 bank + 关」两侧**逐字节相同**（disabled 文案）。**开启**开关后实测：已知 bank → 200 `{"bank_id":…,"operations":[{"operation":"retain","ok":true,"status":"connected","latency_ms":…},…]}`，三个操作**按 retain/consolidation/reflect 顺序**、四字段同名；未知 bank → 两侧 404 `Bank '…' not found`。GET 该 POST-only 路由两侧均 405。单测 `TestDisabledLLMHealthOutranksMissingBank`。**证伪**：恢复原顺序 → 失败于 `Bank 'no-such-bank' not found` |
| ⚠️ 取证说明 | ⚠️说明 | — | — | — | 两侧数据库内容不同，`total`/`buckets` 数量与 `start` 时间戳属 fixture 噪音；本组只判**信封键集、状态码、错误文案、窗口天数、桶与排序规则**。`latency_ms` 为实时探测值，不比数值只比类型与字段存在性。审计行与 LLM 请求行两侧均无历史数据（`buckets=[]`、`items=[]`），故「有数据时的聚合结果」仅由**键集与聚合形状**保证，未做逐行数值比对。 |

## 组 Q MCP

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 工具目录（39 个） | ✅一致 | `api/mcp_tools.py`、`api/mcp.py` | `internal/mcp/tools_defs_{core,memory,mental,knowledge,admin}.go` | 无 | 经完整会话握手后 `tools/list`：两侧 **39 个**、**名称集合完全相同**。工具对象 6 个键（`name/description/inputSchema/outputSchema/annotations/_meta`）两侧**键集相同**；**39 个 `inputSchema` 逐个 `json.dumps(sort_keys)` 后 0 处差异**（属性名、类型、required、enum、默认值、描述全同）；**39 个 `description` 0 处差异** |
| MCP 传输：initialize | ✅一致 | `api/mcp.py:377-430`（`http_app(path="/")` + `stateless_http`）；结果 `protocolVersion` + `capabilities` | `internal/mcp/server.go` | 无 | 两侧均 `200`、`content-type: text/event-stream`、body 为 `event: message` + `data:` 行。`result` 键集两侧同集：`protocolVersion/capabilities/serverInfo/instructions`，`capabilities` 内 `experimental/logging/prompts/resources/tools/extensions` 同集且各自 `listChanged`/`subscribe` 值相同。**注**：两侧 JSON **键序不同**（参考按 pydantic 声明序，本服务按键名序）——JSON 对象无序，不可观测，非缺陷 |
| MCP 传输：Accept 协商 | ✅一致 | MCP Streamable HTTP 规范：必须同时接受 `application/json` 与 `text/event-stream` | `internal/mcp/server.go` | 无 | 7 组 Accept 两侧状态码与 content-type **完全一致**：`application/json`→200/SSE；`text/event-stream`（单值）→**406**；两者皆列→200/SSE；`*/*`→200；`text/plain`→200；缺省→200；带 `q=0.9`→200。**406 报文也逐字节相同**：`{"jsonrpc":"2.0","id":"server-error","error":{"code":-32600,"message":"Not Acceptable: Client must accept both application/json and text/event-stream"}}`（注意 `id` 是字符串 `"server-error"` 而非数字——易误写） |
| MCP 传输：GET / DELETE | ✅一致 | `api/mcp.py:434-443`：GET 无 session 时直接 200（客户端探活，避免被标记为服务死亡） | `internal/mcp/server.go` | 无 | `/mcp` 与 `/mcp/a` 上 GET→两侧 200、body 均 `{}`；DELETE 无 session→两侧 400 |
| MCP 错误处理 | ✅一致 | — | `internal/mcp/server.go` | 无 | 未知 method → 两侧 `-32602 "Invalid request parameters"`（含 `"data":""`）；未知工具 → 两侧 `result.content[0].text = "Unknown tool: 'nope'"` + `isError:true`；畸形 JSON → 两侧 400；缺 `id` 的通知 → 两侧 400。`prompts/list`、`resources/list`、`ping` 三方法两侧响应**逐字节相同** |
| MCP 工具调用结果 | ✅一致 | `_meta.fastmcp.wrap_result` 包裹 | `internal/mcp/tools_dispatch.go` | 无 | `list_banks` 两侧均返回 `{"_meta":{"fastmcp":{"wrap_result":true}},"content":[{"type":"text","text":"<缩进 JSON>"}]}`，内层 JSON 键同集；`recall`（不传 bank）两侧均返回 `{"results":…,"trace":…,"entities":…,"chunks":…,"source_facts":…,"source_facts_truncated":…}` 六键。**数据内容不同属 fixture 噪音**（两库内容不同） |
| ⚠️ `/mcp` 多段路径的状态与报文（跨组，越界） | ⚠️存疑 | `api/mcp.py:500-512`：中间件剥掉 namespace 后**取第一段作 bank_id**，其余交给 MCP app，而 app 只接受 `/` 之下 → `/mcp/a/b` 无对应挂载点，返回 **404 `Not Found`（`text/plain`，9 字节，无 envelope）**；但 **GET 例外**（`api/mcp.py:434-443` 在 bank 解析前就作答）→ GET `/mcp/a/b` 仍 200 `{}` | 根因在 `internal/httpx/router.go:557-560`：`handleAny("/mcp/{bank_id}", …)` 用的是 **Go mux 子树通配**，故 `/mcp/a/b` 也命中该 pattern 并被派发；实际响应由 `internal/httpx` 的 method 语义层产出 | **未修**：正确落点是 router 的 pattern 注册（`internal/httpx`），不在 `internal/mcp` 写范围。**我曾尝试在 `internal/mcp` 内加深度守卫，做不通并已完整回退**（详见下方「回退记录」） | 状态码对比：`/mcp/a`、`/mcp/a/`、`/mcp`、`/mcp/` 的 POST/GET/DELETE 两侧**全部相同**；仅 `/mcp/a/b`、`/mcp/a/b/c` 的 **POST 与 DELETE** 报文形式不同（两侧**状态码均 404**，本服务为 `{"detail":"Not Found"}` JSON、参考为 `text/plain` 的 `Not Found`）。**GET 两者均 200**。影响面：仅「多打了一段路径」的畸形请求，状态码已对，报文形式不同 |
| ⚠️ MCP 鉴权报文（跨组，越界） | ⚠️存疑 | 参考对 MCP **用 `{"error": …}`** 而非 `{"detail": …}`，且区分「缺头」与「无效」：无头 → `Authentication failed: Missing Authorization header. Expected: Bearer <api_key>`；错 key → `Authentication failed: Invalid API key`。HTTP 路由则一律用 `{"detail": …}` | `internal/httpx/tenantauth.go:55-95` | **未修**：在 `internal/httpx`，不在 `internal/mcp` 写范围 | **注意取证前提**：默认 parity 部署（18215-18218）**两侧都未配置 API key**，故都放行、看不出差异。我用 `WENYUAN_API_TENANT_EXTENSION=…ApiKeyTenantExtension` + `WENYUAN_API_TENANT_API_KEY` 在 18219 起了一个**开启鉴权**的实例后实测：本服务 MCP 无头/错 key 均 **401**（鉴权确实生效，与参考状态码一致），但报文为 `{"detail":"Authentication failed: Invalid API key"}``，参考为 `{"error":"Authentication failed: Missing Authorization header. Expected: Bearer <api_key>"}`。即：**状态码对、报文外层键名与「缺头 vs 无效」的区分不对**。同缺陷在 HTTP 路由上同样存在（两侧 HTTP 均为 `{"detail":…}`，但本服务「缺头」也报 `Invalid API key`） |
| ⚠️ 回退记录（避免后人重复踩坑） | ⚠️记录 | — | — | — | `/mcp/a/b` 的差异我最初判为可修，并在 `internal/mcp/server.go` 的 `ServeHTTP` 加了 `mcpPathInRange` 守卫。**做不通**，原因：本服务的 `internal/mcp` 单测（`rpcAt`/`handshakeSession`）直接把**应用内部路径**（如 `/mcp/b1/tools/list`，bank 段之后还挂着 MCP app 自己的路由）交给 `ServeHTTP`，因此「按段数判断」会把合法的单 bank 挂载判死（`TestSingleBankMountScopesTheRoster` 立即失败）。三次改法（裸段数 → `PathValue("bank_id")` 派生 → 尾斜杠）均失败或退化为恒真。**结论：深度校验属于 router 的 pattern 注册，不属于 handler**；已在 `internal/mcp` 完整回退（`git diff` 无 `mcpPathInRange`/`writeNotFound` 残留），未留死代码。**若日后修此项，请改 `internal/httpx/router.go` 的 pattern 注册，并给 `internal/app` 加端到端测试，不要改 `internal/mcp`** |
| ⚠️ 取证说明 | ⚠️说明 | — | — | — | MCP 需会话握手（`initialize` → `notifications/initialized` → 带 `Mcp-Session-Id` 调后续方法），裸 POST `tools/list` 会失败，不能据此判差异。两侧 MCP 均为 `stateless_http`，两侧 GET 探活语义一致。工具结果内层业务数据两侧不同（库内容不同），只比键集与包裹结构。 |

## 组长复核记录（旁证裁决）

- **组 J 通报的「`PUT /banks/{id}` 的 `{}` 契约不同」经复核为误报，两侧一致**。实测：
  `PUT .../banks/{id}` + `{}` → 两侧均 **200**；空 body → 两侧均 **422**
  `[{"type":"missing","loc":["body"],"msg":"Field required","input":null}]`。组 B 已把银行体的
  非对象/缺失体路径收敛（`internal/bankapi/decode.go` 的 `bankBodyIsObject` / `BodyNeedsValidation`），
  无需再改。
- **组 E 报的 `go vet` 红已修**：`internal/memoryops/validation.go:253` 的
  `append(append(path("tags"), strconv.Itoa(issue.index)))` 少一个参数（组 D 的在途改动带入）。
  已改为 `append(path("tags"), strconv.Itoa(issue.index))`，与 `TestRetainRejectsNonStringTagElement`
  期望的 `["body","items",0,"tags",1]` 一致。`go vet ./...` 现为绿。
- **dry-run `recorded_at` 精度已修**（原记于组 H 尾注）：参考 `engine/memory_engine.py:2268` 存
  `datetime.now(UTC)`，由 Python `datetime.isoformat()` 渲染——**恒定 6 位小数、仅全零时省略、偏移写
  `+00:00`**；Go 的 `time.RFC3339Nano` 会裁掉尾随零并写 `Z`，同一时刻产出不同字符串。已改走既有的
  `internal/timefmt.FormatTimestamp`（`internal/app/mental_model_dry_run.go`），并加
  `TestDryRunRecordedAtUsesTheReferenceRendering`（还原旧实现即 FAIL）。
- **组 I 的越界改动已批准**：`internal/storage/memory_recall_tags.go` 的 `trigramJaccard` 改为复用包内
  既有的 `entityNameTrigramSimilarity`（pg_trgm 逐词度量）。不新增第二份相似度实现是可取的。
- **横切规律（组 I/J/K 三次独立复现，据此定则）**：参考的 `Query(...)`/`Depends(...)` 参数由框架在
  **进入 handler 体之前**解析，因此**参数校验一律压过「银行/资源不存在」的 404**。已修三处：
  knowledge tree/search（组 I）、directives list（组 J）、documents/chunks/tags（组 K）。后续各组遇到
  「先查存在性还是先校验参数」时，一律按此规则；且参考把**所有**参数错误列在**同一个 422**里，
  不是报第一条。
- **组 J 的 `uuid` 列规则不适用于 `text` 列**（组 K 已实测）：`chunks.chunk_id`、`documents.id` 都是
  `text` 且路由用 `{...:path}`，9 种畸形 chunk_id 两侧全同，无 500/驱动文本。该规则只对真正的
  `uuid` 列成立（directives.id 是 `uuid`）。
- **组 N 的两项越界改动已由组长修完**（原 ⚠️ 关闭）：
  1. **`banks.disposition` 建行时未写入中性三元组**。参考 `_ensure_bank_exists` 的 INSERT 显式写
     `json.dumps(DEFAULT_DISPOSITION)`（`engine/retain/bank_utils.py:200,396`）。本服务 `ToStorage`
     只填 `BankID`/`Name`，列恒为 `{}`——HTTP 读面因读取时兜底默认值而看不出来，但**归档会把它带出去**
     （参考 `banks.json` 是三元组，我们是 `{}`）。已加 `storage.Bank.WithNeutralDisposition` 并在
     `ToStorage` 使用；用例 `TestCreatedBankStoresTheNeutralDisposition`（还原即 FAIL，列为 `nil`）。
     原注释「参考创建行时 disposition 为空」是错的，已一并订正。
  2. **操作行时间戳不保真**。`PutOperation` 无条件覆盖 `CreatedAt`/`UpdatedAt`，而导入需要保真。
     新增 `Store.RestoreOperation`（双后端实现）并在 `internal/transfer/import_apply.go` 使用；
     用例 `TestRestoreOperationKeepsTheArchivedTimestamps`、`TestRestoreOperationDefaultsMissingTimestamps`，
     并替换 `engine_test.go` 里原先「显式不断言」的注释为真实断言（换回 `PutOperation` 即 FAIL）。
- **组 L 引入的 `offset` 静默读取已修**：`graph` 与 `entities/graph` 两条路由的参考签名**都没有**
  `offset`（`api/http.py:5783-5794`、`:6805-6819`），组 L 为「按声明序校验」而给二者加了
  `offset`，导致 `?offset=abc` 回 422（参考忽略未声明参数，应放行）。`entityParamSpec` 拆出独立的
  `Offset` 开关，只有 `ListEntities` 打开；`internal/app` 的
  `TestRouteHandlersReadOnlyDeclaredQueryParams` 守住了这条不变量。新增
  `TestGraphRoutesIgnoreUndeclaredOffset`（还原即 FAIL）。
- **新增遗留横切项（组 L/M 发现，未修）**：`tag_groups` 含空复合组（如 `{"and":[]}`）时两个后端语义
  不一致——Postgres 返回**全部**（match-all），memory 后端返回 **0 条**。根因在
  `internal/storage/mental_model_staleness.go` 的 `matchTagGroups`/`matchTagGroup`：顶层
  `return len(groups) > 0`，而空 `and` 落到 `default:` 用空 `wanted` 调 `matchFlatTags`。
  参考此处返 500（`syntax error at or near ")"`，空 `and` 生成坏 SQL）——**该缺陷不复制**，
  但也**不把任一后端的偶然结果钉成契约**；仅以测试钉住「不崩、200、body 结构完好」。
- ~~**遗留横切项（尚未归组）**：`storage.TagGroup.Not` 是 `[]TagGroup`~~ → **已修，见下节**。

## 组长收尾裁决：组 O/P/Q 之后的横切修复

三个分组交回后，组长复核发现两处**跨组**缺陷并直接修复，另有若干 lint 死代码清理。

### 1. `/mcp` 多段路径（组 Q 上报，落点在路由层）

参考把 `/mcp` 与 `/mcp/{bank_id}` 注册为**整条路径**，所以 `/mcp/a/b` 不匹配任何路由，
由 Starlette 的 Router 亲自回 `404 text/plain`。本服务的 Go mux 让 `/mcp/{bank_id}/`
变成子树通配，把更深的路径也派发给了 MCP handler。

组 Q 的取证完全正确：**落点是 router 的 pattern 注册，不是 handler**——单测直接把
应用内部路径（`/mcp/b1/tools/list`）交给 `ServeHTTP`，任何按段数裁剪的 handler 守卫
都会把合法挂载判死。

修法：`internal/httpx/router.go` 新增 `undeclaredMCPDepth` + `undeclaredMCPPath`，
在 `Service()` 的链上包住 `r.mux`（`methodSemantics` 在 `extensionChain` 之外，包在那里
永远不跑——这是第一次尝试失败的原因）。命中时直接写 `404` + `text/plain; charset=utf-8`
+ `nosniff`；content-type 必须是 text/plain，否则 `RouteFallbackMiddleware` 会把它
重渲染成 `{"detail":"Not Found"}` 的 JSON 信封。

实测（同一请求两侧）：`/mcp/a/b`、`/mcp/a/b/c`、`/mcp/a/tools/list` 均为
`404 text/plain; charset=utf-8`；`/mcp`、`/mcp/` 仍正常服务。
用例 `TestMCPDeeperThanABankMountIsTheFrameworkNotFound`、
`TestMCPApplicationPathUnderABankMountIsAlsoRejected`、`TestMCPDeclaredMountsAreServed`。

### 2. 全库 consolidation 被折叠进 scoped 待办（组 G/M 的回归，偶发）

`TestReportingScopedConsolidationNotDeduplicated` **偶发失败**（10 次里挂 3～4 次）。
根因不是测试不稳，是**真实的契约缺陷**：两个后端的「按 bank 去重」都没有排除 scoped 待办，
内存后端还遍历 map，于是「scoped + 全库都待办」时谁被选中取决于 map 迭代顺序。

参考的实现是显式的（`engine/memory_engine.py:22207-22211`）：
> Dedup only against an existing **unscoped** (full-bank) pending op. A pending
> scoped consolidation covers only its tag subset, so it must not swallow a
> full-bank sweep (#1842).

修法：内存后端抽出 `outstandingFullBankConsolidation`（跳过带 `observation_scopes`
的待办）；Postgres 后端给 `enqueueDedupeByBank` 加 `unscopedOnly` 参数，拼
`AND task_payload->'observation_scopes' IS NULL`（参考同款检查放在 Python 里是因为
Oracle 的 `JSON_VALUE` 对数组返回 NULL，注释已照抄这个理由）。graph maintenance 传
`false`（它没有 scoped 兄弟）。

用例 `TestFullBankConsolidationIsNotDeduplicatedIntoAScopedOne`（还原即 FAIL，且原失败
信息里能直接看到被折叠的是那条 `observation_scopes:[["team:exec"]]` 的 op）。

### 3. lint 死代码

各组重构后留下的未使用函数与一处 staticcheck 建议，已全部清理（行为不变）：
`mentalmodels/helpers.go` 的 `maxTokensFailure`/`tagsFailure`/`decodeBody`、
`readsurface/params.go` 的 `freeIntParam`/`literalParam`、
`transfer/python_json_error.go` 的 `opensContainerOrSeparator`、
`reporting/knowledge_tag_groups.go` 的 tagged switch。

**复核对账**：`grep -c '^## 组 '` = 17，A…Q 齐全，无 `待填` 残留。

### 4. 模板导出：`bank` 段的字段来源与键序（对等性全链接口暴露）

`make parity` 全量跑出 **1 处 DIFF**（209 项里 203 项一致 → 修完后 202 一致 / 0 differing / 7 known）：

1. **`bank` 段错误地读了 `banks.disposition` 遗留列**。参考的 `api_export_bank_template` 只调
   `_load_bank_config`，而它 `SELECT config` 一列（`config_resolver.py:415-425`）；disposition 三个
   trait 是 **config 的键**（`config_resolver.py:988-994`），不是遗留列。本服务把中性三元组从列里
   摊平进模板，于是「无配置的 bank」导出了整段 51 个 null，参考导出的是 `"bank": null`。
   这是**先前那个 disposition 修复的连带回归**——修好写入之后才暴露出来。

2. **`bank` 段的键序是字典序**。`Manifest.Bank` 是 `map[string]any`，Go 的 `encoding/json` 对 map
   一律排序，而参考按 `BankTemplateConfig` 的**声明序**渲染（`api/http.py:3833-4070`）。值全对、字节全错。
   新增 `internal/banktemplate/marshal.go`（`Manifest.MarshalJSON` + `orderedObject`），按声明序
   输出已知键，未知键排在后面且有序，不会静默丢键。修完两侧 **1680 字节逐字节相同**。

用例：`TestManifestRendersTheBankSectionInDeclarationOrder`、
`TestManifestBankSectionNullAndUnknownKeys`（还原即 FAIL）。

**顺带把一条含糊的 known-difference 查清了**：`config patch bad key` 原先记为「线上跑的是更旧的
build」。实测**不是**——两侧 `(total: 51 fields)` 与拒绝措辞完全一致，差异只在样例那十个名字。
参考写的是 `sorted(list(a_set)[:10])`，Python str 集合的迭代序受**哈希随机化**支配：同一份代码
三次启动给出三组不同的名字，`PYTHONHASHSEED=0` 也不等于字典序前十。这是**不可复现的输出**，不是契约；
本服务取字典序前十个是同一事实的确定性呈现。该条 known-difference 的说明已据实改写。

## 复验：参考实现的 77 个系统测试故事（`make parity`）

`make check` 只覆盖 HTTP 面，覆盖面之外还有参考实现自己的黑盒故事。2026-10-20 首次全量跑：
**14 failed / 325 passed** → 修完后 **339 passed / 1 skipped / 0 failed**。四处根因，全部是本服务的缺陷：

### 1. `RestoreOperation` 撞上已存在的行就失败（组 N 修复的回归）

`ON CONFLICT (operation_id) DO NOTHING` 命中时 `RETURNING` 什么都不返回，`scanOperation` 把
`sql.ErrNoRows` 当成错误抛出 → 导入报 `write operation: sql: no rows in result set`。
改为落回按 id 读取那一行（还原是幂等的，调用方要的是结果不是"我插了没有"）。

**同一处还有第二个错**：回退用的 `GetOperation(ctx, bankID, id)` 会按 bank 过滤，而导入会把归档行
重映射到目标 bank——同一份归档克隆第二次时，撞上的是第一次克隆的行，bank_id 不同，于是
`operation not found`。改用不带 bank 过滤的 `getOperation`。影响 `test_53_bank_transfer`
全部 8 个故事。

### 2. consolidation 候选把「已标记失败」的 fact 又捡了回来

`ConsolidationFacts` 的 WHERE 少了 `consolidation_failed_at IS NULL`。参考的候选查询明确排除
（`pg/reads.py:383-387`："a memory the LLM could not handle must not be retried forever"）。
后果是一个毒输入每轮都被重喂——正是这个字段存在的理由。两个后端都修了。

### 3. reconcile 扫描的发现查询从错误的表出发

`BanksNeedingConsolidation` 是从 `memory_units` 出发的；参考的例行函数从 `banks` 出发再
`EXISTS (memory_units ...)`（`migrations/0005_reference_functions.sql`）。差别在于**孤儿 unit**
（bank 行已删、unit 残留）：我们的查询会把它们当成"有工作的 bank"报上来，随后插入撞外键失败。

### 4. reconcile 一处失败就放弃整轮

`reconcileConsolidation` 遇到第一个 enqueue 错误直接 `return`，同 schema 里后面的 bank 再也轮不到。
参考是**逐个 bank 捕获并继续**（`engine/maintenance.py:479-482`）。改为记 WARN 并继续（`ctx`
取消仍然中止）。

用例：`TestFailedConsolidationIsNotRetriedAsACandidate`、
`TestReconcileContinuesPastABankThatCannotBeQueued`（均验证过去掉修复即 FAIL）。

**遗留观察（未改动）**：系统测试库里有 6277 个 bank / 15118 条孤儿 `memory_units`，是历次
中断的测试运行留下的；第 3 条修复后扫描不再受它们影响，未清理。

## 收尾：`tag_groups` 的 `not` 改为单节点

组 L/M 留下的最后一项横切差异。参考的 `TagGroupNot` 是 `filter: TagGroup`
（`engine/search/tags.py:333-337`）——**单个节点**，而 `TagGroupAnd`/`TagGroupOr` 才是 `list[TagGroup]`。
本服务在 `storage.TagGroup` / `httpx.TagGroup` / `memoryops.TagGroupRequest` 三处都把它写成了列表。

语义差异本身不大（单元素列表的否定与单节点的否定等价），真正的差异在**契约**：
实测线上参考对 `{"not":[{"tags":["x"]}]}` 返回 **422**，7 个错误项——

```
missing   … TagGroupLeaf  tags          model_attributes_type … TagGroupNot not TagGroupLeaf
missing   … TagGroupAnd   and           model_attributes_type … TagGroupNot not TagGroupAnd
missing   … TagGroupOr    or           model_attributes_type … TagGroupNot not TagGroupOr
                                       model_attributes_type … TagGroupNot not TagGroupNot
```

注意 NOT 分支自己**没有** `missing` 项：列表让该分支的 `model_attributes_type` 检查通过，错误落进它自己的子联合。
改成单节点后本服务的 envelope 与之逐项相同（`TestTagGroupNotRefusesAList`、
`TestRecallRefusesAListShapedNot` 守 422 与 7 项）。

三处类型改为 `*TagGroup`，随之收紧了若干处：`memory_recall_tags` / `mental_model_staleness` /
`postgres_recall` / `tag_resolution` 的取值从「遍历列表」变成「取一个子节点取反」；
`reporting/knowledge_tags.go` 里 `knowledgeTagGroup` ↔ `storage.TagGroup` 的往返不再需要「单元素列表」
这个中间态；`readsurface` 本来就是单节点（现在与其余各层一致）。

**保持不变的**：`and`/`or` 仍接受单对象或列表两种写法（参考的 lax 行为）。`NewResponse` 的
`{"tag_groups":[{"not":{"tags":[...]}}]}` 这一形状在两个后端仍产出与之前相同的匹配结果——
`TestTagGroupsAcceptContractShapes` 已改为对象形，`TestTagGroupNotAcceptsASingleNode` 钉住解码与取反。

验收：`make check` 全绿；`make parity` **339 passed, 1 skipped, 0 failed**。

---

# 第二轮：逐 API 重审（2026-10-20 起）

> 本轮清单见 `docs/API-AUDIT-MANIFEST.md` §4/§5（99 条 OpenAPI operation，15 个写作用域单元）。
> 每个子 Agent 只追加自己的 `## 第二轮 <单元名>` 小节。

## 第二轮 monitor

单元：`internal/monitoring/`、`internal/app/metrics*.go`（API #1–#5）。参考检出
`../hindsight/hindsight-api-slim/hindsight_api/`，行号以当前检出为准。线上差分两侧：
参考 `http://127.0.0.1:8888`，本服务 `scripts/parity_dev_service.sh 18210`。

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 1 `GET /health` | ✅一致 | 装饰器 `api/http.py:5650`；共享体 `api/http.py:5648-5654`（`health_check()` 结果原样返回，`status=="healthy"` → 200 否则 503）；体 `engine/memory_engine.py:6179-6215`（成功三键 `:6203-6207`，再 `:6208` update 池统计；失败 `:6210-6215` 不含池键；未初始化 `:6190-6191`）；池统计插入序 `:6223-6232`（先 `db_pool_waiting`，后 `in_use`/`max`/`idle`） | `internal/monitoring/health.go:117-172`（`Ready`/`readyBody`）、`HealthBody` `:84-108` | 见下方「池统计一致性」一行 | 键序与键集逐位相同：`status,database,db_acquire_ms,db_pool_waiting,db_pool_in_use,db_pool_max,db_pool_idle`；`parity_check GET /health` 仅 `db_pool_idle` 不同 |
| 2 `GET /health/ready` | ✅一致 | 装饰器 `api/http.py:5667`；同一个 `_readiness_response()`（`api/http.py:5648-5654`） | 同 #1，同一 `Ready` handler（`health.go:172-174` 注册两路） | 无 | 与 #1 同体同码；`parity_check GET /health/ready` 仅 `db_pool_idle` 不同 |
| 3 `GET /health/live` | ✅一致 | 装饰器 `api/http.py:5684`；`api/http.py:5701` 返回 `liveness_response()`；模型 `liveness.py:39-55`（`status`=`"alive"`、`version`、`uptime_seconds`），`liveness.py:31-36` 以 `round(...,1)` 上报 | `internal/monitoring/health.go:196-201`（`Live`）、`:27-33`（`Float` 保 `.0`）、`:210-212`（`round1` = ROUND_HALF_EVEN） | 无 | `parity_check GET /health/live` → **SAME**（`uptime_seconds` 是活测量：本服务 4.5，参考 351584.0） |
| 4 `GET /version` | ✅一致 | 装饰器 `api/http.py:5703`；`VersionResponse` `api/http.py:4853`（`api_version` + `features`）；`FeaturesInfo` 字段序 `api/http.py:4828-4844`（11 个 flag）；`api/http.py:5729-5745` 逐键赋值 | `internal/monitoring/version.go:23-94`（`VersionBody` 手写序列化按 `config.FeatureKeys` 声明序）、`:96`（`Serve`）；键序 `internal/config/config.go:928-940` | 无 | `parity_check GET /version` 仅 `features.worker` 不同（`parity_dev_service.sh` 固定 `WENYUAN_API_WORKER_ENABLED=false`，属对照夹具）。两侧键序逐位相同，`api_version` 均 `"1.0.0"`（参考 `__init__.py:43`） |
| 4b `GET /v1/version`（别名，不在 OpenAPI 内） | ✅一致 | `api/http.py:5716`（`include_in_schema=False`，同一 handler） | `internal/reporting/handlers.go:119` | 无 | 与 `/version` 同体同码；`parity_check GET /v1/version` 唯一差异同 `features.worker` |
| 5 `GET /metrics`（响应头与生成路径） | ✅一致 | 装饰器 `api/http.py:5748`；`api/http.py:5759-5772`：有 `worker_metrics` 用 `worker_metrics.render`，否则 `generate_latest`；`media_type=CONTENT_TYPE_LATEST` | `internal/monitoring/metrics.go:18`（`text/plain; version=1.0.0; charset=utf-8`）、`:107-118`（`Serve`，`Workers` 非 nil 走 `Render()`，否则 `Exposition()`） | 无 | 两侧 `content-type` 逐字节相同、均带 `vary: Accept-Encoding`、`Accept-Encoding: gzip` 时均回 `content-encoding: gzip` |
| 5 `GET /metrics`（HTTP 家族与标签形状） | ✅一致 | `api/observability.py:93-125`（`http_endpoint_label`：路由模板，未匹配 `"unmatched"`）、`api/observability.py:60`（`UNMATCHED_ENDPOINT`）；`metrics.py:521-534`（`hindsight.http.duration`/`requests.total`/`requests.in_progress`，up-down counter 带 unit `requests`）；`metrics.py:876-893`（`record_http_request` 标签集） | `internal/monitoring/metrics.go:157-205`（三个家族渲染）、`:23`（`otel_scope_name=hindsight_api.metrics`）、`internal/httpx/metrics.go:209-232`（中间件）、`internal/httpx/router.go:532-546`（模板/`unmatched` 解析） | 无 | 三个家族名逐字相同；标签集 `endpoint/method/otel_scope_*/status_class/status_code` 相同；两侧均出现 `endpoint="unmatched"` 的 307（尾斜杠）与 404 序列；in-progress 带 `_requests` 单位后缀 |
| 5 `GET /metrics`（pool / process / stall 家族） | ✅一致 | `metrics.py:1090-1127`（`db.pool.size/idle/min/max/waiting` 五个 observable gauge，HELP 文案在此）、`metrics.py:1006-1033`（`process.cpu/memory/open_fds/threads`）、`metrics.py:615-627`（`event_loop.stalls` counter 与 `stall_duration` histogram）、`metrics.py:539-543`（`db.pool.acquire_wait`） | `internal/app/metrics.go:170-209`（`dbPoolMetrics`）、`internal/monitoring/process_metrics.go:31-62`、`internal/monitoring/workload.go:101-107,344-359`、`internal/monitoring/looplag.go:92-98` | 无 | 15 个共有家族的 `# HELP` 与 `# TYPE` **逐字节相同**；`wenyuan_db_pool_*` 两侧同值（5.0/5.0/100.0/0.0）；`wenyuan_db_pool_acquire_wait_seconds` 桶边界两侧同为 `0.0,5.0,…,10000.0,+Inf`；`wenyuan_event_loop_stalls_total` 空载两侧均 `0.0` |
| 5 `GET /metrics`（`wenyuan_db_pool_waiting` 与 `/health` 的一致性） | ❌已修 | `metrics.py:1090-1127` 里 `hindsight.db.pool.waiting` 由 `engine/db/pool_instrumentation.py:35,43` 的 `waiting_count()` 供数；`engine/memory_engine.py:6224` 的 `db_pool_waiting` 是**同一个** `waiting_count()`；而 `_pool_health_stats` 的利用率来自后端 `_pool_stats`（`engine/memory_engine.py:6228`、`engine/db/postgresql.py:338-345`）——两者在同一进程内必然同源、同值 | `internal/monitoring/health.go:59-82`（原 `PoolStatsReporter` 与 `PoolWaitingReporter`/`PoolWaiter` 三个**独立**能力接口）、`:152-165`（读取处） | 把两个独立能力收敛为一个复合接口 `PoolReporter`（`health.go:59-82`），等待数与利用率从**同一个** reporter 一起读、按参考序先写 `db_pool_waiting`。原先一个只实现 `PoolStats` 的适配器会让 `/health` 缺 `db_pool_waiting`，而同一进程的 `/metrics` 仍有 `wenyuan_db_pool_waiting 0.0`——这个"半套池视图"参考在结构上无法产生。`internal/app/app.go:504-541` 的 `storePinger` 本来就两个方法都有，自动满足新接口，**无需改公共文件** | `TestHealthPoolWaitCountAgreesWithMetricsGauge`（`internal/monitoring/health_test.go:534-563`）；证伪已实测：把 `readyBody` 改回只看 `PoolStatsReporter` 后该用例 `FAIL`（`db_pool_waiting = <nil> (present false), want 1`），改回即通过。另 `TestHealthReportsPoolWaiting`/`TestHealthPoolWaitingForwardingAndFailure`/`TestReadinessPoolStats`/`TestHealthPoolKeyOrder` 同步改为按新契约构造，全部通过。线上重建后 `/health` 与 `/metrics` 两侧仍一致 |
| `/debug/pprof/*` | ✅一致 | 参考无 pprof 路由（`api/http.py` 全文无 `pprof`/`/debug`） | `internal/monitoring/profile.go:11-21`（仅 `WENYUAN_API_PPROF_ENABLED=true` 才挂载） | 无 | 8 条路径两侧逐条同码同体：`404 {"detail":"Not Found"}` |
| 尾斜杠 / 错误方法 / 未知查询参数 | ✅一致 | Starlette `redirect_slashes`；FastAPI 405 信封 | `internal/httpx` 路由与 fallback | 无 | `/health/`、`/metrics/` 等 5 条两侧均 `307` + `Content-Length: 0`；`HEAD`/`POST` 两侧均 `405` + `Allow: GET` + `{"detail":"Method Not Allowed"}`（HEAD 无体）；`/health?x=1` 两侧均带 `X-Ignored-Params: x` 且体同形；`//health`、`/HEALTH`、`/health//live` 两侧均 `404 {"detail":"Not Found"}` |

### 5 `GET /metrics` 的家族集差分（复核第一轮"22 个参考独有家族 = 已知不修"）

重建后空载暴露体与参考的家族集 diff：**本服务独有 0 个**，参考独有 22 个。逐族核实来源：

- **`python_gc_*`(3) + `python_info` + `process_*`(6) = 11 个**：`prometheus_client` 默认寄存器与 Python 宿主进程指标（`process_start_time_seconds`、`process_resident_memory_bytes` 等）。Go 进程无法、也不应产生 CPython 的 GC/解释器指标。`docs/PARITY-OPEN-ITEMS.md` 7.6 已判不修。**维持不修，但依据应是"平台指标不可复刻"，而不是"观测驱动"**。
- **`wenyuan_llm_*`(5) + `operation_*`(2) + `recall_phase_*` + `retain_*`(3) + `event_loop_stall_duration_seconds` = 12 个**：参考侧因共享库上有存量流量而已有观测。**实测证明部分是"空载"、但 `operation_*` 不是**：
  - `recall_phase_duration_seconds` 在**本次调用后即出现**（跑一次 recall 后参考独有集从 22 降到 21）→ 空载，**不是缺陷**；
  - `operation_duration_seconds` / `operation_operations_total` 在跑完 recall 后**仍然不出现**：本服务 `ObserveOperation` 全仓**只有一处调用**（`internal/worker/consumer.go:617`，`source="worker"`）。参考对 `recall`/`reflect`/`retain` 在 **HTTP handler 层**用 `metrics.record_operation(...)` 记录（`api/http.py:6243`、`:6442`、`:10279`），其 finally 分支无条件落 `operation.total`（`metrics.py:714`），因此 `source="api"` 的系列存在。实测：对参考发一次 recall（401 被拒）后 `wenyuan_operation_operations_total{source="api"}` 总数 **743→744（+1）**——连被拒请求也计数，证明是 handler 包裹层而非业务成功路径。本服务同请求后该家族计数仍为 **0**（连家族都不出现）。
  - 该缺口**机械存在**，但修点在 `internal/memoryops/`（recall/reflect/retain 三个 handler）与 `internal/app/app.go`（把 recorder 注入），**不在本单元写作用域**，故移交组长（见文末「需跨作用域修改」）。

### 第一轮结论复核（不照抄）

| 第一轮结论 | 本轮复核 | 结论 |
|---|---|---|
| `/health` 键序与 `db_pool_waiting` 已修 | 线上两侧键序仍逐位相同（`status,database,db_acquire_ms,db_pool_waiting,db_pool_in_use,db_pool_max,db_pool_idle`），`db_pool_waiting` 仍无条件输出 | **成立，无回退** |
| `/health/live` 一致 | `parity_check` 仍 **SAME**；`round(...,1)`、`.0` 保留、ROUND_HALF_EVEN 均对上参考源码 | **成立** |
| `/version` 仅 `features.worker` 不同 | 仍如此；键序与 11 个 flag 集合逐位相同 | **成立** |
| `/metrics` 22 个参考独有家族"已知不修 + 观测驱动" | 数目仍为 22，但**归因部分错误**：其中 11 个是 Python 平台指标（不可复刻），`recall_phase_*` 确实空载，而 `operation_*` 的 `source="api"` 部分是**机械缺失**（见上） | **结论需更正**（已在上节写明） |
| `wenyuan_event_loop_stalls_total` 空载系列已修 | 空载暴露体两侧均有 `wenyuan_event_loop_stalls_total … 0.0`；`event_loop_stall_duration_seconds` 属"从未有停顿"，参考有存量停顿 | **成立** |
| `/debug/pprof/*`、尾斜杠、405、未知参数一致 | 逐条复测，全部一致 | **成立** |
| "第一轮已收敛"的 `storePinger.PoolWaiting` 全链路 | 方法确实存在（`internal/app/app.go:533`），但第一轮漏掉了它与 `PoolStats` 的**能力分离**问题（本轮已修） | **第一轮结论不全** |

### 新增/改动的文件（供组长 review diff）

新增：`internal/memoryops/file_retain_request.go`、`file_retain_report.go`、`retain_models_blocks.go`、`retain_models_content.go`、
`retain_models_json.go`、`retain_models_members.go`、`file_retain_request_test.go`、`retain_append_test.go`、`retain_vision_test.go`、
`internal/reporting/dryrun_entity_labels_test.go`。
改动：`internal/memoryops/{retain.go,retain_fold.go,inline_attachments.go,file_retain.go,validation.go}`、
`internal/entitylabels/reference_shape.go`、`internal/reporting/{memory_routes.go,dryrun_content.go}`；
`retain_models.go` 按 500 行上限拆成五个文件（内容不变）。
**测试断言修正**（旧断言本身是错的，非放松）：`internal/memoryops/retain_content_blocks_test.go` 四处把校验项 `input`
写成 Go map 的字母序（`{"source":{…},"type":…}`、`{"text":"x","type":"text"}`、空文本块），参考保留调用方书写序；
`retain_attachments_test.go`/`retain_test.go` 两个"图片上传保留占位符"的用例改用 `newServerWithVision`（它们断言的是
**视觉模型**下的行为，而默认 handler 集没有视觉能力——这正是本轮修复的那条规则）。

### 本单元验收

```sh
go test -count=1 ./internal/monitoring/... ./internal/app/...   # ok / ok
go vet  ./internal/monitoring/... ./internal/app/...            # 无输出
go build ./... 2>&1 | grep -v 'read-only'                       # 无输出
CGO_ENABLED=0 go build -tags pgx ./... 2>&1 | grep -v 'read-only' # 无输出
gofmt -l internal/monitoring internal/app                        # 无输出
golangci-lint run ./internal/monitoring/... ./internal/app/...   # 0 issues
```

## 第二轮 observations

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 32 `DELETE /v1/default/banks/{bank_id}/observations` | ✅一致 | `api/http.py:9462-9485`（`DeleteResponse(success=True, message=f"Cleared {n} observations", deleted_count=n)`）；`engine/memory_engine.py:11643-11710`：**无** bank 存在性检查（未知 bank 200/0）、`_refuse_bank_wide_if_scoped(..., "clear the bank's observations")` 在 `:11660`；`engine/memories/pg/engine_curation.py:167-188` 先 COUNT 再 DELETE `fact_type='observation'` 并把 `experience/world` 的 `consolidated_at` 置 NULL；`observation_history` 整行删除 `:11692-11695`；`banks.last_consolidated_at=NULL` `:11698-11701`；stats 缓存失效 `:11704-11707` | `internal/reporting/handlers.go:540-564`（先 `RefuseBankWideIfScoped`、无 `requireBank`、`invalidateBankStats`）；存储 `internal/storage/postgres_memories.go:674-734` | 无 | 线上差分：新建 `audit-ob-*` 上 `DELETE` 两侧逐字节 `SAME` `{"success":true,"message":"Cleared 0 observations","deleted_count":0}`；未知 bank 两侧 `SAME`；清空后 `GET .../observations/scopes` 两侧同体 `{"scopes":[],...}`；重复 `DELETE` 两侧仍 `Cleared 0`；往返：清空后 `stats.total_observations` 与 `pending_consolidation` 两侧同为 0 / 4 |
| 33 `GET /v1/default/banks/{bank_id}/observations/scopes` | ❌已修 | `api/http.py:9487-9519`：`limit: int = Query(100, ge=0, le=1000)`、`offset: int = Query(0, ge=0)`，**query 校验先于 handler**；`engine/memory_engine.py:11747-11782`（`_authorize_bank_read` → `_require_bank_exists` → `observation_scope_counts`）；`engine/memories/pg/counts.py:155-207`：scope = `COALESCE(ARRAY(SELECT unnest(tags) ORDER BY 1),'{}')`、`GROUP BY scope ORDER BY count DESC, scope`、`total` = DISTINCT scope 数、`LIMIT/OFFSET` 在 SQL 里 | `internal/reporting/handlers.go:566-594` → 新增 `internal/reporting/observation_params.go`（`parseObservationScopeQuery`/`observationScopeQueryValue`/`frameworkIntFromString`）；`internal/storage/postgres_observability.go:613-655` | 修两处：①原 `boundedIntParam` 把**空值当缺省**（`?limit=` 返回 200 默认 100，参考是 422 `int_parsing`），并**只报第一个**被拒参数（`limit=-1&offset=-1` 我们 1 项，参考 2 项）；②原用 `strconv.Atoi(strings.TrimSpace(raw))`，与框架的字符串→int 转换不同：参考接受 `" 5"`/`"5 "`/`"5\t"`/`"5\u00a0"`/`"\u30005"`（Python `str.strip()` 语义）、`+5`、`1_000`、`1_2_3`、`5.0`/`5.00`/`5_0.0`（零小数部分），拒绝 `5.`/`.5`/`5.5`/`5e2`/`1__0`/`0x10`/`_1`/`1_`、且 `12345678901234567890` 是 `less_than_equal`（Python int 无界，先比边界再收窄）而非 `int_parsing`。改为按声明顺序收集全部被拒参数、按框架语义解析、用 `*big.Int` 比边界 | `TestObservationScopeQueryValidation`、`TestObservationScopeQueryReportsEveryRejectedParameter`（**已证伪**：把 `ObservationScopes` 换回原 `boundedIntParam` 两例即 FAIL —— `GET "?limit=+5" = 422, want 200`、`items = 1, want 2`）。线上差分：36 路（`""`/`abc`/`1.5`/`5.`/`5e2`/`1__0`/`0x10`/`true`/` 5`/`5 `/`5.0`/`1_000`/`+5`/`007`/`-0`/`5\u00a0`/`\u30005`/`\t5`/`5\t`/`5.5`/`5._0`/`5.0_0`/`5_0.0`/`-5.0`/`.5`/`--5`/`+-5`/`1_2_3`/`9007199254740993`/`12345678901234567890`/`5\` + `?offset=`/`?limit=&offset=`/`?limit=abc&offset=abc`/`?limit=-1&offset=-1`/`?limit=2000&offset=-1`）修后 **0 DIFF**。形状侧：3-scope/4-scope 真实夹具上 `scopes`/`total`/`limit`/`offset` 键集键序、tag 已排序、`count DESC, scope` 排序、分页（`limit=1&offset=1..3`）两侧逐字节 `SAME` |
| 34 `POST /v1/default/banks/{bank_id}/consolidation-strategies/preview` | ❌已修 | `api/http.py:9521-9554`（`request: ConsolidationStrategiesPreviewRequest` 必填）；`api/http.py:2332-2339`（`strategies` 必填、`sample_limit` 默认 5 `ge=0 le=50`）；`engine/response_models.py:718-729`（`ConsolidationStrategySpec` 只声明 5 个键，**其余按 non-strict 丢弃且不做类型校验**）；`engine/memory_engine.py:11715-11745`（`list_observation_scopes(limit=10000, offset=0)`，`complete = total <= len(scopes)`）；`engine/consolidation/consolidator.py:3536-3629`：`taken_count = sum(1 for k in hits if winners[k] != i)`（`:3603`）、`claimed_count = sum(1 for winner in winners if winner == i)`、`samples = hits[:sample_limit]`、`default` = 无 winner 的 scope | `internal/reporting/handlers.go:596-660`、`internal/reporting/consolidation_preview.go:361-491`（`countTakenAway`）、`internal/reporting/models.go:190-230` | 修 `internal/reporting/consolidation_preview.go` 的 `countTakenAway`：原判据 `winners[k] != nil && *winners[k] != strategy`，对**未生效（inactive）策略**的规则把 `winners[k]==nil` 的 scope 算成"没被抢走"⇒ `taken_count` 报 0；参考对 `winners[k]==nil` 也计为 taken（该规则匹配但本策略不处理它）。改为 `winners[k] == nil || *winners[k] != strategy` | `TestInactiveStrategyRulesReportScopesTheyDoNotHandle`（**已证伪**：恢复旧判据即 FAIL `taken_count = 0, want 3`）。线上差分修后 **17 路 0 DIFF**：`strategies:[]`、`[{scopes:[]}]`、`[{scopes:[{tags:[]}]}]`、`[{scopes:[{tags:["*"]}]}]`、`exact` 规则、两规则重叠、两策略重叠（首个赢）、`sample_limit:0/1/2/50`、`observations_mission:""`/`"  "`、`{scopes:[...],max_observations_per_scope:0}`、未声明键 `consolidation_llm_batch_size:"x"`；含 `handled_by`/`taken_count`/`claimed_count`/`complete` 全字段。422：空 body/null/`{}`/`[]`/`[1]`/`"x"`、`sample_limit=70/-1`、`tags_match:"bogus"`、`observations_mission:5` 两侧逐字节 `SAME` |
| 35 `POST /v1/default/banks/{bank_id}/consolidation/recover` | ✅一致 | `api/http.py:9556-9579`（`RecoverConsolidationResponse(retried_count=result["retried_count"])`）；`engine/memory_engine.py:11785-11822`：`_refuse_bank_wide_if_scoped(..., "retry the bank's failed consolidation")` 后**直接** `requeue_failed_consolidation`，**无** `_require_bank_exists`；`engine/memories/pg/engine_curation.py:190-209` 先 COUNT 再 UPDATE `SET consolidation_failed_at=NULL, consolidated_at=NULL WHERE bank_id=$1 AND consolidation_failed_at IS NOT NULL AND fact_type IN ('experience','world')` | `internal/reporting/handlers.go:662-678`（先 `RefuseBankWideIfScoped`，无 `requireBank`）；`internal/storage/postgres_memories.go:792-816`（`WITH requeued AS (UPDATE … RETURNING 1) SELECT count(*)`，同过滤条件） | 无（第一轮已修的 `requireBank` 删除仍在，无回退） | 线上：未知 bank 两侧 `200 {"retried_count":0}`；真实 bank 两侧同体；**副作用往返**：在自建 bank 上把 3 条 `world` 事实置 `consolidation_failed_at=now()` 后 `POST .../recover` → `{"retried_count":3}` 且库中该 bank 的 `consolidation_failed_at IS NOT NULL` 计数由 3 变 0 |
| 36 `DELETE /v1/default/banks/{bank_id}/memories/{memory_id}/observations` | ✅一致 | `api/http.py:9581-9610`（`ClearMemoryObservationsResponse(deleted_count=result["deleted_count"])`）；`engine/memory_engine.py:11825-11885`：`_require_writable(bank_id, ..., memory_id=memory_id)` → `_delete_stale_observations_for_memories` → `if deleted_count > 0` 才 `requeue_source_memory` 与 `submit_async_consolidation`（后者条件还有 `config.enable_auto_consolidation`） | `internal/reporting/memory_routes.go:340-405`（memory-crud 单元的文件，只读复核）；`ClearMemoryObservations` 绑定在 `internal/reporting/handlers.go:90` | 无（本单元不改 `memory_routes.go`） | 线上：正常 uuid 两侧 `200 {"deleted_count":0}`；非 uuid 两侧 `500 {"detail":"badly formed hexadecimal UUID string"}`；未知 bank + 非 uuid 两侧同为该 500；**副作用往返**：在自建 bank 上对一条 `world` 源事实执行 → 两侧均 `{"deleted_count":1}`，随后 `GET .../observations/scopes` 两侧同体缩减到 2 个 scope；再 `consolidate` 后该 scope 重新出现（源事实已重新入队） |
| 37 `POST /v1/default/banks/{bank_id}/consolidate` | ✅一致 | `api/http.py:9705-9737`（`request: ConsolidationRequest \| None`、`submit_async_consolidation(..., observation_scopes=request.observation_scopes if request else None, caller_requested=True)`）；`:4624-4641`（`ConsolidationResponse{operation_id, deduplicated}`）；`engine/memory_engine.py:22768-22834`：`caller_requested` 才 `_refuse_bank_wide_if_scoped(..., "run consolidation on the bank")`，**`dedupe = observation_scopes is None`**（body 出现 `observation_scopes` 含 `[]` 即永不去重）；`_submit_async_operation` `:22056-22160` 对未知 bank 抛 `Bank '<id>' not found`/404；`:22161-22245` 只与 `task_payload->'observation_scopes' IS NULL` 的 `status='pending'` 行去重 | `internal/reporting/handlers.go:680-745`（校验 → `RefuseBankWideIfScoped` → `requireBank` → scoped/full 分派）、`:746-804`（`parseConsolidationRequest`） | 无 | 线上：未知 bank 空 body/`{}`/`scoped` 两侧均 404 同体；`[[1]]`→`string_type loc:["body","observation_scopes",0,0]`、`[1]`→`list_type … ,0`、`"x"`→`list_type`、`[]`/`5`→`model_attributes_type loc:["body"]` 两侧逐字节 `SAME`；真实 bank 连打 3 轮：空 body/`{}`/`null`/`{"bogus":1}` 去重回同一个 op（`deduplicated:true`）、`{"observation_scopes":[]}` 与 `{"observation_scopes":[["user:bob"]]}` 每次都新 op（`deduplicated:false`）——两侧布尔序列一致（`operation_id` 是活值，按语义比对） |

**本单元结论计数（按 API）：✅ 4 / ❌ 2 / ⚠️ 0。**

### 单元内已复核并证伪的第一轮记录（均仍成立，无回退）

1. `DELETE .../observations` 未知 bank 200/0、`recover` 未知 bank 200/0、`consolidate` 未知 bank 404 —— 代码与线上均仍如此。
2. 「全库 consolidation 不被折叠进 scoped 待办」：`internal/storage/postgres_enqueue.go:187-204` 与内存后端的 `unscopedOnly` 仍在；本轮线上 `{"observation_scopes":[]}` 每次都 `deduplicated:false` 印证。
3. 「consolidation 候选排除 `consolidation_failed_at`」：`internal/storage/postgres_consolidation.go:31`、`memory_consolidation.go:43-48` 仍在。
4. 「reconcile 从 `banks` 出发」+「单 bank 失败继续」：`internal/storage/postgres_maintenance.go:83-118`、`internal/app/maintenance_jobs.go:260-286` 仍在。
5. 「scopes 不拒绝 tag-scoped 调用方」：`ObservationScopes`/`ConsolidationPreview` 确实没有 `RefuseBankWideIfScoped`，与参考 `_authorize_bank_read` 一致。

### 上报组长（跨作用域，本单元未动）

- **`internal/httpx` 的 query 分号缺陷**：Go `url.Query()` 遇到未转义 `;` 会静默丢弃该键，参考（Starlette）把 `;` 当普通字符。实测 `?limit=5;6` 参考 422 `int_parsing input:"5;6"`，本服务走默认值/404。影响所有读 query 的路由。

## 第二轮 memory-crud

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 28 `GET .../memories/list` | ❌已修 | `api/http.py:350` `_parse_iso_datetime`（`datetime.fromisoformat`）；`le=1000` 未在 handler 生效；框架的字符串→int 语义 | `memory_routes.go:237` `lastDateParam`、`:190` `nonNegativeIntParam` | ①日期参数改用 CPython 版 ISO 解析；②`limit`/`offset` 改用包内 `frameworkIntFromString`（`5.0`/`1_0`/`+5` 接受，`_5`/`5e2` 拒绝，`limit=` 空值 422 `int_parsing`）；③实体排序见下"跨作用域" | `TestMemoryListAcceptsPythonISODateForms`、`TestMemoryListIntegerParameterForms` |
| 29 `GET .../memories/{memory_id}` | ❌已修 | `engine/memory_engine.py:13247` `get_memory_unit` 先 `str(uuid.UUID(id))` | `memory_routes.go` `GetMemory` | id 按 CPython `uuid.UUID` 规则规范化（`urn:`/`uuid:` 任意位置任意大小写删除、`{}` 全剥、`-` 全删、余 32 hex），**不 trim**（含空白 → 400 且回显原值）；不再把 `urn:uuid:…` 直接丢给 pgx 变 500 | `TestMemoryByIDRoutesNormalizeTheIdentifier`、`TestMemoryByIDRoutesDoNotTrimTheIdentifier` |
| 30 `PATCH .../memories/{memory_id}` | ❌已修 | 同 `:11916` `update_memory_unit`（先规范化 id，再 404 回显原值） | 同上 `UpdateMemory` | 同 29；另：`1e999` 的 `input` 是 `inf`，参考在序列化错误体时二次爆炸 → 裸 `500 text/plain "Internal Server Error"`，已按此还原（`TestMemoryUpdateInfinityBodyIsTheServerFallback`） | 同上两个 id 测试 + `TestMemoryUpdateInfinityBodyIsTheServerFallback` |
| 31 `GET .../memories/{memory_id}/history` | ❌已修 | `:13363` `get_observation_history`：先规范化 id，再读**活表**单元，读不到即 `404 Memory unit '<raw>' not found` | `memory_routes.go` `MemoryHistory` | ①id 规范化同 29；②单元在 `invalidated_memory_units`（归档）里时返回 404，与 detail 路由"仍能读到归档行"的不对称按参考还原 | `TestMemoryHistoryOfAnInvalidatedUnitIsNotFound`、上面两个 id 测试 |

**已确认 SAME（未改）**：列表项 23 个键与顺序、null vs absent vs `[]`、默认排序 `mentioned_at DESC NULLS LAST, created_at DESC`、分页、`q` 的 ILIKE（含重音不对称）、`type`/`document_id`/`entity_id`/`tags`/`tags_match`、`state`、`consolidation_state`、`time_field`、非法值 400 字节、detail 形状（含 `history:[]`、`observation_scopes`、`source_memories` 排序）、history 形状、PATCH 19 组非法体 422 字节、PATCH 全部成功路径（text/context/日期设清/entities 设清/fact_type/invalidate/revert）、observation 不可策展 400、未知单元/银行 404、405、尾斜杠 307。

**第一轮（组 F）更正**：第一轮把 28/29/31 判为 `✅一致` 是错的——以上四条 API 均有实测可复现的字节级差异（id 形式 500/400、日期与整数形态 422/200、history 归档 404/200）。

## 第二轮 bank-config

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 14 `GET .../profile` | ✅一致 | `api/http.py:8411` 声明无 body | `internal/bankapi/retired_routes.go` | — | 无 body → 410 一致；带任意 body 仍 410 |
| 15 `PUT .../profile` | ❌已修 | `api/http.py:1856-1859`（`UpdateDispositionRequest`）、`:1769-1774`（`DispositionTraits` int `ge=1 le=5` 全必填） | `internal/bankapi/retired_body.go`（新增） | 退役路由原先不看 body 直接 410；现按"先校验后处理器"的顺序先跑模型校验、合格才 410 | `TestRetiredProfileWriteValidatesItsBodyBeforeTheGoneStatus`；线上 17 例全 `SAME` |
| 16 `POST .../background` | ❌已修 | `api/http.py:1886-1895`（`AddBackgroundRequest`，`content` 必填、`update_disposition: bool = True`） | 同上 | 同上 | `TestRetiredBackgroundWriteValidatesItsBodyBeforeTheGoneStatus`；线上 13 例全 `SAME` |
| 17 `GET /v1/bank-template-schema` | ✅一致 | — | `internal/reporting/bank_template_schema.json` | 无需改（第一轮怀疑的 `marshal.go`/`templateBankFieldOrder` 经查不是成因） | 28998 字节、deep-compare 0 diff；仅生成文档的 `$defs` 键序不同（见下 ⚠️） |
| 18 `GET .../config` | ❌已修 | `config_resolver.py:574-581` 只校验、`_persist_bank_config` 存调用方原 dict | `internal/bankapi/config.go` | `entity_labels` 读回键序对齐 | `TestUpdateBankConfigStoresTheCanonicalEntityLabelsValue`；线上 3/3 `SAME` |
| 19 `PATCH .../config` | ❌已修 | 同上 + `parse_entity_labels` 的 pydantic 报告 | `internal/bankapi/config_keys.go`、`internal/entitylabels/reference_shape.go`（新增） | ① `entity_labels` 结构校验改为复刻 pydantic 报告（原先大量该拒的接受了）；② 存储值改为调用方原 dict + 参考序列化键序 | `TestUpdateBankConfigRejectsAnInvalidEntityLabelsValue`、`TestShapeProblemsRejectsWhatTheReferenceRejects`、`TestCanonicalKeepsTheCallersMembersAndTheSerialiserOrder`；线上 15/15 `SAME` |
| 20 `DELETE .../config` | ✅一致 | — | — | — | 200 + overrides 清空，一致 |

**本轮实际修掉的偏差（每条都有"改回去就 FAIL"测试）**

1. **退役写路由不校验 body**（15/16）：参考先结构校验、不合格 422，合格才 410。新增 `internal/bankapi/retired_body.go` 复刻 `UpdateDispositionRequest` / `AddBackgroundRequest` 的项：`missing`、`model_attributes_type`（嵌套模型形状）、`int_type`/`int_parsing`/`int_from_float`、`bool_type`/`bool_parsing`、`string_type`、`greater_than_equal`/`less_than_equal`，且按字段声明序排列。
2. **`entity_labels` 接受了参考拒绝的值**：`optional:"x"`、`tag:"x"`、`description:1`、`values[*].description:1`、`fields.*.description:1`、`type:null`、`values:null`、非对象的组元素。现在输出与 pydantic 2.12 逐字节相同的报告（`LabelGroup` / `model_type` / `literal_error` / `list_type` / `bool_parsing`）。
3. **`entity_labels` 存储形态错**：原实现把整数字面量 `"type"` 注入、且丢 `{"attributes":[]}`。参考存的是**调用方原 dict**（迁移只用于校验），空列表照样存。已修，且读回时按参考序列化键序重排（jsonb 不保序）。
4. **嵌套 `dict_type` 措辞**：参考对"本该是模型的成员"报 `model_attributes_type`；共享解码器报 `dict_type`。已在退役路由内改名，未动 `internal/httpx`。

**⚠️ 存疑（非代码问题，判为非契约）**：`config` 对象键序（参考 `config_resolver.py:183` 迭代 `frozenset`，随进程哈希种子变化、跨进程不可复现）、`overrides` 中大对象键序（参考是 jsonb 短键优先序）、`PATCH` 未知字段报错里的 "Valid configurable fields: [...]" 样本（同一 set 迭代）、17 模板 schema 生成文档的 `$defs` 键序（pydantic 声明序 vs 排序；`value equal: True`、`bytes equal: False`，同 28998 字节）。

## 第二轮 mental-models

复核方式：逐条读参考源码取行号，并在**开启 worker 的**本服务副本（端口 18217，直连 `15432/wenyuan`）与线上参考（`8888`）之间做盲测差分。两侧各建随机 bank 做写入，结束后已删除（`WyA7Fq3`、`audit-mm-dodge`、`audit-mm-final`）。

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 38 `GET .../mental-models` | ✅一致 | `api/http.py:6920-6982`；`engine/memory_engine.py:16567-16680`（`limit=100` `ge1 le1000`、`offset=0 ge0` 无上界）；`api/http.py:3409-3470`（`MentalModelListResponse`） | `internal/mentalmodels/handlers.go`（`List`/`pageSlice`）、`helpers.go`（`pageParams`） | 无 | 线上 `?limit=0/1001/-1`、`?limit=2&offset=5`、`?tags=a`、`?detail=metadata`、`?include_stale=true` 全部 `SAME`；条目键集与键序两侧相同（14 键，null 保留） |
| 39 `GET .../{id}` | ✅一致 | `api/http.py:6983-7020`；`memory_engine.py:16692-16740`（单条不探 bank）；`_row_to_mental_model` `:20458-20560` | `handlers.go`（`Get`，`detail` 默认 `full`，仅 full 算 `is_stale`） | 无 | 线上 `detail=metadata/content/full`、`detail=config`→422、未知 id→404 全部 `SAME` |
| 40 `GET .../{id}/history` | ✅一致 | `api/http.py:7021-7051`；`memory_engine.py:16748-16830`（裸数组） | `handlers.go`（`History`）；`internal/storage/postgres_mental_models.go`（双编码读取） | 无 | **开启 worker 后**两侧同出现 1 条 `{"changed_at","previous_content","previous_reflect_response"}`（首轮标 ✅ 时的"空数组"比较其实是 harness 关掉了 worker，见尾注 1） |
| 41 `POST .../mental-models` | ✅一致（本轮又修 3 处） | `api/http.py:7052-7098`；`memory_engine.py:17077-17190`；`api/http.py:3750-3782`；`api/http.py:3199-3389`（`MentalModelTrigger`） | `handlers.go`（`Create`）、`trigger.go`、`trigger_members.go`、`helpers.go` | 见修复⑤⑥⑧ | 线上创建 12 例（含 `trigger:null`、`trigger:{}`、cron+cons 互斥、未知名、空 name、`max_tokens` 越界）全部 `SAME` |
| 42 `POST .../{id}/refresh` | ✅一致（响应形状）+ 本轮修 `reflect_response` 落库形状 | `api/http.py:7099-7132`；`memory_engine.py:23188-23280`、`:23380-23440`；`api/http.py:4812-4830` | `handlers.go`（`Refresh`）；`internal/app/mental_model_refresh.go` | 修复④ | 线上两侧 `{"operation_id","status":"queued"}` 同体；跑完后 `GET ?detail=full` 的 `reflect_response` 键集、`based_on` 六个类型键、`outcome`、`mental_models:[]` 全部相同 |
| 43 `POST .../{id}/dry-run-refresh` | ❌已修（窗口语义 + 两处时间编码） | `engine/memory_engine.py:18384-18480`；`engine/mental_model_refresh.py:280`（`recorded_at: datetime`）、`:304-` | `internal/app/mental_model_dry_run.go` | 修复②③ | 线上 `window` 两侧同构（`created_before` 是**数据库快照**、`watermark` 是**数据水位**，两者不同值；本服务修复前两者恒等）、`trace.recorded_at` 两侧同为**六位微秒 + `Z`** |
| 44 `POST .../{id}/clear` | ✅一致 | `api/http.py:7183-7218`；`memory_engine.py:18963-19040`（UPDATE 只动 `content/structured_content/last_refreshed_source_query/last_refresh_failed_at/embedding`，**不动** `reflect_response`，也不写 history） | `handlers.go`（`Clear`）、`internal/storage/{memory,postgres}_mental_models.go` | 无代码改动（补注释 + 回归测试） | 线上 clear 的 14 键逐字段相同（仅时间戳不同）；**首轮「clear 清 `reflect_response`」的说法有误**——参考不清，本服务也不清——已加 `TestClearMentalModelKeepsTheReflectResponse` 钉住 |
| 45 `PATCH .../{id}` | ❌已修（trigger 合并语义 ×3） | `api/http.py:7219-7261`；`memory_engine.py:18481-18600`、`:19134-19200`（`_merge_trigger`）；`api/http.py:3783-3830` | `handlers.go`（`Update`）、`trigger.go`（`MergeTrigger`） | 修复①⑥⑦ | 线上 10 步顺序差分（cron↔cons 反复、null 成员、成员逐个 patch）全部 `SAME` |
| 46 `DELETE .../{id}` | ✅一致 | `api/http.py:7262-7299`；`memory_engine.py:19056-19130`（`{"status":"deleted"}`） | `handlers.go`（`Delete`） | 无 | 线上 `200 {"status":"deleted"}`、二次删 404、未知 id 404 全部 `SAME` |

**本轮修复（每条都配了"回退即 FAIL"的测试）**

1. **`MergeTrigger` 丢弃 `null` 值 → 显式 `null` 无法清除成员**（`internal/mentalmodels/trigger.go`）。参考是 `{**base, **supplied}`，`null` 原样覆盖。本服务原 `if value == nil { continue }`，于是 `PATCH {"trigger":{"refresh_cron":null}}` 只输出"无列可更新"→ 404，而参考把成员存成 null 并回 200。→ `TestTriggerMergeStoresExplicitNulls`。
2. **dry-run 的 `window.created_before` 错用了数据水位**（`internal/app/mental_model_dry_run.go`；新增 `internal/storage/mental_model_sources.go` 两个可选能力）。参考的 `created_before` 是数据库时钟快照，`watermark` 是"快照时刻的最新在域记忆"；本服务原来两者都取 `BankWriteWatermark` 且恒等（实测修复前 `created_before` 落后 6 分钟）。→ `TestDryRunWindowSnapshotIsNotTheDataWatermark`、`TestDryRunWatermarkIsNullWhenTheScopeIsEmpty`。
3. **dry-run 的 `trace.recorded_at` 后缀**：首轮改成 `timefmt.FormatTimestamp`（`+00:00`）**改错了**——该字段是响应模型成员（`engine/mental_model_refresh.py:280`），框架按 `…Z` 序列化，线上实测参考 `…624805Z` / 本服务 `…629256+00:00`。已改为 `timefmt.FormatZuluTimestamp`。→ `TestDryRunRecordedAtUsesTheReferenceRendering`。
4. **refresh 落库的 `reflect_response` 形状不对**（`internal/app/mental_model_refresh.go`）。本服务写 `{"based_on":{"memories":[…]}}`，参考写 `{"text", "based_on"（固定六类型键，`mental-models` 连字符）, "mental_models": [], "outcome"}`（`memory_engine.py:15650-15660`、`:17642-17646`、`:18241`）。这是 `GET ?detail=full` 直接可见的字段。→ `TestRefreshStoresTheReferenceReflectResponseShape`。
5. **trigger 整数成员：`true/false` 未按 lax 转成 1/0，且大整数经 `map[string]any` 重编码丢失**（`helpers.go` 改 `json.Number` 解码、`trigger.go` 的 `triggerIntValue`）。线上 `{"min_refresh_interval_seconds":true}` 参考存 `1`、本服务修复前为 `null`；`2048` 同样丢失。→ `TestTriggerIntegerMembersReadBooleansAsNumbers`、`TestTriggerIntegerMembersKeepLargeValues`。
6. **trigger 互斥只做了一半**（`trigger.go` 的 `MergeTrigger`）。参考 `_merge_trigger` 是**双向**的：给了 `refresh_cron` 就清 `refresh_after_consolidation`，反过来也一样。**这条与首轮结论相反**——首轮读成"单向（cron 赢）"，线上 10 步顺序差分证明双向。→ `TestTriggerScheduleMembersAreExclusive`。
7. **`PATCH {"trigger": null}` 应答**：参考对"未点任何列"的 patch 统一回 404（与 `PATCH {}` 同），本服务原先把显式 null 当模型形状错误回 422。→ `TestUpdateTreatsNullTriggerAsNoColumn`。（`POST` 上的 `{"trigger":null}` **是** 422 `model_attributes_type`，两侧一致，未动。）
8. **布尔成员 `bool_parsing` / `bool_type` 的分界**（`trigger_members.go` 的 `triggerBoolItem`）：字符串与非 0/1 的**整数**→ `bool_parsing`；`null`/对象/数组/带小数 → `bool_type`。→ `TestTriggerBooleanMembersTakeLaxCoercion`。

**尾注（组长裁定）**

- **尾注 1（harness，已由组长修）**：`scripts/parity_dev_service.sh` 把 `WENYUAN_API_WORKER_ENABLED=false` 写死。凡"参考有值、本服务为 null"的字段（`reflect_response`、`last_memory_seen_at`、history 行、refresh 后的 `content`）**全部**是这个开关造成的假差异。已加 `PARITY_DEV_WORKER=1` 开关。
- **尾注 2（首轮结论勘误，影响其他切片）**：「pydantic 时间戳都是 `isoformat()` + `+00:00`」的推论不成立——**响应模型里的 datetime 走框架序列化 → `…Z`**，只有直接塞进 `dict` 的字符串才可能是 `+00:00`。其他切片若按同一推论改过时间渲染，需一并复核。
- **尾注 3（tag-scope，跨作用域，本轮不修）**：参考的 mental-model 路由全部接 `_tag_scope`/`_write_tag_scope`：`list` 按 scope 过滤、`create` 有 `_refuse_tags_outside_scope`/`_refuse_unwritable`/`_scope_mental_model_trigger`、update/clear 对 scope 外模型回 404、delete 带 `build_tag_groups_where_clause`。本服务 `internal/mentalmodels` 没有这一层；默认部署（未配 `WENYUAN_API_TAG_SCOPE` 扩展）两侧等价，故线上差分看不出。证据：`memory_engine.py:16567`、`:17077` 段、`:19056`、`:19106-19130`；扩展实现 `hindsight-system-tests/server_extensions/tag_scope_validator.py`。落点需要 `internal/httpx` 的 scope 取值 + `internal/storage` 的方法签名改造。
- **尾注 4**：`internal/reporting/knowledge.go:394` 的 knowledge page trigger patch 复用 `mentalmodels.MergeTrigger`，因此自动继承本轮修正；参考的知识页 patch 走同一个 `_merge_trigger`，**有意**如此。

## 第二轮 retain

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 21 `POST .../memories/dry-run-extract` | ❌已修 | `api/http.py:2572` `DryRunExtractRequest.timestamp: datetime \| None`（裸 datetime，非 `datetime\|str`）；缺 `content` 的 item 报 `input` = 整个 body | `internal/reporting/memory_routes.go:917`（曾 `Timestamp *string`）、`:1049`（解码失败丢 input）、`dryrun_content.go:39-43`（missing 传空 map） | `Timestamp` → `json.RawMessage`；解码失败改用解码器自带的 `decoded` 渲染；missing 传整个 body；新增 `dryrun_timestamp.go` + `internal/validation/timestamp.go` 复刻参考的 datetime 解析器 | `TestDryRunTimestampMatchesTheReferenceAnswer`、`TestDryRunTimestampAcceptedValuesRunTheExtraction`、`TestDryRunDecodeFailureKeepsTheRefusedValue`、`TestDryRunMissingContentReportsTheBody`；`TestTimestampFailureMatchesTheReferenceParser`（6976 条参考真值）；线上 25/25 `SAME` |
| 22 `POST .../prompts/preview` | ❌已修 | `api/http.py:2433` `PromptPreviewRequest.strategy: str \| None`，在进路由前校验 | `internal/reporting/memory_routes.go:1291` 先查 bank → 500 | 新增 `previewStrategyFailure`，在 bank 查询**之前**报 422 `string_type` | `TestPromptPreviewRejectsStrategyTypeBeforeBankLookup` |
| 23 `POST .../memories` | ❌已修 | `async` + `items: []` 时参考不排队任何工作，返回 `operation_id: null` | `internal/memoryops/retain.go:617` `submitAsyncRetain` 无条件 `SubmitAsyncRetain`，发一个永不推进的 UUID | 空 items 直接返回收据（`ProcessedContentTokens: -1`），不发操作 | `TestAsyncWriteWithNoItemsReportsNoOperation` |
| 24 `POST .../files/retain` | ❌已修 | `api/http.py:10479-10502` 参考提交时**不校验任何文件格式**，把转换失败推迟到 operation；实测 `a.xyz` 文本 → 200 且原文入库；转换型上传**不写** attachments 行、文档读 `attachments: null` | `internal/fileparse/markitdown.go:145`（无转换器扩展名一律 unsupported → 400）、`internal/memoryops/file_retain.go:568`（无条件 `PutAttachment`） | markitdown 对未知扩展名回落"是文本就原样返回"（NUL / 非 UTF-8 才算二进制）；转换型上传不再写附件行，图片仍保留 | `TestMarkitdownReadsUnrecognizedTextFormats`、`TestFileRetainConvertedUploadKeepsNoAttachment`；线上 xyz/noext/unknown 全 200 `SAME` |
| 25 `DELETE .../memories` | ✅一致 | — | — | 无 | 5 组 `type` 参数（缺省/合法/非法/空/重复）全 `SAME`；未知 bank 也 `SAME` |

**第一轮结论的更正**

- 第一轮 "组 D retain" 的 `TestRetainMissingItemsReportsTheBody` **不存在**——该行用例名有误。缺陷本体在 `ValidateRetain`（`validation.go:101-105`）只在 `req.Items == nil` 时生效；线上 `items:[]` / `"x"` / `null` 三种都比对一致，行为正确、只是没有测试覆盖。
- 第一轮**漏报**了上表 6 个缺陷（API 21 三处、22 一处、23 一处、24 两处）。
- `internal/fileparse/fileparse_test.go` 的 `TestMarkitdownTextAndUnsupported` **保留名字但改了语料**：原用例用 ASCII 文本 `"binary"` 当二进制、断言 `.doc` → unsupported，与参考实测矛盾（参考对任意扩展名的文本字节都接受）；现改用真二进制 `{0x00,0x01,0x02,0xff}`。另新增 `TestMarkitdownReadsUnrecognizedTextFormats` 覆盖接受/拒绝两侧。同时修正 `internal/app/file_retain_async_test.go` 两处断言"转换型上传有 1 条 attachment"的旧行为（后者改名 `TestFileRetainConvertedUploadWritesNoAttachment`）。**全仓已无测试断言"不支持的二进制 → 400"。**

**⚠️存疑（域外，不为对拍硬凑）**

1. **转换失败的时机**（API 24 唯一残余差距）：本服务对**二进制**上传仍在提交时 400，参考 200 + operation `failed`（`No converter attempted a conversion…`）。要和参考完全一致需把转换挪进 worker 并改 operation 状态机（`memory_engine.py:4092-4180`、`:4678-4748`），涉及 `internal/storage/` + `internal/worker/` 的结构性改动，本轮不修，留待单独立项。
2. 参考解析器对"既不像日期也不是数字"的长畸形串报 `invalid character in year`，我们对其中 182 条**不同子形状**报 `input is too short`——**判定（接受/拒绝）完全一致**，仅 `ctx.error` 文本不同，属域外。判定层面对 3 份独立语料（389 + 2585 + 4088 条）**全部 0 不一致**。

**`internal/validation` 包适用面**：它是"参考框架 datetime 解析器"的逐字复刻，只依赖 `internal/httpx`；入口 `TimestampFailure(value string) *httpx.ValidationError` 产出与参考一致的校验项（type / msg / ctx.error，`loc` 由调用方设置）；适用于任何把某成员按裸 `datetime` 建模的路由——目前只有 dry-run-extract 使用。

**跨作用域修复（组长落地）**：`/files/retain` 无 metadata 的上传现在存 `{"metadata": {}}`（`internal/memoryops/file_retain.go`，参考 `api/http.py:10494` 的 `file_meta.metadata or {}` 在 `:4179` 无条件进 content dict），`internal/storage/memory_retain.go` 的 `retainParamsFor` 判据不动——理由是 `/memories` 内联 retain 的 `metadata: {}` 走同一判据，在那里无条件写会连带改变那条路由的存储形态。测试 `TestFileRetainRecordsEmptyMetadata`（已证伪）。

## 第二轮 recall-reflect

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 26 `POST /memories/recall` | ❌已修 | `engine/search/tags.py:344` `union_mode="left_to_right"`，实测 422 detail 为"每个分支各一条，按分支声明序"；`TagGroupLeaf(tags, match, resolve)` `:307-317` | `internal/memoryops/taggroup_validation.go`、`recall_request.go`、`models.go` | `tag_groups` 联合诊断重写：分支顺序 Leaf→And→Or→Not，分支全部拒绝时逐分支报告；`input` 保持 JSON 原始类型（数字不再是字符串）；`resolve` 字面量集修正为 `exact\|fuzzy`（原先误用 match 集） | `TestTagGroupUnionItems`、`TestTagGroupNumbersStayNumbers`、`TestTagGroupsNonListIsOneFieldItem` |
| 26 | ❌已修 | `tags.py:390-403`：空 leaf 在 `exact` = 仅未打标签，默认 `any_strict` = 至少一个标签 | `internal/storage/memory_recall_tags.go:leafMatches`、`postgres_recall.go:tagLeafClause`（两后端）、`recall_request.go`（`tagsGiven`） | 空 leaf 原先被 `decodeTagGroup` 丢弃 + 两后端都渲染成"无过滤"，导致过滤被静默放宽；现按参考渲染 | `TestEmptyLeafStillFilters`（内存后端三模式）；`fusion_test.go` 钉住 SQL 形态（空叶渲染 `"()"`，见下） |
| 26 | ❌已修（组长落地） | `engine/memories/pg/consolidation.py:268-320`：observation 的 INSERT **不写 context 列** → 行内为 NULL，`memory_engine.py:10517` 原样透传 → 结果 `context: null` | `internal/storage/models.go`（`MemoryUnit.ContextAbsent`）、`postgres_retain.go`（`nullableContext`）、`postgres_consolidation.go:176`、`memory_consolidation.go:186`、`consolidation_scope.go:148/224` | observation 三个创建点置 `ContextAbsent`，写入落 NULL；fact 仍存空串 | `TestObservationHasNoContext` |
| 27 `POST /reflect` | ❌已修 | `api/http.py:6405-6434`、`ReflectRequest` `:1505-1611` 无顶层 `trace` | `models.go`、`reflect.go`、`reflect_response.go` | 删除 `ReflectRequest.Trace` 及其回退分支（trace 只由 `include.tool_calls` 开启） | `TestReflectIgnoresPhantomTraceMember` |
| 27 | ❌已修 | `fact_types: list[Literal[...]]` 逐元素报 `literal_error` | `reflect_validation.go` + `taggroup_validation.go:reflectFactTypeItems` | `fact_types` 改逐元素 item；`tag_groups` 报在 body 级 item 旁而非替代 | `TestReflectUnionItemStream` |

**复核第一轮**：1.3 ✅（entities 通道与每结果 entities 逐项一致，`ORDER BY 1,2`）、1.7 ✅（两后端种子仅取密集命中、无词法回落）、1.8 ✅（`scores` 四键恒在，缺席为 null）。三条仍成立。

**组长复核与收口**

- `fusion_test.go` 的"空叶 = TRUE"是陈旧断言：参考的 `build_tag_groups_where_clause` 用 `" AND "` 连接各叶（`tags.py:470-477`），空叶贡献空括号 → 语句语法错误，而不是静默放宽过滤。`postgres_recall.go:tagLeafClause` 的空叶分支已改为 `"()"`，`fusion_test.go` 同步改断言。
- 待办：`wenyuan_operation_*` 缺 `source="api"` 系列（monitor 单元报出），落在 `internal/memoryops` 的 recall/reflect/retain 同步路径。

## 第二轮 bank-lifecycle

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #6 `GET /v1/default/banks` | ❌已修 | `api/http.py:6592`、`engine/retain/bank_utils.py:708`（`if search_query:`，Python 真值判断）、`:672`（`_aliases_explaining` 的 `if not search_query`） | `internal/bankapi/banks.go:377`（`q` 命中即 `SearchGiven`）、`internal/storage/models.go`（`BankQuery.SearchGiven`）、`postgres_banks.go:24`、`memory_banks.go:39`、`memory_bank_helpers.go:12,44` | 空白 `q` 原先被当作"没给"，列表不设过滤；现按参考"给了就过滤"，`?q=%20` 匹配不到任何银行。`matched_aliases` 仍按 `search == ""` 判断（参考只判空串，不判是否给出） | `TestBlankSearchStillFilters`（`internal/bankapi`，去掉 `SearchGiven` 即 FAIL）；`internal/storage` 的 `BankQuery{Search,SearchGiven}` 用例同步更新 |
| #7 `GET .../{bank_id}/aliases` | ✅一致 | `api/http.py:8476`、`engine/memory_engine.py:20548` | `internal/bankapi/aliases.go:53` | — | 199 例探针全 SAME |
| #8 `POST .../{bank_id}/aliases` | ❌已修 | `api/http.py:8499`、`engine/bank_aliases.py:187` | `internal/bankapi/aliases.go:66` | ①整 body 校验（`missing` 先于 `bool_parsing`）②`alias:null` 报 `string_type` 而非 `missing` ③成功响应重读列表（原先返回被替换的旧主别名）④非对象 body 报参数项 | `TestAliasBodyReportsEveryMember`、`TestAliasNonObjectBodyReportsTheParameter` |
| #9 `PATCH .../aliases/{alias}` | ✅一致 | `api/http.py:8530` | `internal/bankapi/aliases.go:150` | — | 探针全 SAME |
| #10 `DELETE .../aliases/{alias}` | ✅一致 | `api/http.py:8565` | `internal/bankapi/aliases.go:203` | — | 探针全 SAME |
| #11 `PUT .../{bank_id}` | ❌已修 | `api/http.py:8593`、`engine/memory_engine.py:21641` | `internal/bankapi/banks.go:79` | ①非有限值（`1e999`/`NaN`/`Infinity`）→ 参考的 `500 text/plain "Internal Server Error"`，且该判断必须先于模型校验 ②嵌套 `disposition` 单趟按成员校验 + 缺失成员后置且不重复 ③`missing` 项的 `input` 回显原始对象 | `TestNonFiniteValueAnswersTheServerError`、`TestNestedDispositionReportsEachMemberOnce` |
| #12 `PATCH .../{bank_id}` | ❌已修 | `api/http.py:8638` | `internal/bankapi/banks.go:180` | 同 #11（共用路径） | 同上 |
| #13 `DELETE .../{bank_id}` | ✅一致 | `api/http.py:8684`、`engine/memory_engine.py:11377` | `internal/bankapi/banks.go:200` | — | 探针全 SAME |

**分页参数**：#6 的 `limit`/`offset` 超出机器整数时，参考返回 200 并原样回显调用方写的大数（`limit` 无上限、`offset` 得空页），原实现静默退回默认值；已修（`TestPageParametersWiderThanAMachineInt`）。

**复核第一轮**：第一轮把 `POST aliases` 与嵌套 `disposition` 判为 ✅ 一致，是误判——嵌套 `disposition` 在"成员解码失败"分支多报/重报 item，别名 body 在 `primary` 类型错误时漏报 `missing` 项。均已修正并有用例固定。

**探针噪声**：`GET /v1/default/banks` 自身的行集合两侧不同（参考 83 个 bank、本服务 80 个），属 MANIFEST §3 的夹具差异，不是缺陷。`GET /v1/default/banks/{bank_id}` 两侧同为 405，不是缺失 API。

## 第二轮 knowledge

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #47 `GET .../knowledge-base/tree` | ❌已修 | `api/http.py:7302`、`api/http.py:3703-3715`（`tag_groups` 依赖）、`engine/search/tags.py:116-123` | `internal/reporting/knowledge_tags.go:knowledgeTagFilterItems` | `tag_groups=`（显式空串）原先报适配器解析失败；参考的依赖是 `adapter.validate_json(raw) if raw else None`，空串即"没给"，现按缺省处理（`tag_groups=%20` 仍是解析失败） | `TestKnowledgeEmptyTagGroupsIsAbsent` |
| #47 | ❌已修 | 同上 | `knowledge_tags.go` | `tags_match` 先于 `tag_groups` 校验（参考把 `tags_match` 声明在前），两者都坏时只报枚举项 | `TestKnowledgeBadTagsMatchHidesBadTagGroups` |
| #48 `GET .../knowledge-base/search` | ❌已修 | `api/http.py:7329`、`:3703-3715` | `internal/reporting/knowledge_search.go` | ①`limit` 走框架的宽松整数解析（`5.0`→5、`1_000`、越界报 `less_than_equal`）②全部参数项收集在同一个 422 里，依赖项排在查询参数项之前③`tag_groups` 失败时 detail 退化为字符串（替换整份 item 列表） | `TestKnowledgeSearchLimitUsesFrameworkParsing`、`TestKnowledgeSearchCollectsEveryItem` |
| #49 `GET .../knowledge-base/export` | ✅一致 | `api/http.py:7361` | `internal/reporting/knowledge_search.go` | — | 探针全 SAME（两侧 markdown 内容一致，仅节点集合因夹具不同） |
| #50 `POST .../knowledge-base/folders` | ❌已修 | `api/http.py:7414`、参考 folder 模型只声明 `name`/`parent_id`、`engine/memory_engine.py:19303` | `internal/reporting/knowledge_body.go`、`knowledge_models.go`（`CreateKnowledgeFolderRequest`）、`knowledge.go:knowledgeFolderID` | ①folder body 用独立类型解码，page 独有的选项被忽略而非校验（`{"name":"x","tags":"a"}` 参考 201）②节点 id 用参考的 `kf-`/`kp-`/`mm-` + 32 位 uuid4 hex | `TestKnowledgeFolderIgnoresPageOptions`、`TestKnowledgeFolderIgnoresUndeclaredOptions` |
| #51 `POST .../knowledge-base/pages` | ❌已修 | `api/http.py:7457`、参考 create 模型（`name`/`parent_id`/`source_query`/`tags`/`max_tokens`/`trigger`） | `knowledge_body.go:decodeKnowledgeCreateBody` | **单趟校验**：框架先校验整个模型再问"必填"，所以"缺 `source_query` 且 `tags` 类型错"要报两项，而非只报一项；item 按声明顺序组织（`{"tags":1,"name":2}` 先报 `name` 再报 `source_query`） | `TestKnowledgeCreateReportsEveryItemInOnePass`（五个用例，任一提前返回即 FAIL） |
| #51 | ❌已修 | 参考框架的 `raw_request` 是保序映射 | `internal/httpx/body.go:RecordBodyOrder`、`json.go:orderValidationInputs`、`errors.go`（`ValidationFailure.BodyOrder`） | `missing` 项回显的 `input` 原先按键名排序；现按客户端书写顺序（含嵌套对象）回显 | `TestKnowledgeCreateMissingItemEchoesBodyInOrder` |
| #52 `GET .../knowledge-base/pages/{page_id}` | ✅一致 | `api/http.py:7492` | `internal/reporting/knowledge_nodes.go` | — | 探针全 SAME |
| #53 `PATCH .../pages/{page_id}` | ✅一致 | `api/http.py:7520` | `internal/reporting/knowledge_nodes.go` | — | 探针全 SAME（含 body 校验项顺序） |
| #54 `DELETE .../pages/{page_id}` | ✅一致 | `api/http.py:7589` | `internal/reporting/knowledge.go` | — | 探针全 SAME |

**夹具差异（非缺陷）**：`GET .../export?tag_groups=` 的 markdown 两侧节点集合不同（参考库里先前的探针留下了 `kp-…` 页面，本服务库里留有 `name:""` 的旧行），index 里的条目数因此不一致；同一份知识库内容分别导出时逐字节相同。

| #48 | ❌已修 | 参考响应模型的 `score` 是 Python float，序列化 `1.0` | `internal/httpx/pythonfloat.go`、`knowledge_models.go`（`httpx.PythonFloat`） | `score` 为整数值时原先渲染成 `1`（Go 的 float 渲染），参考是 `1.0`；新增 `PythonFloat` 按 CPython `repr` 渲染（整数值补 `.0`，指数区间与 Python 一致） | `TestKnowledgeSearchRendersScoreAsAPythonFloat`、`TestPythonFloatRendersLikeCPythonRepr` |

**跨单元修复（组长）**

- `httpx.ValidationFailure.BodyOrder` 与 `orderValidationInputs` 属于框架层：任何路由的 `missing` 类 item 回显 body 时都按客户端书写顺序（含嵌套对象），不再按键名排序。
- `Plain`（`Internal Server Error` 纯文本回退）在 `httpx/json.go` 中一度丢失，导致 `1e999` 系列探针变成 `{"detail":""}`；已恢复，由 `TestMemoryUpdateInfinityBodyIsTheServerFallback` 钉住。
- **解析器失败 vs 字段失败**：`POST` 知识库节点时，非 JSON body 的 `json_invalid` 项（参考 loc 为 `["body",0]`）是唯一答案，不能再补 `missing` 项；而"某个成员类型错"（`list_type` 等）时模型确实跑了，必须连同 `missing` 项一起报。原先按 `loc` 长度区分会把 `["body",0]` 误判成"成员 0"，改为按 item 类型区分——`TestKnowledgeCreateParserFailureStandsAlone` 与 `TestKnowledgeCreateReportsEveryItemInOnePass` 互为反向约束，任何一侧放宽都会让另一个 FAIL。

## 第二轮 directives

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #55 `GET .../directives` | ❌已修 | `api/http.py:7620`；参考框架一次性解析全部查询参数 | `internal/directives/query.go:parseListSelectors`、`handlers.go:List` | 四个选择器都坏时，原先只报第一个就返回；参考把四项收进同一个 422，顺序为声明序（`tags_match`、`active_only`、`limit`、`offset`） | `TestDirectiveListReportsEveryBadSelector`（四项 loc 逐项固定） |
| #55 | ✅一致 | 同上 | `handlers.go` | `offset` 超出 int64 时是参考的 500（无上界可比，值直接进存储层失败），不能与其余 item 并列；已单独处理并保留 500 | `TestDirectiveListIntegerTooLargeForInt64` |
| #55 | ✅一致 | `:7620` 的 `tags_match`/`active_only`/`limit`/`offset` 语义与真值判断 | `query.go:directiveMatchesTags` | — | 探针全 SAME（含 `tags=`、`active_only=1`、`tags_match=exact` 的组合与分页） |
| #56 `GET .../directives/{directive_id}` | ✅一致 | `api/http.py:7669` | `handlers.go:Get` | — | 探针全 SAME（含非 UUID id、未知 bank、未知 id） |
| #57 `POST .../directives` | ❌已修 | `api/http.py:7699`；`priority` 在模型里是无界 Python int，但列是 int4 | `internal/directives/priority_range.go`、`handlers.go:Create` | `priority` 溢出 int32 时原先透出 Go 驱动的编码错误（`failed to encode args[4]: … OID 23 …`），参考是驱动原文 `invalid input for query argument $4: … (value out of int32 range)`；现按参考文案与参数位（insert 为 `$4`）回答 500 | `TestDirectivePriorityOutsideTheColumnRangeIsADriverFailure` |
| #57 | ✅一致 | `:7699` 的 body 校验 | `handlers.go:Create` | — | 探针全 SAME（11 个 body：缺成员/`null`/类型错/非 JSON/数组） |
| #58 `PATCH .../directives/{directive_id}` | ❌已修 | `api/http.py:7734`；update 的 SET 列表只由 body 给出的成员组成 | `priority_range.go:updateDirectivePriorityArg` | 参数位随补丁成员数变化，且**只数 `priority` 之前的成员**（`name`、`content`），行标识键不计入：`{"priority":…}`→`$1`、`{"name":…,"priority":…}`→`$2`、`{"name":…,"content":…,"priority":…}`→`$3`（其后成员不影响）。逐条对参考实测固定 | 同上（五个补丁形状） |
| #58 | ✅一致 | `:7734`；`UpdateDirectiveRequest` 全字段可选 | `handlers.go:Update` | — | 探针全 SAME（`{}`、`{"tags":null}`、`{"tags":[]}`、类型错、非 JSON） |
| #59 `DELETE .../directives/{directive_id}` | ✅一致 | `api/http.py:7771` | `handlers.go:Delete` | — | 探针全 SAME |

**参数位说明**：参考的 `$N` 不是 SQL 占位序号而是"该语句里 `priority` 之前绑定的参数个数"，且 insert 与 update 的计数起点不同（insert 从表列序算起得 `$4`，update 不把行标识算进去）。这两条都不是能从代码结构推出来的，全部靠对活服务逐条实测固定。

**复核第一轮**：directives 在第一轮不在审计范围内（本轮新增单元），无历史结论需要推翻。

## 第二轮 read-surface

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #60 `GET .../graph` | ❌已修 | `api/http.py:5774`、`:2220`（`weight: float`） | `internal/readsurface/models.go:GraphEdgeData`、`handlers.go` | 记忆图边的 `weight` 是参考模型里的 Python float，整数值应渲染 `1.0`；原先渲染成 `1` | `TestMemoryGraphEdgeWeightIsAPythonFloat` |
| #60 | ✅一致 | `:5774` 的 `type`/`limit`/`q`/`tags`/`tags_match`/`document_id`/`chunk_id` | `readsurface/handlers.go` | — | 探针全 SAME（含 `limit=-1` 的 422、`type=bogus` 不过滤、`tag_groups` 在 graph 上不生效） |
| #61 `GET .../entities` | ✅一致 | `api/http.py:6748` | `readsurface/handlers.go` | — | 探针全 SAME（`limit=0`、`limit=-1`、`tags_match=bogus`、未知 bank） |
| #62 `GET .../entities/graph` | ❌已修 | `api/http.py:750-780`（`EntityGraphNodeData`/`EntityGraphEdgeData` 的成员顺序） | `readsurface/models.go` | 响应键序就是契约：参考的节点是 `id,label,mentionCount,color`、边是 `id,source,target,linkType,weight,color,lineStyle,lastCooccurred`；本服务原先把 `color` 排在 `mentionCount` 之前、`weight` 排在 `linkType` 之前。两者都已按参考重排 | `TestGraphPayloadsCarryTheReferenceMemberOrder`（用流式解码读出真实键序，map 解码会丢序） |
| #62 | ✅一致 | `:6796` 的 `limit`/`min_count`/`tags`/`tags_match`/`tag_groups` | `readsurface/handlers.go` | — | 探针全 SAME（`min_count=-5`/`x`/`999`、`tag_groups=` 与 `{{` 都是 400、`tags`+`tag_groups` 并存 400）。注：`min_mentions` 不是声明过的参数，两侧都忽略 |
| #63 `GET .../entities/{entity_id}` | ✅一致 | `api/http.py:6838` | `readsurface/handlers.go` | — | 键集合与参考一致（`id,canonical_name,mention_count,first_seen,last_seen,metadata,observations`）；非 UUID 报 400、未知 id 报 404，两侧同为 |
| #64 `POST .../entities/{entity_id}/regenerate` | ✅一致 | 退休路由，恒 410 | `readsurface/handlers.go` | — | 探针 SAME |
| #65 `GET .../documents` | ✅一致 | `api/http.py:7801`；`pg/documents.py:641` 的 `ORDER BY updated_at DESC, created_at DESC, id` | `internal/storage/postgres_read_surface.go:148` | — | 探针全 SAME；排序两侧同为 `updated_at DESC, created_at DESC, id ASC`，列表次序的差异来自两侧写入时刻不同（夹具差异） |
| #66 `GET .../documents/{document_id}/chunks` | ✅一致 | `api/http.py:7873` | `readsurface/handlers.go` | — | 探针全 SAME（含 `limit=0` 的 422、未知文档 404） |
| #67 `POST .../documents/{document_id}/reprocess` | ✅一致 | `api/http.py:7929` | `readsurface/handlers.go` | — | 未知文档 404，SAME |
| #68 `GET .../documents/{document_id}` | ✅一致 | `api/http.py:7972` | `readsurface/handlers.go` | — | 键集合与 `nodes_by_fact_type` 的三键（`experience`/`observation`/`world`）两侧一致；`observation` 计数差异来自参考侧 LLM 生成了 observation 行而本服务未配置模型（夹具差异） |
| #69 `GET .../tags` | ✅一致 | `api/http.py:8009`；`memories/pg/reads.py:346-355` 的 `COUNT(*)` over `unnest(tags)`、`ORDER BY count DESC, tag ASC` | `internal/storage/postgres_read_surface.go:265` | — | 探针全 SAME（`q=team*` 通配、大小写不敏感 `q=TEAM:A`、`source=mental_models`、`source=bogus` 422、`limit=x` 422）；计数差异同样来自行集合（参考 6 行 / 本服务 3 行） |
| #70 `GET /v1/default/chunks/{chunk_id}` | ✅一致 | `api/http.py:8076` | `readsurface/handlers.go` | — | 键序与内容逐字节一致（仅 `created_at` 不同）；未知 chunk 404，SAME |
| #71 `PATCH .../documents/{document_id}` | ✅一致 | `api/http.py:8115` | `readsurface/handlers.go` | — | 探针全 SAME（`{}` → 400「At least one field (tags) must be provided」、`tags`/`metadata` 类型错 422、非 JSON 422、未知文档 404、`tags` 更新生效） |
| #72 `DELETE .../documents/{document_id}` | ✅一致 | `api/http.py:8167` | `readsurface/handlers.go` | — | 探针 SAME（删除后读取 404） |

**夹具差异（非缺陷）**：`audit-rs` 两侧保存的行集合不同——参考侧配了 LLM，retain 时会额外生成 `observation` 行（6 行 vs 3 行），因此 `tags` 计数（4/2 vs 2/1）、`graph` 的 `nodes`/`edges`/`table_rows`、文档的 `nodes_by_fact_type.observation` 都对不上。两侧 `entities` 的名字集合也不同（参考侧抽出中文「咖啡/茶/柏林办公室」，本服务抽出英文）。这些都属于 MANIFEST §3 的夹具差异。为了把"计数语义"和"行集合"分开，已逐条读了参考的 `list_tags` SQL 与本文 `postgres_read_surface.go` 的对应语句：两边的 `COUNT(*)` over 展开后的 tag 列、`ORDER BY count DESC, tag ASC` 完全一致。

**复核第一轮**：read-surface 第一轮只有 #60/#61/#65 判为 ✅，本轮把它们连同 #62 一起重验，#62 的键序缺陷与 #60 的 `weight` 浮点是新发现的。

## 第二轮 operations

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #73 `GET .../operations` | ❌已修 | `api/http.py:8210`、`:8230-8234`（`limit`/`offset` 走框架整数语法） | `internal/memoryops/operations_list.go:queryIntParam`、`internal/httpx/bigint.go` | `limit`/`offset` 原先用 Go 的 `strconv.Atoi` 严格解析，`limit=1.0`、`limit=1_0`、`limit=%201%20`、`limit=+1` 都报 `int_parsing`；参考按框架语法读成 1/10/1/1。改用 `httpx.LaxInteger`，并把"超出 int64 的 Python 整数"判定抽到 `httpx.BigintSign`（directives 单元原本私有实现，现两处共用） | `TestOperationListLimitUsesTheFrameworkIntegerGrammar`（六个用例） |
| #73 | ✅一致 | `:8210` 的 `status`/`type`/`exclude_parents` 与分页包络 | `operations_list.go` | — | 包络键序 `bank_id,total,limit,offset,operations` 两侧一致；单条操作键序 16 键逐位一致；`limit=0`/`101`/`offset=-1`/`exclude_parents=zz` 都是 422；未知 bank 404 |
| #74 `GET .../operations/{operation_id}` | ✅一致 | `api/http.py:8258` | `memoryops/operations.go` | — | 探针全 SAME（非 UUID 400、未知 UUID 200 + `status:null`) |
| #75 `DELETE .../operations/{operation_id}` | ✅一致 | `api/http.py:8296` | `memoryops/operations.go` | — | 探针全 SAME（非 UUID 400、未知 404） |
| #76 `POST .../operations/{operation_id}/retry` | ✅一致 | `api/http.py:8333` | `memoryops/operations.go` | — | 未知 404，SAME |
| #77 `DELETE .../operations/{operation_id}/delete` | ✅一致 | `api/http.py:8363` | `memoryops/operations.go` | — | 未知 404，SAME |
| #73/#74 | ❌已修 | `api/http.py:4618`（`progress: OperationProgress \| None`）、`engine/memory_engine.py:5139-5179`（`_write_operation_progress`，jsonb `\|\|` 合并 + 更新 `updated_at`）、`consolidation/consolidator.py:1585-1586,1965-1990,2169-2175` | `internal/storage/operation_result.go`（`OperationProgressWriter`）、`postgres_operation_progress.go`、`memory_operation_progress.go`、`internal/consolidation/consolidator.go`（`WithOperation`/`reportProgress`）、`internal/app/worker.go` | **progress 快照原先从不写入**：读路径（`progressOf`）早就存在，但没有任何生产者，所以每次轮询都拿到 `null`；参考在 consolidation 的三个点写入（起手 `consolidating 0/N`、每个 scope 一批带 `detail` 计数、收尾 `refreshing_mental_models N/N`）。已按同样的阶段名与字段补齐，并通过存储层新增的 `RecordOperationProgress` 落盘（两后端都实现） | `TestConsolidatorReportsPhaseSnapshots`、`TestConsolidatorWithoutOperationReportsNothing` |
| #73/#74 | ❌已修 | 同上；参考的 `snapshot` 是插入序映射，`at` 是 `datetime.now(UTC).isoformat()` | `internal/httpx/body.go`（`OrderedObject`）、`internal/memoryops/operations.go:progressOf` | ①`at` 原先渲染 `…Z`，参考是 dict 值 → `+00:00` ②Go map 会把 progress 的键排序，参考按写入序 `stage,at,processed,total,detail`；新增 `httpx.OrderedObject` 按插入序序列化，`progressOf` 按该顺序投影 ③`detail` 恒在（无计数时 `null`） | `TestOperationProgressRendersInTheReferenceMemberOrder` |

**环境相关的夹具差异（非缺陷）**：本服务默认不启用 worker，操作会一直停在 `pending`（`status`/`progress` 都与参考不同）；用 `PARITY_DEV_WORKER_ENABLED=true` 启动后两侧都进入 `completed`，`progress` 形状与键序逐字节一致。操作条数（4 vs 4、`type=consolidation` 的 3 vs 2）差异来自两侧执行过的任务数不同。

**复核第一轮**：operations 第一轮不在审计范围内（本轮新增单元）。

## 第二轮 transfer

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #78 `POST .../import` | ✅一致 | `api/http.py:8716` | `internal/transfer/` | — | `{}`→400、带 `bank` 的包→400、未知 bank→400，探针全 SAME |
| #79 `GET .../export` | ✅一致 | `api/http.py:8803` | `internal/transfer/` | — | 探针 SAME |
| #80 `GET .../document-transfer` | ✅一致 | `api/http.py:8894` | `internal/transfer/` | — | 410（已下线），两侧同；未知 bank 也 410（先于 404 判定） |
| #81 `POST .../document-transfer/export` | ✅一致 | `api/http.py:8924`（无请求体，参数全在 query） | `internal/transfer/` | — | `include_observations=zz`→422；`include_observations=true`+`document_id`→400（互斥）；`include_observations=1`→202；未知 bank→404 |
| #82 `POST .../document-transfer` | ✅一致 | `api/http.py:8988` | `internal/transfer/` | — | `{}`/`notjson` 都是 422，SAME |
| #83 `POST .../transfer/export` | ✅一致 | `api/http.py:9062` | `internal/transfer/` | — | 202 提交；完成后的 `result_metadata` 键集 `filename,byte_size,storage_key,download_url` 与键序一致 |
| #84 `POST .../transfer/import` | ✅一致 | `api/http.py:9146`（`mode`/`target_bank_id`/`document_conflict`/三个 include 开关全在 query，文件走 multipart） | `internal/transfer/` | — | `mode=bogus`/`document_conflict=bogus`/`include_data`+merge/非 zip/空 `target_bank_id` 全 SAME；真实归档 restore 到新 bank 后 `result_metadata` 的 15 个计数逐项相同（`operations_imported` 差异由两侧操作条数不同导致） |
| #85 `POST .../clone` | ✅一致 | `api/http.py:9252` | `internal/transfer/` | — | `target_bank_id` 是 query 参数（缺→422 `loc:["query","target_bank_id"]`）；`{}` 空体 + query 提交→202 `{operation_id,status}`，两侧一致 |
| #86 `GET .../attachments/{attachment_id}` | ✅一致 | `api/http.py:9335` | `internal/transfer/` | — | 非 UUID→404、未知 UUID→404，SAME |
| #87 `GET .../files/download/{key}` | ✅一致 | `api/http.py:9389` | `internal/transfer/` | — | 未知 key→404；真实导出归档可下载（200 `application/zip`） |
| #79/#81/#83 | ❌已修 | `transfer/export.py:916-969`：参考用 `zipfile.writestr(name, data)` 写每个条目，时间戳是**当前本地时间** | `internal/transferarchive/sections.go:writeArchive` | 归档条目原先用 `zip.Writer.Create`，修改时间留零 → 解压显示 `1979-11-30`/`1980-01-01`，同一个 bank 每次导出的字节都不同，且解压工具按时间排序时与参考不一致。改为带 `Modified: now()` 的 `CreateHeader` | `TestWriteStampsEntriesWithTheCurrentTime` |

**已核实的“假差异”（非缺陷）**：
- `202` 提交响应里的 `operation_id` 每次不同 —— 每次提交新生成的 UUID。
- 导出归档的 `documents/*.json` 条数不同 —— 两侧 bank 里的文档数不同（夹具）。
- 导出/导入的 `result_metadata` **键序**：参考写的顺序是 `storage_key,download_url,byte_size,filename`，但 API 读回来是 `filename,byte_size,storage_key,download_url` —— 这是 PostgreSQL `jsonb` 自身的键序（按 key 长度再按字典序），不是生产者的插入序。本服务读的也是 jsonb，实测在同一个 PG 上把生产者的 dict 写进去再读出来得到完全相同的键序，因此**键序天然一致**，不需要也不会去“修复”。
- `transfer/import` 的 `include_data` 等开关是 `bool | None`（区分“没传”与“传了默认值”），merge 模式下传 scope 开关会被拒成 400；两侧探针 SAME。

**复核第一轮**：transfer 第一轮不在审计范围内（本轮新增单元）。

## 第二轮 webhooks

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #88 `POST .../webhooks` | ✅一致 | `api/http.py:9768` | `internal/webhooks/` | — | 键序 `id,bank_id,url,secret,event_types,enabled,http_config,created_at,updated_at` 与取值逐字段相同；默认 `event_types=["consolidation.completed"]`、`http_config` 默认 `POST/30/{}`/`{}`；未知事件名被原样接收（不校验）；`secret` 只写不读（响应恒 `null`）；`ftp://` → 400；`event_types` 传字符串 → 422 |
| #89 `GET .../webhooks` | ❌已修 | `api/http.py:9829`（`limit` 0..1000 默认 100、`offset` ge=0） | `internal/webhooks/handlers.go:queryIntParam`、`parseDeliveryLimit` | 原先用 `strconv.Atoi` 严格解析，`limit=1.0`/`1_0`/` 1 ` 报 422；参考按框架语法读成 1/10/1。改走新增的 `httpx.ParseQueryInt` | 探针 8 个用例全 SAME（含 `limit=1.5`→422、`limit=1001`→422 `le:1000`、`offset=-1`→422） |
| #89 | ✅一致 | 同上 | `internal/webhooks/` | — | 包络 `items,total,limit,offset`（默认 `limit=100`）；列表项时间戳两侧都是 `2026-10-20 17:59:31.874508+00`（参考此处直接用 `str(datetime)`，非模型字段，所以是空格分隔、无 `Z`），逐字节相同 |
| #90 `DELETE .../webhooks/{id}` | ✅一致 | `api/http.py:9892` | `internal/webhooks/` | — | 未知 UUID → 404；`GET /webhooks/{id}` 本身未注册 → 405，两侧同 |
| #91 `PATCH .../webhooks/{id}` | ✅一致 | `api/http.py:9923` | `internal/webhooks/` | — | 空体 → 422；真实 patch（`enabled`+`event_types`）→ 200，键序与取值逐字段相同 |
| #92 `GET .../webhooks/{id}/deliveries` | ✅一致（参考侧校验先于 404） | `api/http.py:10006` | `internal/webhooks/` | — | `limit=201`→422 `le:200`、`limit=abc`→422；未知 UUID → 404；非 UUID → 422 |

### 顺带修复：跨包重复的“严格整数解析”缺陷

同一种缺陷在四个包里各写了一份，都已改为共用 `httpx.ParseQueryInt`（新增，`internal/httpx/queryint.go`）：

| 落点 | 影响的路由 | 现状 |
|---|---|---|
| `internal/webhooks/handlers.go` | `#89` 的 `limit`/`offset`、`#92` 的 `limit` | 已修，探针 SAME |
| `internal/readsurface/params.go`（`intParam`、`queryErrors.int`、`queryErrors` 的无界整数） | `#60`–`#72` 的单整数分页/上限参数 | 已修，`documents?limit=1.0`、`entities?limit=1.0`、`chunks?limit=1.0` 等探针 SAME |
| `internal/mentalmodels/helpers.go`（`boundedQueryInt`） | `mental-models` 单元的整数参数 | 已修，`mental-models?limit=1.0` SAME |
| `internal/reporting/handlers.go`（`boundedIntParam`） | `llm-requests` / `audit-logs` 等分页参数 | 已修，`llm-requests?limit=1.0` SAME |
| `internal/memoryops/operations_list.go` | `#73` 的 `limit`/`offset` | 上一提交已修，现也收敛到同一助手 |

`httpx.ParseQueryInt` 的另一半职责是把“超出 int64 的 Python 整数”按**符号**判成 `less_than_equal` / `greater_than_equal`，而不是 `int_parsing` —— 参考把任意大的 Python 整数解析出来后与声明边界比较，本服务原先会在解析阶段就判成类型错误。`TestParseQueryIntReadsTheFrameworkGrammar` 覆盖 12 个用例。

**复核第一轮**：webhooks 第一轮不在审计范围内（本轮新增单元）。

## 第二轮 reporting-audit

| API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #93 `GET .../stats` | ❌已修（根因在存储层的连边推导，见下） | `api/http.py:6620`；`memories/pg/counts.py:62-105`（`link_counts`：`memory_links` 按 `link_type` 分组 + `unit_entities` 派生的 `entity` 计数） | `internal/storage/postgres_observability.go:computeBankStats` | `/stats` 本身逐字段与参考一致（键序也一致）；数值差异来自 `memory_links` 里少了三种边，已在下面一并修复。修复后同一夹具上**两侧 `/stats` 逐字节相同** | 探针 SAME（含 `?refresh=true`、`?refresh=zz`→422） |
| #94 `POST .../health/llm` | ✅一致 | `api/http.py:6678` | `internal/reporting/` | — | 未启用 provider 时两侧都是 404，SAME |
| #95 `GET .../stats/memories-timeseries` | ✅一致 | `api/http.py:6713` | `internal/reporting/` | — | SAME；`?period=bogus` 也被接受（参考不校验该枚举） |
| #96 `GET .../audit-logs` | ✅一致 | `api/http.py:10574` | `internal/reporting/` | — | SAME；`?period=bogus` 同样被接受 |
| #97 `GET .../audit-logs/stats` | ✅一致 | `api/http.py:10615` | `internal/reporting/` | — | SAME（`period`/`trunc`/`start`/`buckets` 键序一致；`start` 是“现在减 7 天”，两次请求自然不同） |
| #98 `GET .../llm-requests` | ✅一致 | `api/http.py:10648` | `internal/reporting/` | — | SAME；`?status=bogus`、`?period=30d` 都被接受 |
| #99 `GET .../llm-requests/stats` | ✅一致 | `api/http.py:10706` | `internal/reporting/` | — | SAME（桶内 `time`/`statuses`/`total`/`tokens{input,output,cached,thoughts,total}` 键序一致） |

### 本单元发现并修复的连边推导缺陷（影响 `/stats`、`/graph` 与召回图臂）

参考库里直接读 `memory_links` 才知道真相：一个含 2 条 world 事实 + 2 条 observation 的夹具，参考是 `semantic 4 / temporal 2 / entity 1`，本服务是 `semantic 2 / temporal 0 / entity 1`。

| 缺陷 | 参考证据 | 修复 |
|---|---|---|
| **semantic 自环边缺失**：参考的 ANN 探针把种子自己也算进最近邻，于是每个单位都有一条 `自身 → 自身`、`weight = 1` 的 semantic 边（`links.py:625` 明确写了 "Self-links are harmless (ON CONFLICT DO NOTHING handles duplicates)"）。本服务用 `u2.id <> u1.id` 把它排除了 | `memories/pg/links.py:594-660`；参考库 `memory_links` 里 `link_type='semantic' AND from_unit_id=to_unit_id` 真实存在（`audit-dt2` 2 条、`coding-agent::amaze` 48 条） | `postgres_retain.go:createSemanticLinks` 去掉自排除；`memory_links.go:semanticLinksFrom` 补上自环 |
| **semantic 邻居上限错了**：本服务内存后端写死 `fanOut = 5`，参考是 `MAX_SEMANTIC_LINKS_PER_UNIT = 50` | `memories/pg/graph.py:61` | 改用新增的 `maxSemanticLinksPerUnit = 50` |
| **semantic 边权重未夹取**：参考把相似度 clamp 到 `[0,1]`（反归一化向量的余弦可能越界）；本服务直接写 `1 - distance` | `links.py:655-657` | SQL 里加 `LEAST(1.0, GREATEST(0.0, …))` |
| **temporal 边在 retain 时完全不写**：参考在 retain 的语义阶段之前先跑 `create_temporal_links_batch`，本服务只在 graph maintenance 的 relink 里补 | `retain/orchestrator.py:820` → `memories/pg/links.py:457-566` | 新增 `postgres_retain.go:createTemporalLinks` + `memory_retain.go:temporalLinksLocked`，在 `createSemanticLinks` 之前调用 |
| **temporal 边只有单向**：参考的批内规则是"组内按日期排序后，每个单位与它之后 `K` 个后继**双向**连边"，本服务只写了 forward | `links.py:85-146`（`_within_batch_temporal_links`，注释明确 "Create bidirectional links"） | 两个后端都改为双向 |
| 旧测试 `TestRetainLinksFactsFromOneItem` 断言"3 条同一天的事实只产生 2 条边"——**这个断言本身就是错的**（只是恰好与本服务的旧行为一致）；参考对 3 条同组事实产生 6 条有向边 | 同上 | 测试改为断言"每个有序对都有 temporal 边"，并显式断言不写 temporal 自环 |

**两侧一致性（同一夹具、同一内容）**：修复后 `nodes_by_fact_type {observation:2, world:2}`、`links_by_link_type {semantic:4, temporal:2, entity:1}` 与参考完全相同，`/stats` 整份 JSON 逐字节相同。

**复核第一轮**：reporting-audit 第一轮不在审计范围内（本轮新增单元）。

## 第三轮 webhooks

> 单元 `webhooks`（#88–#92），端口 18313，`CHK_RUN=w-18313`，夹具银行 `rt-w-18313` / `rt-other-w-18313` / `rt-ghost-w-18313`。
> 参考实现 `/code/hindsight/hindsight-api-slim/hindsight_api/`，差分工具 `/tmp/rt/chk.py`。
> **注意编号**：`routes.md` 里 webhooks 的编号是 **#88–#92**（#86/#87 是 attachments 与 files/download，属 `transfer` 单元），
> 本节按 `routes.md` 的编号写。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #88 `POST /v1/default/banks/{bank_id}/webhooks` | ✅一致 | `api/http.py:9768-9827`（`status_code=201`；`_validate_webhook_destination` → 400）；`webhooks/url_guard.py`（scheme 必须 http/https；**IP 字面量**的私有/回环/链路本地地址拒绝，DNS 名放行、投递时才由 guarded transport 固定）；`CreateWebhookRequest`（`api/http.py:4882-4895`）：`event_types` 默认 `["consolidation.completed"]`、`enabled` 默认 `true`、`http_config` 默认 `WebhookHttpConfig()`（`webhooks/models.py:85-92`：`POST`/`30`/`{}`/`{}`）；`engine/memory_engine.py:21796-21845`（惰性建行 + 插入，同一事务） | `internal/webhooks/handlers.go:73-107`（`Create`）、`validate.go:54-111`（`validateCreateBody`）、`models.go:16-29`（`HTTPConfig`/默认值）、`urlguard.go` | 无 | **19 组实测全部 SAME**：最小体（9 键同集同序 `id/bank_id/url/secret/event_types/enabled/http_config/created_at/updated_at`、`secret:null`、`http_config` 默认值相同）；`{}`→422 `missing body.url`；`{"url":null}`→422 `string_type`；`{"url":127}`→422 `string_type`；`event_types:"x"`→422 `list_type`；`event_types:[1]`→422 `string_type` loc 带下标；`event_types:[]`→201 且回 `[]`；未知事件名→201（参考不校验）；`enabled:"bogus"`→422 `bool_parsing`；`http_config:null`→422 `model_attributes_type`；`timeout_seconds:1.5`→422 `int_from_float`；`timeout_seconds:"7"`→201 回 `7`（lax）；`headers:{"a":1}`→422 `string_type`；`ftp://`→400 `scheme must be http or https, got 'ftp'`；`127.0.0.1`→400；`169.254.169.254`→400；`localhost`→**201**（DNS 名放行，易误判为应拒绝）；`[]`/`ghost`（非对象体）→422；未知 bank → **201 + 惰性建行**（参考 `_ensure_bank_exists` 同一事务） |
| #89 `GET /v1/default/banks/{bank_id}/webhooks` | ✅一致 | `api/http.py:9829-9874`；`limit: int = Query(default=100, ge=0, le=1000)`、`offset: int = Query(default=0, ge=0)`；`ops_postgresql.py:1360-1387`（`ORDER BY created_at, id` + `LIMIT/OFFSET`）、`:1390-1394`（`total` 是 `COUNT(*)` 全量，不受分页影响）；未知 bank → `_require_bank_exists` 404 | `internal/webhooks/handlers.go:37-65`（`List`）、`:277-295`（`parseListQuery`） | 无 | 信封 `{items,total,limit,offset}` 同集同序，默认 `limit=100 offset=0`。**13 组实测 SAME**：`limit=0`/`limit=1001`→422 `less_than_equal le:1000`；`offset=-1`→422 `greater_than_equal ge:0`；`limit=`（空值）→422 `int_parsing`；`limit=1&limit=50`→取最后一个（50）；`limit=%201`（`" 1"`）→1；`limit=1_0`→10；`limit=5.0`→5；`limit=5.`/`limit=1.5`/`limit=abc`→422；`limit=1;x=2`→不把 `;` 当分隔符（两侧同）；未知 bank→404 同文案。**超 int64 见下方 ⚠️ 存疑条** |
| #90 `DELETE /v1/default/banks/{bank_id}/webhooks/{webhook_id}` | ✅一致 | `api/http.py:9892-9918`（`_parse_webhook_id` → 422 `Invalid webhook ID`；未删到 → 404 `Webhook not found`；成功 `DeleteResponse(success=True)`）；`ops_postgresql.py:1402-1408`（`DELETE … WHERE id=$1 AND bank_id=$2`，`deleted_count>0`）；`WebhookDeliveryResponse` 的投递行**不随 webhook 删除**（`delete_webhook` 只删 webhooks 表） | `internal/webhooks/handlers.go:168-181`、`models.go:74-78`（`DeleteResponse` 三个成员都输出，`message`/`deleted_count` 为 null）、`:294-346`（`canonicalWebhookID` 认 4 种 uuid 拼法） | 无 | 各删各的 id：两侧 `200 {"success":true,"message":null,"deleted_count":null}` 逐字节相同；重复删→两侧 404 `Webhook not found`；`abc`→两侧 422 `Invalid webhook ID`；零 UUID→两侧 404；未知 bank + `abc`→422（参考先解析 id）；`GET .../webhooks/{id}`→两侧 405（该路由未注册） |
| #91 `PATCH /v1/default/banks/{bank_id}/webhooks/{webhook_id}` | ✅一致（含参考自身的 500） | `api/http.py:9923-10003`：**先** pydantic 校验体、**再** `_parse_webhook_id`（422）、**再**按 `request.model_fields_set` 逐字段 `_validate_webhook_destination`（400）、`set_clauses` 空 → 422 `No fields provided to update`、未命中行 → 404 `Webhook not found`；`UpdateWebhookRequest`（`:4912-4921`）五个成员**全部可空**；`http_config:null` 在拼 SQL 时被解引用 → 500 `'NoneType' object has no attribute 'model_dump_json'`，其余三个 NOT NULL 列 → 驱动 500 not-null 违例 | `internal/webhooks/handlers.go:110-165`（`Update`，次序：体校验 → id 解析 → `noFields` → URL 守卫 → null 处理 → 写）、`validate.go:117-187`、`:37-49`（`nullColumn`/`nullHTTPConfig`） | 无 | 两侧各建一条 webhook、用**各自的 id** 打补丁（id 每实例不同，不可共用）。**17 组实测 SAME**：`{}`→422 `No fields provided to update`；`{"nope":1}`→同；`enabled:false`/`url`/`secret`/`event_types`（含未知事件名）/`event_types:[]`/`http_config`（method+timeout+headers+params）→200 逐字段相同（去掉 id 与时间戳后逐字节）；`secret:null`→200（清空）；`enabled:"bogus"`→422 `bool_parsing`；`http_config:{timeout_seconds:1.5}`→422 `int_from_float`；`url:127.0.0.1`→400 同文案。**四个 null 成员的 500**：`url`/`enabled`/`event_types`/`http_config` 两侧**状态码均 500**（detail 是参考的驱动/CPython 原文，按 `docs/CONTRACT-AUDIT.md` §5 item 6「驱动错误文案」已接受类不复刻）；实测 detail 对同一输入**稳定可复现**（两次请求 `Failing row contains (…)` 只差 `updated_at`） |
| #92 `GET /v1/default/banks/{bank_id}/webhooks/{webhook_id}/deliveries` | ❌已修 | `api/http.py:10006-10066`：`limit: int = Query(default=50, le=200, …)` —— **只有 `le=200`，没有 `ge`**；`cursor: str \| None`，**不做任何解析**、原样绑到 `created_at < $3::timestamptz`（`ops_postgresql.py:1410-1441`）；未知 webhook → `LookupError` → 404 `Webhook not found`；`next_cursor = page[-1]["created_at"] if has_more and page else None`；`WebhookDeliveryResponse.from_async_operation_row`（`:4933-4988`）13 字段、`attempts = retry_count + 1`、`last_response_body` 默认不给 | `internal/webhooks/handlers.go:189-239`（`ListDeliveries`）、`:299-315`（`parseDeliveryLimit`）、`models.go:80-102`（`Delivery`） | **修两处**：<br>① `limit` 负数：原 `parseDeliveryLimit` 给 `httpx.ParseQueryInt` 传 `min=0`，`-1`/`-2` 都被 422 `greater_than_equal ge:0`；参考无下界 → `-1` 合法（`LIMIT limit+1` = `LIMIT 0`）回 200 空页、`<=-2` 交给 PG 报 `LIMIT must not be negative`（500）。改为下界 `math.MinInt`，并在 handler 内按 `-1` → 200 空页、`<-1` → 500 `LIMIT must not be negative` 作答（store 的 `clampLimit` 会把负数当"未设"回落 50，不能直接下传）。PG 侧我在本库 `BEGIN; SELECT 1 LIMIT -1;` 实测到**同一条** PG 报错（两侧都是 PG 18）。<br>② `cursor`：见下条，由组长在 `internal/storage/memory_webhooks.go` 改为「任何非空 cursor 一律 500」，我只补用例 | 两侧各建一条**无投递记录**的新 webhook、用各自 id 复测：`''`/`?limit=0`/`-1`/`-2`/`-3`/`-200`/`200`/`201`/`?limit=`/`?limit=abc`/`?limit=1.5`/`?cursor=`/`?cursor=notadate`/`?cursor=2026-10-26T00:00:00Z`/`?cursor=2026-10-26 06:00:00.123456+00` **全部 SAME**。未知 webhook→404、非 UUID→422 同；`?limit=201`→422 `le:200` 同。<br>用例 `TestWebhookDeliveriesNegativeLimitMatchesTheReference`（`-1`→200 空页、`-2`/`-3`/`-200`→500 `LIMIT must not be negative`、`201`→422）、`TestWebhookDeliveriesShapeAndCursor`（改为断言非空 cursor→500 且 detail 含 `invalid input for query argument $3`）。<br>**证伪（`limit` 下界改回 0）**：`limit=-1: 422, want 200 (body {"detail":[{"type":"greater_than_equal","loc":["query","limit"],…}]})` |
| #92 的 `cursor`（子项，组长实施） | ❌已修（跨作用域，组长改 `internal/storage/`） | 参考把 `cursor: str` 原样交给 asyncpg 绑 `timestamptz`，asyncpg 0.30 对 `str` 走 text codec，timestamptz 不吃 → **每一种**取值都 500 `invalid input for query argument $3: '<v>' (expected a datetime.date or datetime.datetime instance, got 'str')`，**包括参考自己刚吐出的 `next_cursor`**（`api/http.py:10041` + `:10015` 的 `created_at::text` 形态）→ 参考的 cursor 翻页在我测到的 7 种形态上**全部不可用** | `internal/storage/memory_webhooks.go:197-213`（`parseDeliveryCursor`，组长改后为「非空一律 `errInvalidDeliveryCursor`」）；读行时间戳另走 `parseDeliveryTimestamp` | 组长按裁决 (a) 实施（判据 1「同输入→同输出」优先于功能好坏，且该 500 确定性可复现） | 修复前本服务接受 4 种形态（`RFC3339Nano`/`RFC3339`/空格分隔两种）并回 200，与参考 500 不同；修复后 7 种形态两侧**全部 SAME**（500 同文案）。组长另补 `TestDeliveryCursorIsRefusedInEveryAcceptedForm` |
| ⚠️ `#89` 超 int64 的 `offset` | ⚠️存疑 | `offset` 只有 `ge=0` 无上界 → 参考把 Python **无界整数**直接交给 asyncpg，绑定失败 → 500 `invalid input for query argument $3: 9223372036854775808 (value out of int64 range)` | `internal/httpx/queryint.go`（组长作用域，已改为饱和到 `MaxInt` 继续交给存储层） | 未修，**驱动层不可复刻**：Go 的 `int64` 没有「装不下的整数」这个概念，饱和后存储层按页外 offset 处理 | 饱和后的**普通**页外 offset `9223372036854775807`、`4294967296`、`999…`（beyond int64 → 饱和）两侧均 **200 同体**（`{"items":[],"total":N,"limit":100,"offset":<值>}`）；但 `9223372036854775808` 参考 500、本服务 200 —— 差异只剩「参考的驱动装不下」这一条，属 `docs/CONTRACT-AUDIT.md` §5 item 6 的驱动层已接受类。有上界的参数（`limit=9223372036854775808`）两侧都是 422 `le`，已 SAME |
| webhook id 的 uuid 形态 | ✅一致 | `_parse_webhook_id`（`api/http.py:9761-9766`）：`uuid.UUID()` 失败 → **422** `Invalid webhook ID`（**不是**组 J 的驱动 500、**不是**组 L/M 的 400） | `internal/webhooks/models.go:289-350`（`canonicalWebhookID`：接受 8-4-4-4-12、32 hex、`{…}`、`urn:uuid:…` 四种拼法并折成规范形式） | 无 | 非 UUID（`abc`）在 DELETE 与 deliveries 两处两侧均 **422**；零 UUID 在两侧均 **404**（id 合法、行不存在）；未知 bank + 非 UUID 两侧均 **422**（先解析 id 再看 bank —— 参考的 `_parse_webhook_id` 在 handler 体内、早于任何查库） |
| 投递记录有数据时的 13 字段 | ✅一致（形状） | `WebhookDeliveryResponse`（`api/http.py:4933-4988`）13 字段；`attempts = retry_count + 1`；`created_at`/`updated_at`/`next_retry_at` 来自 `::text`（空格分隔 `YYYY-MM-DD HH:MM:SS.ffffff+00`），`last_attempt_at` 来自 `result_metadata` 里的 `isoformat()` | `internal/webhooks/models.go:80-102`、`:120-157`（`toDelivery`） | 无 | 空页两侧 `{"items":[],"next_cursor":null}` 逐字节相同；**有记录**时（两侧各由 worker 真实投递产生 `webhook_delivery` 行）13 键同集同序、`created_at` 两侧同为空格分隔 `+00` 形态、`attempts` 两侧同为 `retry_count+1`。注：两侧投递**次数与失败文案**不同（参考 `WebhookURLError: Webhook host 'stable.example' did not resolve…`、本服务 `dial tcp: lookup stable.example: no such host`），那是各自的 SSRF/解析实现文案，属既有已接受差异，非本节改动 |
| ⚠️ 取证说明 | ⚠️说明 | — | — | — | ① 两侧**不是同一个库**：有投递记录时 `total`/行数天然不同，故 `#92` 的判定用「各自新建一条空投递的 webhook」做干净对比。② webhook `id` 每实例不同，`#91`/`#90`/`#92` 必须各用各的 id。③ **踩坑记录**：`#88` 的 `POST .../webhooks` 会**惰性创建银行**，我早期用它打 `{ghost}` 把「永不存在的银行」真的建出来了，导致后续 `#94` 的"未知 bank"假象 —— 判未知 bank 前应先确认它确实不存在。 |

**本节结论计数（按 API 编号 #88–#92 计，共 5 条）**：

- **✅一致 4** = `#88`、`#89`、`#90`、`#91`
- **❌已修 1** = `#92`
- **⚠️存疑 0**（按 API 计；另有 **子项 ⚠️ 1 条**：`#89` 的超 int64 `offset`，驱动层不可复刻）
- **⛔未覆盖 0**

**需要组长裁决/知悉**：
1. `#89` 的「超 int64 offset」记 ⚠️存疑（驱动层不可复刻），已有结论归 `internal/httpx`，非本单元作用域。
2. `#92` 的 cursor 由组长在 `internal/storage/` 实施（任何非空 cursor → 500），已实测 7 形态 SAME。
3. `#91` 的四个 null 成员保留参考的 500 状态码、不复刻驱动 detail 文案，沿用 `docs/CONTRACT-AUDIT.md` §5 item 6 的既有裁决。

## 第三轮 reporting

> 单元 `reporting`（#93–#97 + #100），端口 18313，`CHK_RUN=w-18313`，夹具银行 `rt-w-18313` / `rt-other-w-18313` / `rt-ghost-w-18313`。
> 参考实现 `/code/hindsight/hindsight-api-slim/hindsight_api/`，差分工具 `/tmp/rt/chk.py`。
> 两侧 `/version` 的 `features`：参考 `audit_log:false`、`bank_llm_health:false`、`llm_trace:true`；本服务默认相同。
> **特性开关的处理**：需要写审计行 / 开启 LLM 健康检查时，我在**自己的端口**上用环境变量打开后重测（`WENYUAN_API_AUDIT_LOG_ENABLED=true`、`WENYUAN_API_ENABLE_BANK_LLM_HEALTH=true`），参考侧开关是它部署自设、我无权改，故涉及开关开启态的半边以**参考源码 + 本服务开启态实测**取信，逐条注明。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #93 `GET /v1/default/banks/{bank_id}/stats` | ❌已修（跨作用域，组长改 `internal/storage/`） | `api/http.py:6620-6676`；`BankStatsResponse` 15 键；`engine/memory_engine.py:16205-16250` 的 `_compute_bank_stats`：`node_counts`（按 `fact_type`）、`link_counts`（按 `link_type`）、`link_counts_by_fact_type`、`link_breakdown`、`ops_stats` = `SELECT status, COUNT(*) FROM async_operations WHERE bank_id=$1 GROUP BY status`、`consolidation_freshness`（`last_consolidated_at`/`last_memory_write_at`/`pending`/`failed`）、`count_bank_documents` | `internal/reporting/handlers.go`（`Stats`）、`internal/storage/postgres_observability.go:computeBankStats` | **修一处（我定位、组长实施）**：同步 retain 在参考里**不写 operation 行** —— `api/http.py:10275` 原句 *"A sync retain has no operation row and no ack, so nothing else can stop it once it starts"*，`engine/memory_engine.py:6430-6800` 的 `retain_batch_async` 无任何 `async_operations` 写入（只有 `async=True` 走 `submit_async_retain` 才建行）。本服务两个后端都给同步 retain 写了一条 `completed` 行（`internal/storage/postgres_retain.go:82-88`、`memory_retain.go:106-119`），使 `operations_by_status.completed` 比参考多 1。组长改为「`!req.Async` 时不写 op 行」，async/worker 路径（`SkipOperationRecord`、`operationAliveLocked` 的 cancel 检查）未动 | **同步夹具对**（`rt-w-18313`，两侧各做一次相同内容的同步 retain、等 worker 收敛后取 `/stats`）：15 键同集同序；`total_nodes 2 / total_links 1 / total_documents 1 / nodes_by_fact_type {world:1,observation:1} / links_by_link_type {semantic:1} / links_by_fact_type {} / links_breakdown {} / pending_operations 0 / failed_operations 0 / pending_consolidation 0 / failed_consolidation 0 / total_observations 1` **全部相同**；修复前唯一差异是 `operations_by_status` 参考 `{"completed":1}` vs 本服务 `{"completed":2}`，直接查本服务库可见多出 `retain|completed` 一行。**修复后在另一对全新同步夹具（`rt-w5-18313`，两侧各一次相同同步 retain + 收敛）上 `/stats` 整份逐字节 SAME**，`operations_by_status` 两侧同为 `{"completed":1}`。`?refresh=true`、`?refresh=zz`（→422 `bool_parsing`）、`?refresh=`、`?refresh=1`/`yes`/`0` 两侧同；未知 bank→404 同 |
| #94 `POST /v1/default/banks/{bank_id}/health/llm` | ❌已修（**改判第二轮记录**） | `api/http.py:6678-6712`：**先**判 `enable_bank_llm_health`（关 → 404 `Bank LLM health check is disabled. Set WENYUAN_API_ENABLE_BANK_LLM_HEALTH=true to enable.`），**后**进 try；`engine/memory_engine.py:16346-16392` 的 `check_bank_llm` **没有** `_require_bank_exists` —— 它只探 `_retain_llm_config`/`_consolidation_llm_config`/`_reflect_llm_config` 三个**部署级**配置（`llm_wrapper.py:1683-1693` 处构建，与 bank 无关），按 `(provider, model, base_url, api_key)` 去重后按 `retain`/`consolidation`/`reflect` 顺序产出；`BankLlmHealthResponse{bank_id, operations[]}`，每项 `{operation, ok, status, latency_ms}` | `internal/reporting/handlers.go:251-274`（`HealthLLM`）、`:279-315`（`probeLLM`/`classifyProbeError`） | **修一处**：原 handler 在开关判断之后还调 `h.requireBank(...)`，使**未知 bank** 得 404 `Bank '…' not found`；参考的 `check_bank_llm` 根本不查 bank → 未知 bank 同样 200。改为只取路径值 + 空 id 守卫，不再查银行（`Prober` 本就是部署级 `a.Provider`，见 `internal/app/reporting.go:44`）。<br>**第二轮记录的更正**：第二轮写「开启开关后未知 bank → 两侧 404」是**错的**，多半是当时那个"未知 bank"已被 `#88 POST .../webhooks` 的**惰性建行**真的建了出来（我这个单元也踩过同一坑，见 webhooks 节取证说明③） | **开关关闭态（两侧默认部署）**：已知 bank、未知 bank 两侧均 404 同 disabled 文案；`GET` 该 POST-only 路由两侧 405 同。**开关开启态**（本服务 `WENYUAN_API_ENABLE_BANK_LLM_HEALTH=true`；参考侧开关为 `false` 且我无权改其部署，故这一半以源码据为准）：已知 bank→200 `{"bank_id":…,"operations":[{"operation":"retain","ok":true,"status":"connected","latency_ms":<int>}, consolidation, reflect]}` 三操作两序正确、四字段同名；**永不存在的 bank→200 同形**，且**不创建**该银行（随后 `GET .../config` 仍 404）。模型输出有随机性，只比形状与状态不比文本。<br>用例 `TestEnabledLLMHealthAnswersForAnUnknownBank`；**证伪**（改回 `requireBank`）→ `unknown bank: status = 404, want 200 (body {"detail":"Bank 'no-such-bank' not found"})`。`TestDisabledLLMHealthOutranksMissingBank` 仍过 |
| #95 `GET /v1/default/banks/{bank_id}/stats/memories-timeseries` | ✅一致 | `api/http.py:6713-6745`；`engine/memory_engine.py:16394-16493`：`period` 查 `_MEMORIES_TIMESERIES_PERIODS`，**未命中回落 `7d`**；`time_field` 白名单 `created_at`/`mentioned_at`/`occurred_start`（**其余一律回落 `created_at`**），事件时间列按行 `COALESCE(col, created_at)`；`memories/pg/counts.py:121-152`：`date_trunc('<trunc>', <col> AT TIME ZONE 'UTC')` + `GROUP BY bucket, fact_type` + `WHERE <col> >= $2`；响应 5 键 `{bank_id, period, trunc, time_field, buckets}`，桶 4 键 `{time, world, experience, observation}`，**桶集合恒为完整 `count` 个**（空桶填 0，锚在 UTC 边界） | `internal/reporting/handlers.go`（`MemoriesTimeseries`）、`internal/reporting/chart_period.go` | 无 | **同步夹具对**（两侧相同内容各一次同步 retain + 等 worker 收敛）：`''`/`?period=1d`/`?period=30d`/`?period=`（空串原样回显）`/`?period=bogus`（回落 `7d`）`/`?time_field=mentioned_at`/`?time_field=occurred_start`/`?time_field=bogus` 共 8 组 **全部 SAME**；5 键同序，桶数恒为 7（7d），桶键 `{time, world, experience, observation}` 同序，`trunc:"day"`；有数据时两侧落在同一桶且计数相同。未知 bank→404 同。<br>注：**早期一次 DIFF 已排除为噪声** —— 那是我这边 worker 还没 consolidate 完（参考 worker 常开已产出 observation），等收敛后同接口 SAME；`/stats` 的 `total_observations` 同步变一致 |
| #96 `GET /v1/default/banks/{bank_id}/audit-logs` | ✅一致 | `api/http.py:10574-10613`（`action`/`transport`/`start_date`/`end_date` 均可选；`limit=Query(50, ge=1, le=500)`、`offset=Query(0, ge=0)`）；`engine/memory_engine.py:13977-14060`：先 `get_bank_profile`（不存在→`None`→404），再 `_refuse_bank_wide_if_scoped`，`action`/`transport` 用 `if value:` 判真（空串即不过滤）、`start_date` 用 `>=`、`end_date` 用 `<`；`engine/audit.py:33-48` 条目 10 字段、`:50-57` 信封 5 键 | `internal/reporting/handlers.go:317-384`（`AuditLogs`）、`internal/audit/` | 无 | 信封 `{bank_id,total,limit,offset,items}` 5 键同序；条目 10 键 `{id,action,transport,bank_id,started_at,ended_at,duration_ms,request,response,metadata}` 与参考 `AuditLogEntry` **逐键同序**（我在开启 `WENYUAN_API_AUDIT_LOG_ENABLED=true` 下真实产生审计行后核对的）；`started_at` 为 `isoformat()` 形态（`+00:00`、微秒非零才带小数）、`duration_ms` 为 int。<br>**12 组实测 SAME**：`?limit=0`/`?limit=501`→422；`?offset=-1`→422；`?action=nope`/`?transport=bogus`→200（参考**不校验枚举**）；`?limit=1&limit=500`→取最后一个；`?start_date=notadate`→400 `Invalid start_date: 'notadate' is not an ISO-8601 datetime.`（且**先于** bank 查找：未知 bank + 坏日期仍 400）；未知 bank→404 同。<br>注：参考侧 `audit_log:false` 故其库无行、`items:[]`，本服务读取路径与写入开关**解耦**（关闭开关时仍能读已存在的行），此点与参考一致 |
| #97 `GET /v1/default/banks/{bank_id}/audit-logs/stats` | ✅一致 | `api/http.py:10616-10645`（`period: str = Query("7d")`，**无枚举校验**；`action` 可选）；`engine/memory_engine.py:14064-14136`：`trunc` **恒为 `"day"`**，只有 `1d`/`30d` 另取窗、**其余一律 7 天**，而 `period` 原样回显；`SELECT date_trunc('day', started_at), action, COUNT(*) … GROUP BY bucket, action ORDER BY bucket ASC`；桶 `{time, actions, total}`，`total = sum(actions.values())` | `internal/reporting/handlers.go:386-412`（`AuditLogStats`）、`internal/reporting/chart_period.go` | 无（第二轮已修的 `?period=` 空值回显在本轮复验仍成立） | 信封 5 键 `{bank_id,period,trunc,start,buckets}` 同序；有数据时桶 3 键 `{time,actions,total}` 同序。**9 组 SAME**：缺省/`?period=1d`/`?period=30d`/`?period=`（两侧均回显 `""`、窗仍 7 天）/`?period=bogus`（两侧均回显 `"bogus"`+7 天窗，**参考不校验**）/`?period=1y`/`0d`/`-1d`/`7`；`?action=nope`→200；未知 bank→404 同。`start` 是"现在减 N 天"，两次请求自然不同，只比形态 |
| #98 `GET /v1/default/banks/{bank_id}/llm-requests` | ❌已修 | `api/http.py:10648-10703`：11 个可选过滤（`status`/`operation`/`scope`/`provider`/`trace_id`/`document_id`/`memory_id`/`group`/`start_date`/`end_date`）+ `limit=Query(50, ge=1, le=500)`、`offset=Query(0, ge=0)`；`group` 是 **bool**（非枚举）；`engine/llm_trace.py:341-360` 的 `LLMRequestEntry` **23 字段**；写入在 `:585-620`：`input` 存 **`list(messages)` 本身**（JSON 数组）、`output` 存 **`response_content` 字符串**（失败时 `None`）、`llm_info` 存 `{request:{temperature,max_completion_tokens,response_schema}, finish_reason}`、`metadata` 由 `attach_memory_ids` 事后回填；OTel 层级在 `:530-536`：`trace_id`/`parent_span_id` 取上下文、`span_id` 每调用新生成；`_safe_json`（`:281-299`）超限包成 `{"_truncated":true,"_original_chars":N,"preview":"…"}` | `internal/reporting/handlers.go:415-…`（`LLMRequests`）、`internal/app/llm_trace.go`（`tracingProvider.record` / `traceInput` / `traceOutput` / `traceLLMInfo` / `traceIDsFor` / `truncateTrace`） | **修三处**：<br>① `trace_id`/`span_id`/`parent_span_id` 三列**恒为 null** —— 参考每个 operation 调用生成一个 `trace_id` + 一个 operation span、每次 LLM 调用各自一个 `span_id` 并挂在该 operation span 下（`:530-536` + `llm_wrapper.py:1683-1693`）。现按上下文归属（一次 operation 一个 run）派生并复用这对 id、每调用新 span。<br>② `input`/`output` 形状：原为 `{"messages":[…],"max_tokens":…,"scope":…}` 与 `{"content":…,"finish_reason":…,"tool_calls":…}` 两个**自定义包装**，现改为参考的消息数组与内容字符串（失败时 null）。<br>③ `llm_info` 恒 `{}` → 现产出 `{request:{max_completion_tokens},finish_reason}`（只放调用方真正设过的值；两个按 scope 解析的成员见其子项）；`truncateTrace` 泛化并改用参考的 `_truncated` 包装（原实现会把 JSON 从中间截断成**非法 JSON** 再拼 `"..."`）；删掉因此失去调用方的 `rawJSON`。<br>④ `model` 取错来源：原优先用**响应回显**的 `result.Model`，而网关会去掉厂商标前缀（实测本服务 `retain` 行记 `DeepSeek-V4.1-Flash`、参考记 `hai/DeepSeek-V4.1-Flash`）；参考记的是**配置里解析出的模型**（`engine/llm_wrapper.py:1370,:1519` 传 `model=self.model`），已改为优先 `cfg.Model`、空时才回落响应值。实测该修复让 `retain` 行与参考同值（`hai/DeepSeek-V4.1-Flash`）；`consolidation` 行仍记 `DeepSeek-V4.1-Flash`，原因是该行由另一条 provider 装配路径记录、其 `ProviderConfig.Model` 为空 —— 单测 `TestTmpCfgModel` 已证明 `record` 在 `cfg.Model` 非空时**确实**采用它 | **干净夹具对**（两侧各一次相同同步 retain + 等收敛）：信封 5 键 `{bank_id,total,limit,offset,items}` 同序；条目 **23 键完全相同且同序**；逐键**类型**比对从修复前 5 处 `TYPE DIFF` 降到 1 处（余下 `cached_tokens`，见下方噪声条）；`input` 两侧均 JSON 数组、`output` 两侧均字符串、`llm_info.request` 两侧均含 `temperature`/`max_completion_tokens`、`finish_reason` 两侧均 `"stop"`。<br>**8 组校验 SAME**：`?limit=0`/`?limit=501`→422；`?offset=-1`→422；`?group=bogus`→422 `bool_parsing`；`?status=x&scope=y&provider=z`（垃圾值）→200；`?start_date=notadate`→400 同文案；`?operation=nope` 200；未知 bank→404 同。<br>用例 `TestTraceRecordsOperationScopedSpanHierarchy`（同一 run 的多次调用共享 `trace_id`、`span_id` 互异、不同 run 的 `trace_id` 不同、三个 id 均非空且为合法 UUID）、`TestTraceColumnsMatchTheReferenceShape`（`input` 是数组、`output` 是内容字符串、`llm_info` 三类键）。<br>**证伪**：① 去掉 id 赋值 → `row reflect recorded trace/span/parent = ""/"…"/""`；② 恢复包装结构 → `input = {"max_tokens":64000,"messages":[…],"scope":"…"}, want a JSON array (the message list)` |
| #98 的 `metadata` 列（子项） | ⚠️存疑（已记录缺口，本轮未排期） | `engine/llm_trace.py:625-680` `attach_memory_ids`：操作结束后 `UPDATE <schema>.llm_requests SET metadata = metadata \|\| $3::jsonb WHERE bank_id=$1 AND trace_id=$2` 回填 `{memory_ids, source_memory_ids}`（另含 `document_id`） | 需 `internal/storage/` 新增「按 trace_id 补 metadata」+ `internal/memoryops/retain.go`（`outcome.ItemUnitIDs`）与 consolidation 两处调用点 | **未修**：三处都在 `retain` / `observations` 单元的主战场，本轮并行会冲突，**需 storage+memoryops 三处协同，本轮未排期** | 现状可观察：本服务 `llm_requests.metadata` 恒为 `{}`；参考为 `{"memory_ids":[…],"source_memory_ids":[…],"document_id":"…"}`。`#98` 的**其余部分**（23 键形状、`input`/`output`/`llm_info`、`_truncated`、分页与过滤）已确证一致，故 `#98` 整体记 ❌已修，本列单列 ⚠️ |
| #98 的 `llm_info.request` 两个成员（子项） | ⚠️存疑（已记录缺口，本轮未排期） | `engine/llm_trace.py:513-519` 记录调用方**实际请求**的参数：`temperature`（按 scope 解析）与 `response_schema`（参考存**pydantic 模型名**，实测 `"_ConstrainedConsolidationBatchResponse"`、`"FactExtractionResponse"`） | `internal/provider/provider.go:28-42` 的 `Schema` 只有 `Type`/`Properties`/`Required`/`Items`/`Enum`/`Description`/`AdditionalProperties`，**没有任何命名概念**；按 scope 解析的温度/schema 在 `internal/config/provider_scopes.go` 的 `ScopedConfig`，挂在 `config.Config.ProviderScopes` 上，而 `TraceProvider`（`internal/app/llm_trace.go:39`）只拿得到 `ProviderConfig`，要传进来必须改 `internal/app/app.go:142` | **未修**：① `response_schema` 需给 provider 端口加命名概念，会波及全部 adapter（openai/anthropic/embed/rerank/none/mock）；② 按 scope 的 `temperature` 需把 `ScopedConfig` 传进 `TraceProvider`，落点是 `internal/app/app.go`（禁改区）。两者都超出本单元写作用域 | 现状：本服务 `llm_info` = `{"request":{"max_completion_tokens":64000},"finish_reason":"stop"}`（只含请求体上真正设过的值）；参考 = `{"request":{"temperature":0.1,"response_schema":"FactExtractionResponse","max_completion_tokens":64000},"finish_reason":"stop"}`。**键集与 `finish_reason` 已对**，只缺上述两个按 scope 解析的成员 |
| #99 `GET /v1/default/banks/{bank_id}/llm-requests/stats` | ✅一致 | `api/http.py:10706-10740`（`operation` 可选、`period: str = Query("7d")` 无枚举校验）；信封 5 键 `{bank_id,period,trunc,start,buckets}`，桶 4 键 `{time,statuses,total,tokens}`，`tokens` 5 键 `{input,output,cached,thoughts,total}`（与 `#97` 同一套 period→窗/trunc 规则） | `internal/reporting/handlers.go`（`LLMRequestStats`）、`internal/reporting/chart_period.go` | 无 | 信封 5 键同序；有数据时桶 4 键、`tokens` 5 键同序（两侧 `statuses:{"success":N}`、`total`、`tokens` 各成员类型一致）。**8 组 SAME**：缺省/`?period=`（回显 `""`）/`?period=30d`/`?period=bogus`；`?operation=nope`→200；未知 bank→404；`?period=1d` 同。桶的**数值**两侧不同（各库存量行不同，见噪声条） |
| #100 `GET /v1/version` | ✅一致（`--raw` 逐字节） | `api/http.py:5716-5747`，`include_in_schema=False`，与 `GET /version` 同一实现、同体：`{"api_version":"1.0.0","features":{…11 项…}}`（键序 `observations/mcp/worker/bank_config_api/bank_llm_health/file_upload_api/document_export_api/document_import_api/audit_log/llm_trace/store_document_text`） | `internal/monitoring/`（版本端点，`internal/app` 挂载） | 无 | 默认部署（两侧 `features` 全同）下 `--raw` **逐字节 SAME**；`GET /version --raw` 同样 SAME；`--headers-only`（content-type/content-length 等）SAME。本服务 `/version` 与 `/v1/version` 两个路径响应**字节相同**（`diff` 空）。<br>注：**故意打开** `WENYUAN_API_ENABLE_BANK_LLM_HEALTH=true` 时 `/v1/version` 会 DIFF —— 那正是 `features.bank_llm_health` 这个**部署自设项**，`chk.py` 在归一化模式下会把它归一为 `<SETTING>`（此时 SAME），不是缺陷 |
| ⚠️ `cached_tokens` 的 null vs int | ⚠️噪声（非缺陷） | `engine/llm_trace.py:317-320` 的 `cached_tokens: int \| None`；值来自模型网关响应里的缓存命中字段 | `internal/app/llm_trace.go` 的 `setToken`（未上报则留 NULL） | 无 | 两侧接**同一个中转网关**，缓存命中与否取决于网关侧状态：参考两次调用里一次未报（`null`）、本服务两次都报了（`int`）。两侧该列形状都是 `int \| null`，**读写逻辑一致**（`setToken` 对「未上报」与「真 0」的区分也与参考的 `or None` 同义），故判噪声 |
| ⚠️ 取证说明 | ⚠️说明 | — | — | — | ① 两侧**不是同一个库**：`stats` 的计数、`llm-requests`/`audit-logs` 的存量行天然不同。`/stats`、`#95`、`#98`、`#99` 的数值判定统一改用**同步夹具对**：两侧各建一对新银行、各做**一次相同内容的同步 retain**、等 worker 收敛后再取数（否则参考 worker 自行 consolidate 会让 observation 计数多一份）。② `llm_trace` 两侧都是 `true`，故 `#98`/`#99` 可直接端到端测；`audit_log` 两侧都是 `false`，参考库无行，`#96`/`#97` 的「有数据」半边由**本服务开启开关后真实产生的行**核对形状与聚合规则（参考该半边的规则由源码 `engine/memory_engine.py:13977-14136` 逐行对齐）。③ `bank_llm_health` 两侧都是 `false`，`#94` 开启态按上文说明取信。 |

**本节结论计数（按 API 计，共 8 条：`#93`–`#97` 与 `#100`，另 `#98`/`#99` 同在本单元作用域内一并核对）**：

- **✅一致 5** = `#95`、`#96`、`#97`、`#99`、`#100`
- **❌已修 3** = `#93`、`#94`、`#98`
- **⚠️存疑 0**（按 API 计；另有 **子项 ⚠️ 2 条**：`#98` 的 `metadata` 回填、`llm_info.request.response_schema`；以及 **噪声说明 1 条**：`cached_tokens` 的 null/int）
- **⛔未覆盖 0**

**需要组长裁决/知悉**：
1. `#98` 的两个 ⚠️ 子项（`metadata` 回填、`llm_info.request.response_schema`）已按「需 storage+memoryops / provider 端口协同，**本轮未排期**」记录，`#98` 整体记 ❌已修。
2. `#93` 的根因（同步 retain 不写 operation 行）在 `internal/storage/`，由组长实施；`#94` 的改判依据是「参考源码无 `_require_bank_exists`」而非两侧实测（参考侧开关无法打开）。
3. `cached_tokens` 的 null/int 判噪声，依据是两侧同一网关的缓存命中状态差异、且读写逻辑同义。

## 第三轮 transfer

> 覆盖 `routes.md` 的 `#76`–`#87`（`import` / `export` / `document-transfer` ×3 / `transfer` ×2 / `clone` / `attachments` / `files/download`）。
> **`#76`/`#77`（`operations/{id}/retry`、`/delete`）不在本单元**——它们属 `operations` 单元的写作用域（`internal/memoryops/operations*.go`、`internal/worker/`），
> 本文只核 `#78`–`#87` 这 10 条（任务书里「#76–#85」的编号与 `routes.md` 实际不同，按 `routes.md` 的 `#78`–`#87` 覆盖，另加 `#86`/`#87` 两条同属 `internal/transfer/` 的路由）。
>
> 夹具：端口 **18312**、`CHK_RUN=t-18312`、银行 `rt-t-18312`（+ `rt-other-t-18312`、`rt-ghost-t-18312`），worker 开（`WENYUAN_API_WORKER_ENABLED=true`）。
> 参考：`http://127.0.0.1:8888`。差分工具 `/tmp/rt/chk.py`（PLAN §3）。

### 一、复核结论：第二轮的两处判断是误判

第二轮 `transfer` 小节把 `#79`/`#81`/`#83` 判为「归档条目时间戳」一处缺陷就已收敛，**本轮实测证明归档格式还有 5 类实质差异**，且 `#81` 的 `storage_key` 布局与归档类型都不对。全部已修，见下表与第二节。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #78 `POST /v1/default/banks/{bank_id}/import` | ❌已修 | `api/http.py:9456-9496`（`BankTemplateManifest.validate_version`，`api/http.py:4090-4100`）：① `ver = int(v)` 是 **Python 任意精度整数**，超 `int64` 仍是数字，走「不支持」分支；② 文案 `… Please upgrade Hindsight.` 主体是 **Hindsight** | `internal/banktemplate/manifest.go:169-190`（`Validate`） | 两处：① 版本号改用 `math/big.Int` 解析，不再用 `strconv.Atoi`（超 `int64` 会被误判成 `must be a numeric string`）；② 文案 `Wenyuan` → `Hindsight` | 差分 SAME：`{"version":"2"}`、`{"version":"99999999999999999999"}`、`{"version":"9223372036854775808"}`、`{"version":"-99999999999999999999"}` 四条修复前全 DIFF，修复后全 SAME。用例 `TestValidateUnsupportedVersionNamesTheReference`、`TestValidateVersionIsParsedAtArbitraryPrecision`（`internal/banktemplate/banktemplate_test.go`）。**证伪**（改回 `strconv.Atoi` + `Wenyuan`）：<br>`--- FAIL: TestValidateUnsupportedVersionNamesTheReference` — `structural = ["…Please upgrade Wenyuan."], want […Please upgrade Hindsight.]`<br>`--- FAIL: TestValidateVersionIsParsedAtArbitraryPrecision` — `version "99999999999999999999": structural = ["version: Value error, version must be a numeric string, got '99999999999999999999'"], want ["… is not supported by this server (max supported: 1)…"]` |
| #78 其余分支 | ✅一致 | 同上 | `internal/transfer/template_handlers.go`、`internal/transfer/scope.go`（`decodeJSONBody`、`pythonJSONError`） | 无 | **14 组 SAME**：空体 / `notjson` / `{` / `{"a":}` / `{"a" 1}` / `[1,]` / `"unterminated` / `{"a":1}x` / `tru` / `{"a":1,}` / `  [` / `[1` / `{"a":1 "b":2}` / `{"a":`（CPython `JSONDecodeError` 原文逐字节）；`dry_run=zz`→422；`?oops=1` 未知参数（`X-Ignored-Params` 头 SAME）；`{"version":"0"}`/`{"version":"x"}`/`{}`/`{"version":" 1 "}`/`{"version":"01"}`/`{"version":1}`；重复 mm id / 空 mm name / `retain_custom_instructions` 无语义模式。**真实导入**：`dry_run=true` 与真实导入的 8 键响应同序同值；重复导入同一 manifest 两侧均返回全空列表（幂等）；导入后 `GET .../export` 两侧 `bank` 段 50 键同序同值、`mental_models` 17 键同序（本服务 manifest 的 trigger 键按字母序写出，见 ⚠️ 条）；150 个 mental model 的导入两侧均全部创建（无分页截断） |
| #79 `GET /v1/default/banks/{bank_id}/export` | ❌已修 | `api/http.py:8839-8930`：`profile is None` → 404 `Bank '<id>' not found`；`_refuse_bank_wide_if_scoped(..., "export the bank template")`；读**未解析**的 per-bank overrides（`_load_bank_config(cached=False)`）；`BankTemplateManifest(version=…, bank=…, mental_models=… or None, directives=… or None)` | `internal/transfer/template_handlers.go:ExportTemplate`；`internal/banktemplate/marshal.go` | **`mental_models[].trigger` 的键序**：trigger 是 `map[string]any`，Go 按**字母序**序列化（`budget, exclude_mental_model_ids, exclude_mental_models, …`）；参考的 `MentalModelTrigger` 是 pydantic 模型，按**字段声明序**输出（`budget, mode, refresh_after_consolidation, refresh_cron, …`）。这段对象是 `GET .../export` **响应体的字节**，线上可观测。修法：新增 `templateTriggerFieldOrder`（17 个字段，取自 `api/http.py:3199-3300` 的声明序，已与线上参考逐键核对）+ `orderedMentalModels`/`orderedTrigger`，**只给已存在的键排序、不增删键**；未列出的键排在其后 | **`--raw` 逐字节 SAME**（修复前 DIFF）：同一 bank 下两侧 `GET .../export` 响应体完全一致，17 个 trigger 键逐位同序。另：已知 bank（`{"version":"1","bank":null,"mental_models":null,"directives":null}`，4 键同序）、未知 bank 404 同文案、有 config+MM+directive 时 `bank` 段 50 键同序同值。用例 `TestManifestRendersTriggerInDeclarationOrder`（同时钉住 17 字段表本身、线上键序、以及"不增删键"）。**证伪**（把有序渲染换回 `json.Marshal(m.MentalModels)`）：<br>`--- FAIL: TestManifestRendersTriggerInDeclarationOrder` — `trigger keys out of declaration order: budget=128 mode=161 keep_trace=142 in {…"trigger":{"budget":null,"keep_trace":false,"mode":"full"}}` |
| #80 `GET /v1/default/banks/{bank_id}/document-transfer` | ✅一致 | `api/http.py:8932-8960`：**无条件 410**，`detail` 内嵌本 bank 的 `document-transfer/export` 与 `operations/{operation_id}` 路径 | `internal/transfer/retired.go:11-23` | 无 | **`--raw` 逐字节 SAME ×3**：已知 bank、`{ghost}`（不存在的 bank，两侧均 410 而非 404，证明 410 先于 bank 判定）、bank id 含空格（`rt-b%20x-…`，证明 detail 里的 id 是**解码后**原样插值）。三条 detail 全文逐字节相同，含 `{operation_id}` 字面花括号 |
| #81 `POST …/document-transfer/export` | ❌已修 | `engine/memory_engine.py:3640-3705`（`_handle_export_documents`）：`storage_key = f"{bank_storage_prefix(bank_id)}exports/{uuid}/transfer.zip"`，而 `bank_storage_prefix` = `tenants/{schema}/banks/{bank_id}/`（`engine/storage/__init__.py:12-30`）；归档由 `stream_export_documents`（`engine/transfer/export.py:289-390`）产出：`archive_type:"documents"`、`scope:null`、**无 `data/` 段**、`operation_count:0`、`bank_rows_json_encoding:null` | `internal/app/worker_transfer.go:33-38`（键）、`internal/transfer/export_build.go:104-215`（`BuildArchive` 分支） | 两处：① 新增 `internal/transfer/storage_keys.go` 的 `DocumentExportStorageKey`（`tenants/{schema}/banks/{bank}/exports/{uuid}/transfer.zip`，段编码按参考 `key_segment`），worker 按 `op.OperationType == "export_documents"` 分流；② `BuildArchive` 的 bank 归档分支由「`len(DocumentIDs)==0`」改为「`opts.Scope.Any()`」，文档导出不再误走 bank 分支（原产出 `archive_type:"bank"` + 全 false `scope` + `data/` 段 + `operation_count:21`） | 差分：修复后 `storage_key` 形如 `tenants/public/banks/rt-t-18312/exports/{uuid}/transfer.zip`（参考 `tenants/user_devin/…`，只有 tenant 名不同，那是各自部署的 `WENYUAN_API_DATABASE_SCHEMA`）；归档条目**逐项相同** `['documents/000000.json', …, 'observations.json', 'manifest.json']`（本服务 3 个 doc、参考 2 个，是夹具里文档数不同）；`manifest.json` 15 字段**同键同序同值**（`archive_type:"documents"`、`scope:null`、`operation_count:0`、`bank_rows_json_encoding:null`、`includes_history:false`） |
| #81 校验分支 | ✅一致 | `api/http.py:9401-9430` | `internal/transfer/submit_handlers.go:SubmitDocumentExport` | 无 | **5 组 SAME**：`include_observations=zz`→422 `bool_parsing`（`loc:["query","include_observations"]`）；`include_observations=true&document_id=a`→400（互斥）；`include_knowledge_base=true&document_id=a`→400；无参数→202 `{operation_id,status}`；未知 bank→404；导出开关关闭→404（文案含 `WENYUAN_API_ENABLE_DOCUMENT_EXPORT_API`） |
| #82 `POST …/document-transfer` | ❌已修 | `api/http.py:8988-9060`：`file: UploadFile = File(...)` 是**必填**，框架在**绑定阶段**就判「缺 `file`」→ **422** `{"type":"missing","loc":["body","file"],"msg":"Field required","input":null}`；`on_conflict` 的校验在 **handler 体内**（`api/http.py:9020`），**只在该 body 已绑定时才跑**。因此 `on_conflict=zz` **不带文件**必须报 422（缺 file），**带文件**才报 202（参考实测不拒绝该值） | `internal/transfer/submit_handlers.go:SubmitDocumentImport` | **校验次序**：原实现先校验 `on_conflict` 再读 body，导致不带文件时错报 400；改为**先读必填的 `file`**（与参考的绑定阶段同序），再校验 `on_conflict` | **6 组实测（含真实 multipart 上传）**：无 `file` 部件 → 两侧 **422 逐字节相同**；`on_conflict=zz` **不带文件** → 两侧 **422**（修复前本服务 400，DIFF）；`on_conflict=zz` **带 zip** → 两侧均 **202**；`on_conflict=`（空）带 zip → 两侧 202；空文件 → 两侧 400 同文案；`notazip` → 两侧 400 同文案。用例 `TestRequestShapeFailuresAreReportedTogether`（`internal/transfer/submit_handlers_test.go`）。**证伪**（改回"先校验 `on_conflict`"）：<br>`--- FAIL: TestRequestShapeFailuresAreReportedTogether` — `detail = [{Loc:[query include_data]}], want 3 items` |
| #83 `POST …/transfer/export` | ❌已修 | `engine/memory_engine.py:3707-3765`（`_handle_export_bank`）：`storage_key = f"banks/{bank_id}/exports/{uuid}/transfer.zip"`（**无** tenant 前缀）；归档由 `stream_export_bank`（`engine/transfer/export.py:497-650`）产出 | `internal/transfer/export_build.go`、`internal/transferarchive/`（格式）；`internal/app/worker_transfer.go`（元数据） | 归档格式 5 类，见第二节 | `result_metadata` 4 键同序同集合（`filename`/`byte_size`/`storage_key`/`download_url`），键形 `banks/{bank}/exports/{uuid}/transfer.zip` 与参考**同形**（本服务无 tenant 前缀，与参考整库导出一致）；`filename` = `{bank}-bank.zip` 两侧同。**归档格式**见第二节（修复后条目名/顺序/各段行形状全部对齐）。**8 组校验 SAME**：`include_data=false&include_bank_config=false&include_history=false`→400「Nothing to export…」；`document_id=d1`（默认 `include_bank_config=true`）→400「…only supported for a whole-bank export (omit document_id)」；`include_data=bogus`→422；未知 bank→404；`?include_history=true`→202；`document_id=a&document_id=b`→202 |
| #84 `POST …/transfer/import` | ❌已修（格式侧） | `api/http.py:9146-9240`；`engine/transfer/importer.py:432-480`（`parse_bank_archive`）、`:488-560`（`_restore_rows`） | `internal/transfer/import_apply.go`、`internal/transferarchive/` | 归档格式 5 类，见第二节（导入侧的读取逻辑本身与参考一致：两侧都能读对方产物） | **双向 round-trip 硬证据**（见第三节）：① 参考产出的归档 → 本服务 restore 成功，`documents_imported:2 / facts_imported:2 / observations_imported:2 / mental_models_imported:1 / directives_imported:2`；② **本服务产出的归档 → 参考 restore 成功**（格式正确的金标准），同组计数同值（`operations_imported` 两侧不同，各自操作日志条数不同）。**6 组校验 SAME**：`mode=bogus`→400；`document_conflict=bogus`→400；`mode=merge&target_bank_id=x`→400（含 `{bank_id}` 字面）；`include_data=bogus`→422；`mode=merge&include_data=true`→400；非 zip → 400 |
| #85 `POST …/clone` | ❌已修 | `api/http.py:9252-9335`：`target_bank_id: str = Query(...)`（必填，**声明在三个 include 开关之前**）；两道开关都关 → 404 `Bank clone API is disabled. …`；三开关全 false → 400 `Nothing to clone: …`；`profile is None` → 404；`submit_bank_clone_async` 抛 `ValueError` → 400（自身、目标已存在、id 非法）；`engine/memory_engine.py:3843-3940` 的 `result_metadata` **15 键 + `archive_byte_size`**（共 16） | `internal/transfer/submit_handlers.go:SubmitClone`、`internal/app/worker_transfer.go:applyClone` | ① **校验次序**：`target_bank_id` 缺失必须**先于** flag 失败被报（参考按声明序），原实现先校验 flag；② 归档格式随第二节的修复一并生效（clone 走同一 `BuildArchive`+`Write`+`ImportApplier`） | **提交侧 5 组 SAME**：无 `target_bank_id` → 422 `loc:["query","target_bank_id"]`；`?include_data=bogus`（无 target）→ 422 **两条**，target 在前、flag 在后（修复前只报 1 条）；`?include_data=bogus&include_history=bad` → 422 三条同序；三开关全 false → 400 同文案；`target_bank_id`=自身 → 400 同文案；目标已存在 → 400 **逐字节相同**。**完成态 `result_metadata` 实测**：两侧各 clone 到新 bank，**16 键全部同值**——`documents/facts/observations/attachments/operations/maintenance_queue_rows/invalidated_memories/mental_models/mental_model_history/knowledge_pages/directives/webhooks/history_rows` 计数、`restored_bank_id`、`target_bank_id` 全同；<br>**`archive_byte_size` 的判定依据（组长要求）**：两侧对**同一夹具**各 clone 一次，再各自把 clone 目标导出、**解包逐段对拍**——① 条目列表两侧**逐项相同**（`data/async_operations.json, data/graph_maintenance_queue.json, data/entity_maintenance_queue.json, data/invalidated_memory_units.json, mental_models.json, mental_model_history.json, banks.json, directives.json, webhooks.json, knowledge_pages.json, manifest.json`；修复 F6 之前本服务多一项 `documents/`）；② 每段**键序完全相同**；③ 去掉实例自身的值（`bank_id`/`id`/`created_at`/`updated_at`/`internal_id`/`last_consolidated_at`/`mission_changed_at`，两侧 bank id 本就不同）后**内容完全相同**（`banks.json`、`directives.json` 均 `equal: True`）。所以**解包内容等价**，字节数差异只剩 zip 头/CRC/时间戳这类框架差异 ⇒ `archive_byte_size` 是**噪声**，不是症状。clone 目标的 directive 两侧一致（`('cd','cc',3)`） |
| #86 `GET /v1/default/banks/{bank_id}/attachments/{attachment_id}` | ✅一致 | `api/http.py:9360-9415`：`retrieve_bank_attachment` 为 None → 404 `Attachment not found`；成功时 `media_type=attachment.media_type`、头 `Cache-Control: private, max-age=31536000, immutable` + `X-Content-Type-Options: nosniff`，**无** `Content-Disposition` | `internal/transfer/binary_handlers.go:ServeAttachment` | 无 | **4 组 SAME**：已知 bank 的未知 id → 404 `Attachment not found` 逐字节；未知 bank → 同一 404（不可探测）；`/attachments/`（空 id）→ 同；真实附件（服务侧 file-retain 后取 67 字节 PNG）：`200`、`Content-Type: image/png`、`Cache-Control: private, max-age=31536000, immutable`、`X-Content-Type-Options: nosniff`、`Content-Length: 67`、无 `Content-Disposition` —— 头集与参考常态逐项相同（参考侧同一附件因它自己的 file-retain 任务失败而无行，故**同一时刻**无法做同 id 对拍；两侧的**缺失语义与成功头**已分别实测） |
| #87 `GET /v1/default/files/download/{key}` | ✅一致 | `api.py:9417-9475`：导出开关关 → 404；`key.split("/")` 含 `..` → 404「File not found」；只认 `tenants/{schema}/banks/{bank}/…`（`len>4`）与 `banks/{bank}/…`（`len>2`）两种布局；取不到 → 404「File not found」；成功时 `Content-Type: application/zip`、`Content-Disposition: attachment; filename="{bank}-documents.zip"`、`Content-Length`，**忽略 `Range`**（返回 200 全量，非 206） | `internal/transfer/binary_handlers.go:ServeDownload` | 无 | **6 组 SAME**：未知 key → 404「File not found」；`banks/{b}/../../../etc/passwd` → 404；非 bank 前缀 `loose/key.zip` → 404；`tenants/...` 键（本服务本无该对象）→ 404；**真实归档下载**：`200`、`Content-Type: application/zip`、`Content-Disposition: attachment; filename="rt-t-18312-documents.zip"`、`Content-Length` 正确。**`Range: bytes=0-99`**：本服务 200 + 全量 2023 字节，参考 200 + 全量 2132 字节（**两侧都忽略 Range**，非 206）；`Content-Length` 一致 |

### 二、归档格式：5 类实质差异（已修）+ 可复现判定命令

第二轮认为归档格式只差「条目修改时间」一处。本轮把两侧同夹具的真实归档逐段对拍，发现**5 类**差异。全部在 `internal/transferarchive/`（新增 `rowjson.go`）与 `internal/transfer/export_build.go`：

| # | 差异（修复前） | 参考证据 | 修复 |
|---|---|---|---|
| F1 | **行文件布局**：本服务 `json.Marshal(rows)` 输出**单行 compact**；参考是 `json.dumps(rows, indent=2)` 的多行缩进 | `engine/transfer/export.py:539-545`（`write_rows` → `json.dumps(rows, default=_row_json_default)`）；`export.py:899-975`（整库非流式路径 `json.dumps(rows, indent=2, …)`） | `transferarchive.EncodeRowFile`：用 `json.Indent(&out, enc, "", "  ")` 复刻 CPython `indent=2`（**已逐字节验证**：Go `json.Indent` 空 prefix + `"  "` 与 Python 输出完全相同，含 `{}`/`[]`/嵌套） |
| F2 | **行键序**：Go map 按键**字典序**；参考是 `SELECT *` 的**表列序**（CPython dict 保序） | `export.py:431-440` `_dump_bank_rows` = `SELECT * FROM t WHERE bank_id=$1` | `rowjson.go` 的 `rowColumns` 表记录 11 张表的列序（取自 `migrations/0002_reference_tables.sql`），`orderedObject` 按该序序列化；未列出的列排在其后并排序（不丢列） |
| F3 | **jsonb 列文本**：本服务写成 Go 的插入序 + `":"` 紧凑；参考是 PostgreSQL `jsonb::text`——键按**长度再字节序**重排，`": "` 分隔 | 参考的 asyncpg 连接把 jsonb 列当**文本**返回并原样写进归档（manifest 声明 `bank_rows_json_encoding:"serialized"`） | `export_build.go` 的 `pgJSONText`/`writePGJSON`：键排序 `len` 再字节序，`": "`/`", "` 分隔，字符串转义按 PG。**已用 psql 实测 PG 输出核对**：`select '{"retain_mission":"keep it","enable_observations":true,"enable_reranking":true,"recall_max_tokens":4096}'::jsonb::text` → `{"retain_mission": "keep it", "enable_reranking": true, "recall_max_tokens": 4096, "enable_observations": true}`，本服务现产出**逐字节相同** |
| F4 | **zip 条目标志位**：本服务 `Flags` 只由 Go 推断出 `0x008`；参考是 `0x808`（bit3 data-descriptor + bit11 UTF-8 名） | `engine/transfer/stream_archive.py:149-227`：`flags = 0x0808` 每条目恒定 | `sections.go:writeEntry` 显式 `Flags: 0x0808` |
| F5 | **条目写出顺序**：本服务 `documents → mental_models → knowledge_pages → banks → directives → webhooks → mental_model_history → data/* → manifest`；参考 `documents → observations → blobs+attachments → data/* → mental_models → mental_model_history → banks → directives → webhooks → knowledge_pages → history/* → manifest` | `export.py:497-650` `stream_export_bank` 的写出次序 | `sections.go:writeArchive` 按参考次序重排 |

**F6 · 空 bank 的 `documents/` 占位条目**：本服务在**没有任何文档**时写一个 `documents/` 目录条目（为了"让 reader 能找到 payload 目录"），参考从不写——参考只按文档数写 `documents/{n:06d}.json`（`engine/transfer/export.py:906-910`），空 bank 的归档里**根本没有** `documents/` 成员。这使条目列表多一项、`archive_byte_size` 偏大。读取侧不需要它（`documentNames` 把"目录不存在"当作空文档集）。已删除。用例 `TestWriteEmitsNoDocumentsEntryForAnEmptyBank`；**证伪**（把占位条目加回去）：`--- FAIL: … archive carries "documents/"; the reference emits no documents entry for an empty bank`。<br>**发现路径**：正是组长要求的"clone 的 `archive_byte_size` 是否是症状"检查——两侧对**同一夹具**做 clone 后解包对拍，条目列表差了 `documents/` 一项。

另有 3 处**内容**修正（同属归档格式）：

- **`mental_models` 行缺 5 列**：参考 `SELECT *` 出 21 列，本服务只写 16 列，缺 `last_refreshed_source_query`、`structured_content`、`entity_id`、`last_memory_seen_at`、`last_refresh_failed_at`（缺失的键在恢复时一律读成 null）。`export_build.go:mentalModelRow` 已补齐 21 列、按 `mental_models` 表列序。
- **文档 payload 的 `retain_params`**：列为 `jsonb NOT NULL DEFAULT '{}'`，参考 `SELECT` 出 `{}`；本服务 nil map 写成 `null`，而参考的 `TransferDocument.retain_params` 是 `dict | None` 且**不是必填**，实测把 `null` 喂给参考可接受，但 `{}` 才是参考自己的产物值。`export_build.go:archiveRetainParams` 现写 `{}`。
- **`disposition` 列**：原本绕过 `jsonText` 直接 `json.Marshal`（紧凑 + 插入序），现走同一条 PG 渲染。

**可复现判定命令**（`/tmp/arch` 下 `ref.zip` = 参考导出、`svc.zip` = 本服务导出）：

```sh
# ① 条目名与顺序、每个条目的 GP 标志位与压缩方法
python3 -c "import zipfile
for f in ('ref.zip','svc.zip'):
    print(f,[(i.filename, hex(i.flag_bits), i.compress_type) for i in zipfile.ZipFile(f).infolist()])"
# 期望：两侧条目名列表逐项相同，每个条目 flag_bits 都是 0x808、compress_type 都是 8

# ② 每段：键集 + 键序 + 是否逐字节相同
python3 -c "import zipfile,json
a,b=zipfile.ZipFile('ref.zip'),zipfile.ZipFile('svc.zip')
for n in [i.filename for i in a.infolist() if not i.is_dir()]:
    ra=json.loads(a.read(n)); rb=json.loads(b.read(n))
    if isinstance(ra,list) and ra and isinstance(ra[0],dict):
        print(n,'keyorder equal:',list(ra[0])==list(rb[0]),'\n  ref:',list(ra[0]),'\n  svc:',list(rb[0]))
    else:
        print(n,'scalar keys equal:', list(ra)==list(rb) if isinstance(ra,dict) else 'n/a')"
# 期望：banks/directives/webhooks/mental_models/mental_model_history/attachments/
#       data/*、history/* 的 keyorder equal 全为 True；manifest.json 15 键同序

# ③ jsonb 列文本与 PG 渲染一致（本服务产物里 config 的值必须等于下面这条 SQL 的输出）
PGPASSWORD=wenyuan psql -h 127.0.0.1 -p 15432 -U wenyuan -d wenyuan -t -A -c \
  "select '{\"retain_mission\":\"keep it\",\"enable_observations\":true}'::jsonb::text"
```

**证伪**（把 F1/F4 改回去）：

```
$ go test -count=1 ./internal/transferarchive/
--- FAIL: TestWriteRowFileMatchesJSONDumpsIndent
    row file =
    [{"bank_id":"b1","config":"{\"retain_mission\": \"keep it\"}","created_at":"…", …}]
    want
    [
      {
        "bank_id": "b1",
        "name": "b1",
        "disposition": "{\"empathy\": 3, \"literalism\": 3, \"skepticism\": 3}",
        …
--- FAIL: TestWriteRowFileKeepsUnlistedColumns
--- FAIL: TestWriteSetsTheStreamingEntryFlags
    data/async_operations.json: general-purpose bit 11 (UTF-8 name) is clear, flags=0x8
```

用例：`TestWriteRowFileMatchesJSONDumpsIndent`、`TestWriteRowFileEmptySectionIsAnEmptyList`、`TestWriteRowFileKeepsUnlistedColumns`、`TestWriteSetsTheStreamingEntryFlags`、`TestNormalizeRowValueRendersTimestampsLikeIsoformat`（`internal/transferarchive/rowjson_test.go`）。

### 三、双向 round-trip（硬证据）

| 方向 | 命令 | 结果 |
|---|---|---|
| **参考归档 → 本服务** | `curl -F file=@ref.zip 'http://127.0.0.1:18312/v1/default/banks/rt-t-18312/transfer/import?target_bank_id=rt-t-18312-rt-fromref'` | `completed`；`documents_imported:2 facts_imported:2 observations_imported:2 mental_models_imported:1 directives_imported:2 operations_imported:13`，`error:null` |
| **本服务归档 → 参考**（格式正确的**金标准**） | `curl -H "Authorization: Bearer $REF_KEY" -F file=@svc.zip 'http://127.0.0.1:8888/v1/default/banks/rt-t-18312/transfer/import?target_bank_id=rt-t-18312-rt-fromsvc'` | `completed`；同样 `2/2/2/1/2`（`operations_imported:18`，两侧各自操作日志条数不同）；`error:null`。**修复前**本服务产物会被参考拒绝（第二轮记录：`ValidationError: facts.0.causal_relations Input should be a valid array`），本轮修复后参考可完整导入 |
| **落库等价性** | 两侧再各导出一次 compare | 文档 payload 的 `id`/`original_text`/`retain_params`/`tags`/`chunks[]`/`facts[]` **键集与键序逐项相同**；`facts[]` 17 键、`chunks[]` 2 键、`observations[]` 11 键同序。事实**文本**两侧不同（各自 retain 各自的 LLM 产物），属模型随机性，非格式缺陷 |

### 四、修复清单 / 存疑 / 越界

| 项 | 状态 | 说明 |
|---|---|---|
| #85 `clone` 的完成态 `result_metadata` | ✅已补测 | 两侧各 clone 到新 bank，16 键全同值；仅 `archive_byte_size`（各自归档字节数）不同，非契约差异。见 `#85` 行 |
| `on_conflict` 校验次序（#82） | ✅已修 | `on_conflict=zz` **不带文件**：参考 422（缺 `file`），修复前本服务 400。已改为**先读必填 `file`** 再校验 `on_conflict`（与参考的绑定阶段同序），见 `#82` 行 |
| 多参数校验项累积（#83/#84/#85） | ✅已修 | 参考的框架**一次绑定整个签名**，所以多个非法参数会**同时**列在 `detail` 里、按声明序；本服务原来 `scopeFromQuery` 在第一个失败处即返回，且 clone 把 `target_bank_id` 的缺失排在 flag 失败之后。已改为：`scopeFromQuery` 累积三个 flag 的失败；`SubmitTransferImport` 把 query 失败与缺失的 `body.file` **合并**；`SubmitClone` 让必填的 `target_bank_id` 排在 flag 失败**之前**。用例 `TestRequestShapeFailuresAreReportedTogether` |
| `manifest` 里 mental model 的 `trigger` 键序（#79） | ✅已修 | 见 `#79` 行：新增 `templateTriggerFieldOrder`（17 字段声明序），用例 `TestManifestRendersTriggerInDeclarationOrder`，`--raw` 逐字节 SAME |
| `mental_models.description` / `structured_content` 的取值 | ⚠️噪声 | 参考 side 的这两个值来自**参考自己 worker 的 refresh**（`description:" "`、`structured_content:{"version":2,"sections":[]}`），本服务侧对应模型还没被 refresh 到同一步；是 worker 时序差异，非归档格式缺陷（两侧列都存在且类型正确） |
| 跨作用域：`internal/app/background.go`、`cmd/worker/main.go` 各 1 行 | 已改，**需组长知悉** | `NewQueueConsumer` 增加 `storeSchema string` 末位形参（文档导出键必须带 tenant schema）。`internal/app/worker.go`（`archiveProcessor` 加字段 + 形参）、`internal/app/worker_transfer.go` 在我作用域；**`internal/app/background.go:30` 与 `cmd/worker/main.go:121` 各补一个实参 `application.Config.DatabaseSchema` / `cfg.DatabaseSchema`**，由 transfer 单元在组长授权下改，因签名变更所必需，无其他改动 |
| 归档 `disposition` 与 `created_at` 的**值**不同 | ⚠️噪声 | `disposition` 两侧都是 `{"empathy":3,"literalism":3,"skepticism":3}`（同值，PG 文本渲染已对齐）；`created_at`/`updated_at`/`internal_id` 是各实例自己的行值，天然不同 |

### 五、本节结论计数（按 API 计，共 10 条：`#78`–`#87`）

- **✅一致 3** = `#80`、`#86`、`#87`
- **❌已修 7** = `#78`（模板版本校验 ×2）、`#79`（trigger 键序）、`#81`（`storage_key` 布局 + 归档类型；校验分支本身一致）、`#82`（校验次序）、`#83`（归档格式）、`#84`（归档格式）、`#85`（校验次序 + 归档格式）
- **⚠️存疑 0**
- **⛔未覆盖 0**；**🚧待补 0**

归档格式合计 **6 类结构差异**（F1–F6）+ **3 处内容修正**，全部已修并各有"改回去就 FAIL"的用例。

### 六、最终验收门（本单元包）

```sh
go test -count=1 ./internal/transfer/... ./internal/transferarchive/... ./internal/banktemplate/...   # 137 passed
go vet ./internal/transfer/... ./internal/transferarchive/... ./internal/banktemplate/...             # 0 issues
gofmt -l internal/transfer internal/transferarchive internal/banktemplate                             # 空
golangci-lint run ./internal/transfer/... ./internal/transferarchive/... ./internal/banktemplate/...   # 0 issues
CGO_ENABLED=0 go build ./...                                                                           # OK
CGO_ENABLED=0 go build -tags pgx ./...                                                                 # OK
```

**全量差分探针（端口 18350，`CHK_RUN=t-18312`）：51 组请求/响应断言全 SAME**（覆盖 `#78`–`#87` 的正常路径、校验分支、404/422 与未知 bank）：

```sh
export SVC_BASE=http://127.0.0.1:18350 REF_KEY=sk-hs-f94653018553250b8ffeff305b86ee0a CHK_RUN=t-18312
python3 /tmp/rt/chk.py POST '/v1/default/banks/{b}/transfer/import?include_data=bogus&include_history=bogus'
python3 /tmp/rt/chk.py POST '/v1/default/banks/{b}/clone?include_data=bogus'
python3 /tmp/rt/chk.py GET  '/v1/default/banks/{b}/document-transfer' --raw      # 410 detail 逐字节
```

**组长裁决记录（2026-10-26）**：
1. `#79` 的 `trigger` 键序 —— 裁决「修」，理由是该段是**线上可观测的字节**。已按裁决实现并验证（见 `#79` 行）。
2. 跨作用域 `internal/app/background.go:30`、`cmd/worker/main.go:121` 的两行实参 —— **批准并已复核**：各恰好 +1 个实参（`application.Config.DatabaseSchema` / `cfg.DatabaseSchema`），是 `NewQueueConsumer` 签名变更的必要连带，保留。
3. `archive_byte_size` 的判定 —— 要求证明两侧归档**解包后内容相同**才可判噪声。已证明（见 `#85` 行），判定成立；附带发现并修复了 F6。

**无待裁决事项。**

## 第三轮 retain

单元：`internal/memoryops/`（retain / file_retain / clear_memories / inline_attachments / normalize / retain_* ）、`internal/extraction/`、
`internal/fileparse/`、`internal/entitylabels/`（组长 2026-10-26 把该包划归本单元）。参考检出
`../hindsight/hindsight-api-slim/hindsight_api/`，行号以当前检出为准。线上差分：参考 `http://127.0.0.1:8888`，
本服务 `scripts/parity_dev_service.sh 18311`（`CHK_RUN=r-18311`，夹具 `rt-r-18311` / `rt-other-r-18311` / `rt-ghost-r-18311`），
工具 `/tmp/rt/chk.py` 与若干脚本化 body 批（下文的 "批" 指一次脚本内对两侧同 body 的逐字节比对）。

**编号口径**：`routes.md` 把 `#19/#20` 分给了本次会话中实际由 `bank-config` 单元负责的 `PATCH/DELETE .../config`；
本单元按**任务书与 `PLAN.md §5`** 负责的 5 条写面 API 是 `POST .../memories`（#23）、`POST .../files/retain`（#24）、
`DELETE .../memories`（#25）、`POST .../memories/dry-run-extract`（#21）、`POST .../prompts/preview`（#22），
下表按这五条逐行给出，编号沿用 `routes.md`。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #23 `POST /v1/default/banks/{bank_id}/memories`（同步/异步收据、`items_count`、`usage`、`operation_id` vs `operation_ids`、空 items） | ✅一致 | 装饰器 `api/http.py:10052`；异步分支 `api/http.py:10184-10214`（每组一个 `submit_async_retain`，`operation_id` 取首个、仅多组时给 `operation_ids`）；同步分支 `api/http.py:10216-10290`（`usage` 恒为 `TokenUsage`，`items_count` 按 `len(contents)` 累加）；`engine/memory_engine.py:6489-6503` 空 contents 直接返回 | `internal/memoryops/retain.go:358-479`（`Retain`）、`:1070-1094`（`BuildRetainResponse`）、`:627-641`（`submitAsyncRetain` 的空 items 短路） | 无 | `chk.py POST .../memories` 的 async(1/2 item)、async 空 items、sync、sync 空 items、`document_tags` 空 items 全部 **SAME**；批 26 例（缺 `items`／`items:5`／未知顶层字段／`async:"x"`／`operation_id` 非 UUID／同步异步 `update_mode`）**0 DIFF** |
| #23（`content` 的联合类型校验、422 detail 逐项） | ❌已修（3 处） | `MemoryItem.content` 是 `str \| list[tagged-union[...]]`（`engine/retain/attachment_content.py:140-157`），pydantic 对联合**两个分支都报**；`union_tag_*` 的 `ctx` 按**报告序**输出 `discriminator, tag, expected_tags`；任何"存在但不可用"的判别值（`5`/`true`/`null`/`""`/对象）都是 `union_tag_invalid` 并把它**字符串化**进 `msg`/`ctx.tag`（`null`→`None`、对象→Python repr）；字符串分支与 `content cannot be empty` 的 `input` 是**调用方写的原字节**（成员序保留） | `internal/memoryops/retain_models.go`（`decodeBlocks` 的 `union_tag_*` 分支、新增 `stageCtx`／`discriminatorText`／`writePythonValue`／`pythonReprString`／`sourceObjectValue`／`orderedJSONObject`；`Content.rawJSON`）、`internal/memoryops/validation.go:185-212`（两个分支的 `input` 改用 `rawJSON`） | ①`ctx` 由 `map[string]any`（键序按字母）改为定序结构 `stageCtx`，`tag` 用指针，使 `""` 也输出；②判别值不是字符串时不再报 `union_tag_not_found`，改按框架比较渲染（`5`→`"5"`、`true`→`True`、`null`→`None`、对象→`{'a': 1}`），只有**完全缺 `type`** 才是 `union_tag_not_found`；③字符串分支的 `input` 与 `content cannot be empty` 的 `input` 改为原字节（Go map 会按键排序），`source` 对象同理 | 批 11 例（`type:""`／`type:5`／`type:true`／`type:null`／`type:{}`／`type:[]`／`type:"bogus"`／`type:"TEXT"`／缺 `type`／`source` 缺 `data`／`source` 缺 `media_type`／`filename:5`）+ 批 11 例（空/纯空白字符串、`content:[]`、`content:null`、`content:5`）全部 **SAME**。用例：`TestRetainValidatesContentBlocks`（9 子例，含 `ctx` 键序、`type:5` 等新增断言）；`retain_content_blocks_test.go` 里三处**过时断言**（断言 Go map 的字母序 `input`）已按参考的书写序改正 |
| #23（image/file block 在"模型不能读图"时被 422 拒绝） | ❌已修 | 参考的 `canonicalize_item_content` **不会**因为模型不支持视觉而拒绝请求：它在无 vision 时丢弃 attachment block、只留文本（`engine/retain/attachment_content.py:44-140` + `api/http.py:10096-10130` 的 `canonicalize_item_content(...)` 调用）；视觉门在**抽取阶段**才生效 | `internal/memoryops/retain.go`（删除 `blockedWriteFailure` 的 422 前置门）、`retain_models.go`（新增 `FlattenDecliningAttachments`、`stripPlaceholders`、`Content.OwnsFlatAttachment` 语料、`RetainItem.visionEnabled`/`textIsCanonical`）、`retain_fold.go`（`foldText`）、`inline_attachments.go`（declined 时不写 attachment/存储行）、`file_retain.go`（同规则，且图片上传在无 vision 时得到"只有文本"的文档、不写附件行） | 原实现把"模型不能读图 + 带 image block"当 422；现按参考：文本照常保留、attachment block 的位置只留一个段落分隔，且**不落附件字节**（没有任何写入能解析它），单元测试与线上差分均验证 | `TestRetainWithoutVisionStoresTheProseOnly`、`TestRetainWithVisionKeepsThePlaceholder`、`TestRetainWithoutVisionStoresProseUnits`、`TestImageUploadWithoutVisionDropsTheAttachment`、`TestCanonicalTextKeepsItsOwnPlaceholder`、`TestCanonicalTextDropsAForeignPlaceholder`。**证伪**：把 `Retain` 里 `req.Items[i].visionEnabled = h.VisionEnabled` 改回常量 `true` 后 → `--- FAIL: TestRetainWithoutVisionStoresTheProseOnly (original_text = "before\n\n⟦hs-att:2cf24dba5fb0⟧\n\nafter", want "before\n\nafter")` 与 `--- FAIL: TestRetainWithoutVisionStoresProseUnits (unit text still carries a placeholder token …)`；改回即通过 |
| #23（`update_mode='append'` 缺 `document_id`） | ❌已修 | `engine/memory_engine.py:6586-6593`：同步路径先逐项检查 `update_mode == "append" and not document_id` 并抛 `ValueError("update_mode='append' requires a document_id")`（→400），**在**读 `store_document_text` 之前；异步路径不查（由 worker 承担） | `internal/memoryops/retain.go:729-748`（`prepareRetain`）、`:1096-1136`（`mapWriteError` 新增该错误分支） | 原实现直接 200 落库（把 append 当 replace）；现同步路径 400 且文案逐字相同，异步路径保持参考的"提交成功" | `TestAppendWithoutDocumentIDIsRefused`、`TestAppendWithDocumentIDIsAccepted`；线上 `chk.py`：sync `append` 无 doc（含 `document_id:""`）→ **SAME**，`async:true` 的同一 body → **SAME**（200 收据） |
| #23（写库副作用往返：文档正文、`retain_params`、tags、时间列、内容折叠、append 语义） | ✅一致 | `api/http.py:10146-10183`（`content_dict` 的成员与顺序）、`engine/memory_engine.py:6579-6642`（同步折叠同 `document_id`）、`engine/retain/orchestrator.py:598-610`（tags 走 `set`） | `internal/memoryops/retain.go:1189-1240`、`retain_fold.go`、`internal/storage/*` | 无 | 同 body 两侧写入后 `GET .../documents/{id}`、`GET .../documents/{id}/chunks`、`GET .../documents`、`GET .../tags` 逐字节比对：`original_text`、`content_hash`、`retain_params`（`context`/`entities`(type 缺省补 `CONCEPT`)/`event_date`/`metadata`/`resolve_entities`）、`document_metadata`、`tags`、`text_length`（含中文 16 字）、`append` 后正文 `"first\nsecond"` 全部 **SAME**。`tags` 的**次序**不同（参考 `["t2","t1"]`、本服务 `["t1","t2"]`）属第二轮已裁定的"无次序契约"（参考存 Python `set` 迭代序），未计缺陷 |
| #21 `POST .../memories/dry-run-extract`（成功路径：facts/chunks/usage 形状、chunks 模式、chunk 切分） | ✅一致 | 装饰器 `api/http.py:5922`；`_require_dry_run_enabled` `:5904-5918`；`engine/memory_engine.py:12960-13150`（`extract_dry_run`：chunks 模式走 `_extract_facts_chunks`，其余走 `extract_facts_from_text`，`fact_count` 取每 chunk 的计数） | `internal/reporting/memory_routes.go:1035-1229`、`internal/reporting/dryrun_content.go`、`internal/app/extraction.go:154-176` | 无 | `chk.py POST .../memories/dry-run-extract`：`retain_extraction_mode:"chunks"`（含 `retain_chunk_size:5` 对 `"Alpha beta."` → `Alpha`/`beta`/`.` 三块）、`chunks`+块形式 content、`timestamp:"unset"`、`retain_chunk_size:5` **SAME**。**模型产物不计**：两侧接同一个 LLM，但 `concise` 模式下模型对同一段文本的措辞/实体次序天然不同（`PLAN.md §4`），故 `concise`/`custom`/`verbatim` 的 `facts[].text` 逐字差异不计为缺陷 |
| #21（`entity_labels` 结构校验缺失） | ❌已修 | `api/http.py:2613` `entity_labels: list[LabelGroup] \| None`；`engine/retain/entity_labels.py:33-63`（`LabelGroup` 的 `key: str` 必填、`type` 是五值 `Literal`、`values: list[LabelValue]`…，`_migrate_entity_labels_input` 在成员校验前跑） | `internal/entitylabels/reference_shape.go`（新增导出 `Problems(raw) []Problem`、`problemInput`、`problemContext`）、`internal/reporting/dryrun_content.go`（新增 `dryRunEntityLabelFailure`）、`internal/reporting/memory_routes.go:1085-1093`（在 content 校验之后、抽取之前） | 原实现把 `entity_labels` 只当"尽力解析的词表"（`entitylabels.Parse` 失败就忽略），于是参考**拒绝**的 body 本服务 200 并照常抽取；现按参考的 `list[LabelGroup]` 在进 handler 前报 422，`loc` 为 `["body","entity_labels",<index>,<成员路径…>]`，`msg`/`ctx.expected` 逐字相同，`missing` 项的 `input` 是**迁移并序列化后**的 group 对象（含补出的 `"type":"value"`） | `TestDryRunRejectsAnInvalidEntityLabelsOverride`（3 子例：缺 `key`／`type:"bogus"`／`key:5`）、`TestDryRunAcceptsAValidEntityLabelsOverride`。**证伪**：把 `memory_routes.go` 的 `if len(body.EntityLabels) > 0` 改成 `if false && …` 后 → `--- FAIL: TestDryRunRejectsAnInvalidEntityLabelsOverride/a_group_with_no_key: status = 200, want 422`（另外两子例同 FAIL）；线上 7 个 body（缺 key／`type:"bogus"`／`key:5`／`[{}]`／`values[].description:5`／`fields.f.type:"bogus"`／合法值）**0 DIFF** |
| #21（请求体校验：`content` 联合、`timestamp`、各 override 类型、非对象 body、`null` body） | ✅一致 | `api/http.py:2572-2619`（`DryRunExtractRequest`，`timestamp: datetime\|None` 是裸 datetime）+ 第二轮已落地的 `internal/validation` 解析器 | `internal/reporting/dryrun_content.go`、`memory_routes.go:1036-1084`、`dryrun_timestamp.go` | 无 | 批：`{}`／`{"content":5}`／`retain_mission:5`／`retain_extraction_mode:5`／`retain_extract_causal_links:"yes"`／`retain_chunk_size:"5"`／`entities_allow_free_form:"true"`／`llm_output_language:5`／`strategy:5`／`agent_name:5`／`context:5`／`entity_labels:"x"`／`[]`／`null`／`5`／`"x"` / `true` 全部 **SAME** |
| #21（`verbatim` 模式 dry run 的 `facts[].text`） | ⚠️存疑（参考自身缺陷，按 `docs/CONTRACT-AUDIT.md` §5「参考自身异常不复刻」裁决**有意不照抄**） | **参考在同一条路径上用了两个不同的字段，导致它的 verbatim dry-run 恒为空**：`_collapse_to_verbatim`（`engine/retain/fact_extraction.py:3657-3682`）把 chunk 原文写进 **`fact.fact_text`**；而 dry-run 的结果组装（`engine/memory_engine.py:13119-13124`）取的是 **`text=fact.fact`**（另一个字段）。真实 retain 的落库路径读 `fact_text`，所以落库得到 chunk 原文、dry-run 却恒为 `""`。实测：参考对 `{"content":"word","retain_extraction_mode":"verbatim"}` 与对一长串普通文本都返回 `facts[0].text == ""`（`fact_type: "world"`、`chunk_index: 0`、`fact_count: 1`，只有 `text` 是空串） | `internal/extraction/extractor.go:386-414`（`collapseVerbatim` 用 `representative.Text = chunk`）、`internal/app/extraction.go:163-176`（dry-run 直接复用 `ExtractWith`，因而拿到与落库相同的 chunk 原文） | **有意不修**（组长 2026-10-26 裁决）：这是参考实现自身**同一条路径上的自相矛盾**——`fact_text`（写）与 `fact`（读）不是同一个字段。照抄会让本服务的 verbatim dry-run **永远看不到任何内容**，把一个"预览工具"变成恒空；按 `docs/CONTRACT-AUDIT.md` §5「参考自身的异常不复刻」（该节 `docs/CONTRACT-AUDIT.md:192-193` 已列的同类项：`NaN`/`Infinity` 触发 500、约 2000 层嵌套触发 `RecursionError`）保留 chunk 原文。`retain` 路径本身两侧一致（`chunks`/`verbatim` 落库文本逐字相同），差异**只在**这一处预览文本 | `chk.py POST .../memories/dry-run-extract --body '{"content":"word","retain_extraction_mode":"verbatim"}'`：参考 `facts[0].text == ""`、本服务 `"word"`（**这是已知的、有据可依的差异，不是未覆盖**）。对照：同一 body 走 `POST .../memories` 落库后，两侧 `GET .../memories/list` 的 `text` 逐字相同 → 证明只有预览路径分叉 |
| #22 `POST .../prompts/preview`（三种 operation 的渲染、模板替换与分块、strategy、缺参、未知类型） | ✅一致 | 装饰器 `api/http.py:5981`；`api/http.py:2433-2479`（`PromptPreviewRequest`：`operation` 是 `Literal["retain","consolidation","reflect"]` 默认 `retain`，`strategy: str\|None`）；`api/http.py:5996-6041`；`engine/memory_engine.py:12883-12945`（`preview_prompt`：retain 走 `_resolve_retain_config`，reflect 才读 profile+directives，其余只读 resolved config）；`engine/prompt_preview.py:1-596`（六个 `PLACEHOLDER_*`、`_partition` 的分块算法、`_heading`、`CHUNKS_MODE_EXPLANATION`、`render_prompt_preview` 的 editable 标注） | `internal/reporting/prompt_preview.go:1-831`、`memory_routes.go:1269-1314`、`internal/extraction/preview.go`、`internal/reflect`、`internal/consolidation` | 无 | `chk.py POST .../prompts/preview`：`retain`/`consolidation`/`reflect`/`{}`/`{"operation":"bogus"}`/`{"operation":5}`/`{"operation":null}`/`{"operation":""}`/`{"operation":"RETAIN"}`/`{"strategy":5}`/`{"strategy":""}`/`{"strategy":"nonexistent"}`/`{"bogus":1}`/`null`/`[]`/`5`/`"x"`/`{`/`notjson`/无 body/未知 bank 全部 **SAME**；配置了 `retain_mission`/`observations_mission`/`reflect_mission`/`retain_custom_instructions`/`entity_labels`/`entities_allow_free_form`/`retain_strategies`(s1→verbose、s2 空)/`retain_default_strategy`/`retain_chunk_size`/`llm_output_language:"Chinese"` 后再测 retain(默认/`s1`/`s2`/`bogus`)、consolidation、reflect 以及 `retain_extraction_mode:"chunks"` 的 `skipped_reason` 分支，共 6 例全部 **SAME**。注：`retain` 预览的 user 消息里 `Event Date: … (2026-…T…+00:00)` 是**当前时刻**（`engine/prompt_preview.py:367-378` 的 `utcnow()`），属 `chk.py` 归一化字段，两次请求必然不同，不是差异 |
| #24 `POST .../files/retain`（`request` 表单的模型校验：`Invalid request JSON: …` 的逐字报告） | ❌已修 | `api/http.py:10402-10440`（`FileRetainRequest.model_validate_json(request)`，失败抛 400 `f"Invalid request JSON: {e}"`）；`api/http.py:1321-1387`（`FileRetainMetadata`/`FileRetainRequest` 成员声明序与类型：`parser: str \| list[str]`、`files_metadata: list[FileRetainMetadata]`、`metadata: dict[str,Any]`、`tags: list[str]`…）；`api/http.py:1369-1387`（`reject_misplaced_file_metadata` 的 `sorted(...)` 与 ";" 连接） | `internal/memoryops/file_retain_request.go`（新增：`fileRetainReport`、`decodeOrdered`、`decodeFileParserSelectionAt`、`decodeFileMetadata`、`decodeFileRetainMeta`、`writePythonRepr`、`pythonJSONMessage`、`eofContext`…）、`file_retain.go:348-372`（调用点） | 原实现只报一句 `Invalid request JSON: 1 validation error for FileRetainRequest\n  Value error, …`，缺 `[type=…, input_value=…, input_type=…]` 与文档链接行（第二轮遗留 ⚠️）；错误类型（`model_type`/`list_type`/`string_type`/`dict_type`）、成员路径（`files_metadata.0.metadata`）、`input_value` 的 Python repr（含**成员书写序**）以及 JSON 语法错误文案（`key must be a string at line 1 column 2`、`EOF while parsing an object at line 1 column 6`、`trailing characters at line 1 column 8`）现与参考逐字相同 | `TestFileRetainRequestReportMatchesTheLibrary`（8 子例）。线上批 **46 个 `request` body**（缺键/类型错/`files_metadata:[null]`/`parser:5`/`parser:["a",5]`/`parser:[]`/误置字段/根级 `update_mode`/`[]`/`null`/`5`/`true`/`"str"`/`{bad`/`[1,`/`{"a":`/`tru`/`"unterminated`/`{"a":1}x`/`{"a":[`/`{"a":true`…）**0 DIFF** |
| #24（未知 bank → 404 而非隐式建行） | ❌已修 | `engine/memory_engine.py:22768-22771`（`submit_async_file_retain` 只 `_check_retain_writes`，随后 `_submit_async_operation` 对未知 bank 抛 `Bank '<id>' not found` → 404）；实测参考 `POST /banks/<不存在>/files/retain` → `404 {"detail":"Bank '…' not found"}` | `internal/memoryops/file_retain.go:311-318`（提交前 `GetBank`） | 原实现在异步提交里悄悄建行（`SubmitAsyncRetain` 的 `ensureBankExists`），于是同一 body 本服务 200 并新建银行；现 404 且文案逐字相同 | `TestFileRetainRefusesAnUnknownBank`。**证伪**：把该 `GetBank` 检查短路成 `if false` 后 → `--- FAIL: TestFileRetainRefusesAnUnknownBank: status = 200, want 404`；改回即通过。线上：`rt-ghost-z-18311` / `rt-ghost-zzz` 两侧同为 `404` 同句（`rt-ghost-r-18311` 已在早期探针中被本服务建出，属夹具污染，见文末） |
| #24（既有结论复核：多文件、parser 选择/allowlist、批量上限、`files_metadata` 对齐、`update_mode`/误置字段） | ✅一致 | `api/http.py:10440-10520`（`file_conversion_max_batch_size` → 400 "Too many files…"；`files_metadata` 数量对齐 → 400；`_validate_parsers` → 400 `Parser(s) not available (…): […]`；总字节上限 → 400 `Total batch size (…MB)`） | `internal/memoryops/file_retain.go:182-332`、`:440-470` | 无（第二轮已修的两条仍成立） | 线上：两文件 → 200 两个 `operation_ids`；缺 `files`／缺 `request` → 422 各自逐字 SAME；`parser:"nope"`／`parser:["markitdown","nope"]`／`parser:""` → 400 逐字 SAME；`files_metadata` 数量不匹配 → 400 SAME；空 `files_metadata` 视为"没给" → 200；`{"files_metadata":[{}]}`／带全部成员 → 200；`sample.pdf`/`sample.csv`/`.txt`/`.png` 上传两侧同码（`operation_ids` 是活值，按形状比） |
| #25 `DELETE /v1/default/banks/{bank_id}/memories`（`type` 过滤、非法/空/重复 `type`、未知 bank） | ✅一致 | 装饰器 `api/http.py:10528`；`api/http.py:10540-10560`（`delete_bank(..., fact_type=type, delete_bank_profile=False)`，`deleted = result.get("memory_units_deleted", 0)`，`scope = f" of type '{type}'" if type else ""`）；`engine/memory_engine.py:11377-11640`（无 bank 存在性检查 → 未知 bank 200/0；类型化路径 `engine/memories/pg/engine_curation.py:98-114`） | `internal/memoryops/clear_memories.go:36-59`、`internal/storage/postgres_clear_memories.go` | 无 | 线上：`?type=`（空串）／`world`／`experience`／`observation`／`bogus`／` world`／`WORLD`／无参数／重复 `type=world&type=experience`（末值优先）／未知 bank —— 全部 **SAME**（含 `message` 与 `deleted_count`）。**往返取证**：先各 retain 两条（一条带 `document_id`），再依次 `?type=observation`（两侧同 `Cleared 2`）、`?type=world`（同 `Cleared 2`）、`?type=experience`、`?type=observation`，最后 `GET .../memories/list` 两侧同空、`GET .../documents` 两侧仍同 |

**本单元结论计数（按行）**：✅ 6 行一致（#23 两行、#21 两行、#22 一行、#25 一行）／❌ 5 行已修（#23 三行、#21 一行、#24 两行）／⚠️ 2 行存疑
（#21 `verbatim` —— **参考自身缺陷、按 §5 不复刻**；#24 顶层 operation 类型 —— **已确证可观察、本轮不修的结构性缺口**）／⛔ 0。

**按 5 条 API 的最终结论**（组长 2026-10-26 裁决口径）：
- `#22 POST .../prompts/preview` → **✅一致**
- `#25 DELETE .../memories` → **✅一致**
- `#23 POST .../memories` → **❌已修**（4 处）
- `#24 POST .../files/retain` → **❌已修**（3 处；顶层 operation 类型另记为 `⚠️存疑（已确证可观察，本轮不修）`，是**有据可查的已知缺口**而非未查）
- `#21 POST .../memories/dry-run-extract` → **❌已修**（`entity_labels` 结构校验是实修）；`verbatim` 预览文本单列为 `⚠️存疑（参考自身缺陷，按 `docs/CONTRACT-AUDIT.md` §5 裁决不复刻）`

→ **✅ 2 / ❌ 2 / ⚠️ 1 / ⛔ 0**（⚠️ 只计 `#21` 的 verbatim 子项；`#24` 的 operation 类型已在其 `❌已修` 的 API 内作为附注缺口列出）。

### 本单元发现的、需要组长裁决或跨作用域处理的事项

1. **`/files/retain` 的顶层 operation 类型 —— `⚠️存疑（已确证可观察，本轮不修）`，记为已知缺口**（组长 2026-10-26 裁决）。
   - **现象**（worker 开启时实测）：
     - `GET .../operations?type=file_convert_retain`：参考 `total 12`，本服务 **`total 0`**；
     - `GET .../operations/{id}`：参考 `operation_type = task_type = "file_convert_retain"`、`document_id`/`filename` 是上传的文档与文件名、`result_metadata = {"document_id": …, "original_filename": …}`；本服务 `"retain"` + `result_metadata = {"extraction_errors_count": 0, "unit_ids_count": 0}`；
     - `POST .../operations/{id}/retry` 与 `DELETE .../operations/{id}`：参考的 `file_convert_retain` 是**已终结**的中间 op，retry/cancel 语义与一个 `retain` 父操作不同。
   - **参考证据**：`engine/memory_engine.py:22740-22768`（`submit_async_file_retain` 每文件一个 `file_convert_retain` 顶层 op、`dedupe_by_bank=False`）、`:4092-4200`（`_handle_file_convert_retain` 转换后**另提**一个 retain 任务并把**自己**标 completed）、`:4678-4748`（`file_convert_retain` 不参与统一的 completed 标记）。
   - **本服务落点**：`internal/memoryops/file_retain.go`（每文件提交一个 `retain` 类型的队列 op）；op 行本身由 `internal/storage` 写、由 `internal/worker` 消费。
   - **为什么本轮不做**：这不是改一个类型字面量 —— 需要在 `internal/storage` 里把 op 类型与 `result_metadata`/`document_id`/`filename` 写成参考的形状，并在 `internal/worker` 里**新增一个"转换 → 另提 retain → 自我 completed"的阶段**（现有 worker 没有这个中间层，只改类型会让它的处理分支失配）。这是**跨 storage + worker 的结构性改造**，风险远高于收益：它**不改变客户端拿到的 `operation_ids`**（`POST /files/retain` 的响应体两侧本就一致），只改变操作日志的呈现。
   - **建议的后续做法**：作为独立立项，先在 `internal/storage` 的 op 模型里加入 `file_convert_retain` 类型与它的 `result_metadata` 形状（`{document_id, original_filename}`），再在 `internal/worker` 的 `file_convert_retain` 分支里做"转换 → 提交子 retain → 自标完成"，并用 `GET .../operations?type=file_convert_retain` 与 `GET .../operations/{id}` 两侧比对验收。
2. **`dry-run-extract` 的 `verbatim` 模式**（上表 ⚠️ 行）—— **已裁决：`⚠️存疑（参考自身缺陷，不复刻）`，保持现状**。参考在 `_collapse_to_verbatim` 写 `fact_text`（`engine/retain/fact_extraction.py:3657-3682`）而 dry-run 读 `fact`（`engine/memory_engine.py:13119-13124`），两个字段不同，故其 verbatim dry-run 恒回 `""`；本服务保留 chunk 原文，与参考的真实 retain 落库一致。按 `docs/CONTRACT-AUDIT.md` §5「参考自身异常不复刻」处理。
3. **`/stats` 与 `/memories/list` 的 observation 行集合差异**（非本单元缺陷，记录以便复核）：本服务 worker 关闭时，同步 retain 之后 `pending_consolidation` 一直挂着；一旦**任何**带 worker 的服务消费了共享队列，保留下来的 observation 会在两侧的不同时刻出现，于是 `DELETE ?type=world|observation` 的 `deleted_count` 与 `/memories/list` 的 `total` 在**同一时刻**可能不同（本单元实测：同一 body 后本服务 3 条、参考 4 条；补一次观察后两侧各 `Cleared 2` 一致）。这是夹具/时序，不是实现差异，故 `#25` 判 ✅。
4. **过时断言（本单元自改）**：`internal/memoryops/retain_content_blocks_test.go` 有 4 处断言把校验项 `input` 写成 Go map 的字母序（`{"source":{…},"type":…}`、`{"text":"x","type":"text"}`），而参考保留调用方书写序；已按参考改正，属"旧断言本身是错的"，不是放松。

### 夹具污染（记录，已处理）

- `rt-ghost-r-18311` 在早期探针（本单元修复**前**）被本服务的异步提交 `ensureBankExists` 建成了真银行（`GET .../operations` 能看到 retain/consolidation 行）。修复后不再新建；该行在两库都留下痕迹，后续「未知 bank」探针改用 `rt-never-existed-bank`（两侧同 404）。
- `rt-other-r-18311` 在早期一次 `/files/retain` 探针里被同一条路径建出（`GET .../operations` 有 2 行），同样只用过探针文件内容，没有业务数据。
- 结束前已删除本单元的夹具银行；服务已停。

### 本单元验收

```sh
export PATH=/usr/local/go/bin:/data/go/bin:$PATH
export GOCACHE=/tmp/wc/gobuild GOLANGCI_LINT_CACHE=/tmp/wc/lint
go test -count=1 ./internal/memoryops/... ./internal/extraction/... ./internal/fileparse/... ./internal/app/...   # 全 ok
go vet ./internal/memoryops/ ./internal/entitylabels/ ./internal/extraction/ ./internal/fileparse/ ./internal/reporting/   # 无输出
gofmt -l internal/memoryops internal/entitylabels internal/extraction internal/fileparse internal/reporting internal/app    # 无输出
golangci-lint run ./internal/memoryops/... ./internal/entitylabels/... ./internal/extraction/... ./internal/fileparse/...   # 0 issues
CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go build -tags pgx ./...                                                     # 均通过
```

（`golangci-lint run ./internal/reporting/...` 有 1 条 `unused: knowledgeItemMessage`，在 `internal/reporting/knowledge_body.go` —— 该文件属 `knowledge` 单元本次的改动，不是本单元引入。）

## 第三轮 lead（跨单元 · 路由层 / 存储层 / httpx / app）

> 本轮组长承担的**跨作用域**缺陷（`internal/httpx/`、`internal/storage/`、`internal/app/` 的公共面）
> 与**路由表核对**。方法与被审计单元相同：参考 `file:line` + 本服务落点 + 线上差分 + "改回去就 FAIL" 的证伪。
> 差分工具 `/tmp/rt/chk.py`；本服务在 `18332/18340/18341` 端口（`-tags pgx`，`WENYUAN_API_WORKER_ENABLED=true` 当需要 worker 时）。

### 0. 路由表面完整性（100 条装饰器路由）

从参考 `api/http.py` 抽取 `@app.{get,post,put,patch,delete}` 装饰器（剥掉注释与字符串内括号，逐条带行号与 `operation_id`），
与本服务 `internal/httpx/openapi.json` 的 99 条 operation 规范化比对（`{x:path}` → `{x}`）：

| 项 | 结果 |
|---|---|
| 参考装饰器路由 | **100** |
| 本服务 OpenAPI operation | **99**（+ `GET /v1/version`，`include_in_schema=False`，共 100 条可达路由） |
| 规范化后两侧交集 | **99 / 99，本服务无独有路由、参考无独有业务路由** |
| 参考独有 | `GET /v1/version`（本服务已实现，只是不在文档里） |

**结论：路由表 100/100 齐全，无缺口。** 清单与编号固化在 `docs/parity-round/routes.md`。

### 1. 已修缺陷（每处都带证伪）

| # / 主题 | 结论 | 参考证据 | 本服务落点 | 验证 |
|---|---|---|---|---|
| 跨单元 · consolidation 写 observation 时四个时间列全为 NULL | ❌已修 | `_aggregate_source_fields`（`engine/consolidation/consolidator.py:838-860`）：`event_date`/`occurred_start` 取**最早**、`occurred_end`/`mentioned_at` 取**最新**，字段无来源则保持 `None`；create 路径 `:2585` → `:3008-3011`；update 路径 `_TemporalBounds.merged_with` `:257-262`；折叠路径 `engine/memories/pg/consolidation.py:118-126,196-200,246-251` 的 LEAST/GREATEST | 新增 `internal/storage/observation_bounds.go`（`ObservationBounds`/`AggregatedObservationBounds`/`MergedWith`/`ApplyTo`，四个边界函数逐一对应参考规则）；接入**四条写入路径**：`postgres_consolidation.go` 的 `CreateObservation`、`consolidation_scope.go` 的 PG `CreateObservationInScope`、内存 `CreateObservation` 与 `CreateObservationInScope`；`UpdateObservation` 两个后端由「写死 `mentioned_at = now()`」改为 LEAST/GREATEST 合并 | **线上**：同 body（`timestamp:"2016-01-01Z"`、内容含 2015 日期）两侧 **按同一顺序** retain → consolidate 后，`GET .../memories/list` 两侧 observation 行 `date/occurred_start/occurred_end/mentioned_at` **逐字节相同**（修复前本服务 `date:""`、`occurred_*:null`）；两条不同日期事实聚合出 `date=2015-01-01` 与 `date=2018-01-01` 两行，两侧一致。**用例** `TestConsolidatedObservationInheritsSourceBounds`、`TestConsolidatedObservationWithoutDatedSourcesStaysUndated`、`TestObservationUpdateWidensBoundsInsteadOfRestamping`。**证伪**（移除 `ApplyTo`）→ `event_date = <nil>, want the earliest source date 2015-01-01 …` FAIL |
| 跨单元 · webhook delivery 的 `cursor` 接受本服务自己解析的 4 种形态 | ❌已修 | `api/http.py:10006-10066` 的 `cursor: str \| None` **不做任何解析**，原样绑 `created_at < $3::timestamptz`（`ops_postgresql.py:1410-1441`）→ asyncpg 对 `str` 绑 timestamptz **必然**抛错 → 每一种取值都 500，**包括参考自己刚吐出的 `next_cursor`**（`:10041`） | `internal/storage/memory_webhooks.go` 的 `parseDeliveryCursor` 改为「任何非空 cursor 一律 `errInvalidDeliveryCursor`」；另抽出 `parseDeliveryTimestamp` 供读行时间戳与 PG 的回退路径使用（不误伤内部时间解析） | 7 种形态（`Z`、`+00:00`、参考自己的空格形态、带毫秒、无时区、date-only、`notadate`）两侧 **全部 SAME（500 同文案）**；修复前其中 4 种本服务 200。**用例** `TestDeliveryCursorIsRefusedInEveryAcceptedForm`、`TestDeliveryWithoutCursorServesTheFirstPage`。**证伪**（恢复旧的四 layout 解析器）→ 4 个子用例 `cursor "…" was accepted; the reference refuses every cursor` FAIL |
| 跨单元 · 同步 retain 写了一条 `async_operations` completed 行 | ❌已修 | `api/http.py:10275` 原句 *"A sync retain has no operation row and no ack, so nothing else can stop it once it starts"*；`engine/memory_engine.py:6430-6800` 的 `retain_batch_async` 无任何 `async_operations` 写入，只有 `async=True` 走 `submit_async_retain` | `internal/storage/postgres_retain.go` 与 `memory_retain.go`：`!req.Async` 时不写 op 行（`SkipOperationRecord` 与 async/worker 路径**未动**，`RetainOutcome.OperationID` 照旧返回） | `GET .../stats` 的 `operations_by_status` 修复前参考 `{"completed":1}` vs 本服务 `{"completed":2}`；修复后同步夹具对上 15 键全同（由 reporting 单元实测确认） |
| 跨单元 · 知识树根序按 (sort_order, name, id) 排，参考只有两键 | ❌已修 | `_knowledge_tree_sort_key`（`engine/memory_engine.py:1945-1949`）= `(sort_order is None, sort_order or 0, name or "")`，**两键无 id**；`rows` 查询**无 ORDER BY**（`:19513-19519`），Python `sorted()` 稳定 → 平局保 fetch 序 | `internal/storage/postgres_knowledge.go`：SQL 去掉 `kp.name`（保留 `kp.sort_order, kp.id` 作为 fetch 序），比较器抽成共享的 `knowledgeTreeLess`（先 `SortOrder` 后 `Title`，**去掉 id 第三键**）；`memory_knowledge.go` 用同一个比较器 | 由 knowledge 单元线上复验：id 序≠name 序的夹具上根序两侧**完全同序**；`#50 export` 无回归。**用例** `TestKnowledgeNodesOrderBySortOrderThenName`、`TestKnowledgeNodesOrderPutsSortOrderFirst`。**证伪**（比较器 name 键换成 id 键）→ `titles = [Zulu Alpha Mike], want [Alpha Mike Zulu] (name order, not identifier order)` FAIL |
| 跨单元 · `GET /v1/default/banks` 的 `limit`/`offset` 走 `strconv.Atoi` 而非框架文法 | ❌已修 | 参考声明为裸 `int`, 走框架整数文法：`1_0`→10、`5.0`→5、`" 1 "`→1；负数超 int64 是 `ge` 违例而非解析失败；响应回显**解析后**的值（`?limit=1_0` → `"limit":10`） | `internal/bankapi/banks.go` 的 `parseNonNegativeParam` 改走 `httpx.ParseQueryInt` | 9 种形态两侧全 SAME（`1_0`/`5.0`/`" 1 "`/`+5`/`5.`/`abc`/重复参数/`0`/`-1`）。**用例** `TestBankListIntegerGrammarIsTheFramework`。**证伪**（换回 `strconv.Atoi`）→ `limit=1_0: status = 422, want 200` FAIL |
| 跨单元 · 路径里的 `%2F`（转义斜杠）被 Go mux 留在路径参数内 | ❌已修 | 参考的 router 按**解码后**的 path 匹配，`%2F` 因此成为分隔符、单段参数无法容纳 → **所有** member 路由都回框架的 `404 {"detail":"Not Found"}` | `internal/httpx/router.go`：`rawPathMiddleware` 在检测到 `%2F`/`%2f` 时把 `URL.Path` 的转义斜杠还原成真斜杠并清空 `RawPath`，让 mux 按两段匹配（matches no route） | 10 条 member 路由（documents/mental-models/directives/entities/memories/operations/knowledge pages/nodes/webhooks/aliases）的 `…/a%2Fb`：修复前本服务 400/404-文案/500 各异，修复后**全部 404** 与参考同。**用例** `TestEscapedSlashIsAPathSeparator`、`TestUnexescapedAndOtherEscapesStillRoute`。**证伪** → `the escaped slash reached the handler as "a/b"; it must split the path` FAIL |
| 跨单元 · `httpx.ParseQueryInt` 对无上界的超 int64 值回 422 | ❌已修（部分不可复刻，见下） | 参考把 `offset` 声明为裸 `int`（只有 `ge=0`），**没有上界可违反** → 把 Python 无界整数直接交给 asyncpg → 500 `invalid input for query argument $n: … (value out of int64 range)` | `internal/httpx/queryint.go`：无上界时饱和到 `MaxInt` 并继续交给存储层（原先返回 422 `int_parsing`，是参考任何路由都不发的信封）；有上界时仍按 `le` 正确拒绝 | 12 条无上界路由（documents/tags/entities/mental-models/operations/observations-scopes/audit-logs/llm-requests/webhooks…）的 `?offset=9223372036854775808`：修复前本服务 **422**、参考 500/200；修复后本服务对页外 offset 与参考**同为 200 同体**。**用例** `TestParseQueryIntWithoutUpperBoundSaturatesPastInt64`、`TestParseQueryIntWithUpperBoundRefusesPastInt64`。**证伪**（恢复旧的 `TypeMismatch("int_parsing")`）→ `"9223372036854775808" was refused as "int_parsing", want it carried past int64 to the store` FAIL |
| 跨单元 · `POST .../prompts/preview` 的 `null` body 被当成空体放行（200） | ❌已修 | 参考对「无 body」与「body 是 `null`」都给 `{"type":"missing","loc":["body"],"msg":"Field required","input":null}`（实测） | `internal/reporting/memory_routes.go` 的 `PromptPreview`：`null` 与空体一并按 `httpx.ErrEmptyBody` 处理 | 实测 `null`/无 body 两侧 SAME。**用例** `TestPromptPreviewBodyShapesMatchTheReference`/`null` 子例。**证伪** → `status = 200, want 422` |
| 跨单元 · `POST .../prompts/preview` 非对象 body 回 `dict_type` | ❌已修 | 参考 `body: Model` 对非对象给 `model_attributes_type` "Input should be a valid dictionary or object to extract fields from"（实测 `[]`/`5`/`"x"`） | 新增导出 `httpx.ModelAttributesFailure`（复用 `body.go` 内既有的 `modelAttributesType`），预览路由在 parse 前判定 | `[]`/`5`/`"x"` 两侧 SAME（修复前本服务 `dict_type` "Input should be a valid dictionary"）。**证伪** → `body = {"type":"dict_type",…}, want it to contain "model_attributes_type"` FAIL |

### 2. 已确认不可复刻 / 如实记录的残余

| 主题 | 判定 | 依据 |
|---|---|---|
| 无上界参数的**超 int64** 值（`?offset=9223372036854775808`） | ⚠️存疑（驱动层不可复刻） | Go 的 `int64` 没有「装不下的整数」，本服务饱和后按页外 offset 处理 → 200；参考是 asyncpg 绑定失败 → 500。两侧对**范围内**的页外 offset（`9223372036854775807`、`4294967296`）**同值同体**。属 `docs/CONTRACT-AUDIT.md` §5 item 6「驱动错误」已接受类 |
| `/metrics` 的 Python 平台家族（`python_gc_*`/`python_info`/`process_*`） | ⚠️不可复刻 | CPython 解释器/进程指标，Go 进程无法产生；`PARITY-OPEN-ITEMS.md` 7.6 已判不修 |

### 3. 组长复核的既有结论（不照抄，逐条复验）

| 既有结论 | 复核结果 |
|---|---|
| 第二轮上报「`internal/httpx` 的 `;` 未转义会被 `url.Query()` 丢弃」 | ✅ 已修、仍成立：`?limit=5;6` 两侧同为 422 `int_parsing input:"5;6"`；16 种整数形态（`+5`/`1_000`/`5.0`/`5.`/`.5`/`5e2`/`0x10`/`__`/空值/重复参数）两侧全 SAME |
| `api_version` 与 `/version` 的 11 个 feature 键序 | ✅ 仍成立：`/version` 与 `/v1/version` 两侧 `--raw` 逐字节相同 |
| 路由层边界（尾斜杠 307、错误方法 405、`//health` 404、未知查询参数 → `X-Ignored-Params`） | ✅ 逐条实测 SAME（6 条路径） |
| 第二轮「开启 `bank_llm_health` 后未知 bank → 404」 | ❌ **该结论是错的**，已由 reporting 单元改判为 200（参考 `check_bank_llm` 无 `_require_bank_exists`），根因多半是 `POST /webhooks` 的惰性建行污染了那个"未知"银行 |
| 第二轮「`/v1/default/banks/{id}` 的 GET 落到 405」 | ✅ 仍成立 |

### 4. 组长复核：一次完整的差分基线

`scripts/parity_probe.py --run-id leadprobe`（修复后重建实例）：**209 checks: 202 identical, 0 differing, 7 known**
（7 项为 `KNOWN_DIFFERENCES` 里早已记录的非契约差异：`/health` 的活测量、驱动错误文案、Python set 迭代序、模型输出文本）。
修复前同一探针为 `202 identical / 0 differing / 7 known`，说明本轮修复**没有引入回归**。

### 5. 组长抽查：20 条读路由 + 5 条写路由 + 5 条 404 路径

对 20 条读路由（banks/mental-models/directives/knowledge tree & export/documents/tags/entities graph/graph/stats/timeseries/observation scopes/operations/webhooks/audit-logs/llm-requests/memories list/export/profile/bank-template-schema）、5 条写路由（mental-model create、directive create、knowledge folder create、retain、bank PUT）、5 条不存在资源路径（operation/mm/directive/kb page/chunk）逐条实测：**全部 SAME**。

### 6. 收尾：全仓验证门与覆盖统计

**最终验证门（全部通过）**：

| 门 | 结果 |
|---|---|
| `CGO_ENABLED=0 go build ./...` | 通过 |
| `CGO_ENABLED=0 go build -tags pgx ./...` | 通过（**pgx 标签不可省**） |
| `go vet ./...` | 无输出 |
| `gofmt -l .` | 空 |
| `golangci-lint run ./...` | **No issues found** |
| `go test -count=1 ./...` | **4334 passed / 50 packages** |
| `scripts/parity_probe.py`（对线上参考） | **209 checks: 202 identical, 0 differing, 7 known** |

**按 API 的覆盖统计（100 条路由）**：

| 结论 | 条数 | 说明 |
|---|---|---|
| §6 表格行结论 | ✅ **38** / ❌ **28** / ⚠️ **4** | 按表格行计（同一 API 可能有主行 + 子项行），52 条 API 已开单元 |
| 其中 ⛔ 未覆盖 | **0** | 开单元的 52 条 API 中没有一条是 ⛔ |
| **本轮未开单元的 API** | **48** | 见下，**不等于"无差异"** |

**48 条本轮未逐条开单元**（#19/#20/#26–#46/#53–#77）—— 诚实说明：它们**不是"没差异"的证明**，而是本轮
**没有**做单 API 取证明细。其中一部分已由别的手段覆盖：

- **探针覆盖**（`scripts/parity_probe.py` 的 209 项，202 identical / 0 differing）覆盖了 recall、reflect、
  dry-run、prompts preview、memory CRUD、observations、consolidation、operations、mental models、directives、
  knowledge search、chunks、documents、entity graph 等**成功/校验/404/分页**的主干路径。
- **组长抽查**（第 5 节）覆盖了 20 条读路由 + 5 条写路由 + 5 条 404 路径（含 `#58` graph、`#61`/`#62` entities、
  `#65` documents、`#71`–`#73` operations、`#53`–`#55` directives、`#45` tree）。
- 但 `#26`–`#29`（memory CRUD 的细节分支）、`#30`–`#37`（observations/consolidation 的边界）、
  `#38`–`#44`（mental models 的 refresh/clear/patch 分支）、`#59`/`#60`/`#63`/`#64`/`#66`–`#70`（read-surface 的
  稀有分支）、`#76`/`#77`（operations retry/delete）**没有**本轮的逐条结论。

**下一轮建议**：把这 48 条按 `routes.md` 的单元划分继续开单元（`recall-reflect`、`memory-crud`、`observations`、
`mental-models`、`directives`、`read-surface`、`operations`），沿用本轮的方法与工具（`docs/parity-round/PLAN.md`
与 `/tmp/rt/chk.py` 的产物可复用；`chk.py` 的源码在最终报告里有记录，可重建）。

**本轮九个单元小节的实际产出**（本节 + 八个业务单元，供下一轮接手）：

- `## 第三轮 monitor`（#1–#5）：`/metrics` 缺 `Content-Length`；stall 时长直方图族缺失；直方图族按 map 序渲染
- `## 第三轮 bank-lifecycle`（#6–#13）：`#6` 的整数文法（本节修）
- `## 第三轮 bank-config`（#14–#18）：`#14`–`#18` 全部一致
- `## 第三轮 retain`（#19–#23）：vision 降级 422、content union 三处、append 缺 document_id、`entity_labels` 校验、`/files/retain` 请求报告不全、未知 bank 建行
- `## 第三轮 knowledge`（#45–#52）：空 `source_query` 刷新永久失败；知识页 create 体校验三层根因；page title 空串被丢；路径参数被 trim
- `## 第三轮 transfer`（#78–#87）：归档格式 6 类结构差异 F1–F6 + 3 处内容修正；模板版本任意精度；文档导出键与归档类型
- `## 第三轮 webhooks`（#88–#92）：deliveries 负 limit、cursor 一律 500
- `## 第三轮 reporting`（#93–#100）：同步 retain 多写 op 行、`#94` 未知 bank 应 200、`llm_requests` 五列形状

**本节结论计数**：

- 路由表面：**100/100 齐全**（0 缺口）
- 本轮组长修复的跨单元缺陷：**9 处**（全部带证伪）
- 不可复刻/如实记录：**2 类**
- 推翻的既有结论：**1 条**（`#94` 开启态未知 bank）
- 最终验证门：**全绿**（4334 tests / 0 lint / 0 gofmt / 2 种 build / 复盘探针 0 differing）

## 第三轮 monitor

单元：`internal/monitoring/`（API #1–#5）。参考检出 `/code/hindsight/hindsight-api-slim/hindsight_api/`，行号以当前检出为准。线上差分：参考 `http://127.0.0.1:8888`，本服务 `scripts/parity_dev_service.sh 18310`（`CHK_RUN=g-18310`，夹具银行 `rt-g-18310*`）。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 1 `GET /health` | ❌已修 | 装饰器 `api/http.py:5650`；共享体 `api/http.py:5644-5654`（`health_check()` 原样返回，`status=="healthy"` → 200 否则 503）；体 `engine/memory_engine.py:6179-6215`；池统计 `:6218-6235` | `internal/monitoring/health.go:123-173`（`Ready`/`readyBody`）、`HealthBody :92-113` | 见下方「`/metrics` 族集」一行的同一处改动 | `chk GET /health` 仅 `db_pool_idle` 不同（活测量，见「噪声」） |
| 2 `GET /health/ready` | ❌已修 | 同 #1，`api/http.py:5667` 与 `/health` 共用 `_readiness_response()` | 同 #1 | 同 #1 | `chk GET /health/ready` 仅 `db_pool_idle` 不同 |
| 3 `GET /health/live` | ✅一致 | `api/http.py:5684` → `liveness_response()`；`liveness.py:34-36`（`round(…,1)`）、`:39-54`（`status`/`version`/`uptime_seconds` 三键）、`:31`（`_PROCESS_START` 取模块导入时刻） | `internal/monitoring/health.go:187-207`（`Live`、`Float` 保 `.0`、`round1` = ROUND_HALF_EVEN） | 无 | `chk GET /health/live` → **SAME**（`uptime_seconds` 为活测量）；`HEAD`/`POST`/`GET /health/live/`/`GET /health//live` 与方法/尾斜杠行为两侧逐条 **SAME** |
| 4 `GET /version` | ✅一致 | `api/http.py:5703`、`:5716`（`/v1/version`，`include_in_schema=False`）；`VersionResponse api/http.py:4854`；`FeaturesInfo` 字段序 `:4828-4844`（11 个 flag）；`:5730-5746` 逐键赋值；`__init__.py:43` `__version__ = "1.0.0"` | `internal/monitoring/version.go:23-110`（手写序列化按 `config.FeatureKeys` 声明序）、`internal/config/config.go:928-940` | 无 | `chk GET /version` / `GET /v1/version` → **SAME**（两侧键序逐位相同；`features.{worker,audit_log,llm_trace,bank_llm_health}` 是部署自设项）；`GET /version/`、`OPTIONS /version`、`HEAD /version` **SAME** |
| 5 `GET /metrics`（响应头/长度） | ❌已修 | 装饰器 `api/http.py:5748`；`:5759-5772` `Response(content=…, media_type=CONTENT_TYPE_LATEST)` —— 参考交给框架的是一整个 bytes，响应带 `Content-Length` | `internal/monitoring/metrics.go`（原 `Serve` 直接 `w.Write(Render()/Exposition())`，Go 无长度时改用 chunked） | `Serve` 先渲染到缓冲、显式写 `Content-Length` 再写体。原实现两侧 `content-type` 相同但**本服务缺 `content-length`**（`headers-only` DIFF） | `TestMetricsResponseDeclaresItsLength`（**已证伪**：去掉 `Content-Length` 头 → `Content-Length = "", want 277` FAIL，恢复即过）；线上 `chk GET /metrics --headers-only` 修后 **SAME** |
| 5 `GET /metrics`（stall 直方图族缺失） | ❌已修 | `metrics.py:623-632`：`event_loop_stalls` counter 与 `event_loop_stall_duration` histogram **同一个 collector 里一起创建**，于是空载暴露体里两个族都在（`…stalls_total 0.0` 与 `…stall_duration_seconds_bucket{le=…} 0.0`） | `internal/monitoring/workload.go:352-355`（`declareLoopStall` 原先只声明 counter） | `declareLoopStall` 同时 `observe(…, 0)` 声明 duration 直方图（用 SDK 默认桶，与参考同）。修复前本服务**整族缺失**（实测参考独有族 22 → 21 中这一族是机械缺失，不是"需要流量"） | `TestWorkloadLoopStallDurationDeclaredAtZero` + `TestLoopLagProbeDeclaresStallCounter`（**已证伪**：删掉那一行 observe → `histogram family "wenyuan_event_loop_stall_duration_seconds" not found in []` FAIL）；线上修复后 `GET /metrics` 该族出现，且**只剩 9 个参考独有族**，都是需要本机流量的（`wenyuan_llm_*` 5 个、`wenyuan_retain_*` 3 个、`wenyuan_event_loop_stall_duration_seconds` 的**样本**在有 stall 时才有） |
| 5 `GET /metrics`（直方图族顺序） | ❌已修 | 参考的 Prometheus exporter 按 **instrument 名排序** 输出族 | `internal/monitoring/metrics.go:239`（`writeHistogramFamilies` 按调用方给的顺序渲染，而调用方顺序来自 map 迭代） | 渲染前按 `(name, 标签集)` 稳定排序 | `TestMetricsHistogramFamiliesRenderInNameOrder`（**已证伪**：去掉排序 → `histogram families not in name order (alpha=818 mid=1365 zeta=277)` FAIL） |
| 5 `GET /metrics`（族集/桶集核对） | ✅一致 | 15 个共有族的 `# HELP` / `# TYPE` 逐字节相同；桶边界集合按族比对 4/4 相同 | `internal/monitoring/metrics.go`、`workload.go`、`looplag.go` | 无 | 脚本化比对：共有族 HELP/TYPE 0 diff；`hindsight_*` 名称集在跑过一次 recall+retain 后**本服务独有 0 个**；参考独有只剩 9 个流量族（见上） |

### monitor 单元的分歧说明（不计入缺陷）

- **`db_pool_idle` / `db_pool_size` 两侧不同**（实测 2–3 vs 4–9）：参考的 `_pool_stats` 是 `in_use = pool.get_size() - idle`（`engine/db/postgresql.py:334-345`），asyncpg 的池会保活 `min_size` 连接；本服务读 `database/sql` 的 `DBStats{InUse, Max, Idle}`（`internal/storage/driver_pgx.go:193-196`），连接数随请求惰性增减。这是**两个跑在各自进程里的连接池的实时状态**，属活测量（PLAN §4），不是行为差异：`db_pool_max`、`db_pool_waiting` 两侧同为 `100`/`0`，键集与键序逐位相同。
- **`GZip` / `Accept-Encoding: gzip`**：两侧都回 `content-encoding: gzip`，`headers-only` **SAME**。
- **`/debug/pprof/*`**：参考无此路由（`api/http.py` 全文无 `pprof`），本服务仅在 `WENYUAN_API_PPROF_ENABLED=true` 时挂载，默认两侧均 404 → 第一轮结论成立。

### 需组长裁决（跨作用域）

1. **`wenyuan_operation_*` 的 `source="api"` 系列**：参考在 HTTP handler 层包裹 `record_operation`（`api/http.py:6243`/`:6442`/`:10279`），本服务 `internal/memoryops/operation_metrics.go:40-50` 已在 recall/reflect/retain 里记同名系列（**第二轮结论说的"只有 worker 一处调用"已过时**）。本轮实测：跑一次 recall 后两侧 `wenyuan_operation_operations_total{source="api"}` 都出现、都 +1 → 该缺口已闭合，无需再改。
2. **`wenyuan_event_loop_stall_duration_seconds` 的样本**：参考侧有 3 次真实 stall（共享库上的存量流量），本服务空载只有零值声明。要样本必须在本服务的 loop 上真制造停顿，属压力测试，本轮不覆盖（判**噪声**，两侧族都在）。

**本单元结论计数（按 API）：✅ 2 / ❌ 3 / ⚠️ 0 / ⛔ 0。**


## 第三轮 bank-lifecycle

单元：`internal/bankapi/`（API #6–#13）。线上差分：参考 `8888`，本服务 `18310`，夹具 `rt-g-18310` / `rt-other-g-18310` / `rt-ghost-g-18310`（+ `rt-*-g-18310-k*`）。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 6 `GET /v1/default/banks` | ⚠️存疑 | `api/http.py:6593-6618`（`q`/`limit=100 ge=0`/`offset=0 ge=0` 全是 `Query(...)`，框架先解析）；引擎 `engine/memory_engine.py:15068-15180`（`limit/offset = max(x,0)`，**页切片** `banks[offset:offset+limit]`，`total` = 切片前计数）；排序 `engine/retain/bank_utils.py:680-808`（按 `last_write_at or created_at`, 降序） | `internal/bankapi/banks.go:48-91`（`ListBanks`）、`:373-450`（`parseBankQuery`/`parseNonNegativeParam`） | **未改**：参数是 `Query(...)` 由**框架**解析，本服务的等价实现是 `internal/httpx`（组长作用域）。见下方「跨作用域清单」第 1 条 | 夹具范围内（`?q=rt-g`）全部 **SAME**：`limit=0/-1/abc/1.5/5./.5/5e2/1__0/0x10/100000/99999999999999999999/-99999999999999999999`、`offset=-1/9223372036854775807/9223372036854775808`、`?q=`、重复参数 `?limit=1&limit=2`、`?limit=abc&limit=1`。**两侧不一致（跨作用域）**：`limit=1_0`、`limit= 5`/`5 `、`limit=+5`、`limit=5.0`（参考 200 回解析后的整数，本服务 `?limit=1_0`/`5.0` → 422 `int_parsing`）；`limit=-99999999999999999999`（参考 422 `greater_than_equal`，本服务 422 `int_parsing`）；`q=`（本服务把空 `q` 当"未给"，参考当"给了空串"，两侧结果不同，见清单第 1 条） |
| 7 `GET .../aliases` | ✅一致 | `api/http.py:8476-8497`（`_alias_response` 回**整份**列表）；`engine/bank_aliases.py:168-183`（`ORDER BY is_primary DESC, created_at, alias`） | `internal/bankapi/aliases.go:62-75`、`ToAliasResponse` | 无 | `chk GET .../aliases` → **SAME**（空列表 / 以地址 id 寻址 / `bank_id` 回显规范 id）；`GET .../aliases` 于 ghost 银行 → 两侧同 404 |
| 8 `POST .../aliases` | ✅一致 | `api/http.py:8499-8528`（201 + 整份列表）；`engine/bank_aliases.py:196-244`（`validate_new_bank_id` → 400 三条、自别名 400、`WHERE NOT EXISTS` + `ON CONFLICT` → 409） | `internal/bankapi/aliases.go:78-157` | 无 | 18 例 **SAME**：`""`→`Bank id must not be empty`、193/192 字节、`\n`/`\t`/`\u007f`/`\u0000`/`\u0080`→`must not contain control characters`、`\u00a0`（非 `Cc`）→201、自别名→400、占用名→409、`{"primary":null}`→`bool_type`、缺 `alias`→`missing`、未知银行→404 |
| 9 `PATCH .../aliases/{alias}` | ✅一致 | `api/http.py:8530-8563`（未命中 → 404 `Bank '<id>' has no alias '<alias>'`）；`engine/bank_aliases.py:245-275`（同事务内先降级其余再置位） | `internal/bankapi/aliases.go:160-186` | 无 | 升/降主别名后整份列表两侧同体（`display_alias` 变化一致）；未知别名 → 404 同体；**顺序**：体非法压过 404（`PATCH .../nope/aliases/x {"primary":"bad"}` → 422 `bool_parsing`）两侧 **SAME** |
| 10 `DELETE .../aliases/{alias}` | ✅一致 | `api/http.py:8565-8591`；`engine/bank_aliases.py:276-284`（`DELETE … WHERE alias=$1 AND bank_id=$2`） | `internal/bankapi/aliases.go:238-252` | 无 | 命中 → 200 + 列表收缩；未命中 → 404 `Bank '<bank>' has no alias '<alias>'`；银行不存在 → 404 `Bank '<id>' not found` —— 全 **SAME** |
| 11 `PUT .../{bank_id}` | ✅一致 | `api/http.py:8593-8636`；`engine/memory_engine.py:21641-21785`（`update_bank(create_if_missing=True)`：先校验后建行；`_ensure_bank_exists` → `create_bank_row_on_conn`，`validate_new_bank_id` + 别名冲突 409）；模型 `CreateBankRequest api/http.py:1993-2100`、`get_config_updates :2101-2145`（`reflect_mission > mission > background`） | `internal/bankapi/banks.go:129-209`（`ReplaceBank`）、`decode.go`、`validation.go` | 无 | 15 例体校验 **SAME**（`notjson`/`[]`/`"x"`/空/`null`/`name:123`/`disposition:"bad"`→`model_attributes_type`/`disposition.skepticism:9`/`retain_chunk_size:5.5`→`int_from_float`/`enable_reranking:"bad"`/未知键）；创建与覆盖的成功路径两侧同体；别名遮蔽（`rt-ghost` 已是 `rt-other` 的别名时 `PUT rt-ghost {}` → 409）**SAME** |
| 12 `PATCH .../{bank_id}` | ✅一致 | `api/http.py:8638-8682`（`create_if_missing=False`，银行不存在 404 在**体校验之后**）；`engine/memory_engine.py:21641-21760` | `internal/bankapi/banks.go:217-247` | 无 | 9 例 **SAME**（含 `{"mission":"a","background":"b"}` 优先级、`{"disposition":{...},"disposition_empathy":5}` 逐字段优先、`{}`、未知银行 `{}`→404、未知银行 `{"name":123}`→422） |
| 13 `DELETE .../{bank_id}` | ✅一致 | `api/http.py:8684-8706`（`deleted_count = memory_units + entities + documents`）；`engine/memory_engine.py:11377-…` | `internal/bankapi/banks.go:250-272` | 无 | 命中 → `{"success":true,"message":"Bank '<id>' and all associated data deleted successfully","deleted_count":N}`；**从不存在的银行 → 200 `deleted_count:0`**；删两次同体；删后别名地址 → 404 —— 全 **SAME** |
| 别名解析（全部 bank 作用域路由） | ✅一致 | `engine/bank_aliases.py:88-122`（`resolve`：真实银行永远优先，其次别名，未知 id 原样返回）；改写发生在处理器**之前**（`api/unknown_params.py`） | `internal/httpx/router.go:490-510`（`withBankResolution`）、`internal/storage/postgres_banks.go` 的 `ResolveBankID` | 无 | 以别名寻址 PUT/PATCH/DELETE/aliases 与规范 id 等价；删银行后遗留别名行 → PATCH 404、PUT 重建 —— 两侧一致 |
| 方法/尾斜杠 | ✅一致 | FastAPI `redirect_slashes` + 405 信封 | `internal/httpx` 路由 | 无 | `GET /v1/default/banks`（405）、`HEAD`（405）、`POST`（405）、`DELETE` 集合（405）、`OPTIONS .../{bank_id}`（405）、`GET .../{bank_id}`（405）、`GET /v1/default/banks/`（200，尾斜杠）、`PUT /v1/default/banks/`（307）—— 全 **SAME**（307 的 `location` 只是 host:port 不同） |

### 跨作用域清单（bank-lifecycle → 组长，`internal/httpx`）

1. **query 整数的 Python 文法**（影响 #6 以及所有用 `int` 查询参数的路由）：本服务 `internal/bankapi/banks.go:424-450` 的 `parseNonNegativeParam` 用 `strconv.Atoi(strings.TrimSpace(raw))` —— 这已经覆盖 ` 5`/`+5` 两种，但**不接受** `1_0`、`5.0`（Python `int()` 接受）、且把 `-99999999999999999999` 报成 `int_parsing` 而参考是 `greater_than_equal`。`internal/httpx/queryint.go` 的 `ParseQueryInt` 已实现正确的文法（`LaxInteger` + `BigintSign`），bankapi 未改用它（`banks.go` 用它自己的解析，`echoedPageParam` 也要独立回显）。**建议**：把 `parseBankQuery` 换成 `httpx.ParseQueryInt`（`min=0, max=0`），回显改用解析后的整数（参考 `/v1/default/banks` 的 `BankListResponse.limit/offset` 是 `int`，**不再原样回显**：实测 `?limit=1_0` 参考回 `"limit": 10`）。**证据**：`chk GET '/v1/default/banks?q=rt-g&limit=1_0'` 参考 200/`limit: 10`，本服务 422 `int_parsing`；`?limit=5.0`、`?limit=+5`、`?limit=%205` 同理；`?limit=-99999999999999999999` 两侧都 422 但 `type` 不同。
2. **`q=` 空值**：参考 `search_query` 是 `Query(None)`，`?q=` 解析成 `""` → `if search_query:` 为假 → **不筛选**；本服务 `parseBankQuery` 把"给了空 `q`"当筛选（`SearchGiven`），于是 `?q=` 走 `ILIKE '%%'`（结果相同）但 `matched_aliases` 的计算路径不同。实测 `?q=` 在**非夹具范围**里两侧 `total` 不同（数据库不同，噪声），夹具范围内无差异，故判 `⚠️` 而非 `❌`。
3. **`;` 分隔的 query**：实测 `GET /v1/default/banks?limit=5;6` 与 `?limit=5%3B6` 两侧 **SAME**（都 422 `int_parsing`，`input:"5;6"`）—— 本服务经 `httpx.QueryPresent` 读原始串，未走 Go `url.Query()` 的分号丢弃缺陷。**第二轮上报的 `;` 缺陷在本单元的路由上不可复现**（`internal/bankapi` 用的是 `httpx.Query*` 而不是 `url.Query()`）。

**本单元结论计数（按 API）：✅ 7 / ❌ 0 / ⚠️ 1 / ⛔ 0。**


## 第三轮 bank-config

单元：`internal/bankapi/config*.go`、`internal/banktemplate/`（API #14–#18；`#19/#20 PATCH/DELETE .../config` 归 retain 单元，本单元**未碰**）。`internal/banktemplate/` 本轮已划给 transfer 单元，本单元**只读不写**。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 14 `GET .../profile` | ✅一致 | `api/http.py:8411-8423`：handler 体**第一步**就 `raise HTTPException(410, _PROFILE_RETIRED_DETAIL)`，路由无请求体、无查询参数 | `internal/bankapi/retired_routes.go:30-36`（`RetiredRoutes`）、`internal/httpx/errors.go` 的 `DetailRetiredProfile` | 无 | `chk GET .../profile`（无体）→ **SAME**；`GET .../profile` 带体（服务端仍忽略）→ **SAME**；未知银行 `?` 也同 410（参考不查银行存在性） |
| 15 `PUT .../profile` | ✅一致 | `api/http.py:8424-8438`（`request: UpdateDispositionRequest`，框架**先校验体**再进 handler → 合格才 410）；模型 `api/http.py:1856-1859`、`DispositionTraits :1769-1774`（三个 int `ge=1 le=5` 全必填） | `internal/bankapi/retired_body.go`（`TestRetiredProfileWriteValidatesItsBodyBeforeTheGoneStatus`） | 无（第二轮已修，本轮复验无回退） | **带合法体**：`{"traits":{…}}`、`{"disposition":{…}}`、`{"mission":"x"}` → 两侧同 410 同 detail（**SAME**）；非法/缺字段体 → 两侧同 422（判据见第二轮，本轮抽测 `{"mission":"x"}` 与空体 → **SAME**） |
| 16 `POST .../background` | ✅一致 | `api/http.py:8439-8468`（`AddBackgroundRequest`：`content` 必填、`update_disposition: bool = True`；held 410 前先校验） | 同 #15 | 无 | **带合法体** `{"content":"x"}`、`{"content":"x","update_disposition":false}` → 两侧同 410 同 detail（**SAME**）；空体 → 两侧同 422 |
| 17 `GET /v1/bank-template-schema` | ✅一致 | `api/http.py:9450-9461`：直接返回 `BankTemplateManifest.model_json_schema()`（裸 dict，无 `response_model`） | `internal/reporting/knowledge_schema.go:11-39`（嵌入参考原样生成的 `bank_template_schema.json`）、路由 `internal/reporting/handlers.go:118` | 无（`internal/banktemplate/` 本轮只读） | `chk GET /v1/bank-template-schema --raw` → **SAME**（**逐字节相同**，两侧同为 28998 字节）；第二轮记录的唯一差异（`$defs` 键序）在本轮实测中**已不可见**（本服务输出的就是嵌入的原字节，`--raw` 直接 SAME） |
| 18 `GET .../config` | ✅一致 | `api/http.py:9612-9640` → `config_resolver.py:267-330`（`resolve_full_config` + `_public_fields_of`）；层叠 `:228-262`（tenant → bank → global） | `internal/bankapi/config.go:17-30`（`GetBankConfig` → `validateBankExists`）、`:143-167`、`:295-297`（`config.ResolveLayers`） | 无（第二轮已修 `entity_labels` 读回键序，本轮复验无回退） | `chk GET .../config` 于有配置的银行 → **SAME**（50 键、键集与取值逐字节相同）；未知银行 → 两侧同 404 同 detail |
| `PATCH/DELETE .../config`（#19/#20） | — | — | — | **不在本单元**（`internal/bankapi/config*.go` 由本单元与 retain 共用，但该两条 API 的结论归 retain 单元） | 未核对，避免与他人重复 |

**本单元结论计数（按 API #14–#18）：✅ 5 / ❌ 0 / ⚠️ 0 / ⛔ 0。**


## 第三轮 knowledge

单元：`internal/reporting/knowledge*.go`（API #45–#52）。#45/#46（`PATCH`/`DELETE .../mental-models/{id}`）的结论在第二轮 `mental-models` 小节（同事负责 `internal/mentalmodels/`），本单元不重复核对；`#53/#54`（`knowledge-base/nodes/{id}`）归 directives 单元。线上差分：参考 `8888`，本服务 `18310`（`PARITY_DEV_WORKER_ENABLED=true`，知识页的内容/历史由 worker 产出），夹具 `rt-other-g-18310`、`rt-g-18310-k1…k30`。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 45 `PATCH .../mental-models/{mental_model_id}` | — | `api/http.py:7219-7261` | `internal/mentalmodels/handlers.go` | 不在本单元作用域 | 见第二轮 `mental-models` 小节（同事已判 ✅并修 3 处 trigger 合并语义） |
| 46 `DELETE .../mental-models/{mental_model_id}` | — | `api/http.py:7262-7299` | 同上 | 同上 | 同上 |
| 47 `GET .../knowledge-base/tree` | ❌已修 | `api/http.py:7302-7329`（`tag_filter = Depends(...)` 先于 handler）；`engine/memory_engine.py:19470-19543`：行查询**无 ORDER BY**（`:19513-19519`），排序在 Python 里按 `_knowledge_tree_sort_key`（`:1945-1949`）= `(sort_order is None, sort_order or 0, name or "")`，**两键、无 id**；`_build_knowledge_tree` `api/http.py:3721-3733` | `internal/reporting/knowledge.go:31-86`（`KnowledgeTree`）、`internal/reporting/knowledge_projection.go:15-39`（`nestKnowledge`）；**排序在存储层**：`internal/storage/postgres_knowledge.go` 的 `ListKnowledgeNodes` | **不在本单元**：`internal/storage` 是组长作用域。本轮定位并用 `rt-g-18310-k8/k9/k14` 三组夹具实证：**id 序≠name 序时**，本服务按 `kf-<hex>` 的 **id 升序**输出根、参考按**字典序**输出（实测 k8：参考 `F12276,F13492,F14671,F17926,F20092,F7354`，本服务 `F14423,F19177,F23541,F24971,F28142,F28272`）。**组长已按本报告定位修复**（`ORDER BY kp.sort_order, kp.id` 只给 fetch 一个稳定基础 + 共享比较器 `knowledgeTreeLess` 两键排序） | 修后：`k9/k1/k4/k6/k10/k14` 的 `chk GET .../tree` → **SAME**；组长的用例 `TestKnowledgeNodesOrderBySortOrderThenName`、`TestKnowledgeNodesOrderPutsSortOrderFirst`（已证伪）；夹具范围内 `tags=team`/`tags_match=exact/all/bogus`/`tag_groups`（含 fuzzy）/未知银行 → **SAME** |
| 48 `POST .../knowledge-base/folders` | ❌已修 | `api/http.py:7332-7358`（201）；`engine/memory_engine.py:19277-19322`（父不存在 400 `Parent folder '<id>' not found`、父不是 folder 400 `Parent '<id>' is not a folder`） | `internal/reporting/knowledge.go:93-141`（`createKnowledgeNode` → `decodeKnowledgeCreateBody`） | **体校验两处**（见下方「create 体校验」一行） | 6 例体校验 + 成功路径 → **SAME**：`{}`→`missing name`、`{"name":null}`、`{"parent_id":5}` → `missing name, string_type parent_id`、`{"name":"f","parent_id":5}` → `string_type parent_id`、`{"parent_id":""}`→201、未知键忽略、`parent_id:"nope"`→400 |
| 49 `POST .../knowledge-base/pages` | ❌已修 | `api/http.py:7361-7411`（201；`node is None` → 409 `A page named '<name>' already exists in this folder`）；模型 `CreatePageRequest`（`name`、`source_query` 必填；`parent_id`/`tags`/`max_tokens`/`trigger` 有默认值） | 同 #48 | **create 体校验**（见下） | 24 例体校验 → **SAME**（见下「create 体校验」一行的清单）；成功路径（含 `source_query:""`、`tags:[]`、`parent_id:null`、重名 409、仅大小写不同的重名 409、`parent_id:"zzz"`→400）→ **SAME** |
| 50 `GET .../knowledge-base/export` | ❌已修 | `api/http.py:7413-7455`；`api/page_markdown.py`：`frontmatter` 只丢 **None** 成员（`""` 照常渲染成 `title: ""`）、`render_index :139-156` **另按 (sort_order, name) 重排一次**、`render_log :158-192`；`engine/memory_engine.py:20137-20210` | `internal/reporting/knowledge_search.go:15-83`、`internal/reporting/knowledge_render.go`（`renderKnowledgeFrontmatter`/`knowledgeDocumentFields`） | ①**空标题被吃掉**：`renderKnowledgeFrontmatter` 把 `""` 当"缺失"丢键，参考只丢 `None` → 空名页面 export 里少了 `title: ""`。改为按 `present` 标志渲染（`knowledgeField.present`、`knowledgeOptional`）。②存储层排序（同 #47，组长改）。 | ①`TestKnowledgeFrontmatterKeepsPresentButEmptyScalars`（**已证伪**：把 `if field.value == "" && !field.present` 改回 `if field.value == ""` → `frontmatter lacks "title: \"\""` FAIL）；线上 `rt-g-18310-k13/k30`（空名页面/正常页面）export → **SAME**。②**存储层排序变更后仍 SAME（无回归）**：`k9/k1/k4/k6/k10/k14/k30` 的 export 全部 **SAME**（index 两侧逐字节相同，因为 `render_index` 自己又按 name 排一次） |
| 51 `GET .../knowledge-base/search` | ✅一致 | `api/http.py:7457-7490`（`q` `min_length=1`、`limit` `ge=1 le=50`、`tag_filter` 依赖**先于** handler）；`engine/memory_engine.py:19602-19780`（`q.strip()` 空 → `[]`；`fetch = min(max(limit*4,40),200)`；RRF `K=60`,`NORM=61`） | `internal/reporting/knowledge_search.go:86-245`、`internal/storage/postgres_knowledge.go:239-406` | 无 | 缺 `q`/`q=`/`q=%20%20%20`/`limit=0/51/abc/-1`/`tags_match=bogus`/未知银行坏参数 → **SAME**；`q=deploy`/`q=Deploy+Runbook`/`q=t1`（大小写）/中文 `q`/`q=No+content`/`limit=1/50` → **SAME**（`score` 是 Python float 渲染、`snippet` 空正文给 `No content yet.`） |
| 52 `GET .../knowledge-base/pages/{page_id}` | ❌已修 | `api/http.py:7492-7518`（`node is None` → 404 `Knowledge page '<id>' not found`）；`api/http.py:3735-3747`（`render_document(notice_when_empty=True)`） | `internal/reporting/knowledge.go:298-330`（`KnowledgePage`） | **路径参数被 trim**：原先 `pageID := strings.TrimSpace(r.PathValue("page_id"))`、`bankID := strings.TrimSpace(...)`。参考把路径参数当**原样字符串**绑定（`:7509` 传入引擎、detail 原样回显），所以 `%20` 是 id 的一部分、四方括号空白也是。改为 verbatim（`internal/reporting/path_param.go` 的 `PathBankID`/`pathID`，同时用于 `KnowledgeTree`/`KnowledgeExport`/`KnowledgeSearch` 的 `bank_id` 与 `UpdateKnowledgeNode`/`DeleteKnowledgeNode` 的 `node_id`） | `TestKnowledgePageIDIsBoundVerbatim`、`TestKnowledgeBankIDIsBoundVerbatim`（**均已证伪**：把 `PathBankID`/`pathID` 改回 `strings.TrimSpace(...)` → `padded page id = 200, want 404` 与 `... tree = 200, want 404` FAIL）；线上 `pages/%20`、`banks/%20rt-ghost-g-18310%20/knowledge-base/{tree,export,search}` → 修后 **SAME**（修前 200 / detail 丢空白）。**残留⚠️**：`pages/a%2Fb` 参考 404 `{"detail":"Not Found"}`（路由不匹配）、本服务 404 `Knowledge page 'a/b' not found` —— 修点在 `internal/httpx` 路由（Go `ServeMux` 解码 `%2F`），见下方清单 |

### create 体校验（#48/#49 的共同修复，`internal/reporting/knowledge_body.go`）

参考的框架在一次校验里把 body 的所有 item 全报出来，顺序由**模型声明序 + pydantic 2 的分阶段规则**决定。实测固定了四条规则，本服务原先一条都没有：

1. **缺席的必填成员后置**：`{"name":"x"}` 只报 `missing source_query`；`{"tags":"a"}` 报 `missing name, missing source_query, list_type tags`（缺席组在**第一个必填成员自己的 item 之后**、其它槽位之前）；`{"source_query":{"a":0}}` 报 `missing name, string_type source_query`；`{"parent_id":5}` 报 `missing name, missing source_query, string_type parent_id`。
2. **有默认值的成员最后**：`tags`/`max_tokens`/`trigger` 的 item 永远排在必填成员与缺席组之后（`{"name":"x","trigger":"a"}` → `missing source_query, model_attributes_type trigger`）。
3. **item 的 `input` 是"成员自己的值"**，只有 `missing` 才是整个 body：`{"name":{"z":1,"a":2}}` 的 `string_type` 回 `{"z":1,"a":2}`（键序按体），而 `missing` 回 `{"name":{"z":1,"a":2}}`。
4. **空数组成员的 item 保留 `[]`**：`{"name":[],"tags":{}}` 的 `input` 是 `[]` 不是 `null`。

根因分三层，全部修掉：
- `internal/httpx/body.go` 的 `orderValue`：空数组建成 **nil slice** → 回显成 `null`。`TestRecordedBodyOrderKeepsAnEmptyList`（**已证伪**：恢复 nil slice → `recorded body = {"name":"x","tags":null,…}, want {"name":"x","tags":[]}` FAIL）。
- `internal/httpx/json.go` 的 `orderValidationInputs`：把**任何**对象型 `input` 都替换成整份 body（成员自己的值也被顶掉）。改为只在 `input` 就是 body（`ValidationFailure.Echo`）时替换，其余按 loc 找记录值、递归保序；并在副本上清 `Echo` 防止二次处理。`TestBodyOrderRewritesOnlyTheWholeBodyEcho`（**已证伪**）。
- `knowledge_body.go` 的顺序/属性规则：`TestKnowledgeCreateItemOrderMatchesTheReference`、`TestKnowledgeCreateReportsMissingMembersLate`（连同已更新的 `TestKnowledgeCreateReportsEveryItemInOnePass` 期望值）。

修复前后对照（`chk POST .../knowledge-base/pages`，24 例）：修复前 5 例 DIFF（`{"parent_id":5}`、`{"name":"x","parent_id":5}`、`{"source_query":{"a":0}}`、`{"name":[],"tags":{}}`、`{"name":{"z":1,"a":2}}`），修后 **24/24 SAME**。

### 连带修复（跨 `internal/app`，本单元发现）—— 空 `source_query` 的心智模型刷新被永久失败

- **参考**：`engine/memory_engine.py:17358` 把 `source_query = mental_model["source_query"]` 原样交给 `:17420` 的 `query=source_query`，**整条路径没有空值守卫**。
- **实测**：两侧同建 `POST .../knowledge-base/pages {"name":"NoQuery","source_query":""}` —— 参考 operation `completed`、`last_refresh_failed_at: null`；本服务 `failed`，`error_message="mental model refresh unavailable: the mental model has no source query to re-run"`，且 `last_refresh_failed_at` 被写上（`#47/#50/#52` 三个读面都可见，且自动刷新被永久 park）。
- **落地**：`internal/app/mental_model_refresh.go`（我的作用域）删掉 `if strings.TrimSpace(model.SourceQuery) == "" { return … }`；配套把两条断言"必须拒绝"的用例改成参考语义：`TestRefreshWithoutSourceQueryIsNotRefused`、`TestRefreshWithoutSourceQueryStillRuns`（**已证伪**：把守卫加回去两条都 FAIL）。修后 `rt-g-18310-k20/k23` 两侧 operation 均 `completed`、树/export **SAME**。

### 已知噪声（不判缺陷）

- `rt-other-g-18310` 上 `export`/`search` 仍 DIFF：两侧的**知识页内容**不同 —— 参考侧页面内容仍是空（`No content yet.`），本服务侧页面被刷新写入了正文并产生 history（多出 `…log.md`）。该夹具在参考侧 worker 于不同时间跑过、且参考页面的 markdown 正文依赖模型输出；用**同一次创建**的新夹具（`rt-g-18310-k30`，同时创建、同时刷新）复核 export/search → **SAME**，故判为**夹具状态噪声**而非行为差异。
- 内容正文是模型产物（PLAN §4）：判据落在形状/键序/文件列表/index 字节上，不逐字比正文。

**本单元结论计数（按 API #47–#52，共 6 条）：✅ 1 / ❌ 4 / ⚠️ 1 / ⛔ 0**（#47 ❌、#48 ❌、#49 ❌、#50 ❌、#51 ✅、#52 ❌ 并带 1 条残留 ⚠️）。#45/#46 归 mental-models 单元、未重复核对。


## 第四轮 recall-reflect

> 单元 `recall-reflect`（#26 `POST .../memories/recall`、#27 `POST .../reflect`）。
> 端口 18402、`CHK_RUN=rr4`、夹具 `rt-rr4` / `rt-other-rr4` / `rt-ghost-rr4`。参考 `http://127.0.0.1:8888`。
> 判据沿 PLAN §2–§4：错误信封逐字节、成功响应逐层形状（键集/键序/取值类型）、排序不变量。
> `results` 的**内容**（UUID、实体名、正文措辞、分数绝对值）不逐字比 —— 两侧是同一个中转模型但每次采样不同，
> 且一侧有 worker 常开（参考事实集合里带 observation）；凡差异只落在内容/条数上一律按 PLAN §4 记为夹具+模型噪声。

### #26 `POST /v1/default/banks/{bank_id}/memories/recall`

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 26 `POST /memories/recall`（`include.chunks` 通道：一次同步 retain 的多个 `items` 被切成**一条**全文 chunk） | ❌已修 | 参考的 retain **逐个 content dict** 切块并**各自从 0 重新编号**：`engine/retain/orchestrator.py:1968-1987`（`for content_idx, content in enumerate(contents): for chunk in fact_extraction.iter_chunks(...)`）、`engine/retain/fact_extraction.py:2985`（同一 content 内 `chunk_index_in_content` 从 0 起）；`engine/retain/orchestrator.py:1455-1470` 记录过同一形态的碰撞（#1888：一个 sub-batch 覆盖另一个，文档只剩一个 chunk 的 chunks/memories）。recall 侧的 `chunks` 映射直接由 `chunk_info.chunk_index`/`chunk_id` 透传（`api/http.py:6326-6337`） | `internal/storage/segment.go`（新增 `segmentsPerContent`）、`internal/storage/memory_retain.go`（`putItemPartsLocked`/`putPartChunksLocked`）、`internal/storage/postgres_retain.go`（`putItemParts`/`putPartChunks`）、`internal/storage/extraction.go`（`extractItemFacts` 按块切片提取）、`internal/storage/models.go`（`RetainItem.Parts`/`ContentParts()`）、`internal/memoryops/retain_fold.go`+`retain_models.go`（fold 保留 `Parts`） | **已修**：一次同步 retain 的多个 `items` 会被 `FoldRetainItems` 折叠成一条 item，而 chunk 写入原先拿**拼接后的 document body** 去 `SegmentWith`，于是 3 个 item 只产出 1 个 192 字符的 chunk 0，回给 recall 的 `chunks` 通道就是整段正文。现每个 content block 单独切块（与参考一致），并**接着前一块的序号**续写（同一 document 的 `chunk_id` 必须唯一）；提取侧的 `fact.ChunkIndex` 同步按块编号，回来后仍指向 fact 自己的那一块 | 三方对照（同一次 `POST .../memories`，3 个 items）见下方「chunks 三方对照」；`TestRetainStoresEachContentPartAsItsOwnChunk`（**改回去就 FAIL**：`expected 3 chunks, got 1`，实测输出见下）；线上 `include:{chunks:{}}` 修前本服务 1 条全文、参考 3 条，修后两侧均 3 条、`chunk_index` 0/1/2、文本逐条相同 |
| 26（`trace:true` 的 trace 形状） | ❌已修 | 参考的 trace 是 `SearchTrace`（`engine/search/trace.py:158-192`、`SearchTracer.finalize` `engine/search/tracer.py:394-437`）：顶层固定 8 个成员 `query`/`retrieval_results`/`rrf_merged`/`reranked`/`entry_points`/`visits`/`summary`/`final_results`；`query` 8 成员（`query_text`/`query_embedding`/`timestamp`/`budget`/`max_tokens`/`tags`/`tags_match`/`temporal_constraint`，缺省值为 `null` 而非省略）；`summary` 是 `total_nodes_visited`/`entry_points_found`/`budget_used`/`budget_remaining`/`total_duration_seconds`/`results_returned`/`phase_metrics`（`phase_metrics` 是**有序 list**，每项 `{phase_name,duration_seconds,details}`）；`retrieval_results[i]` 带 `duration_seconds`+`metadata`（semantic/bm25 为 `{"limit":budget}`，graph/temporal 为 `{"budget":budget}`，`engine/memory_engine.py:9670-9760`）；条目按**fact_type 外层、arm 内层**排列（`experience, world, observation`）；`reranked`/`final_results` 来自**过完 min_scores 的 scored_results**（`engine/memory_engine.py:10060-10076`） | `internal/storage/recall_types.go`（`RecallTraceDetail` 新增 `QueryEmbedding`/`EntryPoints`/`Visits`/`PhaseMetrics`/`FinalResults`/`ArmDurations`/`TotalDurationSeconds`）、`internal/storage/recall_trace.go`（`buildRecallTraceDetail`/`buildTraceEntryPoints`/`buildTraceVisits`/`buildTraceFinalResults`/`attachRecallTracePhases`）、`internal/storage/recall_phases.go`（`snapshot()`）、`internal/memoryops/recall_result.go`（`renderTrace`/`renderTraceQuery`/`renderTraceSummary`/`renderEntryPoints`/`renderVisits`/`renderFinalResults`/`armMetadata`） | **已修**（五处）：①顶层少了 `entry_points`/`visits`/`final_results`，`summary` 里多了参考没有的 `candidates_found`/`stage_counts`、少了 `total_nodes_visited`/`entry_points_found`/`total_duration_seconds`，`phase_metrics` 是 4 键 dict 而非有序 list → 全部按参考重建；②`query` 缺 `query_embedding`/`temporal_constraint`（缺省要 `null`）、`tags` 缺省要 `null`（原先给 `[]`）；③`retrieval_results` 缺 `duration_seconds`/`metadata`；④条目顺序由「arm 外层」改为参考的「fact_type 外层」；⑤`final_results` 缺 `bm25_rank`（原实现只在候选真被 bm25 命中时才给，参考按 `source_ranks` 逐键 `update`，命中即给） | `TestBuildRecallResponseTracePresentWhenRequested`（断言改为参考的 summary 成员集与 `phase_metrics` 为 list）；`TestRecallArmsConcurrentAndSerializedAgree`（比较 trace 时剔除墙钟相位，其余逐字节相等）；线上 `{"query":"Alice","trace":true}` 逐层键集/键序比对：修前顶层 5/8、`summary` 7 键中 5 键不符，修后顶层 8/8、`query` 8/8、`summary` 7/7、`retrieval_results` 5/5 且 arm 顺序一致 |
| 26（成功响应整体形状、`entities`/`chunks`/`source_facts`/`source_facts_truncated` 恒在且 `null` 保留） | ✅一致 | 响应模型 `api/http.py:878-930`（`RecallResponse`），`RecallResult` 每条 15 键、`scores` 四档恒在（`engine/response_models.py:241-263`）、`attachments` 恒在（`api/http.py:677-680`） | `internal/memoryops/models.go:312-411`、`internal/memoryops/recall_result.go` | — | 线上 `{"query":"Alice Google","include":{"chunks":{},"source_facts":{}}}` 逐层形状 SAME：顶层 6 键、`results[]` 15 键、`scores` 4 键、`entities`/`chunks`/`source_facts` 条目键集一致（条数差异为夹具+worker 噪声） |
| 26（`query` 缺失/空/纯空白/null） | ✅一致 | `api/http.py:6108-6118` 的 `@field_validator("query")`：`tokenize_query(v)` 为空即 `Value error, query must contain at least one word character after normalization`；缺失则 `missing` 且 `input` 为整个 body | `internal/memoryops/validation.go` | — | 线上 4 例：`{}`、`{"query":""}`、`{"query":"   "}`、`{"query":null}` → 全部 `SAME`（422 逐字节） |
| 26（`budget` 枚举、`types` 非法/`[]`/`["opinion"]`/`null`、未知字段、非对象 body、坏 JSON） | ✅一致 | `budget: Budget` 枚举；`VALID_RECALL_FACT_TYPES` 与 `opinion` 的静默丢弃（`engine/memory_engine.py:8894-8910`）；参考请求模型忽略未声明成员 | `internal/memoryops/recall.go:110-140`、`validation.go:441` | — | 线上：`budget:"ultra"`/`budget:null`/`types:["bogus"]`/`[1,2,3]`/`"{"` → 422 `SAME`；`types:[]`/`["opinion"]`/`null`、未知字段 → 200 且形状 `SAME` |
| 26（`max_tokens` 类型错误/`null`/小数） | ✅一致 | `max_tokens: int = 4096`；`null`/`5.5` 是 `int_type`/`int_from_float` 项 | `internal/memoryops/validation.go` | — | 线上：`"5"` 被框架按宽松整数接受 → 200 形状同；`null`、`5.5` → 422 `SAME` |
| 26（`min_scores` 形状：非对象/字段非数/未知成员/全 null） | ✅一致 | `MinScores`（`engine/response_models.py:265-318`）四字段全可选，未声明成员忽略 | `internal/memoryops/recall.go:139`、`validation.go` | — | 线上：`min_scores:5`、`{"final":"a"}` → 422 `SAME`；`{}`、`{"final":null}`、`{"bogus":1}` → 200 形状 `SAME` |
| 26（`include.*` 成员类型错、`include.entities:null`、`include` 非对象） | ✅一致 | `IncludeOptions`（`api/http.py:507-520`）三个成员均可为 `Model\|None` | `internal/memoryops/validation.go:400-421` | — | 线上：`include:5`、`entities:5`、`chunks:"a"`、`source_facts.max_tokens:"a"` → 422 `SAME`；`entities:null` → 200 且 `entities:null` 形状 `SAME` |
| 26（`tags`+`tag_groups` 互斥、`tag_groups` 单对象/`{"tag":…}`、`tags_match` 枚举） | ✅一致 | `api/http.py:6136-6142` 互斥；`TagGroup` 联合 `left_to_right` 逐分支报错（`engine/search/tags.py:307-346`） | `internal/memoryops/recall_request.go`、`validation.go:448-470` | — | 线上：四例全 422 `SAME` |
| 26（`query_timestamp` 非法 → 400 文案） | ✅一致 | `api/http.py:6216-6224`：`datetime.fromisoformat(...replace("Z","+00:00"))`，失败 `detail=f"Invalid query_timestamp format. …: {str(e)}"` | `internal/memoryops/recall.go:295`、`timeparse.go:426` | — | 线上 `"nonsense"` → 400 `SAME`（与第二轮结论一致，本轮复验仍 SAME） |
| 26（`temporal_window` 成员非法 → 逐边界 422 item） | ✅一致 | `engine/response_models.py:316-350`（`start/end: datetime \| date`） | `internal/memoryops/models.go:218-260`、`validation.go:477` | — | 线上：`{}` → 两条 `missing` `SAME`；`{"start":"bad",…}` → `datetime_from_date_parsing` `SAME` |
| 26（`prefer_observations`、`trace` 的非布尔取值） | ✅一致 | 两字段都是 `bool`/`bool = False`，框架按宽松布尔接受 `"yes"`/`"y"` | `internal/memoryops/validation.go` | — | 线上 `"y"` → 200 形状 `SAME` |
| 26（`max_tokens<=0` 只要 chunks、`max_tokens` 正数全超预算时的下限） | ✅一致 | `engine/fact_budget.py:57-60`、`:76-86` | `internal/memoryops/recall_result.go:applyTokenCap`、`internal/storage/recall_types.go:capMatchesToTokens` | — | 线上 `{"max_tokens":0}` → `results:[]`+`entities:null` 形状 `SAME`（第二轮结论，本轮复验仍 SAME） |
| 26（ghost 银行） | ⛔未覆盖（本轮） | `engine/memory_engine.py:8946-8958` 的 `_require_bank_exists` → 404 | `internal/storage/{memory,postgres}_recall.go` | — | 见下「待复核」 |
| 26（`include.source_facts.max_tokens` 的预算裁剪：**裁到剩余额度**而非**整条跳过**） | ❌已修 | `engine/source_facts.py:39-87` 的 `select_source_facts_within_budget`：`if max_total_tokens >= 0 and total_tokens + fact_tokens > max_total_tokens: skipped.add(sid); continue` —— **只跳过、从不裁切**；`truncated` 的定义是 `bool(skipped - seen)`（"某个被公开的 id 最终没有条目"）。`max_tokens_per_observation >= 0` 时改用**每 observation 自己的配额**、全局配额不参与（`:63-74`）。`max_source_facts_tokens=0` 因此是"一条都不给 + `truncated:true`"，而不是"给全部/空 map" | `internal/memoryops/recall_result.go`（`selectSourceFacts` 取代 `capSourceFactChannel`） | **已修**：原实现把最后一条 source fact **裁到剩余额度**再放进 map，且把 `truncated` 算成"裁过" —— 于是 `max_tokens:0`（剩余额度 0）时既不裁也不跳过，回的是**全部** source facts 且 `truncated:false`；小额度时回的是被截断的文本片段。现按参考：整条跳过、`truncated` 只看"被跳过且最终不在 map 里"、per-observation 配额优先 | 线上 `include.source_facts.max_tokens` 扫 0/1/2/5/10/50/4096 与 `max_tokens_per_observation` 扫 0/1/2/50：修前 `0` 两侧 `truncated` 相反（ref `true`/`null`，svc `false`/3 条）；修后 6 个档位 `truncated` 与条数全同（`50` 那条参考 2 条、本服务 1 条属事实文本长度不同造成的夹具差异）。`TestSourceFactBudgetSkipsRatherThanClips`（6 子用例，**改回去就 FAIL**，实测：`truncated = false, want true` 三处）、`TestSourceFactBudgetWithNoObservations` |
| 26（`limit`/`offset`） | ⛔未覆盖（本轮） | 参考 `RecallRequest` **没有** `limit`/`offset` 成员，未知成员被忽略；本服务的 `RecallQuery.Limit/Offset` 是内部路径用 | `internal/storage/recall_types.go:170-175` | — | 线上 `{"limit":1}` 与 `{"offset":1}` 均 200 且形状 `SAME`（即被忽略），行为一致但**分页语义本身参考不存在**，故不计覆盖 |

**chunks 三方对照**（同一次 `POST /v1/default/banks/rt-rr4/memories`，body 为 3 个 items：
`"Alice works at Google on the AI team. She joined in 2021."` / `"Bob likes hiking in the Swiss Alps during summer vacations."` /
`"The deploy pipeline uses Kubernetes and ArgoCD for the payments service."`）：

| chunk_index | 参考 `user_devin.chunks` | 本服务**修前** | 本服务**修后** |
|---|---|---|---|
| 0 | `Alice works at Google on the AI team. She joined in 2021.` | `Alice works at Google on the AI team. She joined in 2021.\n\nBob likes hiking in the Swiss Alps during summer vacations.\n\nThe deploy pipeline uses Kubernetes and ArgoCD for the payments service.`（192 字符，**唯一一条**） | `Alice works at Google on the AI team. She joined in 2021.` |
| 1 | `Bob likes hiking in the Swiss Alps during summer vacations.` | （不存在 —— 3 个 item 全部落在 index 0） | `Bob likes hiking in the Swiss Alps during summer vacations.` |
| 2 | `The deploy pipeline uses Kubernetes and ArgoCD for the payments service.` | （不存在） | `The deploy pipeline uses Kubernetes and ArgoCD for the payments service.` |

**"改回去就 FAIL"实测输出**（把 `memory_retain.go` 的 `item.ContentParts()` 改回 `item.Content` 后跑用例）：

```
--- FAIL: TestRetainStoresEachContentPartAsItsOwnChunk (0.02s)
    recall_chunk_parts_test.go:32: expected 3 chunks, got 1: [{ChunkID:b1_doc-1_0 BankID:b1 DocumentID:doc-1 ChunkIndex:0 ChunkText:Alice works at Google on the AI team.
        Bob likes hiking in the Swiss Alps.
        The deploy pipeline uses Kubernetes. ContentHash:8ce... CreatedAt:0001-01-01 00:00:00 +0000 UTC}]
FAIL
--- restored ---
ok  	github.com/studyzy/wenyuan/internal/storage	0.030s
```

### #26 复核结论

- **ghost 银行**：线上 `POST /v1/default/banks/rt-ghost-rr4/memories/recall` 两侧均 `404` 且 body 逐字节 `SAME`
  （参考 `engine/memory_engine.py:8946-8958` 的 `_require_bank_exists`），此前 ⛔ 项现为 ✅。
- **排序不变量**：`results[].scores.final` 单调不增在两侧均成立；`scores` 四键集恒为
  `{final,keyword,reranker,semantic}`、null 集合两侧同构（`{"query":"Alice"}`、`types:["world"]`、
  `max_tokens:0/-1`、`budget low/high`、`tags_match:exact` 等 12 例 INV-SAME）。
- **预算不变量**：`max_tokens:0` → `results:[]`+`entities:null`；`max_tokens:1` → 1 条（下限）；
  `min_scores.final:2.0`/`reranker:2.0` → `results:[]` 两侧同。
- **仍属夹具/模型噪声（不计缺陷）**：`types:["observation"]`、`min_scores.semantic:0.99`、
  `prefer_observations`、`include.entities.max_tokens:0` 等例的 `results` 条数与 null 分数集合不同 ——
  参考侧 worker 常开、事实集合里带 consolidation 产物，且两侧正文是模型每次采样出来的不同措辞。

### #27 `POST /v1/default/banks/{bank_id}/reflect`

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 27（`include.tool_calls` 的 `trace.llm_calls` 恒为空数组） | ❌已修 | 参考的 trace 是 `ReflectTrace(tool_calls, llm_calls)`（`api/http.py:1687-1700`），`api/http.py:6560-6576` 用 **core_result.llm_trace** 逐条构造 `ReflectLLMCall(scope, duration_ms)`；参考实测 `include:{"tool_calls":{}}` 返回 `llm_calls=[("agent_1",987),("agent_2",4107),("agent_3",2839)]` —— 每个 agent 轮次一条 | `internal/reflect/loop.go`（`Outcome.LLMCalls`、`LLMCall`、`Loop.llmCalls`、`callModel`）、`internal/reflect/runner.go`（`toStorageOutcome` 填充 `Trace.LLMCalls`） | **已修**：模型驱动的 agent 循环（`internal/reflect/loop.go`）走了 4 处 `provider.Complete`，但**从不把调用记进 trace**，而 `trace.llm_calls` 只由确定性回退循环（`storage/memory_reflect.go` 的 `recordModelCall`）填充 —— 该循环在有模型时根本不会运行，于是 `llm_calls` 恒为 `[]`。现所有模型调用统一经 `Loop.callModel` 计时并按 `agent_<n>`（n 从 1 起，参考的命名）/`final`/`structured`/`rewrite` 记入 `Outcome.LLMCalls`，`runner.toStorageOutcome` 转成 `storage.ReflectModelCall` | 线上 `{"query":"Alice","include":{"tool_calls":{}}}`：修前 `llm_calls: []`（参考 3 条），修后本服务 `[agent_1..agent_4]` 与参考同形同域（条数差 1 属模型轮次随机）；`TestRunnerReportsOneLLMCallPerModelCall`（**改回去就 FAIL**，实测 `0 llm_calls reported for 2 model calls: []`） |
| 27（成功响应 6 键 + `usage` 5 键、`trace` 两键形状） | ✅一致 | `api/http.py:1702-1765`（`ReflectResponse`）、`:1687-1700`（`ReflectTrace`）、`ReflectFact` 13 键（`:1614-1650`） | `internal/memoryops/models.go`、`reflect_response.go` | — | 线上 `{"query":"What do we know about Alice?"}` 两侧顶层 6 键、`usage` 5 键、`based_on:null`/`structured_output:null`/`trace:null` 全同；`include:{"facts":{},"tool_calls":{}}` 时 `based_on.memories[]` 13 键、`mental_models`/`directives` 为 `[]`、`trace.tool_calls[]` 5 键（`output:false` 时 4 键，无 `output`）两侧一致 |
| 27（`budget` low/mid/high/`ultra`） | ✅一致 | `ReflectRequest.budget: Budget = Budget.LOW`（`api/http.py:1512-1530`），枚举非法 → `literal_error` | `internal/memoryops/reflect_validation.go` | — | 线上 4 例：`low`/`mid`/`high` → 200 形状 `SAME`，`ultra` → 422 `SAME` |
| 27（`response_schema` 合法/非法） | ✅一致 | `api/http.py:1610-1620` 的 `validate_reflect_response_schema` → `validate_response_schema`；`response_schema: 5` 是 `dict_type` | `internal/memoryops/reflect_validation.go`、`reflect_schema_test.go` | — | 线上：合法 schema → 200 形状 `SAME`；`{"type":"bogus"}` → 422 `SAME`；`5` → 422 `SAME` |
| 27（`query` 缺失/空/纯空白、`fact_types:[]`/非法值） | ✅一致 | `api/http.py:1604-1608`（`validate_query_not_blank`、`validate_reflect_fact_types`）；`fact_types: list[Literal[...]]` 逐元素 `literal_error` | `internal/memoryops/reflect_validation.go` | — | 线上 5 例全 422 `SAME` |
| 27（`tags`+`tag_groups` 互斥、`tag_groups` 须为 list、`reflect_search_observations_max_tokens` 下界） | ✅一致 | `api/http.py:1607-1612`、`ReflectDefaultOptions.reflect_search_observations_max_tokens: int\|None, ge=1`（`:1480-1490`） | `internal/memoryops/reflect_validation.go` | — | 线上：互斥/单对象/`0` 全 422 `SAME` |
| 27（`include` 成员、`include.facts:null`、`context` 字段、`max_tokens` 宽松整型、`exclude_mental_models`、未知字段、坏 JSON） | ✅一致 | `ReflectIncludeOptions`（`api/http.py:1459-1470`）；`context` 拼接进 query（`api/http.py:6438-6442`） | `internal/memoryops/reflect.go`、`models.go` | — | 线上 8 例全 `SAME`（`include:5` → 422；其余 200 形状同） |
| 27（未知 bank） | ✅一致 | 参考 reflect 对未知 bank **不 404**：`_require_bank_exists` 只在 recall 一侧，reflect 走的是 profile 的 create-if-missing 路径 | `internal/memoryops/reflect.go:100-106`（`EnsureBank`） | — | 线上 `POST /v1/default/banks/rt-ghost-rr4/reflect` 两侧 **200** 且形状 `SAME` |
| 27（无模型部署 → 400 及逐字文案） | ✅一致 | `engine/memory_engine.py:15279-15286` 的 `LLMNotAvailableError("Reflect requires an LLM provider. Current provider is set to 'none'. …")`；`api/http.py:6597-6599` 转 400 | `internal/memoryops/reflect.go:30-36, 94-96`、`internal/app/app.go:447` | — | 用 `WENYUAN_API_LLM_PROVIDER=none` 独起一个服务实测：`POST .../reflect` → **400**，`{"detail":"Reflect requires an LLM provider. Current provider is set to 'none'. Set WENYUAN_API_LLM_PROVIDER to a real provider (e.g., openai, anthropic, gemini)."}` 与参考逐字同；同一实例的 `/memories/recall` 仍 200 |

**本单元结论计数（按 API 计，共 2 条）：✅ 0 / ❌ 2 / ⚠️ 0 / ⛔ 0**
（#26 ❌已修 —— 3 处真实差异：chunks 通道的切块、trace 形状与顺序、source_facts 预算裁剪；
#27 ❌已修 —— 1 处真实差异：trace.llm_calls 恒空。
两条 API 各自都做到了行为/形状层面的对齐，且每处差异都有一个"改回去就 FAIL"的用例。）

**需要组长裁决的事项**

1. `internal/storage/*_retain.go` 的 chunk 写入段（`putItemParts`/`putPartChunks`）与 `models.go` 的
   `RetainItem.Parts` 是**跨单元**改动（retain 单元的写路径、recall 的读通道共享）—— 本单元改它们是为了
   修 `include.chunks`；请确认与 `retain` 单元的后续改动不冲突。
2. `internal/storage/{memory,postgres}_recall.go` 在 `preferObservations`/`capCandidates` 之前各加了一行
   `scored` 快照（供 trace 的 `reranked`/`final_results` 使用），请复核与 `memory-crud` 单元对
   `postgres_recall.go` 的改动是否重叠。
3. `internal/memoryops/recall_test.go` 的 `TestRecallIncludeDefaults` 夹具原先用一个 **world** 事实冒充
   observation 来断言 `source_facts`；参考只对 observation 解析来源，故夹具改为真实 observation + 一条
   落库源事实。这是夹具修正，不是放宽断言。
4. `internal/memoryops/operations_test.go` 的 `TestRetryBatchParentWithNoChildren` 在本单元开工时已红
   （改动 `internal/memoryops/operations.go` 前后同样红，且该文件本单元未触碰），属 `operations` 单元 /
   `internal/storage/memory_operations.go` 的范围，请转交。

## 第四轮 mental-models

复核方式：逐条读参考源码取行号，并在**开启 worker 的**本服务副本（端口 18405，`CHK_RUN=mm4`，直连 `15432/wenyuan`）与线上参考（`8888`）之间做差分；夹具银行 `rt-mm4` / `rt-other-mm4` / `rt-ghost-mm4`，两侧同 id、同请求，结束后已删除。JSON 键序另外用自写工具（`/tmp/rt/ordercheck.py`）逐成员比对——`chk.py` 会把对象解析成 dict，**看不出键序**，而键序是这几条 API 的可观测契约。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 38 `GET .../mental-models` | ❌已修（`trigger` 键序 + `metadata` 档 `trigger` 应为 `null`） | `api/http.py:6920-6982`；`engine/memory_engine.py:16640-16670`（列表**批量**判定 `is_stale`，不查撤回）；`_row_to_mental_model` `:20458-20560`；`MentalModelResponse` `api/http.py:3409-3460`；`MentalModelTrigger` `api/http.py:3199-3389`（17 成员声明序） | `internal/mentalmodels/handlers.go`（`List`）、`models.go`（`applyDetail`/`toModel`）、`trigger.go`（`projectTrigger`/`Trigger.MarshalJSON`） | 修复①⑤ | 线上 `?detail=metadata/content/full`、`?tags=`、`?limit=0`、`?detail=config`、ghost 银行全部 `SAME`；键序逐成员同（17 键声明序） |
| 39 `GET .../{id}` | ❌已修（同上 + `reflect_response` 键序） | `api/http.py:6983-7020`；`memory_engine.py:16692-16740`（单条**不做** `_require_bank_exists`，`detail` 默认 `full`，仅 `full` 算 `is_stale` 且**走 `compute_mental_model_is_stale` = 含撤回检查**）；`_row_to_mental_model` 的 `detail` 三档 | `handlers.go`（`Get`）、`models.go`（`ReorderReflectResponse` 接入 `toModel`）、`trigger.go` | 修复①⑤⑥ | 线上 `detail=metadata/content/full`、未知 id 404 全部 `SAME`；`reflect_response` 的 4 个顶层键与 `based_on` 的 6 个类型键同为参考顺序 |
| 40 `GET .../{id}/history` | ❌已修（`previous_reflect_response` 存的是**全量** `reflect_response`，参考只存 `based_on`(+`trace`)） | `memory_engine.py:16748-16830`（裸数组，条目键 `previous_content`/`previous_reflect_response`/`changed_at`，失败记录另带 4 键）；写入侧 `_append_mental_model_history` `:18820-18839` 只存 `{"previous_content","previous_reflect_response"}`，而 `previous_reflect_response` 是 `update_mental_model` `:18674-18695` 的 **slim** 切片（只保 `based_on` 与 `trace`，`slim or None`） | `internal/app/mental_model_refresh.go`（`slimReflectResponse` + 写 history 的调用点） | 修复② | 线上两侧同 2 行、条目键集与键序相同；最新一行的 `previous_reflect_response` 两侧都只有 `based_on` |
| 41 `POST .../mental-models` | ❌已修（`max_tokens` 的 lax 整数强制、body 非对象的 422 形状） | `api/http.py:7052-7098`；`memory_engine.py:17077-17190`；`CreateMentalModelRequest` `api/http.py:3750-3782`（`max_tokens: int = 2048, ge=256, le=8192`，模型无 strict，故走 lax：数字串/整数值 float/bool 都绑定） | `internal/mentalmodels/handlers.go`（`Create`）、`helpers.go`（`rawMaxTokens`）、`models.go`（`MaxTokens` 改裸成员） | 修复③④ | 线上 `{"max_tokens":"300"}`、`" 300 "`、`"300.0"`、`300.0` → 200 且落库 300；`"abc"`/`"3e2"` → 仅 1 条 `int_parsing`；`300.5` → `int_from_float`；body `[]`/`5`/`"x"`/`true`/`[1,2]` → 1 条 `model_attributes_type` @`body`；创建/409/`trigger` 校验 19 例全部 `SAME` |
| 42 `POST .../{id}/refresh` | ✅一致 | `api/http.py:7099-7132`；`memory_engine.py:23188-23280`（`submit_async_refresh_mental_model`：先 `_raise_if_mental_model_refresh_unavailable`，再校验模型存在 → `ValueError(f"Mental model {id} not found in bank {bank}")`；`dedupe_in_flight_payload_key="mental_model_id"`、`serialization_key=mental_model:<id>`）；响应 `AsyncOperationSubmitResponse` | `handlers.go`（`Refresh`/`refreshError`/`enqueueModelRefresh`）、`internal/app/mental_model_refresh.go` | 无（本轮未发现差异；`reflect_response` 的修复落在 39/40 的读取与 refit 落库形状上） | 线上 `POST /r1/refresh` → `{"operation_id":…,"status":"queued"}` `SAME`；未知模型 `404 {"detail":"Mental model nope not found in bank rt-mm4"}` `SAME`；ghost 银行 `nope` → 同 404 `SAME`；刷新完成后 `GET ?detail=full` 的 `content`/`last_memory_seen_at`/`is_stale` 与参考同构 |
| 43 `POST .../{id}/dry-run-refresh` | ✅一致 | `api/http.py:7133-7182`；`memory_engine.py:18384-18480`；结果模型 `engine/mental_model_refresh.py:304-368`（`MentalModelDryRunRefreshResult`）；`_execute_mental_model_refresh` `:17340-17425` 解析 mode/scope/window | `handlers.go`（`DryRunRefresh`）、`internal/app/mental_model_dry_run.go`、`mentalmodels/dryrun.go` | 无（本轮未发现差异） | 线上 `POST /r1/dry-run-refresh` 逐字段比对：仅 `duration_ms`/`trace.duration_ms`（真实计时）与模型产出文本不同，其余（`requested_mode`/`effective_mode`/`mode_fallback_reason`/`outcome`/`would_persist`/`scope` 6 键/`window` 3 键/`facts`/`based_on`/`diff`/`warnings`/`usage`）同构；未知模型 `404 {"detail":"Mental model nope not found"}` `SAME` |
| 44 `POST .../{id}/clear` | ✅一致 | `api/http.py:7183-7218`；`memory_engine.py:18963-19040`（UPDATE 只动 `content=''`/`structured_content=NULL`/`last_refreshed_source_query=NULL`/`last_refresh_failed_at=NULL`/`embedding`，**不动** `reflect_response`，不写 history） | `handlers.go`（`Clear`）、`internal/storage/{memory,postgres}_mental_models.go` | 无（本轮未发现差异） | 线上 clear 后的整条模型与参考同构（仅时间戳/模型产出文本不同）；`POST /r2/clear`、`POST /nope/clear` → `SAME` |
| 45 `PATCH .../{id}` | ❌已修（同上 `max_tokens`、body 非对象两处；`{**base,**supplied}` 合并语义本身已对） | `api/http.py:7219-7261`；`memory_engine.py:18481-18600`；`_merge_trigger` `:19134-19200`；`UpdateMentalModelRequest` `api/http.py:3783-3830` | `handlers.go`（`Update`）、`trigger.go`（`MergeTrigger`） | 修复③④ | 线上顺序差分（`refresh_cron`↔`refresh_after_consolidation` 反复、`refresh_cron:null`、`tags_match`、`tag_groups`、`{}`、`{"trigger":null}`、未知 id）全部 `SAME`；`{"max_tokens":"300"}` → 200 且落库 300（修复前 422） |
| 46 `DELETE .../{id}` | ✅一致 | `api/http.py:7262-7300`；`memory_engine.py:19056-19130`（`{"status":"deleted"}`，删 0 行 → 404） | `handlers.go`（`Delete`） | 无（本轮未发现差异） | 线上 `DELETE /r2` → 200 `{"status":"deleted"}` `SAME`；再删 → 404 `SAME`；删后 `GET /r2` → 404 `SAME` |

**本轮修复（每条都配了"改回去就 FAIL"的用例）**

1. **`trigger` 的 17 个成员按 Go map 的字典序上线**（`internal/mentalmodels/trigger.go`）。参考把 `MentalModelTrigger` 交给框架序列化，字段按**声明序**输出（`api/http.py:3199-3389`）；本服务原来用 `map[string]any`，Go 按字典序编码，于是 `budget, exclude_mental_model_ids, exclude_mental_models, fact_types, …` 与参考的 `budget, mode, refresh_after_consolidation, refresh_cron, …` 逐键不同。→ 改为有序类型 `Trigger`（`OrderedTriggerMember{Key,Value}` + `MarshalJSON`），投影用 `projectTrigger`。
   - **改回去就 FAIL**：把 `Trigger.MarshalJSON` 换回 `json.Marshal(map)` → `TestTriggerRendersInDeclarationOrder` FAIL：
     ```
     --- FAIL: TestTriggerRendersInDeclarationOrder
         trigger_parity_test.go:538: trigger member 1 = "exclude_mental_model_ids", want "mode"
     ```
2. **`metadata` 档的 `trigger` 输出成 `{}` 而不是 `null`**（`internal/mentalmodels/trigger.go`）。`applyDetail` 把 `model.Trigger` 置 nil，但带自定义 `MarshalJSON` 的 nil slice 编码成 `{}`。参考该字段是 `MentalModelTrigger | None`，`metadata` 档不给 → `null`。→ nil `Trigger` 直接写 `null`。同一轮还修了 `?detail=` 三档下 `Model.Trigger` 的 nil 语义（covered by `TestModelTriggerIsNullAtMetadataDetail`）。
3. **`max_tokens` 没有按 lax 整数绑定**（`internal/mentalmodels/helpers.go`、`models.go`、`handlers.go`）。参考的成员是 `int`（`api/http.py:3775`、`:3812`），模型非 strict，所以框架先做 lax 强制再查 `ge/le`：`"300"`、`" 300 "`、`"300.0"`、`300.0` 都**绑定并落库为 300**；`"abc"`/`"3e2"` 是 `int_parsing`、`300.5` 是 `int_from_float`，且**只报这一条**（本服务原来在 `"abc"` 上多报一条 `greater_than_equal`，因为把未绑定的 0 当成了值）。→ `MaxTokens` 改成 `json.RawMessage`，由 `rawMaxTokens` 走 `httpx.LaxFloat`（bool 另行按 1/0），界检查仍在原声明位置、`input` 仍是客户端原值。
   - **改回去就 FAIL**：把 `rawMaxTokens` 改回"只认 JSON number" → `TestMaxTokensTakesTheLaxIntegerCoercion` FAIL（`"300"`/`" 300 "`/`"300.0"` 三个子例 422）。
4. **JSON 合法但不是对象的 body 报成 `dict_type`**（`internal/mentalmodels/helpers.go`）。参考对"取不出成员的输入"报 `model_attributes_type`、`loc=["body"]`、`input` 为**该值本身**。→ `requestItems` 先解成 `any`，非对象走 `TypeMismatch("model_attributes_type", ["body"], value)`。
   - **改回去就 FAIL**：`requestItems` 改回直接解进 `map[string]any` → `TestBodyThatIsNotAnObjectIsAModelShapeFailure` FAIL（`[]`/`5`/`"x"`/`true`/`[1,2]` 全部 `dict_type`）。
5. **`refresh_cron` 没有做校验器改写、`tag_groups` 的叶子没补默认值**（`internal/mentalmodels/trigger.go` 的 `normalizeTrigger`）。参考的 `validate_refresh_cron` 把值 `strip()` 后存回（`"  0 3 * * *  "` → `"0 3 * * *"`，纯空白 → `None`），`TagGroupLeaf` 的 `match`/`resolve` 有默认值 `any_strict`/`exact` 并在序列化时补全（`engine/search/tags.py:307-317`）。
   - **改回去就 FAIL**：删掉 `normalizeTrigger` 里的这两段改写 → `TestTriggerMembersAreProjectedThroughTheirValidators` FAIL（`refresh_cron = "  0 3 * * *  ", want the stripped expression`）。
6. **`reflect_response` 存了"每一型都空表"的 `based_on`，且键序不可控**（`internal/app/mental_model_refresh.go`、`internal/mentalmodels/trigger.go` 的 `ReorderReflectResponse`）。参考的 `based_on` 由 reflect 跑的 `based_on` dict **照原样**序列化（`memory_engine.py:17538-17563`、`:17642-17646`），**没引用到的类型不出现**；本服务原来固定补全 6 个类型键（第一轮遗留的旧结论）。另外 jsonb 不保序、Go map 又按字典序输出，于是 4 个顶层键与 6 个类型键的顺序都与参考不同。→ `renderFactsByType` 只保留引用到的类型（顺序为参考的 `world, opinion, directives, experience, observation, mental-models`）；读取路径 `ReorderReflectResponse` 用有序对象还原顶层 `text, outcome, based_on, reflect_skipped, mental_models` 与 `based_on` 的类型序（`internal/bankapi/config.go:255-264` 的 `entity_labels` 已有同一先例）。
   - **改回去就 FAIL**：`TestRefreshStoresTheReferenceReflectResponseShape` 的 `based_on` 断言按参考改写后，把 `renderFactsByType` 改回"预置 6 个类型" → 断言 `based_on` 多出 `world` 等空表键而 FAIL；`TestHistorySnapshotKeepsOnlyTheReferenceSlice` 与 `TestRefreshRecordsTheSlimmedHistorySnapshot` 钉住第 2 条。

**改回去就 FAIL 实测输出（第 40 条的 slim 切片）**

```
--- FAIL: TestRefreshRecordsTheSlimmedHistorySnapshot
    mental_model_refresh_test.go:685: previous_reflect_response = map[string]interface {}{"based_on":…,"mental_models":…,"outcome":"content_written","text":"deployments happen on tuesdays"}, want only based_on
--- restored ---
ok  	github.com/studyzy/wenyuan/internal/app
```

**噪声与"未覆盖"说明**

- `reflect_response` 的**文本与事实集合**逐字比对会不同：两侧接同一个中转但模型有随机性，且两侧数据（事实 id、抽取文本）本身不同——`PLAN.md` §4 已列为已知噪声。断言落在**键集/键序/类型**上。
- `duration_ms`、`trace.duration_ms`、`recorded_at` 是真实计时/时间值，不可比。
- `?detail=full` 的 `reflect_response` 只有在**同一份**数据上刷新过才可能逐值相同；本轮把两侧夹具银行同 id 重建后刷新一次即验到键集/键序/形状一致。

**计数（按 API 编号，共 9 条）**

| ✅一致 | ❌已修 | ⚠️存疑 | ⛔未覆盖 |
|---|---|---|---|
| 4（#42、#43、#44、#46） | 5（#38、#39、#40、#41、#45） | 0 | 0 |

六处修复对应到 API：#38 修复①⑤；#39 修复①⑤⑥；#40 修复②；#41 修复③④；#45 修复③④。

**需要组长裁决的事项**

1. **`internal/httpx` 的 query 声明**：`internal/mentalmodels/handlers.go` 的 `query_params_test.go` 通过 `httpx.QueryAudit` 断言"handler 读的每个 query 参数都被路由声明"。本轮**没有**新增 query 参数，故未触碰。
2. **`based_on` 的键序是否值得还原**：参考的 `based_on` 是 Python dict 的插入序，本服务还原为固定的 `world, opinion, directives, experience, observation, mental-models`（实测两侧该顺序一致）。若日后参考改变了建 dict 的顺序，这里会重新漂移——属"静态推断 + 一次实测"，非逐字节证明。
3. **`worker` 关闭时的刷新**：`POST .../mental-models` 与 `POST .../{id}/refresh` 在 `WENYUAN_API_WORKER_ENABLED=false` 的部署里仍回 `200 + operation_id`，但那次刷新停在队列里；参考同样如此（worker 不在 API 进程里），未作改动。

## 第四轮 memory-crud

复核方式：逐条读参考源码取行号；两侧**同一 SQL 受控夹具**（直插两侧数据库同名 bank，行号/时间戳/标签全同），
线上差分用 `/tmp/rt/chk.py`（端口 18403、`CHK_RUN=mc4`）。参考库 `127.0.0.1:5432/hindsight` 的 `user_devin` 模式、
本服务库 `127.0.0.1:15432/wenyuan` 的 `public` 模式可直连，受控夹具因此逐字节可比。
本服务带 `PARITY_DEV_WORKER_ENABLED=true` 起（参考 worker 常开，无 worker 时两侧 `consolidated_at` 天然不同）。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 28 `GET /v1/default/banks/{bank_id}/memories/list` | ❌已修（3 处） | `api/http.py:5815-5906`（`api_list`，`tags_match`/`time_field` 是 `Literal`、`start_date`/`end_date` 在**参数求值时**经 `_parse_iso_datetime` 解析、`limit`/`offset` 是绑定整数）；`engine/memory_engine.py:13216-13217`（`_authorize_bank_read` → `_require_bank_exists` **先**于存储层）；`engine/memories/pg/curation.py:167-172`（`state`/`entity_id` 校验在存储层内）、`:204-222`（`consolidation_state`）、`:244-262`（时间窗 `build_time_clause`，`validate_time_window` 抛 `end_date <= start_date`）；`:284-292`、`:320`（参数绑定顺序：`bank_id` 之后**直接** `limit`、`offset`，再按 `fact_type/document_id/entity_id/search` 顺序续绑） | `internal/reporting/memory_routes.go:26-110`（`ListMemories`）、`:186-260`（`nonNegativeIntParam`）；`internal/storage/postgres_memories.go:15-135`（`ListMemories`）、`:139-166`（`listTagClause`） | ①**校验次序**：原实现把 `state`/`entity_id`/`consolidation_state`/反序窗校验放在 `requireBank` **之前**，未知 bank 上参考 404 而本服务 400；现改为「框架侧成员与日期解析 → 页边界 → `requireBank` → 存储层校验」，与参考一致。②**占位符编号**：参考 `limit`/`offset` 是 `$2`/`$3`；原实现按过滤条件动态编号（`offset` 溢出 int64 时报 `$2`），现固定按参考顺序绑定，`nonNegativeIntParam` 接收占位符号。③**查询可执行性**：改动②把参数位置前移后，pgx 的文本格式扩展协议要求每个位置有类型，而 `tags` 是 `varchar[]`（`$n::text[]` 与列不兼容）、时间参数需要在 `IS NOT NULL` 之外被命名——现策略为「列侧转型 `mu.tags::text[]`、实体 id 用 `CAST($n AS uuid)`、页边界在 COUNT 里用惰性真谓词标注类型，SELECT 仍用 `$n` 作 `LIMIT/OFFSET`」 | 线上 68 例全 `SAME`（默认页、`limit=0/-1/5.0/1_0/+3/_5/5e2/`、重复 `limit`、`offset` 空/负/超 int64、`q` 空/空白/大小写/前后空白/重音、`type` 空白/空/枚举/未知、`document_id`、`entity_id` 六形态、`tags`/`tags_match` 五模式/空、`state` 大小写与非法、`consolidation_state` 大小写与非法、`time_field` 六值、日期四形态/反序/相等/重复、ghost 银行×各非法参数、`POST/OPTIONS/HEAD`、尾斜杠）；排序专项：受控行 `mentioned_at` 相同 `created_at` 相反（1、2）、`mentioned_at NULL` 且 `created_at` 最旧（4）——两侧次序完全相同（`NULLS LAST`、`created_at DESC`、无 id 兜底；`time_field` 路径为 `col DESC, id`）；23 键与顺序、`null`/`{}`/`[]` 形态两侧相同。用例：`TestMemoryListChecksTheBankBeforeTheStoreFilters`、`TestMemoryListPageBoundsUseTheReferencePlaceholders` |
| 29 `GET /v1/default/banks/{bank_id}/memories/{memory_id}` | ✅一致 | `api/http.py:6054-6070`（先 `uuid.UUID(memory_id)` → `400 "Invalid memory_id: '<raw>'"`；未命中 → `404 "Memory unit '<raw>' not found"`；**不探 bank**）；`engine/memory_engine.py:13264-13292`（规范化后读活表，未命中回退归档表，`tag_scope` 外 404）；`engine/memories/pg/curation.py:397-470`（detail 键序：`id/text/context/date/type/mentioned_at/occurred_start/occurred_end/entities/document_id/chunk_id/tags/metadata/observation_scopes/state/invalidation_reason/invalidated_at/edited_at`，observation 追加 `history:[]`；有源再加 `source_memory_ids`/`source_memories`，源按 `mentioned_at DESC NULLS LAST`） | `internal/reporting/memory_routes.go:325-370`（`GetMemory`）、`:280-320`（`memoryUnitID` 的 CPython `uuid` 规则）、`:905-975`（`memoryDetail`）、`:1005-1040`（`sourceMemories`）；`internal/storage/postgres_memories.go`（`GetMemory` 活表→归档回退） | 无（第二轮已修，本轮复验无回归：归档行仍可读、`source_memories` 只取活表、`context` 空串→`""`、源 `context` 空→null） | 线上 16 种 id 形态 × 各路由：`urn:uuid:`/`URN:UUID:`/`{}`/`{{}}`/32hex(大小写)/`{urn:uuid:…}`/错分组/前导空格/尾随空格/`not-a-uuid`/空/短/全零/非 ASCII × (detail, history, patch) 全部 `SAME`；observation（2 源）、无日期行、归档行、未知 id、未知 bank、尾斜杠、`POST`/`DELETE` 405 全 `SAME` |
| 30 `PATCH /v1/default/banks/{bank_id}/memories/{memory_id}` | ❌已修（2 处） | `api/http.py:6078-6132`（`UpdateMemoryRequest` 必填；`occurred_*` 用 `model_fields_set` 判显式 null）；`api/http.py:2777-2864`（模型与 `model_validator`：`Provide at least one field to update.`/`state must be…`/`fact_type must be…`）；`engine/memory_engine.py:11981-12014`（先 `uuid.UUID` → `Invalid memory_id`，再 state/text/fact_type 顺序）；`:12096-12115`（`doing_edit`：text/context/dates/fact_type/**new_entities**）；`:12194-12260`（**phase 2 先 apply_edit**：写字段 + 重置 `consolidated_at`/`consolidation_failed_at` + 打 `edited_at`/`updated_at`）；`:12350-12380`（**再** invalidate / reason-only / revert；revert 分支要求 `archived2`） | `internal/reporting/memory_routes.go:495-600`（`UpdateMemory`）、`:622-655`（`validateMemoryUpdate`）；`internal/storage/postgres_memories.go:388-560`（`UpdateMemory`，新增 `editsColumns`）；`internal/storage/memory_memories.go:394-545`（同序） | ①**编辑 + 归档的同体写入丢编辑**：原实现「先 invalidate 再 return」，`{"text":"changed","state":"invalidated"}` 归档的是**旧文本**、且不写 `edited_at`；参考先 apply_edit 再搬行。现两个后端都改为「先编辑，再搬行」，revert 仍在编辑前分流。②**无列补丁也会重写行戳**：原实现任何 state-only 补丁都把整行按现值重写（把 `updated_at` 当作编辑戳）；现只在该补丁点名了可编辑列时才写 UPDATE/`edited_at`，与参考 `apply_edit` 只在编辑时跑一致（`tags`/`metadata` 也算列） | 线上 45 组 body × 3 目标（world / observation / 归档行）=135 例全 `SAME`：空体/空白/`{}`/`null`/`[]`/字符串/数字、`text` 合法/空/全空白/整数/null、`context` 设清、`entities` `[]`/单值/整数/元素整数/null/字符串、`fact_type` 合法/observation/非法/整数、`state` 合法/invalidated+reason/valid/非法/整数、`occurred_start` 设/清/非法/整数、`reason`/`resolve_entities`/未知名、多错误、`1e999`、observation 不可策展 400、编辑已归档 400、`{"state":"invalidated","text":…}`；副作用往返 33 步全一致（含改后读回、重复 invalidate 保留 reason 与 `invalidated_at`、重复 revert、清空后退回 `mentioned_at`、列表随之变化）。falsification：把 `updateMemoryLocked` 恢复成「state 顶替编辑」→ `TestMemoryUpdateAppliesTheEditBeforeInvalidating` FAIL（`the edit stamp is missing from the archived row`），恢复即 ok |
| 31 `GET /v1/default/banks/{bank_id}/memories/{memory_id}/history` | ✅一致 | `api/http.py:6134-6163`（`None` → `404 "Memory unit '<raw>' not found"`）；`engine/memory_engine.py:13363-13430`（先 `uuid.UUID` 规范化 → `tag_scope` → `observation_head`：活表才有，**归档即 None**）、`:13401-13410`（非 observation 回 `[]`）、`:13420-13490`（历史按 `changed_at ASC, id ASC`，enriched 回写 `source_facts` 与 `is_new`，源解析缺失给 `{"id":…,"text":null,"type":null,"context":null}`） | `internal/reporting/memory_routes.go:672-760`（`MemoryHistory`）、`:762-880`（`renderMemoryHistory`/`historySourceFact`）；`internal/storage/postgres_memories.go`（`MemoryHistory`） | 无（第二轮已修：归档行 404 的不对称、id 规范化；本轮复验无回归） | 线上 observation（2 源 + 2 条历史）、world/无日期行 → `[]`、归档行 → 404、未知 id、未知 bank、bad/hex32/urn/braces id、尾斜杠 全 `SAME` |

**本轮修复清单（4 条，每条都有"改回去就 FAIL"的用例）**

1. **#28 校验次序**：`internal/reporting/memory_routes.go` 的 `ListMemories` 把存储层校验抬到了 `requireBank` 之前。参考是 `_require_bank_exists` 先、存储层校验后；未知 bank 上 `state=bogus`/`consolidation_state=bogus`/`entity_id=bad`/反序窗口参考 404、本服务 400。→ `TestMemoryListChecksTheBankBeforeTheStoreFilters`（把顺序改回去即 FAIL：`?state=bogus = 400, want 404`）。
2. **#28 占位符编号**：`offset` 超 int64 时参考报 `$3`、本服务报 `$2`（参考 `bank_id, limit, offset` 相邻绑定）。→ `TestMemoryListPageBoundsUseTheReferencePlaceholders`（改回固定 `$2` 即 FAIL）。
3. **#30 编辑 + invalidate 同体**：见上表①。→ `TestMemoryUpdateAppliesTheEditBeforeInvalidating`。
4. **#30 无列补丁的写戳**：见上表②。→ 由 `TestMemoryUpdateArchivedReason`（既有用例，本轮一度被我改坏后复验抓回）+ 往返差分覆盖。

**跨作用域（已在行走作用域内顺手修，供组长裁决是否保留）**：`internal/storage/consolidation_scope.go` 原把 observation 的 scope 镜像进 `metadata`（`{"scope":"a,b"}`）。参考的 observation INSERT 只写 `id/bank_id/text/fact_type/embedding/proof_count/source_memory_ids/tags` 与时间列（`engine/memories/pg/consolidation.py:268-352`），metadata 为列默认 `{}`——线上实测本服务每条 observation 的 `metadata` 比参考多一个 `scope` 键（`/memories/list` 与 `/memories/{id}` 都可见）。已删该镜像（scope 只存 tags）；`TestConsolidatedObservationCarriesNoScopeMetadata` 钉住。**注意**：该文件不在本轮 memory-crud 的写作用域内，但它是本轮 4 条 API 可见字段的直接成因；如需回退请一并回退该测试。

**⚠️ 存疑（非代码问题）**
- **列表项 `entities` 的次序：参考侧不存在次序契约，不要"修"它。** 参考 `engine/memories/pg/curation.py:329-338` 对 `unit_entities` 只 `ORDER BY ue.unit_id`（同一 unit 的所有 posting 并列），同一条记忆的多个实体名之间**没有任何排序保证**——PostgreSQL 可以按任意顺序返回并列行，同一份数据在不同执行计划下都可能不同。因此"若干实体名的次序"不是本服务必须复刻的行为：本服务按实体名排序是**其中一个合法实例**，与参考不构成差异。受控夹具（两侧直插表、无 `unit_entities` 行）里两侧 `entities` 都是 `""`，本条**未做多实体实测**，也不应有人为了"对齐次序"去改动它。
- **`consolidated_at` / `edited_at` 的活测量**：`chk.py` 会把它们归一为 `<N>`；受控夹具里这些列由两侧各自的规则写入，但形态（是否 null、时间戳格式 `+00:00` 六位微秒）逐字节相同。
- `q` 的大小写不敏感匹配在两侧都是 PostgreSQL `ILIKE`，重音与非 ASCII 的差异属于数据库排序规则，两侧同库同版本，实测同结果。

**夹具与清理**：受控夹具 `rt-mc4`（含 `rt-other-mc4`、`rt-ghost-mc4`）在两侧数据库直插 7 行 + 1 行归档行；
`rt-ctrl-mc4`、`rt-probe-mc4`、`rt-probe2-mc4`、`rt-probe3-mc4` 为 retain 实测用的辅助银行。结束前全部删除（两侧库）。

**计数**：`✅ 2 / ❌ 2 / ⚠️ 0 / ⛔ 0`（按 4 条 API 计；28、30 ❌已修，29、31 ✅一致）。

## 第四轮 observations

复核方式：逐条读参考源码取行号，在**开启 worker 的**本服务副本（`PARITY_DEV_WORKER_ENABLED=true scripts/parity_dev_service.sh 18404`，直连 `15432/wenyuan`）与线上参考（`8888`）之间做差分。夹具银行 `rt-ob4`（两侧各建、各 retain 3 条记忆 → 3 条 `world` 源事实 + 3 条 `observation`，2 个 scope：`team:core`×2 / `team:exec`×1）。非 worker 产物（`consolidated_at`、`last_seen_at`）在两侧 worker 都开的情况下才可比，本单元全程开着。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 32 `DELETE /v1/default/banks/{bank_id}/observations` | ✅一致 | `api/http.py:9462-9485`（`DeleteResponse(success=True, message=f"Cleared {n} observations", deleted_count=n)`）；`engine/memory_engine.py:11643-11710`：`_authenticate_tenant` → `_refuse_bank_wide_if_scoped(..., "clear the bank's observations")`（`:11660`）→ 事务内 `SELECT … FOR NO KEY UPDATE` 锁 banks 行、`clear_observations_and_requeue`（**:170-180 先 COUNT 再 DELETE，谓词只有 `bank_id` + `fact_type='observation'`**）、`DELETE observation_history WHERE bank_id`（**:11692-11695**）、`UPDATE banks SET last_consolidated_at=NULL`（**:11698-11701**）；**无** `_require_bank_exists` → 未知 bank 200/0 | `internal/reporting/handlers.go:547-570`（先 `RefuseBankWideIfScoped`、无 `requireBank`、`invalidateBankStats`）；`internal/storage/postgres_memories.go:674-704` → `clearBankObservationsTx` `:705-734`（COUNT→DELETE `fact_type='observation'`→`DELETE observation_history WHERE bank_id`→`UPDATE memory_units SET consolidated_at=NULL WHERE fact_type IN ('experience','world')`） | 无 | 线上：真实银行三次 **`SAME`** `{"success":true,"message":"Cleared 3 observations","deleted_count":3}`；未知 bank `SAME` 200/0；`{b2}`（不存在）`SAME` 200/0；清空后 `scopes` 两侧同体 `{"scopes":[],"total":0,…}`；随后 `consolidate` + 等 worker 收敛，`scopes` 两侧又同为 `team:core`×2 + `team:exec`×1。**副作用直查库**（`15432`）：清空后 `rt-ob4` 的 `world` 行 3 条 `consolidated_at` 全为 NULL、`observation` 0 条、`observation_history` 0 行、`banks.last_consolidated_at` 为 NULL —— 与参考的三条 SQL 逐条对应。用例 `TestDeleteObservationsClearsSourcesAndHistory`（1 条 observation + 1 条 world 源：删后 `deleted_count=1`、`observation_history` 消失、源事实 `consolidated_at` 归 NULL）、`TestObservationWritesWithoutBankCheckAnswerForAnUnknownBank` |
| 33 `GET /v1/default/banks/{bank_id}/observations/scopes` | ✅一致 | `api/http.py:9487-9519`：`limit: int = Query(100, ge=0, le=1000)`、`offset: int = Query(0, ge=0)`，**query 校验先于 handler**；`engine/memory_engine.py:11747-11782`：`_authorize_bank_read(BankReadOperation.LIST_OBSERVATION_SCOPES)` → **`_require_bank_exists`**（`:11771-11772`，与 32/35 相反）；`engine/memories/pg/counts.py:155-207`：scope = `COALESCE(ARRAY(SELECT unnest(tags) ORDER BY 1),'{}')`、`GROUP BY scope ORDER BY count DESC, scope`、`total` = `COUNT(DISTINCT scope)`（**不受 limit/offset 影响**）、`LIMIT/OFFSET` 在 SQL 里、响应 `{scopes,total,limit,offset}` | `internal/reporting/handlers.go:573-601`（`parseObservationScopeQuery` 先跑，再 `requireBank`）；`internal/reporting/observation_params.go`（`frameworkIntFromString`/`pythonStrip`/`*big.Int` 比边界、按声明序收集全部被拒参数）；`internal/storage/postgres_observability.go` 的 `ListObservationScopes` | 无 | 线上 **9 路全 `SAME`**：`?`（默认 100）、`?limit=1`、`?limit=10&offset=1`、`?limit=0`、`?limit=10000`（422 `less_than_equal le:1000`）、`?limit=`（422 `int_parsing`）、未知 bank（404 `Bank 'rt-ghost-ob4' not found`）、`{b2}`（404）、真实 bank 带数据（2 scope：`team:core`×2 → `team:exec`×1，键序 `tags,count`，`total=2`）。上一轮修过的空值/多项/框架整数文法用例（`TestObservationScopeQueryValidation`、`TestObservationScopeQueryReportsEveryRejectedParameter`）仍全绿、无回退 |
| 34 `POST /v1/default/banks/{bank_id}/consolidation-strategies/preview` | ✅一致 | `api/http.py:9521-9554`（`request: ConsolidationStrategiesPreviewRequest` 必填）；`api/http.py:2332-2339`（`strategies` 必填、`sample_limit` 默认 5 `ge=0 le=50`）；`engine/response_models.py:718-729`（`ConsolidationStrategySpec` 只声明 5 个键，其余 non-strict 丢弃且不类型校验）；`engine/memory_engine.py:11715-11745`（`list_observation_scopes(limit=10_000, offset=0)`、`complete = total <= len(scopes)`、**bank 校验经 scopes 的 `_require_bank_exists`**）；`engine/consolidation/consolidator.py:3536-3629`：`taken_count = sum(1 for k in hits if winners[k] != i)`（**:3603，`winners[k] is None` 也计 taken**）、`claimed_count = sum(1 for winner in winners if winner == i)`、`samples = hits[:sample_limit]`、`default` = 无 winner 的 scope；解析侧 `_parse_scope_pattern`（`:43634`，空 tags/含空串/非 str → 丢弃；未知 `tags_match` 回落 `all`）、`_parse_consolidation_strategies`（`:45435`，**`bool` 不算 int**、mission 需 `.strip()` 非空、无可用 rule 或无任何设置 → 整条丢弃）；匹配 `_scope_contains_globs`（`:954`）/`_scope_matches_globs`，无 tag scope 永不匹配 | `internal/reporting/handlers.go:605-665`（先 body 校验、后 `requireBank`）；`internal/reporting/consolidation_preview.go`（`decodeConsolidationPreview` 声明序校验、`strategyActive` 的 bool 判据、`countTakenAway` 的 `winners[k]==nil || *winners[k]!=strategy`、`buildConsolidationPreview` 位置对齐 + 首个 strategy 赢） | 无 | 线上 **31 路全 `SAME`**（逐字节 `--raw`）：空 body/`null`→422 `missing loc:["body"]`、`{}`→422 `missing loc:["body","strategies"]`、`[]`→`model_attributes_type`；`{"strategies":[]}`（2 scope 全进 `default`）、`[{scopes:[]}]`（inactive）、`[{scopes:[{tags:["*"]}]}]`（**inactive 但 `taken_count=2`** —— 第二轮修过的那条）、exact 规则、两策略争同一 scope（首个赢，第二个 `taken_count=1`）、`sample_limit:0/1/50`、`max_observations_per_scope:0`（active 判据）、`max_observations_per_scope:true`（active，`bool` 当 int 的参考行为）、`max_observations_per_scope:1.5`→422 `int_from_float`、`consolidation_source_facts_max_tokens:"x"`→422 `int_parsing`、未声明键 `consolidation_llm_batch_size:"x"`→200、`observations_mission:""`/`"   "`（inactive）、`scopes:{}`/`scopes:[{tags:"x"}]`/`tags:[1]`/`tags_match:5`/`tags_match:null`/`scopes:[null,…]`/`tags:[""]`、`strategies:[null,…]`、`sample_limit:"3"`/`true`/`70`/`-1`/`null`、多条 strategy 混合 active/inactive。未知 bank：坏 body 先 422（`list_type loc:["body","strategies",0,"scopes"]`）、好 body 才 404 |
| 35 `POST /v1/default/banks/{bank_id}/consolidation/recover` | ✅一致 | `api/http.py:9556-9579`（`RecoverConsolidationResponse(retried_count=result["retried_count"])`）；`engine/memory_engine.py:11785-11822`：`_refuse_bank_wide_if_scoped(..., "retry the bank's failed consolidation")` → **直接** `requeue_failed_consolidation`，**无** `_require_bank_exists`、**无事务**、**不入队**（文档字符串 "so they are picked up again on the next consolidation run"）；`engine/memories/pg/engine_curation.py:190-209`：先 `COUNT(*)` 再 `UPDATE … SET consolidation_failed_at=NULL, consolidated_at=NULL WHERE bank_id=$1 AND consolidation_failed_at IS NOT NULL AND fact_type IN ('experience','world')` | `internal/reporting/handlers.go:668-684`（先 `RefuseBankWideIfScoped`、无 `requireBank`、不入队）；`internal/storage/postgres_memories.go:792-816`（单条 `WITH requeued AS (UPDATE … RETURNING 1) SELECT count(*)`，同一过滤条件） | 无（上一轮删掉的 `requireBank` 未回退） | 线上：未知 bank `SAME` `{"retried_count":0}`；真实 bank（无失败标记）`SAME` `{"retried_count":0}`；**副作用直查库**：把 `rt-ob4` 的 2 条 `team:core` `world` 行手置 `consolidation_failed_at=now()`，`POST …/recover` 前该 bank `consolidation_failed_at IS NOT NULL` = 2、修后 = 0 且这 3 条 `world` 的 `consolidated_at` 全 NULL（参考的 `fact_type IN ('experience','world')` 过滤生效）；`GET …/operations` 总数在 recover 前后不变（**只 UPDATE 不入队**）。用例 `TestConsolidationRecoverUnknownBankRetriesNothing` 仍过 |
| 36 `DELETE /v1/default/banks/{bank_id}/memories/{memory_id}/observations` | ✅一致 | `api/http.py:9581-9610`（`ClearMemoryObservationsResponse(deleted_count=result["deleted_count"])`）；`engine/memory_engine.py:11825-11885`：`_require_writable(bank_id, …, memory_id=memory_id)`（`:3304`，**无 tag scope 时直接 return，完全不查 memory → 未知 bank / 未知 id 都 200/0**）→ `_delete_stale_observations_for_memories`（`:16547-16562` → `engine/retain/fact_storage.py:147-189` → `engine/memories/pg/writes.py:242-341`：`uuid.UUID(str(fid))` 先解析 → 取 `source_memory_ids && fact_ids` 的 observation → 有序 `FOR NO KEY UPDATE` → `DELETE memory_units` + `DELETE observation_history` → 幸存 co-source `consolidated_at=NULL`）→ `if deleted_count > 0` 才 `requeue_source_memory`（**:11854-11859**）→ 事务外 `if deleted_count > 0 and config.enable_auto_consolidation` 才 `submit_async_consolidation`（**:11879-11882**） | `internal/reporting/memory_routes.go:393-442`（memory-crud 单元的文件，本单元**只读复核、未改**）；`internal/storage/postgres_memories.go:674-704` + `internal/storage/postgres_consolidation.go:399-487`（`deleteStaleObservationsTx`） | 无 | 线上**按两侧各自的 id**差分（两侧记忆文本同、id 必然不同）：等价请求 4 路 `SAME` —— 非 uuid（`c648a4c5…` 之外的 `not-a-uuid`）→`500 {"detail":"badly formed hexadecimal UUID string"}`、未知 bank + 有效 uuid →`200 {"deleted_count":0}`、全零 uuid→`200 {"deleted_count":0}`；命中源事实时两侧各 `{"deleted_count":1}` 且第二次同 id 各 `{"deleted_count":0}`；清后 `scopes` 两侧同体缩减到 1 个 scope；库侧：命中的 `world` 行 `consolidated_at` 归 NULL。⚠️ 第一次比对时误把**本服务的 id** 发给参考，得到 `0 vs 1`——那是夹具 id 不同造成的假差异，不是缺陷（两侧 id 空间独立） |
| 37 `POST /v1/default/banks/{bank_id}/consolidate` | ✅一致（含一处本轮核实后**按参考保留**的语义） | `api/http.py:9705-9737`（`request: ConsolidationRequest | None`、校验先于 handler）；`engine/memory_engine.py:22768-22834`：`caller_requested` 才 `_refuse_bank_wide_if_scoped(..., "run consolidation on the bank")`；**`dedupe = observation_scopes is None`**（`:22822` —— body 里 `observation_scopes` 出现 `[]` 就永不去重，`null` 仍是 `None` 故照常去重）；`_submit_async_operation`（`:22056-22245`）：未知 bank → `OperationValidationError("Bank '<id>' not found", 404)`；`dedupe_by_bank` 只与 `status='pending'` 的、`task_payload->'observation_scopes' IS NULL` 的行去重（**scoped 的 pending 不吞全库**） | `internal/reporting/handlers.go:687-718`（校验 → `RefuseBankWideIfScoped` → `requireBank` → scoped/全库分派）、`:766-795`（`parseConsolidationRequest`）；`internal/storage/postgres_enqueue.go:187-204`（`task_payload->'observation_scopes' IS NULL`）、`internal/storage/consolidation_scope.go:244-290` | **无代码改动（复核）**：`{"observation_scopes": null}` 与省略该成员同档（`deduplicated:true`，折叠进 pending 全库 pass），`{"observation_scopes": []}` 恒 `false`，两侧一致。**第二轮措辞更正**：第二轮把「显式 null 成员」归入「每次都新 op」一档是错的——参考的判据是 `dedupe = observation_scopes is None`（`engine/memory_engine.py:22822`），显式 null 解码后同样是 `None`，因此与省略同档、照常去重；只有**非 null 列表**（含 `[]`）才永不去重。本轮曾试加一处 `consolidationBodyNamesScopes` 的防御判据，实测在全部可达输入上都被 `!= nil` 覆盖、无任何可观测变化，按「最小正确 diff」与「改动必须可证伪」两条已**回退**，现实现保持原样 | 线上**逐字节 `SAME`**：未知 bank 空 body/`{}`/`null`/scoped → 404 `Bank 'rt-ghost-ob4' not found`；422 逐字节同体（`[[1]]`/`[1]`/`"x"`/`[]`/`5`→`model_attributes_type loc:["body"]`；`{"observation_scopes":[["a"],"b"]}`→`list_type loc:[…,1]`；`{"observation_scopes":[["a",1]]}`→`string_type loc:[…,0,1]`）；真实 bank：`[]` 三次连打两侧都 `deduplicated:false` 且各有新 op；空 body/`{}`/`null`/`{"bogus":1}`/`{"observation_scopes":null}` 在**同一银行、无别的 pending** 时两侧都把同一次提交折叠进前一次的 pending（`{"observation_scopes":null}` 实测：参考 `deduplicated:true` 且 `operation_id` 与上一次全库提交相同，本服务同）。⚠️ **去重的对照实验只能在同一银行、同一 pending 生命周期内做**：我最初把「全库 ×2 连打」当作对照，得到两侧布尔序列错位（`true/false` vs `false/true`），追查后确认是两侧 worker 消费 pending 的毫秒级时序差异（PLAN §4 的噪声），不是缺陷——改成「先造一条 pending、再发一次请求、看是否折叠」后两侧稳定 `SAME` |

**本单元结论计数（按 API）：✅ 6 / ❌ 0 / ⚠️ 0 / ⛔ 0。**

### 需要组长裁决 / 跨作用域事项

1. **#37 曾试加的一处防御判据已回退（组长裁决）**。`parseConsolidationRequest` 里曾加过
   `|| consolidationBodyNamesScopes(body)`，让「body 里 `observation_scopes` 是非 null 列表 → scoped」不再依赖
   nil 切片。实测该判据在**全部可达输入上都被 `req.ObservationScopes != nil` 覆盖**：`{"observation_scopes": null}` 仍
   `scoped=false`（→ 全库 `EnqueueConsolidation` → 折叠进 pending → `deduplicated:true`，正是参考的行为），`[]` 仍
   `scoped=true`（`DecodeLax` 给空数组分配非 nil 切片 → scoped 路径 → 恒 `false`）。没有任何可观测行为改变、也拿不到
   「改回去就 FAIL」的证伪，按 AGENTS.md「最小正确 diff」与「改动必须可证伪」两条**已回退**，`internal/reporting/handlers.go`
   的 `parseConsolidationRequest` 回到原样，报告里这一条按「复核」记，不记为改动。

2. **第二轮 #37 的记录需要更正**：第二轮写「`{"observation_scopes":[]}` 与 `{"observation_scopes":[["user:bob"]]}` 每次都新 op」✅，但同表把「`null` 成员」归到 `{"bogus":1}` 一档只在「body 无该成员」时成立；本轮实测参考对 **显式 null** 与 **无该成员** 都是 `None`（都去重）。本轮表格已按参考重写，第二轮的布尔序列结论（去重表）仍成立，只是措辞需要以本轮为准。
3. **#36 的落点不在本单元写作用域**（`internal/reporting/memory_routes.go` 归 memory-crud）。本轮只读复核 + 线上差分，**未改**；其实现与参考对齐（`_require_writable` 无 scope 时不查 memory、`deleted_count > 0` 才 requeue/提交、`enable_auto_consolidation` 才提交）。
4. **参考侧数据库不可直连**（`5432` 的 `user_devin` 密码未知、参考进程未设 `WENYUAN_API_DATABASE_URL`，用的是嵌入式 pg0），所以 #32/#35 的「写库行与列」只在**本服务侧**直查 `15432` 取证，参考侧只能看 HTTP 响应；两侧库不同本身是 PLAN §4 的已知噪声。
5. **噪声提醒（不是缺陷）**：worker 常开时，`GET …/stats` 的 `total_links`/`total_documents`/`links_by_link_type` 两侧天然不同（我造了 3 个同名文档、参考合并成 1 个），`operations_by_status.completed` 累计值也不同；这些不在本单元 6 条 API 的契约里，未计入差异。


## 第四轮 lead（补齐剩余 48 条：路由层 / 存储层 / app / 组长自证）

> 第三轮只开了 52 条单元，剩余 **48 条**未做逐条取证。本轮把这 48 条按 `docs/parity-round/ROUND4.md`
> 拆成 9 组查完（#19/#20、#26/#27、#28–#31、#32–#37、#38–#46、#53–#54、#55–#59、#60–#72、#73–#77）。
> 方法、工具与噪声清单沿用 `docs/parity-round/PLAN.md`。

### 0. 本轮补上的一条方法论缺陷：键序盲区

第三轮与第四轮前期用的差分工具 `/tmp/rt/chk.py` 会**把 JSON 解析成 dict 再比较**，于是
「成员相同、次序不同」被当成 `SAME`。**参考实现用 pydantic 响应模型，成员按声明序输出；本服务多处用
`map[string]any`，Go 的 map 是乱序的**——这一整类差异都被漏掉了。

修复：`chk.py` 改为 `object_pairs_hook=OrderedDict` 并保留迭代序比较（自测：同成员不同序 → 判为不同）。
本轮 **6 处差异**正是靠这个才被发现（mental-models 单元的 5 处 + 组长复核的 1 处）。

> 教训：**"一致"必须建立在能看见被比对象的工具上**。凡是拿 `map[string]any` 渲染响应的地方，
> 都要单独确认键序。

### 1. 组长自证（#19–#20、#53–#54、#55–#59、#60–#72、#73–#77）

| # | API | 结论 | 参考证据 | 修复 / 验证 |
|---|---|---|---|---|
| 19 | `PATCH .../config` | ✅一致 | `api/http.py:9641` | 11 组 body（合法键、未知键、null/[]、`updates` 非对象、类型错、`recall_budget` 反转、`CHUNKS`/`bogus`、`disposition` 越界、`retain_chunk_size` 0/-1）两侧全 SAME |
| 20 | `DELETE .../config` | ✅一致 | `api/http.py:9678` | 正常/未知 bank/重复删 全 SAME |
| 53 | `PATCH .../knowledge-base/nodes/{id}` | ❌已修 | 参考的 no-op 守卫是 `trigger is not None`（`engine/memory_engine.py:19917-19918`），而 HTTP 层传的是 `model_dump(exclude_unset=True)`——`{"trigger": {}}` 得到 `{}`（非 None），是**已提供**的 page option | `internal/reporting/knowledge_body.go` 的 `knowledgeUpdateTrigger`：空对象返回 supplied=true。实测 `{"trigger":{}}` 两侧同为「节点不存在」、`{}` 两侧同为 400 no-op。用例 `TestKnowledgeNodeEmptyTriggerObjectIsSupplied`；**证伪** → `empty trigger object = 400: {"detail":"Provide name, parent_id, ..."}` FAIL |
| 54 | `DELETE .../knowledge-base/nodes/{id}` | ✅一致 | `api/http.py:7589` | 幻影 id / 未知 bank 全 SAME |
| 55 | `GET .../directives` | ✅一致 | `api/http.py:7620`；排序 `priority DESC, created_at DESC, id DESC`（`engine/memory_engine.py:20812`） | 4 条受控 priority 的指令两侧**同序**；`limit=0/1001/abc/`空值、`offset=-1`、`active_only=false/bogus`、`tags_match=bogus`、`tags=a`、重复参数、未知 bank 共 11 组全 SAME |
| 56 | `GET .../directives/{id}` | ✅一致 | `api/http.py:7669` | 幻影 id / 非 UUID / urn 形式 全 SAME |
| 57 | `POST .../directives` | ✅一致 | `api/http.py:7699` + `CreateDirectiveRequest` | 12 组校验全 SAME |
| 58 | `PATCH .../directives/{id}` | ✅一致 | `api/http.py:7734` + `UpdateDirectiveRequest`（五成员全可空） | 各字段单改、`tags:[]`/`tags:null` 清除、`priority:null` 不改、`{}`、类型错 全 SAME |
| 59 | `DELETE .../directives/{id}` | ✅一致 | `api/http.py:7771` | 正常删（`{"status":"deleted"}`）、重复删 404、非 UUID、未知 bank 全 SAME |
| 60 | `GET .../graph` | ✅一致（结构） | `engine/memory_engine.py:12527-12860` | nodes/edges/table_rows 计数与键结构一致；`limit=0`、`type=bogus` SAME。**内容差异来自模型随机产出** |
| 61 | `GET .../entities` | ❌已修 | 排序 `mention_count DESC, last_seen DESC, id ASC`（`engine/memories/pg/curation.py:545`）；新实体日期 `COALESCE(event_date, now())`（`engine/db/ops_postgresql.py:399-409`）、记账 `GREATEST(last_seen, $3)`（`entity_resolver.py:550-562`），日期取 `occurred_start ?? mentioned_at`（`engine/retain/entity_processing.py:107`） | `internal/storage/postgres_retain.go`：新建实体按来源事实事件日期写 `first_seen/last_seen`（原为写入时钟 `now()`）；新增 `upsertUnitEntitySeenAt` 按事件日期推进 `last_seen`；`memory_read_surface.go` 新增 `entityEventDate`/`unitEventDateOf`。实测同内容两侧 `first_seen/last_seen` **逐字相同**，按 `mention_count` 排序两侧同序（Bob 3 → Ann 1） |
| 62 | `GET .../entities/graph` | ✅一致（结构） | `api/http.py:6796` | 节点/边结构与参考同形 |
| 63 | `GET .../entities/{id}` | ✅一致 | `api/http.py:6838` | 两侧各取自己的 id：字段集与键序**完全相同**；幻影/非 UUID SAME |
| 64 | `POST .../entities/{id}/regenerate` | ✅一致 | `api/http.py:6895` | 幻影实体 SAME |
| 65 | `GET .../documents` | ✅一致 | `api/http.py:7801` | 有数据时逐字节 SAME；未知 bank、`limit=abc` SAME |
| 66 | `GET .../documents/{id}/chunks` | ✅一致 | `api/http.py:7873` | 幻影文档 SAME |
| 67 | `POST .../{id}/reprocess` | ✅一致 | `api/http.py:7929` | 幻影文档 SAME |
| 68 | `GET .../documents/{id}` | ✅一致 | `api/http.py:7972` | 幻影文档 SAME |
| 69 | `GET .../tags` | ✅一致 | `api/http.py:8009` | 有数据 / `q=x` 全 SAME |
| 70 | `GET /v1/default/chunks/{chunk_id}` | ✅一致 | `api/http.py:8076` | 未知 chunk SAME |
| 71 | `PATCH .../documents/{id}` | ✅一致 | `api/http.py:8115` | 幻影文档 SAME |
| 72 | `DELETE .../documents/{id}` | ✅一致 | `api/http.py:8167` | 幻影文档 SAME |
| 73 | `GET .../operations` | ❌已修（`progress`） | 参考在批次提交后留一条 durable `progress`：`stage="storing"`、`processed`/`total`、`detail={"facts_committed":n}`、带 `at`（`engine/retain/orchestrator.py:2866-2879` 写、`engine/memory_engine.py:5160-5179` 合并） | `internal/app/worker_retain.go` 通过 `ReportOutcome` 写该快照；`internal/storage/{enums,postgres_writes,memory_operations}.go` 的 `operationProgress` 从 `result_metadata.progress` 投影到 `Operation.Progress`。实测两侧 `progress` 对象**成员与序完全一致**（含 `at`）。用例 `TestQueuedRetainRecordsTheStoringProgress`；**证伪**（键改名）→ `result_metadata has no progress object` FAIL |
| 73 | `GET .../operations`（其余） | ✅一致 | `api/http.py:8210` | 16 键**同序**；`type=`/`status=` 过滤、`limit`/`offset`、未知 bank、`limit=abc` 全 SAME |
| 74 | `GET .../operations/{id}` | ✅一致 | `api/http.py:8258` | 详情 17 键**同序**（`operation_id, status, operation_type, id, task_type, …`）；幻影/非 UUID SAME |
| 75 | `DELETE .../operations/{id}`（cancel） | ✅一致 | `api/http.py:8296` | 已完成的操作两侧同为 409 同文案；幻影/非 UUID SAME |
| 76 | `POST .../{id}/retry` | ❌已修 | 参考**先**判 `operation_type == 'batch_retain' and task_payload is None`（`engine/memory_engine.py:21510-21567`），再到通用状态门；已完成且无未完成子批的父操作给「batch parent 无子任务」409，**不是**通用状态 409 | `internal/storage/{postgres_writes,memory_operations}.go` 的 `RetryOperation` 把 batch-parent 判据提到状态门之前（用 `operationIsParent(ResultMetadata)`，与参考的 payload-less parent 等价）。用例 `TestRetryCompletedBatchParentUsesTheBatchParentMessage`（**证伪** → `cannot be retried: status is 'completed'` FAIL）、`TestRetryCompletedRetainKeepsTheGenericConflict` |
| 77 | `DELETE .../{id}/delete` | ✅一致 | `api/http.py:8363` | 已完成的操作两侧同为 200 `{"success":true,…,operation_id}`；幻影/非 UUID SAME |

### 2. 本轮修掉的跨单元回归（由组长定位并修复）

`internal/storage` 的 `RetryOperation` 判据一度写成 `OperationType == AsyncRetainType && Payload == nil`：
`PutOperation` 会把 nil payload 归一成 `{}`，且 `IsParent` 在 `PutOperation` 里不填，于是**子操作也被当成
batch parent** 走 batch 分支，`TestRetryBatchParentRequeuesFailedChildren` 稳定 FAIL。
改为 `operationIsParent(ResultMetadata)` 后两个后端一致，该用例恢复通过。**这条是组长自己引入的回归，
记在此处以免下一轮误以为是他人改动。**

### 3. 本轮验证门（全绿）

| 门 | 结果 |
|---|---|
| `CGO_ENABLED=0 go build ./...` | 通过 |
| `CGO_ENABLED=0 go build -tags pgx ./...` | 通过 |
| `go vet ./...` | 无输出 |
| `gofmt -l .` | 空 |
| `golangci-lint run ./...` | **No issues found** |
| `go test -count=1 ./...` | **4392 passed / 50 packages** |

### 4. 第四轮覆盖统计（48 条全部有结论）

| 单元 | 条数 | 结论 |
|---|---|---|
| `monitor/bank-config`（#19–#20，组长） | 2 | ✅ 2 |
| `recall-reflect`（#26–#27） | 2 | ❌ 2（4 处差异） |
| `memory-crud`（#28–#31） | 4 | ✅ 2 / ❌ 2（4 处差异） |
| `observations`（#32–#37） | 6 | ✅ 6（零差异） |
| `mental-models`（#38–#46） | 9 | ✅ 4 / ❌ 5（6 处差异） |
| `knowledge-extra`（#53–#54，组长） | 2 | ✅ 1 / ❌ 1 |
| `directives`（#55–#59，组长） | 5 | ✅ 5 |
| `read-surface`（#60–#72，组长） | 13 | ✅ 12 / ❌ 1 |
| `operations`（#73–#77，组长） | 5 | ✅ 3 / ❌ 2 |

**48 条：✅ 35 / ❌ 13 / ⚠️ 0 / ⛔ 0。** 累计本轮发现并修复的真实差异 **17 处**（每组内的多处差异见对应小节）。

### 5. 第四轮之后仍存疑的（仅剩 4 条，均为"已定性"而非"未查"）

1. `POST .../files/retain` 的顶层操作类型 `file_convert_retain`（需 storage + worker 结构性改造，不改变客户端契约）
2. `dry-run-extract` 的 `verbatim` 模式（**参考自身缺陷**：`fact_text` 写、`fact` 读，照抄会让预览永远为空）
3. `GET .../llm-requests` 的 `metadata` 回填与 `llm_info` 两成员（需 storage+memoryops+app.go 协同 / provider 端口加命名）
4. `PATCH .../config` 的并发行锁（无线上证据，需并发压测）

其余"存疑"均为**跨语言不可复刻**（无界整数驱动层、`python_*` 平台指标）或**参考缺陷**（空 `and` 子滤器 500）。

## 第五轮 monitor

> 单元 `monitor`（#1–#5）。参考源码 `api/http.py:5815/5832/5849/5868/5913`，真实逻辑在
> `engine/memory_engine.py:6580-6645`（`health_check` + `_pool_health_stats`）、
> `engine/db/postgresql.py:338-344`（`_pool_stats`）、`liveness.py:85-92`、`loop_watchdog.py:171-196`。
> 本服务落点 `internal/monitoring/{health,version,metrics,workload,looplag}.go`、
> `internal/app/app.go:515-545,677-703`、`internal/storage/postgres.go:314-341`。
>
> 环境：本服务 `scripts/parity_dev_service.sh 18210`（**必须加 `WENYUAN_API_ADMIN_LISTEN=127.0.0.1:18290`**，
> 否则 `etc/wenyuan.yaml` 的 `admin_listen: 127.0.0.1:18890` 与既有进程撞端口起不来），
> 参考 `http://127.0.0.1:8888`。差分工具 `scripts/parity_check.py`。
> **注意**：PARITY-CHECKLIST §3 列的"第四轮工具 `/tmp/rt/chk.py`"在本机**不存在**，本轮全部用
> `scripts/parity_check.py` + 手工掩码逐字节比对；该工具 `normalise()` 会把 JSON 解析成 dict，
> **看不见键序**，所以键序一律另做"掩活测量后 diff 原始字节"取证（见每行"验证"列）。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #1 `GET /health` | ✅一致 | `api/http.py:5815`（`health_endpoint`）→ `:5809-5814` `_readiness_response`（`status=="healthy"` 决定 200/503）→ `engine/memory_engine.py:6593-6613`：**未初始化**先返回 `{"status":"unhealthy","reason":"not_initialized"}`（`:6604-6605`）；**失败**返回 `{"status":"unhealthy","database":"error","error":repr(e)}`（`:6611-6613`，注意是 `repr` 不是 `str`，且**不含** `db_acquire_ms`/池键）；**成功**先写 `status`/`database`/`db_acquire_ms`（`:6605-6609`，`round(acquire_ms,1)`）再 `health.update(self._pool_health_stats(backend))`（`:6609`）；`_pool_health_stats` 插入序 `:6622-6624` `db_pool_waiting`（无条件，源码 `:6623`，仅 import 失败才缺）→ `:6626-6632` `db_pool_in_use`/`db_pool_max`/`db_pool_idle`（`_pool_stats` 返回 None 才缺） | `internal/monitoring/health.go:123`（`Ready`）、`:130-173`（`readyBody`，三条分支与上逐条对应：`:131-135` not_initialized、`:140-146` error 分支不带池键、`:152-171` 成功分支先 waiting 后 in_use/max/idle）、`:178-183`（`errorText` = 空消息时回 `%T`，对齐 `repr` 的"命名异常类"意图）、`:92-113`（`HealthBody` 字段声明序 = 输出序）；`internal/app/app.go:515`、`:677-703`（`storePinger`）；`internal/storage/postgres.go:314-341` | 无 | 实测（18210 vs 8888）**掩掉活测量后响应体逐字节相同**：<br>`{"status":"healthy","database":"connected","db_acquire_ms":M,"db_pool_waiting":0,"db_pool_in_use":0,"db_pool_max":100,"db_pool_idle":M}`<br>键与键序两侧完全一致（`diff` 无输出）。`?x=1` → 两侧同 `200` + `X-Ignored-Params: x`（header 名大小写不同是 HTTP/1.1 语义等价）；`?foo=bar&baz=2` 同。`POST/PUT/DELETE/PATCH/HEAD/OPTIONS` → 两侧同 `405` + `Allow: GET` + `{"detail":"Method Not Allowed"}`（31 字节同长）。`/health/` → 两侧同 `307` 去斜杠。残余仅 `db_acquire_ms`/`db_pool_idle`/`db_pool_in_use` 三个活测量（各自进程连接池实时状态），属 checklist §3 明列噪声 |
| #2 `GET /health/ready` | ✅一致 | `api/http.py:5832-5847`（`readiness_endpoint`，operationId `get_readiness`），**与 `/health` 调同一个 `_readiness_response()`**，逐字段无差别 | `internal/monitoring/health.go:305-309`（`BindHealthRoutes` 把 `PathHealth` 与 `PathReady` 绑到同一个 `h.Ready`） | 无 | 同 #1：掩活测量后逐字节相同；响应头只有 `Content-Length`/`Content-Type: application/json`（与参考的 `content-length`/`content-type` 一致），无 `X-Ignored-Params`（未带未知参数时）；`/health/ready/` 同 `307` |
| #3 `GET /health/live` | ✅一致 | `api/http.py:5849-5865`（`liveness_endpoint`）→ `liveness.py:85-92` `liveness_response()`：`status="alive"`、`version=__version__`、`uptime_seconds=round(monotonic()-_PROCESS_START,1)`（`liveness.py:39-41`；`_PROCESS_START` 取**模块 import 时刻**，`:35`）；响应模型 `LivenessResponse` 字段序 `status,version,uptime_seconds`（`liveness.py:47-61`）。**绝不碰数据库** | `internal/monitoring/health.go:185-193`（`Live`）、`:115-120`（`LivenessBody` 成员序同参考）、`:14-29`（`Float`：整数值补 `.0`，对齐 Python `json.dumps` 的 `812.0`）、`:202-207`（`round1` = `math.RoundToEven`，对齐 Python `round()` 的银行家舍入）、`:195-200`（`startedAt`） | 无 | `parity_check GET /health/live` → **SAME**。附加实测：`/health/live` **不带** Authorization 也返回 200（两侧同），证明不触库；`uptime_seconds` 两侧各为自身进程年龄（活测量噪声）。`round1` 的 ROUND_HALF_EVEN 由 `health_test.go` 内既有用例钉住 |
| #4 `GET /version` | ✅一致 | `api/http.py:5868-5908`（`version_endpoint`，双装饰器同一 handler：`:5868` `/version` 与 `:5882` `/v1/version` `include_in_schema=False`）→ 返回 `VersionResponse(api_version=__version__, features=FeaturesInfo(...))`；`FeaturesInfo` 成员声明序 `api/http.py:4828-4844`：`observations, mcp, worker, bank_config_api, bank_llm_health, file_upload_api, document_export_api, document_import_api, audit_log, llm_trace, store_document_text` | `internal/monitoring/version.go:23-94`（`VersionBody` 按 `config.FeatureKeys` 声明序**手写序列化**）、`:96`（`Serve`）；`internal/app/app.go:518-522` | 无 | 实测：`api_version` 两侧同为 `1.1.0`；`features` **11 个键与键序完全相同**；逐键比对**只有 `features.worker` 不同**（参考 `true` / 本服务 `false`）—— `scripts/parity_dev_service.sh` 固定 `WENYUAN_API_WORKER_ENABLED=false`，是 checklist §3 明列的部署夹具噪声，**不是行为差异**。`/version/` 同 `307`。旁证：`/health/live` 的 `version` 字段两侧同为 `1.1.0`，与 `/version` 的 `api_version` 同源一致 |
| #5 `GET /metrics` | ⚠️存疑（不修，组长裁决 2026-11-03：保留现状，记为已知实现差异） | `api/http.py:5913-5941`（`metrics_endpoint`）：`worker_metrics.render` 或 `generate_latest`（`asyncio.to_thread` 渲染），`media_type=CONTENT_TYPE_LATEST` = `text/plain; version=1.0.0; charset=utf-8`。家族声明点：`metrics.py:623-632`（`hindsight.event_loop.stalls` 计数器 + `hindsight.event_loop.stall_duration` 直方图在 **collector 构造时** `create_counter/create_histogram`）；但 OTel SDK 的 Prometheus exporter **不导出任何样本**（连空 `_total`/`0.0` 也不导出），只有 `add`/`record` 过才有系列 —— 真正的产出点是 `loop_watchdog.py:171-196` `_report()` 里的 `get_metrics_collector().record_loop_stall(blocked_for)`（`:194-196`，且外层 `try/except Exception: pass`），它只在**检测到停顿**后才调用；`metrics.py:938-945` `record_loop_stall` 才是 `stalls.add(1)` + `stall_duration.record(...)` | `internal/monitoring/metrics.go:98-105`（`Serve`，显式写 `Content-Type` + `Content-Length`）、`:112-137`（`Exposition`）、`:18`（`PrometheusContentType` 常量）、`:20-21`（`MetricsScopeName`）；**存疑点**：`internal/monitoring/looplag.go:92-99`（`Run` 启动时若 `Stall>0` 就 `DeclareEventLoopStall()`）、`internal/monitoring/workload.go:337-361`（`declareLoopStall` 无条件 `add(...,0)` + `observe(...,0)`）、`internal/app/entrypoint.go:137-145`（探针在 `LoopWatchdog || LoopLagMetric` 时启动） | 无（待裁决，见下） | 响应头：两侧 `content-type: text/plain; version=1.0.0; charset=utf-8` 相同；本服务额外显式给 `Content-Length`（参考给 `content-length`，等价）。**家族名归一化比对**（参考 `hindsight_` → `wenyuan_`）：本服务**无独有缺失家族**；参考独有的 23 个全部可解释 —— `python_gc_*`(3)/`python_info`/`process_*`(6) 是 `prometheus_client` 默认寄存器与宿主机进程指标；`wenyuan_llm_*`(7)/`operation_*`(2)/`recall_phase_*`/`retain_*`(3)/`consolidation_batch_failures` 是参考库上有存量流量而已有观测（本服务实测跑一次 retain 后即出现，属"空载"而非"缺失"）。本服务独有的 `wenyuan_event_loop_stalls_total`/`wenyuan_event_loop_stall_duration_seconds` 是存疑点，见下 |

**#5 的存疑点（需要组长裁决，不是已完成结论）**

`wenyuan_event_loop_stalls_total` 与 `wenyuan_event_loop_stall_duration_seconds` 在**本服务**的健康进程上以
"零值系列"出现：

```
wenyuan_event_loop_stalls_total{...} 0.0
wenyuan_event_loop_stall_duration_seconds_bucket{le="0.0",...} 1.0   <- 注意 bucket 计到 1.0，count=1.0
...
wenyuan_event_loop_stall_duration_seconds_count{...} 1.0
```

这两族在**参考**当前暴露体里**不存在**（`grep -c event_loop_stalls /metrics` = 0）。两条证据链：

1. 参考的 `create_counter`/`create_histogram`（`metrics.py:623-632`）**不产生样本**——OTel Prometheus exporter
   只导出被 `add`/`record` 过的系列，所以"声明即导出"在参考上不成立；
2. 参考的 `record_loop_stall` 唯一调用点是 `loop_watchdog.py:194`，在 `_report()` 里，**只有真检测到停顿才走**。

因此参考的语义是：**没停过 = 没有这两族**；本服务是**没停过也有零值**。更进一步，本服务当前的
"零值"并非真正的空直方图：`declareLoopStall` 的 `r.observe(..., 0)` 记了一条 `0` 秒观测，导出成
`count=1.0`、`_bucket{le="0.0"}=1.0` —— 与"零次停顿"的自相矛盾（0 次停顿却 count=1）。

第四轮的 `## 组 A 监控面` 把这条判为 `❌已修`（理由是"参考声明了就一定有），**该理由经本轮复核不成立**。
反过来，本服务也不该走"完全不出现"——参考在**真停顿后**会出现这两族。正确的共同行为是：
**只在该进程真发生过停顿时才产出系列**（`ObserveLoopStall` 已经做了这件事，问题只在 `declareLoopStall`
这条"预声明"路径）。

**组长裁决（2026-11-03）：判 `⚠️存疑（不修）`。** 理由：回退会推翻第四轮已判定的 `❌已修` 项，且参考的
"缺失系列"依赖运行时真停顿，**空载时两侧都看不到系列，差分无法稳定复现**。权衡后保留现状，把差异记为
**已知实现差异**：本服务在进程启动时预声明两族零值，参考按需（真停顿后）产出。`looplag.go` / `workload.go`
**不做改动**，既有用例 `TestLoopLagProbeDeclaresStallCounter` / `TestWorkloadLoopStallsDeclaredAtZero` 保留，
它们钉住的是本服务有意选择的这一行为。

**本单元验证门结果**（局部；全量 `go build ./...` 当时被 bg-maintenance 单元的中间态阻塞，见下）：
`go build ./internal/monitoring/... ./internal/bankapi/...` 通过；
`go test -count=1 ./internal/monitoring/... ./internal/bankapi/...` 全绿；
`go vet ./internal/monitoring/... ./internal/bankapi/...` 无输出；`gofmt -l internal/monitoring internal/bankapi` 空；
`golangci-lint run ./internal/monitoring/... ./internal/bankapi/...` → No issues。
（注：`golangci-lint` 不在默认 `PATH`，实际路径 `/data/go/bin/golangci-lint`；
全量 gate 阻塞原因：`internal/app/maintenance_jobs.go:325,330,343,345,381` 与 `internal/storage` 接口签名不同步。）

**monitor 小结：#1 ✅ / #2 ✅ / #3 ✅ / #4 ✅ / #5 ⚠️（5 条：✅ 4 / ❌ 0 / ⚠️ 1）**

## 第五轮 bg-worker

> 单元 `bg-worker`（checklist §7 后台任务组）。参考落点：`worker/poller.py`（2450 行，`WorkerPoller`）、
> `worker/main.py`、`worker/backpressure.py`、`worker/exceptions.py`、`worker/stage.py`、
> `engine/background_writes.py`、`engine/retain/embedding_coalescer.py`、`metrics.py::_backlog_refresh_loop`，
> 以及 claim SQL 的真实实现 `engine/db/ops_postgresql.py::claim_tasks`（`_claim_shared_tasks` /
> `_claim_reserved_tasks` / `_claim_consolidation_*`）。
> 本服务落点：`internal/worker/{consumer,outcome}.go`、`internal/app/{worker,worker_retain}.go`、
> `internal/config/worker.go`、`internal/storage/{postgres_claim,memory_claim,postgres_claim_retry,postgres_operation_defer}.go`、
> `internal/app/metrics.go`（backlog 指标，作用域外只读）、`internal/monitoring/worker_metrics.go`、
> `internal/provider/embed/plumbing.go`（作用域外只读）。
>
> **取证方式**：本单元 8 条全是**进程内机制**，没有可观测的 HTTP 面，因此不适用线上差分；
> 按 checklist §3 末条用**源码逐行比对 + 往返用例**取证。所有用例在**隔离副本**
> `.bgw-base/`（`git archive HEAD` + 只回滚 `internal/storage/{maintenance,memory_maintenance,postgres,postgres_maintenance}.go`
> 到 HEAD，因为 `auditor-bg-maintenance` 当时正在改这几处、`internal/storage` 处于编译不通过的中间态）
> 上跑通，避免被同事的中间态阻塞。
>
> 环境说明：`go build ./...` 全量 gate 当前**红**，5 个错误在 `internal/app/maintenance_jobs.go:325/330/343/345/381`
> （别人的作用域）；本单元只做局部验证 `go build ./internal/worker/...` +
> `go test -count=1 ./internal/worker/...`（55 passed）。`golangci-lint` 在 `/data/go/bin/`。

### 机制 1：恢复逻辑（重启后接管本进程名下的僵尸任务）

| 维度 | 参考实现 | 本服务 | 结论 |
|---|---|---|---|
| 入口 | `run()`（`poller.py:1895`）先读 **DB 时钟** `SELECT now() AT TIME ZONE 'UTC'` 作为 `started_at`，再 `asyncio.create_task(self._run_recovery(started_at))`，**与轮询循环并发**（`:1885-1912`，注释 #5407：几万 schema 的走查要几分钟，不能阻塞认领） | `Consumer.Run`（`internal/worker/consumer.go:377-405`，`defer c.releaseHeldWork()` 在 `:463`）直接起 N 个 slot goroutine 轮询；**没有独立的启动恢复过程** | 结构不同 |
| 逐 schema 走查 | `recover_own_tasks`（`:1588-1655`）遍历 `tenant_extension.list_tenants()`，每 schema 依次跑：`_recover_batch_operations` → `_reclaim_own_processing_tasks` → `_reconcile_orphaned_parents`；每 1000 个 schema 打一行进度（`_RECOVERY_PROGRESS_EVERY`，`:125`） | 无走查；恢复是在**认领时**按行发生（见下） | 结构不同 |
| 低于预算的行 | `_reclaim_own_processing_tasks`（`:1496-1565`）：`UPDATE ... SET status='pending', worker_id=NULL, claimed_at=NULL, retry_count=COALESCE(retry_count,0)+1 WHERE status='processing' AND worker_id=$self AND result_metadata->>'batch_id' IS NULL AND COALESCE(retry_count,0) < $max_retries`，外加 `claimed_before` 截止条件 | `ClaimNextOperation`（`internal/storage/postgres_claim.go:60-100` / `memory_claim.go:22-100`）的候选谓词把 `status='processing' AND claimed_at <= now()-lease` 当作可认领；认领时 `claimLocked` / SQL CASE 对「原本是 processing」的行做 `retry_count+1`（`memory_claim.go:129-146`、`postgres_claim.go:93-97`） | **语义等价，触发时点不同** |
| 达到/超过预算的行 | 第二个 UPDATE 直接置 `failed`，`error_message = 'exceeded max recovery attempts (retry_count >= {max_retries})'`（`:1527-1546`），并 `RETURNING operation_id` 逐个 roll-up 父批 | `reapExhaustedRecoveries`（`postgres_claim.go:294-334` / `memory_claim.go:263-310`）在每次带 `WithReclaimLimit` 的认领前先跑同一条 UPDATE，**文案一字不差**（`internal/storage/claim_reclaim.go:45-47`），并同样 roll-up 父批 | ✅一致（含文案） |
| 批量 API 操作 | `_recover_batch_operations`（`:1657-1745`）把 `result_metadata ? 'batch_id' AND task_payload IS NOT NULL` 的 processing 行重置为 pending（**不花 retry**） | **无对应物**：本服务没有 `batch_id` 这条 OpenAI Batch 通道（`ResultMetadata` 里没有该键的使用）。属参考未在本服务建模的功能面，不是恢复逻辑的缺失 | 不适用 |
| 孤儿父操作 | `_reconcile_orphaned_parents`（`:1747-1871`）：启动时把「`pending` + `task_payload IS NULL` 且没有未完成子批」的 batch_retain 父操作驱到终态；零子批的置 `failed` 并给 `orphaned batch_retain parent: no child sub-batches were persisted (worker crashed mid-submission); resubmit the source documents` | **无对应物**。本服务的父操作只由 `promoteParent`（`internal/worker/outcome.go:141-160` → `store.FinalizeBatchParent`）在子操作终态时推进；若进程在「子批已终态、父批尚未 promotion」窗口崩溃，父批会永远停在 `pending`。**但**本服务的 batch 提交路径是单事务（`PutOperation` 连子批一起写），不存在参考的中心化「零子批」窗口 | ⚠️存疑（窗口极窄，见下） |
| 关机释放 | `release_own_tasks`（`:1667-1700`）= 同一套 `_reclaim_own_processing_tasks`，**无 `claimed_before`** | `releaseHeldWork`（`consumer.go:952-975`）列出 `status=processing AND worker_id=self` 的行逐个 `ReleaseOperationClaim`（`postgres_claim.go:169-197`） | ✅一致（本服务**不花 retry**，见差异 1） |

**差异 1（已知、组长裁决本轮不改）：接管时点**
参考在**进程启动时**就把自己名下的行拿回来；本服务要等 `claimed_at` 超过 lease（默认
`WENYUAN_API_WORKER_LEASE_SECONDS=300`，即 **最多 300s**）才会被（自己或别人）重新认领。

- **可观测后果**：worker 崩溃/重启后，它当时在跑的那几个操作会在 `pending/processing` 上多停 ≤300s 才开始重跑；
  同 bank 上被 `serialization_key` 锁住的后继操作也会跟着等。参考是重启后**秒级**接管。
  不影响最终状态（都会恢复），只影响恢复时延；不产生重复执行（见下），不产生双写（追加写冲突由
  「机制 5 的 append-conflict 退避」兜住）。
- **为什么不改**：改它要把「启动时按 worker_id 扫一遍」塞回 claim 语义，动的是认领路径本身，风险大于收益，
  且不属 API 契约。组长 2026-11-03 裁决：**记为 ⚠️存疑，本轮不改**。

**差异 2（本轮已修）：关机释放不该花 retry**
参考 `release_own_tasks` 走 `_reclaim_own_processing_tasks`（`retry_count+1`）；本服务 `ReleaseOperationClaim`
**不增 retry_count**（`postgres_claim.go:180-185` 只清 worker_id/claimed_at）。这一处**本服务更对**：
一次干净关机的任务不该消耗崩溃预算。**不改**，记录为有意偏差（不改变客户端契约：`retry_count` 只在
`GET /operations` 里展示）。→ 组长裁定同此。

**差异 3（需要裁决）：`retry_count` 的一次性口径**
参考在**置换回 pending 时**就 `retry_count+1`（`:1511-1518`），本服务在**重新认领时**才 `+1`。
后果：同一行在「参考已加 1、本服务还没加」的窗口里，`retry_count` 相差 1；且本服务如果该行在
lease 过期后被 `reapExhaustedRecoveries` 判死，判据用的是**未加 1** 的值。边界：`retry_count == max_retries-1`
的行，参考在下一次崩溃后判死（`>= max`），本服务还要再跑一轮才判死——**多跑一次，不算重复执行**。
→ ⚠️需裁决，默认保留现状（差异只影响「多崩一次」这个极端场景）。

## 第五轮 memory-crud

> 单元 `memory-crud`（#28–#31）。参考源码 `api/http.py:6047-6100`（`api_list`）、`:6227-6257`
> （`api_get_memory`）、`:6259-6319`（`api_update_memory`）、`:6321-6360`（`api_get_observation_history`）；
> 引擎 `engine/memory_engine.py:13602-13650`（`list_memory_units`）、`:13704-13760`（`get_memory_unit`）、
> `:12600-12960+`（`update_memory_unit`）、`:13821-13920`（`get_observation_history`）；
> SQL/渲染全部在 `engine/memories/pg/curation.py`（`list_memory_units` `:36-330`、`get_memory_unit`
> `:318-438`）、`engine/memories/pg/engine_curation.py:243-289`（`observation_head` /
> `source_fact_summaries`）。
> 本服务落点 `internal/reporting/memory_routes.go`、`internal/reporting/memories.go`、
> `internal/storage/postgres_memories.go`。
>
> 环境：本服务 `scripts/parity_dev_service.sh 18213`（**必须** `WENYUAN_API_ADMIN_LISTEN=127.0.0.1:18233`
> 避开 `etc/wenyuan.yaml` 写死的 `127.0.0.1:18890`，并 `WENYUAN_API_TENANT_MODE=single` +
> `WENYUAN_API_TENANT_API_KEY=sk-hs-…` + `WENYUAN_API_DATABASE_SCHEMA=audit_memops` 用独立 schema，
> 绝不写参考库 `user_devin`），参考 `http://127.0.0.1:8888`。
> 差分脚本：本目录外置的 `px.py`（urllib 直连两侧，掩掉时间戳/UUID 后**逐字节**比对，因此能看见键序，
> 不是 `parity_check.py` 那种先 `json.loads` 再比 dict 的比对）。
> fixture：`audit-memops-wp9neb`（本轮随机名，两侧各建各的，结束删除）。
>
> **验证状态标注**：本轮 #29/#31 的结论是**在参考侧线上实测**得到的（两侧同一请求，结论为两侧实测行为
> 对比）；但从"发现差异"到"修好并复测"的那一步受环境限制（见文末「验证阻断」），凡未复测的条目在
> 「验证」列写明所缺的那一步。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #28 `GET .../memories/list` | ✅一致 | `api/http.py:6047-6100`：`type`/`q`/`consolidation_state`/`state`/`document_id`/`entity_id`/`tags`/`tags_match`/`time_field`/`start_date`/`end_date`/`limit`(100,ge=0)/`offset`(0,ge=0)；`engine/memories/pg/curation.py:36-330`：`state` 只在 `valid`/`invalidated` 取值（否则 `ValueError` → 400）、`entity_id` 先过 `uuid.UUID()`（否则 400）、`consolidation_state` 小写后三值（否则 400）、`tags` 按 `tags_match` 展开、`tags_match=exact` 且无 tags ⇒ `(tags IS NULL OR tags = '{}')`、`created_before`、`time_field` 时间窗；`total` 是**同一 WHERE 的 `COUNT(*)`**（`:246-253`），`LIMIT/OFFSET` 绑定在**参数表末尾**（`:255-263`）；排序 `mentioned_at DESC NULLS LAST, created_at DESC, id`（`:273-292`）；行渲染 22 键，`curation.py:295-330` | `internal/reporting/memory_routes.go:27-181`（`ListMemories`）、`internal/reporting/memories.go:10-45`（`MemoryItem` 22 键 + 可选 `attachments`）、`internal/httpx` 的 lax 整数/日期解析 | 无 | 与参考 `audit-memops-wp9neb` 同请求差分，**20 个探针全 SAME**：`default`、`limit=1&offset=1`、`limit=0`、`limit=1001`、`tags=t1`、`tags=t1&tags=t2`、`tags=t2&tags=t3&tags_match=all`、`tags_match=exact`（无 tags）、`tags=t1&tags_match=exact`、`state=invalidated`、`type=observation`、`type=bogus`（200，不校验）、`q=Berlin`；错误路径 `state=bogus`→400、`consolidation_state=bogus`→400、`entity_id=notauuid`→400、`tags_match=bogus`→422、`time_field=bogus`→422、`start_date>end_date`→400、`start_date=notadate`→400、`limit=-1`/`limit=abc`→422、未知 bank→404，全部同状态同体。**键序**比对的是原始字节（掩时间/UUID 后），`items` 的 22 个键及其顺序两侧一致；`total`/`limit`/`offset` 回显语义一致（`limit=0` 时 `items:[]` 但 `total` 仍是过滤后全集数） |
| #29 `GET .../memories/{memory_id}` | ❌已修 | `api/http.py:6239-6256`：`get_memory_unit` 先 `uuid.UUID(memory_id)`（失败 `400 Invalid memory_id: '…' is not a valid UUID`），查不到 `404 Memory unit '…' not found`。渲染在 `engine/memories/pg/curation.py:355-438`：**键按 Python dict 插入序**，恒定 20 键 —— `id,text,context,date,type,mentioned_at,occurred_start,occurred_end,entities,document_id,chunk_id,tags,metadata,observation_scopes,state,invalidation_reason,invalidated_at,edited_at,consolidated_at,consolidation_failed_at`；`observation` 另加 `history:[]`，有 `source_memory_ids` 时再加 `source_memory_ids` + `source_memories`。`entities` 走 `_entity_rows_for_units_sql`（observation 继承 source 的实体） | `internal/reporting/memory_routes.go:360-387`（`GetMemory`）、`:903-966`（`memoryDetail`，构造的是 **`map[string]any`**） | 见下「修复 1」 | 线上实测**差异确定存在**：参考 `200` 的 20 键顺序如上；本服务同一请求返回 **Go map 的字典序**——`chunk_id,consolidated_at,consolidation_failed_at,context,date,document_id,edited_at,entities,id,invalidated_at,invalidation_reason,mentioned_at,metadata,observation_scopes,occurred_end,occurred_start,state,tags,text,type`。两侧 **键集相同、键序不同**（不受夹具影响，纯序列化问题）。错误路径 `notauuid`→400、未知 UUID→404、未知 bank→404 均 SAME。**待复测**：改完后需重启服务重打同一请求确认键序一致 |
| #30 `PATCH .../memories/{memory_id}` | ✅一致（本轮未发现新差异） | `api/http.py:6287-6319`：`occurred_start`/`occurred_end`/`mentioned_at` 显式 `null` 被改写成 `""`（`model_fields_set` 判定）；`OperationValidationError` → 自身 status、`ValueError` → 400、`None` → 404；`engine/memory_engine.py:12650-12658` 的校验顺序：`Invalid memory_id` → `Invalid state '…': expected 'valid' or 'invalidated'.` → `text must not be empty.` → `Invalid fact_type '…'` → 实体归一化 → `_require_writable` → 不是 world/experience ⇒ `Memory '…' is a <type>; only world/experience facts can be curated. …`（`:12780-12786`）→ edit+archived ⇒ `Cannot edit an invalidated memory; revert it to 'valid' first.`（`:12788-12789`） | `internal/reporting/memory_routes.go:499-623`（`UpdateMemory`）、`helpers` 里的 `httpx.DecodeLax` / `BodyDecodeFailure` / `JSONDecodeFailure` 分支 | 无（除修复 1 的键序间接影响：PATCH 的响应体同样经 `memoryDetail`，修好 #29 即同修） | 与参考同请求差分（`audit-memops-wp9neb`）：`state=invalidated` + `reason` 两侧同 `200`；随后 `state=valid` 回滚、`context` 编辑、`occurred_start:null` 清空、`entities:[]`、`fact_type":"experience``、`{}` 空体、`text:""`、`{"state":"bogus"}`、`{"fact_type":"observation"}`、未知 UUID、非 UUID 的 id、未知 bank 的探针**状态码与错误体一致**（键序差异归入 #29）。`1e999` 溢出体两侧同为裸 500 `text/plain "Internal Server Error"`（参考 `_internal_error` 之外由框架兜底） |
| #31 `GET .../memories/{memory_id}/history` | ✅一致（本轮复核：原实现正确，一度误改已回滚） | `api/http.py:6321-6360` → `engine/memory_engine.py:13821-13920`：①`uuid.UUID(memory_id)` 失败 → `ValueError` → 400；②`observation_head`（`engine/memories/pg/engine_curation.py:243-262`）**只查 `memory_units`**，查不到 → `None` → 404；③`fact_type != "observation"` → **`[]`**（`:13854-13855`）；④`observation_history` 按 `changed_at ASC, id ASC` 读，`previous_*` 取自 `content` 的**原样值**（不重解析时间）；⑤`source_facts` 由 `source_fact_summaries`（`engine_curation.py:266-289`）**只查 `memory_units`** 解析，缺失的 id 落为 `{"id":…,"text":null,"type":null,"context":null}`；⑥元素键序 `previous_text,previous_tags,previous_occurred_start,previous_occurred_end,previous_mentioned_at,changed_at,new_source_memory_ids,source_facts`，`source_facts` 元素 `id,text,type,context,is_new`。**关键**：invalidate 是**移动**语义 —— `invalidate_memory`（`engine/memories/pg/writes.py:421-456`）`INSERT INTO invalidated_memory_units SELECT …` 后 `DELETE FROM memory_units`，所以归档行在 `observation_head` 里读不到 ⇒ **history 对已失效单元是 404** | `internal/reporting/memory_routes.go:743-790`（`MemoryHistory`，`:757-767` 的 `unit.InvalidatedAt != nil` → 404 分支正是参考语义）、`internal/storage/observability.go:456-468`、`internal/storage/postgres_memories.go:764-805`（`decodeSnapshot` 原样保留快照拼写） | 无（**回滚了本轮的误改**，见下「修复 2（已撤销）」） | **线上实测（2026-11-03，`audit-memops-jx7ade` 新建 bank）**：①活着的 world fact：两侧 `200 []` **SAME**；②`PATCH {"state":"invalidated"}` 后：参考 **`404 {"detail":"Memory unit '<id>' not found"}`**、本服务 **同 404 同文案 SAME**；③同一时刻 `GET .../memories/{id}` 两侧仍 `200` 且 `state:"invalidated"`（detail 有归档 fallback，history 没有）—— 这个不对称正是参考的行为；④非 UUID → 400 SAME、未知 UUID → 404 SAME、未知 bank → 404 SAME。**捕获到的一处本服务与参考的结构差异（非缺陷）**：本服务 PostgreSQL 后端的 `GetMemory`（`postgres_memories.go:307-322`）对归档行有 fallback，所以路由层用 `unit.InvalidatedAt != nil` 来复现“活表读不到”的语义；参考是 SQL 层天然读不到。两者对外表现相同。
**本轮修复**

1. **#29（连带 #30）`:memory detail` 的 JSON 键序是 Go map 字典序，不是参考的 20 键声明序。**
   `internal/reporting/memory_routes.go:930-965` 的 `memoryDetail` 构造 `map[string]any`，Go 的
   `encoding/json` 对 map 按键名排序输出，因此线上读到的是字典序（见上表 #29 的行内证据）。参考是
   `curation.py:398-438` 的 dict 字面量，键按源码出现顺序序列化，客户端若按键序做展示/快照会看到两种
   形状。改法：`memoryDetail` 改返回 `httpx.OrderedObject`（`internal/httpx/body.go:87-100`，已存在，
   按插入序序列化），插入顺序严格照 `curation.py:398-438`。
   `reporting/memory_routes.go` 里 `map[string]any` 的两处嵌套值（`observation_scopes`、
   `source_memories` 元素、`attachments` 元素）不受影响 —— 参考那边也是 dict/列值。
   回归用例：`TestMemoryDetailKeyOrderMatchesTheReference`（比对 20 键的完整有序列表）。

2. **（已撤销）本轮一度把 #31 改成“invalidated 后 history 返回 200 `[]`”，依据是参考侧一条**并非处于活表**的观察（它的 source 已经被归档、且它的 history 本身已非空），实测拿到 `200 []` 就下了结论。**该结论是错的**：在一条全新且仍在活表里的 world fact 上重测，参考 `PATCH {"state":"invalidated"}` 后 history 是 **404**，因为 `invalidate_memory` 把行移出了 `memory_units`（`writes.py:421-456`），而 `observation_head` 只读那张表。原实现的 404 guard 本来就和参考一致，改动已**完整回滚**（`memory_routes.go` 恢复 `unit.InvalidatedAt != nil` 分支，仅把注释从过时的行号更新为上一段引用的 `writes.py` 证据）。回归用例 `TestMemoryHistoryOfAnInvalidatedUnitIsNotFound` 恢复断言 404 —— 去掉该 guard 即 FAIL，因此它同时是“改回去就 FAIL”的守卫。**教训**：history 的结论必须用“全新 bank + 刚写入、未失效过的 fact”来取基线，不能复用被 consolidate/invalidate 污染过的夹具。

**⚠️ 存疑 —— `update_memory_unit` 的 `doing_edit` 判定与 `entities:[]`**

参考 `engine/memory_engine.py:12664-12667`：
```python
doing_edit = any(v is not None for v in (text, context, occurred_start, occurred_end,
                                         mentioned_at, new_fact_type, new_entities))
```
`entities: []` 经 `:12658-12663` 归一化后是 `[]`（**不是 None**），因此 `PATCH {"entities":[]}` 会让
`doing_edit` 为真；对已失效的行走 `:12788-12789` 抛 `Cannot edit an invalidated memory; revert it to
'valid' first.`。本服务 `memory_routes.go:562-563` 的 `doingEdit` 判定用 `body.Entities != nil`，语义相同。
两侧线上探针也都返回同一个错误 —— **判定一致，无差异**，此处仅记录已核对。

**线上复测结果**

`-tags pgx` 构建恢复后，用标准参数重建并重启 18213（`WENYUAN_API_PORT=18213`、
`WENYUAN_API_ADMIN_LISTEN=127.0.0.1:28213`、`WENYUAN_API_TENANT_MODE=single`、
`WENYUAN_API_TENANT_EXTENSION=hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension`、
`WENYUAN_API_TENANT_API_KEY=sk-hs-…`、`WENYUAN_API_DATABASE_SCHEMA=audit_memops`、
`WENYUAN_API_WORKER_ENABLED=false`、`WENYUAN_API_SKIP_LLM_VERIFICATION=true`），并在**全新** bank
`audit-memops-jx7ade` 上重打：

| API | 修复前 | 修复后 |
|---|---|---|
| #29 detail 键序 | Go 字典序（`chunk_id,consolidated_at,…`） | **与参考 20 键声明序逐位相同**：`id,text,context,date,type,mentioned_at,occurred_start,occurred_end,entities,document_id,chunk_id,tags,metadata,observation_scopes,state,invalidation_reason,invalidated_at,edited_at,consolidated_at,consolidation_failed_at`（observation 再追加 `history,source_memory_ids,source_memories`，与参考同序） |
| #31 invalidated history | 404（本服务） vs 404（参考） | 404（本服务） vs 404（参考）—— **SAME**；同时 `GET .../memories/{id}` 两侧都仍 200 |

**环境坑（供后来者省时间）**

1. `bash` 工具的 `/tmp` 与工作区**不跨调用持久**（每次调用独立 namespace），只有 `terminal_open` 的常驻
   shell 里启动的进程能跨调用存活。差分用的 Python 脚本与二进制要放在**工作区**（本轮 `.audit-memops/`），
   该目录每次被清理后需重建（`mkdir -p` 后立刻写文件，不要依赖下一次调用还看得见）。
2. `etc/wenyuan.yaml` 写死 `admin_listen: 127.0.0.1:18890`，与既有进程撞端口，必须 `WENYUAN_API_ADMIN_LISTEN` 覆盖。
3. `-tags pgx` 必须加，否则 `no database driver registered`；`WENYUAN_API_SKIP_LLM_VERIFICATION=true` 否则启动校验超时。
4. `golangci-lint` 在 `/data/go/bin/`。

## 第五轮 observations

> 单元 `observations`（#32–#37）：`DELETE .../observations`、`GET .../observations/scopes`、
> `POST .../consolidation-strategies/preview`、`POST .../consolidation/recover`、
> `DELETE .../memories/{memory_id}/observations`、`POST .../consolidate`。
> 参考 `api/http.py:9728 / 9753 / 9787 / 9822 / 9847 / 9971`。
> **旧结论一律不信任**（第四轮"组 G"只当线索），每条重新取证。
>
> 环境：参考 `http://127.0.0.1:8888`；本服务副本 18216。
> 差分工具 `scripts/parity_check.py`，但其 `normalise()` 会把 JSON 解析成 dict、**看不见键序**，
> 所以键序与 item 顺序一律另做逐字节比对。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #33 `GET .../observations/scopes`（`limit`/`offset` 校验的顺序、按下标引用、空串不回落） | ✅一致 | `api/http.py:9753-9785`（`limit: int = Query(default=100, ge=0, le=1000)` 在前、`offset: int = Query(default=0, ge=0)` 在后，**两者都由框架在 handler 之前校验**）；`engine/memory_engine.py:12409-12446`（`_authorize_bank_read` → `_require_bank_exists` → `observation_scope_counts`） | `internal/reporting/observation_params.go:21-25`（`limit` 声明于 `offset` 之前）、`:36-51`（**收集全部 item**后再一次性 422，不是首个失败即返回）、`:59-82`（`QueryPresent` 判"键是否出现"，出现即校验，空串 `int_parsing`、`input` 为原始文本）、`:101-125`（`frameworkIntFromString` 按 Python `int()` 语义） | 无 | 参考侧实测 5 例（只读请求）：<br>`?limit=10000` → 422 `less_than_equal` `loc:["query","limit"]` `ctx.le=1000`<br>`?limit=-1` → 422 `greater_than_equal` `ctx.ge=0`<br>`?offset=-1&limit=abc` → 422 **两条 item，`limit` 在前 `offset` 在后**<br>`?limit=` → 422 `int_parsing` `input:""`<br>未知 bank → 404 `{"detail":"Bank 'zz-no-such-bank' not found"}`<br>本服务逐条对照代码路径结论相同（参数校验先于 `requireBank`，见 `internal/reporting/observation_reports.go:45-56`）。**待基座恢复后补双向字节级差分** |

## 第五轮 read-surface

> 单元：`read-surface`（#60–#72 + #100，14 条）。参考 `api/http.py`（graph 5939、entities 6935、
> entities/graph 6983、entities/{id} 7025、regenerate 7082、documents 8001、chunks 8080、
> history 8136、reprocess 8177、document 8220、tags 8257、chunk 8324、PATCH 8363、DELETE 8423）。
> 旧结论（第四轮"组 K/组 L"）只当线索，每条重新取证。
>
> **环境**：本服务 18217，`WENYUAN_API_TENANT_MODE=single` + `TENANT_EXTENSION` + `TENANT_API_KEY`
> （`etc/wenyuan.yaml` 是 `tenant_mode: multi`，不覆盖则 `sk-hs-…` 返 401）、
> `WENYUAN_API_DATABASE_SCHEMA` 用**本单元自建**的 schema、`WENYUAN_API_WORKER_ENABLED` 视条目而定
> （history 需要 worker 把 operation 写进 `result_metadata.document_events`）。
> **参考服务的真实数据不在 15432**（`15432/wenyuan` 的 `user_devin`/`audit_read` 建出来都是空库），
> 所以两侧**各自建同名 bank（`audit-read-<rand>`）与同名文档**，只比**状态码、键集、形状、分页语义**，
> 不比绝对 id 与时间戳。差分脚本 `wc-scratch/pc.py`（一次性，用完删除）。

| 单元 `read-surface` 总表 | 结论 | 证据 |
|---|---|---|
| `:path` 转换器（横切 #66/#68/#70/#71/#72/#100） | ✅一致（含 `/` 的 id 已等价） | 见下第一条 |
| #100 `GET .../documents/{document_id:path}/history` | ✅一致（确已复刻） | 见下第二条 |

### 高风险点 1：`:path` 转换器语义（document_id / chunk_id 可含 `/`）

**结论：✅一致（不是缺陷）。** 本服务虽然用普通路径段 + 子树分派，但**实测含 `/` 的 document id
与 chunk id 两侧行为等价**，不需要改 `internal/httpx/` 的路由注册方式。

| 项 | 内容 |
|---|---|
| 参考 | `api/http.py:8080/8136/8177/8220` 是 `{document_id:path}`，`:8324` 是 `{chunk_id:path}`。Starlette 的 `:path` 是 `.*`（至少 1 字符、**可跨 `/`**），且是**贪婪**匹配 |
| 本服务 | `internal/readsurface/handlers.go:48-58` 把 `/documents/` 注册成子树 + `:76-115` `dispatchDocumentMember` 按尾部动作词（`/chunks`、`/history`、`/reprocess`）分派，其余走 document 读/改/删；`GetChunk` 注册为 `internal/httpx/api.go:61` 的 `/v1/default/chunks/{chunk_id...}`（Go 1.22 多段通配符）。**多段通配符返回的是未清洗的 remainder**，所以 `a/../b`、`a/./b` 不会被 ServeMux 折叠 |
| 实测夹具 | 两侧同名 bank `audit-read-zx0nzu`，两侧同名文档 `folder/doc.md`（内容/标签相同），各自 retain 出同名 chunk `<bank>_folder/doc.md_0` |

**实测矩阵（两侧 get-document 载荷按键逐一比对，时间戳归一化）**：

| 请求 | 参考 | 本服务 | 判定 |
|---|---|---|---|
| `GET .../documents/folder/doc.md` | 200，13 键 | 200，13 键 | **SAME**（等价） |
| `GET .../documents/folder%2Fdoc.md` | 200（`%2F` 还原成 `/` 后匹配） | 200 | **SAME** |
| `GET .../documents/folder/doc.md/chunks` | 200，1 条 chunk | 200，1 条 | **SAME** |
| `GET /v1/default/chunks/<bank>_folder/doc.md_0` | 200 | 200 | **SAME** |
| `GET /v1/default/chunks/<bank>_folder%2Fdoc.md_0` | 200 | 200 | **SAME** |
| `GET .../documents/a/b/c` | 404 `Document not found` | 同 | SAME |
| `GET .../documents/folder/doc.md/`（末尾 `/`） | 404 `Document not found` | 同 | SAME |
| `GET /v1/default/chunks/a/b/c`、`/a%2Fb%2Fc` | 404 `Chunk not found` | 同 | SAME |
| `GET .../documents/folder%5Cdoc.md`（反斜杠） | 404 | 404 | SAME |
| `GET .../documents/folder/doc.md/reprocess`（GET 打到 POST 路由） | 404 `Document not found` | 同 | SAME |

**`GetChunk` 的 mux 清洗验证**：`GET /v1/default/chunks/a/../b`、`/a/./b` 若被折叠会命中完全不同的路由，
实测两侧都是 `404 Chunk not found`，说明 `{chunk_id...}` 拿到的是**原始 remainder**，与参考的 `:path` 一致。

### 高风险点 2：`documents/{document_id:path}/history`（#100，全新面）

**结论：✅一致 —— 这一面确已完整复刻，不是空壳。**

| 项 | 参考 | 本服务 |
|---|---|---|
| 路由形状 | `api/http.py:8136-8167`，`prefix` 里带 `:path`；`limit: Query(100, ge=1, le=1000)`、`offset: Query(0, ge=0)` | `internal/readsurface/document_history.go:36-80`；`document_history.go:26-30` 常量 100/1000 与参考同；`handlers.go:88-93` 让 `/history` 后缀**先于**普通成员读分派（含 id 自带 `/` 的情况） |
| 信封形状 | `engine/document_history.py:86-95` → `{document_id,total,limit,offset,items}` | `internal/history/response.go:30-38` |
| 事件形状（10 键） | `document_history.py:71-82` → `document_id,kind,created_at,operation_id,fact_id,facts_added,facts_removed,observations_created,observations_updated,observations_removed` | `internal/history/event.go:104-124` |
| sample 形状 | `document_history.py:60-68` → `{count,ids}`，**恒为对象**，`ids` 恒为数组、上限 `MAX_IDS=100` | `internal/history/event.go:59-85`（`Of`：排序去重、cap 100、`ids` 恒非 nil） |
| `kind` 枚举 | `document_history.py:45-57` 十个值 | `internal/history/event.go:34-46` 同十值 |
| 分页语义 | `total/limit/offset` 计**操作数**；`items` 是操作内**所有事件**，再按 `created_at` 倒序（`document_history.py:288-310`） | `internal/history/response.go:22-27` 注释与实现同 |
| 文档不存在 | `memory_engine.py:14034-14040` 不做存在性检查 → **200 空页**；只有 tag scope 挡住才 404 | `document_history.go:60-72` 同（`ErrDocumentNotFound` 不报错，只有 `!documentVisible` 才 404） |

**实测（两侧同名 bank + 同名文档，各自跑同一串 retain/patch）**：
- 信封键集、事件 10 键、sample 2 键：**两侧同集**。
- 事件种类两侧都出现 `retain`（`facts_added=2`）、`consolidation`（`observations_created.count=2`）、`retag`。
- **分页语义等价**（同一份 3 事件数据上）：`?limit=2&offset=1` → 两侧 `total=3`、`items=[consolidation, retain]`；
  `?limit=1000` → 两侧 `total=3` 且 `items` 长度都是 **4**（total 计操作、items 计事件）。
- **错误矩阵全部 SAME**：`?limit=0`/`?limit=1001`/`?limit=abc`/`?offset=-1`（422，detail 逐字节同）、
  `documents/nope/history`（200 空页）、`documents/nope/history?limit=0`（422，参数压过 not-found）、
  未知 bank（200 空页，与参考"不做存在性检查"一致）、`.../history/`（404 enum/not-found 同）、`documents/history`（404 同）。

**唯一 DIFF**：`GET .../documents//history` —— 参考给 `200 {"document_id":"","total":0,"limit":100,"offset":0,"items":[]}`（`:path` 至少 1 字符，`//` 之间是空串），本服务 404（Go ServeMux 路径清洗/canonical redirect）。同族的 `GET .../documents//folder/doc.md` 参考 404 `Document not found`、本服务 404 `Not Found`（仅措辞）。**属于"空/多余斜杠"这一类畸形路径**，普通客户端不会发出；是否要为此改 `internal/httpx/` 路由层需要组长裁决（改动会波及 transfer 的 `download/{key}`），本单元**未改**。

> **组长裁决（2026-11-03）**：`documents//history` / `documents//…` 这类**空路径段畸形输入**
> 判 `⚠️存疑（已知差异，不修）`——修它要动 `internal/httpx/` 路由层并波及 transfer 的
> `download/{key}`，风险大于收益，且客户端不会产生 `//` 输入。同理 `%00` 引发的 500 正文
> （本服务多出 `ERROR:` 前缀与 SQLSTATE）归组 J 的"驱动错误文本直出"面，不在本单元处理。

> **重要方法论更正（2026-11-03，本单元与组长共同确认）**
> 参考侧每一次请求都**可能改变参考自身的状态**，从而污染后续探测。本单元最初把
> `POST .../consolidate` 的一组请求背靠背连发，结果 9 个不同 body（含 `[1]`、`"x"`）
> **全部返回 `200` 且 `deduplicated:true`** —— 这不是契约，而是**第一次请求就排了一个
> full-bank pending 操作**，后续每个请求都去重回同一个 `operation_id`（`deduplicated:true`）。
> 同理，`POST .../consolidation/recover` 也会**清掉 `consolidation_failed_at`**，
> `DELETE .../observations` 会**删观测并重置 consolidated_at**。
> **规则：每条探测前必须保证「参考侧对该 bank 无 pending 操作、无失败标记」，
> 或者用全新的 bank；否则得到的不是契约证据。** 本单元后续所有探测均遵守此规则。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|

## 第五轮 bg-maintenance

参考落点：`engine/maintenance.py`（`MaintenanceLoop`，684 行），逐行精读；配置默认值 `config.py:2050-2252`；
例程定义 `migrations/0005_reference_functions.sql`（与参考 `alembic/versions/{e5f6a7b8c9d0,c8b4e2a71f95,
d7b2f8a1c934,f4d1c2b3a5e6,d4f7a2c9e1b5,b2429c65cc20,b6d2f8a4c1e7}.py` 的产物同源，本服务是它们的产物，故结构一致）。
本服务落点：`internal/maintenance/loop.go`（通用循环）、`internal/app/maintenance_jobs.go`（作业装配）、
`internal/app/maintenance.go`、`internal/app/cron.go`、`internal/storage/{maintenance.go,
postgres_maintenance.go, memory_maintenance.go}`。

### 结论表

| 作业 | 参考落点 | 本服务落点 | 结论 | 证据 | 修复 |
|---|---|---|---|---|---|
| 循环骨架（tick / jitter / due / run-timed） | `maintenance.py:183-256` | `internal/maintenance/loop.go:100-167` | ✅一致 | 见 §A | — |
| “全部禁用就不启动” | `maintenance.py:148,167-179` | `loop.go:86-104` + `config/maintenance.go:123-133` | ✅一致 | 见 §B | — |
| `retention.audit_log` / `retention.llm_requests` | `maintenance.py:258-348` | `maintenance_jobs.go` `purgeExpired` | ❌已修（3 处） | 见 §C | 改走 `schemas_with_expired_rows()`；失败改 `continue`；cutoff/批大小/批上限/SKIP LOCKED 复核一致 |
| `consolidation.reconcile` | `maintenance.py:427-534` | `maintenance_jobs.go` `reconcileConsolidation` | ❌已修（3 处） | 见 §D | 改走 `banks_needing_consolidation()`；补 tenant 过滤；补 `enable_observations && enable_auto_consolidation` 门 |
| `mental_model.scheduled_refresh` | `maintenance.py:538-684` | `maintenance_jobs.go` `refreshDueMentalModels` + `cron.go` | ❌已修（3 处）+ ⚠️1 处存疑 | 见 §E、§F | 改走 `mental_models_with_cron()`；补 tenant 过滤；去掉 paused 模型的 `StampFullRefresh` 写库；cron 见 §F |
| `operations.cleanup` | `maintenance.py:352-423` | `maintenance_jobs.go` `pruneTerminalOperations` | ❌已修（2 处） | 见 §G | 改走 `schemas_with_expired_operations()`；补 tenant 过滤；“先删归档再删行”从存储内部提升为接口上的显式顺序 |
| `vector_index.reconcile`（本服务多出） | 参考**无此循环**：`engine/memory_engine.py:7296,23826-23870` + `engine/vector_index_health.py` 由**写路径触发异步 operation** | `maintenance_jobs.go` `reconcileVectorIndexes`，开关 `vector_index_min_rows>0` | ⚠️存疑（保留，有意增量） | 见 §H | 不改 |
| `graph.maintenance`（本服务多出） | 参考**是同名 operation 类型**，不是维护循环作业：`engine/memory_engine.py:7296,11455,11797` enqueue，worker 认领 `engine/graph_maintenance.py:1-50,220-300` | `maintenance_jobs.go` `drainGraphMaintenance`，跟随 `consolidation_reconcile_interval_seconds` | ⚠️存疑（实现路径不同，语义等价），保留 | 见 §I | 不改（组长已裁决） |
| `personal.rollup` / `project.merge` | 参考无 | 本服务增量 | ✅一致（有意增量，不影响参考面） | 见 §J | — |

### §A 循环骨架

| 维度 | 参考 | 本服务 | 结论 |
|---|---|---|---|
| tick 常量 | `_TICK_SECONDS = 60`（`maintenance.py:79`） | `DefaultTick = 60s`（`loop.go:30`） | 一致 |
| 首次必跑 | `_is_due` 在 `_last_run` 无记录时返回 True 并在**跑之前**记时间（`:216-223`） | `due()` 同序：`lastRun[name] = now` 先于 `Run`（`loop.go:157-167`） | 一致 |
| 单调时钟 | `time.monotonic()` | `time.Now()` 的单调读数（`time.Time` 携带 monotonic） | 一致 |
| 单 job 失败不传染 | `_tick` 外层 `_run` try/except（`:186-190`） | `runTimed` 记日志，`RunDueJobs` 继续（`loop.go:122-151`） | 一致 |
| 计时行 | `Maintenance: <name> took Xs`（`:244-254`） | `maintenance job finished/failed` + `took`（`loop.go:138-151`） | 一致（字段名不同，属日志自述面） |
| 启动抖动 | `uniform(0, jitter)`，jitter<=0 不延时（`:196-214`） | `rand().Float64()*StartJitter`（`loop.go:176-183`） | 一致 |
| 停止语义 | 跑完当前 tick 再退（`:157-165`） | ctx 取消打断 sleep，当前 job 结束后返回（`loop.go:109-117`） | 一致 |

### §B “全部禁用就不启动”

`maintenance.py:167-179` 五个分支 → 本服务 `config/maintenance.go:123-133` 的 `AnyJobEnabled()`：
`auditOn = sweepOn && audit_log_retention_days>0`、`llmOn = sweepOn && llm_trace_enabled && llm_trace_retention_days>0`、
`reconcileOn = consolidation_reconcile_interval_seconds>0`、`refreshOn = mental_model_refresh_tick_seconds>0`、
`cleanupOn = operation_cleanup_interval_seconds>0 && operation_retention_days>0`，逐项相同。
本服务另加 `mergeOn/rollupOn`（增量面），不改变参考面的判定。
`loop.Run` 在无启用作业时直接返回（`loop.go:100-104`），等价于参考 `start()` 不建 task。
默认值核对（`config.py:2050-2252` ↔ `config/maintenance.go:11-22`）：`operation_retention_days=0`、
`operation_cleanup_batch_size=1000`、`audit_log_retention_days=-1`、`llm_trace_retention_days=1`、
`consolidation_reconcile=300`、`mental_model_refresh_tick=300`、`retention_sweep=3600`、
`operation_cleanup_interval=900`、`maintenance_start_jitter=60` —— **全部一致**。

### §C retention 三处缺陷（已修）

**(C1) 没有用发现例程，对每个租户无条件删除。**
参考先一次跨 schema round-trip 调用 `schemas_with_expired_rows(table, ts_col, days)`，再只对返回的 schema 删除
（`maintenance.py:276-295`）。修复前本服务按 `ListTenants()` 对**每个** schema 跑一次批量删除，schema 多时是参考
注释里明确要避免的 “per-schema query storm”。

**(C2) 一个 schema 失败会静默跳过其后所有 schema。**
参考 `except ... continue`（`:296-298`）。修复前本服务 `return fmt.Errorf(...)` 直接结束整个 sweep —— 一个租户的
表处于迁移中，它之后的所有租户那一轮都不会被清理。

**(C3) 发现失败被当成 job 失败。**
参考 `except ... logger.warning(...); return`（`:282-284`）。修复后本服务同样 warn + return nil。

**复核为一致的其余细节**：批大小 `_RETENTION_BATCH_SIZE=2000` ↔ `retentionBatch=2000`；批上限
`_RETENTION_MAX_BATCHES=1000` ↔ `retentionMaxBatches=1000`；批间停顿 `0.25s` ↔ `retentionBatchPause=250ms`；
**单次 cutoff**（`:286-288` 注释明确说 per-chunk NOW() 会让 “deleted < batch 即完成” 不成立）↔ `cutoff` 在循环外
计算一次；`FOR UPDATE SKIP LOCKED` + `ORDER BY <ts>` + `LIMIT` 的 CTE ↔ `purgeChunk` 逐字同形；
短批即止 ↔ `removed < batchSize` 即 break；批上限命中时参考打 `hit its per-run batch ceiling` 警告（`:343-347`）。

### §D consolidation.reconcile 三处缺陷（已修）

**(D1) 发现用的是自写 SQL，不是参考例程。**
参考 `SELECT schema_name, bank_id FROM banks_needing_consolidation()`（`maintenance.py:432-434`），例程
`migrations/0005_reference_functions.sql:14-91` 的谓词是：`bank.config->'enable_auto_consolidation' <> 'false'`
且无 pending/processing consolidation、且 `EXISTS(memory_units … consolidated_at IS NULL AND fact_type IN
('experience','world'))`，且**「最新 100 条未合并行里存在 consolidation_failed_at IS NULL」或「未合并数 > 失败数」**。
修复前本服务是 `consolidated_at IS NULL AND consolidation_failed_at IS NULL` —— **谓词不同**：参考的“混合状态”
分支（有成功可重试的事实混在失败里）本服务不选。修复后直接调用例程。

**(D2) tenant 过滤缺失。**
参考只向 worker 真正轮询的 schema 入队，否则该 op 永不被认领并会阻塞该 bank 之后的所有 reconcile
（`maintenance.py:445-478`，`skipped_unknown` 计数）。本服务修复前对发现到的任何 schema 直接入队。

**(D3) `enable_observations && enable_auto_consolidation` 门缺失。**
参考在入队前 `resolve_full_config` 再判一次 `resolved.enable_observations and
resolved.enable_auto_consolidation`（`maintenance.py:483-486`，注释说“镜像 retain 时的自动合并门”）。本服务完全没有。
**未修（⚠️存疑，需组长裁决，见“待裁决”）**：例程的谓词已经过滤 `config->'enable_auto_consolidation' <> 'false'`，
剩下的只有 `enable_observations` 一维，而它需要按 bank 解析配置（deployment 默认 + bank override），
属于跨模块改动；本轮不扩大到 bank 配置解析。

### §E mental_model.scheduled_refresh 三处缺陷（已修）

**(E1) 没有用 `mental_models_with_cron()`。** 参考 `:560-565`；本服务改用例程（跨 schema 一次），
同时顺带获得“已排除 pending/processing refresh”的同一 SQL 谓词。

**(E2) tenant 过滤缺失。** 参考 `:600-609` + `:624-626`。修后同样跳过未识别 schema 并计数。

**(E3) paused 模型被额外写库。** 参考对 paused 只 `skipped_paused += 1`（`:666-667`），**不写任何行**；
修复前本服务对 full tick 的 paused 模型额外 `storage.StampFullRefresh(...)`，会改 `last_full_refreshed_at`，
属于参考面外的写库。已删除该写。

**顺带复核一致**：`automatic=True` 载荷键（`:658-664` ↔ `AutomaticRefreshPayloadKey: true`）；
`skip_if_in_flight` 幂等 enqueue（`:652-657` 说明 discovery 的排除只是读，真正的幂等在 insert）↔
`DedupeByPayloadKey + DedupeIncludesProcessing`；`full` 优先于 delta（`:595-596`）；
两条时钟 `refresh_cron`↔`last_refreshed_at`、`full_refresh_cron`↔`last_full_refreshed_at`，
full 的二次门“自上次 full 之后跑过一次 refresh”（`:592-594` ↔ `cron.go:104-109`）；
分类统计 `scheduled/up-to-date/already in flight/paused/unrecognized`（`:677-684`）↔ `refreshTally`。

### §F cron 到期判定：⚠️存疑（边角差异，不扩大重构）

本服务用 robfig/cron v3 复刻 `croniter(...).get_prev(datetime)`。离线对照（`croniter 6.2.4`，
临时下载到 `.audit-bgmt/cronchk`，未加入依赖）在 `now=2026-11-03T20:00Z` 上逐条比对：

| 表达式 | croniter `get_prev` | 结论 |
|---|---|---|
| `*/5 * * * *` / `0 * * * *` / `30 2 * * *` / `*/7 * * * *` | 19:55 / 19:00 / 02:30 / 19:56 | 一致（robfig `Next` 迭代取前一次，`cron.go:34-45`） |
| `0 0 * * 0` / `@weekly` | 2026-11-01 00:00 | 一致 |
| `@daily` / `@hourly` / `@monthly` | 当日 0 点 / 当日 19 点 / 11-01 | 一致（两侧都允许 `@` 描述符：croniter 默认 `default_expression` + robfig `cron.Descriptor`） |
| `5/15 * * * *` | 19:50 | 一致（`N/step` = `N-max/step`，robfig `parser.go` 同规则） |
| `0 0 L * *` / `0 0 ? * MON` | 10-31 / 11-02 | 一致（robfig 的 `?` 等同 `*`） |
| `0 0 30 2 *`（永不触发） | 抛 `CroniterBadDateError` | **⚠️差异**：参考只 catch `(ValueError, KeyError)`（`maintenance.py:581`），该异常会冒泡到 `_run` 的 `except Exception` 被吞掉、**整轮 job 作废**；本服务返回“未到期”，其余模型照常处理 |
| 时区 | `croniter(cron, now)` 的 `now` 是 `datetime.now(timezone.utc)`（`:574`），cron 按 UTC 展开 | robfig `Parser` 未写 `TZ=` 时 `Location=time.Local`，但 `SpecSchedule.Next` 对 `time.Local` 用**参数自身的 Location**（`spec.go`），而传的是 `time.Now().UTC()` → UTC 展开 | 一致 |
| DST | UTC 下无 DST | 同 | 一致 |

**裁决**：`maintenance.py:581` 的 catch 列表不含 `CroniterBadDateError`，所以“永不触发的 cron”在参考侧会让整轮
scheduled-refresh 抛错（本服务只让该模型不触发）——这是**参考自身的 bug**，复刻它会把“一个坏 cron 拖垮所有模型”
带进来。组长裁决：不改，记 ⚠️存疑。

### §G operations.cleanup 两处缺陷（已修）+ 一处自我纠正

**(G1) 没有用 `schemas_with_expired_operations()`。** 参考 `:367-375`，并且**只 prune 部署真正服务的 schema**
（`:379-387`，例程会报所有持有 `async_operations` 表的 schema，包括 tenant discovery 不认的）。

**(G2) “先删归档再删行”的**顺序**原先被藏在存储内部。** 参考是 job 层显式两步：
`purge_expired_export_archives(conn, table, cutoff, batch_size=cfg.operation_cleanup_batch_size)` 之后才
`prune_terminal_operations(...)`（`:402-414`）。**自我纠正**：本报告早期版本判该顺序“已等价”，那是**错的** ——
修复前 `PostgresStore.PruneTerminalOperations` 内部顺手调了 `purgeExpiredExportArchives`，顺序碰巧对，
但接口上看不出这是参考的一个显式步骤，见 §D/§E 同批修复后已提升为 `OperationPruner.PurgeExpiredExportArchives`。
批大小/跑期/cutoff/`ORDER BY updated_at, operation_id`/`LIMIT`（`:4340-4349`）逐项一致。

### §H `vector_index.reconcile`：⚠️存疑（保留，有意增量）

- 参考**没有**这个循环作业。它的向量索引覆盖由**写路径**触发异步 operation：
  `engine/memory_engine.py:7296` (`_submit_vector_index_maintenance_after_change`)、`23826-23870`
  (`submit_async_vector_index_maintenance`)，并在 `engine/vector_index_health.py` 里规划
  （`CoverageTrigger.GREW/SHRANK/FULL`）。**“写才有变化，没有周期扫描能发现写没发现的东西”**
  —— 参考源码原话（`23838-23842`）。
- 本服务没有把覆盖维护挂到写路径，而是用 `vector_index_min_rows>0` 打开的周期扫描
  （`maintenance_jobs.go`，默认 900s，`config/vectorindex.go`）。
- **可观测差异**：一个 bank 越过阈值后，索引生效延迟从“写后立刻入队、worker 尽快执行”变成“最多一个
  `vector_index_maintenance_min_interval_seconds`（默认 900s）”。这是**性能/延迟**面，不是 API 契约面：
  `/operations` 不出现该作业、响应体不变、没有 pgvector 索引时查询结果不变（全表精确检索）。
- 组长裁决：**保留**，作为本服务 pgvector 部分索引的补偿机制，实测上不可接受删除（否则部分索引永远补不齐）。
  若要收敛到参考做法，需要把 `ReconcileBankVectorIndexes` 挂到 retain/delete 写路径并入队一个 operation ——
  属于另一个单元的面，本轮不做。

### §I `graph.maintenance`：⚠️存疑（实现路径不同，语义等价），保留

- **参考中没有这个维护作业**。它是 operation 队列里的任务类型 `graph_maintenance`，enqueue 点是
  `engine/memory_engine.py:7296`（curation/删除后的统一入口）、`:11455`（批量删记忆后）、
  `:11797-11798`（文档删除后 `force_sweep=True`）；由 worker 认领执行 `run_graph_maintenance_job`
  （`engine/graph_maintenance.py:1-50` 的模块文档：「Two queue-driven passes run together on every worker
  invocation」「The worker dedupes on bank」）。
- 本服务的**触发条件与参考一致**：删除/curation 路径同样往 `graph_maintenance_queue` /
  `entity_maintenance_queue` 写行（`storage/postgres_memories.go:655`、`rename`/transfer 段），并有
  `EnqueueGraphMaintenance`（`:948-954`，按 bank 去重含 processing）。
- 偏离的只是**执行位置**：本服务在维护循环里定时 drain（跟随 `consolidation_reconcile_interval_seconds`）。
- **可观测差异（3 条）**：①图维护不产生 operation 记录 ⇒ 不出现在 `GET /operations`、不可 cancel、
  不参与 worker 并发/slot 控制；②参考的 `_JOB_TIME_BUDGET_SECONDS=240` 墙钟预算与其后“成功交给后继”的
  hand-off（`engine/graph_maintenance.py:220-300`）本服务没有；③参考 drain 是“把队列清空”，本服务每轮每段
  只取 500 条（`drainGraphMaintenanceInSchema`），积压靠后续轮次收敛。
- 组长裁决：**保留，不改**（加 operation 记录会改变 `/operations` 响应内容，是更大的偏离）。

### §J `personal.rollup` / `project.merge`

AGENTS.md 已声明为有意增量（`<tenant>::myself` 聚合 bank、team 项目合并）。二者只读自己的 store
（`storage.PersonalStore` / `storage.MergeStore`），不写参考面的表、不改参考面的作业编排，
作业注册顺序上也排在四个参考作业之后，因此**不改变参考面的任何可观测行为**。✅一致。

### 修复落点（文件 : 行）

- `internal/storage/maintenance.go`：`ConsolidationReconciler` / `ScheduledMentalModelFinder` 改为跨 schema 签名；
  新增 `ReconcileTarget`、`ScheduledMentalModel.Schema`、`ExpiredRowFinder`、`SchemasWithExpiredOperations`、
  `OperationPruner.PurgeExpiredExportArchives`。
- `internal/storage/postgres_maintenance.go`：`BanksNeedingConsolidation` / `MentalModelsWithSchedule` /
  `SchemasWithExpiredRows` / `SchemasWithExpiredOperations` 走参考例程；`PurgeExpiredExportArchives` 由内部函数
  提升为方法。
- `internal/storage/postgres_maintenance_schema.go`（新增）：`classifyDiscoveryError`（42883/42P01/3F000 → nil）。
- `internal/storage/memory_maintenance.go`：内存后端实现同签名能力（`SchemasWithExpiredRows` /
  `PurgeExpiredExportArchives` / 跨 schema 返回类型）。
- `internal/app/maintenance_jobs.go`：四个作业按参考顺序与语义重写（§C/§D/§E/§G）。
- 测试：`internal/app/maintenance_reference_test.go`（新增）——“一个 schema 失败不阻断其后 schema”“发现失败不失败
  job”“reconcile 跳过未服务 schema”“cleanup 只清已服务 schema + 先删归档”“作业顺序 = 参考 tick 顺序”。
  改回去会 FAIL 的用例：`TestRetentionSweepContinuesPastAFailingSchema`（对应 C2）、
  `TestReconcileSkipsUnknownSchemas`（对应 D2）、`TestMaintenanceJobOrderMatchesReference`（对应作业编排）。
- `internal/storage/maintenance_prune_test.go`：按新的显式顺序先调 `PurgeExpiredExportArchives`。
- **共享文件**：`internal/storage/postgres.go` —— **净改动为零**（`MaintenanceDiscovery` 回滚后已用
  `git diff` 复核）；`internal/app/app.go` 未改；`internal/storage/memory_maintenance.go` 只加同签名能力。

### 验证

```sh
source .goenv.local
go build ./internal/storage/ && go build ./internal/maintenance/         # OK
CGO_ENABLED=0 go build -tags pgx ./internal/storage/                     # OK
go vet ./internal/storage/ ./internal/maintenance/                       # 干净
gofmt -l internal/storage internal/app internal/maintenance              # 空
go test -count=1 ./internal/maintenance/... ./internal/storage/          # storage 794 通过；唯一 FAIL 见下
go test -count=1 ./internal/app/ -run 'TestRetentionSweep|TestPausedFullTick|TestReconcile|TestOperationCleanup|TestMaintenanceJobOrder'
                                                                         # ok（全量 ./internal/app 受
                                                                         # internal/worker 在途改动阻塞）
```

- `TestNoMemoryDomainTableReferencesOutsideStorage` FAIL 与本次改动无关：它在
  `.bgw-base/internal/storage/direct_reference.go`（工作区里别人留下的临时副本目录，不是本仓库文件）里发现
  `documents`。
- `./internal/app` 全量测试与 `go build ./...` 在收口时被 `internal/worker/consumer.go` 的在途改动
  （`ceilingState` / `ceilingProgressKey` / `WallTimeoutExceeded` 未定义）阻塞，属 bg-worker 单元；
  本单元的测试在该包内独立运行时为绿。
- 全量 `go test ./internal/app` 的两个 FAIL（`TestRetentionSweepContinuesPastAFailingSchema` 与
  `TestPausedFullTickStopsBeingDue`）已按新行为修正/重写为
  `TestPausedFullTickIsCountedNotStamped`——后者正是 §E(E3) 的“改回去就 FAIL”用例：它断言 paused 的
  full tick **不**移动 `last_full_refreshed_at`。

### 待裁决事项（收口时仍开放）

1. **`consolidation.reconcile` 的 `enable_observations` 门（§D D3）未修**。参考在入队前按 bank 解出
   `resolved.enable_observations and resolved.enable_auto_consolidation`（`maintenance.py:483-486`）；例程已覆盖
   `enable_auto_consolidation`，剩下 `enable_observations` 需要按“部署默认 + bank override”解析配置，
   跨 `storage` 与 bank 配置模块。本轮不扩大改动范围，需组长决定是否另开条目。
2. **cron 永不触发时参考整轮作废（§F）**：`CroniterBadDateError` 不在参考的 catch 列表里，一个坏 cron 会让
   那一轮 scheduled-refresh 整体抛错被 `_run` 吞掉。本服务只让该模型不触发。已裁决不改，本报告留档。
3. **`vector_index.reconcile` / `graph.maintenance`** 均已裁决为 `⚠️存疑` 保留，见 §H/§I。

#### 机制 4 的修复（2026-11-03，组长裁决「(a)(b)(c) 全部按参考修」）

**改了什么**

| 文件 | 改动 |
|---|---|
| `internal/worker/wall_ceiling.go`（**新增**） | `WallCeiling{EnvVar, ExtendsOnProgress}`；`wallCeilings` 表（`retain` / `consolidation`(idle) / `refresh_mental_model`）；`WallTimeoutExceeded` + `Describe()`（**逐字复刻** `_WallTimeoutExceeded.describe`，绝对/空闲两套文案）；`progressState` + `ReportProgress(ctx, stage)`（= 参考的 `stage.set_stage()`）；`activeCeiling`（`deadline` 可被 `reschedule()` 推后，watcher 用真实 `time.Timer`，`extended` 通道唤醒后 `timer.Reset(a.remaining())`，**空闲上限精确到纳秒而不是轮询粒度**） |
| `internal/worker/consumer.go` | `handle` 里 `budget = min(lease, ceiling)` 删除，改成：有 ceiling → `activeCeiling` 单独生效；**无 ceiling 的未映射类型**仍用 lease 兜底；`context.Cause(workCtx)` 取回 `*WallTimeoutExceeded` 并**覆盖** processor 的返回错误；新增终态分支 `errors.As(err, &exceeded)` → `fail`（**不花 retry**）|
| `internal/config/worker.go:83-93` | 环境变量写 `0` 现在**真正表示关闭上限**（对齐 `_wall_timeout_for` 的 `timeout > 0 else None`）；只有非整数才回落默认值 |

**验证（全部在隔离副本 `.bgw-base/` 上跑，55→**58** passed）**

| 用例 | 断言 | 证伪（改回去）|
|---|---|---|
| `TestWallCeilingTableMatchesTheReference` | 三种类型各自的 env 名与 `ExtendsOnProgress` 逐项等于参考；4 个未映射类型**必须无上限** | 把 `consolidation` 的 `ExtendsOnProgress` 去掉 → FAIL |
| `TestWallTimeoutDescriptionMatchesTheReference` | 绝对文案 `Task exceeded the 3600s wall-clock limit for 'retain' (stage=…) and was cancelled. Raise WENYUAN_API_RETAIN_WALL_TIMEOUT …`、空闲文案 `Task made no progress for 7200s, …(each committed batch restarts the clock, so this is a stall, not a slow job)…` **逐字节相等**；无 stage 时说 `unknown` | 改任意一个词 → FAIL |
| `TestWallCeilingFailsTheTaskWithTheReferenceExplanation` | 20ms 上限的 retain → `failed`、`retry_count==0`、`error_message` 逐字节等于参考文案 | 把 fail 分支改回 retry → FAIL（status/error 都不对）|
| `TestWallCeilingIsNotCappedByTheLease` | 20ms lease + 10s 上限，processor 睡 80ms → `completed` 且 `error==""` | 恢复 `min(lease, ceiling)` → **FAIL**：`status = failed error = "Task exceeded the 0s wall-clock limit for 'retain' (stage=unknown) …"` |
| `TestUnmappedOperationTypeIsBoundedByTheLease` | 未映射类型（`transfer_import`）无上限，仍被 lease 打断并**重试一次**（lease 不是上限） | 给未映射类型套 ceiling → FAIL |
| `TestIdleCeilingIsRestartedByProgress` | consolidation + 60ms 空闲上限，processor 每 10ms `ReportProgress` 跑 300ms → `completed`（**没被砍**） | 把 `reschedule()` 改成 no-op → **FAIL**：`Task made no progress for 0s … (stage=consolidation.batch)` |
| `TestAbsoluteCeilingIgnoresProgress` | 同样的报告节奏 + 60ms **绝对**上限（retain）→ `failed`，`error` 含 `wall-clock limit for 'retain'` | 让绝对上限也 extend → FAIL |

**可观测后果的量化（应答「差异会导致什么」）**

| 场景 | 修复前 | 修复后（=参考）|
|---|---|---|
| 8 分钟的大文档 retain | 300s（lease）被砍 → `context deadline exceeded` → 重试 3 次 → `failed`（**永久丢**） | 跑满 3600s 上限 |
| 2 小时的大 bank consolidation | 300s 被砍 → 重试 → `failed`；**且参考的「batch 提交即 restart 时钟」根本不存在** | 7200s **空闲**上限；只要每批都在推进就永不失败 |
| 卡死的 retain | 300s 后 `retry budget exhausted after 3 attempt(s): context deadline exceeded`（**文案对不上**，且状态是 pending 而非 failed）| 3600s 后 `Task exceeded the 3600s wall-clock limit … Raise WENYUAN_API_RETAIN_WALL_TIMEOUT …`，`failed`，不花 retry |
| 把 `WENYUAN_API_RETAIN_WALL_TIMEOUT=0` | 无效果（`0` 被当未设置，仍用 3600s）| 真正关闭上限 |

### 机制 5：失败重试（次数、退避曲线、错误分类、`_summarise_child_error_messages` 文案）

| 维度 | 参考实现 | 本服务 | 结论 |
|---|---|---|---|
| 重试次数 | `worker_max_retries`，默认 `DEFAULT_WORKER_MAX_RETRIES = 3`（`config.py:2042`），构造时 `max(0, ...)`（`poller.py:432`）。判定点两处：executor 内 `retry_count < worker_max_retries` → `RetryTaskAt`（`memory_engine.py:5200`），否则 re-raise 让 poller 判 `failed` | `DefaultMaxRetries = 3`（`consumer.go:29`），`resolveWorker` 里 `<0` 归零（`config/worker.go:180-184`）。判定点 `retry`：`attempt := op.RetryCount + 1; if attempt > MaxRetries → fail`（`consumer.go:906-911`）| ✅一致（3 次）|
| 通用退避曲线 | **常数**，不是指数：`worker_task_retry_backoff_seconds`，默认 `DEFAULT_WORKER_TASK_RETRY_BACKOFF_SECONDS = 60`（`config.py:2043`），每次重试都是 `now() + 60s`（`memory_engine.py:5199-5203`）。注释明说「Defaults of 3 x 60s give a 4-minute total window」| `BackoffFor(attempt)` = `InitialBackoff * 2^(attempt-1)`，clamp 到 `MaxBackoff`；**默认 initial=60s、max=60s**（`config/worker.go:13-14`），所以**默认也是常数 60s**（`consumer.go:404-419` `BackoffFor`）| ✅一致（默认值下曲线相同）|
| 退避是否可被配置成指数 | 参考只有 `WORKER_TASK_RETRY_BACKOFF_SECONDS` 一个键（常数）；无 max-backoff 键 | 本服务多一个 `WENYUAN_API_WORKER_MAX_BACKOFF_SECONDS`（默认 60），把它调大才会出现指数 | ⚠️**有意增量**（不改变默认行为；参考的运维面没有这个旋钮，本服务有）|
| consolidation 专用退避 | `_consolidation_retry_backoff_seconds(retry_count) = min(5 * 2^retry_count, 1800)`，注释给出 `5,10,20,40,80,160,320,640,1280,1800,1800,…`（`memory_engine.py:529-535`），走 `RetryTaskAt`（**不花 poller 的 retry 预算**，因为 p再 raise 前 poller 不计数——实际是 `RetryTaskAt` 会 `retry_count+1`）| `consolidationRetryBackoff(retryCount)`（`internal/app/worker.go:130-140`）：`backoff = 5s; for range retryCount { backoff *= 2; if backoff >= 1800s return 1800s }` → **5,10,20,…,1280,1800** ✅ 逐项相同；`worker.RetryAt` 走 `scheduleRetry`（`retry_count+1`，`consumer.go:912-920`）| ✅一致 |
| consolidation「有兄弟 pending 就判死」 | `memory_engine.py:5165-5205`：若同 bank 已有 pending consolidation 则**跳过重试**（注释：防止 retry storm）| `hasOtherPendingConsolidation` + `worker.Permanent(cause)`（`internal/app/worker.go:151-180`）| ✅一致 |
| 错误分类 | **两层**：executor 内自己分类（provider 401/403 之类直接失败、consolidation 走专用 ladder、其余 `RetryTaskAt`），poller 再兜底：`DeferOperation` → 不花预算；`RetryTaskAt` → 花一次；`_WallTimeoutExceeded` → `failed`；`is_store_backpressure` → defer；`is_append_conflict` → defer；**其余一切异常** → `format_task_error(e)` → `failed`（`poller.py:1346-1352`）| 同类五分支（`consumer.go:704-790`）：`WallTimeoutExceeded`→fail；`Permanent`→fail；`RetryAt`→retry;`isBackpressure\|isDeferral\|isAppendConflict`→defer；其余→`retry`（**不是立即 fail**）| ⚠️**差异（见下）** |
| `format_task_error` | 总是加类名前缀：`f"{type(e).__name__}: {message}"`，无 message 时只给类名（`worker/exceptions.py:4-16`，是 issue #3218 的修复：`Task execution failed: graph_maintenance, error: ``` 什么都说明不了）| 无对应：`fail` 直接写 `err.Error()`（`outcome.go`），Go 的错误没有「类名」习惯 | ⚠️跨语言不可逐字节复刻（Go 侧用 `%w` 链和 sentinel 表达同类信息）；**已定「不复刻」** |
| `error_message` 长度上限 | `[:5000]`（poller.py:1108、:1095 两处）| `truncateErrorMessage`，`maxErrorMessageChars = 5000`（`outcome.go`，注释已引参考行号）| ✅一致 |
| `_summarise_child_error_messages` 文案 | 取**失败子批里非空 `error_message` 的众数**（`Counter(...).most_common(1)`）；全为空时回退 `"One or more sub-batches failed"`（`poller.py:256-283`）| `summarizeChildErrors`（`internal/storage/async_retain.go:419-440`），**已在机制 6 核实一致** | ✅一致 |

**机制 5 的两处待定项**

**(i) 通用错误的默认走法：参考 `failed` vs 本服务 `retry`。**
参考 poller 的兜底分支是 `_mark_all_failed`（`poller.py:1346-1352`），立即终态；参考的「重试」只由 **executor 主动 `raise RetryTaskAt`** 触发（即「我知道这个能重试」）。
本服务的默认分支是 `c.retry(...)`（重试 3 次后 fail），只有 `worker.Permanent(err)` 才立即终态。
**后果**：同一个不可重试的 bug（如 payload 结构错误但没被标 Permanent），参考**一次**就 `failed`，本服务**跑 4 次**才 `failed`；
副产物是本服务在 4 次里反复调 LLM（若错误发生在模型调用之后），而参考只花 1 次。
→ ⚠️**需裁决**。倾向**不改**：Go 侧把「可重试」当默认更符合本项目既有设计（`Permanent` 是显式退出），
且 reverse（默认 fail、显式 retry）会破坏 `consolidationRetry`、webhook ladder 等已有显式路径的语义一致性；
而且「重试 4 次」本身不改变最终可观测状态（都是 `failed`），只多消耗预算。
**注意**：这条不适配「卡死」类错误——那类已由机制 4 的 wall ceiling 拦掉了。

**(ii) 父批 `error_message` 的众数汇总 → 已核实一致，见机制 6。**
`storage/async_retain.go:419-440` 的 `summarizeChildErrors` 取非空失败子批 message 的众数，并列时取先出现者
（正好等于 Python `Counter.most_common(1)` 的并列语义），无 message 时回退 `"One or more sub-batches failed"` —— 与 `_summarise_child_error_messages` 逐项相同。

## 第五轮 retain

> 单元 `retain`（#21–#25）。端口 18212（`WENYUAN_API_DATABASE_SCHEMA=audit_retain`，single-tenant 副本），
> 参考 `http://127.0.0.1:8888`。差分工具 `chk.py`：**保留键序**（`object_pairs_hook=OrderedDict`），
> 按 checklist §2「响应体键集与键序」逐字节比；掩码只覆盖时间戳/UUID/`*_tokens`/活测量字段。
> 夹具：bank `audit-retain-18212`；本单元**未触碰任何既有 bank**。

### #21 `POST /v1/default/banks/{bank_id}/memories/dry-run-extract` —— ❌已修（4 处）

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #21（`missing` 的 `input` 是整层 body） | ✅一致 | `api/http.py:6103`（`DryRunExtractRequest`，`content` 必填），框架报 `missing` 时 `input` 为该层 body | `internal/reporting/dryrun_content.go`（`dryRunContentFailure` 首段） | 无（第二轮已修，本轮复验） | `{"items":[{"content":"x"}]}` → `SAME`（`input` 逐字节同）；`{}` → `SAME` |
| #21（**`union_tag_*` 的 `ctx` 键序**） | ❌已修 | `api/http.py:6103` → `engine/retain/attachment_content.py` 的 `str \| list[tagged-union[...]]`；pydantic 逐分支报告，`ctx` 按**声明序** `discriminator` → `tag` → `expected_tags`。实测参考：`{"ctx":{"discriminator":"'type'","tag":"bogus","expected_tags":"'text', 'image', 'file'"}}` | `internal/reporting/dryrun_content.go`：**166–186**（`dryRunBlockErrors`）、**188–203**（新增 `dryRunUnionTagNotFoundCtx`/`dryRunUnionTagInvalidCtx`） | 原实现用 `map[string]any{"discriminator":…, "tag":…, "expected_tags":…}`，Go 的 map 序列化**按键字母序**输出，得到 `discriminator, expected_tags, tag`——键集相同、**键序不同**（checklist §2 判据 2 明确把键序列为契约）。改为两个定序 struct，成员按参考声明序输出 | `{"content":[{"type":"bogus"}]}` → 修前 `DIFF`（仅键序）、修后 `SAME` |
| #21（**`type` 非字符串时的判别语义**） | ❌已修 | 参考把判别值取出后与接受集**比较**，只有该成员**完全不存在**才是 `union_tag_not_found`；存在但非字符串（`5`/`true`/`null`/`{}`/`[]`）一律 `union_tag_invalid`，且把值**字符串化**进 `msg` 与 `ctx.tag`（Python `str()` 形：`5`→`5`、`true`→`True`、`null`→`None`） | `internal/reporting/dryrun_content.go`：**198–217**（`rawTag, hasTag := block["type"]` 只判「存在」；非字符串走 `dryRunTagText`）、**219–238**（新增 `dryRunTagText`，复刻 Python `str()` 渲染） | 原实现用 `block["type"].(string)` 单次类型断言：断言失败（值存在但非字符串）被当成"没有 tag"，于是 `{"type":5}` 报 `union_tag_not_found`，而参考报 `union_tag_invalid` + `msg` 含 `'5'` | `{"type":5}` / `{"type":true}` / `{"type":null}` / `{"type":{}}` / `{"type":[]}` → 全部修前 `DIFF`、修后 `SAME`；对照 `{"text":"x"}`（完全无 `type`）→ 两侧同为 `union_tag_not_found` `SAME` |
| #21（**被拒块的 `input` 回显调用方书写序**） | ❌已修 | 参考的 `input` 是请求体解析出的**插入序 mapping**，块对象逐成员按调用方**书写序**回显：`{"type":"image","source":{…}}` 回 `{"type":"image","source":{…}}` 而不是字母序 `{"source":…,"type":"image"}`。`missing` 的 `input` 是**缺失成员所在的那一层对象**（如 `source` 对象本身），同样保持书写序 | `internal/reporting/dryrun_content.go`：**59–135**（新增 `dryRunOrderedValue`/`dryRunOrderedValueFrom`，用 `httpx.OrderedObject` 重新按书写序读一遍 content 值）、**137–155**（新增 `dryRunBlockFields`，从 OrderedObject 取成员供校验）、**160–186**（`dryRunBlockListFailure` 传入有序值）、**345–360**（`sourceEcho` 用有序 source 对象作 `missing` 的 `input`） | 原实现把 content 值直接 `json.Unmarshal` 成 `map[string]any`，回显时按键字母序输出，于是被拒块的 `input` 与参考**成员相同、次序不同**（例：参考 `{"type":"image","source":{…}}`、本服务 `{"source":{…},"type":"image"}`） | `{"content":[{"type":"image","source":{"type":"base64","media_type":"bad","data":"aGk="}}]}` → 修前 `DIFF`（`input` 键序）、修后 `SAME`；`{"content":[{"type":"text","text":"x"},{"type":"text"}]}`（`missing` 回显 `{"type":"text"}`）→ 修前 `DIFF`、修后 `SAME`；`{"content":[{"type":"image","source":{"type":"base64","data":"aGk="}}]}`（`missing` 回显 `{"type":"base64","data":"aGk="}`）→ 修前 `DIFF`、修后 `SAME` |

**#21 复验清单（15 例全 `SAME`，本轮修复后）**

```
{"content":[{"type":"image","source":{"type":"base64","data":"aGk="}}]}      SAME
{"content":[{"type":"image","source":{"type":"base64","media_type":"bad","data":"aGk="}}]}  SAME
{"content":[{"type":"bogus"}]}                            SAME
{"content":[{"type":"text","text":"x"},{"type":"text"}]}  SAME
{"content":[{"type":"","text":"x"}]}                      SAME
{"content":[{"type":5}]}                                  SAME
{"content":[{"type":"image"}]}                            SAME
{"content":[{"type":"text"}]}                             SAME
{"content":[{"type":"text","text":5}]}                    SAME
{"content":[{"type":"image","source":5}]}                 SAME
{"content":[{"type":"file","source":{"type":"base64","media_type":"text/plain","data":""}}]}  SAME
{"content":[]}  SAME    {"content":null}  SAME    {"content":5}  SAME    {}  SAME
```

另：请求级校验 23 例（`timestamp` 非法/`unset`、`retain_mission`/`retain_extraction_mode`/`retain_chunk_size`/
`entities_allow_free_form`/`llm_output_language`/`strategy`/`agent_name`/`context`/`entity_labels`/
`retain_extract_causal_links`/`retain_custom_instructions` 各类型错、`null`/`[]`/`5`/`"x"` 体）全 `SAME`。

**证伪方式**：以上四处改动**各自**都能单独证伪 ——
1. `ctx` 定序 struct 改回 `map[string]any` → `{"type":"bogus"}` 变 `DIFF`（`expected_tags` 与 `tag` 对调）；
2. `dryRunTagText` 分支删掉、恢复 `block["type"].(string)` → `{"type":5}` 报 `union_tag_not_found` 而非 `union_tag_invalid`，变 `DIFF`；
3. `dryRunOrderedValue` 删掉、直接 `json.Unmarshal` → 块的 `input` 变字母序，变 `DIFF`；
4. `sourceEcho` 改回 `source`（map） → 缺 `media_type` 时的 `input` 变字母序，变 `DIFF`。

**`verbatim` 模式的既有裁决保持**：参考在 `_collapse_to_verbatim` 写 `fact.fact_text`、dry-run 读 `fact.fact`，
其 verbatim 预览恒为 `""`。按 `docs/CONTRACT-AUDIT.md` §5「参考自身异常不复刻」**有意不照抄**，本轮维持 `⚠️存疑（参考自身缺陷）`，不另计入 #21 的缺失。

## 第五轮 bank-config

> 单元 `bank-config`（#14–#20）。参考源码 `api/http.py:8670`（`get_bank_profile`）、`:8683`
> （`update_bank_disposition`）、`:8698`（`add_bank_background`）、`:9716`
> （`get_bank_template_schema`）、`:9878`（`get_bank_config`）、`:9907`
> （`update_bank_config`）、`:9944`（`reset_bank_config`）；配置解析在 `config_resolver.py`
> 全文逐段读（`resolve_full_config` `:262-300`、`_public_fields_of` `:311-330`、
> `_load_bank_config` `:394-450`、`_active_bank_overrides` `:452-475`、
> `validate_bank_config_updates` `:532-655`、`_persist_bank_config` `:712-770`、
> `reset_bank_config` `:771-806`、`_validate_config_value_types` `:863-881`、
> `_coerce_stored_bank_overrides` `:884-951`）；未知参数上报 `api/unknown_params.py:167-200`；
> 前缀归一 `config.py:115-132`。
>
> 本服务落点 `internal/bankapi/{config,config_keys,config_defaults,retired_routes,retired_body}.go`、
> `internal/config/resolver.go`、`internal/entitylabels/reference_shape.go`（**跨包，见 #19-b**）、
> `internal/reporting/knowledge_schema.go`。
>
> **环境与差分口径**：本服务 `18211`（持久终端托管；`WENYUAN_API_TENANT_MODE=single`、
> `WENYUAN_API_DATABASE_SCHEMA=audit_cfg`、`WENYUAN_API_ADMIN_LISTEN=127.0.0.1:28311`、
> `WENYUAN_API_SKIP_LLM_VERIFICATION=true`、`WENYUAN_API_WORKER_ENABLED=false`），参考 `8888`。
> 两侧各建 `audit-config-<random>` 夹具 bank，结束即删。**本沙箱 `/tmp` 每次 bash 调用清空、
> 后台进程随 bash 调用结束被杀**，所以服务必须跑在 `terminal_open` 持久终端里、临时脚本放工作区
> （`scripts/parity_dev_service.sh` 在本沙箱直接不可用）。差分工具为本轮自写的
> `.audit-cfg/{diff.py,rt.py,config-steps.py}`（**保序**解析 `object_pairs_hook`，能看见键序；
> 同时比 `x-ignored-params`/`content-type`/`allow`/`retry-after` 四个响应头）。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #14 `GET .../profile` | ✅一致 | `api/http.py:8670-8682`：handler 体只有 `raise HTTPException(410, _PROFILE_RETIRED_DETAIL)`；detail 常量见 `:8700-8707` 上方 | `internal/bankapi/retired_routes.go:30-36`、`internal/httpx/errors.go`（`DetailRetiredProfile`） | 无 | 差分 **SAME**（410 + detail 逐字节）。带 `?bogus=1` → 两侧同 410 且都**不带** `X-Ignored-Params`（该路由无 query 声明也无 body 模型，参考同样不报） |
| #15 `PUT .../profile` | ❌已修（X-Ignored-Params） | `api/http.py:8683-8696`（`request: UpdateDispositionRequest`）；`:1920-1923` 模型只有 `disposition` 一个成员 → 体里的 `mission` 是未声明成员；`api/unknown_params.py:187-200`：`POST/PUT/PATCH` 且 `content-type` 含 `application/json` 时，把 body 里不在 `model_fields` 的顶层键并入 `X-Ignored-Params`（与未知 query 同一条 header，`,`+空格连接、保序） | `internal/bankapi/retired_body.go:48-56`（`RetiredBodyGate`）、`:33-37`（`UpdateDispositionRequest`）、`retired_routes.go:47-58`（`ServeRoute`：先 body gate 再 410） | **修**：`RetiredBodyGate` 读到 body 后新增 `httpx.RecordUnknownBodyFields(r, body, model)`（`retired_body.go:53-56`）。修复前该路由是 body 的唯一读者且从不调用上报，未知顶层成员静默丢弃；参考的 route class 在**进 handler 之前**上报，410 也带 header | `PUT .../profile` body `{"disposition":{...},"mission":"x"}`：修复前 `HEADER x-ignored-params: ref='mission' svc=None`；修复后 **SAME**。非法体 422 各档（`model_attributes_type`/`missing`/`greater_than_equal`）原本已 SAME |
| #16 `POST .../background` | ❌已修（X-Ignored-Params） | `api/http.py:8698-8726`：**独立 410 detail**（"The bank background endpoint has been removed. The background was folded into the reflect mission: write it with PATCH .../config as `reflect_mission`. That call replaces the value rather than merging into it, so read the current mission from GET .../config first if you relied on this endpoint's append behaviour."）；体模型 `:1950-1960` = `content: str` + `update_disposition: bool = True`，没有 `mission` | `internal/bankapi/retired_routes.go:35`（`DetailRetiredBackground`）、`internal/httpx/errors.go`、`retired_body.go:39-42` | 同 #15（同一处 `RetiredBodyGate` 修复覆盖两条路由） | `POST .../background` body `{"content":"x","mission":"y"}`：修复前 `HEADER x-ignored-params: ref='mission' svc=None`；修复后 **SAME**。**410 detail 文案本身逐字节通过**（带体/不带体两档），无需改动 |
| #17 `GET /v1/bank-template-schema` | ⚠️存疑（不修，组长裁决：仅键序不同） | `api/http.py:9716-9731`：`return BankTemplateManifest.model_json_schema()` —— **裸 dict、无 `response_model`**，故每个可选字段的 `"default": null` 都在线上；pydantic 用普通 dict 承载，成员序 = 模型声明序 | `internal/reporting/knowledge_schema.go:11-39`（`//go:embed bank_template_schema.json` + 原样输出） | 无 | **递归逐节点内容比对**：`content diffs: 0`；`json.load(ref)==json.load(svc)` → `True`；字节数**同为 31553**；`$defs` 的 14 个键名与集合相同；`required`/`enum`/`default`/`anyOf` 全等。**唯一差异是成员键序**（本服务解成 `map[string]any` 再输出，Go 按字典序）。JSON 对象键序不属成员集合，判 ⚠️ 不修；**若要求字节相同，落点是改为回写嵌入原始字节（`internal/reporting`，非本单元作用域）** |
| #18 `GET .../config` | ✅一致（键集/值/类型；键序 ⚠️ 见下） | `api/http.py:9878-9905` → `config_resolver.py:262-300` `resolve_full_config`（tenant 层 `:228-262` 先叠、bank 层后叠、`_with_overrides` `:271-285`）→ `:311-330` `_public_fields_of`（`configurable - credential`）；handler 包 `BankConfigResponse(bank_id, config, overrides)`。银行不存在 = `_require_bank_exists` → 404 `"Bank '<id>' not found"`；解析后非法 → `ValueError` 逃到 catch-all → 500 | `internal/bankapi/config.go:17-30`（`GetBankConfig`）、`:143-167`（`writeConfigResponse`）、`:169-198`（`validateResolvedConfig`/`validateChunkingInt`）、`:228-263`（`bankOverrides` = `_active_bank_overrides` 复刻：normalize 键、丢非 configurable 与 `null`、按声明类型过滤、字符串字段保留 JSON 编码）、`:295-297`（`resolvedConfig` → `config.ResolveLayers`，`internal/config/resolver.go:18-29` 逐层覆盖）；默认值表 `internal/bankapi/config_defaults.go:22-99` | 无 | **逐键比对（自写比较器，键集+值+JSON 类型）**：`config key count ref=57 svc=57`、`only-ref keys: []`、`only-svc keys: []`、`value/type diffs: 0`。未知 bank → 两侧同 `404 {"detail":"Bank '...' not found"}`。`?bogus=1` → 两侧同带 `x-ignored-params: bogus`。`?limit=5`（未声明的 query）→ 两侧同带 `x-ignored-params: limit` |
| #18 键序 | ⚠️存疑（不修） | `config_resolver.py:183`：`_public_fields` 是 `frozenset(configurable - credential)`，`_public_fields_of` 迭代它产出 dict → **成员序 = frozenset 迭代序**，取决于 `PYTHONHASHSEED`：同进程内稳定、**跨进程必然变化** | 本服务回 `map[string]any`，Go `encoding/json` 按字典序；`overrides` 走 jsonb 短键优先序 | 无 | 实测参考同一进程连续 3 次 `list(config.keys())[:4]` 恒为 `['retain_structured_chunk_size','max_observations_per_scope','llm_gemini_safety_settings','recall_budget_function']`（进程内稳定），但这是**参考自己都不稳定的量**，不构成契约 |
| #19 `PATCH .../config` | ❌已修 ×1（`entity_labels` 读回形状），其余 ✅ | `api/http.py:9907-9942`（flag 关→404 固定文案；`OperationValidationError`→其 status；`ValueError`→400 `str(e)`；兜底 500）→ `config_resolver.py:532-655` 校验序（凭据 → 静态 → 未知 → 权限 → **类型** → entity_labels → knowledge_page_default_trigger → reflect_default_options → retain_strategies → consolidation_strategies → default_observation_scopes → recall_budget → disposition → 跨字段投影）、`:712-770` `_persist_bank_config`（`SELECT ... FOR UPDATE` 后 `config = COALESCE(config,'{}') \|\| $1`，jsonb **逐键合并**；`null` 由读侧 `v is not None` 过滤 = tombstone） | `internal/bankapi/config.go:36-95`（flag → `RefuseBankWideIfScoped` → 解码 → `updates` 必填/`dict_type` → `configPatch` → `EnsureBank` → `UpdateBank`）、`config_keys.go:124-216`（三条字段集消息 + `configPatch` 逐键）、`:218-330`（`validateConfigValue` 分派）、`:620-660`（`validateRecallBudget`）、`:672-760`（`validateRetainStrategies`）、`:760-900`（`validateConsolidationStrategies`/`validateStrategyScopes`）、`:956-964`（`normalizeConfigKey`） | 见 #19-b | 见「#19 逐项差分」 |
| #19-a `HINDSIGHT_API_*` 前缀 | ✅一致（组长修，本轮线上复核） | `config.py:115-132`：`normalize_config_dict` 剥 `HINDSIGHT_API_` 前缀后转小写，**前缀匹配区分大小写** | `internal/bankapi/config_keys.go:956-964`（`normalizeConfigKey` 依次试 `WENYUAN_API_`/`HINDSIGHT_API_` 两个前缀，再 `ToLower`） | 组长已修（本行仅复核） | 线上复核：`HINDSIGHT_API_ENABLE_OBSERVATIONS` → 两侧 **200**；`HINDSIGHT_API_RETAIN_MISSION:"hs"` → 两侧 200 且 `overrides.retain_mission=='hs'`；`HINDSIGHT_API_RETAIN_CHUNK_SIZE:2500` → 两侧 200 且落 2500；`hindsight_api_retain_mission`（**小写化前缀**）→ 两侧同 **400** `Unknown configuration fields: ['hindsight_api_retain_mission']`（证明前缀匹配大小写敏感，两侧同） |
| #19-b `entity_labels` 空值读回形状 | ❌已修（**跨包：`internal/entitylabels` 归 read-surface，已获组长追认**） | `config_resolver.py:596-601`（`parse_entity_labels` 只用于**校验**）+ `:750-760`（`_persist_bank_config` 存 `validated.updates` = **调用方原 dict**）→ 故存进去的就是调用方顶层形状：`[]` 存 `[]`、`[{...}]` 存裸数组、`{"attributes":[...]}` 存 envelope | `internal/entitylabels/reference_shape.go:143-175`（`Canonical`）、`internal/bankapi/config_keys.go:245`（`configValues[normalized] = entitylabels.Canonical(raw)`）、`internal/bankapi/config.go:257-265`（`orderRevealingValue`/`Reorder` 读侧） | **修 `Canonical`**：原实现无论调用方顶层形状如何，一律返回 `OrderedObject{attributes: [...]}`（注释还写着"空列表也保留 envelope"）。现改为：**只有调用方发的是 `map[string]any`（envelope）才回 envelope，否则回裸数组**；组内成员仍按模型声明序重排（jsonb 丢序，必须读侧重排）。见下方「#19-b 的证伪」 | 线上实测参考 `PATCH {"entity_labels":[]}` → `overrides.entity_labels == []`，本服务修复前 `== {"attributes": []}`；`PATCH {"entity_labels":[{"key":"k","type":"text"}]}` → 参考 `overrides == [{"key":"k","type":"text"}]`。修复后两侧一致。**注意：旧测试 `internal/entitylabels/canonical_test.go` 的 `"a bare list is wrapped the same way"` 断言的正是错误行为（裸数组被裹成 envelope），必须按参考实测改断言** |
| #19-c `entity_labels` 校验失败时 `input_value` 键序 | ⚠️存疑（组长裁决：修，但**改派 read-surface**，本单元只移交证据） | `config_resolver.py:601-607`（`f"Invalid entity_labels format: {e}"` 原样透传 pydantic 2.12 报告）。pydantic 的 `input_value=` 渲染的是**调用方 dict**：键序 = **调用方发送序**，末尾由校验器补上 `type`（见下实测） | `internal/entitylabels/reference_shape.go` 的 `problemWithShown`（用 `map[string]any` 渲染该 dict → **字典序**） | **未改**（移交给 read-surface） | 实测同一请求 `{"attributes":[{"tag":"t","optional":"x"}]}`：<br>参考 `input_value={'tag': 't', 'optional': 'x', 'type': 'value'}`（**发送序 + 末尾 type**）<br>本服务 `input_value={'optional': 'x', 'tag': 't', 'type': 'value'}`（**字典序**）<br>反序发送 `{"optional":"x","tag":"t"}` 时参考变成 `{'optional': 'x', 'tag': 't', 'type': 'value'}` —— 证明是**发送序**而非固定序。移交说明见下 |
| #20 `DELETE .../config` | ✅一致 | `api/http.py:9944-9969`（同 flag 门）→ `config_resolver.py:771-806` `reset_bank_config`：`UPDATE banks SET config = '{}'::jsonb, updated_at = now() WHERE bank_id = $1`，**不检查银行是否存在**、**不加行锁**（注释：单语句已在锁上排队，且"无 override"状态不可能违反跨字段约束） | `internal/bankapi/config.go:97-141`（`ResetBankConfig`：flag → scope 拒 → `ResolveBankID`（未知名原样透传）→ `UpdateBank{ClearConfig:true}`；`ErrBankNotFound` 时构造 `storage.Bank{BankID: bankID}` 照常 200） | 无 | 正常删 → SAME；删后 `GET .../config` → SAME（`overrides:{}`）；**未知 bank 删** → 两侧同 `200` + 全默认配置（不建行、不 404）；重复删 → SAME；`?bogus=1` → 两侧同带 `x-ignored-params: bogus` |

### #19 逐项差分（52 步往返，两侧各 1 个夹具 bank，脚本 `.audit-cfg/rt.py`）

逐条 `PATCH` → `GET` 往返，比较状态码、`x-ignored-params`、以及（GET 时）`overrides` 与该键的 resolved 值：

**全部 SAME（38 条）**：`{"updates":{}}`、`{"updates":null}`→422、`retain_mission` 设值/设 `null`/设 `42`→400、`retain_chunk_size` `0`/`-1`/`2500.0`/`"2500"` 全 400、`retain_structured_chunk_size` `100`/`0`/`null`、`retain_max_attachments_per_chunk:0`→400、`recall_budget_min:5000+max:100`→400、`recall_budget_function` `adaptive`/`BOGUS`→400、`recall_budget_adaptive_low:0.1`/`adaptive_high:0`→400、`recall_budget_fixed_low:0`→400、
`retain_strategies` 正常 / 空策略名→400 / 非对象→400、`consolidation_strategies` 正常（含 `scopes.tags`）/ `tags:[1]`→400 / `tags_match:"bogus"`→400 / `scope` 拼错→400 / 非 list→400、
`default_observation_scopes` `[["topic:*"]]`/`"per_tag"`/`"bogus"`→400、
`knowledge_page_default_trigger` 正常 / `{"bogus":1}`→400 / `refresh_cron:5`→400、
`reflect_default_options` 正常 / `{"bogus":1}`→400 / 非对象→400、
`disposition_*` 设 5/1/3、`disposition_skepticism:null` 清除、`disposition_skepticism:9`/`0`/`3.5`→400、
`mcp_enabled_tools`、`observation_scope_limits`、`llm_gemini_safety_settings`、`audit_log_enabled`+`store_document_text`、
凭据 `llm_api_key`→400、静态 `database_url`→400、`observation_scope_limits:5`→400、`memory_defense` 坏策略→422、`{}`→422。

**⚠️ 不可判定项（不是差异，比对工具必须忽略）**：`Unknown configuration fields` / `Cannot override static ... fields` 两条消息尾部含 `Valid configurable fields: [<10 个>]...` / `Configurable fields include: [<10 个>]...`，其内容取自 `sorted(list(self._configurable_fields)[:10])` 而 `_configurable_fields` 是 **set** —— 样本随 `PYTHONHASHSEED` 每进程不同。实测两侧条数与措辞模板完全相同（`(total: 51 fields)`），仅**样本成员**不同。**这与第一轮结论一致，属不可复刻的进程内随机**。

**#19-b 的证伪**：把 `Canonical` 改回"一律裹 envelope"，线上 `PATCH {"entity_labels":[]}` 后 `GET` 的 `overrides.entity_labels` 立刻变回 `{"attributes": []}` 而参考仍是 `[]` —— `TestCanonicalKeepsTheCallersMembersAndTheSerialiserOrder` 新增的 `a bare list stays a bare list` / `an empty list stays an empty list` 两个子用例同时 FAIL。

**#19-c 移交给 read-surface 的证据包**（组长 2026-11-03 裁决：修，改派 read-surface）：
- **规则**：pydantic 2.12 的 `input_value=` 渲染**调用方 dict 的发送序**，末尾由 `parse_entity_labels` 的迁移补一个 `type` 键。
- **实测对照**：`{"tag":"t","optional":"x"}` → 参考 `{'tag': 't', 'optional': 'x', 'type': 'value'}`；反序 `{"optional":"x","tag":"t"}` → 参考 `{'optional': 'x', 'tag': 't', 'type': 'value'}`。
- **本服务现状**：`internal/entitylabels/reference_shape.go` 的 `problemWithShown` 用 `map[string]any` 渲染 → 字典序。
- **落点与影响面**：`ShapeProblems(raw)` 现在只收解码后的 map，**丢了键序**。要修必须让调用方把**原始 JSON 字节/保序对象**传进来（`internal/bankapi/config_keys.go:290-300` 与 `internal/memoryops` 的 retain 侧调用点都要跟着改），或改用 `httpx` 的 `OrderedObject` 承载。**因此需要 entitylabels + bankapi + memoryops 三个包协同，组长已改派给 read-surface 统筹。**

### 本单元改动文件

1. `internal/bankapi/retired_body.go`（+6 行）：`RetiredBodyGate` 记 `RecordUnknownBodyFields`。
2. `internal/entitylabels/reference_shape.go`（`Canonical` 重写，~10 行净改）：保留调用方顶层形状。**跨包，组长已追认**。
3. **未改** `internal/bankapi/config.go` / `config_keys.go` / `config_defaults.go` / `retired_routes.go` / `internal/reporting/knowledge_schema.go`（本轮判定无需改动；`normalizeConfigKey` 的前缀修复是组长做的，本单元只做线上复核）。

## 第五轮 operations

> 单元 `operations`（#73–#77）。参考源码 `api/http.py:8466-8512`（`api_list_operations`）、`:8514-8550`
> （`api_get_operation_status`）、`:8552-8587`（`api_cancel_operation`）、`:8589-8620`（`api_retry_operation`）、
> `:8622-8650`（`api_delete_operation`）；引擎 `engine/memory_engine.py:21925-22042`（`list_operations`）、
> `:22043-22205`（`get_operation_status`）、`:22207-22292`（`cancel_operation`）、
> `:22294-22439`（`retry_operation`）、`:22441-22490`（`delete_operation`）、`:2273-2305`（`_operation_details`）。
> 响应模型 `OperationResponse`（17 个显式成员 + 3 个 pydantic 默认补全）、`OperationStatusResponse`（17 键）、
> `ChildOperationStatus`（5 键）、`Cancel/Retry/DeleteOperationResponse`（`success,message,operation_id`）、
> `OperationProgress`（`stage,at,processed,total,detail`）。
> 本服务落点 `internal/memoryops/operations.go`、`operations_list.go`、`operations_payload.go`、
> `operation_models.go`、`internal/storage/postgres_writes.go`、`internal/storage/enums.go`。
>
> 环境：本服务 `18213`（`-tags pgx` 构建，`WENYUAN_API_DATABASE_SCHEMA=audit_memops` 独立 schema，
> `WENYUAN_API_WORKER_ENABLED=false`，`WENYUAN_API_SKIP_LLM_VERIFICATION=true`，
> `WENYUAN_API_ADMIN_LISTEN=127.0.0.1:28213`），参考 `http://127.0.0.1:8888`。
> 差分：自写 urllib 直连两侧，**掩掉时间戳/UUID 后逐字节比对**（含键序），不用
> `scripts/parity_check.py`（它先 `json.loads` 再比 dict，键序不可见）。
> 夹具：`audit-memops-jx7ade`。**参考侧只读**（从不写参考库 `user_devin`）。
> 为拿到「parent / child / pending」三种参考侧真实形状：在参考上提交一次 `async:true` 的 retain 得到真实的
> `batch_retain` 父 + 子行；本服务的同形状行用 SQL 写入**本服务自己的** `audit_memops` schema（不触碰参考库）。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #73 `GET .../operations` | ✅一致 | `api/http.py:8481-8491`：`status`/`type` 是**裸字符串**（不校验枚举）、`limit`(20, **ge=1 le=100**)、`offset`(0, ge=0)、`exclude_parents`(bool 默认 False)；`engine/memory_engine.py:21955-21990`：`status = $n`、`operation_type = $n` 原样进 WHERE，`exclude_parents` 展开成 `NOT (result_metadata::jsonb @> '{"is_parent": true}')`；`total` 与分页共用一个 WHERE（`:21965-21990`）；`ORDER BY created_at DESC`（`:21995`）；列表项取自 `OperationResponse`（17 成员，见 `api/http.py` 的 `OperationResponse`），`progress` 取 `result_metadata.get("progress")`、`details` 取 `_operation_details(...)` | `internal/memoryops/operations_list.go:19-77`（`ListOperations`、`parseOperationListQuery` 的声明序 limit→offset→exclude_parents）、`:92-114`（`queryIntParam` 走 `httpx.ParseQueryInt`）、`internal/memoryops/operations.go:364-394`（`ToOperationListDTO`）、`internal/storage/postgres_writes.go:362`（`exclude_parents` 谓词）、`:376`（`ORDER BY created_at DESC, operation_id ASC`） | 无 | 线上差分：`default`、`limit=1.0`（→ limit 回显 1）、`status=bogus`（→ **200**，不校验枚举，返回空页）、`type=bogus`（→ 200 空页）、未知 bank（→ 404 `{"detail":"Bank '…' not found"}`）全部 SAME；`limit=0`→422、`limit=101`→422、`offset=-1`→422、`exclude_parents=zz`→422 全部 SAME。**包络键序** `bank_id,total,limit,offset,operations` 两侧一致；**列表项 17 键及键序**逐位一致：`id,task_type,operation_id,operation_type,items_count,document_id,filename,mental_model_id,details,created_at,updated_at,status,error_message,retry_count,next_retry_at,progress`。`total` 差异（参考 5 vs 本服务 2）是两侧执行过的任务数不同（夹具） |
| #74 `GET .../operations/{operation_id}` | ✅一致（`result_metadata` 键序见下 ⚠️） | `api/http.py:8561-8571`：先 `uuid.UUID(operation_id)` 校验，失败 `400 Invalid operation_id format: <原样>`；`include_payload` 才 SELECT `task_payload`。`engine/memory_engine.py:22043-22105`：**查不到不是 404**，而是 200 + `status:"not_found"`（其余成员 null）；父操作走 `is_parent` 分支，查子操作 `WHERE result_metadata @> {"parent_operation_id": …} ORDER BY (result_metadata->>'sub_batch_index')::int`，若所有子均已 `completed`/`failed` 而父仍 `pending`，**读路径自愈**改写父状态并返回修正后的值（`:22121-22145`）；否则 10 个成员 + `id/task_type/mental_model_id` + `progress/details/result_metadata/task_payload` | `internal/memoryops/operations.go:96-156`（`GetOperation`）：`:100-102` 先校验 id、`:119` 的 `notFoundOperationStatus`、`:126-154` 的父自愈分支 `batchChildrenFinished` + `RepairPendingBatchParent`；`operation_models.go` 的 `OperationStatus`/`ChildOperationStatus` 字段声明序；`internal/storage/postgres_writes.go:300-320`（子操作查询） | 无 | **17 键及键序两侧逐位一致**：`operation_id,status,operation_type,id,task_type,mental_model_id,created_at,updated_at,completed_at,error_message,retry_count,next_retry_at,progress,result_metadata,details,child_operations,task_payload`。`not_found` 分支：未知 UUID 两侧同 **200 + 17 键、`status:"not_found"`、其余 null**（原始字节比过）。非 UUID → 两侧同 `400 {"detail":"Invalid operation_id format: notauuid"}`。`child_operations` 元素 5 键及键序两侧一致：`operation_id,status,sub_batch_index,items_count,error_message`。`details` 两侧同 `null`（`refresh_mental_model` 之外的类型不产出）；`task_payload` 两侧同 `null`（父行 payload 为 NULL）；`progress` 两侧同 `null`（本服务 worker 关闭，无生产者） |
| #75 `DELETE .../operations/{operation_id}`（cancel） | ✅一致 | `api/http.py:8596-8618`：先校验 UUID（400），`ValueError`→**404**，`OperationValidationError`→自身 status。`engine/memory_engine.py:22246-22262`：行不存在 → `ValueError("Operation <id> not found for bank <bank>")`（**404**）；`status not in ("pending","processing")` → 409 `"Operation <id> cannot be cancelled: status is '<status>', only 'pending' and 'processing' operations can be cancelled"`；随后**带 `status IN ('pending','processing')` 守卫的 UPDATE**，若影响 0 行 → 409 `"… cannot be cancelled: it reached a terminal state concurrently"`；成功后 best-effort 把父操作 roll-up（`_maybe_update_parent_operation`，失败只 warn）。成功体 `{"success":true,"message":"Operation <id> cancelled","operation_id":"<id>"}`（`CancelOperationResponse` 字段序 success→message→operation_id） | `internal/memoryops/operations.go:197-225`（`CancelOperation`）、`:313-327`（`mapCancelError`）、`:254-260`（`rollUpBatchParent`） | 无 | 线上：**pending → 200** 成功体逐字节与参考模型一致（`{"success":true,"message":"Operation <id> cancelled","operation_id":"<id>"}`，键序 success,message,operation_id）；**重复 cancel（已 cancelled）→ 409**，文案与参考源码逐字符相同（实测本服务：`"Operation <id> cannot be cancelled: status is 'cancelled', only 'pending' and 'processing' operations can be cancelled"`）；**参考侧对 completed 行 → 409** 同款文案（实测：`status is 'completed'`）；非 UUID → 400；未知 UUID → 404 `{"detail":"Operation <id> not found for bank <bank>"}` 两侧一致 |
| #76 `POST .../operations/{operation_id}/retry` | ✅一致 | `api/http.py:8609-8618`。`engine/memory_engine.py:22294-22439`：**单条条件 UPDATE**（`status IN ('failed','cancelled')` 或 `pending AND next_retry_at > NOW()`，且 `NOT (operation_type='batch_retain' AND task_payload IS NULL)`），命中即 `{success, message:"Operation <id> queued for retry", operation_id}`；未命中再查行：行不存在 → 404；**`batch_retain` 且 payload 为 NULL 的父**走专属分支 —— 先把失败/取消（或延迟）的**子**重置为 pending，再仅在「存在非 completed 子行」时复活父；若无 → 409 `"Operation <id> is a batch_retain parent with no incomplete sub-batches to retry (its children have all completed, or it has none). Resubmit the source documents to re-ingest them, then delete this record via DELETE /operations/<id>/delete."`；其余状态 → 409 `"Operation <id> cannot be retried: status is '<status>', expected 'failed', 'cancelled' or deferred"`（**`deferred` 无引号**） | `internal/memoryops/operations.go:228-250`（`RetryOperation`）、`:332-351`（`mapRetryError`，三条文案含 `ErrBatchParentNoWork` 的长文案与 `'failed', 'cancelled' or deferred`）、`internal/storage/postgres_writes.go` 的条件 UPDATE | 无 | 线上：**cancelled → 200** `{"success":true,"message":"Operation <id> queued for retry","operation_id":"<id>"}`（与参考同形）；**参考侧对 completed 行 → 409**，实测参考体 `"… cannot be retried: status is 'completed', expected 'failed', 'cancelled' or deferred"`，与本服务 `mapRetryError` 逐字符一致；未知 UUID → 404 两侧一致。batch parent 分支见 #74 的父操作夹具（本服务对「子已全 completed 的父」返回的是上面那条专属 409，与参考 `engine/memory_engine.py:22407-22415` 同文案；该路径**未能线上对打**，因为触发它需要参考侧存在合法父行且有权限，故本行以源码逐行比对为证据） |
| #77 `DELETE .../operations/{operation_id}/delete` | ✅一致 | `api/http.py:8636-8648`。`engine/memory_engine.py:22441-22490`：**单条 `DELETE … WHERE status IN ('failed','cancelled','completed') RETURNING`**（避免与 retry 的 TOCTOU），命中则先 `_delete_operation_export_archive(result_metadata)` 再返回 `{success,message:"Operation <id> deleted",operation_id}`；未命中则查行：不存在 → 404 `"Operation <id> not found for bank <bank>"`，存在 → 409 `"Operation <id> cannot be deleted: status is '<status>', expected 'failed', 'cancelled' or 'completed'"` | `internal/memoryops/operations.go:265-289`（`DeleteOperation`）、`:293-306`（`mapDeleteOperationError`）、`internal/storage/postgres_writes.go:600-640`（`DeleteOperation` 与 export 归档清理） | 无 | 线上：**cancelled → 200** `{"success":true,"message":"Operation <id> deleted","operation_id":"<id>"}`；**pending → 409** `"… cannot be deleted: status is 'pending', expected 'failed', 'cancelled' or 'completed'"`（与参考源码逐字符一致）；删除后再 `GET` → **200 `status:"not_found"`**（不是 404，与 #74 一致）；未知 UUID → 404 两侧一致 |

**顺带核对的边界（源码，全部一致）**

- 五个路由的 id 校验都在任何存储访问之前（`api/http.py:8563-8566/8601-8604/8639-8642`），本服务
  `rejectMalformedOperationID` 同样先跑 —— 因此非法 id 即使 bank 不存在也报 400，两侧一致。
- 三个写路由的 `ValueError` → **404**、`OperationValidationError` → **409**，两条分支的映射两侧一致
  （本服务 `mapCancelError`/`mapRetryError`/`mapDeleteOperationError`）。
- `/operations/{id}` 的未知 **bank** 不是 404：参考的 `get_operation_status` 不查 bank 存在性，只按
  `operation_id + bank_id` 查行，查不到就 `not_found`。本服务同样不查 bank —— 实测未知 bank 的
  `GET …/operations/<uuid>` 返回 200 `not_found`，与参考一致（注意与 **列表** 路由相反：列表路由查 bank，未知 bank 是 404）。

**⚠️ 存疑 —— `result_metadata` 的键序（`GET .../operations/{id}`）**

- **现象（原始字节，非 json.loads）**：参考 `"result_metadata":{"is_parent":true,"items_count":1,"total_tokens":6,"unit_ids_count":1,"num_sub_batches":1,"extraction_errors_count":0}`；
  本服务 `"result_metadata":{"extraction_errors_count":0,"is_parent":true,"items_count":1,"num_sub_batches":1,"total_tokens":6,"unit_ids_count":1}`。
- **归因（已用可复现实验钉死，不是夹具）**：在**本服务自己的** schema 里把该列写成
  `'{"zebra":1,"alpha":2,"aaaa":3,"bbbb":4,"yy":5}'::jsonb`，PostgreSQL 存回的是它自己的 jsonb 序
  `{"yy":5,"aaaa":3,"bbbb":4,"alpha":2,"zebra":1}`；而本服务发出的字节是 `{"aaaa":3,"alpha":2,"bbbb":4,"yy":5,"zebra":1}`
  —— 即 **Go `map[string]any` 的字典序**，jsonb 的序被重编码掉了。
  参考侧 `conn.parse_json(...)` 得到的是 Python dict（保留 jsonb 序），再作为
  `result_metadata: dict[str, Any]` 交给 pydantic 序列化，**按插入序输出**。
- **影响面**：`result_metadata` 是 `OperationStatusResponse.result_metadata`，SWAGGER 里自述
  “Internal metadata for debugging. Structure may change without notice. Not for production use.”
- **是否值得改**：改它需要让 `storage.Operation` 保留该列的**原始字节**（现在 `scanOperation` 立即
  `json.Unmarshal` 成 map，`internal/storage/postgres_writes.go:678-695`、`internal/storage/operation_payload.go:29-41`），
  两个后端都要跟，改动落在共享的 `internal/storage`。组长 2026-11-03 的裁决口径是：「键序非契约可记 ⚠️，
  不据此改代码」。**本轮按此不改**，只记证据；若后续裁决要修，最小改法是给 `storage.Operation` 加一个
  `ResultMetadataRaw []byte`，`scanOperation` 一并取出，`OperationStatus` 用 `json.RawMessage` 直接输出。
- 注：`progress` 子对象**不受此影响** —— 本服务 `progressOf`（`operations.go:487-505`）按固定
  `stage,at,processed,total,detail` 用 `httpx.OrderedObject` 重排，与参考生产者的写入序一致。

**未覆盖项（说明为什么没有线上对打）**

1. `progress` 快照的**非空**形状：本服务以 `WENYUAN_API_WORKER_ENABLED=false` 起（避免消费共享队列），
   没有生产者，两侧都读到 `null`。要覆盖需另起**开 worker** 的副本并让它真的跑到 consolidation 的
   `consolidating 0/N` / `refreshing_mental_models N/N` 检查点；本轮未做，`progressOf` 的固定序由
   包内 `TestOperationProgressRendersInTheReferenceMemberOrder` 覆盖。
2. `batch_retain` 父「子全 completed 后 retry → 专属 409」：需要参考侧一行合法父，参考侧我只有只读权限、
   不能造这种行；本服务侧已用 SQL 造出父 + 子验证了**读**路径（#74 的 `child_operations`），写路径以
   源码逐行比对为证据。
3. `details` 的非空形状（`refresh_mental_model` 的 `outcome`/`failure_reason`）：需要一次真实的 mental-model
   refresh 跑完，同样依赖 worker；以 `operations.go:507-550` 的取值域与参考
   `engine/memory_engine.py:2287-2305` 逐项比对为证据（枚举集合两边一致）。

## 第五轮 组长横切修复（entity_labels 测试断言）

| 项 | 结论 | 证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| `entity_labels` 裸数组读回（canonical_test.go） | ❌已修（测试断言） | config-directives 单元的线上实测：参考存调用方原值，裸数组读回仍是裸数组 | `internal/entitylabels/canonical_test.go` "a bare list stays a bare list" 用例改为断言裸数组原样保留 | Canonical 修复已由 config-directives 落地；旧断言（裹 envelope）按参考实测改写，组长代为收口 | `go test ./internal/entitylabels/` 35 passed |

## 第五轮 bg-engines

参考落点：`engine/graph_maintenance.py`（executor，约 380 行）、`engine/mental_model_refresh.py`、
`engine/retain/` 的异步副作用、`engine/llm_trace.py` 的异步落库。
本服务对应：`internal/app/maintenance_jobs.go`（`drainGraphMaintenance`）、`internal/app/mental_model_refresh.go`、
`internal/storage/postgres_maintenance_graph.go`、`internal/app/llm_trace.go`。
（`internal/worker/` 归 bg-worker 单元，本小节只报告不动。）

### B1 `graph_maintenance` 执行侧（参考 `engine/graph_maintenance.py:220-360`）

| 维度 | 参考 | 本服务 | 结论 |
|---|---|---|---|
| 两个队列的消费 | `relink_pass`（`graph_maintenance_queue`）→ `entity_prune_pass`（`entity_maintenance_queue`），**顺序固定，prune 用剩余预算**（`:220-241`） | `DrainGraphMaintenance` → `DrainEntityMaintenance`，同样顺序（`drainGraphMaintenanceInSchema`，`maintenance_jobs.go:262-268`） | ✅一致 |
| 批大小 | `_DRAIN_BATCH_SIZE = 50` / `_ENTITY_PRUNE_BATCH_SIZE = 50`（`engine/memories/pg/graph.py:67,92`） | 每段 `500`（`maintenance_jobs.go:263,268`） | ⚠️存疑：本服务一次取 10 倍，**每轮处理上限更高**（更快收敛，单次事务更重）。不是契约面 |
| 单轮是否 drain 到空 | 参考**循环 claim 直到队列空**（或预算/迭代上限）；本服务每轮每段**只取一批** | 只取一批，积压靠后续轮次（默认跟 `consolidation_reconcile_interval_seconds`，300s） | ⚠️存疑（执行位置差异，见 bg-maintenance §I） |
| 迭代上限 | `_RELINK_ITERATION_CAP` 防跑飞（`graph.py:680-690`），命中打 `error` | 无（因为不循环） | 不适用 |
| 240s wall budget | `_JOB_TIME_BUDGET_SECONDS = 240.0`（`graph_maintenance.py:60`），`deadline` 传进两个 pass，到点不再 claim、已提交的保留（`graph.py:648-651,663-667`） | **无预算**：一次 drain 一批，单条 `RelinkUnit` 失败即回队 | ⚠️存疑（见下“可观测差异”） |
| 后继 hand-off | 预算耗尽且仍有排队 → `submit_async_graph_maintenance(dedupe_excludes_operation_id=...)` 链式后继；队列已空但运行期间又有新行 → 只有“本轮有进展”才链（防死循环）（`:250-330`） | **无**：新入队的行等下一轮定时 drain | ⚠️存疑 |
| 按 bank 去重 | `submit_async_graph_maintenance` 去重含 `processing`（`:23856+`），且 `claim_tasks` 拒绝同 bank 并发认领（`graph.py:628-637`，#3230） | `EnqueueGraphMaintenance` 去重含 processing（`postgres_memories.go:948-954`）；drain 侧**无 bank 级串行化**，但一轮内按 schema 串行、且只有本循环在 drain | ⚠️存疑：多进程部署下两个进程可能同时 claim 同一 bank 的不同批，drain 语句是 `DELETE ... WHERE (bank_id,unit_id) IN (ORDER BY enqueued_at LIMIT n)`，**没有 SKIP LOCKED**，两个进程的林会互相等待 |
| 失败处理 | 参考 pass 内 `_PRUNE_BATCH_MAX_RETRIES = 3` 退避重试（`graph.py:997-1001`）；整 job 抛错由 worker 记为 operation 失败并可重试 | 本服务 drain **先删队列行再做事**，失败就把目标 `Requeue*` 回队（`maintenance_jobs.go:233-268`），**无退避**：一个持续失败的目标会在每个 300s 轮次被重取一次 | ⚠️存疑（可接受：回队语义保住了工作，但没有退避会让持续失败的目标每轮重试一次） |
| 计数与日志 | `[GRAPH_MAINT] bank=... done: {...}`，字段 6 个（`:96-104`） | 只有 `graph relink failed` / `entity prune failed` 警告 + job 计时行，**没有每个 target 的成功计数** | ⚠️存疑（纯日志面） |

**可观测差异（组长已裁定保留，此处补全证据）**：
① 本服务不产生 operation 记录 ⇒ 不出现在 `GET /operations`、不可 cancel、不参与 worker 并发/slot 控制；
② 无 240s 预算与后继 hand-off；
③ 一轮一批而非 drain 到空；
④ drain 语句缺 `SKIP LOCKED`（`postgres_maintenance_graph.go:22-26`），多进程下可能互等 —— **这是本小节的唯一一条“建议但有风险”项**：因为该 drain 只在维护循环里被调用，而维护循环在每个进程都跑，多进程部署确实存在两个进程同时 drain 同一 schema 的窗口。加 `SKIP LOCKED` 是 `internal/storage/` 的一行改动，但属 bg-worker/存储范围，**本单元只报告不修**。

### B2 consolidation reconcile 的 `enable_observations` 门（bg-maintenance §D D3 的补修）

任务书（组长裁定 a）要求在本单元内补齐。参考 `maintenance.py:483-486`：
```python
resolved = await engine._config_resolver.resolve_full_config(bank_id, context)
if not (resolved.enable_observations and resolved.enable_auto_consolidation):
    continue
```
修复：`internal/app/maintenance_jobs.go` 的 `reconcileConsolidationWithConfig` 在入队前调用
`bankConfigAnswers(ctx, schema, bankID, "enable_observations")`；解析器由
`internal/app/maintenance.go` 的 `bankObservationsGate` 提供，走本服务**现有的**分层：
`config.ResolveLayers(bank 覆盖, tenant=nil, 部署默认)`，部署默认来自 `bankapi.PublicConfigDefaults(process)`
（与 `GET /banks/{id}/config` 读的是同一套 process 层）；bank 覆盖只取 `enable_observations` 这一个布尔，
键先按 `normalizeConfigKey` 的规则去掉 `WENYUAN_API_` / `HINDSIGHT_API_` 前缀（组长本轮修的双前缀）。
构造入口：`app.NewMaintenanceLoopWithBankConfig(...)`，`internal/app/background.go:39` 与
`cmd/worker/main.go:131` 都已改用它（否则作业拿不到部署默认层）。

**证伪用例**：`internal/app/maintenance_reference_test.go::TestReconcileSkipsObservationDisabledBank`
（把门撤掉 → `queued 1 consolidations for an observation-disabled bank, want none` → FAIL，已实测）与
`TestBankObservationsGateResolvesOverrideOverDefault`（覆盖优先级 + 前缀归一）。

**残留差异**：参考的 `enable_auto_consolidation` 那一半来自 `banks_needing_consolidation()` 例程本身的过滤，
本服务同样如此（`migrations/0005_reference_functions.sql:14-91`），所以门是完整的两半。

### B3 待续（未完成，下一轮）
`engine/mental_model_refresh.py`、`engine/retain/` 的异步副作用、`engine/llm_trace.py` 的异步落库
（后者改动前需先行报备）。本小节先落 B1/B2。

### B3 mental model refresh 执行（参考 `engine/memory_engine.py:4900-4945, 19729-19790, 24140-24150, 24256-24280`）

| 维度 | 参考 | 本服务 | 结论 |
|---|---|---|---|
| 自动触发的暂停判定 | `_automatic_refresh_paused`：`last_refresh_failed_at` 非空且 `last_refreshed_at` 为空或早于它（`:24256-24280`）；`submit_async_refresh_mental_model` 里 `automatic && paused → {"paused": True}`（`:24146-24147`） | `refreshPaused(model)` 逐字同形（`internal/app/worker.go:888-893`） | ✅一致 |
| 失败→暂停的分类 | `pause_automatic=not is_transient_remote_error(e)`（`:4937`，含 429/5xx/超时/连接错误） | `!transientRefreshFailure(err)`（`provider.IsTransient` \|\| `IsRateLimitReset`，`worker.go:756-758`；openai 适配器把 429/5xx/413 归入这两类，`provider/openai/errors.go:44-61`） | ✅一致（映射逐类对得上） |
| **失败记录的覆盖面** | **每一条失败路径都记录**（`:4929-4938` unexpected_error / `:19021` / `:19123` 拒绝写入），只是 `pause_automatic` 不同 | 修复前 `worker.go:659-664`：**transient 失败在 `recordMentalModelRefreshFailure`（history 行）之后，却从不写 `last_refresh_failed_at`**——history 有行、模型不暂停，与参考一致；但**分支返回值是 `worker.Permanent(err)`**，把“provider 挂了”判成永久失败，吞掉了重试预算 | ❌已修 |
| 失败后返回值 | transient → 原异常抛出（`:4929-4945` 的 except 链），由 worker 按异常类决定重试 | 修复后：transient → 返回原错误（正常重试预算）；永久类 → `worker.Permanent`；rate-limit-reset → `Process` 的 `worker.Defer`（`worker.go:100-102`） | ❌已修 |

**修复内容**（`internal/app/worker.go:657-682`）：**本修复落在 bg-worker 作用域的文件里，由 bg-maintenance 代改**
（组长追认）。收尾时需复核该文件没有与 bg-worker 的在途改动冲突。
把“是否暂停”从分支结构里解耦出来，改成“先按失败类别决定暂停/记录，再按可重试类别决定返回值”：
- 非 transient → 记录 `RecordMentalModelRefreshFailure`（写 `last_refresh_failed_at`）；transient → 只留 history 行；
- transient → `return err`（重试）；`permanentRefreshFailure || IsAuthRejected` → `worker.Permanent`；其余 `return err`。
**证伪**：`TestTransientRefreshFailureDoesNotParkTheModel`（transient/rate-limit 两类都不暂停且都不是永久失败）。
改回原分支会让 transient 失败重新走 `worker.Permanent`，该用例对“不是永久失败”的断言即失败。

### B4 `graph_maintenance` drain 的并发正确性：❌已修（`FOR UPDATE SKIP LOCKED`）

`internal/storage/postgres_maintenance_graph.go`：`DrainGraphMaintenance`（:31-37）与
`DrainEntityMaintenance`（:213-219）的 claim 都补了 `FOR UPDATE SKIP LOCKED`（组长批准，一行/处）。

**并发推理（单进程测试难以复现，写明推理）**：维护循环在每个进程都跑且无 leader election
（`engine/maintenance.py:31-37` 的同一前提），所以两个进程可以在同一秒 drain 同一个 schema。
修复前 `SELECT … ORDER BY enqueued_at LIMIT n` 不带 SKIP LOCKED，两个进程的行集合是**同一批**：
第二个进程在前一个的行锁上排队，两个进程按 `enqueued_at` 顺序取行时形成的锁序相同，不会成环，但**会互相串行化**、
并把维护窗口拉长到两倍；若一个进程同时 drain 了 graph 与 entity 两张表、另一个顺序相反，就具备成环条件
（PG 报 deadlock_detected 并回滚一方）。补上 SKIP LOCKED 后两边取**不相交**的行集合，
与 retention sweep 的批处理、参考 `claim_graph_maintenance_batch` 的做法一致。
行为面：不改变任何 API 响应，只把并发冲突消掉。

### B5 其余 ⚠️ 项（组长定稿：可接受偏差，不修）
批大小 500 vs 50、单轮 drain 到空 vs 取一批、无 240s wall budget、无后继 hand-off、pass 内无退避重试、
缺每轮计数日志 —— 全部判 `⚠️存疑（实现参数/路径差异，语义等价，可接受）`。
`internal/app/llm_trace.go`（写路径）**本轮未改**：`engine/llm_trace.py` 的异步落库对照尚未完成，留待下一轮。

### B6 llm_trace 写路径（参考 `engine/llm_trace.py` ↔ `internal/app/llm_trace.go`）

| 维度 | 参考 | 本服务（修复前） | 结论 |
|---|---|---|---|
| 落库时机 | **缓冲后批量写**：`FLUSH_INTERVAL_SECONDS=0.5` 或 `FLUSH_MAX_ROWS=500`（`:647-648`，`:809-842`），一行必须等一次 flush 才进表 | **同步逐行写**，`_ = store.RecordLLMRequest(ctx, request)` 在调用路径上（`llm_trace.go:210`）——一次 recall 的 embedding/rerank/decision 各占一个连接 | ❌已修 |
| 关停时冲刷 | `flush_on_shutdown()`（`:865-875`），有界 drain | 无 | ❌已修 |
| 批量写失败 | 一个 batch 失败后**逐行重试**，只丢坏行（`:915-925`），全部只 warn 不抛 | 无（每行独立写自然就是逐行） | ✅（修复后保持等价） |
| scope 过滤 | `is_enabled`：exclude 优先、allow 空即全收、`*` 后缀前缀匹配（`:705-713`） | `traceScopeRecorded` 逐条同形（`llm_trace.go:85-111`） | ✅一致 |
| 行字段 | 23 列：id/bank/operation/scope/trace/span/parent/provider/model/status/started/ended/duration/5 类 token/input/output/error/llm_info(默认 `{}`)/metadata(默认 `{}`)（`:876-899`） | `storage.LLMRequest` 同 23 列，`llm_info`/`metadata` 空时存 `{}`（`traceMetadata`/`traceLLMInfoPayload`），token 为 0 时留 NULL（`setToken`，参考 `thoughts_tokens or None` 同义） | ✅一致 |
| 截断 | `_safe_json` 超 `LLM_TRACE_MAX_CHARS` 包成 `{"_truncated":true,"_original_chars":N,"preview":…}`（`:421-439`） | `truncateTrace` 同形（`llm_trace.go:425-445`） | ✅一致 |
| 失败的 output | `output=None if error else response_content`（`:221`） | 同（`llm_trace.go:181-186`） | ✅一致 |
| trace/span 层级 | 每 operation 一次 `trace_id`+`operation_span_id`，每次调用一个 `span_id`、`parent_span_id` 指向 operation span（`:530-536`） | `traceIDsFor` 按 (bank, operation) 缓存同形（`llm_trace.go:469-488`） | ✅一致 |
| embedding/rerank 行 | `trace_model_call`：input 是请求、output 只有 rerank 分数、`input_count` 进 llm_info（`:179-227`） | 同（`tracingEmbedder` / `tracingReranker`） | ✅一致 |
| `metadata.memory_ids` 补丁 | 操作结束后把该 operation 产出/消费的 memory_units 以 `UPDATE … SET metadata = metadata ‖ patch WHERE bank_id=$1 AND trace_id=$2` 补到该 trace 的每一行（`:931-990`），由 retain（`memory_engine.py:8029`）与 consolidation（`consolidator.py:1618`）调用 | **没有**：没有任何路径写 `metadata.memory_ids` / `source_memory_ids` | ⚠️存疑（见下） |

**修复**（`internal/app/llm_trace.go`）：
- 新增 `llmTraceRecorder`（进程内按 store 单例，`internal/app/llm_trace.go:535-650`）：`record` 入缓冲，满 `traceFlushMaxRows`(500) 立即写，否则 `time.AfterFunc(500ms)` 定时刷；`flush`/`closeTraceRecorder` 供关停使用；`write` 逐行落库，失败只 warn。
- `emitTraceRow` 改走 `traceRecorder(store).record(...)`（`llm_trace.go:217`），不再在调用路径上同步 INSERT。
- `internal/app/app.go:406`（`App.Close()`）增加 `closeTraceRecorder(a.Store)`（对应参考 `flush_on_shutdown`）。
- 顺带修正：`emitTraceRow` 在 `call.start` 为零值时用 `ended` 补齐——缓冲写发生在调用返回之后，零 `started_at` 会让 `#llm-requests` 的 `start_date` 过滤看不到该行。

**测试**：`TestTraceRowsAreBufferedAndFlushedOnClose`（不 flush 读不到、关停后读到）、
`TestTraceBatchWritesWhenFull`（满批立即写）；原有 9 个 trace 用例改为先 `flushTraceRows(store)` 再读——
这本身就是“改成同步写就 FAIL / 改回无缓冲也 FAIL”的证伪。

**⚠️存疑（memory_ids 补丁缺失）——需组长裁决**：参考的 `/llm-requests?memory_id=` 过滤器依赖该补丁
（`llm_trace.py:931-990`），本服务存储层的 `matchesLLMRequest` **已经**支持
`memory_ids`/`source_memory_ids` 过滤（`internal/storage/memory_observability.go:462-465`），但写入侧从不产生这两个键，
所以该过滤永远空集。补齐需要：在 retain（`internal/app/worker_retain.go` / `internal/memoryops/retain.go`）与
consolidation 结束时，按 trace 拿到产出/消费的 unit id 并回写——跨 `maintenance` 边界（retain 归 task-3、
consolidation 归 task-5），**本轮只报告不修**。

## 第五轮 directives

> 单元 `directives`（#55–#59）。参考源码 `api/http.py:7820`（`api_list_directives`）、`:7869`
> （`api_get_directive`）、`:7899`（`api_create_directive`）、`:7934`（`api_update_directive`）、
> `:7971`（`api_delete_directive`）；请求/响应模型 `api/http.py:3238-3282`
> （`DirectiveResponse` / `DirectiveListResponse` / `CreateDirectiveRequest` / `UpdateDirectiveRequest`）；
> 引擎侧 `engine/memory_engine.py:21545-21676`（`list_directives`）、`:21677-21718`
> （`get_directive`）、`:21719-21780`（`create_directive`）、`:21781-21872`（`update_directive`）、
> `:21873-21920`（`delete_directive`）、`_row_to_directive`；未知参数上报
> `api/unknown_params.py:167-200`。
>
> 本服务落点 `internal/directives/handlers.go`（`List`/`Get`/`Create`/`Update`/`Delete`/`toDirective`/
> `directiveMatchesTags`/`pageSlice`/`newID`）、`internal/directives/query.go`
> （`parseListSelectors`/`queryParam`/`boolWord`/`boundedQueryInt`）、
> `internal/directives/priority_range.go`、`internal/directives/directive_uuid.go`、
> `internal/storage/` 的 `ListDirectives`/`GetDirective`/`PutDirective`/`UpdateDirective`/`DeleteDirective`。
>
> **环境**：同 `## 第五轮 bank-config` 节首（18211 单租户副本 + 参考 8888，两侧各建
> `audit-config-<random>` 夹具 bank，结束已删——`GET .../config` 两侧均回 404 确认清除）。
> 差分工具 `.audit-cfg/{dd.py,dl.py,lifecycle.py}`：`dd.py` 逐字节比（含 `x-ignored-params`/
> `content-type`/`allow` 响应头）；`dl.py` 对列表响应把 `id`/`bank_id`/`created_at`/`updated_at`
> 掩成 `<V>`（两侧 id 天然不同，属夹具噪声，checklist §3 已列），**保留成员的键与键序**再比。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #55 `GET .../directives` | ✅一致 | `api/http.py:7820-7867`：`tags_filter: list[str] \| None = Query(None, alias="tags")`（**list 类型 = 重复参数取全部**）、`tags_match: Literal["any","all","exact"] = Query("any")`、`active_only: bool = Query(True)`、`limit: int = Query(100, ge=1, le=1000)`、`offset: int = Query(0, ge=0)` —— 全部是 `Query(...)`，**在进 handler 体之前**由框架解析校验，故坏参数压过 404；响应 `DirectiveListResponse(items, total, limit, offset)`。引擎 `engine/memory_engine.py:21545-21676`：`WHERE bank_id=$1 [AND is_active = TRUE]` + 可选 tag 过滤，`ORDER BY priority DESC, created_at DESC, id DESC`（id 兜底保证分页稳定）；`total` 是**过滤后**的计数（不含 limit/offset）；不给 tag 过滤时返回**全部**指令（注释明确与 reflect 不同） | `internal/directives/handlers.go:86-156`（校验 → `requireBank` → `ListDirectives` → 内存过滤/排序/分页）、`:158-192`（`directiveMatchesTags`：未打标恒真；`all`/`exact`/默认 `any`）、`internal/directives/query.go:13-33`（`queryParam` 取**末次**出现，只用于 scalar）、`:35-60`（`boolWord` 词表大小写不敏感、**不 trim**）、`:62-130`（`LazyInteger` + `ge`/`le` 信封，`input` 回原始 query 串）、`:147-156`（排序与参考 `ORDER BY` 同） | 无 | **33/33 SAME**（`.audit-cfg/dl.py`，掩 id/时间戳后）。覆盖：无参数、`limit=1/100/1000/1001/0/-1/abc/`（空值）、`offset=0/-1/999/abc`、`active_only=true/false/yes/1/bogus/`（空值）、`tags_match=any/all/exact/bogus/`（空值）、`tags=`（空值）、`tags=alpha`、`tags=alpha&tags=beta`、`+tags_match=all`、`+tags_match=exact`、`tags=gamma`、`?bogus=1`（两侧同带 `x-ignored-params: bogus`）、`limit=2&offset=1`、四参数同时坏 `tags_match=any&limit=0&offset=-1&active_only=zz`（两侧同 422，item 按声明序）。未知 bank：无参数 → 两侧同 404；带 `?limit=0`/`?tags_match=bogus`/`?active_only=bogus` → 两侧同 **422**（Query 先于 bank 查询） |
| #55 tag 过滤语义 | ✅一致 | `engine/memory_engine.py:21610-21670`：`tags` 非空时 `build_tags_where_clause`，外层再包 `((tags IS NULL OR tags = '{}') OR (...))` —— **未打标指令恒生效**；`tags` 为空表/缺省时**不加过滤**（返回全部） | `internal/directives/handlers.go:158-192`（`len(recordTags)==0 → true`；`len(tags)==0` 时 `List` 根本不调它）、`:120-125`（`if len(tags) > 0`） | 无 | **fixture 实测（两侧各 4 条：`alpha` / `beta` / `alpha+beta` / 无 tag）**：<br>`?tags=alpha` → 两侧同 `[tag-a, tag-ab, global]` total 3<br>`?tags=beta` → 两侧同 `[tag-b, tag-ab, global]` total 3<br>**`?tags=alpha&tags=beta` → 两侧同 `[tag-a, tag-b, tag-ab, global]` total 4**（证明本服务**取全部**而非末次；若取末次会只剩 `[tag-b, tag-ab, global]`）<br>`+tags_match=all` → 两侧同 `[tag-ab, global]` total 2<br>`+tags_match=exact` → 两侧同 `[tag-ab, global]` total 2<br>`?tags=gamma` → 两侧同 `[global]` total 1<br>`?tags=`（空值）→ 两侧同仅 `global`（空串是一个 tag，不匹配任何 → 只剩未打标） |
| #56 `GET .../directives/{id}` | ✅一致 | `api/http.py:7869-7897`：`directive is None` → 404 `Directive '<id>' not found`；`except Exception` → `_internal_error` 500 `detail=str(e)`。引擎 `engine/memory_engine.py:21677-21718`：`SELECT ... WHERE bank_id = $1 AND id = $2`，**路径串原样当绑定参数**；参考 schema `directives.id` 是 `uuid` 列 → 非 UUID 段在**驱动绑定参数时**就失败，驱动消息经兜底成 500；**注意此路由没有 `_require_bank_exists`**，未知 bank 与不存在的指令同回 404 | `internal/directives/handlers.go:195-206`（`Get`：`uuidMember` → `GetDirective` → `directiveError`）、`:340-350`（`member`/`uuidMember`）、`:352-360`（`directiveError`：`ErrDirectiveNotFound` 与 `ErrBankNotFound` 同回 `Directive '<id>' not found`）、`internal/directives/directive_uuid.go`（驱动消息复刻） | 无 | **逐字节 SAME**（`.audit-cfg/dd.py`）。非 UUID 段 6 种：`not-a-uuid`→两侧同 500、37 位 hex→500、`-`→500、40 个 `a`→500、`urn:uuid:...`→500（均含驱动原文）；`01234567-89abcdef0123456789abcdef`（连字符不在规范位置但**是合法 uuid**）→两侧同 404。合法但不存在的 uuid → 两侧同 404 同体。未知 bank → 两侧同 404 `Directive '...' not found`（**不是** `Bank '...' not found`） |
| #57 `POST .../directives` | ✅一致 | `api/http.py:7899-7932`：**没有 bank 存在性检查**，直接 `create_directive`；`ValueError`→400 `str(e)`，兜底 `except Exception`→`_internal_error` 500。模型 `:3261-3271`：`name: str`（必填）、`content: str`（必填）、`priority: int = 0`、`is_active: bool = True`、`tags: list[str] = []`。引擎 `engine/memory_engine.py:21719-21780`：直接 INSERT（列序 `bank_id, name, content, priority, is_active, tags`），未知 bank 由 `fk_directives_bank_id` 外键拒 → 500 | `internal/directives/handlers.go:208-266`（`Create`：bank 名 trim → 别名解析 → `decodeBody` → 两个 missing item → `priorityRangeFailure` → `PutDirective` → `foreignKeyReport`）、`internal/directives/priority_range.go`（`createDirectivePriorityArg=4`） | 无 | **body 校验 17 例全 SAME**：`{}`、缺 `name`、缺 `content`、`name:null`、`content:null`、`name:1`、`priority:"x"`、`priority:null`、`is_active:"z"`、`is_active:null`、`tags:"x"`、`tags:[1]`、`tags:null`、`notjson`、`[]`、`{"name":"","content":""}`（两侧同 **200**，空串合法）、带未知成员 `{"name":"n","content":"c","bogus":1}`（两侧同 200 且同带 `x-ignored-params: bogus`）。**未知 bank** → 两侧同 **500** 且 detail 逐字节相同（外键拒 + `DETAIL:  Key (bank_id)=(...) is not present in table "banks".`），带 tags 时也同 |
| #58 `PATCH .../directives/{id}` | ✅一致 | `api/http.py:7934-7969`：`directive is None` → 404；兜底 500。模型 `:3274-3282`：五个成员**全部** `\| None = None`。引擎 `engine/memory_engine.py:21781-21872`：`updates = ["updated_at = now()"]`，然后 `if name is not None` / `content` / `priority` / `is_active` / `tags` **逐个非 None 才拼进 SET** → **显式 `null` = 不改**（不是清空）；`updated_at` 恒更新；最后 `params.extend([bank_id, directive_id])`，故 id 的参数序号 = `1 + 非 null 成员数 + 1` | `internal/directives/handlers.go:267-291`（`Update`：`member` → `decodeBody` → `updateDirectiveArgNumber` → `priorityRangeFailure` → `UpdateDirective`）、`directive_uuid.go:41-63`（`updateDirectiveArgNumber`）、`priority_range.go:22-35` | 无 | **合并语义 10 步顺序 PATCH 全 SAME**（`.audit-cfg/lifecycle.py`，两侧各建同内容指令后逐步施加）：`{"priority":99}`、`{"name":"next"}`、`{"tags":[]}`、`{"is_active":false}`、`{"content":null}`、`{}`、五成员全 `null`、`{"tags":null}`、`{"tags":["y","z"]}`、`{"priority":-5}` —— 每步响应体（掩 id/时间戳）相同，最终 `GET` 全字段相同。**非 UUID 段的驱动消息在 6 种 body 下逐字节相同**：`{}`($2)、`{"priority":5}`($3)、`{"name","priority"}`($4)、`{"name","content","priority"}`($5)、`{"name":null}`($2)、五成员全给($7) |
| #59 `DELETE .../directives/{id}` | ✅一致 | `api/http.py:7971-7999`：`deleted` 为假 → 404 `Directive '<id>' not found`，否则 `{"status":"deleted"}`；兜底 500。引擎 `engine/memory_engine.py:21873-21920`：`DELETE ... WHERE bank_id = $1 AND id = $2`，`result == "DELETE 1"` | `internal/directives/handlers.go:293-303`（`Delete`）、`directive_uuid.go`（`uuidMember` → `$2`） | 无 | 正常删 → 两侧同 `200 {"status":"deleted"}`（键序同）；重复删 → 两侧同 404（detail 里各自的 id）；删后 `GET` → 两侧同 404；**非 UUID 段 5 种** → 两侧同 500 且驱动消息逐字节相同；未知 bank → 两侧同 404 `Directive '...' not found` |

### 结论计数

| # | API | 结论 |
|---|---|---|
| 55 | `GET .../directives`（含 tags 重复参数） | ✅一致 |
| 56 | `GET .../directives/{id}` | ✅一致 |
| 57 | `POST .../directives` | ✅一致 |
| 58 | `PATCH .../directives/{id}` | ✅一致 |
| 59 | `DELETE .../directives/{id}` | ✅一致 |

**5/5 ✅一致，本单元零代码改动。** 这与第四轮「组长自证」的结论一致，但本轮是**独立重新取证**（不是沿用旧结论）：
新增的证据面是 ①带 4 条不同 tag fixture 的 tag 过滤矩阵（第四轮只测了 `tags=a` 单值，**从未测过重复参数**，
而重复参数正是 checklist 点名的重点）②`PATCH` 的 10 步顺序合并（显式 null 不改、未提及成员保留）
③非 UUID 段在 12 种 body/路径组合下的驱动消息逐字节对比 ④`x-ignored-params` 响应头（第四轮的工具不读响应头）。

**本单元的作用域外发现（只报告，不在 `internal/directives/` 修）**：无。`internal/directives/` 包本身零缺陷。

### 机制 6：子任务派发与汇总（batch retain 拆子批 / 并发上限 / 父操作汇总）

| 维度 | 参考实现 | 本服务 | 结论 |
|---|---|---|---|
| 拆批粒度 | `retain_batch_tokens`，默认 `DEFAULT_RETAIN_BATCH_TOKENS = 10_000`（`config.py:1854`，注释 "~40KB of text"），按 **token** 计；`sub_batch_stream` 生成器按此切（`memory_engine.py:7560` 一带）| `WENYUAN_API_RETAIN_BATCH_TOKENS`，默认 `DefaultRetainSubBatchTokens = 10000`（`config/config.go:411`），`AsyncRetainSubBatches`（`storage/async_retain.go:251-283`）按 `CountTokens(item.Content)` 累加切批 | ✅一致（10000，按 token）|
| 拆批边界规则 | 参考的切分是「文档感知」的：同一 `document_id` 的子批共享 chunk_index 偏移（`memory_engine.py:7656-7680`）| `groupByDocumentID`（`async_retain.go:288` 起）：**连续**同 document_id 的 item 成组，无 document_id 的各自成组；超预算才 flush（`:275-277`），所以**单个超预算的组会独占一个子批**（不拆开一个文档）| ✅语义一致 |
| 子批**并发上限** | `retain_subbatch_concurrency`，默认 `DEFAULT_RETAIN_SUBBATCH_CONCURRENCY = 1`（`config.py:2056`，注释 "Default 1 keeps today's behaviour exactly; a deployment opts in"）；实现 `concurrency = max(1, get_config().retain_subbatch_concurrency)`，用 `pending` 列表 + `while len(pending) >= concurrency: await asyncio.wait(..., FIRST_COMPLETED)`（`memory_engine.py:7574-7712`）| **本服务没有子批并发**：每个子批是**独立的 operation 行**，由 consumer 的 N 个 slot 各自认领，并发度 = `WENYUAN_API_WORKER_MAX_SLOTS`（默认 10），再由 `serialization_key = childDocumentID` 把同一文档的子批**串行化**（`storage/postgres_claim.go` 的 serialization 谓词、`async_retain.go:376`）| ✅**等价且更稳**（默认 1 = 串行，本服务同一文档也串行；不同文档天然并行 —— 这正好是参考在 concurrency=1 时做不到的，但**不违反**参考的任何下限行为）|
| 派发时机的「首个子批屏障」 | 参考第 1 个子批是**屏障**：`collected.append(await task)` 同步等待，因为它是 upsert document 行的那一批，后续子批的所有权检查要读它（`memory_engine.py:7682-7690`）| 本服务无此屏障：子批是独立行、独立认领，**同一文档靠 `serialization_key` 串行**（后一个子批要等前一个不再 processing），效果等价 | ✅等价（换机制实现同一约束）|
| 取消传播 | 每个子批循环前 `_check_op_alive(operation_id)`；发现 dead → `cancel()` 所有 pending 子批再 gather（`memory_engine.py:7631-7660`）| `operationAlive` 在 `handle` **开始时**检查一次（`consumer.go:667-680` 一带，`OperationLiveness` 能力）；子批是独立认领，每个子批**各自**在自己认领时被检查 | ✅等价（粒度更细，不会漏）|
| 父操作推进者 | 最后一个子批终态 → `_maybe_update_parent_operation` 把父批推到 `completed`/`failed`/`cancelled`；`'cancelled'` 算 done（issue #4131）；`failed` 优先于 `cancelled`（`poller.py:1005-1090`）| `promoteParent`（`outcome.go:141-160`）→ `store.FinalizeBatchParent(bankID, parentID)`；守卫在 store 内（并发安全，只推进一次）| ✅一致 |
| 父操作 `error_message` 文案 | `_summarise_child_error_messages`：失败子批非空 message 的**众数**；全空 → `"One or more sub-batches failed"`（`poller.py:256-283`）| `summarizeChildErrors`（`storage/async_retain.go:419-440`）：按首次出现顺序计数取众数（`> bestCount`，**并列取先出现者**，正好等于 Python `Counter.most_common(1)` 的并列语义）→ `"One or more sub-batches failed"` 兜底 | ✅**已核实一致**（含并列规则与兜底串）|
| 零子批的父操作 | `_reconcile_orphaned_parents` 置 `failed`，文案 `orphaned batch_retain parent: no child sub-batches were persisted (worker crashed mid-submission); resubmit the source documents` | 本服务提交是**单事务**（`asyncRetainPlan` 连父子一起写），不存在零子批窗口；但**也不存在**这条对账（见机制 1 的 ⚠️）| ⚠️见机制 1 |
| 折叠（fold）| `_fold_retain_peers`（`poller.py:803-891`）：把排队在同 `serialization_key` 上的 pending retain **折进**当前执行；`max_fold_peers_for_retry = base >> retry_count`，`DEFAULT_MAX_FOLD_PEERS = 16`（`engine/retain/fold.py:132-148`）| **无对应物**（纯优化，参考自己说 "delete it and the system is still correct, only slower"）| ⚠️**性能差距，非正确性差异**。需裁决是否补 |

### 机制 7：embedding 合批

| 维度 | 参考实现（`engine/retain/embedding_coalescer.py`）| 本服务 | 结论 |
|---|---|---|---|
| 合批机制 | `CoalescingEmbedder`：并发 caller 停在共享 `_pending` 队列，dispatcher **不等定时器**，`await asyncio.sleep(0)` 一个事件循环 tick 让「已就绪」的 caller 加入同一批 | **无主动合批**。合批发生在 `batchEmbed`（`internal/provider/embed/plumbing.go:211-255`）：把一次 `EmbedPassages` 的 texts **按 `batchSize` 顺序切片**，用 `maxConcurrent` 信号量并发发出 | ⚠️**机制不同**（见下）|
| 每请求批大小 | `resolve_max_batch_size(backend, slots)` = `backend.batch_size * max(1, backend.max_concurrent_requests // slots)`；backend 不发布 batch_size 时回落 `DEFAULT_MAX_BATCH_SIZE = 64` | `resolveBatch(cfg, DefaultXxx)`：TEI 默认 `DefaultTEIBatchSize = 32`（`embed/embed.go:24`），其它 provider `DefaultBatchSize = 100`（`embed.go:22`）| ⚠️数值不同（见下）|
| 并发请求上限 | `MAX_CONCURRENT_REQUESTS = 4`（coalescer 的 slot 数），`slots` 与 backend 自身 `max_concurrent_requests` 相乘后**向下取整**分摊 | `maxConcurrent`（各 provider 的 `Config` 字段，`batchEmbed` 用 `permits` channel 限流）| 结构等价（都是「固定并发池 + 每批 ≤ batchSize」）|
| 等待窗口 | **无定时器**：`await asyncio.sleep(0)` 只是让出一次事件循环，让已经 runnable 的 caller 加入；"a caller that arrives while a backend slot is free is dispatched on the very next event-loop tick — so a lone caller pays no added latency" | **无等待窗口**：调用方传入的是**完整 texts 列表**，`batchEmbed` 立即切片发出，不存在「等别人凑批」 | ✅**参考刻意不做的等待，本服务也没有**（都不是定时合批）|
| 失败传播 | `_run_request`：后端抛错时给**批内每一个 waiter** 的 future `set_exception(exc)`，并调用 `waiter.future.exception()` 取走异常避免 "unretrieved"；`CancelledError` 再 raise。注释：「silently dropping them would commit the document with those facts missing」 | `batchEmbed`：第一个错误记进 `firstErr`（`mu` 保护，**只记第一个**），`wg.Wait()` 后**整体返回 `nil, firstErr`**；所有 goroutine 都跑完才返回 | ⚠️**差异**：参考让每个 waiter 各自失败，本服务让**整次调用**失败。后果：本服务一个坏批会让同一文档的**全部**文本一起失败（参考只失败共享该请求的那批）。→ 见下 |

**机制 6 的裁决点**
1. **`fold`（折叠排队 retain）未复刻** → ⚠️性能差距，不影响正确性（参考自述）。建议**不补**：本服务已用独立 operation 行 + `serialization_key` 串行，语义等价；补 fold 会改动 claim 事务与 payload 语义，风险 > 收益。
2. **子批并发**：本服务「不同文档天然并行」比参考默认 1 更好，且同文档仍串行 → ✅**不报为差异**。

**机制 7 的裁决点（需要拍板）**
- **(a) 批大小数值**：参考 TEI 走 `resolve_max_batch_size`（TEI 的 `batch_size` 通常 32 × 并发分摊），本服务 TEI 明文默认 32，其它 100。设置了 `WENYUAN_API_EMBEDDINGS_TEI_BATCH_SIZE` 时两侧都能调 → **默认值层面接近，语义不同**。
- **(b) 失败传播粒度**：这是**真实可观测差异**——后端一次 4xx/5xx，参考只让共享该请求的那批失败（其它批继续），本服务让整次 `EmbedPassages` 失败（同一文档全部再试）。
  **但要修它得改 `internal/provider/embed/`（不在我作用域）**，且「整批失败」其实更保守（不会提交部分事实）。→ ⚠️**需裁决**：是否要按参考改成「分批独立失败」。
  倾向**不修**：参考合批是为了吞吐（把多次调用并成一次），本服务的调用方本来就是「一次性传全文档的 texts」，两者的失败域本就不同；改成部分成功会让 `RetainWrite` 拿到不完整的向量集，反而要新增「部分失败」的语义。
- **(c) 无合批 → 吞吐差异**：参考的 coalescer 解决的是「每个 chunk 一次 embedding 调用」的碎片化；本服务的 `RetainWrite` 已经是按文档聚合后一次性 embed（`storage/embeddings.go:114-130` `embedContents`），**不存在**参考要解决的那个碎片化问题。✅**不报为差异**。

### 机制 8：backlog 指标刷新

| 维度 | 参考实现（`metrics.py:1045`、`:1133-1220`、`:1220-1313`）| 本服务 | 结论 |
|---|---|---|---|
| 开关 | `metrics_backlog_enabled`，默认 **`DEFAULT_METRICS_BACKLOG_ENABLED = False`**（`config.py:2152`）| `WENYUAN_API_METRICS_BACKLOG_ENABLED`，默认 `false`（`config/tuning.go:217`，`config/reference.go:409`）| ✅一致 |
| 指标名 | `hindsight.async_operations` / `hindsight.consolidation.backlog` / `hindsight.consolidation.failed`（OTel → Prometheus 下划线化）| `wenyuan_async_operations` / `wenyuan_consolidation_backlog` / `wenyuan_consolidation_failed`（`internal/app/metrics.go:16-20`）| ✅一致（整体改名 `hindsight_`→`wenyuan_`，AGENTS.md 列明的有意改名）|
| HELP 文案 | `"(pending=queued backlog, processing=in-flight, failed=stranded)"` 等 | `internal/app/metrics.go:24-28` **逐字照抄**参考描述 | ✅一致 |
| 标签维度 | `tenant`、`operation_type`、`status`；`bank_id` 仅当 `metrics_include_bank_id` 开启时（`metrics.py:1157-1166`）| `tenant`/`operation_type`/`status`，`bank_id` 仅当 `IncludeBankID`（`internal/app/metrics.go:136-158`）| ✅一致 |
| 统计口径 | `WHERE status IN ('pending','processing','failed')` —— **只统计这三态**，`completed`/`cancelled` 明确排除 | `CountAsyncOperations(ctx, perBank)`（`storage/observability.go:230` 起的 `AsyncOperationCounter`）| ✅一致（需以 store 实现确认，见下）|
| consolidation backlog / failed 语义 | 两个 gauge **互斥**：永久失败的 fact **不算** backlog，否则「backlog > 0 持续 N 分钟」这个告警条件永远不消（`metrics.py:1276-1280`）| `stats.PendingConsolidation` / `stats.FailedConsolidation` 分开两组（`metrics.go:183-200`）| ✅一致 |
| **刷新方式** | **30s 缓存循环**：`BACKLOG_METRICS_REFRESH_SECONDS = 30`（`metrics.py:107`），`_backlog_refresh_loop` 每 30s 调 `_refresh_backlog()` 重算并写进 `_async_ops_counts` / `_consolidation_backlog` / `_consolidation_failed`；observable gauge 的 callback **只读缓存**，永不在 scrape 路径跑 COUNT | **scrape 时实时 COUNT**：`backlogMetrics` 返回的闭包在每次 `/metrics` 渲染时执行，带 **2 秒**超时（`internal/app/metrics.go:78-100`）| ⚠️**实现差异（组长裁定：不修，可接受）**|
| 循环启动点 | `set_db_pool` → 若 `metrics_backlog_enabled` 则 `_setup_backlog_metrics()` → `loop.create_task(...)`（`metrics.py:1045-1046`、`:1209`）| 无循环；`expositionExtras` 在 handler 构造时装配闭包 | ⚠️同上 |
| 单次刷新的扫描范围 | 遍历 `information_schema` 里所有有 `memory_units` 表的 schema（= 所有已 provision 的租户），**每个 schema 3 条 COUNT**（`metrics.py:1238-1310`）| `metricTargets`：单租户取进程 schema，多租户向 tenant extension 要 active set（`metrics.go:105-124`），然后每 schema 1 条 `CountAsyncOperations` + 每 bank 1 次 `BankStats` | ⚠️**成本结构不同**（本服务在 scrape 时按 bank 逐个 `BankStats`，bank 多时 scrape 会变重；2s 超时会静默截断成空 series）|
| 失败处理 | `_refresh_backlog` 里每条查询各自 `try/except: logger.debug(...)`，一个 schema 查不动不影响其它；整个 `_refresh_backlog` 抛错由 `_backlog_refresh_loop` 的 `try/except: logger.debug` 吞掉，循环继续 | 每个 target 出错 `continue`（静默跳过），整体 2s 超时后返回已收集的部分 | ✅一致（都是「尽力而为、不打断 exposition」）|

**机制 8 的结论**：指标名、标签维度、统计口径、开关默认值、互斥语义**全部一致**；唯一差异是
「30s 缓存刷新 vs scrape 实时 COUNT」——对外暴露的**契约面（名字/标签/取值语义）不变**，只影响数值新鲜度与 scrape 开销。
**组长 2026-11-03 裁定：不要求复刻缓存循环，记 `⚠️存疑（实现差异，可接受）`，不修。**

**顺带发现的成本风险（不改，仅报告）**：本服务在 scrape 时对每个 bank 调一次 `BankStats`（`metrics.go:161-181`）。
`BankStats` 会算 `PendingConsolidation`/`FailedConsolidation`。bank 数量增长到数千时，一次 `/metrics` 会打出数千次查询，
2s 超时后**静默返回空 series**（`out` 只含已收集部分），表现为「指标突然消失」而不是「指标变慢」。
参考用 30s 后台缓存根本不会出现在 scrape 路径上。→ 建议（不在本轮作用域）把 consolidation 两态换成
一次 `GROUP BY bank_id` 的聚合查询，或按参考加缓存。

### bg-worker 小结与验证门

**8 个机制逐条结论**

| # | 机制 | 结论 | 说明 |
|---|---|---|---|
| 1 | 恢复逻辑（`recover_own_tasks` / `release_own_tasks` / `_reclaim_own_processing_tasks`）| ✅一致（时点不同）＋ ⚠️1 项存疑 | 回收语义与文案逐项一致；差异是「重启即接管」vs「等 lease（≤300s）」，组长裁决**不改**。关机释放不花 retry —— 本服务更对，有意偏差 |
| 2 | lease 续租 / 超时 / 失去 lease | ✅一致 | 两侧**都不续租**；本服务多一条「lease 过期可被认领」的时间回收（有意增量，等价实现）；守卫（`status NOT IN (...)`）到位，不会双写。附带发现 `WorkerID` 默认常量 `"worker"` 的多进程风险 ⚠️ |
| 3 | 背压（`_backpressure_defer_seconds` / `_append_conflict_defer_seconds`）| 背压 ✅一致；**append-conflict ❌已修** | 背压 120s 常数一致；append-conflict 阶梯 `min(2s·2^(n-1), 300s)·uniform(0.5,1.5)` + `append_conflict_defers` 计数**本轮补齐**，3 条用例 + 证伪通过 |
| 4 | wall ceiling（`_wall_timeout_for`）| **❌已修**（三处） | (a) `min(lease, ceiling)` 让 3600/7200 全部失效成 300s；(b) 超限走 retry 而非 fail；(c) consolidation 的空闲上限缺失。全部按参考修，7 条用例 + 3 条证伪通过 |
| 5 | 失败重试（次数/曲线/分类/`format_task_error`）| ✅一致（重试次数、默认曲线、consolidation ladder、`[:5000]` 截断、众数文案）；⚠️1 项待裁决 | 「通用错误默认 fail（参考）vs 默认 retry（本服务）」需裁决，倾向不改 |
| 6 | 子任务派发/汇总 | ✅一致 | 10000 token 拆批、文档感知分组、父批推进、众数文案全部一致；子批并发用「独立 operation + `serialization_key` 串行」等价实现；`fold` 未复刻 → 性能差距，建议不补 |
| 7 | embedding 合批 | ⚠️需裁决（2 项） | 本服务不做主动合批（调用方本就整文档一次 embed），无等待窗口（参考也没有）；差异在**每请求批大小默认值**与**失败传播粒度**（参考分批独立失败，本服务整次失败） |
| 8 | backlog 指标刷新 | ✅一致（契约面）；⚠️实现差异（组长裁定不修）| 指标名/标签/口径/开关默认/互斥语义全部一致；差异是「30s 缓存循环」vs「scrape 实时 COUNT」 |

**计数：✅ 5 / ❌已修 2（机制 3、机制 4）/ ⚠️存疑 5 项（机制 1×1、机制 2×1、机制 5×1、机制 7×2 中的批大小与失败粒度）**

**本轮代码改动清单（全部在我的作用域）**

| 文件 | 改动 | 行数 |
|---|---|---|
| `internal/worker/consumer.go` | append-conflict 常量/阶梯/`DeferAppendConflict`/`Config.Jitter`/`deferOperation` 分支/`appendConflictDefers`/`mergeMetadata`；`handle` 的 ceiling 装配（去 `min(lease, ceiling)`）+ `WallTimeoutExceeded` 终态分支 | 约 +120 |
| `internal/worker/wall_ceiling.go` | **新增**：`WallCeiling`、`wallCeilings` 表、`WallCeilingOf`、`WallTimeoutExceeded.Describe`、`ReportProgress`、`activeCeiling`（可推后的 deadline + 真实 timer）| 新文件 ~250 行 |
| `internal/worker/append_conflict_test.go` | **新增**：3 条用例 | 新文件 |
| `internal/worker/wall_ceiling_test.go` | **新增**：6 条用例 | 新文件 |
| `internal/worker/consumer_test.go` | `TestWallCeilingBoundsATask` 改断言（原断言「必须重试一次」是错的）；新增 `TestWallCeilingFailsTheTaskWithTheReferenceExplanation`、`TestWallCeilingIsNotCappedByTheLease`、`TestUnmappedOperationTypeIsBoundedByTheLease` | ±60 |
| `internal/app/worker_retain.go` | `classifyRetainFailure` 的 `ErrDocumentConflict` 分支：`worker.Defer(err)` → `worker.DeferAppendConflict(err)`（**唯一改动**，`applyRetain` 函数体未动）| +5/-3 |
| `internal/config/worker.go` | `resolveWallTimeouts`：显式 `0` 现在真正关闭上限（对齐参考 `timeout > 0 else None`）| +6/-4 |

**共享文件改动**：**无**。`internal/storage/`、`internal/app/app.go`、`internal/monitoring/worker_metrics.go` 一律未改。

**验证门（本单元）**

| 门 | 结果 |
|---|---|
| `go build ./internal/worker/...` | 通过 |
| `go build ./...` | 通过（本单元收尾时）|
| `CGO_ENABLED=0 go build -tags pgx ./...` | 通过 |
| `go vet ./internal/worker/...` | 无输出 |
| `gofmt -l internal/worker internal/app/worker_retain.go` | 空 |
| `/data/go/bin/golangci-lint run ./internal/worker/...` | **0 issues**（修掉 1 条 staticcheck S1005）|
| `go test -count=1 ./internal/worker/...` | **62 passed** |
| `go test ./internal/app/...` | 收尾时被别的单元在 `internal/app/maintenance.go` 的中间态阻塞（`bankapi.NormalizeConfigKey` 未定义），非本单元问题 |

**未做差分的说明**：本单元 8 条全是进程内机制，没有可观测 HTTP 面；且 `/metrics` 的 backlog 家族默认关闭
（`metrics_backlog_enabled=false`），差分也会是空集。按 checklist §3 末条以「源码逐行比对 + 表驱动往返用例」取证。

## 第五轮 bank-lifecycle

> 单元 `bank-lifecycle`（#6–#13）。参考源码 `api/http.py:6779`（`api_list_banks`）、
> `:8726-8733`（`_alias_response`）、`:8735/8758/8789/8824`（别名四件套）、
> `:8852/8897/8943`（PUT/PATCH/DELETE bank）；真实逻辑 `engine/memory_engine.py:15596-15693`（`list_banks`）、
> `:22503-22641`（`update_bank`）、`:14808-14933`（`_ensure_bank_exists`）、`:12037-12200`（`delete_bank`）、
> `engine/retain/bank_utils.py:341-356`（`validate_new_bank_id`）、`:817-900`（`list_banks_page`）、
> `engine/bank_aliases.py`（`create_alias`/`set_primary`/`delete_alias`/`list_aliases`/`primary_aliases`）、
> `engine/memories/pg/engine_curation.py:118-123`（`count_bank_contents`）。
> 本服务落点 `internal/bankapi/{banks,aliases,validation,decode,models}.go`、
> `internal/storage/postgres_banks.go:364-430`（`DeleteBank`）。
>
> **环境（本轮踩过的坑，后续单元直接照抄）**：
> - `parity_dev_service.sh` 会先 `go build` 再起服务，**构建红就把旧进程一起带走**；且它起在 `etc/wenyuan.yaml` 的
>   `tenant_mode: multi` 上，data plane 会 401。正确做法：`CGO_ENABLED=0 go build -tags pgx -o .audit-monitor/srv ./cmd/server`
>   （**`-tags pgx` 不可省**，否则启动报 `no database driver registered`），然后在**持久 terminal**里跑：
>   `WENYUAN_API_PORT=18210 WENYUAN_API_ADMIN_LISTEN=127.0.0.1:28210 WENYUAN_API_TENANT_MODE=single
>   WENYUAN_API_TENANT_EXTENSION='hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension'
>   WENYUAN_API_TENANT_API_KEY=sk-hs-… WENYUAN_API_DATABASE_SCHEMA=audit_monitor
>   WENYUAN_API_WORKER_ENABLED=false WENYUAN_API_SKIP_LLM_VERIFICATION=true .audit-monitor/srv`
> - `DATABASE_SCHEMA=audit_monitor` 会**自动建表迁移**（实测 **25 张表**），写操作与参考库 `user_devin` 完全隔离。
> - `parity_check.py` 的 **service 侧要显式给 `WENYUAN_PARITY_SERVICE_KEY`**，否则服务按空 key 认证、全线 401。
> - `parity_check.py` **没有 `--raw`**（那是旧 `/tmp/rt/chk.py` 的参数），该文件本机不存在；键序另做原始字节比对。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #6 `GET /v1/default/banks` | ✅一致 | `api/http.py:6779-6802`（`q`/`limit` 默认 100 且 `ge=0`/`offset` 默认 0 且 `ge=0`）；`engine/memory_engine.py:15618-15624`（`limit=max(limit,0)`/`offset=max(offset,0)` 夹取）、`:15668`（`apply_sql_fact_counts`/`apply_store_fact_counts` 只对页内）、`:15673-15680`（`display_alias` + `_overlay_bank_config_disposition_mission`）；页切片 `engine/retain/bank_utils.py:782`（`sort_keys`）、`:807` `result.sort(key=..., reverse=True)`（**按 `last_write_at` 降序**，`last_write` 取 `max(last_document_write_at, last_fact_at)`，`:771-773`，都空则回退 `created_at`）、`:786`（`total` 取**切片前**的 `len(banks)`）、`:744-753`（`matched_aliases` 按 `ORDER BY alias`，仅 `q` 非空时算）、`:757-760`（`_aliases_explaining`：命中 bank_id/name 时为空）；`BankListResponse` 成员序 `api/http.py:2022-2061`，`BankListItem` 成员序 `:1980-2018` | `internal/bankapi/banks.go:48`（`ListBanks`）、`:67-88`（`beyondEnd`：offset 超出即空页但 `total` 不变）、`:253`（`parseBankQuery`）、`:96-137`（`echoedLimit`/`echoedOffset`/`echoedPageParam` 回显）；`internal/bankapi/models.go`（`BankListResponse`/`BankListRow` 成员序） | 无 | **实测全一致**：<br>① **键集与键序**：自建 bank 的原始字节两侧成员序完全相同 —— `banks[].{bank_id,name,disposition{skepticism,literalism,empathy},mission,created_at,updated_at,fact_count,last_document_at,last_write_at,display_alias,matched_aliases}` 与顶层 `banks,total,limit,offset`，`diff` 无差异（除 `created_at/updated_at` 为各自请求时刻）。<br>② **排序**：三个 bank `-a/-b/-c` 各自写入后，两侧都返回 `['c','b','a']`（`last_write_at` 降序）。<br>③ **分页**：`limit=1&offset=0/1/2/3` → 两侧逐档同序（`c`/`b`/`a`/`[]`），且 `total` 恒为 3、`limit`/`offset` 回显相同。<br>④ **`q` 大小写不敏感**：`audit-monitor-six…`/`AUDIT-MONITOR-SIX`/`n6`/`N6` 两侧 same（各 1），`m6`/`zzznope` 两侧都 0（`m6` 命中 mission 不算，参考只匹配 id/name）。<br>⑤ **`matched_aliases`**：给 bank 加别名 `…-zzq` 后按 `q=zzq` 搜索，两侧都返回该 bank 且 `matched_aliases:['…-zzq']`、`total:1`。<br>⑥ **参数校验**：`limit=-1`/`offset=-1` → 两侧 `422 greater_than_equal`（`ctx.ge=0`、`input:"-1"` 逐字节同）；`limit=abc`/`limit=1.5` → 两侧 `422 int_parsing`；`limit=0` → 两侧 `200` 且 `"banks":[]`、回显 `limit:0`；`q=zzz` 空集 → 两侧 `{"banks":[],"total":0,"limit":100,"offset":0}`。<br>⑦ **未知查询参数**：`?bogus=1` → 两侧同 `X-Ignored-Params: bogus`；`?bogus=1&bogus2=x` → 两侧同 `bogus, bogus2`（逗号+空格分隔、保持请求序）。<br>⑧ `GET /v1/default/banks/{bank_id}`（参考未声明该方法）→ 两侧同 `405 {"detail":"Method Not Allowed"}` |
| #7 `GET /v1/default/banks/{bank_id}/aliases` | ✅一致 | `api/http.py:8735-8756`（`api_list_bank_aliases`）→ `:8726-8733` `_alias_response`：返回 `BankAliasesResponse{bank_id, aliases:[{alias, primary}]}`；`engine/bank_aliases.py:list_aliases` 的 `ORDER BY is_primary DESC, created_at, alias`（**主别名在前，其余按创建时间再按别名**）；银行不存在 → `OperationValidationError` 404 | `internal/bankapi/aliases.go:12`（`ListAliases`）、`:213`（`resolveCanonical` 先解析别名再判存在）、`internal/storage/postgres_aliases.go` 同名排序 | 无 | ① 两个别名 `-a1`/`-a2`（都非主）→ 两侧 `{"aliases":[{"alias":"…-a1","primary":false},{"alias":"…-a2","primary":false}],"bank_id":"…"}` **逐字节相同**；<br>② 提升 `-x1` 为主后 → 两侧都 `[{x1,true},{x2,false}]`；再提升 `-x2` → 两侧都 `[{x2,true},{x1,false}]`（**主别名置顶 + 前任降级**，顺序与参考 `is_primary DESC` 一致）；<br>③ 不存在的 bank → 两侧同 `404 {"detail":"Bank 'audit-monitor-ghost' not found"}`；<br>④ 以别名寻址访问别名列表 → 两侧同样返回 `bank_id` 为**规范 id** |
| #8 `POST /v1/default/banks/{bank_id}/aliases` | ✅一致 | `api/http.py:8758-8795`（装饰器 `status_code=201`、`operation_id="create_bank_alias"`）→ `engine/bank_aliases.py:create_alias`：先 `validate_new_bank_id(alias)`（`:209`，**别名与 bank id 同规则**：空、>192 字节、`Cc` 控制字符、`_` 前缀），再 `alias == bank_id` → **400** `A bank cannot be an alias of itself`（`:211-212`），然后 `INSERT … WHERE NOT EXISTS (SELECT 1 FROM banks WHERE bank_id=$1) ON CONFLICT (alias) DO NOTHING`（`:216-226`）——**一张表里同时挡住"撞 bank id"和"撞已有别名"**，`inserted is None` → **409** `'<alias>' is already taken by another bank or alias`（`:227-228`）；`primary=true` 时同事务调 `set_primary`（`:231-234`）。响应体来自 `_alias_response`（**整个别名列表**） | `internal/bankapi/aliases.go:73`（`AddAlias`）、`:61-70`（`aliasItems`：先校验体再解析 bank）、`:137-147`（`addAliasError` 把 `ErrInvalidBankID`→400 自身别名、`ErrAliasConflict`→409）、`internal/storage/postgres_aliases.go` | 无 | ① 首次创建 → 两侧同 `201` + `{"bank_id":"…","aliases":[{"alias":"…-a1","primary":false}]}`（**逐字节相同**）；<br>② 追加第二个 → 两侧同 `201` + 两个别名按创建序；<br>③ 冲突（别名已被占用）/自身别名（`alias == bank_id`）/缺 `alias`（`missing`）/`{"alias":null}`（`string_type`）/`{"primary":null}`（`bool_type`）—— 全部与参考一致（第四轮已逐条 SAME，本轮复核 `primary:null`→`bool_type`、缺 `primary` 不报错仍 201 的行为未变）；<br>④ 未知 bank → 两侧同 `404`（体先于存在性校验，见 #9）；<br>⑤ `PUT` 到"仅作为别名存在"的名字 → 两侧同 `409`（不能创建会遮蔽既有别名的 bank） |
| #9 `PATCH /v1/default/banks/{bank_id}/aliases/{alias}` | ✅一致 | `api/http.py:8789-8838`（`api_set_bank_alias_primary`，`operation_id="set_bank_alias_primary"`）→ 体模型 `SetBankAliasPrimaryRequest{primary: bool}`，**框架先于处理器校验**；处理器 `if not await set_bank_alias_primary(...)` → **404** `Bank '<bank_id>' has no alias '<alias>'`（`:8824`）；否则 `_alias_response`。引擎 `engine/bank_aliases.py:set_primary`：**同事务**内先 `SELECT 1 … WHERE alias=$1 AND bank_id=$2`（不归属 → False），`primary=true` 时**先降级**本 bank 其它主别名（`is_primary=FALSE AND alias<>$2`）再置位（`:255-267`），否则唯一索引会撞；`primary=false` 只置自身为假 | `internal/bankapi/aliases.go:162`（`PromoteAlias`）、`:170`（`resolveAlias` 只解析不判存在）、`:177-185`（`primaryRequiredFailure`）、`:199-209`（`aliasOrWriteError` → 404 `Bank '<bank>' has no alias '<alias>'`） | 无 | ① **提升**：`{"primary":true}` → 两侧同 `200` + `[{x1,true},{x2,false}]`（**逐字节相同**）；<br>② **改提升另一个**（冲突语义）→ 两侧同 `200` + `[{x2,true},{x1,false}]`，即"前任被降级"而非报冲突；<br>③ **降级** `{"primary":false}` → 两侧同 `200` + 两个都 `false`（不自动选新主）；<br>④ **未知别名** → 两侧同 `404 {"detail":"Bank 'audit-monitor-al10131' has no alias 'nope-alias'"}`；<br>⑤ **银行不存在 + 体非法** → 两侧同 `422 bool_parsing`（**体校验先于 404**，与参考的框架前置校验一致）；<br>⑥ `{}` → 两侧同 `422 missing ["body","primary"]`；`{"primary":null}` → 两侧同 `422 bool_type` |
| #10 `DELETE /v1/default/banks/{bank_id}/aliases/{alias}` | ✅一致 | `api/http.py:8824-8870`（`api_delete_bank_alias`）→ `False` → **404** `Bank '<bank_id>' has no alias '<alias>'`；否则 `_alias_response`。引擎 `engine/bank_aliases.py:delete_alias`：`DELETE … WHERE alias=$1 AND bank_id=$2 RETURNING alias`（**按 bank 限定**，不能通过别名删别的 bank 的名字），随后 `invalidate(alias)` | `internal/bankapi/aliases.go:224`（`RemoveAlias`）、`:229`（先 `resolveCanonical` 判 bank 存在 → 404 `Bank '<id>' not found`）、`:199-209`（`aliasOrWriteError`） | 无 | ① **删主别名** `…-x1`（当时 `primary:true`）→ 两侧同 `200` + `[{x2,false}]`（**列表整体回传、剩余别名不自动接任主位**），且随后 `GET /banks?q=` 该 bank 的 `display_alias` 两侧都变成 `null`；<br>② **删普通别名** → 两侧同 `200` + `{"aliases":[]}`；<br>③ **重复删**（已不存在）→ 两侧同 `404 {"detail":"Bank '…' has no alias '…-x2'"}`；<br>④ **bank 不存在** → 两侧同 `404 {"detail":"Bank 'ghost-bank-xyz' not found"}`（**bank 层 404 先于别名层**） |
| #11 `PUT /v1/default/banks/{bank_id}` | ✅一致（已取证，结论见下条追加） | `api/http.py:8852-8895`（`api_create_or_update_bank`，`operation_id="create_or_update_bank"`）→ `app.state.memory.update_bank(..., create_if_missing=True)`；`engine/memory_engine.py:22503-22641`：`mission is not None or config_updates` 才做 bank-wide 授权（`:22515-22517`），`normalized_config_updates` 先校验（`:22556-22563`），`create_if_missing` 时 `_ensure_bank_exists`（`:22577-22578`，内含 `validate_new_bank_id`，**只在创建路径**），config 持久化（`:22580-22596`），`name`/`mission` **只 SET 实际提供的列**（`:22601-22623`），最后回读 profile；响应 `BankProfileResponse{bank_id,name,disposition,mission,background}`（成员序 `api/http.py:1840-1859`，`background = mission` 向后兼容） | `internal/bankapi/banks.go:120`（`ReplaceBank`）、`:166-199`（不存在 → 别名冲突 409 / `writeIdentifierError` / `CreateBank`）、`:200-220`（存在 → `applyBankUpdate` 只写提供的字段）、`:205`（`configPatch` + `mergeConfig`） | 无 | 详见下方「#11–#13 追加取证」一行 |
| #12 `PATCH /v1/default/banks/{bank_id}` | ✅一致（已取证，结论见下条追加） | `api/http.py:8897-8940`（`api_update_bank`）→ 同一 `update_bank` 但 `create_if_missing=False`；`engine/memory_engine.py:22520-22536`：**不存在直接 404** `Bank '<bank_id>' not found`，且 `:22592-22598` 对 `BankConfigPersistenceConflictError` 也归 404（并发删除）；错误前缀是 `PATCH /v1/default/banks/{bank_id}` | `internal/bankapi/banks.go:158`（`UpdateBank`）、`:163-176`（**先解码校验体、后查银行**，使"bank 不存在 + 体非法"返回 422 而非 404）、`:213`（`resolveAlias`） | 无 | 详见下方「#11–#13 追加取证」一行 |
| #13 `DELETE /v1/default/banks/{bank_id}` | ✅一致（已取证，结论见下条追加） | `api/http.py:8943-8973`（`api_delete_bank`）→ `DeleteResponse{success, message, deleted_count}`（成员序 `api/http.py:3063-3072`），`deleted_count = result.get("memory_units_deleted", 0)`；`engine/memory_engine.py:12078-12083`（"never created 是零值 no-op 而非错误"）、`:12188-12192`（`count_bank_contents` 取 memory_units/entities/documents）、`engine/memories/pg/engine_curation.py:118-123`（三表 `COUNT(*)`）；bank 不存在仍 200 且 `deleted_count:0` | `internal/bankapi/banks.go:249`（`DeleteBank`）、`:253`（`ResolveBankID` 改写别名）、`:274`（`DeleteResponse{Success,Message,DeletedCount}`，成员序同参考）、`internal/storage/postgres_banks.go:364-430`（`counts["memory_units"]+counts["entities"]+counts["documents"]`，与参考同式） | 无 | 详见下方「#11–#13 追加取证」一行 |

**#11–#13 追加取证（2026-11-03 22:1x–23:5x 实测，同一 `audit_monitor` 隔离 schema 上的自建 bank）**

- **#11 PUT（创建路径）**：`{"name":"mon","mission":"m1"}` → 两侧同 `200` + `{"bank_id":…,"name":"mon","disposition":{"skepticism":3,"literalism":3,"empathy":3},"mission":"m1","background":"m1"}`，**逐字节相同**。
- **#11 PUT（更新路径，幂等）**：同 body 再 PUT → 两侧仍同体同码。
- **#11 PUT `{}`** → 两侧同 `200` 且**保留**原 `name/mission`（`m1`），证明"只 SET 提供的列"。
- **#11 PUT 只给 `name`** → 两侧同 `200`，`name` 变 `mon2` 而 `mission` 仍 `m1`（**不因 PUT 语义清空未提供字段**）。
- **#11 PUT config 字段合并**：`{"reflect_mission":"rm1"}` → 两侧 `mission`/`background` 都变 `rm1`（config 覆盖 legacy 列的 overlay 语义）；随后 `{"retain_chunk_size":2048}` → 两侧 `GET …/config` 的 `overrides.retain_chunk_size` 都是 `2048`（**逐键增量合并，不重置前一次写的键**）。
- **#11 PUT 错误路径**：`{"name":123}` → 两侧同 `422 string_type`；`{"disposition":"bad"}` → 两侧同 `422 model_attributes_type`（**不是** `dict_type`）；`{"bogus":1}` → 两侧同 `200`；`/banks/_audit-x` → 两侧同 `400` "must not start with '_': that prefix is reserved"；`/banks/` → 两侧同 `307`。
- **#12 PATCH null 语义**：`{"name":null}` → 两侧同 `200` 且 `name` **不变**（`null` 等于未提供，不清除）。
- **#12 PATCH 未知/未知字段**：`{"bogus":1}` 走 PUT 同路径已证 `200`；体类型错误两侧同 `422`。
- **#12 PATCH 顺序**：bank 不存在 + `{"name":123}` → 两侧同 **`422 string_type`**（不是 404）；bank 不存在 + `{"name":"x"}` → 两侧同 `404 {"detail":"Bank 'audit-monitor-nope' not found"}`。
- **#12 PATCH disposition 部分**：`{"disposition":{"skepticism":5}}` → 两侧同 `422`，**两个 item 按成员序**：`literalism` 后 `empathy`，均 `missing`、`input` 为传入的子对象。
- **#13 DELETE 真值（有内容）**：同一 bank 两侧各 retain 两条内容后删除，`deleted_count` 参考 `10` / 本服务 `9` —— **差异来自模型抽取的事实条数不同（同 bank `stats.total_nodes` 参考 4 / 本服务 3）**，公式本身相同（`memory_units + entities + documents`，`postgres_banks.go:394-429` 与 `engine_curation.py:118-123` 逐项对照）。属模型随机性，**不是契约差异**，不据此改代码。
- **#13 DELETE 空/不存在**：从未创建的 bank → 两侧同 `200 {"deleted_count":0,"message":"Bank '…' and all associated data deleted successfully","success":true}`（**键序 `deleted_count,message,success` 语义等价，原始字节序 `success,message,deleted_count` 两侧一致**）。
- **#13 DELETE 别名**：以别名 `…-al` 删除 → 两侧 `message` 里都是**规范 id**；删后以该别名 `GET …/aliases` → 两侧同为 `404`（**别名行随 bank 删除而清掉**）；删后以该别名 `PUT` → 两侧都**新建**一个同名 bank（别名不再路由）。

**bank-lifecycle 小结：#6 ✅ / #7 ✅ / #8 ✅ / #9 ✅ / #10 ✅ / #11 ✅ / #12 ✅ / #13 ✅（8 条全部 ✅一致，零处改动）**

**本单元顺带发现、移交 bank-config 单元的一处真实差异（不在本单元写作用域，未改）**：
`GET /v1/default/banks/{bank_id}/config`（#18）的 **`config` 对象键序**两侧不同 —— 键集完全相同（57 个），参考按声明序
（`retain_structured_chunk_size, max_observations_per_scope, llm_gemini_safety_settings, recall_budget_function, …`），
本服务按**字母序**（`audit_log_enabled, consolidation_llm_batch_size, …`）。参考序**不是**字母序（`sort -c` 报
`disorder: max_observations_per_scope`）。这正是第四轮 §0 记录的"键序盲区"复发：`parity_check.py` 把 JSON 解析成
dict，看不到键序。详见 `.audit-tmp/handoff-notes.md`。

## 第五轮 mental-models

> 单元 `mental-models`（#38–#46），参考 `api/http.py:7107 / 7175 / 7213 / 7244 / 7291 / 7333 / 7383 / 7419 / 7462`。
> **第四轮"组 H"修过 6 处，本轮按"旧结论只当线索"重审。**
> 差分方式：两侧各建同名 bank（`audit-obsmm-mm-*`）后逐请求比对；工具 `./.audit-obsmm/diff.sh`
> （内含 `up.sh` 起副本 + 逐请求 `curl` 两侧 + 状态码与响应体**整字节**比对，不做 JSON 规范化，
> 因此键序差异也会被抓到）。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #38 `GET .../mental-models`（列表：空 bank、未知 bank、`detail`/`tags_match`/`limit`/`offset` 的界与空串） | ✅一致 | `api/http.py:7107-7172`：`tags`(alias)、`tags_match: Literal["any","all","exact"]`、`detail: Literal["metadata","content","full"]`（默认 `metadata`）、`limit: int = Query(100, ge=1, le=1000)`、`offset: int = Query(0, ge=0)`（**无上界**）、`exclude_knowledge_pages: bool`；`memory_engine.py:17190-17320` 先 `_require_bank_exists` 再分页；响应 `api/http.py:3602-3608`（`items/total/limit/offset`） | `internal/mentalmodels/handlers.go:55-112`（先 `detailParam`/`tagsMatchParam`/`pageParams`/`excludeKnowledgePagesParam`，再 `GetBank` 404）、`:66-69`（detail 默认 `metadata`）、`:70-73`（tags_match 默认 `any`）、`:74-77`（limit 1..1000 默认 100、offset `ge=0` 无上界）；`internal/mentalmodels/helpers.go:31-58`（`detailParam`/`tagsMatchParam`：**出现即校验**，空串 422 不回落默认）、`:77-118`（`pageParams`/`boundedQueryInt`，`max<min` 表示无上界） | 无 | 两侧实测 **11/11 SAME**（整字节，含 422 的 `type`/`loc`/`msg`/`input`/`ctx`）：<br>`list` 空 bank → `200 {"items":[],"total":0,"limit":100,"offset":0}`<br>未知 bank → `404 {"detail":"Bank 'zz-nosuch-9a' not found"}`<br>`?detail=` → 422 `literal_error` `input:""` `ctx.expected="'metadata', 'content' or 'full'"`<br>`?tags_match=` → 422 `literal_error` `ctx.expected="'any', 'all' or 'exact'"`<br>`?limit=` → 422 `int_parsing` `input:""`<br>`?offset=` → 422 `int_parsing` `input:""`<br>`?limit=0` → 422 `greater_than_equal` `ctx.ge:1`<br>`?limit=1001` → 422 `less_than_equal` `ctx.le:1000`<br>`?offset=999999` → `200` 空页（**offset 无上界**，第四轮修过，本轮确认无回归）<br>`?detail=bogus` / `?tags_match=bogus` → 422 `literal_error` |


**结论：✅一致（形状/参数/错误路径）；⚠️存疑（边计数，见下）。**

| 项 | 参考 | 本服务 |
|---|---|---|
| 参数 | `limit: Query(1000, ge=0)` 是**唯一** `Query(...)`；`type`/`q`/`tags_match`/`document_id`/`chunk_id` 是裸 `str`；**不声明 `tag_groups`** | `internal/readsurface/handlers.go:64-66` 只声明 `type/limit/q/tags/tags_match/document_id/chunk_id`；`entity_params.go` 的 `NoTagGroups` |
| 信封 | `{nodes, edges, table_rows, total_units, limit}` | 同 |
| node.data | `id/label/text/date/context/entities/color`（`memory_engine.py:2741-2760`） | 同 7 键 |
| edge.data | `id/source/target/linkType/weight/entityName/color/lineStyle`（`memory_engine.py:2719-2735`） | 同 7 键 |
| 配色 | node 按实体数 0/1/>1 → `#e0e0e0`/`#90caf9`/`#42a5f5`；edge `#ffd700`(entity)/`#ff69b4`(semantic)/`#999999`(causal)/`#00bcd4` dashed(temporal) | `internal/storage/graph_derive.go`、`internal/readsurface/handlers.go` 同 |
| 边预算 | `GRAPH_MAX_EDGES=10_000`（`memories/base.py:807`），stored 与 derived **各一份预算**；每单位每实体邻居上限 10（`memory_engine.py:2584`） | 同（`graph_derive.go:8-13`） |

**实测（两侧同名 bank，同两侧各自独立 retain 的等价数据）**：
`?limit=-1`（422 `greater_than_equal`）、`?limit=abc`（422 `int_parsing`）、`?limit=0`（200，nodes/edges 空）、
`?type=bogus`（200 空）、`?tags_match=bogus`（422）、`?chunk_id=nope` → **全部 SAME**；
`?tag_groups=bogus` 两侧都 **200**（参考不声明该参数，本服务同样忽略）——SAME。

**⚠️ 边计数差异（存疑，非确定性差异）**：同一请求上参考 144 条边 / 本服务 137 条。
拆开看：参考 `semantic=72, temporal=54, entity=18`，本服务 `semantic=84, temporal=27, entity=26`。
根因是**两侧的底层事实不同**：两侧各自跑 retain，同一个抽取模型给出的 `fact_type` 与关系边不一致
（本方 `world 4 / experience 2 / observation 6`，参考侧 `world 6 / observation 6`），
而 `_assemble_graph_data`（`memory_engine.py:2593-2796`）的**边是纯粹由 units+links+unit_entities 组装**出来的：
`direct_links` → `copied_links`（观察单元继承其来源单元的链）→ `semantic_links`（同一来源下的观察两两配对）
→ `entity_links`（每实体按稀有度排序、滑动窗口 10）。装配算法本身逐行比对一致，差异全部来自输入数据。
**待组长裁决**：是否接受"边计数差异来自夹具数据（不同 retain 结果）"这一解释。若要完全消弭，
只能拿**真正同一份数据**（参考自己的库）来比，而本机取不到。


**结论：✅一致（形状/参数/错误路径/limit 语义）；⚠️存疑（节点集合差异，见下）。**

| 项 | 参考 | 本服务 |
|---|---|---|
| 参数 | `limit: Query(1000, ge=0)`、`min_count: Query(1)`（**无上下界**）、`tags`、`tags_match: TagsMatch=Query("any")`、`tag_groups` | `internal/readsurface/handlers.go:67-68`；`entity_params.go` 的 `MinCount` 用 `unboundedInt` |
| 语义 | `limit` 限**边**数，不是节点数；节点从被返回的边里收集（`engine_curation.py:407-460`），所以 `total_entities = len(nodes)` 而不是"库里的实体总数" | 同（`handlers.go:559-609`） |
| 信封 | `{nodes, edges, total_entities, total_edges, limit}` | 同 |
| node.data | `id/label/mentionCount/color`（>1 → `#42a5f5`，否则 `#90caf9`） | 同 |
| edge.data | `id=f"{from}-{to}"`、`source/target`、`linkType="cooccurrence"`（**不是**"entity"）、`weight=count`、`color="#ffd700"`、`lineStyle="solid"`、`lastCooccurred` | 同（实测样本逐键相同） |

**实测**：`?limit=0`（0/0/0/0）、`?limit=-1`（422）、`?limit=abc`（422）、`?min_count=abc`（422）、
`?min_count=2/5`（0 条）、`?tags_match=bogus`（422 `literal_error`，五个候选值一致）、
`?tag_groups=bogus`（400 `Invalid tag_groups: [...]` **逐字节相同**）、`?limit=-1&min_count=abc`（单信封两项）、
`?limit=3`（节点 3/边 3/total 一致）、`?tags=env:prod`（8/6/8/6）→ **全部 SAME**。

**⚠️ 节点集合差异（存疑）**：无参数时参考 `13 实体 / 10 边`，本服务 `12 实体 / 10 边`。
参考多出实体 **`柏林团队`**（中文拟人化标签名），本服务没有。两侧 `entity_cooccurrences` 边集合完全等价
（10 条，`(Alice,柏林) (Bob,巴黎) (Acme,Carol) (东京,Acme) (东京,Carol) (Dave,柏林) (Eve,Frank) (Eve,巴黎路线图) (Frank,巴黎路线图) (柏林,报告)`），
差别只在**实体抽取的词表**——参考多抽出一个中文团队名，属于 LLM 抽取结果差异，不是本服务 graph 逻辑的差异。
**待组长裁决**：同 #60，是否接受"夹具数据不同导致"的解释。

### 机制 9（补齐 checklist §7 的「后台写 fire-and-forget」）：`engine/background_writes.py`

checklist §7 把「后台写（fire-and-forget）」列为独立一组。参考的 `PendingWrites`（`engine/background_writes.py:17-69`）有两处使用：
`engine/audit.py:156`（`self._writes = PendingWrites("audit log write")`）与 `engine/llm_trace.py:673`。

| 维度 | 参考 `PendingWrites` | 本服务 | 结论 |
|---|---|---|---|
| 为什么保留引用 | 注释：事件循环对 task 只有**弱引用**，无引用的写可能被 GC 中途回收；且调用方在**关闭连接池**或**对同一批行发 UPDATE** 前必须先让写落盘 | Go 的 goroutine 不受 GC 这种影响（调度器持有栈），所以「保引用」这一条**不适用**；「先落盘再 UPDATE」这一条有对应实现 | 不适用的部分不适用；适用的部分见下 |
| 按 key 分桶 / `drain(key)` | `_pending: dict[str \| None, set[Task]]`，`schedule(coro, key)` / `drain(key)` / `drain_all(timeout)` | 无分桶；`llmTraceRecorder.attachMemoryIDs` 在发 goroutine **之前**同步 `r.flush()`（`internal/app/llm_trace.go:746-748`），正是参考说的「先把行落库再 UPDATE」 | ✅**该约束已满足**（用同步 flush 而不是 drain-key）|
| 关闭时 `drain_all(timeout)` | `audit.py:210` 在关池前 `await self._writes.drain_all(_DRAIN_TIMEOUT_SECONDS)`；超时的写被放弃并**打 warning 计数** | `attachMemoryIDs` 的 goroutine（`llm_trace.go:749`）**没有登记、没有 drain**：进程退出时这些 metadata 补丁可能丢 | ⚠️**差异**（只在关机窗口）|
| audit 写 | `_writes.schedule(self._safe_log(entry))` —— **异步**（`audit.py:202`）| `audit.Logger.Record`（`internal/audit/audit.go:214-254`）**同步**执行 `l.store.RecordAuditLog`，错误只打 warn | ✅**更保守**（同步写不会丢，代价是请求尾延迟；`httpx/audit.go:97` 用 `context.WithoutCancel` 保证客户端断开也写完）|
| 错误处理 | `_safe_log` 吞掉异常只记日志 | 同样只 `slog.Warn` | ✅一致 |

**结论**：`PendingWrites` 的两个用处在这里都**没有原样存在**，但各自的**正确性约束**都已满足（audit 同步写；llm trace 先 flush 再 UPDATE）。
唯一残留是 `attachMemoryIDs` 的 goroutine 没有 drain，**关机瞬间可能丢一次 metadata 补丁** —— 这是 `llm_trace.py` 的补丁，
归组长正在做的 `llm_trace` 工作（我上一条消息已把这条约束告知 `auditor-bg-maintenance`）。
→ ⚠️**移交**，本单元不改（`internal/app/llm_trace.go` 不在我的作用域）。


**结论：✅一致（键集、错误路径、过滤语义）；⚠️存疑（`first_seen` 值，见下）。**

| 项 | 参考 | 本服务 |
|---|---|---|
| 参数 | `tags`、`tags_match: TagsMatch=Query("any")`、`tag_groups`，全部 `Query(...)` 先于 handler 体 | `internal/readsurface/handlers.go:495-535`；`entity_params.go:44-100` 参数校验前移到 UUID 解析之前 |
| 信封 | `{id, canonical_name, mention_count, first_seen, last_seen, metadata, observations}`（7 键） | 同 7 键（实测两侧 `list(a)` 逐键相同） |
| `observations` | 恒 `[]`（观察已被 mental model 取代，store 两端都硬编码空列表） | `handlers.go:620-637` `entityDetail` 同 |
| 非 UUID id | 参考自己的 400：`'<x>' is not a valid UUID`（**不是**驱动 500，`entities.id` 是 uuid 列但 handler 先解析） | `validUUIDShape` 前置判定，同 400 |
| 未知 bank | **不预检**，返回实体自身的 not-found | 同（`GetEntity` 不调 `requireBank`） |

**实测（两侧同名实体，按 canonical_name 对齐）**：
`nope`/`not-a-uuid`（400）、全零 UUID（404）、未知 bank + UUID（404）、未知 bank + 非 UUID（**400**，参数/id 先于 bank）、
`?tags_match=bogus` 配非 UUID id（**422** 压过 400）、`?tag_groups=bogus` 配非 UUID id（**400 `Invalid tag_groups`** 压过）、
`?tags=no-such-tag`（404 `Entity <id> not found`，两侧仅 id 不同）、`?tags=env:prod`（200 且 mention_count 都变 2）
→ **状态码与正文形态全部一致**。

**⚠️ `first_seen` 差异（存疑）**：同一实体两侧 `first_seen` 不同——参考 `2026-11-03T13:40:48.654672+00:00`（真实时刻），
本服务 `2026-11-03T00:00:00+00:00`（**当天零点**）。查库确认本服务 `memory_units.event_date` 就是零点
（`select event_date from memory_units` → `2026-11-03 00:00:00+00`），而参考的事件日期带真实时刻。
两侧写入路径同为 `COALESCE(event_date, now())`（`ops_postgresql.py:399-401` / `postgres_retain.go:817-819`），
差别只在**喂进来的 event_date 值**。这个值是 retain 抽取阶段的产物，属 `retain` 单元（#21–#25）的输入面，
**不在本单元写作用域内**，故只报告不改。


| 项 | 结论 | 参考证据 | 本服务落点 | 验证 |
|---|---|---|---|---|

**#22 差分清单（15 例全 `SAME`）**

```
{}                       {"operation":"retain"}       {"operation":"consolidation"}
{"operation":"reflect"}  {"operation":"bogus"}        {"operation":5}
{"operation":null}       {"operation":""}             {"strategy":5}
{"strategy":""}          {"bogus":1}                  null    []    5    "x"
```

本轮**未发现差异、未改代码**。三类 operation 的渲染、`strategy` 的类型/空串处理、
未知 operation 的 `Literal` 拒绝、未知成员的忽略、非对象 body 的 `model_attributes_type`，
以及 `run_settings`/`strategies`/`response_schema`/`skipped_reason` 的键集与键序全部与参考逐字节一致。

> 与第三轮结论一致（第三轮曾用 20 例覆盖含配置注入的 retain/consolidation/reflect 渲染）；本轮复验仍全 `SAME`。


| 项 | 结论 | 参考证据 | 本服务落点 | 验证 |
|---|---|---|---|---|

**#25 差分清单（10 例全 `SAME`）**

| 请求 | 两侧结果 |
|---|---|
| 无 `type` | `SAME`（`message` 无 ` of type` 子句） |
| `?type=world` / `experience` / `observation` | `SAME`（`message` 带 ` of type '…'`） |
| `?type=bogus` | `SAME`（未知类型不报错，`Cleared 0`） |
| `?type=`（空串） | `SAME`（空串按"未给"处理，无子句） |
| `?type=%20world`（前导空格） | `SAME`（`' world'` 不是已知类型，`Cleared 0`，子句带 `' world'`） |
| `?type=WORLD` | `SAME`（大小写敏感，`Cleared 0`） |
| `?type=world&type=experience`（重复参数） | `SAME`（末值优先） |
| 未知 bank `audit-retain-ghost-18212` | `SAME` 200 `{"success":true,"message":"Cleared 0 memory unit(s) from bank '…'","deleted_count":0}`（参考的 `delete_bank` 不做 bank 存在性检查，未知 bank 不是 404） |

本轮**未发现差异、未改代码**。`deleted_count` 在两侧都是活测量值（各自库内容不同），
上表比对的是**状态码 + 键集 + 键序 + `message` 模板**，这些是契约面。

> 级联语义（删 `fact_type='observation'` 时同时清 observation 历史、`invalidated` 计数触发 consolidation）
> 属存储层副作用，第三轮已用「同 body 两侧逐类型建库 + 往返再读」取证为一致，本轮沿用该结论；
> 本轮复验的是 HTTP 契约面。

### 附：`entity_labels` 校验失败的 `input_value` 键序（由 bank-config 单元移交）

**结论：✅一致（线上已复验，不需要改）。**

`internal/bankapi/config_keys.go:287` 的 `entitylabels.ShapeProblems(raw)` 渲染 pydantic 的
`input_value={'...'}`，该 dict 的键序在参考里是**调用方发送序 + 末尾由 `parse_entity_labels` 的
legacy 迁移补一个 `type`**（`engine/retain/entity_labels.py:96-108`：`patched = dict(raw); patched["type"] = ...`）。

实测（两侧各自建的同名 bank `audit-read-elx`，同一组 `PATCH /config`）：

| `entity_labels` | 参考 `input_value` | 本服务 |
|---|---|---|
| `{"attributes":[{"tag":"t","optional":"x"}]}`（反序） | `{'tag': 't', 'optional': 'x', 'type': 'value'}` | **同** |
| `{"attributes":[{"optional":"x","tag":"t"}]}` | `{'optional': 'x', 'tag': 't', 'type': 'value'}` | **同** |
| `[{"tag":"t","optional":"x"}]`（裸列表） | `{'tag': 't', 'optional': 'x', 'type': 'value'}` | **同** |

三次 `detail` **逐字节相同**；反序那次两侧都是 `tag` 在前，说明本服务跟随发送序而非字典序。

**⚠️ 残留风险（不是与参考的差，是不确定性）**：`migrationDictOrder`
（`internal/entitylabels/reference_shape.go:760-775`）排序依据是 `migrate()` 后 map 的键，
而 `migrate()` 用 `for k, v := range raw` 把调用方键按 **Go map 随机迭代序** append，再补 `type`。
因此同一请求多次调用**可能给出不同键序**。根治需 `bankapi` 把 `httpx.RecordBodyOrder`/`BodyOrder`
的保序解码结果传进 `ShapeProblems`，改动外溢到 `internal/bankapi/`（不在 read-surface 写作用域），
本轮**不改**，作为建议提交给 bankapi 单元。

### 机制 2 的补充修复：`WorkerID` 启动告警（组长裁决 (c)，2026-11-04）

**问题**：`workerID()` 在未配置时回退为常量 `"worker"`（原 `consumer.go:1026-1031`）。同一部署的多个 worker 因此**同名**，
而两处写都按该 id 选择行：启动/认领时的回收、以及关机的 `releaseHeldWork`
（`ListOperations{Status: processing, WorkerID: owned}` → `ReleaseOperationClaim`）。后者的谓词只有
`operation_id + worker_id`（`postgres_claim.go:180-187`），无法区分进程 —— 于是一个 worker 优雅退出会
**把另一个 worker 正在跑的操作释放回 pending**，第二个 worker 随后会看到自己的行被改写。
参考有同类告警 `warn_if_container_default_worker_id`（`worker/main.py:246`，其默认 id 是 hostname，同理会撞）。

**修复**：`internal/worker/consumer.go`
- 抽出 `const defaultWorkerID = "worker"`（语义不变，行为不变）；
- 新增 `warnAboutDefaultWorkerID()`：仅在 `cfg.WorkerID == ""` 时于 `Run` 开头打一条 `Warn`，文案点名
  `WENYUAN_API_WORKER_ID` 并说明后果（"one worker's shutdown can release another's in-flight operations"）；
- `Run` 起始处调用一次。
**只加告警，不改任何行为**：单 worker 部署（默认形态）依然完全正确，所以不拒绝启动。

**验证**（`internal/worker/worker_id_test.go`，新增 3 条）

| 用例 | 断言 | 证伪 |
|---|---|---|
| `TestDefaultWorkerIDWarnsAtStartup` | 未配置 id → `Run` 实跑一轮后日志含 `worker id is not configured` 且含 `WENYUAN_API_WORKER_ID` | **删掉 `Run` 里的 `c.warnAboutDefaultWorkerID()` → FAIL**（`an unconfigured worker id must warn at startup, got:` 空日志）|
| `TestConfiguredWorkerIDDoesNotWarn` | 配置了 id → 不出现该告警（避免正确配置的部署被噪声掩盖）| 去掉 `if c.cfg.WorkerID != "" { return }` → FAIL |
| `TestDefaultWorkerIDIsStable` | 回退值仍是稳定常量 `"worker"`（跨重启稳定的 id 正是「回收自己遗留行」的前提）| 改成随机 id → FAIL |

`go test ./internal/worker/...` **65 passed**；`gofmt` 空；`golangci-lint` 0 issues。

### 组长裁决落定（2026-11-04）

| 项 | 裁决 | 处理 |
|---|---|---|
| 机制 5(i) 通用错误默认走法 | **不改** | 记 `⚠️存疑（失败前重试次数不同：参考 1 次 / 本服务 4 次；终态一致均为 failed）`。理由：终态相同，默认重试 3 次对瞬时故障更宽容；反转会破坏 consolidation/webhook 显式 retry 路径的既有语义 |
| 机制 7 失败传播粒度 | **不改** | 记 `⚠️存疑（批内传播粒度不同，调用形态不同所致）`。理由：本服务调用方本就整文档一次 embed，失败域天然不同；改成分批独立成功会引入"部分向量"新语义 |
| 机制 2 `WorkerID` 启动告警 | **修** | 见上，已修 + 3 条用例 + 证伪 |
| `attachMemoryIDs` 无 drain | 已由 bg-maintenance 在 memory_ids 补丁轮一并修 | 本单元不再处理 |
| backlog 指标千 bank 成本 | **本轮不修**，记 `⚠️存疑（性能风险，非契约差异）` | 组长收尾时并入 `docs/PARITY-OPEN-ITEMS.md`。风险：`internal/app/metrics.go:161-181` 在 scrape 时逐 bank 调 `BankStats`，bank 数上千时一次 `/metrics` 数千查询，2 秒超时后**静默返回空 series**（指标表现为「消失」而非「变慢」）。建议方向：换成一次 `GROUP BY bank_id` 聚合，或按参考加 30s 缓存 |

## 第五轮 transfer

> 单元 `transfer`（#78–#87）。参考源码 `api/http.py`：`:8975`（import）、`:9062`（export）、
> `:9159`（document-transfer 已退役 410）、`:9189`（document-transfer/export）、`:9253`（document-transfer）、
> `:9327`（transfer/export）、`:9411`（transfer/import）、`:9518`（clone）、`:9601`（attachments/{id}）、
> `:9655`（files/download/{key:path}）。本服务落点 `internal/transfer/`、`internal/transferarchive/`、
> `internal/objectstore/`、`internal/app/transfer*.go`、`internal/migration/archiveimport/`。
>
> 环境：本服务 18210（`DATABASE_SCHEMA=audit_monitor` 隔离 schema，25 张表自动迁移），参考 `127.0.0.1:8888`。
> 夹具 `audit-transfer-<rand>`，跑完两侧 `GET /banks?q=audit-transfer` 均为 `total=0`（已清理）。
>
> **已知夹具噪声（不计为差异）**：本服务副本 `WENYUAN_API_WORKER_ENABLED=false`，所以异步操作的
> `status` 停在 `pending`，而参考 worker 常开、已跑到 `completed`。这批 `status` 差异属部署夹具，
> checklist §3 已明列；**操作的字段集与键序仍逐项比对**（见 #85）。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #78 `POST /v1/default/banks/{bank_id}/import` | ✅一致 | `api/http.py:8975`（`api_import_bank`）；请求体是 archive（zip/JSON），响应 `ImportResponse`；`?dry_run=true` 时 `dry_run` 回显真值 | `internal/transfer/handlers.go`、`import_apply.go`、`import_gate.go` | 无 | ① **正常导入**：用自己刚导出的 archive 导回同一 bank → 两侧**响应体逐字节相同**：`{"bank_id":"audit-transfer-26969","config_applied":true,"mental_models_created":[],"mental_models_updated":[],"directives_created":[],"directives_updated":[],"operation_ids":[],"dry_run":false}` `[200]`；<br>② **`?dry_run=true`** → 两侧同体、仅 `"dry_run":true`，其余成员与序完全相同；<br>③ **非法 archive**（body `notazip`）→ 两侧同 `400 {"detail":"Expecting value: line 1 column 1 (char 0)"}`（**参考解析器的原样报错文本**，本服务复刻了 Python json 的错误措辞） |
| #79 `GET /v1/default/banks/{bank_id}/export` | ✅一致 | `api/http.py:9062`（`api_export_bank`），`response_class=Response`、`media_type=application/json` | `internal/transfer/export_build.go`、`handlers.go` | 无 | ① **响应头**：两侧同为 `200`、`content-type: application/json`、`content-length` 相同（**1909 字节，两侧完全同长**）；<br>② **响应体**：归一化（时间戳/UUID）后两侧**完全相等** —— 顶层键与序 `version, bank, mental_models, directives` 相同；`bank` 对象 57 个配置键的**键名与序完全相同**（`reflect_mission, retain_mission, retain_extraction_mode, retain_custom_instructions, retain_chunk_size, retain_structured_chunk_size, enable_observations, observations_mission, enable_text_search, enable_temporal_retrieval, enable_graph_retrieval, enable_reranking, disposition_skepticism, …`），`version` 两侧同为 `"1"`；<br>③ **ghost bank** → 两侧同 `404 {"detail":"Bank 'ghost-xyz' not found"}` |
| #80 `GET /v1/default/banks/{bank_id}/document-transfer` | ✅一致 | `api/http.py:9159`（参考已移除，保留 410 语义） | `internal/transfer/retired.go` | 无 | 两侧同 `410`，`detail` 文案**逐字节相同**（含把 bank_id 插进 URL 模板的那句：`Synchronous document export has been removed because it could take down the shared API on large banks. Submit an async export via POST /v1/default/banks/<bank_id>/document-transfer/export, poll GET …/operations/{operation_id}, then download …`） |
| #81 `POST /v1/default/banks/{bank_id}/document-transfer/export` | ✅一致 | `api/http.py:9189`（`api_export_documents`），异步提交 → `202` + `{"operation_id","status":"pending"}` | `internal/transfer/submit_handlers.go` | 无 | `{}` 提交 → 两侧同 `202`，响应**键集与键序** `operation_id, status` 相同，`status` 两侧都 `"pending"`（`operation_id` 为各自 UUID，属生成 id 噪声）；后续该 bank 的 `operations` 列表里两侧都出现同一类型 `export_documents` |
| #82 `POST /v1/default/banks/{bank_id}/document-transfer` | ✅一致 | `api/http.py:9253`（`api_import_documents`，multipart，`file` 必填） | `internal/transfer/binary_handlers.go`/`submit_handlers.go` | 无 | 空 body `{}` → 两侧同 `422`，`detail` **逐字节相同**：`[{"type":"missing","loc":["body","file"],"msg":"Field required","input":null}]` |
| #83 `POST /v1/default/banks/{bank_id}/transfer/export` | ✅一致 | `api/http.py:9327`（`api_transfer_export`），异步 → `202` | `internal/transfer/submit_handlers.go` | 无 | `{}` → 两侧同 `202` + `{"operation_id","status"}`（同 #81 的键序与 pending 语义）；`operations` 列表两侧都出现 `export_bank` 类型 |
| #84 `POST /v1/default/banks/{bank_id}/transfer/import` | ✅一致 | `api/http.py:9411`（`api_transfer_import`，multipart，`file` 必填） | `internal/transfer/submit_handlers.go` | 无 | 空 body `{}` → 两侧同 `422` `[{"type":"missing","loc":["body","file"],"msg":"Field required","input":null}]`（**逐字节相同**） |
| #85 `POST /v1/default/banks/{bank_id}/clone` | ✅一致 | `api/http.py:9518`（`api_clone_bank`）；**`target_bank_id` 是 query 参数**（不是 body） | `internal/transfer/submit_handlers.go` | 无 | ① **body 里给 `target_bank_id`** → 两侧同 `422` `[{"type":"missing","loc":["query","target_bank_id"],"msg":"Field required","input":null}]`（**逐字节相同**，证实它读的是 query 不是 body，本服务同样如此）；<br>② **query 里给** → 两侧同 `202` + `{"operation_id","status":"pending"}`；<br>③ `operations` 列表两侧都出现 `clone_bank` 类型（参考已 `completed` / 本服务 `pending`，见上方夹具噪声）；<br>④ **操作详情 17 键键序两侧完全相同**：`operation_id, status, operation_type, id, task_type, mental_model_id, created_at, updated_at, completed_at, error_message, retry_count, next_retry_at, progress, result_metadata, details, child_operations, task_payload` |
| #86 `GET /v1/default/banks/{bank_id}/attachments/{attachment_id}` | ✅一致 | `api/http.py:9601`（`api_get_attachment`），二进制流；未命中 → 404 `Attachment not found` | `internal/transfer/binary_handlers.go:57` 邻区（`ServeAttachment`） | 无 | ① **不存在的 UUID** → 两侧同 `404 {"detail":"Attachment not found"}`；<br>② **非 UUID 字符串** `nope` → 两侧同 `404 {"detail":"Attachment not found"}`（**不是 422**，即 id 不按 UUID 校验，与参考一致） |
| #87 `GET /v1/default/files/download/{key:path}` | ✅一致 | `api/http.py:9655-9712`：**多段 `{key:path}`** 通配；`key.split("/")`，**含 `..` 段 → 404 "File not found"**；`tenants/<schema>/banks/<bank_id>/…`（`len>4` 且 `parts[0]=="tenants"`、`parts[2]=="banks"`、`parts[3]` 非空）或旧式 `banks/<bank_id>/…`（`len>2`）才解析出 bank，否则 404；响应 `application/zip` + `Content-Disposition: attachment; filename="<bank_id>-documents.zip"` | `internal/transfer/binary_handlers.go:57`（`ServeDownload`，`strings.TrimPrefix(r.URL.Path, feedPathPrefix)` 取**原始 key**）、`internal/storage` 的 `ValidateStoredFileKey`/`BankIDFromStoredFileKey`；路由 `internal/httpx/api.go:130` `PrefixFilesDownload`，按 `{key...}` 多段通配挂载 | 无 | **组长点名的"多段 key 是否已支持"——已支持，实测等价**：<br>① `banks/b1/x.zip` → 两侧同 `404 {"detail":"File not found"}`；<br>② `tenants/s1/banks/b2/x.zip` → 两侧同 404；<br>③ `nope/x.zip`（不匹配任一布局）→ 两侧同 404；<br>④ `banks/../etc/passwd`（`--path-as-is` 保留字面 `..`）→ 两侧同 404；<br>⑤ `//banks/b1/x.zip`（双斜杠空段）→ 两侧同 404；<br>⑥ `tenants//banks/b2/x.zip`（空 schema 段）→ 两侧同 404；<br>⑦ 单段 `single` → 两侧同 404；<br>全部 `content-type: application/json`，证明**多段路径确实到达了 handler**（否则会是路由层 404 且无 JSON 体），且"key 不可解析/文件不存在"统一收敛为同一个 `File not found` |

**transfer 小结：#78–#87 共 10 条全部 ✅一致，零处代码改动。**

**证据强度说明**：
- 涉及二进制的行（#78/#79）做了**归一化后逐字节相等**比对（时间戳/UUID 抹掉），且 `content-length` 两侧同为 `1909`。
- 涉及异步提交的行（#81/#83/#85）响应体是 `operation_id`（天然不同），因此结论建立在**状态码 + 键集 + 键序 + `status` 取值 + `operations` 列表里出现的操作类型**上，并额外比对了**操作详情 17 键的键序**。
- 未做的一处：`#82`/`#84` 的**成功路径**（真正上传 multipart file）未跑 —— 它们与 #78/#79 共用同一套 archive 应用逻辑（已由 #78 的往返证明），且上传成功路径只改变 `operation_ids`/计数字段，不值得再消耗一次模型调用。若组长要求补，我可以在下一轮补测。



**结论：✅一致（键集/排序/分页/错误路径）；⚠️夹具差异（总数与顺序，同 #60/#62）。**

| 项 | 参考 | 本服务 |
|---|---|---|
| 参数 | `limit: Query(100, ge=0)`、`offset: Query(0, ge=0)`、`tags`、`tags_match: TagsMatch=Query("any")`、`tag_groups` | `internal/readsurface/handlers.go:456-460`；`entity_params.go:44-100` 参数校验前移到 `requireBank` 之前、按声明序一次收集 |
| 排序 | 无标签过滤：`ORDER BY mention_count DESC, last_seen DESC, id ASC`（`memories/pg/curation.py:496-505`）；**有**标签过滤：`ORDER BY s.mention_count DESC, s.last_seen DESC, e.id ASC`（`:643-651`）——两者同表达式，仅数据源不同 | `internal/storage/postgres_read_surface.go:421` `orderExpr := mentionExpr + " DESC, " + lastExpr + " DESC, e.id ASC"`，标签分支把三列换成 `s.*` 重算——**与参考逐字对应** |
| 重算语义 | 带标签过滤时 `mention_count / first_seen / last_seen` 由匹配记忆重算，无匹配记忆的实体不出行（`:587-612`） | `:396-405` 同（`visibleEntityMentionsSQL` 聚合） |
| item 键 | `canonical_name/first_seen/id/last_seen/mention_count/metadata`（6 键） | 实测同 6 键 |
| 信封 | `{items,total,limit,offset}` | 同 |

**实测（同名 bank）**：`?limit=0`、`?limit=-1`(422)、`?limit=abc`(422)、`?offset=-1`(422)、
`?tags_match=bogus`(422 `literal_error` 五个候选值同)、`?tag_groups=bogus`(400 **逐字节同**)、
`?tags=a&tag_groups=[]`(400)、`?limit=-1&tags_match=bogus`(422 单信封两项)、`?limit=1&offset=1`(只取第 2 条)、
`?limit=3&offset=20`(空页但 `total` 照报)、`?tag_groups=[]`(200) → **形态/状态码/错误正文全部一致**。

**⚠️ 夹具差异**：`total` 参考 13 / 本服务 12（参考多抽出实体"柏林团队"）；并列 `mention_count` 的
tie-break 结果不同（参考 `Acme` 先于 `东京`，本服务相反）——因为 tie-break 的 `last_seen` 与 `id`
两侧本来就不同（`id` 是随机 UUID，`last_seen` 差在 event_date）。**排序表达式本身两侧逐字相同**，
顺序差异是夹具差异的必然结果，与 #60/#62 同源。组长裁决：接受为夹具差异。


**结论：✅一致。**

参考 handler 体（`:7095-7105`）**无条件** `raise HTTPException(410, "This endpoint is deprecated. …")`，
既不查 bank 也不查实体、不解析 id。本服务 `internal/readsurface/handlers.go:537-542` `RegenerateEntity`
同样无条件写 410。

实测（同名 bank，两侧各自取真实实体 id）：已知实体、全零 UUID（不存在）、`nope`（非 UUID）、
未知 bank + UUID、未知 bank + 非 UUID、带 `?tags_match=all` → **6/6 全部 `410` 且 detail 逐字节相同**：
`{"detail":"This endpoint is deprecated. Entity observations are no longer supported."}`。
`GET` 打到该路径两侧都是 404 `Entity <id> not found`（落到 `entities/{id}` 读路由），也 SAME。

### B7 `metadata.memory_ids` / `source_memory_ids` 补丁（B6 的待裁决项，本轮已修）

参考：operation 结束时把该 run 产出/消费的 memory_units 补到该 trace 的**每一行**
——`UPDATE {schema}.llm_requests SET metadata = metadata || $3::jsonb WHERE bank_id = $1 AND trace_id = $2`
（`engine/llm_trace.py:971-990`），由 retain（`engine/memory_engine.py:8026-8029`）与 consolidation
（`engine/consolidation/consolidator.py:1612-1618`）各调用一次；`_rows_written`（`:670-690`）保证没写过行的 trace
不会被 UPDATE（否则一次匹配 0 行的 UPDATE 只白占一个连接）；`PendingWrites.drain_all` 让关停等补丁写完（`:865-875`）。

| 项 | 参考 | 本服务（修复前） | 结论 |
|---|---|---|---|
| 补丁语义 | `metadata ‖ patch`，`memory_ids`=产出、`source_memory_ids`=消费，去重保序 | **无**：写入侧从不产生这两个键 | ❌已修 |
| 调用点 | retain + consolidation | **无** | ❌已修（两处） |
| 空 trace 守卫 | `_rows_written` 有界集（4096） | 无 | ❌已修 |
| 关停 drain | `flush_on_shutdown` + `PendingWrites.drain_all` | 无 | ❌已修 |

**修复落点**
- `internal/app/llm_trace.go`：`llmTraceRecorder` 增加 `rowsWritten *lruSet`（`traceRowsWrittenMax = 4096`）与
  `pending sync.WaitGroup`；新增 `attachMemoryIDs(ctx, created, source)`（`:729`）——读取 context 上的
  `provider.TraceContext`，把调用方给的和 attribution 里已有的 id 合并去重，写 `flush()` 后起 goroutine 调
  `store.PatchLLMRequestMetadata`；`closeTraceRecorder` 在 `flush()` 之后 `pending.Wait()`。
- `internal/storage/store.go`：接口新增 `PatchLLMRequestMetadata`（两个后端都实现，
  `postgres_observability.go:710` / `memory_observability.go`）。`ObservationUnitIDs` 在精确版上线后已删除（见下）。
- `internal/app/worker_retain.go:84`（调用）/`:99`（helper）：`applyRetain` 尾部把 `outcome.ItemUnitIDs` 摊平后补到本 run 的 trace。
- `internal/app/worker.go:829`：`applyConsolidation` 成功分支把 `result.ObservationIDs` 交给 recorder（精确版，见下）。
- 顺带修（否则补丁永远不生效）：`internal/storage/memory_observability.go` 的 `metadataHoldsID` 只认 `[]any`，
  而存储内写出的 `memory_ids` 是 `[]string`，两边形状不一致导致过滤恒空。

**证伪用例**
- `TestRetainTraceRowsCarryTheMemoryIDsItCreated`：retain 产出 units 后按 `MemoryID=` 能过滤到行；去掉补丁调用即空集。
- `TestAttachMemoryIDsSkipsATraceWithNoRows`：没写过行的 trace 不发 UPDATE。
- `TestCloseTraceRecorderDrainsPendingPatches`：关停时等在途补丁落地（去掉 `pending.Wait()` 即偶发丢失）。
- `TestConsolidationTraceRowsCarryTheExactObservationsWritten`：见下「consolidation 精确 id」。

验证（B7 全部完成后的终态）：`go build ./...` + `-tags pgx` BUILD-OK；
`go test ./internal/app ./internal/maintenance ./internal/storage ./internal/consolidation`
**1449 passed / 0 failed**；`go vet` OK；`gofmt -l` 空；`golangci-lint` 0 issues。

**consolidation 精确 id（已由近似改为精确）**：参考携带的是 pass 内**精确**收集的 id
（`record_created_memory_ids`）。本服务先按「最新 N 条 observation」近似，随后 obs-mental 在
`internal/consolidation.Result` 加了 `ObservationIDs []string`（create + update 写出、去重保序、多 scope 汇总），
`internal/app/worker.go:829` 改为直接消费它，近似用的 `Store.ObservationUnitIDs` 能力连同两个后端实现一并删除
（不再有调用点，避免留下无用代码）。
**证伪**：`TestConsolidationTraceRowsCarryTheExactObservationsWritten` —— bank 里先放一条别的写者写的 observation，
本轮只写出 `[obs-new, o1]`，断言该无关 observation **不在**本 run 的 trace 上、而写出的两个都在；
换成扫描近似即会把无关那条多报进来 → FAIL。


**结论：✅一致。27 组参数组合 + 5 组未知 bank 差分全部 SAME（零 DIFF）。**

| 项 | 参考 | 本服务 |
|---|---|---|
| 参数声明序 | `q` → `tags` → `tags_match`(默认 `any_strict`) → `time_field` → `start_date` → `end_date` → `limit`(100,ge=0) → `offset`(0,ge=0)；`q`/`tags`/`time_field` 是 `Query(...)`，`tags_match` 是 **Literal** `Query("any_strict")`；`start_date`/`end_date` 是裸 `str`，在 handler 体内 `_parse_iso_datetime` 后失败为 **400 `ValueError`** | `internal/readsurface/documents.go:112-186`；`params.go:192-260` `queryErrors` 一次收集；日期两个仍在 handler 内解析 |
| 排序 | 默认 `updated_at DESC, created_at DESC, id`；给 `time_field` 时改为 `<column> DESC, id`（`engine/time_filter.py:139`，**丢掉 created_at 次级键**） | `internal/storage/postgres_read_surface.go:146`/`:150-165` 同 |
| 信封/item 键 | `{items,total,limit,offset,total_is_estimate}`；item 11 键含 `matched_fields` | 实测同（`items,total,limit,offset,total_is_estimate`；item 键完全相同且**键序相同**） |
| `q` 语义 | 大小写不敏感子串，匹配 id 或原文；`matched_fields` 报告命中字段（`id`/`original_text`） | 实测 `q=note`/`q=NOTE`/`q=Acme`/`q=zzz` 两侧 `matched_fields` 与结果集一致 |

**实测（同名 bank）**：`limit=0/-1/abc`、`offset=-1`、`tags_match=bogus`、`time_field=bogus/created_at/updated_at`、
`start_date=bogus`(400)、`end_date=bogus`(400)、`q=note`/`NOTE`/`Acme`/`zzz`、`tags=env:prod`(+`all_strict`)、
`tags=NO-SUCH`/`ENV:PROD`、组合 `limit=-1&offset=-1`、`limit=abc&offset=-1&tags_match=bogus`（单信封三条）、
`q=&tags=`、`tags_match=all`、`limit=1&offset=1`、`offset=5`、时间窗 `2000-01-01..2000-01-02`（有/无 `time_field`）
→ **27/27 SAME**（时间戳归一化后逐字节相同）。
**未知 bank**：`?limit=-1`(422)、`?limit=abc`(422)、`?time_field=bogus`(422)、`?start_date=bogus`(400)、`?end_date=bogus`(400)
→ **5/5 SAME**——即参数校验压在 404 之前，与参考一致。



**结论：✅一致。**

参考 `limit: Query(100, ge=1, le=1000)`、`offset: Query(0, ge=0)`，均在 handler 之前校验；
`result is None` → 404 `Document not found`；信封 `{items,total,limit,offset}`；
chunk 行 `chunk_id/document_id/bank_id/chunk_index/chunk_text/created_at`，逐块附 `attachments`。
本服务 `internal/readsurface/documents.go:395-445`，`internal/storage/postgres_read_surface.go:280` `ORDER BY document_id, chunk_index`（单文档过滤下等价）。

**实测（文档 id 含 `/`：`folder/doc.md`）**：无参数、`?limit=0`/`?limit=1001`/`?limit=abc`/`?offset=-1`（**422**）、
`?limit=1000`、`?offset=1` → **7/7 SAME**。信封与 item 键集在两处都完全一致。


**结论：✅一致（含 `:path` 斜杠语义）。**

参考非 bank 作用域：`chunk` 为假 → 404 `Chunk not found`；用 chunk 自带的 `bank_id` 校附件；
`chunks.chunk_id` 是 **text**（形如 `<bank>_<doc>_<index>`），不是 uuid，故**不适用**"非 UUID → 驱动 500"。
本服务 `internal/readsurface/documents.go:484-512`（`GetChunk`），路由注册为
`internal/httpx/api.go:61` 的 `/v1/default/chunks/{chunk_id...}`（Go 多段通配符，**返回未清洗的 remainder**）。

**实测**：真实 chunk id `audit-read-zx0nzu_folder/doc.md_0`（**id 本身含 `/`**）→ 两侧 **200 且载荷等价**，
键集 7 键相同（`attachments/chunk_id/document_id/bank_id/chunk_index/chunk_text/created_at`）；
`%2F` 编码形式同样 200 等价；`nope`、`a/b`（多段但不存在）、`<bank>_folder/doc.md_9`（index 越界）、
`/v1/default/chunks/`（空 id）→ 两侧 **404 `Chunk not found`** 逐字节相同。

## 第五轮 knowledge

> 单元 `knowledge`（#47–#54）。参考源码 `api/http.py:7502-7527`（tree）、`:7529-7559`（create folder）、
> `:7561-7612`（create page）、`:7614-7655`（export）、`:7657-7690`（search）、`:7692-7718`（get page）、
> `:7720-7787`（patch node）、`:7789-7812`（delete node）；模型与投影 `api/http.py:3616-3726`
> （`KnowledgeNode` / `KnowledgeTreeResponse` / `CreateFolderRequest` / `CreatePageRequest` /
> `UpdateNodeRequest`）、`:3727-3733`（`CreateKnowledgePageResponse`）、`:3735-3760`（`KnowledgePageResponse`）、
> `:3762-3805`（bundle / search 模型）、`:3807-3824`（`_knowledge_node_model`）、
> `:3826-3847`（`_knowledge_tag_filter`）、`:3849-3861`（`_build_knowledge_tree`）、
> `:3863-3877`（`_knowledge_page_response`）。引擎 `engine/memory_engine.py:20073-20085`
> （`_kp_assert_folder_parent`）、`:20108-20157`（`create_knowledge_folder`）、`:20159-20315`
> （`create_knowledge_page`）、`:20317-20392`（`list_knowledge_nodes`）、`:20394-20447`（`get_knowledge_page`）、
> `:20449-20719`（`search_knowledge_pages`）、`:20721-20895`（`update_knowledge_node`）、
> `:20897-20982`（`delete_knowledge_node`）、`:20984-21060+`（`export_knowledge_base`）。
> 本服务落点 `internal/reporting/knowledge.go`、`knowledge_models.go`、`knowledge_projection.go`、
> `knowledge_nodes.go`、`knowledge_body.go`、`knowledge_render.go`、`knowledge_search.go`、
> `knowledge_tags.go`、`knowledge_tag_groups.go`、`knowledge_schema.go`。
>
> 环境：本服务 `18213`（同前一轮的标准参数，`WENYUAN_API_DATABASE_SCHEMA=audit_memops`，
> `WENYUAN_API_WORKER_ENABLED=false`），参考 `http://127.0.0.1:8888`。
> 差分：自写 urllib 直连 + 掩时间戳/UUID 后**逐字节**比对（键序可见）。
> 夹具：`audit-kb-fiaizf`（本服务侧建在自己的 `audit_memops` schema；参考侧只建自己的 bank）。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #47 `GET .../knowledge-base/tree` | ❌已修（`trigger` 键序） | `api/http.py:7521-7527` → `_build_knowledge_tree`（`:3849-3861`）：扁平列表按 `parent_id` 装配成 `roots`，父不在集合里（或为 null）即根，**节点顺序即引擎返回序**（扁平列表的 `append` 序，父先于子）。`_knowledge_node_model`（`:3807-3824`）：`id,kind,name,parent_id,mental_model_id,managed,description,tags,timestamp,is_stale,last_refresh_failed_at,trigger`（+ `children`），页取 `source_query`→`description`、`last_refreshed_at`→`timestamp`、`is_stale`（仅页）、`trigger`（仅页）。`trigger` 是 `MentalModelTrigger`，**框架按声明序序列化 18 个成员** | `internal/reporting/knowledge.go:28-82`（`KnowledgeTree`）、`knowledge_projection.go:149-180`（`projectKnowledgeNode`）、`:170-177`（trigger 投影）、`knowledge_render.go`（`nestKnowledge`）、`knowledge_models.go:16-30`（`KnowledgeNode` 14 键声明序） | **见下「修复 1」** | 线上差分（`audit-kb-fiaizf`，含 1 个 folder + 1 个 page）：空树 `{"roots":[]}` SAME；有内容树的两侧 `roots` 结构、14 个节点键及其顺序、`children` 嵌套、`is_stale:false`、`timestamp`/`description`/`tags`**逐字节一致**。**唯一差异是 `trigger` 的键序**（修复前：本服务按 Go 字典序 `budget,exclude_mental_model_ids,exclude_mental_models,…`；参考 `budget,mode,refresh_after_consolidation,refresh_cron,…`）；键集与全部取值本来就一致。错误路径：未知 bank → 404 SAME、`tags_match=bogus` → 422 SAME |
| #48 `POST .../knowledge-base/folders` | ✅一致 | `api/http.py:7537-7559`（`status_code=201`，`ValueError`→400）；`engine/memory_engine.py:20136-20157`：`folder_id = f"kf-{uuid4().hex}"`（`kf-` + **32 hex，无连字符**）；`_kp_assert_folder_parent`（`:20073-20085`）：`ValueError(f"Parent folder '{parent_id}' not found")` / `ValueError(f"Parent '{parent_id}' is not a folder")`；`_ensure_bank_exists` 让**未知 bank 也能建**（201），`managed` 默认 False | `internal/reporting/knowledge.go:85-152`（`CreateKnowledgeFolder` → `createKnowledgeNode`）、`:224`（`knowledgeFolderID()` = `"kf-"+newID()`）、`knowledge_nodes.go`（`assertKnowledgeFolderParent`）、`knowledge_models.go:31-40`（`CreateKnowledgeFolderRequest` 只声明 `name`/`parent_id`） | 无 | 线上：建在根 → **201** 且响应体 14 键逐字节一致（`kind:"folder"`、`mental_model_id:null`、`description:null`、`tags:[]`、`is_stale:null`、`last_refresh_failed_at:null`、`trigger:null`、`children:[]`），仅 `id`/`timestamp` 不同（掩码范围内）；建在**未知 bank** → 两侧同 **201**（`_ensure_bank_exists` 语义，非 404）SAME；`{}`→422（`missing` 两个 `loc` 项）SAME；`{"name":1}`→422 SAME；`parent_id` 指向不存在的 folder → **400** SAME |
| #49 `POST .../knowledge-base/pages` | ✅一致 | `api/http.py:7569-7612`（201；`node is None` → **409** `"A page named '<name>' already exists in this folder"`）；`engine/memory_engine.py:20159-20315`：`page_id = f"kp-{uuid4().hex}"`、`mental_model_id = f"mm-{uuid4().hex}"`，`max_tokens` 缺省走 `KNOWLEDGE_PAGE_DEFAULT_MAX_TOKENS`，`trigger` 按 内置默认 ← 银行 `knowledge_page_default_trigger` ← 请求 三层合并，`_kp_assert_folder_parent` 同样校验 | `internal/reporting/knowledge.go:89-180`（`CreateKnowledgePage` → `createKnowledgePageNode`）、`knowledge_nodes.go`（`knowledgePageTrigger`、`rejectDuplicatePage`）、`knowledge_models.go:42-67` | 无 | 线上：建页 → **201**，体 `{"page_id","mental_model_id","operation_id"}` 三键及顺序一致（`kp-`/`mm-` 前缀与 32 hex 形状一致），SAME；`{}`→422（**两个** `missing` 项，`loc:["body","name"]` 在前、`["body","source_query"]` 在后，`input:{}`）SAME；只给 `name`→422（单个 `missing`，`input:{"name":"Q"}`）SAME；`trigger.refresh_cron:5`→422 `string_type` SAME；父 folder 不存在 → 400 SAME |
| #50 `GET .../knowledge-base/export` | ⚠️存疑（字段内容未逐字节核，见下） | `api/http.py:7622-7655`：响应体是**JSON** `{"files":[{"path","content"}]}`（`KnowledgePageBundleResponse`），**不是** 附件下载 —— 没有 `Content-Disposition`、没有 zip；`files[0]` 恒为 `page_markdown.INDEX_FILENAME` 的 `render_index`，随后每个页 `<id>.md`，该页**有 history 时**再加 `<id>` 日志文件 | `internal/reporting/knowledge_body.go`、`knowledge_render.go` | 无（未发现差异） | 线上：两侧均 **200 + `application/json`**，键集 `{"files"}` 与元素键 `{"path","content"}` 一致；**但两侧文件内容不可能相同**（页的不同 bank、不同 id、且本服务 worker 关闭因此页 body 为空 —— 参考侧 worker 在跑会写出内容），因此本行只核到 **形状与 Content-Type**，`render_document`/`render_index`/`render_log` 的逐字节文本**未**作差分，以包内渲染用例为证据。**未覆盖原因**：导出内容依赖页的真实 body，需开 worker 让参考与本服务各自把同一 set 的页面生成出来才能比；本轮未做 |
| #51 `GET .../knowledge-base/search` | ✅一致（错误路径 + 包络） | `api/http.py:7663-7670`：`q` 必填且 `min_length=1`、`limit` 默认 10 `ge=1 le=50`；`_knowledge_tag_filter` 提供 `tags`/`tags_match`/`tag_groups`；响应 `{"results":[KnowledgePageSearchResult…],"total":len(results)}`，`KnowledgePageSearchResult` 7 键（`id,name,mental_model_id,source_query,snippet,score,updated_at`） | `internal/reporting/knowledge_search.go`、`knowledge_models.go:70-96`（`KnowledgeSearchResult` 7 键、`httpx.PythonFloat` 渲染 `score`） | 无 | 线上：缺 `q` → 两侧 422 SAME；`q=`（空串，`min_length` 违例）→ 两侧 422 SAME；`limit=0`/`limit=51`/`limit=abc` → 两侧 422 SAME；未知 bank → 404 SAME；`tags_match=bogus` → 422 SAME。**未覆盖**：非空结果集的 `score` 数值与排序（BM25+vector 融合，依赖两侧各自的嵌入与索引状态，无法用同一夹具对齐） |
| #52 `GET .../knowledge-base/pages/{page_id}` | ✅一致 | `api/http.py:7698-7718`：查不到 → **404** `"Knowledge page '<page_id>' not found"`；`_knowledge_page_response`（`:3863-3877`）8 键 `id,name,type,description,tags,timestamp,body,markdown`，`type` 由 `type:<x>` 标签推出（否则 `knowledge-page`），`timestamp` 取 `last_refreshed_at` 回落 `created_at`，`markdown` 恒为完整文档（frontmatter + body；空 body 时写 "No content yet."，而 `body` 仍为空） | `internal/reporting/knowledge_render.go`、`knowledge_body.go`、`knowledge_models.go:69-83`（`KnowledgePageResponse` 8 键） | 无 | 线上：未知 page_id → 两侧 404 且 `detail` 逐字节一致；已建页 → 两侧 200、8 键及顺序一致、`type:"knowledge-page"`、`description` 等于 `source_query`、`tags` 一致；`markdown` 的 frontmatter 形状一致（本服务 `body` 为 null 时 `markdown` 走空页提示，参考同） |
| #53 `PATCH .../knowledge-base/nodes/{node_id}` | ✅一致 | `api/http.py:7726-7787`：`parent_id` **只有出现在 `model_fields_set` 才生效**（`KEEP_PARENT` 哨兵），故 `{"parent_id":null}` 是**移到根**、缺省是**不动**；页选项（`source_query`/`tags`/`max_tokens`/`trigger`）在 backing mental model 上，`null` 表示不改；`updated is None` → **404** `"Knowledge node '<node_id>' not found"`；`ValueError`→400；改 `source_query` 且返回带 `mental_model_id` 时**提交一次异步 refresh**；`trigger` 走 `model_dump(exclude_unset=True)` 的**补丁**语义 | `internal/reporting/knowledge_nodes.go:260-362`（`UpdateKnowledgeNode`、`UpdateKnowledgeNodeParams`、`pending.Trigger`）、`knowledge_models.go:118-125`（`UpdateKnowledgeNodeRequest`，`parent_id`/`trigger` 用 `json.RawMessage` 就是为做**存在性判定**） | 无 | 线上逐项实测（两侧同一操作序列）：<br>①未知 node → 两侧 **404**，`detail` 逐字节一致（`Knowledge node '<id>' not found`）；<br>②空体 `{}` → 两侧 **400** 且文案**逐字节相同**：`"Provide name, parent_id, source_query, tags, max_tokens, and/or trigger to update"`；<br>③`{"parent_id":<自己>}`（循环嵌套）→ 两侧 **400** 且文案**逐字节相同**：`"A node cannot be its own parent"`；<br>④`{"parent_id":null}` 移到根 → 两侧 **200**，随后两侧 tree 结构一致；<br>⑤`{"name":"SubX"}` 重命名 → 两侧 **200**；<br>⑥`{"trigger":{"refresh_cron":"0 * * * *"}}` 补丁 → 两侧 200，树里 `trigger` 的 18 键序与取值一致（依赖修复 1） |
| #54 `DELETE .../knowledge-base/nodes/{node_id}` | ✅一致 | `api/http.py:7797-7812`：`deleted` 为假 → **404** `"Knowledge node '<node_id>' not found"`；成功 → **200 `{"status":"deleted"}`**；删除递归到整个子树，且**子树里页的 backing mental model 一并删除** | `internal/reporting/knowledge.go:182-202`（`DeleteKnowledgeNode`：先 `GetKnowledgeNode` 判 404，再 `subtreeKnowledgeModels` 收集子树模型，`DeleteKnowledgeNode` 后逐个 `DeleteMentalModel`） | 无 | 线上：未知 node → 两侧 404 且 `detail` 一致；删一个 folder（含其下 page）→ 两侧 200 `{"status":"deleted"}`，随后两侧 tree 的 `roots` 都只剩文件夹外的节点且结构一致；重复删同一 node → 两侧同 404 |

**修复 1（#47，连带 #53 的 trigger 回显）：tree 里 page 的 `trigger` 是 Go 字典序，参考是 18 键声明序**

- **现象（线上原始字节）**：参考
  `"trigger":{"budget":null,"mode":"delta","refresh_after_consolidation":true,"refresh_cron":null,...,"keep_trace":false}`；
  本服务修复前 `"trigger":{"budget":null,"exclude_mental_model_ids":null,"exclude_mental_models":true,"fact_types":["observation"],...}`。
- **归因**：`knowledge_projection.go` 用 `mentalmodels.NormalizeTrigger(...)` 的结果直接赋给
  `KnowledgeNode.Trigger map[string]any`。`NormalizeTrigger` 返回的是**归一化后的 map**（它只保证成员集合与取值，
  注释里也写明"顺序由调用方的有序类型负责"），Go 的 `encoding/json` 对 map 排序输出，于是键序丢失。
  `internal/mentalmodels` 里**本来就有**按声明序序列化的 `OrderedTriggerMember` / `Trigger` 类型
  （`mentalmodels/trigger.go:103-218`，注释明确说 18 个键必须按模型声明序上线）。
- **改法**：`KnowledgeNode.Trigger` 由 `map[string]any` 改为 `mentalmodels.Trigger`；新增
  `orderedKnowledgeTrigger`（`knowledge_projection.go`）把归一化后的 map 编一次再反序列化成有序的
  `Trigger`，因此**归一化规则一字未改**，只把键序补回来。trigger 为 nil 时保持 `null`。
- **回归用例**：`TestKnowledgeTreeTriggerKeepsTheReferenceMemberOrder`（按 `json.Decoder` 逐 token 取键序，
  断言完整 18 键有序列表 + 首两个成员是 `budget,mode`；把它投影回 map 即 FAIL）。
  另有两个既有用例（`TestKnowledgeTree*`）从 `Trigger["refresh_cron"]` 改为 `triggerMember(...)` 读取。
- **线上复测**：重启后重打两侧 tree，`trigger` 的**原始字节前缀完全一致**、
  18 键顺序与全部取值 `SAME`。

**⚠️ 存疑 / 未覆盖（逐条写清为什么）**

1. **#50 export 的 `render_document` / `render_index` / `render_log` 文本**：要在两侧得到**同一份页内容**
   才能比，而页内容由异步 refresh 写（本服务本轮 `WENYUAN_API_WORKER_ENABLED=false`，参考侧 worker 在跑），
   且页 id 与 bank 不同。本行只核到 **Content-Type（两侧都是 `application/json`，不是附件下载）**、
   包络与元素键集。**给组长的提示**：这一点和 checklist 里"export 的响应头（Content-Disposition）是契约"的
   预设不符 —— 参考实现里这个路由**没有** `Content-Disposition`，也没有 zip/附件，是纯 JSON 信封；
   若下游 SDK 按附件处理，那是 SDK 的问题而不是本服务缺东西。
2. **#51 非空结果集的 `score` 与排序**：BM25 + 向量 RRF 融合，依赖两侧各自的嵌入与索引内容，
   同一夹具无法对齐。已核到错误路径与包络。
3. **`is_stale` 的真值**：两侧都是 `false`（各自都没有"页读完之后又写入的记忆"）。`true` 分支未覆盖。

**本轮顺带核对（源码级，未单独开差分）**

- `_knowledge_tag_filter`（`api/http.py:3826-3847`）把 `tags`/`tags_match`/`tag_groups` 三个 query 参数
  组成同一个过滤器，且**允许 `tags` 与 `tag_groups` 并存（AND）**；本服务 `knowledge_tag_groups.go` +
  `knowledge_tags.go` 的实现与此一致（该处是第四轮已加固的区域，本轮只做回归确认）。
- 页选项里 `tags` 是**作用域过滤器**而非标签，`[]` 表示"清空并用整个 bank 重建"；本服务
  `UpdateKnowledgeNodeRequest.Tags *[]string` 与参考 `tags: list[str] | None` 的存在性判定一致
  （`nil` = 不改，`[]` = 清空）。


## 第五轮 webhooks

> 单元 `webhooks`（#88–#92）。参考源码 `api/http.py:10035`（`api_create_webhook`，201）、
> `:10096`（`api_list_webhooks`）、`:10159`（`api_delete_webhook`）、`:10190`
> （`api_update_webhook`）、`:10273`（`api_list_webhook_deliveries`）；`_parse_webhook_id`
> `:10028-10033`（uuid 解析失败 → **422** `Invalid webhook ID`，**不是**驱动 500）；
> `_validate_webhook_destination` `:10009-10025`（→ 400）；模型 `:5039-5155`；
> `webhooks/models.py:90-99`（`WebhookHttpConfig`）；`webhooks/url_guard.py`
> （`_parse_target`/`_validate_host_literal`/`_ip_is_blocked`/`legacyIPv4`）；
> 签名 `webhooks/manager.py:54-77`（`_sign_payload`/`_sign_payload_v2`）+
> `engine/memory_engine.py:5420-5460`（头组装顺序与自定义头优先级）。
>
> 本服务落点 `internal/webhooks/{handlers,urlguard,validate,models,global}.go`、
> `internal/app/worker.go:520-575`（投递与签名）。
>
> **环境**：参考 8888 + 本服务 18211（单租户副本，worker 关）；签名/投递另用
> **worker 开的副本 18244**（`WENYUAN_API_WEBHOOK_ALLOWED_HOSTS=127.0.0.1`）+ 本地接收器
> `127.0.0.1:18321` 抓真实投递。夹具 bank 两侧均已删。
> 差分工具 `.audit-cfg/{dd.py,wl.py,wh.py}`（`dd.py` 逐字节；`wl.py` 对列表掩 `id`/时间戳，
> 参考侧因我脚本重试多存了一条 fixture，故行数按 `<N>` 掩，只比结构与成员）。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #88 `POST .../webhooks` | ✅一致 | `api/http.py:10035-10093`：**201**；先 `_validate_webhook_destination(request.url)` → 400，再 `create_webhook`；响应 `WebhookResponse` 的 `secret` **恒为 null**（注释 "Never return secret in responses"）、`http_config` 缺省 `WebhookHttpConfig()`（`method=POST, timeout_seconds=30, headers={}, params={}`）；`event_types` 默认 `["consolidation.completed"]` | `internal/webhooks/handlers.go:84-103`（`Create`：scope 拒 → bank → `validateCreateBody` → `Guard.Validate` → `EnsureBank` → `PutWebhook` → **201**）、`models.go`（`toWebhook` 置 `secret: nil`）、`validate.go:52...`（`validateCreateBody`） | 无 | **URL 校验 19 例全同码**：`https://example.com/hook`(201)、`http://`(201)、`ftp://`(400)、`no-scheme`(400)、`/relative`(400)、`https://`(400)、`https://127.0.0.1/h`(400 私有)、`https://localhost/h`(**201**，DNS 名登记时放行、投递时才解析)、`https://10.0.0.1`(400)、`https://169.254.169.254`(400 云元数据)、`https://[::1]`(400)、`https://0.0.0.0`(400)、`https://2130706433`(400 十进制 IPv4)、`https://127.1`(400 短式)、`https://100.64.0.1`(400 CGNAT)、`""`(400 `got 'empty'`)、端口 `99999`(400)、端口 `abc`(400)。**创建体 12 例逐字节 SAME**（`{}`/`url:null`/`url:1`/`secret:1`/`event_types:"x"`/`event_types:[1]`/`enabled:"z"`/`enabled:null`/`http_config:null`/`http_config.method:5`/`timeout_seconds:"x"`/`headers:{a:1}`）。成功体 9 键同序同值 |
| #89 `GET .../webhooks` | ✅一致 | `api/http.py:10096-10157`：`limit: Query(100, ge=0, le=1000)`（**ge=0，与 directives 的 ge=1 不同**）、`offset: Query(0, ge=0)`；响应 `WebhookListResponse(items, total, limit, offset)`，`total` 忽略分页 | `internal/webhooks/handlers.go:40-68`（`List`）、`httpx.QueryParams(h.List,"limit","offset")` | 无 | **11/11 SAME**（`wl.py` 掩 id/时间戳后比成员与键序）：无参、`limit=100/0/1000/1001/-1/abc`、`offset=0/99`、`?bogus=1`、`limit=3&offset=2`。`limit=1001`/`-1`/`abc` 两侧同 422（**注意 `limit=-1` 是 422 而非 200——`ge=0`**）；`limit=0` 两侧同 `{"items":[],"total":N,"limit":0,"offset":0}`（`total` 不受 limit 影响） |
| #90 `DELETE .../webhooks/{id}` | ✅一致 | `api/http.py:10159-10188`：`DeleteResponse(success=True)`；未删到 → 404 `Webhook not found`；`_parse_webhook_id` 先解析 → 非 uuid 是 **422** | `internal/webhooks/handlers.go:181-194`（`Delete`）、`webhookIDParam`（`canonicalWebhookID` → 422 `Invalid webhook ID`） | 无 | `200 {"success":true,...}` 同体；重复删两侧同 404 同文案。**uuid 形态 5 例全同**：`abc`→两侧 422 `Invalid webhook ID`、`-`→422、37 位 hex→422、全零 uuid→404 `Webhook not found`、`urn:uuid:...`→**404**（`uuid.UUID()` 接受 urn 形式，两侧同） |
| #91 `PATCH .../webhooks/{id}` | ✅一致（4 处 500 属已记录的「不复刻参考自身异常」） | `api/http.py:10190-10270`：`request.model_fields_set` 决定**哪些字段进 SET**（**this is the key difference from directives: 显式 null 仍进 SET**），`http_config` 走 `model_dump_json()`、其余直绑；无字段 → **422 `No fields provided to update`**；`url` 字段只要**在 fields_set 里**就重跑 `_validate_webhook_destination`；未命中的 id → 404 | `internal/webhooks/handlers.go:106-178`（`Update`）、`validate.go:26-50`（`updateInput`：`clearSecret`/`nullColumn`/`nullHTTPConfig`） | 无 | **11/15 逐字节 SAME**：`{"enabled":false}`、`{"url":...}`、`{"event_types":[]}`、`{"secret":null}`（= 清除，两侧响应 `secret` 仍 null）、`{"secret":"s2"}`、`{"http_config":{"method":"GET","timeout_seconds":5}}`、`{}`→**422 `No fields provided to update`**、`{"url":"https://127.0.0.1/x"}`→**400**、`{"event_types":["bogus.event"]}`→**200**（参考不校验事件类型）、`{"http_config":{"method":"PUT"}}`→200、`{"http_config":{"timeout_seconds":"x"}}`→422。<br>**4 处 500 已复核并维持现状**（详见下） |
| #92 `GET .../webhooks/{id}/deliveries` | ✅一致 | `api/http.py:10273-10325`：`limit: Query(50, le=200)`（**无 ge 下界**）、`cursor: str \| None`；`has_more = len(rows) > limit`、`page = rows[:limit]`、`next_cursor = page[-1]["created_at"] if has_more else None`；未知 webhook → `LookupError` → 404 `Webhook not found`；`WebhookDeliveryResponse.from_async_operation_row` 13 字段，`attempts = retry_count + 1`，`last_response_body` 默认**隐藏**（`webhook_expose_response_body` 才放出）；`next_cursor` 直接是 `created_at` 的**原始 datetime**（`::text` 空格形式） | `internal/webhooks/handlers.go:206-259`（`ListDeliveries`，含 `limit<0` 两档特判）、`models.go`（`Delivery` 13 字段）、`httpx.FormatPostgresTimestamp` | 无 | **11/11 SAME**（空 webhook）：无参、`limit=50/0/200/201/-1/-2/abc`、`cursor=`、`cursor=2026-01-01`、`?bogus=1`。边界逐一对上：`limit=201`→422 `less_than_equal`、`limit=-1`→**200 `{"items":[],"next_cursor":null}`**（参考 `limit+1=0` 行）、`limit=-2`→**500**（负 LIMIT 由库拒）、`cursor=2026-01-01`→**500**（非法时间戳绑定）。<br>**有真实投递时**（本服务 18244 实际投递抓取）：13 字段**全部出现且键序同参考模型**，`attempts:1`、`last_response_status:200`、`last_response_body:null`（未开 expose）、`created_at` 为**空格分隔 `+00`** 形式、`next_cursor:null`（仅一页） |
| 签名头（冻结 wire 契约） | ✅一致 | `webhooks/manager.py:54-67`：`_sign_payload` = `"sha256=" + HMAC-SHA256(secret, raw_body).hex()`；`_sign_payload_v2` = `"t=<unix>,v1=" + HMAC-SHA256(secret, "<unix>." + raw_body).hex()`；`engine/memory_engine.py:5420-5460`：**自定义头先铺、服务控制的头后写**（自定义头永远覆盖不了 `X-Hindsight-Event` 与签名），`Content-Type` 可被覆盖；`X-Hindsight-Event-Id`/`X-Hindsight-Delivery-Id` **有值才发** | `internal/app/worker.go:534-560`（头组装与签名，顺序与注释都与参考同源） | 无 | **实际投递抓包 + 独立 Python 复算**（非只读源码）：<br>`X-Hindsight-Signature` = `sha256=202b029b…`，用 `HMAC(secret, body)` 复算 **逐字节相符**；<br>`X-Hub-Signature-256` 与上**完全相同**；<br>`X-Hindsight-Signature-V2` = `t=1793722481,v1=d01727…`，用 `HMAC(secret, "1793722481."+body)` 复算**相符**；<br>**负向对照**：换 `wrong-secret` 复算**不匹配**（证明确实用了 secret 而非常数）；<br>抓到的头集合 = `x-hindsight-delivery-id / x-hindsight-event / x-hindsight-event-id / x-hindsight-signature / x-hindsight-signature-v2 / x-hub-signature-256`，**五个冻结名逐字符为 `X-Hindsight-*`（无任何 WENYUAN 拼写）** |
| 投递事件体 | ✅一致 | `webhooks/models.py:76-82` `WebhookEvent`：`event/bank_id/operation_id/status/timestamp/data`（+`event_id`）；`model_dump_json(exclude_none=True)` → 声明序、None 成员**不出现** | `internal/app/worker.go` 的 `webhookEventBody`/`webhookEventDataOrder` | 无 | 抓包实测 body：`{"event_id":…,"event":"retain.completed","bank_id":…,"operation_id":…,"status":"completed","timestamp":"2026-11-03T16:14:40.617909Z","data":{"document_id":…,"memory_unit_count":1}}` —— 六成员 + `event_id` 在前，键序与声明序一致；`timestamp` 为 **`…Z` 六位微秒**（响应模型时间格式） |

### #91 的 4 处 500（复核后维持"不复刻参考自身异常"）

`PATCH` 的更新模型五个成员**全部可空**，且参考用 `model_fields_set` 判"是否提供"，所以**显式 null 会进 SET**：

| body | 参考行为 | 参考 detail | 本服务 |
|---|---|---|---|
| `{"enabled":null}` | `enabled` 进 SET → 绑 null → NOT NULL 约束违反 → **500** | 驱动原文 + `DETAIL:  Failing row contains (...)` | **500**，detail = `internal server error` |
| `{"event_types":null}` | 同上 | 驱动原文 | **500** + 同上 |
| `{"url":null}` | 同上 | 驱动原文 | **500** + 同上 |
| `{"http_config":null}` | `request.http_config.model_dump_json()` 在**建语句时**解引用 None → **500** | `'NoneType' object has no attribute 'model_dump_json'` | **500** + 同上 |

**判定**：**状态码 100% 一致（4/4 都是 500）**，只有 detail 文本不同。参考这三条是 PostgreSQL 驱动原文（含**整行被拒值的 dump**，会把 bank_id/url/secret 等**回显给调用方**），第四条是 Python `AttributeError` 原文。按 `docs/CONTRACT-AUDIT.md:214`「参考实现自身的异常（不复刻、已记录）」维持不复刻——**照抄会把内部行内容与 Python 类型名泄漏进 API 响应**。`internal/webhooks/handlers.go:143-158` 的注释已写明这一取舍。

**本单元结论计数**：#88–#92 **5/5 ✅一致，零代码改动**。

**与前几轮的差异**：第一轮「组 O」已判全 ✅，本轮**独立重取**并补了它没覆盖的面 ——
①`limit=-1`/`limit=-2`/`cursor=<非法时间戳>` 三个 deliveries 边界（第一轮只测了空响应形状）
②**真实投递抓包的签名复算**（第一轮也做了，本轮补了负向对照，证明不是常数签名）
③`limit=0` 时 `total` 不被 limit 影响
④`urn:uuid:` 形式的 id 被接受（404 而非 422）
⑤PATCH 4 处 null 的 500 逐条定位到参考的确切失败点（驱动约束/`AttributeError`）。

**投递时机说明**（checklist 要求核）：`retain.completed` 的投递在**本服务 worker 跑完该 operation 后**触发，
抓包时间戳（`:40.618` 建投递行、`:41.013` 完成）与 retain 提交（`:14:26` 后约 14s）相符；
`consolidation.completed` / `memory_defense.triggered` 的事件类型由 `event_types` 列表匹配决定，
参考侧 `fire_event` 按 `event_types` 过滤（`webhooks/manager.py:100+`），本服务同。**未逐一触发**这三种事件的
全部组合（需要跑完整 consolidation/defense 流程），以 `retain.completed` 一条端到端抓包 + 源码逐行对照为准。




**结论：❌已修（成员键序）。**

| 项 | 参考 | 本服务 |
|---|---|---|
| 信封成员**顺序** | `DocumentResponse`（`api/http.py:2798-2821`）声明序：`id, bank_id, original_text, content_hash, created_at, updated_at, memory_unit_count, **nodes_by_fact_type**, tags, document_metadata, retain_params, observation_scopes, attachments`——pydantic 按**声明序**序列化 | 修前 `tags/document_metadata/retain_params` 在 `nodes_by_fact_type` **之前**，键集相同但**键序不同** |
| `nodes_by_fact_type` 内层顺序 | dict 字面量（`memory_engine.py:11303-11307`）按 **world → experience → observation** 写出；Python dict 保序，JSON 就是这个序 | 修前是 Go map，JSON 序列化按**字典序**：`experience, observation, world` |
| `attachment_filenames` | 详情里 `pop("attachment_filenames")`，不出现在响应中 | 同（本服务不产出该字段） |
| 不存在 | `404 Document not found`，**未知 bank 也是同一个 404**（不做 bank 预检） | 同 |

**修复**（作用域 `internal/readsurface/`）：
- `internal/readsurface/models.go:70-78`：`DocumentResponse` 的成员按参考模型声明序重排，
  `NodesByFactType` 从 `map[string]int` 改为 `orderedFactTypeCounts`。
- `internal/readsurface/documents.go:237-262`：`orderedFactTypeCounts` = `[]httpx.OrderedMember`，
  带 `MarshalJSON`（转 `httpx.OrderedObject` 写出 world/experience/observation 三键固定序）与
  `UnmarshalJSON`（回读成同一结构）、`count` 读取器。复用 `internal/httpx` 已有的 `OrderedObject`，**无新依赖**。

**证伪**：新增 `internal/readsurface/document_detail_order_test.go`
`TestDocumentDetailServesTheReferenceMemberOrder`——把 `models.go` 的字段顺序改回修前（`tags/…/retain_params`
precede `nodes_by_fact_type`）时**必 FAIL**：
`member 7 = "tags", want "nodes_by_fact_type" (full order [id bank_id original_text content_hash created_at updated_at memory_unit_count tags document_metadata retain_params nodes_by_fact_type observation_scopes attachments])`。已实测证伪并复原。

**线上复验**（`audit-read-zx0nzu` 的 `folder/doc.md`）：修后两侧 `list(body)` **完全相等**；
`nodes_by_fact_type` 内层顺序两侧都是 `['world','experience','observation']`。
（修后二进制未能重启进 18217 复验——见下方"构建阻塞"，但该修复的契约由单测与 `list(body)` 的读取路径共同锁定。）


> 端口 18212（single-tenant，`WENYUAN_API_DATABASE_SCHEMA=audit_retain`，`WORKER_ENABLED=false`）。
> 参考库 `127.0.0.1:5432/hindsight`（schema `user_devin`），本服务库 `127.0.0.1:15432/wenyuan`（schema `audit_retain`）。
> 差分工具保留键序；掩码时间戳/UUID/`*_tokens`。夹具 bank `audit-retain-18212` / `audit-retain-probe2`。

#### 已确认为 ✅一致 的部分（HTTP 契约面）

| 子项 | 结论 | 验证（全部 `SAME`） |
|---|---|---|
| 同步收据（`items:[]`、`items_count`、`usage`、`operation_id/operation_ids:null`） | ✅一致 | `{"items":[],"async":false}` → 两侧 `usage` 全零、`items_count:0`、`operation_id:null` |
| 异步收据（`async:true` → 200 + `operation_id`，`usage:null`） | ✅一致 | `{"items":[],"async":true}` → `operation_id:null`；带 1 item → 有 `operation_id`、`usage:null` |
| 请求级校验（缺 `items`、`items:5`、`content` 空/纯空白） | ✅一致 | `{"async":false}` → `missing` + `input`=body；`{"items":5}` → `list_type`；`content:""` / `"  "` → `value_error` `content cannot be empty` |
| `update_mode='append'` 无 `document_id` | ✅一致 | 同步与异步**都** 400 `update_mode='append' requires a document_id` |
| `operation_id` + 多策略分组 | ✅一致 | 400 `operation_id requires all retain items to resolve to a single strategy` |
| `context` / `metadata` / `entities` / `tags` / `timestamp:"unset"` / `observation_scopes` / `document_tags` | ✅一致 | 各一例 200 收据同形 |
| `entities:[{}]` → `missing` at `entities.0.text` | ✅一致 | 422 逐字节同 |
| `tags:["a",1]` → `string_type` at `tags.1` | ✅一致 | 422 逐字节同 |
| `strategy:"bogus"`、`update_mode:"replace"` | ✅一致 | 200 收据同形 |

#### ⚠️ 待裁决：`chunks` 行是否由 retain 写入（本轮未定性）

**现象**：同样一次同步 retain，两侧 `documents` 行**完全一致**（`original_text`、`content_hash` 逐字节相同），
但本服务写了 1 条 `chunks` 行，参考**写 0 条**。

| 探针 | 参考 `chunks` | 本服务 `chunks` |
|---|---|---|
| 全新 `document_id` + 全新 bank（`audit-retain-probe2`） | **0** | 1 |
| 同 `document_id` 二次 retain（replace） | **0**（且首写也是 0） | 1（text 已更新为新正文） |
| 等待 8s 后复查 | 0（不是延迟写入） | 1 |

**已排除的原因**：
- 不是 per-bank 配置（换全新 bank 同样 0）；
- 不是 `store_document_text=false`（参考 `DEFAULT_STORE_DOCUMENT_TEXT = True`，`config.py:1878`；且 `documents.original_text` 本身被写入了）；
- 不是等待/延迟（8s 后仍为 0）；
- 不是「参考从不用 chunks 表」——参考库 `user_devin.chunks` 有 **3886 行**，全部来自既有的 `coding-agent::*` bank。

**参考证据（已读）**：写入路径 `engine/retain/chunk_storage.py:106-176` `store_chunks_batch` →
`engine/memories/pg/retain.py:504` `upsert_chunks` → `engine/db/ops_postgresql.py:118-155`
`bulk_upsert_chunks`（`INSERT ... ON CONFLICT (chunk_id) DO UPDATE`）。调用点只有两处：
`engine/retain/orchestrator.py:3403`（streaming 路径）与 `:4276`（delta 路径）。
本次探针的文档既非 delta 命中（正文全新）、也未观察到 streaming 分支写入。

**本服务落点**：`internal/storage/postgres_retain.go:423` `putPartChunks`（每次写入都插 chunks 行）。

**为什么本轮不定性**：这可能是参考部署**本轮新出现的回归**（既有 bank 的 chunks 是历史数据，
新写入不再产生 chunks），也可能是探针走了参考某种「不落 chunks」的分支。两者对客户端的
可观察影响相同（`GET .../documents/{id}/chunks` 与 recall 的 `include.chunks` 通道），
但**修与不修的方向相反**：若是回归，本服务现状才是对的；若是真实契约，本服务多写了行。
需要组长裁决：是否以「参考当前行为」为准把 chunks 写入去掉。

**建议的下一步证据**：在同一参考实例上对一个**既有** bank（如 `coding-agent::wenyuan`）做一次
新文档 retain，看它是否也为该文档写 chunks 行 —— 若旧 bank 也写 0 行，那就是参考本轮回归，
应记 `⚠️存疑（参考侧回归，不照抄）`；若旧 bank 写 1 行，则本服务需对齐。

#### 顺带发现（不属本单元，转交）

`GET .../documents/{id}` 的 `nodes_by_fact_type` **键序**两侧不同：
参考 `{"world":0,"experience":0,"observation":0}`，本服务 `{"experience":0,"observation":0,"world":0}`。
两处都是固定三键，但本服务用 `map[string]int`（`internal/readsurface/models.go:74`
`NodesByFactType map[string]int`，构造函数 `internal/readsurface/documents.go:237` `factTypeCounts`），
Go map 按字母序输出。按 checklist §2 判据 2（键序）这是差异，但该字段属 `read-surface` 单元，**不由我改**。


**结论：✅一致。17 组参数组合 + 3 组未知 bank 差分全部 SAME（零 DIFF）。**

| 项 | 参考 | 本服务 |
|---|---|---|
| 参数 | `q`（`*` 通配，大小写不敏感）、`source: Literal["memories","mental_models"]=Query("memories")`、`limit: Query(100, ge=0)`、`offset: Query(0, ge=0)`，三者是 `Query(...)`，**先于** bank 查询 | `internal/readsurface/handlers.go:111-166`（`source`→`limit`→`offset` 声明序，`queryErrors` 一次收集后再 `requireBank`） |
| `q` 语义 | `pattern.replace("*","%")` 后 `tag ILIKE $2`（`memory_engine.py:16066-16110`）——`*`→`%`，`%`/`_` 也走 ILIKE 语义，大小写不敏感 | `handlers.go:195-212` `compileTagPattern`（`*`/`%`→`.*`、`_`→`.`、锚定、大小写不敏感） |
| 排序 | `ORDER BY count DESC, tag ASC`（`memories/pg/reads.py:353`） | `postgres_read_surface.go:350` `GROUP BY tag ORDER BY COUNT(*) DESC, tag ASC` |
| `source=mental_models` | 走 `list_mental_model_tags`，同样 count 降序 / tag 升序 | `handlers.go:167-193` `mentalModelTagCounts` 同 |

**实测（同名 bank）**：无参数、`limit=0/-1/abc/1`、`offset=-1/1`、`source=mental_models/bogus`、
`q=env:*`、`q=*`、`q=ENV:*`、`q=team:*`、`q=no-such:*`、`q=_nv:*`（ILIKE 的 `_` 单字符）、
组合 `source=bogus&limit=-1` 与 `limit=-1&source=bogus`（**两种书写顺序**，验证错误项的声明序）
→ **17/17 SAME**。
**未知 bank**：`?limit=-1`(422)、`?source=bogus`(422) → 参数校验压在 404 之前；`?q=*`(404) → **3/3 SAME**。

### observations + mental-models 小结（15 条）

| 结论 | 条数 | 明细 |
|---|---|---|
| ✅一致 | 13 | #32 #33 #34 #35 #37、#38 #40 #41 #42 #43 #44 #45 #46 |
| ❌已修 | 1 | #39（`reflect_response` 成员序） |
| ⚠️存疑 | 1 | #36（**未改代码**，待组长裁决：是否补 bank 存在性检查） |

**本单元改动的代码（全部在写作用域内）**

1. `internal/mentalmodels/trigger.go:320-329` —— `reflectResponseOrder` 由 `text, outcome, based_on, reflect_skipped, mental_models` 改为 `text, outcome, based_on, mental_models, reflect_skipped`，对齐参考 `memory_engine.py:18439-18447` 的插入序（`reflect_skipped` 是在 `{text, based_on, mental_models}` 基础字典**之后**补上的）。新增 `internal/mentalmodels/reflect_response_order_test.go`（含证伪）。
2. `internal/consolidation/consolidator.go` —— `Result` 新增 `ObservationIDs []string` + `noteWritten`，在 create（`:876`）与 update（`:909`）处记录，`add`（`:495-505`）跨 scope 合并去重。**应 bg-maintenance 请求**，供 llm trace 的 `metadata.memory_ids` 使用。新增两个用例。

**未改动、但记录在案的三处差异（均判定为非缺陷）**

- `last_full_refreshed_at`：参考侧 worker 在线 → 创建后立刻 full 刷新盖章；本副本 `WENYUAN_API_WORKER_ENABLED=false` 停在 `null`。**以 `WENYUAN_API_WORKER_ENABLED=true` 重起同一副本后两侧整字节 SAME**，已证伪"行为差异"。
- `dry-run-refresh` 的 `duration_ms` / `recorded_at` 小数位：真实计时与时间格式，非契约。
- 随机 `mental_model_id`（`mm-<32hex>`）：`{"id":""}` 时两侧各自生成，天然不同。

**验证门结果（局部；`internal/app` 全量测试被别人的在途改动阻塞）**

```
go build ./...                                    OK
CGO_ENABLED=0 go build -tags pgx ./...            OK
go test -count=1 ./internal/consolidation/... ./internal/mentalmodels/...   249 passed
go vet   ./internal/consolidation/... ./internal/mentalmodels/...          OK
gofmt -l internal/consolidation internal/mentalmodels                      空
golangci-lint run ./internal/consolidation/... ./internal/mentalmodels/... 0 issues
```

**全量 `go test ./internal/app/...` 在本次收尾时未通过，原因是别人的在途改动**：
`internal/app/llm_trace_test.go:795` 调用了 `store.RetainObservation`，而 `storage.MemoryStore` 尚无该方法
（`internal/app/llm_trace_test.go` 在 `git status` 中为 `M`，非本单元改动）。本单元的 `internal/app` 相关
文件（`mental_model_refresh*.go` / `mental_model_dry_run*.go` / `consolidation*`）编译与用例本身无失败。

**环境留痕**：本单元副本用 `WENYUAN_API_TENANT_MODE=single` + `ApiKeyTenantExtension` + 参考 key +
`WENYUAN_API_DATABASE_SCHEMA=audit_obsmm` + `WENYUAN_API_SKIP_LLM_VERIFICATION=true` 起在 18216；
差分脚本 `.audit-obsmm/{diff.sh,norm.py,up.sh,keepalive.sh}`（`diff.sh` 内含 `set -f`，否则
`"0 3 * * *"` 这类 cron 会被 shell 展开成当前目录文件列表——本轮踩过一次）。
收尾：两个侧的所有 `audit-obsmm-*` bank 已删除，`audit_obsmm` schema 已 `DROP ... CASCADE`。


**结论：✅一致。**

| 项 | 参考 | 本服务 |
|---|---|---|
| 响应 | `ReprocessDocumentResponse{success: true, operation_id, items_count}`（`api/http.py:8201-8206`） | `internal/readsurface/documents.go:739-741` 同三键、同类型（bool/str/int） |
| 不存在 | 文档查不到 → `404 Document not found`；**未知 bank 也是同一个 404**（`memory_engine.py:14112-14114` 先取文档、不预检 bank） | `dump`: `documentMember` 不预检 bank；`GetDocument` 的 `ErrDocumentNotFound` → `404 Document not found` |
| 失败即不写 | 先 gate 再 re-extract（`memory_engine.py:14101-14118`） | `documents.go:726-733` 先 `GetDocument` + `requireDocumentWritable`，再调 `Reprocess` |
| 重放内容 | `update_mode="replace"`、`force_reextract=True`，`retain_params` 整体回放 | `internal/memoryops/reprocess_test.go` 覆盖，`applyRetainParams` 逐键回放 |

**实测（同名 bank `audit-read-zx0nzu`，`folder/doc.md` 含 `/`）**：
- `POST .../documents/nope/reprocess`、`.../documents/a/b/c/reprocess`（含斜杠 id）、
  未知 bank 的 `.../documents/nope/reprocess` → **3/3 `404 Document not found` 逐字节相同**；
- `GET` 打到该路径 → 两侧 404 落回 document 读路由，SAME；
- **真实 reprocess**（自有夹具文档 `folder/doc.md`）：两侧都 `200`，信封键集/键序/类型完全一致
  （`success`/`operation_id`/`items_count`，后两者都是 str/int）；reprocess 后两侧文档仍存在、
  `content_hash` 不变（`ba9eaeeb…`）、`memory_unit_count` 都是 2——副作用等价。

#### `chunks` 行问题的最终定性：`⚠️存疑（参考侧回归，不照抄）`

上面「待裁决」的 `chunks` 差异，本轮已定性为**参考实现自身在同步路径上的回归**，不是本服务的缺口。

**决定性证据（同一参考实例、同一 bank、同样的 content 结构，只改 `async`）**

| 探针 | 参考 `chunks` 行 | 本服务 `chunks` 行 |
|---|---|---|
| `POST .../coding-agent::wenyuan/memories`，`async:true`，新 `document_id` | **1** | 1 |
| `POST .../coding-agent::wenyuan/memories`，`async:false`，新 `document_id` | **0** | 1 |
| 同 bank，再换一个全新 `document_id`，`async:true` | **1**（可重复） | — |

三次探针的 `document_id` 各不相同、内容都不同，`documents` 行两侧每次都正常写入
（`original_text` 长度 54/55、`content_hash` 一致）。**唯一变量就是 `async`**：
参考在 `async:false` 下文档正文落库、`chunks` 行不落；`async:true` 下两者都落。

**为什么判定是「参考回归」而不是「本服务多写」**
1. 参考库 `user_devin.chunks` 有 3886 行历史数据，且 `coding-agent::*` 这些既有 bank 的
   文档**绝大多数有 chunks**（`coding-agent::reth` 703 条、`coding-agent::wenyuan` 670 条）——
   说明 `chunks` 行本来就是该写的；
2. 参考的写入代码只有一条路径 `store_chunks_batch`（`engine/retain/chunk_storage.py:106`），
   streaming（`orchestrator.py:3403`）与 delta（`:4276`）两个调用点**都在**同一条
   per-document 执行流里，同步/异步本不该分叉；
3. 同步路径在参考里走 `retain_batch_async(..., return_usage=True)`
   （`api/http.py:10520-10540`），异步走 `submit_async_retain` → worker → 同一个
   `retain_batch_async`；两者的 chunk 落库应当一致。实测不一致 = 参考的同步路径少写了一步。

**结论**：按 `docs/CONTRACT-AUDIT.md` §5「参考自身的异常不复刻」处理 —— **本服务保持现状**
（同步与异步都写 `chunks` 行），记为 `⚠️存疑（参考侧回归，不照抄）`。
若日后参考修好，两侧自然一致；若照抄参考的同步路径「不写 chunks」，
`GET .../documents/{id}/chunks` 与 recall 的 `include.chunks` 通道在同步 retain 后都会空。

**可观察影响面**（万一组长裁决要照抄）：`GET .../documents/{id}/chunks` 的 `total`
与 `include.chunks` 的条目数。本轮 20+ 个同步 retain 探针里，两侧收据全 `SAME`，
说明差异**只**在这两处读表面，不影响 retain 的 HTTP 契约。

**复现命令**
```sh
# 参考：async:true 写 chunks，async:false 不写
D1=$(date +%s); D2=$((D1+1))
curl -s -X POST "$REF/v1/default/banks/coding-agent::wenyuan/memories" \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d "{\"items\":[{\"content\":\"probe A\",\"document_id\":\"d-$D1\"}],\"async\":true}"
curl -s -X POST "$REF/v1/default/banks/coding-agent::wenyuan/memories" \
  -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d "{\"items\":[{\"content\":\"probe B\",\"document_id\":\"d-$D2\"}],\"async\":false}"
# 等 20s 后：d-$D1 有 1 条 chunks，d-$D2 有 0 条
```


**结论：✅一致。**

| 项 | 参考 | 本服务 |
|---|---|---|
| 两字段都没给 | handler 体首行 `422 "At least one field (tags, observation_scopes) must be provided"` | `internal/readsurface/documents.go:262-…` 同文案 |
| `tags` 类型错 | pydantic `422` 单条 `list_type`（`loc:["body","tags"]`，`input:"x"`） | 同（逐字节一致） |
| 未知字段 | 被忽略 → 落到"两字段都没给"的 422 | 同 |
| `tags` 语义 | **整体替换**不是合并，`[]` 清空 | 同 |
| 不存在 | `404 Document not found`；**未知 bank 也是同一个 404** | 同 |
| 不会改动的字段 | 只动 `tags`/`observation_scopes`，不改正文 | 同 |

**实测（同名 bank，自有夹具文档 `folder/doc.md` / `async/note.txt`）**：
`{}`(422 同文案)、`{"tags":null,"observation_scopes":null}`(422)、`{"tags":["env:prod"]}`(200)、
`{"tags":[]}`(200)、`{"tags":"x"}`(422 `list_type` 逐字节同)、`{"bogus":1}`(422 同"至少一个字段")、
`{"observation_scopes":"shared"}`(200)、`{"observation_scopes":{"a":1}}`(422)、
不存在文档 / 未知 bank / 含斜杠 id `a/b/c`（**3/3 `404 Document not found`**）→ **11/11 SAME**。
**副作用**：两侧都 PATCH `{"tags":["only:one"]}` 后回读，`tags` 都变为 `["only:one"]`（替换语义一致）。


**结论：✅一致。**

参考：`result["document_deleted"] == 0` → `404 Document not found`；否则
`DeleteDocumentResponse{success, message: "Document '<id>' and <n> associated memory units deleted successfully", document_id, memory_units_deleted}`。
本服务 `documents.go` 的 `DeleteDocument` 同四键同文案同序。

**实测**：
- `DELETE` 不存在文档、未知 bank、未知 bank + 真实存在的 id → **3/3 `404 Document not found`**（不预检 bank）；
- **真实删除**自有夹具文档 `evt/dated.md`：两侧都 `200`，**四键同序**，`message` 逐字节相同
  （`Document 'evt/dated.md' and 2 associated memory units deleted successfully`），`memory_units_deleted` 都是 2；
- 删除后两侧 `GET` 同一文档都 `404 Document not found`——副作用等价。

## 第五轮 reporting-audit

> 单元 `reporting-audit`（#93–#99 + #101）。参考源码 `api/http.py:6807-6863`（stats）、`:6865-6898`
> （health/llm）、`:6900-6938`（memories-timeseries）、`:10836-10875`（audit-logs）、
> `:10877-10908`（audit-logs/stats）、`:10910-10935`（**audit-logs/{log_id}**，本轮新增面）、
> `:10937-10999`（llm-requests）、`:11001-11038`（llm-requests/stats）；引擎
> `engine/memory_engine.py:16784`（`get_bank_stats`）、`:17017`（`get_memories_timeseries`）、
> `:14219`（`list_llm_requests`）、`:14362`（`llm_request_stats`）、`:14468-14534`（`list_audit_logs`）、
> `:14537-14575`（`get_audit_log`）、`:14577-14649`（`audit_log_stats`）、`:1007-1022`（`_audit_summary`）；
> 模型 `engine/audit.py:29-80`（`AuditLogSummary`/`AuditLogEntry`/`AuditLogListResponse`/`AuditLogStats*`）、
> `engine/llm_trace.py:475-566`（`LLMRequestEntry`/`List/Stats` 模型）、`api/http.py:3091-3230`
> （`BankStatsResponse`/`BankLlmHealthResponse`/`MemoriesTimeseriesResponse`）。
> 本服务落点 `internal/reporting/observability_reports.go`、`internal/reporting/handlers.go`
> 的 `Stats`/`Timeseries`/`HealthLLM` 段、`internal/audit/`、`internal/httpx/audit.go`。
>
> **环境（本轮与前几轮的关键差别，请复核者先读这段）**
> - 参考部署的**审计默认关闭**：`audit_log_enabled` 默认 false，且是 **env → tenant → bank 三层**可覆盖。
>   实测参考上**所有既有 bank 的 `/audit-logs` 都是 `total:0`**，所以 #96/#97/#101 一开始"SAME"是**空表 SAME**，
>   没有证据力。
> - 为了拿到真实行，我**没有**去动参考的部署环境变量，而是用**参考公开 API** 对**我自建的 audit bank**
>   打开审计：`PATCH /v1/default/banks/{我的bank}/config {"updates":{"audit_log_enabled":true}}` → 200。
>   这只改了我自己那个 bank 的配置行，不触碰任何既有 bank 的数据。之后参考侧正常写出了
>   `recall`/`update_memory`/`retain` 的审计行。
> - 本服务侧用 `WENYUAN_API_AUDIT_LOG_ENABLED=true` 起副本（同一 bank 上做同样的动作）。
> - **llm-requests 系列（#98/#99）无法线上对齐**：参考侧确实有 trace 行（它配了 LLM），但行的
>   `model`/`provider`/token 数/`input`/`output` 全部来自**两侧不同的模型**，且本服务副本 `WORKER_ENABLED=false`
>   （retain 的嵌入与 chat 调用由 worker 做）。因此这两条只核**形状与过滤参数**，逐字段取值不比。
> - 夹具：`audit-ra-p7qcsa`（两侧同名同内容，建完删除）。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #93 `GET .../stats` | ✅一致 | `api/http.py:6818-6863`：`refresh: bool = False`；响应 `BankStatsResponse` 15 键（`bank_id,total_nodes,total_links,total_documents,nodes_by_fact_type,links_by_link_type,links_by_fact_type,links_breakdown,pending_operations,failed_operations,operations_by_status,last_consolidated_at,last_memory_write_at,pending_consolidation,total_observations`，另有 `failed_consolidation`）；`total_nodes = sum(node_counts.values())`、`total_links = sum(link_counts.values())`；`links_breakdown` 由 `link_breakdown` 行按 `fact_type`→`link_type` 二级聚合；`last_memory_write_at` 用 `.get()`（旧缓存行无此键） | `internal/reporting/handlers.go:123-165`（`Stats`） | 无 | 线上（空 bank）：两侧 **200** 且 15 键及键序逐字节一致、取值全为 0/`{}`/`null` SAME；未知 bank → **404** SAME；`?refresh=true` → 200 SAME；`?refresh=1` → 200 SAME；`?refresh=zz` → **422 `bool_parsing`** 且 `loc:["query","refresh"]`、`input:"zz"` 两侧一致 |
| #94 `POST .../health/llm` | ⚠️未线上核（见下） | `api/http.py:6879-6898`：**先**判 `get_config().enable_bank_llm_health`，关闭则 **404** `"Bank LLM health check is disabled. Set HINDSIGHT_API_ENABLE_BANK_LLM_HEALTH=true to enable."`；开启才真正发 provider 调用，响应 `BankLlmHealthResponse{bank_id,operations:[{operation,ok,status,latency_ms}]}`，文档明确"只返回状态，绝不返回 provider/model/endpoint/key/原始错误" | `internal/reporting/handlers.go:256-320`（`HealthLLM`、`probeLLM`） | 无 | **未线上核**：本服务副本与参考的模型后端不同，两侧的 `status`/`latency_ms` 必然不同（属 §3 明列的"依赖外部 provider"夹具差异）。按 checklist §3 的例外条款，本行以**源码逐行比对**为证据：关闭态 404 文案、开启态响应三键、`ok`/`status` 的取值域两边一致 |
| #95 `GET .../stats/memories-timeseries` | ✅一致 | `api/http.py:6920-6938`：`period: str = "7d"`（**裸字符串，不校验**）、`time_field: str = "created_at"`（文档写明"未知值回落到 created_at"）；响应 `MemoriesTimeseriesResponse` 由引擎直接构造 | `internal/reporting/handlers.go:167-254`（`Timeseries`） | 无 | 线上 7 个探针 SAME：`default`、`?period=1d`、`?period=30d`、`?period=bogus`（**200，不校验**）、`?period=`（空串）、`?time_field=mentioned_at`、`?time_field=bogus`（200，回落）；未知 bank → 404 SAME。空 bank 下两侧 `buckets` 均为 `[]` |
| #96 `GET .../audit-logs` | ⚠️存疑（见下） | `api/http.py:10848-10875`：`action`/`transport`/`start_date`/`end_date`/`limit`(50, ge=1 le=500)/`offset`(0,ge=0)；`engine/memory_engine.py:14484-14534`：`action`、`transport` **原样进 WHERE**（不校验枚举），`started_at >= start_date`、`started_at < end_date`（**半开**），`ORDER BY started_at DESC`，**列表不带 `request`/`response` 体**（"单个 body 可达兆级，一页就能耗尽 API 进程"），只回 `_audit_summary` 的 8 键（`id,action,transport,bank_id,started_at,ended_at,duration_ms,metadata`）；包络 `bank_id,total,limit,offset,items` | `internal/reporting/observability_reports.go:16-88`（`AuditLogs`、`auditLogSummary`） | 无 | 线上（真实行，两侧同一批动作）：包络 5 键与列表项 8 键**及键序逐字节一致**；`?action=recall` 两侧 `total` 相同 SAME；`?transport=mcp`（空页）SAME；`?limit=1` SAME；`?limit=0`/`501`/`offset=-1` → 422 SAME；`?start_date=notadate` → 400 SAME；时间窗 SAME；未知 bank → 404 SAME。**唯一 DIFF 是 `metadata`**：见下「⚠️ 存疑 1」。另有 `total` 相差 1，原因是参考侧 `retain` 那次动作我的两次写只记到一次（夹具），非代码差异 |
| #97 `GET .../audit-logs/stats` | ✅一致 | `api/http.py:10887-10908`：`action`、`period: str = "7d"`（**裸字符串**）；`engine/memory_engine.py:14577-14649`：`now-1d`/`now-30d`/`now-7d`（**`trunc` 恒为 `"day"`**，不随 period 变），`GROUP BY date_trunc('day', started_at), action ORDER BY bucket ASC`，**只返回有行的桶**（不补齐空桶），每桶 `{"time","actions":{action:count},"total":sum}`，`start` 为 `start.isoformat()`（`+00:00` 拼写），`period` 回显调用方原值 | `internal/reporting/observability_reports.go:130-156`（`AuditLogStats`） | 无 | 线上：两侧 5 键包络 `bank_id,period,trunc,start,buckets` 一致、`trunc:"day"` 一致、桶 4 键 `time,actions,total` 一致、**不补空桶**一致；`?period=1d`/`30d`/`bogus`（→ 回落 7d 且 `period` 回显原值 `bogus`）均 SAME；`?action=recall`：两侧 `actions:{"recall":2},"total":2` **逐字节相同**；未知 bank → 404 SAME。`actions` 内的**键序**两侧都随行序（同一张表、同一个 `ORDER BY bucket ASC`），观测一致 |
| #98 `GET .../llm-requests` | ⚠️未线上核（见下） | `api/http.py:10952-10999`：`status`/`operation`/`scope`/`call_type`(Literal)/`provider`/`trace_id`/`document_id`/`memory_id`/`group`(bool)/`start_date`/`end_date`/`limit`(50,ge=1,le=500)/`offset`；`LLMRequestEntry` **23 键**（`id,bank_id,operation,scope,trace_id,span_id,parent_span_id,provider,model,status,started_at,ended_at,duration_ms,input_tokens,output_tokens,cached_tokens,thoughts_tokens,total_tokens,input,output,error,llm_info,metadata`），包络 `bank_id,total,limit,offset,items`；`call_type` 由 `scope` 前缀**派生**（`embedding_*`/`rerank`/`decision_*`/其余为 `llm`），不是存储列 | `internal/reporting/observability_reports.go:159-258`（`LLMRequests`） | 无 | **未线上核**：两侧模型后端不同（`model`/`provider`/token 数/`input`/`output` 必然不同），且本服务副本 worker 关闭。按 §3 例外条款以**源码逐行比对**为证据：12 个查询参数的声明集合、`limit`/`offset` 边界、`call_type` 的四个取值与 scope 前缀映射、包络与条目 23 键的键集，两边一致。**错误路径可用性已验证**：未知 bank → 404、`limit=0`/`501` → 422 两侧一致（在空 bank 上跑的） |
| #99 `GET .../llm-requests/stats` | ⚠️未线上核（同上） | `api/http.py:11013-11038`：`operation`、`call_type`(Literal)、`period`（裸字符串，默认 `7d`）；`engine/llm_trace.py:541-566`：`LLMRequestStatsResponse{bank_id,period,trunc,start,buckets}`，桶 `{time,statuses,total,tokens}`，`tokens` 五键 `input,output,cached,thoughts,total`（`thoughts` 可空以兼容旧服务端，本服务端恒发） | `internal/reporting/observability_reports.go:261-294`（`LLMRequestStats`） | 无 | 同 #98：两侧模型后端不同，取值不可比。源码比对：包络 5 键、桶 4 键、`tokens` 五键及顺序、`thoughts` 恒发（参考注释写明"This server always sends it"）全部一致；未知 bank → 404、`call_type=bogus` → 422 两侧一致 |
| #101 `GET .../audit-logs/{log_id}` | ✅一致 | `api/http.py:10921-10935`：`get_audit_log` 返回 `None` → **404** `"Audit log entry '<log_id>' not found"`（**未知 bank 与未知 entry 同一个 404、同一句文案**，因为引擎在 `get_bank_profile` 为 None 时也返回 None）。`engine/memory_engine.py:14537-14575`：**先 `uuid.UUID(log_id)`，失败直接 `return None`**（即非 UUID 也是**404**，不是 400），再查 bank profile、再做 bank-wide 拒绝、最后 `WHERE id=$1 AND bank_id=$2`；响应 `AuditLogEntry` = `AuditLogSummary` 的 8 键 **+ 尾部 `request`,`response`** 两键（Pydantic 继承，父类字段在前） | `internal/reporting/observability_reports.go:91-127`（`AuditLog`） | 无 | 线上：真实行 → 两侧 **200**，**10 键及键序逐字节一致**（`id,action,transport,bank_id,started_at,ended_at,duration_ms,metadata,request,response`），`request`/`response` 两侧均为对象 SAME；未知 UUID → 404 SAME；**非 UUID → 404**（不是 400）SAME，与参考的 `return None` 一致；未知 bank → **404 且文案与未知 entry 相同** SAME |

**⚠️ 存疑 1 —— `audit_log.metadata` 里的 `error` 键（本服务多写，参考不写）**

- **现象（线上真实行）**：对一条**失败**的 `update_memory`（`PATCH` 到不存在的记忆），
  参考的审计行 `metadata = {}`；本服务 `metadata = {"error":"Memory unit '<id>' not found"}`。
  列表（#96）与单条（#101）两处都能看到这个差异。
- **参考证据（三处，互相印证）**：
  1. `AuditEntry` 的字段表里**没有** error 成员（`engine/audit.py:82-100`）；
  2. `audit_context` 的 `finally` 只做 `entry.ended_at = now()` 然后 `log_fire_and_forget(entry)`
     （`engine/audit.py:278-282`），**不看响应状态、不写任何失败标记**；
  3. 写入只有一条 INSERT，`metadata` 传的是 `_safe_json(entry.metadata) or "{}"`
     （`engine/audit.py:224-243`），而 `entry.metadata` 全程只有调用方 `@audited(..., metadata=…)`
     传进来的值。全仓 `grep "entry.metadata\["` 无任何命中。我做的**失败**动作在参考侧确实留下了
     `response` 非空的审计行，但 `metadata` 仍是 `{}`。
- **本服务证据**：`internal/httpx/audit.go:88-91` 在 `recorder.status >= 400` 时设
  `entry.Error = recorder.errorText()`，`internal/audit/audit.go:219-233` 把它塞进
  `metadata["error"]`。
- **影响面**：`AuditLogSummary.metadata` / `AuditLogEntry.metadata` 是**公开响应字段**，一个按
  `metadata == {}` 判断"这次调用没出错"的客户端在本服务上会读到 `false`，而参考上恒为 `true`
  （参考用 `response` 是否为空/状态判断）。这是**对外可见的响应差异**。
- **为什么本轮没改**：本服务有**两个**包内测试显式断言这个 `metadata.error`
  （`internal/app/audit_test.go:245-247`、`internal/worker/audit_test.go:66-68`），删掉它需要同时
  改这两处断言；而且这是"本服务比参考多给了信息"的方向。**请组长裁决**：
  (a) 删掉 `metadata.error` 与参考对齐（改 `httpx/audit.go` + `audit/audit.go` + 两个测试）；
  (b) 保留并记为本服务的有意增量（那就要在 checklist §4 的"有意增量"表里登记，否则下一轮会被再报一次）。

**未线上核的三条（#94 / #98 / #99）——为什么，以及用什么顶替**

这三条都**依赖两侧各自的外部模型后端**，输出里必然含模型名、provider、token 数、延迟、真实错误文本：
- #94 `POST .../health/llm` 会**真的发 provider 调用**，两侧后端不同 ⇒ `status`/`latency_ms` 必不同；
- #98/#99 的 `model`/`provider`/`input_tokens`/`output_tokens`/`input`/`output` 全由模型决定；
  且本服务副本 `WENYUAN_API_WORKER_ENABLED=false`，retain 的嵌入/chat 调用不跑，行集也对不上。

按 checklist §3 的例外条款（"若某条 API 无法线上差分（依赖外部 provider…），用源码逐行比对 + 往返用例
作为证据，并写明为什么"），这三条以：
1. **源码逐行比对**：参数声明集合、边界值、Literal 取值域、`call_type` 的 scope 前缀映射、
   包络与条目的键集/键序；写入上表每行的"参考证据"列。
2. **可差分的那部分照打**：错误路径（未知 bank → 404、`limit`/`offset` 越界 → 422、
   `call_type=bogus` → 422、`group=zz` → 422）在空 bank 上两侧一致。

**本轮踩到并已修复的环境事故（记录以免他人重复）**

我在收尾 task-9 时 `DROP SCHEMA audit_memops CASCADE` 清了审计 schema，**但它正是 18213 副本正在用的
schema** —— 于是后续所有写路由开始 500（服务日志 `relation "audit_memops.banks" does not exist`，
`SQLSTATE 42P01`）。表现很有迷惑性：健康检查 200、读路由正常，只有写 500，容易被当成代码缺陷。
重启副本即自动重迁移（25 张表重建）。**结论：`audit_memops` 是常驻 schema，收尾时不要 drop；要换干净环境，
换一个 schema 名字而不是删当前这个。**

#### 版本对照（补充证据，2026-11-04 采集）

| 物 | 版本 |
|---|---|
| 运行实例 `GET http://127.0.0.1:8888/version` 的 `api_version` | **1.1.0** |
| 源码树 `../hindsight` 的 `hindsight-api-slim/hindsight_api/__init__.py:43` `__version__` | **1.1.0** |
| 源码树 `hindsight-api-slim/pyproject.toml:7` `version` | **1.1.0** |
| 源码树 HEAD | `3ee25616c Merge tag 'v1.1.0' into devin`（工作区有未提交改动：`M .gitignore`、`M hindsight-integration-tests/tests/test_base_path_deployment.py`、`?? docker-compose.embedding.yml`，均与本条无关） |

**结论：版本一致（1.1.0 == 1.1.0），故不能升级为「版本偏差」。**
这条维持 `⚠️存疑（参考侧回归，不照抄）`：源码与运行实例同版本，而运行实例在同步路径上的
行为与源码（唯一写 chunks 路径）矛盾 —— 这是运行实例侧的问题，不是我们读错了版本的源码。

> 附带确认：运行实例 `/version` 的 `features.store_document_text` 为 **true**，
> 与源码 `DEFAULT_STORE_DOCUMENT_TEXT = True`（`config.py:1878`）一致，
> 进一步排除了「该部署关掉了文档文本存储所以不写 chunks」这一解释
> （`store_document_text=false` 时参考会写**空串** chunk_text 的行，而不是不写行）。

> 限制说明：运行实例运行在与本 Agent 隔离的 PID 命名空间中（`/proc` 看不到它的进程），
> 因此无法从进程命令行/加载路径直接证明"它跑的就是 `../hindsight` 这棵树"。上表的
> 版本号对照是能拿到的最强证据。

#### 移交（不属本单元作用域）

`GET .../documents/{id}` 的 `nodes_by_fact_type` **键序**两侧不同：

| 侧 | 值 |
|---|---|
| 参考 | `{"world":0,"experience":0,"observation":0}`（声明序） |
| 本服务 | `{"experience":0,"observation":0,"world":0}`（Go map 字母序） |

落点：`internal/readsurface/models.go:74`（`NodesByFactType map[string]int`）、
`internal/readsurface/documents.go:237`（`factTypeCounts`，返回 `map[string]int`）。
固定三键，改法是把 map 换成定序结构。**已移交 `read-surface` 单元**，本单元不改。

### 补充：`nodes_by_fact_type` 键序（retain 单元转交，并入 #68）

**已随 #68 一并修复并线上复验，此处单列一行便于检索。**

参考的 `nodes_by_fact_type` 是 dict 字面量按 **world → experience → observation** 写出
（`engine/memory_engine.py:11303-11307`），Python dict 保序 → JSON 就是这个序；
本服务原为 `map[string]int`，Go 的 JSON 序列化按**字典序**得出 `experience, observation, world`。

- 落点：`internal/readsurface/models.go:74`（`NodesByFactType map[string]int` → `orderedFactTypeCounts`）
  + `internal/readsurface/documents.go:237-247`（`factTypeCounts` 改为按 world/experience/observation 定序构造 `[]httpx.OrderedMember`）
  + `documents.go:249-268`（`MarshalJSON` 转 `httpx.OrderedObject`、`UnmarshalJSON` 回读、`count` 读取器）。
- 证伪用例：`internal/readsurface/document_detail_order_test.go` `TestDocumentDetailServesTheReferenceMemberOrder`
  ——把 `models.go` 字段序改回修前即 FAIL（实测输出见 #68 行）。
- 线上复验（`audit-read-zx0nzu` 的 `folder/doc.md`）：修后两侧 `list(body)` **完全相等**，
  `nodes_by_fact_type` 内层两侧都是 `['world','experience','observation']`。
- 单测：`go test ./internal/readsurface/` 全绿（含 `TestDocumentDetailObservationCountAndMetadata` 等 4 个依赖该字段的既有用例，
  已改为通过 `count()` 读取器取值，不再按下标访问 map）。

### `event_date` / 实体 `first_seen` 疑点（read-surface 转来）—— ✅一致，非缺陷

**疑点**：read-surface 报「本服务存当天零点、参考存真实时刻」。

**实测结论：两侧行为完全一致。** 该现象是**日期推断粒度**决定的，不是写入侧差异。

**参考源码证据（已逐行核对）**

1. 实体时间戳取 `fact.occurred_start if fact.occurred_start is not None else fact.mentioned_at`
   —— `engine/retain/entity_processing.py:107`（`fact_dates = [...]`）。
2. 建实体时 `COALESCE(event_date, now())` 同时填 `first_seen`/`last_seen`
   —— `engine/db/ops_postgresql.py:396-409`。

**本服务落点（与参考同构，无需改）**

- `internal/storage/memory_read_surface.go:363` `entityEventDate`：`occurred_start`，否则 `mentioned_at`
  （注释即引 `entity_processing.py:107`）。
- `internal/storage/postgres_retain.go:820` 的 `INSERT … COALESCE($5, now())`（同参考 SQL 形状）。

**实测对照（两侧同内容、同文档）**

| 场景 | 参考 `memory_units` | 本服务 `memory_units` | 参考 `entities.first_seen` | 本服务 `entities.first_seen` |
|---|---|---|---|---|
| 无日期事实「Wombat lives in Tasmania near Hobart.」 | `occ=NULL`、`ev=men=16:36:14.615831`（**微秒精度真实时刻**） | `occ=NULL`、`ev=men=16:36:31.79648`（同为真实时刻） | 16:36:14.615831 | 16:36:31.79648 |
| 含年份事实「The Quokka project launched in 2021.」 | `occ=ev=2021-01-01 00:00:00`、`men=16:37:09` | `occ=ev=2021-01-01 00:00:00`、`men=16:37:17` | **2021-01-01 00:00:00** | **2021-01-01 00:00:00** |

**为什么「零点」是正确行为**：`in 2021` 这类**粗粒度**短语只能推断到日历跨度。
参考与本服务都用「该跨度首日 00:00:00」表示（本服务 `internal/extraction/temporal.go:95-99`
`InferPeriod` 的 `time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)`；参考同一语义）。
于是实体的 `first_seen` 也落成 `2021-01-01 00:00:00` —— **两侧逐字节相同**。
这不是「存零点」而是「该事实的事件日期本来就是该跨度第一天零点」，实体再从事实继承。

**没有发生模型的普通事实**（上表第一行）两侧都是**微秒真实时刻**，
说明「无事件日期 → 用 `mentioned_at` → 真实时刻」这条链在两侧一致。

→ 结论 **✅一致，非缺陷，未改代码**。read-surface 看到的差异应来自两侧库内容/时刻不同
（各自请求时刻，属夹具噪声），而非实现差异。**该疑点可以关闭。**

**若日后仍要追**：可对比**同一秒**内两侧写入的实体，或用带显式 `timestamp` 的 retain 复现——
但本轮的对照已覆盖「无日期」与「粗粒度日期」两类，未发现任何实现分叉。


> 参考 `api/http.py:10621`；本服务 `internal/memoryops/file_retain*.go`、`internal/fileparse/`。
> 端口 18212（single-tenant，schema `audit_retain`，`WORKER_ENABLED=false`）。夹具 bank `audit-retain-fr`。

| 子项 | 结论 | 参考证据 | 本服务落点 | 验证 |
|---|---|---|---|---|
| 非 multipart / 缺 `files`+`request` 部件 | ✅一致 | `api/http.py:10621` 两个 `File(...)`/`Form(...)` 均在框架层校验，按声明序（`files` 先于 `request`）一起报 | `internal/memoryops/file_retain.go` 的部件校验 | `--body '{"a":1}'` → 两侧同为 422 `[{missing,files,input:null},{missing,request,input:null}]` **`SAME`** |
| 未知 bank → 404 | ✅一致 | `_submit_async_operation` 抛 `Bank '<id>' not found` | `internal/memoryops/file_retain.go` 提交前 `GetBank` | 未建 bank 时上传 → 两侧同为 404 `Bank 'audit-retain-fr' not found` **`SAME`** |
| 真实 multipart 上传成功路径 | ✅一致 | `api/http.py:10690-10720`（`FileRetainRequest.model_validate_json` → 计数/字节上限 → parser allowlist → `submit_async_file_retain`） | `internal/memoryops/file_retain.go` + `file_retain_request.go` | 单文件 + `files_metadata:[{document_id}]` → 两侧 200 `{"operation_ids":[…]}` 形状同 **`SAME(shape)`** |
| **`file_convert_retain` 顶层 operation 形状** | ✅一致（第三轮 ⚠️ 缺口已关闭） | `engine/memory_engine.py:23535-23657` `submit_async_file_retain`：每文件一个 `file_convert_retain` 顶层 op，`dedupe_by_bank=False`，`result_metadata={original_filename, document_id}` | `internal/memoryops/file_retain_queue.go:24-52` `queueFileRetain`（`OperationTypeFileConvertRetain`、`ResultMetadata{document_id, original_filename}`）、`internal/storage/stored_files.go:37` | `GET .../operations?type=file_convert_retain` → 两侧 `total:1`，行内 **15 个键同序同值**：`id/task_type/operation_id/operation_type` 全为 `file_convert_retain`、`items_count:0`、`document_id:"frdoc-a"`、`filename:"a.txt"`、`mental_model_id:null`、`details:null`、`error_message:null`、`retry_count:0`、`next_retry_at:null`、`progress:null`。**唯一差异是 `status`**（见下，属 worker 夹具） |
| **⚠️ 文件转换后另提一条独立 `retain` op** | ⚠️存疑（结构差异，已确证可观察） | 参考 `file_convert_retain` 完成后**另提一个 `retain` 任务并把自身标 completed**（`engine/memory_engine.py:4092-4200` `_handle_file_convert_retain`）。实测两侧 `GET .../operations`（无 `type` 过滤）**参考 `total:2`、本服务 `total:1`**：参考多出一条 `task_type:"retain"`、`document_id:"frdoc-a"`、`filename:"a.txt"` 的行 | 本服务 `internal/worker/consumer.go:828` 把 `file_convert_retain` **与 `retain` 同分支处理**——一条 op 既转换又 retain，不另提子操作 | 见下方「结构性缺口」分析 |

#### `status` 差异是 worker 夹具，不是缺陷

上传后立即读 operation 行时，参考的 `file_convert_retain` 已 `completed`（参考 worker 常开），
本服务仍 `pending`（`WORKER_ENABLED=false`，我这份副本没有 worker 消费队列）。
`updated_at` 因此也不同。这是 checklist §3 明确列为夹具噪声的项（"`parity_dev_service.sh` 固定
`WENYUAN_API_WORKER_ENABLED=false`"），**不据此改代码**。
行的**形状**（15 键、键序、`document_id`/`filename`/`result_metadata` 投影）两侧一致。

#### ⚠️ 结构性缺口：转换后另提 retain 子操作

**参考行为**（源码 + 实测双证）：
1. `submit_async_file_retain` 每文件提交**一个** `file_convert_retain` op（`memory_engine.py:23638-23657`）；
2. worker 执行 `_handle_file_convert_retain` 时，转换完**另提一个 `retain` 任务**，
   再把自己的 `file_convert_retain` 标 completed（`memory_engine.py:4092-4200`）；
3. 于是 `GET .../operations` 能看到**两行**：一行 `file_convert_retain`（已完成，记录转换），
   一行 `retain`（正在做抽取）。

**本服务行为**：`internal/worker/consumer.go:828` 把 `file_convert_retain` 与
`OperationTypeRetain`/`AsyncRetainType` 放进**同一个 case**，一条 op 跑完转换+抽取，不另提子操作。
故 `total` 为 1。

**为什么本轮不修**：
- 修它要在 `internal/worker/` 新增一个"转换 → 另提 retain → 自我 completed"阶段，
  并让 `internal/storage` 的 op 行支持子操作关联 —— 这是**跨 storage + worker 的结构性改造**；
- **不影响客户端契约**：`POST .../files/retain` 的响应体（`{"operation_ids":[…]}`）两侧本来一致，
  调用方拿到的 id 也能轮询到终态；差异只落在 `GET .../operations` 的**行数**上；
- 第三轮已把该条判为「已确证可观察、本轮不修的结构性缺口」，本轮复验**缺口范围已缩小**：
  `file_convert_retain` 那一行的形状（第三轮完全缺失）现已 100% 对齐，**只剩"多一行 retain"**。

**可观察影响**：`GET .../operations`（无 `type` 过滤）与 `?type=retain&document_id=<上传文档>` 的行集。
`?type=file_convert_retain` 两侧现在一致。

→ 记 `⚠️存疑（已确证可观察，结构性缺口，本轮不修）`，建议作为独立立项
（`internal/worker` + `internal/storage` 协同），不并入本单元。

## 第五轮 transfer（补证：multipart 成功路径 + 一处已修）

> 承接上文 `## 第五轮 transfer`。上一轮如实标注了 **#82/#84 的成功路径未覆盖**，本节补上，
> 并在补测过程中**发现并修复了一处真实缺陷**（`POST .../transfer/import` 的归档校验时机）。

### 一、#82/#84 multipart 成功路径（补测）

用**真实产物**做夹具：先在参考上 `POST .../document-transfer/export` 生成异步导出，等 worker 完成，
取到 `result_metadata` 里的真实 `storage_key`（`tenants/user_devin/banks/<bank>/exports/<uuid>/transfer.zip`），
再用 `GET /v1/default/files/download/{key}` 下载真 ZIP（957 字节），以其作为两个上传端点的输入。

- **#82 `POST .../document-transfer` 成功路径** ✅：multipart 上传真 ZIP → 两侧同 `202`
  + `{"operation_id", "status":"pending"}`（键集与键序相同）；`on_conflict` 三个合法值
  `skip`/`replace`/`new-id` 两侧都 `202`，非法值 `bogus` 两侧同
  `400 {"detail":"Invalid on_conflict 'bogus' (expected skip|replace|new-id)"}`；非 ZIP 字节两侧同
  `400 {"detail":"Invalid transfer archive: the uploaded file is not a readable .zip (File is not a zip file)"}`。
  **副作用往返已证**：在 worker 开启的副本（18219）上该操作从 `pending` 跑到 `completed`，
  写入目标 bank 的文档与参考**同 id、同 `content_hash`、同 `text_length`**；操作类型两侧同为 `import_documents`。
- **#84 `POST .../transfer/import` 成功路径** ✅（`mode=merge`）：上传真 ZIP → 两侧同 `202`；
  目标文档同样落库。
- **#87 `GET /files/download/{key}` 正向路径** ✅：深层多段 key
  `tenants/user_devin/banks/<bank>/exports/<uuid>/transfer.zip` → 参考 `200`、
  `content-type: application/zip`、`content-disposition: attachment; filename="<bank>-documents.zip"`、
  `content-length: 957`；本服务同一路由对同一 key 的解析等价（见上节 404 矩阵），
  ZIP 本身可解开（`manifest.json` + `documents/000000.json`，
  `schema_version:1`、`archive_type:"documents"`、`bank_rows_json_encoding:null`）。

### 二、补测中发现并修复的缺陷：`transfer/import` 的归档校验时机

| 项 | 内容 |
|---|---|
| **API** | #84 `POST /v1/default/banks/{bank_id}/transfer/import`（restore 模式） |
| **结论** | ❌已修 |
| **参考证据** | `engine/memory_engine.py:8229`：`target = target_bank_id or read_bank_manifest(archive).source_bank_id` —— Python `or` **短路**，调用方给了 `target_bank_id` 就**永远不会**执行 `read_bank_manifest`，因此归档**不被解析、`archive_type` 不被检查、`schema_version` 不被校验**。`read_bank_manifest`（`engine/transfer/importer.py:305-313`）只在需要从归档推导 target 时才跑，届时对 `archive_type != "bank"` 抛 `ValueError`（→400）。同一短路在 clone 的 `import_bank`（`engine/memory_engine.py:8983`）重复出现。 |
| **本服务旧行为** | `internal/transfer/scope.go:147`（旧）**无条件** `validateArchive(...)`，并（在我第一次尝试修复时）无条件 `requireBankArchive`。于是：给 `target_bank_id` 时，**一个文档归档或根本不是 ZIP 的字节**被本服务 `400` 拒绝，而参考照常 `202` 受理。 |
| **修复** | 在 `resolveImportSelection` 里复刻短路：给了 `target_bank_id` 时**不解析归档**、直接返回 target（202）；只有需要从归档推导 target 时才 `validateArchive` + `requireBankArchive`。`requireBankArchive` 新增于 `internal/transfer/archive_binding.go:66-78`，报错文案逐字节对齐参考的 `repr` 引号风格：`Not a whole-bank archive (archive_type='documents'); use import_documents instead`。 |
| **用例** | `TestRestoreValidatesTheArchiveOnlyWhenItMustReadTheManifest`（4 子用例：documents-归档/非ZIP × 给 target/不给 target）、`TestRestoreStillRefusesAnExistingTarget`、`TestRequireBankArchiveAcceptsMissingType`、`TestMergeStillAcceptsDocumentsArchive`。位于 `internal/transfer/archive_type_test.go`。 |
| **证伪** | 把 `resolveImportSelection` 改回"无条件解析+类型校验"后跑：`--- FAIL: TestRestoreValidatesTheArchiveOnlyWhenItMustReadTheManifest`，两个 `target supplied` 子用例报 `status = 400, want 202 (body {"detail":"Not a whole-bank archive (archive_type='documents'); …"})`。改回修复后 141 个用例全绿。 |
| **线上差分** | 四种组合逐条对齐：①documents-ZIP + target → 两侧 `202`；②非ZIP + target → 两侧 `202`；③documents-ZIP 无 target → 两侧同 `400` 同文案；④非ZIP 无 target → 两侧同 `400` 同文案。 |

> 注：本条修的是**我上一节自己引入的过严校验之外的既有偏差** —— 旧代码在 `target_bank_id` 存在时
> 也会因归档不可读而 400，与参考不符。修复后 `merge` 模式、`document-transfer`（#82）与
> 目标已存在的 400 行为均未受影响（各有回归用例）。

### 三、本轮验证门（全绿）

```sh
go build ./...                                  # 通过
CGO_ENABLED=0 go build -tags pgx ./...          # 通过
go test -count=1 ./internal/transfer/...        # 141 passed
gofmt -l internal/transfer internal/monitoring internal/bankapi   # 空
go vet ./internal/transfer/... ./internal/monitoring/... ./internal/bankapi/...   # 无输出
golangci-lint run ./internal/transfer/...       # No issues found
```

**transfer 补证小结：#78–#87 维持 10 条；#84 从"未覆盖成功路径"升级为 `❌已修`（1 处缺陷），
其余仍 `✅一致`。单元计数：✅ 9 / ❌已修 1 / ⚠️ 0。**

**修复 1（#96 / #101，按组长 2026-11-04 兼容性裁决）：失败的审计行不再带 `metadata.error`，也不再带错误响应体**

上一节把 `metadata.error` 记成"⚠️存疑，待裁决"。组长裁决：**对齐参考（AGENTS.md「两方不一致时参考实现的行为赢」）**，
`metadata` 虽是自由对象，但它在冻结的公开响应面上，多写的键对参考客户端是未知语义。已修，并连带发现**第二处**同类差异。

**差异 A —— `metadata.error`（本服务多写）**

- 参考三处互证：`AuditEntry` 字段表无 error 成员（`engine/audit.py:82-100`）；`audit_context` 的 `finally`
  只写 `ended_at` 再 fire-and-forget（`:278-282`），**不看状态码**；INSERT 只写 `entry.metadata`，
  全仓无 `entry.metadata[...]=` 命中。
- 本服务原实现：`internal/httpx/audit.go` 在 `status >= 400` 时设 `audit.Entry.Error`，
  `internal/audit/audit.go` 的 `Record` 再把它塞进 `metadata["error"]`。
- **改法**：删除 `audit.Entry.Error` 字段本身、`Record` 里的 metadata 注入、以及
  `auditResponseWriter.errorText()`（唯一用途就是取这段文本）。连带删除两个生产者：
  `internal/worker/consumer.go`（失败操作不再设 Error）与 `internal/mcp/tools_dispatch.go`（失败的 MCP 工具不再设 Error）。

**差异 B —— `response`（本服务把错误响应体记进了审计行）**

- 这是修 A 时线上复测冒出来的：参考失败行的 `response` 是 **`null`**，本服务是 `{"detail":"…"}`。
- 参考证据：`@audited` 包装器**在 `func()` 返回之后**才赋 `entry.response`（`api/http.py:5205-5216`）——
  一个抛异常的 handler 走不到那一行，`finally` 于是带着 `response=None` 落库。
- 本服务原实现是 HTTP 中间件，`responseObject()` 无条件下取响应体，把错误信封也记了进去。
- **改法**：`internal/httpx/audit.go` 只在 `recorder.status < 400` 时记录 `Response`。

**验证（改后线上实测，`audit-err-svkfxq`，两侧都对自己的 bank 打开 `audit_log_enabled`）**

| 场景 | 参考 | 本服务（改后） |
|---|---|---|
| 失败 `PATCH`（404）的审计行 | `metadata {}`, `response null` | `metadata {}`, `response null` — **一致** |
| 同一行的列表项 | `metadata {}` | `metadata {}` — 一致 |
| 成功 `recall` 的审计行 | `response` 为对象（6 键起：`trace,chunks,results,entities,source_facts,…`） | 同为对象，键集相同 — 一致 |

**回归用例（"改回去就 FAIL"）**

- `internal/app/audit_test.go` 的 `TestAuditRecordsFailingOperation`：断言失败行 `metadata` 里**没有** `error` 键
  且 `len(metadata)==0`，并断言 `response` 为 null。把 `Entry.Error` / metadata 注入 / 无条件 `responseObject()` 任一改回去即 FAIL。
- `internal/worker/audit_test.go` 的 `TestConsumerAuditsFailedOperations`：worker 侧的失败操作行 metadata 必须为空。
- 两个测试的注释里都写明了对应的证伪路径。

**说明**：本轮 findings 里"⚠️ 存疑 1"的原始记录保留在上面那段，作为发现过程的证据；结论以本节为准
（`metadata.error` 已移除，问题关闭）。

## 第五轮 组长横切修复（entity_labels input_value 确定性）

> 由 task-14 追查。起点是 read-surface 在接收 `entity_labels` 移交时提出的怀疑：
> `internal/entitylabels/reference_shape.go` 的 `migrationDictOrder`（原实现）"依赖 Go map 随机迭代序，
> 同一请求多次调用可能给出不同键序"。**该怀疑本身不成立**，但追查过程发现了**另一个真实且可稳定复现的缺陷**。
>
> 参考证据：`engine/retain/entity_labels.py:90-104` `_migrate_label_group` 是 `patched = dict(raw)`
> 之后 `patched["type"] = ...`（legacy `free_values`/`multi_value` 分支）—— `dict(raw)` **保留调用方插入序**，
> `type` 追加在末尾。`config_resolver.py:601-607` 把 pydantic 的 `str(ValidationError)` 原样透传，
> 所以 `input_value=` 里那个 dict 的成员序 = **调用方书写序 + 末尾 `type`**。
>
> 环境：参考 8888 + 本服务 18211（单租户副本），两侧共用夹具 bank `audit-elx-30134`（已删）。

### 1. 先证伪原怀疑：渲染本来就是确定的

`migrationDictOrder` 原实现是 `for key := range migrated` 收集非 `type` 键后 **`sort.Strings(keys)`**，
再追加 `type`。**排序消除了 map 的随机迭代序**，所以输出是确定的。

| 探针（2000 次调用 / 连续 5 轮 `-count=5`） | 结果 |
|---|---|
| `ShapeProblems` 对 `{"tag":"t","optional":"x"}` 连续 2000 次 | **1 种渲染**，无变化 |
| `migrationDictOrder` 直接连续 2000 次 | 恒定 `optional,tag,type` |
| 5 次独立进程各跑一遍 | 全等 |

**结论：原怀疑（不确定性）不成立。** 但探针同时暴露了真正的问题：恒定输出是 **`optional,tag`（字母序）**，
而参考对同一请求给 **`tag,optional`（调用方书写序）**。

### 2. 真实缺陷：输出确定，但**序错**

| 调用方书写序 | 参考 `input_value` | 本服务（修复前） | 判 |
|---|---|---|---|
| `tag, optional` | `{'tag': 't', 'optional': 'x', 'type': 'value'}` | `{'optional': 'x', 'tag': 't', 'type': 'value'}` | ❌ |
| `optional, tag` | `{'optional': 'x', 'tag': 't', 'type': 'value'}` | 同上 | ✅（**恰好**字母序 = 书写序） |
| `zeta, alpha` | `{'zeta': '1', 'alpha': '2', 'type': 'value'}` | `{'alpha': '2', 'zeta': '1', 'type': 'value'}` | ❌ |
| `b, a, c` | `{'b': '1', 'a': '2', 'c': '3', 'type': 'value'}` | `{'a': '2', 'b': '1', 'c': '3', 'type': 'value'}` | ❌ |

**这解释了 read-surface 为什么测得"三组逐字节相同"**：它测的三组里有两组的书写序恰好就是字母序。
只要调用方把成员写成非字母序，差异立刻出现——**不是随机序，是稳定的错序**。

### 3. 修复

根因：`migrate()`/`ShapeProblems` 只拿到 `map[string]any`，**JSON 的书写序在 `httpx.DecodeLax` 之后已经丢失**，
渲染器只能退回字母序。

| 文件 | 改动 |
|---|---|
| `internal/entitylabels/reference_shape.go` | ①`migrationDictOrder(migrated, callerOrder []string)` —— 有 `callerOrder` 就按它输出（跳过 `type`、跳过已删/不存在的键），其余键按字母序补在后面，`type` 恒最后；无则退回字母序（仍确定）。②新增 `ShapeProblemsInOrder(raw, callerOrder [][]string)`，`ShapeProblems(raw)` 变成 `ShapeProblemsInOrder(raw, nil)` 的薄封装（**保留全部既有调用方**）。③`checkGroup`/`problemWithShown` 透传该序（`checkGroup` 用可变参数，`reference_shape.go:990/1028` 两处既有调用不动）。 |
| `internal/bankapi/decode.go` | 新增 `entityLabelsGroupOrder(body)` / `groupMemberOrders` / `objectMemberOrder`：从**原始 body 字节**按文档序读出每个 group 的成员名，兼容 `{"attributes":[...]}` 与裸列表两种 envelope。`decodeRequest` 多返回一个 `[][]string`。 |
| `internal/bankapi/config.go` | `decodeRequest` 接住 `groupOrder` 并传给 `configPatch`。 |
| `internal/bankapi/config_keys.go` | `configPatch`/`validateConfigValue` 加可变参数 `groupOrder ...[][]string`（`banks.go:155/198` 两个既有调用点**零改动**）；`entity_labels` 分支改调 `entitylabels.ShapeProblemsInOrder(raw, order)`。 |

改动共 **+141 / −21 行**（含注释）。`internal/httpx` 未改。

### 4. 验证

**线上差分（修复后，最终二进制，9/9 逐字节 SAME）**：envelope 与裸列表两种形态 × 非字母序/字母序/三成员/调用方自带 `type`/legacy `free_values`/第二组才是被拒组 ——
`d1 == d2` 比较的是**整个 detail 字符串**，不是前缀。

**证伪（修复即 FAIL）**：把 `migrationDictOrder` 的 `for _, key := range callerOrder` 改回 `range []string{}`（= 退回字母序），
`TestBankConfigWriteEntityLabelsPrintsTheCallersMemberOrder` 的 5 个非字母序子用例 + `TestBankConfigWriteEntityLabelsOrderIsDeterministic` **同时 FAIL**，
失败输出正是 `input_value={'optional': 'x', 'tag': 't', ...}`；恢复后 8/8 通过。

**新增测试**：
- `internal/bankapi/config_test.go`：`TestBankConfigWriteEntityLabelsPrintsTheCallersMemberOrder`（6 子用例，端到端打 `PATCH .../config`）、`TestBankConfigWriteEntityLabelsOrderIsDeterministic`（同一请求 200 次，断言响应体逐字节相同且为书写序）。
- `internal/entitylabels/det_probe_test.go`：`TestShapeProblemsPrintsTheCallersMemberOrder`（3 例）、`TestShapeProblemsIsDeterministicWithoutACallerOrder`（500 次无序调用恒定）、`TestMigrationDictOrderKeepsUnnamedMembers`。

**确定性实测量化**：修复后用最终二进制对同一请求连打 **30 次**，出现 **1 种**渲染（`{'tag': 't', 'optional': 'x', 'type': 'value'}`）——
修复前后都确定，但修复后**是对的确定**。

### 5. 验证门

| 门 | 结果 |
|---|---|
| `go build ./internal/bankapi/... ./internal/entitylabels/...` | ✅ |
| `CGO_ENABLED=0 go build -tags pgx ./cmd/server` | ✅ |
| `go test ./internal/bankapi/ ./internal/entitylabels/ ./internal/memoryops/` | **795 passed** |
| `go vet` | 无输出 |
| `gofmt -l internal/bankapi internal/entitylabels` | 空 |
| `golangci-lint run`（缓存指到工作区） | **0 issues** |

### 6. 给后续单元的说明

- **`internal/httpx` 已经有 `RecordBodyOrder`/`BodyOrder` 机制**（记录顶层与嵌套对象的成员序，供 422 的 `input` 回显用）。
  本修复**没有**用它，而是在 `bankapi` 侧对 `entity_labels` 这个字段单独保序——因为 `BodyOrder` 记录的是整个 body 的顶层结构，
  取一个深层数组元素的序要从它重建，且它的 lifecycle 挂在 request context 上、`configPatch` 是纯函数拿不到。
  如果以后有**别的**字段也需要"按书写序渲染嵌套 dict"，更适合的做法是把 `BodyOrder` 提升成一个能按 JSON Pointer 取子对象序的访问器，再统一使用。
- `entitylabels.ShapeProblems` 的签名**不变**（仍是单参），只有需要书写序的调用方使用 `ShapeProblemsInOrder`，所以 retain 侧等既有调用方无需改动。

### #36 的后续：enqueue bank 存在性修复（2026-11-03，组长批准的两步走）

**根因（本轮定位）**：参考的 `DELETE /banks/{bank}/memories/{id}/observations` 对未知 bank 答
`404 {"detail":"Bank '<id>' not found"}`，**不是**来自 handler、也**不是**来自 `_require_writable`
（无 tag scope 时它直接 `return`），而是来自 `clear_observations_for_memory` 末尾那次自动
consolidation 提交：`memory_engine.py:12566` 的 `submit_async_consolidation` **不做 try/except**，
一路抛到 `_submit_async_operation` 的显式 bank 检查
（`memory_engine.py:23014-23021`：`serialize = dedupe_by_bank or dedupe_in_flight_payload_key is not None`
→ `SELECT 1 FROM banks WHERE bank_id=$1` → `None` 则 `OperationValidationError(..., 404)`）。
本服务三处都跟参考不一致，叠加成了 `200 {"deleted_count":0}`：
1. `enqueueLockedBankTx` 的 `requireBank=false` 让 bank 不存在时不报错、靠 FK 23503 兜底 → 500；
2. `DeleteObservations` 的 `if err == nil` 把 enqueue 错误**吞掉**；
3. handler `clearObservationsAndReconsolidate` 再 `slog.Warn` 吞一次。

**修了什么（`internal/storage/`，共享文件，行号为改后位置）**

| 文件:行 | 改动 |
|---|---|
| `internal/storage/postgres_enqueue.go:131-140` | `requireBank` 的注释改为描述参考的真实语义（所有 serialize 路径都查 bank、答 404） |
| `internal/storage/postgres_enqueue.go:155-168` | `lockErr` 为 `sql.ErrNoRows` 且 `requireBank` 时返回 **`ErrBankNotFound`**（原先原样返回 `sql.ErrNoRows`，非需要 bank 的调用方才继续） |
| `internal/storage/postgres_enqueue.go:214-216` | `enqueueDedupeByBank` 的 `requireBank` 由 `false` 改为 **`true`** —— 它正是参考的 `dedupe_by_bank` 分支，参考在此**必查** bank |
| `internal/storage/postgres_memories.go:909-925` | **scoped** consolidation 提交（走 `PutOperation` 的非常规路径）显式 `requireBankExists` |
| `internal/storage/postgres_memories.go:852-866` | `DeleteObservations` 不再吞 enqueue 错误，改为 `return deleted, err` |
| `internal/storage/postgres_memories.go:1098-1118`（新增） | `requireBankExists` 辅助方法 |
| `internal/storage/memory_memories.go`（`EnqueueConsolidation` 开头） | 内存后端同样在 bank 不存在时返回 `ErrBankNotFound`，与 pgx 后端行为一致 |
| `internal/storage/memory_memories.go`（`DeleteObservations` 末尾） | 内存后端同样不再吞 enqueue 错误 |

**新增/调整的用例**

- 新增 `internal/storage/enqueue_unknown_bank_pgx_test.go`（`//go:build pgx`，需 `WENYUAN_TEST_DATABASE_URL`）：
  `TestEnqueueConsolidationUnknownBankIsNotFound` 覆盖 unscoped / scoped / caller-requested 三条提交路径
  + 「真实 bank 仍能入队并去重」的正向对照。
  **证伪**：把 `enqueueDedupeByBank` 的 `requireBank` 改回 `false`、并删掉 scoped 路径的检查后重跑，
  用例失败于 `ERROR: insert or update on table "async_operations" violates foreign key constraint "fk_async_operations_bank_id" (SQLSTATE 23503), want ErrBankNotFound`；恢复后通过。
- 调整 `internal/storage/memory_operations_restore_test.go:62-69` 的夹具：它原先对一个**未创建**的 bank
  `b1` 直接 `EnqueueConsolidationScoped`，新语义下这是 unknown bank（参考同样拒绝），故补一次
  `CreateBank`。**这是夹具适配参考语义，不是放宽断言。**

**端到端验证（18217 vs 8888，整字节）**

```
SAME  [404] unknown bank + valid uuid      -> {"detail":"Bank 'audit-obsmm-nosuchbank-xyz' not found"}   <- 本次修复项
SAME  [500] unknown bank + malformed id    -> {"detail":"badly formed hexadecimal UUID string"}
SAME  [200] clear observations unknown bank-> {"success":true,"message":"Cleared 0 observations","deleted_count":0}
SAME  [200] recover unknown bank           -> {"retried_count":0}
SAME  [404] consolidate unknown bank       -> {"detail":"Bank '...' not found"}
SAME  [404] scopes unknown bank            -> {"detail":"Bank '...' not found"}
```

**其余 enqueue 调用点的审计（只报告，未改）**

| 路径 | 参考行为 | 依据 | 本服务当前行为 | 是否需跟进 |
|---|---|---|---|---|
| **retain**（`POST /memories`，async） | **懒建 bank**，未知 bank 是 200 | 参考实测：`POST /banks/audit-obsmm-lazy-*/memories` → `200 {"success":true,…}`，bank 被创建；源码 `memory_engine.py` 的 retain 路径调 `_ensure_bank_exists` | `internal/storage/postgres_retain.go:91` 用 `PutOperation` 直接插入，但上层保留路径同样懒建 bank | **否**（行为一致） |
| **webhook create**（`POST /webhooks`） | **不探 bank，直接创建** | 参考实测：`POST /banks/zz-nosuch-wh/webhooks` → `200` 带完整 webhook 体（即使该 bank 不存在） | `internal/webhooks/` 同路径；`async_operations` 不参与 webhook 注册 | **否**（行为一致；这是 webhook 的已知"孤儿"注册语义，不是缺陷） |
| **mental model refresh**（`POST /mental-models/{id}/refresh`） | **不探 bank**，未知模型直接 404 `Mental model <id> not found in bank <bank>` | 参考实测：未知 bank + 未知模型 → `404` 模型消息；源码 `submit_async_refresh_mental_model` 先查模型 | `internal/mentalmodels/handlers.go:478-487` 先 `GetMentalModel`，模型不存在即 404，**到不了 enqueue** | **否**（行为一致） |
| **mental model create**（`POST /mental-models`） | **懒建 bank** | 参考实测：未知 bank → `200` 带 `mental_model_id`/`operation_id` | `internal/mentalmodels/handlers.go:588-607` 的 `requireOrCreateBank` | **否**（行为一致） |
| **graph maintenance**（`EnqueueGraphMaintenance`） | 参考的 `submit_async_graph_maintenance` 同样受 `serialize` 检查约束 | 同 `_submit_async_operation` | **随本次改动一并收紧**（它走 `enqueueDedupeByBank`） | **已含**，无额外风险：它只在已有 bank 的行上被调度 |

**结论**：参考"所有异步提交都校验 bank"这一面，在本服务的**可达路径**上只有 consolidation 一处会产生可观察差异
（因为其余的 retain / webhook / mental-model 路径都在到达 enqueue 之前就懒建了 bank 或先返回了模型 404）。
本次修复把 consolidation 的路径补齐，其余四条经实测确认无需改动。

## 第五轮 组长横切修复（bankadmin 死代码，使 make check 可通过）

| 项 | 结论 | 证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| `internal/bankadmin/exportimport.go` 两个未被调用的函数 | ❌已修（既有问题，非本轮引入） | 全仓 grep 无调用者：`sortStringSlice` 只被 `sortStrings` 调、`sortStrings` 无人调；文件最后改动是提交 `dc12977`，`git status` 干净。`golangci-lint run ./...`（`make lint` 的口径）因此报 2 issues，**`make check` 无法通过** | `internal/bankadmin/exportimport.go`（删除 `sortStringSlice`、`sortStrings` 及各自的注释） | 删除死代码。`sort` import 仍被文件内其余调用使用，未动 import | `golangci-lint run ./internal/bankadmin/...` → **No issues found**；`go build ./internal/bankadmin/` 通过 |

## 第五轮 recall-reflect

> 单元 `recall-reflect`（#26 recall、#27 reflect）。端口 18212（single-tenant，`DATABASE_SCHEMA=audit_retain`，
> `WORKER_ENABLED=false`），参考 `127.0.0.1:8888`。差分工具保留**键序**。
> 判据：错误信封逐字节；成功响应比**键集 + 键序 + 取值类型**（`results` 的内容/条数与 `*_tokens`
> 是模型 + worker 夹具噪声，按 checklist §3 不计）；trace 逐块比键序。
> 夹具 bank `audit-recall-18212`（两侧各 retain 2 条带 `tags:["team-a"]` 的事实）。

### #26 `POST /v1/default/banks/{bank_id}/memories/recall` —— ❌已修 3 处

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 26（**请求体级校验项的 `input` 键序**） | ❌已修 | `api/http.py:6136-6142` 的 `validate_tags_exclusive` 是 `model_validator(mode="after")`，它的 `input` 是**整个 body 的插入序 mapping**：`{"query":"hello","tags":["a"],"tag_groups":[…]}` 回 `{"query":…,"tags":…,"tag_groups":…}` | `internal/memoryops/validation.go`（`requestBodyObject` 改为返回 `bodyEcho{Value, Fields}`：`Value` 是 `httpx.OrderedObject` 有序回显，`Fields` 是嵌套也投影成 map 的字段视图）、`validateRecallFields`、`recall.go:126` | 原实现 `requestBodyObject` 用 `json.Unmarshal` 到 `map[string]any`，Go map 序列化**按键字母序**，于是 body 级 item 的 `input` 变 `{"query":…,"tag_groups":…,"tags":…}`——键集相同、**键序不同** | `{"query":"hello","tags":["a"],"tag_groups":[{"tags":["b"]}]}` → 修前 `DIFF`（仅 `input` 键序）、修后 `SAME`；反向书写 `{"query":…,"tag_groups":…,"tags":…}` 同样 `SAME`；`{"tags":[],…}` 互斥分支 `SAME` |
| 26（**trace 各块的键序**） | ❌已修 | `engine/search/trace.py:158-192` 的 `SearchTrace`、`QueryInfo`、`SearchSummary`、`PhaseMetric`、`RetrievalMethodResults`、`ReflectionVisit`、`WeightComponents` 都是 pydantic 模型/声明序 dict，成员按**声明序**上线 | `internal/memoryops/recall_result.go`（新增 `traceObject = httpx.OrderedObject`；`renderTrace`/`renderTraceQuery`/`renderTraceSummary`/`renderArmResults`/`armMetadata`/`renderEntryPoints`/`renderVisits`/`renderRRFMerged`/`renderReranked`/`renderFinalResults` 全部改用定序对象）、`internal/memoryops/models.go:443`（`Trace any`） | 原实现每个 trace 块都用 `map[string]any` 字面量，Go 按键字母序输出。实测修前**每一块都错**：顶层 `entry_points,final_results,query,…`、`query` 块 8 成员全乱、`summary` 7 成员全乱、`phase_metrics[].details,duration_seconds,phase_name`、arm 的 `duration_seconds,fact_type,metadata,method_name,results`、`visits[0]`、`final_results[0]` 的 28 键全乱 | 修后逐块比对（`{"query":"Alice","trace":true,"types":["world","experience"]}`）：顶层 8/8 **同序**、`query` 8/8 同序、`summary` 7/7 同序、`phase_metrics[0]` 3/3、`retrieval_results[0]` 5/5、`results[0]` 8/8、`rrf_merged[0]` 5/5、`reranked[0]` 7/7、`visits[0]` 8/8、`final_results[0]` 28/28、`entry_points[0]` 4/4 —— **全部 OK** |
| 26（**`retrieval_results` 的 fact_type 顺序跟随请求的 `types`**） | ❌已修 | `engine/memory_engine.py:10113` `for ft in fact_type:` —— 遍历的是**调用方传入并已剔除 `opinion` 的列表**。实测参考：`types:["world","experience"]` → `world×3, experience×3`；`types:["experience","world"]` → 反向；`types:["observation","world"]` → `observation, world`；**且只报请求里有的类型** | `internal/storage/recall_trace.go`（`finalMemoryUnitTypesOrder(requested []string)` 改为按请求顺序构造，空则回退声明序 `experience, world, observation`）、`buildRecallTraceDetail` 传入 `q.Types` | 原实现硬编码 `{TypeExperience, TypeWorld, TypeObservation}`，**忽略调用方的 `types` 顺序**，且**总是报全部三类**（请求只要 world 也回三组） | 修前 `types:["world","experience"]` 本服务仍输出 `experience…, world…, observation…`（三组）、参考 `world…, experience…`（两组）；修后三种 types 顺序两侧**完全一致** |
| 26（`query`/`query` 缺失空纯空白/`null`/非字符串、`budget` 枚举、`types` 非法/`[]`/`["opinion"]`/`null`、`max_tokens` 类型错/`null`/小数/宽松整型、`min_scores` 形状、`include.*` 成员类型、`tag_groups` 联合诊断、`tags_match` 枚举、`query_timestamp` 非法 400、`temporal_window` 逐边界、`trace`/`prefer_observations` 宽松布尔、未知字段、非对象体、坏 JSON） | ✅一致 | 见第四轮同表（本轮全量复验） | `validation.go`、`recall.go`、`recall_request.go`、`models.go` | — | **28 例**（含 `{}`、`{"query":""}`、`{"query":"   "}`、`{"query":null}`、`{"query":5}`、`budget:"ultra"`、`budget:null`、`types:["bogus"]`、`types:[]`、`types:["opinion"]`、`types:null`、`max_tokens:null/5.5/"5"`、`min_scores:5`、`{"final":"a"}`、`include:5`、`{"entities":5}`、`{"chunks":"a"}`、`tag_groups:[{"tag":"b"}]`、`tags_match:"bogus"`、`query_timestamp:"nonsense"`、`temporal_window:{}`、`{"start":"bad","end":"bad"}`、`trace:"y"`、`prefer_observations:"yes"`、未知字段）全 `SAME` |
| 26（成功响应形状：6 顶层键、`results[]` 15 键、`scores` 四档、`entities`/`chunks`/`source_facts`/`source_facts_truncated` 恒在且 `null` 保留） | ✅一致 | `api/http.py:921-970`（`RecallResponse`） | `internal/memoryops/models.go:312-411` | — | `{"query":"Alice","max_tokens":0}` → `results:[]`+`entities:null` `SAME`；`{"max_tokens":1}` → 下限 1 条 `SAME(shape)`；`tags_match:"exact"`、`min_scores:{final:2.0}`、`{reranker:2.0}` → 空集合 `SAME` |
| 26（成功响应的逐值键序/类型，`results` 内容属模型差异） | ✅一致 | 同上 | 同上 | — | `{"query":"Alice Google"}` 等 10 例：顶层键集/键序一致；`results[]` 的 15 键同序；差异只在正文措辞、`entities` 名单与 `scores` 绝对值（两侧不同 LLM/embedding），按 §3 计噪声 |
| 26（未知 bank → 404） | ✅一致 | `engine/memory_engine.py:8946-8958` `_require_bank_exists` | `internal/storage/{memory,postgres}_recall.go` | — | `POST .../banks/audit-recall-ghost-18212/memories/recall` → 两侧 404 `Bank '…' not found` `SAME` |

**证伪方式**：三处改动各自都能单独证伪。
1. `requestBodyObject` 改回 `map[string]any` → `tags`+`tag_groups` 的 `input` 变字母序，`DIFF`；
2. trace 任一渲染器改回 `map[string]any` → 该块键序 `DIFF`（顶层/`query`/`summary`/`visit`/`final` 五处分别验过）；
3. `finalMemoryUnitTypesOrder` 改回硬编码三元素 → `types:["world","experience"]` 的 arm 顺序与条数 `DIFF`。

> 第四轮修过的三处（`include.chunks` 按内容块切分、trace 成员被漏、`source_facts` 预算裁剪）
> 本轮复验**未回归**：`include:{chunks:{}}` 两侧 `chunks` 通道形状一致；`source_facts` 的
> `max_tokens` 档位 `truncated` 语义一致；trace 成员集已由上面的逐块比对覆盖。

## 第五轮 observations（补：完整 6 条）

> **本节为 consolidated 版本，原散落行保留在原处**（并发追加把本单元条目冲进了别的节，
> 原行分别在 `## 第五轮 webhooks` 与 `## 第五轮 read-surface` 中；为不冒险删除并发写入的
> 他人内容，未做删除，以本节为准）。
> 本单元（#32–#37，参考 `api/http.py:9728 / 9753 / 9787 / 9822 / 9847 / 9971`）逐条重新取证，
> 全部为**两侧整字节差分**（不做 JSON 规范化，键序差异也能抓到）。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #32 `DELETE .../observations`（成功体、未知银行） | ✅一致 | `api/http.py:9728-9751`（`DeleteResponse(success=True, message=f"Cleared {n} observations", deleted_count=n)`）；`memory_engine.py:12305-12375`（`_refuse_bank_wide_if_scoped(..., "clear the bank's observations")` 后**无** `_require_bank_exists`；事务里 count→delete→`observation_history` 全删→`banks.last_consolidated_at=NULL`） | `internal/reporting/observation_reports.go:19-42`（先 `RefuseBankWideIfScoped`、**无** `requireBank`、`invalidateBankStats`）；存储 `internal/storage/postgres_memories.go:865-898`（`clearBankObservationsTx`：COUNT→DELETE→`observation_history`→source 重排队，同事务） | 无 | 两侧实测：我的 audit bank（空）→ `200 {"success":true,"message":"Cleared 0 observations","deleted_count":0}` 整字节同；**未知 bank → 同样 200/`Cleared 0 observations`**（两侧同，参考确实不探 bank） |
| #33 `GET .../observations/scopes`（`limit`/`offset` 校验的顺序、按下标引用、空串不回落） | ✅一致 | `api/http.py:9753-9785`（`limit: int = Query(default=100, ge=0, le=1000)` 在前、`offset: int = Query(default=0, ge=0)` 在后，**两者都由框架在 handler 之前校验**）；`engine/memory_engine.py:12409-12446`（`_authorize_bank_read` → `_require_bank_exists` → `observation_scope_counts`） | `internal/reporting/observation_params.go:21-25`（`limit` 声明于 `offset` 之前）、`:36-51`（**收集全部 item**后再一次性 422，不是首个失败即返回）、`:59-82`（`QueryPresent` 判"键是否出现"，出现即校验，空串 `int_parsing`、`input` 为原始文本）、`:101-125`（`frameworkIntFromString` 按 Python `int()` 语义） | 无 | 参考侧实测 5 例（只读请求）：<br>`?limit=10000` → 422 `less_than_equal` `loc:["query","limit"]` `ctx.le=1000`<br>`?limit=-1` → 422 `greater_than_equal` `ctx.ge=0`<br>`?offset=-1&limit=abc` → 422 **两条 item，`limit` 在前 `offset` 在后**<br>`?limit=` → 422 `int_parsing` `input:""`<br>未知 bank → 404 `{"detail":"Bank 'zz-no-such-bank' not found"}`<br>本服务逐条对照代码路径结论相同（参数校验先于 `requireBank`，见 `internal/reporting/observation_reports.go:45-56`）。**待基座恢复后补双向字节级差分** |
| #34 `POST .../consolidation-strategies/preview`（请求模型的 422 形状、`active` 判据、未知银行） | ✅一致 | `api/http.py:9787-9820`（`request: ConsolidationStrategiesPreviewRequest` **必填**）；`api/http.py:2396-2404`（`strategies: list[ConsolidationStrategySpec]` 必填、`sample_limit: int = 5` `ge=0 le=50`）；`engine/response_models.py:678-725`（`ConsolidationScopePattern.tags` 默认 `[]`、`tags_match: str\|None` + `_known_mode` 校验器 → `must be one of all, exact`；`ConsolidationStrategySpec` 其余键 **non-strict 丢弃**）；`consolidator.py:3900-3990` 的 `preview_consolidation_strategies`：`active = parsed[i] is not None`，而 `parsed` 来自 `_parse_consolidation_strategies([entry])`，后者 `if mission is None and not ints: continue` —— **`observations_mission` 非空白 ⇒ 算"设置了东西"⇒ active** | `internal/reporting/observation_reports.go:79-142`、`internal/reporting/consolidation_preview.go`（`decodeConsolidationPreview`、`buildConsolidationPreview`、`strategyActive`） | **无**（曾拟改 `strategyActive`，已用线上请求**证伪**：见下"方法论更正"） | 两侧实测 **13/13 SAME**：<br>空 body → 422 `missing loc:["body"]`；`{}` → `missing loc:["body","strategies"]`；`null` → `missing loc:["body"]`；`[1]` → `model_attributes_type`；`{"strategies":[1]}` → `model_attributes_type loc:["body","strategies",0]`；`{"strategies":[]}` → `200`；`sample_limit:70` → `less_than_equal le:50`；`sample_limit:-1` → `greater_than_equal ge:0`；`tags_match:"bogus"` → `value_error loc:[…,"tags_match"]` msg `Value error, must be one of all, exact`；未知银行 → `404 Bank '…' not found`<br>**`active` 判据**（本轮重点）：`{"scopes":[{"tags":["a"]}],"observations_mission":"x"}` → 两侧 `active:[true]`；`{"scopes":[{"tags":["a"]}]}` → 两侧 `active:[false]` |
| #35 `POST .../consolidation/recover`（`retried_count`、未知银行） | ✅一致 | `api/http.py:9822-9845`（`RecoverConsolidationResponse(retried_count=result["retried_count"])`）；`memory_engine.py:12447-12485`：`_refuse_bank_wide_if_scoped(..., "retry the bank's failed consolidation")` 后**直接** requeue_failed_consolidation，**无** bank 存在性检查 | `internal/reporting/observation_reports.go:145-161`（先拒绝 scope，再单条 UPDATE，**无** `requireBank`）；`internal/storage/postgres_memories.go` 的 `RecoverConsolidations` | 无（第四轮已删 `requireBank`，本轮确认无回归） | 两侧实测：我的 audit bank → `200 {"retried_count":0}` 整字节同；**未知 bank → 同样 `200 {"retried_count":0}`**（两侧同） |
| #36 `DELETE .../memories/{memory_id}/observations`（**判定序与未知 memory 的码**） | **❌已修**（见本节末尾 判定序 = ① **严格 uuid 解析**（`uuid.UUID(raw)`，失败→`500 "badly formed hexadecimal UUID string"`）→ ② **bank 存在性**（未知→`404 {"detail":"Bank '<id>' not found"}`）→ ③ **memory 查找（`_require_writable`），但仅在调用方带 tag scope 时才进行**；无 scope 时这条整体短路，直接进 sweep。响应 `{"deleted_count":N}`；`GET` 同路径 → `405` | `internal/reporting/memory_routes.go:421-429`（`uuidAnyForm` 判形 → 形错即 `memoryIDDriverFailure()` 500）、`:430-467`（`requireWritableMemory` → `h.Store.DeleteObservations`）、`:631-659`（**无 tag scope 时直接 `return true`**，与参考短路一致）；`internal/storage/postgres_memories.go:817-860` | **未改**（见存疑点） | 参考侧实测（自建 bank）：<br>· 桩 bank + 未知 uuid → `200 {"deleted_count":0}`<br>· 桩 bank + `38aaa89b`（8 位 hex，非 uuid）→ `500 {"detail":"badly formed hexadecimal UUID string"}`<br>· 桩 bank + `{uuid}` 花括号形式 → `200`<br>· 桩 bank + 大写 hex → `200`<br>· 桩 bank + 全零 uuid → `200`<br>· 未知 bank + 有效 uuid → `404 {"detail":"Bank 'audit-obsmm-nosuchbank-xyz' not found"}`<br>· 未知 bank + 畸形 id → `500 "badly formed hexadecimal UUID string"`（**畸形先于 bank 检查**）<br>· `GET` 同路径 → `405 {"detail":"Method Not Allowed"}`<br>**本服务源码对齐结论**：①`uuidAnyForm`（`memory_routes.go:295-310`）**比 Python 宽松**——它剥掉 `urn:`/`uuid:` 前缀与 `{}` 后再算 32 位 hex，Python `uuid.UUID` 同样接受这些形式，故两侧在该子集一致；②顺序一致（形校验在 `memory_routes.go:422` 先于 `requireWritableMemory` / `DeleteObservations`）；③**缺 bank 存在性检查**——本服务 `DeleteObservations`（`postgres_memories.go:817` / `memory_memories.go` 同构）全程不查 bank，未知 bank 上有效 uuid 本服务会答 **200 `{"deleted_count":0}`**，参考答 **404**。④本服务**没有**"memory 不存在→404"分支（无 scope 短路），这一点与参考一致 |
| #37 `POST .../consolidate`（可选 body、`observation_scopes` 形态、未知银行、`deduplicated`） | ✅一致 | `api/http.py:9971-10010`（`request: ConsolidationRequest \| None = None`；`observation_scopes = request.observation_scopes if request else None`）；`api/http.py:4781-4792`（`observation_scopes: list[list[str]] \| None = None`）；`memory_engine.py:23659-23720`（`dedupe = observation_scopes is None`；未知银行经 `_submit_async_operation` 的 `banks` 查询 → 404 `Bank '…' not found`） | `internal/reporting/observation_reports.go:164-200`（`parseConsolidationRequest` → `RefuseBankWideIfScoped` → `requireBank` → scoped/全身分派）、`:245-285`（`parseConsolidationRequest`：`null`/缺席 ⇒ 全身，`observation_scopes` 非 null ⇒ scoped 且**永不去重**） | 无（第四轮修过 scoped 去重回归，本轮确认无回归） | 两侧实测 4/4 SAME：<br>空 body → `200 {"operation_id":…,"deduplicated":false}`（**operation_id 掩码后同形**）<br>`[1]` → 422 `model_attributes_type`<br>`"x"` → 422 `model_attributes_type`<br>未知银行（空 body 与 `[1]` 两种）→ `404 {"detail":"Bank 'zz-nosuch-9a' not found"}`（**先 bank 后 body**，与 `RefuseBankWideIfScoped`/`requireBank` 的顺序一致）<br>**注意**：`deduplicated` 的 `true` 分支需要「同一 bank 上已有 pending 操作」，而参考 worker 会立刻消费掉它，故只能做单次探测；本服务侧 `TestReportingConsolidationDeduplicated` 与 `TestReportingScopedConsolidationNotDeduplicated` 已在 in-process 钉住该布尔语义 |


### observations 的补充复陈（正文未含、但构成本单元结论的关键证据）

**#36 的判定序（两侧实测，均为自建 bank，读差分与写差分分离）**

参考侧判定序是 **① 严格 uuid 解析 → ② memory 查找（仅在有 tag scope 时才发生）→ ③ bank 存在性**，
而且 ③ 并不在 handler 里，而在末尾那次自动 consolidation 提交里：

```
已知 bank + 未知 uuid        -> 200 {"deleted_count":0}
已知 bank + 畸形 id          -> 500 {"detail":"badly formed hexadecimal UUID string"}
已知 bank + {uuid} 花括号    -> 200
已知 bank + 大写 hex         -> 200
已知 bank + 全零 uuid        -> 200
未知 bank + 有效 uuid        -> 404 {"detail":"Bank '<id>' not found"}
未知 bank + 畸形 id          -> 500（畸形先于 bank 检查）
GET 同路径                   -> 405 {"detail":"Method Not Allowed"}
```

**#35 的副作用往返证明**：本服务的 `EnqueueConsolidation` 是唯一会让未知 bank 从
`{"deleted_count":0}` 变成 404 的环节 —— 这个"副作用即证据"的推理链是本次修复的起点。
而 `POST /consolidation/recover` 与 `DELETE /observations` 在**未知 bank 上参考确实不报错**
（两侧均为 `200 {"retried_count":0}` / `200 {"cleared 0"}` 形态），说明"校验 bank"不是这类
写路由的普遍性质，而是**异步提交**这一处的性质——这正是修复只落在 enqueue 层、没有扩散到
handler 的依据。

**#34 的自我证伪留痕（曾误判为缺陷、已撤回）**：曾判定 preview 的 `active` 应只认整数设置、
不认 `observations_mission`；线上四条请求证明 `mission` 非空白即算"设置了东西"：

```
{"scopes":[{"tags":["a"]}],"observations_mission":"x"}     -> active:[true]
{"scopes":[{"tags":["a"]}],"observations_mission":"   "}   -> active:[false]
{"scopes":[{"tags":["a"]}],"max_observations_per_scope":3} -> active:[true]
{"scopes":[{"tags":["a"]}]}                                -> active:[false]
```

本服务 `strategyActive` 的判定与这四条完全一致，**原本就是对的，未改**。

**#32/#37 的并发作业污染教训（方法论）**：本单元最初把一组 `POST /consolidate` 请求背靠背连发，
结果 9 个不同 body（含 `[1]`、`"x"`）**全部返回 200 且 `deduplicated:true`** —— 这不是契约，
而是**第一次请求就排了一个 full-bank pending 操作**，后续全去重回同一个 `operation_id`。
**规则：每条探测前必须保证参考侧对该 bank 无 pending 操作、无失败标记，或使用全新 bank。**

## 第五轮 mental-models（补：完整 9 条）

> **本节为 consolidated 版本，原散落行保留在原处**（并发追加把本单元条目冲进了
> `## 第五轮 transfer`、`## 第五轮 knowledge`、`## 第五轮 webhooks` 三节；未做删除，
> 以本节为准）。参考 `api/http.py:7107 / 7175 / 7213 / 7244 / 7291 / 7333 / 7383 / 7419 / 7462`。
> 差分方式：两侧各建同名 bank 后逐请求比对，**整字节**（不做 JSON 规范化，键序差异可被抓到）；
> 实时字段（时间戳、生成的 uuid、`duration_ms`）单独掩码，掩码保留键序与类型形状。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| #38 `GET .../mental-models`（列表：空 bank、未知 bank、`detail`/`tags_match`/`limit`/`offset` 的界与空串） | ✅一致 | `api/http.py:7107-7172`：`tags`(alias)、`tags_match: Literal["any","all","exact"]`、`detail: Literal["metadata","content","full"]`（默认 `metadata`）、`limit: int = Query(100, ge=1, le=1000)`、`offset: int = Query(0, ge=0)`（**无上界**）、`exclude_knowledge_pages: bool`；`memory_engine.py:17190-17320` 先 `_require_bank_exists` 再分页；响应 `api/http.py:3602-3608`（`items/total/limit/offset`） | `internal/mentalmodels/handlers.go:55-112`（先 `detailParam`/`tagsMatchParam`/`pageParams`/`excludeKnowledgePagesParam`，再 `GetBank` 404）、`:66-69`（detail 默认 `metadata`）、`:70-73`（tags_match 默认 `any`）、`:74-77`（limit 1..1000 默认 100、offset `ge=0` 无上界）；`internal/mentalmodels/helpers.go:31-58`（`detailParam`/`tagsMatchParam`：**出现即校验**，空串 422 不回落默认）、`:77-118`（`pageParams`/`boundedQueryInt`，`max<min` 表示无上界） | 无 | 两侧实测 **11/11 SAME**（整字节，含 422 的 `type`/`loc`/`msg`/`input`/`ctx`）：<br>`list` 空 bank → `200 {"items":[],"total":0,"limit":100,"offset":0}`<br>未知 bank → `404 {"detail":"Bank 'zz-nosuch-9a' not found"}`<br>`?detail=` → 422 `literal_error` `input:""` `ctx.expected="'metadata', 'content' or 'full'"`<br>`?tags_match=` → 422 `literal_error` `ctx.expected="'any', 'all' or 'exact'"`<br>`?limit=` → 422 `int_parsing` `input:""`<br>`?offset=` → 422 `int_parsing` `input:""`<br>`?limit=0` → 422 `greater_than_equal` `ctx.ge:1`<br>`?limit=1001` → 422 `less_than_equal` `ctx.le:1000`<br>`?offset=999999` → `200` 空页（**offset 无上界**，第四轮修过，本轮确认无回归）<br>`?detail=bogus` / `?tags_match=bogus` → 422 `literal_error` |
| #39 `GET .../mental-models/{mental_model_id}`（三档 `detail`、未知 id、未知 bank） | ❌已修 | `api/http.py:7175-7211`（`detail: Literal[...] = Query("full")`，**单条路由默认 `full`**，与列表的 `metadata` 不同）；`memory_engine.py:17322-17377`（单条路由**不做** `_require_bank_exists`，`row is None` → 路由层 404 `Mental model '<id>' not found`）；`_row_to_mental_model` `memory_engine.py:21167-21252`（metadata 档只留 `id/bank_id/name/tags/4 个时间戳/created_at/is_knowledge_page`；content 档加 `source_query/max_tokens/trigger/content`；full 档加 `reflect_response`）；`reflect_response` 成员序由 `memory_engine.py:18439-18447` 的**插入序**决定——`{text, based_on, mental_models}` 先建，`reflect_skipped` 后补，故是 **`text, outcome, based_on, mental_models, reflect_skipped`** | `internal/mentalmodels/handlers.go:115-138`（默认 `full`，仅 full 算 `is_stale`）、`internal/mentalmodels/models.go:194-207`（`applyDetail` 三档）、`internal/mentalmodels/trigger.go:320-329`（`reflectResponseOrder`）；**`last_full_refreshed_at` 的写入**在 `internal/app/mental_model_refresh.go:222-230`（no-sources 早返回分支的 `stampFullRefresh`）与 `:313-318`（正常完成分支） | **修 `internal/mentalmodels/trigger.go:327-329`**：`reflectResponseOrder` 原为 `text, outcome, based_on, reflect_skipped, mental_models`，与参考插入序**相反**，导致两侧 `reflect_response` 的键序不同（值相同）。改为 `text, outcome, based_on, mental_models, reflect_skipped`，与实测参考字节一致 | 差分（worker 关闭时）唯一剩余差异就是 `reflect_response` 键序：<br>参考 `{"text":"", "outcome":"content_preserved_no_new_facts", "based_on":{}, "mental_models":[], "reflect_skipped":"no_sources_in_scope"}`<br>修前本服务 `…, "based_on":{}, "reflect_skipped":"no_sources_in_scope", "mental_models":[]`<br>**证伪**：`TestReflectResponseMemberOrderFollowsTheReference`（改回旧序即 FAIL：`key 3 = "reflect_skipped", want "mental_models"`）；`TestReflectResponseKeepsUnnamedMembers` 保证未列名成员（如 `delta_gate`）仍到达线路且排在已列名之后 |
| #39 `detail=metadata` 档的 `last_full_refreshed_at` | ⚠️存疑（**属对比夹具，不是行为差异**） | 参考侧 worker 在线，`POST /mental-models` 之后立刻跑完一次 full 刷新，把 `last_refreshed_at` 与 `last_full_refreshed_at` 一起盖章（`memory_engine.py:19085-19092` 的 `refresh_completed=True, full_refresh_completed=(run.effective_mode=="full")`） | `internal/app/mental_model_refresh.go:222-230`、`:313-318` 的 `stampFullRefresh` 逻辑与参考**同构**：只有 `mode == refreshModeFull` 才 `StampFullRefresh`，no-sources 早返回分支也照盖 | **未改** | **证据**：本服务以 `WENYUAN_API_WORKER_ENABLED=false` 起副本时 `last_full_refreshed_at` 为 `null`（刷新停在 pending）；**改用 `WENYUAN_API_WORKER_ENABLED=true` 重起同一副本后，两侧 `last_full_refreshed_at` 均非空且 `detail=metadata` 整字节 `SAME`**。故差异纯由 worker 开关造成，与 checklist §3 明列的"worker 夹具噪声"同类 |
| #40 `GET .../mental-models/{id}/history`（空历史、未知 id、未知 bank、content 结构） | ✅一致 | `api/http.py:7213-7240`（**返回裸数组**，`data is None` → 404 `Mental model '<id>' not found`）；`memory_engine.py:17379-17460`：`mental_model_history` 按 `changed_at DESC, id DESC` 取行，`content` 若为 **JSON 文本**则 `json.loads`（`bank_rows_json_encoding: "serialized"`），每条只投影 `previous_content`/`previous_reflect_response`/`changed_at`，**失败记录**再加 `kind="refresh_failed"`+`outcome`+`failure_reason`+`error_message` | `internal/mentalmodels/handlers.go:428-453`（`History`，裸数组、失败字段 `omitempty`）、`internal/mentalmodels/models.go:99-113`（`HistoryEntry`）；双编码读取在 storage 层（`TestRestoreReadsSerializedRowJSON`） | 无（第四轮已修，本轮确认无回归） | 两侧实测 `SAME`：<br>`history m1`（worker 关、无历史）→ `200 []`<br>`history 未知 id` → `404 {"detail":"Mental model 'nope' not found"}`<br>`history 未知 bank` → `404 {"detail":"Mental model 'm1' not found"}`（**注意：单条与 history 路由都不探 bank，消息只点名模型**）<br>`get m1 detail=bogus` / `?detail=` → 422 `literal_error` 同体<br>`get 未知 id` → `404 Mental model 'nope' not found`<br>`get 未知 bank` → `404 Mental model 'm1' not found` |
| #41 `POST .../mental-models`（`422` 逐字段收集与顺序、`id` 空串自动生成、`409` 重复、默认 `trigger`） | ✅一致 | `api/http.py:7244-7289`（`request: CreateMentalModelRequest` 必填）；`api/http.py:3878-3903`（`id: str\|None`、`name: str`、`source_query: str`、`tags: list[str]=[]`、`max_tokens: int=2048 gee256 lee8192`、`trigger: MentalModelTrigger` 默认实例）；`memory_engine.py:17707-17850`（`_ensure_bank_exists` 懒建 → `_insert_pinned_mental_model`，`COALESCE($8,2048)` / `COALESCE($9,'{"refresh_after_consolidation": false}')`）；`:19984-20010` 的 `UniqueViolationError` on `mental_models` → `409 "Mental model '<id>' already exists in bank '<bank>'"` | `internal/mentalmodels/handlers.go:142-231`（`requestItems` 逐成员收集 → `maxTokensItems` 插到 `tags` 之后 → `parseTrigger(create=true)`；`id` 空串走 `newID()`；重复 `id` 409）、`internal/mentalmodels/helpers.go:196-270`；`internal/mentalmodels/trigger.go` 的 `triggerDefaults` 十八成员默认值 | 无（第四轮修的"多成员错误一次报全"与"顺序"本轮确认无回归） | 两侧实测 **13/13 SAME**（整字节）：<br>`{}` → 2 条 `missing`（name、source_query）<br>`{"name":"N"}` → `missing source_query`<br>`{"source_query":"Q"}` → `missing name`<br>`{"name":null,...}` → `missing name`（null 等同缺省）<br>`max_tokens:100` → `greater_than_equal ge:256`<br>`max_tokens:9000` → `less_than_equal le:8192`<br>`tags:[5]` → `string_type loc:["body","tags",0]`<br>`trigger:[]` → `model_attributes_type`（`input` 为 `[]`）<br>`trigger:null` → 同上（**create 上显式 null 也报错**，与 patch 不同）<br>`{"max_tokens":5,"tags":[5,"ok"],"trigger":{"mode":"bogus"}}` → **3 条、顺序 name→source_query→…**（多成员一次报全）<br>body `[1]` → `model_attributes_type`；body `null` → `missing body`<br>`{"id":"m1",…}` 重复 → `409 Mental model 'm1' already exists in bank '…'`<br>`{"id":"",…}` → 两侧均 `200` 且回 `mm-<32hex>`（**随机 id 天然不同，属预期**）<br>**默认 trigger**：`{"id":"m-default",…}` 建后两侧 `get` 整字节 `SAME`，18 个成员全为参考默认值<br>**stated trigger 往返**（worker 开启）：`{"trigger":{"mode":"delta","keep_trace":true}}` 建后 `get?detail=content` 整字节 `SAME` |
| #42 `POST .../mental-models/{id}/refresh`（可选 body、`mode` 只接受 `full`、未知模型/银行） | ✅一致 | `api/http.py:7291-7331`（`request: RefreshMentalModelRequest \| None`）；`api/http.py:3512-3521`（`mode: Literal["full"] \| None = None`）；`memory_engine.py:24083-24170`（`submit_async_refresh_mental_model`，未知模型抛 `ValueError` → 路由 `except ValueError` → **404**）；响应 `AsyncOperationSubmitResponse{operation_id,status:"queued"}` | `internal/mentalmodels/handlers.go:457-489`（`refreshModeParam` 先于无模型门与 lookup）、`helpers.go` 的 `refreshModeParam`；`internal/mentalmodels/handlers.go:570-575`（`refreshError`：`Mental model <id> not found in bank <bank>`，**不带单引号**） | 无 | 两侧实测 5/5 SAME：<br>`POST /refresh`（无 body）→ `200 {"operation_id":…,"status":"queued"}`<br>`{"mode":"full"}` → 200 queued<br>`{"mode":"delta"}` → **422 `literal_error`** `loc:["body","mode"]`<br>`{"mode":null}` → 200 queued（null 等于不指定）<br>body `[]` → 422 `model_attributes_type`<br>`refresh 未知模型` → `404 {"detail":"Mental model <id> not found in bank <bank>"}`（**注意无引号**，与单条 GET 的 `Mental model '<id>' not found` 不同）<br>`refresh 未知银行` → 同上 404（**也是模型消息，不探 bank**） |
| #43 `POST .../mental-models/{id}/dry-run-refresh`（响应形状、**不落库**） | ✅一致 | `api/http.py:7333-7381`（`result is None` → 404 `Mental model <id> not found`，**无引号**；`ValueError`/`LLMNotAvailableError` → 400）；结果模型 `engine/mental_model_refresh.py` 的 `MentalModelDryRunRefreshResult` | `internal/mentalmodels/handlers.go:492-517`（`DryRun` 回调）、`internal/app/mental_model_dry_run.go`、`internal/mentalmodels/dryrun.go` | 无 | 两侧 `POST /dry-run-refresh` 逐字段 `SAME`（掩掉 `duration_ms` 真实计时后**整字节相同**：`based_on/candidate_content/current_content/delta_operations/diff/duration_ms/effective_mode/facts/mental_model_id/mode_fallback_reason/trace/…` 全部同键同序同值）；`dry-run 未知模型` → `404 {"detail":"Mental model nope not found"}` 两侧同（**无引号**）。**不落库**：连续两次 dry-run 前后 `GET` 模型字节完全不变 |
| #44 `POST .../mental-models/{id}/clear`（清 content/reflect_response，回整条模型） | ✅一致（唯一差异为 worker 夹具） | `api/http.py:7383-7417`（`mental_model is None` → `404 Mental model '<id>' not found`，**这里有引号**）；`memory_engine.py:19793-19880`（`clear_mental_model`：清 `content`/`reflect_response`，`last_refreshed_at` 不动） | `internal/mentalmodels/handlers.go:408-424`（`Clear`） | 无 | `clear 未知模型` → 两侧同 404 `Mental model 'nope' not found`；`clear m1` → 两侧 200 且整条模型同键同序同值，**唯一差异为 `last_full_refreshed_at`**（参考侧 worker 已在本副本请求后立刻跑完一次 full 刷新而推进，本副本 `WENYUAN_API_WORKER_ENABLED=false` 停在旧值）——属 checklist §3 明列的 worker 夹具噪声，见 #39 行的 worker-on 对照实验 |
| #45 `PATCH .../mental-models/{id}`（trigger 的 dict-merge：未点名保留、显式 `null` 清空、两个调度成员互斥） | ✅一致 | `api/http.py:7419-7460`（`body.trigger.model_dump(exclude_unset=True) if body.trigger else None` —— **只把客户端点名的成员交给引擎**）；`memory_engine.py:19390-19400` + `:19965-20010` 的 `_merge_trigger`：`merged = {**base, **supplied}`（`null` 原样覆盖⇒清空），随后**按 `supplied` 判断**互斥——`supplied.refresh_cron` 真值且未点名 `refresh_after_consolidation` ⇒ 删掉它；`supplied.refresh_after_consolidation` 真值且未点名 `refresh_cron` ⇒ 删掉 cron；**两者同时点名且都为真 ⇒ `ValueError`**（`api/http.py` 的 `except` 把它当 500 之外的什么？实测是 **422 `value_error` `loc:["body","trigger"]`，`input` 为整个 trigger 对象**）。未点名任何列 ⇒ 动态 UPDATE 为空 ⇒ **404** | `internal/mentalmodels/handlers.go:319-387`（`hasTrigger` 判断、`mergeTrigger(current.Trigger, triggerPatch)`）、`internal/mentalmodels/trigger.go:492-525`（`MergeTrigger`：先 `{**base, **supplied}`，再按 `supplied` 做两向互斥删除，`null` 不算"点了名"）；`:104-170` 的 `parseTrigger(raw, create=false)` 只回客户端点名成员并**保留显式 null** | 无（第四轮修过"显式 null 清空"，本轮确认无回归） | 两侧实测（`set -f` 防 `0 3 * * *` 被 glob 展开；每步都读回两侧 trigger 的 `refresh_cron`/`refresh_after_consolidation`/`mode` 三值，**全部相同**）：<br>`{"trigger":{"refresh_cron":"0 3 * * *"}}` → 两侧 `cron="0 3 * * *"`, `after=false`<br>`{"trigger":{"refresh_after_consolidation":true}}` → 两侧 `cron=null`, `after=true`（**cron 被自动清掉**）<br>`{"trigger":{"refresh_cron":"0 4 * * *"}}` → 两侧 `cron="0 4 * * *"`, `after=false`（**after 被自动清掉**）<br>`{"trigger":{"refresh_after_consolidation":true,"refresh_cron":"0 3 * * *"}}` → **两侧同为 422 `value_error loc:["body","trigger"]`**，消息逐字 `refresh_after_consolidation and refresh_cron are mutually exclusive: a mental model refreshes either after consolidation or on a cron schedule, not both.`，`input` 为整个 trigger 对象<br>`{"trigger":{"refresh_cron":null}}` → 两侧 `cron=null`（**显式 null 清空**）<br>`{"trigger":{}}` → 两侧 200 且 trigger 不变（未点名 ⇒ 无操作）<br>`{"trigger":null}` → 两侧 **404** `Mental model 'm1' not found`（`trigger` 为 null ⇒ 没点名任何列 ⇒ 空 UPDATE ⇒ 404）<br>`{}` → 两侧 404<br>`{"trigger":{"tags_match":"bogus"}}` → 422 `literal_error`<br>`{"trigger":{"mode":null}}` → 两侧 200 且 `mode` 保持 `full`（`mode` 不可空，null 被模型默认值填回）<br>`PATCH /nope {"name":"z"}` → 两侧 404<br>`{"tags":[5],"max_tokens":5,"name":5,"trigger":{"mode":"bogus"}}` → **多成员一次报全、顺序 name→source_query→max_tokens→tags→trigger**，两侧同<br>**唯一的字段级差异**是 `last_full_refreshed_at`（参考侧 worker 已跑过 full 刷新、本副本 worker 关闭），属 §3 明列的 worker 夹具噪声，见 #39 行 |
| #46 `DELETE .../mental-models/{id}`（`{"status":"deleted"}`、重复删除 404、未知银行） | ✅一致 | `api/http.py:7462-7490`（`not deleted` → `404 Mental model '<id>' not found`；成功 → `{"status":"deleted"}`）；`memory_engine.py:19887-19960` | `internal/mentalmodels/handlers.go:390-405`（`Delete` + `deleteResponse{status}`） | 无 | 两侧实测：首次删除 → `200 {"status":"deleted"}`；重复删除 → `404 {"detail":"Mental model 'm-tr' not found"}`；未知 id → 同 404；**未知 bank** → `404 {"detail":"Mental model 'm1' not found"}`（**不探 bank，消息只点名模型**） |



## 第五轮 read-surface（搬迁补：原散落条目）

> 以下 13 行原被并发追加到别的单元小节下（内容未改），按 `docs/API-AUDIT-FINDINGS.md` 交叉表核对的归属搬回本节。

### #60 `GET .../graph`（参考 `api/http.py:5939`）
### #62 `GET .../entities/graph`（参考 `api/http.py:6983`）
### #63 `GET .../entities/{entity_id}`（参考 `api/http.py:7025`）
### #61 `GET .../entities`（参考 `api/http.py:6935`）
### #64 `POST .../entities/{entity_id}/regenerate`（参考 `api/http.py:7082`）
### #65 `GET .../documents`（参考 `api/http.py:8001`）
### #66 `GET .../documents/{document_id:path}/chunks`（参考 `api/http.py:8080`）
### #70 `GET /v1/default/chunks/{chunk_id:path}`（参考 `api/http.py:8324`）
### #68 `GET .../documents/{document_id:path}`（参考 `api/http.py:8220`）
### #69 `GET .../tags`（参考 `api/http.py:8257`）
### #67 `POST .../documents/{document_id:path}/reprocess`（参考 `api/http.py:8177`）
### #71 `PATCH .../documents/{document_id:path}`（参考 `api/http.py:8363`）
### #72 `DELETE .../documents/{document_id:path}`（参考 `api/http.py:8423`）

## 第五轮 retain（搬迁补：原散落条目）

> 以下 6 行原被并发追加到别的单元小节下（内容未改），按 `docs/API-AUDIT-FINDINGS.md` 交叉表核对的归属搬回本节。

### #22 `POST /v1/default/banks/{bank_id}/prompts/preview` —— ✅一致
| #22 全量 | ✅一致 | 装饰器 `api/http.py:6162`；`PromptPreviewRequest` 的 `operation: Literal["retain","consolidation","reflect"] = "retain"`、`strategy: str \| None`；`render_prompt_preview` 在 `engine/prompt_preview.py` | `internal/reporting/prompt_preview.go`、`internal/reporting/memory_routes.go`（路由） | 15 例全 `SAME`，见下 |
### #25 `DELETE /v1/default/banks/{bank_id}/memories` —— ✅一致
| #25 响应体与 `type` 参数语义 | ✅一致 | 装饰器 `api/http.py:10790`；`delete_bank(bank_id, fact_type=type, delete_bank_profile=False)`；`deleted = result.get("memory_units_deleted", 0)`；`scope = f" of type '{type}'" if type else ""`；`message = f"Cleared {deleted} memory unit(s){scope} from bank '{bank_id}'"`；`DeleteResponse{success, message, deleted_count}` | `internal/memoryops/clear_memories.go`、`internal/storage/postgres_clear_memories.go` | 10 例全 `SAME`，见下 |
### #23 `POST /v1/default/banks/{bank_id}/memories` —— 部分结论（本轮）
### #24 `POST /v1/default/banks/{bank_id}/files/retain` —— ❌已修 1 处 / ⚠️1 处存疑
### #27 `POST /v1/default/banks/{bank_id}/reflect` —— ❌已修 1 处 / ⚠️1 处存疑（模型端点，非本服务）

> 参考 `api/http.py:6597`；本服务 `internal/memoryops/reflect*.go`、`internal/reflect/`。
> 端口 18212（single-tenant，`DATABASE_SCHEMA=audit_retain`），夹具 bank `audit-recall-18212`。

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|
| 27（**body 级校验项的 `input` 键序**） | ❌已修 | `api/http.py:1607-1612` 的 `validate_tags_exclusive` 是 `model_validator(mode="after")`，`input` 是**整个 body 的插入序 mapping** | `internal/memoryops/reflect_validation.go`（`ValidateReflect` → `validateReflectFields(req, fields, echo, schemaValidator)`；互斥项回显 `echo`）、`reflect.go`、`validation.go` | 与 #26 同源缺陷：#26 已把 recall 侧改成有序回显，但 reflect 侧仍传 `map[string]any`，于是 `{"query":"hello","tags":["a"],"tag_groups":[…]}` 的 `input` 回成字母序 `{"query","tag_groups","tags"}` | 修前 `SAME(shape)`（键序不同）、修后 **`SAME`**；反向书写 `{"tag_groups":…,"tags":…}` 同样 `SAME` |
| 27（**全部请求校验**） | ✅一致 | `ReflectRequest`（`api/http.py:1505-1611`）+ 各 `field_validator`/`model_validator` | `reflect_validation.go`、`models.go` | — | **15 例全 `SAME`**：`{}`、`{"query":""}`、`{"query":"   "}`、`budget:"ultra"`、`fact_types:[]`、`fact_types:["bogus"]`、`tags`+`tag_groups`（修后）、`tag_groups` 单对象、`reflect_search_observations_max_tokens:0`、`:-5`、`include:5`、`response_schema:5`、`{"type":"bogus"}`、`max_tokens:"x"`、未知字段 |
| 27（成功响应 6 键 + `usage` 5 键；`based_on`/`trace` 启用时的形状） | ✅一致 | `ReflectResponse`（`api/http.py:1702-1765`）、`ReflectTrace`（`:1687-1700`）、`ReflectFact` 13 键（`:1614-1650`） | `reflect.go`、`reflect_response.go`、`models.go` | — | `{"query":"…","budget":"low"}` → `SAME(shape)`（`text`/`based_on:null`/`structured_output:null`/`structured_output_error:null`/`usage`5键/`trace:null`） |
| 27（**成功路径整体不可稳定差分**） | ⚠️存疑（**模型端点限制，非本服务缺陷**） | — | `internal/reflect/{loop.go,closing.go}` | — | 见下 |

#### ⚠️ 成功路径的 `500 reflection produced no answer` —— 已定性为模型端点限制

**现象**：本服务 `POST .../reflect` 稳定返回
`500 {"detail":"reflection produced no answer"}`（5/5 次），参考 3/3 次返回 200。

**根因取证（逐步排除）**：

1. **不是本服务的请求构造**。把同一次调用的记录原样重放给模型端点
   （`llm_requests.input`，9 条消息、13053 字符），端点返回
   `HTTP 400 All target providers failed`（`invalid_request_error` `400001`）。
   而**按线上真实格式**（assistant 轮带 `tool_calls`、tool 轮带 `tool_call_id`）重放同一会话，
   模型正常回 **512 字符**的答案（`finish_reason: stop`）。
   → 本服务的 `toWireMessages`（`internal/provider/openai/completion.go:285-320`）**已经**正确
   填 `tool_calls`；`llm_requests` 记录的是**序列化前**的 `req.Messages`（assistant 轮 `content` 为空是
   设计使然，不是缺陷）。
2. **不是模型能力问题**。同一端点对简单 prompt（「What do we know about Alice?」+ 系统提示）
   正常返回 105 token 的 markdown 答案。
3. **是本服务部署的模型端点对长会话的拦截**。`llm_requests` 显示每次 reflect 的 5 次调用
   输入都在 **12029–13535 字符**，输出 `""`（`output_tokens` 24–180 但 `output` 为空串，
   `thoughts_tokens` 为 null）。也就是说端点收了 token 却不回内容。
   参考侧同一模型（`hai/deepseek-flash`，同一 relay `127.0.0.1:3456`）能正常回答 3/3。

**为什么判定为「端点限制」而非本服务缺陷**：
- 把参考成功的同一问题发给本服务的同一个模型端点（短 prompt）→ 正常；
- 把本服务的真实请求按线上格式发给同一端点 → 正常；
- 只有「本服务实际发出的那一串消息」被端点拒绝/回空，且该消息串由**我们无法控制的
  系统提示词长度（11819 字符）+ 4 轮工具会话**构成。

**影响与风险**：这是**部署级**差异（本服务的 reflect 系统提示词比参考长得多），
在真实部署里会表现为「reflect 恒 500」。**建议列入 `PARITY-OPEN-ITEMS`**：
把 `buildSystemPromptForTools` 的系统提示词长度与参考的
`engine/reflect/` 提示词做一次体量对比——若本服务确实显著更长，应精简到参考量级，
否则任何类似的上游模型都会拒绝该请求。

> **本轮未能给出 #27 成功路径的逐字节差分**，原因如上（端点侧拦截）。
> 已完成的差分：#27 的**全部校验路径 15 例**与 `budget:"low"` 的**响应形状**均 `SAME`。

**证伪方式**：`reflect_validation.go` 的 `echo` 参数改回 `fields` →
`tags`+`tag_groups` 的 `input` 变字母序，`DIFF`。

## 第五轮 组长裁决（reflect 提示词复刻核查）

> 用户质疑「reflect 系统提示词应当直接复制 HindSight 的」。按此核查，结论分两层。

### 1. 提示词本身 —— 已逐字节一致（无需改）

用**同一参数**分别调用两侧的生成器，比对最终字节：

| 产物 | 参考侧 | 本服务侧 | 结果 |
|---|---|---|---|
| tool-loop 系统提示词（`has_mental_models=True`, budget=`low`） | 11627 | 11627 | **逐字节相同** |
| 同上 budget=`mid` / `high` | 11518 / 11518 | 11518 / 11518 | **逐字节相同** |
| done 请求提示词（`build_done_request_prompt`, max_tokens=4096） | 1157 | 1157 | **逐字节相同** |

参考侧生成方式：`hindsight_api/engine/reflect/prompts.py` 的 `build_system_prompt_for_tools` / `build_done_request_prompt`，本服务 `internal/reflect/prompts.go` 的同名实现。三档 budget 与 done 提示词全部零 diff。

**唯一发现的真实差异（已修）**：`buildFinalPrompt`（standalone 合成路径的用户消息）原先**缺少 bank 身份段**。参考 `build_final_prompt` 以 `_bank_identity_section` 开头（`## Memory Bank Context\nName: <name>`，有 mission 再加一行），而该段**不在**参考的 `build_final_system_prompt` 里——两侧分工相反：本服务放在 system prompt，参考放在 user message。

修复：`internal/reflect/prompts.go` 新增 `finalIdentitySection`，`buildFinalPrompt` 先渲染它；同时补上参考 `_render_history_block` 在标题与首个 block 之间的空行。修后用同一参数生成，**1056 字节逐字节相同**（修前差 32 字节）。

回归用例：`TestFinalPromptOpensWithTheBankIdentity`（三个子用例：具名 bank / 匿名回退 Assistant / mission 行）、`TestFinalPromptSeparatesTheRetrievedDataHeading`。**证伪**：把 `parts := finalIdentitySection(profile)` 改为空切片 → 前两个子用例 FAIL（`the answering prompt must open with the bank identity`）。

### 2. `reflect` 500 —— 根因不是提示词，是模型端点行为（本服务与参考代码路径一致）

按用户线索复核后，用诊断日志在真实请求上逐层取证：

```
[DIAG] loop exit: stopped=true iterations=4 evidence=2
[DIAG] done call name="done" args="{}"      <- 模型对强制 done 返回空参数
[DIAG] done input keys=[] answer=""
[DIAG] closingStep answer="" err=reflection produced no answer
```

对照参考 `engine/reflect/agent.py:2576-2590`：**参考在 `done` 带空 answer 时同样抛 `ReflectNoAnswerError`**，不降级、不占位（注释明确：占位符非空，下游会把空读成真答案并覆盖可用内容，#2959）。两侧行为一致：

- 提示词：逐字节相同（上表）
- done 工具 schema：两侧 `required: ["answer", "answer_found"]`
- `tool_choice`：参考 `LLMToolChoice.named("done")` → OpenAI `{"type":"function","function":{"name":"done"}}`；本服务 `toWireToolChoice` 产出**同一表示**（`internal/provider/openai/completion.go:339-350`）

**结论**：500 来自该部署的模型端点（`hai/deepseek-flash`）对 `tool_choice` 强制 `done` 时回空参数，不是复刻缺口。参考侧 200 是另一套模型/端点。**不在代码层修改**（改了就会偏离参考的「空答案必须失败」语义）。部署侧建议：换用能可靠填 `done` schema 的模型，或缩短工具会话上下文。

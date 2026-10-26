# 逐 API 净室复刻核查（第三轮，2026-10-26）

> 本轮目标：把参考实现的 **100 条路由装饰器**逐条拆成工作单元，按 **API URL** 核对本服务
> （`/code/wenyuan`）的实现逻辑与参考实现（`/code/hindsight/hindsight-api-slim/hindsight_api/`）
> 是否**一致**；不一致的**一律按参考实现改**。判据是**两侧同请求的实测差分**，不接受"看起来差不多"。

## 1. 权威路由表

`docs/parity-round/routes.md` 是本轮的路由清单，由脚本从源码抽取、两侧核对后固化：

- 参考侧：`api/http.py` 的 `@app.{get,post,put,patch,delete}(...)` 装饰器，**100 条**
  （抽取器剥掉委托器的括号内注释与字符串内的括号，逐条给出 `file:line` 与 `operation_id`）。
- 本服务侧：`internal/httpx/openapi.json` 的 **99 条** operation（文档即契约，逐字节等于参考的
  `/openapi.json`；`GET /v1/version` 是第 100 条，`include_in_schema=False`）。
- 规范化后（`{x:path}` → `{x}`）两侧交集 **99 条**，参考独有 1 条（`GET /v1/version`），本服务独有 0 条。
- 本轮先核对路由表本身是否齐全：**是的**，100/100 都在本服务实现了。

## 2. 判"一致"的标准（按顺序，前面的优先）

1. **行为等价**：同输入 → 同输出（状态码 + 响应体的键集/键序/取值形状）+ 同副作用（写库的行与列）。
2. **代码路径等价**：在行为相同的条件下，参考源码里的分支、次序、SQL 谓词、边界值与本服务对应位置
   是否逐步对齐（用来捞出"当前夹具碰不到、但会在别的输入上分歧"的差异）。
3. **静态审查**：只在前两条都无法取证时使用，且必须在报告里注明"静态"。

**反向覆盖的硬性要求**：每条 API 至少给出**一个能真正走到该 handler 的实测差分**。
对 410 退役路由、需要 worker 的路由、需要模型的路由，"404/422" 不算证据——两侧都 404 说明请求
根本没到 handler，它的逻辑**未被验证**。这类情况必须在报告里写明"未覆盖"，由组长补 worker/模型。

## 3. 线上差分的方法

两侧都在跑：

| 角色 | 地址 | 鉴权 |
|---|---|---|
| 参考实现（`hindsight-api` 进程，v1.0.0） | `http://127.0.0.1:8888` | `Authorization: Bearer <reference-api-key>` |
| 本服务（各单元自己的端口 `183xx`） | `scripts/parity_dev_service.sh <端口>` 起在**本工作树** | 无需鉴权 |

单元检查器 `/tmp/rt/chk.py`（无状态，可直接调用）：

```sh
export PATH=/usr/local/go/bin:/data/go/bin:$PATH
export GOCACHE=/tmp/wc/gobuild GOLANGCI_LINT_CACHE=/tmp/wc/lint
export REF_KEY=<reference-api-key>
export SVC_BASE=http://127.0.0.1:18310          # 你自己的端口
export CHK_RUN=g-18310                          # 你独有的夹具后缀（必须改，别和别人撞）

python3 /tmp/rt/chk.py GET  '/v1/default/banks/{b}'
python3 /tmp/rt/chk.py POST '/v1/default/banks/{b}/memories/recall' '{"query":"x"}'
python3 /tmp/rt/chk.py GET  '/health' --headers-only     # 只比状态码与响应头
python3 /tmp/rt/chk.py POST '/x' --body-file /tmp/body.json
python3 /tmp/rt/chk.py PUT  '/x' '' --raw                 # 逐字节比 body（不解析 JSON）
```

`{b}` → 你的夹具银行 `rt-<CHK_RUN>`，`{b2}` → `rt-other-<CHK_RUN>`，`{ghost}` → `rt-ghost-<CHK_RUN>`（永不存在的银行）。
时间戳/UUID/32 位 hex/`mm-…`/`k[pf]-…` 会被归一；`*_tokens` 与少数活测量字段被归一为 `<N>`；
`features.{worker,audit_log,llm_trace,bank_llm_health}` 是部署自设项，被归一为 `<SETTING>`。

**夹具归属**：银行 `rt-<CHK_RUN>` 只归你所有，**绝不碰别人的银行，绝不碰非 `rt-` 前缀的银行**
（参考库里还有 46 个存量银行，任何写操作都不许落到它们身上）。结束前删掉自己的银行。

## 4. 已知噪声（不是缺陷，不要据此改代码）

- 两侧**不是同一个库**：参考读 `127.0.0.1:5432/hindsight` 的 `user_devin` schema，本服务读
  `127.0.0.1:15432/wenyuan`。带全局计数/聚合且不限夹具银行的响应（`GET /banks` 的 `total`、
  `/metrics` 的部分家族）会天然不同 —— **只在夹具银行范围内比对**。
- 活测量：`db_acquire_ms`、`uptime_seconds`、`seconds_since_last_poll`、各时间戳。
- 模型输出文本：两侧接的是**同一个** LLM/embedding/reranker（同一 `etc/wenyuan.yaml` 与参考 `.env`
  指向同一个中转），所以 `recall`/`reflect`/`dry-run` 的模型产物可以逐值比对；但模型本身有随机性，
  断言应落在**形状、键序、通道、分数类型与排序不变量**上，而不是逐字文本。
- 参考 worker 常开（会自行 consolidate），本服务默认 `PARITY_DEV_WORKER_ENABLED=false`。
  只看 worker 产物的字段（`consolidated_at`、`last_seen_at`、`is_stale` 之后的刷新结果）在无 worker 时
  本来就不同；需要这些字段的单元用 `PARITY_DEV_WORKER_ENABLED=true` 起一个**独占端口**的服务。

## 5. 工作单元与写作用域（互不重叠）

> 并行批次：**第一批** `monitor`、`bank-lifecycle`、`bank-config`、`retain`、`transfer`、`webhooks` 与
> `reporting` 部分；**第二批**在其后启动其余单元。`retain` 与 `recall-reflect` 共享 `internal/memoryops/`，
> 必须**不同时**进行（第二批做 `recall-reflect` 时 `retain` 已结束）。

| 单元 | API 编号（见 `routes.md`） | 写作用域 |
|---|---|---|
| `monitor` | 1–5 | `internal/monitoring/` |
| `bank-lifecycle` | 6–13 | `internal/bankapi/`（banks/aliases/decode/validation） |
| `bank-config` | 14–18 | `internal/bankapi/config*.go`、`internal/app/background.go`、`internal/banktemplate/` |
| `retain` | 19–23 | `internal/memoryops/`（retain/clear/file_retain/dry-run/prompts）、`internal/extraction/`、`internal/fileparse/` |
| `recall-reflect` | 24–25 | `internal/memoryops/`（recall/reflect）、`internal/reflect/` |
| `memory-crud` | 26–29 | `internal/reporting/memory*.go` |
| `observations` | 30–35 | `internal/reporting/`（observation/consolidation）、`internal/storage/*consolidat*`、`internal/consolidation/` |
| `mental-models` | 36–44 | `internal/mentalmodels/`、`internal/app/mental_model*.go` |
| `knowledge` | 45–52 | `internal/reporting/knowledge*.go` |
| `directives` | 53–57 | `internal/directives/` |
| `read-surface` | 58–70 | `internal/readsurface/`、`internal/entitylabels/` |
| `operations` | 71–75 | `internal/memoryops/operations*.go`、`internal/worker/` |
| `transfer` | 76–85 | `internal/transfer/`、`internal/hindsightarchive/`、`internal/app/transfer.go`、`internal/app/worker_transfer.go` |
| `webhooks` | 86–90 | `internal/webhooks/`、`internal/app/webhooks.go` |
| `reporting` | 91–97 + 100 | `internal/reporting/handlers.go`（stats/audit/llm 部分）、`internal/reporting/{stats,audit,llm}*.go`、`internal/audit/`、`internal/app/llm_trace.go` |
| 组长 | 跨单元 | `internal/httpx/`、`internal/app/app.go`、`internal/storage/`、`internal/config/` |

**跨单元公共文件**（`internal/app/app.go`、`internal/httpx/`、`internal/storage/`、`internal/config/`）：
单元可以读、可以出结论，**不要改**；把 `file:line` + 参考证据 + 建议改法写进报告，由组长统一改。

## 6. 报告约定

每单元只往 `docs/API-AUDIT-FINDINGS.md` **追加**自己的一节，标题固定：

```
## 第三轮 <单元名>
```

表头固定，**每条 API 一行**（编号与 URL 必须与 `routes.md` 一致）：

| # / API | 结论 | 参考证据 | 本服务落点 | 修复 | 验证 |
|---|---|---|---|---|---|

- 结论只用 `✅一致` / `❌已修` / `⚠️存疑` / `⛔未覆盖`。
- `✅一致` 必须有：参考 `file:line`、本服务 `file:line`、**至少一个实测差分结果（SAME）**。
- `❌已修` 必须有：**"改回去就 FAIL"** 的用例名 + 证伪方式（真的把代码改回去跑一次并贴输出）+ 差分结果。
- `⛔未覆盖` 要写清原因（需要 worker / 需要模型 / 需要对象存储 …），**不要用 404/422 冒充覆盖**。
- 报告末尾给出 `✅ x / ❌ y / ⚠️ z / ⛔ w`（按 API 计）与需要组长裁决的事项。

## 7. 验收

单元内：

```sh
go test -count=1 ./internal/<包>/...        # 全绿
go vet ./internal/<包>/...
gofmt -l internal/<包>                      # 必须为空
golangci-lint run ./internal/<包>/...       # 0 issues
```

改完全仓再跑一次：`CGO_ENABLED=0 go build ./...`、`CGO_ENABLED=0 go build -tags pgx ./...`（**pgx 标签不可省**）。
不提交 git（组长统一提交）；不改 `go.mod`；不新增依赖。

# 第四轮：补齐剩余 48 条 API（2026-10-26）

> 第三轮只开了 52 条单元，剩余 **48 条**未做逐条取证。本轮目标：**一条不落地查完**，
> 与参考不一致的**一律按参考改**。方法、差分工具与噪声清单沿用 `docs/parity-round/PLAN.md`（§2–§4）。

## 1. 本轮清单（48 条）

| # | API | 单元 |
|---|---|---|
| 19 | `PATCH /v1/default/banks/{bank_id}/config` | bank-config |
| 20 | `DELETE /v1/default/banks/{bank_id}/config` | bank-config |
| 26 | `POST /v1/default/banks/{bank_id}/memories/recall` | recall-reflect |
| 27 | `POST /v1/default/banks/{bank_id}/reflect` | recall-reflect |
| 28 | `GET /v1/default/banks/{bank_id}/memories/list` | memory-crud |
| 29 | `GET /v1/default/banks/{bank_id}/memories/{memory_id}` | memory-crud |
| 30 | `PATCH /v1/default/banks/{bank_id}/memories/{memory_id}` | memory-crud |
| 31 | `GET /v1/default/banks/{bank_id}/memories/{memory_id}/history` | memory-crud |
| 32 | `DELETE /v1/default/banks/{bank_id}/observations` | observations |
| 33 | `GET /v1/default/banks/{bank_id}/observations/scopes` | observations |
| 34 | `POST /v1/default/banks/{bank_id}/consolidation-strategies/preview` | observations |
| 35 | `POST /v1/default/banks/{bank_id}/consolidation/recover` | observations |
| 36 | `DELETE /v1/default/banks/{bank_id}/memories/{memory_id}/observations` | observations |
| 37 | `POST /v1/default/banks/{bank_id}/consolidate` | observations |
| 38–46 | `mental-models` 全族（list/get/history/create/refresh/dry-run-refresh/clear/patch/delete） | mental-models |
| 53 | `PATCH /v1/default/banks/{bank_id}/knowledge-base/nodes/{node_id}` | knowledge-extra |
| 54 | `DELETE /v1/default/banks/{bank_id}/knowledge-base/nodes/{node_id}` | knowledge-extra |
| 55–59 | `directives` 全族 | directives |
| 60 | `GET /v1/default/banks/{bank_id}/graph` | read-surface |
| 61 | `GET /v1/default/banks/{bank_id}/entities` | read-surface |
| 62 | `GET /v1/default/banks/{bank_id}/entities/graph` | read-surface |
| 63 | `GET /v1/default/banks/{bank_id}/entities/{entity_id}` | read-surface |
| 64 | `POST /v1/default/banks/{bank_id}/entities/{entity_id}/regenerate` | read-surface |
| 65 | `GET /v1/default/banks/{bank_id}/documents` | read-surface |
| 66 | `GET /v1/default/banks/{bank_id}/documents/{document_id}/chunks` | read-surface |
| 67 | `POST /v1/default/banks/{bank_id}/documents/{document_id}/reprocess` | read-surface |
| 68 | `GET /v1/default/banks/{bank_id}/documents/{document_id}` | read-surface |
| 69 | `GET /v1/default/banks/{bank_id}/tags` | read-surface |
| 70 | `GET /v1/default/chunks/{chunk_id}` | read-surface |
| 71 | `PATCH /v1/default/banks/{bank_id}/documents/{document_id}` | read-surface |
| 72 | `DELETE /v1/default/banks/{bank_id}/documents/{document_id}` | read-surface |
| 73–77 | `operations` list/get/cancel/retry/delete | operations |

## 2. 写作用域（互不重叠）

| 单元 | 编号 | 写作用域 |
|---|---|---|
| `bank-config` | 19–20 | `internal/bankapi/config*.go`、`internal/storage/*config*` |
| `recall-reflect` | 26–27 | `internal/memoryops/recall*.go`、`reflect*.go`、`internal/reflect/`、`internal/storage/*recall*` |
| `memory-crud` | 28–31 | `internal/reporting/memory*.go`、`internal/storage/{memory,postgres}_memories.go` |
| `observations` | 32–37 | `internal/reporting/{observations,consolidat}*.go`、`internal/consolidation/`、`internal/storage/*consolidat*` |
| `mental-models` | 38–46 | `internal/mentalmodels/`、`internal/app/mental_model*.go` |
| `knowledge-extra` | 53–54 | `internal/reporting/knowledge*.go` |
| `directives` | 55–59 | `internal/directives/` |
| `read-surface` | 60–72 | `internal/readsurface/`、`internal/entitylabels/` |
| `operations` | 73–77 | `internal/memoryops/operations*.go`、`internal/worker/` |
| 组长 | 公共 | `internal/httpx/`、`internal/app/app.go`、`internal/config/` |

**跨单元公共文件**：读、出结论、写报告，**不要改**；把 `file:line` + 参考证据 + 建议改法报给组长。

## 3. 端口与夹具（每人一个，不与第三轮冲突）

| 单元 | 端口 | CHK_RUN | 夹具银行 |
|---|---|---|---|
| bank-config | 18401 | bc4 | `rt-bc4` |
| recall-reflect | 18402 | rr4 | `rt-rr4` |
| memory-crud | 18403 | mc4 | `rt-mc4` |
| observations | 18404 | ob4 | `rt-ob4` |
| mental-models | 18405 | mm4 | `rt-mm4` |
| knowledge-extra | 18406 | kn4 | `rt-kn4` |
| directives | 18407 | di4 | `rt-di4` |
| read-surface | 18408 | rs4 | `rt-rs4` |
| operations | 18409 | op4 | `rt-op4` |

差分工具仍是 `/tmp/rt/chk.py`（PLAN.md §3 有用法）；每个单元 `export CHK_RUN=<上表>`。
**绝不碰别人的 `rt-*` 银行，绝不碰非 `rt-` 前缀的银行。**

## 4. 判据与交付（与第三轮相同）

- 判据顺序：行为等价 > 代码路径等价 > 静态审查（须注明"静态"）。
- **每条 API 至少一个真正走到 handler 的实测差分**；404/422 不算覆盖。
- 发现不一致 → 按参考改；每处带**"改回去就 FAIL"**的证伪（真跑一次并贴输出）。
- 报告追加到 `docs/API-AUDIT-FINDINGS.md`，标题 `## 第四轮 <单元名>`，**每条 API 一行**。
- 结论只用 `✅一致` / `❌已修` / `⚠️存疑` / `⛔未覆盖`。
- 验收：`go test ./internal/<包>/...`、`go vet`、`gofmt -l`、`golangci-lint`、两种 build。

## 5. 特别提示

- **worker 相关**：`mental-models` 的 refresh/clear、`operations` 的 retry、`observations` 的 consolidate
  都依赖 worker。需要时用 `PARITY_DEV_WORKER_ENABLED=true scripts/parity_dev_service.sh <端口>`（独占端口）。
- **已知噪声**（PLAN §4）：两侧不同库、活测量、模型输出文本、参考 worker 常开。
- **本轮之前刚修的**（若你的基线更早会看到差异）：observation 时间列聚合、同步 retain 不写 op 行、
  知识树两键排序、delivery cursor 一律 500、`prompts/preview` body、`%2F` 路径、bank 查询整数文法、
  超 int64 offset 饱和。

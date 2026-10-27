# 计划：把 HINDSIGHT 痕迹全部改为 WENYUAN

状态：**已完成**（2026-10-27）
拟定日期：2026-10-27
决策依据：用户选择「方案 B 硬切断」+「指标名也改成 wenyuan_*」+「不接受旧名」

---

## 0. 一句话概括

把 552 个 `HINDSIGHT_*` 环境变量名、约 45 个 `hindsight_*` Prometheus 指标名、
以及配套的 68 个 Go 文件、13 篇文档、模板与脚本，全部改成 `WENYUAN_*` / `wenyuan_*`；
**不保留旧名兼容**。这是一次显式的破坏性变更，所有既有部署必须同步迁移。

---

## 1. 现状调查（已完成）

### 1.1 环境变量

| 项目 | 数量 | 位置 |
|---|---|---|
| 不同变量名总计 | **552** | — |
| `HINDSIGHT_API_*` 出现次数 | **1025** | 全库 |
| Go 非测试文件 | **54** | 主体在 `internal/config/` |
| Go 测试文件 | **36** | 硬编码 `HINDSIGHT_API_*` |

**7 个非 `HINDSIGHT_API_` 前缀的变量**（需单独处理，不能靠统一前缀替换覆盖）：

| 变量名 | 位置 | 用途 |
|---|---|---|
| `HINDSIGHT_FS_DIR` | `internal/cli/fs.go:84` | CLI 文件系统根目录 |
| `HINDSIGHT_FS_INTERVAL` | `internal/cli/fs.go:115` | CLI 同步间隔 |
| `HINDSIGHT_BANK_ID` | `internal/cli/fs.go:100,125` | CLI 默认 bank |
| `HINDSIGHT_PROFILE` | `internal/cli/config.go:18` | CLI profile 名 |
| `HINDSIGHT_CP_DATAPLANE_API_URL` | `internal/cli/ui.go:24`、`frontend/instrumentation.ts:6`、`Makefile:21,169` | 控制台数据面地址 |
| `HINDSIGHT_CP_DATAPLANE_API_KEY` | `internal/cli/ui.go:25`、`frontend/instrumentation.ts:7` | 控制台数据面密钥 |
| `_HINDSIGHT_DAEMON_CHILD` | `internal/app/process.go:32` | daemon 子进程标记 |

### 1.2 配置机制（改名必须穿透的 6 处）

| 机制 | 文件 | 体积 | 性质 |
|---|---|---|---|
| 运行期 key 契约表 | `internal/config/reference.go` | 36.2K | 552 条中约 130 条，手动维护，每条带 Resolve/Reason |
| 运行期 key 常量 | `internal/config/reference_keys.go` | 231 行 | 手动维护 |
| Provider key 常量 | `internal/config/provider_env_keys.go` | 444 行 | **代码生成，DO NOT EDIT** |
| key 生成器 | `scripts/gen_provider_env_keys.py` | 5.1K | 正则 `HINDSIGHT_API_` 前缀，8 个 family marker 硬编码 |
| 文档生成器 | `scripts/render_config_reference.py` | 11.0K | `PREFIX = "HINDSIGHT_API_"`，生成 3 个产物 |
| YAML 前缀耦合 | `internal/config/configfile.go:30` | `envPrefix = "HINDSIGHT_API"` | YAML 键 = 环境名去前缀小写 |

**关键结论（YAML 兼容性）**：YAML 键 = 环境变量名去掉 `HINDSIGHT_API_` 后小写。
因为键体（`PORT`→`port`、`LLM_PROVIDER`→`llm_provider`）不变、**只有前缀变**，
所以 `etc/wenyuan.yaml` 里所有业务键**一个都不用改**。
唯一受影响的是 `HINDSIGHT_API_CONFIG_FILE` → `WENYUAN_API_CONFIG_FILE` 这个
「指向配置文件」的变量本身，以及 `etc/*.yaml` 注释里的说明文字。

### 1.3 Prometheus 指标

约 **45 个**指标名，分布在 9 个 Go 文件：

```
internal/monitoring/metrics.go          internal/monitoring/looplag.go
internal/monitoring/workload.go         internal/monitoring/process_metrics.go
internal/storage/observability.go       internal/app/metrics.go
internal/reporting/prompt_preview.go    internal/config/config.go
internal/memoryops/retain_defense.go    （+ 对应 _test.go）
```

命名族：`hindsight_http_*`、`hindsight_event_loop_*`、`hindsight_db_pool_*`、
`hindsight_process_*`、`hindsight_llm_*`、`hindsight_retain_*`、`hindsight_recall_*`、
`hindsight_consolidation_*`、`hindsight_operation_*`、`hindsight_async_operations`、
`hindsight_validator_*`、`hindsight_event_loop_*`。

**消费方（改了会断）**：

| 消费方 | 位置 | 影响 |
|---|---|---|
| contract smoke 断言 | `scripts/contract_smoke.sh:101-102` 用 `grep '^hindsight_http_requests_total'` | **会失败，需同步改** |
| 文档 | `docs/` 下 9 篇 + `CHANGELOG.md` | 需更新 |
| 配置模板 | `etc/wenyuan.yaml` | 注释里的指标名 |
| 外部仪表盘/告警 | 库外（Grafana 等） | **不在本仓库，需运维同步改** |

> 注：`hindsight_key`（`internal/memoryops/retain_defense.go:194`）**不是指标**，
> 是脱敏正则的**标签**，用于标记「被识别为上游 `hsk_...` 格式的密钥」。
> 本次**不动**——它描述的是「谁的密钥」，不是本服务自己的名字。

### 1.4 明确不动的东西

| 类别 | 具体 | 不动的原因 |
|---|---|---|
| HTTP 头 | `X-Hindsight-Event` / `-Signature` / `-Signature-V2` / `-Bank-Id` | **webhook 验签的线上契约**。接收方按这几个头名验签，改名会让所有已注册 webhook 失效 |
| OpenAPI 文档 | `internal/httpx/openapi.json` | `scripts/reference_openapi.py --check` 做字节级比对；它描述的是参考实现的 HTTP 面 |
| 脱敏标签 | `hindsight_key` | 描述的是「上游 `hsk_` 格式的密钥」，不是本服务名字 |
| Vendored 系统测试 | `tests/system/**` | AGENTS.md 记录为「参考实现黑盒套件原样 vendored」，是证据件。改了就不再是证据（见 §4.3 的替代方案） |
| 客户端的 `HINDSIGHT_API_URL` | `clients/*/` 集成测试读的地址 | **需要你确认**：见 §5 问题 1 |
| 前端 cookie | `hindsight_cp_access` / `hindsight_cp_locale` / `hindsight_cp_theme` | cookie 名属前端会话契约，且不在「后端服务环境变量/配置」范围 |
| `tests/system` 的 `hindsight_api` 包名 | `scripts/system_parity_plugin.py` | 那是上游 Python 包名，不是我们的 |

---

## 2. 方案 B 的具体设计

### 2.1 前缀映射

| 旧 | 新 |
|---|---|
| `HINDSIGHT_API_*` | `WENYUAN_API_*` |
| `HINDSIGHT_FS_DIR` | `WENYUAN_FS_DIR` |
| `HINDSIGHT_FS_INTERVAL` | `WENYUAN_FS_INTERVAL` |
| `HINDSIGHT_BANK_ID` | `WENYUAN_BANK_ID` |
| `HINDSIGHT_PROFILE` | `WENYUAN_PROFILE` |
| `HINDSIGHT_CP_DATAPLANE_API_URL` | `WENYUAN_CP_DATAPLANE_API_URL` |
| `HINDSIGHT_CP_DATAPLANE_API_KEY` | `WENYUAN_CP_DATAPLANE_API_KEY` |
| `_HINDSIGHT_DAEMON_CHILD` | `_WENYUAN_DAEMON_CHILD` |
| `HINDSIGHT_API_LLM_PROVIDER`（如实例子） | `WENYUAN_API_LLM_PROVIDER` |
| `hindsight_http_requests_total`（指标） | `wenyuan_http_requests_total` |

### 2.2 契约语义保持不变

`reference.go` 的「解析 or 拒绝，没有第三种结果」语义**原样保留**，
只是 `Name` 的值换了前缀。`Unsupported` 报错机制不变——设了一个本服务没实现的
key 仍然在启动时报错并指明 key 名（此时报的是新名字）。

### 2.3 新增迁移期断言

为了防「有人漏改一处导致某个 key 变成静默无效」，加一个测试：
扫描全库源码，若发现任何 `HINDSIGHT_API_` 字面量（排除 §1.4 白名单文件）即失败。
这样遗漏是**编译期/测试期可见**的，而不是运行时才发现。

---

## 3. 实施顺序（7 步，每步可独立验证）

| 步 | 内容 | 文件数 | 验证 |
|---|---|---|---|
| **P1** | `internal/config/`：`envPrefix`、`reference_keys.go`、`reference.go`、生成器前缀与 family marker | 5 | `go test ./internal/config` |
| **P2** | 重生成 `provider_env_keys.go`（跑脚本，不手改） | 1 | 脚本幂等，`gofmt` 干净 |
| **P3** | 其余 49 个非测试 Go 文件里的硬编码字符串 | ~49 | `make build && make pgx` |
| **P4** | 指标名 `hindsight_` → `wenyuan_`（9 个文件 + 对应测试） | ~14 | `go test ./internal/monitoring ./internal/app ./internal/storage` |
| **P5** | 36 个测试文件 | 36 | `make test` |
| **P6** | 重生成 3 个配置产物：`.env.example`、`etc/wenyuan.yaml`、`docs/CONFIGURATION.md` | 3 | 跑 `render_config_reference.py`，diff 复核 |
| **P7** | `Makefile`、`scripts/contract_smoke.sh`、13 篇文档、`AGENTS.md` 那张说明表 | ~18 | 全库残留扫描归零 |

### 每步的硬门禁

```sh
make check      # build + test + vet + pgx + lint
gofmt -l .      # 必须无输出
```

P7 结束后追加：全库 `HINDSIGHT_API_` 残留扫描（白名单外必须为 0）。

---

## 4. 风险与应对

### 4.1 【高】既有部署全部失效

改完后，一个带着 552 个 `HINDSIGHT_API_*` 的 `.env` 启动服务，**不会静默降级**——
会被 `reference.go` 的拒绝机制逐条拒绝（因为新名字下这些 key 属于「未知 key」）。
实际表现可能是：服务用自己的默认值起来了，但操作员的配置全部没生效。

**应对**：在 `LoadConfigFile` / `FromEnv` 入口加一个启动自检——扫描环境，
若发现任何 `HINDSIGHT_*` 变量，**启动失败并明确告知「请改用 WENYUAN_*」**。
这比静默忽略安全得多，也正是本仓库一贯的「拒绝优于静默」哲学。

### 4.2 【高】vendored 系统测试失效

`tests/system/hindsight_system_tests/server.py` 硬编码 27 处 `HINDSIGHT_API_*`
拉起被测服务 → 改名后这套套件起不来。

**三个选项，需你拍板**（见 §5 问题 2）：
- (a) 改 vendored 文件（丧失「原样」属性，但能跑）
- (b) 加一个翻译 shim（`scripts/` 里，把 `WENYUAN_*` 反译成上游要的 `HINDSIGHT_*` 注入子进程）
- (c) 该套件标记为「仅对参考实现有效」，不再对本服务运行

### 4.3 【中】指标改名破仪表盘

约 45 个指标改名后，任何既有 Grafana 面板/告警规则都读不到数据。

**应对**：在 `CHANGELOG.md` 显著位置列出完整的「旧名 → 新名」对照表，供运维批量替换。

### 4.4 【中】代码生成器的一致性

`provider_env_keys.go` 是生成物。**必须跑生成器重生成，不能手改**，
否则下次有人跑生成器就把改动冲掉了。

### 4.5 【低】`openapi.json` 里残留 `HINDSIGHT_API_*` 文案

`description` 里仍有 `HINDSIGHT_API_ENABLE_BANK_LLM_HEALTH=false` 这类文字。
那份文档是字节级校验的，**不改**。但它是「本服务文档里仍出现 HINDSIGHT」的残留点，
需要在 AGENTS.md 里说明清楚，避免以后有人当成漏改。

---

## 5. 需要你确认的 3 个问题

### 问题 1：`clients/*` 生成 SDK 里的 `HINDSIGHT_API_URL`

四个 SDK（Go/Python/TS/Rust）的集成测试读 `HINDSIGHT_API_URL` 指向被测服务。
它们**不是后端服务的配置**，而是测试环境变量。改还是不改？
（我倾向**不改**——保持 SDK 用例与外部世界一致，但列出来你定。）

### 问题 2：`tests/system/**` 怎么处理？

见 §4.2 的 (a)/(b)/(c) 三选一。

### 问题 3：前端 `frontend/instrumentation.ts` 的两个变量

`HINDSIGHT_CP_DATAPLANE_API_URL` / `_API_KEY` 同时被 `internal/cli/ui.go` 和
前端读取。这是**前后端共享的部署配置**，改名要一起改。确认一并改？

---

## 6. 回滚

单次提交内含全部改名 + 重生成的产物。回滚 = `git revert` 该提交，
不需要数据迁移（数据库里没有任何 `HINDSIGHT` 字符串参与主键/枚举）。
唯一残留风险：已写入 `llm_requests` / `audit_log` 等表的**日志文本**里可能
含旧变量名，但那只是历史记录，不影响行为。
---

## 7. 实施结果（2026-10-27 完成）

### 最终用户决策（覆盖 §5 的三问）
1. `HINDSIGHT_API_URL` → **`WENYUAN_API_URL`**（clients/* SDK 同步改）
2. `tests/system/**` 及所有硬编码 —— **全部改**，包括原本标注「原样 vendored」的部分
3. `WENYUAN_CP_DATAPLANE_API_URL/API_KEY` **一并改**（CLI + frontend + Makefile）

### 实际改动范围
| 类别 | 结果 |
|---|---|
| Go 源码 | 全部 `HINDSIGHT_` → `WENYUAN_`（含 552 个变量名、36+ 测试文件） |
| Prometheus 指标 | ~45 个 `hindsight_*` → `wenyuan_*`，含 `MetricsScopeName` → `wenyuan_api.metrics` |
| 生成器 | `scripts/gen_provider_env_keys.py` 引入 `SOURCE_PREFIX`/`TARGET_PREFIX`，从上游读、按我方拼 |
| 配置产物 | 跑 `render_config_reference.py` 重生成 `.env.example`、`etc/example.wenyuan.yaml`、`docs/CONFIGURATION.md` |
| 外部面 | Makefile、scripts/*.sh、frontend（含 10 个 locale catalog）、integrations/coding-agents、四个 SDK |
| 文档 | 13+ 篇 docs、AGENTS.md 命名表、README ×2、CHANGELOG |
| openapi.json | 6 处描述文案内的变量名同步改（结构未动，`derive_client_spec.py --check` 通过） |

### 仍然保留 `HINDSIGHT` 的三处（均为刻意，非遗漏）
| 位置 | 原因 |
|---|---|
| `scripts/gen_provider_env_keys.py` 的 `SOURCE_PREFIX` | 生成器要**读取上游**声明（上游用 `HINDSIGHT_API_`），再按 `TARGET_PREFIX` 输出 |
| `internal/httpx/openapi.json` 之外的契约串：`X-Hindsight-*` 头、`hindsight_key` 脱敏标签、`hindsight_api.extensions.builtin.tenant`、`~/.hindsight/daemon.log` | HTTP/webhook/脱敏/扩展部署契约，改了会破已注册 webhook 与既有部署 |
| `internal/hindsightarchive`、`internal/migration/hindsight`、`bankctl --from-hindsight` | 包/参数名指向「参考实现的导出格式」与导入方向，是语义命名 |
| 本文档 | 记录改名本身，必然出现旧名 |

### 验证门（全部通过）
```
go build ./...                       ✓
CGO_ENABLED=0 go build -tags pgx ./... ✓
go vet ./...                         ✓
go test -count=1 ./...               ✓  (0 failures)
golangci-lint run ./...              ✓  (0 issues)
gofmt -l <project sources>           ✓  (empty)
derive_client_spec.py --check        ✓
```

### 部署迁移须知（破坏性变更）
既有部署必须把所有 `HINDSIGHT_*` 环境变量改名为 `WENYUAN_*`。
新代码不再识别旧名——旧变量会被当作未知键，配置**不会生效**。
指标名同步改名，Grafana 面板与告警规则需批量替换：

```sh
# 示例：替换既有 .env
sed -i 's/^HINDSIGHT_/WENYUAN_/' .env

# 示例：Prometheus 规则
sed -i 's/\bhindsight_/wenyuan_/g' alerts.yml dashboards/*.json
```

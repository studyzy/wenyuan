# 从 Hindsight 迁移到 Wenyuan

本文档是**运维手册**：把一套已有的 Hindsight 服务切换到 Wenyuan，并保留全部记忆数据。

> 适用范围：已有 Hindsight 实例及其 PostgreSQL 数据库中有真实记忆数据，希望用 Wenyuan 替换它。
>
> 见 §1：目标库已经是 Hindsight 建好的库时，直接接管即可（§1.1）；
> 目标库是空库时按 §1.2 的导出/导入搬运数据。

---

## 1. 迁移原理

有两条路径，按**目标库的现状**选择：

| 目标库现状 | 路径 | 数据 |
|---|---|---|
| 已经由 Hindsight 建好 schema（同一套 schema） | **直接接管**（adoption）：Wenyuan 启动时读出目录、认定这就是它服务的那套 schema，把迁移系列记为已应用，**不重建任何对象** | 原地保留，无需搬运 |
| 空库，或 schema 不是 Wenyuan 服务的那一套 | **导出/导入**：用 Hindsight 官方整库导出格式搬运数据 | ZIP 搬运 |

> 前提结论：Wenyuan 的数据库 schema 与 Hindsight **一致**（由参考库目录逐对象生成，
> 见 `docs/SCHEMA-ALIGNMENT.md`），因此已建好的 Hindsight 库可以被直接接管；
> 导出/导入只在目标库是空库、或要搬到另一台数据库时才需要。

### 1.1 直接接管已有的数据库（adoption）

Hindsight 用 `alembic_version` 记录自己的迁移状态，Wenyuan 用 `schema_migrations` 记录自己的；
两边表名不同，所以 Wenyuan 启动时**不看对方的账本，而是读目录**：

1. 连接后先装好扩展（`vector` / `pg_trgm` / `btree_gin`），再在**一个事务 + advisory lock** 内做判断，
   多个进程同时启动也不会互相踩到。
2. 把目标 schema 的**表、列、约束、函数**与迁移系列会创建的对象清单逐项比对：

   | 目录现状 | 行为 |
   |---|---|
   | 系列的对象一个都不在（空库） | 正常执行整个迁移系列 |
   | 系列的对象全在（已有 Hindsight 库或已有 Wenyuan 库） | **接管**：把整个系列写进 `schema_migrations`，不创建任何对象，并打一条 INFO 日志 |
   | 只在一部分（半截 schema，或 Hindsight 版本与 Wenyuan 服务的那套不一致） | **报错退出**，错误里点名第一个缺失的对象；不改动数据库 |

   接管**只可能发生在"齐全"这一格**，所以它不会成为跳过工作的后门：空库照样跑系列，
   半截库一律拒绝（既不能接管，也不能把系列盖上去——后者会撞上已存在的对象）。

3. 索引不参与"是否接管"的判断，但缺失的索引会以 WARN 日志点名。
   原因：Hindsight 的向量索引不在它的迁移系列里——同一个 alembic 版本的库里，
   同名列上的 HNSW 索引名称可以不同，说明它们是运行期建的。为了一个索引拒绝启动，
   或者在启动时锁住一张线上的大表去建索引，都比"先不带它服务"更糟。

接管后的 `schema_migrations` 就是这台库的正常账本：以后的启动只会比对账本，
不再重复读目录。日志形态：

```text
INFO adopted the existing schema instead of re-creating it schema=public recorded_migrations=5 total_migrations=5 latest_version=5 alembic_revision=6ab9509eeb56
```

### 1.2 导出/导入（目标库是空库时）

Hindsight 提供一个带版本号的整库导出格式（`SCHEMA_VERSION = 1`）：

- 接口：`POST /v1/default/banks/{bank_id}/export` 产出 ZIP；
- 内容：`manifest.json` + `documents/NNNNNN.json`（每个文档含 chunks 与 facts）；
- 关键点：导出的事实**不含向量**（上游导入端会重新 embedding），
  因此迁移不搬运向量数据 —— 这与当前 Wenyuan 无向量列的实现一致。

Wenyuan 侧读取该 ZIP，映射为自身的 schema 并写入。

---

## 2. 迁移前检查

- [ ] Wenyuan 二进制版本已知（`bankctl` 与 `server` 同版本）。
- [ ] 目标 PostgreSQL 可达，且 Wenyuan 有建表权限。
- [ ] 已确认要迁移的 `bank_id` 列表。
- [ ] **规划停机窗口**（见 §5）。
- [ ] 若目标库已有同名 bank，确认用 `--replace` 还是 `--append`（见 §4）。

---

## 3. 操作步骤

### 3.1 从 Hindsight 导出

```sh
curl -X POST \
  "http://<hindsight-host>:8888/v1/default/banks/<bank_id>/export" \
  -H "Authorization: Bearer <token>" \
  -o "hindsight-<bank_id>.zip"
```

对每个 bank 重复。

### 3.2 在 Wenyuan 上预演（不写库）

```sh
bankctl bank migrate --archive hindsight-<bank_id>.zip --dry-run
```

输出示例：

```
would migrate <bank_id> from hindsight-<bank_id>.zip (replace mode)
  documents: 42
  chunks: 318
  facts: 1204
  entities: 233
  links: 57
  archive fields with no target column (preserved in metadata where possible):
    - fact.source_id (archive-internal fact identity; not persisted)
    - fact.observation_scopes (no column; regrouped on the target)
    - fact.consolidated_at / consolidation_failed_at (no column)
    - document.retain_params (no column on the target document)
```

**核对计数**与 Hindsight 侧的实际数量一致，并确认"未映射字段"清单可接受。

### 3.3 正式导入

```sh
# 幂等重建（推荐）：先清空目标 bank 再写入，可重复执行
bankctl bank migrate --archive hindsight-<bank_id>.zip --replace

# 追加合并：保留目标 bank 已有行
bankctl bank migrate --archive hindsight-<bank_id>.zip --append

# 指定目标 bank id（默认用导出里的 source_bank_id）
bankctl bank migrate --archive hindsight-<bank_id>.zip --bank-id <new-id>
```

### 3.4 验证

```sh
# 起 Wenyuan（或指向目标 PG 并用 API）
./bin/wenyuan --port 18888

# 抽查一条记忆能否 recall
curl -X POST "http://127.0.0.1:18888/v1/default/banks/<bank_id>/memories/recall" \
  -H "Content-Type: application/json" \
  -d '{"query":"<一个已知出现在记忆里的关键词>"}'
```

确认：返回的 results 非空、文本与预期一致、`occurred_start` / `context` 等字段保留
（`context`/`event_date` 由迁移 `0006` 引入的列承载）。

---

## 4. `--replace` vs `--append`

| 场景 | 用法 | 说明 |
|---|---|---|
| 首次迁移 / 重试失败 | `--replace` | 先 `DeleteBank` 再写入，**幂等**；重复跑结果一致 |
| 合并到已有数据的 bank | `--append` | 保留已有行，追加导出中的行 |
| 目标 bank 不存在 | 两者皆可 | 都会创建 bank（默认名 = bank_id，中性 disposition = 3） |

**警告**：`--replace` 会**删除目标 bank 的既有全部数据**。对已有数据的 bank 慎用。

---

## 5. 停机窗口与一致性

迁移是**快照式**的，不做增量同步。

推荐流程：
1. 停止 Hindsight 的写入（或整体下线旧实例）。
2. 执行导出（此时数据静止，导出完整）。
3. 执行 Wenyuan 导入。
4. 验证后，把客户端指向 Wenyuan。

若在导出后旧实例仍在写入，则**迁移期间新增的数据会丢失** —— 必须避免。

如需在线迁移，需要额外实现增量同步（当前**未实现**，见 `docs/ROADMAP.md` §2.2）。

---

## 6. 迁移后的已知差异

迁移保证**记忆内容**（文档、分块、事实文本、时间、标签、实体关联、因果关系）保留。
以下为已知差异：

1. **向量/语义检索**：导出不含向量，Wenyuan 的语义检索是占位实现
   （见 `docs/ROADMAP.md` §1.1）。迁移后 recall 的排序质量与 Hindsight 存在差距。
2. **部分字段无列**：`source_id`、`observation_scopes`、`consolidated_at`、
   `consolidation_failed_at`、`document.retain_params` 未落库（迁移结束会打印清单）。
3. **实体 id 是派生的**：按 `bank_id + name` 哈希生成；同名实体会合并。
4. **异步操作记录**：`async_operations` 不随本次迁移携带（导出格式中属于可选 scope）。

---

## 7. 回滚

迁移**不修改源 Hindsight 库**，源数据始终可用。回滚 = 把客户端指回旧 Hindsight 实例。

Wenyuan 侧若需撤销某个 bank 的导入：
```sh
# 删除该 bank 及其全部内容
curl -X DELETE "http://<wenyuan>:18888/v1/default/banks/<bank_id>" \
  -H "Authorization: Bearer <token>"
```

---

## 8. 故障排查

| 现象 | 可能原因 | 处理 |
|---|---|---|
| `relation "async_operations" already exists` | 目标库已有一套 schema，但这版 Wenyuan 没识别它就直接跑了迁移系列 | 用带直接接管（§1.1）的版本；库若已被写进半截账本（例如只记到 `0001`），新版本会读目录后补齐记录，不会重复建表 |
| `database is not the schema this build serves: ... the first missing one is ...` | 目标库的表/列/约束/函数与 Wenyuan 服务的这套 schema 不一致：可能是半截 schema，或 Hindsight 版本比 Wenyuan 的系列更旧 | 用官方导出把数据搬到空库（§1.2）；不要用 `--replace` 之外的写操作去凑 schema |
| `unsupported archive schema_version N` | 上游导出格式版本变了 | 升级 Wenyuan，同步 `internal/hindsightarchive/archive.go` 的 `SupportedSchemaVersion` 与类型 |
| `archive is missing manifest.json` | ZIP 不完整或不是整库导出 | 重新导出；确认用的是 `/export` 而非 `/document-transfer` |
| 导入后 recall 为空 | 目标 bank id 与查询的不一致 | 确认 `--bank-id`；`GET /v1/default/banks` 列出实际 id |
| 实体数量异常少 | 多个同名实体被合并 | 预期行为（见 §6.3）；如需区分，源侧需有不同 canonical name |
| `migration series has a gap` | 自行加了跳号的迁移文件 | 迁移版本必须从 1 连续到 N（`internal/storage/migrate.go`） |

---

## 9. 相关代码

- 迁移系列与执行：`internal/storage/migrate.go`（`MigrationSeries`、语句切分、版本）、
  `internal/storage/postgres.go`（`Migrate`：advisory lock 内执行整个系列）
- 系列会创建的对象清单：`internal/storage/schema_expected.go`
  （从嵌入式 SQL 读回，不另写一份与之漂移）
- 目录识别与接管：`internal/storage/postgres_adoption.go`
  （`readCatalogue`、`classifySchema`、`adoptSeries`）
- 归档解析：`internal/hindsightarchive/archive.go`（类型与 `Read`）、`zip.go`（`OpenZip`）
- 映射：`internal/migration/hindsight/map.go`（字段映射、id 派生、未映射清单）
- 导入：`internal/migration/hindsight/run.go`（`Run`、`Replace`/`Append`、事务写入）
- CLI：`internal/bankadmin/migrate.go`、`cmd/bankctl/main.go`
- 测试：`internal/hindsightarchive/archive_test.go`、
  `internal/migration/hindsight/run_test.go`（含构造导出 ZIP 的端到端用例）
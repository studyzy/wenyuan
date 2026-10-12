# 从 Hindsight 迁移到 Wenyuan

本文档是**运维手册**：把一套已有的 Hindsight 服务切换到 Wenyuan，并保留全部记忆数据。

> 适用范围：已有 Hindsight 实例及其 PostgreSQL 数据库中有真实记忆数据，希望用 Wenyuan 替换它。
>
> 前提结论：Wenyuan 的数据库 schema 与 Hindsight **不同**，不能直接指向同一个库。
> 迁移通过 Hindsight 的**官方整库导出格式**完成。

---

## 1. 迁移原理

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
./bin/wenyuan --port 8888

# 抽查一条记忆能否 recall
curl -X POST "http://127.0.0.1:8888/v1/default/banks/<bank_id>/memories/recall" \
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
curl -X DELETE "http://<wenyuan>:8888/v1/default/banks/<bank_id>" \
  -H "Authorization: Bearer <token>"
```

---

## 8. 故障排查

| 现象 | 可能原因 | 处理 |
|---|---|---|
| `unsupported archive schema_version N` | 上游导出格式版本变了 | 升级 Wenyuan，同步 `internal/hindsightarchive/archive.go` 的 `SupportedSchemaVersion` 与类型 |
| `archive is missing manifest.json` | ZIP 不完整或不是整库导出 | 重新导出；确认用的是 `/export` 而非 `/document-transfer` |
| 导入后 recall 为空 | 目标 bank id 与查询的不一致 | 确认 `--bank-id`；`GET /v1/default/banks` 列出实际 id |
| 实体数量异常少 | 多个同名实体被合并 | 预期行为（见 §6.3）；如需区分，源侧需有不同 canonical name |
| `migration series has a gap` | 自行加了跳号的迁移文件 | 迁移版本必须从 1 连续到 N（`internal/storage/migrate.go`） |

---

## 9. 相关代码

- 归档解析：`internal/hindsightarchive/archive.go`（类型与 `Read`）、`zip.go`（`OpenZip`）
- 映射：`internal/migration/hindsight/map.go`（字段映射、id 派生、未映射清单）
- 导入：`internal/migration/hindsight/run.go`（`Run`、`Replace`/`Append`、事务写入）
- CLI：`internal/bankadmin/migrate.go`、`cmd/bankctl/main.go`
- 测试：`internal/hindsightarchive/archive_test.go`、
  `internal/migration/hindsight/run_test.go`（含构造导出 ZIP 的端到端用例）
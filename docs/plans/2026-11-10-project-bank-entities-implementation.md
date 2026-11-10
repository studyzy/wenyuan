# 项目库实体归集 — 实现计划

对应设计：`docs/plans/2026-11-10-project-bank-entities-design.md`（已定稿）

参考实现语义：**在团队 schema 内按归一化名字精确重建实体**，不搬成员
`entities.id`，不模糊匹配，不加唯一索引，零模型调用。

## 0. 总原则

- 只用既有 `normalizeEntityName`（`internal/storage/entity_match.go:48`），
  团队库不另写一份归一化规则。
- 实体生命周期严格跟随团队事实：事实没了 → posting 没了 → 实体入队清理。
- 不新增 schema 对象。团队 schema 与参考实现逐对象一致
  （AGENTS.md 的硬约束）。
- 现有测试必须继续通过；`make check` 与 `make test-pg` 都要跑。

---

## 步骤 1：纯规则（`internal/projectmerge`）

**文件**：新建 `internal/projectmerge/entities.go` + `entities_test.go`

### 1.1 `EntityNameKey`

```go
// EntityNameKey is the identity of a team entity: the whitespace-normalized
// name, lowercased. Two member entities whose keys match are one team entity.
//
// Exact equality is the whole rule. The weighted similarity a member's bank
// resolves names with merges "支付网关" into "支付系统" often enough that
// carrying it into a shared bank would conflate two people's systems; a missed
// neighbour costs a graph edge, a wrong merge is unrecoverable.
func EntityNameKey(name string) (string, bool)
```

- 内部调用 `storage.normalizeEntityName`。**问题**：该函数在 `storage` 包内且
  未导出。两个选择：
  - **A（选）**：在 `storage` 包导出 `NormalizeEntityName(name string)(string,bool)`，
    内部实现不变，`normalizeEntityName` 改为调用它（或直接改名导出）。
  - B：`projectmerge` 自己实现一份 —— **不采纳**，两份规则必然漂移。
- 导出后，`entity_match.go` 的所有内部调用点改名为 `NormalizeEntityName`。
  调用点少（`entity_match.go` 自身 + `retain` 路径），改动机械。

### 1.2 `EntitySet` / `EntityPairs`

```go
// EntitySet is one fact's entity names reduced to team identities, in a stable
// order. The display name is the first raw name that produced each key, so the
// team bank stores a real spelling rather than a lowercased key.
type EntitySet struct {
    // Names holds the display name for each key, keyed by EntityNameKey.
    Names map[string]string
}

// NewEntitySet folds a fact's raw entity names into team identities.
// Names that normalize to nothing are dropped, matching the member's bank,
// which never created an entity for them either.

// Pairs returns the unordered co-occurrence pairs of the set, in a fixed order,
// which is what the team bank's entity_cooccurrences records.
func (s EntitySet) Pairs() [][2]string
```

### 1.3 测试

逐行覆盖设计 §4.2.1 的表：

| 用例 | 断言 |
|---|---|
| `"ABC"` / `"abc"` / `" Abc "` | 同一个 key |
| `"支付  网关"` / `"支付\t网关"` / `"支付网关"` | 同一个 key |
| `"支付\u3000网关"`（全角空格） | 同一个 key |
| `"ＡＢＣ"` vs `"ABC"` | **不同** key |
| `"支付网关。"` vs `"支付网关"` | **不同** key |
| 513 runes | `ok == false` |
| `""` / `"   "` | `ok == false` |
| `EntitySet` 里 `ABC` 与 `abc` 同时出现 | `Names` 只有一条，值为先出现的那个 |
| `Pairs` 顺序 | 与输入顺序无关的确定性顺序 |

**验证**：`go test ./internal/projectmerge/ -run Entity`

---

## 步骤 2：读成员实体名

**文件**：`internal/storage/merge_store.go`、`internal/storage/postgres_merge_store_input.go`

### 2.1 `MergeFact` 增加字段

```go
// EntityNames are the member fact's entity names, carried so the team bank can
// rebuild its own entities. Ids are not carried: an entity is an identity
// within one bank, and the team bank resolves names to its own rows.
EntityNames []string
```

### 2.2 `loadMemberInput` 补一次查询

`postgres_merge_store_input.go` 在 `out.Fresh = fresh` 之后、`out.Links` 之前：

```go
if err := s.attachMemberEntityNames(ctx, tx, member, qualified, fresh); err != nil {
    return out, err
}
```

`attachMemberEntityNames`：

```sql
SELECT ue.unit_id::text, e.canonical_name
FROM <member>.unit_entities ue
JOIN <member>.entities e ON e.id = ue.entity_id
WHERE ue.unit_id = ANY($1::uuid[]) AND e.bank_id = $2
ORDER BY ue.unit_id, e.canonical_name
```

- 按 unit id 分组写回 `MergeFact.EntityNames`，**同一 unit 内按
  `EntitySet` 去重**（`ABC` + `abc` 只留一条）。
- 一次 ORDER BY 保证确定性；不解码成 map 再乱序。
- `fresh` 为空时直接返回。

### 2.3 测试

- **纯规则**（`_pgx_test.go` 需要库）或内存后端对齐 —— 见步骤 6。
- 单元级：`EntityNames` 在 `PlanBatch` 比较中**故意不参与**
  （`sameTeamFact` 不加这个字段，理由：实体名变化不该让事实行重建；
  posting 的同步是另一条路径，见步骤 3.3）。

**验证**：`go test -tags pgx ./internal/storage/ -run MergeInput`

---

## 步骤 3：写团队实体

**文件**：新建 `internal/storage/postgres_merge_store_entities.go`（`//go:build pgx`）

### 3.1 `applyEntities`

签名与调用点：

```go
func (s *PostgresMergeStore) applyEntities(
    ctx context.Context, tx Tx, qualified, bankID string,
    ids map[string]string,          // 本轮新插入的团队 unit: sourceUnitID -> teamUnitID
    facts map[string][]string,      // sourceUnitID -> 成员实体名
) error
```

在 `applyFacts` 的 `applyLinks` 之前调用（`postgres_merge_store_write.go:122`）。

实现：

1. 对每个 (teamUnitID, 名字) 计算 `EntityNameKey`，跳过 `ok == false`。
2. 收集全部 key → 存储拼写（先出现者胜）。**存成员库的原始拼写**（如 `ABC`），
   不存小写键：`idx_entities_bank_lower_name` 已经对小写形式唯一，冲突由它兜底。
3. 插入 + 冲突忽略 + 回查：
   ```sql
   INSERT INTO <team>.entities (bank_id, canonical_name, entity_kind, metadata)
   VALUES ($1, $2, 'regular', '{"origin":"merge"}'::jsonb)
   ON CONFLICT (bank_id, lower(canonical_name)) DO NOTHING
   ```
   然后
   ```sql
   SELECT id::text, lower(canonical_name)
   FROM <team>.entities
   WHERE bank_id = $1 AND lower(canonical_name) = ANY($2::text[])
   ```
   - **唯一索引已存在**：`idx_entities_bank_lower_name UNIQUE
     (bank_id, lower(canonical_name))`，`migrations/0004_reference_indexes.sql:65`。
     不需要新增任何 schema 对象（详见设计 §7）。
   - 成员库同样受这条索引约束，所以**同一个成员 bank 里不会有 `ABC` 与 `abc`
     两行**，读到什么就写什么，不会与团队库产生二次判断分歧。
   - 回查用 `lower()` 与索引表达式**完全一致**，PG 能命中该索引。
   - 加 `metadata.origin = 'merge'` 以便运维区分，不参与任何判定。
4. 批量写 posting：
   ```sql
   INSERT INTO <team>.unit_entities (unit_id, entity_id)
   SELECT * FROM unnest($1::uuid[], $2::uuid[])
   ON CONFLICT DO NOTHING
   ```
   利用 `pk_unit_entities (unit_id, entity_id)` 做 `ON CONFLICT DO NOTHING`。
   （仍先在内存里按 (unit, entity) 去重，冲突交给数据库兜底。）
6. 返回本轮涉及的实体 id 集合，供 3.2 用。

### 3.2 共现

对本轮每个事实的 `EntitySet.Pairs()` → 团队实体 id 对，调用既有
`upsertEntityCooccurrence`（`postgres_retain.go:1450`）。它已经做
`GREATEST` 时间戳与 count 累加。

**只处理本轮新写入或替换的事实**，不做全表重算。

### 3.3 变更事实时的 posting 同步

`applyFacts` 的顺序是「先删被替换的行，再插新行」。被替换的行删除时
`unit_entities` 由 `fk_unit_entities_unit_id_memory_units ... ON DELETE CASCADE`
自动消失（已核对），**不需要显式删除 posting**。替换的行重新插入时是新
unit id，走 3.1 重新写 posting。

替换的行重新插入时是新 unit id，走 3.1 重新写 posting。

### 3.4 实体清理

被替换/删除的行丢弃的 posting 会让实体变孤儿。**不在这里写清理逻辑**：
在 `retireRows` / `removeReplacedFacts` 删除行之前，收集受影响的实体 id，
写进 `entity_maintenance_queue`：

```sql
INSERT INTO <team>.entity_maintenance_queue (bank_id, entity_id)
SELECT $1, unnest($2::uuid[])
ON CONFLICT (bank_id, entity_id) DO NOTHING
```

既有维护循环的 `drainGraphMaintenance` 会 `DrainEntityMaintenance` →
`PruneEntity`（`postgres_maintenance_graph.go:235`），后者只在**没有 posting
时**删实体，并顺带 `pruneStaleCooccurrences`。

**顺序要求（已核对前置检查 #2、#3）**：cascade 一旦发生，These entity ids
就查不到了。所以必须在 `removeReplacedFacts` / `retireRows` 的 `DELETE` **之前**
执行：

```sql
SELECT DISTINCT entity_id FROM <team>.unit_entities WHERE unit_id = ANY($1::uuid[])
```

拿到后与删除在同一事务里入队：

```sql
INSERT INTO <team>.entity_maintenance_queue (bank_id, entity_id)
SELECT $1, unnest($2::uuid[])
ON CONFLICT (bank_id, entity_id) DO NOTHING
```

既有维护循环的 `drainGraphMaintenance` 会 `DrainEntityMaintenance` →
`PruneEntity`（`postgres_maintenance_graph.go:235`），后者只在**没有 posting
时**删实体，并顺带 `pruneStaleCooccurrences`。

**已核对：团队 schema 在该 job 的遍历范围内。** `drainGraphMaintenance` 走
`RegistryExtension.ListTenants`（`internal/app/tenant_registry.go:104`），它只
按 `Status: active` 过滤，**不按 `kind` 过滤**，团队租户因此也在列表中。不需要
新增 job，也不需要改遍历范围。

### 3.5 说明

- 团队库的 `entities.canonical_name` 存**成员库的原始拼写**；身份由
  `idx_entities_bank_lower_name` 上的 `lower()` 判定，与成员库同一条规则。
- `metadata.origin = 'merge'` 标记由合并写的实体，仅用于运维辨识。

### 3.6 测试（`_pgx_test.go`）

| 用例 | 断言 |
|---|---|
| 两个成员各有一条事实提到 `ABC` | 团队 `entities` 只有 1 行；`unit_entities` 2 行 |
| 两个成员分别提到 `ABC` / `xyz` | 团队 2 行 |
| `ABC` 与 `abc` 在**同一条**事实 | 该 unit 只有 1 条 posting |
| 事实被编辑（文本变化 → 替换） | 旧 posting 消失，新 posting 指向同一实体 |
| 事实被删除 | 实体入 `entity_maintenance_queue`，`PruneEntity` 后消失 |
| 空名 / 513 rune 的名字 | 不建实体，事实照常写入 |
| 共现 | 同一事实的两个实体在 `entity_cooccurrences` 记一对 |
| 幂等 | 同一批重跑 `applyEntities` 不产生重复行 |

**验证**：`PG_TEST_URL=.../wenyuan_test go test -tags pgx ./internal/storage/ -run MergeEntity`
（**只指向 `wenyuan_test`，绝不用服务库**）

---

## 步骤 4：端到端

**文件**：`internal/app/parity_integration_test.go` 同级新增用例

场景：

1. 建团队、建项目、两个成员。
2. 成员 A retain 一条提到 `支付网关` 的 world 事实；成员 B retain 一条提到
   `支付网关` 的另一条事实。
3. 跑一轮 `project.merge`。
4. `POST /v1/{tenant}/banks/{balanceBank}/recall`，`scope=team`，
   查询只与 A 的事实向量接近的语义。
5. **断言 B 的事实出现在结果里**（通过实体邻居扩展召回）。

再加一条否定用例：成员 B 提到的是 `支付系统`（不同名），断言不被实体扩展
召回。

**验证**：`go test ./internal/app/ -run ProjectEntity`

---

## 步骤 5：读路径

**无改动。** `/entities`、`/entities/{id}`、`/entities/graph` 仍不 fallback
（设计 §2.6）。用户明确不关心。

`recall` 的实体通道与图扩展臂**自动生效**——它们本来就查
`unit_entities`，团队库现在有内容了。

---

## 步骤 6：~~内存后端~~ —— 已确认不存在

**作废。** `MergeStore` 只有 pgx 实现；`internal/storage/memory_bank_presence.go:10`
明确写着 "MergeStore 没有内存类比"。合并路径只在 PostgreSQL 后端存在，
`unit_entities` 的写入与读取也只在 pgx 侧。

后果：内存后端**没有**项目合并，因此也没有团队实体。这不影响现有的
`make test`（离线），但意味着本特性的所有测试都必须带 `-tags pgx` 并有
`PG_TEST_URL`。纯规则部分（步骤 1）仍然可以在离线模式下测。

---

## 步骤 7：文档与上线

### 7.1 `AGENTS.md`

在团队平面段落补一段：

- 项目库**有实体**，由合并轮按归一化名字精确重建，零模型调用。
- 团队库用**小写归一化名**作为 `canonical_name`，`metadata.origin='merge'`。
- 精准匹配是有意的：不做模糊，避免跨成员误合。

同时在「Deliberate deviations」里说明这与参考实现无关（参考实现没有项目库
这个概念），属于本服务团队平面的自有行为。

### 7.2 设计文档

把 §3.5 的说明（原始拼写、`origin='merge'`）回写进设计文档。

### 7.3 上线步骤

写入 `docs/MIGRATION.md` 或对应运维文档：

```
部署后，对每个已有项目执行一次：
POST /admin/v1/teams/{team}/projects/{project}/merge-rebuild
```

全量重放零模型调用；事实越多耗时越长，但没有失败依赖。

---

## 步骤 8：验收

```sh
make check
PG_TEST_URL=postgres://.../wenyuan_test go test -tags pgx ./internal/storage/
```

外加手动：

1. 对一个已有项目跑 rebuild。
2. `?scope=team` recall，对比 rebuild 前后的结果条数（应增多）。
3. 查团队 schema：`SELECT count(*) FROM <team>.entities WHERE bank_id = '<team>::<project>'`，应 > 0。

---

## 前置检查清单 — 已核对结果（2026-11-10）

| # | 检查 | 结果 |
|---|---|---|
| 1 | `unit_entities` 有主键吗 | **有**：`pk_unit_entities (unit_id, entity_id)`，`migrations/0003_reference_constraints.sql:88` |
| 2 | `unit_entities.unit_id` 有 `ON DELETE CASCADE` 吗 | **有**：`fk_unit_entities_unit_id_memory_units ... ON DELETE CASCADE`，同文件 :145。删除事实行即删除 posting，**无需显式清理** |
| 3 | 团队租户是否在 `ListTenants()` 里 | **是**：`RegistryExtension.ListTenants`（`internal/app/tenant_registry.go:104`）只按 `Status: active` 过滤，**不过滤 `kind`**，团队 schema 在列表中 → `drainGraphMaintenance` 会覆盖团队库，实体清理链路可用 |
| 4 | 内存后端有合并实现吗 | **没有**。`MergeStore` 只有 pgx 实现（`internal/storage/memory_bank_presence.go:10` 明确说明 MergeStore 没有内存类比）。**步骤 6 作废**，合并路径只有 PG |

由此确定的三条实现要点：

- `unit_entities` 有主键 → `ON CONFLICT DO NOTHING` 可用，且同一事实内的
  重名去重由数据库兜底（但仍应在内存里先去重，避免无谓的冲突）。
- posting 随事实行 cascade 消失 → `removeReplacedFacts` / `retireRows`
  **不需要**删除 posting；但**需要在那之前收集受影响的实体 id** 入队清理，
  因为 cascade 发生时已经拿不到它们了。实现方式：删除前先
  `SELECT DISTINCT entity_id FROM unit_entities WHERE unit_id = ANY($doomed)`。
- 团队库的实体清理走既有 `graph.maintenance` job，不需要新 job。

⚠️ 需要验证的隐患：`schemaFor` 会要求上下文 schema 的 `schema_migrations`
记录包含 base series。团队 schema 走的是 admin provisioning 路径，**必须确认
它也在 `schema_migrations` 里登记了 base 系列**；否则 `drainGraphMaintenance`
在团队 schema 上会报错，实体永远清不掉。实现时加一条 `_pgx_test.go` 用例：
以团队 schema 为上下文跑一次 `DrainEntityMaintenance` + `PruneEntity`，
若因迁移记录缺失而失败即被暴露。

## 待写代码前的最后确认

`unit_entities` 的行删除后，`entities` 行仍在（没有反向外键）。孤儿实体
只有在维护循环跑到时才被 `PruneEntity` 删掉，后者带 `NOT EXISTS posting`
条件。所以“先入队、后删除”的顺序是**必须**的，颠倒会丢失目标。

---

## 提交切分

1. `export NormalizeEntityName and add team entity key rules`（步骤 1）
2. `carry member entity names into the merge input`（步骤 2）
3. `rebuild team entities and postings during a merge round`（步骤 3）
4. `cover the team entity path end to end`（步骤 4，全部带 `-tags pgx`）
5. `document the project bank's own entities`（步骤 7）
# 项目库（Project Bank）是否归集成员实体 — 设计文档

状态：**已批准，待实现**（2026-11-10 撰写并定稿）

## 1. 问题

`project.merge` 逐字复制成员的 `world` / `experience` 事实及其 `tags` 与
`memory_links`，但**不复制实体**（设计 v3 §16.4，理由：实体是一个 bank 内的
身份）。结果是项目库的 `entities` / `unit_entities` / `entity_cooccurrences`
全空。

问题：Recall / Reflect / 观察生成这三条路径，在项目库上是否真的需要实体？

## 2. 事实核查（本仓库代码证据）

### 2.1 Recall — 图扩展臂整条失效

`internal/storage/postgres_recall.go:543` `graphArm` 从强种子沿两类信号扩展：

- `memory_links`（已复制，正常）
- **`unit_entities` 的实体共现**（`seed_entities` → `entity_targets`，
  权重 `tanh(共现实体数 × 0.5)`，每实体封顶 `perEntityLimit` 条）

项目库没有 `unit_entities`，`entity_targets` 永远为空，图扩展臂**只剩一半**。
图臂是 RRF 融合的四路之一（semantic / keyword / graph / temporal），少一半
直接影响 `?scope=team` 的召回。

### 2.2 Recall — 实体通道永远为空

`internal/storage/postgres_recall_channels.go:43` `attachRecallEntities`
按返回的命中事实查 `unit_entities JOIN entities`，结果挂进实体通道。
项目库返回的每条事实实体列表都是空。

### 2.3 Recall — embedding 目前"侥幸正确"

`MergeFact.Embedding` 复制成员行的向量。retain 的向量语义是
「文本 + 日期 + **解析出的实体名**」（`internal/storage/embeddings.go:47`
`indexedEmbeddingText`）。项目行没有自己的实体，所以只能复制——一旦想重算
就重算不出来。当前不算故障，但是个隐式约束。

### 2.4 Reflect — 观察的实体提示为空

`internal/reflect/loop.go:58` 与 `runner.go:388`：`search_observations`
默认（`ObservationIncludeEntities` 为真）给每条观察挂实体名。项目库返回空。
模型少一层 grounding，不致命。

### 2.5 观察生成 — **不依赖实体表**

`internal/consolidation/` 判断"是否同一件事"靠：

- `scope.go` 的分桶（时间窗 / 标签 / 主题），不查实体
- `prompt.go:46` 让模型自己按实体/切面比对，是**模型读文本**，不查表
- `dedup.go:155` 同理

`entity_match.go` 的 trigram/共现/时间加权打分（阈值 0.6）用于
**retain 时实体消歧**（`resolveEntityLocked` / `resolveRetainEntityOn`），
不是观察合并。

**结论：观察生成路径完全不受项目库缺实体影响。**

### 2.6 用户可见面

`/entities`、`/entities/{id}`、`/entities/graph` 不 fallback（设计 v3 §16.4）。
用户明确表示**不关心控制台能否查项目库实体**，因此本设计不以读接口为目标。

## 3. 影响汇总

| 路径 | 项目库当前损失 | 严重度 | 本设计是否处理 |
|---|---|---|---|
| Recall 图扩展臂 | 实体分支空转，只剩 link 分支 | 高 | 是 |
| Recall 实体通道 | 恒空 | 中 | 是 |
| Reflect 观察实体提示 | 恒空 | 低 | 是（附带） |
| 观察生成 | 无 | — | 否 |
| `/entities` 读接口 | 无项目视角 | 用户不关心 | 否 |

## 4. 方案

### 4.1 选定方向：合并时在团队 schema 内按名字重建实体

不搬 `entities.id`（它是成员 bank 的行身份）。改为：

1. 合并读成员事实时，**同时读出该事实的实体名**（`canonical_name`）；
2. 在团队 bank 里按**名字身份**解析：名字归一化后完全相同的复用同一行，
   不同则新建；
3. 写团队 `unit_entities`；
4. 按团队事实的共现重建 `entity_cooccurrences`。

仍然**零模型调用**：实体名来自 retain 时已抽好的 `RetainEntity.Text`，
不需要对团队文本重新抽取。

### 4.2 名字身份：只做精确合并，不做模糊

**这是本设计最重要的取舍。**

成员侧解析用的是加权打分（名字 0.5 + 共现 0.3 + 时间 0.2，及格线 0.6），
单用户 bank 内合理。直接搬到团队库会**过度合并**：

- 两个团队各有一个"配置服务"，会被合成一个
- 两个项目各有一个"张三"，会被合成一个
- 中文短名（2–3 字）的 trigram 区分度极低，"支付网关" 与 "支付系统" 极易过线

因此团队库**只用精确匹配**：两个名字的归一化键相同即同一实体。
不相似的名字留作两个实体——图扩展臂宁可少连一条边，不可连错一条边。
**误合的代价（把 A 的事实算成 B 的）远高于漏合的代价（少一个邻居）。**

### 4.2.1 归一化键的定义与边界

归一化键 `entityNameKey(name)` = `strings.ToLower(normalizeEntityName(name))`，
其中 `normalizeEntityName` 复用 `internal/storage/entity_match.go:48`。逐条列出
边界，实现必须与此一致，否则成员库与团队库的实体数量会对不上：

| 输入差异 | 结果 | 说明 |
|---|---|---|
| 大小写：`ABC` / `abc` / `Abc` | **同一实体** | `ToLower` 在归一化之后施加。`strings.ToLower` 是 Unicode 感知的，`Ä`/`ä` 也合并 |
| 首尾空白：`" ABC "` | **同一实体** | `strings.Fields` 去掉两端并折叠中间 |
| 内部连续空白 / 制表 / 换行：`"支付  网关"` / `"支付\t网关"` | **同一实体** | `strings.Fields` + `Join(" ")` 把所有空白字符折叠成单个空格 |
| 全角空格 `U+3000` | **同一实体** | Go 的 `strings.Fields` 按 `unicode.IsSpace` 切分，全角空格属于它 |
| 全角/半角字符：`ABC` / `ＡＢＣ` | **两个实体** | 不做 NFKC 归一化。这是有意的：全角变体常是不同系统的真实名字 |
| 标点：`支付网关` / `支付网关。` | **两个实体** | 不剥标点 |
| 大小写敏感场景：`Go` / `GO` | **同一实体** | 同上，`ToLower` 无条件合并。语言名与缩写会并到一起，接受 |
| 超过 512 字符（按 rune 计） | **该名字被丢弃，不建实体** | `normalizeEntityName` 返回 `ok=false`。与成员侧行为一致：成员侧也没为它建实体 |
| 空字符串 / 纯空白 | **丢弃** | 同上 |
| 名字本身含 `::`、路径、UUID 形态 | **照常** | 归一化不解析语义，只做空白与大小写 |
| 同一事实里 `ABC` 与 `abc` 同时出现 | **团队库只写一条 `unit_entities`** | 两者键相同。成员侧加权打分可能做成两行，团队库不做那一步 |
| 两个成员的 `支付网关` 与 `Payment Gateway` | **两个实体** | 不做跨语言、跨拼写的合并 |

由此得到两条实现要求：

1. **归一化必须走同一个函数**。团队库不能另写一份"差不多"的规则，否则同一
   名字在两侧得到不同的 `canonical_name`，`/entities` 的展示名会莫名变样。
   团队库存的是**首次遇到的那个名字的归一化形式**（即 `normalizeEntityName`
   的首个结果，保留大小写），键只用于判定相等。
2. **512 字符超限的名字不报错、不截断**，直接跳过该名字的实体写入，事实本身
   照常写入。这与成员侧"丢弃而非截断"一致，避免团队库出现成员库没有的实体。

### 4.2.2 为什么不做 NFKC

NFKC 会把 `ＡＢＣ` 折叠成 `ABC`、把 `①` 折叠成 `1`。在单用户 bank 里这通常
是好事；在团队库里它会让"某个系统的全角名字"和"另一个系统的半角名字"合
并——又是一个不对称的误合来源。**YAGNI**：没有证据表明成员会在中文全角与
半角两种写法下指同一个东西，而误合无法事后拆开。

### 4.3 embedding 不重算

继续复制成员向量。理由不变：同一部署同一嵌入方案，复制才可比。若将来团队库
有了自己的实体仍不重算，因为重算会让同一事实在两侧向量不同，
`?scope=team` 与 `?scope=personal` 的语义臂排序产生偏差。

记录为显式约束，不再是"因为没有实体所以只能复制"。

### 4.4 link 的 entity_id

`merge_store_links.go` 目前不写 `entity_id`。本设计**仍然不写**：
`memory_links.entity_id` 在团队库里需要映射到团队自己的实体行，而 link 的两端
可能分批到达。图扩展臂的实体分支走 `unit_entities`，不走 link 的 `entity_id`，
所以缺它不影响召回。

如需补齐，作为后续独立项：`applyLinks` 在解析出端点后，按"两端共享的实体名"
回填。**不在本设计范围内。**

## 5. 数据流

```
成员 bank                          团队 bank
──────────                         ──────────
memory_units (world/experience)
   │
   ├── unit_entities ──┐
   │    └ entities     │  canonical_name
   │       (name)      │
   ▼                   ▼
loadMemberInput ──► MergeFact.EntityNames []string
                             │
                             ▼
                     resolveTeamEntity(name)
                     ┌───────┴───────┐
                  命中              ���命中
                 复用 id          INSERT entities
                     └───────┬───────┘
                             ▼
                   INSERT unit_entities (team unit, team entity)
                             │
                             ▼
                   重建 entity_cooccurrences（本轮 + 存量）
```

## 6. 改动点

### 6.1 `storage.MergeFact` 增加字段

```go
// EntityNames are the member fact's resolved entity names, carried so the
// team bank can rebuild its own entities. Ids are not carried: an entity is
// an identity within one bank.
EntityNames []string
```

### 6.2 `postgres_merge_store_input.go`

在 `loadMemberInput` 主查询后补一次按 `unit_id` 批查实体名
（一个 `unit_entities JOIN entities WHERE ue.unit_id = ANY($1)`，按 unit id 分组），
写入 `MergeFact.EntityNames`。

同一事实内重名去重，名字顺序按 `canonical_name` 排序，保证确定性。

### 6.3 `postgres_merge_store_*` 写入侧

新增 `applyEntities(ctx, tx, qualified, bankID, units, entities)`：

1. 收集本轮所有事实的名字集合；
2. `INSERT ... ON CONFLICT (bank_id, lower(canonical_name)) DO NOTHING`。
   参考 schema **本来就有**这条唯一索引（`idx_entities_bank_lower_name`，
   `migrations/0004_reference_indexes.sql:65`），不需要新增，见 §7；
3. 回查拿到 id；
4. 批量写 `unit_entities`（主键 `(unit_id, entity_id)`，`ON CONFLICT DO NOTHING`）；
5. 对本轮涉及的实体对记录 `entity_cooccurrences`（见 §8）。

### 6.3.1 已核实的数据库事实（2026-11-10，实现步骤 1–2 期间）

三条约束直接决定了实现方式，全部来自参考 schema，不需要任何改动：

| 事实 | 位置 | 影响 |
|---|---|---|
| `idx_entities_bank_lower_name` 是 `UNIQUE (bank_id, lower(canonical_name))` | `migrations/0004_reference_indexes.sql:65` | **团队库天然不会出现同名两行**。原来担心的"需要加唯一索引"不成立——索引已在。用例：往同一个 member bank 插 `ABC` 与 `abc` 会直接违反约束 |
| `unit_entities` 主键 `(unit_id, entity_id)` | `migrations/0003_reference_constraints.sql:88` | posting 去重由数据库兜底 |
| `unit_entities.unit_id` 外键 `ON DELETE CASCADE` | 同文件 :145 | 删除团队事实行自动清 posting，**但入队清理必须在删除之前** |

**由此推翻 §4.2 的一个担忧**：原本担心成员侧会把 `ABC` 与 `abc` 做成两行，
团队侧只写一条会不对称。实际上成员 bank 的 `lower(canonical_name)` 唯一索引
禁止这种状态——两个拼写根本进不了同一个 bank。所以团队库的 `lower()` 身份
与成员库的**是同一个规则**，不是第二次判断。

附带结论：团队库 `canonical_name` 可以存**成员库的原始拼写**（`ABC`），不必存
小写键——`lower(canonical_name)` 上的唯一索引会兜住 `ABC` 与 `abc` 的冲突。

### 6.4 `internal/projectmerge`（纯规则）

新增纯函数，可在无数据库下测试：

- `NormalizeEntityName(name) string` — 折叠空白 + 小写
- `EntityPairs(units []MemberFact) [][2]string` — 本轮共现对
- `EntityDiff(previous, incoming) …` — 团队实体的增删（删除见 §8）

### 6.5 读路径：无改动

`/entities` 等仍不 fallback。用户不关心，且新增 fallback 需要跨 schema 读，
成本与本设计目标无关。

## 7. Schema 影响

**零改动，且已核实。**

项目库与参考实现的 tenant schema 必须逐对象一致（AGENTS.md「A tenant schema
must stay object-for-object identical to the reference schema」）。

早先的担忧是"要在团队库加唯一索引才能防重复"，因此准备用进程内去重绕开。
**2026-11-10 实现期间核实：索引本来就在。**

```
idx_entities_bank_lower_name  UNIQUE (bank_id, lower(canonical_name))
migrations/0004_reference_indexes.sql:65
```

它是参考 schema 的一部分，团队库作为 tenant schema 天然具备。所以：

- 插入用 `INSERT ... ON CONFLICT (bank_id, lower(canonical_name)) DO NOTHING`，
  并发与重放都不会产生重复行；
- 不新增任何 schema 对象，与参考实现仍逐对象一致；
- 原"进程内去重"方案作废——它防的是一个数据库已经防住的错误。

## 8. 删除与一致性

实体的生命周期跟随事实：

- 团队事实被 `Remove`（成员删了事实，或文本变化重建）→ 该行的
  `unit_entities` 随之删除（外键 cascade）。
- 团队实体失去全部 posting → 应被清理。**复用既有维护路径**：把目标实体
  入 `entity_maintenance_queue`，由 `DrainEntityMaintenance` →
  `PruneEntity`（`postgres_maintenance_graph.go:235`）删除无 posting 的实体
  并清理 `entity_cooccurrences`。不新增清理逻辑。
- `entity_cooccurrences` 的重算：`PruneEntity` 已带 `pruneStaleCooccurrences`，
  本轮写入时对新出现的实体对做 upsert 计数。**不要全表重算**——存量可能有
  十万级实体对。

## 9. 存量迁移

不重建历史。新建的实体从下一轮合并开始生效；已存在的项目库在部署后一轮内补齐，
因为合并轮会重放（`RebuildRequested`）或增量读到新事实时才写实体——
**但存量事实不会被重新读一遍**。

两个选项：

- **A（推荐）：强制一次重建。** 部署后调用
  `POST /admin/v1/teams/{id}/projects/{pid}/merge-rebuild`。重建是全量重放，
  每批都会走新的实体写入路径。代价：项目事实越多耗时越长，但零模型调用。
- B. 不做，投影只在事实下次变更时补。结果是旧事实永远没有实体，图臂半瘫
  维持原状。

采用 A，并在文档中写明：**这是本特性的上线步骤，不是可选优化。**

## 10. 测试

| 层 | 测试 |
|---|---|
| 纯规则 | `EntityNameKey` 的 §4.2.1 表，逐行一条用例（大小写、首尾/内部空白、全角空格、全角字符不合并、标点不剥、超长丢弃、空名丢弃）；`EntityPairs` 去重与顺序 |
| 内存后端 | 合并后团队 bank 的 `unit_entities` 与成员一致（按名字） |
| PG（`-tags pgx`） | 两个成员同名的实体合成一行；不同名的保持两行；`ON CONFLICT` 路径 |
| PG | 事实被删除后，实体失去 posting 并入队，`PruneEntity` 后消失 |
| PG | 重建后实体数量与成员实体名集合的并集一致 |
| 端到端 | `?scope=team` 的 recall 图扩展臂能通过实体邻居召回到成员 B 的事实 |

## 11. 风险

| 风险 | 处理 |
|---|---|
| 跨成员误合并 | **只做精确名字匹配**（§4.2），不做模糊 |
| 同名不同实体（两个"张三"） | 接受。项目库的语义是"团队共享词汇"，同名即视为同一事物；用户可用不同名字区分 |
| 中文短名归一化不足 | 大小写对中文无效，等价于完全相同；这正是想要的行为 |
| 存量项目库无实体 | 部署后强制 rebuild（§9） |
| 合并轮变慢 | 每批多两条查询（读名字、回查 id）。批量事实通常 ≤ 500，影响可忽略 |
| 团队库实体与成员实体 id 不同 | **设计如此**。调用方拿到的实体 id 在 `?scope=team` 下是团队库的 id，与个人库 id 不可互用——需在 API 文档注明 |

## 12. 决策记录

- 2026-11-10：确认「实体是 bank 内身份」无法推导出"项目库不该有实体"。
  前者约束的是不能搬 `entities.id`，不是不能重建。
- 观察生成不依赖实体表，因此本设计的动机**只有 recall 图扩展臂与实体通道**。
  若这两项也被判定为不关心，则本设计可以整体不做，只需在 AGENTS.md 补一句
  「图扩展臂在项目库上仅走 link 分支」。
- 2026-11-10 定稿：只做精确名字匹配；不加唯一索引；存量强制
  rebuild；link 的 `entity_id` 不在本次范围。实现计划见
  `2026-11-10-project-bank-entities-implementation.md`。
- 2026-11-10 实现步骤 2 修正：核实到参考 schema 已有
  `UNIQUE (bank_id, lower(canonical_name))`（`0004_reference_indexes.sql:65`），
  且成员 bank 因此**无法**同时持有 `ABC` 与 `abc`。结论：(a) 不需要加索引，
  也不需要进程内去重，`ON CONFLICT` 即可；(b) 团队库的 `lower()` 身份与成员库
  是同一条规则，不是第二次判断；(c) 团队库可存原始拼写。见 §6.3.1、§7。

## 13. 定稿（2026-11-10 已拍板）

| 事项 | 决定 |
|---|---|
| 名字匹配 | **只做精确匹配**，不做模糊（§4.2） |
| Schema | **零改动**：唯一索引本就存在（`idx_entities_bank_lower_name`），用 `ON CONFLICT`（§7） |
| 存量 | 部署后对已有项目**强制跑一次 `merge-rebuild`**（§9） |
| link 的 `entity_id` | **本设计不做**，留作后续独立项（§4.4） |
# Personal Aggregate Bank（`<TenantId>::myself`）—— 设计方案

状态：2026-10-31 v2b。产品先定调了四件事（§十三），随后又明确了第 5 条：
**`A::myself` 完全由后台生成与管理，用户不往里写、也不删它的内容**；再后来定了第 6 条：
**`project.merge` 不把 `<member>::myself` 当成员 bank**。第 5 条让 v2 的"两条水线 +
`origin` 分双写"整体塌回"一条水线 + 一个兜底标签"，第 6 条关掉了 v2b 里唯一残留的
交叠点，§零 记下了这两次改动的理由与结果。v1 的 409 写门仍然不回——`myself` 不是保留名。
前作：`docs/plans/2026-10-29-team-project-shared-memory-design.md`（v2 决策 19 条）、
`docs/plans/2026-10-30-team-memory-design-v3.md`（已实现：`project.merge`、
`MergeStore`、团队读回退）。本设计**复用** v3 的合并机制，不复制它的结论。

范围：本服务自己的能力，不属于参考实现的兼容面。数据面的路由、JSON 字段、状态码、
请求头一律不改；能用参考实现已有的读路由（recall / memories list / knowledge /
mental-models）就用它，不新增数据面路由。**单一 Tenant 部署同样启用**
（§7.5）——这是它和团队能力最大的不同。

## 零、产品定调与修订摘要

产品在 2026-10-31 定了五件事，v1 中与之冲突的部分全部改写。

| # | 产品定的规则 | v1 的错误 | 落地 |
|---|---|---|---|
| V1 | **单一 Tenant 部署也要有 `myself` bank；团队 Tenant 是团队不是个人，不要 `myself`** | v1 的 Q2 倾向"团队 tenant 也要"，门控也没排除团队 tenant | 汇总对象是 `kind='personal'` 的 tenant；`tenant_mode: single` 的唯一 schema 也开。§7.5、§6.1 |
| V2 | **`myself` 不是保留名**，用户有一个叫 `myself` 的工程 bank 时照常可写 | v1 决策 2 与写门小节对 `A::myself` 的写返回 409 | 写路由一行不改，没有 409 门（§8.2） |
| V3 | **`A::myself` 完全由后台生成与管理**：用户不往里写，也不删它的内容 | v1/v2 把它当"源 + 目标"双写 bank | 它是**目标**，不是源；上一版即全部内容（§0.1、§6.3） |
| V4 | **按 `(updated_at, id)` 水线增量整合**（同团队合并） | v1 用整 bank `source_hash` 重合成 | §6.3：每个源 bank 一条水线 + 每轮存活反连接 |
| V5 | **不存在关闭开关；不提供一次性全量读** | v1 有 `PERSONAL_AGGREGATE_ENABLED` 与一个独立的索引任务 | 决策 4/16，§七、§十一 |
| V6 | **`project.merge` 排除 `<member>::myself`**：个人汇总结论不得进入团队内容 | v2b 把 `A::myself` 当项目成员 bank 时，团队合并会读到个人汇总结论 | §3.5、§十三 N4 |

### 0.1 v2 → v2b：为什么两条水线塌成一条

v2 当时的推导：既然用户也能往 `A::myself` 写，它就既是源又是目标，于是
**每个源 bank 一条水线 + `A::myself` 一条目标水线**。两条水线、`origin` 标记、
按 `origin` 过滤的删除语句——这一套复杂度全部来自"人也在写 `A::myself`"。

v2b 有一条新定的规则：**`A::myself` 只由后台生成与管理，用户不删除、也不往里
写它当作数据源的东西。** 它的一切内容都由后台从该 tenant 的其他 bank 推导出来。
于是：

| 消失的东西 | 为什么 |
|---|---|
| 目标水线（`personal_state.target_cursor_*`） | 只有一个写入者，上一版就是"全部 `origin='aggregate'` 的行"，按 id 全读即可 |
| `origin` 标记与按它过滤的删除语句 | 行归属没有歧义：`A::myself` 里的 world 事实都是汇总条目 |
| "`A::myself` 同时作为源 bank" | 它是目标，不是源；源只有 `A::X`、`A::Y`…… |
| 用户直写事实参与 `sources` | 没有用户直写的事实 |

剩下的水线就是**每个源 bank 一条 `(updated_at, id)` 游标**（§6.3），与 v3 团队合并
同构，只是"成员"换成了"该 tenant 的 bank"。

### 0.2 仍然保留的边界：用户偶然写进 `A::myself`

"用户不会写"是产品预期，系统层面仍然可能发生（旧的客户端脚本、误配的 bank
模板、直接调 API）。因此：

- V2 仍然成立：**`A::myself` 不是保留名，没有 409 写门**，写路由一行不改；
- 这类行不是源、不是账本键，下一轮 rollup 按"汇总条目之外的行不是我的"清掉它们
  ——用的是**行级标记 `metadata.origin='aggregate'`**，但这里它只用于**兜底识别**，
  不再是两半数据的结构（§6.4）；
- 删除检测仍然需要它：账本里的源 unit 不是汇总自己的行，反连接只对**源 schema 的
  memory_units** 做（§6.3），计数 `user_facts` 在 `aggregate-status` 里给运维看。

一句话：`origin` 从"两条数据线"降级成"一个兜底标签"；水线从两条降回一条。

### 0.3 被否的备选：`A::myself` 同时宣布为源

如果哪天要让用户直写的内容也参与汇总（v2 的 V3），就必须回到两条水线——这是
这件事不可压缩的代价，写在这里以备将来重新讨论时不必重新推导。

## 一、问题

一个用户在一个 tenant schema 里有多个 bank：每个项目一个（`A::X`、`A::Y`、
`A::Frontend-refactor`），成员身份带来的项目 bank 也在同一个 schema 里。
agent 每次会话只知道"当前在哪个 bank"，用户自己脑子里那张"我在哪些项目上、
各自知道什么"的图，机器没有。

team bank（`T::X`）解决的是**两个人共享一个项目**；它不解决**一个人跨项目**。
用户要的是第三种记忆面：

- 一个**汇总 bank** `A::myself`：把该 tenant **所有 bank** 的提炼事实合并成一份
  全景知识；
- 一份**索引**：书签式目录 + 跨 bank 的概念词表，让 agent 一次 recall 就知道
  "这个问题该去哪个 bank 深挖"；
- 一个**发散入口**：从汇总命中发散到相关的其他 bank、其他实体，形成"先定位后
  展开"的检索路径。

## 二、决策（v2b）

| # | 决策 |
|---|---|
| 1 | 汇总 bank 的 id 是 **`<TenantId>::myself`**，位于**该 tenant 自己的 schema**，不新建 tenant、不新建 schema。 |
| 2 | **它不是保留名**：写路由不为它加任何新拒绝（不返回 409）。但它的内容**由后台生成与管理**（决策 5），用户往里写的东西不是数据源，下一轮会被清掉（§0.2）。 |
| 3 | **汇总对象是 `tenant.kind='personal'` 的 tenant**；团队 tenant 是团队不是个人，**没有** `A::myself`（§7.5）。 |
| 4 | **功能默认开启且不可关闭**：没有"关掉"的配置。`tenant_mode: single` 的唯一 schema 同样汇总。唯一的"不跑"是两个自然条件：没有 chat provider，或进程根本没在跑维护循环。 |
| 5 | **`A::myself` 由后台独占生成与管理。** 用户不往里写、也不删它的内容；它的一切内容都是从该 tenant 其他 bank 推导出来的。数据面不为写它加拒绝（决策 2），但用户写进去的行下一轮会被替换（§0.2）。 |
| 6 | **每个源 bank 一条 `(updated_at, id)` 水线**。`A::myself` 是目标不是源，没有目标水线：上一版就是它当前的全部内容（§6.3）。 |
| 7 | 输入只取**源 bank**（该 tenant 除 `A::myself` 以外的 bank）里 `fact_type='world'` 的事实。`experience` 永不进入；`observation` 由 consolidation 生成，其 id 会随重建变化，不作为账本键（与 v3 决策 8 同因）。 |
| 8 | 只读本 schema。团队 bank（`T::X`，在团队自己的 schema 里）的内容**永不进入** `A::myself`；成员身份带来的项目 bank `A::X` 就是本 schema 的普通 bank，照常合并（§3.4）。 |
| 9 | 汇总条目带**双重溯源**：`metadata.sources`（源事实 unit id，可展开到原文）与 `metadata.banks`（出现在哪些源 bank）。前者是"发散"的路线，后者是"索引"的键。 |
| 10 | 跨 bank 的同题归并在**词表**里完成：`personal_topic` 是"规范主题 → bank"的索引，它既是去重的答案，也是发散的边。 |
| 11 | 并发沿用 v3 的两段事务 + `merging_since` fence，key 是 `(tenant_id)`，advisory lock 每个 tenant 一把。 |
| 12 | 删除检测是**每轮存活反连接**，只查"仍在支撑汇总内容的源行"（v3 R4/R2）；`'__dropped__'` 不参与。 |
| 13 | 汇总 bank 的观察由既有 consolidation 生成（入队 `consolidate`），**不调用二次模型**。 |
| 14 | 读路径不变：`teamread.Resolver` 与 `storage.withFallback` 一行不改。`myself` 的定位价值靠"可寻址 + 索引"实现，不靠隐式回退（§3.2）。 |
| 15 | 账本独立：`admin.personal_state` / `personal_bank_state` / `personal_source` / `personal_link` / `personal_topic`（新迁移 `0004`）。不复用 `merge_state`/`merge_source_unit`：它们的 FK 挂在 `team_project`/`team_member` 上。 |
| 16 | 目录是在**已产出的内容上**的纯函数投影（词表 + 溯源），**不单独调模型**；数据面不新增路由，目录落在 `A::myself` 的 knowledge 树里（§7.2）。 |

## 三、备选方案与取舍

### 3.1 迭代单位：水线增量 vs 整 bank 重合成

v3 的项目合并按 `(updated_at, id)` 水线增量，要求"源 bank"与"目标 bank"是**不同的
bank**。v2b 满足这个前提：`A::myself` 由后台独占写（决策 5），源只是 `A::X`、`A::Y`……
所以个人汇总可以直接沿用 v3 的水线，而不需要 v1 的整 bank `source_hash` 重合成，也不
需要 v2 一开始设计的两条水线（§0.1）。这样：

| 方案 | 每轮模型输入 | 删除检测 | 代价 |
|---|---|---|---|
| **A. 每源水线 + 存活反连接**（选） | 上一版 + 该批增量事实 | 每轮存活反连接（v3 R4） | 上一版必须带进 prompt，受 `PersonalRollupMaxTeamFacts` 约束 |
| B. 整 bank `source_hash` 重合成（v1） | 该 bank 全部事实 + 旧摘要 | 哈希变化即重算 | 大 bank 每次改动都整包重算 |
| C. 单一 prompt 全量 | 全部 bank 的全部事实 | 全量重建 | 规模上限硬撞 |

A 的产出是**压扁的一版汇总条目**（每轮把"上一版 + 新事实"合成完整一版，与被替换的
行保持 `merge_key` 相同则 id 不变、观察跟着走）。**上一版必须进 prompt**（v3 决策 14），
否则模型会把本轮没见过的旧内容丢掉；条目数因此受 `PersonalRollupMaxTeamFacts`
（默认 500）约束——超限是**容量问题**：记 `last_error`、本轮跳过，**不失败关闭**
（没有隐私问题，与 v3 R8 同一判断）。

代价必须写明：**汇总条目是压缩过的**。用户问"这个 API 的端口是多少"时，
`A::myself` 给的是汇总句；原文在 `A::X` 里，靠 `metadata.sources` 定位过去（决策 9）。
所以 prompt 硬性要求保留**可复核的具体值**（端口、路径、版本、约束），§6.2 的
golden case 与 §十二 的 golden 用例是这条的验收。

### 3.2 寻址：显式可寻址 vs 读回退

可以像 v3 那样做"个人 bank 为空时回退到 `A::myself`"。**不做**，三个理由：

1. v3 的回退规则是"按 bank 是否有内容"，而 `A::myself` 只要合成过就永远有内容，
   于是 `A::X` 一旦合成过一次，`A::X` 的 recall 就会被永久重定向——这会改变
   现有 bank 的读取结果，是兼容性事故，不是新功能；
2. `teamread.Resolver` 只在 `TenantMode == multi` 时装配
   （`internal/app/app.go:294-311`），而本能力要覆盖单一 Tenant 部署；
3. 回退的语义是"用户问的不是这个 bank"，而 `A::myself` 的问题恰恰是"用户问的是
   这个 bank"——只是答案在别的地方。这是**发散**，不是**兜底**。发散由 `sources`
   完成，不需要改读路径。

所以：`POST /banks/A::myself/memories/recall` 就是入口，`recall` 结果里每条
`metadata.sources` 是下一步的路线。

### 3.3 目录：knowledge 树 vs mental model vs 事实

| 载体 | 优点 | 缺点 |
|---|---|---|
| knowledge 树（`A::myself`） | 参考实现已有读路由（`GET …/knowledge-base/tree`、`/pages/{id}`），控制台已有树形 UI；父子结构就是"目录" | 每个节点背后是一个 mental model，刷新走 `source_query`，**内容从句**，而不是模型再合成一次（V1 不能用节点正文直接写，因为 `body` 由 refresh 生成） |
| mental model | 可定时刷新、可展示 | 与 knowledge 节点一对一，重复 |
| 汇总事实（`metadata.kind="index"`） | 最便宜，recall 天然命中 | 全是扁平文本，没有层级；"目录"的价值在层级 |

选 **knowledge 树**做目录（决策 9），节点 = 每个成员 bank 一页 + 每个主题一页，
`source_query` 写成能从汇总 bank 取到对应内容的查询。这样"AI 的入口"是：
`GET …/knowledge-base/tree` → 选页 → `GET …/pages/{id}` 拿 markdown → 需要原文时
`POST …/memories/recall` 带 `bank_id=A::X`（来自页面的 `sources`）。

代价与风险要写明：knowledge 节点第一次创建时会建一个 backing mental model 并
**入队一次 refresh**（`internal/reporting/knowledge_nodes.go:44-75`），那条 refresh
会花一次模型调用。所以目录**不单独调模型**（决策 16）：分组、页标题、页摘要都由
rollup 已经产出的主题与溯源**纯函数投影**得到，节点只在增删时才动（先 diff 再写），
页面的正文交给那一次 expiring refresh。这样"索引"不吃额外预算，也就没有"索引失败
导致目录过期"这一类状态。

### 3.4 与团队 bank 同名

`A::X` 既可能是"用户自己的项目材料"，也可能是"用户作为成员的项目材料"，两者
**在数据上没有区别**——都是该 tenant 的普通 bank。团队面据此做读回退是对的
（v3 §七）。对汇总来说，两者都只是"一个 bank"，一视同仁；决策 8 只禁止一件事：
**把团队 bank `T::X` 的内容抄进 `A::myself`**。因此 rollup 只读本 schema 的
`memory_units`，永远不跨 schema 读团队 bank——这一点用一个集成测试钉住
（§十二）。

### 3.5 团队合并与 `A::myself`：排除，不是特判

`A::X` 既是"用户自己的项目材料"也是"用户作为成员的项目材料"，数据上没有区别（§3.4）。
但当项目名恰为 `myself` 时，成员 bank `<member>::myself` 与**汇总目标**是同一个 bank，
而 `project.merge` 读成员 bank 的 `world` 事实时会把个人汇总结论当成该成员的贡献——团队
内容因此被个人汇总污染（团队看到的是"这个人跨项目的总结"，不是"他对本项目的输入"）。

两条团队合并查询都要加这条排除：

| 位置 | 查询 | 加什么 |
|---|---|---|
| `postgres_merge_store.go` 的 `loadMemberInput` | 成员的新事实（`bank_id = <member>::<project name>`） | 当 `member.ProjectName == tenant.PersonalBankName` 时，该成员 `Fresh` 为空 |
| 同文件的 `sourceDeleted` | 该成员的账本反连接 | 团队侧**没有**这条账本（见下"为什么不用改"），因此无需处理 |

**判定放在 Go 里，不写进 SQL**：`ProjectName` 已经在 `MergeProjectMember`
（`internal/storage/merge_store.go:34-46`）里，判断是一次字符串比较；写在 SQL 里会
让这条排除条件出现在一个与个人汇总无关的查询里，将来读代码的人会以为 `merge_source_unit`
也需要它。

**为什么账本不用改**：`A::myself` 作为成员 bank 时，`loadMemberInput` 不再读它的行，
所以这些行**永远不会上账本**（账本只在轮次结束时按本轮消费的源写）。已存在的旧账本
（升级前这个 bank 作为成员 bank 参与过合并）由下一次重建清掉——重建轮的账本整体
替换，不会留下指向它的行。

**代价**：一个用户如果真有名为 `myself` 的团队项目，他在那个项目上的个人 bank 就等于
汇总 bank，而汇总不参与团队合并 → **他在该项目上的贡献不会进入团队内容**。这是产品
选定的方向（N4 = A）：宁可少汇总，不让个人汇总结论冒充团队贡献。管理面
`aggregate-status` 对此显式报 `team_project_named_myself: true`，`merge-status` 也照旧
报告该项目的成员数，运维一眼能看出这个人没贡献。

## 四、数据布局

```
Schema A（个人的 tenant）
  banks: A::X  A::Y  A::Proj   <- 源 bank，每个一条水线
  banks: A::myself             <- 汇总 bank，后台独占写（决策 5）
    memory_units (fact_type='world')
      metadata.origin   = 'aggregate'
      metadata.sources  = [{unit_id, bank_id}]   <- 源事实，展开即原文
      metadata.banks    = ["A::X", "A::Y"]       <- 索引键
      metadata.topics   = ["部署", "postgres"]
      metadata.merge_key = sha256(normalize(text))
    fact_type='observation'  既有 consolidation 生成
    knowledge 树（分组 → 每个 bank 一页 + 每个主题一页）  <- 索引
  （A::X / A::Y 的 fact、observation、documents 一行都不改）

Schema T（团队的 tenant）
  不建 A::myself，也没有 personal_* 账本行（决策 3）

admin（本服务自有 schema）
  personal_state       每个 tenant 的 fence、熔断、心跳
  personal_bank_state  每个 (tenant_id, source_bank_id) 的水线
  personal_source      每个源 unit → 它支撑的汇总 merge_key
  personal_link        发散边与排序权重（由 personal_source 聚合得出）
  personal_topic       主题 → bank 词表（定位索引）
```

**为什么账本是新表，不复用 `merge_state`**：`0003` 的两张表 FK 到
`team_project(team_tenant_id, project_id)` 与 `team_member(team_tenant_id, tenant_id)`
（`embeddedadmin/0003_merge_state.sql:40-60`），个人汇总没有这两行，硬塞要伪造一条
`team_project`——那是往团队语义里写假数据，比多五张表贵得多。

## 五、admin 迁移（`0004_personal_aggregate.sql`）

```sql
-- One row per personal tenant that takes part in the aggregate. There is exactly
-- one per tenant, because a tenant has exactly one A::myself.
CREATE TABLE IF NOT EXISTS personal_state (
    tenant_id       text PRIMARY KEY,
    -- merging_since is the cross-transaction half of the round claim (v3 §5.2).
    merging_since   timestamptz,
    last_round_at   timestamptz,
    last_error      text,
    fail_count      integer NOT NULL DEFAULT 0,
    rebuild_requested boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id) REFERENCES tenant(tenant_id) ON DELETE CASCADE
);

-- Where one source bank's contribution stands. A row exists for every bank that
-- contributed at least once. A::myself is never a source: it is written by the
-- round and read as the previous version (decision 6).
CREATE TABLE IF NOT EXISTS personal_bank_state (
    tenant_id         text NOT NULL,
    source_bank_id    text NOT NULL,
    cursor_updated_at timestamptz NOT NULL DEFAULT '-infinity',
    cursor_unit_id    uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    last_merged_at    timestamptz,
    fail_count        integer NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, source_bank_id),
    FOREIGN KEY (tenant_id) REFERENCES tenant(tenant_id) ON DELETE CASCADE
);

-- The provenance ledger: which source fact a rollup entry was made from.
-- '__dropped__' marks a consumed-but-folded source, excluded from delete
-- detection exactly as in merge_source_unit (v3 R2).
CREATE TABLE IF NOT EXISTS personal_source (
    tenant_id      text NOT NULL,
    source_unit_id uuid NOT NULL,
    merge_key      text NOT NULL,
    bank_id        text NOT NULL,
    consumed_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, source_unit_id),
    FOREIGN KEY (tenant_id) REFERENCES tenant(tenant_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS personal_source_live_idx
    ON personal_source (tenant_id) WHERE merge_key <> '__dropped__';
CREATE INDEX IF NOT EXISTS personal_source_key_idx
    ON personal_source (tenant_id, merge_key);

-- The navigation index: an edge from the aggregate to a bank, aggregated from
-- personal_source. weight orders the directory.
CREATE TABLE IF NOT EXISTS personal_link (
    tenant_id  text NOT NULL,
    to_bank_id text NOT NULL,
    weight     integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, to_bank_id),
    FOREIGN KEY (tenant_id) REFERENCES tenant(tenant_id) ON DELETE CASCADE
);

-- The shared vocabulary: one normalized topic and the entries/banks holding it.
CREATE TABLE IF NOT EXISTS personal_topic (
    tenant_id  text NOT NULL,
    topic      text NOT NULL,
    bank_id    text NOT NULL,
    weight     integer NOT NULL DEFAULT 1,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, topic, bank_id),
    FOREIGN KEY (tenant_id) REFERENCES tenant(tenant_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS personal_topic_bank_idx ON personal_topic (tenant_id, bank_id);
```

与 v1 的三处结构差异：

1. **`source_bank_id` 从 `personal_source` 的主键里拿掉。** 一个源 unit 只存在于一个
   bank 里，unit id 本身就隐含了 bank；保留 `bank_id` 只为了让"按 bank 聚合成目录边"
   不必回表。主键回到 `(tenant_id, source_unit_id)`——这也是**跨 bank 去重**的前提：
   同一个 unit 只可能被记账一次。
2. **`personal_bank_state` 没有 `source_hash`/`last_error`**：水线增量下它只记游标；
   失败记在 `personal_state` 上（一轮就是一次调用，粒度对得上）。
3. **没有目标水线**（v2 有、v2b 删掉）：`A::myself` 是目标不是源，上一版就是它当前的
   全部内容，见 §6.3。

迁移归属：**`0004` 不加进 `adminTeamMigrations`**（`internal/storage/admin_migrate.go:29`），
所以单一 Tenant 部署也执行它。三个现有测试要一起改（v3 §四 的教训）：
`TestAdminMigrationSeries` 的 `len(series) != 3`、`TestMergeStateMigration` 的
`series[2]`、`TestAdminSeriesSkipsTeamTablesForSingleTenant` 的 `len(full) != 3` 与
`len(single) != 1`。最后一条内置了一串"single 系列不得含
`team_member`/`team_project`/`merge_state`/`merge_source_unit`"
（`internal/storage/admin_migrate_test.go:138`），所以断言要落成"团队表仍在被跳过，
`personal_*` 出现"——**不能**顺手把 `0004` 加进 `adminTeamMigrations` 让测试变绿。

### 5.1 为什么 fence 在 `personal_state` 上就够了

v3 的 fence 是按 `(team, project, member)` 逐行盖章。这里一轮读的是**一个 tenant 的
全部源水线增量**、写的是**同一个 `A::myself`**，所以 fence 只有一处：`personal_state`
的一行。`FinishPersonalRound` 是唯一写入入口，它先验 fence 再写事实、账本与水线，
一轮一个事务。`personal_bank_state` 只存游标，不参与并发控制。

这带来一个**必须写明的代价**：同一 tenant 的一轮是**串行**的，一个慢 bank 的合成会
推后其他 bank 进入下一轮。v3 的项目级锁是同一性质，只是这里的粒度是 tenant。
`DefaultPersonalRoundDeadline = 2 × interval，最小 5 分钟`，超过视为崩溃轮次可被接管。

## 六、`personal.rollup`：水线增量合成

### 6.1 一轮

`internal/app/personal_rollup.go`，注册进 `maintenanceJobs`（与 `project.merge`
并列，**没有** `TenantMode == multi` 的前提，也**没有**开关，见 §7.5）：

1. 发现：`admin.tenant` 中 `status='active' AND kind='personal'` 的 tenant
   （决策 3：团队 tenant 不汇总）。单一 Tenant 部署时这个查询退化成"那一个 schema"，
   因为默认 `tenant` 扩展没有注册表行——实现要同时覆盖"有注册表"与"无注册表、只有
   配置里的那一个 schema"两种情况（`tenant.Extension.ListTenants` 本来就是后台任务的
   枚举入口，`internal/tenant/tenant.go:81-84`）；
2. 每个 tenant 抢 `pg_try_advisory_xact_lock(hashtext('wenyuan:personal-rollup'),
   hashtext(tenant_id))`，检查/接管 `personal_state.merging_since`（v3 §5.2 原样）；
3. **读水线**：对每个源 bank（该 schema 的 `banks` 里除 `A::myself` 外的全部行）取
   `(updated_at, id) > cursor` 的 world 事实，按 `last_merged_at NULLS FIRST` 排序，
   合计不超过 `PersonalRollupBatchSize`（默认 20，与 `ProjectMergeBatchSize` 同量级）；
4. **读上一版**：`A::myself` 里 `fact_type='world'` 的全部行（§6.3）；
5. **删除检测**：每轮一次存活反连接（§6.3）；
6. 没有新事实、没有删除、没有重建请求 → 只更新 `last_round_at`，**不调模型**；
7. 有变化 → 调一次模型，写库（§6.4）；
8. 事实真的变了 → 入队一次 `consolidate`；目录（knowledge 树、`personal_link`、
   `personal_topic`）在同一事务里按 §7.2 的纯函数投影重建。

**一个 tenant 一轮一次模型调用**，不是每 bank 一次：水线已经给出"这一轮所有 bank 的
新事实"，而目标只有一个 `A::myself`；分 bank 循环会产出互相矛盾的版本（每个 bank 都以
为自己是全集），最后还得再合并一次。

### 6.2 输入与输出

输入：

```
A::myself 的当前内容（previous，全部进 prompt，受 MaxTeamFacts 约束）：
[P0] …
本批新事实（来自各源 bank，按 bank 分组）：
A::X:
[F0] … [F3] …
A::Y:
[F4] …
已知主题（personal_topic 前 PersonalRollupTopicWindow=30 条）：
T0 … T29
```

输出（`projectmerge.ParseAnswer` 的形状 + 一个 `topics` 成员）：

```json
{"facts": [{"text": "…", "sources": [0, 3], "topics": ["postgres-端口", "部署"]}]}
```

- `sources` 语义与 v3 完全一致：本轮 F 下标，服务端据此写账本，**不信模型给的 id**；
  一个 F 下标属于哪个 bank 由服务端从 batch 里查，模型无从伪造；
- `topics` 必须复用语表里已有的主题或提出新主题；新主题入 `personal_topic` 前过
  `tenant.Normalize`（`internal/tenant/mergekey.go:21`）；
- 空答案沿用 v3 的不变量：有 previous 时 `ErrAnswerEmpty` 拒绝整轮（否则会清空用户的
  汇总），没有 previous 时接受；
- **具体值必须保留**：prompt 要求端口、路径、版本、接口名、约束等具体值不得抽象掉。
  验收见 §十二 的 golden case。

### 6.3 变更检测：每源水线 + 存活反连接

v3 的两条判定在这里原样保留，只是"成员"换成了"该 tenant 的 bank"：

```sql
-- 水线：某个源 bank 的新事实。A::myself 不参与——它是目标，不是源。
SELECT id::text, text, updated_at
FROM <tenant>.memory_units
WHERE bank_id = $1
  AND fact_type = 'world'
  AND (updated_at, id) > ($2::timestamptz, $3::uuid)
ORDER BY updated_at, id
LIMIT $4;

-- 上一版：汇总 bank 的全部 world 事实。没有目标水线，因为只有一个写入者，
-- 也就不存在"自己的写入把游标推过未读数据"的问题（§0.1 的 v2 讨论）。
SELECT id::text, text, COALESCE(metadata->>'merge_key', ''), updated_at
FROM <tenant>.memory_units
WHERE bank_id = $self AND fact_type = 'world'
ORDER BY created_at, id;

-- 存活反连接（v3 R4 原样）：账本里仍在支撑内容的源 unit 不见了，就整轮重建。
SELECT 1
FROM admin.personal_source l
LEFT JOIN <tenant>.memory_units m ON m.id = l.source_unit_id
WHERE l.tenant_id = $1 AND l.merge_key <> '__dropped__' AND m.id IS NULL
LIMIT 1;
```

三条语句各自为什么不能省：

- **水线不能省**：全量重读就是 v1 的做法，写多的用户每改一个字都要整包重跑；
- **上一版全量读不能省、也不能截断**（v3 决策 14）：模型看不到的旧条目会被它当成
  "不存在"而丢掉。超出 `PersonalRollupMaxTeamFacts` 是容量问题，跳过本轮即可（§6.6）；
- **反连接不能省**：`updated_at` 在删除时不推进，水线看不见删除（v3 R4 的整个论证）。
  漏判的后果是**用户删掉的记忆继续出现在汇总里**，这是隐私底线。

水线只在 `FinishPersonalRound` 的同一事务里推进，且只推进到**实际消费**的最后一条；
失败或超时都不推进（§6.6）。没有单独的目标水线，因为 `A::myself` 从不作为源被读取。

### 6.4 写库（事务 B）

一个事务（同一 `*sql.Tx`，tenant schema 与 admin schema 同库，v3 §5.5 的前提）：

1. fence：`UPDATE personal_state SET merging_since=NULL WHERE tenant_id=$1 AND
   merging_since=$stamp`，`RowsAffected()==0` → `ErrPersonalClaimLost`，整笔回滚；
2. 上一版（`A::myself` 里 `fact_type='world'` 的行）按 `merge_key` 做三分类：
   - 同 `merge_key` → 保留原行（id 不变，observation 跟着走）；
   - 本轮结果里没有 → 删除该行，并清掉因此失去**全部**来源的 observation
     （照 `deleteStaleObservationsReportingTx`，`postgres_consolidation.go:462`；
     **不要**照 `DeleteBank`，`postgres_banks.go:396`——那是删整个 bank 的顺序）；
   - 新增 → 插入（§6.5 的 metadata）；
   - **兜底**：`A::myself` 里没有 `metadata.origin='aggregate'` 标记的 world 行
     （用户偶然写进来的，§0.2）一律删除——它们不是汇总条目，下一轮也不该存在。
     这一步同时把"用户写进来自以为能留下"的事实清掉，与决策 5 一致；
3. 账本：本轮消费的每个源 unit 写一行 `merge_key`（进了输出的）或 `'__dropped__'`；
   重建轮整体替换该 tenant 的账本；
4. 水线：每个被消费的 bank 推进到本批最后一条 `(updated_at, id)`；没达到的留到下轮；
5. `personal_state`：`last_round_at=now()`、`fail_count=0`、`last_error=NULL`；
6. 目录投影（§7.2）同事务写 `personal_link` / `personal_topic`；knowledge 树在事务
   提交后异步建（它要走既有 knowledge 服务，不在同一个 tx 里）。

### 6.5 汇总条目的 metadata

```json
{
  "origin": "aggregate",
  "merge_key": "sha256(normalize(text))",
  "sources": [{"unit_id": "…", "bank_id": "A::X"}, {"unit_id": "…", "bank_id": "A::Y"}],
  "banks": ["A::X", "A::Y"],
  "topics": ["部署", "postgres"],
  "merged_at": "2026-10-31T00:00:00Z"
}
```

- `origin` 在 v2b 里只是**兜底标记**（§0.2），不再划分两半数据；写它是因为第 2 步要
  用它把"不是我的行"识别出来；
- `sources` 是**发散**的路线：一条汇总条目 → 若干原文 unit（跨 bank）；
- `banks` 是**索引**的键：一条汇总条目出现在哪些 bank，`personal_link` 就是它的聚合；
- **不写 tag**（v3 决策 10：tag 会进 `ListTags` 与 fuzzy 解析词汇）；
- `fact_type='world'`、`document_id=NULL`。

### 6.6 失败与熔断

- 模型失败 / 形状错误 / 存储失败：`fail_count+1`、`last_error`，**水线不推进**，
  下一轮重试（同一批事实再喂一次，幂等）；
- `fail_count >= PersonalRollupFailThreshold`（默认 5）：该 tenant 停摆，直到
  `POST /admin/v1/tenants/{id}/aggregate-reset`。停摆期间**上一版照常可读**（宁可
  陈旧，不能消失）；
- 上一版条目数超 `PersonalRollupMaxTeamFacts`：容量问题，本轮跳过并记 `last_error`，
  **不失败关闭**（v3 R8）；
- 重建输入超 `PersonalRollupRebuildMaxUnits`（默认 5000）：**失败关闭**——清掉
  `A::myself` 的全部汇总条目与失去来源的 observation，让用户读到的是各个源 bank
  本身。这是隐私底线（v3 §7.2 同一判断）；
- 汇总 bank 行被删：下一轮 `ensureAggregateBank` 幂等重建，`personal_state` 保留，
  水线不动（不会因此重放历史，上一版为空即从头合成一次）。

## 七、目录：纯函数投影出来的索引

### 7.1 目录是什么、不是什么

目录 = **在已经产出的内容上的一次投影**，没有自己的模型调用、没有自己的状态机：

- **是什么**：`A::myself` 里的一棵 knowledge 树（分组 → bank 页 → 主题页）、
  `personal_link`（bank → 权重）与 `personal_topic`（主题 → bank）。
- **不是什么**：不是第二个 LLM 任务。v1 设想过 `personal.index` 独立跑一次模型产出
  分组与页标题；产品和架构都否掉了——它吃一次预算、引入"目录过期"状态、而输入
  （主题与溯源）本来就在库里，排序与分组是确定性的。

### 7.2 怎么算（每轮、同事务）

| 产物 | 由什么算 | 规则 |
|---|---|---|
| `personal_link.weight` | `personal_source` 按 `bank_id` 计数 | 引用次数；`__dropped__` 不计 |
| `personal_topic` | 汇总条目的 `metadata.topics` × `metadata.banks` | 主题归一化后 upsert；条目消失则权重递减，归零删除 |
| 树分组 | 权重排序后的 bank 前 `PersonalDirectoryTopGroups`（默认 8）个 | 分组名取该组最高权重主题；剩余归"其他" |
| bank 页 | 每个有汇总条目的 bank | `title` = 该 bank 里 `name`（没有则 bank id）；正文由页面的 `source_query` 生成 |
| 主题页 | 权重前 `PersonalDirectoryTopTopics`（默认 20）个主题 | 同上 |

页面的 `source_query` 是确定性生成的中文句子：
`"来源包含 A::X 的汇总结论"` / `"主题：部署"`——它交给 knowledge 的 refresh 去
`A::myself` 取内容从句，**正文不进我们的事务**。

**先 diff 后写**：只有新增/消失的 bank 与主题才动节点（knowledge 节点第一次创建会
建一个 backing mental model 并入队一次 refresh，`internal/reporting/knowledge_nodes.go:44-75`），
不能每轮重建整棵树——否则每轮都是几十次模型调用。

### 7.3 读的时候怎么用

| 动作 | 调用 | 得到 |
|---|---|---|
| 整体定位 | `GET /banks/A::myself/knowledge-base/tree` | 分组 + bank 页 + 主题页 |
| 进入某页 | `GET …/pages/{id}` | 该 bank 的汇总 markdown |
| 直奔事实 | `POST /banks/A::myself/memories/recall` | 汇总条目 + `metadata.sources` |
| 展开到原文 | `POST /banks/A::X/memories/recall`（`bank_id` 来自 sources） | 原文事实/经历 |
| 顺边发散 | `personal_link` → 控制台展示；数据面靠 `metadata.banks`/`topics` 串起主题页 | 相邻 bank、相邻主题 |

**数据面不新增路由**（Q5 已定）：目录走已有 knowledge 路由，发散走已有 recall 路由 +
`metadata.sources`；"一次性全量读"不做，内容只按水线增量自动整合。

### 7.4 词表：跨 bank 去重的唯一机制

决策 10 把"两个 bank 说同一件事"的判断从模型调用里拿出来，交给 `personal_topic`：

- rollup 每轮把已知主题（前 `PersonalRollupTopicWindow`=30 条）注入 prompt，要求新
  条目**优先复用**已有主题；新主题只在确实没有合适旧主题时才允许；
- `A::X` 产出"deploy 端口 15432"、`A::Y` 产出"部署用的是 15432"：两条是不同 bank 的
  事实，但**主题相同** → 在汇总的排序里相邻、在目录里同一组、`personal_link` 有权重。
  用户看到的"去重"是**分组**，不是把两条合成一条；
- 词表的归一化与合并（同义主题）在 §7.2 的投影里做，rollup 只增不改。

### 7.5 门控：单一 Tenant、团队 Tenant、团队模式

这是最容易搞混的一处，单独写清楚。

| 条件 | `project.merge` | `personal.rollup` |
|---|---|---|
| `tenant_mode: single` | 不注册 | **注册**（产品定调，V1） |
| `tenant_mode: multi`，个人 tenant | 注册 | 注册 |
| `tenant_mode: multi`，团队 tenant（`kind='team'`） | 以其项目为对象 | **不是汇总对象**（产品定调）；它自己的 bank 仍可被 `project.merge` 使用 |
| 无 chat provider | 不注册 | 不注册（没有模型时每轮只会写 `last_error`） |

**没有开关**：不存在 `PERSONAL_AGGREGATE_ENABLED` 这样的键。能停的只有两个自然条件：
没有 chat provider；维护循环没在跑（`tenant_mode` 不影响）。后台发现要覆盖
"有注册表"与"无注册表、只有配置里那一个 schema"两种情况
（`tenant.Extension.ListTenants`，`internal/tenant/tenant.go:81-84`）。

多租户 + `auto_provision` 下，`tenant://alice` 与 `tenant://bob` 是两个 tenant、两个
schema，各自的 `A::myself` 独立，会话不串。`tenant_mode: single` 下所有 key 解析到
同一个 schema，"用户的汇总"即"这个部署的汇总"——这与该部署里所有 bank 本来共享一个
schema 一致，不是新泄漏；但控制台文案不能写成"你的私人记忆"，管理面标
`shared_schema: true`。

接线：`internal/app/background.go:33`、`internal/app/app.go:310`、
`cmd/worker/main.go:126` 三处装配 `project.merge` 的位置旁边各装配一次本任务，条件
`tenantMode ∈ {single, multi} && model != nil`。不碰 `AnyJobEnabled`
（`internal/config/maintenance.go:94-103`，目前没有调用方）。

## 八、读语义与写语义

### 8.1 读

| 请求 | 行为 |
|---|---|
| `recall` / `list` / `knowledge` / `mental-models` 寻址 `A::myself` | 正常；它是普通 bank，内容 = 汇总条目 + 由它们生成的 observation |
| 同类请求寻址 `A::X` | **完全不变**（决策 14），包括 `teamread` 的团队回退 |
| `A::myself` 的 `documents`、`chunks` | 空；汇总条目 `document_id` 恒空 |
| `entities` / `graph` | 只有 `A::myself` 自己的实体（汇总条目各自计算），不跨 bank 汇总 |
| `list?state=invalidated` | 汇总条目也会进入 invalidated（用户可标记失效），不影响源 bank；下一轮 rollup **不会**把它变回有效（它只把有效的上一版带进 prompt） |

可见性窗口：写 `A::X` 后到 `A::myself` 出现对应结论，上界是
`PersonalRollupInterval` + 一次模型调用（默认 300s + 一次调用）。汇总对象是"沉淀"
不是"实时"；需要即时结果的用户直接读源 bank。

### 8.2 写：路由不加拒绝，但内容归后台

`myself` **不是保留名**（V2）：写路由一行不改，`ValidBankID`
（`internal/storage/bank_ids.go:13-27`）本来就接受 `::` 与 `myself`，retain 的懒创建
（`internal/storage/postgres_retain.go:22-31`）本来就接受任意合法 bank id。v1 设计的
409 写门与"接管"逻辑全部不存在。

但与 `A::X` 的区别是**内容归属**（决策 5）：

- 往 `A::X` 写 → 是源，下一轮进入汇总；
- 往 `A::myself` 写 → **不是源**（§6.3 的水线不读它），下一轮被 §6.4 第 2 步的兜底
  清掉。这是 v1 的"团队 bank 直写会被替换"（v3 设计 §7.4）在个人场景的同一取舍；
- 它为什么还能写、且没有报错：产品前提是"用户不会这么干"（§0.2），所以数据面不为一个
  不会发生的操作加一个可能挡住正常客户端的拒绝；`aggregate-status.user_facts` 会把这个
  计数暴露给运维，静默变成可见。

### 8.3 删除

`A::myself` 的内容是派生物，用户删它没有意义，产品前提也说了不会删（决策 5）。系统
层面的处置是确定性的：

| 动作 | 行为 |
|---|---|
| 用户删 `A::myself` 里的一条汇总条目 | 删得掉（数据面无写拒绝）；下一轮 rollup 会重建它——它仍被账本里的源支撑 |
| 用户删 `DELETE /banks/A::myself` | bank 行没了：下一轮 `ensureAggregateBank` 重建空 bank，`personal_state` 保留、水线不动，于是下一轮按全部水线之后的事实重新合成一次 |
| 用户删 `A::X` 里的一条事实 | 它是**源**：下一轮反连接命中 → 整轮重建 → `A::myself` 不再含该内容（隐私底线） |
| 用户删 `A::X`（整个 bank） | 同上，整轮重建；`personal_bank_state` 的行随之下次清理 |

**不做"用户删除即永久屏蔽"的墓碑**：汇总条目是派生物，屏蔽语义会让"删源事实"与
"删派生"两种意图混在同一张表里；真要撤回，用户删源事实即可（上一行）。

## 九、生命周期与边界情形

| 事件 | 处理 |
|---|---|
| 首次运行 | 该 tenant 第一次有水线增量时，`ensureAggregateBank` 幂等建 `A::myself` 的 bank 行（`disposition` 默认、`mission` = 跨 bank 汇总用途） |
| 新建 `A::X` | 下一轮水线增量，进汇总与目录 |
| 删除 `A::X` | 下一轮反连接命中 → 整轮重建；该 bank 的条目与 `personal_link` 边随之消失 |
| 删除 `A::X` 里的一条事实 | 同上，整轮重建后不再出现在汇总里（隐私底线） |
| 用户往 `A::myself` 写 | 不是源；下一轮被兜底清掉，`user_facts` 计数可见（§8.2） |
| `A::myself` 是升级前就存在的用户工程 bank | 保留其 bank 行与非汇总内容？——**不保留 world 事实**：它们是"直写"，按决策 5 下一轮被替换。第一次发生时在 `aggregate-status` 里记 `adopted_preexisting: true` 与首次清掉的条数，让运维知道这个 id 原来有用户数据 |
| 该 tenant 是**项目成员**，项目名恰为 `myself` | `A::myself` 既是汇总目标又是项目成员 bank。团队读回退（`teamread`）照旧按成员资格决定是否读 `T::myself`；`project.merge` **排除** `<member>::myself` 的输入（N4 = A，§3.5），个人汇总结论永不进入团队内容。代价：该用户在这个项目上的贡献不进团队，`aggregate-status.team_project_named_myself` 与 `merge-status` 都可见 |
| tenant 软删 | 不再滚动；schema 还在时照常可读 |
| tenant purge | 删 schema 后 `personal_*` 随 FK 级联删除 |
| team tenant | **不汇总、不建 `A::myself`**（决策 3）；它的 bank 仍受 `project.merge` 管理 |
| 部署从 multi 降级到 single | `personal_*` 表保留，个人 schema 仍汇总；团队表不再使用 |

## 十、管理面与控制台## 十、管理面与控制台

只在管理面（operator key），不进 `/openapi.json`：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/v1/tenants/{id}/aggregate-status` | `{bank_id, shared_schema, kind, last_round_at, last_error, halted, fail_count, source_banks, pending_banks, aggregate_facts, user_facts, directory_generated_at, links, topics}` |
| POST | `/admin/v1/tenants/{id}/aggregate-reset` | 清熔断 |
| POST | `/admin/v1/tenants/{id}/aggregate-rebuild` | 置 `rebuild_requested`（下一轮忽略水线全量重算） |

- `kind` 为 `team` 的 tenant 返回 `aggregate:false` 与一个明确原因（决策 3）；
- `user_facts` 是用户直接写进 `A::myself` 的事实数，`aggregate_facts` 是汇总条目数——
  运维看一眼就知道两类行各有多少，不需要进库；
- 没有 `enabled` 字段：功能不可关闭（决策 4）。`halted` 是熔断，不是开关。

控制台"我的记忆全景"页：分组目录（由 `personal_link`/`personal_topic` 投影）+ 每个
bank 的卡片 + 关联线；文案进 10 语种。**只读**：汇总条目是派生物，编辑会在下一轮被
重建（§8.3）。

## 十一、配置

**只有节奏与预算，没有开关**（决策 4）。新增键
（`internal/config/maintenance.go` + `internal/config/reference.go` +
`scripts/render_config_reference.py` 重新生成 `docs/CONFIGURATION.md` /
`etc/wenyuan.yaml` / `.env.example`）：

```
WENYUAN_API_PERSONAL_ROLLUP_INTERVAL_SECONDS        默认 300   ← 水线推进的节奏
WENYUAN_API_PERSONAL_ROLLUP_BATCH_SIZE              默认 20    ← 一轮消费的新事实条数
WENYUAN_API_PERSONAL_ROLLUP_MAX_TEAM_FACTS          默认 500   ← previous 上限（容量，超限跳过）
WENYUAN_API_PERSONAL_ROLLUP_REBUILD_MAX_UNITS       默认 5000  ← 重建输入上限（超限失败关闭）
WENYUAN_API_PERSONAL_ROLLUP_FAIL_THRESHOLD          默认 5     ← 熔断
```

- 与 v1 的差别：**删掉了 `PERSONAL_AGGREGATE_ENABLED`、`PERSONAL_INDEX_INTERVAL_SECONDS`、
  `PERSONAL_INDEX_MAX_BANKS`**——前两个是 v1 的"开关"与"第二个模型任务"，第三个是
  v1 的 bank 级容量闸门，在"一个 tenant 一轮一次调用"的模型下不再有意义。
  `ROLLUP_INTERVAL_SECONDS` 设成 `<=0` 只表示"降到默认"，**不表示关闭**（`resolveMaintenance`
  对非正数回落到默认值，`internal/config/maintenance.go:123-139`）——这条要在
  `docs/CONFIGURATION.md` 的键说明里写死，否则运维会以为设 0 就关了；
- 没有 chat provider 时任务不注册（`personalEnabled`，§7.5）——这是唯一"不跑"的情况，
  且是因为跑了也没有意义；
- 非配置常量（代码内，与 `roundDeadline` 同形，不进 `reference.go`）：
  `PersonalRollupTopicWindow = 30`、`PersonalDirectoryTopGroups = 8`、
  `PersonalDirectoryTopTopics = 20`、`DefaultPersonalRoundDeadline = 2 × interval，最小 5min`。

## 十二、验证

纯函数（`internal/projectrollup`，不依赖 DB）：

- `ParseRollupAnswer` 的 `topics` 越界/缺省；空答案在有无 previous 下的两种处置；
- `Diff` 三类（复用 `projectmerge.Diff` 语义）；
- `PersonalPrompt`：每条 F 都在 prompt 里、F 的归属 bank 分组正确、上一版每条都在、
  主题清单被注入；具体值保留的 golden case（"应保留端口/路径/版本"的输入与期望子串）；
- `Directory(links, topics)`：纯函数投影的分组、权重排序、"其他"组的判定。

PostgreSQL 集成（必须实际执行，`WENYUAN_TEST_DATABASE_URL`）：

- **水线推进**：先写 `A::X`、`A::Y`；跑一轮后两条水线各推进到其最后一条；下一轮
  零模型调用、零写入；
- **批次截断**：新事实超过 `PersonalRollupBatchSize` 时只消费该条数，水线只推进到实际
  消费的位置，剩余下轮继续；
- **删除传播**：删 `A::X` 的一条被引用的源事实 → 反连接命中 → 整轮重建 → `A::myself`
  不再含该内容、其 observation 同步消失；
- **被折叠来源被删不触发重建**（账本 `'__dropped__'` 语义，v3 R2）；
- **兜底清写**：往 `A::myself` 写三条不带 `origin` 的事实，跑一轮，三条都没了，
  且 `user_facts` 计数在轮后归零、轮前可见；
- 团队 bank `T::X` 的内容不出现在 `A::myself`（给团队 bank 塞独有事实，断言不入汇总，
  `metadata.sources` 不含跨 schema 的 bank id）；
- 团队 tenant（`kind='team'`）**没有** `A::myself`，`aggregate-status` 返回
  `aggregate:false`；
- 两个进程同时跑同一 tenant：只有一个执行，另一个 fence 失败且无副作用；
- 熔断：连续 5 次模型失败后停摆、`halted=true`、reset 后恢复；停摆期间旧汇总照常读。

端到端（served handler）：

- `POST /banks/A::myself/memories/recall` 命中汇总条目，`metadata.sources` 是
  `[{unit_id, bank_id}]`，指向的 unit 真实存在；
- `GET /banks/A::myself/knowledge-base/tree` 有分组、bank 页与主题页；
- `POST /banks/A::myself/retain` → **200**（不是 409，V2），随后跑一轮 → 那条事实
  不再出现在 `A::myself`（V3）；
- `GET /banks/A::X/memories/list` 与升级前**逐字节相同**（数据面不因本功能改变）；
- `tenant_mode: single` 部署下任务注册、`A::myself` 可读，且**没有任何关闭它的配置键**；
- 项目名恰为 `myself` 的成员 tenant：跑两轮团队合并，`T::myself` 的内容里**不出现**
  任何个人汇总结论（N4 = A），且 `aggregate-status.team_project_named_myself=true`。

## 十三、拍板记录（2026-10-31，产品）

v1 列过五个问题，产品一次性定了：

| # | 问题 | 结论 | 落在哪 |
|---|---|---|---|
| Q1 | 单一 Tenant 部署要不要开 | **开，且不可关闭** | 决策 4、§7.5、§十一 |
| Q2 | 团队 tenant 要不要也有 `myself` | **不要**：团队是团队不是个人 | 决策 3、§6.1、§九、§十 |
| Q3 | `A::myself` 撞上用户自己的同名 bank | **不是保留名**，写路由不加拒绝 | 决策 2、§8.2 |
| Q4 | 关闭后已生成的汇总怎么办 | 问题消失：**没有关闭开关** | 决策 4、§十一 |
| Q5 | 目录要不要"一次性全量读" | **不做**，也**不单独跑索引任务**：按水线自动整合 | 决策 16、§七 |
| Q6 | `A::myself` 归谁写 | **只归后台**：用户不写、不删它的内容，内容全部由该 tenant 的其他 bank 推导 | 决策 5、§0.1、§6.3、§8.2 |

Q6 把 v2 因 Q3 产生的三个中间问题消掉了两个：

| 中间问题（v2 有） | v2b 的结论 |
|---|---|
| N1 用户行与汇总行怎么区分 | 不需要区分数据线；`origin` 只作兜底标签（§0.2） |
| N2 一个 bank 既是源又是目标，水线怎么算 | 不成立：`A::myself` 只是目标，每个源 bank 一条水线（§6.3） |
| N3 汇总被自己的写入推进水线而漏读源 | 不成立：没有任何读路径把 `A::myself` 当源 |

**唯一还需要产品确认的一处**（不在上面六条里，是本轮改稿时发现的）：

| # | 问题 | 选项 |
|---|---|---|
| N4 | 某用户正好是某个**项目名 `myself`** 的项目成员时，`A::myself` 同时是"汇总目标"与"`project.merge` 的成员 bank" | **已定 A（2026-10-31）**：`project.merge` 的成员输入排除 `<member>::myself`，个人汇总结论永不进入团队内容；代价是该用户在那个项目上的贡献不进团队（§3.5） |

## 十四、分期与工作项## 十四、分期与工作项

```
P-0 迁移与模型（§五）：0004 五张表 + AdminMigrationSeries 测试改动
 │
 ├─▶ P-1 纯函数 internal/projectrollup（§十二 第一组）────────┐
 │                                                            │
 ├─▶ P-2 存储 PostgresPersonalStore（§6.3 读 / §6.4 写）──────┴─▶ P-3 rollup 任务 + 配置（§六 / §7.5 / §十一）
 │                                                                  │
 │                                                                  ├─▶ P-4 目录投影 + knowledge 树（§七）
 │                                                                  │
 │                                                                  └─▶ P-5 管理面（§十）
 │                                                                        │
 └────────────────────────────────────────────────────────────────────────┴─▶ P-6 控制台 + 文档
```

- **PR-1 = P-0…P-3**：汇总可用（水线、账本、`sources` 定位）。验收 = §十二 的纯函数组 +
  PG 集成组 + "`POST /banks/A::myself/retain` 返回 200、随后跑一轮被兜底清掉"那条端到端
  （V2 与 V3 一起验）；
- **PR-2 = P-4…P-5**：目录投影与管理面；
- **PR-3 = P-6**：控制台、`AGENTS.md`、`docs/CONFIGURATION.md` 重新生成。

与 v1/v2 的分期差异：

- v1 的"保留名与 409 写门（P-3）"**删除**——`myself` 不是保留名，写路由不用改；
- v2 的"目标水线 / 双写分半"**删除**——`A::myself` 归后台独占（Q6），水线一条；
- v1 的独立 `personal.index` 任务合并进 PR-2 的目录投影（没有模型调用）；
- **N4 = A** 随 PR-1 落地：`project.merge` 的成员输入（`internal/storage/postgres_merge_store.go`
  的 `loadMemberInput`）在 `member.ProjectName == tenant.PersonalBankName` 时返回空
  `Fresh`，并加一条 PG 测试（项目名 `myself` 的项目合并两轮，团队 bank 里不出现个人
  汇总结论）。它只碰团队合并的读输入，不改任何数据面行为，所以放 PR-1 而不是另开 PR。

`AGENTS.md` 要补五句：`A::myself` 是**每个个人 tenant** 的、后台自动生成与管理的汇总
bank；单一 Tenant 部署同样启用且不可关闭；团队 tenant 没有它；用户往里写的内容不是
源、下一轮会被替换；`project.merge` 排除 `<member>::myself`。再加上**它不改任何数据面
路由与 JSON**。

## 十五、风险与非目标

### 15.1 为什么没有第三个任务

考虑过把 `A::myself` 的 `observation` 也交给模型再合成一层（"更高更全"的汇总）。
**不做**，理由三条：

1. observation 的 id 由 consolidation 生成、随源事实重建而变，把它当输入等于把
   "会抖动的行"当作账本键——v3 在同一个坑里栽过一次（决策 7/13）；
2. 用户要的是"全"，多一层抽象只会更远于原文；"全"由 `sources` 指回原文保证；
3. 每多一层就多一次模型调用与一份可见性窗口，收益无法证明。

同理，目录（§七）也不单独调模型：它的输入本来就是 rollup 产出的主题与溯源。

### 15.2 风险

| 风险 | 对策 |
|---|---|
| 汇总条目压缩掉具体值 | prompt 硬要求 + §十二 的 golden case；`sources` 永远指回原文 |
| 模型调用成本 | 一个 tenant 一轮**一次**调用（§6.1）；水线让无变化的 tenant 零调用；默认 300s |
| 一轮消费不完 | `PersonalRollupBatchSize` 限制新事实条数，水线只推进到实际消费的位置 |
| 上一版超 `MaxTeamFacts` | 容量问题：本轮跳过 + `last_error`，**不**失败关闭（v3 R8） |
| 删除漏判 → 已删记忆仍可见 | 存活反连接每轮做，不做版本粗筛（v3 R4）；重建输入超限时失败关闭 |
| 用户以为往 `A::myself` 写能留下 | 决策 5 明确它是后台生成物；`user_facts` 计数暴露给运维（§8.2），不会静默 |
| 用户删掉某条汇总条目 | 下一轮重建（§8.3）；要撤回就删源事实 |
| 汇总 id 恰好是某成员的项目 bank | N4 = A：团队合并排除 `<member>::myself`（§3.5），个人汇总结论不进团队；`aggregate-status.team_project_named_myself` 显式暴露 |
| knowledge 树每轮重建花模型调用 | 先 diff 后写，只有新增/消失的 bank 与主题动节点 |
| 团队 tenant 被误汇总 | 发现在 `kind='personal'` 上过滤（§6.1）+ §十二 端到端测试 |
| 单一 Tenant 部署的控制台文案误导 | `aggregate-status.shared_schema=true`（§7.5） |

### 15.3 非目标

- 自动向 `A::X` 写入（汇总单向：源 → 汇总）；
- 让用户往 `A::myself` 写并参与汇总（决策 5；真要如此就得回到两条水线，§0.3）；
- 新增数据面路由（目录走 knowledge，发散走 `metadata.sources`；"一次性全量读"不做）；
- 跨 tenant 汇总（`A::myself` 只含该 tenant 自己 schema 的 bank）；
- 把团队 bank `T::X` 的内容抄进 `A::myself`（决策 8）；
- 团队 tenant 的 `A::myself`（决策 3）；
- 关闭本功能的开关（决策 4）；
- rollup 的内存后端（跨 schema/admin 事务、fence 都没有内存对应物，理由同 v3 R13）；
- 用户编辑/屏蔽汇总条目；
- 汇总 bank 的配额、限流与成本核算。

## 十六、实施时最容易做错的五处

v1、v2 自己就错了其中四处，按改稿顺序列：

1. **把 `A::myself` 当托管 bank，对写返回 409**（v1）。错的：`myself` 不是保留名。
   写路由一行不改。
2. **以为用户写进 `A::myself` 的事实是源**（v2）。错的：它由后台独占生成（Q6）。
   水线不读它，兜底清掉它。
3. **为了"双写"设计两条水线**（v2）。Q6 之后是多余复杂度：每个源 bank 一条即可，
   `A::myself` 从不当源被读。
4. **上一版截断，或只做增量而忘了把上一版全量带进 prompt**。前者丢旧内容，
   后者模型会把没见过的旧条目当成不存在（v3 决策 14）。
5. **删除语句没带 `origin='aggregate'` 的兜底语义，或照抄 `DeleteBank`**。照抄会把
   整个 `A::myself` 清空；正确的参照是 `deleteStaleObservationsReportingTx`。

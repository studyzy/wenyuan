# Team Memory（项目合并与共享记忆）—— 设计方案 v3

状态：2026-10-30 评审修订。取代
`docs/plans/2026-10-29-team-project-shared-memory-design.md` 的 §六、§七、§八，
并取代 `docs/plans/2026-10-31-team-merge-implementation-plan.md`（B 部分实施计划）
的错误部分。A 部分（`docs/plans/2026-10-31-team-registry-implementation-plan.md`，
已合并 `620fa48`）的团队对象维护不受影响。

范围：本服务自己的能力，不属于参考实现的兼容面。数据面的路由、JSON 字段、
状态码、请求头一律不改；团队管理只出现在管理面。

## 一、v3 修订摘要

评审 2026-10-31 的实施计划后确认：**骨架对、三个机制错**。v3 只改机制，不改
分期与产品范围。

| # | 主题 | v2 / 实施计划 | v3 | 依据 |
|---|---|---|---|---|
| R1 | `MergeStore` 归谁持有 | 暗示挂在 `PostgresStore` 上，schema 当参数传 | 独立对象，直接持 admin 与租户两个句柄；**所有 schema 一律走 context**（与 `schemaFor` 一致） | `schemaFor` 一个 context 只解一个 schema；`PostgresTenantRegistry` 与 `PostgresStore` 本就是两个对象 |
| R2 | 账本粒度 | `merge_source_unit` 只记"消费过" | 账本记 **`(source_unit_id) → merge_key`**，加 `'__dropped__'` 标记冗余来源 | 只记"消费过"会让"来源被判定冗余后删除"触发全量重建；重建受上限约束，一次误判可清空团队内容 |
| R3 | 模型输出契约 | `{"facts": ["…"]}` 纯文本 | `{"facts": [{"text": …, "sources": [下标…]}]}`，下标指本轮 `fresh` 输入 | 纯文本无法回溯来源，账本的支撑关系写不出来 |
| R4 | 删除检测 | `(updated_at, count)` 粗筛 + 偶尔全反连接 | **每轮一次"存活支撑"反连接**，去掉 `bank_updated_at`/`bank_unit_count` | `updated_at` 在删除时不推进；"删一条加一条"计数不变 → 粗筛判"没变"永久漏判 |
| R7 | 锁与事务边界 | "整轮一个事务，模型调用在事务内" | **两段事务 + 事务级 advisory lock 承载第一段**，模型在锁外；用 `merge_state` 的 `merging_since` 列做跨段的互斥 | 设计 §六 原文就是"模型调用在事务之前"；本仓库所有 advisory 锁都是 `pg_advisory_xact_lock` + 回调，无 session 锁先例 |
| R8 | 团队上一版无上限 | 只限新输入 | 加 `ProjectMergeMaxTeamFacts`（默认 500），超限不合并、**不失败关闭** | 决策 14 要求上一版始终在输入里，项目跑一年 prompt 会爆 |
| R9 | 读回退位置 | handler 层逐处改（9+ 处 `requireBank`） | **storage 层内部回退**，`RecallQuery` / `MemoryQuery` 各加一个 `Fallback` 字段 | 设计 §四 决策 16 原本就定在 storage；recall 的 `tagVocabulary`/`graph`/`chunks` 全按 `q.BankID` 读，handler 层要逐处补 |
| R13 | 内存后端 | 要给 `MergeStore` 写内存实现 | **不做**。`MergeStore` 只有 PG 后端，引擎行为全部走 PG 集成 | 跨 schema + admin 事务 + advisory lock + FK 级联，内存版测不到任何真实风险，只会让测试"通过"一套假语义 |
| R6 | 退出项目的账本 | `merge_state`/账本靠 `team_member` 级联 | 退出**项目**必须显式删账本（外键挂在 `team_member`/`team_project`，不挂 `team_project_member`） | 漏删 → 该成员账本行永不匹配输入 → **每轮都触发重建，永不收敛** |
| R11 | `purge-contribution` | 未启用合并时返回 `rebuild:false`（半吊子状态） | `DELETE .../members/{id}` 只移出；`purge-contribution` 是"移出 + 清账本 + 重建"的原子操作，失败即回滚 | "移出了但内容还在"不叫撤回；且破坏 A 部分已发布的响应形状 |
| R5 | 回退后的过滤语义 | 未定义 | 逐一列表（见 §8.3） | `state=invalidated`、`document_id`、`entity_id` 回退后必然为空，需明写 |
| R12 | 团队 bank 被直写 | "下一轮会删掉" | 保留该行为，但计数进 `merge-status` 供运维告警 | 静默丢用户数据是最难排查的一类 |
| R14 | 单 Tenant 兼容未成文 | 未论证"无 Team 时现有功能不变" | **§十三 逐调用方清单 + 三条可验证条款**；`BankHasUnits` 改为能力检测（GAP-1）；`AnyJobEnabled` 计入条件含 `mergeStore != nil`（GAP-3） | 兼容性声明没有测试就是没有声明；给 `Store` 加方法会强迫所有实现与测试替身付编译代价 |

不改的（评审确认做对）：§B3 的"只回退 recall 与 list"、§B2 的"不写 tag"、
§B4 的"个人 bank 不存在时团队内容保留"、两个 PR 的切分。

**§十三.3.1 有一处待拍板**：`0003` 是否在单 Tenant 部署里执行（方案 A/B）。两案都
不破坏现有功能，区别只在"单 Tenant 的库里该不该有团队表"。

## 二、问题与决策（承 v2 §二、§三，只列改动）

v2 的 19 条决策全部保留。其中三条在 v3 被**具体化**，实现以 v3 为准：

| # | v2 原文 | v3 落地 |
|---|---|---|
| 9 | "按内容 hash 增量保留，内容不变的事实保留原行" | 保留原行的判据是 `merge_key`；`merge_key = sha256(normalize(text))`，**不含来源**。因此一条团队事实的来源集合变化（某成员改了措辞的心一样的话）不改变 `merge_key`，id 不变 |
| 11 | "`(updated_at, id)` 双键游标；`LastWriteAt` 只做粗筛" | 游标保留，**粗筛删掉**（R4）。每轮直接做存活支撑反连接；新输入靠游标增量读，`idx_memory_units_bank_updated_at` 已存在，无需新索引 |
| 12 | "反连接检测删除 → 重建；重建失败失败关闭" | 反连接**只查 `merge_key <> '__dropped__'` 的行**（R2）；失败关闭保留，但仅限"重建输入超上限"这一种（R8 的超限是容量问题，不是隐私问题） |

## 三、数据模型（`0003_merge_state.sql`）

A 部分已定的不能动：`0002_team_registry.sql` 的表结构、`team_project.bank_id`
单列、无 `team_project_member.project_name`。新的表结构只走 `0003_*.sql`。

```sql
-- 0003_merge_state.sql
--
-- The merge ledger: which member fact each team fact is made of, and where each
-- (project, member) pair's incremental cursor stood.
--
-- One deviation from the design's table is load-bearing: the ledger records the
-- merge_key a source supports, not merely that it was consumed. A source the
-- model judged redundant lands under '__dropped__' and is excluded from delete
-- detection, so removing it does not force a rebuild.

CREATE TABLE IF NOT EXISTS merge_state (
    team_tenant_id    text NOT NULL,
    project_id        text NOT NULL,
    tenant_id         text NOT NULL,
    cursor_updated_at timestamptz NOT NULL DEFAULT '-infinity',
    cursor_unit_id    uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    last_merged_at    timestamptz,
    last_error        text,
    fail_count        integer NOT NULL DEFAULT 0,
    -- merging_since is the cross-transaction half of the merge lock: the first
    -- transaction stamps it, the second clears it. A row older than the round
    -- deadline is a crashed round and may be taken over (see §六 concurrency).
    merging_since     timestamptz,
    rebuild_requested boolean NOT NULL DEFAULT false,
    PRIMARY KEY (team_tenant_id, project_id, tenant_id),
    FOREIGN KEY (team_tenant_id, project_id)
        REFERENCES team_project(team_tenant_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (team_tenant_id, tenant_id)
        REFERENCES team_member(team_tenant_id, tenant_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS merge_source_unit (
    team_tenant_id  text NOT NULL,
    project_id      text NOT NULL,
    tenant_id       text NOT NULL,
    source_unit_id  uuid NOT NULL,
    -- The merge_key of the team fact this source supports, or '__dropped__'
    -- when the model folded it away. Never NULL: "consumed but unsupported" and
    -- "not consumed" must not be the same row.
    merge_key       text NOT NULL,
    consumed_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_tenant_id, project_id, tenant_id, source_unit_id),
    FOREIGN KEY (team_tenant_id, project_id)
        REFERENCES team_project(team_tenant_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (team_tenant_id, tenant_id)
        REFERENCES team_member(team_tenant_id, tenant_id) ON DELETE CASCADE
);

-- Delete detection reads the live sources of one member; the primary key puts
-- source_unit_id last, so this index is what makes that scan index-only-ish.
CREATE INDEX IF NOT EXISTS merge_source_unit_live_idx
    ON merge_source_unit (team_tenant_id, project_id, tenant_id)
    WHERE merge_key <> '__dropped__';
```

与实施计划的两处结构差异：

- **`bank_updated_at` / `bank_unit_count` 不建**（R4）。粗筛漏判，且漏判的后果是
  已删数据继续可见——正是隐私底线要防的。
- **`cursor_unit_id` 用 `uuid`**（与 `memory_units.id` 同型），`merge_key` 与
  `'__dropped__'` 的比较是文本。

`fail_count` 与 `merging_since` 是两个不同的问题：前者是"这个项目连续失败了几轮"
（熔断，实施计划 §五 风险表已提到但 §B1 的表里没有），后者是跨事务锁（§六）。

### 账本的两个不变量

写在文档里，因为它们在实现时最容易被绕过：

1. **账本行只增不删，除三种操作**：重建成功（整体替换）、成员退出项目（`R6`）、
   成员退出团队（FK 级联）。增量轮绝不删账本行。
2. **一条团队事实的 `merge_key` 必须至少有一个来源行**，除非它来自 `previous`
   的保留（那时来源行是上一轮留下的，仍在）。重建后若某 `merge_key` 无来源，
   说明模型凭空生成了它——**记 `last_error` 并在下一轮按普通增量处理**，
   不删该事实（删会抖动；凭空生成本就是模型问题）。

## 四、迁移与模型（B-0）

- `internal/storage/embeddedadmin/0003_merge_state.sql`：§三 的两张表 + 索引。
- `internal/tenant/teams.go` 增加：

```go
// MergeState is one row of merge_state: where one member's contribution to one
// project stands.
type MergeState struct {
    TeamID, ProjectID, TenantID string
    CursorUpdatedAt             time.Time
    CursorUnitID                string
    LastMergedAt                *time.Time
    LastError                   string
    FailCount                   int
    MergingSince                *time.Time
    RebuildRequested            bool
}

// MergeSourceUnit is one row of merge_source_unit: one member fact and the team
// fact it supports (or DroppedMergeKey when the model folded it away).
type MergeSourceUnit struct {
    TeamID, ProjectID, TenantID string
    SourceUnitID                string
    MergeKey                    string
    ConsumedAt                  time.Time
}

// DroppedMergeKey marks a source the model judged redundant. Excluded from
// delete detection: its disappearance cannot invalidate the team content.
const DroppedMergeKey = "__dropped__"

// MergeKey is the identity of a team fact: the hex SHA-256 of its normalized
// text. Two facts with the same key are the same fact and keep their row.
func MergeKey(text string) string

// MergeLockName is the advisory-lock name of one project's merge round.
func MergeLockName(teamID, projectID string) (string, string)
```

`MergeKey` 放 `tenant` 而不是 `projectmerge` 的理由：账本要写它、读路径不需要，
而 `projectmerge` 是纯函数包、会被 storage 之外的大量代码引。**`tenant` 是两者
都能引的最底层**。`projectmerge` 复用 `tenant.MergeKey`，不自己定义一份。

验证：`go test ./internal/storage -run TestAdminMigrationSeries`。
**两个现有测试要同时改**：

- `TestAdminMigrationSeries` 里 `len(series) != 2` → `!= 3`（实施计划漏了这条，
  PR-1 第一步就会红）；
- `TestTeamRegistryMigrationKeepsTheMergeLedgerOut` 反过来断言：0003 的
  `AppliedAt`/`Version` 存在，且 `0002` 的 migration 对象本身仍不含
  `merge_state`。用 `series[1]` / `series[2]` 分别比对，而不是把整个系列拼起来
  再搜——拼起来搜的断言在加了 0003 之后必然失败。

## 五、`MergeStore`：持有者、事务边界、接口（B-1，R1/R7）

### 5.1 它不是一个 `PostgresStore`

一次合并要碰三个 schema：成员的（读事实）、团队的（读写事实）、admin 的（写账本）。
而 `PostgresStore.schemaFor(ctx)` **一个 context 只解析一个 schema**，admin 表由
`PostgresTenantRegistry` 用自己的句柄读写，两者是不同的对象。

**结论**：`MergeStore` 是一个**独立对象**，构造时同时持有两个句柄：

```go
// internal/storage/merge_store.go

// MergeStore is the cross-schema store the project merge job needs.
//
// It is a separate object because one merge round touches three schemas while a
// Store is bound to one: member schemas are read, the team schema is written,
// and the ledger lives in the admin schema. Which *tenant* schema a statement
// uses therefore always comes from the context, exactly as it does for Store —
// only the admin schema is fixed, because there is only one.
//
// There is no in-memory implementation on purpose: the risks this store carries
// (cross-schema transactions, advisory locks, FK cascades) have no in-memory
// analogue, so an in-memory double would only test a semantics nobody runs.
type MergeStore interface {
    // ListMergeProjects returns every active team project that has members,
    // with each member's job state, in (team, project, tenant) order.
    ListMergeProjects(ctx context.Context) ([]MergeProject, error)

    // BeginMergeStanza runs fn holding the project's advisory lock. The lock is
    // transaction-scoped: it ends with fn, so fn must do the cheap reads and the
    // merge_state.merging_since stamp — never the model call.
    BeginMergeStanza(ctx context.Context, key MergeKeyPair, fn func(stanza MergeStanza) error) error

    // FinishMergeStanza writes one round's output. Team facts (in ctx's schema),
    // the ledger (in ctx's admin handle) and merge_state are one transaction.
    FinishMergeStanza(ctx context.Context, req FinishMergeRequest) error

    // AbandonMergeStanza clears merging_since after a round that produced no
    // output, recording why.
    AbandonMergeStanza(ctx context.Context, key MergeKeyPair, state MergeStateFailure) error

    // ClearMemberProject removes one member's ledger rows and state for one
    // project. It is what makes an un-share operation converge (§R6).
    ClearMemberProject(ctx context.Context, teamID, projectID, tenantID string) error

    // RequestRebuild asks the next round to ignore cursors and rebuild.
    RequestRebuild(ctx context.Context, teamID, projectID string) error

    // MergeStatus reports the admin-side state a console shows.
    MergeStatus(ctx context.Context, teamID, projectID string) (MergeStatus, error)
}
```

`MergeStanza` 只暴露**已锁定的读**：`Previous() []TeamFact`、`MemberJobs() []MemberJob`、
`State(tenantID) MergeState`。它不暴露写——写只有 `FinishMergeStanza` 一个入口，
"模型调用在锁外"就有了类型层面的保证。

### 5.2 两段事务（R7）

实施计划写"整轮一个事务，模型调用也在事务内"，并说设计 §六"明确允许"。**设计 §六
（`2026-10-29-team-project-shared-memory-design.md:247`）原文是"模型调用在事务之前，
失败不改任何行"**——方向相反。而本仓库所有
advisory 锁都是 `pg_advisory_xact_lock` + 回调（`admin_migrate.go:51`、
`postgres.go:429`、`postgres_mental_models.go:539`），**没有任何 session 级锁先例**。

session 级锁在这里也不可用：`database/sql` 是连接池，session 锁绑连接，
"You must unlock the same connection" 在 Go 里意味着手工把连接钉住跨模型调用，
既复杂又容易泄漏。

**v3 的做法**——两段事务 + 事务锁 + 一行状态做跨段占位：

```
第 0 段（事务 A，advisory lock）：
  pg_try_advisory_xact_lock(hashtext('wenyuan:project-merge'), hashtext(key))
    拿不到 → 记 skip，返回
  SELECT … FROM merge_state WHERE … FOR UPDATE
  merging_since IS NOT NULL 且 merging_since > now() - deadline
      → 另一轮正在模型调用中，记 skip，返回
      → 否则视为上一轮崩溃，接管（记 warn，覆盖 merging_since）
  UPDATE merge_state SET merging_since = now() WHERE …
  读：team 上一版事实、各成员 job 状态、存活支撑反连接（§七）
  若判定"无需合并" → 清 merging_since 并提交，结束（不调模型）

（锁外）模型调用

第 1 段（事务 B，无锁）：
  UPDATE merge_state … WHERE merging_since = <第 0 段盖的时间戳>   ← 见下
  事实 diff + 账本替换 + 清 merging_since + 推进游标 + 清 last_error
  整个在事务 B 里
```

**事务 B 为什么不需要锁**：`merging_since` 是它认领的那一轮的凭据。`FinishMergeStanza`
的条件更新写成

```sql
UPDATE merge_state SET merge_fence = $stamp
WHERE team_tenant_id=$1 AND project_id=$2 AND tenant_id=$3
  AND merging_since = $stamp
```

`merge_fence` 不是新列——用 `merging_since IS NOT DISTINCT FROM $stamp` 作为 WHERE
条件，`RowsAffected()==0` 表示"这一轮已被接管或已结束"，**整笔回滚并记 skip**。
这样两个进程的模型调用可能重叠（代价：浪费一次模型调用），但**写入只有一轮会成功**，
且不需要跨段的持锁。

**代价与取舍**（必须写进文档，不能假装没有）：最坏情况下同一项目会被两次模型调用
各算一遍，第二次的写入被 fence 拒绝。这比"挂住 PG 事务几分钟"便宜得多。

### 5.3 失败与熔断

- 模型失败 / 解析失败 / 存储失败：`AbandonMergeStanza` 清 `merging_since`、
  `fail_count+1`、写 `last_error`，**游标不推进**；
- `fail_count >= ProjectMergeFailThreshold`（默认 5）：跳过该项目直到
  `purge-contribution` 或管理面手工重置（`POST .../merge-reset`，运维用）；
- 熔断期间 `merge-status` 显示 `halted: true` 与 `last_error`，控制台红色。

### 5.4 `FinishMergeStanza` 的事务语义

这是 B 部分最需要被钉住的东西，按序：

1. `SELECT … FROM merge_state … FOR UPDATE`，校验 fence；
2. 读团队 bank 现有 `world` 事实，按 `metadata.merge_key` 建索引；
3. 结果 → 规范化 → `merge_key`：同 key 的**保留原行，id 不变**；
4. 团队有、结果没有的删掉，并清掉因此失去**全部**来源的观察——照
   `deleteStaleObservationsReportingTx`（`postgres_consolidation.go:462`）的做法：
   反连接 `source_memory_ids && doomed`、按 id 序 `FOR NO KEY UPDATE` 加锁、
   再删。**不要**照 `DeleteBank`（`postgres_banks.go:396`）——那是删整个 bank 的
   顺序，这里只删部分事实，照抄会把整个团队 bank 清空；
5. 结果有、团队没有的插入：`fact_type='world'`、`document_id=NULL`、
   `metadata={"merge_key":…, "merged_at":…}`、**不写 tag**（决策 10）；
6. 账本：本轮消费的每条来源写一行 `merge_key`（进了输出的）或
   `'__dropped__'`（被折叠的）。重建时**整体替换**：先删该 `(project, tenant)`
   的旧行，再写新行；
7. `merge_state`：游标推进到实际读到的最后一条、`last_merged_at=now()`、
   `fail_count=0`、`last_error=NULL`、`merging_since=NULL`；
8. 提交。

第 4 步之后、第 6 步之前若抛出，整个事务回滚，团队事实**保持上一版**——这是刻意的：
下一轮重试拿到的输入与这一轮相同，结果收敛。

### 5.5 admin 与租户 schema 在同一个事务里

要求 `FinishMergeStanza` 自己 `BeginTx`，用**一个 driver 连接**同时限
`admin.merge_source_unit` 与 `<team>.memory_units`。PostgreSQL 支持跨 schema 的
单事务写入；前提是**同一个 `*sql.Tx`**。所以 `MergeStore` 必须直接持有一个
`storage.DB`（而非复用 `PostgresStore` 的方法——那些方法各自 `BeginTx`）。

**门面**：`internal/app` 构造时传 admin 句柄与租户句柄，二者来自同一
`*sql.DB`（同一 `databaseURL`），所以跨 schema 事务成立。若将来 admin 与租户
分库，这个设计会失效——**在 `MergeStore` 构造处用注释钉住这个前提**，并在
PG 集成测试里断言 `current_database()` 一致。

## 六、合并引擎（B-2）

`internal/projectmerge/`，纯函数，不依赖数据库与 HTTP。

```go
// Normalize folds away the differences that must not create two team facts.
func Normalize(text string) string

// Answer is one team fact the model produced, with the inputs it came from.
type Answer struct {
    Text    string
    Sources []int // indexes into the round's Fresh slice
}

// MergeKey is tenant.MergeKey; re-exported so callers need one import.
func MergeKey(text string) string

// Diff classifies what a round's answer changes.
func Diff(previous []TeamFact, merged []Answer) (keep, remove, insert []TeamFact)

// Prompt builds the round's messages.
func Prompt(mission string, previous []TeamFact, fresh []MemberFact) []provider.Message

// ParseAnswer decodes the model's reply.
//
// Two failures are distinguished: ErrAnswerShape for text that is not the
// expected JSON at all, and ErrAnswerEmpty for a well-formed answer carrying no
// facts. The distinction matters because an empty answer deletes the team
// content, so it is refused unless the round itself had no previous content.
func ParseAnswer(raw string, freshCount int) ([]Answer, error)
```

### 6.1 输出契约（R3，本版最重要的改动之一）

模型必须返回**带来源下标**的结构：

```json
{"facts": [{"text": "The deploy uses port 15432.", "sources": [0, 3]}]}
```

- `sources` 是本轮 `fresh` 输入的**下标**，不传 uuid——模型不该被要求复述 id，
  hallucinate 的 uuid 会静默落进账本；
- 下标越界、类型不对 → `ErrAnswerShape`，**整轮不写任何行、游标不推进**；
- `sources` 为空：允许，但该事实在账本里**没有来源**。它只能是 `previous` 的保留行
  改写的（另一条事实的措辞变化），否则违反 §三 不变量 2 → 记为 `last_error`
  但不回滚（见 §三 不变量 2 的处置）。
- 同一 `merge_key` 在答案里出现两次 → 合并 `sources`，取第一个文本。

**为什么这个改动能救 R2**：`source → merge_key` 的映射由**服务端**从下标确定，
不信任模型；被折叠掉的来源（`fresh` 里没被任何 `sources` 引用的下标）写
`'__dropped__'`。账本的支撑关系由此精确，删除检测才能只查"活的"来源。

### 6.2 `previous` 的下标不参与 `sources`

`Prompt` 里 `previous` 与 `fresh` 分别编号（`previous` 用 `P0…`，`fresh` 用
`F0…` 标注在 prompt 里），`sources` **只允许引用 `F` 编号**。理由：`previous` 的
来源已上一轮写进账本，本轮不该重写；让模型引用 `previous` 会把"这条事实由谁支撑"
变成"模型这一轮怎么想"，账本就会每轮抖动。

### 6.3 prompt 结构与批次上限

- `previous` 全部进入输入（决策 14），但受 `ProjectMergeMaxTeamFacts`（R8）约束：
  超过上限**本轮不合并**，`last_error="team facts N exceed cap M"`，
  `merging_since` 清掉，**不失败关闭**（容量问题不是隐私问题）；
- `fresh` 是本轮新输入，合计 ≤ `ProjectMergeBatchSize`；
- prompt 里 `previous` 按上次合并顺序排，`fresh` 按成员分组，每组标 `[F i]` 序号。

验证：`go test ./internal/projectmerge -count=1`，表驱动——规范化等价性（空白折叠、
unicode 归一）、`Diff` 三类、prompt 里上一版每条都在、`ParseAnswer` 的形状/空/
越界/重复 key 四种、`tenant.MergeKey` 与 `projectmerge.MergeKey` 一致。

## 七、`project.merge` 维护任务（B-3）

`internal/app/project_merge.go`。一次 tick：

1. `ListMergeProjects`：`tenant.kind='team'` 且 `status='active'`，且
   `team_project_member` 有行；按 `last_merged_at NULLS FIRST` 排（设计 §六：
   否则某个成员每轮都写，其他人永远轮不到）；
2. 对每个项目 `BeginMergeStanza`：抢事务锁 → 检查/接管 `merging_since` →
   读上一版事实、各成员状态、做**存活支撑反连接**（§R4）→ 判断本轮是否需要合并；
3. 需要合并则 `FinishMergeStanza`；不需要则清 `merging_since` 提交，结束；
4. 团队事实真的变了 → 向 `T::X` 入队一次 `consolidate`
   （`EnqueueOperation` + `ConsolidationSerializationKey`）；
5. 单轮时间上限 `min(10×interval, 5min)`，超时回滚 + `last_error`。

### 7.1 删除检测（R4 的落地）

替换实施计划 §B4 的四步判定。**每轮每个 `(project, member)` 一次反连接，不做粗筛**：

```sql
SELECT 1
FROM admin.merge_source_unit l
LEFT JOIN <member_schema>.memory_units m ON m.id = l.source_unit_id
WHERE l.team_tenant_id = $1 AND l.project_id = $2 AND l.tenant_id = $3
  AND l.merge_key <> '__dropped__'
  AND m.id IS NULL
LIMIT 1
```

成本 = 该成员的**活来源行数**（走 `merge_source_unit_live_idx`），不是账本全量，
更不是事实全表。设计 §十三 担心的"每个 project 每轮扫一遍账本"在这里被收窄到
"只扫正在支撑当前内容的行"。

为什么必须每轮做：`updated_at` 在删除时不推进，"删一条加一条"计数不变——任何
基于 `(updated_at, count)` 的粗筛都会把它判成"没变"，而漏判的后果是**已删数据
继续可见**。省下的那点查询远不值这个风险。

**判定表**：

| 条件 | 动作 |
|---|---|
| 个人 `banks` 行不存在 | 跳过该成员（onboarding）；团队内容保留 |
| 反连接命中 | 该项目**重建**：忽略游标读全部成员 `world` 事实 |
| 无命中，且有新输入 | 增量合并 |
| 无命中，且无新输入，`rebuild_requested=false` | 只更新 `last_merged_at`，**不调模型** |
| `rebuild_requested=true` | 重建，成功后清标志 |

### 7.2 重建与失败关闭

1. 忽略游标，读该项目所有成员当前全部 `world` 事实（按成员分页）；
2. 总数 > `ProjectMergeRebuildMaxUnits`（默认 5000）→ **失败关闭**：
   删 `T::X` 的全部 `world` 事实与失去来源的观察，清该项目的 `merge_state` 与账本，
   写 `last_error`；读路径自然回退到成员自己的 bank；
3. 否则用与增量相同的规则重算整版，`FinishMergeStanza` 整体替换账本；
4. 成功后游标推进到本轮实际读到的最后一条。

**失败关闭只用于这一种情况**。R8 的超限、模型的形状错误、存储失败都不关闭——
它们下一轮重试就好，没必要为了让一次可恢复的失败清空用户的团队内容。

### 7.3 配置（`internal/config/maintenance.go`）

```
WENYUAN_API_PROJECT_MERGE_INTERVAL_SECONDS       默认 60，<=0 关闭
WENYUAN_API_PROJECT_MERGE_BATCH_SIZE             默认 10
WENYUAN_API_PROJECT_MERGE_REBUILD_MAX_UNITS      默认 5000
WENYUAN_API_PROJECT_MERGE_MAX_TEAM_FACTS         默认 500      ← v3 新增（R8）
WENYUAN_API_PROJECT_MERGE_FAIL_THRESHOLD         默认 5        ← v3 新增（熔断）
```

- `<= 0` 按"关闭"处理，与 `ConsolidationReconcileInterval` 等现有键一致
  （`resolveMaintenance` 里用 `> 0` 才开）；
- `AnyJobEnabled` 计入 `project.merge`，但**计入条件是
  `interval > 0 && mergeStore != nil`**（GAP-3）。只写 `interval > 0` 是危险的：
  `interval` 有默认值 60，于是单 Tenant 部署会因为"有个 job 开着"启动维护循环并
  每轮空转——它没有 `MergeStore`，什么也做不了。`mergeStore` 在 single 模式恒 nil，
  把它写进条件里，默认关闭就由类型保证，而不是靠部署记得把间隔设成 0；
- **`docs/CONFIGURATION.md` 是生成的**（`scripts/render_config_reference.py`），
  加完键必须重跑，实施计划漏了这条；
- 模型选择：**没有 chat provider 时不注册该任务**（`provider none` 下每轮只会写
  `last_error`），与 `RefuseWithoutReflectModel` 同一思路；
- `NewMaintenanceLoop` / `NewBackgroundRoles` / `cmd/worker` 都接一个
  `storage.MergeStore`，nil 不注册；**single 模式永远 nil**。

### 7.4 团队 bank 被直写（R12）

团队 key 有 `POST /banks/{id}/retain` 权限。直接写进 `T::X` 的事实，下一轮
`FinishMergeStanza` 第 4 步会按"结果里没有"删掉——**并且连带清掉它的观察**。
行为是对的（团队 bank 由合并独占写），但**静默丢数据**是最难排查的一类。

对策（轻量，不加逻辑分支）：`merge-status` 每轮统计并返回
`unmanaged_facts`——团队 bank 里 `fact_type='world'` 且
`metadata->>'merge_key' IS NULL` 的行数。控制台非零时黄字提示
"该项目的团队库有 N 条非合并写入，将在下一轮被替换"。运维看一眼就知道。

验证：
- 单测 `go test ./internal/app -run TestProjectMerge`（假模型 + 假 `MergeStore`）：
  锁跳过、fence 拒绝、批次截断、熔断计数、超限不失败关闭；
- PG 集成 `internal/app/project_merge_pgx_test.go`，覆盖设计 §十一 的九条场景
  加三条新场景（v3 特有）：**被折叠来源被删不触发重建**（R2）、**fence 让第二轮
  写入失败**（R7）、**退出项目后不再每轮重建**（R6）。

## 八、读回退（B-4，R9）

### 8.1 位置：storage 层内部

实施计划把回退放在 handler 层（`memoryops.Handlers.TeamScope`、
`reporting.Handlers.TeamScope`），要改 9+ 处 `requireBank` 调用点，且 recall 里
`tagVocabulary(ctx, q.BankID)`、`graph` 种子、`chunks`、`source_facts` 全按
`q.BankID` 读，每一处都要单独补。

设计 §四 决策 16 原文是"回退实现在 storage 层内部"，**v3 回到这个位置**：

```go
// internal/storage/recall_types.go
type RecallQuery struct {
    // …现有字段…
    // Fallback, when set, is read instead of BankID once BankID is found to hold
    // no units at all. The pipeline runs once, against exactly one bank — never
    // as a merge of the two — so a member sees one stable view per request.
    Fallback *FallbackScope
}

// FallbackScope names another tenant's bank to read when this one is empty.
type FallbackScope struct {
    Schema string // the team's tenant schema
    BankID string // the team's bank "<team>::<name>"
}
```

`PostgresStore.Recall` 在 `GetBank`/首查询发现主 bank 无 unit 后，用
`WithSchema(ctx, fb.Schema)` 建子 context，**整个 pipeline 重跑一次**；最后把
`RecallMatch.BankID` / `MemoryUnit.BankID` 改回请求里的值（决策 16 要求客户端
看到的仍是 `A::X`）。`MemoryQuery` 同理加 `Fallback`，`ListMemories` 的
`bank_id` 改写发生在 `ListMemories` 返回前。

**handler 的唯一工作**是解析出 `FallbackScope` 并填进 query：

```go
// internal/teamread/
func ParseBankID(bankID string) (tenantID, projectName string, ok bool) // 按第一个 "::" 拆
type Resolver struct{ … }
// Resolve returns the team bank to fall back to, when the caller is a member of
// a project whose personal bank is named after it.
func (r *Resolver) Resolve(ctx context.Context, tenantID, bankID string) (*storage.FallbackScope, error)
```

三步解析（承设计 §七）：前缀必须等于当前 schema → 查注册表
（`team_project_member JOIN team_project JOIN tenant`，`status='active'`）→
返回 `FallbackScope`。**bank id 只用于寻址，不用于反推团队**（决策 4）。

### 8.2 resolver 缓存（R10）

设计 §十三 承认"每个 bank-scoped 读多一次 admin 查询，规模变大后加缓存"。
v3 直接做，因为**回退会让每次 recall 多一次 admin 查询**，而 `requireBank` 与
recall 内部 `GetBank` 已经是两次。

- 进程内 `map[key]entry` + `RWMutex`，`key = tenantID + "\x00" + bankID`；
- **TTL 30s**，与 `ProjectMergeInterval` 同量级；成员变更靠 TTL 失效，**不做主动
  失效**（多进程下主动失效本来就不完整，TTL 是唯一诚实的做法）；
- 负命中（非成员）**同样缓存**，否则最贵的场景（每个个人 bank 的每次读）全部穿透；
- 上限 10000 条，超了整体清空（不做 LRU，YAGNI）。

### 8.3 回退后的过滤语义（R5，必须写清楚否则会被当 bug）

团队 bank 只有合并写入的 `world` 事实和它自己的观察，**没有 documents/chunks/
attachments，没有 invalidated 行**。因此：

| 请求 | 回退到团队后的语义 |
|---|---|
| `POST /recall` | 正常。`include.chunks` / `include.source_facts` 在团队 bank 上为空（团队事实 `chunk_id`/`source_memory_ids` 都空），**不报错** |
| `GET /memories` | 正常，返回团队事实 |
| `GET /memories?state=invalidated` | **不启用回退**。团队侧没有 invalidated 行，回退必然空且掩盖了"个人 bank 有失效事实" |
| `GET /memories?document_id=…` | **不启用回退**。团队事实 `document_id` 恒空，同理 |
| `GET /memories?entity_id=…` | 启用。`entity_id` 是请求方给的 uuid，团队 bank 里也有实体；命中与否是数据问题 |
| `GET /memories?q=…`、`tags`、时间窗 | 启用，沿用现有 pipeline |
| `/tags`、`/graph`、`/entities`、`/stats`、`/documents`、`/chunks` | **不回退**（实施计划 §B3 的判定正确，保留） |
| 按 id 读/改/删 unit | 不回退（D2）。团队行对成员只读 |
| reflect | 不回退（本轮）。它基于 recall，团队内容经回退自动可见；但 mission 取自哪个 bank 是产品决定，留后续 |

**规则一句话**：回退只在"个人 bank 的过滤条件在团队 bank 上是有意义的"时才启用；
`state=invalidated` 与 `document_id` 这两个条件是团队 bank 的结构上无法满足的，
启用回退只会把"没有"伪装成"查过了"。

### 8.4 回退的判定是"按 bank"不是"按 query"

`T::X` 有任何 unit（`fact_type` 不限）就是主源，本次读只查它；团队有内容但本次
query 没命中就返回空，**不再去个人 bank 补**（决策 15）。否则同一成员问不同问题
会看到不同来源。

判定用 `BankHasUnits`。**它不进 `storage.Store` 接口，而是能力检测**（GAP-1）：
`Store` 是个大接口，往里加方法会强迫**每一个实现**补上——两个真实后端，以及
`internal/app/parity_integration_test.go` 等处所有测试替身——为一条只在 multi 模式
下才会被调用的路径付编译代价。仓库里 `ConsolidationReconciler`、`RetentionPurger`
都是能力检测，v3 沿用：

```go
// BankPresence is the optional capability recall needs to decide the team
// fallback: whether a bank holds any unit at all.
//
// "Any" is what makes the fallback decision per-bank rather than per-query: once
// a team bank holds content it is the source, even for a query that matches none
// of it.
//
// It is a capability rather than a Store method so a backend that has no team
// concept — and every test double — keeps compiling untouched. A store that does
// not implement it never falls back, which is exactly the single-tenant default.
type BankPresence interface {
    BankHasUnits(ctx context.Context, bankID string) (bool, error)
}
```

两个**真实**后端仍然都实现它（内存版语义与 PG 一致，只是个计数，不像 `MergeStore`
那样有跨 schema 事务可测）；但"不实现"是一个合法的、默认为关闭的状态，而不是
编译错误。

### 8.5 接线

- `internal/app/app.go`：multi 模式构造 `teamread.Resolver` 注入
  `memoryops.Handlers` 与 `reporting.Handlers`；single 模式为 nil（今天的行为）；
- 两个 handler 各自在自己的入口解析一次，填 `Fallback`，**其余代码不改**；
- recall 的 `BankConfig(ctx, bankID)` **仍读个人 bank**：成员的召回偏好属于成员，
  不是团队的。这条要写进代码注释，否则后来者会"顺手统一"。

验证：单测 `go test ./internal/teamread -run TestResolve`（有行/无行/非 active/
团队为空/团队有内容/个人 bank 不存在/TTL 过期/负命中缓存）；端到端
`internal/app/team_read_test.go`：成员 recall 命中团队事实且 `bank_id` 仍是
`A::X`、团队为空回退个人、B 的凭证读 `T::X` 不回退、`state=invalidated` 不回退。

## 九、生命周期与撤回（B-5，R6/R11）

### 9.1 外键级联只解决一半

`merge_state` / `merge_source_unit` 的外键挂在 `team_member` 与 `team_project` 上，
**不挂 `team_project_member`**。因此：

| 事件 | 级联是否清除该成员账本 | 动作 |
|---|---|---|
| 成员退出团队（`DELETE .../teams/{id}/members/{tid}`） | ✅ 是 | 无需动作；已合并内容保留（设计 §八） |
| 成员退出项目（`DELETE .../projects/{pid}/members/{tid}`） | ❌ 否 | **必须显式 `ClearMemberProject`** |
| purge-contribution | ❌ 否 | `ClearMemberProject` + `RequestRebuild`，同一操作 |

**漏掉的后果**：账本留着该成员的行 → 下一轮反连接仍查它 → 该成员已不在输入集合
→ 重建产出不含它 → 账本行仍在 → **每轮都重建，永不收敛**（每轮一次模型调用，
且 `fail_count` 不涨，熔断也拦不住）。这是 v3 相对实施计划必须补的一句。

### 9.2 两个端点的语义划清（R11）

实施计划让 `purge-contribution` 在合并未启用时返回 `rebuild:false`——那意味着
**成员已经被移出、团队内容还在**，不叫撤回。而且它要删掉 A 部分已发布的
`rebuild_deferred` 字段，控制台 `team-admin-routes.test.ts` 与 10 语种文案都得改。

v3 的做法：

| 端点 | 语义 | 响应 |
|---|---|---|
| `DELETE .../projects/{pid}/members/{tid}` | **只移出** + 清账本（`ClearMemberProject`）。已合并内容保留，此后该成员的删除不再影响团队 | `{"status":"removed"}`（**保持 A 部分的形状**） |
| `POST .../projects/{pid}/purge-contribution` | 移出 + 清账本 + `RequestRebuild`，**三者原子**；合并未启用时 **409**，不是半吊子成功 | `{"status":"purged","rebuild":true}` / 未启用时 `409` |
| `GET .../projects/{pid}/merge-status` | 新增，管理面只读 | 见 §九.3 |

`purge-contribution` 的原子性：`ClearMemberProject` 与该行的 `merge_state` 删除在
一个事务里；若 `project.merge` 未注册（`MergeStore == nil`）**先拒绝、后动手**，
不留下"移出了但没撤回"的中间态。响应里保留 `rebuild_deferred` 作为**可选**字段
（不再总是出现），这样 A 部分写的客户端不用改。

### 9.3 `merge-status`（B-6 的数据源）

```
GET /admin/v1/teams/{id}/projects/{project_id}/merge-status
```

```json
{
  "project_id": "web",
  "bank_id": "acme::Web",
  "merged": true,
  "last_merged_at": "2026-10-30T12:00:00Z",
  "last_error": "",
  "halted": false,
  "fail_count": 0,
  "pending_members": 2,
  "team_facts": 128,
  "unmanaged_facts": 3
}
```

`merged=false` 表示从未合并；`halted=true` 是熔断；`unmanaged_facts` 是 §7.4 的
直写计数。管理面只读，**不进 `/openapi.json`**（与团队面其余路由一致）。

### 9.4 控制台（B-6）

- 项目行展示 `last_merged_at` / `last_error` / `team_facts` / `unmanaged_facts`；
- `purge-contribution` 按钮的文案从"移出"改为"撤回贡献并重建"，未启用合并时
  **按钮禁用**并提示原因（不是点了才报 409）；
- 消息进 10 语种，沿用 `teams.detail.*` 命名空间（导航那条教训：共享组件读哪个
  命名空间就把键放哪个）。

验证：`npm run typecheck && npm run lint && npm run test && npm run build`；
新增 `frontend/test/team-merge-status.test.ts`；`team-admin-routes.test.ts`
补 `purge-contribution` 的 409 与成功两条。

## 十、工作项与顺序

```
B-0 迁移与模型（§四，含两个已有测试的改动）
 │
 ├─▶ B-1 MergeStore（§五）────┬─▶ B-3 维护任务（§七）─▶ B-5 生命周期（§九）─┐
 │                            │                                            ├─▶ B-7 文档
B-2 纯函数引擎（§六）─────────┘                                            │
 │                                                                         │
 └─▶ B-4 读回退（§八）──────────────────────────────────────────────────────┘
B-6 控制台在 B-3、B-5 之后（需要 merge-status）
B-8 兼容性测试（§13.4 三条）随各自 PR 落地，不是独立阶段
```

- B-2 与 B-0/B-1 可并行（不同包）；
- B-3 依赖 B-1 的 `MergeStore` 与 B-2 的 `Answer`/`Diff`；
- B-5 依赖 B-3（`RequestRebuild` 的真实语义）；
- **两个 PR**：PR-1 = B-0…B-3（合并），PR-2 = B-4…B-6（回退 + 撤回 + 控制台）。
  回退要等合并已经在跑，否则回退永远命中空团队——这条实施计划判断正确，保留。

### B-8 兼容性测试（§13.4）

不单列阶段，按 PR 归属：

| 条款 | 落地位置 | 归属 |
|---|---|---|
| 1：单 Tenant 不注册 `project.merge` | `internal/app/maintenance_single_tenant_test.go` | **PR-1**（B-3 的验收项） |
| 3：无 `BankPresence` 的 store 不回退 | `internal/storage/bank_presence_test.go` | PR-1（B-1 的能力检测先落地） |
| 2：`Fallback` 恒为 nil | `internal/memoryops/recall_fallback_test.go`、`internal/reporting/list_fallback_test.go` | **PR-2**（唯一触碰数据面读路径的 PR） |

条款 2 是 PR-2 的 **exit criteria**：PR-2 的验收标准就是"单 Tenant 的
recall / list 与升级前逐字节相同"，条款 2 是它的可执行形式。

### B-7 文档

- `docs/plans/2026-10-29-team-project-shared-memory-design.md` 的 §六/§七/§八
  加一行"已被 `2026-10-30-team-memory-design-v3.md` 取代"（已做）；
- `docs/plans/2026-10-31-team-merge-implementation-plan.md` 同样标注被取代（已做）；
- `AGENTS.md` 团队段落补：合并任务、读回退、可见性窗口（默认 60s + 一次模型调用）、
  **"团队 bank 由合并独占写，直写会被下一轮替换"**、**"单 Tenant 下 `MergeStore`
  与 resolver 恒为 nil，`Store` 接口不因团队能力而加方法"**；
- `docs/CONFIGURATION.md` 重新生成（5 个新键）；
- 若 §13.3.1 选 A：`docs/SCHEMA-ALIGNMENT.md` 补一句 admin schema 的迁移分层；
  选 B 则补一句 admin schema 不参与 extra 检查；
- 本文件文末追加实施记要。

## 十一、风险与对策

| 风险 | 对策 |
|---|---|
| 跨 schema 写入的租户安全 | `FinishMergeStanza` 的唯一写路径；PG 集成测试用 B 的凭证读 `T::X` 必须 404，且断言 `current_database()` 一致（跨 schema 事务的前提） |
| 删除检测漏判 | 每轮存活支撑反连接，**不做版本粗筛**（R4）；漏判的后果是已删数据继续可见，是隐私底线，不接受任何"省一次查询"的折中 |
| 被折叠的来源被删触发全量重建 | 账本记 `'__dropped__'` 并排除出反连接（R2）；PG 测试专测这条 |
| 模型的空结果清空团队内容 | `ParseAnswer` 区分 `ErrAnswerEmpty`；只有本轮 `previous` 也为空时才接受空结果（§6.1） |
| 两个进程的模型调用重叠 | `merging_since` 做跨段占位，`FinishMergeStanza` 的 fence 拒绝后者；代价是浪费一次模型调用，换来不挂住 PG 事务（R7） |
| 熔断后项目永久停摆 | `purge-contribution` / 管理面 `merge-reset` 重置 `fail_count`；`merge-status` 的 `halted` 让运维看得见 |
| 团队事实无界增长 | `ProjectMergeMaxTeamFacts`；超限不合并、不失败关闭，`last_error` 指路（R8） |
| 读回退误命中 | 前缀必须等于当前 schema、注册表必须 active、resolver TTL 30s；B 的凭证读 `T::X` 有端到端测试 |
| 观察 id 每轮被重建 | 按 `merge_key` 保留原行，id 不变；观察只在下一次 consolidation 中更新 |
| 团队 bank 被直写后静默消失 | `merge-status.unmanaged_facts` 计数 + 控制台告警（R12） |
| 可见性窗口（默认 60s + 一行模型调用） | 设计承认的折中；写触发入队留作后续（需要 worker 新 operation 类型） |
| 每轮多一次 admin 解析查询 | resolver 带 TTL 缓存，负命中同样缓存（R10） |
| 单 Tenant 被团队能力波及 | 团队门控全部在 `TenantMode == multi` 上；`MergeStore`/resolver 恒 nil；`Store` 接口不加方法（能力检测）；§13.4 三条测试钉住 |

## 十二、明确不做

- 跨 Team 共享；共享原始对话/documents/chunks/attachments。
- 秒级可见（写触发的合并入队）。
- 按成员逐条撤回的 UI（只有显式 purge-contribution）。
- 项目/bank 改名；团队 bank 的配额、限流、成本核算。
- reflect 在团队视图下的 mission 语义（§8.3 已列，留作后续）。
- **`MergeStore` 的内存后端**（R13）：跨 schema 事务、advisory lock、FK 级联都没有
  内存对应物，假后端只会让测试通过一套没人运行的语义。引擎行为全部走 PG 集成。
- 按来源分片的轮转删除检测（设计 §十三 的后续项）：v3 的反连接已经只扫活来源，
  没有分片的必要。

## 十三、单 Tenant 与向后兼容

这一节回答一个必须回答、但 v3 前十二节只顺带提到的问题：**一个 `tenant_mode: single`
的部署，从来没有 Team，升级到带 B 部分的版本后，现有功能是否逐字节不变？**

结论：**是**——因为团队门控完全没动，`MergeStore` 是独立对象，回退是可选字段。
但"是"必须能被测试证明，所以本节既给清单，也给三条可验证条款。

### 13.1 单 Tenant 下的门控（现有代码，v3 不改）

| 机制 | 现有代码 | v3 后 |
|---|---|---|
| 默认模式 | `config.go:1042` `TenantMode: TenantModeSingle` | 不变 |
| 团队注册表 | `app.go:281` 只在 `TenantModeMulti` 时构造，否则 `nil` | 不变 |
| 管理面团队路由 | `adminapi` 每路由 `teamsAbsent` → 501 | 不变 |
| `MergeStore` | —— | `nil`（与 `teamRegistry` 同一处门控） |
| `teamread.Resolver` | —— | `nil`（同上） |
| `project.merge` | —— | 不注册（§7.3） |
| 维护循环 | `AnyJobEnabled` | 计入条件含 `mergeStore != nil`（GAP-3），故不启动 |

**关键点**：v3 没有修改 `TenantMode` 的语义，也没有给 `storage.Store` 加任何方法
（GAP-1 已改成能力检测）。单 Tenant 路径上的代码一行未动。

### 13.2 逐调用方清单：内部 recall / list 一律不受影响

`Fallback` 是**可选指针字段**，`nil` 即今天的行为。所有内部调用方都不设置它：

| 调用方 | 位置 | 单 Tenant | 多 Tenant 无回退 |
|---|---|---|---|
| HTTP recall | `memoryops/recall.go:209` | 不设 ✅ | resolver 为 nil 或不命中时不设 |
| reflect 内部 recall | `storage/reflect_retrieval.go:330` | 不设 ✅ | **永不设**（reflect 不参与回退，§8.3） |
| MCP recall | `mcp/tools.go:225` | 不设 ✅ | 同 HTTP 规则 |
| worker 内部 recall | `app/worker.go:1419` | 不设 ✅ | **永不设**（worker 无请求上下文） |
| 导出 export_build | `transfer/export_build.go:733,802` | 不设 ✅ | **永不设**（导出读原始账本） |
| HTTP list | `reporting/memory_routes.go:113` | 不设 ✅ | 按 §8.3 的门控 |

这张表是"不破坏"的**证据**，不是声明：六个调用方中四个**永不**设置 `Fallback`，
两个只在 multi 模式且解析命中时设置。任何一处漏改都表现为"该调用方读到了团队
bank"——13.4 的条款 2 就是为了抓住它。

### 13.3 行为差异清单（全部在 multi 模式下才可达）

| 差异 | 可达性 | 单 Tenant 影响 |
|---|---|---|
| `purge-contribution` 未启用合并时 409 | 管理面，multi 才有该路由 | 不可达 ✅ |
| `DELETE .../projects/{id}/members/{tid}` 新增清账本 | 同上 | 不可达 ✅ |
| 新增 `merge-status` 只读端点 | 同上 | 不可达 ✅ |
| 新增 5 个配置键 | 有默认值；`<=0` 关闭 | 生效但无消费者（`MergeStore` nil） ✅ |
| `docs/CONFIGURATION.md` 重新生成 | 纯文档 | 无 ✅ |
| 新增 `0003_merge_state.sql` 迁移 | **见 13.3.1** | ⚠️ 需决定 |

#### 13.3.1 遗留决定：`0003` 是否在单 Tenant 部署里执行

**事实（已核对代码）**：`migrateAdminSchema`（`admin_migrate.go:40`）没有任何
tenant-mode 判断，`admin_plane.go:28` 也没有。所以一个
`tenant_mode: single` + `run_migrations_on_startup: true` 的部署，**今天就已经在
执行 `0001`+`0002`**，`admin` schema 里已存在 `tenant`、`tenant_key`、
`admin_audit`、以及 `team_member`、`team_project`、`team_project_member`。

也就是说：**"单 Tenant 下不该有 team 表"这件事，在 A 部分就已经不成立**，
`0003` 只是延续同一模式。两个方案：

| 方案 | 做法 | 优点 | 代价 |
|---|---|---|---|
| **A（推荐）** | 给 `AdminMigrationSeries` 加 `MultiTenantOnly` 标记，`migrateAdminSchema` 在 single 模式**跳过**这些版本；`0002` 与 `0003` 都标上 | "单 Tenant 无 Team"在**数据层**也成立，心智模型与 schema 一致 | 要给迁移类型加字段与一个跳过分支；需要一个从 multi 降级到 single 的说明（跳过=不再新增，不回滚） |
| **B** | 接受现状，在 `docs/SCHEMA-ALIGNMENT.md` 与 `AGENTS.md` 写明 `admin` schema 的表不参与 extra 检查 | 改动最小 | 一个从不用团队的部署，库里仍有合并账本表；对齐文档要开一个例外 |

**A 与 B 都不破坏现有功能**——区别只在"单 Tenant 部署的库里该不该有团队表"这个
认知问题。**这是一处需要产品/运维拍板的决定，v3 两案都写在这里而不擅自选定。**

若选 A，需一并处理：`0002` 也标 `MultiTenantOnly`（否则修一半）；已应用过
`0002` 的部署升级时 `schema_migrations` 里保留既有行，跳过只影响**未应用**的版本。

### 13.4 三条可验证条款（缺了就是"没有兼容性声明"）

单 Tenant 的兼容**必须由测试证明**，而不是由文档声明。三条断言：

**条款 1 —— 单 Tenant 不注册合并任务。**

```go
// internal/app/maintenance_single_tenant_test.go
// 单 Tenant 配置下，维护循环的 job 集合里没有 "project.merge"，
// 且 AnyJobEnabled 不因新增的默认间隔而变真。
func TestProjectMergeIsAbsentInSingleTenantMode(t *testing.T)
```
断言：`TenantMode=Single` 构造出的 `MaintenanceConfig.ProjectMergeInterval` 即使
被设为 60（默认值）也不使 `AnyJobEnabled()` 因它而为真；`NewMaintenanceLoop` 的
job 列表不含 `project.merge`。

**条款 2 —— 单 Tenant 与"未命中回退"下，读路径的 query 不带 `Fallback`。**

```go
// 用一个记录 query 的 fake store，断言 recall 与 list 传给 store 的
// Fallback 字段恒为 nil。
func TestRecallPassesNoFallbackWithoutTeamScope(t *testing.T)
func TestListMemoriesPassesNoFallbackWithoutTeamScope(t *testing.T)
```
同时覆盖两个场景：`TeamScope == nil`（单 Tenant），以及 `TeamScope` 存在但
resolver 未命中（多 Tenant 下的普通个人 bank）。**这条也覆盖 13.2 表里"永不设置"
的四个内部调用方**——它们根本不经过 handler，最容易被后续重构误改。

**条款 3 —— 不实现 `BankPresence` 的 store 行为与今天一致。**

```go
// 一个不实现 BankPresence 的 store：recall 与 list 正常返回个人 bank 的内容，
// 不报错、不回退。这是单 Tenant 与所有测试替身的默认路径。
func TestStoreWithoutBankPresenceNeverFallsBack(t *testing.T)
```
这条把 GAP-1 的"能力检测"从设计意图变成契约：**能力缺失 = 回退关闭**，而不是
"能力缺失 = 报错"或"能力缺失 = 静默走错路径"。

### 13.5 分阶段验证

- PR-1（B-0…B-3）落地后：单 Tenant 部署的**数据面完全未变**（回退还没接），
  只需验证 `project.merge` 不注册、`0003` 已按 13.3.1 的选定方案生效；
- PR-2（B-4…B-6）落地后：条款 2、3 必须全绿；这是唯一真正触碰数据面读路径的 PR，
  **它的验收标准就是"单 Tenant 的 recall/list 与升级前逐字节相同"**。

## 十四、实施记要

实施完成于 2026-10-30。§13.3.1 的**方案 A 被采纳**：单 Tenant 部署的 admin
schema 只应用 `0001`。

### 交付清单

| 工作项 | 落地 |
|---|---|
| B-0 迁移与模型 | `embeddedadmin/0003_merge_state.sql`；`storage.AdminMigrationSeriesFor` / `MigrateAdminSchemaFor` + `adminTeamMigrations{2,3}`；`tenant/mergekey.go`（`Normalize`/`MergeKey`）；`tenant/teams.go` 的 `MergeState`/`MergeSourceUnit`/`DroppedMergeKey`/`MergeLockName` |
| B-1 `MergeStore` | `storage/merge_store.go`（接口）、`postgres_merge_store.go`（读 + claim）、`postgres_merge_store_write.go`（`FinishMergeStanza` + fence + 账本 + 生命周期）；`storage/bank_presence.go`（`BankPresence` + `FallbackScope`） |
| B-2 引擎 | `internal/projectmerge/merge.go`：`Normalize`/`MergeKey`/`ParseAnswer`/`Diff`/`MergeResult.SourceMergeKeys`/`Prompt` |
| B-3 任务 | `internal/app/project_merge.go`（`mergeRunner`、`planRound`、`buildFinishRequest`、`mergeEnabled`）；`maintenanceJobsWithMerge`；`merge_store{,_pgx,_nopgx}.go` |
| B-4 回退 | `internal/teamread/resolver.go`；`storage/team_fallback.go`（`withFallback`/`restoreBankID`）；`RecallQuery.Fallback`、`MemoryQuery.Fallback`；`PostgresStore`/`MemoryStore` 两后端；两个 handler 的 `TeamScope` |
| B-5 生命周期 | `adminapi/team_merge.go`（`merge-status`、`merge-reset`）；`deleteProjectMember` 清账本；`purgeContribution` 原子化（未启用合并 → 409） |
| B-6 控制台 | **未做**（前端 `merge-status` 展示与文案留作后续，见下） |
| B-7 文档 | 本文件、`AGENTS.md`、`CONFIGURATION.md`/`etc/example.wenyuan.yaml`/`.env.example` 重新生成（500 键） |
| B-8 兼容与集成 | `admin_migrate_test.go`、`maintenance_single_tenant_test.go`、`team_read_test.go`、`team_fallback_test.go`、`project_merge_test.go`、`project_merge_pgx_test.go`、`postgres_merge_store_pgx_test.go`、`resolver_test.go`、`mergekey_test.go` |

### 实施中发现的、设计没预料到的三处

1. **`requireBank` 挡住了 onboarding。** 设计说"成员个人 bank 不存在时仍读团队"，
   但 `reporting.requireBank` 在 handler 层先返回 404。已改为：部署有
   `TeamScope` 且解析出 scope 时放行，否则仍是 404。`recallWhere` 对缺失的个人
   bank 返回空结果而不是错误（`TestMemberRecallReadsTheTeamBank` 覆盖）。
2. **内存后端忽略了 schema。** `MemoryStore` 是一个扁平命名空间，所以
   `withFallback` 的 PostgreSQL 版本对它不生效。已为 `MemoryStore` 的
   `Recall`/`ListMemories` 各加一层同样的回退（重定向 + bank id 改写）。
3. **`'-infinity'` 游标扫不进 `time.Time`。** 驱动把它当字符串返回。已加
   `parseCursorTimestamp`，两种格式都读（零值 = 从头读）。

### 与设计的偏离

- **`GET /memories` 的响应字段是 `items`**，`MemoryItem` **不带 `bank_id`**。
  设计 §B-4 说"改写 `MemoryItem` 的 bank id"——该字段在响应里本就不存在。实际
  改写发生在 storage 层返回的 `MemoryUnit.BankID` 上（下游查询用它），响应形状
  不变。这是**正确**的：客户端本来就不该知道服务端从哪个 bank 读。
- **`enqueueTeamConsolidation` 用 `storage.OperationEnqueuer` 能力检测**，在团队
  schema 的 context 下入队；失败只记 warn（合并事实已提交，reconciler 会兜底）。
- **`roundDeadline` 下限 1 分钟**，既是单轮上限也是 claim 的失效阈值。

### 验证

`make check` 全绿（build / test / vet / pgx / lint / gofmt）；带
`WENYUAN_TEST_DATABASE_URL` 的 `go test -tags pgx ./...` 全绿，含 PG 集成的
8 个 merge store 场景与 4 个 merge job 场景。

### 后续（本轮明确不做）

- **B-6 控制台**：`merge-status` 端点已就绪，前端展示与 10 语种文案未接。
- 写触发的合并入队（秒级可见）。
- reflect 在团队视图下的 mission 语义。
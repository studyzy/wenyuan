# B 部分：项目合并与共享记忆 —— 实施计划

依据：`docs/plans/2026-10-29-team-project-shared-memory-design.md`（设计 v2）§六、
§七、§八，以及 `docs/plans/2026-10-31-team-registry-implementation-plan.md`（已实施
的 A 部分，团队对象维护）。设计文档里 A 部分没做的事，是这个计划的全部内容：

1. 把成员的个人事实**合并**成一个项目的团队事实（`project.merge`）；
2. 让成员的 **recall / memories 列表**读回团队内容；
3. 成员删除事实后，团队内容**跟着消失**；
4. `purge-contribution` 真正**撤回**某个成员的贡献。

状态：待实施。前置：A 部分已合并（`620fa48`），`tenant_mode: multi` 下管理面与
合同表已在线。

## 一、先说清楚 A/B 之间的边界

A 部分已定的、B 部分**不能动**的东西：

- `0002_team_registry.sql` 的表结构（`team_project.bank_id` 一列、无
  `team_project_member.project_name`、无 `merge_state`）。新的表结构只走
  `0003_*.sql`。
- 同一个项目在多个 Team 下各有一份：`A::Web` 与 `B::Web` 是两个不同 Team 的两个
  项目，各合并各的。一个成员的 `A::Web` 事实**同时**是两个项目的输入——这是 A 部分
  确认的行为。
- 删项目 = 删该项目前缀下的全部 bank。

B 部分要新加的东西只有：一张迁移文件、一个维护任务、两处读路径、一组管理面
端点。

## 二、本轮要先定的四个设计决定

设计与 A 部分的实际情况有出入，实施前必须定死：

### B1. 合并状态表：`0003_merge_state.sql`

设计 §五 的表按原样落地，但**外键补全**：

```sql
CREATE TABLE IF NOT EXISTS merge_state (
    team_tenant_id    text NOT NULL,
    project_id        text NOT NULL,
    tenant_id         text NOT NULL,
    cursor_updated_at timestamptz NOT NULL DEFAULT '-infinity',
    cursor_unit_id    text NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    last_merged_at    timestamptz,
    last_error        text,
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
    consumed_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_tenant_id, project_id, tenant_id, source_unit_id),
    FOREIGN KEY (team_tenant_id, project_id)
        REFERENCES team_project(team_tenant_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (team_tenant_id, tenant_id)
        REFERENCES team_member(team_tenant_id, tenant_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS merge_source_unit_tenant_idx
    ON merge_source_unit (team_tenant_id, project_id, tenant_id);
```

与设计的两处差异：

- `source_unit_id` 用 `uuid` 而不是 `text`：它永远是一个 unit id，用 uuid 才能
  在外键/索引上比较得快；A 部分已经证实 `memory_units.id` 是 uuid。
- 加 `rebuild_requested`：`purge-contribution` 要“移出成员后重建”，A 部分只做到
  移出，B 部分用这一列承接（设计 §八 的“撤回贡献”）。

### B2. 合并落点：团队 bank 由合并独占写

团队 bank 是 `<team>::<project name>`，也就是 A 部分建项目时**已经算出**的
`team_project.bank_id`。合并写入时：

- bank 不存在就建（`EnsureBank` + 一条 `banks` 行），A 部分的“建项目只写注册表”
  在这里收口；
- 写 `fact_type='world'`、`document_id=NULL`、`metadata={"merge_key":…,
  "merged_at":…}`；
- **不写**任何 tag（设计决策 10：provenance 只在账本里，tag 会污染 fuzzy 解析与
  `ListTags`）。

### B3. 读回退只覆盖两个读路径，且按 bank 判定

A 部分把“按 id 读不回退团队”定为 D2。B 部分集成前再确认一次范围：

| 读路径 | 是否回退 | 理由 |
|---|---|---|
| `POST /banks/{id}/recall` | **是** | 设计的核心价值 |
| `GET /banks/{id}/memories` | **是** | 与控制台一致，否则控制台看不到团队内容 |
| `GET /banks/{id}/tags`、`/graph`、`/entities`、`/stats`、`/documents`、`/chunks` | **否** | 它们是“这个 bank 的原始账本”，回退会让一个 bank 的统计变成另一个 bank 的；设计 §七 也只写了 recall 与列表 |
| 按 id 读/改/删 unit | **否** | D2 |
| reflect | **否**（本轮） | 它基于 recall，回退后团队内容自动可见；但“团队视图下 reflect 的 mission 取自哪个 bank”需要单独的产品决定，留作后续 |

### B4. 删除检测的触发条件（A 部分 D1/D3 的落地）

每个 `(project, member)` 每轮只做**一次轻量判定**，不做全量反连接：

1. `merge_state` 行不存在 → 首次合并，不需要删除检测；
2. 个人 `banks` 行不存在 → **跳过**（onboarding；团队内容保留）；
3. 个人 bank 的 `(bank.updated_at, count(memory_units))` 与 `merge_state` 记录的
   `bank_version` 一致，且已消费的输入没有变化 → 团队内容不会过期，跳过；
4. 否则做反连接：`merge_source_unit LEFT JOIN <member>.memory_units ON id`，
   命中 `m.id IS NULL` 即触发重建。

为此 `merge_state` 再加两列：

```sql
ALTER TABLE ... ADD COLUMN bank_updated_at timestamptz;   -- 最近一次判定时个人 bank 的 updated_at
ALTER TABLE ... ADD COLUMN bank_unit_count bigint NOT NULL DEFAULT 0; -- 同一时刻的 unit 数
```

（这就是 A 部分计划里说的“反连接只在有原因时才做”的具体实现。）

## 三、工作项

### B-0 迁移与模型

- `internal/storage/embeddedadmin/0003_merge_state.sql`：§B1 的两张表 + §B4 的两列。
- `internal/tenant/teams.go` 增加 `MergeState`、`MergeSourceUnit` 两个结构体，以及
  `MergeKey(teamID, projectID)`（advisory lock 的名字来源）。

验证：`go test ./internal/storage -run TestAdminMigrationSeries`（现在的
`TestTeamRegistryMigrationKeepsTheMergeLedgerOut` 要改成断言 0003 存在、0002 仍不含
它——用 `AppliedAt` 区分：0002 的行不变，0003 的行新增）。

### B-1 `MergeStore` 接口与两个后端

`internal/storage/merge_store.go`：

```go
type MergeStore interface {
    // 调度：列出有成员的项目及其成员状态
    ListMergeProjects(ctx context.Context) ([]MergeProject, error)
    // 锁：拿不到返回 ok=false，不报错
    TryMergeLock(ctx context.Context, teamID, projectID string) (release func() error, ok bool, err error)
    // 输入
    MemberWorldFacts(ctx context.Context, memberSchema, bankID string, after MergeCursor,
        limit int) ([]MergeFact, error)
    AllMemberWorldFacts(ctx context.Context, memberSchema, bankID string,
        limit int) ([]MergeFact, error)
    // 删除检测
    MissingSourceUnit(ctx context.Context, memberSchema string, key MergeKey) (bool, error)
    MemberBankVersion(ctx context.Context, memberSchema, bankID string) (MergeBankVersion, error)
    // 输出（一个事务）
    ReplaceTeamFacts(ctx context.Context, req ReplaceTeamFactsRequest) error
    FailClosedTeam(ctx context.Context, req FailClosedTeamRequest) error
    // 状态
    SaveMergeState(ctx context.Context, state tenant.MergeState) error
    RequestRebuild(ctx context.Context, teamID, projectID string) error
}
```

- Postgres 实现 `internal/storage/postgres_merge_store.go`；内存实现
  `internal/storage/memory_merge_store.go` 只够单测（接口能力检测，与
  `ConsolidationReconciler` 同风格）。
- 锁：`pg_try_advisory_xact_lock(hashtext('wenyuan:project-merge:<team>:<project>'))`，
  整轮在一个事务里，模型调用也在事务内——设计 §六 明确允许。

**`ReplaceTeamFacts` 的事务语义**（这是 B 部分最需要被钉住的东西）：

1. 读完团队 bank 现有 `world` 事实，按 `metadata.merge_key` 建索引；
2. 模型输出 → 规范化 → `merge_key`：同 key 的保留原行（**id 不变**，观察不受影响）；
3. 团队有、结果没有的删掉，并清掉因此失去全部来源的观察（照
   `postgres_banks.go` 里 `DeleteBank` 的清除顺序）；
4. 结果有、团队没有的插入；
5. 追加 `merge_source_unit`、更新 `merge_state`（游标、`bank_updated_at`、
   `bank_unit_count`、`last_merged_at`、清 `last_error`）；
6. 提交。

验证：`go test ./internal/storage -run TestMergeStore`（两个后端同一套）；
`make pgx`。

### B-2 合并引擎（不依赖数据库的部分）

`internal/projectmerge/` 新包，纯函数便于单测：

- `Normalize(text string) string`：去首尾空白、折叠内部空白；
- `MergeKey(text string) string`：`sha256(Normalize(text))` 的 hex；
- `Diff(previous []TeamFact, merged []TeamFact) (keep, remove, insert []TeamFact)`；
- `Prompt(mission string, previous []string, fresh []MemberFact) []provider.Message`；
- `ParseAnswer(raw string) ([]string, error)`：模型返回 JSON
  `{"facts": ["…"]}`，解析失败要能区分“结构不对”与“空结果”。

验证：`go test ./internal/projectmerge -count=1`，表驱动：规范化等价性、diff 三类、
prompt 里上一版内容一定在、解析失败不 panic。

### B-3 `project.merge` 维护任务

`internal/app/project_merge.go`：

1. `ListMergeProjects` 取 kind=team、active、有成员的项目；
2. 对每个项目 `TryMergeLock`，拿不到记一次 skip；
3. 对每个成员：§B4 的判定 → 有需要才取事实（按 `(updated_at, id)` 游标，只取
   `fact_type='world'`）；
4. 全项目本轮新输入合计 ≤ `ProjectMergeBatchSize`；
5. 无变化 → 只更新心跳（`last_merged_at`），**不调模型**；
6. 有变化 → 模型合并 → `ReplaceTeamFacts`；团队事实换了 → 向 `T::X` 入队一次
   `consolidate`（复用 `EnqueueOperation` + `ConsolidationSerializationKey`）；
7. 重建路径：忽略游标读全部，超过 `ProjectMergeRebuildMaxUnits` → `FailClosedTeam`
   并把 `last_error` 写清楚；
8. 一轮耗时上限 `min(10×interval, 5min)`，超时回滚并写 `last_error`。

配置（`internal/config/maintenance.go`）：

```
WENYUAN_API_PROJECT_MERGE_INTERVAL_SECONDS       默认 60，0 关闭
WENYUAN_API_PROJECT_MERGE_BATCH_SIZE             默认 10
WENYUAN_API_PROJECT_MERGE_REBUILD_MAX_UNITS      默认 5000
```

`AnyJobEnabled` 计入 `project.merge`；`NewMaintenanceLoop` 与
`NewBackgroundRoles` 多接一个 `storage.MergeStore`（nil 不注册）；`cmd/worker` 同样。

模型选择：**没有 chat provider 时不注册该任务**（`provider none` 下无法合并，注册
了只会每轮写 `last_error`）。这一点与 `RefuseWithoutReflectModel` 同一思路。

验证：单测 `go test ./internal/app -run TestProjectMerge`（内存后端 + 假模型：
游标不推进、锁跳过、批次截断、失败关闭）；PG 集成
`internal/app/project_merge_pgx_test.go` 覆盖设计 §十一 的九条 PostgreSQL 场景
（三 schema 一轮、无变化零写入、批次超限、删来源触发重建、退出项目、双进程锁、
超上限失败关闭、观察生成）。

### B-4 读回退

`internal/teamread/`（新包，与 `projectmerge` 一样不依赖 HTTP）：

- `ParseBankID(bankID string) (tenantID, projectName string, ok bool)`：按**第一个**
  `::` 拆；
- `Resolver`：持 `storage.TeamRegistry`（A 部分的 `ListTeamProjects` 之类不够，需要
  新增 `ResolveProjectForMember(ctx, tenantID, projectName) (tenant.TeamProject, bool,
  error)`）；三步解析：前缀必须等于当前 schema → 查注册表 → team 必须 active；
- 回退判定：团队 bank 是否存在且**有任何 unit**（`BankHasUnits`，两个后端都要实现）。

接线：

- `memoryops.Handlers.TeamScope`（nil = 今天的行为），`Recall` 里在
  `h.Store.Recall` 前解析、在 `WithSchema(ctx, teamSchema)` 后调用；
- `reporting.Handlers.TeamScope`，`ListMemories` 与 `requireBank` 同法；
- 团队命中后把 `MemoryUnit.BankID` 改写回请求里的个人 bank id（memories 列表的
  `MemoryItem` 不带 `bank_id`，所以要改写的是 `UnitEntityNames` /
  `MemoryAttachments` 这些以 bank 为参数的下游查询——它们必须用**实际读取的
  bank**，只有返回给客户端时才替换）。recall 的 `RecallMatch` 结构里根本没有 bank
  id（`internal/storage/models.go:235`），recall 无需改写；`MemoryUnit` 有。
- `internal/app/app.go` 在 multi 模式注入，single 模式为 nil。

验证：单测 `go test ./internal/teamread -run TestResolve`（有行/无行/非 active/
团队为空/团队有内容/个人 bank 不存在）；端到端
`internal/app/team_read_test.go`：成员 recall 命中团队事实且 `bank_id` 仍是
`A::X`、团队为空回退个人、B 的凭证读 `T::X` 不回退。

### B-5 `purge-contribution` 与生命周期

- `POST .../projects/{id}/purge-contribution` 的语义从 A 部分的“只移出”改为：
  移出项目 → `RequestRebuild(team, project)` → 下一轮重建；响应
  `{"status":"purged","rebuild":true}`。若 `project.merge` 未启用，返回
  `rebuild:false` 并说明原因（不能假装重建了）。
- 成员退出 Team：`merge_state` / 账本随 `team_member` 外键级联删除，已合并内容
  保留（设计 §八）。
- 删除个人记忆 / 删个人 bank：§B4 的反连接命中 → 重建。

验证：`go test ./internal/app -run TestTeamLifecycle`（内存）+ PG 集成
`TestPurgeContributionRebuilds`。

### B-6 控制台

- 团队项目行显示合并状态：`last_merged_at`、`last_error`、团队 bank 的 unit 数
  （`GET .../projects/{id}/merge-status`，管理面只读端点）。
- `purge-contribution` 按钮的响应从“只移出”改为“已撤回并重建”，未启用合并时
  如实提示。
- 消息进 10 个语种（沿用 A 部分的 `teams.detail.*` 命名空间；导航那条教训：
  共享组件读哪个命名空间就把键放哪个）。

验证：`npm run typecheck && npm run lint && npm run test && npm run build`；新增
`frontend/test/team-merge-status.test.ts` 与组件测试。

### B-7 文档

- `docs/plans/2026-10-31-team-registry-implementation-plan.md` §六 指向本文件。
- `AGENTS.md` 团队段落补“合并任务、读回退、可见性窗口（默认 60s+一次模型调用）”。
- 本文件文末追加实施记要。

## 四、顺序与依赖

```
B-0 ─▶ B-1 ─┬─▶ B-3 ─▶ B-5 ─┐
            │               ├─▶ B-7
B-2 ────────┘               │
B-1 ─▶ B-4 ─────────────────┘
B-6 在 B-3、B-5 之后（需要 merge-status）
```

- B-2 与 B-1 可并行（不同文件、不同包）。
- B-3 依赖 B-1 的 `MergeStore` 与 B-2 的纯函数。
- B-5 依赖 B-3（重建路径）。
- 建议两个 PR：**PR-1 = B-0…B-3（合并）**、**PR-2 = B-4…B-6（读回退 + 撤回 +
  控制台）**。读回退要等合并已经在跑，否则回退永远命中空团队。

## 五、风险与对策

| 风险 | 对策 |
|---|---|
| 合并的多租户安全性：团队 bank 在另一 schema 下写 | 所有合并写入都经由 `WithSchema(teamSchema)` + 独立句柄；PG 集成测试里用 B 的凭证读 `T::X` 必须 404 |
| 删除检测漏判 | §B4 用 `(updated_at, count)` 粗筛 + 反连接兜底；重建失败关闭时清空团队内容而不是保留已删数据 |
| 模型返回不可解析 | 解析失败不写任何行、游标不推进、`last_error` 记录；连续失败 N 轮后熔断（`merge_state.fail_count`，第 N 轮起跳过该项目直到 `purge`/手工重置） |
| 读回退误命中 | 前缀必须等于当前 schema，且 team 必须 active；B 的凭证读 `T::X` 有端到端测试 |
| 观察 id 每轮被重建 | 合并按 `merge_key` 保留原行，id 不变；观察只在下一次 consolidation 里更新 |
| 可见性窗口（默认 60s + 模型调用） | 设计承认的折中；写触发入队留作后续（需要 worker 新操作类型） |

## 六、明确不做

- 跨 Team 共享、共享原始对话/documents/chunks/attachments。
- 秒级可见（写触发的合并入队）。
- 按成员逐条撤回的 UI（只有显式 purge-contribution）。
- 项目/bank 改名；团队 bank 的配额、限流、成本核算。
- reflect 在团队视图下的 mission 语义（B-4 列了，留作后续）。
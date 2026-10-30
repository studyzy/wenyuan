# Team / Project 共享记忆 —— 实施计划

依据：`docs/plans/2026-10-29-team-project-shared-memory-design.md`（设计 v2）与
`docs/plans/2026-10-29-multi-tenant-design.md`（P5）。多租户基座已落地（提交
`d03ea7d` 之前的多租户系列），本计划只做团队层。

状态：**已拆成两部分实施。Team / Project 注册表对象的维护（迁移、注册表、管理面
路由、控制台）见 `docs/plans/2026-10-31-team-registry-implementation-plan.md`，
本轮先做那部分；本文件保留 B 部分（合并任务、读回退、提取项目记忆）的完整设计，
待 A 部分落地后按 §二 的 S4–S6 执行。**
门槛：每一步完成即跑该步的验证命令，全部完成跑 `make check`。

## 零、先说清楚的三处设计细化

设计与现有代码对不上的地方，按现有代码收紧，实施时以此为准：

| # | 设计里的说法 | 实施口径 | 理由 |
|---|---|---|---|
| D1 | §六「每次 tick 每个 `(project, member)` 做反连接」 | 反连接**只在有原因时才做**：该成员自上次合并以来 `banks.last_write_at` 或 `updated_at` 变了，或 `banks` 行已不存在。未变的成员跳过 | 全表反连接是每 60s 每成员的账本全扫，规模一大就是热点；水位粗筛正好用于“值不值得查” |
| D2 | §七「按 id 取 unit 时在团队 bank 里只读解析」 | **同时移出 §七与 §十一**：unit 按 id 读**不**回退团队；只有 recall 与 `GET .../memories` 列表回退 | 个人侧 `PATCH/DELETE /memories/{id}` 是只读个人 schema 的一次调用；若 GET 能解析到团队行、后续写却 404，客户端看到的是坏对象。设计 §十二 的非目标里，按成员撤回也只有 `purge-contribution` |
| D3 | §六 删除检测用 `banks.last_write_at` 与团队成员行 `updated_at` | 个人 bank 不存在（设计 §七 的 onboarding 路径）**不触发**反连接，团队内容原样保留 | `readAtTeam` 对不存在的个人 bank 返回 `found=false`，不是错误 `ErrBankNotFound` |

其余按设计执行：`admin` 自有迁移序列、`(updated_at, id)` 游标、项目级 advisory
lock、批次只限新输入、失败关闭、`bank_id` 响应改写回请求里的个人 bank id。

## 一、范围与硬约束

- 只在 `tenant_mode: multi` 可用；`single` 模式不迁移 `admin`、不注册
  `project.merge`、不做读回退，行为与今天完全一致。
- 租户 schema 不新增任何对象（`SCHEMA-ALIGNMENT` 只忽略 `schema_migrations`）。
- 数据面路由、JSON 字段、状态码、请求头一律不改；不新增数据面路由。
- 管理面路由只在 operator key 下，不进 `/openapi.json`。
- 不碰 `migrations/` 与 `internal/storage/embedded/`；
  `scripts/reference_schema_migrations.py` 重新生成不影响 `embeddedadmin/`。
- 每个行为变更带一个不带它就失败的测试；端到端行为进
  `internal/app/parity_integration_test.go` 或同目录新文件。
- 文件 ≤500 行（测试 ≤800 行），按包边界拆文件。

## 二、工作项

### S1 注册表：`admin` 迁移 0002 / 0003 与共享模型

做什么

- 新增 `internal/storage/embeddedadmin/0002_team_registry.sql`：
  `tenant.kind` 列（`personal` / `team`，默认 `personal`）、`team_member`、
  `team_project`、`team_project_member`（含 `UNIQUE (tenant_id, project_name)`）。
- 新增 `internal/storage/embeddedadmin/0003_team_merge.sql`：
  `merge_state`（游标两列、`last_merged_at`、`last_error`）、`merge_source_unit`。
- 迁移文件逐字取自设计 §五，只补两处：`merge_state / merge_source_unit` 对
  `team_member` 的外键（设计只对 `team_project` 级联，成员行的清理按 §五 末段
  由代码显式做，外键用 `ON DELETE CASCADE` 兜底不会与“显式清理”冲突）。
- 共享模型放 `internal/tenant/teams.go`（新文件）：`TeamMember`、`TeamProject`、
  `ProjectMember`、`MergeState`、`MergeSourceKey`、`MemberRole` 常量、
  以及 `ValidateProjectID` / `ValidateProjectName`（正则与设计 §五 一致）。
  `tenant` 包放模型是因为注册表接口的实现两侧（memory / postgres）都要用，且
  `adminapi` 已在依赖它。

怎么验证
- `go test ./internal/storage -run TestAdminMigrationSeries`（新增用例：序列为
  0001..0003，且 0002/0003 的每个语句都能在空 schema 上执行——用现有
  `migrateAdminSchema` 的 schema 参数指向临时 schema）。
- `go test ./internal/tenant -run TestValidateProject`（表驱动：合法、大写、
  过长、下划线开头、含 `::`、含空格）。

### S2 注册表接口与两个后端

做什么

- `internal/storage/team_registry.go`：`TeamRegistry` 接口，方法集（全部
  `ctx` 参数，返回 `tenant.ErrNotFound` / `tenant.ErrExists` 语义）：
  `CreateTeam`、`ListTeams`、`GetTeam`、`ListTeamMembers`、`AddTeamMember`、
  `RemoveTeamMember`、`ListTeamProjects`、`CreateTeamProject`、
  `GetTeamProject`、`DeleteTeamProject`、`ListProjectMembers`、
  `AddProjectMember`、`RemoveProjectMember`、
  `ResolveProjectForMember`（读路径入口，设计 §七 第 2 步的那条查询）、
  `PurgeContribution`（移出 + 清状态账本；重建由 S5 做）。
- `internal/storage/memory_team_registry.go`：内存实现（测试与无库开发用，
  含 `UNIQUE (tenant_id, project_name)` 冲突 → `tenant.ErrExists`）。
- `internal/storage/postgres_team_registry.go`：`*PostgresTenantRegistry` 的
  方法（同一 `db` 与 `admin` schema），复用 `qualified` / `isUniqueViolation`。
- `ListTeams` 按 `tenant.kind='team'` 过滤；`CreateTeam` 只允许把刚由
  `CreateTenant` 写出的行标成 team（`kind` 不可从 personal 改 team，
  `UpdateTenant` 加一个 `Kind` 字段的**拒绝**逻辑：非零且与现值不同即报
  `tenant.ErrInvalidTransition`）。
- 边界测试：`admin` 表名出现在 `internal/storage/` 之外的 SQL 形状即失败，
  扩展现有 `boundary_test.go` 的检查器到团队表。

怎么验证
- `go test ./internal/storage -run 'TestTeamRegistry|TestTeamTableBoundary'`
  （两个后端跑同一张表驱动的接口用例）。
- `make pgx`（新文件带 pgx 标签的分支也要编译）。

### S3 管理面路由：Team / Member / Project

做什么

- `internal/adminapi/teams.go`（新文件，设计 §九 的 15 条路由）：
  `GET/POST /admin/v1/teams`、`GET/PATCH/DELETE /admin/v1/teams/{id}`、
  `GET/PUT/DELETE .../members[/{tenant_id}]`、
  `GET/POST .../projects`、`GET/DELETE .../projects/{project_id}`、
  `GET/PUT/DELETE .../projects/{project_id}/members[/{tenant_id}]`、
  `POST .../projects/{project_id}/purge-contribution`。
- `Config` 增加 `Teams storage.TeamRegistry` 与 `BankCreator`（建 `T::X`、
  查 `T::X` 是否存在）。`Teams` 为 nil 时整组路由 501（single 模式）。
- 规则落地：
  - `POST /teams`：复用 `Registry.CreateTenant` + `Provisioner(adopt=false)`，
    然后 `CreateTeam`；返回 tenant 与一把 key（与 `POST /tenants` 同形）。
  - 建项目 `{project_id, name, adopt_bank?}`：`bank_id = <team>::<name>`；
    `T::X` 已存在且未 `adopt_bank` → 409；建 bank 失败回滚项目行。
  - 加项目成员前置：已是 Team 成员、目标 tenant `status='active'`；该成员已有
    同名项目 → 409（设计决策 19）。
  - `DELETE /projects/{id}?delete_bank=true` 才删 `T::X`，否则只删注册表行；
    未带参数返回 409 并要求显式二选一。
  - `PATCH /teams/{id}` 只改 `display_name`。
- 路由注册集中在 `adminapi.New`，鉴权与错误信封沿用现有。

怎么验证
- `go test ./internal/adminapi -run TestTeamRoutes`（表驱动：每个路由的正例，
  以及 409 / 404 / 400 三类反例；不依赖数据库，用内存注册表）。
- `go test ./internal/adminapi -run TestTeamsNotConfigured`（`Teams=nil` → 501）。

### S4 读路径：成员资格解析与两级回退

做什么

- `internal/storage/bank_scope.go`（新文件）：
  - `SplitBankID(bankID) (tenantID, projectName, ok)`：按**第一个** `::` 拆。
  - 常量 `SafeFallbackUnavailable` 不需要；回退由调用方决定。
- `internal/app/read_scope.go`（新文件）：`TeamReadScope`，构造参数是
  `storage.TeamRegistry` + 一个 `*storage.PostgresStore` 的自有 schema 读句柄
  （默认 schema 已设成请求的租户 schema；`WithSchema` 切换时用同一句柄）。
  核心函数：
  ```
  func (s *TeamReadScope) Apply(ctx context.Context, bankID string) (ReadPlan, error)
  type ReadPlan struct {
      TeamSchema string   // "" 表示没有团队回退
      TeamBankID string
      Fallback   func(ctx context.Context, err error) (bool, error) // 主源失败时是否回退
  }
  ```
  `Apply` 的三步即设计 §七「解析」：前缀必须等于当前 schema → 查
  `ResolveProjectForMember` → 命中才带出 `TeamSchema/TeamBankID`。
- `internal/memoryops/recall.go`：在 `h.Store.Recall` 外加一层
  ```
  if h.TeamScope != nil { ... teamCtx := storage.WithSchema(ctx, plan.TeamSchema) ... }
  ```
  主源选择按设计 §七：先查团队 bank 是否**存在且有任何 unit**（新增存储方法
  `BankHasUnits(ctx, bankID)`，两个后端都要实现，SQL:
  `SELECT EXISTS (SELECT 1 FROM <schema>.memory_units WHERE bank_id=$1)`），
  有则只查团队，没有才查个人；个人 bank 不存在（`ErrBankNotFound`）且团队有
  内容时也必须返回团队内容 —— 这条要求主源判定放在句柄调用**之前**，不能
  靠错误回退。团队命中后把 `RecallResult` 里每行的 `BankID` 改写成请求里的
  个人 bank id。
- `internal/reporting/memory_routes.go` 的 `ListMemories` 与
  `internal/reporting/handlers.go` 的 `requireBank` 同法：`requireBank` 增加
  “个人不存在但团队有内容则解析为团队” 的分支，查询走团队 schema，返回行
  `bank_id` 改写。
- 只覆盖 recall 与 `GET .../memories`（见 D2）。reflect、documents、chunks、
  entities、graph、tags、operations 不动。
- 接线：`memoryops.Handlers.TeamScope` / `reporting.Handlers.TeamScope` 由
  `internal/app/app.go` 在 `tenant_mode: multi` 且注册表可用时注入，否则 nil
  （零开销、行为不变）。

怎么验证
- 单测 `go test ./internal/storage -run TestSplitBankID`（`A::X`、无 `::`、
  前缀不等于当前 tenant、多个 `::` 只按第一个拆）。
- 单测 `go test ./internal/app -run TestTeamReadScope`（内存注册表：有行 / 无行 /
  team 非 active / 团队为空 / 团队有 unit / 个人 bank 不存在）。
- 端到端 `internal/app/team_*_test.go`：
  - 成员 recall 命中团队事实，响应 `bank_id` 仍是 `A::X`；
  - 团队为空 → 回退个人 bank，JSON 与今天逐字段一致；
  - `GET .../memories` 同一组三种情形；
  - B 的凭证读 `T::X` 不被回退（前缀不等于 B 的 tenant）。

### S5 合并任务 `project.merge`

做什么

- `internal/storage/merge_store.go`（接口 `MergeStore`，带能力名，与
  `ConsolidationReconciler` 同风格）：
  - `ListMergeProjects`（kind=team 且 active、有成员的 project + 成员 + 各自
    `merge_state`）
  - `TryMergeLock(ctx, team, project) (release func(), ok bool)` ——
    `pg_try_advisory_lock(hashtext(...))` 用**专用连接**持有（`db.Conn()` 或
    事务级锁 + 一个事务贯穿整轮；采用 `pg_try_advisory_xact_lock` 并让整轮跑在
    一个显式事务里，模型调用也在这个事务里——设计允许“锁覆盖到模型”）。
  - `MemberWorldFactsSince(ctx, memberSchema, bankID, cursor, limit)`
  - `AllMemberWorldFacts(ctx, memberSchema, bankID, limit)`（重建）
  - `MissingSourceUnits(ctx, memberSchema, key)`（反连接）
  - `MemberBankTouched(ctx, memberSchema, bankID, since)`（D1 粗筛：
    `banks.last_write_at > since`，`banks` 行不存在返回 `found=false`）
  - `ReplaceTeamFacts(ctx, key, facts, sources, cursors)`（单事务：按
    `merge_key` 增删、追加账本、推进游标、写 `last_merged_at`）
  - `FailClosedTeam(ctx, key, reason)`（清团队事实与失去来源的观察、清状态账本、
    写 `last_error`）
  - `PurgeProjectState(ctx, key)` / `DeleteProjectMemberState(ctx, ...)`
  - 两个后端都实现；内存后端只实现够单测用的子集（用能力检测跳过其余）。
- `internal/app/project_merge.go`（新文件）：
  - `projectMerger`：一轮的执行体，按设计 §六 的 1–5 步。
  - 输入构造与输出解析是纯函数，便于单测：
    `mergePrompt(previous []string, fresh []mergeMemberFact) (messages, error)`、
    `parseMergeResult(raw string) []string`、
    `mergeDiff(previous []string, merged []string) (keep, remove, insert)`、
    `mergeKey(text) string`（`sha256(规范化文本)`，规范化=去首尾空白 + 压空白 +
    单行）。
  - 模型：`provider.Provider.Complete` 一次调用；没有模型时该 job 不注册
    （与 `RefuseWithoutReflectModel` 同思路：`provider none` 无法合并，注册了
    只会每轮写 `last_error`）。
  - 观察：团队事实替换后，向 `T::X` 入队一次 `consolidate`（复用
    `EnqueueOperation` + `ConsolidationSerializationKey`，幂等），让设计 §七
    “观察由团队 bank 自己的 consolidation 生成”落地。
- 并发与失败：拿不到锁记一次 skip 返回 nil；模型或存储失败回滚、写
  `last_error`、游标不推进；重建输入超 `ProjectMergeRebuildMaxUnits` →
  `FailClosedTeam`。
- `internal/config/maintenance.go` 增三个键
  `WENYUAN_API_PROJECT_MERGE_INTERVAL_SECONDS`（默认 60，0 关闭）、
  `WENYUAN_API_PROJECT_MERGE_BATCH_SIZE`（默认 10）、
  `WENYUAN_API_PROJECT_MERGE_REBUILD_MAX_UNITS`（默认 5000）；`AnyJobEnabled`
  计入；`internal/app/maintenance_jobs.go` 注册 `project.merge`；
  `NewMaintenanceLoop` 多一个 `merge store.MergeStore`（nil 不注册）；
  `background.go` 与 `cmd/worker` 传入。

怎么验证
- 单测 `go test ./internal/app -run TestMerge`：diff 三类、规范化与 `merge_key`
  稳定性、批次截断（成员 `last_merged_at NULLS FIRST` 排序）、解析失败的返回。
- PG 集成 `internal/storage/*_pgx_test.go` 或 `internal/app` 里带 DATABASE_URL
  守卫的用例（与现有 pgx 测试同风格）：
  - 三 schema 一轮：`T::X` 含两人去重后的事实，账本记全，游标推进；
  - 游标未变再跑：没有模型调用、没有写入；
  - 新事实超批次：只消费 N 条，旧内容不丢，剩余下轮；
  - 删 A 的一条来源事实：下轮重建，`T::X` 不再含它；
  - 退出项目不再消费；`purge-contribution` 后重建内容消失；
  - 两个进程同时跑：一个执行、一个跳过；
  - 超重建上限：失败关闭，读回退到个人 bank，`last_error` 有值；
  - 合并后 `T::X` 的 consolidation 生成观察。
- 配置键：`go test ./internal/config -run TestProjectMergeKeys`，再跑
  `go test ./internal/config -run TestDumpKeyInventory && python3 scripts/render_config_reference.py`
  重生成 `etc/example.wenyuan.yaml`、`.env.example`、`docs/CONFIGURATION.md`。

### S6 生命周期与租户交互

做什么

- 成员退出项目：删 `team_project_member` + 该成员 `merge_state` + 账本
  （`DeleteProjectMemberState`），已合并内容保留（S2 接口里已有方法）。
- 撤回贡献：`purge-contribution` 先移出再标记“需要重建”；重建读当前成员，
  内容随之消失。实现为 `merge_state` 加一列 `rebuild_requested boolean`（放
  0003 迁移里），或复用 `FailClosedTeam` 后由下一次合并重建——**采用前者**，
  语义更明确。
- 删 Team（软删）：读路径因 `status!='active'` 不再命中；`project.merge` 跳过。
- purge Team：`DropTenantSchema` 删团队 schema；注册表行保留；不级联删注册表。
- 删除个人 bank / 删个人记忆：下一轮粗筛命中 → 反连接命中 → 重建。
- single 模式：`maintenanceJobs` 不注册 `project.merge`；`TeamScope` 为 nil。

怎么验证
- `go test ./internal/app -run TestTeamLifecycle`（内存后端 + 假 store 的能力桩）。
- 端到端：软删 team 后成员 recall 回退个人 bank；purge 后团队行仍在注册表。

### S7 前端控制台（Team 页面）

做什么

- `frontend/app/api/admin/teams/**`（BFF，透传到管理面，沿用现有
  `proxy` 与鉴权写法）：teams 列表/创建/详情、members、projects、
  project members、purge-contribution。
- `frontend/app/[locale]/teams/`：列表、创建、详情（成员 Tab、项目 Tab）。
  `frontend/components/team-*`、`frontend/lib/admin-api.ts` 增类型与调用、
  `console-nav.tsx` 增入口。
- 页面形态对齐现有 `tenants-view.tsx` / `tenant-detail-view.tsx`，不引入新依赖。

怎么验证
- `cd frontend && pnpm build`（或现有脚本里的前端构建步骤）。
- 手工路径：建 Team → 加成员 → 建项目 → 加项目成员 → 同成员二次加同名项目
  得到 409 提示。

### S8 文档与收尾

做什么

- `docs/plans/` 的两份设计文档保留；本计划完成后在文末追加“实施记要”小节，
  记偏差（D1–D3 的最终落地形态）。
- `AGENTS.md` 的 “The admin tenant plane is our addition” 段补 Team 面（新路由、
  只在 multi 模式、团队表只在 `admin`）。
- `docs/ROADMAP.md` 勾掉团队项；`docs/FUNCTIONAL-GAPS.md` 若列过团队相关条目则
  一并更新。
- 清理本轮产生的临时脚本 / 临时 schema。

怎么验证
- `make check` 全绿；`git status` 只有预期文件。

## 三、执行顺序与依赖

```
S1 ─▶ S2 ─┬─▶ S3 ─┐
          └─▶ S4 ─┼─▶ S7 ─▶ S8
                 └─▶ S5 ─▶ S6 ─┘
```

- S4 只依赖 S2 的 `ResolveProjectForMember`，先做它能让 S5 的实施建立在一个
  已验证的读路径上（失败关闭时“读回退到个人 bank”正是靠 S4 证明的）。
- S3 与 S4 可并行（不同文件：`adminapi/` vs `memoryops/` + `reporting/` +
  `app/read_scope.go`）。
- S5 是最大的一块，建议单独一个 PR；S6 紧跟；S7 不阻塞后端。
- 每一步一个可独立审查的提交，提交信息用祈使句，正文说明为什么。

## 四、风险与对策

| 风险 | 对策 |
|---|---|
| 读路径多一次 admin 查询 | 先接受（设计 §十三）；S4 里把 `ResolveProjectForMember` 做成一次查询，后续再加进程内缓存与失效 |
| 合并锁覆盖模型调用，慢模型占住项目 | 锁内加 `context.WithTimeout`（一轮上限 = `ProjectMergeInterval` 的 10 倍，下限 5min），超时即回滚并写 `last_error` |
| 重建失败关闭清空团队内容 | 监控 `last_error` 与 `T::X` unit 数骤降；`FailClosedTeam` 前后各打一条 slog 带计数 |
| `UNIQUE (tenant_id, project_name)` 让两段式 bank id 失真 | 加项目成员时就返回 409（S3），不在读路径才发现歧义 |
| 团队表误进租户 schema | S2 的边界测试固定“团队表只在 `admin`”；`SCHEMA-ALIGNMENT` 提供第二道 |

## 五、非目标（重申设计 §十二）

跨 Team 共享；共享原始对话/documents/chunks/attachments；秒级可见（写触发入队
留待后续）；按成员逐条撤回的 UI；项目/bank 改名；一个成员同时加入两个同名
项目；团队 bank 的配额、限流与成本核算。
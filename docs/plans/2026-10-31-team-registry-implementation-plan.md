# Team / Project 维护（A 部分）—— 实施计划

依据：`docs/plans/2026-10-29-team-project-shared-memory-design.md`（设计 v2）的
§三 决策 1–5、§五 数据模型、§八 生命周期、§九 管理面 API、§十 迁移。B 部分
（`project.merge` 合并、读回退、提取项目记忆）不在本计划内，见 §六。

目标只有一句：**像 Tenant 维护一样，在数据库和控制面板里能增删改查 Team、
Member、Project、Project Member 以及它们之间的关系。**

状态：**已实施（2026-10-30）**，实施记要与偏差见文末 §八。

## 一、本轮已定的方案（2026-10-30 与产品负责人确认）

| # | 决定 | 落地方式 |
|---|---|---|
| 1 | **`merge_state` / `merge_source_unit` 本轮不建** | 0002 只建三张团队表；合并状态表留到 B 部分，用 `0003_*.sql` 加。`merge_state` 缺行本就等价于“从未合并过”（设计 §五），A 部分不需要它 |
| 2 | **两个 Team 各自独立建同名项目** | 每个 Team 有自己的 `Web` 项目与自己算出的 `bank_id`（`<team>::Web`）；同一个成员可以同时加入 A 队和 B 队的 `Web`。**不加** `UNIQUE (tenant_id, project_name)`，删除设计 §五 里为决策 19 保留的那条冗余列 `project_name`，`team_project_member` 退化为纯 (team, project, member) 三元关系 |
| 3 | **删项目级联删除它的全部 bank** | `team_project` 保留 `bank_id` 一列（仍是一个项目一个 bank），但删除项目时**遍历该 Team schema 下所有以 `<team>::<name>` 开头的 bank，逐个删除**（不是只删 `bank_id` 那一行），由 `bank_id_prefix` 索引支撑；与目标 bank 的对账见 §二 |
| 4 | **建 Team 同时发一把 key** | `POST /admin/v1/teams` 的响应与 `POST /admin/v1/tenants` 同形：Team 记录 + `plaintext` + `key`。复用现有 `issueKey` 逻辑，不新增发 key 的路径 |

决定 2 的推论（写进代码注释）：B 部分“一个成员参与的两个 Team 各自合并”时，两个
团队结果互不覆盖；`A::Web` 这个个人 bank 同时是 A 队 `Web` 与 B 队 `Web` 的输入，
这是本轮接受的行为，不是缺陷。

## 二、决定 3 的对账规则（重要）

设计 §五 的前提是“一个项目一个 bank = `T::X`”。删项目要“级联删除项目下的所有
BankId”，那么必须说清楚在什么集合上删：

- **规范集合**：`team_project.bank_id`（一个项目一个）。
- **后缀集合**：Team schema 里所有形如 `<team>::<project name>` 的 bank id。这是
  用户在控制台手工建的、后缀与服务端约定同名但前缀不同的情况，以及未来一个项目
  多个 bank（如 `T::Web`、`T::Web.docs`）的落点。
- 删除前置校验 `sanity`：若**规范集合 ⊄ 后缀集合**，返回 409 并列出两个集合的差
  集，不做删除。这是防止“项目行被人改坏后删项目误伤别的 bank”的闸门；正常创建
  路径下这条校验总是通过。
- 删除动作：先删**后缀集合**的每个 bank（走 `DeleteBank`，它本来就清 bank 内容），
  再删注册表行；任何一步失败整体 400/500 返回，注册表行保留，操作可重试。
- 删除是**显式二跳**，不是隐式：`DELETE /admin/v1/teams/{id}/projects/{project_id}`
  必须带 body `{"confirm": "<project_id>", "delete_banks": true}`；“只想删注册表
  不想删数据”不支持——那会让 bank 变成孤儿（`banks.last_write_at` 之类会漂移），
  而“删项目就删 bank”是你确认的语义。
- `BankAdmin` 因此需要两个方法：

  ```go
  type BankAdmin interface {
      ListBanks(ctx context.Context, schema, prefix string) ([]string, error)
      DeleteBank(ctx context.Context, schema, bankID string) (int, error)
  }
  ```

## 三、范围与硬约束

- 表只在 `admin`；租户 schema 不新增任何对象（`SCHEMA-ALIGNMENT` 只忽略
  `schema_migrations`）。
- 管理面路由只在 operator key 下，不进 `/openapi.json`；数据面零改动。
- 不碰 `migrations/` 与 `internal/storage/embedded/`，只加
  `internal/storage/embeddedadmin/0002_team_registry.sql`。
- 只在 `tenant_mode: multi` 且管理面开启时可用；否则 Team 路由 501。
- `make check` 全绿；前端 `npm run typecheck && lint && test && build` 全绿。

## 四、工作项

### A-1 迁移与共享模型

- `internal/storage/embeddedadmin/0002_team_registry.sql`（照设计 §五，按 §一 改）：

  ```sql
  ALTER TABLE tenant ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'personal'
      CHECK (kind IN ('personal', 'team'));

  CREATE TABLE IF NOT EXISTS team_member (
      team_tenant_id text NOT NULL REFERENCES tenant(tenant_id) ON DELETE CASCADE,
      tenant_id      text NOT NULL REFERENCES tenant(tenant_id) ON DELETE CASCADE,
      role           text NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'member')),
      joined_at      timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (team_tenant_id, tenant_id)
  );
  CREATE INDEX IF NOT EXISTS team_member_tenant_idx ON team_member (tenant_id);

  CREATE TABLE IF NOT EXISTS team_project (
      team_tenant_id text NOT NULL REFERENCES tenant(tenant_id) ON DELETE CASCADE,
      project_id     text NOT NULL,
      name           text NOT NULL CHECK (name !~ ':'),
      bank_id        text NOT NULL,
      created_at     timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (team_tenant_id, project_id),
      UNIQUE (team_tenant_id, name),
      UNIQUE (team_tenant_id, bank_id)
  );

  CREATE TABLE IF NOT EXISTS team_project_member (
      team_tenant_id text NOT NULL,
      project_id     text NOT NULL,
      tenant_id      text NOT NULL,
      added_at       timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (team_tenant_id, project_id, tenant_id),
      FOREIGN KEY (team_tenant_id, project_id)
          REFERENCES team_project(team_tenant_id, project_id) ON DELETE CASCADE,
      FOREIGN KEY (team_tenant_id, tenant_id)
          REFERENCES team_member(team_tenant_id, tenant_id) ON DELETE CASCADE
  );
  CREATE INDEX IF NOT EXISTS team_project_member_tenant_idx
      ON team_project_member (tenant_id);
  ```

  与设计的逐条差异：无 `merge_state` / `merge_source_unit`；无
  `team_project_member.project_name` 与其唯一约束；`bank_id` 唯一约束限定在同一
  Team 内（`UNIQUE (team_tenant_id, bank_id)`），因为不同 Team 的 `bank_id` 前缀
  不同本就不会撞。

- `internal/tenant/teams.go`（新文件）：`KindPersonal` / `KindTeam`、
  `tenant.Record.Kind`、`MemberRole` 与 `RoleOwner` / `RoleMember`、
  `TeamMember`、`TeamProject`、`ProjectMember`、`ValidateProjectID`
  （`^[a-z][a-z0-9_]{1,62}$`）、`ValidateProjectName`
  （`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`，且不含 `:`）、
  `ProjectBankID(teamID, name) = teamID + "::" + name`。
- `tenant.Record` 加 `Kind`；`UpdateTenant` 的 `Kind` 只在**与现值相同**时接受，
  不同即 `tenant.ErrInvalidTransition`（设计 §九：“已存在的 tenant 不能改成 team”）。

验证：`go test ./internal/tenant -run 'TestValidateProject|TestProjectBankID'`
（表驱动）；`go test ./internal/storage -run TestAdminMigrationSeries`（序列
0001、0002，且在临时 schema 上能跑完）。

### A-2 注册表接口与两个后端

- `internal/storage/team_registry.go`（新文件）：

  ```go
  type TeamRegistry interface {
      // Teams
      ListTeams(ctx, TeamFilter) ([]Team, error)
      GetTeam(ctx, teamID string) (Team, error)
      UpdateTeamName(ctx, teamID, displayName string) (Team, error)
      SoftDeleteTeam(ctx, teamID string, now time.Time) (Team, error)
      // Members
      ListTeamMembers(ctx, teamID string) ([]tenant.TeamMember, error)
      AddTeamMember(ctx, teamID, tenantID string, role tenant.MemberRole) error
      RemoveTeamMember(ctx, teamID, tenantID string) error
      // Projects
      ListTeamProjects(ctx, teamID string) ([]tenant.TeamProject, error)
      GetTeamProject(ctx, teamID, projectID string) (tenant.TeamProject, error)
      CreateTeamProject(ctx, project tenant.TeamProject) error
      DeleteTeamProject(ctx, teamID, projectID string) error
      // Project members
      ListProjectMembers(ctx, teamID, projectID string) ([]tenant.ProjectMember, error)
      AddProjectMember(ctx, teamID, projectID, tenantID string) error
      RemoveProjectMember(ctx, teamID, projectID, tenantID string) error
  }
  ```

  哨兵错误只有三个：`tenant.ErrNotFound`、`tenant.ErrExists`、
  `tenant.ErrInvalidTransition`；handler 层按路由拼文案。`Team` =
  `tenant.Record` + `MemberCount` / `ProjectCount`（一条带子查询的
  `SELECT`，不做 N+1）。
- `internal/storage/memory_team_registry.go`：内存实现，构造时接收
  `*MemoryTenantRegistry`，共用同一份 tenant 映射（“成员必须是 tenant”、“Team 必
  须是 kind=team 的 tenant”在内存后端同样成立）。自己维护
  `teamMembers` / `teamProjects` / `projectMembers` 三张 map，删除时按 §三 的外键
  语义级联。
- `internal/storage/postgres_team_registry.go`：`*PostgresTenantRegistry` 的方法
  （同一 `db`、同一 `admin` schema），复用 `qualified` / `isUniqueViolation`。
  建 Team = 一次 `INSERT INTO admin.tenant (..., kind) VALUES (..., 'team')`，
  不再有“先建 personal 再改 kind”的两步。
- `TenantFilter` 加 `Kind`，`GET /admin/v1/tenants?kind=team` 可用（默认仍返回全部）。
- 边界测试：`boundary_test.go` 的表名清单加三张团队表。

验证：`go test ./internal/storage -run TestTeamRegistry`——一张表驱动套件，两个后
端各跑一遍；`make pgx`。

### A-3 管理面路由

- `internal/adminapi/team_config.go`（新文件）：`Config.Teams`（nil → 整组 501）
  与 `Config.BankAdmin`（只在删项目时用，nil → 删项目 501，其余路由不受影响）。
- `internal/adminapi/teams.go`（新文件）：设计 §九 的 15 条路由，按 §一 调整。

  | 方法 | 路径 | 行为 |
  |---|---|---|
  | GET | `/admin/v1/teams` | 列 `kind=team` 的 Team，支持 `q`/`limit`/`offset` |
  | POST | `/admin/v1/teams` | 建 Team：注册表建 `kind=team`/`provisioning` → `Provisioner(adopt=false)` 建 schema 并跑参考迁移 → 置 `active` → **发一把 key**；响应 `{...team, plaintext, key}` |
  | GET | `/admin/v1/teams/{id}` | Team 详情（成员数、项目数）；非 team 的 tenant → 404 |
  | PATCH | `/admin/v1/teams/{id}` | 只接受 `display_name`，别的字段 400 点名 |
  | DELETE | `/admin/v1/teams/{id}` | 软删（`status=deleted`）；不删 schema、不删 bank |
  | GET | `/admin/v1/teams/{id}/members` | 列成员 |
  | PUT | `/admin/v1/teams/{id}/members/{tenant_id}` | body `{role}`（默认 member）；目标 tenant 必须存在且 `active`（否则 409）；幂等（重复 PUT 改 role） |
  | DELETE | `/admin/v1/teams/{id}/members/{tenant_id}` | 移出 Team；级联清该成员在本队各项目里的关系 |
  | GET | `/admin/v1/teams/{id}/projects` | 列项目 |
  | POST | `/admin/v1/teams/{id}/projects` | `{project_id, name, adopt_bank?}`：校验 §A-1 规则 → 算 `bank_id` → `adopt_bank?` 才接管已存在的 bank，否则已存在即 409 → 插注册表行；插行失败删 bank 补偿 |
  | DELETE | `/admin/v1/teams/{id}/projects/{project_id}` | **必须** body `{confirm, delete_banks:true}`；按 §二 对账后级联删 bank 与注册表行 |
  | GET | `/admin/v1/teams/{id}/projects/{project_id}/members` | 列项目成员 |
  | PUT | `.../projects/{project_id}/members/{tenant_id}` | 前置“已是 Team 成员”；加入本身不建 bank |
  | DELETE | `.../projects/{project_id}/members/{tenant_id}` | 移出项目 |
  | POST | `.../projects/{project_id}/purge-contribution` | 本轮只做“移出项目”，响应 `rebuild:false`（B 部分接重建） |

- 复用现有 `issueKey` 的发 key 逻辑（抽出一个不依赖 `PathValue("id")` 的内部函数，
  由建 Team 与建 Key 两处调用）。
- `writeRegistryError` 增加 `tenant.ErrKindMismatch` 的映射（409/404 按路由决定）。

验证：`go test ./internal/adminapi -run TestTeamRoutes`（每条一个正例 + 反例：
非 active 成员 409、非 team 的 tenant 404、非法 name 400、删项目缺 confirm 400、
删项目对账失败 409），用内存注册表 + 假 `BankAdmin`；
`TestTeamsNotConfigured`（`Teams=nil` → 501）。

### A-4 接线

- `internal/app/team_registry.go`：`newTeamRegistry(...)`，有库用 postgres 实现，
  否则用与 `MemoryTenantRegistry` 共享实例的内存实现。
- `internal/app/bank_admin.go`：`BankAdmin` 实现，持 `AdminDB()` 句柄：
  - `ListBanks(schema, prefix)`：`SELECT bank_id FROM <schema>.banks WHERE
    left(bank_id, length($1)) = $1 ORDER BY bank_id`（用 `LIKE` + `ESCAPE` 也行，
    但 `left()` 不受 `%`/`_` 通配影响，`name` 允许 `._` 时必须用它）；
  - `DeleteBank(schema, bankID)`：`WithSchema(schema)` 后调现有 `DeleteBank`。
- `internal/app/admin_plane.go`：`newAdminHandler` 多接 `teams` / `banks` 两个参数；
  single 模式传 nil。
- `internal/app/app.go`：multi 模式打开团队注册表，管理面开启时注入。

验证：新增 `internal/app` 测试——经 served handler 走“建 Team（拿 key）→ 加成员 →
建项目 → 加项目成员”，并断言数据面 `GET /v1/default/banks` 行为不变；用假
`BankAdmin` 断言删项目时**后缀集合**被完整删除。

### A-5 控制台

- BFF：`frontend/app/api/admin/teams/**`（`serializeUpstream`、`tenantScoped:false`）。
- `frontend/lib/admin-api.ts` 增 Team / TeamMember / TeamProject / ProjectMember
  类型与 14 个方法。
- 页面：`frontend/app/[locale]/teams/`（列表、`new`、`[teamId]` 详情，详情内两个
  Tab：成员、项目；项目展开显示项目成员）。组件拆成 `components/teams-view.tsx`、
  `components/team-detail-view.tsx`，都 ≤500 行。
- 建 Team 成功后展示一次性 `plaintext` key（复用 tenant 详情的既有展示组件）。
- 删项目弹二次确认：输入项目 id 才能提交，文案写明“会删除该项目下的全部 bank”。
- `console-nav.tsx` 增 “Teams”；`messages/<locale>.json` 增 `teams` 命名空间，
  10 个语种键结构一致（英文、简中为基准，其余简译）。
- 不引入新依赖。

验证：`cd frontend && npm run typecheck && lint && test && build`；新增
`frontend/test/team-admin-routes.test.ts` 与
`frontend/tests/components/teams-view.test.tsx`；`messages-coverage` 与 `i18n`
测试通过。

### A-6 文档与收尾

- `AGENTS.md` 的 “The admin tenant plane is our addition” 段补 Team 面。
- 本文件文末追加“实施记要”（实际偏差）。
- 清理临时 schema / 临时脚本。
- `make check` 全绿，`git status` 只有预期文件。

## 五、执行顺序

```
A-1 ─▶ A-2 ─▶ A-3 ─┬─▶ A-4 ─▶ A-6
                   └─▶ A-5
```

建议提交：`add the team registry tables and models` → `add the team registry
store` → `serve the team management routes` → `wire the team plane` →
`add the team console pages`。

## 六、明确不做（B 部分，下次再说）

- `merge_state` / `merge_source_unit` 两张表（`0003_*.sql`，B 部分再加）。
- `project.merge` 维护任务与三个 `WENYUAN_API_PROJECT_MERGE_*` 配置键。
- recall / `GET .../memories` 的团队读回退与 `bank_id` 改写。
- `PurgeContribution` 的重建语义（本轮只移出）。
- 观察生成、可见性窗口、retain 触发的合并入队。

B 部分实施时**不得回头改 0002**；表结构的任何追加都走新迁移。

## 七、风险

| 风险 | 对策 |
|---|---|
| 删项目按前缀删 bank 误伤 | §二 的对账闸门：规范集合必须 ⊂ 后缀集合，否则 409 不动手 |
| 建 bank 与建注册表行非同一事务 | 先插行再建 bank，建 bank 失败删行补偿；错误响应说明部分完成 |
| Team 与控制台 key 混用 | Team 的 key 就是该 tenant 的普通 key，权限与 tenant 一致；本轮不加 team 专属 key 类型 |
| 团队表误进租户 schema | A-2 边界测试 + `SCHEMA-ALIGNMENT` |
| 10 语种补齐成本 | 只要求键结构一致，非基准语种简译；结构由测试守住 |
## 八、实施记要（2026-10-30）

按 §四 的 A-1…A-6 全部落地。与计划的差异只有一处，另有三条实施中做出的决定：

| # | 计划 | 实际 | 原因 |
|---|---|---|---|
| 1 | `BankAdmin` 只有 `ListBanks` / `DeleteBank` | 去掉 `CreateBank`：建项目只写注册表，bank 在首次写入时由既有惰性建行产生；另加 `POST .../adopt-bank` 报告 bank 是否存在 | 计划 A-3 里“建 bank 失败回滚”的事务边界会跨两套存储；惰性建行没有这个问题 |
| 2 | 团队 key 未明确 | 增加 `GET/POST /admin/v1/teams/{id}/keys`、`DELETE .../keys/{keyId}`，复用租户 key 的同一批 handler，前面加一道 `teamOnly` 校验 | 一个团队的 key 就是它的租户 key；e2e 验证时发现只有 `/tenants/{id}/keys` 能发，团队路径下 404，补上 |
| 3 | — | `decodeJSON` 拒绝 JSON 值之后的多余数据 | 计划测试里的“畸形 body”用 `{}{}` 试出来，原来会静默丢弃第二个对象 |
| 4 | — | `vitest.config.ts` 固定 `NODE_ENV=test` | 本机 `NODE_ENV=production` 时 React 19 不导出 `act`，全部 41 个既有组件测试本来就是红的；这是环境缺陷，不是本次引入 |

**删除项目的前缀闸门**最终形态（比 §二 更严）：先按 `bank_id` 前缀列出该团队 schema 里的 bank，若项目的规范 bank **不在**其中就 409 且不删任何东西；否则按 `left(bank_id, length($1)) = $1` 逐个小删。`left()` 而非 `LIKE`，因为项目名允许 `.` 与 `_`。

**验证过的内容**（除单测与 `make check` 之外）：

- `make test-pg` 全绿（`WENYUAN_TEST_DATABASE_URL` 指向 compose 里的 PostgreSQL）。
- 起了一个独立的多租户实例（API 28888 / admin 28890 / 控制台 29999）走完整链路：
  建两个 tenant → 建 Team（响应里带 `wyk_` key）→ 该 key 打通数据面 200 → 加两个成员 →
  建项目（`acme::Web`）→ 项目成员 → 非成员 409 → retain 写入 `e2eteam::Web` →
  删项目（`confirm` + `delete_banks`）→ `banks` 与 `memory_units` 都归零。
- 控制台：以 operator key 登录后 `/api/admin/teams` 200、建项目 201，`/teams`、
  `/teams/new`、`/teams/{id}` 三个页面都服务端渲染 200。探针用的一次性 schema、
  registry 行与临时二进制已全部清理。

**仍然没做**（B 部分）：`merge_state` / `merge_source_unit`、`project.merge`、
读回退、`purge-contribution` 的重建——`purge-contribution` 现在只移出成员并在响应里
如实写 `rebuild: false`。这些的详细计划见
`docs/plans/2026-10-31-team-merge-implementation-plan.md`。

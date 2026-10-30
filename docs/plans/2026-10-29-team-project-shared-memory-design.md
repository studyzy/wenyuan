# Team / Project 共享记忆 —— 设计方案

> **§六、§七、§八 已被 `docs/plans/2026-10-30-team-memory-design-v3.md` 取代。**
> 2026-10-30 评审发现三处机制错误：合并账本无法回溯来源、删除检测的版本粗筛会漏判、
> 合并锁的事务边界与本仓库的 advisory-lock 用法冲突。v3 只改这三处机制，其余决策
> （§三 的 19 条、分期、产品范围）保持不变。**实现以 v3 为准。**

状态：2026-10-29 初版与产品负责人确认；2026-10-30 按同日架构评审与
`docs/plans/2026-10-29-multi-tenant-design.md` 的 P5 修订为 v2；2026-10-30 晚
§六/§七/§八 由 v3 取代。
范围：这是 wenyuan 自己的能力，不属于参考实现的兼容面。数据面的路由、JSON
字段、状态码、请求头一律不改；团队管理只出现在管理面。

## 一、修订摘要（v1 → v2）

| 主题 | v1 | v2 | 原因 |
|---|---|---|---|
| 管理表位置 | Team schema 持有 `project` / `member` / `project_member` | 全部落 `admin` | 租户 schema 必须与参考 schema 逐对象一致；`SCHEMA-ALIGNMENT` 只排除 `schema_migrations`，在 Team schema 建表会变成 extra |
| 合并粒度 | 按来源分片、每片独立重生成 | 项目级一次合并、整体替换 | “每片独立”与“跨成员去重”互斥；只取水位后 N 条又会覆盖上一轮结果 |
| 变更检测 | `Bank.LastWriteAt` 水位 | `(updated_at, id)` 游标；`LastWriteAt` 只做粗筛 | consolidation 也会推进 `LastWriteAt`，它不能当精确水位 |
| 删除传播 | 假设成员只追加、不删除 | `merge_source_unit` 账本 + 反连接 + 重建 | `DELETE /memories`、删 bank、保留期清理都会删行，团队内容不能永久保留 |
| 并发 | 假设两个进程产出相同 | 每个 project 抢 advisory lock | 维护循环没有 leader election，server 与 worker 都会跑 |
| 分片标记 | `source:<tenant_id>` tag | 账本表，不用 tag | tag 会进入 `ListTags` 与 fuzzy 解析词汇 |
| 观察的来源 | 成员观察直接合入 | 团队 bank 自己 consolidation 生成 | observation id 会随成员 consolidation 重建，直接合入会让账本每轮都判定“被删” |
| 读回退 | 按 query 回退 | 按 bank 回退，并回查成员资格 | 按 query 回退会让同一成员不同问题看到不同来源；bank id 不能反推团队 |
| 迁移 | 开放项 | `admin` 自有迁移序列 | 与租户注册表同一机制，参考迁移生成器不会覆盖 |
| 管理面 | 未定义 | `admin` 路由管理 Team / Member / Project | 需要“设置 Team 的 Member”的入口 |

评审项的处理：

| 评审项 | v2 的处理 |
|---|---|
| B1 多 schema 基座不存在 | 多租户基座已落地：按凭证选 schema、`WithSchema` / `schemaFor` 失败关闭、worker 枚举租户、`admin` 自有序列。团队方案现在是它的上层 |
| B2 团队发现路径缺失 | `admin.team_project_member` 是唯一发现入口；读时回查 `tenant` 状态与成员资格，绝不从 bank id 反推 |
| B3 写后读不一致 | 读路径按 bank 回退；可见性窗口由 `ProjectMergeInterval` 决定，默认 60s，写入触发入队列为后续项 |
| B4 回退语义未定义 | “团队 bank 为空”定义为**该 bank 没有任何 unit**，不是本次 query 无命中；个人 bank 不存在但团队有内容时仍读团队 |
| C1 水位与删除 | `(updated_at, id)` 游标 + `merge_source_unit` 账本；删除命中即重建 |
| C2 批次丢数据、跨成员去重矛盾 | 合并输入始终包含团队上一版内容；去重是项目级的 |
| C3 并发重生成 | 每个 project 一轮只允许一个进程，`pg_try_advisory_lock` |
| C4 tag 污染词汇 | 不再写 `source:` tag，provenance 只在账本里 |

## 二、问题

一次 agent 会话在该用户的 tenant schema 里写个人 bank（`A::X`）。同一项目的
两名工程师无法分享彼此已经学到的东西：A 的世界事实对 B 不可见，而唯一的共享
途径是原始对话，那恰恰不允许共享。

想要的是：A 和 B 持续为项目 X 贡献，关于 X 的**提炼后**事实变成共享的，同时
双方永远看不到对方的对话历史、documents 或 chunks。

## 三、决策

| # | 决策 |
|---|---|
| 1 | Team 就是一个 tenant：独立 PG schema，`tenant.kind = 'team'`。A、B、T 各自是 tenant。 |
| 2 | 用户 bank 与团队 bank 位于不同 schema，各自寻址。 |
| 3 | 项目的目标 bank 是 **Team schema 自己的 bank**：`T::X`；成员的个人 bank 是 `A::X`、`B::X`。 |
| 4 | bank id 统一为 `<TenantName>::<ProjectName>`，前缀就是 tenant_id（等于 schema 名），其余部分是项目名。bank id 只用于寻址，**不用于反推团队**。 |
| 5 | 一个项目在每个 tenant 下一个 bank。用户同时做 X 和 Y 就有 `A::X`、`A::Y`；一个 bank 只放该项目的材料。 |
| 6 | 合并是 **LLM 合并，不是重新抽取**。输入是成员已经抽取好的世界事实（`fact_type='world'`），原文从不进入输入。 |
| 7 | 合并是**项目级**的：一次模型调用看到该项目所有成员的新事实，因此可以跨成员去重。v1 的“按来源分片、独立重生成”不再成立。 |
| 8 | 团队 bank 的观察不直接合并：由既有 consolidation 在 `T::X` 上从团队事实生成。这样 observation id 不会每轮被成员侧的 consolidation 重建。 |
| 9 | 合并结果按内容 hash 增量保留：内容不变的事实保留原行（id 不变），只删真正消失的、插真正新增的。 |
| 10 | provenance 记在 `admin.merge_source_unit`，不写 tag。 |
| 11 | 变更检测用 `(updated_at, id)` 双键游标；`LastWriteAt` 只作为“这一轮值不值得查”的粗筛，绝不作为游标。 |
| 12 | 删除或撤回靠账本反连接检测：账本里的某个来源 unit 不存在了，就重建该项目；重建失败时失败关闭（清空团队内容），不让已删数据继续可见。 |
| 13 | 由现有 maintenance 循环调度，注册为 `project.merge`；没有 leader election，靠每个 project 的 advisory lock 去重。 |
| 14 | 批次只限制**本轮新输入**的条数，不限制团队上一版内容；上一版始终在输入里。 |
| 15 | 读取是服务端完成的两级回退：团队 bank 有任何 unit 就只读它；为空才回退到成员自己的 bank。原始经历（documents、chunks）只读个人 bank。 |
| 16 | 回退实现在 storage 层内部，排序、去重、分页保持现有行为；返回给客户端的 `bank_id` 仍是请求里的个人 bank id。 |
| 17 | 所有团队表都放 `admin`；租户 schema 不新增任何对象。 |
| 18 | Team / Member / Project 的增删改只在管理面；数据面不新增路由。 |
| 19 | 一个成员不能同时属于两个同名项目（`A::X` 只能指向一个团队 bank）；由注册表唯一约束保证。 |

## 四、布局

```
Schema A (成员)            Schema B (成员)            Schema T (团队)
  bank: A::X                 bank: B::X                 bank: T::X   <- 合并目标
  documents/chunks           documents/chunks           memory_units (合并事实)
                                                        (只有参考 schema 的表)

admin (本服务自有 schema)
  tenant(kind = personal | team)
  team_member
  team_project
  team_project_member
  merge_state
  merge_source_unit
```

`A::X`、`B::X`、`T::X` 在三个不同 schema 里各指一个 bank，永远不会碰撞：
bank 总是由 schema 加 id 定位，成员的凭证只选中成员的 schema。

Team schema 里没有任何本服务的表。这是硬约束：参考 schema 的逐对象对齐
（`docs/SCHEMA-ALIGNMENT.md`）只忽略 `schema_migrations`，多一张表就是一条 extra。

## 五、admin 数据模型

新增两个迁移文件，走 `internal/storage/embeddedadmin/` 的既有序列，不碰参考迁移。

```sql
-- 0002_team_registry.sql

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
    name           text NOT NULL,
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
    project_name   text NOT NULL,
    added_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_tenant_id, project_id, tenant_id),
    FOREIGN KEY (team_tenant_id, project_id)
        REFERENCES team_project(team_tenant_id, project_id) ON DELETE CASCADE,
    FOREIGN KEY (team_tenant_id, tenant_id)
        REFERENCES team_member(team_tenant_id, tenant_id) ON DELETE CASCADE,
    UNIQUE (tenant_id, project_name)
);
CREATE INDEX IF NOT EXISTS team_project_member_project_idx
    ON team_project_member (team_tenant_id, project_id);
```

```sql
-- 0003_team_merge.sql

CREATE TABLE IF NOT EXISTS merge_state (
    team_tenant_id    text NOT NULL,
    project_id        text NOT NULL,
    tenant_id         text NOT NULL,
    cursor_updated_at timestamptz NOT NULL DEFAULT '-infinity',
    cursor_unit_id    text NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
    last_merged_at    timestamptz,
    last_error        text,
    PRIMARY KEY (team_tenant_id, project_id, tenant_id),
    FOREIGN KEY (team_tenant_id, project_id)
        REFERENCES team_project(team_tenant_id, project_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS merge_source_unit (
    team_tenant_id  text NOT NULL,
    project_id      text NOT NULL,
    tenant_id       text NOT NULL,
    source_unit_id  text NOT NULL,
    consumed_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_tenant_id, project_id, tenant_id, source_unit_id),
    FOREIGN KEY (team_tenant_id, project_id)
        REFERENCES team_project(team_tenant_id, project_id) ON DELETE CASCADE
);
```

规则：

- `tenant.kind` 让“Team 是一个 tenant”落地，不新增 `team` 表；已有行默认
  `personal`，不需要回填。
- `project_id` 是稳定 slug（`^[a-z][a-z0-9_]{1,62}$`），`name` 是 bank id
  的后缀（`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`）。`name` 创建后不可改；改名等于
  新建项目。`bank_id` 在创建时算好并存下，避免读路径重新拼接。
- `team_project_member.project_name` 是 `team_project.name` 的冗余副本，只为
  支撑 `UNIQUE (tenant_id, project_name)`：一个人的 `A::X` 只能指向一个团队
  bank。这条限制是两段式 bank id 的代价，管理面在第二次加入同名项目时返回 409。
- `merge_state` 缺行等价于“从未合并过”，游标在无限远的过去。
- `merge_source_unit` 是“这一版团队内容由哪些来源事实支撑”的集合；增量轮只
  增不减，重建时整体替换。成员离开项目时显式清理，不做外键级联。
- 项目相关的表都以 `team_project` 为根做级联（`team_project_member`、
  `merge_state`、`merge_source_unit`）；删项目会带走状态与账本，但团队 bank
  本身由管理面显式删除，避免一次误删项目就丢掉整库。

## 六、合并任务

### 调度与配置

在 `maintenanceJobs`（`internal/app/maintenance_jobs.go`）注册 `project.merge`，
由 `MaintenanceConfig`（`internal/config/maintenance.go`）的三个键控制：

```go
ProjectMergeInterval       time.Duration // 为零即关闭，默认 60s
ProjectMergeBatchSize      int           // 默认 10
ProjectMergeRebuildMaxUnits int          // 默认 5000
```

一次 tick：

1. 从 `admin` 读所有 `tenant.kind='team'` 且 `status='active'` 的项目及其成员；
   只有 `team_project_member` 里有行的项目才参与。
2. 对每个 project 尝试 `pg_try_advisory_lock(hashtext('wenyuan:project-merge:<team>:<project>'))`。
   拿不到就跳过，这一轮由另一个进程负责。锁在整轮（含模型调用）持有，结束时释放。
3. 对该 project 的每个成员，先做删除检测（见下），再做新事实检测。
4. 没有删除、也没有新事实：不调用模型，记录心跳后结束。
5. 有变化：构造输入、调用模型、在一个事务里替换团队事实并推进游标。

### 输入

每个成员的个人 schema：

```sql
SELECT id::text, text, updated_at
FROM <member_schema>.memory_units
WHERE bank_id = $1
  AND fact_type = 'world'
  AND (updated_at, id) > ($2::timestamptz, $3::uuid)
ORDER BY updated_at, id
LIMIT $4
```

- 只取 `world`。`experience` 是原文经历，永不共享；`observation` 的 id 会随
  成员侧 consolidation 重建，不能当稳定来源（决策 8）。
- 成员按 `last_merged_at NULLS FIRST` 排序，先处理最久没合并的人，避免某个成员
  每轮都写、其他人永远轮不到。
- 全项目本轮新输入合计不超过 `ProjectMergeBatchSize`。剩余的下轮再取；游标只
  推进到实际消费的位置。
- 团队上一版事实始终作为输入的一部分（按内容 hash 去重后），因此批次只影响
  “新增”，不会丢旧内容。

### 输出与写入

模型返回一组事实文本。写入前做规范化与精确去重，按 `sha256(规范化文本)` 生成
`merge_key`：

- 团队 bank 里已有同 `merge_key` 的行原样保留（id 不变，观察不受影响）；
- 团队 bank 里有、这次结果里没有的行删除，并清掉因此失去全部来源的观察；
- 结果里有、团队 bank 里没有的行插入，`fact_type='world'`，`document_id=NULL`，
  `metadata={"merge_key": ..., "merged_at": ...}`。

三件事在同一个事务里完成：事实增删、`merge_source_unit` 追加本轮消费的来源 id、
`merge_state` 推进游标与 `last_merged_at`。模型调用在事务之前，失败不改任何行。

团队 bank 由合并任务独占写入：成员的个人 key 寻址不到它；团队自己的 key 可以
读，若有人直接往里写，下一轮合并会按“结果里没有”把它删掉。`adopt_bank` 是唯一
的接管入口。

### 删除与重建

每次 tick 对每个 `(project, member)` 做一次反连接：

```sql
SELECT 1
FROM admin.merge_source_unit l
LEFT JOIN <member_schema>.memory_units m ON m.id = l.source_unit_id::uuid
WHERE l.team_tenant_id = $1 AND l.project_id = $2 AND l.tenant_id = $3
  AND m.id IS NULL
LIMIT 1
```

命中说明有来源事实被删（`DELETE /memories`、删 bank、保留期清理都算）。此时该
项目进入重建：

1. 忽略游标，读该项目所有成员当前的全部 `world` 事实（仍按成员分页，总数受
   `ProjectMergeRebuildMaxUnits` 限制）。
2. 用与增量相同的模型合并规则重算整版内容，整体替换团队事实与账本。
3. 重建成功后把各成员游标推进到本轮实际读到的最后一条 `(updated_at, id)`；
   失败则不推进，下一轮重试。

重建输入超过上限时**失败关闭**：删除该项目在 `T::X` 的全部事实与因此失去来源
的观察，清空该项目的 `merge_state` 与账本，记录 `last_error`，让读取回退到成员
自己的 bank。已删数据不能因为重建做不完而继续可见；运维抬上限或拆分项目后再恢复。

### 并发

- 每个 project 一把 advisory lock，锁覆盖“读输入 → 模型 → 替换”整轮。
- 维护循环在 server 和 worker 都会启动（`internal/app/background.go`、
  `cmd/worker`），没有 leader election；锁是唯一的互斥手段。
- 锁被占用不是错误，记一次跳过即可。

### 失败

- 模型或存储失败：事务回滚，游标不推进，写 `last_error`，下一轮重试。
- 单个成员的个人 bank 不存在：跳过该成员，不影响其他成员；不自动建 bank。
- 团队 bank 不存在：在该 project 第一次合并时用 `T::X` 幂等创建。项目创建时
  如果 `T::X` 已经存在，管理面要求显式 `adopt_bank` 才接管，否则返回 409——
  已有的 bank 可能是人写的，不能被一次合并静默覆盖。

## 七、读取路径

### 解析

对一次 bank-scoped 读请求（recall、按 bank 列 memories、按 id 取 unit）：

1. 把 bank id 按第一个 `::` 拆成 `prefix` 与 `name`。`prefix` 必须等于当前
   请求的 tenant id（也就是当前 schema），否则不做团队回退。
2. 查 `admin`：

   ```sql
   SELECT p.team_tenant_id, p.project_id, p.bank_id
   FROM admin.team_project_member m
   JOIN admin.team_project p
     ON p.team_tenant_id = m.team_tenant_id AND p.project_id = m.project_id
   JOIN admin.tenant t ON t.tenant_id = p.team_tenant_id
   WHERE m.tenant_id = $1 AND m.project_name = $2 AND t.status = 'active'
   ```

   没有行：只读个人 bank，行为与今天完全一致。
3. 有行：先读团队 bank `p.bank_id`，schema 为 `p.team_tenant_id`。这是
   **回查成员资格**，不是信任 bank id：bank id 可以伪造，注册表不能。

### 主源与回退

- `T::X` 有任何 unit（`fact_type` 不限）就是主源，本次读只查它。
- `T::X` 不存在或没有任何 unit：回退到个人 bank `A::X`，行为与今天一致。
- 原始经历（documents、chunks、attachments）从不查团队 bank：它们只存在于成员
  自己的 schema。
- 回退判定是**按 bank**的，不是按 query：团队有内容但本次 query 没命中，返回空，
  不再去个人 bank 补，避免同一成员不同问题看到不同来源。团队 observation 在
  该 bank 的 consolidation 完成前可能为空，这是稳定视图的代价。
- 成员个人 bank 不存在但团队 bank 有内容时，仍然返回团队内容；这是新成员
  onboarding 的路径（`A::X` 还没被 retain 创建）。

### 返回形状

- 团队行的 `bank_id` 在响应里改写为请求里的个人 bank id；客户端看到的仍是
  `A::X`，不需要知道 `T::X`。
- unit id 是团队 bank 的真实 id。按 id 读取时，个人 schema 未命中就在该成员
  所属项目的团队 bank 里只读解析；写、删、改、retag 仍只在个人 schema 进行，
  团队行对成员只读。
- 排序、去重、分页、tag 过滤、rerank 都沿用现有 pipeline；回退只改变“在哪个
  schema 查”，不改变结果组装。

### 可见性窗口

成员新写的事实要等下一轮 `project.merge` 才进入 `T::X`。窗口上界是
`ProjectMergeInterval` 加一次模型调用（默认 60s 量级），期间该成员自己 recall
也看不到这条新事实。这是 v2 接受的产品折中：

- 团队视图因此是稳定的，不会因为一次新写入而抖动；
- 需要秒级可见时，再加“retain 成功后幂等 enqueue 一次 `project.merge`”的路径，
  它需要 worker 新增一个 operation 类型；v1 不做，避免动数据面可见的
  `operation_type` 取值。

## 八、生命周期

| 事件 | 处理 |
|---|---|
| 成员加入 Team | 插 `team_member`；加入项目再插 `team_project_member`。不建个人 bank。 |
| 成员退出项目 | 从 `team_project_member` 删除；删除该成员的 `merge_state` 与账本；**已合并的内容保留**（它在成员期间已经共享），此后其来源删除不再触发重建。需要撤回时走下一行。 |
| 撤回某成员的贡献 | 管理面显式操作：先把该成员移出项目，再删除其状态与账本并重建；重建只读当前成员，内容随之消失。 |
| 删除个人记忆 | 下一轮反连接命中，项目重建，团队内容不再包含它。 |
| 删除个人 bank | 同上：该成员的所有来源消失，项目重建。 |
| 删除项目 | 删 `team_project`（级联 `team_project_member` / `merge_state` / 账本）；团队 bank `T::X` 由管理面显式删除，需二次确认。 |
| 删除 Team | 租户软删（`status='deleted'`）：读路径因 `status != 'active'` 不再返回团队内容，数据保留。 |
| purge Team | 与租户 purge 相同：`DropTenantSchema` 删团队 schema；注册表行保留（软删），团队行不级联；读路径因 `status != 'active'` 不再使用它。 |

删除语义的底线：成员删掉自己的事实后，团队视图不能继续显示它。重建失败时的
失败关闭（清空团队内容）就是为了守住这条底线。

## 九、管理面 API

只在管理面（operator key），不进 `/openapi.json`，不改数据面。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/v1/teams` | 列 Team（`tenant.kind='team'`） |
| POST | `/admin/v1/teams` | 建 Team：建 tenant + 独立 schema + 迁移，返回 tenant 与一把 key |
| GET | `/admin/v1/teams/{id}` | Team 详情（含成员、项目计数） |
| PATCH | `/admin/v1/teams/{id}` | 改展示名 |
| DELETE | `/admin/v1/teams/{id}` | 软删 Team |
| GET | `/admin/v1/teams/{id}/members` | 列成员 |
| PUT | `/admin/v1/teams/{id}/members/{tenant_id}` | 加成员（幂等，body `{role}`） |
| DELETE | `/admin/v1/teams/{id}/members/{tenant_id}` | 移出 Team |
| GET | `/admin/v1/teams/{id}/projects` | 列项目 |
| POST | `/admin/v1/teams/{id}/projects` | 建项目：`{project_id, name, adopt_bank?}`，建 `T::X` |
| DELETE | `/admin/v1/teams/{id}/projects/{project_id}` | 删项目（二次确认删 bank） |
| GET | `/admin/v1/teams/{id}/projects/{project_id}/members` | 列项目成员 |
| PUT | `/admin/v1/teams/{id}/projects/{project_id}/members/{tenant_id}` | 加项目成员 |
| DELETE | `/admin/v1/teams/{id}/projects/{project_id}/members/{tenant_id}` | 移出项目 |
| POST | `/admin/v1/teams/{id}/projects/{project_id}/purge-contribution` | 移出某成员并重建，撤回其贡献 |

规则：

- 建 Team 复用租户 provisioning（建 schema、跑参考迁移），并要求 `kind='team'`；
  已存在的 tenant 不能直接改成 team。
- 加成员要求目标 tenant 存在且 `status='active'`；加项目成员要求已经是 Team 成员。
- 同一成员加第二个同名项目返回 409（决策 19）。
- 建项目时 `name` 不可重复；已存在的 `T::X` 需要 `adopt_bank: true` 才接管。
- 管理路由复用 `internal/adminapi` 的 operator 鉴权与错误信封。

## 十、迁移

- 两个新文件进 `internal/storage/embeddedadmin/`，由 `MigrateAdminSchema` 应用。
  它与参考迁移序列物理隔离，`scripts/reference_schema_migrations.py` 重新生成时
  不会碰它。
- 租户 schema 不新增任何对象；`ProvisionTenantSchema` / 参考迁移不变。
- 不改 `migrations/` 与 `internal/storage/embedded/`；schema 对齐检查保持 0 extra。
- 团队能力只在 `tenant_mode: multi` 下可用。single 模式不打开注册表、不迁移
  `admin`，也不注册 `project.merge`；行为与今天一致。

## 十一、测试

单测（不需要数据库）：

- bank id 解析：`A::X`、无 `::` 时不回退、前缀不等于当前 tenant 时不回退；
- `name` / `project_id` 校验；
- 按 `merge_key` 做事实 diff：保留、删除、插入三类；
- 成员排序（`last_merged_at NULLS FIRST`）与批次截断；
- 读回退选择：团队有 unit、团队为空、团队不存在、个人 bank 不存在。

PostgreSQL 集成（必须实际执行）：

- 建三个 schema（A、B、T），跑一轮：`T::X` 同时有两人的去重事实，账本记全，
  两边的游标都推进；
- 游标未变时再跑：没有模型调用，没有任何写入；
- 新事实超过批次：只消费 `ProjectMergeBatchSize` 条，剩余下轮继续，旧内容不丢；
- 删除 A 的一条来源事实：下一轮触发重建，`T::X` 不再含该事实，观察同步消失；
- 成员退出项目：不再消费其新事实；撤回贡献后重建，内容消失；
- 两个进程同时跑同一 project：只有一个执行，另一个跳过；
- 重建输入超过上限：团队内容被清空，读回退到个人 bank，`last_error` 有记录；
- 团队事实替换后，`T::X` 的 consolidation 生成观察；
- 按 id 读取团队 unit 成功，按 id 写/删返回 404；
- A 的凭证无法寻址 B 的个人 bank；B 的凭证无法直接读 `T::X`。

端到端（`internal/app/parity_integration_test.go`）：

- 成员 recall 命中团队内容，响应里的 `bank_id` 仍是个人 bank id；
- 团队为空时回退个人 bank，路由与 JSON 字段与今天一致；
- 管理面建 Team/项目/成员走 served handler，数据面行为不变；
- 租户 schema 的对象清单与参考 schema 完全一致（extra = 0）。

## 十二、非目标与后续

非目标：

- 跨 Team 共享；一个项目只属于一个 Team。
- 共享原始对话、documents、chunks、attachments。
- 秒级可见（见“可见性窗口”；后续可加 retain 触发的 merge 入队）。
- 按成员逐条撤回的 UI（只有显式的 purge-contribution 操作）。
- 项目/bank 改名；改名等于新建。
- 一个成员同时加入两个同名项目。
- 团队 bank 的配额、限流与成本核算。

后续项：

- 写触发 `project.merge` 入队（需要 worker 新增 operation 类型）。
- 团队合并的 prompt 质量评估与 golden set。
- 大项目的增量重建（按成员分段重建，避免一次性读全量）。
- 管理面 Team 页面（控制台）。

## 十三、风险

- **租户 schema 多一个对象就是对齐失败**：任何团队表误加进租户 schema 都会被
  `SCHEMA-ALIGNMENT` 抓到；用边界测试固定“团队表只在 `admin`”。
- **删除检测的全量反连接**：每个 project 每轮扫一遍账本。规模大时这是热点；
  先按 `ProjectMergeInterval` 调低频率，必要时改成按来源分片的轮转检查。
- **重建失败关闭会清空团队内容**：这是隐私优先的取舍；运维需要监控
  `last_error` 与团队 bank 的 unit 数骤降。
- **模型调用在锁内**：一个慢模型会占住该项目的合并；用超时与单轮时间上限兜底，
  锁被占用时下一轮重试。
- **可见性窗口**：默认 60s；对“刚写完立刻 recall”的场景是已知限制，需要产品
  接受或启用后续的入队路径。
- **同名项目唯一约束**：两段式 bank id 的直接代价；管理面必须在加成员时就报
  409，而不是等到读路径才发现歧义。
- **读路径多一次 admin 查询**：每个 bank-scoped 读都要解析成员资格。v1 先接受，
  规模变大后给 `(tenant_id, project_name)` 加进程内缓存，成员变更时失效。

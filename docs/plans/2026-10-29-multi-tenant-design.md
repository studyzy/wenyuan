# 多租户改造方案 —— Tenant 管理与请求期 schema 路由

状态：2026-10-29 与产品负责人逐节确认；同日架构评审后修订（purge 边界、adopt 空 schema、
`schemaFor` 失败语义、worker/备份/配置命名）。
范围：这是 wenyuan 自己的控制面能力，不属于参考实现的兼容面。数据面的路由、
JSON 字段、状态码、请求头一律不改。

## 问题

当前一次请求只能落到一个 schema：进程启动时由 `WENYUAN_API_DATABASE_SCHEMA` 固定。
`tenant.Extension` 虽然已经能按请求返回 Context，但默认实现固定返回同一个 schema；
唯一会消费 extension 的 `TenantAuthenticator` 目前只在测试里接线，生产组合根用的是
`TokenAuthenticator`（`internal/app/auth.go`）。于是：

- 同一部署里的两个用户共享一个 schema，彼此的 bank 互相可见；
- 后续的「团队 / 项目共享记忆」需要把成员与团队分到不同 schema，前提不成立；
- 后台维护循环虽然写着「遍历所有租户」，实际只看得到自己（`tenant.Default.ListTenants`）。

参考实现的租户能力是扩展点（`TenantExtension.authenticate / list_tenants`），
每次请求 set `_current_schema`；内置实现仍然只支持单 schema，控制台也只管 bank。
所以本方案没有可照抄的上游表结构或接口，路线由我们定。

## 已确认的决策

| # | 决策 |
|---|---|
| 1 | 控制台范围：租户 CRUD + 跨租户总览 + 进入某个租户操作它的 bank。 |
| 2 | 进程模型：单进程多 schema。一个 API 进程按凭证选 schema，不按租户拆进程。 |
| 3 | `tenant_id` 就是 schema 名：不可变 slug，创建后不可改名；展示名另存。 |
| 4 | 管理面与数据面同进程、不同端口、不同凭据；管理路由不进 `/openapi.json`。 |
| 5 | 控制台「进入租户」由管理面代签短期租户 key，数据面鉴权模型不新增凭据类型。 |
| 6 | 租户级配置存在注册表 `config jsonb`，控制台可编辑；只承载 `TenantOverrides` 认识的 bank 级开关，provider 凭据仍留在进程级（见下）。 |
| 7 | 注册表放固定 schema `admin`；operator key 只在进程 env/配置文件，绝不进库。配置文件的 key 走 `WENYUAN_API_` 命名空间，所以两个进程级新键拼作 `WENYUAN_API_ADMIN_LISTEN` / `WENYUAN_API_ADMIN_API_KEY`。 |

## 拓扑

```
                    ┌──────────── wenyuan 进程 ────────────┐
控制台 ──BFF──┬──▶ 数据面 :18888   租户 key → tenant → schema ─┐
              │     路由/JSON 与参考实现逐字节一致              │
              └──▶ 管理面 :18890   operator key → admin/聚合 ───┤
                                                               ▼
                                                     PostgreSQL
                                                     ├─ admin（本服务自己的表）
                                                     └─ <tenant> × N（参考 schema 原样）
```

- 数据面：路由、字段、状态码、请求头都不变；鉴权从「和一把静态 key 比较」改为
  「按 key hash 查注册表」，然后把租户上下文放进 request context，存储层据此选 schema。
- 管理面：独立 mux、独立 operator key；只放本服务自己的接口。
- 单租户兼容：`WENYUAN_API_TENANT_MODE=single`（默认）时仍返回
  `tenant.NewDefault(cfg.DatabaseSchema)`，行为与今天完全一致。

## admin schema

注册表 schema 固定 `admin`，由本服务的本地迁移创建。它**不是租户**：

- 不出现在 `ListTenants` 的结果里（列表直接读 `admin.tenant`，不扫 `information_schema`）；
- 不对它跑参考迁移，数据面永远不会路由到它；
- `admin` 是保留名，不能拿来建租户。

```sql
tenant(
  tenant_id     text PRIMARY KEY,   -- slug，同时是 schema 名
  display_name  text NOT NULL,
  status        text NOT NULL,      -- provisioning | active | suspended | deleted
  config        jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  deleted_at    timestamptz
)

tenant_key(
  key_id        uuid PRIMARY KEY,
  tenant_id     text NOT NULL REFERENCES tenant(tenant_id),
  key_hash      bytea NOT NULL UNIQUE,   -- sha256(明文)
  prefix        text NOT NULL,           -- 前 8 位，列表识别用
  label         text NOT NULL,
  ephemeral     boolean NOT NULL DEFAULT false,
  expires_at    timestamptz,
  revoked_at    timestamptz,
  last_used_at  timestamptz,
  created_at    timestamptz NOT NULL DEFAULT now()
)

admin_audit(
  id bigserial PRIMARY KEY,
  actor text NOT NULL,
  action text NOT NULL,
  target text NOT NULL,
  detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  at timestamptz NOT NULL DEFAULT now()
)
```

规则：

- `tenant_id` 校验 `^[a-z][a-z0-9_]{1,62}$`，保留 `admin`、`pg_*`、
  `information_schema`。**不保留 `public`**：现有单租户部署的 schema 就是
  `public`，保留它会堵死迁移路径，详见下面的 adopt。
- 状态机 `provisioning → active → suspended → deleted`。`deleted` 立即拒 key，
  schema 保留；`DELETE` 只是软删，`DROP SCHEMA` 是独立的 purge 动作，需二次确认。
- **purge 拒绝 `public` 与 `admin`。** `public` 是扩展宿主
  （`extensionSchema`，vector / pg_trgm / btree_gin 都装在那里），
  `DROP SCHEMA public CASCADE` 会连带删掉所有租户依赖的类型和操作符类，
  等于打死整个部署。adopt 自 `public` 的租户只能软删（`DELETE`：移出 `ListTenants`、
  拒 key、保留 schema），不能 purge；其他 adopt 进来的 schema 允许 purge，但同样要二次确认。
  守卫放在 storage 层（`DropSchema` 拒绝 `extensionSchema` 与 `admin`），
  管理面只负责二次确认——`extensionSchema` 是 `internal/storage` 的未导出常量
  （`internal/storage/postgres.go`），handler 拿不到它。
- key 只存 sha256，明文只在创建时返回一次；轮换按下面「管理面 API」的定义是新建 + 吊销。
- `config` 存租户级 overrides，鉴权时随上下文下发（现有
  `httpx.TenantOverrides` → `config.ResolveLayers` 管道只覆盖 bank 级开关）。
  **v1 不承载 provider 凭据**：provider 是启动时一次性构建的，没有 per-tenant
  解析路径；要支持得先加 provider seam，否则写进 config 也不会生效。
  写入时按已知 override 键做 allowlist——现成的清单是
  `internal/bankapi` 的 `configurableFields`，需要把它提到共享位置或导出，
  不要让管理面反向 import 数据面 handler；列表读取时对敏感键掩码。
- 后续团队方案的 `project` / `project_member` 也放这里（发现数据天然跨租户），
  Team schema 只留 `merge_state`、`merge_unit` 这类团队私有状态。
- 注册表仓储放在 `internal/storage`（`memory_tenant.go` / `postgres_tenant.go`，
  沿用 `memory_<area>` / `postgres_<area>` 前缀），不新建一个在 storage 之外写 SQL 的包：
  `boundary_test.go` 目前只拦 `MemoryDomainTables`，admin 三张表不在其中，
  放在外面等于绕开边界测试；同时把 admin 表加进一张独立的 boundary 清单。

### 凭据边界

| 配置 | 位置 | 用途 |
|---|---|---|
| `WENYUAN_API_ADMIN_API_KEY` | wenyuan 进程的 env/配置文件 | 管理面鉴权 |
| `WENYUAN_CP_ADMIN_API_KEY` | 控制台 BFF 的 env | BFF 调管理面 |
| `WENYUAN_CP_ACCESS_KEY` | 控制台 BFF 的 env | 人登录控制台 |

- operator key **不写进 `admin` 库**：否则数据库读权限就等于管理面完全控制，
  而且第一次启动时无处可取。
- 配置文件的 key 是环境名去掉 `WENYUAN_API_` 前缀后的拼写
  （`internal/config/configfile.go`），所以进程级新键必须以 `WENYUAN_API_` 开头
  才能写进 `etc/wenyuan.yaml`。`WENYUAN_CP_*` 是控制台 Next.js 进程的环境变量，
  不经过 Go 配置解析器，保持现名。
- 人登录与服务凭据分开。v1 若想少配一把，可让 `WENYUAN_CP_ADMIN_API_KEY` 取
  `WENYUAN_CP_ACCESS_KEY` 的值，但这属于「单人运维模式」，旋转时两处都要改，
  文档里必须写明。

## 请求链路

1. `RegistryExtension.Authenticate` 按 key hash 查注册表，校验 `status=active`
   与 `expires_at`，返回 `tenant.Context{ID, Schema, Overrides}`（`ID` 为新增字段，
   现有结构体只有 `Schema` 与 `Overrides`）。
2. 中间件把租户上下文放进 request context，并写入 `storage.WithSchema(ctx, schema)`；
   存储层不 import `internal/tenant`。`WithSchema` 只接受通过 slug 校验的非空值
   （与注册表同一条正则），空值不写入，而不是写一个空字符串。
   注意 `TenantAuthMiddleware` 现在在 `apiKey == "" && mcpAuthToken == ""` 时整体旁路
   （`internal/httpx/tenantauth.go:57-60`）；多租户模式下注册表本身就是开关，
   不能再用「静态 key 为空」当作关闭。健康检查、文档等 `UnprotectedExactPaths`
   仍保持免鉴权。
3. `PostgresStore` 新增 `schemaFor(ctx)`，所有语句用 `schemaFor(ctx)` 而不是字段；
   字段改名 `defaultSchema`，只留给迁移和现有「显式 schema 参数」的接口。
   **`schemaFor` 在 multi 模式下 ctx 没有 schema 时返回 error，绝不回退
   `defaultSchema`**；single 模式只有一个 schema，仍返回 `defaultSchema`，
   行为与今天一致。multi 下的回退会把任何漏挂中间件的路径（或 worker 忘记传 ctx）
   静默变成对 `public` 的读写，正是本方案要防的跨租户泄漏。所有 `(T, error)`
   方法直接透传这个 error；返回 `(string, []any)` 的查询构造函数多一个 schema
   参数，由调用方解析后传入。
4. **不引入 store 池**：`PostgresStore` 里只有 schema 是每租户的，DB pool、tuning、
   vector 扩展都共享，一个实例服务所有 schema。

代价与安全网：

- 现有 `s.schema` 直接引用 289 处、分布在 55 个文件；改动机械但面大。
  已核对：其中约 280 处在带 `ctx` 的方法里，真正要改签名的只有
  `knowledgeFrom()`、`bm25ArmQuery()` 这类查询构造函数，以及
  `Schema`/`searchPath`/`migrateTarget`/`partialSchemaError` 这些迁移与诊断方法。
- 备选是 132 个方法的委托包装器，不改调用点，但重复代码过多，不选。
- 备选「按请求复制一份 `PostgresStore`」也不选：结构体带 mutex，
  复制会触发 `copylocks`（`migrateTarget` 的注释已记录同一约束）。
- 加一条静态检查/测试：除 `schemaFor` 与迁移路径外禁止读 `defaultSchema`；
  再加一条：multi 模式下 `schemaFor(context.Background())` 必须返回 error，
  不能返回默认 schema。
- PG 集成测试用两个租户的同名 bank 验证隔离，并覆盖每个写路由的交叉访问。
- 内存后端保持单 schema：`WithSchema` 在内存后端确定性忽略（或拒绝）租户 schema，
  多租户相关集成测试只跑 PG，避免单测通过而 PG 泄漏。

## 管理面 API

管理面由 `WENYUAN_API_ADMIN_LISTEN` 启用，凭据 `WENYUAN_API_ADMIN_API_KEY`（operator key，
与租户 key 无关）。

管理面在同一个进程里多绑一个 listener。`app.Serve` 现在只 bind 一个地址，
`--workers N` 靠继承 fd 复用端口；管理端口要同样处理绑定与优雅关闭，
否则多 worker 下只有主进程能绑。

```
GET    /admin/v1/tenants?status=&q=&limit=&offset=
POST   /admin/v1/tenants                  {tenant_id, display_name, config, adopt?}
GET    /admin/v1/tenants/{id}
PATCH  /admin/v1/tenants/{id}             {display_name, config, status}
DELETE /admin/v1/tenants/{id}             软删
POST   /admin/v1/tenants/{id}/purge       {confirm: "<tenant_id>"} 才 DROP SCHEMA
GET    /admin/v1/tenants/{id}/keys
POST   /admin/v1/tenants/{id}/keys        返回一次明文
DELETE /admin/v1/tenants/{id}/keys/{keyId}
POST   /admin/v1/tenants/{id}/session     代签短期租户 key
POST   /admin/v1/tenants/{id}/migrate     单租户迁移
GET    /admin/v1/overview          跨租户聚合；N 个 schema 的聚合要设缓存与上限
```

- key 轮换 = 新建一把 + 吊销旧的一把，不提供单独的 rotate 端点：
  「同一把 key 换值」没有可审计的语义（测试项里的「轮换」按这个定义写）。
- `wenyuan-admin tenant ...` 是管理面 HTTP 客户端：沿用 `internal/cli` 的
  `Client`/profile 形状，profile 的 `api-url` 指管理端口，不 import server 包。
  CLI 的 API-client 约束（AGENTS.md）不变。

创建流程（分步，不做一个大事务）：

1. 校验 slug / 保留名；
2. 插入 `tenant` 行 `status=provisioning`（唯一约束挡住并发重名）；
3. 按目标 schema 的现状走四种状态之一（见下节）；
4. 改成 `active` 才返回 201。

第 3 步失败时行停在 `provisioning`，重试安全（迁移幂等）。

### adopt 已有 schema

`public`（或任何已存在的完整参考 schema）必须能注册成租户，否则单租户部署无法
迁移到多租户。判定复用现有的 `classifySchema`
（`internal/storage/postgres_adoption.go:95`）：

| 目标 schema 状态 | 行为 |
|---|---|
| 不存在 | `CREATE SCHEMA` + 跑参考迁移序列 |
| 存在且是完整参考 schema | 只有带 `adopt: true` 才记账、不建对象 |
| 存在但为空（无参考表、无 alembic） | 与「部分」同等对待：只有带 `adopt: true` 才原地迁移 |
| 存在但部分/非参考 | 拒绝，报出缺失或多余的对象 |

`classifySchema` 对「存在但为空」返回 `schemaEmpty`，会直接跑序列；`public`
在新库上永远存在，所以创建 `tenant_id=public` 而不带 `adopt` 等于静默接管
`public`。**规则是「任何已存在的 schema 都必须显式 adopt」**，不是只对完整
schema 要求。

- adopt 只允许显式发起：`POST /admin/v1/tenants` 带 `adopt: true`，管理面
  二次确认。默认不接管同名 schema，避免误 attach 到别人的库。
- adopt 的目标是 `public` 时，`tenant_id` 就是 `public`；扩展仍装在 `public`
  （`extensionSchema`），单租户部署的现有数据原样保留。
- 如果 `admin` 本身已存在但不是本服务的表，启动直接拒绝，不 adopt。

## provisioning 与迁移

- 多租户模式下启动**不再**自动迁移 `cfg.DatabaseSchema`，改为调用
  `MigrateAdminSchema`：只跑本服务的本地序列（`IF NOT EXISTS` + 自己的
  ledger，如 `admin.schema_migrations`），绝不跑参考序列。
- **admin 序列必须放在自己的 embed 目录**（如
  `internal/storage/embeddedadmin/`），不能放进 `migrations/` 或
  `internal/storage/embedded/`：`scripts/reference_schema_migrations.py`
  在写入前会删掉这两个目录下所有 `*.sql`，手加的迁移会在下次重生成时消失；
  且 `MigrationSeries()` 要求版本从 1 连续，两套序列同目录必然冲突。
- 启动迁移的入口有三处，必须同时进入多租户分支：`app.New`（server，
  `internal/app/app.go`）、`app.OpenStore`（worker，`internal/app/store_access.go`）和
  `cmd/bankctl` 的 `openStore` + `runDBMigration`。`BackendConfig.RunMigrations`
  目前不被 `PostgresBackend.Open` 读取，不能指望它兜底。
- `MigrateAdminSchema` 用 DDL 角色跑（管理面，或配置了
  `WENYUAN_API_MIGRATION_DATABASE_URL` 的进程）。数据面若只拿 DML 角色，
  启动时不能硬建 `admin`：`admin` 不存在就直接失败关闭并给出明确错误。
  admin 序列不装扩展、不重建向量列。
- 租户 schema 在创建/adopt 时迁移；启动不扫全部租户（N×5 个迁移拖慢启动、
  放大故障）。
- 版本收敛已经有 `bankctl migrate`：它通过 `migrationSchemas` 调
  `NewTenantExtension.ListTenants` 逐 schema 迁移（`cmd/bankctl/main.go`），
  注册表接上后自动覆盖所有租户；设计里的 `wenyuan-admin tenant migrate`
  应与它复用同一实现，不要造第二套扫描。
- 新键（`WENYUAN_API_TENANT_MODE`、`WENYUAN_API_ADMIN_LISTEN`、
  `WENYUAN_API_ADMIN_API_KEY`）由 `config.FromEnvironment` 读取，再跑
  `go test ./internal/config -run TestDumpKeyInventory` +
  `python3 scripts/render_config_reference.py` 重新生成 `docs/CONFIGURATION.md`
  与 `etc/wenyuan.yaml`。不要登记到 `internal/config/reference.go`：
  那是参考实现的 key 表，不是本服务自有 key 的清单。

## 控制台接入

- BFF 新增 `WENYUAN_CP_ADMIN_API_URL` + `WENYUAN_CP_ADMIN_API_KEY`；现有
  `WENYUAN_CP_ACCESS_KEY` 继续做控制台登录。
- 新页面：`/tenants`、`/tenants/new`、`/tenants/[id]`（配置、key、用量、迁移版本）、
  `/overview`。
- 进入租户：BFF 调 `POST /admin/v1/tenants/{id}/session` 拿短期租户 key，放在
  加密签名的 HttpOnly cookie 里（现有 session 只是 HMAC 签名，存密钥要升级为 AEAD
  加密；Node 与 Edge 两份实现 `frontend/lib/server/session.ts` /
  `session-edge.ts` 都要改），再拿它代理现有 bank 页面；到期自动续签。
  key 永远不进浏览器 JS。
- cookie 保持 `SameSite=Lax` + `HttpOnly`，BFF 的写请求再加一层 Origin 校验：
  cookie 里现在装的是可用的租户凭据，不再只是一个登录事实。
- 未选租户时保持现有单租户控制台；后端未配注册表时管理页面整体隐藏。

## worker 与可观测

- `ListTenants` 只返回 `status=active` 的租户；`suspended`/`deleted` 不参与
  后台维护与新操作（重新激活后恢复），purge 掉的 schema 绝不出现。
- 消费循环从 `admin.tenant` 刷新租户列表（带 TTL 缓存），逐 schema
  `ClaimNextOperation`，轮转保证一个热租户不饿死其他租户。claim 时把 schema
  写进 ctx；`handle` 用 `context.WithoutCancel` 派生工作 ctx，会保留这个值。
- `archiveProcessor.storeSchema` 现在是进程级字符串（`internal/app/worker.go`），
  导出对象 key 用它拼（`internal/app/worker_transfer.go`）；多租户下必须改成
  从当前操作的 ctx 取，否则两个租户的导出会落到错误的前缀。
- 鉴权缓存：key hash → tenant 加 30–60s TTL 缓存；缓存项带 `tenant_revision`
  （注册表行的 `updated_at` 即可），管理面在本进程内的变更立即失效缓存，
  跨副本按 TTL 收敛，吊销延迟写进文档。
- 指标：`internal/app/metrics.go` 现在把 `cfg.DatabaseSchema` 当 tenant 标签，
  请求侧改成从请求上下文取，worker 侧按当前 schema 取；标签用 `tenant_id`
  （现在等于 schema 名，但不能假设永远相等），基数要有上限。
- 日志：请求日志带 tenant_id；管理面所有变更与代签 key 记 `admin_audit`。
- 备份：`bankctl backup` 现在只覆盖一个 schema（全局 `--schema` 选目标），
  没有 `--all`/`--tenant` 旗标（`--all` 是 `repair-bank` 的）。多租户要新增
  `backup --all-tenants`（或按注册表循环），把 `admin`（tenant、tenant_key、
  admin_audit）与每个租户 schema 各写一段。这不是加一个表名：
  `storage.PersistentTables()`（`internal/storage/admin_table_store.go`）从参考
  迁移序列解析 `CREATE TABLE`，admin 三张表不在其中；restore 还会拒绝
  「序列里没有声明的表」（`internal/bankadmin/backup.go`）。需要独立的 admin
  备份段、固定恢复顺序（tenant → tenant_key → admin_audit），并让 restore 的
  校验认识这段。
- 维护循环：代码不变，`ListTenants` 改由注册表实现后自动看到所有租户；探测成本
  随租户数增长，interval 已是配置项。

## 错误与安全

- 未知 key、过期、吊销、`suspended`、`deleted` 一律 401，响应体一致，不做租户枚举。
- 注册表查询失败 → 503 失败关闭，绝不回退到 `public`。
- 管理面默认只绑 `127.0.0.1`；operator key 常量时间比较，与租户 key 完全分离。
- 数据面与管理面用不同数据库角色：数据面只 DML（无 `CREATE`/`DROP`），
  管理面持有 DDL 权限。`purge` 是 API 动作，这一层让数据面进程被攻破时
  拿不到 `DROP SCHEMA`。现成的机制是 `WENYUAN_API_MIGRATION_DATABASE_URL` /
  `MigrationDatabaseURL`（`internal/storage/postgres.go` 已支持迁移走独立 DSN、
  serving pool 不发 DDL），管理面复用同一形状即可。
- 代签 key：TTL ≤ 10 分钟，label `console:<operator>`，在 key 列表中以 `ephemeral`
  可见、可审计。
- slug 校验保留名与控制字符；key 不进 URL、不回显、不写日志，日志只出现 `prefix`。
- key 规格：≥128 bit 随机、固定前缀（如 `wyk_`），sha256 无盐对高熵密钥是安全的
  （与口令哈希不同，不需要 KDF）；`last_used_at` 按分钟采样写，避免每请求一次写放大。
- `admin_audit` 要有保留期与分批清理（照 `PruneTerminalOperations` 的形状），
  否则审计表随租户数线性长。
- purge 要求输入租户 id 二次确认，全部进 `admin_audit`。

## 测试

- 单元：slug 校验、key 哈希、状态机、注册表仓储（内存 + PG 两套实现）。
- PG 集成：两个租户同名 bank 的隔离，且每个写路由交叉访问都返回 404/空结果；
  创建失败重试；key 过期/吊销/轮换；suspend → 401；purge；
  **purge `public` 被拒绝且扩展仍在**；迁移版本漂移。
- 上下文：`schemaFor(context.Background())` 返回 error；
  任意租户 key 都读不到 `admin` schema。
- adopt：把已有的完整参考 schema（含 `public`）注册成租户后数据原样可读；
  部分 schema 被拒绝且不留下 `tenant` 行；空 schema 不带 `adopt` 也被拒绝。
- 备份：`admin` + 两个租户往返，tenant/key/审计/数据完整，恢复顺序固定。
- worker：两个 schema 各有 pending 操作时都能被抽干，轮转公平；
  文档导出对象 key 落在各自 schema 前缀下。
- 兼容回归：未配注册表时整套 parity 测试与 `reference_openapi.py --check` 原样通过；
  管理路由不出现在 `/openapi.json`。
- 控制台：BFF 任何响应都不含租户 key；session 加密；管理页在后端未启用时隐藏。
- 故障注入：注册表不可用时数据面 503，且没有请求落到 `public`。

## 分阶段

| 阶段 | 内容 | 完成标志 |
|---|---|---|
| P1 | `admin` 表 + `MigrateAdminSchema` + `RegistryExtension` + `wenyuan-admin tenant` + 管理面 API | CLI 能建租户、拿 key，数据面还不认 |
| P2a | context 携带 schema + `schemaFor` fail-closed + 隔离测试 | 一个进程用两把 key 读到两个 schema，隔离测试过 |
| P2b | worker 多租户 + 指标 | 两个 schema 的队列都抽干，导出前缀正确 |
| P3 | 控制台租户 CRUD + 总览 | 控制台能建/停/删租户，看不到数据面 key |
| P4 | 控制台「进入租户」（代签） | 管理员能从控制台操作指定租户的 bank |
| P5 | 团队方案修订 | `project/project_member` 落到 `admin`，团队设计更新 |

P2a 是唯一高风险步（289 处 schema 引用），单独一个 PR，先只做隔离测试再加控制台；
P2b 单独一个 PR，避免 worker 的 `storeSchema` 问题被大 diff 掩盖。

## 非目标（本轮）

- per-tenant 配额与限流（v1 只记录用量，不拦截）。
- 租户改名（slug 不可变；改名等于新建租户或手工 schema 重命名）。
- 跨租户数据迁移、计费、OAuth provider 集成、per-tenant 模型 provider
  （需要新的 provider 解析 seam，v1 的 `config` 不承载它）。
- 对参考实现路由面、JSON 字段、PostgreSQL 参考表的任何改动。

## 与团队方案的关系

团队方案的「A 的凭证选中 A 的 schema」依赖本方案的 P2a 才算成立。本方案落地后，
团队设计需要改两处：

1. 决策 2：`project` / `project_member` 放 `admin` 注册表，不放 Team schema；
2. 读取路径：成员资格从注册表查，读时仍回 Team schema 复核一次，防伪造指向。

其余部分（合并任务、水位、切片、回退规则）仍按 `2026-10-29-team-project-shared-memory-design.md`
讨论，但那份文档的读路径与删除语义还需要按 review 结论修订后再实现。

## 风险

- **P2a 漏改一处 `s.schema` 就是跨租户泄漏**。静态检查 + 双租户隔离测试是硬要求；
  `schemaFor` 的 fail-closed 是第二道网，不能退化成回退默认 schema。
- adopt 的「已存在」判定必须覆盖空 schema，否则会在不带 `adopt` 时静默接管
  一个已存在的空 schema（`public` 在新库上就是这种）。
- admin 序列与参考序列必须物理隔离（目录 + ledger），否则一次 schema 重生成
  会静默删掉它。
- 注册表是新的单点：它不可用时数据面无法鉴权。用 503 失败关闭，不做降级。
- 一个进程服务所有租户，故障域变大；吵闹邻居只能靠后续的配额/限流解决。
- 管理面是新的攻击面；默认只绑本机、独立凭据、全量审计。
- `admin` 与既有 schema 撞名时启动拒绝；`public` adopt 必须显式开关，避免误接管。
- 控制台 session 从签名升级为加密，属于既有安全模型的变更，需要单独评审。

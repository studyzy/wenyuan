# Team read scope（团队读取范围与 `?scope=` 参数）——设计方案

状态：2026-11-09 设计确认。范围：本服务自己的能力，不属于参考实现的兼容面。
数据面的路由、请求体 JSON、响应字段、状态码一律不改；新增的只有一个**不写入
OpenAPI 的可选查询参数**。

## 一、问题

团队平面（Team / Project / `project.merge`）已经落地：成员的 `world` /
`experience` 事实会被逐条复制进项目团队 bank `<team_tenant_id>::<project>`。
但“成员的 recall 到底读哪个 bank”这件事在设计文档里只有一句话，实测下来与
直观预期不符，且调用方无法选择。

现状（`internal/storage/team_fallback.go`，`internal/teamread/resolver.go`）：

1. 只有当请求的 bank id 形如 `<调用方自己的 tenantID>::<projectID>`、且调用方是
   该 team 项目的成员时，`teamread.Resolver` 才解析出一个 `FallbackScope`。
2. 解析成功后，`withFallback` 先探团队 bank：**有内容就读团队 bank**，团队 bank
   为空才读个人 bank。
3. 回退只发生在 `recall` 与 `memories/list`。`tags`、`graph`、`entities`、
   `stats`、`documents`、`chunks`、按 unit id 的读写、`state=invalidated`、
   `document_id` 都不回退。
4. 调用方没有任何办法表达“我这次只想读自己的”或“我要两个都读”。

结论：**“团队优先、个人兜底”已经是当前行为**。缺的是可选择性，以及一处把
行为写清楚的地方——上一版讨论里曾把它记反，说明它不够显式。

本次要解决的是后者：让调用方能选择读取范围，并把“团队优先”确认为正式语义
而不是实现的副作用。

## 二、决策

### 决策 1：在服务端实现，客户端不改

客户端（`integrations/coding-agents`）无法得知成员关系：`ResolveProjectForMember`
读的是 admin schema 的 `team_project_member`，只存在于 `/admin/v1/teams/**` 管理面，
且需要 operator key。客户端唯一能做的“先查团队再查个人”是拿静态配置拼一个团队
bank id 去猜——猜错落到别的团队或空结果，再补一次请求，还要自己融合去重。那是
把服务端已经掌握的信息搬到每台开发机的配置里。

因此决策逻辑留在 `teamread` + `storage`，客户端零改动。

### 决策 2：参数走 query string，不进请求体

`/openapi.json` 是逐字节从参考实现抓取的（`scripts/reference_openapi.py --check`），
`clients/openapi.json` 由它派生，四个 SDK 由后者生成。往 `RecallRequest` 里加一个
body 成员会让这三层全部失配。

因此参数放在 query string 上：

```
POST /v1/default/banks/{bank_id}/memories/recall?scope=team
GET  /v1/default/banks/{bank_id}/memories/list?scope=personal
```

query string 不出现在 OpenAPI 文档里，SDK 生成的方法签名不变，参考实现的
`--check` 继续通过。

### 决策 3：取值 `team | personal | both`，缺省 `team`

| `scope` | 团队 bank 有内容 | 团队 bank 空 |
|---|---|---|
| `team`（缺省） | 团队 bank | 个人 bank（兜底） |
| `personal` | 个人 bank | 个人 bank |
| `both` | 422（本期不实现） | 422 |
| 其他值 | 422 | 422 |

- `team` 是当前行为的显式名字，不是为了新行为取的别名。
- `personal` 表示“回到没有团队之前”：不探测团队 bank，因此也省掉一次
  `BankHasUnits` 查询。它**不需要**调用方是成员——非成员用 `personal` 与不传参数
  等价，不报错。
- `both` 在解析阶段就被拒绝，不是运行时降级。静默把它当成 `team` 会让调用方
  以为自己在读两份，这是最难排查的一类差异。
- 大小写敏感，与其它枚举参数（`budget`、`tags_match`、`state`）一致。

### 决策 4：`both` 本期不实现

跨库合并不是“多查一次”，是 recall 的第二条执行路径：

- 两个 bank 各跑一遍完整流水线（召回臂、RRF 融合、rerank、token 预算），再对
  两份结果做一次跨库融合。RRF 是排序级融合，混合后的名次必须重新融合；
  rerank 只能在合并后的候选池上重跑，否则两侧分数不可比。
- 同一条事实在个人 bank 与团队 bank 各有一行（团队行是个人行的镜像），必须去重。
  去重键只能是团队行的 `metadata.source_unit_id`，不能是文本——成员改过措辞后
  文本不再相等，按文本去重会留下两份。
- token 预算是全局的：两侧各自截断再拼接必然超预算。
- `include.source_facts` / `graph` / `chunks` 的跨库解析没有定义：团队行的 unit id
  属于团队 schema，个人 bank 的图边不覆盖它。

这些规则需要单独一节设计，且测试成本不低于现有整条 recall。在有人真正需要之前
不做。

### 决策 5：不做 bank 级配置开关

`?scope=` 已经覆盖“每个调用方各自选择”。再叠一层 bank 配置，只会产生“配置说
团队、请求说个人”的优先级问题而没有新增能力。

## 三、实现

### 3.1 存储层（`internal/storage`）

`team_fallback.go` 增加一个偏好枚举，`withFallback` 增加一条短路：

```go
// FallbackPreference says which bank a redirectable read prefers.
type FallbackPreference int

const (
    // PreferTeam reads the team bank when it holds anything, and the caller's
    // own bank otherwise. It is the zero value, so a caller that states no
    // preference gets the behaviour this helper has always had.
    PreferTeam FallbackPreference = iota
    // PreferPersonal never redirects. The team bank is not even probed, because
    // the caller has said the answer it wants is its own.
    PreferPersonal
)

type FallbackQuery struct {
    BankID     string
    DocumentID string
    State      string
    Preference FallbackPreference
}
```

`withFallback` 在 `fallback == nil` 之后、能力探测之前加：

```go
if query.Preference == PreferPersonal {
    value, err := read(ctx, query.BankID)
    return value, query.BankID, err
}
```

其余分支一字不改。`CanFallBack`（`document_id` / `state=invalidated`）语义不变，
且优先于偏好：`scope=team` 配 `state=invalidated` 仍然读个人 bank——团队 bank 没有
invalidated 行，重定向会把“这个 bank 有归档事实”变成“什么都没有”。

### 3.2 参数解析（`internal/memoryops`、`internal/reporting`）

新增一个共用的解析函数，返回 `(storage.FallbackPreference, *httpx.ValidationFailure)`：

- 缺失或空串 → `PreferTeam`。
- `team` / `personal` → 对应偏好。
- `both` → `httpx.ValueError([]string{"query", "scope"}, "both", "both is not supported yet; use team or personal")`。
  错误形状与其它字段校验一致，走 `httpx.WriteValidationFailure`，状态 422。
- 其他值 → `httpx.Literal([]string{"query", "scope"}, value, []string{"team", "personal", "both"})`。

`scope=both` 的 item 仍把 `both` 列进 `expected`，因为它是被承认的取值、只是尚未
实现——这与“未知取值”是两种不同的错误，调用方需要能区分。

调用点：

- `internal/memoryops/recall.go:165`：`query.Fallback = h.teamFallback(...)` 之前
  先把解析结果写进一个本地值，并在 `FallbackQuery` 里带上偏好。
  `h.teamFallback` 本身只解析 scope，不需要改签名；`buildRecallQuery` 之后单独
  处理，因为 422 必须在使用 store 之前返回。
- `internal/reporting/memory_routes.go:118`：同样在构造 `MemoryQuery` 之前解析。

两处都用 `r.URL.Query()`，`scope` 取最后一个值（与 `queryLast` 的既有约定一致）。

### 3.3 不受影响的路径

`tags`、`graph`、`entities`、`stats`、`documents`、`chunks`、按 unit id 的读写都不
回退，因此 `?scope=` 对它们没有意义。对它们**静默忽略**这个参数，而不是报错：
一个把 `?scope=` 带到整个请求组的客户端不该因此拿到 4xx。这一点写进
`docs/PARITY-OPEN-ITEMS.md`。

## 四、文档

- `docs/PARITY-OPEN-ITEMS.md`：新增一条——`?scope=` 是本服务自加的、不写入 OpenAPI
  的可选查询参数，兼容性审计必须排除它；同时记下它只对 recall 与 memories/list
  生效，其余路由静默忽略。
- `AGENTS.md` 团队一节：读取优先级由一句描述改为“团队优先、个人兜底，`?scope=`
  可覆盖”。

## 五、测试

失败优先（新增行为先有会失败的用例）：

1. `internal/storage/team_fallback_test.go`
   - `PreferPersonal` 即使团队 bank 有内容也只读个人 bank，且**不调用**
     `BankHasUnits`（用脚本化的 presence 记录调用次数断言）。
   - `PreferPersonal` 在 store 没有 `BankPresence` 时行为不变（仍读个人）。
   - `scope=personal` 配 `state=invalidated` 仍然读个人。
2. `internal/memoryops`
   - 缺省（不带参数）走团队 bank。
   - `?scope=personal` 走个人 bank。
   - `?scope=both` 返回 422，错误定位在 `query.scope`。
   - `?scope=bogus` 返回 422，`expected` 包含三个取值。
   - `?scope=PERSONAL` 返回 422（大小写敏感）。
3. `internal/reporting`
   - 同上四类，走 `memories/list`。
4. `internal/app/team_read_test.go`
   - `TestMemberRecallReadsTheTeamBank` 增加 `scope=personal` 的对照：同一 fixture
     下返回个人的事实而非团队的事实。
   - 现有的两个主用例保持不变——它们断言的“缺省读团队”正是决策 3 的缺省值。

`make check` 必须全绿（含 `make pgx`）。

## 六、不做

- `both` 的跨库融合与去重。
- bank 级 `team_read_preference` 配置。
- 客户端改动。
- 把回退扩展到 `tags` / `graph` / `entities` / `stats` / `documents` / `chunks` /
  按 unit id 的读写。
- 修改 `internal/httpx/openapi.json`、`clients/openapi.json` 或任何 SDK。

## 七、遗留

- **可见性窗口**：团队 bank 由 `project.merge` 写入，默认 60 秒一轮。`scope=team`
  下成员刚 retain 的事实要等下一轮才可见。这是团队平面既定取舍（“宁可旧也要全”），
  本次不改。
- **`both` 的后续设计**：若确有需求，从 `metadata.source_unit_id` 去重与共享 token
  预算两条规则开始。
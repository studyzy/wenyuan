# Review: `integrations/` — 对接 AI Agent 的 Hooks / MCP / 插件

Reviewed 2026-10-29. Scope: `integrations/coding-agents` (~6 300 行 TS, 4 800 行测试) and
`integrations/dsh-plugin` (~2 900 行 TS)。参考实现：`/code/hindsight/hindsight-integrations/coding-agents`
(~21 000 行 + 24 000 行测试，22 个 harness)。

Evidence format: `文件:行号` on the wenyuan side, `H:…` = upstream `src/installer.ts`, `Hc:…` = upstream
`src/core/*.ts`. Items marked **(验证)** were executed against the built package, not just read.

**Gate status（评审开始时）**: `tsc --noEmit` 通过；`vitest tests/unit` 338/338 通过；
`vitest tests/e2e` 157/157 通过。测试全绿 —— 但不覆盖下文任何一条最主要的缺陷（见 §7）。

---

## 1. 先回答你的问题：是不是"增加 Hooks + MCP，还有别的修改"

**是 Hooks + MCP + Skill 三件套，但这个包实际做的"别的修改"比这三件套还多，且有几处会改动宿主机器的全局状态。**

| 层 | 做了什么 | 落在哪里 | 是否与上游对应 |
|---|---|---|---|
| **Hooks** | 3 个生命周期钩子（SessionStart / UserPromptSubmit / Stop），merge 进宿主配置文件 | `src/hosts/*.ts`, `src/hosts/hooks.ts`, `src/runtime/*-stage.ts`, `hooks/*.ts` | 对应上游 `installer.ts` 的 hook 合并；**形状与上游不一致**（§4.1） |
| **MCP** | 往宿主 MCP 配置写一个 stdio server 条目（或调 `claude mcp add`） | `src/mcp/*` | 对应上游 `mcpServerEntry`；**3 个机制有实现缺陷**（§4.2） |
| **Skill** | 把一个 `SKILL.md` 拷进宿主 skills 目录，并在 SessionStart 时"漂移就刷新" | `src/hosts/skills.ts`, `src/runtime/skill-refresh.ts` | 对应上游 `installSkill`；**内容是占位**（§5.3） |
| **额外：运行时暂存** | `install` 把 `dist/runtime` 整目录拷到 `~/.wenyuan/wenyuan-memory/`，所有钩子/MCP 都指向这份拷贝 | `src/runtime/stage.ts` | 对应上游 `~/.hindsight/coding-agents` |
| **额外：配置持久化** | 写 `~/.wenyuan/config.json`（`serverMode/apiUrl/apiToken/bankId/apiPort`） | `src/config/*` | 上游对应 `~/.hindsight/coding-agent.json`（~50 个字段） |
| **额外：DSH 用 Cordis 插件替代 hooks** | `install dsh` 调 `dsh plugin --profile <p> add @studyzy/dsh-wenyuan-memory`，写 MCP 条目 + skill | `src/hosts/dsh.ts`, `src/hosts/dsh-plugin.ts` | 对应上游 `dsh.ts`（Cordis 插件） |
| **额外：改宿主全局设置** | Claude Code：`--enable-workspace-mcp` 时写 `enableAllProjectMcpServers: true` | `src/hosts/claude-code.ts:43,110-130` | **上游没有**；该键是"本机所有项目 MCP 全开"，与 user-scope 服务器无关（§4.1 H6） |
| **额外：外呼宿主 CLI** | `claude mcp add/remove`、`dsh plugin add` | `src/mcp/command-line.ts`, `src/hosts/dsh-plugin.ts` | 上游同样外呼 `claude`/`dsh` |

还有两处**声称要做但其实是空壳**（README 未如实说明）：

- `--import-conversations`：`src/commands/install.ts:141-150` 固定打印"本包还没有 transcript importer"，**永远不导入**。
- `stats`：`src/stats/usage-log.ts` 只有读取器，**全包没有任何写入者**，所以 `~/.wenyuan/usage.jsonl` 永远不存在，`stats` 永远输出"no recorded usage yet"。

---

## 2. 结论摘要

**结构性结论：这个包是"上游 coding-agents 的插件安装骨架"，而不是"上游功能的复刻"。**
上游约 21 000 行里，真正让 memory 生效的部分（transcript 读取、知识页注入、reflect、按仓库分 bank、git 播种、cursor/去重、retry、usage、auto-update）**几乎全部缺失**；上游约 22 000 行的 harness 装配也只覆盖了 4/22。

具体三种情况：

1. **已接线但接线错误（会静默不工作）**：hook 注册形状 `{"matchers":[...]}` 不是任何宿主读的格式；hook 的 `command` 是裸 `.mjs` 路径；Codex 的 hook 文件路径 `~/.codex/settings.json` 是 Codex 根本不读的文件 → `install` 打印 `wired`，但宿主一条钩子都不会跑（§4.1）。
2. **根本没实现（功能缺失）**：Stop 钩子从不读 `transcript_path`，所以 **Claude Code 下一条对话都不会被存入**；没有任何知识页/reflect 调用；MCP server 只暴露 1 个工具。
3. **实现存在但行为错**：所有仓库共用一个 `default` bank；Stop 每次把整段对话重新提交一次且不带 `document_id`（重复入库）；DSH 插件一个进程多仓库时会读写错 bank。

**严重度总览（去重后）**

| 级别 | 数量 | 含义 |
|---|---|---|
| **P0** | 14 | 宿主文件损坏 / 宿主完全未接线 / 记忆静默丢失或串仓 |
| **P1** | 21 | 常见路径行为错误，可恢复 |
| **P2** | 13 | 卫生、文档、潜在风险 |

---

## 3. 致命问题（P0）

### 3.1 钩子注册的形状没有任何宿主能读 【会静默失效】

```jsonc
// 现在写进 ~/.claude/settings.json 的内容（验证：跑 install 后的真实文件）
"hooks": {
  "Stop": [
    {"hooks": [{"type":"command","command":"echo foreign"}]},          // 用户自己的（正确形状）
    {"matchers":[{"type":"command",   // ← 键名错：宿主读 "hooks"，不是 "matchers"
                  "command":"/home/u/.wenyuan/wenyuan-memory/hooks/stop.mjs",  // ← 裸 .mjs 路径，不是 shell 可执行命令
                  "timeout":30}]}
  ],
  "SessionStart": {"matchers":[...]},   // ← 同事件下的 Stop 是数组、这里却是对象：自身形状不一致
  "UserPromptSubmit": {"matchers":[...]}
}
```

- 上游写的是 `{"hooks":[{"type":"command","command":"node \"<dist>/claude-stop-hook.js\"","timeout":30}]}`（`H:227-229`）。
- 问题代码：`src/hosts/hook-shape.ts:29`（`MATCHER_GROUP_KEY = "matchers"`）、`:73-80`（`command: scriptPath`）、`src/hosts/hooks.ts:76-84`。
- 裸路径能不能跑？**取决于宿主的执行方式（验证）**：`/bin/sh -c "<裸 .mjs>"` 能跑（有 shebang）；但 `spawn(cmd, {shell:false})` 会 `ENOENT`。上游一律带 `node`，就是为了不赌这一点；这里赌了。
- 另外一个语义问题：**同一事件下出现"数组里塞两种形状"**（用户的 `{"hooks":[...]}` 与我们的 `{"matchers":[...]}`）。Claude Code 按 `{"hooks":[...]}` 解析每条 group，我们的 group 没有 `hooks` 键 → 该 group 被忽略或报错，两种结果都不是"装上"。

### 3.2 Codex 的钩子写进了 Codex 不读的文件 【会静默失效】

`src/hosts/codex.ts:30` 声明 `configFiles: [".codex/settings.json"]`，`README.md:37` 也这么写。
上游写的是 **`~/.codex/hooks.json`**，并且在 `config.toml` 里需要 `[features] hooks = true`（`H:652-686`）。
Codex 没有 `~/.codex/settings.json` 这套钩子配置 → 装上后 Codex 零钩子。

### 3.3 Stop 钩子从不读 transcript → Claude Code 下**一条对话都不入库**

`Stop` 的 payload 是 `{session_id, transcript_path, cwd, hook_event_name, stop_hook_active}` —— **没有 prompt/messages**。
`src/runtime/conversation.ts:70-78` 只从 payload 里挖 `turns/messages/conversation/transcript`，都挖不到就 `[]`；
`src/runtime/stop-stage.ts:41-43` 遇到 `turns.length === 0` 直接 `{written:false}` 返回。
全包**没有任何一处读取 `transcript_path`**。上游对应位置是 `Hc:retain-hook.ts:122 const turns = readTranscript(transcriptPath)`。

→ 即使 §3.1 修好，Claude Code / Codex / CodeBuddy 的会话内容依然是 **0 写入**。

### 3.4 写入没有身份、没有去重、每次全量重发

```ts
// src/runtime/stop-stage.ts:53-58
const previous = options.previousTurns ?? [];        // 没有任何调用方传入 → 永远是 []
const content = composeConversation(previous, conversation.turns);
const outcome = await client.capture({ content });   // → POST /memories {items:[{content}]}
// src/runtime/memory-client.ts:76
const response = await this.post("/memories", { items: [item] });
```

- `documentId`（`conversation.ts:22-24`，`sha256(host\0session)`）被算出来、被返回，**但从未发送**。
- 服务端接收 `document_id`/`update_mode`/`operation_id`/`metadata`，这里一个都没发 → 每次 Stop 都新造一个 document。
- 上游：`conversation:<sessionId>` 作 ref-id + `updateMode: append` + 确定性 `operation_id`（`Hc:chat.ts:371,417,434`），并有 `retain-cursor.ts` 记录"已发到第几轮"。
- 后果：一次会话按轮次响应即产生 N 份重复记忆，每份都被抽取成 fact/observation。

### 3.5 所有仓库、所有 agent、所有机器共用一个 bank

`src/config/connection.ts:40` `DEFAULT_BANK_ID = "default"`，`:198,:207` 原样写入。
上游默认是 **按仓库动态 bank**：`coding-agent::{gitProject}`（`Hc:bank.ts:53`），并且支持 `mapPathToBank` / `optInOnly` / `banks.<id>`。
后果：A 项目的回忆会出现在 B 项目，B 的写入污染 A。这是本次评审里**对使用体验影响最大的一条**。

### 3.6 DSH 插件不按 workspace 分 bank 【串仓】

`integrations/dsh-plugin/src/index.ts:170,191` 在 `apply()` 时把 `bankId` 解析一次，全进程共用，从不读 `agent.session.header.cwd`。
上游对此有明确说明与实现：一个 `dsh` 进程服务多个仓库，`RuntimeCore` **按 workspace root 建并缓存**（`H:dsh.ts:10-15,162,372-376`）。
→ Web UI 里在 repo B 开会话，回忆的是 repo A 的 bank，并把 B 的对话写进 A。

### 3.7 DSH 插件的 seam 不是真正的 fail-open

`index.ts:22` 声明 "Everything here is fail-open"。实际：

- `clientNow()`（`:190` 附近 `new WenYuanClient` → `settings.ts:78` 的 `normalizeApiUrl(...).replace`）无 try/catch，配置漂移时抛 `TypeError`；
- `agent.inject(...)`（`:222`）是宿主调用，无 try/catch；
- `agent/created` 是 `serial` 且"抛错即建会话失败"。
- `client.ts:330-358` 的 10s 超时只包住 `fetch`，**没有包住 `response.text()`** → 半开连接会让 `agent/turn-stopping`（串行）一直挂着。

### 3.8 DSH 工具失败被报成"成功"

`index.ts:372-392` 的 `textTool` 把所有 `!outcome.ok` 都变成字符串返回（`"… failed: ECONNREFUSED"`），管线记 `isError:false`。
上游是**抛错**（`H:dsh.ts:345-357`）。模型会把"查询失败"当成"查到没有"。

### 3.9 JSONC 编辑在注释/尾随逗号旁会删掉宿主的其他配置 【已验证】

`src/write/jsonc-scan.ts:59-63` 的 `previousSignificant` 遇注释就停，`jsonc-edit.ts:330-340` / `jsonc-scan.ts:338-348` 因此在删元素时越过注释去取逗号：
`{"a":[…], // note, "b":1}` 删除我们的 a 之后 **`"b": 1` 消失**。
尾随逗号场景 `insertMember`（`jsonc-scan.ts:312-315`）会产出 `…},]` 形状的错误（验证：生成 `[...,\n ]`，靠 JS 方言侥幸可读，严格 JSON 下坏）。

**已修正评审中的一个误报**：评审 04/05 认为"多行外来数组 + 我们的插入 → 文件不可解析"。我实测（`insertIntoJsoncArray` + 真实 `install`）**没有出现**不可解析 —— 插入点正确落在 `]` 之前。真实缺陷是上面的注释/尾随逗号边界，以及 3.3 之外的一条：`upsertJsoncMember` 在 `hooks.<Event>` 是**对象**且外来时会置 `conflict:true` 且不写（`jsonc-edit.ts:240-242`），而 `hooks.ts:121-127` **忽略 `conflict`** 仍返回 `ok:true` → **用户已有同事件钩子时我们静默不装，却打印 wired**。

### 3.10 `claude mcp add` 的参数放在 `--` 之后 → 环境变量一个都不会写进去

`src/mcp/command-line.ts:33-50` 生成：
`claude mcp add --scope user wenyuan-memory -- node <server> --env WENYUAN_MCP_HARNESS=claude-code …`
`--` 之后的都是 **server 的 argv**，`--env` 不是 `claude mcp add` 的选项了 → 条目里没有 `env`。
上游：`claude mcp add --scope user hindsight --env HINDSIGHT_MCP_HARNESS=claude-code -- node <server>`（`H:590-601`）。
连带：`register.ts:118` 的 `executable ?? input.hostId` 默认是 `"claude-code"`，**这不是一个可执行文件名**（是 `claude`），所以非经由 adapter 显式传入时永远走 fallback。
而且 `command-line.ts:90-98` 的 fallback 返回 `ok:true, written:false` → 命令层打印 "wired"。

### 3.11 MCP 归属判定用"当前暂存路径"完全相等 → 搬家后既不能重装也不能卸载 【已验证】

`src/mcp/ownership.ts:43-56`：`args.some(a => a === serverPath)`。
`HOME` 变了 / 包换了目录 / 暂存布局变了 → 我们**自己写的**条目被判为 `foreign`：重新 install 拒绝覆盖，uninstall 拒绝删除，条目永久残留。
上游按**形状**判定（`command==="node"` 且 `args[0]` 的 basename 是 `mcp-server.js`，父目录 `dist`、祖父目录 `…/coding-agents`，`Hc:util.ts:78-89`），容忍任意安装目录。
同处（`:51-55`）：`args.some(...)` 会把"任何参数里出现我们路径"的包装器条目也认成自己的，install 覆盖 / uninstall 删除别人的启动器。

### 3.12 TOML 表只按字面 header 剥离 → 重装写出重复表

`src/mcp/toml-table.ts:64-67` 只识别 `[mcp_servers.wenyuan-memory]` 字面量；点分/引号形式或 `[[...]]` 形式会被漏掉 → 重装追加第二份表，宿主 TOML 解析失败。

### 3.13 DSH 安装忽略 `$DSH_HOME`

`src/hosts/dsh.ts:40,43` 固定 `~/.dsh/profiles`；全包不读 `DSH_HOME`。上游 `H:dsh.ts:1743-1745` 先读 `process.env.DSH_HOME`。
→ 设置了 `DSH_HOME` 的机器上装到错误目录、且可能新建一个空的 `web` profile。

### 3.14 `WENYUAN_BANK_ID` 永远到不了 DSH 插件

`install.ts:102` 把 `environment`（含 `WENYUAN_BANK_ID`）交给宿主 adapter；`hosts/dsh.ts:54-70` 只把它塞进 **MCP 条目**。
DSH 插件读的是自己的 profile settings（`dsh-plugin/src/settings.ts:20-40`，默认 `"default"`），**从不读 `process.env`**。
→ `WENYUAN_BANK_ID=team-a` 时除 DSH 外所有宿主都进 `team-a`，DSH 静默写 `default`。

---

## 4. 主要问题（P1）

### 4.1 Hooks

| # | 问题 | 位置 |
|---|---|---|
| H4 | Claude/CodeBuddy 的钩子形状与事件语义按 Claude 写死；`hook-host.ts:22-36` 在 payload/env 都没宿主信息时**回落到 `declared[0]` = claude-code**，而钩子命令里并不带宿主 env → Codex/CodeBuddy 的 firing 一律被当成 Claude Code（上游为此专门改过一次：`H:238-245`） | `hook-host.ts:22-36` |
| H5 | 三个通用脚本（`session-start.mjs/pre-prompt.mjs/stop.mjs`）vs 上游**每宿主一套入口**（各宿主 stdin/ stdout 协议不同） | `lifecycle.ts:28-32` |
| H6 | `enableAllProjectMcpServers` 是本机**所有项目**的 MCP 开关，与 user-scope 服务器无关；上游从不碰它 | `claude-code.ts:43,110-130` |
| H7 | uninstall 只在文件"空了"时删文件（`hooks-remove.ts:69-88`），但**区分不了"install 创建的文件"和"用户原有的文件"**；且 uninstall 前又调一次 `backupExistingFile`（`hooks-remove.ts:45`），会把第一次 install 前的原始备份覆盖成"已安装"版本 | `hooks-remove.ts:45,69-88` |
| H8 | JSON（非 JSONC）宿主整档 `JSON.stringify` 回写 → 丢注释、重排版（上游明确点名为要避免的做法，`H:180-193`） | `hooks.ts:139-149`, `json-server-map.ts:105-127` |
| H9 | BOM 文件让所有 JSONC 编辑静默失效（`rootObjectRange` 返回 null），install 仍打印 wired | `jsonc-scan.ts:157-163`, `json-merge.ts:36-44` |
| H10 | `hooks.ts:76-84` 的 budget 转换把 `seconds` 分支原样写 `budget`、`milliseconds` 分支用 `budgetMilliseconds` —— 语义正确，但 `deadline.ts:39-43` 对非 seconds 分支只减 5（**当 ms 用**），预算单位一旦不是 seconds，stop 的"5 秒余量"变成 5 毫秒 | `deadline.ts:39-43` |
| H11 | 上游每个宿主有管道化的 `SessionStart/UserPromptSubmit/Stop` + 追加事件（Grok 的 `PostToolUse`、Droid 的 `Notification`）；这里只有三个固定事件 | `lifecycle.ts:47-84` |

### 4.2 MCP

| # | 问题 | 位置 |
|---|---|---|
| M1 | 打包 MCP server **只暴露 1 个工具** `wenyuan_recall`（`RECALL_TOOL`，`runtime/mcp-server.ts:29-42`），`tools/list` 也只列它。上游 `knowledge-tools.ts` 有 **8 个**（`hindsight_sync_status / diagnose / search_knowledge_pages / list_knowledge_pages / read_knowledge_page / reflect / capture_initiative / ingest_document`） | `runtime/mcp-server.ts:29,113-115` |
| M2 | 手写 JSON-RPC：`initialize` 对**不认识**的 `protocolVersion` 回最新版（应回自己能支持的版本并让客户端决定）；不实现 `notifications/initialized` 之外的 `resources/*`、`prompts/*`（可接受）；不支持 `id` 为字符串以外的形状（已处理）；**未校验 `WENYUAN_MCP_HARNESS`**（上游在缺失时**拒绝启动**，`H:62-74`） | `runtime/mcp-server.ts:92-112` |
| M3 | `MCP_HARNESS_ENV` 写进条目但**服务端/运行时都不读**（`grep` 全仓仅声明与测试）→ 归因信息实际未使用 | `mcp/mechanisms.ts:20` |
| M4 | fallback（外部 CLI 不可用）返回 `ok:true`，install 因此报成功，实际什么都没注册 | `command-line.ts:90-98` |
| M5 | `install dsh` 既装插件（进程内 8 工具）又写 MCP 条目（1 工具、另一套 bank 解析）→ 一个宿主两套记忆入口、两套凭据 | `hosts/dsh.ts:86-91` |

### 4.3 运行时 / 配置 / CLI

| # | 问题 | 位置 |
|---|---|---|
| R1 | 无知识页注入、无 reflect（上游默认 `autoInject:"reflect"`，含 page-search → recall 回退） | `memory-client.ts:59-78`（只 2 个路由） |
| R2 | recall 查询是**原始 prompt 全量**，不截断、不带 `recallOptions`（类型/预算） | `pre-prompt-stage.ts:76-78` |
| R3 | pre-prompt 只在**整个 session 第一次**注入；上游是每轮（首个 prompt 注入 + `pageRefreshEveryTurns` 节奏刷新 roster） | `pre-prompt-stage.ts:52-58` |
| R4 | `attempts` 从不自增 → `RETRY_BUDGET`/`EMPTY_RESULT_BUDGET` 是死逻辑 | `pre-prompt-stage.ts:52-93` |
| R5 | "失败"与"空结果"被合并成一条；一次超时即定死整个 session 的注入 | `pre-prompt-stage.ts:79-87` |
| R6 | 无会话 id 的 firing 共用 `"anonymous"` 状态文件 → 第一个这样的 session 之后所有 session 都不再注入 | `hooks/pre-prompt.ts:29`, `hook-state.ts:39-41` |
| R7 | 写入不带 `context`/`tags`/`harness`/`source` → 抽取器无法区分"agent 声称修好了"与事实 | `memory-client.ts:74-78` |
| R8 | 单次请求、无 429 退避、超时与"已被接受"不可区分 | `memory-client.ts:80-107` |
| R9 | 无 usage 日志写入者 → `stats` 永远为空 | `stats/usage-log.ts`（全包无 writer） |
| R10 | 无 `autoUpdate`、无 `disabled`、无 `optInOnly`、无 `retainSessions`、无 per-harness/per-path/per-bank 分层 —— 配置面只有 5 个标量 | `config/connection.ts:19-25` |
| R11 | `local` 模式**不启动任何东西**（上游会按需拉起 `hindsight-embed`），装完 `ECONNREFUSED` 到永远 | `connection.ts:112-120` |
| R12 | `hosted` 模式名不副实：忽略 `apiUrl`、无 hosted 默认地址，实际等价于 self-hosted + 强制 token | `connection.ts:186-187,218` |
| R13 | env 只作**兜底**而非覆盖（README 说 override），且 `bankId` 一旦落盘永远压过 env，也没有 `--bank` | `connection.ts:198`, `argv.ts:35-39` |
| R14 | `store.ts:23-34` 对 `config.json`（含 `apiToken`）做非原子、无锁的读-改-写 | `store.ts:23-34` |
| R15 | 技能只有 49 行（上游 730 行），且**指令里的工具名全都不存在**（见 §5.3） | `skill/SKILL.md` |
| R16 | `update claude-code` 静默忽略宿主参数、退出 0；未知 flag 被静默丢弃（还被测试写成"特性"） | `cli/main.ts:34-37`, `argv.ts:74-80` |
| R17 | `--import-conversations` 恒为 no-op | `commands/install.ts:141-150` |

### 4.4 DSH 插件

| # | 问题 | 位置 |
|---|---|---|
| D4 | 写回游标在 await **之后**推进 → 同一批轮次可能二次入库 | `index.ts:331,351` |
| D5 | transcript projection 忽略 `surfaceOp:"replace"` → 压缩(compaction)后重复 + 丢头部上下文 | `transcript-projection.ts:91-99` |
| D6 | 注入块剔除依赖标签；`KNOWLEDGE_BLOCK_TAG` 声明了但从未使用 | `context-blocks.ts:22` |
| D7 | 凭据候选 `WENYUAN_API_KEY` / `WENYUAN_API_TOKEN` 的前者优先，卡片上报的可能是另一个 ref | `index.ts:160-170,254-266` |
| D8 | `peerDependencies: ^0.2.0-rc.2` 不满足运行中的 `0.2.2-alpha.2`；`register()` 返回类型在两个版本间不同，被 `as unknown as` 掩盖 | `index.ts:176-177`, `package.json` |
| D9 | 一个事件里 `turn-stopping` 串行等待上一个写回（`if (writing) await writing`）+ 无整体超时 → 慢网络下会话"停不下来" | `index.ts:247-255` |
| D10 | 无 git 播种、无知识页 roster/preamble、无页面同步、无自动 reflect、无 survey、无 auto-update、无 `disabled`/`optInOnly` | 见 `07-dsh-plugin.md` §3 |
| D11 | `promptTextOf` 把所有 text block 拼成查询，包含其他插件注入的上下文（上游只取 `source.kind==='user'`） | `index.ts:284-292` |
| D12 | 无诊断输出，注入为空与钩子坏掉无法区分 | `index.ts`（无 diag） |

---

## 5. 与上游的能力对照

### 5.1 宿主覆盖：4 / 22

已支持：`claude-code`、`codex`、`codebuddy`、`dsh`。
缺失 18：`opencode`、`opencode2`、`kilo`、`pi`、`prime-agent`、`antigravity-cli`(agy)、`devin-cli`、`cursor-cli`、`copilot-cli`、`grok-build`、`qwen-code`、`kimi-code`、`factory-droid`、`zcode`、`traecode`、`cline-cli`、`dcode`、`workbuddy`。
每个缺失宿主的路径/事件/形状/超时单位/MCP 机制/skill 目录，已逐条列在 `05-hosts-and-mcp.md` §1（可直接当作后续移植清单）。

注意其中 5 个是**固件式插件**而非 hooks（opencode/opencode2/kilo/pi/prime-agent/dcode/cline-cli），移植成本与 hooks 宿主不同，且与 DSH 的 Cordis 插件是同一类。

### 5.2 功能对照（memory 真正生效的部分）

| 能力 | 上游 | wenyuan coding-agents | wenyuan dsh-plugin |
|---|---|---|---|
| transcript 读取 | ✔ 每宿主 reader（22 种格式） | ✘ 不读文件 | ✔ 从 session log 折叠 |
| 知识页注入 / roster / tool guide | ✔ | ✘ | ✘（标签声明了没用） |
| 自动 reflect（含回退链） | ✔ 默认 | ✘ | ✘（只有工具） |
| 按仓库分 bank | ✔ 默认 | ✘ 共用 `default` | ✘ 每进程一个 |
| git 历史播种 | ✔ | ✘ | ✘ |
| 写入 cursor + 去重 + append | ✔ | ✘ 全量重发 | ✔ 有 cursor（但 D4 有竞态） |
| retry/429/超时协调 | ✔ | ✘ | 部分 |
| optInOnly / disabled / per-bank 配置 | ✔ | ✘ | ✘ |
| usage / stats | ✔ | 读端有、写端无 | ✘ |
| auto-update | ✔ | ✘ | ✘ |
| 内置 MCP 工具数 | 8 | 1 | 8 |

### 5.3 Skill 内容不可用

`skill/SKILL.md`（49 行）告诉模型去调：`retain`、`create_knowledge_page`、`search_knowledge_base`、`get_knowledge_page`、`get_knowledge_base_tree`、`reflect`；
而实际存在的是：MCP server 的 `wenyuan_recall`、DSH 插件的 `wenyuan_*` 8 个工具、服务端 MCP 的 `retain/recall/reflect/search_knowledge_base/get_knowledge_page/get_knowledge_base_tree/create_knowledge_page`（**无前缀**，且 DSH 场景下根本没有 MCP）。
→ 技能里的每一条工具调用都可能解析不到；`skill-src/preamble.md:39-42` 还让模型写 "the correction marker" 但**从没说明这个 marker 是什么**。

---

## 6. 建议的修复顺序（按 ROI）

**第 0 步（半天）：停止"打印 wired 却没装"的假成功。**
- `install` 对每个宿主做**读回校验**：写完后重新读宿主配置，确认自己的条目在、形状是宿主认的形状；否则 `ok:false`。
- 让 `hooks.ts` 尊重 `edit.conflict`（现在静默吞掉）、让 `command-line.ts` fallback 返回 `ok:false`。

**第 1 步（1–2 天）：让 Hooks 真的生效。**
1. 改钩子注册形状为宿主原生形状：`{"<Event>":[{"hooks":[{"type":"command","command":"node \"<abs path>\"","timeout":N}]}]}`（`hook-shape.ts:29,73-80`）。
2. Codex 改到 `~/.codex/hooks.json`，并在 `config.toml` 补 `[features] hooks = true`（`codex.ts:30`）。
3. Stop 阶段读 `transcript_path`，实现至少 Claude Code / CodeBuddy(WorkBuddy JSONL) / Codex 三种 reader（`conversation.ts`），做到"挖不到 payload 时读文件"。
4. 写入带 `document_id = conversation:<sessionId>` + `update_mode: append` + `operation_id`，并按 session 记 cursor（`stop-stage.ts`, `memory-client.ts`）。
5. 去掉 Claude 的 `enableAllProjectMcpServers` 副作用（`claude-code.ts:110-130`）。

**第 2 步（2–3 天）：配置与 bank。**
- 加 `bankIdTemplate`（默认 `coding-agent::{gitProject}`）+ `mapPathToBank` + `optInOnly/optInPaths` + `banks.<id>`（`config/connection.ts`）。
- DSH 插件按 `session.header.cwd` 解析 bank（`dsh-plugin/src/index.ts:170,191`），并让 `WENYUAN_BANK_ID` 能到达插件（`settings.ts` 读 env 兜底）。

**第 3 步（3–5 天）：把 memory 变"有内容"。**
- 知识页 roster + tool guide 注入；`autoInject: pages|recall|reflect` 三档；pre-prompt 改每轮 + 预算分离（失败 2 轮 / 空 5 轮）。
- MCP server 补齐 8 个工具（复用 `dsh-plugin` 的工具实现，去掉前缀差异或统一前缀）。
- usage 日志写入 + `stats` 生效；skill 内容按真实工具面重写（>200 行）。

**第 4 步：宿主扩展。** 按 `05-hosts-and-mcp.md` §1 的表，优先 `cursor-cli`、`copilot-cli`、`opencode`（插件式，与 DSH 同构）、`factory-droid`、`qwen-code`。

---

## 7. 测试与门禁的观察

- 338 + 157 个测试全绿，但**没有一条**覆盖：真实宿主的入口形状、`transcript_path` 读取、`document_id` 发送、外来的同事件钩子（`conflict` 分支）、MCP 归属判定跨路径、DSH 多 workspace。
- `argv.test.ts:90-94` 把"未知 flag 被静默丢弃"写成断言 —— 这会把一个缺陷固化。
- 结论：测试度量的是"本包自己定义的行为"，不是"宿主能不能用"。建议补 E2E：每个宿主一个 fake-home 快照，断言**写出的 JSON 能被该宿主文档里的 schema 解析**。

---

## 8. 分册

| 文件 | 内容 |
|---|---|
| `04-config-write-commands.md` | 配置模型（~50 字段对照）、`write/` 原语 W1–W10、CLI、skill 生成 |
| `05-hosts-and-mcp.md` | 22 宿主逐条装配表、4 个已支持宿主的逐项核对、hooks/MCP 缺陷 H1–H11 / M1–M5 |
| `06-runtime.md` | runtime 各模块、19 条 C1–C14 缺陷、10 轴对照、P0/P1/P2 |
| `07-dsh-plugin.md` | dsh-plugin 端到端、B1–B14、与上游 `dsh.ts` 的 17 项对照、跨包重复性结论 |

评审过程中对 `04/05` 的两处结论做了实测复核：**"多行外来数组导致不可解析"不成立**（`insertIntoJsoncArray` 插入点正确）；**"注释旁的删除丢成员 / 尾随逗号插入"成立**。以本文与 04/05 中 `[验证]` 标注的条目为准。
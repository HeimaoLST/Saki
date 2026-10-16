# AGENTS.md — Saki 多 Agent 协作协议

更新日期：2026-10-16。

> 本文件对在本仓库工作的所有 agent（AI 或人类协作者）生效。
> **本仓库由多条业务线共同维护，采用两层协作。** 独立业务线／独立任务之间通过 GitHub 分派、交接与交付；同一任务内的主 agent／子 agent 通过 tmux 直接协作。
> **GitHub 管任务与交付，tmux 管主子执行。** Issue／PR／Label／Milestone 保存共享任务、跨线依赖与正式证据；主 agent 使用 pane split、send-keys／paste-buffer、capture-pane 分派、指导和监督子 agent，由主 agent 汇总关键结论到 GitHub。
> **用户明确指令优先于本协议默认流程**（§0）；用户对当前任务的授权不需要先写 issue 才生效，但执行后要如实补记。
> 本文件只定义协作规则。**任务列表以 GitHub issue 为准，不要在本文件维护 TODO。**

给没时间读完的 agent，八条底线：

1. 先认领 issue 再动手；一切改动走 PR，不直推 `master`／`dev`（用户明确授权的当次例外除外，见 §0）。
2. 项目按五条业务线划分；使用稳定实例 ID（如 `agent:runtime#kira`），不冒用身份。tmux 地址只作当次定位，不能当作实例身份。
3. 按业务线与任务声明的范围修改；跨线代码先交接，随模块维护事实文档见 §3.3。
4. 标准开发任务由主 agent 负责 commit／push 闭环，可明确分派执行；只读、用户要求不提交或操作受阻时按 §5 记录，不丢弃现场。
5. 行为变化必须带确定性测试与证据，证据贴在 issue／PR 里。
6. 每个 issue 同一时间只有一个主责实例，归属由唯一 `agent:*` 路由；主 agent 可在该范围内通过 tmux 分派子任务，子 agent 不重复认领。
7. 长期事实（需求、设计、决策、实验结论）写回 `docs/`；issue 只承载过程，不散落临时笔记。
8. 开发在独立 `git worktree` 中进行，主检出固定在 `master`；合并后回收自己已合并且无依赖的临时工作分支与 worktree，保留 `master`／`dev`。

---

## 0. 优先级与授权（先读）

### 0.1 优先级顺序

用户（含人类 maintainer `@HeimaoLST`）对当前任务的明确指令 **高于本协议默认流程**；本协议高于个人习惯。收到明确指令时按指令执行，并在能访问 GitHub 时把授权范围、与默认流程的差异以及执行结果记录到对应 issue／PR 评论；执行本身不以「先写 issue」为前提。

- **review／audit／解释／调研默认只读**：只报告与提案，不修改文件、不 commit／push、不开 PR；提案 ≠ 实施授权，「去看一下」≠「去改」。独立主责被明确指定到研究／评审 issue 时，可按范围认领和记录；普通只读请求不自动进入开发流程，子 agent 按 §2.2 本地回报。认领本身不授权修改产线文件。
- **用户明确要求「直接改 master」「不建 worktree」「不用 commit」等，是当次任务的范围例外**：只对本次任务生效，不改变默认流程，不构成永久豁免，也不扩大到无关实例或后续任务；用户／监督者在授权范围内明确分派给具体实例的实施不受影响。任务完成后恢复默认模式。
- **如实记录，不虚报**：无法访问 GitHub 时，用户已授权且与他人无冲突的工作可以继续；不得声称「已认领／已看过 issue」。恢复访问后第一时间补认领与进度记录，注明离线时段与执行范围。
- **master 是生产可用稳定分支，dev 是开发集成分支**：feature／普通修复进 `dev`；`dev` → `master` 走发布 PR（`Refs`，不写 `Closes`）；hotfix 基于 `master` 开出并及时回流 `dev`。

### 0.2 哪些动作已被授权

- **标准开发任务的主责实例**（已认领 issue 且有实现范围）默认授权：在独立 worktree 内编辑、小步 commit、push 自己的分支、开 PR、按 §10 补充证据。主 agent 可把已授权范围内的具体实施分派给自己的子 agent，不扩大业务线或任务范围；子 agent 的 Git 动作以主 agent 的明确分派为准。
- **不默认授权**：合并 PR／开启 auto-merge、打 tag／发 Release／部署、修改他人分支或 worktree、跳过仓库必需门禁。合并只在用户明确要求合并某个 PR 时执行（§5 步骤 9、§7）。
- 只读任务只需要巡检、分析、报告与评论，不需要 worktree、commit 或 PR；只有在获得修改授权、且确实要留下持久文档时，才转为提交交付，不为写报告而自动创建文档。

### 0.3 快速开工（复制给新 agent）

不同 agent 工具（pi／Claude Code／Codex 等）对 `AGENTS.md` 的加载方式不一致，**不要假设它已自动进入上下文**：启动时应主动完整读取所在工作区版本的本文件，并核对该 worktree／主检出实际所在的分支与路径，避免套用旧的或别处的快照。启动时给它下面这段，显式指定业务线与实例名：

```text
你是 agent:<line>#<name>。先完整阅读 AGENTS.md，确认自己是主 agent 还是子 agent。
主 agent 按 §5 核对用户指定的任务或已授权的队列范围，巡检 ready/review 与 blocked 状态，
确认 DoR 与唯一主责后认领，按 §2 通过 tmux 分派和监督自己的子 agent。
子 agent 只执行本地分派，不另行巡检、认领或通过 GitHub 与主 agent 对话。
只读任务交付报告；文件修改在任务 worktree 内完成。主 agent 负责提交、推送与 PR 闭环，
可明确分派其中步骤，base 按 §7 选择。不得自行合并或清理其他实例资源。
```

- pi 用户可直接用项目模板：`/start runtime#kira`（见 `.pi/prompts/start.md`）。
- 全局习惯可写到用户级 `~/.pi/agent/AGENTS.md`：「在 Saki 仓库工作时先完整阅读仓库根目录 AGENTS.md」。
- 开工时核对实际文件，不只靠口头复述；规则变更默认走 PR，用户当次例外见 §0。

## 1. 业务线、实例与地盘

### 1.1 五条业务线

项目按五条业务线组织，业务线的标识就是 `agent:*` 标签。每个 issue 的归属是**业务线**。

| 业务线标签 | 业务线 | 职责 | 拥有的路径（可写） |
| --- | --- | --- | --- |
| `agent:runtime` | Agent Runtime 开发 | Agent 执行循环、模型调用、上下文压缩、会话存储、公共协议、模型目录、应用组装、内置工具（read/bash/edit/write）与输出边界 | `src/agent/`、`src/client/`、`src/protocol/`、`src/model/`、`src/session/`、`src/tools/`、`src/app.rs`、`src/main.rs`、`Cargo.toml`、`Cargo.lock` |
| `agent:tui` | TUI 开发 | TUI、console、输入与展示适配、本地命令系统 | `src/frontend/`、`src/command/` |
| `agent:gui` | GUI 开发 | Go 桌面 contacts 设计预览（独立 module，不接入 Rust core） | `desktop/` |
| `agent:qa` | 测试 | 测试策略、跨模块与端到端／PTY 验收、CI 门禁需求与结果核对、发布前验证 | `tests/` 中跨模块与验收部分；**只读产线代码** |
| `agent:pm` | 产品经理 | 产品方向、需求边界、优先级、triage、验收标准、文档结构与索引、Milestone、协作工具脚本 | `docs/`、`README.md`、`AGENTS.md`、`.github/`、`.pi/prompts/`、`scripts/` |

### 1.2 业务线与实例：一条业务线可以有多个 agent

- `agent:<line>` 是**业务线标签**（如 `agent:runtime`），写在 issue 上，表示归属；它不代表某一个具体 agent。
- 每个 agent 会话有唯一的**实例 ID**：`agent:<line>#<name>`，例如 `agent:runtime#kira`、`agent:tui#nova`。实例名在启动会话时确定，同一会话／同一段工作保持稳定。
- **一个实例同一时间只属于一条业务线**；一条业务线可以同时运行多个实例（例如两个 runtime agent 分别做会话和多 session 并发）。多实例并行的写冲突规则见 §9。
- 消息头、PR 声明、commit 尾注必须写实例 ID；issue 的 `agent:*` 标签只到业务线一级。
- `TO:` 写业务线 = 该业务线任意实例均可接；写具体实例 = 点名叫人。
- 主／子是当前任务的执行关系，不是新业务线。子 agent 有独立实例 ID，其任务、可写路径与权限由主 agent 明确分派；更换 pane 不改变实例身份，改做独立任务才按相应业务线重新登记。

### 1.3 补充约定

- 单模块测试文件位于 `tests/<module>/`，通过 `#[path]` 挂载进 `src/` 对应模块，因此**归该模块所属业务线维护**（例如 `tests/tools/mod.rs` 归 `agent:runtime`）。`tests/support/mod.rs`、`examples/growth_experiment.rs` 等跨模块测试支撑与实验入口归 `agent:runtime` 维护。QA 负责跨模块、PTY、端到端与验收测试；发现产线缺陷时开 `type:bug`，不直接改 `src/`。
- 需要动别人地盘时，开 issue 并 `KIND: handoff`（§8）。
- 人类协作者（当前 `@HeimaoLST`）是最终仲裁者，负责产品方向、密钥与权限；日常流转不经过人类。
- 本协议不绑定具体 agent 工具（pi／Claude Code／Codex 等）或模型；任何会读 `AGENTS.md` 的执行体都按本协议工作。

## 2. 沟通协议：GitHub 管跨线协作，tmux 管主子执行

### 2.1 使用哪条通道

| 场景 | 通道与责任 |
| --- | --- |
| 跨独立业务线的需求、bug、依赖、交接、独立评审／QA 请求 | GitHub issue／PR，按唯一 `agent:*` 路由 |
| 同一任务内主 agent 给子 agent 派活、补充要求、纠偏、询问进展、读取结果 | tmux 直接交互，不经 GitHub 中转，不为每个内部步骤新建 issue |
| 阶段成果、会影响他人的决定／阻塞、最终提交与验收证据 | 主 agent 核对后汇总到对应 issue／PR；不要求子 agent 同步发重复评论 |

**不能把自己的子 agent 当作 GitHub 上的另一条业务线来对话。** 独立 QA 团队的子 agent 也先向自己的 QA 主 agent 返回结果，由该主 agent 对外提交真实证据并注明实际执行／评审身份；实施团队内部核查不替代独立 QA。跨独立团队的正式交接继续走 GitHub。

### 2.2 主子 agent 的 tmux 工作循环

1. **先确认关系与当前目标。** 主 agent 核对自己实际所在的 session／window，发现已有子 agent；需要新建时，在已核实的主 pane 下 `split-window`，接收命令返回的 pane ID。既有 pane 必须核对当前进程、工作目录、实例身份、任务和空闲／执行状态；同名窗口、相同目录或“第几个 pane”都不足以证明归属。`TMUX_PANE` 可能从旧进程继承，不能单独作为当前会话依据；未定位清楚前不发送任务，也不打断候选 pane。
2. **绑定稳定身份。** 首次核实归属后，为主 pane 和子 pane 分别设置各自的 `@saki_agent_id`；子 pane 另设 `@saki_parent_agent_id` 和 `@saki_task`（issue 引用或任务名）。这些是当前 tmux 运行的定位元数据，不能给未核实的他人 pane 覆盖标识。pane 重建／复用后重新确认和绑定。
3. **每次操作前重新发现。** 根据稳定实例 ID、父实例 ID 和任务查询当前 pane，读回元数据并用 `capture-pane` 核对状态；只有唯一匹配且身份／任务一致时才操作。遇到缺失、重复或任务不符，先恢复映射，不沿用记忆里的数字或静态笔记。session／window 索引可能变化；pane ID 在其存活期内稳定，但进程复用、pane 重建或 server 重启后不能视为同一 agent。
4. **直接分派与监督。** 主 agent 通过 `send-keys -l` 发送短指令；长文本存为本地任务文件，用独立命名的 buffer 和 `paste-buffer -p` 送入，确认输入后再单独发送 Enter。任务写明目标、范围、验收、当前目录／分支及允许的 Git 动作。用 `capture-pane` 获取回执和结果，必要时继续指导；投递成功不等于子 agent 已理解或完成。子 agent 空闲或正在执行本任务时才投递，不覆盖其另一个任务。
5. **结果回到主 agent。** 子 agent 在 pane 中报告进展、阻塞和结果；长日志放任务临时目录，并回传路径、SHA、命令／退出码与限制。默认不操作 GitHub、不自行领其他 issue、不向其他业务线派活。主 agent 复核后负责对外记录和后续流转；原始执行／评审归属如实标明。

动态发现示例（输出中的地址仅供当前操作，不能抄进长期交接作为路由）：

```sh
tmux list-panes -a -F '#{pane_id} agent=#{@saki_agent_id} parent=#{@saki_parent_agent_id} task=#{@saki_task} session=#{session_name} window=#{window_id} pid=#{pane_pid} cmd=#{pane_current_command} cwd=#{pane_current_path}'
```

下列变量必须来自本次核实的唯一匹配，不是固定编号；buffer 使用本主 agent／任务的独立名称。确认应用支持 bracketed paste，避免多行任务被拆成多次提交：

```sh
tmux capture-pane -p -t "$child_pane" -S -60
tmux load-buffer -b "$dispatch_buffer" "$task_file"
tmux paste-buffer -p -b "$dispatch_buffer" -t "$child_pane"
# 核对输入与状态后，再单独执行
tmux send-keys -t "$child_pane" Enter
```

**GitHub 和长期文档使用稳定 agent ID、issue、分支与 commit 识别任务；不保存固定 session／pane 编号来指挥后续操作。** 旧日志中的地址只是当时快照。tmux 命令语义见[官方手册](https://man.openbsd.org/tmux)，执行时以本机版本为准。

### 2.3 GitHub 对外消息与交付

- **Issue** = 一个任务 + 一条消息线 + 工作日志 + 决策记录。
- **Comment** = 一条消息。
- **PR** = 一次交付，必须关联 issue。
- 主 agent 将重要进展、失败、决定与交接证据汇总到对应 issue；子 agent 的 pane 对话和本地过程日志无需逐条上传。共享任务与正式结论不能只留在本地。

跨独立业务线的 GitHub 消息以结构化头开始，便于检索；`FROM` 写实际发帖实例。主 agent 汇总子 agent 结果时，在正文注明执行／评审实例与证据，不冒用其身份：

```
TO: agent:tui#nova
FROM: agent:qa#rhea
KIND: test-request
REFS: #42, 1a2b3c4, src/frontend/tui/view.rs:120
```

| `KIND` | 含义 | 必须包含 |
| --- | --- | --- |
| `bug` | 行为与预期不符 | 复现步骤、期望／实际、原始输出证据 |
| `feature` | 新能力需求 | 用户价值、可验证的验收标准、非目标 |
| `test-request` | 需要补充或执行测试 | 被测行为、确定性验收场景、允许的 mock 边界 |
| `handoff` | 独立任务的主责移交（跨线换 `agent:*`，同线不换） | 交接清单（§8）；主子内部派活按 §2.2 |
| `decision` | 需要架构／协议决策 | 候选方案、影响面、需要哪些业务线 `ACK` |
| `question` | 需要澄清 | 已确认事实（带证据）、阻塞了什么 |
| `status` | 例行进度、计划、阻塞变化 | 已完成／下一步／阻塞 |
| `review` | 评审结论 | `APPROVE` 或 `REQUEST-CHANGES` + 理由 |
| `blocked` / `unblocked` | 阻塞声明与解除 | `BLOCKED-BY: #N` / `UNBLOCKS: #N` |

评论礼仪：

- 一条评论一个主题；先给证据（命令、输出摘要、commit、`文件:行号`），再给判断。
- 明确写出「我需要什么」与接收方，不要把待办埋在叙述里。
- 结论变化时发新评论说明，不要改旧评论掩盖历史。
- 重要里程碑、决策、阻塞、验收证据必须记录在 issue／PR；临时终端输出与例行进度不必逐条评论，汇总后再发。
- 用户指令与协议默认不同的执行方式，在评论里写明授权范围与差异；记录可以事后补写，但不因此不执行用户指令。
- 确实需要人类决定或新增权限、费用时，`@HeimaoLST` 并加 `needs:human`；已有授权范围内无需重复确认。

## 3. 文档与事实记录：`docs/` 是唯一长期事实来源

Issue 和 PR 是**过程与沟通**的载体，`docs/` 是**长期事实**的载体。两者不能互相替代。

### 3.1 什么内容写到哪里

| 内容 | 唯一归属 |
| --- | --- |
| 需求边界、行为需求交接 | `docs/requirements/` |
| 设计、架构、接口契约、决策结论（含 RFC 落地） | `docs/design/` |
| 外部系统调研、源码／文档核对、证据整理 | `docs/research/` |
| 实验协议、真实运行结果、结论边界 | `docs/experiments/` |
| 阅读入口、文档索引与分类规则 | `docs/README.md` |
| 迭代计划、优先级、实时进度、讨论过程 | GitHub（Milestone／issue／Projects） |

### 3.2 强制规则

- **结论必须沉淀**：issue 关闭前，把长期有效的结论回写到 `docs/` 对应位置，并在关闭评论中链接；确实不需要沉淀的（纯重复性维护、一次性修复）要在关闭评论里说明原因。
- **不要把内容写得到处都是**：不在仓库根目录、`src/`、`tests/`、`desktop/` 或 `.saki/` 下创建临时 md、plan、笔记或备忘文件；临时草稿放 issue 评论或本地不入库的文件。
- **一个主题一个文件**：按主题归类，更新已有文档优先于新建；不为一次对话、一天或单个发现创建孤立文件。
- **不复制粘贴**：一份内容只有一个权威位置，其他地方用链接引用。
- **命名与结构**：文件用英文 `kebab-case`，放入上表对应目录；新增目录结构前先与 `agent:pm` 确认。

### 3.3 文档维护责任

- **每条业务线维护自己的模块事实文档**：行为、接口、配置发生变化时，在同一 PR 更新对应主题；确实需要后续工作时链接 follow-up issue，不允许无声漂移。这属于模块交付范围，不需要另做跨线交接。新增文件前查找已有主题；新增目录或调整文档分类先与 `agent:pm` 确认。
- `agent:pm` 负责需求、产品决策、文档结构与索引、一致性巡检以及 `.github/` 配置；模块实现者负责核实和撰写实现事实。
- 文档标注**更新日期**；事实尽量固定到 commit 或版本，避免按过时快照继续设计。
- 沿用 `docs/README.md` 的结论分级：**实现事实 / 作者报告 / 设计推论与提案 / 待验证**。每条结论标明属于哪一类，不把提案写成已批准，也不把一次运行写成普遍结论。
- 发现文档与代码不一致时，开 `type:bug` 或 `type:docs` issue 给对应业务线。

## 4. 标签体系（Label = 路由与状态）

### 类型 `type:*`（分诊完成后恰好 1 个）

| 标签 | 用途 |
| --- | --- |
| `type:bug` | 行为与预期不符，需要修复 |
| `type:feature` | 新能力或行为增强 |
| `type:test` | 需要补充／修复的测试或验收 |
| `type:refactor` | 不改变外部行为的重构 |
| `type:docs` | 文档新增或修正 |
| `type:research` | 调研、实验、证据核对 |
| `type:decision` | 架构／协议决策（RFC/ADR） |
| `type:question` | 需要澄清的问题 |

### 状态 `status:*`（每个 issue 恰好 1 个）

| 标签 | 含义 |
| --- | --- |
| `status:triage` | 待分诊：补信息、定优先级、定归属业务线 |
| `status:ready` | 已就绪，等待负责业务线认领 |
| `status:in-progress` | 唯一主责实例正在组织执行，可通过 tmux 分派子任务 |
| `status:review` | 待评审、验收或授权合并；评审通过后未集成仍保留此状态 |
| `status:blocked` | 被依赖或外部条件阻塞；写明原因、解除条件，有 issue 依赖时写 `BLOCKED-BY: #N` |
| `status:done` | 验收通过且交付完成，等待关闭或已关闭 |

### 归属 `agent:*`（分诊完成后恰好 1 个）

即 §1.1 的五条业务线，**当前负责业务线唯一**。独立任务跨线移交换标签，同线主责交接不换标签（§8）；主子内部派活不改标签、不另认领，按 §2.2 执行。

### 优先级 `priority:*`（triage 时确定）

| 标签 | 含义 |
| --- | --- |
| `priority:p0` | 阻断、安全或数据损坏，立即处理 |
| `priority:p1` | 当前迭代必须完成 |
| `priority:p2` | 计划内，可排队 |
| `priority:p3` | 低优先，有空再做 |

### 协作需求 `needs:*`（可叠加，表示「等对方产出」）

| 标签 | 含义 |
| --- | --- |
| `needs:design` | 需要设计或决策产出 |
| `needs:review` | 需要代码／文档评审 |
| `needs:test` | 需要补充或执行测试验证 |
| `needs:info` | 需要补充信息 |
| `needs:repro` | 需要最小复现 |
| `needs:human` | 需要人类决策／介入 |

### 流转与其他

| 标签 | 含义 |
| --- | --- |
| `handoff` | 独立任务主责交接进行中，不用于主子内部派活 |
| `epic` | 跨多个 issue 的目标追踪 |
| `platform:rust` / `platform:go` / `platform:ci` | 涉及平台（可选补充） |
| `good first issue` / `help wanted` / `duplicate` / `invalid` / `wontfix` | 保留的 GitHub 默认标签 |

强制规则：

- 每个 issue 始终只有一个 `status:*`。`triage` 阶段允许类型、归属或优先级待定；进入 `ready` 前必须各有一个 `type:*`／`agent:*`／`priority:*`，并挂到当前迭代 Milestone（尚无对应迭代时由 PM 记录安排）。PM 主导补全；已有归属按交接规则变更，不擅自抢占，不把标签当作认领锁（§9）。
- 禁止随手创建新标签；需要新标签时开 `type:decision` issue，通过后由 `agent:pm` 创建。
- 默认标签 `bug`、`enhancement`、`documentation`、`question` 已被 `type:*` 取代，请勿再使用。
- **triage 由 `agent:pm` 主导**：新 issue 在 1 个工作日内定 `type:*`、`priority:*`、`agent:*`；无法定归属时保持 `status:triage` 并 `needs:info`。

### 状态机

```text
                    ┌──────────────────────────────────────────┐
                    ▼                                          │
(新建) ──► triage ──► ready ──► in-progress ──► review ──► done ──► closed
                       ▲           │    ▲         │
                       │           ▼    │         │
                       └────── blocked ─┴─────────┘
```

| 迁移 | 执行者 | 条件 |
| --- | --- | --- |
| 新建 → `triage` | 创建者 | 默认状态 |
| `triage` → `ready` | 分诊者（`agent:pm` 主导） | 满足 Definition of Ready（§6） |
| `ready` → `in-progress` | 负责业务线的主责实例 | 评论认领（可说明主子分工）并移除 `ready`；子 agent 不重复认领 |
| `in-progress` → `review` | 交付者 | 已开 PR（或报告／决策结论已就绪），证据齐全 |
| `review` → `in-progress` | 评审者 | `REQUEST-CHANGES` + 理由 |
| `review` → `done`（实现类） | 评审者 | `APPROVE` + CI／门禁绿 +（若有 `needs:test`）QA 验收通过 + **已合入目标集成分支**；评审通过但尚未合并时保持在 `review` |
| `review` → `done`（非实现类） | 评审者 | research／question／decision／纯验收**未产生文件修改**时，以报告、决策评论或验收结论作为完成证据，不要求 PR；若产生持久文档修改，则按实现类走提交交付并在合入后 `done` |
| `done` → 关闭 | 交付者或 QA | 关闭评论附最终证据链接；`Closes` 未生效时，仅当改动已进入最终目标分支且验收通过才附证据直接关闭，不另造“收尾 PR” |
| 任意 → `blocked` | 负责实例 | 正文写 `BLOCKED-BY: #N`（有真实 issue 依赖时）或具体原因与解除条件 |
| `blocked` → `ready`／`in-progress`／`review` | 阻塞解除方 | 评论 `UNBLOCKS: #N`，恢复到阻塞前合适的阶段，不丢失评审上下文 |
| `triage` → 关闭 | 分诊者 | duplicate／invalid／wontfix，必须给理由 |

`epic` 追踪型 issue 自身不要求 commit／PR，按子任务完成情况流转与关闭。

## 5. 标准工作循环

本节由独立任务的主责／主 agent 执行。主 agent 通过 §2.2 给子 agent 分派具体步骤，子 agent 不另走 GitHub 巡检、认领与汇报循环。

0. **核对范围与状态**：先确认用户授权、当前分支与脏文件来源。需要开发时 `git fetch origin`；普通 feature／修复从最新 `origin/dev` 开分支，hotfix 从 `origin/master` 开分支。只读检查不要求 fetch 或创建分支。
1. **巡检**（每次开工前必做）：

   ```sh
   # 等我业务线认领的
   gh issue list --state open --label "agent:<line>,status:ready"
   # 等我业务线评审的
   gh issue list --state open --label "agent:<line>,status:review"
   # 仓库里被阻塞的
   gh issue list --state open --label status:blocked
   # 搜索发给我的实例/业务线的消息（评论正文）
   gh search issues --repo HeimaoLST/Saki --state open --match comments "TO agent:<line>"
   # 查看自己的 worktree 是否遗留（不要运行全局 prune 清理他人资源）
   ./scripts/agent-worktree.sh list
   ```

2. **认领**，由唯一主责留下消息（实例 ID、文件范围；有子 agent 时说明分工，无需 pane 地址）：

   ```sh
   gh issue edit 42 --remove-label status:ready --add-label status:in-progress
   gh issue comment 42 --body "TO: agent:runtime
   FROM: agent:runtime#kira
   KIND: status
   REFS: #42

   已认领。文件范围：src/session/mod.rs。计划：…；预计…"
   ```

   认领后读回状态，确认无其他独立实例占用同一范围。一个 issue 有唯一主责，可分派子 agent 实施；子 agent 在 tmux 确认范围，不重复改标签或发认领评论。文件写入不得重叠，独立评审可并行只读。

3. **开 worktree 与分支**：`./scripts/agent-worktree.sh add agent/<line>/<issue>-<slug> [base]`（base 默认 `origin/dev`；默认根目录 `$HOME/Workspace/worktrees/Saki`，可用 `SAKI_WORKTREE_ROOT` 覆盖），例如 `agent/tui/142-scroll-fix`；hotfix 用 `origin/master` 作 base，stacked 用直接父分支。**不要在主检出 `git switch`**；主检出固定 `master`。只读任务不需要 worktree。
4. **实施与监督**：主 agent 在 tmux 给出范围并持续检查子 agent 的进展／结果，子 agent 直接回报；每个可解释的小步骤及时落盘，Git 提交按下一步分工执行。跨线影响、阶段成果或真实阻塞由主 agent 汇总到 issue，内部指导不逐条发评论。
5. **提交**：主 agent 对 commit／push 闭环负责，可明确分派给子 agent；提交署实际执行身份，记录实现分工与 SHA。交接／阻塞前保存可复核的阶段成果。只读或用户要求不提交时不执行；操作受阻报告并保留现场，不丢弃改动或夹带他人文件。
6. **交付**：有文件改动时按 `.github/pull_request_template.md` 开 PR，base 与 `Closes`／`Refs` 按 §7 选择；将对应 issue 置 `status:review`、加 `needs:review`，点名非作者评审。只读任务交付报告。未完成或自身检查失败的改动可开 draft PR，明确剩余工作；不自动开启 auto-merge。
7. **评审**：评审者用 `KIND: review` 评论给出结论、理由与所审 SHA。参与该交付设计／修改的人可以指导和验收，但不能作为唯一独立评审；作者不得自行宣布通过或冒用他人 `APPROVE`。内容评审与仓库门禁分别记录，不能把局部通过写成所有门禁通过。评审不等于合并授权。
8. **验收**：标了 `needs:test` 的，由独立 QA 任务验收。其主 agent 可通过 tmux 分派只读核查，汇总实际评审身份、SHA、证据与限制到 PR；实现类 issue 在通过且已集成后才 `done`。参与交付设计／修改的主子关系不能充当该交付的唯一独立评审。
9. **合并与清理**（合并需要单独授权）：
   - **授权**：必需门禁通过 + 独立评审 `APPROVE` 是质量条件；只有用户明确要求合并该 PR 时才合并／开启 auto-merge。合并方法见 §7。广泛的「非关键决策直接处理」不推导为合并授权，合并发布 PR 也不自动授权 tag／Release／部署。
   - **同步**：合并前把 PR 同步到最新 base——未发布的本地分支可以 rebase；已推送／开 PR 的共享分支用 merge commit 合入 base（不 force-push、不覆盖远端）。同步后重新跑门禁，并在证据／评审评论注明验证 SHA。
   - **stacked**：从底层向上合并；底层合并后核对上层 PR 的实际 base，必要时改到下一个目标（通常为 `dev`）。改 base 或删除父分支不保证上层 diff 正确，尤其底层使用 squash 时；作者需同步并核对 diff、重新验证后再请求合并。中间层用 `Refs`，进入最终目标分支才可 `done`。
   - **`Closes #N`**：只在 base 为默认分支（`dev`）时生效；发布 PR（`dev` → `master`）用 `Refs` 列出 issue。若 `Closes` 未生效，只有当改动**已进入该 issue 要求的最终目标分支且验收通过**时才在 issue 评论附证据直接关闭，不需要另造“收尾 PR”；仅合入 stacked 中间层、尚未进入最终目标分支的不能关闭。
   - **清理**：待评审／待合并保留干净 worktree 与分支。确认自己的 `agent/*` 临时分支内容已合入最终目标、没有后续提交、脏文件、活跃实例或上层依赖后，再按 §9 回收。`master`／`dev` 永久保留；不清理他人资源，不要求全局 worktree 列表为空。

> 检查按 §10 的影响范围执行；实际配置为 required 的 CI 检查仍必须通过，不能自行跳过。CI 尚未接入时附对应命令与输出。无关基线失败单独记录，不阻止提交可审查 PR 或内容评审；不能据此声称仓库全绿。发布到 `master` 前执行全套仓库检查与本次发布的回归验收。

## 6. Issue 类型与必须内容

仓库提供以下 issue 模板（`.github/ISSUE_TEMPLATE/`），创建 issue 时按类型选择：

| 模板 | 默认标签 | 用途 |
| --- | --- | --- |
| Bug 报告 | `type:bug` + `status:triage` | 复现、期望／实际、证据、是否可离线复现 |
| Feature 需求 | `type:feature` + `status:triage` | 用户价值、验收标准、非目标、涉及层 |
| 测试／验收请求 | `type:test` + `status:triage` | 被测行为、确定性验收场景、mock 边界 |
| Agent 任务交接 | `status:triage` + `handoff` | 原任务优先在原 issue 交接；新任务按目标补类型与归属 |
| 决策／RFC | `type:decision` + `status:triage` | 候选方案、影响面、需要谁 ACK |
| 澄清问题 | `type:question` + `status:triage` | 已确认事实、阻塞点、期望答案形式 |

模板创建后处于 `triage`，由 PM 补齐类型、归属、优先级与迭代安排。是否需要 PR 取决于交付是否修改文件，不取决于 issue 类型；只读报告无需 PR，研究产生文档修改则仍需提交交付（§4）。

**Definition of Ready**（满足后才能进 `status:ready`）：

- 目标单一，有可验证的完成标准（不是「优化一下」）。
- 已确定唯一 `type:*`／`agent:*`／`priority:*`，并挂当前迭代 Milestone（里程碑尚未建立时由 `agent:pm` 先建立或说明例外）。
- 写明业务线、唯一主责与路径；有子 agent 时由主 agent 明确各自范围。主子内部步骤不单独建 issue，能独立交付／验收或跨独立业务线的任务再拆分。
- 写明证据要求（离线确定性测试／真实模型运行／PTY／截图／报告或决策结论）。
- 依赖已识别；有真实 issue 依赖时写 `Blocked-by #N`／`Blocks #N`，无依赖写「无」，其他外部条件写明解除方式。

**Definition of Done**（满足后才能进 `status:done`）：

- 验收标准逐条满足，证据附在 issue／PR 评论；实现类 issue 已合入目标集成分支。
- 相关门禁通过，回归覆盖到位；测试按 §10 适用范围记录（不适用项写 N/A 与理由，不强制补无关测试）。
- 改动已全部 commit 并 push，工作区无未提交残留，issue 中有 commit hash；只读或用户明确不提交的任务注明「不提交」及范围。
- 行为变化同步了相关既有 `docs/` 文档，或已开 follow-up issue 并链接；非实现类 issue 的长期结论已沉淀或说明无需沉淀。
- 未提交密钥、token、`.env` 或运行产物；共享文件改动已在 PR 中声明。
- issue／PR 中有完整的执行记录，收尾者能独立复现结论。

## 7. 分支、提交与 PR

**分支模型（三层）**：

| 分支 | 角色 | 规则 |
| --- | --- | --- |
| `master` | 生产可用稳定分支 | 只接收已验证的发布 PR（`dev` → `master`）和基于 `master` 的 hotfix PR；禁止 feature 分支直接合入 |
| `dev` | 开发集成分支（默认分支） | 普通 feature／bug／docs／test PR 的最终目标；合并须门禁通过；`Closes #N` 在此生效 |
| `agent/<line>/<issue-number>-<short-slug>` | 临时工作分支 | 通常从 `origin/dev` 开出；hotfix 从 `origin/master`、stacked 从直接父分支开出；合并后满足 §9 条件再回收 |

- **PR base 必须与实际目标一致**：feature／bug／docs／test → `dev`；hotfix → `master`；stacked → 直接父分支（上一层 PR 的 head），正文只写 `Refs`。base 选错先改 base 并重新验证，不在错误 base 上合并。
- **同步集成分支**：未发布的本地分支可以 rebase 到最新 base；已推送／开 PR 的共享分支用 merge commit 合入 base，不 force-push、不覆盖远端。同步后重新验证最新 HEAD，并在证据／评审中注明 SHA。参考官方说明：[Linking a pull request to an issue](https://docs.github.com/en/issues/tracking-your-work-with-issues/using-issues/linking-a-pull-request-to-an-issue)、[Keeping your pull request in sync with the base branch](https://docs.github.com/en/pull-requests/how-tos/create-pull-requests/keeping-your-pull-request-in-sync-with-the-base-branch)。
- **合并方法**：普通短期工作分支默认 squash merge；`dev` → `master` 的发布 PR、`master` → `dev` 的回流 PR 使用 merge commit，保留长期分支的共同历史，禁止 squash／rebase 这两个长期分支。原因见 [GitHub 对长期分支 squash 的说明](https://docs.github.com/en/pull-requests/reference/pull-request-merges#squashing-and-merging-a-long-running-branch)。
- **发布**：开 `dev` → `master` PR，标题 `[release] <范围>`，用 `Refs` 列出相关 issue，附目标版本、验证与回归结论。只有门禁与验收通过且用户明确授权该 PR 后才合并；保持 `dev` 分支，不随发布清理。tag／GitHub Release／部署按用户授权的发布范围执行，不从合并授权自动推导，也不重复询问已授权的步骤。
  - tag 使用 annotated `vX.Y.Z`，与 `Cargo.toml` 的 `version` 对齐，指向已验证的 `master` 发布提交。
  - GitHub Release notes 写变更摘要、验证结果与已知限制。
- **hotfix**：`agent/<line>/hotfix-<slug>` 从 `origin/master` 开出，PR 目标 `master`；合入后开 `master` → `dev` 的回流 PR，验证并取得该 PR 的合并授权，避免修复只存在于生产分支。获授权发布时递增 patch 版本。
- 默认不直推 `master`／`dev`，开发在独立 worktree 的工作分支进行；用户当次明确例外见 §0。

**提交**：主 agent 负责组织实施、提交与推送，可明确委托子 agent 执行；只读、用户要求不提交或操作受阻时按 §0／§5 记录。规则：

- **小步提交**：一个可解释的最小步骤一个 commit；不攒巨型提交，也不留「本地改完等别人提交」。
- 完成提交应保证受影响模块能编译、相关测试通过。未完成的阶段成果可保存为明确标注 WIP 的 commit／draft PR，附失败与剩余工作，不请求通过或合并；基线失败与自身引入的失败分别记录。
- commit message 用 Conventional Commits + scope + issue 引用 + 实例尾注：

```text
fix(tui): keep viewport position after resize

Refs: #142
Agent: agent:tui#nova
```

- 一次提交只包含一个 issue 的改动；发现无关脏文件先查明来源，不顺手提交。
- 提交前检查 `git status` 与 `git diff --staged`，确认不含密钥、`.env`、`.saki/` 运行产物或临时文件。
- 除非用户明确要求重写历史，否则不 force-push。用追加 commit 响应评审、merge commit 同步已共享分支；普通 push 被拒为 non-fast-forward 时核对差异并报告，不强推覆盖。
- 交接、阻塞或结束前记录自己的阶段成果、验证状态与 SHA；不删除改动来制造干净状态，不处理其他实例的脏文件。

scope 建议：`agent`、`tools`、`tui`、`console`、`command`、`session`、`client`、`protocol`、`model`、`desktop`、`docs`、`ci`。

**PR 规则**：

- 标题：`[#142] fix(tui): 一句话说明`。
- 正文使用 `.github/pull_request_template.md`，包含 issue 引用、动机、改动、证据、风险、回滚方式与 `FROM`／`REVIEWER` 实例。
- 普通 PR 解决一个目标，排除无关修改；发布／回流 PR 可以汇总多个已交付 issue，用 `Refs` 列出。
- **关闭 issue**：目标为默认分支 `dev` 且该 PR 完成任务时写 `Closes #N`；stacked 中间层、发布、hotfix 和回流 PR 用 `Refs #N`。自动关闭未生效时，只有改动已进入任务规定的最终目标分支且验收通过，才附证据手动关闭；不为关闭 issue 创建空 PR。
- **资源回收**：待评审／待合并保留分支与干净 worktree；合并后的临时资源按 §9 条件回收，长期分支永久保留。
- 改动共享文件（`Cargo.toml`、`AGENTS.md`、`README.md`、`.github/`）必须在正文显式列出。
- 评审按 §5 步骤 7 由独立实例完成。当前实例共用 GitHub 账号，以带实例 ID 的 `KIND: review` 评论记录真实结论；不伪造他人或人类评审。共享账号评论不替代 GitHub 分支保护要求的原生审批；遇到该限制报告，由 maintainer 处理。
- **合并授权**：CI 绿与非作者评审通过是质量门槛，不自动授权合并；只有用户明确要求合并某个 PR 时才执行 merge／auto-merge，「非关键决策直接处理」等广泛授权不推导为合并授权。授权与执行记录写在对应 PR／issue 评论（§5 步骤 9、§2）。关键决策（产品方向、安全、权限、费用、破坏性架构变更）仍按 §8 升级人类。
- 禁止直推 `master`／`dev`（用户当次明确授权除外）、禁止 force-push 他人分支、禁止在无评审或无明确合并授权的情况下合并 PR。

## 8. 交接、阻塞与升级

### 交接（Handoff）

这里的交接是独立业务线／主责之间的所有权移交。主 agent 给自己的子 agent 派活、换一个子 agent 或接收其结果，使用 tmux，保留原 issue 主责与标签，不套用以下 GitHub 交接步骤。

**跨业务线**移交（换标签）：

```sh
gh issue edit 42 --remove-label agent:runtime --remove-label status:in-progress \
  --add-label agent:tui --add-label handoff --add-label status:ready
gh issue comment 42 --body "TO: agent:tui#nova
FROM: agent:runtime#kira
KIND: handoff
REFS: #42, 1a2b3c4

交接清单：
- 当前状态：…
- 已完成：…
- 下一步（具体动作）：…
- 相关文件／commit：…
- 已知坑：…
- 验收标准复述：…"
```

**同一业务线内换实例**（不换标签，交接后仍为 `agent:runtime`，在评论中写明接收实例），其余步骤相同。

- 同一任务在**原 issue** 评论交接，保留类型、历史与验收标准。目标不同则开独立任务并引用来源，按新目标确定类型与归属；分诊未完成保持 `triage`，DoR 完整才可 `ready`。
- 交接清单必须包含证据（commit／命令输出）、依赖关系、已知坑与验收标准复述。
- 独立接收方评论第一步动作与实例 ID 后才确认交接，移除 `handoff` 并置合适阶段；不以固定 session／pane 编号指定接收方。内部子任务的回执只需回到主 agent 的 tmux pane。移交方按 §5 保存和记录成果及接手方式。

### 阻塞（Blocked）

子 agent 先通过 tmux 向主 agent 报告阻碍；内部可解决的问题由主 agent 处理。确实阻塞独立任务或需要其他业务线产出时，由主 agent 在 GitHub 写明原因与解除条件：

```sh
gh issue edit 42 --remove-label status:in-progress --add-label status:blocked
gh issue comment 42 --body "TO: agent:qa
FROM: agent:runtime#kira
KIND: blocked
REFS: #42

BLOCKED-BY: #77            # 有真实 issue 依赖时写；权限／环境／人类决定等改写具体原因与解除条件
原因：…
需要的动作：…
解除条件：…
我等待期间的替代方案：…"
```

阻塞解除方评论 `UNBLOCKS: #42`；负责实例确认后恢复到合适阶段（`ready`／`in-progress`／`review`）。阻塞前按 §5 保存阶段成果，记录 SHA 或无法提交推送的具体原因，不丢失评审上下文。

### 升级到人类

以下情况需要 `needs:human` + `@HeimaoLST`；只有确实阻止当前任务继续时才置 `status:blocked`：

- 阻塞超过 1 个工作日且双方无法推进；
- agent 之间对协议、接口或职责有争议；
- 需要新增权限、账号、费用或开通外部服务等实际决定（日常使用现有账号与环境的普通问题不在此列）；
- 产品方向、身份／成长机制等需要用户拍板的决策；
- 安全或数据泄漏风险。

## 9. 并发、冲突与冻结

一条业务线可以同时有多个实例，因此除了业务线之间的边界，还要管理**同线实例之间**的写冲突。

- **唯一主责与明确分工**：一个 issue 只有一个主责实例，对标签、范围与交付负责；可通过 tmux 分派子 agent，子 agent 不另领同一 issue。既有认领不自动因主子关系变更，需换主责时按 §8 交接。标签不提供原子锁，不先动手抢写。
- **唯一写入路径**：主 agent 明确每个子任务的文件范围，同一文件同时只有一个写入者；并行修改需范围不重叠。主 agent 读代码、capture 进展和验收不算争抢写入；要修改子 agent 正在写的文件，先让其暂停并交回。Git 提交、同步等工作区操作串行进行；任务执行期间保持约定分支，不在共享工作区切换分支。
- **遵守修改范围**：跨线代码先明确交接或取得当前任务的授权；模块事实文档按 §3.3 同步，不能借机修改无关内容。
- **抢占**：两个实例同时认领同一 issue，以注明实例与文件范围、时间较早的认领评论为准；后来者不要撤销对方标签，改领其他 ready issue 或在评论中协商。
- **公共契约决策**：新增跨业务线公共契约，或对既有公共接口／协议／事件作不兼容变更，先开 `type:decision` 写明动机、方案、影响面、迁移与回滚，取得受影响业务线的实例 `ACK`。需人类拍板的事项按 §8 确认，再拆实施任务；不改变公共契约的局部维护走普通 PR。决策结论沉淀到 `docs/design/`。
- **同步 base**：合并前同步到最新 base（普通 PR 为 `dev`，hotfix 为 `master`，stacked 为直接父分支）。未发布的本地分支可以 rebase；已推送／开 PR 的分支用 merge commit，不 force-push、不覆盖远端。有冲突由分支作者解决，不要求评审者代劳。
- **工作目录隔离（默认开发规则）**：`Saki/` 主检出固定在 `master`，用于查看与巡检；开发在独立 worktree 中进行。主检出更新仅在干净且无其他实例写入时执行；用户指定的当次例外按 §0。
  - 每个独立开发 issue 一个 worktree：`./scripts/agent-worktree.sh add agent/<line>/<issue>-<slug>`，目录位于 `<Workspace>/worktrees/Saki/<branch 中 / 替换为 ->`（默认 `$HOME/Workspace`，可用 `SAKI_WORKTREE_ROOT` 覆盖）。主 agent 统一分配给该任务的子 agent，不为内部每个步骤重复建 worktree。
  - 禁止在主检出切分支；独立任务不共用 worktree。同一任务主子 agent 可在约定工作区监督／执行，遵守文件范围与 Git 操作串行要求；不得因窗口或目录相同就认定是自己的子 agent。
  - 每个 worktree 的 `target/` 与 `.saki/` 独立；Cargo 依赖缓存（`~/.cargo`）共享。需要跨 worktree 共享会话时设置 `SAKI_SESSION_DIR`。
  - **生命周期**：按 §5 步骤 9 保留待审资源、回收已合并的自有 `agent/*` 临时分支；`master`／`dev` 不删除。不运行全局 `prune` 清理其他实例资源。调用 `finish <branch>` 前确认归属、最终集成、无新增提交／脏文件与依赖；当前脚本先移除 worktree 再检查合并、还可能删除未合并远端分支，不能代替这些核对。未合并或状态不明不调用。
- 大范围机械改动（rename、格式化）单独开 `type:refactor` issue，与功能改动分离。

## 10. 证据与验收要求

| 改动类型 | 最低证据 | 备注 |
| --- | --- | --- |
| runtime／client／session 行为 | 确定性离线测试（mock／受控协调，**禁止 sleep 碰运气**）；依赖真实模型时附真实运行摘要 | 测试覆盖真实行为与非平凡不变量；基线数字以最近一次全量运行／CI 结果为准，不写死在协议里 |
| 内置工具（runtime 线） | 离线工具测试 + 边界（大输出、超时、错误）；涉及真实模型调用的闭环验证 | 大输出落盘回读也要验证 |
| TUI／console | 单元与渲染测试 + 真实 PTY／tmux 运行记录；交互变化附前后对比 | 项目已有 PTY 验收实践 |
| GUI（desktop） | `cd desktop && go test ./...`；视觉变化附截图 | 独立 Go module |
| docs | 与代码或来源一致；延续「实现事实／作者报告／设计推论／待验证」的标注约定 | 见 `docs/README.md`；文档改动不强制跑模型／PTY，按适用性记录 N/A |
| 用户可见行为变化 | 同步更新相关 `docs/design/` 文档，或开 follow-up issue 并链接 | 不允许只改代码不更新文档且不说明 |

检查范围：

- Rust 源码、依赖或相关测试变化：`cargo fmt --check`、`cargo clippy --all-targets -- -D warnings`、`cargo test`，并验证受影响的行为边界。
- 独立 `desktop/` 变化：`cd desktop && go test ./...`，交互／视觉变化补对应证据；未影响 Rust 时无需重跑 Rust 全套。
- 仅文档／模板变化：检查内容与来源一致、链接和格式有效；涉及脚本时检查语法及受影响流程，不强制新增无关测试。
- 发布到 `master`、hotfix 与 `master` 回流 `dev`：全套 Rust 检查、Go 测试及对应的发布／修复回归。已有 required CI 检查在所有适用 PR 中仍必须通过。

证据规则：

- 子 agent 通过 tmux 回传证据或本地日志路径；主 agent 核对并汇总到 issue／PR，注明实际执行／评审者、SHA 与范围。`.saki/` 和临时过程日志不提交。
- **适用范围**：按上表选择最低证据，不适用的项写 N/A 与理由；文案、标签映射等简单机械改动不强制新增测试，但要在 PR 说明为什么不适用。文档改动不强制跑模型／PTY。
- 按上述范围执行必需检查，注明验证 SHA 与结果；未运行或失败要说明原因。不能将其他分支／旧提交的通过结果当作当前版本证据。
- 失败也是证据：先贴原始输出摘要，再给判断。
- 结论要标注适用范围：固定 commit、环境、模型 ID；不把一次运行说成普遍结论。重要里程碑、决定、阻塞与验收证据记录到 issue／PR，不要求每条临时终端输出都发评论。
- 违反红线：任何密钥、token、`.env` 内容不得出现在 issue、PR、日志或评论中。发现泄漏立即 `@HeimaoLST` 并轮换。

## 11. 常用 gh 命令

```sh
# 新建 bug
gh issue create --title "[bug] TUI 滚动位置在 resize 后丢失" \
  --label "type:bug,status:triage,platform:rust" --body-file /tmp/bug.md

# 分诊：确认 DoR 后定优先级、归属并挂本轮 milestone（类型已由 bug 模板提供）
gh issue edit 42 --remove-label status:triage \
  --add-label "priority:p2,agent:tui,status:ready" --milestone "<iteration-title>"

# 查看当前工作全景
gh issue list --state open --limit 100 --json number,title,labels

# 按业务线看队列
gh issue list --state open --label "agent:qa,status:ready"

# 搜索发给某条业务线或某实例的消息（评论正文）
gh search issues --repo HeimaoLST/Saki --state open --match comments "TO agent:runtime"
gh search issues --repo HeimaoLST/Saki --state open --match comments "TO agent:runtime#kira"

# 关闭并留原因
gh issue close 42 --comment "验收通过，证据见 PR #43"
```

## 12. 仓库设施（按需启用）

| 设施 | 用途 | 建议启用时机 |
| --- | --- | --- |
| Milestones | 每个迭代／版本一个 milestone，进入 `ready` 时必须挂载 | 第一次计划迭代时 |
| Projects 看板 | 列 = `status:*`，用于人类总览 | 同一时间超过 10 个活跃 issue |
| GitHub Actions | `fmt`／`clippy`／`test`／`go test`；标签守卫按 §4 区分 triage 与 ready 后的要求 | QA 提供门禁需求与验收标准，PM 维护 `.github/`，或明确交接实施；以实际配置为准 |
| Branch protection | 要求 PR + CI；平台要求的原生审批不能由结构化评论替代 | CI 接入后由 maintainer 配置 |
| 自动删除合并分支 | 自动清理策略需保护 `master`／`dev` 并处理上层 PR 依赖；是否已删除以实际状态为准 | 2026-10-16 已核对 `deleteBranchOnMerge=true`；设置由 maintainer 管理 |
| CODEOWNERS | 每个业务线（或实例）有独立 bot 账号后，按路径与 `agent:*` 自动路由评审 | 启用独立 bot 账号时 |
| Discussions | 开放探索类话题；产生结论后必须转为 issue 或文档 | 需要社区输入时 |

## 13. Agent 行为红线

- 用户明确指令优先于本协议；不得以协议条款拒绝、拖延用户明确授权范围内的执行，也不得把单次例外扩大为长期豁免。
- 不直推 `master`／`dev`（用户当次明确授权除外），不做无评审或无明确合并授权的合并。
- 不在 issue／PR／commit 中写入密钥、token、`.env` 内容，不提交 `.saki/` 运行产物。
- 不越过任务范围；跨线代码按 §9 交接，模块文档按 §3.3 同步。
- 不冒用其他实例署名、认领或伪造评审；主 agent 汇总结果注明实际执行者。独立评审要求见 §5。
- 不用 GitHub 代替主子日常对话，不让子 agent 重复认领、逐步发评论或自行跨线派活；主 agent 对外汇总。
- 不按记忆中的 session／pane 数字或单独依赖 `TMUX_PANE` 投递任务；动态发现并确认父子身份与任务，禁止误投／打断其他独立任务。
- 不同时持有多个同路径的 `status:in-progress` issue；同线实例文件范围重叠时先协商。
- 标准开发任务按 §5 保存阶段成果；用户要求不提交或操作受阻时如实报告，不丢弃改动或夹带他人文件。
- 不关闭他人创建的 issue 而不给理由和证据。
- 不把未验证的结论写成事实；区分事实、报告、推论与待验证。
- 不做与 issue 验收标准无关的重构或格式化（scope creep）。
- 不用 `sleep` 或时间竞态写测试；不把偶发通过当作验收证据。
- 必需检查失败时不请求最终合并；内容评审可单独记录，不伪称所有门禁通过。
- 不把长期事实留在 issue 里不沉淀，也不把 plan／需求／决策散落成仓库中的临时文件。
- 不删除他人或长期分支；除非用户明确要求重写历史，不 force-push。
- 不在主检出切换分支；独立任务不共用 worktree，主子同任务按 §9 明确写入范围并串行操作 Git。
- 自己已合并且无依赖的临时资源按 §9 回收；待评审／待合并不提前清理，保留长期分支与他人资源。

## 附：本文件维护

- 本文件由 `agent:pm` 维护，默认修改走 PR；协议本身变更必须开 `type:decision` issue 并获得受影响业务线的 `ACK`。用户明确授权直接修订时（§0），按授权范围执行并把改动与理由记录到 issue；单次例外不改变默认流程。
- 标签的实际创建与更新由 `scripts/setup-labels.sh` 完成（需要 `gh` 已登录且对仓库有写权限）。

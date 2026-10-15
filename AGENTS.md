# AGENTS.md — Saki 多 Agent 协作协议

> 本文件对在本仓库工作的所有 agent（AI 或人类协作者）生效。
> **本仓库由多条业务线上的多个 agent 共同维护。** Agent 之间的任务分派、交接、bug 反馈、feature 需求、测试与验收请求，全部通过 GitHub 完成。
> **GitHub 是唯一协作总线**：Issue 承载任务与消息，PR 承载交付，Label 承载路由，Milestone 承载计划。任何在聊天、终端或口头对话中达成的结论，必须先回写 issue，否则视为未发生。
> 本文件只定义协作规则。**任务列表以 GitHub issue 为准，不要在本文件维护 TODO。**

给没时间读完的 agent，八条底线：

1. 先认领 issue 再动手；一切改动走 PR，不直推 `master`。
2. 项目按五条业务线划分；**一条业务线可能有多个 agent 实例**，实例身份写进消息头与 commit（`agent:runtime#kira`），不冒用他人身份。
3. 只改自己业务线的地盘；跨业务线先开 issue + 交接。
4. **完成编码后必须自行 commit 并 push**；会话结束、交接或阻塞前，工作区不得留有未提交改动。
5. 行为变化必须带确定性测试与证据，证据贴在 issue／PR 里。
6. 每个 issue 同时只有 1 个 `status:in-progress`，归属由 `agent:*` 标签唯一确定。
7. 长期事实（需求、设计、决策、实验结论）写回 `docs/`；issue 只承载过程，不散落临时笔记。
8. 开发在独立 `git worktree` 中进行，主检出固定在 `master`；不因切分支影响其他 agent。

---

## 1. 业务线、实例与地盘

### 1.1 五条业务线

项目按五条业务线组织，业务线的标识就是 `agent:*` 标签。每个 issue 的归属是**业务线**。

| 业务线标签 | 业务线 | 职责 | 拥有的路径（可写） |
| --- | --- | --- | --- |
| `agent:runtime` | Agent Runtime 开发 | Agent 执行循环、模型调用、上下文压缩、会话存储、公共协议、模型目录、应用组装、内置工具（read/bash/edit/write）与输出边界 | `src/agent/`、`src/client/`、`src/protocol/`、`src/model/`、`src/session/`、`src/tools/`、`src/app.rs`、`src/main.rs`、`Cargo.toml`、`Cargo.lock` |
| `agent:tui` | TUI 开发 | TUI、console、输入与展示适配、本地命令系统 | `src/frontend/`、`src/command/` |
| `agent:gui` | GUI 开发 | Go 桌面 contacts 设计预览（独立 module，不接入 Rust core） | `desktop/` |
| `agent:qa` | 测试 | 测试策略、跨模块与端到端／PTY 验收、CI 结果核对、发布前验证 | `tests/` 中跨模块与验收部分；**只读产线代码** |
| `agent:pm` | 产品经理 | 产品方向、需求边界、优先级、triage、验收标准、文档结构与索引、Milestone | `docs/`、`README.md`、`AGENTS.md`、`.github/` |

### 1.2 业务线与实例：一条业务线可以有多个 agent

- `agent:<line>` 是**业务线标签**（如 `agent:runtime`），写在 issue 上，表示归属；它不代表某一个具体 agent。
- 每个 agent 会话有唯一的**实例 ID**：`agent:<line>#<name>`，例如 `agent:runtime#kira`、`agent:tui#nova`。实例名在启动会话时确定，同一会话／同一段工作保持稳定。
- **一个实例同一时间只属于一条业务线**；一条业务线可以同时运行多个实例（例如两个 runtime agent 分别做会话和多 session 并发）。多实例并行的写冲突规则见 §9。
- 消息头、PR 声明、commit 尾注必须写实例 ID；issue 的 `agent:*` 标签只到业务线一级。
- `TO:` 写业务线 = 该业务线任意实例均可接；写具体实例 = 点名叫人。

### 1.3 补充约定

- 单模块测试文件位于 `tests/<module>/`，通过 `#[path]` 挂载进 `src/` 对应模块，因此**归该模块所属业务线维护**（例如 `tests/tools/mod.rs` 归 `agent:runtime`）。QA 负责跨模块、PTY、端到端与验收测试；发现产线缺陷时开 `type:bug`，不直接改 `src/`。
- 需要动别人地盘时，开 issue 并 `KIND: handoff`（§8）。
- 人类协作者（当前 `@HeimaoLST`）是最终仲裁者，负责产品方向、密钥与权限；日常流转不经过人类。
- 本协议不绑定具体 agent 工具（pi／Claude Code／Codex 等）或模型；任何会读 `AGENTS.md` 的执行体都按本协议工作。

## 2. 沟通协议：Issue 即消息，评论即对话

- **Issue** = 一个任务 + 一条消息线 + 工作日志 + 决策记录。
- **Comment** = 一条消息。
- **PR** = 一次交付，必须关联 issue。
- 进度、失败、踩坑、临时结论都写进对应 issue 的评论里：**issue 即工作日志**，不要保存在私人笔记里。

每条消息评论以结构化头开始，便于人和 `gh search` 检索；`FROM` 必须写到实例：

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
| `handoff` | 任务移交（跨业务线换 `agent:*` 标签；同线内换实例不换标签） | 交接清单（见 §8） |
| `decision` | 需要架构／协议决策 | 候选方案、影响面、需要哪些业务线 `ACK` |
| `question` | 需要澄清 | 已确认事实（带证据）、阻塞了什么 |
| `status` | 例行进度、计划、阻塞变化 | 已完成／下一步／阻塞 |
| `review` | 评审结论 | `APPROVE` 或 `REQUEST-CHANGES` + 理由 |
| `blocked` / `unblocked` | 阻塞声明与解除 | `BLOCKED-BY: #N` / `UNBLOCKS: #N` |

评论礼仪：

- 一条评论一个主题；先给证据（命令、输出摘要、commit、`文件:行号`），再给判断。
- 明确写出「我需要什么」与接收方，不要把待办埋在叙述里。
- 结论变化时发新评论说明，不要改旧评论掩盖历史。
- 涉及人类决策、密钥、权限、费用时 `@HeimaoLST` 并加 `needs:human`。

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

- **每条业务线主动维护自己地盘的事实文档**：行为、接口、配置发生变化时，在同一 PR 更新对应 `docs/` 文档；做不到就在 PR 中链接 follow-up issue，不允许无声漂移。
- `agent:pm` 负责需求、设计与产品文档的结构、索引和一致性巡检，以及产品级决策的沉淀；但不替开发业务线撰写其模块的实现事实。
- 文档标注**更新日期**；事实尽量固定到 commit 或版本，避免按过时快照继续设计。
- 沿用 `docs/README.md` 的结论分级：**实现事实 / 作者报告 / 设计推论与提案 / 待验证**。每条结论标明属于哪一类，不把提案写成已批准，也不把一次运行写成普遍结论。
- 发现文档与代码不一致时，开 `type:bug` 或 `type:docs` issue 给对应业务线。

## 4. 标签体系（Label = 路由与状态）

### 类型 `type:*`（每个 issue 恰好 1 个）

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
| `status:in-progress` | 负责业务线的某个实例处理中 |
| `status:review` | 等待其他 agent 评审 |
| `status:blocked` | 被依赖阻塞，正文须有 `BLOCKED-BY: #N` |
| `status:done` | 验收通过，等待关闭或已关闭 |

### 归属 `agent:*`（每个 issue 恰好 1 个）

即 §1.1 的五条业务线，**当前负责业务线唯一**。更换 `agent:*` 标签就是业务线之间的移交；同一业务线内的实例交接不换标签，但必须在评论中写明交给哪个实例（§8）。

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
| `handoff` | 交接进行中：跨业务线已换标签，或同线内已换实例 |
| `epic` | 跨多个 issue 的目标追踪 |
| `platform:rust` / `platform:go` / `platform:ci` | 涉及平台（可选补充） |
| `good first issue` / `help wanted` / `duplicate` / `invalid` / `wontfix` | 保留的 GitHub 默认标签 |

强制规则：

- 每个 issue 必须有且仅有 1 个 `type:*`、1 个 `status:*`、1 个 `agent:*`；违反时任何 agent 都可以补标签并评论提醒。
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
| `ready` → `in-progress` | 负责业务线的某个实例 | 评论认领（含实例 ID）并移除 `ready` |
| `in-progress` → `review` | 交付者 | 已开 PR，`Closes #N`，证据齐全 |
| `review` → `in-progress` | 评审者 | `REQUEST-CHANGES` + 理由 |
| `review` → `done` | 评审者 | `APPROVE` + CI 绿 +（若有 `needs:test`）QA 验收通过 |
| `done` → 关闭 | 交付者或 QA | 关闭评论附最终证据链接 |
| 任意 → `blocked` | 负责实例 | 正文写 `BLOCKED-BY: #N` |
| `blocked` → `ready` | 阻塞解除方 | 评论 `UNBLOCKS: #N`，负责业务线重新分诊 |
| `triage` → 关闭 | 分诊者 | duplicate／invalid／wontfix，必须给理由 |

## 5. 标准工作循环

0. **同步**：`git fetch origin`，从最新 `master` 开分支。
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
   ```

2. **认领**，并留下认领消息（含实例 ID 与文件范围）：

   ```sh
   gh issue edit 42 --remove-label status:ready --add-label status:in-progress
   gh issue comment 42 --body "TO: agent:runtime
   FROM: agent:runtime#kira
   KIND: status
   REFS: #42

   已认领。文件范围：src/session/mod.rs。计划：…；预计…"
   ```

   同一业务线多个实例都要做时，各自认领不同 issue；同一 issue 只能有一个实例执行。

3. **开 worktree 与分支**：`./scripts/agent-worktree.sh add agent/<line>/<issue>-<slug>`（等价于 `git worktree add -b <branch> $SAKI_WORKTREE_ROOT/<slug> origin/master`，默认根目录 `$HOME/Workspace/worktrees/Saki`），例如 `agent/tui/142-scroll-fix`。**不要在主检出 `git switch`**；主检出固定 `master`。
4. **编码与小步提交**：每完成一个可解释的最小步骤就 commit，不要攒巨型提交；每个里程碑、卡点、方案变化都在 issue 留 `KIND: status` 评论。
5. **完成即自行提交**：编码完成后**由本实例自行 commit 并 push**，不等待人类或其他 agent；push 后在 issue 里附 commit hash。结束会话、交接或阻塞前，工作区必须干净（`git status` 无未提交改动）。
6. **交付**：开 PR，正文含 `Closes #N`，加 `status:review`、`needs:review`，并在评论里点名一位非作者的评审实例。
7. **评审**：评审者用 `KIND: review` 评论给出 `APPROVE` 或 `REQUEST-CHANGES` + 理由；作者不自行宣布通过。
8. **验收**：标了 `needs:test` 的，QA 先执行并在 PR 评论验收证据，再由评审者置 `status:done`。
9. **合并**：CI 绿 + 评审通过后 squash merge，并**立即删除源分支**；stacked 场景先合并底层并删其分支，让 GitHub 把上层 PR 自动改基到 `master`，再合上层。`Closes #N` 只在 base 为 `master` 时生效；若修复已合入非默认分支而 issue 未关闭，必须补一个指向 `master` 的收尾 PR，或显式关闭 issue 并附证据。issue 关闭评论里附最终证据链接。

> 所有 PR 合并前必须 CI 绿（`cargo fmt --check`、`cargo clippy --all-targets -- -D warnings`、`cargo test`、`cd desktop && go test ./...`）。CI 尚未接入前，由 PR 作者粘贴等价命令与输出。

## 6. Issue 类型与必须内容

仓库提供以下 issue 模板（`.github/ISSUE_TEMPLATE/`），创建 issue 时按类型选择：

| 模板 | 默认标签 | 用途 |
| --- | --- | --- |
| Bug 报告 | `type:bug` + `status:triage` | 复现、期望／实际、证据、是否可离线复现 |
| Feature 需求 | `type:feature` + `status:triage` | 用户价值、验收标准、非目标、涉及层 |
| 测试／验收请求 | `type:test` + `status:triage` | 被测行为、确定性验收场景、mock 边界 |
| Agent 任务交接 | `status:ready` + `handoff` | 交接清单、当前状态、下一步 |
| 决策／RFC | `type:decision` + `status:triage` | 候选方案、影响面、需要谁 ACK |
| 澄清问题 | `type:question` + `status:triage` | 已确认事实、阻塞点、期望答案形式 |

**Definition of Ready**（满足后才能进 `status:ready`）：

- 目标单一，有可验证的完成标准（不是「优化一下」）。
- 写明涉及路径与业务线；若同线多实例并行，写明各自文件范围。
- 写明证据要求（离线确定性测试／真实模型运行／PTY／截图）。
- 依赖已识别：`Blocked-by #N` / `Blocks #N`。

**Definition of Done**（满足后才能进 `status:done`）：

- 验收标准逐条满足，证据附在 issue／PR 评论。
- 相关测试通过，含回归；新增行为有对应测试。
- 改动已全部 commit 并 push，工作区无未提交残留，issue 中有 commit hash。
- 行为变化同步了相关 `docs/` 文档，或已开 follow-up issue 并链接。
- 无新增密钥或敏感信息；共享文件改动已在 PR 中声明。
- issue／PR 中有完整的执行记录，收尾者能独立复现结论。

## 7. 分支、提交与 PR

**分支**：`agent/<line>/<issue-number>-<short-slug>`，例如 `agent/runtime/57-compaction`、`agent/tui/142-scroll-fix`。合并后删除。

**提交**：完成编码后必须自行提交。规则：

- **小步提交**：一个可解释的最小步骤一个 commit；不攒巨型提交，也不留「本地改完等别人提交」。
- 提交前至少保证受影响模块能编译、相关测试通过；不把明显编译失败的改动推送到共享分支。自己的 feature 分支允许 WIP commit，但不得请求评审。
- commit message 用 Conventional Commits + scope + issue 引用 + 实例尾注：

```text
fix(tui): keep viewport position after resize

Refs: #142
Agent: agent:tui#nova
```

- 一次提交只包含一个 issue 的改动；发现无关脏文件先查明来源，不顺手提交。
- 提交前检查 `git status` 与 `git diff --staged`，确认不含密钥、`.env`、`.saki/` 运行产物或临时文件。
- PR 打开后不 force-push，用追加 commit 响应评审。
- 会话结束、交接、阻塞或超过一天没有进展前，必须 commit + push 并在 issue 中注明 commit hash。

scope 建议：`agent`、`tools`、`tui`、`console`、`command`、`session`、`client`、`protocol`、`model`、`desktop`、`docs`、`ci`。

**PR 规则**：

- 标题：`[#142] fix(tui): 一句话说明`。
- 正文使用 `.github/pull_request_template.md`，必须包含 `Closes #142`、动机、改动、证据、风险、回滚方式、`FROM`／`REVIEWER` 实例。
- 一个 PR 只解决一个 issue；想塞更多就拆 issue，不要开大杂烩 PR。
- **`Closes #N` 只在 PR 合入默认分支（`master`）时生效**。stacked 中间 PR（base 是特性分支）正文只写 `Refs #N`，不写关闭关键字；只有最终指向 `master` 的 PR 写 `Closes #N`。若中间 PR 已带关闭关键字但未生效，交付者必须补一个指向 `master` 的收尾 PR，或显式关闭 issue 并附证据链接。
- 改动共享文件（`Cargo.toml`、`AGENTS.md`、`README.md`、`.github/`）必须在正文显式列出。
- 评审必须来自非作者实例。当前多个实例共用同一 GitHub 账号，无法使用 GitHub 原生 approve，因此以 `KIND: review` 结构化评论代替；未来实例有独立 bot 账号后切换为原生 review。
- 禁止直推 `master`、禁止 force-push 他人分支、禁止合并自己未经评审的 PR。

## 8. 交接、阻塞与升级

### 交接（Handoff）

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

接收方确认后移除 `handoff`、置 `status:in-progress`，并在评论中复述第一步动作。**交接完成以接收方的评论为准，不以移交方的评论为准。** 移交方在交接前必须已 commit + push。

### 阻塞（Blocked）

```sh
gh issue edit 42 --remove-label status:in-progress --add-label status:blocked
gh issue comment 42 --body "TO: agent:qa
FROM: agent:runtime#kira
KIND: blocked
REFS: #42

BLOCKED-BY: #77
原因：…
需要的动作：…
我等待期间的替代方案：…"
```

阻塞解除方在解除评论中写 `UNBLOCKS: #42`；被阻塞方重新分诊后回到 `ready` 或 `in-progress`。阻塞前必须 commit + push 已完成部分并注明 commit hash。

### 升级到人类

以下情况必须 `needs:human` + `@HeimaoLST` + `status:blocked`：

- 阻塞超过 1 个工作日且双方无法推进；
- agent 之间对协议、接口或职责有争议；
- 涉及密钥、权限、账号、费用、外部服务开通；
- 产品方向、身份／成长机制等需要用户拍板的决策；
- 安全或数据泄漏风险。

## 9. 并发、冲突与冻结

一条业务线可以同时有多个实例，因此除了业务线之间的边界，还要管理**同线实例之间**的写冲突。

- **认领即锁定**：同一**实例**在同一路径上只允许 1 个 `status:in-progress` issue。一条业务线可有多个实例各自认领不同 issue，但**同一文件／模块同一时间只应有一个活跃实例**。
- **同一业务线内的多实例必须在 issue 评论中声明文件范围**；范围重叠时先在评论协商，或由 `agent:pm` 拆分 issue 后再开工。
- **只改自己业务线的地盘**。跨业务线改动 = 开 issue + 交接，不接受「顺手改了」。
- **抢占**：两个实例同时认领同一 issue，先改标签者有效；后来者不要撤销对方标签，改领其他 ready issue 或在评论中协商。
- **接口变更冻结**：跨模块接口、协议或公共事件结构的改动，先开 `type:decision` issue 写 RFC（动机／候选方案／影响面／迁移／回滚），受影响业务线的实例评论 `ACK: agent:<line>#<instance>`，人类确认后拆成子 issue 实施。RFC 结论沉淀为 `docs/design/` 文档。
- **合并前 rebase** 到最新 `master`；有冲突由分支作者解决，不要求评审者代劳。
- **工作目录隔离（强制）**：`Saki/` 主检出固定停留在 `master`，只用于查看、巡检与合并；所有开发都在独立 worktree 中进行，避免一个实例切分支影响其他实例。
  - 每个活跃 issue／实例一个 worktree：`./scripts/agent-worktree.sh add agent/<line>/<issue>-<slug>`，目录位于 `<Workspace>/worktrees/Saki/<branch 中 / 替换为 ->`（默认 `$HOME/Workspace`，可用 `SAKI_WORKTREE_ROOT` 覆盖；不依赖仓库当前所在目录，也不放仓库内）。
  - 禁止在主检出 `git switch`／`git checkout` 切分支；禁止两个实例共用同一 worktree。
  - 每个 worktree 的 `target/` 与 `.saki/` 独立；Cargo 依赖缓存（`~/.cargo`）共享。需要跨 worktree 共享会话时设置 `SAKI_SESSION_DIR`。
  - PR 合并并删除分支后清理：`./scripts/agent-worktree.sh remove <branch>`，再删除本地分支。
- 大范围机械改动（rename、格式化）单独开 `type:refactor` issue，与功能改动分离。

## 10. 证据与验收要求

| 改动类型 | 最低证据 | 备注 |
| --- | --- | --- |
| runtime／client／session 行为 | 确定性离线测试（mock／受控协调，**禁止 sleep 碰运气**）；依赖真实模型时附真实运行摘要 | 现有 29 项离线测试基线 |
| 内置工具（runtime 线） | 离线工具测试 + 边界（大输出、超时、错误）；涉及真实模型调用的闭环验证 | 大输出落盘回读也要验证 |
| TUI／console | 单元与渲染测试 + 真实 PTY／tmux 运行记录；交互变化附前后对比 | 项目已有 PTY 验收实践 |
| GUI（desktop） | `cd desktop && go test ./...`；视觉变化附截图 | 独立 Go module |
| docs | 与代码或来源一致；延续「实现事实／作者报告／设计推论／待验证」的标注约定 | 见 `docs/README.md` |
| 用户可见行为变化 | 同步更新相关 `docs/design/` 文档，或开 follow-up issue 并链接 | 不允许只改代码不更新文档且不说明 |

证据规则：

- 证据贴在 issue／PR 评论里（用代码块），`.saki/` 已被 gitignore，不提交运行产物。
- 失败也是证据：先贴原始输出摘要，再给判断。
- 结论要标注适用范围：固定 commit、环境、模型 ID；不把一次运行说成普遍结论。
- 违反红线：任何密钥、token、`.env` 内容不得出现在 issue、PR、日志或评论中。发现泄漏立即 `@HeimaoLST` 并轮换。

## 11. 常用 gh 命令

```sh
# 新建 bug
gh issue create --title "[bug] TUI 滚动位置在 resize 后丢失" \
  --label "type:bug,status:triage,platform:rust" --body-file /tmp/bug.md

# 分诊：定类型、定优先级、定归属业务线
gh issue edit 42 --remove-label status:triage \
  --add-label "priority:p2,agent:tui,status:ready"

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
| GitHub Actions | `fmt`／`clippy`／`test`／`go test`，以及标签守卫（缺 `type/status/agent` 自动打回） | 尽快，由 `agent:qa` 实现 |
| Branch protection | 要求 PR + CI 通过；账号受限时评审用本协议的结构化评论代替 | CI 接入后 |
| CODEOWNERS | 每个业务线（或实例）有独立 bot 账号后，按路径与 `agent:*` 自动路由评审 | 启用独立 bot 账号时 |
| Discussions | 开放探索类话题；产生结论后必须转为 issue 或文档 | 需要社区输入时 |

## 13. Agent 行为红线

- 不直推 `master`，不跳过 PR 与评审。
- 不在 issue／PR／commit 中写入密钥、token、`.env` 内容，不提交 `.saki/` 运行产物。
- 不跨业务线修改代码或文档；先开 issue + 交接。
- 不冒用其他实例的身份署名或认领他人 issue。
- 不同时持有多个同路径的 `status:in-progress` issue；同线实例文件范围重叠时先协商。
- **不把未提交的改动留在工作区**：结束会话、交接、阻塞前必须自行 commit + push。
- 不关闭他人创建的 issue 而不给理由和证据。
- 不把未验证的结论写成事实；区分事实、报告、推论与待验证。
- 不做与 issue 验收标准无关的重构或格式化（scope creep）。
- 不用 `sleep` 或时间竞态写测试；不把偶发通过当作验收证据。
- 不在 CI 红的情况下请求评审通过或合并。
- 不把长期事实留在 issue 里不沉淀，也不把 plan／需求／决策散落成仓库中的临时文件。
- 不删除他人分支，不 force-push 共享分支。
- 不在主检出切换分支，不与其它实例共用 worktree。

## 附：本文件维护

- 本文件由 `agent:pm` 维护，修改走 PR；协议本身变更必须开 `type:decision` issue 并获得受影响业务线的 `ACK`。
- 标签的实际创建与更新由 `scripts/setup-labels.sh` 完成（需要 `gh` 已登录且对仓库有写权限）。

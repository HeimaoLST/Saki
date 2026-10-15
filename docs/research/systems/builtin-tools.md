# Builtin tools：harness 实践与 SAKI 首批工具建议

核对日期：2026-10-05。范围：Pi、Codex、OpenCode 的官方源码，以及 Claude Code、OpenAI API 的官方文档。第三方检查为只读，没有运行这些 harness；没有使用网关进行模型实验。以下工具建议尚未实施。

后续状态：用户选择主要参考 Pi／Codex，不采用 OpenCode 作为实现依据，并明确暂不设计持久化或输出文件清理。四工具已实现；当前实际契约、离线验收与官方端点两项真实模型验收见[实现文档](../../design/builtin-tools.md)。下文保留原始研究快照，提案与实现存在差异时以实现文档为准。

## 1. 结论与当前范围

以 SAKI 当前的终端和本地工程任务作为第一阶段，建议默认提供 **`read`、`bash`、`edit`、`write`**。这四项可形成查找材料、读取内容、修改文件、执行验证的任务闭环。文件搜索暂由 shell 中的 `rg` 等命令完成；专用 `grep/glob` 可以在实际任务显示收益后加入。

工具数量并不是主要差异。更值得先落实的是：输出有界且可继续读取、编辑失败不会留下半份修改、命令有明确的退出和超时结果。后台进程管理、网页检索、任务计划和子 agent 各自需要运行时支持，不能仅注册一个函数名就认为能力成立。

用户已明确：当前 `caculator` 是学习 function call 和工具注册的示例，不是产品 builtin 的起点。本研究不建议围绕示例改造，也不修改或删除它。

## 2. 版本与证据边界

| 项目 | 本次核对版本 | 范围 |
| --- | --- | --- |
| Pi | `b78e6a9085343ec0f308c3d377da72528b4cf7ee`，2026-10-05 UTC | CLI 工具选择、核心工具、输出管理、扩展文档 |
| Codex | `823ea830c0fd418b09ff02d36cad9a1fff66465b`，2026-10-05 UTC | core 工具注册、shell schema、patch schema |
| OpenCode | `907b3bc518fa48e90e8ec24dd327d13eee71c36c`，2026-10-03 UTC | V1 registry 和工具；V2 静态组合、输出 store、bash |
| Claude Code | 当日持续更新的官方文档 | 工具参考；未核对闭源实现 |

这些版本与之前的 [compact 调查](context-compaction.md)不完全相同，旧调查保留其原有版本。源码中存在一个工具，不代表每个模型、客户端、功能配置都会暴露它。API 原生工具协议也不等于某个 CLI 的本地 function call。

## 3. 各 harness 的工具设计

### 3.1 Pi：小默认集合，专用搜索按需启用

Pi 默认启用 `read/bash/edit/write`，可选核心 builtin 为 `powershell/grep/find/ls`。CLI 内置扩展另提供 `codemode/tool_search`，默认关闭；SDK 不默认加载这些扩展。证据：[CLI 工具表](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/docs/cli.md#L128)、[核心注册](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/index.ts#L92)、[SDK](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/docs/sdk.md#L116)。

| 工具 | 当前实现中值得参考的契约 |
| --- | --- |
| `read` | `path, offset?, limit?`，行号从 1 开始；受行数和字节数约束，返回继续读取的位置；还支持部分图片格式 |
| `edit` | `path, edits:[{oldText,newText}]`；全部匹配原始文件，各匹配必须唯一、互不重叠；先 exact，失败时允许有限规范化匹配 |
| `write` | 新建或整体覆盖；自动创建父目录；与 edit 共用按文件的 mutation queue |
| `bash` | `command, timeout?`，timeout 单位秒，没有默认超时；每次启动进程并等待完成，支持 abort/timeout 杀进程树 |
| `grep/find/ls` | 分别负责内容搜索、glob 找文件、目录列表；各有结果数和输出大小限制 |

关键来源：[read](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/read.ts#L138)、[edit schema](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/edit.ts#L21)、[匹配检查](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/edit-diff.ts#L292)、[write](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/write.ts#L11)、[bash lifecycle](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/bash.ts#L97)。

Pi 的模型可见 bash 输出取末尾 **2,000 行或 50 KiB**；超出后保留完整临时文件，返回路径和已展示的范围，再由 read 分页取回。[输出格式](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/bash.ts#L338)、[统一上限](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/truncate.ts#L11)。其 accumulator 使用滚动尾部和文件，避免全文长期留在内存；程序调用/codemode 的结构化输出另有 1 MiB 额度，不能把 50 KiB 写成所有渠道的统一限制。[Accumulator](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/output-accumulator.ts#L28)、[结构化输出](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/bash.ts#L390)。

bash schema 没有 session、stdin、yield 或 background 参数。UI 收到流式更新，不意味着模型能在工具未完成时继续工作。用户通过 shell 使用 tmux/nohup 是另一回事，不能据此声称 Pi builtin 已提供统一后台任务管理。

Pi 的 operations 和 spawnHook 可以替换执行实现；官方 SSH 示例保持原有工具名，切换执行后端。这对 SAKI 未来的 sandbox 有参考意义，但不是现在就实现远程后端抽象的理由。[执行替换点](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/bash.ts#L214)、[SSH 示例](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/examples/extensions/ssh.ts#L118)。

### 3.2 Codex：进程会话与 patch，按配置暴露能力

本次检查的 core 注册中，启用 UnifiedExec 时注册 `exec_command` 和 `write_stdin`；关闭时可以只注册 one-shot `exec_command`。shell 的暴露还受环境、功能配置和模型信息约束。因此不存在适用于所有 Codex 配置的一张固定默认清单。[shell 注册](https://github.com/openai/codex/blob/823ea830c0fd418b09ff02d36cad9a1fff66465b/codex-rs/core/src/tools/spec_plan.rs#L1119)。

`exec_command` 的 schema 包括 command、working directory、PTY、等待时间和输出预算。命令未结束时可以返回 session ID，由 `write_stdin` 继续取输出或输入字符。**yield 是返回控制权的等待时间，不能理解成进程执行超时。**这个设计适合编译、服务和交互式实验，但也要求 harness 管理尚未结束的进程。[shell schema](https://github.com/openai/codex/blob/823ea830c0fd418b09ff02d36cad9a1fff66465b/codex-rs/core/src/tools/handlers/shell_spec.rs#L25)、[write_stdin](https://github.com/openai/codex/blob/823ea830c0fd418b09ff02d36cad9a1fff66465b/codex-rs/core/src/tools/handlers/shell_spec.rs#L117)。

`apply_patch` 由模型信息和环境条件决定是否注册；本次 schema 使用带 Lark grammar 的 freeform 输入。[注册条件](https://github.com/openai/codex/blob/823ea830c0fd418b09ff02d36cad9a1fff66465b/codex-rs/core/src/tools/spec_plan.rs#L1313)、[patch schema](https://github.com/openai/codex/blob/823ea830c0fd418b09ff02d36cad9a1fff66465b/codex-rs/core/src/tools/handlers/apply_patch_spec.rs#L9)。局部读取、搜索可以通过 shell 命令完成；本次检查的 core handler 集合没有 Pi 式独立 read/grep/find，不能把这个局部观察扩大为整个 Codex 产品没有相关扩展能力。

计划、询问用户、图片查看、协作 agent、MCP resource 和工具发现等还受各自配置约束。[utility 注册](https://github.com/openai/codex/blob/823ea830c0fd418b09ff02d36cad9a1fff66465b/codex-rs/core/src/tools/spec_plan.rs#L1189)、[协作工具](https://github.com/openai/codex/blob/823ea830c0fd418b09ff02d36cad9a1fff66465b/codex-rs/core/src/tools/spec_plan.rs#L1343)。这些属于进一步的工作流能力，不是完成基础文件修改必须先有的整套功能。

还要区分 OpenAI **API 原生 Shell / Apply Patch** 与 Codex CLI 的工具。API patch 返回 `apply_patch_call`，要求调用方应用文件操作并返回 `apply_patch_call_output`；local shell 同样需要执行方。SAKI 当前的 JSON function dispatch 不能直接接上这些原生协议。[API Shell](https://developers.openai.com/api/docs/guides/tools-shell)、[API Apply Patch](https://developers.openai.com/api/docs/guides/tools-apply-patch)。

### 3.3 OpenCode：完整工作流，V1 与 V2 必须分别核对

V1 的任务能力候选包括 `bash/read/glob/grep/edit/write/task/webfetch/todowrite/websearch/skill/apply_patch/question`，还有条件启用的 `execute/lsp/plan_exit`。`invalid` 是内部错误兜底，不列作用户任务能力。[V1 registry](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/tool/registry.ts#L206)。

这些候选不会全部无条件提供：question 依赖客户端或开关；websearch 依赖 provider 或搜索开关；V1 用 model ID 字符串判断 patch 路径，`gpt-` 且不含 `oss/gpt-4` 时提供 apply_patch，反之提供 edit/write。SAKI 没有证据应照搬这条模型名规则。[条件](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/tool/registry.ts#L231)、[模型筛选](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/tool/registry.ts#L291)。

V1 没有默认独立 `list` 或 `todoread`：read 能读目录，todowrite 更新完整 todo 数组。task 是子 agent，不是后台 shell；后台子 agent另有 experimental 开关。[read](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/tool/read.ts#L263)、[todo](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/tool/todo.ts#L14)、[task](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/tool/task.ts#L43)。

V2 静态 builtin 组合为 `apply_patch/bash/edit/glob/grep/question/read/skill/todowrite/webfetch/websearch/write`；源码说明 provider/model filtering 尚待后续 materialization，task/LSP/plan_exit 等还待移植。**V1 的 patch-vs-edit 规则和 task 能力不能当作 V2 的既有事实。**[V2 builtins](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/tool/builtins.ts#L18)。

输出管理值得借鉴，但仍有版本边界：

- V1 wrapper 对未自行标记截断的结果做统一限制，默认 2,000 行、50 KiB；超出后全文落盘并返回路径。read/grep/glob 自己分页或限制结果数。[wrapper](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/tool/tool.ts#L130)、[truncate](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/tool/truncate.ts#L85)。
- V2 registry 在 settle 时调用 output store，默认相同额度，预览取首尾。[registry](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/tool/registry.ts#L73)、[store](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/tool-output-store.ts#L138)。
- V2 bash 在进程采集阶段先限制 1 MiB，超过的内容已丢失。后续 store 保存采集结果，不等于完整进程日志；源码仍把流式全文保存和 model-facing background 管理列为后续工作。[bash](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/tool/bash.ts#L18)、[采集与返回](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/tool/bash.ts#L165)。

官方工具文档提供能力概览，但实际名单仍应看 registry。例如文档把 edit 描述为 exact replacement，V1 实际包含多种 fuzzy replacer；SAKI 不需要一开始复制它们。[工具文档](https://opencode.ai/docs/tools/)、[V1 replacer](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/tool/edit.ts#L694)。

### 3.4 Claude Code：基础文件工具与工作流工具并存

官方参考列出 `Read/Edit/Write/Bash`，以及 Agent、询问、Web 和 Skill 等能力。当前文档特别注明：`Glob/Grep` 在 macOS/Linux/WSL 默认缺席；`TaskOutput` 已弃用，改用 Read 读取任务输出文件；TodoWrite 默认关闭，任务工具的默认提供依赖模型。不能使用旧清单推断今天的默认工具。[官方参考](https://code.claude.com/docs/en/tools-reference)。

## 4. SAKI 当前接口意味着什么

检查范围为当前工作区，而非已提交版本：

- [main.rs](../../../src/main.rs#L72) 只注册 calculator 示例。
- [Tool / ToolDefinition](../../../src/tools/tool_registry.rs#L18) 已分开定义与 handler，ToolType 目前只有 Function；可直接承载普通 JSON builtin，无需先重做 registry。
- [Agent dispatch](../../../src/agent/mod.rs#L120) 目前实际执行的是 FunctionCall；[工具执行与结果写入](../../../src/agent/mod.rs#L143)逐个等待，然后把完整 JSON 输出写进 Conversation，没有输出大小边界。
- [Conversation cut point](../../../src/agent/conversation.rs#L355) 保留最新 user turn，并按完整调用边界切分。巨大的当前工具输出不能指望通过压缩旧前缀自动消失。

因此，首批工具设计应复用现有注册和 JSON 调用方式，同时在工具结果进入 Conversation **之前**控制体积。Conversation 继续承担历史压缩；执行侧承担当前输出的采集、预览和全文保存，职责保持单一。

## 5. 首批工具建议：四项能力与明确契约

以下是提案，不是已确定的公共 API。

| 工具 | 建议输入 | 调用方必须能观察的结果 |
| --- | --- | --- |
| `read` | `path, offset?, limit?` | 文本片段、行号范围、是否还有内容及继续位置；受字节/行数双重限制 |
| `bash` | `command, workdir?, timeout_ms?` | 有界输出、exit code、是否超时、是否截断；大输出的日志路径 |
| `edit` | `path, edits:[{oldText,newText}]` | 原始文本唯一匹配后修改；任一项无匹配、歧义或重叠则整次失败，文件不变；成功返回简短变更结果 |
| `write` | `path, content` | 新建或整体覆盖的结果；明确创建父目录的行为，不把整份内容再次回显 |

`read` 第一阶段可以先支持文本，不宣称具有尚未打通的图片输入能力。它不仅读取工程文件，也读取 bash 保存的日志，是大输出处理的一部分。单行超过字节额度时，应明确提示切片读取方式，不能返回空结果却声称已读完。

`edit` 优先采用精确匹配，一次多个不重叠修改均以原始文件为基准，完成全部验证后再写回。这样可防止一次调用前半成功、后半失败而留下未说明的部分修改。暂不引入 fuzzy replacer；是否需要容错匹配，等真实失败再判断。当前 Agent 顺序执行工具，暂不需要复制 Pi 的并发 mutation queue。

`write` 用于新文件及确实需要整体替换的文件；已有文件的局部修改优先 edit，以减少生成内容和误覆盖。不要另建 `create_file/update_file/save_file` 三条重复路径。

`bash` 先采用等待式执行，明确 cwd 和超时单位；默认超时需要配置，不照搬 Pi 的无限等待。非零退出、超时和无法启动进程应可区分。测试不通过时要保留实际输出，让模型能修复问题；一个非零退出不能丢掉日志只剩“工具失败”。终止时要清理进程树，避免命令超时后子进程继续运行。

### 为什么不只有 bash？

只有 shell 也能完成任务，工具 schema 最小，但分页、行号、编辑前置检查和输出语义都要每次由模型组合命令。四工具增加少量 schema，换来稳定的读取和编辑契约；shell 保持通用执行入口。这个取舍适合当前阶段，仍需实际任务验证。

### edit 与 apply_patch 怎么选？

patch 在多文件变更和增删文件时更有表达力，但还涉及格式解析、调用协议与模型习惯。建议先用 JSON edit/write，与当前 dispatcher 保持一致。后续可把 patch 文本作为普通 function 参数实现，不必绑定 OpenAI 原生协议；再用同一组任务比较成功率、修改正确性、请求次数和 token 成本。不能从 OpenCode 的模型名规则推导我们使用的 DeepSeek 一定更适合哪种格式。

## 6. 与 compact 一起成立的输出契约

建议先参考 Pi/OpenCode 的 **2,000 行、50 KiB** 作为单次模型可见文本的起始上限，按字节和行数计算，不依赖 tokenizer；这不是 token 数，也不是对所有模型都合适的最终值。包含包装字段和状态说明的最终结果仍需有界。

执行过程中流式写日志，保留小预览；超出额度时返回明确的 `truncated`、已展示范围和 `output_path`。该路径必须实际可由 read 分页读取，必要时用 bash 中的 rg 查找。读取原文件的分页无需复制全文；命令日志则要区分“预览被截断”与“采集过程丢失原始输出”。

全文文件可放在当前 session 的工具输出目录，与已有 history journal 聚合管理；具体目录和保留期在实现时确定。不能为了返回一个有限片段，先把无限输出全部收集到内存；也不能仅增加硬截断，随后让 agent 无法找回失败细节。

这个上限防止单条结果失控，不能保证整个 turn 永不超限。大量调用累积、用户直接粘贴的大输入和工具 arguments 仍可能超过 context；已有 overflow recovery 保留作用。不能把工具输出限制宣传成完整的超限解决方案。

## 7. 后续能力按具体任务加入

| 能力 | 什么时候值得加入 | 当前主要取舍 |
| --- | --- | --- |
| `grep/glob` | 找文件和定位代码频繁、shell 搜索不稳定或格式开销明显 | 稳定行号、范围和结果上限，代价是额外 schema；选择常见命名，避免与 find 重复提供同义工具 |
| execution session / stdin | 长编译、服务、实验需要 agent 等待期间继续工作 | 必须支持继续读取、退出状态、取消及 session 结束时清理；不能只返回一个无人管理的 running ID |
| `web_fetch/web_search` | 研究、最新资料成为经常性任务 | fetch 读取已知 URL，search 发现来源；需要真实搜索后端和适合上下文的内容，不等于注册 curl 的别名 |
| 计划／任务状态 | 多步骤任务在 compact 或恢复后反复丢失进度 | 需要明确状态归属与持久化，避免与长期 Intention 混为一层 |
| 询问用户 | CLI 能让工具暂停并接收结构化答案 | 依赖交互调度；普通最终回复已能提问，不必提前添加一套不可恢复的阻塞协议 |
| 子 agent | 基础 loop 与多任务调度成熟，有实际并行收益 | 上下文隔离、结果投递、取消和预算构成完整能力；task 不能只是递归调用 handle_user_message |
| Skill / MCP / tool search | 出现重复工作流或大量外部能力 | Skill 管理工作说明，MCP 提供外部接口，tool search 控制大 catalog 的暴露成本；四工具阶段无需全部铺开 |
| memory / reflection | 已证明成长机制提升任务结果，且状态归属确定 | 不提前注册“改变人格”工具；保持 experience、reflection、belief 的证据闭环 |

git、cargo、python 等命令先走 bash，不为每个现有 CLI 再做一层 builtin。阶段划分不意味着 SAKI 长期只做 coding agent；它只为当前本地任务提供最小可用起点。

## 8. 建议验收：验证行为，而非工具名

实施时优先检验真实边界：

1. **任务闭环**：同一工程任务能搜索、分页读取、局部修改、运行验证，再依据错误输出修复。
2. **编辑完整性**：无匹配、重复匹配、重叠修改或后一个 edit 失败时，原文件不被部分改动。
3. **输出边界**：大量输出、超长单行和多字节文本有界；返回的全文路径存在，后续能取回中间被省略的关键错误。
4. **进程生命周期**：非零退出、超时均保留状态和已捕获日志；超时不遗留子进程。
5. **真实 function-call 兼容性**：在用户授权的网关上验证 JSON arguments、工具结果 continuation、一次多处 edit 和失败后恢复；这轮研究没有做这些模型实测。

质量指标优先看任务完成率、错误修改、用户纠正次数；同时记录调用次数、上下文增长和延迟。只有结果显示四工具不足，才有理由扩大默认集合。

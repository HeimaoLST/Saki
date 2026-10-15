# Context compaction：harness 实践与职责划分

核对日期：2026-10-05。范围：Codex、Pi、OpenCode 的官方资料与固定版本源码；未安装运行第三方 harness，未调用压缩模型。本文件记录机制与设计推论，不声称摘要质量已经在 SAKI 上验证。

## 版本与证据边界

| 系统 | 固定 commit | 检查范围 |
| --- | --- | --- |
| Codex | `7f892275e31002f0422477c6219189284560e689`，2026-10-04 UTC | local compact、remote V2、turn 调度、context manager、rollout 重建 |
| Pi | `b9ab918c626ad3dd5edb58de037540f9467def88`，2026-10-05 UTC | compaction、AgentSession、SessionManager、序列化与官方说明 |
| OpenCode | `907b3bc518fa48e90e8ec24dd327d13eee71c36c`，2026-10-03 UTC | `packages/core` V2 的 compaction、history、配置；另外辨认 V1 路径 |

Pi 仓库现名为 `earendil-works/pi`，旧 `badlogic/pi-mono` 地址会重定向。源码链接均固定到本次核对 commit。Codex 官方 API 文档描述的公开 endpoint，与 Codex harness 使用的 remote V2 路径需要分别理解。

## 共同机制与关键差异

| 维度 | Codex | Pi | OpenCode V2：所查源码 |
| --- | --- | --- | --- |
| 触发 | 运行层按 context 状态判断，支持手动、turn 前／中／后路径 | 新 prompt 前、工具批次结束后的下一响应前，以及运行结束后的 overflow recovery | 估计 system、messages、tools 的大小，预留 output／buffer |
| 压缩表示 | 普通文本摘要；另有 provider 支持的 opaque compaction item | 结构化任务摘要 + 最近原始消息 | 结构化摘要 + 序列化后的 recent 文本 |
| 近期保留 | local 与 remote V2 有各自筛选，不能概括为保留完整最近 N 轮 | 按 token 预算反向选取，在合法消息边界切分 | 按序列化文本 token 预算选取；recent 工具输出也可能截短 |
| 持久状态 | rollout checkpoint 支持恢复 replacement history | 追加 CompactionEntry，原记录仍保留，投影重建上下文 | 新 checkpoint 入库，查询从最新 checkpoint 开始，不删除较早消息 |
| 职责 | turn／task 编排，独立 compact 模块，context manager 安装历史 | AgentSession 编排，独立函数准备／生成，SessionManager 持久化与投影 | 独立 compaction 服务生成事件，history／projector 管理存储与读取 |

这不是摘要质量排名。三者的模型、协议与数据结构不同，不能直接比较默认阈值，也不能据代码存在就断言哪一个更可靠。

## Codex：区分普通摘要与模型原生 compaction

当前手动任务与自动调度按 provider 的 remote compaction 能力选择路径。常规路径中，unsupported 使用 local 摘要，支持 V2 的 provider 使用 remote V2；另有 TokenBudget feature 分支，本次不作为 SAKI 第一版依据。[任务路由](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/tasks/compact.rs#L40)、[自动调度](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/session/turn.rs#L1482)。

local 路径复制待处理 history，用模型生成工作交接摘要，再构建 replacement history。所查 `build_compacted_history` 保留的是有预算的用户文本和摘要，用户文本预算为 20,000；它没有原样保留最近全部助手／工具交互。摘要本身作为带特定标记的 user context fragment 注入，不能把此前建议的 assistant-role 摘要说成行业唯一方式。[local 实现](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/compact.rs#L250)、[历史构建](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/compact.rs#L656)。

local 摘要请求若本身超出窗口，会从请求副本中移除较早内容后重试；删除调用项时，context manager 同时处理对应 output。这个回退会损失本次摘要可见的证据，不能等同于完整无损压缩。[失败处理](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/compact.rs#L317)、[配对删除](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/context_manager/history.rs#L668)。

remote V2 在模型请求中加入 `CompactionTrigger`，接收并检查 compaction output，然后将筛选后的输入与该 item 组合成新历史；所查筛选并不是普通近期工具 tail。模型调用在独立模块中，结果通过 Session 安装，而不是 history 容器自己选择 endpoint。[请求准备](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/compact_remote_v2_attempt.rs#L82)、[输出与重建](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/compact_remote_v2.rs#L442)。

公开 OpenAI API 另提供 `/responses/compact` 和 `context_management` 服务端 compaction。返回的 encrypted item 不供人阅读；standalone 返回的整个窗口应按文档继续传入，不能只抽出 item 或自行套用普通摘要裁剪规则。[官方 API 文档](https://developers.openai.com/api/docs/guides/compaction)。

**对 SAKI 的推论**：当前 localhost `/responses` 兼容入口尚未验证这些原生能力；HTTP 路径相似不代表支持。第一版优先采用可读摘要。不要照搬 Codex 的 opaque checkpoint，也不要把 Codex 的保留规则误写成通用方案。

## Pi：会话操作入口与具体实现分离

`AgentSession.compact()` 是面向调用方的会话操作。它协调停止当前运行、准备数据、生成结果、追加记录和刷新上下文；AgentSession 同时拥有 model、agent、settings 与 session manager，其职责比 SAKI 当前的 Conversation 容器宽。[手动 compact](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/agent-session.ts#L2717)。

独立 `compaction.ts` 提供 `shouldCompact`、`prepareCompaction`、`compact`。前两类准备函数处理预算和范围，`compact` 本身包含异步模型调用，不能把整个模块描述为无 I/O 的纯函数集合。SessionManager 通过 `appendCompaction` 保存 `summary`、`firstKeptEntryId`、`tokensBefore`、usage 等，再投影为摘要、保留的旧 tail 和新消息。[准备与生成](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/compaction/compaction.ts#L872)、[上下文重建](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/session-manager.ts#L472)、[追加 checkpoint](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/session-manager.ts#L1261)。

默认 `reserveTokens=16384`、`keepRecentTokens=20000`。计数优先使用有效 provider usage，加上之后新增消息的估算；经过 compact／context edit 后，不继续相信旧投影的 usage。没有有效 usage 时估算当前投影。[计数及阈值](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/compaction/compaction.ts#L120)。缓存 token 字段的定义依赖 provider，SAKI 不应直接照搬 Pi 各 usage 分量的加法。

切分不会从 toolResult 开始，避免工具结果与调用拆开。一个用户请求所包含的执行过程很长时，允许 split turn：把已移出的任务前缀生成继续工作所需的摘要，后面的完整交互仍保留。split turn 在有较早历史时可能需要两次摘要调用，不能预设 compact 总是一调用。[切点](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/compaction/compaction.ts#L351)、[split turn](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/compaction/compaction.ts#L976)。

摘要采用目标、约束、完成／进行／阻塞、决定和下一步等任务状态。文件列表从工具动作与上一次 checkpoint 累积。序列化给摘要模型时，工具结果每项限制到 2,000 字符并标明截短；这是摘要请求的输入裁剪，不代表原文从 session 中删除。[序列化](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/compaction/utils.ts#L94)。

输出因为 length 而停止时，默认摘要路径会拒绝把半份结果持久化。overflow recovery 具有一次 compact-and-retry 限制；手动 compact 不自动重试被中断的用户任务。接口错误、取消、阈值触发与 overflow 的结果需要区分。[摘要失败](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/compaction/compaction.ts#L585)、[自动处理](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/agent-session.ts#L2900)。

**对 SAKI 的推论**：最适合借鉴的是 prepare → generate → append checkpoint → rebuild context，以及 usage 失效与合法工具边界。第一版不需要 Pi 的分支树、扩展 hook 或两份摘要生成流程。

## OpenCode：V1、V2 和文档版本不能混用

固定 commit 同时有 `packages/opencode/src/session/compaction.ts` 的 V1 路径，以及 `packages/core/src/session/compaction.ts` 的 V2 路径。V1 的 tail turns、工具输出 prune，不可默认归入 V2。

所查 V2 按最终请求中的 system、messages、tools 估算，阈值为 context limit 减去 output allowance 与 buffer 的较大值。摘要请求使用会话模型且 tools 为空；生成失败、空摘要或摘要请求过大时，不发布成功 checkpoint。摘要之外的 recent 也是经过序列化的文本，工具输出会截短，附件转为说明文字。[V2 实现](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/session/compaction.ts)。

V2 history 查询定位最新 compaction 的 seq，从 checkpoint 和后续消息构建活动上下文；较早数据库消息不会因此删除。[history](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/session/history.ts#L11)。

**已发现的版本差异**：固定代码默认 keep 为 8,000、buffer 为 20,000；2026-10-05 打开的主站 V2 文档写 keep 为 15,000、buffer 为窗口的 10%，并说明 native provider compaction。所查固定代码的这条 V2 摘要路径没有该 native 分流；开发站文档与固定代码的默认值较接近。这里只报告差异，不根据局部代码断言整个项目不存在 native 支持。[主站文档](https://opencode.ai/v2/docs/compaction)、[开发站文档](https://dev.opencode.ai/v2/docs/compaction/)。

**对 SAKI 的推论**：请求预算包含工具定义这一点值得借鉴。近期“保留”需明确是否保留原始协议项；第一版建议保留近期完整原文，只有摘要请求中的超大结果另做显式裁剪。

## 自动压缩阈值：容量不是触发线

以下是上述固定版本的基础路径，不将额外 TokenBudget／scope 模式简化成统一比例。

| 项目 | 默认基础规则 | 1,000,000 容量下的示例 |
| --- | --- | --- |
| Codex | 模型基础阈值为 `floor(context × 0.9)`；已配置的模型阈值取与它的较小值 | 900,000 |
| Pi | `contextTokens > contextWindow - reserveTokens`；默认 reserve=16,384 | 大于 983,616 |
| 当前 SAKI | 输入用量达到 `context_budget - reserve_tokens`；默认 reserve=8,192 | 达到 991,808 |

Codex 的 [ModelInfo::auto_compact_token_limit](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/protocol/src/openai_models.rs#L526) 明确派生 90%；另一个 `effective_context_window_percent` 默认 95%，表示可用窗口上限，两者不同。[context_window.rs](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/session/context_window.rs#L56) 还支持 Total／BodyAfterPrefix 的计数范围和 TokenBudget fallback buffer，因此实际触发不能一概描述成总用量恰好到 90%。另外有 `model_post_turn_compact_threshold_percent`，默认 0 表示关闭 turn-end 百分比触发。

Pi 的 [shouldCompact](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/compaction/compaction.ts#L267) 使用减去固定预留的规则；[配置文档](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/docs/compaction.md#L439) 支持 exact provider/modelId 的 modelOverrides。文档用 1M 模型、400K reserve 举例，使触发线降到 600K；reserve 同时影响摘要输出预算，不能仅理解成触发参数。它不是固定 80% 或 90% 策略。

**下一步建议，尚未实施**：SAKI 将触发阈值与实际输出上限分别配置，可以采用 `min(90% × capacity, capacity - max_output_tokens)` 作为初始策略，再验证质量、延迟和开销。当前 1M 下的 8K reserve 只留下约 0.82% 空间；这是当前代码行为，不是已验证最优策略。

## 上游超限错误恢复

2026-10-05 补查相同固定版本的错误处理源码。**捕获上游溢出错误是必要兜底，但恢复时必须改变请求，而不是原样重试。** 这与按 usage 提前触发压缩互补；首次输入和新工具结果没有 usage 时尤其需要兜底。

| 系统与路径 | 正常请求超限后的处理 | 摘要请求自身超限 |
| --- | --- | --- |
| Pi | overflow 排除出普通重试；省略失败尝试的活动投影，compact 后继续一次；连续失败再次 overflow 时停止恢复 | 摘要生成错误返回失败，不递归裁剪重试 |
| OpenCode V2 | 尚未开始 assistant 输出时 compact，重建请求；恢复后的 attempt 不再开启 overflow recovery | 本地估算超限直接失败；摘要流错误或空文本也失败 |
| Codex 基础 local 路径 | sampling 的 ContextWindowExceeded 标记窗口已满并返回；常规 turn 报错后让用户继续，下次请求前可触发自动压缩 | 移除摘要请求副本中的最早历史项，按配对规则处理工具项，重新尝试；无可缩减项时返回错误 |

Pi 的恢复标记在新用户消息或成功 assistant 响应时重置，因此限制的是连续失败恢复链，不是整个长任务只能压缩一次。[恢复分支](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/agent-session.ts#L2972)、[标记重置](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/agent-session.ts#L1091)、[普通重试排除 overflow](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/agent-session.ts#L3661)。摘要报错或 length 截断不写成功 checkpoint。[摘要校验](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/compaction/compaction.ts#L585)。

OpenCode V2 的 `!publisher.hasAssistantStarted()` 限制防止整体重放已经开始输出的执行过程；重建后的调用不传 `recoverOverflow`，因此该恢复链只有一次机会。[runner 条件](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/session/runner/llm.ts#L291)、[重建请求](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/session/runner/llm.ts#L364)、[摘要失败](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/session/compaction.ts#L190)。

Codex 不能概括为所有请求都自动“compact 后原地重试一次”。普通 sampling 在 [ContextWindowExceeded 分支](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/session/turn.rs#L1716) 立即返回，普通 turn 的[通用错误分支](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/session/turn.rs#L822) 保留会话。另有 guardian review budget 的专门分支，对当前模型 step compact 后继续一次；不能把该条件分支推广成通用行为。[特殊恢复](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/session/turn.rs#L760)。local 摘要自己的超限恢复见 [compact.rs](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/compact.rs#L317)，裁剪会减少摘要可见证据。公开 OpenAI `/responses/compact` 也要求提交的窗口本身仍在容量内，不能把原生压缩 endpoint 当作接受任意超长请求的接口。[官方说明](https://developers.openai.com/api/docs/guides/compaction#standalone-compact-endpoint)。

OpenCode V1 另外将 overflow 转为 `needsCompaction`，外层建立压缩任务，摘要再次超限则 stop。外层存在 `while(true)`，本次未见 Pi／V2 的单次恢复标记，不能写成固定只重试一次。[processor](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/session/processor.ts#L621)、[摘要再次超限](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/session/compaction.ts#L450)。

### 超大工具结果与错误分类

工具产生单条巨大结果，不能只依赖历史摘要。Pi 内置 read／bash 默认限制 2,000 行或 50 KiB；bash 保存完整输出到临时文件并返回预览和路径，read 支持按范围读取。该约束不自动覆盖所有自定义工具。[read](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/tools/read.ts#L76)、[bash](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/tools/bash.ts#L255)。OpenCode V2 在 registry settlement 对输出调用 `ToolOutputStore.bound`，使用同样的默认行数／字节上限，完整内容落盘。[registry](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/tool/registry.ts#L75)、[输出存储](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/tool-output-store.ts#L138)。这与给摘要模型的工具文本限制 2,000 字符是两个环节。

不能把所有 HTTP 400／413 都视为上下文溢出。Pi 按 provider 错误文本识别并排除限流，还检查成功响应 usage 超限与某些零输出 length stop；所以也不能假定所有 provider 都必然返回同一种 HTTP 错误。[overflow 分类](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/ai/src/utils/overflow.ts#L137)。SAKI 当前网关实际返回结构化 `error.code=context_length_exceeded`，可以先支持这一明确契约，保留未知错误，按需要再增加 provider 兼容规则。

### 对 SAKI 的建议与实施决定

保留 usage 自动压缩；Client 区分上下文超限、瞬时错误和不可重试请求错误。超限立即返回给 Agent，由 Agent 调用 Conversation 强制压缩，成功后重建模型请求并重试一次；第二次仍超限或无法缩减时结束当前任务并保留会话。恢复只重试失败的模型请求，已经执行过的工具不重新执行。Conversation 继续拥有切分、摘要、checkpoint 和安装新上下文的职责。

首次单条用户消息自身已超过窗口时可能没有旧历史可压缩。工具单条结果过大时则可考虑“完整落盘 + 有界预览 + 按需读取”。摘要自身过大，Pi／V2 选择明确失败，Codex local 选择裁剪请求副本；SAKI 第一版选择明确失败，避免静默移除摘要证据。工具预览／分块策略仍是未实施建议。

[百万窗口网关实测](../../experiments/compaction-gateway-report.md) 的初始结果验证了 SAKI 原样重试真实超限请求三次，还定位到最近用户消息被移走引起的工具 continuation 协议错误。用户随后明确授权参考 Pi 实施恢复；现已接入 Client 分类、Conversation 强制入口和 Agent 有界恢复，并将 tail 切点收紧到用户消息边界。具体契约与修复后的验收见[实现文档](../../design/conversation-compaction.md#上游超限恢复)及同一实验报告。

## Conversation 内实现，还是单独实现

这是状态所有权与执行流程的划分，不是文件名的二选一。

| 方案 | 优点 | 具体代价 |
| --- | --- | --- |
| Conversation 包办准备、模型请求、应用 | 调用简洁，容易保持流程连贯 | 会话容器同时依赖模型、客户端和运行配置；摘要失败进入会话操作的错误语义 |
| 外部模块包办并直接修改消息 | 摘要实现集中，容易替换模型 | 如果直接改 Vec，工具边界与历史替换不变量容易分散 |
| Conversation 准备与应用，Agent 协调摘要 | 状态规则集中，复用现有客户端和 loop；准备／替换可离线验证 | 多一个明确的准备结果，执行器需要串起一次压缩流程 |

**原研究建议**是第三种：Conversation 提供 `prepare_compaction`、`apply_compaction` 与活动上下文读取；Agent 提供手动／自动触发并调用客户端。这些是研究时的建议接口，本次没有采用。独立模块可以只承载摘要 prompt／生成流程；当前实现短时，先留在 Agent，不要求提前创建 `compact.rs` 或策略 trait。

压缩后重新估算当前投影，并使压缩前正常响应的 usage 基线失效。摘要请求的 usage 记录压缩开销，不能当作压缩后正常请求的上下文大小。以后收到正常请求的新 usage 才建立新的基线。

第一版保留线性日志即可，不复制 Pi 的树或 OpenCode 的事件服务层。压缩成功先保存 checkpoint，再切换活动上下文；不删除原始日志。工作摘要不触发 Belief 更新，也不成为新的独立证据。

当前 SAKI loop 同步处理输入和工具，不必先做并发 checkpoint 协议。如果以后摘要期间允许新用户输入、后台事件或子代理消息进入，会话层必须只应用摘要所覆盖的范围，并保留之后追加的内容；届时再引入必要的版本／范围校验。

验收重点：工具配对、单个长任务的中途 compact、连续两次 compact 的 tail 覆盖、失败／截断不替换历史、压缩后 token 基线更新，以及重建后能继续完成任务。方案边界同步记录在[架构提案](../../design/experience-to-behavior.md)。

## 本地实施决策

2026-10-05 用户选择先将完整压缩操作交给 Conversation；随后限定只做自动路径，command 系统另行设计。当前 Conversation 按参数借用客户端、模型与工具定义，完成判断、生成与应用，Agent 只在请求前调用并记录正常 usage。容量来源随后按用户选择调整为 models.dev 的定期缓存：完整 ID 精确匹配优先，其次统一 model 名称，都失败默认 256K。用量优先读 `/responses` usage，不使用 tokenizer。原方案比较保留，不再作为“尚待实施”的决定。已实现契约与测试见[Conversation 自动压缩](../../design/conversation-compaction.md)。

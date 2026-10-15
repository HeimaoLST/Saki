# Agent runtime 需求交接：多 session 重叠执行与可观察事实

更新日期：2026-10-15。
状态：**需求交接，未实施**。
范围：本文交给负责 agent runtime 的 agent 实施前复核；只描述已确认的需求边界与当前观察，不把任何实现方案当作已批准需求。

> 观察范围提醒：本文中的代码事实来自当前工作区（2026-10-15），不是某个固定版本；仓库正被并行修改，相关源码、命令枚举和事件类型在实施前必须重新检查。“单 active job”是当前 `SessionManager` 实例的限制：它不代表 `Agent` 存在进程级全局 busy，也不代表不同进程中的独立 session 现在不能同时运行；当前已支持多个持久 session。本轮目标是让通用调用接口在同一宿主／管理入口下支持独立 session 重叠执行，而不是要求用户另开 TUI 进程来实现；这不等于全局重写旧设计，也不等于每个 session 或联系人都变成新身份。
>
> 状态说明：状态栏的“未实施”指**本文的新目标**（多 session 重叠执行等）尚未实施，不代表代码中相关基础能力都不存在。给 runtime agent 的待办是先核对差距，再评估实现方案；本文不强行规定技术方案。

## 背景与范围

- Saki 是可由 TUI／GUI／Web／Bot／API／脚本承载的通用 agent runtime；GUI（包括 `desktop/` 的 MyGo 设计预览）只是前端之一。
- 多个独立 session 的重叠执行是 runtime 能力，不能只靠 GUI 多窗口或在应用层复制 agent loop。不得把并发整体归到应用层。
- 本轮重点是 runtime 缺口与验收事实，不设计 contact/前端体验；桌面产品需求见 [Desktop contacts](../design/desktop.md)，会话现状见 [Session management](../design/session-management.md)，身份边界见 [Persistent identity](../design/persistent-identity.md)，压缩现状见 [Conversation compaction](../design/conversation-compaction.md)。

## 术语与边界

| 术语 | 含义 | 边界 |
| --- | --- | --- |
| Runtime | 承载 session、模型调用、工具执行、compact 与事件的核心 | 宿主／调用方不拥有 agent loop；不得复制 loop 到应用层 |
| Session | 一段独立历史与执行的归属单元 | 不等于身份；不同 session 可并发 |
| Task / Turn | 一次用户提交到该轮结束的执行 | 同一 session 内新消息如何处理待决定 |
| Contact | 桌面产品的长期对象（profile／要求／历史） | 产品应用层概念；不要求所有宿主使用，不自动跨联系人注入历史 |
| 产品应用层 | 联系人、预设、配置组装与调用策略 | 复用 runtime 能力；不复制模型／工具／compact loop |
| 宿主／调用方（含前端与 API／脚本） | TUI／GUI／Web／Bot／API／脚本 | 提交用户意图／请求并观察执行事实；不编排内部 model／tool／compact loop |
| Compact | 有限工作上下文＋共享单一摘要路径＋保留原始历史 | 不新增第二套日志 parser／compact；不设 desktop 专用分支 |

## 边界评审要求

新增功能在提交时需标注各部分的归属与契约，不需要新框架：

- 区分事实（runtime 可观察行为）、产品规则（应用层策略，如联系人与预设）与表现（前端展示）。
- 写明涉及的跨层契约与验收方式；runtime 不依赖任何前端，执行语义必须能在无 GUI 的情况下验证。

## 已确认的行为需求

1. **多 session 重叠执行是 runtime 能力。** 不同 session 可以同时处于执行中；调用方创建、加载、查看 B 不应因 A 忙碌而被全局 Busy 阻塞，切换查看对象或查看历史不影响进行中的执行，也不会取消或替换 A。
2. **调用方按 session／task 明确操作，不绑定“全局当前选中会话”。** 需要能创建、加载或恢复、提交、查询／观察某个 session 的执行；查看历史或恢复状态不自动重跑模型与工具。
3. **同一 session 在运行中收到新消息的策略未决定。** 拒绝、排队、追加或分支都是候选；不得承诺同一历史可无约束并发写入。并发也不保证工作目录文件互相隔离。
4. **执行事实可观察并可靠关联。** 需要区分“消息被接受”与“开始处理”；session／task／输入消息与工具调用／输出的关联必须可靠；模型请求、工具、输出、结束、失败的已有事件与缺口要能被调用方看见。已有的 `AgentEvent` 优先复用；确需补充的事件必须对任意调用方有意义，而不是 GUI 专用。
5. **联系人是产品应用层对象。** 联系人有独立 profile／要求／历史；缺省 agent 指无需用户先定义 profile／用途，宿主仍需有效的 provider／model／凭据与工具配置；自定义与预设可选。编辑 profile 只影响之后请求，不重写历史。runtime 提供配置能力，但不强制所有宿主使用联系人，也不自动跨联系人注入历史。
6. **长期历史保留 + 有限工作上下文 + 共享单一 compact。** 摘要不保证无损；以保留原始历史和按需检索弥补。建议的 `search_history`／`read_history` 优先复用现有 `ToolRegistry` 按适用范围注册，具体注册位置由宿主／应用层组装决定（工具名是既有讨论建议，API 细节待设计）；TUI 不必启用，GUI 复用同一 loop/compact。检索默认限定在当前联系人／绑定范围、不泄漏其他联系人，需带来源和范围、能在 compact 后读回原始内容，且 session 之间不自动把彼此历史混入模型上下文；不能引入第二套 parser/compact 或 `if desktop` 分支。搜索算法、向量库与索引均未决定。

## 当前实现的可核验依据（观察，需实施前复核）

- **[src/session/mod.rs](../../src/session/mod.rs)**：`SessionManager` 持有单个 `agent: Option<Agent>`（约第 59 行）；`run` 的注释写明 “A single active job”（约第 161 行）；`Create`／`Open`／`Send` 在 agent 忙时 `bail!("Agent is busy...")`（约第 126、138、143 行）；`SessionCommand` 目前只有 `Send`／`List`／`Create`／`Open`／`Rename`（约第 18–32 行）。
- **[src/agent/mod.rs](../../src/agent/mod.rs)**：`AgentEvent` 已包含 `ContextUpdated`、`ResponseText`／`ResponseTextDelta`、`Reasoning*`、`ToolStarted`、工具结束、`TurnCompleted`／`TurnFailed` 等（约第 21 行起）；存在 `with_cancel`（约第 163 行）与 `TurnInterrupted`（约第 85 行）。是否已端到端接通取消、以及其语义，必须由负责 agent 复核：`with_cancel`、`interrupt_task` 与事件的存在**不能**推出“取消已完整实现”。
- **[src/agent/conversation.rs](../../src/agent/conversation.rs)**：`interrupt_task` 存在（约第 211 行）；`compact_if_needed`／`compact`、`with_compaction_config`、`with_history_file`／`save_history` 按 [Conversation compaction](../design/conversation-compaction.md) 描述工作。
- **[src/agent/journal.rs](../../src/agent/journal.rs)**：JSONL 追加与重放、`schema_version: 1`、compaction checkpoint，且已有 `task_id`（`TaskStarted`／`TaskFinished`，约第 37、40、184 行）；[conversation.rs](../../src/agent/conversation.rs) 以 `active_task` 记录中断（约第 166、214 行）。任务 ID 并非完全不存在，但 `AgentEvent` 目前不携带 task／message 关联，实施前需复核字段与语义。
- **[src/agent/message.rs](../../src/agent/message.rs)**：`UserMessage` 目前只有 `text` 与 `images`（约第 60–63 行）；提交侧尚无 task／message 关联字段。
- **[src/tools/mod.rs](../../src/tools/mod.rs)**：`register_builtins` 目前注册 read／bash／edit／write（约第 34 行）；历史检索工具可复用 [tool_registry.rs](../../src/tools/tool_registry.rs) 的注册能力，但注册位置与范围由宿主／应用层组装决定，不限定在 `register_builtins` 或现有四工具初始化里。
- **[src/frontend/mod.rs](../../src/frontend/mod.rs)** 只有 console／tui；[src/app.rs](../../src/app.rs) 负责前端选择与组装。runtime 本身不依赖终端。
- 联系人、profile、历史归属目前只是产品设计，不是已实现的 runtime 概念。

## 建议验收场景（用受控 mock／确定性协调，不用 sleep 碰运气）

1. **重叠执行**：A 正在执行工具时，B 可被创建／加载／提交，且 A、B 的执行按确定性步骤交错完成。
2. **观察归属不串**：切换查看 A／B 时，事件、工具调用与输出归属到正确的 session／task；查看 B 不取消或替换 A。
3. **失败隔离**：A 失败不错误地结束 B；B 的进行中执行与状态保持；失败事实可被调用方观察到。
4. **同一 session 冲突**：在 A 运行中向 A 提交新消息，其行为与所选策略（拒绝／排队／追加／分支）一致且被文档化；测试断言该明确行为，不依赖偶然顺序。
5. **无 GUI 循环**：不使用任何前端界面，调用方即可完成一次完整的 model／tool 循环并拿到结果。
6. **compact 后可检索且按授权／绑定范围隔离**：compact 之后仍能通过检索读回原始内容；默认在当前联系人范围内检索，不泄漏其他联系人。同一联系人未来可能映射多个 session，映射未决定前不对跨 session 检索一概禁止；session 之间不自动把彼此历史混入模型上下文。
7. **缺省配置可提交**：宿主基础配置（provider／model／凭据／工具）有效时，不要求联系人额外配置 profile 或覆盖模型即可提交任务；不声称无需凭据。
8. **仅查看／恢复不重跑**：加载、查看历史或恢复状态不触发模型调用或工具执行。
9. **取消／断连（待定契约）**：取消、订阅断开、资源释放与宿主退出的确切语义先在“待决定事项”中定稿，再按定稿契约验收；在此之前不作为必须实现项。

## 明确待决定事项

- 同一 session 运行中收到新消息：拒绝／排队／追加／分支的取舍与可见反馈。
- 并发上限、调度与公平性、资源与工作目录隔离（不承诺自动隔离）。
- 取消、订阅断开、释放与宿主退出的契约。（查看切换不影响进行中执行，已确认。）
- contact ↔ session／identity 的映射，以及 profile／预设如何作用于未来请求；不全局重写现有身份与会话边界设计。
- 历史检索工具的名称、入参、范围与返回结构；搜索算法／索引／向量库不预设。
- 为可观察性需补充的 `AgentEvent` 具体形状；“已读”的产品含义仍未决定，不能承诺“已理解”或“永久记忆”。
- 消息“接受成功 ≠ 开始处理 ≠ 模型理解”；输出可能先有文字再调用工具，避免编造简单不可逆的阶段机。

## 非目标

- 不新增 GUI 专用 runtime 状态（头像动画、已读回执、当前面板等）；头像造型本身不在本轮范围。
- 不让前端手动编排 model／tool／compact；不在应用层复制 agent loop。
- 不规定新接口的名字、crate、actor、IPC 传输、无限并发或后台 daemon。
- 不强制所有宿主使用联系人，不自动跨联系人注入历史。
- 不新增第二套日志 parser 或 compact，不引入 `if desktop` 分支。
- 不把本轮用户未决定的事项（尤其取消要求）升级为“已确认必须实现”。

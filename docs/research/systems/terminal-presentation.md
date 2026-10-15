# Terminal presentation：Pi、Codex 与 OpenCode 的信息层次与能力矩阵

核对日期：2026-10-13；能力矩阵补充 2026-10-15。本研究回应 SAKI 实测中的工具链噪声、推理占位、超长行与 Markdown 表格失去结构的问题。第 1–7 节比较消息、活动、推理、Markdown、主题与底栏；第 8 节把固定版本的 TUI 能力矩阵沉淀为长期事实。范围是显示与交互；布局、终端缓冲区和组件选型见 [Terminal UI](terminal-ui.md)。对应提案见 [TUI style](../../design/tui-style.md)。

## 证据与版本

本机 2026-10-13 核对时 `codex --version` 为 `0.162.1`，`pi --version` 为 `1.0.2`。下面读取与安装版本对应的官方发布源码，不把 main 的变化混入版本结论。检查 Pi 安装包中的组件也确认了同样的主要行为。本次没有对两个产品做完整交互或可用性对照实验。

| 产品 | 发布版本 | 固定 commit |
| --- | --- | --- |
| Codex | `rust-v0.162.1` | `092d3acd6bec3e3a14bdc7e7a2810ab628ab759d` |
| Pi | `v1.0.2` | `cd32f7725fdbddbaecdff5b1e68491563394e0ca` |

2026-10-15 追加复核：本机已升级到 Pi `1.2.0`、Codex `0.162.1`；三个第三方最新稳定 release 的 peeled commit 与逐维度能力矩阵见第 8 节。第 1–7 节保留 2026-10-13 的源码快照。

## 1. 消息通过版面区分角色

Codex 的 [UserHistoryCell](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/history_cell/messages.rs#L181) 使用 `›`、背景与上下留白区分请求，续行对齐正文列。Assistant 使用正文和轻量标记，没有给每个段落重复加产品名。

Pi 的 [UserMessageComponent](https://github.com/earendil-works/pi/blob/cd32f7725fdbddbaecdff5b1e68491563394e0ca/packages/coding-agent/src/modes/interactive/components/user-message.ts#L38) 设置成对的用户前景／背景和一行上下内边距。[AssistantMessageComponent](https://github.com/earendil-works/pi/blob/cd32f7725fdbddbaecdff5b1e68491563394e0ca/packages/coding-agent/src/modes/interactive/components/assistant-message.ts#L98) 则直接渲染无背景的正文；段落与工具之间也避免叠加多余空白。

规律：角色交给位置、底色、标记承担；正文保留给信息。每条消息都放角色标题会增加阅读成本。

## 2. 工具显示动作与对象，详情渐进展开

Codex 的 [exploring_display_lines](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/exec_cell/render.rs#L313) 把一组读取／列目录／搜索表示为 `Exploring / Explored`，下方用 `Read / List / Search` 和对象构成短树；相邻成功读取还会合并文件名。普通命令显示 `Running / Ran`，有命令换行与输出预览上限，完整 transcript 提供更多内容。复合命令只报告实际拥有的命令退出码，不伪造每个子动作的结果；搜索退出码 1 也不直接解释成执行故障。

Pi 的 [ToolExecutionComponent](https://github.com/earendil-works/pi/blob/cd32f7725fdbddbaecdff5b1e68491563394e0ca/packages/coding-agent/src/modes/interactive/components/tool-execution.ts#L134) 把调用、结果、状态组织为一个可展开对象，并允许工具提供专门 renderer。通用结果 fallback 默认预览前 10 行，提示剩余行数，点击可展开；这个上限不代表所有内置／扩展工具的统一显示策略。默认外壳通过 pending／success／error 背景区分状态，专用 renderer 可以自行组织外壳。

规律：先回答“在对哪个对象做什么”，需要时再回答“确切参数和完整输出是什么”。显示折叠与工具层截断不同；折叠不能成为删除原始依据的理由。

## 3. 推理的显示取决于状态与阅读模式

Codex 的 [ReasoningSummaryCell](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/history_cell/messages.rs#L324) 使用弱化斜体；`transcript_only` 内容在紧凑历史返回空行集，在完整 transcript 中保留。这证明其显示路径支持“普通阅读不占位，详细阅读可查看”，不意味着所有模型都返回同样的推理内容。

Pi 的 [thinking run](https://github.com/earendil-works/pi/blob/cd32f7725fdbddbaecdff5b1e68491563394e0ca/packages/coding-agent/src/modes/interactive/components/assistant-message.ts#L118) 合并连续 thinking 内容，用次级色和斜体显示；隐藏时仍保留标签，支持逐段点击切换。不能据此声称 Pi 完成推理后完全不留入口。

规律：工作中需要观察当前进展，完成后主要需要读取结果。不同模式可以显示同一份记录的不同投影。

## 4. Markdown 是结构化排版

Codex 的 [Markdown renderer](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/markdown_render.rs#L12) 为标题、代码、列表、表格提供独立路径。标题通过粗体、下划线、斜体区分，不依赖字体变大。表格按内容分配列宽，窄到失去可读性时转为 key/value 记录，保留字段关系。

Pi 的 [Markdown renderer](https://github.com/earendil-works/pi/blob/cd32f7725fdbddbaecdff5b1e68491563394e0ca/packages/tui/src/components/markdown.ts#L766) 计算列表标记宽度，使续行对齐正文，并支持嵌套和任务列表。其 [表格布局](https://github.com/earendil-works/pi/blob/cd32f7725fdbddbaecdff5b1e68491563394e0ca/packages/tui/src/components/markdown.ts#L852) 按列计算显示宽度、收缩与单元格换行；极窄时回退为换行后的原始 Markdown。此版本的表格单元格统一左对齐，不能说它已按数值自动右对齐。

规律：软换行必须保留列表层次、字段归属和代码边界。对所有内容使用同一个段落换行器不能代替排版。

## 5. 色彩是语义角色，次级信息也必须可读

Pi 的 [dark theme](https://github.com/earendil-works/pi/blob/cd32f7725fdbddbaecdff5b1e68491563394e0ca/packages/coding-agent/src/modes/interactive/theme/dark.json) 分开定义 text、muted、thinking、user surface、tool state、Markdown 与 diff 颜色。统一的地方是角色映射，不是让每个组件都显著着色。

Codex 的 [style.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/style.rs#L60) 根据可用的终端背景产生请求底色，未知背景有回退；[secondary_text_style](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/style.rs#L142) 用计算过的前景而不是仅依赖终端 dim。强调与状态也考虑背景和色彩能力。Pi 使用更明显的工具状态背景，Codex 这组路径更偏文本标记，两者并非同一套视觉风格。

规律：正文最清楚，过程退一级，装饰再退一级；弱化不能使信息难以辨认。颜色与文字／符号共同表达状态。

## 6. 输入提示与状态信息职责不同

Codex 的 [footer](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/bottom_pane/footer.rs#L15) 明确区分配置式 status line、临时操作提示与环境信息。内容由输入模式和真实状态决定；宽度不够时按优先级降级，不把完整快捷键参考永久放在底栏。

Pi 的 [footer](https://github.com/earendil-works/pi/blob/cd32f7725fdbddbaecdff5b1e68491563394e0ca/packages/coding-agent/src/modes/interactive/components/footer.ts#L198) 将目录／分支与 usage／模型分行组织，模型靠右；空间不够先移除可选 provider，再处理截断。它的默认统计较丰富，SAKI 没必要全部照搬。context 色彩阈值是显示策略，不能混为自动压缩阈值。

规律：状态回答“现在是什么情况”，帮助回答“我能怎么操作”。底栏需要可预测的降级顺序。

## 7. 终端主题检测的输入所有权（2026-10-14 补充）

自动跟随终端浅／深外观时，颜色的“真相”在终端，但读取它的通道与键盘输入争用同一个 tty：

- 上游 crates.io `crossterm-0.29.0` 的 `EventStream` 拥有输入；本地 registry 源码没有 `OscColor`、`buffer_input` 或 `997` 处理。它的 unix `parse_event` 会把 `ESC ]` 当作 Alt-] 继续解析为按键，`Event` 没有颜色／主题事件。
- Codex 0.162.1（固定 commit `092d3acd6bec3e3a14bdc7e7a2810ab628ab759d`）在 `codex-rs/Cargo.toml` 里虽然声明 `crossterm = "0.29.0"`，但用 `[patch.crates-io]` 指向 [openai-oss-forks/crossterm](https://github.com/openai-oss-forks/crossterm) 的 rev `ed1cdab335221515706178d68495bba2aed1924f`。该 fork 的 `parse_osc` 把 OSC 10/11 解析为 `InternalEvent::OscColor`，`EventFilter` 只把 `InternalEvent::Event(_)` 暴露为公开按键事件，OSC 回复保留在内部队列（`OscColorFilter` 可取），所以迟到的 OSC 回复既不会成为按键，也不会触发缓存更新；该 fork 还公开 `crossterm::event::buffer_input`。[terminal_probe.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/terminal_probe.rs) 的 `DEFAULT_TIMEOUT` 是 250 ms，`Tty::open` 先 `dup` stdin/stdout（必要时回退 `/dev/tty`），所有查询共享一个 deadline；启动探测期间读到的真实按键／粘贴字节通过 `buffer_input` 回放，源码注释明确 `read_until` 只能用于“正常轮询开始前或暂停时”。对整个 `codex-rs/tui/src`（固定 commit，1101 个 `.rs` 文件）搜索没有 `2031`、`OscColor` 或运行期主题订阅；`terminal_palette.rs` 以 attempted 标志缓存 fg/bg，[style.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/style.rs) 从采样的背景推导 light/dark、按色彩深度降级，未知用 `Color::Reset`。**版本结论：0.162.1 未发现 mode2031 或原生 palette 重查询路径；但终端自身改变时，继承的默认前景／背景仍会随终端自动变化（`Color::Reset` 与 terminal defaults 属于继承渲染），Codex 只是不会显式重算 palette。启动探测／缓存与惰性有界回退是它显式采样颜色的全部时机。**
- Pi 1.2.0（release commit [`06a46693`](https://github.com/earendil-works/pi/commit/06a466935536f8c9bcacb2d5efc0fc021be1bdff)；[terminal-colors.ts](https://github.com/earendil-works/pi/blob/06a466935536f8c9bcacb2d5efc0fc021be1bdff/packages/tui/src/terminal-colors.ts)、[theme-controller.ts](https://github.com/earendil-works/pi/blob/06a466935536f8c9bcacb2d5efc0fc021be1bdff/packages/coding-agent/src/modes/interactive/theme/theme-controller.ts)）的 `terminal-colors.js`、`theme-controller.js`、`system-theme.js` 与 1.0.2 安装包逐字节相同，`tui.js` 的差异只是 fake-cursor APC 标记，不涉及主题查询。它仍在自己的输入读取器内发送 OSC 10/11 加 OSC 4（ANSI 0-15）并以 DA1 结束，`queryTerminalColors({timeoutMs, onLateReply})` 在超时后继续收集迟到回复，并 `\x1b[?2031h/l` 开关运行期主题通知；未知时先以 grayscale（native 前景）显示。
- tmux 3.7c 已跟踪外层终端的主题：本机只读执行 `tmux display-message -p -t %1 'theme=#{client_theme} term=#{client_termname}'` 返回 `theme=light term=xterm-ghostty`；官方 [format.c](https://raw.githubusercontent.com/tmux/tmux/3.7c/format.c) 包含 `client_theme`，[CHANGES](https://raw.githubusercontent.com/tmux/tmux/3.7c/CHANGES) 记录 mode2031 自 3.6 起支持。`client-light-theme`／`client-dark-theme` hook 需要设置 hook 才能被动观察，未采用。
- Helix master（[`ba40e547`](https://github.com/helix-editor/helix/commit/ba40e547426b0f9896c8bdc699a4ab11f2b37dbc)；[helix-term/Cargo.toml](https://github.com/helix-editor/helix/blob/ba40e547426b0f9896c8bdc699a4ab11f2b37dbc/helix-term/Cargo.toml)、[application.rs](https://github.com/helix-editor/helix/blob/ba40e547426b0f9896c8bdc699a4ab11f2b37dbc/helix-term/src/application.rs)）同时保留 crossterm 0.28 并引入 [termina](https://github.com/helix-editor/termina) 0.3（`helix-term` 使用 `features = ["event-stream"]`）。Termina v0.3.3（annotated tag `54f04061…`，peeled source [`6f787136`](https://github.com/helix-editor/termina/blob/6f7871364af18f2a36a2be28cfc658fea130992c/src/parse.rs)）公开 `Parser::parse(bytes, maybe_more)`／`pop()` 与 `Event::{Key, Mouse, Paste, Csi, Osc, Dcs, …}`：`parse_osc` 支持 OSC 10-19，`parse_csi_theme_mode` 支持 `CSI ?997;1n`（dark）／`;2n`（light），括号粘贴包装为 `Paste(String)`。`PlatformTerminal::new()` 打开平台终端并捕获 termios，但 raw mode 由显式的 `enter_raw_mode`／`enter_cooked_mode` 和 Drop 管理；alternate screen、括号粘贴、鼠标与 focus 模式由调用方写入，启动不主动发颜色查询。
- 独立 PoC（`/tmp/termina-probe-poc`，`termina = "=0.3.3"`）：分片的 OSC10、OSC11（BEL）、`CSI ?997;1n/2n`、跨分片 UTF-8、括号粘贴内嵌伪 OSC/997 内容、混合顺序，以及“未完成 OSC 不泄漏为 Alt-] 按键”全部通过；已知限制是 0.3.3 的 `RgbColor::from_str` 只接受 `rgb:` 与 `#hex`，`rgba:` 回复被丢弃（Pi 接受 `rgba:`）。PoC 只证明 framing，不证明整棵 TUI 可直接替换。

| 实现 | 查询来源 | 输入所有权 | deadline／迟到回复 | 运行期跟随 | 未知回退 | 依赖负担 |
| --- | --- | --- | --- | --- | --- | --- |
| crossterm 0.29（SAKI 输出仍用） | 无（上游） | 历史上曾是其 `EventStream`；SAKI 现仅用于输出 | 不适用 | 无 | — | 已有 |
| Codex 0.162.1 | OSC 10/11、CSI 6n、DA1 | dup stdin/stdout 或 `/dev/tty`；独占启动窗口；剩余字节 `buffer_input` 回放；`EventFilter` 将 `OscColor` 排除出公开按键事件、内部队列保留 | 共享 250 ms；`read_until` 仅限轮询前／暂停；迟到 OSC 隔离为内部事件、不更新颜色缓存 | 无专门主题监听；默认色仍继承终端 | `Color::Reset`；表面色由采样 fg/bg 推导并处理对比度／深度 | pinned crossterm fork 的 git 依赖 + 回放窗口 |
| Pi 1.2.0 | OSC 10/11/4 + CSI ?997;1n/2n；mode2031 | 单一统一读取器；查询与按键、迟到回复一起协调 | 约 100 ms；迟到回复继续应用 | 有（mode2031 + system theme），退出清理 | grayscale native 直到颜色到达 | 自有终端框架，非可直接复用的 crate |
| tmux 3.7c（SAKI 2026-10-14 曾用，已被 Termina 替代） | `#{client_theme}` | 只读子进程，不读 tty 输入 | 250 ms 上限 | 有，2 s owned watcher | SAKI native fallback | 除 tmux 外无 |
| Termina 0.3.3（Helix master） | OSC 10-19 + CSI ?997 解析为 `Event`；查询由调用方发送 | `Parser`／`EventReader`；`PlatformTerminal` 持有 raw mode／termios；模式由调用方写 | 未内置 deadline／迟到策略 | 需应用自行订阅 | 由应用定义 | 新增输入库；替换 `EventStream` 并交接 raw mode |

### SAKI 当前实现（2026-10-15）

SAKI 已把输入切换到 Termina 0.3.3 `EventReader`/`EventStream`：`PlatformTerminal` 持有 raw mode，事件流同时提供用户事件与协议回复；OSC 10/11 采样默认前景/背景，启动用 CSI ?996 建立 scheme baseline 并启用 mode2031，运行期 CSI ?997 变化只刷新 OSC 10/11。正文保持 `Color::Reset` 继承终端前景，衍生色从采样值推导并保持 ≥4.5 对比度；未知或 `rgba:` 回复时回退 native。查询在单一有界窗口内合并，不按帧或按键触发；surface 到的 I/O 错误照常返回前端。已知依赖边界：Termina EventStream helper 会重试 poll 错误/EOF 且不主动唤醒，ignored-SIGHUP 断开场景不声明通过。

规律：没有输入所有权就不在应用内做 OSC/CSI 颜色查询。SAKI 当前实现已完成向 Termina 原生输入路径的迁移（见上），不再使用 tmux 元数据子进程。

当前验收矩阵（2026-10-15）：

| 场景 | 探测来源 | 外部查询进程 | 终端采样 | 运行期跟随 | 回退 |
| --- | --- | --- | --- | --- | --- |
| 默认 `auto`（当前实现） | 自有 Termina 输入读取器发 OSC 10/11；启动用 CSI ?996 建立 baseline 并启用 mode2031；变化只刷新 OSC 10/11 | 0 | 是（采样 fg/bg） | 是（CSI ?997／focus，有界合并） | native fallback 直到首个可用 pair |
| 手动 `light`/`dark` | 无 | 0 | 否 | 不适用 | 固定 palette |
| 终端不支持／无回复／`rgba:` | 无可用采样 | 0 | 否 | 受有界窗口约束，不循环重试 | native fallback；键／粘贴不受影响 |

历史记录：2026-10-14 的实现曾用 tmux `client_theme` 只读子进程与 2 s watcher；该路径已被原生实现替换。上游 crossterm 0.29 仍是 SAKI 的输出后端，但其 `EventStream` 已不再是输入所有者；Codex fork 与 Termina 的第三方结论保留在上文，SAKI 仅新增 pinned Termina 0.3.3 输入依赖。

适配细节与验收边界见 [TUI style](../../design/tui-style.md)。

## 8. 四系统 TUI 能力矩阵（2026-10-15）

本节把 [Issue #19](https://github.com/HeimaoLST/Saki/issues/19) 的只读核对沉淀为长期事实。固定三个第三方最新稳定 release 的 peeled commit，逐维度记录 SAKI 的实现位置与竞品的可核验行为。未运行任何第三方 harness；竞品行为来自固定 commit 的源码或官方文档，不是端到端可用性实测。结论分级沿用文档约定：**[实现事实]**（直接读固定 commit 源码）、**[作者报告]**（官方文档描述）、**[设计推论]**、**[待验证]**。

| 产品 | 最新稳定 release | peeled commit | 本机安装（2026-10-15） |
| --- | --- | --- | --- |
| Codex | `rust-v0.162.1`（2026-10-09） | [`092d3acd6bec3e3a14bdc7e7a2810ab628ab759d`](https://github.com/openai/codex/tree/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d) | 0.162.1，一致 |
| Pi | `v1.2.0`（2026-10-12） | [`06a466935536f8c9bcacb2d5efc0fc021be1bdff`](https://github.com/earendil-works/pi/tree/06a466935536f8c9bcacb2d5efc0fc021be1bdff) | 1.2.0，一致 |
| OpenCode | `v1.19.0`（2026-10-11） | [`733b2c627e5aec39cb33cc22373bdce33053ef2c`](https://github.com/anomalyco/opencode/tree/733b2c627e5aec39cb33cc22373bdce33053ef2c) | 未安装，仅源码核对 |
| SAKI | `v0.1.0`（2026-10-15 发布） | `origin/dev@082845c770e608c1c92591325eeaee912c0791b2`（审计基线） | 当前 TUI |

Codex 的 `0.163.0-alpha.*` 属预发布，不计入稳定基线。

### 8.1 SAKI 已实现（不作为缺口）

这些能力已存在，不应再作为“缺失”提案：原生终端浅／深主题（`theme.rs`）、彩色 Markdown 与表格（`markdown.rs`、`markdown_layout.rs`）、任务级 Activity（`view.rs`）、流式正文与推理、跨 viewport 拖选（`selection.rs`）、JSONL 会话持久化、AI 标题、Esc 主动取消（`mod.rs` 的 `outcome.cancel`）、96-cell 正文宽度（`markdown_layout.rs:15`）、有界日志分页（`log.rs`，16 KiB／300 行）、图片附件、工具摘要与展开、视口内渲染（`view.rs` 顶部注释）与每记录布局缓存（`state.rs` 的 `CachedLayout`）。[实现事实]

### 8.2 SAKI 缺口与竞品对照

下表只收有源码证据、可描述用户代价的候选；候选名后的 issue 链接指向 GitHub 上的对应候选或交接；迭代与实时优先级以 [audit epic #18](https://github.com/HeimaoLST/Saki/issues/18) 为准，本文档不维护计划。

| 维度／候选 | SAKI 现状（位置） | 竞品可核验行为（固定版本） | 依赖 | 分级 |
| --- | --- | --- | --- | --- |
| 输入历史搜索 · [#22](https://github.com/HeimaoLST/Saki/issues/22) | 仅 older／newer 召回，无搜索（`history.rs`） | **仅 Codex 已核验**：`history_search_previous`=Ctrl+R、`history_search_next`=Ctrl+S（[keymap.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/keymap.rs)）。Pi 的 Ctrl+R 是 `app.session.rename`（会话重命名，[keybindings.ts](https://github.com/earendil-works/pi/blob/06a466935536f8c9bcacb2d5efc0fc021be1bdff/packages/coding-agent/src/core/keybindings.ts)），`tui.editor.historyPrevious/Next` 只是顺序导航，二者都不是历史搜索 | 前端 | [实现事实] |
| 编辑器 undo/redo · [#35](https://github.com/HeimaoLST/Saki/issues/35) | 无撤销栈；`Ctrl+Y` 只是单条 kill/yank，不是 undo（[#20](https://github.com/HeimaoLST/Saki/issues/20) 的 `grep -rn -i "undo|redo"` 为空） | Pi `tui.editor.undo` 默认 `ctrl+-`（Windows `ctrl+z`，WSL `alt+z`，[keybindings.md](https://github.com/earendil-works/pi/blob/06a466935536f8c9bcacb2d5efc0fc021be1bdff/packages/coding-agent/docs/keybindings.md)）+ [undo-stack.ts](https://github.com/earendil-works/pi/blob/06a466935536f8c9bcacb2d5efc0fc021be1bdff/packages/tui/src/undo-stack.ts)；OpenCode `input.undo` 默认 `ctrl+-`/`super+z`（[keybind.ts](https://github.com/anomalyco/opencode/blob/733b2c627e5aec39cb33cc22373bdce33053ef2c/packages/tui/src/config/keybind.ts)），并有 `/undo`、`/redo` 回退消息与文件变更；Codex 的 `undo`/`redo` 仅在 Vim normal 模式（`u`，[keymap.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/keymap.rs)） | 前端；绑定需避开终端 SUSPEND | [实现事实] |
| 文件路径补全 / `@` 引用 · [#28](https://github.com/HeimaoLST/Saki/issues/28) | 仅补全 `/命令`（[command/mod.rs](../../../src/command/mod.rs)） | Codex [file_search.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/file_search.rs)、[mention_codec.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/mention_codec.rs)；Pi [autocomplete.ts](https://github.com/earendil-works/pi/blob/06a466935536f8c9bcacb2d5efc0fc021be1bdff/packages/tui/src/autocomplete.ts)（Tab 路径 + `@` 模糊）；OpenCode [prompt/autocomplete.tsx](https://github.com/anomalyco/opencode/blob/733b2c627e5aec39cb33cc22373bdce33053ef2c/packages/tui/src/component/prompt/autocomplete.tsx) | 路径补全=前端；把文件内容注入消息=core 消息契约 | [实现事实] |
| 外部编辑器 · [#29](https://github.com/HeimaoLST/Saki/issues/29) | 无 | Codex [external_editor.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/external_editor.rs)（Ctrl+G）；Pi [external-editor.ts](https://github.com/earendil-works/pi/blob/06a466935536f8c9bcacb2d5efc0fc021be1bdff/packages/coding-agent/src/modes/interactive/external-editor.ts)（Ctrl+G）；OpenCode [editor.ts](https://github.com/anomalyco/opencode/blob/733b2c627e5aec39cb33cc22373bdce33053ef2c/packages/tui/src/editor.ts) | 前端（需终端挂起／恢复） | [实现事实] |
| 运行中排队／引导 · [#31](https://github.com/HeimaoLST/Saki/issues/31) | 运行中提交只保留草稿并提示（`mod.rs`） | Codex `composer.queue`=Tab + [session_queue_commands.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/session_queue_commands.rs)；Pi Enter 引导当前响应、Alt+Enter 排队、Alt+Up 取回（`interactive-mode.ts`） | **core**：Agent worker 目前单轮顺序 | [实现事实] |
| transcript 文本搜索 · [#27](https://github.com/HeimaoLST/Saki/issues/27) | 无；只有 Home/End 与滚动 | Codex `find_transcript`=F3 + `transcript_view.rs` 的 `Search`；Pi `tui.altScreen.search`=Ctrl+Shift+F + [alt-screen-search.ts](https://github.com/earendil-works/pi/blob/06a466935536f8c9bcacb2d5efc0fc021be1bdff/packages/tui/src/alt-screen-search.ts) | 前端 | [实现事实] |
| 消息级跳转 · [#26](https://github.com/HeimaoLST/Saki/issues/26) | 仅 Ctrl+Home/End 与逐行 | OpenCode `session.message.next/previous`；Pi `altScreen.previousPrompt/nextPrompt`=Ctrl+Shift+Up/Down | 前端 | [实现事实] |
| 变更 diff · [#33](https://github.com/HeimaoLST/Saki/issues/33) | edit 返回 `{"path","applied":n}`；TUI 不画 diff（[tools/edit.rs](../../../src/tools/edit.rs)） | Codex [diff_render.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/diff_render.rs)；Pi [diff.ts](https://github.com/earendil-works/pi/blob/06a466935536f8c9bcacb2d5efc0fc021be1bdff/packages/coding-agent/src/modes/interactive/components/diff.ts)；OpenCode [diff-viewer.tsx](https://github.com/anomalyco/opencode/blob/733b2c627e5aec39cb33cc22373bdce33053ef2c/packages/tui/src/feature-plugins/system/diff-viewer.tsx) | 工具参数派生预览=前端；权威已应用 diff=core 结果契约 | [实现事实] |
| 代码块语法高亮 · [#30](https://github.com/HeimaoLST/Saki/issues/30) | 仅弱化语言标签 + 单色（[markdown.rs](../../../src/frontend/tui/markdown.rs)） | Codex syntect + two-face；Pi `highlightCode` + highlight.js；OpenCode tree-sitter WASM（[parsers-config.ts](https://github.com/anomalyco/opencode/blob/733b2c627e5aec39cb33cc22373bdce33053ef2c/packages/tui/src/parsers-config.ts)） | 前端；引入解析库会改共享 `Cargo.toml`（`agent:runtime` 拥有）→ 跨业务线 handoff；只有跨模块公共契约变更才需 RFC+ACK+人工确认 | [实现事实] |
| 会话搜索／过滤／排序／预览 · [#21](https://github.com/HeimaoLST/Saki/issues/21) | Picker 仅 Up/Down，无过滤（`sessions.rs`） | Codex [resume_picker.rs](https://github.com/openai/codex/blob/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d/codex-rs/tui/src/resume_picker.rs)；Pi [session-selector-search.ts](https://github.com/earendil-works/pi/blob/06a466935536f8c9bcacb2d5efc0fc021be1bdff/packages/coding-agent/src/modes/interactive/components/session-selector-search.ts)；OpenCode [dialog-session-list.tsx](https://github.com/anomalyco/opencode/blob/733b2c627e5aec39cb33cc22373bdce33053ef2c/packages/tui/src/component/dialog-session-list.tsx) | 前端（过滤 `SessionCommand::List` 的 `SessionSummary`） | [实现事实] |
| 模型选择 · [#32](https://github.com/HeimaoLST/Saki/issues/32) | 模型只读展示，来自 `SAKI_MODEL`（[app.rs](../../../src/app.rs)） | Codex model picker；Pi `/model` + Ctrl+L；OpenCode [dialog-model.tsx](https://github.com/anomalyco/opencode/blob/733b2c627e5aec39cb33cc22373bdce33053ef2c/packages/tui/src/component/dialog-model.tsx) | **core/app**：需重建 client 与目录同步 | [实现事实] |
| Help 文案与行为不一致 · [#23](https://github.com/HeimaoLST/Saki/issues/23) | [help.rs](../../../src/frontend/tui/help.rs) 未提 Esc 取消；称拖选仅 “visible” | ——（SAKI 内部一致性问题） | 前端 | [实现事实] |

**会话搜索的有意简化（[#21](https://github.com/HeimaoLST/Saki/issues/21) 范围）**：竞品的会话检索更强——Pi `session-selector-search.ts` 支持 fuzzy token、引号短语与正则，并有 named 过滤；OpenCode `dialog-session-list.tsx` 用 debounced 服务端 query 叠加标题过滤。SAKI 本批只用**本地、大小写不敏感的全 token 子串匹配**（对已有 `SessionSummary` 的 title/ID/dir/model），对本地 JSONL 会话规模足够，且避免为检索新增服务端 API。这是有意的范围收敛，不是能力等价。

### 8.3 明确排除与待验证

- **明确排除**（依 [#19](https://github.com/HeimaoLST/Saki/issues/19) 的产品判断，无观察到的用户问题）：TUI 内主题切换器、记录驱逐（record eviction）、reduced-motion／screen-reader 选项。它们不作为候选，除非出现可复现的用户问题。
- **记录保留不是缺陷**：源记录被有意识地完整保留，以便完整复制／回放；布局缓存上限与源记录保留是两件事。Codex 的布局缓存驱逐（layout cache，上限 64 项／8 MiB）**不能**推广成“应删除旧对话记录”；SAKI 未实测到无界布局增长，因此不写成性能缺陷。
- **依赖与契约的精确区分**：新增 crate 不自动需要 ADR；修改共享 `Cargo.toml`（`agent:runtime` 拥有）走跨业务线 handoff。只有跨模块公共契约（协议、公共事件、工具结果结构）变更才需要 RFC + 受影响业务线 `ACK` + 人工确认。
- **需要 RFC/ACK 的 core 边界**：运行中排队／引导 [#31](https://github.com/HeimaoLST/Saki/issues/31)、模型切换 [#32](https://github.com/HeimaoLST/Saki/issues/32)、权威已应用 diff [#33](https://github.com/HeimaoLST/Saki/issues/33)、`@` 文件内容注入（路径补全见 [#28](https://github.com/HeimaoLST/Saki/issues/28)，内容注入另开 handoff）。不在 TUI 伪造后台任务、undo 或权限能力。
- **已更正事实**：此前评论曾把 OpenCode `session.background` 等同于 follow-up 排队；核对 v1.19.0 `config/keybind.ts` 后确认它是 “Background synchronous subagents”，与排队／引导无关，已删除该等价。Pi 的 Ctrl+R 是 `app.session.rename`，不是历史搜索。
- **[待验证／证据缺口]**：OpenCode 未发现 transcript 文本搜索入口，暂记待验证而非“确认缺失”；Codex／Pi 正文宽度上限未逐位定位常量；三家均未端到端运行；无障碍（screen-reader／reduced-motion）与通知（notifications）无实测证据，属证据缺口，不作为必须功能；长历史布局增长未有实测数据。

### 8.4 验证基线（来自 [#20](https://github.com/HeimaoLST/Saki/issues/20)，固定 `origin/dev@082845c`）

[#20](https://github.com/HeimaoLST/Saki/issues/20) 在实现前记录了本仓库的 clean baseline；这些是实现 PR 的对比基准，本文档据此更新，不再使用旧的 185／未测计数：

| 检查 | 结果 | 备注 |
| --- | --- | --- |
| `cargo fmt --check` | PASS（exit 0，无输出） | |
| `cargo clippy --all-targets -- -D warnings` | **FAIL（exit 101，pre-existing）** | 跨 runtime／frontend／模块测试／examples；工具链 rust/clippy 1.99.0 且无 `rust-toolchain.toml`；由 [#25](https://github.com/HeimaoLST/Saki/issues/25) 跟踪，不代表实现引入 |
| `cargo test` | PASS：**285 passed / 0 failed / 6 ignored**（291 total；其中 **181** 为 `frontend::tui::tests::`） | |
| `cd desktop && go test ./...` | PASS（exit 0） | |

结论：实现与行为验证可继续，但在 [#25](https://github.com/HeimaoLST/Saki/issues/25) 变绿前，评审**不得声称全部仓库检查全绿或据此合并**。审计与迭代以 [audit epic #18](https://github.com/HeimaoLST/Saki/issues/18) 为准；本文档保存长期事实，不承载过程。

## 对 SAKI 的判断（2026-10-13 诊断，状态更新见节末）

| 已观察的问题 | 可核验原因 | 推荐方向 |
| --- | --- | --- |
| 表格变成管道符长段落 | [markdown.rs](../../../src/frontend/tui/markdown.rs:53) 使用 `Options::empty()`；[SoftBreak](../../../src/frontend/tui/markdown.rs:155) 转空格 | 启用结构解析，并实现表格布局和窄屏回退 |
| 每段过程都像一条正式回复 | [view.rs](../../../src/frontend/tui/view.rs:564) 按 Assistant 记录统一绘制，尚无任务过程的显示分组 | 用真实任务边界区分活动与最终答复，完整时间顺序留在详情 |
| 连续出现多个 Thought 入口 | [view.rs](../../../src/frontend/tui/view.rs:578) 每条 reasoning 都画独立标题 | 完成后统一归入 Activity，运行中保留真实推理显示与手动展开 |
| 用户底色在暗色终端过亮 | [view.rs](../../../src/frontend/tui/view.rs:21) 使用固定 RGB 用户前景／背景 | 使用成对的浅／深主题颜色，保持用户矩形但降低反差 |
| 横向扫读困难 | [view.rs](../../../src/frontend/tui/view.rs:564) 正文以整个 viewport 换行 | 试验正文宽度上限，代码与表格另算 |
| 工具摘要缺少可读的工作对象 | [view.rs](../../../src/frontend/tui/view.rs:599) 当前摘要围绕工具名和参数开头 | 四工具优先显示动作、相对路径或真实命令，展开查看完整参数 |

最后两点中的宽度上限和任务级 Activity 分组是 SAKI 的设计建议，不能归为两个产品共同拥有的默认能力。截图中反复改口和冗长结论还涉及模型输出质量；renderer 只能改善组织，不能验证那些数字或让错误结论自动变正确。

**2026-10-15 更新（历史状态）**：本表是 2026-10-13 的诊断快照。上表六项均已由 2026-10-14 的 TUI style 实验实现——结构化 Markdown 与表格布局、任务级 Activity 折叠、成对浅／深主题、96-cell 正文宽度、动作＋相对路径的工具摘要。实际契约与验收见 [TUI 设计](../../design/tui.md)。保留此表用于追溯当时的证据与判断。

## 建议采用与有意保留差异

采用 Codex 的操作语义、推理详细视图、窄表格退化方式，以及 Pi 的主题角色、组件职责和工具可展开对象。SAKI 默认使用更安静的工具过程；不采用 Pi 的大面积成功绿色背景，不永久展示全部 usage 计数，也不把 Codex 的复杂命令解析／活动体系整体搬进第一版。

先验证三件可观察的事：完成后能迅速找到答案，出错时能找到真实依据，复制和缩放不会改变原文或丢失阅读位置。正式方案及验收场景见 [TUI style](../../design/tui-style.md)。

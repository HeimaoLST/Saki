# SAKI 文档

更新日期：2026-10-22。

产品目标：让 SAKI 通过持续积累经验和修正行为，在长期使用中更好用，减少用户重复指导与纠正的负担。研究目标：理解怎样让经历持久地影响判断和行动，并验证这种变化能否改善任务结果与长期协作体验。用户已授权开展第一轮实验，由另一个 agent 负责实现和执行、主 agent 指导与验收；独立 pilot 的两轮真实结果已复核，包含一轮 bearer 配置偏差和一轮修正运行；主 Agent 尚未接入成长机制。

产品形态是可由 Web、Bot、TUI、GUI 等宿主承载的 Agent runtime／core。不同外壳使用同一核心语义；单会话／多会话不等于不同身份。已确认的产品边界见 [Persistent identity](design/persistent-identity.md#产品边界可由不同宿主承载的-agent-runtime)，会话使用场景见 [Session management](design/session-management.md)。

## 源码结构

```text
src/
├── main.rs               # 启动入口
├── app.rs                # 配置、实例组装与生命周期
├── agent/                # Agent loop、事件、Conversation 与用户消息
│   ├── mod.rs
│   ├── conversation.rs
│   ├── message.rs
│   └── journal.rs
├── command/              # 本地命令定义、解析与核心请求映射
│   └── mod.rs
├── client/               # 模型 API 调用与格式转换
│   ├── mod.rs
│   ├── responses.rs
│   └── responses/stream.rs # SSE 增量解析
├── protocol/             # 公共请求、消息、结果、usage 与工具声明
│   └── mod.rs
├── model/                # 模型规格目录、容量同步与匹配
│   ├── mod.rs
│   └── catalog.rs
├── session/              # 会话存储、恢复与活动会话管理
│   ├── mod.rs
│   └── store.rs
├── tools/                # 工具注册、内置工具与输出处理
│   └── mod.rs
└── frontend/             # 输入与展示适配
    ├── mod.rs
    ├── console.rs
    └── tui/
```

`app.rs` 组装各模块；`frontend` 通过带会话 ID 的请求和事件连接 SessionManager，不拥有模型／工具循环。`agent` 使用客户端、公共协议和工具，不依赖前端；`client` 依赖公共协议，不引用 Conversation 或工具执行模块；`model` 管理目录数据。测试继续按行为聚合在 `tests/`，实验入口位于 `examples/`。接口、转换边界和日志版本见[模型调用分层](design/model-runtime.md)。

`desktop/` 是独立的 Go module，用 MyGo 原生 UI 承载联系人应用，通过 `Saki --stdio` 使用 Agent Runtime；联系人与草稿归 GUI，执行与历史事实来自 Runtime。已实现行为及验收限制见 [Desktop contacts](design/desktop.md)，开发需求与逐项验收见 [GUI requirements](requirements/desktop.md)。

## 阅读路径

| 目录 | 文档 | 职责 |
| --- | --- | --- |
| `design/` | [Persistent identity](design/persistent-identity.md) | 项目方向、当前代码事实、需要澄清的设计问题 |
| `research/systems/` | [Memory and reflection](research/systems/memory-and-reflection.md) | 已有系统实际实现了哪些机制 |
| `research/systems/` | [Context compaction](research/systems/context-compaction.md) | Codex／Pi／OpenCode 的压缩机制、版本差异及职责划分 |
| `research/systems/` | [Model runtime](research/systems/model-runtime.md) | Pi 的公共类型、模型目录、API 适配与流式调用边界；SAKI 分层建议 |
| `research/systems/` | [Builtin tools](research/systems/builtin-tools.md) | Pi／Codex／OpenCode／Claude Code 的工具设计、输出与进程管理，以及首批工具提案 |
| `research/systems/` | [Terminal UI](research/systems/terminal-ui.md) | 布局、滚动、主题与 Rust 组件选择；含发布版本核对和兼容性小样 |
| `research/systems/` | [Terminal presentation](research/systems/terminal-presentation.md) | Pi／Codex／OpenCode 发布源码的消息、活动、推理、Markdown、主题与状态栏技巧，以及固定版本的四系统 TUI 能力矩阵 |
| `research/learning/` | [Evidence and beliefs](research/learning/evidence-and-beliefs.md) | 来源、归因、矛盾与判断修正 |
| `research/evaluation/` | [History to behavior](research/evaluation/history-to-behavior.md) | 如何评价记忆对行动的影响，已有评测的限制 |
| `design/` | [Conversation compaction](design/conversation-compaction.md) | 自动触发、容量与 usage、工具边界、日志和验收 |
| `design/` | [Model runtime](design/model-runtime.md) | 公共协议、Responses 客户端、模型目录及日志格式的实际实现 |
| `design/` | [Builtin tools implementation](design/builtin-tools.md) | 四工具的实际接口、输出边界、进程生命周期与离线／真实模型验收 |
| `design/` | [TUI](design/tui.md) | 首版使用、独立前端与 Agent 的模块边界、布局设计和实际终端／模型验收 |
| `design/` | [TUI style](design/tui-style.md) | 下一版统一风格提案：阅读层次、任务活动、主题、结构排版与交互示意 |
| `design/` | [Multimodal input](design/multimodal-input.md) | 图片附件、剪贴板／路径输入、消息契约及图片压缩 |
| `design/` | [Session management](design/session-management.md) | 持久会话、JSONL 恢复、会话选择器、中断语义与核心边界 |
| `design/` | [Desktop contacts](design/desktop.md) | 原生 GUI 的 Runtime 接入、联系人与交互事实、窗口验收限制，以及 Moon/Dawn 和自定义主题设计 |
| `design/` | [CI governance](design/ci.md) | Runtime／TUI／GUI 的 GitHub Actions 门禁、责任矩阵、工具链与失败修复约定 |
| `requirements/` | [Agent runtime requirements](requirements/agent-runtime.md) | 多 session 重叠执行、可观察执行事实、contact 边界与历史检索的 runtime 需求交接（未实施） |
| `requirements/` | [MVP requirements](requirements/mvp.md) | 两端 MVP（TUI + GUI）的长期产品目标、行为边界，以及工程候选／首批真实试用／稳定发布的证据矩阵 |
| `requirements/` | [GUI requirements](requirements/desktop.md) | GUI 产品边界、启动／外观／阅读／输入／配置／资料／并发的需求与验收，以及 Runtime 依赖与交付规则 |
| `design/` | [Local command system](design/command-system.md) | 本地命令解析、会话操作复用、输入边界、TUI 补全与验收 |
| `design/` | [Experience to behavior](design/experience-to-behavior.md) | 最小闭环的架构提案与未决问题 |
| `experiments/` | [History dependence](experiments/history-dependence.md) | 对照实验协议与预算；独立 pilot 已完成真实运行 |
| `experiments/` | [Growth pilot report](experiments/growth-pilot-report.md) | 实际结果、成本、配置偏差与结论边界 |
| `experiments/` | [Compaction gateway report](experiments/compaction-gateway-report.md) | 百万窗口实测、真实超限错误与工具 continuation 协议问题 |
| `research/` | [Sources](research/sources.md) | 一手来源、核对版本和证据边界 |

## 如何使用这些文档

- **项目方向**：来自用户提供的 SAKI 设计讨论。
- **实现事实**：基于实际检查的源码或官方文档；源码尽量固定到 commit。
- **作者报告**：论文或项目方声称的结果，尚未在本地复现。
- **设计推论／提案**：我们从证据形成的建议，不等同于已批准的实现决策。
- **待验证**：本次检查未覆盖，或需要实验才能回答。

第三方 prompt 中要求模型做到某事，只能证明设计意图。代码接受了一个更新，也不能证明该更新在语义上正确。研究文档保留这两个区别。

外部材料以链接、版本和自己的摘要管理，不复制整篇论文或源码。主题文档负责分析，来源索引负责定位。文件使用英文 `kebab-case` 命名；按职责与主题归类，避免为每次对话、日期或单条发现创建孤立文件。实验协议与结果放在 `experiments/` 下，记录配置、原始数据入口与结论边界。

## 当前研究进度

| 工作 | 状态 | 下一步 |
| --- | --- | --- |
| 检查 SAKI 当前实现与已有文档 | 已完成 | 实现前重新检查代码，避免按过时快照设计 |
| 核对 Letta、Hermes、Honcho、Hindsight、SkillOpt 的相关机制 | 已完成有限范围的源码／文档核对 | 按具体问题补查，避免把局部未发现写成全项目不存在 |
| Compact 的 harness 实践 | 已参考 Pi 实施有限 overflow recovery，修复工具 continuation；百万窗口四场景复验通过 | 主程序与成长实验各 29 项离线测试通过；后续评估有界工具输出和复杂摘要质量 |
| Builtin tools 的 harness 实践 | 已按 Pi／Codex 方向实现 read/bash/edit/write；43 项主程序与 29 项实验测试通过 | 默认官方端点与 Magpie 网关的四工具闭环、大输出回读均通过；暂不引入输出文件清理或恢复 |
| TUI 布局与技术栈 | 工作记录首版已实现；Agent 与 TUI 解耦，console 永久保留；真实 tmux、剪贴板、日志与 Magpie 四工具闭环通过；消息背景、正文对齐、自适应底栏、鼠标／键盘滚动与多行粘贴已验收 | 使用中迭代视觉和输入体验；主 Agent 已接入流式正文；命令与主动取消等单独设计 |
| Session 管理 | JSONL 持久化、默认新会话、显式恢复、TUI 选择器与单写锁已实现；进程重启、硬中断恢复及真实 PTY 验收通过 | 本地 command 系统已实现；后台并行任务另行设计 |
| 历史影响行动的对照实验 | 独立 harness 的 12 项离线测试通过；两轮真实 pilot 已完成并复核 | 对比判断与原始 trace 的实际质量和总开销，不预设 Belief 层有收益 |
| 验证自动归因与判断修正的可靠性 | 未验证 | 先检验来源重复、迁移、边界反例与相反证据 |
| 长期偏好、关系、主动目标与模型迁移 | 开放问题 | 在最小闭环成立后逐项研究 |

按指定配置的第二轮，A（反思所得判断）完成 0/2，B（无历史）与 C（原始 trace）均完成 2/2；A 在 3 次调用预算内没有执行修改。首轮存在配置偏差，结果也有明显轮间波动。小样本不能证明稳定收益；当前应保留直接 trace 作为基线，要求反思判断证明额外质量或成本收益。细节见[实验报告](experiments/growth-pilot-report.md)。主循环已接入自动 compact；容量、usage 计数和验收见[实现文档](design/conversation-compaction.md)。成长机制仍未接入主循环。

当前建议是先验证**有来源、可修正的条件判断是否让未来任务做得更好、让用户更省心**，同时用状态干预检查改善是否来自经历。长期方向仍包含偏好、价值、承诺、关系历史和主动性；这些机制的产品价值也要通过长期使用检验。

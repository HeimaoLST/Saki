# SAKI 文档

更新日期：2026-10-30。

Saki 提供可被不同应用复用的 Agent Runtime。Nagi（GUI 暂定名）提供开源的联系人式自主工作台，TUI 侧重用户直接指导执行。三个产品共享运行基础，GUI/TUI 围绕同一工作协作是待实现和验证的产品目标。

GUI 的权威产品定义、概念和行为需求在 [Nagi 产品定义与需求](requirements/desktop.md)；视觉与交互在 [Desktop workbench](design/desktop-workbench.md)；已实现行为与验收证据在 [Desktop](design/desktop.md)。联系人、事项、对话和执行会话的关系以产品需求为准，既有单会话映射只表示阶段实现。成长与人格属于独立研究，不替代当前产品定义，也不是联系人式产品的前置。

Nagi MVP 的基本功能、边界与 MVP-01–MVP-12 验收见 [完整 MVP 交付定义](requirements/desktop.md#首个可用-mvp-的交付边界)；开发 agent 从 [roadmap 总入口 #140](https://github.com/HeimaoLST/Saki/issues/140) 获取阶段、依赖和具体 issue，组合候选与真实试用记录见 [#156](https://github.com/HeimaoLST/Saki/issues/156)，候选与验收证据入口见 [#200](https://github.com/HeimaoLST/Saki/issues/200)。候选 PR 和阶段验收不表示完整产品已交付。桌面应用的首次使用步骤只在根 [README](../README.md#运行桌面应用macos-arm64) 维护，本文不复制操作流程。

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

`app.rs` 组装各模块；`frontend` 经终端适配层使用 Runtime 服务，不拥有模型／工具循环。`agent` 使用客户端、公共协议和工具，不依赖前端；`client` 依赖公共协议，不引用 Conversation 或工具执行模块；`model` 管理目录数据。测试继续按行为聚合在 `tests/`，实验入口位于 `examples/`。调用边界见 [Runtime API](design/runtime-api.md)，模型接口与日志版本见[模型调用分层](design/model-runtime.md)。

`desktop/` 是独立的 Go module，用 MyGo 原生 UI 通过 stdio 消费真实 Agent Runtime；联系人、草稿和映射由 GUI 管理，执行与权威历史由 Runtime 提供。实现事实见 [Desktop](design/desktop.md#已确认需求与交付边界)，当前联系人、气泡与可选工作区设计见 [Desktop workbench](design/desktop-workbench.md)。

## 阅读路径

| 目录 | 文档 | 职责 |
| --- | --- | --- |
| `requirements/` | [Nagi 产品定义与需求](requirements/desktop.md) | GUI 产品定位、联系人／事项／会话语义、目标流程与行为验收、既有工程交接及待定边界 |
| `design/` | [Persistent identity](design/persistent-identity.md) | 长期身份与成长研究的方向、证据边界及待验证问题；不是当前 GUI 产品定义 |
| `research/systems/` | [Memory and reflection](research/systems/memory-and-reflection.md) | 已有系统实际实现了哪些机制 |
| `research/systems/` | [Context compaction](research/systems/context-compaction.md) | Codex／Pi／OpenCode 的压缩机制、版本差异及职责划分 |
| `research/systems/` | [Model runtime](research/systems/model-runtime.md) | Pi 的公共类型、模型目录、API 适配与流式调用边界；SAKI 分层建议 |
| `research/systems/` | [Builtin tools](research/systems/builtin-tools.md) | Pi／Codex／OpenCode／Claude Code 的工具设计、输出与进程管理，以及首批工具提案 |
| `research/systems/` | [Terminal UI](research/systems/terminal-ui.md) | 布局、滚动、主题与 Rust 组件选择；含发布版本核对和兼容性小样 |
| `research/systems/` | [Terminal presentation](research/systems/terminal-presentation.md) | Pi／Codex／OpenCode 发布源码的消息、活动、推理、Markdown、主题与状态栏技巧，以及固定版本的四系统 TUI 能力矩阵 |
| `research/systems/` | [Desktop interaction](research/systems/desktop-interaction.md) | Grok Bot / Qoder 本地界面观察、截图索引与 Saki 设计取舍 |
| `research/learning/` | [Evidence and beliefs](research/learning/evidence-and-beliefs.md) | 来源、归因、矛盾与判断修正 |
| `research/evaluation/` | [History to behavior](research/evaluation/history-to-behavior.md) | 如何评价记忆对行动的影响，已有评测的限制 |
| `design/` | [Conversation compaction](design/conversation-compaction.md) | 自动触发、容量与 usage、工具边界、日志和验收 |
| `design/` | [Model runtime](design/model-runtime.md) | 公共协议、Responses 客户端、模型目录及日志格式的实际实现 |
| `design/` | [Builtin tools implementation](design/builtin-tools.md) | 四工具的实际接口、输出边界、进程生命周期与离线／真实模型验收 |
| `design/` | [TUI](design/tui.md) | 首版使用、独立前端与 Agent 的模块边界、布局设计和实际终端／模型验收 |
| `design/` | [TUI style](design/tui-style.md) | 下一版统一风格提案：阅读层次、任务活动、主题、结构排版与交互示意 |
| `design/` | [Multimodal input](design/multimodal-input.md) | 图片附件、剪贴板／路径输入、消息契约及图片压缩 |
| `design/` | [Session management](design/session-management.md) | 持久会话、JSONL 恢复、会话选择器、中断语义与核心边界 |
| `design/` | [Desktop contacts](design/desktop.md) | 原生 GUI 的实现事实、当前主题接线与后续自定义边界 |
| `design/` | [Desktop workbench](design/desktop-workbench.md) | v08 工作台与统一设置页面；可选工作区、事项概览/详情及开发验收 |
| `design/` | [CI governance](design/ci.md) | Runtime／TUI／GUI 的 GitHub Actions 门禁、责任矩阵、工具链与失败修复约定 |
| `requirements/` | [Agent runtime requirements](requirements/agent-runtime.md) | 通用 Runtime 与应用的职责边界、多会话执行及可观察性需求，以及当前实现核对入口 |
| `requirements/` | [MVP requirements](requirements/mvp.md) | #50 两端工程 MVP 的阶段范围与证据矩阵；不代表完整 Nagi 产品目标 |
| `design/` | [Local command system](design/command-system.md) | 本地命令解析、会话操作复用、输入边界、TUI 补全与验收 |
| `design/` | [Experience to behavior](design/experience-to-behavior.md) | 最小闭环的架构提案与未决问题 |
| `experiments/` | [History dependence](experiments/history-dependence.md) | 对照实验协议与预算；独立 pilot 已完成真实运行 |
| `experiments/` | [Growth pilot report](experiments/growth-pilot-report.md) | 实际结果、成本、配置偏差与结论边界 |
| `experiments/` | [Compaction gateway report](experiments/compaction-gateway-report.md) | 百万窗口实测、真实超限错误与工具 continuation 协议问题 |
| `research/` | [Sources](research/sources.md) | 一手来源、核对版本和证据边界 |

## 如何使用这些文档

- **产品需求**：来自当前用户讨论并整理为可验收行为；待定决策单独标注，不表示实现完成。
- **实现事实**：基于实际检查的源码或官方文档；源码尽量固定到 commit。
- **作者报告**：论文或项目方声称的结果，尚未在本地复现。
- **设计推论／提案**：我们从证据形成的建议，不等同于已批准的实现决策。
- **待验证**：本次检查未覆盖，或需要实验才能回答。

第三方 prompt 中要求模型做到某事，只能证明设计意图。代码接受了一个更新，也不能证明该更新在语义上正确。研究文档保留这两个区别。

外部材料以链接、版本和自己的摘要管理，不复制整篇论文或源码。主题文档负责分析，来源索引负责定位。文件使用英文 `kebab-case` 命名；按职责与主题归类，避免为每次对话、日期或单条发现创建孤立文件。实验协议与结果放在 `experiments/` 下，记录配置、原始数据入口与结论边界。

## 研究证据与边界

长期成长研究关注经历是否能改善后续行动、减少重复指导，以及额外机制的成本是否值得。研究架构见 [Persistent identity](design/persistent-identity.md) 和 [Experience to behavior](design/experience-to-behavior.md)；已执行实验的配置、真实结果和局限只在 [Growth pilot report](experiments/growth-pilot-report.md) 维护。当前小样本没有证明反思所得判断比直接检索经历具有稳定收益，不能把研究意图写成产品已具备的长期学习能力。

其他系统的源码与官方材料在 `research/`，固定运行证据在 `experiments/`，实际模块行为在 `design/`。本索引不复制实时进度、测试计数或旧基线能力表；工程排期和交付状态见 GitHub。

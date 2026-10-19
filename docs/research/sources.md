# Sources：一手资料与核对版本

核对日期：2026-10-12。收录本轮及前续已使用的主要资料；不是穷尽性综述。主题分析见[系统](systems/memory-and-reflection.md)、[证据与判断](learning/evidence-and-beliefs.md)和[评测](evaluation/history-to-behavior.md)。

## 核对约定

源码固定到 commit；论文尽量明确 arXiv 版本；持续更新的官方文档只代表核对当日。引用 prompt 时标设计意图，引用代码时标实际实现；论文和项目评测的结果标作者报告，未经本地复现。

本次使用另一个 tmux pane 做有限范围的只读资料核对，关键结论由主 pane 复核。临时下载样本不作为长期唯一依据；下方链接应足以定位材料。没有安装、运行第三方 agent；SAKI 后续自有实验另见实验报告与自动压缩实现文档。

## 实践与源码

### Model runtime：Pi 的类型与调用边界

2026-10-12 核对 Pi 主分支快照 `4270522473e677ddba7b093caa0064a3df51d459`，提交时间 2026-10-12 00:23:28 UTC。只读检查 `packages/ai`、`packages/agent` 的类型、模型目录、调用入口、provider／API 适配与 Agent loop，未运行 Pi。分析和固定源码入口见 [Model runtime](systems/model-runtime.md)；不覆盖下方早先调查的版本。

### Context compaction：Codex、Pi、OpenCode

分析与完整文件入口见[Context compaction](systems/context-compaction.md)。第三方核对为只读源码与官方材料，没有运行第三方 harness 或调用其压缩 endpoint。后续 SAKI 已使用普通 `/responses` 完成一次真实自动摘要验收，见[实现文档](../design/conversation-compaction.md)。

| 项目 | 固定版本 | 主要一手入口 |
| --- | --- | --- |
| Codex | `7f892275e31002f0422477c6219189284560e689`，2026-10-04 UTC | [local](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/compact.rs)、[remote V2](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/compact_remote_v2.rs)、[调度](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/core/src/session/turn.rs)、[90% 基础阈值](https://github.com/openai/codex/blob/7f892275e31002f0422477c6219189284560e689/codex-rs/protocol/src/openai_models.rs#L526)、[官方 API compaction](https://developers.openai.com/api/docs/guides/compaction) |
| Pi | `b9ab918c626ad3dd5edb58de037540f9467def88`，2026-10-05 UTC | [compaction](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/compaction/compaction.ts)、[AgentSession](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/agent-session.ts)、[SessionManager](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/src/core/session-manager.ts)、[官方说明](https://github.com/earendil-works/pi/blob/b9ab918c626ad3dd5edb58de037540f9467def88/packages/coding-agent/docs/compaction.md) |
| OpenCode | `907b3bc518fa48e90e8ec24dd327d13eee71c36c`，2026-10-03 UTC | [V2 compaction](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/session/compaction.ts)、[V2 history](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/session/history.ts)、[主站文档](https://opencode.ai/v2/docs/compaction)、[开发站文档](https://dev.opencode.ai/v2/docs/compaction/) |

Pi 原仓库 `badlogic/pi-mono` 已重定向至 `earendil-works/pi`。OpenCode 的 V1／V2 路径并存，而且当日主站文档默认值与固定 commit 的 V2 实现不同，分析中保留差异。Codex API 文档与 harness remote V2 的协议入口分别引用，不互相替代。

### Builtin tools：默认集合、输出和执行生命周期

分析见 [Builtin tools](systems/builtin-tools.md)。这轮采用当前源码快照，不覆盖上方 compact 调查的旧版本；第三方只读检查由研究 agent 协助，关键注册和输出路径由主 agent 复核。未运行第三方 harness，也未进行新一轮 SAKI 工具模型实测。

| 项目 | 固定版本／资料 | 主要一手入口 |
| --- | --- | --- |
| Pi | `b78e6a9085343ec0f308c3d377da72528b4cf7ee`，2026-10-05 UTC | [CLI 默认与可选工具](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/docs/cli.md#L128)、[edit 参数](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/edit.ts#L21)、[bash 输出](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/bash.ts#L338)、[输出 accumulator](https://github.com/earendil-works/pi/blob/b78e6a9085343ec0f308c3d377da72528b4cf7ee/packages/coding-agent/src/core/tools/output-accumulator.ts#L28) |
| Codex | `823ea830c0fd418b09ff02d36cad9a1fff66465b`，2026-10-05 UTC | [shell 注册条件](https://github.com/openai/codex/blob/823ea830c0fd418b09ff02d36cad9a1fff66465b/codex-rs/core/src/tools/spec_plan.rs#L1119)、[exec 与 stdin schema](https://github.com/openai/codex/blob/823ea830c0fd418b09ff02d36cad9a1fff66465b/codex-rs/core/src/tools/handlers/shell_spec.rs)、[freeform patch](https://github.com/openai/codex/blob/823ea830c0fd418b09ff02d36cad9a1fff66465b/codex-rs/core/src/tools/handlers/apply_patch_spec.rs) |
| OpenCode | `907b3bc518fa48e90e8ec24dd327d13eee71c36c`，2026-10-03 UTC | [V1 registry](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/tool/registry.ts)、[V1 truncate](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/opencode/src/tool/truncate.ts)、[V2 静态组合](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/tool/builtins.ts)、[V2 bash 采集限制](https://github.com/anomalyco/opencode/blob/907b3bc518fa48e90e8ec24dd327d13eee71c36c/packages/core/src/tool/bash.ts) |
| Claude Code | 当日官方文档，未检查闭源实现 | [Tools reference](https://code.claude.com/docs/en/tools-reference)；默认提供条件可能随模型、平台和版本变化 |
| OpenAI API | 当日官方文档 | [Shell](https://developers.openai.com/api/docs/guides/tools-shell)、[Apply Patch](https://developers.openai.com/api/docs/guides/tools-apply-patch)；原生调用协议不等于 Codex CLI 工具或 SAKI 当前 FunctionCall dispatcher |

### 模型规格：models.dev

- [项目说明](https://github.com/anomalyco/models.dev)：provider 元数据、canonical 模型数据与两者合并的 API；provider 字段可覆盖统一模型规格。
- [combined catalog](https://models.dev/catalog.json)：本次实际下载检查，顶层为 `providers` 与 `models`；当前实现同步此 endpoint，不复制整个规格数据库进源码。
- [provider catalog](https://models.dev/api.json) 与 [统一模型目录](https://models.dev/models.json)：本次分别核对结构。combined catalog 的匹配策略、缓存与 256K 默认见[实现文档](../design/conversation-compaction.md)。

以上为持续更新数据，核对日期 2026-10-05；不宣称数据库规格证明了每个代理入口的实际边界。

### Letta Code

固定源码版本：[`77faf36e34378946c8e69c0bfaa5595a7be4c326`](https://github.com/letta-ai/letta-code/tree/77faf36e34378946c8e69c0bfaa5595a7be4c326)。

- [Context Repositories，2026-02-12](https://www.letta.com/blog/context-repositories/)：文件与 Git 管理记忆；旧布局不可替代当前文档。
- [Memory 官方文档](https://docs.letta.com/configuration/memory)：当日的加载布局与记忆操作。
- [Context Constitution，2026-04-02](https://www.letta.com/blog/context-constitution/)：方向与身份主张；不是自然人格成长实验。
- [Reflection prompt v2](https://github.com/letta-ai/letta-code/blob/77faf36e34378946c8e69c0bfaa5595a7be4c326/src/agent/subagents/builtin/reflection-v2.md)：更新、归档与冲突处理的提示意图。
- [Reflection settings](https://github.com/letta-ai/letta-code/blob/77faf36e34378946c8e69c0bfaa5595a7be4c326/src/reflection-settings.ts)：触发模式。
- [Reflection arena](https://github.com/letta-ai/letta-code/blob/77faf36e34378946c8e69c0bfaa5595a7be4c326/src/cli/helpers/reflection-arena.ts)：候选偏好比较与合并，未验证下游行动收益。
- [生产记忆评测，2026-07-28](https://www.letta.com/blog/evaluating-memory-in-production-agents/)：项目方私有 ContextBench V2；未独立复现，不含已核实的 GPT-6 结果。

### Hermes

固定源码版本：[`af90026aa09949579bd423d24def3d38f743cde0`](https://github.com/NousResearch/hermes-agent/tree/af90026aa09949579bd423d24def3d38f743cde0)。

- [Memory 官方文档](https://hermes-agent.nousresearch.com/docs/user-guide/features/memory)：持久摘要、会话快照与历史检索。
- [Skills 官方文档](https://hermes-agent.nousresearch.com/docs/user-guide/features/skills)：按需程序性记忆。
- [background_review.py](https://github.com/NousResearch/hermes-agent/blob/af90026aa09949579bd423d24def3d38f743cde0/agent/background_review.py)：后台 review 的提示、预算和开关；没有据此穷尽调度路径。

### Honcho

固定源码版本：[`8e4df990d974c146a100e96ab4b3957a6591ceab`](https://github.com/plastic-labs/honcho/tree/8e4df990d974c146a100e96ab4b3957a6591ceab)，commit 日期 2026-10-02。

- [deriver.py](https://github.com/plastic-labs/honcho/blob/8e4df990d974c146a100e96ab4b3957a6591ceab/src/deriver/deriver.py)：消息批次与观察写入。
- [prompts.py](https://github.com/plastic-labs/honcho/blob/8e4df990d974c146a100e96ab4b3957a6591ceab/src/deriver/prompts.py)：目标 peer 提取与时间处理意图。
- [consumer.py](https://github.com/plastic-labs/honcho/blob/8e4df990d974c146a100e96ab4b3957a6591ceab/src/deriver/consumer.py)：任务处理路径。
- [dream_scheduler.py](https://github.com/plastic-labs/honcho/blob/8e4df990d974c146a100e96ab4b3957a6591ceab/src/dreamer/dream_scheduler.py)：explicit 增量计数与调度门槛。
- [orchestrator.py](https://github.com/plastic-labs/honcho/blob/8e4df990d974c146a100e96ab4b3957a6591ceab/src/dreamer/orchestrator.py)：后台推导流程。
- [representation.py](https://github.com/plastic-labs/honcho/blob/8e4df990d974c146a100e96ab4b3957a6591ceab/src/crud/representation.py)：来源 metadata 与 runtime 查询。

检查范围限以上六文件，未核对完整 document 去重／矛盾处理，也未验证下游行动收益。

### Hindsight

固定源码版本：[`f7dd3f4fd7420f7beec60c32c965e5e5cf7be066`](https://github.com/vectorize-io/hindsight/tree/f7dd3f4fd7420f7beec60c32c965e5e5cf7be066)。

- [Hindsight is 20/20，arXiv 2512.12818](https://arxiv.org/html/2512.12818)：原始论文中的 opinion／confidence 机制，不能直接当当前实现。
- [ACL 2026 demo](https://aclanthology.org/2026.acl-demo.27/)：后续发表记录。
- [移除 opinion 与 confidence 的迁移](https://github.com/vectorize-io/hindsight/blob/f7dd3f4fd7420f7beec60c32c965e5e5cf7be066/hindsight-api-slim/hindsight_api/alembic/versions/g2h3i4j5k6l7_remove_opinion_fact_type.py)：版本变化的直接证据，未确定移除动机。
- [Consolidation prompts](https://github.com/vectorize-io/hindsight/blob/f7dd3f4fd7420f7beec60c32c965e5e5cf7be066/hindsight-api-slim/hindsight_api/engine/consolidation/prompts.py)：来源、事件 facet 和时间处理意图。
- [Delta operations](https://github.com/vectorize-io/hindsight/blob/f7dd3f4fd7420f7beec60c32c965e5e5cf7be066/hindsight-api-slim/hindsight_api/engine/reflect/delta_ops.py)：实际局部修改机制。
- [Observations](https://hindsight.vectorize.io/developer/observations)：来源、版本与 freshness。
- [Mental models](https://hindsight.vectorize.io/developer/mental-models)：持续问题的预计算回答与更新。
- [Reflect](https://hindsight.vectorize.io/developer/reflect)：检索、核验与回答路径。

### Graphiti／Zep

- [官方 provenance 说明](https://www.getzep.com/blog/how-zep-tracks-provenance-in-agent-memory/)：源 episode、事实失效与历史追踪。本轮按官方说明引用，未声明完成最新 Graphiti 全路径源码核对。

### SkillOpt

固定源码版本：[`fa4ca184573e42ec11472959dd57422381418096`](https://github.com/microsoft/SkillOpt/tree/fa4ca184573e42ec11472959dd57422381418096)。

- [Gating Reflection for Safe Updates，2026-07-14](https://microsoft.github.io/SkillOpt/blog/gating-reflection-safe-updates/)：项目方反思与 gate 实验报告；逐次数据未完整公开，部分比较有多变量变化。
- [skill_aware.py](https://github.com/microsoft/SkillOpt/blob/fa4ca184573e42ec11472959dd57422381418096/skillopt/optimizer/skill_aware.py)：方法缺陷／执行遗漏的归因分流。
- [reflect.py](https://github.com/microsoft/SkillOpt/blob/fa4ca184573e42ec11472959dd57422381418096/skillopt/gradient/reflect.py)：候选更新与仅提醒路径。
- [sleep gate.py](https://github.com/microsoft/SkillOpt/blob/fa4ca184573e42ec11472959dd57422381418096/skillopt_sleep/gate.py)：按评分接受或拒绝候选。

## 论文与评测

| 来源／核对版本 | 与本项目相关的用途 | 重要边界 |
| --- | --- | --- |
| [MemoryArena，2602.16313v2，2026-09-17](https://arxiv.org/abs/2602.16313v2) | 跨会话历史驱动行动；[ICML 页面](https://proceedings.mlr.press/v306/he26am.html) | 文内 GPT-5-mini／5.1-mini 命名不一致；未发现 GPT-6 实验 |
| [MemDelta，2606.29914v1，2026-06-29](https://arxiv.org/html/2606.29914v1) | 控制 embedding、模型和写入成本 | 主要模型较旧；Mem0 部分比较仅 88 题且 embedding 非完全匹配 |
| [PASTBench，2608.04003，2026-08](https://arxiv.org/abs/2608.04003) | 持久开关与新上下文 | trace 一致不等于具体反思的因果证明；未发现 GPT-6 实验 |
| [STALE，2605.06527，2026-05](https://arxiv.org/abs/2605.06527) | 状态更新与隐式策略适应 | 推荐输出不是实际工具行动；生成模型与被测模型要区分 |
| [Reality Monitoring，2607.23927v1，2026-07-27](https://arxiv.org/html/2607.23927v1) | 来源识别与会话结构 | 词对任务、Gemma／Llama 六模型，不代表目标模型真实工具表现 |
| [MemRL，2601.03192，2026-02 更新](https://arxiv.org/abs/2601.03192) | 反馈改变记忆选择 utility | 任务 utility 不是真实性 confidence，不证明完整个体性 |
| [MemP，2508.06433v4，2026-04-15](https://arxiv.org/abs/2508.06433v4) | 程序性记忆与跨任务行为 | 任务范围内的经验学习不等于长期偏好和关系发展 |
| [EvoMemory，2511.20857v4，2026-05-18](https://arxiv.org/html/2511.20857v4) | 流式记忆更新与简单检索基线 | 实验模型较旧，不用于最新模型排名判断 |

## 版本不一致的材料

[AEL 论文 2604.21725](https://arxiv.org/abs/2604.21725) 的 v2 于 2026-08-25 更新，而本次查看的[公开仓库](https://github.com/WujiangXu/AEL) README 仍描述较早方案。论文机制与公开代码不能默认逐项对应；暂不作为 SAKI 架构的直接实现依据。

## 尚无证据支持的结论

这些材料没有直接证明 SAKI 的长期人格自然形成、完整 identity 跨模型迁移、自动失败归因总是正确，或者所有深层偏好都可以通过任务 reward 学得。本项目已有目标入口上的小样本受控 pilot，见[实验报告](../experiments/growth-pilot-report.md)；它尚不能支持稳定成长或长期使用收益。


## Terminal UI

2026-10-13 追加显示层研究，分析见 [Terminal presentation](systems/terminal-presentation.md)，设计推论见 [TUI style](../design/tui-style.md)。对应本机安装版本：Codex `0.162.1` 固定到 [`092d3acd6bec3e3a14bdc7e7a2810ab628ab759d`](https://github.com/openai/codex/tree/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d)，Pi `1.0.2` 固定到 [`cd32f7725fdbddbaecdff5b1e68491563394e0ca`](https://github.com/earendil-works/pi/tree/cd32f7725fdbddbaecdff5b1e68491563394e0ca)。检查消息／推理、工具预览、Markdown、主题和 footer 的发布源码；未做两者的完整可用性对照实验。以下早期研究版本继续保留。

2026-10-15 四系统能力矩阵补充：为 [Issue #19](https://github.com/HeimaoLST/Saki/issues/19) 固定最新稳定 release 的 peeled commit，只读核对输入／历史／文件引用／外部编辑器、取消／排队／引导、transcript 搜索／跳转、工具 diff／高亮、会话检索、命令／模型／主题／无障碍、流式与长历史性能。本机 Pi 已升级到 `1.2.0`、Codex 仍为 `0.162.1`。矩阵与逐项链接见 [Terminal presentation](systems/terminal-presentation.md) 第 8 节；未运行第三方 harness。

- Codex `rust-v0.162.1` peeled commit [`092d3acd6bec3e3a14bdc7e7a2810ab628ab759d`](https://github.com/openai/codex/tree/092d3acd6bec3e3a14bdc7e7a2810ab628ab759d)。
- Pi `v1.2.0` peeled commit [`06a466935536f8c9bcacb2d5efc0fc021be1bdff`](https://github.com/earendil-works/pi/tree/06a466935536f8c9bcacb2d5efc0fc021be1bdff)。
- OpenCode `v1.19.0` peeled commit [`733b2c627e5aec39cb33cc22373bdce33053ef2c`](https://github.com/anomalyco/opencode/tree/733b2c627e5aec39cb33cc22373bdce33053ef2c)。

核对日期：2026-10-06。以下是 TUI 布局研究使用的新快照，不替换此前 builtin tools 或 compact 的核对版本。分析见 [Terminal UI](systems/terminal-ui.md)，候选与对比页面见 [TUI 设计](../design/tui.md)。

- Pi：固定到 [28dcce2ba45ce4a9efeb0f5b686f0be830fd89b9](https://github.com/earendil-works/pi/tree/28dcce2ba45ce4a9efeb0f5b686f0be830fd89b9)；检查 packages/coding-agent/docs/usage.md、themes.md 及官方 interactive-mode 图。当前仓库为 earendil-works/pi，所检查版本明确支持 fullscreen／regular 两种模式。
- Codex：固定到 [822e58cc3d666166c7446c5b1ea2e52f5d09594c](https://github.com/openai/codex/tree/822e58cc3d666166c7446c5b1ea2e52f5d09594c)；检查 codex-rs/tui/src/lib.rs 与 transcript_mode.rs 的屏幕策略和 transcript ownership。
- Crush：固定到 [8b1d542c2ef1be9a3022f67363d696e53f31b0d4](https://github.com/charmbracelet/crush/tree/8b1d542c2ef1be9a3022f67363d696e53f31b0d4)；检查 internal/ui/model/ui.go、sidebar.go、header.go、chat.go，以及 styles/palette.go、themes.go。
- Lazygit：固定到 [c5f7158154602d23b0750d4304c73e2aead8df5b](https://github.com/jesseduffield/lazygit/tree/c5f7158154602d23b0750d4304c73e2aead8df5b)；检查 docs/Config.md 的面板、焦点大小、宽度适配和鼠标选择说明。
- Ratatui：[Alternate screen](https://ratatui.rs/concepts/backends/alternate-screen/) 与 [Backends](https://ratatui.rs/concepts/backends/)，持续更新的官方文档，代表核对当日。

未运行上述第三方 harness；可用性建议属于设计推论。浏览器中的 SAKI 对比稿已经验证布局与局部交互，真实终端仍需另行验证。

### Codex 自有组件

2026-10-06 追加源码核对，固定到 [`c0c230e6730b3b3c9101b8aff4b9aea4027cea5b`](https://github.com/openai/codex/tree/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b)，不替换早先布局研究快照。分析与逐项链接见 [Terminal UI 第 6 节](systems/terminal-ui.md#6-codex-的自有组件与交互机制)。

- 依赖与复用：workspace／tui Cargo.toml、public_widgets/composer_input.rs、lib.rs 的导出；确认 Crossterm Git patch。
- 编辑与输入：bottom_pane/textarea.rs、textarea/wrapping.rs、textarea/editing.rs、chat_composer.rs、chat_composer/paste_input.rs、paste_burst.rs、chat_composer_history.rs。
- 历史与工具：history_cell/mod.rs、base.rs、markdown_render_cache.rs、chatwidget.rs、exec_cell/model.rs、compact.rs、tool_output.rs。
- 阅读与绘制：transcript_view.rs、layout.rs、mutations.rs、bookmark.rs、tui/frame_requester.rs、frame_rate_limiter.rs、render/renderable.rs、markdown_render.rs。
- 焦点与恢复：bottom_pane/mod.rs、bottom_pane_view.rs、tui.rs 的初始化与 panic hook。
- 阅读的行为测试：transcript_view/layout_tests.rs、app/owned_transcript_follow_tests.rs；未执行这些测试，未编译或运行 Codex。


2026-10-08 输入框交互追加研究：本机 Codex 0.161.0，对应官方 tag `rust-v0.161.0` 的 commit `979011409de0a60b52f179721948e65531d26144`。读取 keymap.rs、bottom_pane/textarea.rs、chat_composer.rs、chat_composer_history.rs、mod.rs、chat_composer/history_search.rs、paste_input.rs 与 chatwidget/interaction.rs；核对官方 developer-commands 和 CLI customization 文档。结论与逐项来源见 [Terminal UI 第 7 节](systems/terminal-ui.md#7-codex-输入框的编辑手感)。Codex 部分是源码阅读，未运行其交互测试；随后经用户授权实现 SAKI 的三组输入改造，SAKI 的实际验收见 [TUI 设计](../design/tui.md#输入框优化验收)。

### Rust TUI 组件

核对日期：2026-10-06。以 crates.io 发布版本及实际发布包为准：Ratatui 0.30.2、Crossterm 0.29.0、ratatui-textarea 0.9.3、tui-markdown 0.3.10、tui-widget-list 0.15.3、tui-scrollview 0.6.8、rat-widget 3.2.1、iocraft 0.9.1。原 tui-textarea 0.7.0 的 Ratatui 0.29 依赖也已核对。详细来源与版本链接见 [Terminal UI 的组件研究](systems/terminal-ui.md#5-rust-技术栈与组件核对)。

核心组件组合在独立 TestBackend 小样中编译与运行通过，依赖解析未出现重复的 Ratatui Core 或 Crossterm；项目依赖未修改。该检查不证明输入法、tmux 或长历史性能已经通过。

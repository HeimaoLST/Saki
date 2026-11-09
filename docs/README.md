# Saki 文档

更新日期：2026-11-09。

本仓库只维护 Saki Runtime。桌面产品文档见 [Nagi](https://github.com/HeimaoLST/Nagi/tree/dev/docs)，终端产品见 [Rous](https://github.com/HeimaoLST/Rous/tree/dev/docs)。产品仓库职责、保留成果和迁移兼容约束在[产品仓库划分](design/product-repositories.md)，实时过程和验收记录在 [#333](https://github.com/HeimaoLST/Saki/issues/333)。

## 使用与实现

- [Runtime API](design/runtime-api.md)：version 1 JSONL、方法、能力、错误、会话快照/元数据通知及生命周期。
- [会话与持久化](design/session-management.md)：执行归属、恢复、历史和状态。
- [模型调用](design/model-runtime.md)：协议、客户端、能力和限制。
- [工具与输出](design/builtin-tools.md)：内置工具、MCP 与结果边界。
- [上下文压缩](design/conversation-compaction.md)：有限上下文、保留原始历史与恢复。
- [多模态输入](design/multimodal-input.md)：图片能力与协议限制。
- [Runtime 需求](requirements/agent-runtime.md)：通用运行能力与产品边界。
- [CI 与产物](design/ci.md)：独立检查、来源身份和消费者兼容。

源码按 `src/agent/`、`src/client/`、`src/model/`、`src/protocol/`、`src/session/`、`src/tools/` 聚合。`src/app.rs` 只组装 Runtime 宿主，`src/main.rs` 提供服务 CLI；前端和产品源码分别归独立仓库。

## 研究与实验

- [持续身份研究](design/persistent-identity.md)、[经历与判断](research/learning/evidence-and-beliefs.md)、[历史如何影响行为](research/evaluation/history-to-behavior.md)。
- [上下文系统调研](research/systems/context-compaction.md)、[模型系统调研](research/systems/model-runtime.md)、[记忆与反思](research/systems/memory-and-reflection.md)。
- [成长试验](experiments/growth-pilot-report.md)、[历史依赖协议](experiments/history-dependence.md)、[压缩网关实验](experiments/compaction-gateway-report.md)。

研究/提案不等于已实施的运行能力。实验、真实模型样本、原生产品和真人验收各有适用范围，不能互相代替。

## 证据与协作

文档区分实现事实、作者报告、设计推论与提案、待验证；固定到注明的提交或版本，历史失败保留。GitHub Spec 承载当前任务 delta、依赖与验收，长期采用的需求/契约写入对应主题；规则见 [AGENTS.md](../AGENTS.md)。

项目 `.agents/` 保留现有 [pstack 来源清单](../.agents/pstack-source.json)、[指南](../.agents/docs/guide/README.md)和许可证。技能不替代用户授权，不自动启用额外模型、后台执行或发布流程。

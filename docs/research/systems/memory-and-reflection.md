# Memory and reflection：已有系统的实现与边界

核对日期：2026-10-05。状态：官方资料与有限范围的固定版本源码检查；未运行第三方系统。版本索引见 [Sources](../sources.md)。

## 与 SAKI 最相关的机制

| 系统 | 可借鉴的机制 | 尚不能据此证明的内容 |
| --- | --- | --- |
| Letta Code | 文件化记忆、按需加载、后台反思、Git 历史 | 人格自然形成、反思必然改善行动 |
| Hermes | 小型持久摘要、历史检索、按需技能、后台 review | 低频成长、完整的 belief 矛盾表示 |
| Honcho | 按观察者建模、来源关联、派生层级、后台推导 | 推导正确、精确的逐条证据映射、行为收益 |
| Hindsight | 来源与版本、observation consolidation、mental model 局部更新 | 论文原始 opinion／confidence 机制仍存在 |
| SkillOpt | 失败归因、候选修改、后续任务验证与选择 | 所有偏好与价值均适合分数优化 |

## Letta Code：连续性由持久状态与加载机制共同提供

官方 Context Repositories 介绍文件与 Git 管理记忆，以及后台隔离修改再合并。当前 Memory 文档使用 `MEMORY.md` 导航、根部核心记忆和子目录按需内容；不能把早期文章的目录布局当成唯一现行结构。[Context Repositories](https://www.letta.com/blog/context-repositories/)、[Memory 文档](https://docs.letta.com/configuration/memory)。

固定版本 `77faf36e34378946c8e69c0bfaa5595a7be4c326` 的 reflection prompt 优先纠正、偏好、稳定事实和可复用方法，也允许没有值得写入的内容。对过时条目倾向原位修正，必要时归档；跨记录优先最新 evidence。这是提示意图，不是语义正确性的保证，也不等价于保留未决观点冲突。[reflection-v2.md](https://github.com/letta-ai/letta-code/blob/77faf36e34378946c8e69c0bfaa5595a7be4c326/src/agent/subagents/builtin/reflection-v2.md)。

同版本的触发设置支持关闭、步数和 compaction 模式；默认模式为 compaction。`reflection-arena` 提供候选反思的盲选与合并，检查到的路径比较的是反思产物偏好，没有证明后续任务更好。[reflection-settings.ts](https://github.com/letta-ai/letta-code/blob/77faf36e34378946c8e69c0bfaa5595a7be4c326/src/reflection-settings.ts)、[reflection-arena.ts](https://github.com/letta-ai/letta-code/blob/77faf36e34378946c8e69c0bfaa5595a7be4c326/src/cli/helpers/reflection-arena.ts)。

**设计推论**：文件、版本和按需加载已经是可落地实践；identity 的措辞不应被误读为经历塑造个体的实验结果。

## Hermes：持续学习的默认激励与低频成长存在取舍

官方 Memory 文档描述 `MEMORY.md`／`USER.md` 的有限字符预算、会话开始时的摘要快照，以及对历史的独立检索。持久摘要更新与当前 prompt 生效存在会话边界，不能假设写入后立刻改变本轮上下文。[Memory 文档](https://hermes-agent.nousresearch.com/docs/user-guide/features/memory)。

固定版本 `af90026aa09949579bd423d24def3d38f743cde0` 的后台 review 使用独立路径整理记忆和技能。其 skill／combined prompt 积极鼓励大多数会话更新，与 SAKI 的低频深层变化原则不同；同时要求尚未找到有效方法的失败不要被写成可靠工作流。技能正文排除临时事件标识，不代表整个系统删除来源历史。[background_review.py](https://github.com/NousResearch/hermes-agent/blob/af90026aa09949579bd423d24def3d38f743cde0/agent/background_review.py)。

同文件中 `max_input_tokens <= 0` 关闭的是输入预算限制，返回无限制标记，不能解释为关闭 review。看到 nudge 间隔也不足以推导自动 review 的调度频率。

**设计推论**：借鉴历史检索与按需程序性记忆，另行验证触发成本；避免以“每次都学一点”为默认成功标准。

## Honcho：防反思自激，与防证据重复是两个问题

核对固定版本 `8e4df990d974c146a100e96ab4b3957a6591ceab`，范围是 deriver、consumer、dream scheduler／orchestrator 和 representation 共六个文件。辅助 pane 提供初查，主 pane 复核来源写入、触发和查询路径。

`save_representation` 把观察写成 Document，带 `message_ids`、消息时间和 session；演绎观察另有 premises。当前检查路径中，同批观察共享目标 peer 的消息 ID 集合，不能据此声称每个结论已定位到一条精确原文。[representation.py](https://github.com/plastic-labs/honcho/blob/8e4df990d974c146a100e96ab4b3957a6591ceab/src/crud/representation.py)。

dream scheduler 仅统计 `level == explicit` 的文档作为增量触发依据，排除 dream 推导产物，避免它们不断抬高触发计数。还检查启用状态、最小间隔和 pending 状态；idle 配置可以为零，因此不是所有配置都必须等待空闲。[dream_scheduler.py](https://github.com/plastic-labs/honcho/blob/8e4df990d974c146a100e96ab4b3957a6591ceab/src/dreamer/dream_scheduler.py)。

`explicit` 仍是从消息中提取的观察，名称并不保证外部验证为真。runtime representation 可以混合语义检索、recent 和可选的 most-derived 排序。后者实际按 `times_derived` 等字段排序，不能简单解释为只加载某个派生 level；`max_observations` 也是数量上限，不是严格 token 上限。[representation.py](https://github.com/plastic-labs/honcho/blob/8e4df990d974c146a100e96ab4b3957a6591ceab/src/crud/representation.py)。

保存路径可见去重计数，但本次未检查 document 层的完整策略；不能把语义去重当作矛盾裁决，也不能根据六个文件未出现某机制就断言全项目不存在。下游行动改善在这次检查中未验证。

**设计推论**：应分别管理反思调度、证据来源和 runtime 排序。一个结论被多次推导、复述或检索，可以说明使用频繁，却不自动说明它更可靠。

## Hindsight：论文机制必须对照当前代码

最初论文提出 opinion 及固定步长 confidence 更新。但核对版本 `f7dd3f4fd7420f7beec60c32c965e5e5cf7be066` 的数据库迁移删除了 opinion 数据、相关索引及 `confidence_score`。这证明版本差异，不证明删除原因，也不能据此否定所有数值 confidence。[论文](https://arxiv.org/html/2512.12818)、[迁移源码](https://github.com/vectorize-io/hindsight/blob/f7dd3f4fd7420f7beec60c32c965e5e5cf7be066/hindsight-api-slim/hindsight_api/alembic/versions/g2h3i4j5k6l7_remove_opinion_fact_type.py)。

当前 observations 文档强调来源、版本和 freshness；mental models 是针对持续问题维护的预计算回答。它们有利于降低读取成本，但问题本身通常由配置给定，不等于个体自动形成目标。[Observations](https://hindsight.vectorize.io/developer/observations)、[Mental models](https://hindsight.vectorize.io/developer/mental-models)。

同版本 `delta_ops.py` 实现按目标 ID 修改块或章节，保留未命中部分，不通过 LLM 重生成整个文档。这是机械上的局部更新；语义正确仍需其他证据。[delta_ops.py](https://github.com/vectorize-io/hindsight/blob/f7dd3f4fd7420f7beec60c32c965e5e5cf7be066/hindsight-api-slim/hindsight_api/engine/reflect/delta_ops.py)。

**设计推论**：版本化派生状态和真实 patch 值得借鉴。需要 LLM 提取的 retain 不能直接充当 SAKI 每次交互的廉价原始 trace。

## SkillOpt：失败后先判断要改什么

固定版本 `fa4ca184573e42ec11472959dd57422381418096` 实现可选的 skill-aware reflection：区分方法缺陷和已有正确方法未执行，分别形成正文修改或执行提醒。归因由模型给出，分流代码存在不意味着归因准确。[skill_aware.py](https://github.com/microsoft/SkillOpt/blob/fa4ca184573e42ec11472959dd57422381418096/skillopt/optimizer/skill_aware.py)。

sleep 路径有候选与当前版本的评分 gate，候选评分更高才接受。它支持用任务结果审查学习产物；有限验证集仍不能覆盖全部未来场景。[gate.py](https://github.com/microsoft/SkillOpt/blob/fa4ca184573e42ec11472959dd57422381418096/skillopt_sleep/gate.py)。

**设计推论**：归因和行为验证应进入闭环；SAKI 还需允许环境变化和无法归因，不把所有失败压进二选一。对价值和关系不能照搬任务评分 gate。

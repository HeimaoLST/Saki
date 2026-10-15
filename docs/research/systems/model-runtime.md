# Model runtime：Pi 的类型、模型与 API 边界

核对日期：2026-10-12。范围：Pi 当前主分支的只读源码核对，以及对 SAKI 的设计推论；未运行 Pi。后续已按用户授权实施 SAKI 分层，实际接口及验证见[实现文档](../../design/model-runtime.md)，以下保留调查时的判断。

固定快照：[4270522473e677ddba7b093caa0064a3df51d459](https://github.com/earendil-works/pi/commit/4270522473e677ddba7b093caa0064a3df51d459)，提交时间 2026-10-12 00:23:28 UTC。该快照的 `pi-ai` 与 `pi-agent-core` package.json 均标记为 1.1.0，这不表示已核验 npm 发布状态。此前 compact 调查使用的 10 月 5 日快照继续保留，不能混用两个版本的目录与入口。

## 实际边界

Pi 使用 `packages/ai` 提供模型调用能力，`packages/agent` 使用其公共类型和可注入的调用函数驱动 Agent loop。下表描述当前实现，没有把 SAKI 的候选目录名套在 Pi 身上。

| 职责 | Pi 源码 | 核对结果 |
| --- | --- | --- |
| 公共调用类型 | [types.ts](https://github.com/earendil-works/pi/blob/4270522473e677ddba7b093caa0064a3df51d459/packages/ai/src/types.ts) | 消息、上下文、工具声明、usage、事件、模型规格与调用选项的公共契约 |
| 模型目录类型 | [model-catalog.ts](https://github.com/earendil-works/pi/blob/4270522473e677ddba7b093caa0064a3df51d459/packages/ai/src/model-catalog.ts) | 按 provider、API 和模型种类组织目录；各 provider 另有模型数据文件 |
| 调用入口与 provider 组装 | [models.ts](https://github.com/earendil-works/pi/blob/4270522473e677ddba7b093caa0064a3df51d459/packages/ai/src/models.ts) | `Models` 集合管理 provider，处理调用分派与认证；该文件并非纯元数据目录 |
| Provider 配置 | [providers/deepseek.ts](https://github.com/earendil-works/pi/blob/4270522473e677ddba7b093caa0064a3df51d459/packages/ai/src/providers/deepseek.ts) | 组合服务地址、认证、模型数据和 API 实现；该 provider 选择 OpenAI Completions 兼容实现 |
| API 适配实现 | [api/openai-responses.ts](https://github.com/earendil-works/pi/blob/4270522473e677ddba7b093caa0064a3df51d459/packages/ai/src/api/openai-responses.ts) | 构造原生请求、调用 SDK、转换响应流；消息及工具转换委托给同目录 shared 模块 |
| Agent 执行 | [agent-loop.ts](https://github.com/earendil-works/pi/blob/4270522473e677ddba7b093caa0064a3df51d459/packages/agent/src/agent-loop.ts) | 在调用前转换 Agent 消息，随后通过传入的 stream 函数获取公共事件与最终消息 |

职责分离不要求每种职责独占一个文件。Pi 的 `types.ts` 同时定义消息与模型类型，`models.ts` 也承担调用组装；其依赖与转换边界比文件名更能说明设计。

## 两种协议需要区分

Pi 的公共 `Message`、`Context` 和 `AssistantMessageEvent` 不是 OpenAI Responses 原生请求／响应 JSON。API 适配实现把公共输入转换为服务端格式，再把响应转换回公共消息及事件。

Agent 还可以持有应用扩展消息。`convertToLlm` 在请求前将它们转为模型消息或滤除；这说明 Agent 历史、模型输入和界面显示不必是同一个数据结构。[调用前转换](https://github.com/earendil-works/pi/blob/4270522473e677ddba7b093caa0064a3df51d459/packages/agent/src/agent-loop.ts#L390)、[转换契约](https://github.com/earendil-works/pi/blob/4270522473e677ddba7b093caa0064a3df51d459/packages/agent/src/types.ts#L193)。

因此，为 SAKI 抽出 `protocol/` 时，需要先明确其中放的是核心公共协议，还是某家 API 的 wire types。仅把原来的 Responses 类型移走，能够整理文件，但还没有获得供应商无关的消息契约。

## 完整结果与流式结果共用路径

当前 `Models.complete()` 返回 `this.stream(model, context, options).result()`；`completeSimple()` 同样等待 `streamSimple()` 的结果。[实现](https://github.com/earendil-works/pi/blob/4270522473e677ddba7b093caa0064a3df51d459/packages/ai/src/models.ts#L893)。

这是“逐步消费事件”与“等待最终消息”两种使用方式，不是两个独立客户端，也不证明底层使用非流式 HTTP 请求。异步调用与是否向调用方暴露增量是不同维度。

SAKI 后续引入流式时，可以借鉴这条执行路径，避免请求、重试和结果处理分别维护两份。现阶段不需要为了命名整理提前实现流式能力。

## 模型、服务商和 API 的关系

Pi 的模型类型显式记录 `provider`、`api` 和调用地址，模型能力与容量同样属于规格数据。[Model 类型](https://github.com/earendil-works/pi/blob/4270522473e677ddba7b093caa0064a3df51d459/packages/ai/src/types.ts#L820)。

三种职责可以分开维护，但并非互不关联：选中的模型入口决定使用哪种 API 适配，不能只凭模型名称猜协议。调查时 SAKI 名为 `DeepseekClient` 的实现实际发送 Responses 请求；与 Pi 的 DeepSeek provider 配置对照，客户端按 API 协议命名更能表达它的实际复用范围。后续已更名为 ResponsesClient。

## 对 SAKI 的建议

以下是调查时的设计推论，后续实施范围见[实现文档](../../design/model-runtime.md)：

- `model/` 管模型规格、能力、容量与目录同步匹配。
- `protocol/` 管核心模型调用所需的公共数据契约，不持有 Conversation、客户端、UI 或工具执行 handler。
- `client/` 管实际请求、API 格式转换、错误分类、重试与增量解析。当前可以只实现 Responses，不预建多个 provider 或插件注册框架。

依赖目标是核心准备公共输入、客户端执行调用并返回公共结果；具体服务端格式留在相应客户端适配实现中。运行时消息与界面事件仍按现有职责区分。

实施前识别出的边界：

1. 调查时 ModelRequest 直接借用 `Conversation`；现在[公共请求](../../../src/protocol/mod.rs) 已接收输入项切片。
2. 调查时 [Conversation](../../../src/agent/conversation.rs) 直接保存 Responses `InputItem`；现在改为公共协议，并给新 journal 加入格式版本。
3. 调查时 ToolDefinition 与 handler／registry 同处一个模块；现在[协议声明](../../../src/protocol/mod.rs) 与[执行实现](../../../src/tools/tool_registry.rs) 已分开。
4. 公共消息转换必须保留后续调用所需的 reasoning、工具关联及提供商特定字段；不能为统一格式丢掉影响 continuation 的内容。

后续扩展仍应先确定数据契约和迁移范围，再调整源码。Pi 的认证体系、动态目录、兼容规则与类型技巧服务于它已有的多 provider 场景，不应一次性搬进 SAKI。

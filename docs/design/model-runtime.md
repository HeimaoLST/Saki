# Model runtime：协议、客户端与模型目录

更新日期：2026-10-13。状态：已实施职责拆分和 Responses 流式响应；主 Agent 默认流式，compact 与实验保留完整响应接口。参考依据见 [Pi 分层研究](../research/systems/model-runtime.md)。

## 模块与依赖

| 模块 | 职责 |
| --- | --- |
| [protocol/](../../src/protocol/mod.rs) | 公共请求、输入／输出项、文字和图片内容、工具声明、推理、usage 与 opaque continuation 数据 |
| [client/responses.rs](../../src/client/responses.rs) | Responses 原生格式转换、HTTP 调用、错误分类、有限重试与响应解码 |
| [client/responses/stream.rs](../../src/client/responses/stream.rs) | SSE 消费、增量转换、终止与断流判断 |
| [model/catalog.rs](../../src/model/catalog.rs) | models.dev 目录同步、缓存、模型匹配与容量读取；尚未扩展为完整能力数据库 |
| [agent/conversation.rs](../../src/agent/conversation.rs) | 公共协议上下文、usage 校准、compact、版本化历史日志 |
| [tools/tool_registry.rs](../../src/tools/tool_registry.rs) | 工具 handler 注册与执行；使用公共协议的 ToolDefinition，不再定义协议类型 |

客户端不引用 Conversation、工具 registry、TUI 或 console；公共协议不引用这些运行模块。模型目录也不依赖推理客户端。Conversation 按既有设计借用客户端执行 compact，当前 Agent 持有一个具体的 ResponsesClient；当前没有引入多客户端 trait 或独立 library。

## 调用契约

`ModelRequest::new(model, input, tools)` 接收 `&[InputItem]`，不再接收 `&Conversation`。Conversation 通过只读 `items()` 提供当前活动输入，调用方仍负责把响应结果加入上下文。

公共协议使用 `ToolCall`／`ToolResult`、`Text`／`Image { url }`；Responses 适配转换为 `function_call`／`function_call_output`、`input_text`／`input_image`。工具声明的 JSON Schema 保持原样；原始 arguments 字符串不在客户端解析，继续由 Agent 处理参数错误并返回工具结果。

`ResponsesClient::request_body` 返回非流式 HTTP JSON body，`respond_stream` 使用相同内容并添加 `stream: true`；公共请求的 Serialize 表示不等同于服务端 body。成长实验继续用该适配入口记录实际请求，响应 output 改为记录公共结果表示。

默认端点、`.env` 加载和环境变量配置沿用原有行为；客户端按实际协议由 DeepseekClient 更名为 ResponsesClient。`respond` 仍对传输失败、408／429／5xx 最多尝试三次；context overflow 与其他请求错误立即返回，成功 HTTP 响应的解析失败不重试。

请求格式转换在发送前完成，失败不重试。`respond_once` 最多发出一次 HTTP 请求，转换失败返回 `attempts=0`；发送后无论成功或失败都返回 `attempts=1`。

## 流式响应契约

`respond_stream(request, on_event)` 是异步调用：收到增量后同步调用轻量回调，成功时返回完整 ModelResponse。公共协议的 ModelStreamEvent 提供 TextDelta／ReasoningDelta／ReasoningCompleted，不向 Agent 或前端暴露 SSE 类型。没有事件接收方也仍能使用完整模型／工具循环；丢弃调用 future 会关闭连接，但当前 UI 尚未提供主动取消入口。

Responses 的 `response.output_text.delta`、`response.reasoning_text.delta` 转为公共增量；`response.completed` 中的完整 output 保留推理续接字段，作为写入 Conversation、usage 与工具执行的依据。工具 arguments 的中间增量不会被执行；即使收到 output_item.done，也必须等整个响应完成。当前不显示推理 summary 增量。TUI 已接入 reasoning_text，使用浅灰色 Thought 区域展示；console 实时输出 reasoning_text。TUI 仅展示实际返回的文本，不为 encrypted-only 响应生成推理正文；若已收到文本增量而最终 content 为空，保留可见片段。

推理项的 `response.output_item.done` 转为 ReasoningCompleted，通过 Agent 立即通知 TUI 折叠为一行 `▸ Thought`，不等整轮 response.completed；该事件本身不提交上下文或触发工具。缺少 item 结束事件或使用 JSON fallback 时，最终 Reasoning 文本事件作为结束依据。完整原文保留，点击 Thought 或选中记录后 Enter 可展开／收起，y 复制原文；布局缓存包含展开状态。正在回看、查看日志、选中当前推理或选择文字时不自动折叠，手动切换后也不再自动改变该条记录。重复完成和最终文本校准不会重置展开状态。未收到结束事件的断流推理保留展开。展开后的入口为 `▾ Thought`，按实际可见位置处理点击。松开鼠标时才切换，拖动继续用于选择复制。点击展开前固定阅读锚点，长推理从开头展示，输入草稿与焦点不变。

解析使用 reqwest 的字节流和 eventsource-stream，不假定网络 chunk 对齐字符、行或事件。支持 UTF-8 跨 chunk、CRLF、多行 data、注释 heartbeat；未知合法事件忽略。EOF／[DONE] 出现在 response.completed 之前、非法 JSON、流内 error／response.failed 和 response.incomplete 都结束本轮并报告失败。若代理忽略 stream 并返回 application/json，则接收完整响应；失败或 incomplete 状态不能作为成功提交。

发送前的转换与 HTTP 重试规则沿用同一实现。成功 HTTP 响应开始消费后不自动重试；Agent 仅在没有显示增量且明确 context_length_exceeded 时，沿用一次 compact-and-retry。已出现文字或推理增量的 overflow 也直接失败，以免重播已显示内容。失败时可见的部分文字留在前端，未完成响应不进入模型上下文、不执行其中的工具。

AgentEvent 的 ResponseTextDelta／ReasoningDelta 是显示进度，ResponseText／Reasoning 是带 item_id 的最终文本。TUI 按 item_id 更新同一记录并使布局缓存失效，最终文本替换该记录，不重复追加。显示记录关联保留到每个响应的 usage 或每轮失败／完成，保证提前收到 ReasoningCompleted 后，最终 Reasoning 文本仍更新同一记录，且下一响应复用 item_id 不会修改旧记录。回看时保持阅读锚点，增量只标记新输出；前端每次最多合并 128 个排队事件再重绘。console 每段立即 flush，并在最终文本到达时只补未显示的后缀。

格式依据：[OpenAI Responses streaming events](https://developers.openai.com/api/reference/resources/responses/streaming-events)、[Streaming API responses](https://developers.openai.com/api/docs/guides/streaming-responses)。SSE 字节解析使用 [eventsource-stream](https://github.com/jpopesculian/eventsource-stream)，避免另写一套 framing 实现。网关行为通过当前 Magpie 实测单独核对，不能由官方协议文档推定。

## 推理与原生扩展

`ProviderData { api, value }` 是指定 API 拥有的不可变续接数据。Responses 推理结果保留原生 content、summary、encrypted_content 和未知字段，转换成下一次输入时去除既有不回传的 id／status。存在 provider_data 时，以该原生数据作为续接依据，公共 reasoning text 用于展示和解释。

没有原生数据的 reasoning 由适配层生成 `reasoning_text`。来自其他 API 的 opaque 数据不会被 Responses 客户端原样误发，而是在 HTTP 请求前返回转换错误。

适配层可用 ProviderItem 携带不属于公共已知类别的原生结果，但当前 Agent 仍只处理文字、推理和普通 Function 工具调用；本轮不新增 web search 或 custom tool 执行能力。协议拆分也不代表已经支持任意 provider 之间的上下文迁移。

## 日志与 compact

新 journal 的每行包含 `schema_version: 1`，保留 `messages`／`compaction` 及原有 checkpoint 语义，并新增带 task ID 的 `task_started`／`task_finished` 执行边界。消息项写入公共协议表示，图片仍保存完整字节快照，工具 arguments 保留原文，推理续接数据同样持久记录。

此前没有版本字段的 journal 属于旧格式；原无版本文件不自动改写或迁移。现有 Session 恢复入口区分旧格式与版本 1；不能把旧 Responses items 当作新的公共协议直接反序列化。没有 metadata 的旧目录在会话选择器中显示为 unavailable，详见[会话管理](session-management.md)。

compact 仍由 Conversation 处理。摘要 transcript 使用公共历史表示，图片替换成引用后作为真实图像另行附加；模型输入的图片格式转换由客户端完成。上下文估算基于公共请求表示的字节近似，再由上游 usage 校准；它不是 Responses wire body 的精确 token 计数，已有 overflow recovery 继续兜底。

## 验证

2026-10-13 串行全量回归通过：主程序 197 项、成长实验 40 项。新增验证包含 HTTP chunk 按单个 UTF-8 字节拆分、CRLF／多行 data／heartbeat、客户端必须收到首段增量和推理结束后服务端才允许发送 completion 的 oneshot 协调、工具参数分片与完整续接、原生 reasoning／usage、EOF／[DONE]／非法 JSON／failed／incomplete、部分文字之后的 overflow 不 compact／不重放，以及无增量时的流内 overflow 仍 compact 一次。前端验证覆盖布局缓存失效、正文不重复、回看锚点、失败后复用 item_id、推理与回答的独立显示／浅灰样式／原文复制／窄窗口、自动折叠／手动展开／鼠标点击与拖动区分／中途断流保留／重复完成不重置状态，以及 console 即时输出；原有图文与 compact 回归继续通过。

格式检查、全 target 编译检查和主程序构建通过。Magpie 的 `opencode-go/deepseek-v4.1-flash` 真实 console 流式验收成功：一次 read，三次流式模型请求，第一轮给出文件标记及解释，第二轮准确复述标记；推理原生字段保留在工具续接请求中，journal 正文与最终输出逐项相同。隔离的本地转发仅用于记录测试的请求与 SSE，不保存 Authorization，不操作用户当前 TUI。观察到 229 次 stdout 数据到达（不是 token 计数）。证据：`/var/folders/_x/4s504vvn6zv5vz36ktpbwl480000gn/T/saki-stream-probe-oydh10om/`。

### 本轮发现的网关限制

同一网关／模型在另一个普通读文件提示下，首轮响应仅有 message 与 function_call，没有 reasoning item 或 reasoning delta；工具续接却返回 HTTP 400，要求回传 `reasoning_text`。原始 SSE 的 output_item.done 与 response.completed 都没有这段内容，因此不是客户端解析丢失。相同输入的非流式请求、显式 reasoning effort=low／none 均复现该错误。对照证据：`/var/folders/_x/4s504vvn6zv5vz36ktpbwl480000gn/T/saki-stream-probe-mq1qwn4q/` 与 `saki-stream-modes-3x5iimj0/`（同一临时目录父级）。

成功的工具链提示明确要求调用工具前简短思考，响应实际含有 reasoning，随后续接通过。这只是一个成功验收场景，不能视作网关缺陷的修复或可靠的提示词 workaround。本轮不编造缺失推理，不增加自动重试该 400，也不更改用户的模型／网关配置；这项上游限制仍可能影响普通工具任务。

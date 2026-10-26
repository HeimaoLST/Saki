# Model runtime：协议、客户端与模型目录

更新日期：2026-10-26。状态：已实施职责拆分、Responses 流式响应，以及 M1 的 Chat Completions 调用边界；主 Agent 默认流式，compact 与实验保留完整响应接口。参考依据见 [Pi 分层研究](../research/systems/model-runtime.md)。

## 模块与依赖

| 模块 | 职责 |
| --- | --- |
| [protocol/](../../src/protocol/mod.rs) | 公共请求、输入／输出项、文字和图片内容、工具声明、推理、usage 与 opaque continuation 数据 |
| [client/mod.rs](../../src/client/mod.rs) | `ModelClient` 枚举分派：Responses 与 Chat Completions 共用同一个 Agent、工具与 compact 调用入口 |
| [client/config.rs](../../src/client/config.rs) | 组装边界配置：`ModelApi` 解析（SAKI_API）、显式值构造与一次性的环境读取 |
| [client/transport.rs](../../src/client/transport.rs) | 共享 HTTP 客户端、Bearer 认证与受限重试，两协议复用同一实现 |
| [client/responses.rs](../../src/client/responses.rs) | Responses 原生格式转换、错误分类与响应解码 |
| [client/responses/stream.rs](../../src/client/responses/stream.rs) | Responses SSE 消费、增量转换、终止与断流判断 |
| [client/chat.rs](../../src/client/chat.rs) | Chat Completions 请求／JSON／SSE 转换、工具调用聚合与终止语义 |
| [model/catalog.rs](../../src/model/catalog.rs) | models.dev 目录同步、缓存、模型匹配与容量读取；尚未扩展为完整能力数据库 |
| [agent/conversation.rs](../../src/agent/conversation.rs) | 公共协议上下文、usage 校准、compact、版本化历史日志 |
| [tools/tool_registry.rs](../../src/tools/tool_registry.rs) | 工具 handler 注册与执行；使用公共协议的 ToolDefinition，不再定义协议类型 |

客户端不引用 Conversation、工具 registry、TUI 或 console；公共协议不引用这些运行模块。模型目录也不依赖推理客户端。Conversation 按既有设计借用客户端执行 compact，Agent、compact 与自动标题都通过同一个 `ModelClient` 调用；协议选择是简单枚举分派，没有动态 trait registry 或第二套消息结构。

## 协议选择与配置

`SAKI_API` 在组装边界（`app.rs`）读取一次：`openai-responses`（默认）或 `openai-chat-completions`；未知值在发任何请求前返回明确错误。`API_KEY`、`SAKI_BASE_URL`、`SAKI_MODEL` 保持既有含义；base URL 是 API 基础地址，各适配器追加自己的路径（Responses 为 `/responses`，Chat 为 `/chat/completions`）。`ClientConfig` 可由显式值构造（api/base/key），非 CLI 宿主不必改环境变量。

`ModelClient` 提供 `respond`、`respond_stream` 与 `respond_once`；compact 与成长实验继续使用单次 HTTP 语义的 `respond_once`，不退化到可重试的 `respond`。

## 调用契约

`ModelRequest::new(model, input, tools)` 接收 `&[InputItem]`，不再接收 `&Conversation`。Conversation 通过只读 `items()` 提供当前活动输入，调用方仍负责把响应结果加入上下文。

公共协议使用 `ToolCall`／`ToolResult`、`Text`／`Image { url }`；Responses 适配转换为 `function_call`／`function_call_output`、`input_text`／`input_image`。工具声明的 JSON Schema 保持原样；原始 arguments 字符串不在客户端解析，继续由 Agent 处理参数错误并返回工具结果。

`ResponsesClient::request_body` 返回非流式 HTTP JSON body，`respond_stream` 使用相同内容并添加 `stream: true`；公共请求的 Serialize 表示不等同于服务端 body。成长实验继续用该适配入口记录实际请求，响应 output 改为记录公共结果表示。

默认端点、`.env` 加载和环境变量配置沿用原有行为；客户端按实际协议由 DeepseekClient 更名为 ResponsesClient。`respond` 仍对传输失败、408／429／5xx 最多尝试三次；context overflow 与其他请求错误立即返回，成功 HTTP 响应的解析失败不重试。

实现事实（#160）：两协议共用的 HTTP transport 在发布非成功 HTTP 错误前，将正文中当前连接 key 的原文精确替换为 `[redacted]`；stdio 错误、stderr 和持久历史因而沿同一错误出口取得处理后的正文。HTTP 状态和其余诊断内容保留，overflow 仍先依据原始结构化错误分类，重试次数及条件不变。范围只涵盖该已知 key 在 HTTP 错误正文中的原文，不代表对任意内容或变换形式的通用敏感信息过滤。

请求格式转换在发送前完成，失败不重试。`respond_once` 最多发出一次 HTTP 请求，转换失败返回 `attempts=0`；发送后无论成功或失败都返回 `attempts=1`。Responses 的 context overflow 由结构化 `error.code == "context_length_exceeded"` 判定；错误文本中的 "context length" 字样不参与分类。

## Chat Completions（M1，实现事实）

首版只承诺文字与 function tools，与 Responses 共用 `ModelRequest`／`ModelResponse`／`ModelStreamEvent`、Agent 循环、工具执行器与 compact；不新增第二套消息结构或执行循环。

- 请求：公共 `InputItem` 转为 messages；连续 `ToolCall` 聚合为一条 assistant 的 `tool_calls`，`ToolResult` 转为 `tool` 消息并携带 `tool_call_id`；工具声明为 `{"type":"function","function":{…}}`。`max_output_tokens`→`max_tokens`，`temperature` 原样传递。`MessageRole::Developer` 保留原生 `developer` 角色，不做供应商降级。
- 流式：`stream:true` 且请求 `stream_options.include_usage:true`；按 index 聚合跨 chunk／交错的工具参数分片，响应完成后才把完整调用交给 Agent；usage 可缺失并保持未知，不编造 0；支持空 choices 的 usage 尾块与网关直接返回 JSON 的 fallback。
- 终止：只有 `stop` 与 `tool_calls` 视为完成（JSON 与 SSE 一致）；`length`、`content_filter`、未知原因或缺少 finish_reason 都在客户端失败，不把截断文本或其中的工具调用交给 Agent，已显示的文本不自动重播。[DONE] 单独出现不构成完成。
- 推理续接（#121 已通过 PR122 集成到 dev；#177 空值修复见下节）：JSON 与 SSE 的 `reasoning_content` 原样聚合并通过现有 Reasoning/ProviderData 写入上下文；下一轮与同一 assistant 的文本和完整工具调用重组，空字符串与缺失区分。无工具调用的历史 assistant 也保留该字段；不伪造供应商未返回的推理。增量使用既有 ReasoningDelta，最终响应仍是写入历史的依据。
- 不可表示的数据：图片输入、其他 API 的 reasoning／ProviderItem continuation 在发请求前返回明确转换错误。Chat 的 `reasoning_effort` 仍明确拒绝；本次不开放任意厂商参数。

### DeepSeek thinking 工具续接（#121 / #177）

2026-10-22 Magpie 网关真实验收发现旧适配器丢弃 `reasoning_content`，read 已完成但第二轮返回 HTTP 400。PR122 已集成的修复复用已有带 API 身份的续接数据，不新增执行循环、存储 schema 或隐式模型降级。依据 [DeepSeek 官方 thinking 契约](https://api-docs.deepseek.com/guides/thinking_mode/)，携带 tools 时须保留此前各轮 assistant 的 reasoning_content。JSON/SSE、文本与工具同条消息、空推理、无工具的历史轮及序列化恢复有确定性覆盖；实际网关闭环证据随 PR 记录，不将其他网关或模型推定为通过。

**#177 实现事实（本次候选，尚未集成）**：Chat JSON/SSE 解码区分字段不存在、原生 `null`、空字符串与非空字符串。原生空值仍通过同一 Reasoning/ProviderData 路径持久化，下一请求原样回传；真正不存在的字段不补 `null` 或推理文本。SSE 的 null 片段不会覆盖此前已聚合的字符串。只有实际文本产生可见推理事件，空续接标记仍保留在 journal；既有 journal 不需要迁移，历史上已经丢失的原生数据无法回填。assistant 文本和随后的完整工具调用按同一轮重组，即使原生 reasoning 字段不存在也不拆成两条 assistant 消息。工具仍只在完整响应通过终止检查后执行，不自动重试该 HTTP400。

**2026-10-26 作者实测**：#177 原 GUI 样本（Runtime722de4b，Magpie `deepseek/deepseek-flash` / Chat / `127.0.0.1:3425`）首 task read 后400；保留的 journal 无 Reasoning，但原始 wire 未捕获。Runtime 独立隔离复现沿相同初次提示与命名 caller context，固定 helper SHA256 `c738c63374c0222b8e4397a15e1fb4109c8ff6bf34b89b8219cdecd7e0fa5516`：首 SSE 200中11个原生 `reasoning_content:null`，其余片段无该字段；模型此次选择两个 Bash 调用。续接丢失原生空值，并把 assistant 文本/两个工具调用拆开，随后400。本次 wire 证明 Runtime 的 null 丢失边界，与原 GUI read 任务不是同一个 task；不将旧样本未捕获的 wire 写成已知。证据位于 `/private/tmp/saki-reasoning-177/live-baseline-wire/`，限本机研究日志，不含 Authorization；不由此推定其他网关/模型兼容或原 GUI 已恢复。


**修复候选作者实测（待 GUI 组合复验）**：在 QA323d996 组合的 Runtime 上只应用本单生产修复，保留已消费的 #160 错误凭据脱敏与 #146/#167 Bash 回收修复；helper SHA256 `79eef3082bac2a04abbaa610a6a3d679ef772991999ef99f4d96266828edf065`。一次有界 DeepSeek Flash / Chat 运行，工具选择显式限定内置 `read`：首 task 读取 probeA，release/load 同一会话后第二 task 读取 probeB并准确报告两个标记，均 completed，两个 read 各执行一次，4次 HTTP 全200。首工具响应原生 null 在续接与恢复后仍存在，无工具的最终 assistant 文本 reasoning 也保留，null 尾块未抹掉文本。relay 只缓冲 SSE 以保存线协议，无请求/响应重写；因此不把它当作 GUI 自动补充/native 或网络增量时序验收。日志在 `/private/tmp/saki-reasoning-177/live-fixed/`；原 GUI 的两条 pending 补充与完整候选消费需由 GUI 主责另验，旧失败会话不回填、不重跑。

## 流式响应契约

`respond_stream(request, on_event)` 是异步调用：收到增量后同步调用轻量回调，成功时返回完整 ModelResponse。公共协议的 ModelStreamEvent 提供 TextDelta／ReasoningDelta／ReasoningCompleted，不向 Agent 或前端暴露 SSE 类型。没有事件接收方也仍能使用完整模型／工具循环；丢弃调用 future 会关闭连接，主动取消沿用既有的 SessionCommand::Cancel／TUI Esc 路径。

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

## M1 验证（2026-10-16）

实现事实：离线行为测试覆盖 Chat 请求映射、图片与 Responses continuation 的请求前拒绝、JSON／SSE 文本与工具参数分片、交错与 usage 尾块、缺失 usage、终止语义（`length`／`content_filter`／缺失 finish_reason／仅 [DONE] 均失败）、结构化 overflow 判定、Agent 层 Chat 工具闭环，以及 Chat compact 的单次请求契约（[client/chat tests](../../tests/client/chat.rs)）；session 侧覆盖跨 API 打开与 torn tail 不修改历史（[session tests](../../tests/session/mod.rs)）。现有 Responses 全量回归继续通过。

作者报告（监督者验收，2026-10-16，按 SHA 区分）：

- `a19213a`：双协议（Responses 默认 + Chat）普通本地 console 编译二进制闭环通过。
- `50131b7`：增强版本地验收（跨 API + torn tail）通过；报告 `/private/tmp/saki-47-acceptance-50131b7.json`，二进制 SHA-256 `00a8e76aea18007140160b1ba4502911ad70a4aabbef1f4074156f09796ec968`。
- `043b6c8`：两个真实 smoke——`opencode-go/gpt-6-luna` Chat 工具闭环成功（一次 `read`、回传、最终标记正确、completed）；DeepSeek thinking Chat 第二轮失败（边界见上节）。

除已验收组合外不宣称支持其他兼容厂商。

## 同连接会话模型配置（#105）

更新日期：2026-10-23。分类：**实现事实，已由 PR120 集成到 dev**。模型切换使用 Runtime 的 `session.config.get/update`，与 typed RuntimeHandle 为同一准备/提交路径；请求形状、错误、revision 和持久化边界见[Runtime 配置契约](runtime-api.md#105-同连接配置候选实现事实)。没有另一个独立 select-model 状态机。

实际 endpoint 模型 ID 与 catalog_id 分开：后者只用于已加载容量查找。自定义模型可提供有效完整 CompactionConfig，配置期间不刷新 catalog、不发送探测推理；上游是否实际可用仍由真实任务确认。模型/预算原子保存到 schema 1 metadata，接纳任务记录当时配置快照；只读恢复不重跑历史工具。旧记录缺少快照保持未知。

空闲目标可切换，已接纳目标返回 busy；另一个 session 运行不把本目标误判 busy。同版本竞争返回 config_conflict，有效值未改变不增加 revision。成功应用清除旧 usage 校准，后续正常 usage 会重新建立 baseline。降低预算沿用唯一 compact，summary/generation 都用新模型；失败保留上下文，不偷偷更换模型。

含不透明 ProviderItem/provider_data 时首版拒绝改变模型，返回 continuation_not_supported 并保持文件和内存；同 API 不被当作原生续接兼容证明。跨连接、prompt/tool 更新和 schema 2 是 #106，GUI/TUI 选择控件不在此 Runtime PR。

# Model runtime：协议、客户端与模型目录

更新日期：2026-10-16。状态：已实施职责拆分、Responses 流式响应，以及 M1 的 Chat Completions 调用边界；主 Agent 默认流式，compact 与实验保留完整响应接口。参考依据见 [Pi 分层研究](../research/systems/model-runtime.md)。

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

请求格式转换在发送前完成，失败不重试。`respond_once` 最多发出一次 HTTP 请求，转换失败返回 `attempts=0`；发送后无论成功或失败都返回 `attempts=1`。Responses 的 context overflow 由结构化 `error.code == "context_length_exceeded"` 判定；错误文本中的 "context length" 字样不参与分类。

## Chat Completions（M1，实现事实）

首版只承诺文字与 function tools，与 Responses 共用 `ModelRequest`／`ModelResponse`／`ModelStreamEvent`、Agent 循环、工具执行器与 compact；不新增第二套消息结构或执行循环。

- 请求：公共 `InputItem` 转为 messages；连续 `ToolCall` 聚合为一条 assistant 的 `tool_calls`，`ToolResult` 转为 `tool` 消息并携带 `tool_call_id`；工具声明为 `{"type":"function","function":{…}}`。`max_output_tokens`→`max_tokens`，`temperature` 原样传递。`MessageRole::Developer` 保留原生 `developer` 角色，不做供应商降级。
- 流式：`stream:true` 且请求 `stream_options.include_usage:true`；按 index 聚合跨 chunk／交错的工具参数分片，响应完成后才把完整调用交给 Agent；usage 可缺失并保持未知，不编造 0；支持空 choices 的 usage 尾块与网关直接返回 JSON 的 fallback。
- 终止：只有 `stop` 与 `tool_calls` 视为完成（JSON 与 SSE 一致）；`length`、`content_filter`、未知原因或缺少 finish_reason 都在客户端失败，不把截断文本或其中的工具调用交给 Agent，已显示的文本不自动重播。[DONE] 单独出现不构成完成。
- 不可表示的数据：图片输入、Responses 的 reasoning／ProviderItem continuation 在发请求前返回明确转换错误；厂商专用 reasoning 扩展不在本轮。

### 已知未支持组合（失败边界，已观察）

DeepSeek thinking 模型经 Chat Completions 的第二轮请求要求回传 `reasoning_content`。本版本不发送也不扩展该字段，因此该模型的多轮工具续接会失败（首轮可正常返回）。这是已运行观察到的真实边界，归类为作者报告／已观察的未支持组合，不是修复或兼容层；如后续要支持，需另行实现并验证，是否开 RFC 取决于是否改变公共契约。

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

## 会话内模型选择（#32，设计提案）

更新日期：2026-10-18。**未批准、未实施。** 本节落实 [#32](https://github.com/HeimaoLST/Saki/issues/32) 的候选与验收，不将已有启动配置误写为运行期切换。跨宿主边界见 [#43 / PR #59](https://github.com/HeimaoLST/Saki/pull/59)，排队冲突见 [#31 / PR #65](https://github.com/HeimaoLST/Saki/pull/65)。

### 推荐范围

推荐原候选 B：使用宿主已加载的配置／模型容量信息，在目标 session 空闲时切换。候选 A 的运行期网络刷新、跨 endpoint/client 重建适合完整 provider 配置成熟后的阶段；候选 C 保留 env 但无法完成应用内选择。

首版保持当前宿主的 API、endpoint、凭据和工具不变；`model` 是实际发给该 endpoint 的模型标识，`catalog_id` 只是容量查找标识，二者不能互相误用。相同 API/endpoint 下的模型选择复用已有 ModelClient，不为改一个请求字段重新建立 transport；跨 API／endpoint／凭据切换不是本契约的隐含副作用。原 `SAKI_MODEL` 等 env 继续作为新 session 的缺省配置，不被某个 session 的选择操作全局改写。

### 调用与容量

建议增加 `SessionCommand::SelectModel { session_id, ... }` 的等价 typed 操作，以及机器入口 `session.select_model`。调用方明确目标，不直接修改 Agent/client，也不通过伪造 Opened 事件重新打开历史。操作只针对已加载、无 active/pending task 的 session；另一个 session 正在执行不自动使本 session busy。TUI/console 新事件接线须由所属业务线确认并交付。

```json
{"version":1,"id":"model1","method":"session.select_model","params":{"session_id":"s1","model":"target-model","catalog_id":"provider/target-model"}}
{"version":1,"type":"response","id":"model1","result":{"session_id":"s1","model":"target-model","api":"openai-chat-completions","effective_from":"next_task"}}
```

模型容量由已加载 catalog 的唯一匹配提供；自定义模型可由调用方提供完整 `compaction`（沿用 CompactionConfig 字段）替代 catalog 查找。一个请求只选一种容量来源。选择操作不调用 `ModelCatalog::load`、不请求 `/models`、不发测试推理或 compact。没有唯一容量或默认 compact 设置在目标容量下不合法时，明确返回 `model_limits_required`／`invalid_model_limits`，不静默猜 256K；调用方可以补充有效的显式配置。

context/input/output 的目标限制与 reserve、summary、keep-recent 必须一起核对，复用当前 CompactionConfig 的单一验证规则。已有上下文超出新预算不靠删除历史解决；下一次 Send 仍走同一个 compact-before-generation 流程，compact 失败保持上下文并报告真实任务失败，不自动退回旧模型或另起第二套摘要。

接受模型配置只证明本地路由／容量合法，不证明 endpoint 已开通该模型或模型支持当前图片／工具能力。首次实际调用的上游错误仍按普通模型失败报告，不通过偷偷降级、移除图片或重试别的模型制造成功。

### 原子生效、历史与原生 continuation

执行顺序建议是一次目标 session 的串行控制操作：

1. 核对 session/active/pending 状态、容量来源、有效 compact 设置，以及当前上下文是否可保留。
2. 准备新的 metadata/model/compact 值；尚不改变当前 Agent、usage 校准或事件。
3. 使用现有 metadata 临时文件＋rename 路径持久化；失败返回错误，旧模型／预算／上下文和 metadata 保持。
4. 在无 await、没有可失败的后置转换的提交段内同步活动 Agent/model/compact，清除旧 usage 校准，再发布真实 ModelChanged/context 信息及成功响应。

同一验证规则只保留一份；必要时从现有 consuming `with_compaction_config` 提取验证/应用边界，避免先移走 Conversation 再因错误丢失旧状态。选择过程中不发模型／工具请求，不重写 `history.jsonl`，不释放/重新取得 session 写锁。重启在持久化提交后恢复新 model/compact，提交前恢复旧值；切换其他会话不串改配置。

当前 ProviderData 只标记 API，没有证明不同模型可接受原生 continuation。首版对包含 `ProviderItem` 或携带 `provider_data` 的 reasoning 上下文，不静默删除／转成文字，也不假定“同 API”足以迁移；改变模型时返回明确 `continuation_not_supported` 并保持原状态。没有这类数据的公共文字／普通 function tool 历史可继续使用。该限制是首版选定的行为边界，不是声称所有厂商都禁止同 API 迁移；后续确需支持时另验证目标模型/厂商的迁移契约。选择原模型且有效配置未变化可作为空操作保留原数据。

未来新 task 的 journal task-start 可以增加可选 model/api 字段，记录实际执行配置；旧记录缺失时保持未知，不以当前 metadata 倒填旧任务的模型。复用现有 record 类型的可选字段，不能为了模型选择新增旧 reader 无法识别的 record 而宣称完全可回滚。task/message 关联与 #60 保持同一来源。

自动标题任务继续使用派发时捕获的旧模型/连接，其结果只更新标题，不改新选择或其 usage。运行/queued 任务未结束时不切换，避免已接纳输入悄悄使用新配置；完整 profile 编辑和跨 provider 迁移另行定稿。

### 验收与交付切分

此 decision 定稿后另建 runtime 实施 issue，范围为 `src/session/` 控制/metadata、`src/agent/` 配置应用、`src/model/` 已加载容量、必要 app 接线和对应模块测试；共享 HTTP/Agent/compact 不复制。TUI picker/command 与 GUI 模型设置归各自业务线，不由 runtime PR 越界实现或声称界面已可切换。

确定性验收至少包括：

- A→B→A 的实际请求 model 与当前 metadata/context/预算一致；恢复后继续 B，原历史内容与工具调用次数不变；选择本身零 HTTP/工具请求。
- active 或 pending 时拒绝、未知/歧义容量拒绝、无效 compact 拒绝、metadata 写入失败：原 Agent/metadata/usage/上下文保持；目标 session 错误不影响另一个 session。
- 小预算触发下一任务现有 compact 路径；summary/generation 都用 B，单次 compact 契约不退化；失败不丢上下文、不自动 fallback。
- 有 opaque continuation 时拒绝并保留字节/状态；普通 tool 历史不重跑。旧 task 缺少模型字段保持未知，新 task 记录实际模型，不生成伪历史。
- 成功后的 context estimate 不复用 A 的 usage 校准；之后 B 的真实 usage 可重新校准。标题作业的迟到结果不改变 B。
- 默认启动/旧 session/取消/队列冲突及两协议回归；最终实现 SHA 的 fmt、strict Clippy、tests/example 和 required CI；上游真实可用性只按实际验证的模型组合声明。

需要 runtime 对持久化/配置提交边界、TUI/GUI 对状态/可发现性和不可迁移反馈、PM 对首版同连接与后续扩展范围的实际 ACK。独立评审和代码验收分别记录；本文合入不等于模型选择已实现，breaking migration 不从现有开发授权推导。

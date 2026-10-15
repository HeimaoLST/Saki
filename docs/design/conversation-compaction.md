# Conversation 自动压缩

更新日期：2026-10-13。状态：已实现自动路径；command 系统不在本次范围内。

## 职责与调用时机

[Conversation](../../src/agent/conversation.rs) 负责预算判断、合法切分、摘要请求、检查、checkpoint 和工作上下文替换。客户端与模型名称通过参数借用，不保存在 Conversation 中。[Agent](../../src/agent/mod.rs) 在每次正常模型请求前调用 `compact_if_needed`，包括新用户输入加入之后，以及一批工具调用全部完成、结果加入之后。

```text
追加用户输入／完成整批工具结果
→ compact_if_needed
→ 不到阈值：直接请求模型
→ 到阈值：完整前缀 → 一次摘要请求 → 校验 → 保存 checkpoint → 替换上下文
→ 继续同一次任务的模型请求
```

原 system 消息保留。较早前缀被带标记的 assistant 摘要替换，近期部分保留原始协议项。切点只选用户消息边界，最新用户消息与其后的全部 reasoning、调用及结果完整保留；未完成调用、孤立结果拒绝压缩。此前允许在同一用户任务的工具批次之间切分，真实网关验证发现会破坏 continuation 协议，现已移除这条切分路径。代价是单个任务的完整工具链自身过大时不能通过压缩旧前缀解决。

这是工作上下文管理，不生成 Belief，不调用成长 Reflection，也不把摘要算作新证据。

## 模型容量和预算

启动时，[ModelCatalog](../../src/model/catalog.rs) 读取 models.dev 的 [combined catalog](https://models.dev/catalog.json)，将 provider 数据与统一模型数据缓存在 `.saki/cache/models.json`。缓存使用 24 小时 TTL；每次启动检查，未过期不联网，过期才同步。没有后台定时器，当前运行中的会话预算也不会因为目录刷新而改变。

匹配顺序按用户最新决定：精确匹配完整 provider/model，或 canonical lab/model ID；其次，去掉 provider 后匹配统一 model ID／名称。模型名不会模糊匹配到其他版本；同名有多个候选时视为未匹配，避免依赖 HashMap 顺序。默认 DeepSeek 模型和 `opencode-go/deepseek-v4-flash` 均可匹配。代理有自定义别名时，可以用 `SAKI_MODEL_ID` 单独指定目录查找 ID，发送 API 的 `SAKI_MODEL` 不变。

都匹配不到时使用 **256K = 262,144 tokens**，并在启动时提示使用了默认值。同步失败使用已有有效缓存；没有可用缓存时也回退到 256K。新的 JSON 完整下载、解析后才替换缓存，失败不覆盖旧文件。公开目录请求不携带模型 API bearer。显式 `SAKI_CONTEXT_TOKENS` 优先，跳过目录加载；默认容量是应用假设，不是对未知入口的硬上限证明。

models.dev 的 `limit.context` 提供总窗口；若 `limit.input` 存在，输入预算取 `min(context - 输出预留, limit.input)`。已知 `limit.output` 会约束默认输出预留及默认摘要输出。容量初始化不再依赖每个兼容 API 的 `/models` 返回扩展字段。

2026-10-05 实测 `http://127.0.0.1:3425/v1/models` 对 `opencode-go/deepseek-v4-flash` 返回：

```json
{"context_window":1000000,"context_length":1000000,"max_input_tokens":1000000,"max_output_tokens":384000}
```

这是前期核对的入口元数据；当前初始化改用 models.dev，同一 provider/model 条目的 `limit.context=1,000,000`、`limit.output=384,000` 与入口一致。后续网关实验已完成实际 input=979,997 的请求；超长请求的错误正文报告硬上限 1,048,576，与目录的百万声明有差异，应用预算仍保持 1,000,000。没有定位精确硬边界，详见[实测报告](../experiments/compaction-gateway-report.md)。DeepSeek 的 [V4 发布说明](https://api-docs.deepseek.com/news/news260424/) 也公布 1M 上下文能力。[当前官方调用说明](https://api-docs.deepseek.com/guides/codex) 说明其官方入口的旧 Flash 名称转到 V4.1；这一别名规则不能直接推断本地 OpenCode Go 路由实际使用的权重版本。

| 配置 | 默认值 | 含义 |
| --- | --- | --- |
| `SAKI_CONTEXT_TOKENS` | 目录匹配；失败默认 262,144 | 显式覆盖优先；总工作预算不再猜 32K |
| `SAKI_OUTPUT_TOKENS` | 8,192 | 正常请求的输出上限，同时留出相同空间 |
| `SAKI_COMPACT_KEEP_TOKENS` | 8,192 | 近期原文保留目标；完整交互可能超过目标 |
| `SAKI_COMPACT_SUMMARY_TOKENS` | 4,096 | 摘要请求的最大输出，不是摘要正文保证长度 |

自动触发条件：`当前输入用量 >= min(总工作预算 - 输出预留, 已知输入上限)`，没有独立输入上限时只做减法。上述入口按默认配置的触发线为 **991,808 tokens**。这些保留量尚未通过质量／成本实验优化；没有引入额外百分比触发规则。

每次压缩只有一次摘要请求，没有摘要重试。摘要输入是较早前缀、前次摘要与总结指令，不包含工具定义；其大小由实际前缀决定，不是固定 4K。调用开销为摘要输入与输出的实际 usage。摘要请求的输入估算加最大输出需在工作预算内，否则返回错误；第一版不静默删掉较早材料来重试。

## 用量计数：usage 优先，无 tokenizer

正常响应追加完毕后，`record_usage` 以 `usage.input_tokens + usage.output_tokens` 作为当前上下文的基线。`input_tokens` 中已有的 cached tokens 不重复相加。之后新增用户输入、工具结果或工具定义的大小，用序列化请求字节数变化估算；不是把整段旧历史再次当成未经校准的估算。

首次请求、没有有效 usage、切换模型，以及压缩后的第一次正常请求，没有适用基线时使用整个请求的 `JSON bytes / 3` 向上取整。它包含 system、消息和工具定义。估算不是精确计数，不提供严格防溢出保证；这里只对已知 usage 与尚未经过模型的新内容作区分，没有使用 tokenizer。

压缩成功清除旧 usage 基线。摘要响应 usage 记录压缩成本，不能当作替换后工作上下文的用量。下一次正常响应建立新的基线。

## API 契约与失败行为

`Conversation::new` 创建会话，不自动启用压缩；调用方通过 `with_compaction_config(CompactionConfig::new(capacity))` 启用。主程序已完成配置接入，独立成长实验继续使用原来的新建会话路径。配置须满足总预算大于输出预留，摘要输出在预留内，近期保留量与摘要需为 system／tools 留出空间。

`compact_if_needed(&client, model, &tools)` 返回 `Ok(None)` 表示未启用或无需压缩，`Ok(Some(result))` 表示已替换。`compact(&client, model, &tools)` 是强制入口，绕过触发阈值，但仍要求启用配置、存在可压缩前缀、完整合法的 tail，以及替换后低于输入上限；自动入口调用同一个强制实现，不维护第二套摘要逻辑。返回错误时工作消息不被替换。

替换前／后的缩减校验使用同一种 JSON 字节估算，不将可能低估的旧 usage 基线当作替换后估算的比较基准。`result` 的 before 为原计数、after 为替换后估算，另携带摘要请求 usage；强制恢复时 after 未必小于低估的 before，但序列化请求必须确实变小。2026-10-12 分层后，Conversation 透明序列化为公共协议项数组，Responses 客户端另行转换模型输入；配置和日志字段不进入模型输入。估算采用公共请求表示，使用上游 usage 校准，仍非精确 wire token 计数。

HTTP／解析失败、incomplete 响应或摘要消息、空摘要、意外工具调用、摘要请求过大，以及替换后仍超过预算或未减小，均拒绝应用。单条巨大用户消息、必须整组保留的巨大工具交互或巨大工具定义，未必能通过摘要解决；此时明确返回错误，不继续发送预算不足的正常请求。

百万窗口实测发现的原样超限重试与工具 tail 边界问题，已在后续实现中修复。原始失败与修复后的验证分别记录在[实测报告](../experiments/compaction-gateway-report.md)。

## 上游超限恢复

Client 保留结构化 `ApiError`，当前明确支持非成功 HTTP 响应中的 `error.code=context_length_exceeded`。这类错误直接交还 Agent；其他请求错误也不重复发送。普通发送错误、HTTP 408／429／5xx 最多尝试三次；成功 HTTP 的响应解析错误不重试。当前没有增加其他 provider 的文本分类规则，也未新增退避／Retry-After 策略。

正常请求收到超限后，Agent 调用 Conversation 强制 compact，再以当前投影重建请求并重试一次。恢复标记作用于连续失败的当前模型 step：第二次超限直接结束当前轮；成功模型响应后，下一工具 continuation 可以有自己的一次恢复机会。摘要自身失败直接返回，不递归 compact、不尝试重发摘要。失败的模型请求没有新增 assistant 消息，因此没有需要移除的错误消息投影。

恢复只重试失败的模型请求，已经执行的工具及其结果仍保留，不重新执行工具。最新用户消息和整个当前工具链原样保留。

`Agent::handle_user_message(message)` 处理一条用户消息并返回 `Result`；终端 `run()` 捕获单轮错误、打印失败信息并继续接受输入。错误不再导致整个终端会话退出；EOF 正常返回成功，因此自动验收不能只看进程 exit code，还需要区分单轮失败。无旧历史可压缩、最新整轮自身过大或摘要仍超限时，保留原始日志并报告需要减少输入／工具输出，不静默丢弃材料。现已通过 Conversation 的恢复 API 和 SessionManager 支持重启后继续原会话，具体中断语义见[会话管理](session-management.md)。

## 原始历史与 checkpoint

主程序为新会话创建 `.saki/sessions/<timestamp>-<pid>/history.jsonl`，默认启动也可恢复已有会话；会话另有 metadata.json，详见[会话管理](session-management.md)。历史目录，已加入 gitignore。`with_history_file` 创建新文件，不能覆盖旧文件，须在首次压缩前配置。追加方法仍不执行 I/O，由 `save_history` 在输入／模型输出／工具批次边界写入；压缩也先保存当前原始记录。

2026-10-12 起，新日志每行带 `schema_version: 1`，消息采用公共协议而非 Responses 原生 input 格式；旧文件保持原样。类型、原生续接数据和未来恢复边界见[模型调用分层](model-runtime.md#日志与-compact)。

JSONL 的模型上下文包含两类记录：`messages` 保存新增原始 input items；`compaction` 保存摘要、相对于此前工作数组的 `keep_from`、before／after 和摘要 usage。重放时追加 messages，遇到 checkpoint 则使用原 system、新摘要与此前数组 `keep_from..` 的 tail。保留较早原文，不为每个 checkpoint 重复写整份 tail。

checkpoint 写入成功后才切换内存上下文。写入失败不安装摘要。Conversation 现有生产恢复 API，可重放多次 checkpoint 并继续追加；SessionManager 负责启动恢复和会话切换。执行边界另以 task_started／task_finished 记录，恢复不自动重跑工具。未配置历史文件的独立调用方，压缩后无法从 Conversation 重新展开已移出的原文。

## 验证

[压缩测试](../../tests/conversation/mod.rs) 使用本地 mock HTTP，覆盖容量元数据、触发与不触发、工具配对、失败／截断、usage、不可压缩内容、连续 checkpoint 重放及写入失败。运行命令：

```sh
cargo test --locked --bin Saki
cargo test --locked --example growth_experiment
cargo build --locked
```

当前 12 项压缩测试、[6 项模型目录测试](../../tests/models/mod.rs)、[6 项 Agent 恢复测试](../../tests/agent/mod.rs)和[5 项 Client 错误测试](../../tests/client/mod.rs)通过，主程序共 29 项。成长实验运行复用 12 项压缩与 5 项 Client 测试，加原有 12 项，共 29 项；没有重新执行付费成长 pilot。恢复测试覆盖低于本地阈值的超限、再次超限停止、下一任务可继续、摘要不递归、日志保留、工具不重跑与成功模型响应后的恢复标记重置；另验证低估 usage 时仍可强制安装实际缩小的上下文。

真实入口验收已通过：本地代理与 `opencode-go/deepseek-v4-flash`，测试临时使用 8,192 总预算、2,048 输出预留、128 近期目标、1,024 摘要输出上限。两轮正常交互之间自动产生一个 checkpoint，before／after 为 7,202 → 5,944；摘要实际 input=1,590、output=784，其中 reasoning=590。后一轮准确恢复“先诊断 retrieval 再改 storage、保留 raw logs、标签 phoenix-17”。数据表明一次自动压缩与继续回答的完整路径可用，不证明摘要在复杂长期工程任务中总能保留细节。

前期小窗口验收报告：`.saki/verification/auto-compaction-1791195205722915000.json`，其中关联原始 session journal。后续已按真实百万窗口运行[网关实验](../experiments/compaction-gateway-report.md)：初始工具 continuation 失败与巨大工具分支未完成的结果保留；修复后的四个场景均通过，真实 overflow 请求只失败一次，再经摘要和重建请求继续回答，巨大单条材料按预期停止当前任务并保留原文。本次没有新增付费成长 pilot。触发阈值的 harness 对比与尚未实施的 90% 策略建议见[研究文档](../research/systems/context-compaction.md#自动压缩阈值容量不是触发线)。

真实启动验收也已通过：从 models.dev 下载约 5.7 MB 的 combined catalog，命中 OpenCode Go 的 Flash 条目；第二次启动复用缓存且文件时间不变；未知 provider/model 使用 256K 并正常进入 loop。此项使用 EOF 结束主循环，没有新增模型生成请求。

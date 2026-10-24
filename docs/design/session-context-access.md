# Session 上下文访问与独立分析接口提案

更新日期：2026-10-24。

结论分级：**设计提案**。供 Runtime 负责人核对契约、拆分实现和验收。用户本次要求交付设计，由用户转交实施；本文件不表示已实现或已验收。

目标：A 持续执行任务，B 通过工具读取和处理 A 的上下文；需要模型判断时，在独立调用中分析资料，只向 B 返回结果。A 不需要额外回答问题，原始材料不自动进入 B 的主上下文。基础能力通过同一个 Runtime 服务提供，GUI、TUI 和 agent 工具共用实现。

## 1 开发基线与现有进展

2026-10-24 发布同步前，已执行 git fetch origin 并核对远端引用：

| 分支 | 提交 | 与另一分支的关系 |
| --- | --- | --- |
| origin/master | c1217fa1f5cf4993a50e6c525dcfe212cab88c6e | 无独有提交；最近提交日期 2026-10-16 |
| origin/dev | fd58ec2500690d877a0406cdedf92aa2605bf133 | 领先 master 103 个提交，包含合并提交；最近提交日期 2026-10-23 |

随后按用户明确要求，通过 [发布 PR #133](https://github.com/HeimaoLST/Saki/pull/133) 将上述 dev 合入 master。发布提交为 `5af21680de9e8e1988ff6d699869fb8eda888f30`；master 与 `dev@fd58ec2` 的文件树一致，仅多一个发布合并提交。发布 PR 的 [CI40051521434](https://github.com/HeimaoLST/Saki/actions/runs/40051521434) 已全部通过。本提案不在该发布中；下列源码依据仍固定在相同内容的 dev 提交。

**实施从最新 origin/dev 建立任务 worktree，不从本设计草稿所在的旧 detached HEAD 开发。** 旧工作区只保存供用户交接的提案，不更新其中既有 runtime-api.md 草稿。

已核实的实现基础：

| 已有能力 | 依据与复用要求 |
| --- | --- |
| RuntimeHandle 与 stdio 共用服务；history/status/send/cancel | [RuntimeCommand](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/session/runtime.rs#L287)。新接口扩展同一服务，不能让 GUI 解析 JSONL |
| 多 session 并发、每 session 单任务、容量满明确拒绝 | [validate_send](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/session/runtime.rs#L2000)。复用业务任务生命周期 |
| 原始历史与压缩后工作上下文分离 | [Replay](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/agent/journal.rs#L110)。压缩追加 Compaction 记录并重建 context，不覆盖原始 history |
| 唯一模型调用边界与一次请求入口 | [ModelClient](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/client/mod.rs)。复用 respond_once 与现有协议适配器 |
| 外部 MCP 工具、固定工具排序、输出边界 | [MCP 安装](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/tools/mcp/mod.rs#L94)、[工具注册](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/tools/tool_registry.rs)、[输出处理](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/tools/output.rs) |
| GUI 接入与 TUI/console 迁移已有交付 | [服务化收尾 #103](https://github.com/HeimaoLST/Saki/issues/103)。PR110/120/122/123/127/128/129/131 已合入 dev |

该 dev 提交的 [CI39935136134](https://github.com/HeimaoLST/Saki/actions/runs/39935136134) 已核对为 success，rust/gui/python/ci-required 四项成功。#103 的运行样本与已知限制继续有效，本提案不把其测试结果扩展为新功能的验收结果。

相关未实施任务：[历史检索 #62](https://github.com/HeimaoLST/Saki/issues/62) 已提出压缩后检索原文、绑定 session 范围和统一 reader。本提案的 history 读取、搜索及工具入口应承接 #62，不建立第二套历史检索。向 A 追加指令或改变执行方向属于 [#31](https://github.com/HeimaoLST/Saki/issues/31)，不混入只读访问。

## 2 范围与职责

首版交付：上下文资源访问、有限结果的读取与搜索、独立单次模型分析、agent 工具入口、可观察的用量和取消行为。

Runtime 负责数据读取、模型调用、并发准入和生命周期。产品负责联系人映射、监督对象、问题和触发时机。System prompt 描述使用策略，tool definition 描述参数与行为；只读、范围限制、结果边界由代码实现。

本轮不做跨 harness 导入、任意 agent 自由对话、修改来源 session、向量数据库、通用脚本执行沙箱、自动监督调度、GUI 专属协作界面或分布式共享存储。检索结果足够时直接使用；只有需要语义分析时才调用模型。

## 3 三种读取对象

把当前工作上下文与实际请求前缀区分开，避免用“context”掩盖不同语义：

| view | 定义 | 用途与限制 |
| --- | --- | --- |
| history | 截至本次读取边界的原始 journal 语义记录 | 检索压缩前原文；包含任务与压缩记录，不直接当作模型输入 |
| working | 最近完整落盘记录重建出的 Conversation 输入项 | 查看 A 当前已提交的工作上下文；可能包含尚待工具结果的调用，不保证可直接追加用户问题 |
| request | 最近一次生成请求开始时使用的完整结构化输入、工具定义和执行配置 | 独立分析复用前缀；不包括正在流式生成的半截输出，也不包括该请求开始之后的新记录 |

request 只捕获正常生成请求，不把压缩摘要请求误作 A 的工作请求。首次模型请求之前、Runtime 重启或 session 重新加载之后，可能没有 request 视图；返回 context_unavailable，不能伪造已使用过的请求。history/working 仍可读。

这种区分允许 B 在 A 等待工具结果时检索最新 working/history，或明确选择稍早但可重用的 request。API 不偷偷在视图间降级。

A 最后一轮生成完成后，其最终回答可能尚未进入任何后续 request。需要检查这段最新结果时，应读取 working/history 或对对应文本区间分析；不能用较早 request 的分析宣称覆盖了最新回答。资源的 captured_at_ms 与 request_sequence 用于说明这个边界。

### 一次读取的语义

每次 context.open 取得一份完整、只读、有限生命周期的资源。A 继续工作或压缩不会改变已取得的资源；需要更新时重新 open。只保留当前发布值与调用方显式持有的资源，不维护历史快照库，不做压缩前后增量镜像同步。

资源描述返回来源 session、视图、读取时间、已提交记录边界，以及有实际依据的 task/config revision。request 另返回请求序号。不存在的字段返回 null，不能把最新 metadata 猜成旧请求配置。任务运行状态使用现有 status 查询；状态查询与上下文读取不是原子事务，应各自标明观察时点。

## 4 服务接口

沿用现有 version=1、id、method、params 请求封套；新增方法通过 runtime.capabilities.methods 宣告。下面均为新增提案。

| 方法 | 必需参数 | 可选参数 | 返回 |
| --- | --- | --- | --- |
| context.open | session_id, view | 无 | ContextResource 描述，不返回完整内容 |
| context.read | context_ref | offset=0, max_bytes=16384 | 有限文本、实际范围、来源定位、next_offset |
| context.search | context_ref, query | cursor, max_hits=20, max_bytes=16384 | 匹配片段、来源定位、搜索进度与 next_cursor |
| context.release | context_ref | 无 | released；未知或已释放引用也返回 released=false |
| analysis.start | context_ref, mode, question | ranges, max_output_tokens=2048, timeout_ms=120000 | analysis_id、accepted 状态、实际分析来源和模型 |
| analysis.get | analysis_id | 无 | 当前状态；终态时附结果或错误、用量、来源 |
| analysis.cancel | analysis_id | 无 | cancel_requested 或已存在的终态 |

读取未知/失效引用使用 context_unavailable；analysis.get/cancel 查询未知分析使用 not_found。释放是幂等操作，读取失效引用不能隐式重新读取最新 session。

### 获取资源示例

~~~json
{"version":1,"id":"open-1","method":"context.open","params":{"session_id":"session-a","view":"working"}}
~~~

~~~json
{
  "context_ref": "ctx-123",
  "session_id": "session-a",
  "view": "working",
  "captured_at_ms": 1792800000000,
  "journal_record_count": 120,
  "source_task_id": "task-a1",
  "config_revision": 3,
  "request_sequence": null,
  "text_bytes": 3800000,
  "projection_omissions": [],
  "expires_at_ms": 1792800600000
}
~~~

数字和 ID 仅为 DTO 示例。context_ref 是 Runtime 内部资源引用，不是本地文件路径、供应商缓存 ID 或持久 session ID。

### 文本投影与读取

1. 服务保留结构化来源，提供用于阅读和搜索的确定性 UTF-8 文本投影。消息角色、工具名、工具调用/结果关联、压缩摘要及任务记录的类型不能丢失。
2. 图片和无法作为可读文本呈现的 provider 原生数据使用类型说明及来源引用，并在 projection_omissions 标注。不能把图片 base64 或 opaque continuation 当作检索正文；request 的内部复用副本仍保留其原有结构，不经文本投影往返。
3. offset 是该 context_ref 文本投影的 UTF-8 字节位置，必须在字符边界；否则 invalid_request。响应给出实际 start/end，next_offset 直接用于下一次读取。到末尾返回空文本和 next_offset=null；越界返回 invalid_request。
4. max_bytes 限制紧凑 JSON 序列化后的完整 result payload 大小，包含转义与来源元数据；服务在有效字符边界返回可容纳的内容，不静默丢弃头部或中间部分。默认 16 KiB，最大 48 KiB；工具最终封装也须实际计量，不能只假定剩余 2 KiB 足够。调用方请求小于可用最小封套预算时明确报 invalid_request。错误结果同样有界，不能原样回传无界上游错误正文。
5. 每个返回片段附来源定位。history 使用 journal record offset；working 使用本资源的 item index；request 使用本资源的 input item index。来源定位不能跨不同 context_ref 当作同一偏移使用。

### 搜索语义

首版实现大小写敏感的字面量匹配，query 非空且最多 4 KiB；不引入正则、语义向量或隐式模型调用。一次扫描至多 1 MiB 投影正文，最多 100 个命中；片段最多 512 字节并遵守总结果预算。超过扫描/命中/输出预算就返回 next_cursor 与 scanned_to，不能把“尚未扫描完”写成“没有更多匹配”。

cursor 由服务编码，绑定 context_ref、query 和扫描位置；不能在其他资源或查询中复用。实现保留跨扫描边界匹配所需的最多 query.length-1 字节，避免长关键词在分块边界漏匹配；同一命中不重复返回。工具与直接服务调用使用同一实现。

## 5 独立分析

### 两种显式模式

| mode | 输入要求 | 模型请求构造 |
| --- | --- | --- |
| reuse_prefix | 必须是 request 资源；禁止 ranges | 保留捕获时的模型、连接、原生输入、工具定义、temperature/reasoning 配置，末尾追加一条分析问题 |
| text | 任意资源；ranges 必填且非空 | 只读取指定投影区间，用固定分析指令和问题组成独立请求；不继承来源 agent 的执行循环或工具集合 |

text 的 ranges 是按 start/end 表示的有序、不重叠 UTF-8 字节区间，最多 32 段，合计最多 256 KiB。不支持省略 ranges 自动装入全部材料。需要处理更多材料时由调用方分批选择；服务不擅自多次调用模型或自动摘要。

首版两种模式都使用资源捕获时解析的来源模型连接；text 读取不能解析模型连接时仍可完成，但 analysis.start 返回 connection_binding_required 或已有的配置错误。不开设另一套凭据输入。换 provider/model 的分析留给后续明确需求，避免混用原生 continuation。

### 执行契约

- analysis.start 先校验资源、模式、范围、问题长度和容量，固定本次输入及配置，再返回 analysis_id。它不调用 task.send，不占用 A 的 session 写槽，也不把问题、结果或分析失败写入 A 的 journal。
- accepted → running → completed/failed/cancelled，终态唯一。首版不增加分析 token 流；宿主用 get 查询，内置工具等待同一分析的终态通知，不通过反复模型调用轮询。
- 每次分析只调用一次 ModelClient.respond_once。转换错误的 HTTP attempts=0，其余最多 1；不自动重试、压缩、改变模式或递归调用 agent。
- 固定尾部指令要求“只基于已有材料回答本次问题，不继续原任务”。reuse_prefix 保留工具定义以避免主动改写前缀，但分析执行器不分派任何工具调用。模型返回工具调用时以 analysis_tool_call 失败；不能启动工具，也不能伪造工具结果后继续模型循环。
- text 模式不带执行工具。成功只接受已完成的可见文本结果。空文本、拒绝、截断、异常输出分别返回可判断的错误或 incomplete 原因；不得把空字符串当作成功监督结论。
- question 最多 8 KiB。输出预算必须满足来源模型与宿主上限。超上下文时返回 context_overflow，不改写来源也不自动压缩输入。reuse_prefix 只允许覆盖输出预算，其他影响前缀的配置保持原值。
- 结果只包含分析文本、真实来源及所选范围、时间、模式、模型、attempts 和供应商 usage。服务提供的是输入来源，不自动背书模型生成的引用或判断为已验证事实。
- 单次分析返回的完整 payload 最大 48 KiB，按紧凑 JSON 序列化后的 UTF-8 字节计量；文本、来源、用量、错误和 partial_text 共用此预算。超限与可省略字段按下述规则处理，不以原始文本长度代替计量。

### 结果预算与错误输出

analysis.get 和 analyze_context 共用同一份有界结果 DTO，accepted/running/所有终态均适用。48 KiB 的计量对象是完整结果对象；stdio 的外层 version/id/result 封套继续遵守现有帧大小限制，不能与结果预算混为一谈。工具包装完成后，以实际送入工具结果的紧凑 JSON 字节再次检查 50 KiB 边界；若增加包装字段，必须相应减少内部预算。不得依赖通用输出截断或尾部预览来裁剪结构化 JSON。

先预留 analysis_id、状态、来源、模式、时间、attempts、规范化用量和错误 code/reason 等必要字段，再尝试容纳文本及可选明细；若合计超限，先省略可选明细，再判定文本是否仍超限。接纳时已知的来源描述、ranges 或配置标识如果无法装入最小结果，analysis.start 返回 resource_too_large(reason=result_metadata_budget)，HTTP attempts=0。上游返回的模型名、错误消息及任意 usage 明细不能成为无界出口。

可选供应商 usage 明细无法容纳时，省略相应明细并设置 usage_details_omitted=true；已知 input/output/cache_read/cache_write 数值必须保留，未知仍为 null。可读错误消息可限长并标记 error_message_truncated，错误 code/reason 不能靠截断后的自然语言推断。原始详细用量只有在预算允许时完整提供，不能无标记地声称是完整原始响应。

完成响应的全文无法装入预算时，以 failed(code=output_limit) 返回，result=null，附字符边界完整的 partial_text 和 partial_text_truncated=true；对该最终错误 DTO 再计量，不把标记与元数据留在预算之外。原本就是拒绝、未完成或上游失败的响应仍保留原失败原因，过长的 partial_text/错误消息只附截断标记，不改写为成功或覆盖原失败原因。相同 analysis_id 的宿主查询与工具结果使用相同终态。

验收必须覆盖引号、反斜杠、控制字符、中文/emoji、长错误消息和大 usage 明细：48 KiB 双引号文本在 JSON 中约占 96 KiB，因此只能返回明确标记的有限片段，不能作为成功全文通过。

### 模型响应适配范围

基线实现存在两个直接缺口：[ChatUsage/转换](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/client/chat.rs#L394) 只保留总输入和输出 token；[Chat 解码](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/client/chat.rs#L410) 在转换 usage 之前检查 finish_reason 并可能提前报错。Responses 的 [项目解码](https://github.com/HeimaoLST/Saki/blob/fd58ec2500690d877a0406cdedf92aa2605bf133/src/client/responses.rs#L377) 也可能在保留 usage 前失败；其 message content 当前按 OutputText 读取，拒绝等非文本内容需要明确识别。只新增 analysis 模块无法补回这些已丢失的信息。

P2 必须一并修改现有 client/protocol 响应适配路径，不另建分析专用 HTTP 客户端或 JSON 解码器：

1. 在完成性检查和具体 output 项转换之前，提取已知 usage、响应状态和供应商结束原因。Chat 保留 prompt/completion token 明细并映射到现有 Usage 的 input/output details；缓存读/写只映射实际存在且语义确认的字段。Responses 复用现有 Usage，不能因下游项目解码失败丢弃它。
2. 使用可检查的结构化失败携带终止原因、已知用量和有限的可见 partial_text；建议在既有 anyhow 错误通道中保留可 downcast 的错误类型，避免为了分析另造执行接口。分析层不得解析错误字符串来猜测 length、拒绝、上下文超限或解码失败。转换前失败仍为 attempts=0，发出一次 HTTP 后的解码/完成性失败为 attempts=1。
3. 两协议各自区分完整文本、工具调用、拒绝、未完成和无有效文本。明确拒绝映射 analysis_refused；截断、过滤、缺失/未知结束原因映射 analysis_incomplete 并保留具体 reason；格式无效映射 upstream_error(reason=decode_error)。HTTP/传输错误与 timeout 保持各自原因。无法可靠解析的正文不补造 usage；取消/超时前确实已观测的用量可保留，尚未取得的字段为 null。partial_text 只含可见文本，不把 reasoning 或 opaque 数据当作分析答案。
4. 保留普通 Agent 对未完成响应的拒绝行为。不能为让分析读取失败详情，就把不完整工具调用或文本作为正常 ModelResponse 交给原执行循环。共享解码器/Usage 变更须回归非流式与流式路径：拒绝、不完整输出不会进入正常 journal，也不会触发工具。首版分析仍只调用非流式 respond_once，不新增分析流接口。

这些字段经过 §5 的统一结果预算后输出。验收从受控 HTTP 原始响应进入现有适配器，不能只手造已经补齐的 ModelResponse/analysis DTO 来证明功能成立。

请求与查询结果示例：

~~~json
{"version":1,"id":"analyze-1","method":"analysis.start","params":{"context_ref":"ctx-request-1","mode":"reuse_prefix","question":"截至这份材料，哪些用户要求已有完成证据，哪些仍无法确认？","max_output_tokens":2048,"timeout_ms":120000}}
~~~

~~~json
{
  "analysis_id": "analysis-1",
  "status": "completed",
  "mode": "reuse_prefix",
  "source": {"context_ref":"ctx-request-1","session_id":"session-a","view":"request","request_sequence":7,"ranges":null},
  "model": "configured-model",
  "started_at_ms": 1792800000100,
  "finished_at_ms": 1792800002100,
  "result": {"text":"示例分析结论"},
  "error": null,
  "attempts": 1,
  "usage": {"input_tokens":null,"output_tokens":null,"input_tokens_details":null,"output_tokens_details":null},
  "cache_read_tokens": null,
  "cache_write_tokens": null
}
~~~

accepted/running 的 result 和 error 为 null；failed/cancelled 的 result 为 null，error 给出 code/reason，允许单独附 partial_text。usage_details_omitted、error_message_truncated、partial_text_truncated 为可选布尔字段，缺省 false，发生省略/截断时必须返回 true。started_at_ms、finished_at_ms、attempts、usage 在事实未知时为 null。取消响应的 cancel_requested 仅表示请求已登记，最终状态以 analysis.get 为准。

### 容量与取消

增加 max_analysis_jobs，默认 2，与业务 max_active_tasks 分开计数；标题生成保持既有容量语义。总可能模型并发为业务任务、分析任务与已有辅助任务之和，必须在 capabilities 中分别暴露，不能把新增分析隐藏在无界辅助任务里。

容量满立即 busy(reason=analysis_capacity)，不隐藏排队。A 和 B 占满业务任务槽时，B 等待的分析仍能使用分析容量；避免 B 等自己的槽位释放。一次分析内部不允许再调用分析。

内置 analyze_context 由服务绑定真实父 task，不能接受模型自填的 parent_task_id。父任务取消、异常退出或工具 future 被丢弃时，取消关联分析并释放容量；仅丢弃等待者不得留下后台模型请求。取消 B 不取消 A；取消 A 不取消已经基于既有资源开始的独立分析。

直接服务启动的分析只有显式 analysis.cancel 才请求取消。超时状态为 failed(reason=timeout)。本地停止等待/关闭 HTTP 不保证供应商已停止计算或计费，未知 usage 不记为零。取消与完成竞争由服务控制循环确定唯一终态。

分析不阻塞控制循环；status/cancel/release 必须在模型请求或读取输出阻塞时仍可处理。正常 shutdown/EOF 沿用已有 drain 语义，等待已接纳分析完成或到达其超时，不把断开显示输出等同于取消。

## 6 Agent 工具与产品绑定

首版内置工具名称：open_context、read_context、search_context、analyze_context。前三者分别调用同名服务能力；analyze_context 组合 analysis.start 与等待终态，只把有限结果返回当前 agent。宿主仍可以直接使用全部服务接口。

工具默认不开放。将“可用内置工具目录”与“兼容默认工具集合”分开：目前默认集合由 builtin_names 生成，直接往目录追加名称会改变旧 session 的工具前缀和行为。默认仍是 read/bash/edit/write，新工具由 tool_selection 显式选入；只重构这处实际需要的目录/默认集合区别。

在 task.send 增加可选 context_sources 字符串数组，默认空。当前 task 可访问自己所在 session，以及宿主明确提供的 session ID；不根据联系人名称猜测范围。该字段是执行选项，不拼进模型输入，不落成 caller_context 文本。产品在提示词或当前任务输入中给出目标 session ID 与用途。服务接纳时固定范围，不在任务中途被其他请求扩大。

typed 入口保留现有 Send 使用方式，增加带选项的入口/构造方法；已有调用默认空范围。不要为了传执行选项把字段塞入通用 UserMessage 文本表示。Rust 公共枚举或结构的新增字段/变体可能影响穷举匹配，实施时逐一核对调用方并记录兼容性，不把 JSON 可选字段等同于所有 Rust 源码兼容。

内置工具使用绑定在实际执行上的范围校验 context.open 和后续 ref 访问；即使模型猜出其他引用，也不能绕过来源范围。宿主服务 API 是同一 Runtime 的受信任调用入口，本轮不引入远程多租户鉴权体系。范围是 #62 已有产品契约的延续。

工具定义说明视图区别、限长、只读、过期和错误语义。system prompt 可指导“优先定位相关资料；必要时独立分析”，不把该策略硬编码成所有 agent 的固定工作流。

## 7 资源与缓存

### 有界资源

默认宿主限制作为实现起点，需在 capabilities 返回有效值：

| 项目 | 默认值与行为 |
| --- | --- |
| 单份 context 来源上限 | 64 MiB；超出返回 resource_too_large，不先无界复制后再检查 |
| 资源总预算 | 256 MiB、最多 32 个 context_ref；包含旧引用保留的工作/请求副本和后台捕获副本；共享 Arc 按实际保留数据计费 |
| 引用有效期 | 最近成功使用后 10 分钟；release 或 Runtime 重启后失效 |
| 已完成分析结果 | 完整序列化 payload 每条最多 48 KiB，含成功/失败/取消及其元数据；最多 64 条，终态后保留 10 分钟；回收后 get 返回 not_found |
| 模型分析 | max_analysis_jobs=2；默认 120 秒，宿主最大 300 秒 |

活跃分析固定持有输入；期间引用 release/过期只禁止新的读取/分析，不改变已接纳分析。其内存仍计入预算，直到分析结束。达到预算明确拒绝新的 open/start，不驱逐正在使用的来源。

agent 工具创建的临时引用在所属 task 结束时释放；直接服务创建的引用由调用方 release 或到期回收。没有跨 Runtime 重启恢复引用或分析任务的承诺。

背景发布 request 也必须受预算约束。若新请求过大或旧引用占满预算，清除“最新 request 可用”标记并记录原因，不阻止 A 正常执行，也不能把上一代 request 冒充最新值；调用方持有的旧 ref 仍有效。

### 前缀缓存契约

reuse_prefix 保留来源请求的结构化输入、工具顺序、系统指令和供应商原生 continuation；问题追加在末尾。text 模式是新请求，不声称复用 A 的长前缀。B 的 system prompt 不插到 A 的长前缀之前；read/search 的文本投影不得用于重建 reuse_prefix 请求。

服务复用既有 ModelClient 与协议编码函数，不另拼一套 Chat/Responses JSON。原请求的 API、连接、模型和配置固定在 ref 中；A 后续更新配置不会改变该 ref 的执行含义。

提供规范化 usage 和可确认的 cache_read_tokens/cache_write_tokens，并在 §5 预算允许时保留供应商原始明细；明细省略必须标记。缺少供应商字段时为 null。兼容 Chat 服务字段存在差异，按实际字段映射，不用“前缀相同”估算缓存命中，不把未返回用量记为零。继承的请求配置、TTL、路由及供应商策略可能影响命中；Runtime 保证构造行为，不保证命中率。

压缩后新的 request 使用新前缀，旧 ref 保留旧前缀；不能把新摘要拼入旧前缀再宣称未变。cached tokens 仍计入上下文窗口，输出预算检查包含新增问题。

缓存条件依据：[Claude 前缀匹配与失效规则](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)、[Gemini 缓存与 token 限制](https://ai.google.dev/gemini-api/docs/generate-content/caching)。二者用于说明设计限制，不代表当前 Saki 已实现其原生 API。

## 8 实现落点

| 位置 | 修改建议 |
| --- | --- |
| src/session/context.rs 新增 | 资源描述、引用表、预算、读取/搜索与来源定位。复用 journal 的唯一 reader/replay；不要再写 JSONL parser |
| src/session/analysis.rs 新增 | 独立单次调用、状态与结果 DTO、取消/超时、用量、完整序列化结果预算。复用 ModelClient，不复制 Agent loop |
| src/session/runtime.rs 与 stdio.rs | 增加命令/响应和生命周期接线，保持控制循环不等待磁盘扫描或模型完成 |
| src/agent/mod.rs | 在正常生成请求准备完成、调用 ModelClient 前发布 request 的不可变副本；带实际配置/工具定义。不要通过 GUI 展示事件转运完整上下文 |
| src/agent/journal.rs | 仅增加确有必要的有界只读辅助能力；working/history 共享同一投影来源 |
| src/tools/context.rs 新增 | 绑定 Runtime 服务访问和实际 task 范围的薄工具包装；不自读 session 文件 |
| src/tools/mod.rs 与 session/config.rs | 可用工具目录、兼容默认集合和显式选择；避免为新工具自动改变所有旧 session |
| src/client/chat.rs | 保留 usage 明细、结束原因和拒绝；完成性检查/解码失败仍携带已知用量，回归共享的流式适配路径 |
| src/client/responses.rs 及共享流式适配 | 保留状态、未完成/拒绝/项目解码失败原因及已知用量；只补共享边界所需信息，不另建分析解码器 |
| src/protocol/mod.rs 与 client/mod.rs | 复用 Usage/ModelResponse/Attempted 和唯一编码路径；补结构化失败所需信息，核对公开类型与旧调用方兼容性 |

request 发布使用每个 session 一个可替换的不可变共享值；可采用短锁或 watch，不在模型请求期间持锁，不因 UI 背压延迟发布。已有源引用通过 Arc 保留所需内容，不保留所有历史代次。

history/working 从最近完整 journal 前缀读取。长文件解析/扫描放已有阻塞作业通道或 spawn_blocking；捕获有效结束位置后按该边界重放，同次读取不包含随后追加的半条记录。只读接口不获取写锁、不修复文件、不为读历史启动 MCP 进程。

P1 在大文件解析和投影前预留资源预算及并发读取容量；不能读完整文件、构建完整 replay 后才决定是否接纳。文件字节数只用于预检查，不能当作解析后内存大小；读缓冲、单条记录解码、replay 与投影产生的临时/保留数据也计入预算，扩张前检查剩余额度。越限、读取失败或取消时释放预留，并停止相应阻塞作业；并发 open 不能各自通过检查后超额分配。不为此引入第二套 journal parser。

首版可以在生成边界复制一次必要的结构化请求，再用 Arc 给多个分析共享；先测量大上下文的复制与序列化成本。不要先全局改写 Conversation 的存储结构。若服务预算不够，按 §7 降低 request 可用性，不妨碍来源任务。

## 9 错误语义

保留既有 code 与 reason 分层。新增错误只在有调用方可区分行为时使用：

| code 或 reason | 调用方含义 |
| --- | --- |
| invalid_request | 视图/模式/区间/字符边界/参数不合法；未执行模型 |
| not_found | session 或 analysis 不存在；不偷偷创建 |
| scope_denied | 内置工具访问不在任务绑定范围内的 session/ref |
| context_unavailable | 引用失效，或 request 尚未发布/已重启/预算不足；reason 区分原因 |
| invalid_history / io_error | 沿用已有 reader 和 I/O 错误；不写盘修复 |
| resource_too_large / busy | 来源过大或资源/分析容量不足；明确 reason，不排队 |
| connection_binding_required / context_incompatible | 沿用连接身份、原生 continuation 和协议兼容规则 |
| context_overflow | 新问题加上输出预算无法容纳，或 provider 实际拒绝；不自动压缩 |
| analysis_tool_call / analysis_no_text / analysis_refused / analysis_incomplete | 分别表示请求工具、无可见文本、明确拒绝、未完成；保留供应商具体结束原因，原 session 不变 |
| timeout / output_limit / upstream_error | 已接纳分析的失败原因；保留已知 attempts、usage 与有限 partial_text |

接纳前失败直接返回 error；接纳后失败保存在 analysis.get 的终态中。未知字段遵守现有 stdio DTO 校验方式。错误正文不得带连接凭据。

## 10 验收矩阵

所有并发场景使用 barrier、oneshot、受控 HTTP/MCP fixture 或可控时钟；不依靠 sleep 碰时序。先验证可观察行为，再记录实现细节。下列为必须验收项，尚无 PASS 结论。

| ID | 场景 | 通过条件 |
| --- | --- | --- |
| R01 | 普通/活动 session 的 history 与 working | 返回对应视图；读取引发的模型/执行工具调用为零；来源 journal/metadata 无新增写入 |
| R02 | 连续两次压缩 | history 仍可搜索到唯一原文；working 正确反映最新摘要与保留区间；两者不混用 |
| R03 | 压缩发布与 open 竞争 | 每次拿到完整旧版或新版；无混合；旧 ref 内容保持不变，新 open 可取得新版 |
| R04 | request 发布时点 | 只捕获正常生成输入；不捕获压缩请求、不包含半截流；来源请求序号和配置正确 |
| R05 | 首次执行前/重启/重新加载 | request 明确 unavailable；history/working 可读；没有为了造 request 发模型请求 |
| R06 | 长工具输出、中文、emoji、JSON 转义 | 分段读取连续拼接与原投影一致；字符边界正确；每次结果实际序列化字节不超限 |
| R07 | 搜索分页与边界 | 跨 1 MiB 边界关键词仍命中且不重复；达到命中/输出上限仍能续查；query/ref 不同的 cursor 拒绝 |
| R08 | 图片和原生 continuation | 可读投影明确标注省略；不误当文本命中；内部 request 原生数据完整保留 |
| R09 | 损坏 journal、torn tail、未知 schema | 与现有 reader 契约一致；读取不修复、不截断、不启动 MCP |
| R10 | 引用到期、release、任务结束、预算满 | 大文件解析前预留容量，覆盖并发 open、单条超大记录和投影扩张；越限/失败/取消释放预留并停止读取；活跃分析的输入仍可用且计预算，无无界保留 |
| A01 | reuse_prefix 结构 | 模型 fixture 捕获实际 wire：原消息/工具/原生项前缀不被包装改写，末尾仅新增分析指令与问题 |
| A02 | text 指定区间 | 请求只含所选区间及固定分析提示/问题；未选内容、B 主历史和 A 执行工具不进入请求 |
| A03 | 不污染 A/B | A journal 无监督问答；B 最终只收到实际工具片段/结果；原始 900K 级材料不通过 open/analyze 工具回传 B |
| A04 | 工具调用/空文本/截断/拒绝 | Chat/Responses 原始响应分别经现有适配器进入分析；区分拒绝、未完成、缺失/未知结束原因与解码失败，保留实际已知用量及有限可见片段；工具执行为零，不补模型轮次，不把失败当完成 |
| A05 | 取消与完成竞争 | 唯一终态；取消 B 回收其分析，A 保持运行；取消 A 不隐式取消已接纳分析 |
| A06 | 父工具 future 被丢弃或父 task panic | 关联分析收到取消并回收容量；无无人持有的后台请求继续占槽 |
| A07 | 容量死锁回归 | 两个业务槽被 A/B 占满，B 仍可启动分析；分析满额立即 busy，不等待 B 结束 |
| A08 | 超时、HTTP/解码错误、序列化输出超限 | HTTP 最多一次，attempts 正确；48 KiB 引号、反斜杠、控制字符、emoji、长错误/usage fixture 均验证完整结果 ≤48 KiB、最终工具 JSON ≤50 KiB；失败原因不被截断覆盖，标记与已知用量保留，缺失 usage 为 null，容量回收 |
| A09 | 输出堵塞与 shutdown | status/cancel 可处理；终态回收不等待 stdout；正常 drain 等已接纳分析完成/超时，A/B 收尾沿用原契约 |
| A10 | A 更换配置/压缩/release | 旧 ref 的分析继续用捕获时来源配置；新 open 反映新状态；不把新连接套到旧 opaque 数据 |
| C01 | 工具目录兼容 | 未启用功能的旧 session 仍只有原默认四工具，原配置/历史可读，模型工具前缀不因升级改变 |
| C02 | 范围绑定 | 默认只读当前 session；绑定 A 后可读 A；未绑定 C 的 open、猜测 ref、search/analyze 全部拒绝 |
| C03 | typed 与 stdio | 同一输入获得相同结果与错误；旧 task.send 不带新字段仍按原行为；原 GUI/TUI 消费回归通过 |
| C04 | 真实 agent 工具闭环 | fixture 驱动 B 先 open/search/read 再回答；另一路 open(request)/analyze 后回答；工具均经 Runtime，A 无新增问答 |
| C05 | 缓存用量 | 两协议从原始 HTTP 成功及失败响应验证 cache/usage 明细精确传递；缺失为 null、已知零仍为零，省略过大明细有标记且已知计数不丢失；总输入、缓存读/写和输出不重复相加；text 不声称 A 前缀命中 |
| C06 | 分支与协议兼容 | 同一资源不能跨 API/provider 重新解释；Chat 与 Responses 各有真实编码 fixture；共享适配器改动后普通 Agent 的非流式/流式未完成输出不写入正常 journal、不执行工具；无新增字段来源时旧默认序列化不变 |
| P01 | 大资料本地性能 | 固定至少 8 MiB 纯文本资源，分别记录 open、有限 read/search 的 p50/p95、峰值保留字节；整个读取期间受控 A 能继续完成至少一个工具步骤 |
| P02 | 真实模型分析 | 至少一个实际已配置且支持缓存观测的连接：A 先建立前缀，再用两次不同尾部问题分析；记录请求配置、缓存读/写、延迟、输出及一次压缩后的变化 |
| P03 | 监督质量 | 固定 10 组有可核验答案的执行记录，覆盖完成/未完成/失败/信息不足；分别做检索路径和完整分析，报告正确判断、漏证据与错误断言，不把来源任务状态当模型猜测 |

P01 不预先虚构跨机器毫秒 SLA。提交原始数据并由实际消费者判断是否可接受；若读取阻塞 A、资源预算被突破或结果无界，直接不通过。缓存实测不得用 mock 的 cached token 数宣称真实命中。

P02 首轮采用模型实际允许、且能超过缓存门槛的规模，不默认花费 900K 真实输入。900K/1M 级别另作明确预算的压力实验；本地 8 MiB 文本测试不冒称精确 900K token 测试。供应商不提供缓存观测时可证明接口可用，但缓存优化结论保持待验证，不能写成该能力已验收。

P03 在测试前固定资料和标准答案。初始通过标准：每条路径至少 9/10 核心事实正确，所有“信息不足”样本明确表示不足，不把失败/未完成任务判为已完成；未达标记录失败输入，不通过调整资料隐藏问题。记录模型与提示词版本，这是该固定样本的结果，不宣称通用准确率。

## 11 实施顺序与交付

| 阶段 | 内容 | 必需证据 |
| --- | --- | --- |
| P1 上下文资料 | 承接 #62，history/working open/read/search/release、解析前预算预留、范围绑定和读取工具 | R01–R03、R06–R10、C01–C03、C04 检索路径、P01 |
| P2 独立分析 | request 发布；两协议响应/失败/用量适配；analysis 生命周期、两种模式、完整结果预算、analyze 工具 | R04–R05、A01–A10、C04 分析路径、C05–C06 |
| P3 实际消费 | 两协议回归、真实模型缓存实验、固定监督样本、文档与示例 | 全矩阵回归、P02–P03、现有 GUI/TUI 消费门禁 |

先让 Runtime 负责人核对新增 DTO 和 #62 的归并范围；GUI/TUI 对公共消费契约确认。没有实现的阶段不加入 capabilities 的可用列表。

每阶段在最新 dev 的独立任务 worktree 实现，不改主检出。遵守实际 worktree 的 AGENTS.md 和用户持续授权。当前 dev 的评审/QA 职责已不同于旧 master：主 agent 负责代码评审及适用验收，QA 负责 CI 工程；本方案不新增人工 QA 等待队列，也不替代用户更高优先级指令。

Rust 改动至少运行 cargo fmt --check、cargo clippy --all-targets -- -D warnings、cargo test --all-targets，补真实 stdio 子进程消费；required CI 必须实际通过。既有 GUI/TUI 回归按修改范围与仓库门禁执行。模型测试的凭据通过已有宿主配置注入，日志只保留不含密钥的请求摘要与用量。

最终交付记录：实现 SHA、基线 SHA、逐项验收结果、失败/限制、真实 provider 样本、兼容性结论和未完成事项。长期事实更新已有 runtime-api.md、session-management.md、builtin-tools.md；本主题文档随实现将已完成部分改标为实现事实，避免把提案与实现状态混写。

### 可直接转交的任务说明

> 以最新 origin/dev 为基线，先阅读本提案并核对 #62。实现 Session 上下文访问与独立分析：Runtime 服务与 agent 工具共用读取/搜索逻辑，支持 history、working 与真实 request 视图，分析在独立调用中完成且不污染来源会话。按 P1/P2/P3 分阶段提交，逐项给出验收证据。重点检查解析前资源预留、压缩期间完整读取、原生请求前缀、完整序列化结果预算、两协议失败及用量保留、默认工具兼容、父任务取消传播和分析容量死锁。联系人与监督策略由产品配置；本轮不实现外部 harness、来源会话修改或 GUI 新界面。若需改变本文契约，先明确差异、理由和受影响验收项，再推进对应修改。

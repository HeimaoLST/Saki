# Runtime 调用契约与实施规划

更新日期：2026-10-22。状态：**调用契约已确认；首轮机器入口由 #60 交付；跨端接入与后续能力分别追踪**。

需求依据：[Agent runtime 需求](../requirements/agent-runtime.md)、[#43](https://github.com/HeimaoLST/Saki/issues/43) 及 [GUI 首轮交接](https://github.com/HeimaoLST/Saki/issues/43#issuecomment-6235030388)。本文定义跨宿主调用契约与验收边界；实时优先级、任务状态和负责人以 GitHub 为准。

## 决策确认

本契约已取得实际消费意见与独立提案评审：[GUI ACK](https://github.com/HeimaoLST/Saki/issues/43#issuecomment-6258133217)、[PM 范围 ACK](https://github.com/HeimaoLST/Saki/issues/43#issuecomment-6258394345)、[TUI 兼容 ACK](https://github.com/HeimaoLST/Saki/issues/43#issuecomment-6262714452)、[最终提案独立评审汇总](https://github.com/HeimaoLST/Saki/pull/59#issuecomment-6244513599)。[PM 实施交接](https://github.com/HeimaoLST/Saki/issues/60#issuecomment-6284753791)已确认 #60 的 DoR，首轮按 #60 → #63 推进；[QA 边界调整](https://github.com/HeimaoLST/Saki/issues/60#issuecomment-6284890371)将 #64 限定为 CI 工程，不作为逐 PR 人工产品验收前置。

以上确认针对调用契约与范围，不代表代码或跨端验收完成。具体 v1 DTO 位于 `src/session/stdio.rs`，可执行驱动位于 `examples/runtime_stdio.py`，运行证据随 #60 交付；共享 typed 事件的实际字段与消费接线按 TUI ACK 继续协调。多会话并发、压缩后原文检索及 #31／#32／#33 仍为独立后续任务。

## 现状与目标

实现事实（#60；以同 PR 的代码和运行证据为准）：`src/session/runtime.rs` 提供 typed 服务核心，`src/session/stdio.rs` 提供 stdio v1 调用边界，按 ID 保留真实 loaded sessions，首轮只有一个执行槽。`Agent` 继续承担唯一模型／工具循环，`SessionStore` 和 journal reader 继续承担持久化／恢复，旧 TUI／console 入口保持。GUI 联系人接入由 #63 交付；重叠执行和 compact 后原文检索仍分别由 #61／#62 交付。

## 推荐方案与取舍

推荐新增 `Saki --stdio`：一个子进程、一条输入 JSONL 和一条输出 JSONL。Go 通过 `os/exec` 的 pipe 调用，stdout 专用于机器响应／事件，stderr 用于诊断。无需新增依赖、监听端口、daemon、Rust library 打包或 Go FFI。

HTTP 适合后续多连接／远程宿主，但当前需要额外服务生命周期；FFI 需要 ABI 与跨语言构建约束。两者不是首轮前提。默认启动及现有 `SAKI_UI=console|tui` 保持原行为，`--stdio` 是独立入口而非新 console 输出格式。

核心不依赖传输或前端。机器适配层负责解析、版本和关联；执行仍进入同一个 Agent、工具 registry、store、journal 和 compact。先复用／提取现有 session 执行与取消职责，不把第二份 model/tool loop 放进适配层，也不为了未来宿主预建插件框架。

## 启动与配置

```sh
cargo build --locked
SAKI_API=openai-chat-completions SAKI_BASE_URL=https://provider.example/v1 \
SAKI_MODEL=model-id SAKI_SESSION_DIR=/path/to/sessions target/debug/Saki --stdio
```

示例假设宿主已经通过进程环境或既有 `.env` 提供 `API_KEY`；示例不表示无需凭据。实际 provider 与模型的兼容性以对应 smoke 运行证据为准。

provider 的协议／endpoint／凭据归宿主，启动时注入一次；不进入请求、事件或 session metadata。模型、工作目录、system prompt、compact 配置归 session；省略覆盖值时使用宿主默认。首轮工具为现有 read/bash/edit/write，Rust 调用方继续用 ToolRegistry 组装，不开放由 JSON 上传可执行工具定义。

机器入口不隐式创建或恢复 session，不读取 `SAKI_SESSION` 来选择全局活动对象。配置不完整时进程初始化失败（stderr + 非零退出），不发 ready 或 task.started；初始化成功先输出 `ready`，包含协议版本和可用 API 标识，不包含 key。新 session 无需联系人 profile；联系人、名称、头像和 contact→session 映射由 GUI 保存。

首轮不提供联系人的运行期 profile 编辑或模型覆盖 UI。后续配置更新在 session 空闲时原子生效，只影响未来模型请求；不重写原始历史。模型更新由 #32 承载，profile 的具体持久化字段在独立实施项中定稿，未定稿前不宣称支持。

## 请求、响应与事件

每条消息是一个 UTF-8 JSON 对象加换行。请求形状：`{"version":1,"id":"r1","method":"session.create","params":{}}`。`id` 是调用方生成的、连接内唯一的字符串；它用于关联响应，不是幂等键。首版不提供自动重试、去重或 exactly-once 提交。

同一版本的消费者忽略未知可选字段；新方法／事件须在兼容性评审中说明消费者处理方式。机器 DTO 显式映射既有运行事件，不把内部 Rust 枚举的偶然命名直接当作稳定外部协议。输入 `message.images` 使用 `[{"name":"image.png","data_url":"data:image/png;base64,..."}]` 的快照形状；仍受所选模型协议的已有图片支持限制，Chat M1 不支持图片时明确拒绝。

响应有 `version:1,type:"response",id` 和恰好一个 `result` 或 `error`。错误至少包含稳定 `code` 和可诊断 `message`，可带 `session_id`；解析不出请求 ID 时 `id:null`。未知版本／方法／参数不执行操作。请求错误不会伪造成任务终态，也不会错误结束其他任务。

| 方法 | 参数／返回与行为 |
| --- | --- |
| `session.create` | 可选 `working_directory`、`system_prompt`；返回 metadata 和稳定 session_id，准备接受任务；失败不回退到另一会话 |
| `session.load` | `session_id`；加载既有配置与上下文并返回 metadata／状态；已加载则返回当前状态，不重放执行 |
| `session.list` | 返回当前 store 的 summaries；不获取其他 session 的写入权 |
| `session.rename` | `session_id,title`；沿用手工名称优先，返回 metadata |
| `session.history` | `session_id,offset?,limit?`；从同一个 journal reader 返回原始记录页、状态和下一 offset；不执行模型／工具、不修复未加载文件 |
| `session.status` | `session_id`；返回 loaded、运行 task 或 idle，以及已有持久完成／中断状态；没有全局选中会话 |
| `session.release` | `session_id`；释放空闲 session 的内存／写锁，不删除历史；运行中返回 busy |
| `task.send` | `session_id,message:{text,images?}`；有效且可运行时返回 task_id、message_id；首轮忙时明确拒绝，不排队 |
| `task.cancel` | `session_id,task_id`；仅取消匹配的活动任务；返回 `requested`、`already_requested` 或 `not_running`，均不是完成证明 |
| `runtime.shutdown` | 停止接纳新工作，等待已接纳执行结束，再关闭写锁和进程；不是取消 |

首轮请求错误 code 覆盖 `invalid_request`、`unsupported_version`、`unknown_method`、`not_found`、`busy`、`locked`、`api_mismatch`、`invalid_history`、`io_error`。最终实现测试固定实际 code 与失败边界；不能只把所有错误包装成一个字符串。

### 最小往返示例

```json
{"version":1,"id":"c1","method":"session.create","params":{}}
{"version":1,"type":"response","id":"c1","result":{"session_id":"s1","metadata":{}}}
{"version":1,"id":"m1","method":"task.send","params":{"session_id":"s1","message":{"text":"Read probe.txt"}}}
{"version":1,"type":"response","id":"m1","result":{"task_id":"t1","message_id":"i1"}}
{"version":1,"type":"event","session_id":"s1","task_id":"t1","message_id":"i1","event":{"type":"task_started"}}
{"version":1,"type":"event","session_id":"s1","task_id":"t1","message_id":"i1","event":{"type":"model_request_started"}}
{"version":1,"type":"event","session_id":"s1","task_id":"t1","message_id":"i1","event":{"type":"response_text_delta","item_id":"a1","delta":"Checking"}}
{"version":1,"type":"event","session_id":"s1","task_id":"t1","message_id":"i1","event":{"type":"response_text","item_id":"a1","text":"Checking."}}
{"version":1,"type":"event","session_id":"s1","task_id":"t1","message_id":"i1","event":{"type":"tool_started","call_id":"k1","name":"read","arguments":"{\"path\":\"probe.txt\"}"}}
{"version":1,"type":"event","session_id":"s1","task_id":"t1","message_id":"i1","event":{"type":"tool_finished","call_id":"k1","name":"read","output":{"content":"marker"}}}
{"version":1,"type":"event","session_id":"s1","task_id":"t1","message_id":"i1","event":{"type":"task_finished","status":"completed"}}
```

metadata 和工具 output 在示例中仅为形状示意，不替代 SessionMetadata 或工具的实际字段契约。工具之后还会有下一次 model_request_started 与最终回答事件，不能因看见工具结果就提前标记完成。

以下是恢复、历史、取消和错误的独立场景示例；真实 session/task ID 由调用方保留：

```json
{"version":1,"id":"o1","method":"session.load","params":{"session_id":"s1"}}
{"version":1,"id":"h1","method":"session.history","params":{"session_id":"s1","offset":0,"limit":100}}
{"version":1,"type":"response","id":"h1","result":{"records":[],"next_offset":null,"status":"completed"}}
{"version":1,"id":"x1","method":"task.cancel","params":{"session_id":"s1","task_id":"t1"}}
{"version":1,"type":"response","id":"x1","result":{"status":"requested"}}
{"version":1,"type":"event","session_id":"s1","task_id":"t1","message_id":"i1","event":{"type":"task_finished","status":"interrupted"}}
{"version":1,"type":"response","id":"bad1","error":{"code":"not_found","message":"session not found","session_id":"missing"}}
{"version":1,"type":"event","session_id":"s1","task_id":"t2","message_id":"i2","event":{"type":"task_finished","status":"failed","error":{"code":"model_error","message":"upstream request failed"}}}
```

历史记录页至少含稳定 record offset 和已解析的 journal record；status 反映实际持久状态，空数组示例不表示有记录的会话应丢弃历史。创建／加载响应返回 metadata 和实际 loaded／active task 状态；实际 v1 字段与下面的实现字段表一致；版本 1 不静默改变含义。

### 关联与顺序

- 接纳 response 必须先于该任务的 task_started 和增量事件；被拒绝没有 task_id，不产生任务开始事件。接纳表示进程内接收，不表示 durable queue 或模型已经理解。
- task_id 在调用事件与 journal 中一致；输入 message_id 与任务关联。旧日志缺少 message_id 时返回缺失，不能编造历史关联。关联放封套，不要求每个 AgentEvent 重复字段。
- task_started 是明确执行事实，不能从首个 ContextUpdated 或文字增量猜测。model_request_started 表示开始一次模型调用，不表示上游已接受或已产生 token；compact 请求与正常生成须可区分。自动标题属于 session 后台工作，不冒记成已结束任务的新阶段。
- 文字／推理按 `(session_id,task_id,item_id)` 聚合；delta 追加到该记录，最终全文替换该记录。失败或取消不自动重播增量。工具按 `(session_id,task_id,call_id)` 关联，工具业务错误可作为结果交给模型，不必使整轮失败。
- 同一任务中的事件按执行观察顺序发送；不同任务可以交错。任务最终恰好一个 completed／failed／interrupted；持久化失败不能报告可靠的 saved success。标题／metadata 更新不属于任务终态。
- GUI 保存所有 session 的状态，当前查看对象只是投影；切换查看不发 cancel/release。不为头像或“已读”增加 runtime 状态。

## 历史、取消与连接生命周期

session.history 返回运行核心已解析的语义记录；GUI 渲染 message／tool／compaction／task 记录，而不是读磁盘或自行解析 JSONL。offset 是当前追加日志的稳定记录位置，不是 SSE 游标；旧 journal 重用既有校验与 replay，不能另写一个宽松 parser。活动任务写入后的完整记录可读，torn tail、损坏完整记录、未知 schema 仍遵守现有错误与恢复契约。

session.load 获得写锁、允许既有 torn-tail 修复；history 本身只读。加载／查看均零模型／工具请求。重启后恢复同一个 ID，报告 interrupted／unknown 等实际历史状态；下一条新消息复用既有 pending tool reconciliation，不重跑旧调用。#47 的协议匹配限制继续保留，错误不得偷偷新建会话。

cancel 的接纳只表示发送协作式信号。任务实际退出并完成 journal 收尾后才发 interrupted；若完成先赢，则保持 completed、取消返回 not_running，不补第二个终态。取消无法回滚文件或其他外部副作用，锁在执行退出前不释放。

stdin EOF 与显式 shutdown 都停止接纳，等待已接纳任务结束；不自动取消。stdout 写端断开表示观察通道丢失：停止接纳新请求，等待已接纳执行落盘／收尾后退出，不冒报 completed；GUI 将状态标为连接中断／未知并重新加载历史核对。首版没有透明重连、事件游标和断线续传；重新启动是新进程，不承诺强杀后任务存活。

输出断连在下一次写入失败时可观察；空闲时没有心跳或独立断连探测，宿主关闭连接时应同时关闭 stdin，不能只关闭 stdout 后保持 stdin 永久打开。已接纳但回包未成功写出的任务仍按不确定接纳语义收尾。

如果调用方没收到 task.send 的接纳响应就失去连接，不能自动重发；任务可能已执行。先恢复对应 session 的历史／状态，只有明确新意图才再次提交。后续若需要幂等提交，必须另定义 durable 接纳和去重契约。

Go 侧用 `os/exec` 的 stdin/stdout pipe，持续独立读取 stdout；不能为了等一次请求响应而暂停事件读取。使用 CommandContext 时，context 应绑定整个子进程生命周期，不绑定当前查看对象。按 [Go StdoutPipe 契约](https://pkg.go.dev/os/exec#Cmd.StdoutPipe)，Start 后完成 pipe 读取再 Wait。默认 Scanner 的行长上限不足以承载图片或较大工具结果，应按 [Scanner.Buffer](https://pkg.go.dev/bufio#Scanner.Buffer) 设置 buffer 或用 Reader。任务结束与进程退出分开：读到 task_finished 不关闭整个 runtime。

可执行无 GUI 驱动位于 [`examples/runtime_stdio.py`](../../examples/runtime_stdio.py)，使用独立 stdout 读取线程分发响应和事件。构建和离线跨进程验收：

```sh
cargo build --locked
python3 examples/runtime_stdio.py --self-test
```

真实模型沿用环境中的 `API_KEY`、`SAKI_API`、`SAKI_BASE_URL`、`SAKI_MODEL`、compact 配置和 `SAKI_SESSION_DIR`，启动 `target/debug/Saki --stdio`。API_KEY 缺失时在 ready 前失败；stdout 只写 JSONL，诊断留在 stderr。示例真实试用：

```sh
python3 examples/runtime_stdio.py --prompt 'Use read to inspect probe.txt and report its marker.'
python3 examples/runtime_stdio.py --session SAVED_ID --prompt 'Continue our previous task.'
```

示例记录并展示所有任务事件；正式 GUI 持续路由所有 response/event，并独立处理 stderr 和子进程退出。`--self-test` 使用受控本地模型服务和真实 read 工具，启动两个独立 Rust 进程，验证恢复／历史零模型调用和新任务继续前文；它不替代真实 provider smoke。

### 实现字段

所有时间为 Unix 毫秒；ID 为不透明字符串。metadata 沿用 `SessionMetadata`（schema_version、id、title、可选 title_source、created_at、working_directory、model、api、compaction）。省略或 null 的可选参数使用默认值，未知参数拒绝。

| 输出 | v1 字段 |
| --- | --- |
| ready | `version:1,type:"ready",api`，api 为当前宿主实际协议标识 |
| create/load result | `session_id,metadata,loaded,state,active_task,status`；state 为 idle/running，active_task 为空或含 task_id/message_id/cancel_requested |
| status result | 同上去掉 metadata；本进程活动任务 status 为 running，其余由 journal replay 返回 completed/failed/interrupted/unknown |
| list result | `sessions` 数组；元素为 metadata、updated_at、status、in_use、unavailable、loaded、state、active_task_id；损坏项保留 unavailable 信息 |
| rename result | `session_id,metadata`；手工名称继续优先 |
| history result | `session_id,records,next_offset,status,state,active_task_id`；records 元素为 `offset,timestamp_ms,record`，record 为唯一 reader 解析的 journal 语义；旧 timestamp 缺失为 0，旧 message_id 保持缺失；next_offset 到末尾为 null，默认 limit=100，必须大于0 |
| release result | `session_id,loaded:false`；释放闲时写锁，不删文件 |
| send result | `task_id,message_id`；ID 与 task_started journal record 一致 |
| cancel result | `status:requested|already_requested|not_running` |
| shutdown result | `state:draining`，等待已接纳任务收尾后进程退出 |
| event 封套 | `version:1,type:"event",session_id,task_id,message_id,event`；event.type 显式 snake_case 映射，model_request_started.purpose 为 generation/compaction；task_finished.status 为 completed/failed/interrupted，failed 带 error.code/message |

文字／推理 item_id 来自模型客户端，final 全文覆盖 delta 投影；工具 call_id 来自唯一执行循环。机器宿主首轮不启动自动标题请求，避免额外背景调用被混成任务阶段；可通过 rename 更新标题。默认终端自动标题路径保持。未加载会话的 send 返回 not_found，先显式 load；release 已释放会话幂等。未知方法、版本、参数不执行操作。

## 综合交付顺序与依赖

| 阶段 | 交付 | 依赖／退出条件 |
| --- | --- | --- |
| 0 | #47 统一客户端合入 dev；#54 同步 README | 以实际最新 dev 为准同步／验收；质量修复 #58 与 CI 治理 #55 的影响须核对，不能沿用旧基线通过结果 |
| 1 | #43 定稿本契约；拆 runtime／GUI／QA 实施任务 | 真实三线 ACK 与最终独立提案评审齐备；#43 decision 已据证据关闭，状态与实现事实随 #60 同步 |
| 2 | [#60](https://github.com/HeimaoLST/Saki/issues/60) Runtime 机器入口、关联、历史与取消；无 GUI 真实闭环 | 基于合入后的 #47；只改 runtime 路径和必要接线，默认 TUI/console 保持；最终 SHA 离线及真实 smoke 通过 |
| 3 | [#63](https://github.com/HeimaoLST/Saki/issues/63) GUI 使用真实入口替换 demo；[#64](https://github.com/HeimaoLST/Saki/issues/64) CI 工程 | GUI 负责人实施并真实试用，主 agent 评审／运行验收与 required CI；首轮完成不意味着后续并发／检索完成 |
| 4 | [#61](https://github.com/HeimaoLST/Saki/issues/61) 同宿主多 session 重叠执行与失败隔离 | 独立 runtime 实施项，保留同一个机器契约和同一个 Agent；A 活动时 B 创建／加载／提交，事件不串，A 失败不结束 B |
| 5 | [#62](https://github.com/HeimaoLST/Saki/issues/62) compact 后 search_history／read_history | 独立 runtime 实施项；复用 journal 与 ToolRegistry，默认当前 session／绑定范围，返回来源与记录位置，不设向量库前提 |
| 6 | #31 follow-up／steering 策略与实施 | 复用 task 接纳边界；推荐先显式 FIFO follow-up、默认 busy 不变；队列、取消、关闭策略定稿后实施，steering 单列取舍 |
| 7 | #32 会话内模型切换 | 定义候选模型配置与容量、闲时切换／失败保持、持久化及 API continuation 兼容；首版优先同 API 已配置连接，跨协议不强行迁移 |
| 8 | #33 已应用变更的权威工具结果 | 先设计 edit 结果中的结构化 before/after 或 change；仅报告真实应用结果，失败不伪造 diff，TUI 消费需 ACK |

阶段 4／5 是完整 runtime 需求的一部分，不因首轮 GUI 可运行就取消；每阶段单独的可验收 issue／PR，避免 #43 混入所有代码。阶段 6–8 复用已有 decision，不另造重复议题；实现顺序可在阶段 2 后按真实使用与 PM 排期调整。M2/M3、新厂商协议、OAuth、订阅授权、长期身份和成长机制不在当前队列的默认实施范围。

阶段 2 的最小机器适配代码放 `src/session/`，入口只在 `src/main.rs`／`src/app.rs` 接线；共享配置与单任务执行辅助函数先检查已有实现再提取。首轮和后续并发共用按 session_id 管理的状态／调用边界，不先做一个 GUI 专用全局当前会话协议，再在阶段 4 换掉。历史和取消属于首轮必需，不能降为可选切片；不通过猜测“第一条 ContextUpdated”充当 task_started，也不生成与 journal 无法关联的临时 task_id。

## 验收矩阵

| 场景 | 首轮必须证明 | 后续独立证明 |
| --- | --- | --- |
| 普通闭环 | 受控模型→真实临时文件 read→结果回传→最终回答；响应／任务／工具关联正确，终态唯一 | 并发 A/B 各自闭环、交错事件归属 |
| 流式显示 | 收到 delta 后受控服务端才允许 final；相同 item_id 不重复追加最终全文；异常断流失败 | 不同任务复用 item_id 也不串记录 |
| 查看 B | A 执行工具时 list/history/load B，不取消／替换 A；首轮向忙碌执行器提交明确 busy | A/B 同时执行，创建／加载 B 不受 A busy 阻塞 |
| 保存与恢复 | 跨进程恢复同一 session、继续前文；仅 load/history 的模型／工具计数为零 | 两个独立上下文、失败隔离和各自写锁 |
| 错误 | 无效 session、损坏／锁定历史、API 不匹配有明确错误；工具业务错误能回传模型 | A 失败不错误结束 B；容量拒绝与同 session 冲突按定稿策略 |
| 取消竞争 | 受控协调 cancel 与 finish 两种顺序；恰好一个终态；错误 task_id 不取消别的任务 | 队列任务与活动任务的定向取消、FIFO 不重排 |
| 退出／断连 | EOF 等待；输出通道丢失不伪造成功；重启不重跑工具；接纳不确定时不自动重试 | 如需 durable queue／幂等重连，另开契约，不能冒用首轮证据 |
| 历史检索 | 历史由同一个 reader 提供，GUI 不复制 parser | 多次 compact 后原文可 search/read，来源可定位，绑定范围外无泄漏 |
| 配置与兼容 | 缺少配置不开始；旧终端接口／session 可用；两种已实现模型协议回归 | 模型切换失败保持原配置；权威 diff 与实际文件变更一致 |

所有并发／取消测试用 oneshot、barrier 或受控 HTTP fixture，不靠 sleep 碰运气。Rust 改动跑 fmt、strict Clippy、cargo test、growth_experiment；发布或跨端验收按仓库门禁补 Go。若 #55 的 CI／required check 已启用，必须取得实际候选的检查结果，不能用本地通过替代必需 CI。每个实现 PR 记录精确最终 SHA、退出码和适用范围；真实 provider 的文字→工具→回答 smoke 与离线 fixture 证据分开，未实测组合不宣称兼容。

## 兼容性、回滚与定稿要求

新增入口和封套，不改变现有终端请求／事件调用方；确需新增底层事件时先取得调用方 ACK。保持旧 session／journal 可读，新增关联字段用可选字段承载，不改变旧记录的含义。先核实共享 helper、实验独立编译接线及 #47 的实际集成状态，不在多个分支重复实现相同职责。

撤回机器入口不删除会话数据；回滚代码前核对新增 metadata／journal 字段的兼容性，不保证旧版本支持新增 API 的会话。不移除旧协议或用户能力；出现实质 breaking change、破坏性数据迁移或产品方向争议时单列人类决策。普通兼容实施按用户已建立的 dev 合并授权执行，仍需独立评审和适用验收。

定稿需要 GUI 确认 Go 消费、标识／生命周期和联系人映射；TUI 确认兼容边界及 #31 的承接；PM 确认首轮／后续切分；独立评审检查失败与恢复语义。ACK／审批只记录实际发出者与所审 SHA，不能由作者代填。实施完成后将本文相应部分改为实现事实，并链接真实证据；规划通过不代表代码已交付。


## Runtime 服务化改进方案

更新日期：2026-10-21。分类：**分阶段设计，#103 已完成内容复审；不代表各阶段已实现。** 本节是服务化设计与验收依据；实际交付以对应 PR 和下文“#104 候选实现事实”为准。保留上文 stdio v1 契约，任务认领与排期继续放 GitHub。

本轮按 [#103 初审](https://github.com/HeimaoLST/Saki/issues/103) 补充旧连接身份绑定、端到端背压及 MCP 版本协商/重新发现规则，并扩展对应验收子场景。[复审已 APPROVE 设计内容](https://github.com/HeimaoLST/Saki/issues/103#issuecomment-6317323474)，用户随后授权从 #104 开始实施；配置、并发、MCP 和终端迁移仍按各自任务交付。

目标是让 GUI、TUI 和其他产品通过同一服务管理 agent 执行。产品提供配置和输入，服务负责模型与工具循环、会话上下文、压缩、历史、任务状态及取消。首个交付指标是 GUI、TUI 都成为这套服务的消费者；仅复用 Agent 类不足以达标。

先统一 session/task 所有权；之后同连接配置与多 session 并发可以分别交付，增强配置再承接外部工具扩展。输入资料作为独立能力，不必等待 MCP。每步都保留可用的默认配置和真实消费方回归，不以完整插件框架作为前置。

### 事实基线与已有工作的关系

实现事实固定到 dev 的快照 **47d01ee83972f8459167503001656a12f9f87c6c**。初稿编写时网络同步失败；本轮已读回 #103，评审者报告其核对的远端 dev 与此相同，#32/PR #68 仍为文档提案、#61 尚未实施。该远端核对属于评审者报告，负责人开工时仍须核对最新代码与在途交付，已有实现优先复用。

| 待解决的问题 | 已核实的实现事实 | 本方案的交付目标 |
| --- | --- | --- |
| 服务实现分叉 | [app.rs](../../src/app.rs) 的 stdio 分支使用 Runtime；默认终端分支使用 SessionManager；两者共享 Agent、store、journal | session/task 管理只有一份实现，stdio 与进程内调用使用同一服务 |
| 配置仍以进程为中心 | [runtime.rs](../../src/session/runtime.rs) 的 create 只接收 cwd/prompt；[session/mod.rs](../../src/session/mod.rs) 恢复 metadata 中的 model/compaction | 同一服务中不同 session 可选不同已配置连接、模型、提示词、工具和压缩参数；空闲时原子更新 |
| 全局只有一个任务槽 | Runtime 的 active 是单个 Option；A 活动时 B send 返回 busy | 不同 session 可重叠执行，同一 session 保持一个任务，满容量明确拒绝 |
| 服务缺少定制入口 | [ToolRegistry](../../src/tools/tool_registry.rs) 提供 Rust 注册能力，机器接口未提供外部工具配置 | 产品能为 session 选择外部工具，无需修改或重编译 Saki |

2026-10-21 的本地验收中，148 项 Rust 测试、5 项真实 Rust 进程消费方集成、Python stdio 自测以及取消/压缩探针通过。模型由受控本地 fixture 提供，不能替代线上模型或两端产品试用。任务 journal 写失败、接纳响应丢失的专项故障注入仍需补齐，列入下方共同验收。

本地 #32 草案位于 commit **ce745eea0938d56f6d5f370f945d8886c1fe0f91** 的 docs/design/model-runtime.md，提出同连接模型切换、容量验证、原子提交和 opaque continuation 拒绝规则。本方案沿用这些约束，扩大到统一 session 配置；不另实现第二套 select-model 状态机。该草案尚不能视为已批准接口。

#31 follow-up/steering、#62 历史检索、#33 已应用编辑结果保持独立范围。多 session 并发不顺带实现同 session 隐藏队列。若接手时 #31 已实现，应把其 pending 状态纳入配置锁定与容量规则，并重新取得契约评审，不能按本节删除已有能力。

### 服务对象与职责

首版以 Session 和 Task 为执行实体。可复用的 Agent 定义暂由产品保存为创建参数模板；没有复用需求前不增加 Agent CRUD、角色数据库或常驻 agent 进程。

| 对象 | 服务拥有的事实 | 产品或宿主的职责 |
| --- | --- | --- |
| HostConfig | 已注入的连接目录、外部工具目录、默认配置、运行容量 | 本地宿主/未来部署方提供 endpoint、凭据引用、工具启动配置 |
| Session | 稳定 ID、有效配置、配置版本、上下文、历史、写锁 | GUI 保存 contact→session 映射；TUI 保存当前选中对象 |
| Task | 接纳结果、固定配置、输入、取消句柄、事件、唯一终态 | 提交输入、展示状态、主动取消 |
| Connection | 协议、endpoint、可用模型/容量资料和客户端 | 凭据注入与更新；不把 key 传入普通 session 请求 |
| Tool source | 工具描述、连接/子进程、调用和关闭 | 编写业务工具；选择需要开放给 session 的工具 |

Runtime 不接管联系人、头像、草稿、未读、窗口或产品工作流。Runtime 负责人提供 API 与接入说明；GUI/TUI 接线归各自负责人。验收包括消费契约，不要求 Runtime 负责人修改其界面。

~~~mermaid
flowchart TD
    G[GUI 或其他程序] --> J[stdio 适配器]
    T[TUI 与 console] --> H[进程内 RuntimeHandle]
    J --> S[RuntimeService]
    H --> S
    S --> C[Session 配置与状态]
    S --> W[Task 执行与取消]
    W --> A[现有 Agent 循环]
    A --> M[ModelClient]
    A --> R[ToolRegistry]
    A --> V[Conversation 与压缩]
    C --> D[SessionStore 与 journal]
    V --> D
    R --> B[内置工具]
    R --> X[MCP 外部工具适配器]
~~~

stdio 和进程内 API 是两种传输/调用方式，不是两种服务。产品不必嵌入 Rust；未来增加 HTTP 适配器也应调用 RuntimeService。本地 GUI 和 TUI 各自启动一个服务进程，不等于已经实现远程多客户端共享同一进程；后者是后续部署能力。

### 一 统一服务实现

#### 模块划分与迁移边界

建议保留 Runtime 作为服务核心名称，提供可克隆的 RuntimeHandle；图中的 RuntimeService 指这一个 Runtime 实现，不是再新增一层服务。服务拥有 session 表和 task 表，handle 只提交 typed 请求并接收 typed 响应/事件。

- src/session/runtime.rs：session/task 所有权、准入、状态转换、任务完成回收。
- src/session/stdio.rs：从现有 runtime.rs 提取 JSONL 解码、v1 DTO 映射、输出与 EOF 处理。
- src/session/mod.rs：共享构建与公共类型；SessionManager 在迁移期仅作委托适配，最终移除其独立 Agent、store 操作和 worker 管理。
- src/app.rs：组装 HostConfig、连接目录和唯一服务；选择 stdio 或终端适配器。
- src/agent、src/client、src/tools、Conversation、journal：沿用现有职责，不复制模型循环、压缩或历史 parser。

这次拆文件有明确目的：把传输从状态所有权中分离。不增加通用插件容器、分布式调度层或一组只有单个实现的抽象 trait。需要故障注入时优先使用现有行为边界上的替身。

进程内 API 提供与机器接口语义一致的操作：create/load/list/rename/history/status/release、send/cancel/shutdown。返回 typed Result；stdio 将同一错误映射为稳定 code。进程内调用不能通过直接取出 Agent 绕开准入、配置和取消规则。

服务以一个控制循环串行提交状态变更；模型/工具 I/O 在任务 worker 中运行。慢磁盘读写、工具发现及配置准备不得在控制循环中长时间等待；通过带 session 操作标记的准备任务回传结果，提交时再核验配置版本和状态。每个 session 同时最多一个修改操作，其他 session 的取消、状态查询仍可处理。

#### 接纳与事件顺序

1. 服务验证输入、目标 session、配置/工具可用性和容量。
2. 在一次串行提交中占用 session 与容量，生成 task_id/message_id，并固定 ExecutionConfig。
3. 将接纳结果交给对应 adapter，然后开放该 task 的事件转发。stdio 保证 response 排在该 task 的 task_started 前；typed 调用方先获得 AcceptedTask，再读取其事件。
4. task_started 表示 worker 已开始执行。接纳不等于执行完成，也不承诺崩溃后自动续跑。
5. 服务等待 worker 结束，完成持久化结果判断与资源回收；观察通道可用时，排空该任务已入队事件再发布唯一 task_finished。通道失效时按下文规则放弃展示投递，任务状态和落盘收尾不等待输出队列。

所有内部任务事件在产生时携带 session_id/task_id/message_id，不能在消费时用“当前 active”补关联。工具 call_id、模型 item_id 保留原值；去重键必须包含 task_id。不同任务间不承诺全局事件先后，同一任务中工具起止、增量/全文和终态顺序必须稳定。

Task 状态按 accepted → running → completed/failed/interrupted 演进，cancel_requested 是附加状态，不是终态。accepted 状态下允许取消；取消与完成以服务观察到的实际 worker 结果仲裁，不能把已完成任务改写为 interrupted。worker 返回前不能释放其 slot、Agent、工具子进程和文件锁。

session.status 扩展返回当前 task_id、配置版本、活动阶段及 cancel_requested；新增 task.status(session_id, task_id) 查询活动任务或 journal 中的已知终态。磁盘没有终态时返回 unknown，不从最后一条 assistant 文本推断 completed。

#### 终端迁移与默认行为

先在 P1 保持单槽与 stdio v1 线协议，迁移 TUI/console 的 session 操作与取消，再引入后续能力。终端本地命令只负责解析和把选中 session 转为显式 ID。界面可以保留自己的投影与草稿，执行状态以服务响应/事件为准。

自动标题归服务的可选附属作业：保持终端默认启用、stdio 默认关闭；使用派发时的配置快照，手工标题优先，迟到结果只能更新标题。标题作业独立计数，上限为 1，不能占用或修改其他任务上下文，也不能成为 load/history 的副作用。capabilities 分别报告用户任务容量与附属作业容量；标题不占用户任务名额，但其网络/内存消耗纳入整个进程的资源观测。资源满时跳过本次自动标题，不阻断用户任务，该降级行为需终端负责人确认。

**P1 完成条件：** 生产入口不再直接驱动独立 SessionManager worker；两端适配器的相同操作通过同一套契约测试。保留旧类型作转换不算分叉，保留第二份 session/task 状态机则不能通过。

### 二 会话级配置与配置更新

#### 配置归属与发现接口

HostConfig 在本地可由新增 SAKI_RUNTIME_CONFIG 指定的配置文件加载；未指定时，现有环境变量映射为名为 default 的连接和默认 session 配置。运行中的普通 session 请求只选择已配置资源，不提交原始 API key 或任意可执行代码。

建议的宿主配置形状如下。字段是拟议接口，不是当前可用配置；相对路径按该配置文件所在目录解析，启动后固定为绝对路径。

~~~json
{
  "default_connection_id": "default",
  "connections": [
    {
      "id": "default",
      "api": "openai-chat-completions",
      "base_url": "https://provider.example/v1",
      "api_key_env": "SAKI_PROVIDER_KEY",
      "default_model": "model-a"
    }
  ],
  "max_active_tasks": 2,
  "tool_sources": [
    {
      "id": "project",
      "transport": "stdio",
      "command": "/absolute/path/project-tools",
      "args": [],
      "env_from": ["PROJECT_TOKEN"]
    }
  ]
}
~~~

连接目录首版在启动时建立，运行期不重绑已有 connection_id。同一服务可同时注入多个连接；每个 session 独立选择，不再共享一个可变“当前模型”。连接配置中某项无效应在初始化时明确报错，不能伪造 ready；暂时无法访问外部工具可以标为 unavailable，其引用校验见扩展章节。

新增只读接口：

| 方法 | 返回与约束 |
| --- | --- |
| runtime.capabilities | 当前协议能力、可用方法、容量、配置/工具功能及版本；ready 可附同一能力摘要 |
| connection.list | ID、API、endpoint 的可展示信息、默认模型和已加载容量来源；不返回凭据 |
| model.list(connection_id) | 已加载 catalog 和宿主显式模型配置；不隐式发起远端刷新 |
| session.config.get(session_id) | 当前有效配置、config_revision、不可修改字段、storage_schema；未 loaded 也可只读查询 |
| tool.list(source_id?) | 发现/缓存的工具描述、来源与可用状态，不执行工具 |

model 是实际发给 endpoint 的模型 ID；catalog_id 仅用于容量查找。列表未列出不直接等于模型不存在：自定义模型提供有效的显式容量后可配置，实际可用性由真实调用验证。

SessionConfig 包含 connection_id、model、容量来源及有效 CompactionConfig、system_prompt、working_directory、tool_selection。配置默认值仅在创建时展开；后续进程默认值变化不能悄悄修改已有 session。working_directory 首版只在创建时设置，改变目录要求新建会话，避免同一历史中的相对路径含义漂移。

对于已显式绑定的会话，连接非秘密身份由 API、规范化 endpoint 和宿主配置 ID 确定，随 session 保存。恢复时若同名连接变成另一 endpoint/API，返回 connection_mismatch，不把旧会话悄悄交给新 provider。凭据轮换不改变连接身份；凭据由宿主重启/重新注入，不写入 metadata、journal、错误详情或事件。

#### 旧会话连接身份未知时的恢复与绑定

schema 1 只保存 api/model，没有 endpoint 或 connection_id，无法从模型名、当前 default、catalog 或已有消息可靠还原来源。对这类会话返回 connection_binding.state="unknown"、connection_identity:null；如果兼容运行使用了当前连接，另返回仅供本进程执行的 execution_connection_id，不能把它写成已验证的历史身份。

| 场景 | 恢复与执行规则 |
| --- | --- |
| 宿主仅配置一个连接，且 API 与 schema 1 相同 | 保留既有 legacy load/send 路径，以当前连接执行；身份仍为 unknown，不自动升级/落盘绑定，不宣称历史 endpoint 相同。P2a 只可在该进程连接内切模型，仍执行 opaque continuation 检查 |
| 宿主配置多个连接，即使同 API 候选只有一个 | load/send 返回 connection_binding_required；不按 default、模型名或候选数量猜测。list/history/config.get 仍只读可用，不触发模型、工具或修复旧 journal |
| schema 1 首次启用增强配置或升级到 schema 2 | 必须显式选择 connection_id，并确认历史来源未知；按下述原子绑定流程处理 |
| schema 2 已有完整身份 | 使用身份匹配规则；不能因缺少已绑定连接退回 default/legacy |

为避免“未绑定不能 load，而 update 又要求已 load”的死结，session.config.update 增加一个窄例外：对未 loaded 的 schema 1 允许仅绑定连接并升级。该请求只接受 patch.connection_id、expected_revision、allow_schema_upgrade:true 与 acknowledge_unknown_origin:true；其他配置修改留到绑定后。示意：

~~~json
{
  "version": 1,
  "id": "bind-1",
  "method": "session.config.update",
  "params": {
    "session_id": "legacy-session",
    "expected_revision": 0,
    "allow_schema_upgrade": true,
    "acknowledge_unknown_origin": true,
    "patch": { "connection_id": "selected-connection" }
  }
}
~~~

绑定复用配置准备/提交函数：取得该 session 写锁后，重新读取 metadata/revision，以只读模式解析 journal，不先截断/修复尾部；核对选定 API 和结构兼容性，再备份并原子发布 schema 2 metadata。失败保持原 metadata/journal 并释放临时锁，锁冲突返回 locked。未 loaded 的绑定完成后仍保持未 loaded，由调用方显式 load；已 loaded 的兼容会话只允许在空闲时绑定，commit_unknown 时停止接纳并要求对账。

绑定不能证明旧 endpoint。schema 2 记录 binding_origin="explicit_legacy_binding" 及 bound_at_revision；该身份约束只覆盖绑定后的运行。已有 task 的连接字段保持未知，不倒填过去。同 API 且只含公共文字/已配对工具历史可绑定；含 ProviderItem/provider_data 的未知来源历史返回 continuation_not_supported，即使调用方声明“应该是同一个 provider”也不绕过。此时保留单连接 legacy 使用，或新建增强会话；跨 API 返回 api_mismatch。

这些约束只阻止 P2b 的不确定身份迁移，不阻止 P1/P2a/P3。保留 legacy 运行是兼容性取舍，不是新提供的连接身份保证。

#### 公共操作与版本冲突

保持现有 session.create(working_directory,system_prompt) 可用；新能力通过可选 config 字段加入，不能同时传顶层 working_directory/system_prompt 与对应 config 字段。相同含义重复提供时返回 invalid_request，避免隐含优先级。

增强会话创建示例（P2b 的拟议接口；工具选择省略时仍使用四个内置工具）：

~~~json
{
  "version": 1,
  "id": "create-1",
  "method": "session.create",
  "params": {
    "storage_schema": 2,
    "config": {
      "connection_id": "default",
      "model": "model-a",
      "catalog_id": "provider/model-a",
      "working_directory": "/absolute/path/project-a",
      "system_prompt": "You help maintain project A."
    }
  }
}
~~~

create/load/config.get 的增强返回包含 session_id、metadata、effective_config、config_revision 和 storage_schema，旧字段含义不变。新建会话 revision 从 1 开始；旧会话缺失 revision 时读取为 0，首次成功更新后为 1。capabilities 不宣告支持的字段应在请求校验时明确拒绝，不接受后忽略。

新增 session.config.update，采用字段白名单的 patch。expected_revision 必填；缺省字段保持，null 不代表删除，只有字段明确允许 null 时才接受；列表整体替换。返回完整 effective_config、新 revision 和 effective_from:"next_task"。

~~~json
{
  "version": 1,
  "id": "cfg-1",
  "method": "session.config.update",
  "params": {
    "session_id": "s1",
    "expected_revision": 3,
    "patch": {
      "model": "model-b",
      "catalog_id": "provider/model-b"
    }
  }
}
~~~

模型选择复用 #32 的准备/提交函数。若 session.select_model 在接手时已批准或实现，保留为该函数的窄适配；若尚未定稿，优先通过 config.update 统一入口，与 #32 负责人确认命名，不新增两份独立配置存储。

普通更新只允许目标 session 已加载且无 accepted/running task、无其他修改操作；未 loaded 的唯一例外是上一节定义的首次绑定。B 空闲而 A 运行不构成 B busy。patch 不改变有效值时返回原 revision，不写磁盘；unknown→显式绑定改变持久状态，不按 no-op 处理。revision 不符返回 config_conflict 并给出当前 revision，调用方重新读取决定是否重试，不自动覆盖另一方修改。

#### 验证与原子生效

配置更新按下面的顺序执行：

1. 标记该 session 正在配置，核对 revision、busy、connection 与工具来源；当前配置继续保持。
2. 构造完整候选配置，复用唯一的 CompactionConfig 验证规则；准备客户端句柄、工具描述及新的请求上下文视图。准备失败不修改旧 Agent、metadata、usage 或历史。
3. 在 session 的串行提交中重新核对状态/revision，以现有临时文件、flush/sync、rename 路径持久化配置；不另建配置数据库。
4. rename 提交成功后，以不包含 await 或可失败转换的步骤替换内存配置、更新 revision、失效旧 usage 校准，再返回成功。
5. 若提交后进程崩溃，下次恢复磁盘新配置。若结果在 rename 后无法确认，返回 commit_unknown 并要求 config.get 对账，不谎称已回滚；不能在“不确定新文件是否存在”的情况下让旧内存继续接纳任务。

create 同样先完整验证再发布 session。创建失败不把半成品放进 session.list；若存在部分目录，应标 unavailable 并可诊断，不能偷偷换一个 session 当作成功。

ExecutionConfig 在**接纳时**固定，而不是第一次 HTTP 请求时读取。任务中所有生成、工具调用和压缩使用同一 model/connection/prompt/toolset/compaction。接受后配置更新会被 busy 拒绝，因此接纳与开始之间也不会漂移。task_started journal 可增加非秘密的配置快照及 revision；旧记录缺失时显示未知，不能用当前配置补填过去。

配置更新不得发测试推理、自动 compact 或业务工具调用。外部工具 schema 的准备可触发协议发现，不算业务调用，但必须可超时并保持其他 session 可操作。只降低预算时，下一次任务进入现有 compact-before-generation 路径；压缩失败保持上下文并失败，不自动换回旧模型。

#### 兼容矩阵与持久化

| 变更 | 首版处理 |
| --- | --- |
| 同连接换模型，历史为公共文字/已配对工具记录 | 允许；容量校验通过后，下次任务使用新配置 |
| 改模型且上下文含 ProviderItem 或 provider_data reasoning | 返回 continuation_not_supported；不删除/转写原生数据 |
| 换连接但 API 相同、没有 opaque continuation | 可在 P2b 开放；本地校验只是结构兼容，实际模型能力仍由调用确认 |
| 跨 API 更换连接 | 返回 api_mismatch，建议新建会话；不承诺自动历史迁移 |
| 新模型不支持上下文中的图片/工具形状 | 在可本地确定时拒绝 config_incompatible，不丢附件或历史 |
| 更新 system_prompt | 仅改变未来请求的有效 system 指令，原始 journal 与旧 task 快照保留；使用原生 continuation 时首版同样拒绝不确定的重解释 |
| 移除工具 | 保留过去的工具结果；新任务只暴露新集合，不能重跑旧工具；有无法验证的 opaque continuation 时拒绝 |
| 改 cwd | 返回 immutable_field，需要新建会话 |

system_prompt 当前存于初始 journal，不能仅改 UI 文本。新配置必须成为请求构造与 compaction 的同一来源：恢复原始历史后，以当前配置构造有效 system 前缀，替换执行上下文中的原 system 前缀而非再追加一个互相冲突的 system 消息；该内存更新不改写已持久化消息。compact summary 不得覆盖该前缀。更新提示词并不会清除历史或摘要中的旧事实，这不是“重置 agent”；需要完全不同上下文时新建 session。

模型/压缩参数原本已存在 metadata；同连接模型选择可复用原 schema，保持 #32 的兼容目标。新增 connection 绑定、可更新 prompt、tool selection 等语义时，旧二进制会忽略不认识的字段而按旧配置运行，因此**不能仅添加可选字段后声称可安全降级**。

推荐为这些增强会话使用 metadata schema 2，保留 schema 1 的读取和默认创建：
- 新增强会话显式选择 storage_schema:2；既有会话首次启用增强字段须显式 allow_schema_upgrade:true。schema 1 的未知来源还须完成上述显式绑定；不能在升级时把当前 default 自动填入历史身份。
- schema 2 保留原有 model/api/compaction/cwd 作为各自唯一值，增加 connection_identity、system_prompt、tool_selection、config_revision；不复制同一字段到两个配置对象。
- 升级在旧 session 写锁内完成，先留一份只用于回滚的 schema 1 metadata 备份，再原子发布新 metadata；不改写历史。
- 旧二进制应明确拒绝 schema 2。该持久化格式决定属于需要确认的兼容性变更，不能从“请编写方案”推导成已获迁移授权。
- 升级后未执行新任务且 journal 未变化，才允许按记录回退 metadata；有新执行后保留数据并使用支持 schema 2 的版本，禁止直接覆盖旧备份来伪造无损回滚。

新 journal 字段优先附在已有记录类型上。若实现必须新增旧 reader 不认识的 record，应另行版本评审；不能把 schema 2 metadata 方案当作任意改 journal 的授权。

### 三 多 session 并发与生命周期

#### 调度规则

由全局一个 active 改为按 task_id 管理活动任务，并维护 session_id→task_id 的唯一关联。只在存在实际工作时创建 worker，保存一千个历史会话不代表启动一千个线程/进程。

- max_active_tasks 为正整数，默认先保持 1，显式配置 2 或更高启用重叠执行。发布时是否提高默认值单独决定。
- 同一 session 已有 accepted/running task：busy，reason=session_busy。
- 服务任务容量已满：保持 v1 顶层 code=busy，增加 reason=capacity_exceeded；不创建 task、不写用户消息、不启动 worker、不隐藏排队。
- 被拒绝的请求不会在容量释放后自动执行。用户或产品若要排队，必须以后按 #31 的显式契约提交。
- A、B 拥有独立上下文、Agent、取消信号、工具工作目录与输出目录。共享只读连接客户端不等于共享对话状态。
- 容量令牌在 worker 和其子资源完成回收后释放，不能收到 cancel 请求就释放。

控制循环负责“检查+占槽”的原子性。不能先 await 获取信号量而把超额请求变成隐形队列。会话表只保存执行控制所需状态，worker 把完成结果及 Agent 归还，崩溃任务也必须归还容量。

子资源存在有界取消策略：网络流/协作工具及时响应取消；外部进程超过关闭宽限后终止并回收。若清理失败，session 标为 unavailable，不能复用仍有副作用的 worker。对外不承诺已撤回工具产生的外部效果。

#### 故障与事件隔离

每个 worker 使用自己的事件通道或直接发送带完整 Association 的信封。处理 A 完成时只能排空 A 的事件；不得从共享无归属队列中把 B 的消息作为 A 的尾事件发送。

模型错误、工具进程退出、worker panic、journal 写失败只影响对应任务/session，B 继续。Runtime 进程自身崩溃、stdout 丢失或宿主被杀会影响整个实例，这是明确的进程级边界，不能承诺同进程故障隔离。

服务在发送 completed 前确认最终历史和终态已成功写入。写入失败时只发送 failed/io_error，并附 persistence:"unconfirmed"；内存标记不可继续执行，重新 load 时以有效 journal 为准。重启后只有 task_started 而无可信终态则显示 unknown/interrupted-recovery，不自动重放业务工具。磁盘无法写入时不能保证“失败终态已持久化”。

#### 整条事件链的背压与输出失效

基线 runtime.rs 同时存在 Agent→Runtime 和 Runtime→writer 两级 unbounded 通道，Agent::emit 及 compact 的同步 hook 也使用 UnboundedSender。P1 提取事件边界时就应处理这条链，P3 再验证并发。只将最终 stdout 队列换成 bounded 不能通过验收。

| 位置 | 容量与阻塞规则 | 取消和关闭路径 |
| --- | --- | --- |
| 模型流→Agent | 按需拉取；解码 frame、单条响应及 SDK 内部缓冲同样有上限，不建立额外无界 token 队列 | 等待下一块数据和事件容量时均可观察 task cancel/观察通道关闭 |
| Agent/Conversation→任务事件流 | 使用有界、可 await 的 EventSink；容量按条数和 payload 字节计。生产者等待许可，禁止用每事件 spawn、无限 Vec 或重试队列搬走积压 | 等待许可与 cancel/OutputClosed 竞争；不持 session/配置锁等待发送。compact 的 hook 改到可等待边界，不保留无界旁路 |
| 任务事件流→展示适配器 | 独立 dispatcher 按任务公平读取，有界合并缓冲；控制循环不 await 展示发送 | task.cancel/shutdown、worker join/结果回收独立于展示队列；完成信息通过每任务一个结果槽返回 |
| 适配器→writer/终端消费者 | 有界输出、受预算约束的序列化副本和最大 frame；禁止先无限复制再统计。超出 frame 限制按观察通道失败处理，不修改业务历史来适配显示 | writer 有可取消的写入期限；终端消费停止与 pipe 丢失使用相同 OutputClosed 信号 |

P1 的实现必须列出每级队列的条数/字节上限、单生产者未入队 payload 上限、同时活动 worker 数和缓冲高水位观测；总展示内存上界由这些固定预算相加/乘以活动数量得到，不能只报告最终队列的长度。模型上下文、原始工具结果等业务数据另按现有模型/工具限制控制，不混称“全部内存恒定”。

stdin 请求解析、取消 watch、服务关闭 watch、worker 结果回收独立运行。控制 mailbox 和响应缓冲同样有界，但 token 积压不能占用其空间；如果连控制响应保留容量也耗尽，转入 OutputClosed 并保留取消/关闭处理，不能靠停止读取 stdin 来无限等待响应空间。已经写坏的 stdout 无法保证传回 cancel/status 响应；验收观察服务端取消生效和 journal/资源状态，不要求堵塞 pipe 立即返回文本。

取消打断 EventSink 等待后，worker 进入持久化和资源清理，不再 await 新展示事件；已入队事件仍按序消费。若取消收尾中有展示事件未投递，task_finished 增加可选 history_required:true，提示消费方用 history 补全投影。终态通过每个已接纳任务预留的单个完成槽交给 dispatcher，不能为了投递终态再让 worker 卡住。待投递终态仍占用一个准入名额，直到入队或通道失效，避免慢消费者制造无界“已完成但待展示”的缓存；task.status 可以先报告已知终态。

正常连接不静默丢弃业务事件；取消后的投影不完整以及观察通道失效是显式例外。journal 的文本、工具结果与终态由原执行路径写入，展示队列从来不是持久化来源；history_required 不能把失败写盘伪装成“历史已齐全”。

writer 单次写入/flush 的绝对期限建议 30 秒，即使仅写出少量字节也不无限重置；超时、I/O error 或消费者关闭触发一次 OutputClosed：

1. 停止接纳新业务工作，通知所有 EventSink/dispatcher 停止等待展示容量；关停并丢弃尚未交付的展示帧，回收字节许可。仍有数据抵达时只排空/丢弃展示副本，不取消业务 worker。
2. worker 继续模型/工具与 journal 收尾；其完成路径不再访问失效输出队列。已接收的 cancel 保持有效，不因输出失败被清除。
3. 服务独立回收 worker、工具进程及写锁，不 await 已失效 writer 的“全部队列刷完”。writer 必须使用可中断的非阻塞 pipe 写入；仅 abort 包装了不可取消阻塞写的 async future 不算完成。
4. 全部已接纳工作结束后非零退出；只能保证唯一终态事实和至多一次展示投递，不能保证断连调用方收到终态。写盘也失败时，恢复继续显示 unknown/unconfirmed。

shutdown/输入 EOF 停止接纳，保持 writer 消费并等待已接纳任务收尾；等待期间 writer 超时仍触发上述失效路径，不能先停止事件消费者再等待 worker。shutdown 不自动取消，若产品要取消，先发 task.cancel。输出恢复之前不得自动重发工具或业务消息。

此处“有界退出”指展示链不再增加无限等待：受控任务完成或取消清理完成后，最多再等待配置的输出期限及可回收资源关闭时间。默认 drain 仍可能等待一个持续运行的模型/任务；不能声称任意任务都在 30 秒内结束。若产品需要无条件固定期限终止全部任务，须另定义显式强制退出/取消契约，而不能借输出超时悄悄改变 shutdown 语义。

#### 恢复和不确定的接纳

response 丢失不意味着操作未执行。request.id 继续只是关联 ID，不作为幂等键；本方案不承诺 exactly-once 提交。

- 已知 session/task ID：使用 status/history/config.get 核对服务事实，不自动再 send。
- 未取得 create 的 session ID 或 send 的 task ID：消费方保留输入并显示接纳未知；能够从关联事件恢复 ID 时再对账，无法确认则交由用户决定，不能按超时自动重建/重发。
- 故障注入必须覆盖“服务已接纳、适配器丢弃 response”与“服务尚未接纳即断连”两种情况。
- durable submission key、服务端去重与自动重连是后续独立契约；如果产品要求全自动可靠重试，应先补该契约，再把它作为商业托管上线门槛。

### 四 服务调用方可用的扩展能力

#### 首版选择与能力边界

推荐把定制分为三种入口，而不是用“插件”覆盖所有需求：

| 定制需求 | v1 入口 | 谁负责执行 |
| --- | --- | --- |
| 角色、模型、容量、工具集合 | SessionConfig | Runtime 校验并固化到任务 |
| 业务操作/知识查询/项目工具 | MCP tools 外部工具源 | Runtime 调用用户提供的独立工具服务 |
| 每次输入附加资料 | task.send 的可选 context 文本数组 | 产品选择资料，Runtime 统一拼装、持久化并计入上下文预算 |
| 自定义压缩算法、替换 Agent loop、添加全新模型协议 | 后续基于真实需求的专门契约 | 首版保留现有核心实现；不把 Rust SDK trait 当作服务已支持 |

Pi 的官方扩展接口覆盖工具、事件、模型 provider 和 UI；DSH 的 Cordis 架构允许从配置替换服务组件。这说明“可定制”有多个深度，不能把外部 tools 宣称为等价于 Pi/DSH 的全部插件能力。[Pi 扩展文档](https://pi.dev/docs/latest/extensions)、[DSH 架构](https://github.com/deepseek-ai/deepseek-harness/blob/master/docs/architecture.md)。

首版推荐 MCP stdio 工具源：Saki 作为 MCP client，用户程序作为 tool server。Rust Runtime 不需要嵌入 TypeScript；工具作者可使用 Rust 或其他实现协议的语言。采用官方 Rust SDK rmcp 作为接入候选，先验证与当前 Tokio/依赖及目标协议版本兼容，再固定 Cargo.lock。2026-10-21 核对的官方资料已指向 MCP 2026-07-28；不能把旧版本初始化/通知规则直接复制为最新契约。[MCP 规范](https://modelcontextprotocol.io/specification/2026-07-28)、[官方 Rust SDK](https://github.com/modelcontextprotocol/rust-sdk)。

v1 交付只需 tools 发现和调用、错误、取消与子进程生命周期。按实际协商版本实现发现与取消，不宣称支持 resources、prompts、sampling、elicitation、MCP tasks 或任意双向扩展。本服务的 task_id 与 MCP 的请求/任务 ID 属于不同层，不得混用。协议不兼容或未支持的交互请求应明确报错，不假装完成。[MCP tools](https://modelcontextprotocol.io/specification/2026-07-28/server/tools)。

选择代价：外部调用有 IPC 开销、工具进程需要生命周期管理，且不能直接修改 Conversation；收益是扩展安装不要求重编译 Saki，同一调用边界以后可接远程工具。Rust 动态库 ABI、嵌入 JS、Wasm host 暂不引入；若以后有更低延迟或深层执行策略的明确需求，再以该需求单独比较。

#### 工具目录与 session 绑定

tool_sources 由宿主安装和配置；普通产品调用者从 tool.list 选择来源/工具，不向 Runtime 上传 Rust/JS 源码。首版修改来源目录通过宿主重启生效，session 在空闲时选择/移除工具无需重启。

tool_selection 显式列出内置工具与外部工具，示意：

~~~json
{
  "builtin": ["read", "bash", "edit", "write"],
  "external": [
    {
      "source_id": "project",
      "tool_name": "lookup_issue",
      "exposed_name": "project_lookup_issue"
    }
  ]
}
~~~

省略 selection 使用兼容默认四工具；显式空集合表示不开放工具。exposed_name 由调用方声明，Runtime 验证目标模型命名限制与唯一性，不静默截断、覆盖内置工具或在名称冲突时选一个。session config 返回完整来源映射与 schema 指纹，便于前端展示实际生效集合。

每个 loaded session 独立拥有其 stdio 工具源实例，工作目录取该 session 的固定 cwd；共享同一可执行文件不共享其可变进程状态。这是首版简单隔离策略，代价是资源开销。设置独立 max_tool_processes 限额；准备会话/工具配置时超额明确报错并关闭本次新建的实例，不替换其他 session 资源。以后若共享无状态远程工具，再明确并发和隔离规则。

create/load/config.update 在暴露工具前完成启动、协议发现、分页 schema 获取及选择校验。默认启动超时建议 10 秒，工具调用超时建议 120 秒，宿主可逐来源覆盖；取消后的关闭宽限建议 2 秒。超时保护不用于验收中模拟并发顺序。

tools schema 在任务接纳时固定，接纳前按下一节重新发现并核对。活动任务仍用原映射，服务端已撤销或更改的工具返回真实调用错误；协议没有为 Saki 锁住外部实现，不能承诺工具语义绝不变化。已绑定工具的声明发生不兼容变化时返回 tool_schema_changed，要求空闲时显式更新绑定，不能把同名新工具无提示替换进已有配置。

缺少来源/启动失败/无法发现所选工具时，session 不进入“可执行”状态，返回 tool_source_unavailable；其他 session 可继续使用内置工具。history/list/config.get 仍可读取，无需启动外部工具。release 关闭该 session 的工具子进程，shutdown 在任务完成后关闭全部自有子进程。

#### MCP 协商版本与工具重新发现

官方 2026-07-28 契约把工具变更通知限定到显式开启 toolsListChanged 的 subscriptions/listen。客户端要核对 acknowledged 接受的过滤项，并按 subscriptionId 关联 stdio 通知；连接重建后需要重新订阅。2025-11-25 的 tools.listChanged 则采用旧通知流程，没有这个订阅请求。[现代 Tools](https://modelcontextprotocol.io/specification/2026-07-28/server/tools#list-changed-notification)、[Subscriptions](https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/subscriptions)、[旧版 Tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools#list-changed-notification)。

Saki 的首版支持矩阵固定为下面两条分支，其他协商版本返回明确不支持。版本策略由 tool source 的宿主配置选择 modern、legacy 或 auto；auto 只在 discovery 明确不支持/协议定义的探测失败时尝试 legacy，不把认证失败、损坏响应或业务调用失败当成降级理由。实现复用 SDK 的生命周期机制，不自行拼两套半兼容握手。

| 协商版本与来源能力 | 变更监听 | 准入前处理 |
| --- | --- | --- |
| 2026-07-28，声明 tools.listChanged | 发 subscriptions/listen，notifications 仅选择 toolsListChanged:true；收到相应 subscriptionId 的 acknowledged 且过滤项被接受后才标 subscribed | 每次 task.send 准入前仍执行完整 tools/list；通知作为提早失效信号 |
| 2026-07-28，无 listChanged、订阅被拒/未接受/结束 | 标记 polling，不假定仍有推送；不请求 resources/prompts 订阅 | 每次准入前重新发现；不能用旧缓存假装已验证 |
| 2025-11-25，声明 tools.listChanged | 使用 legacy 初始化/通知处理器，接收 notifications/tools/list_changed；不发送 subscriptions/listen | 同样每次准入前重新发现 |
| 2025-11-25，无变更通知 | polling，无订阅 | 同样每次准入前重新发现 |

主动 tools/list 是本方案选择的简化策略，不是 MCP 强制要求。它增加一次只读发现开销，但让正确性不依赖通知是否及时送达，也避免首版实现 TTL/条件缓存算法。tools/list 分页均须完成，按 source、协商版本、授权范围和本次进程 generation 隔离结果；不跨 session 复用外部可变连接缓存。

每个 source 维护有界失效状态：generation 在重连时递增；变更通知只递增 dirty revision/合并为失效标记，不创建无限事件日志。准入准备记录 generation/revision，读取全部页面后重新核对；期间变更则丢弃该批结果、最多重试一次，仍变化返回 tool_source_unavailable（reason=catalog_unstable）。不把不同轮次的页面拼成一个已确认 snapshot。没有通知支持时只能保证一次成功枚举的快照，无法排除远端在枚举后立即改变；后续调用错误必须如实报告。

重新发现只更新来源观察值，不修改 session 已绑定的工具声明。未选择的工具增删不改变 session 配置；已选择工具的模型可见描述、input/output schema 或来源映射变化，比较规范化后的声明指纹，明确要求 config.update 重新确认。只有校验通过才固定任务 schema 并占用任务容量；准备期间其他 session 的任务和取消不受阻塞。

订阅正常结束、断连、SDK 报告 lagged/溢出或协议错误时立刻标 dirty/polling，旧 subscriptionId 作废。若连接仍存活，在下一次准入准备阶段最多重建一次订阅；重建失败但 tools/list 成功则继续明确的 polling 模式。子进程/连接已断开时，不在活动工具调用中自动重启并重试业务操作；当前调用按失败处理，下次准入才尝试一次重建、重新协商、订阅和完整发现，失败则拒绝本次准入。旧 generation 的迟到通知忽略；release/shutdown 取消订阅、关闭 reader，不能让长连接订阅阻止进程回收。

P4 的首个技术验证提交必须固定实际 rmcp 发布版本、Cargo.lock 校验和及 features，并运行两版本的发现/变更 fixture 后再宣告能力。建议最小运行依赖使用 default-features=false，加 client 和 transport-child-process；示例工具的 server/macros 放示例自身依赖，不让 Saki 生产依赖默认打开无关 server/HTTP/elicitation。订阅缓冲必须配置上限，lagged 走上述失效路径。具体发布版本的编译/API 兼容由该提交证明，不能用 main 文档代替版本证据。[rmcp 配置](https://github.com/modelcontextprotocol/rust-sdk/blob/main/crates/rmcp/Cargo.toml)、[rmcp 生命周期与订阅](https://github.com/modelcontextprotocol/rust-sdk#subscriptions)。

#### 工具执行与结果边界

在已有 ToolRegistry 中增加 MCP adapter，不建立第二个工具循环：
1. Agent 依据任务固定 schema 向模型声明工具。
2. 模型发出调用后，registry 使用固定来源映射派发，附 session/task/call 关联到本地 trace。
3. MCP 的业务错误映射为现有工具错误结果，让 Agent 按既有规则处理；协议错误、超时、子进程退出也产生明确工具失败，不能伪造正常内容。
4. 文本/structuredContent 按单一转换规则进入现有 ToolOutput，并遵循当前输出截断/落盘回读边界。首版不支持的音视频或资源形状返回说明，不自动远程抓取或静默丢弃。
5. 大输出复用 session 的 tool-output 目录；只有实际暴露 read 等回读能力时才能给模型可用的回读指令，否则返回明确截断信息，不能声称模型已读到全文。
6. tool_finished 与后续模型请求中的结果必须来自同一结果对象，避免前端看到的内容与模型实际收到的内容不同。

取消通过工具 adapter 传递到受影响 MCP 请求；超过宽限后终止本 session 的工具进程并回收。取消不能回滚工具已经完成的外部副作用。Runtime 不自动重试外部业务调用；即使连接恢复，也不能推测旧调用没有执行。

凭据仅通过宿主指定的 env_from 注入所需子进程，不能把整个宿主环境/所有 provider key 无差别传给工具。stdout 只用于协议，诊断进入 stderr 并沿现有日志规则处理。本地工具在 Runtime 所在机器执行；未来托管服务不会因收到本地路径而自动获得用户电脑文件。

#### 输入上下文与后续扩展

task.send 可增加 context:[{name,text}]，用于当前文档选区、产品提供的项目说明等。它是用户提供的资料，不提高为 system 权限。Runtime 按顺序将资料作为有明确边界的输入部分保存，并纳入同一次 UserMessage、token 估算和压缩；旧 message.text/images 形状保持可用。首版资料随会话历史保留，接口不宣称“仅本次使用后自动删除”。大小超过入口限制则接纳前拒绝，不能静默截断。

通用事件订阅用于观察，不能暗中修改正在运行的配置。以后如需 before_model_request、工具审批或自定义 compactor，应分别定义输入/输出、暂停与取消、超时、失败处理、历史和重放语义，再选传输；不能只暴露一个任意 hook 回调就称为完整 server 插件能力。

MCP 的远程传输可作为托管阶段的工具接入候选，但不等于 Saki 已有远程 Agent Server。本轮没有用户鉴权、租户隔离、计费、远程文件系统或商业 SLA 的实现范围；这些属于本地 v1 之后独立的部署设计。

### 五 分阶段交付与兼容要求

工作包已由 Runtime 负责人在 [#103 的拆分记录](https://github.com/HeimaoLST/Saki/issues/103#issuecomment-6316966600) 建立。以下与该记录对齐，链接用于追踪而非表示已经认领/开工；实时状态和排期以对应 issue 为准。

| 工作包 | 具体产出 | 依赖 | 退出门槛 |
| --- | --- | --- | --- |
| P1 服务核心 [#104](https://github.com/HeimaoLST/Saki/issues/104) | typed handle、stdio adapter、单一状态/事件来源及整条事件链背压 | 最新 dev 与两端契约核对 | U01–U05 的核心部分、D04/D05、N06/N07 的单槽组合 |
| P1 终端消费 [#109](https://github.com/HeimaoLST/Saki/issues/109) | TUI/console 正式委托与迁移，由终端负责人交付 | #104 | U02–U05 与实际终端回归；GUI 原传输保持兼容 |
| P2a 同连接配置 [#105](https://github.com/HeimaoLST/Saki/issues/105) | config.get/update、revision、模型/容量原子更新，设计复用 #32/PR #68 | P1 服务核心 | 适用 C01–C06、C09；单连接 legacy 路径保持可用 |
| P2b 增强配置 [#106](https://github.com/HeimaoLST/Saki/issues/106) | 多连接显式绑定、prompt/tool selection、schema 2 迁移 | P2a；身份/格式/消费方确认 | C07–C10、D01–D03，包含未知来源子场景 |
| P3 并发 [#61](https://github.com/HeimaoLST/Saki/issues/61) | per-task worker/事件/取消、容量拒绝、全任务 shutdown | P1 服务核心；与 P2 快照类型对齐 | N01–N08，尤其满队列与取消/EOF/断连组合 |
| P4 外部工具 [#107](https://github.com/HeimaoLST/Saki/issues/107) | 固定 SDK/协议矩阵、MCP adapter、发现/订阅、Rust 示例 | P2b、P3 的生命周期 | E01–E06/E08，包含两版本及失效恢复场景 |
| 独立输入能力 [#108](https://github.com/HeimaoLST/Saki/issues/108) | context 资料输入、预算与历史 | P1 服务核心，不依赖 MCP | E07 |
| P5 联合验收 [#103](https://github.com/HeimaoLST/Saki/issues/103) | 两端同契约、真实 provider、异常恢复和使用说明 | 上述必要交付 | 37 项主验收及其必需子场景通过，适用门禁通过 |

P1 的核心与终端迁移共同构成服务化 MVP；P2/P3 形成可供多个 agent 会话使用的本地服务；P4/P5 才能声称四项问题按本方案解决。仅在 Rust 单测中注册一个新工具不算 P4 完成；仅迁移 stdio 而 TUI 仍直接驱动旧 manager 不算 P1 完成。P2a 与 P3 可分别推进；P4 不是 GUI 并发前置，实际共享源码写入仍需安排顺序。

P2b 先实现配置模型和内置工具选择，外部来源在 P4 才开放；P2b 阶段收到 external 绑定应返回 unsupported_feature。P4 复用已有配置准备/提交边界，不为外部工具另建 session 配置服务，因此两阶段没有循环依赖。

P1 不改变 stdio v1 的方法含义或默认执行容量。后续新增方法/字段先通过 capabilities 发现；旧客户端请求继续有效，新客户端连接旧 Runtime 时展示 unsupported_feature，不静默降级成错误配置。保持现有 busy/api_mismatch 等稳定 code；新增 reason 和新方法的错误有单独契约测试。

持久化 schema 2 是明确的兼容评审点。开发可先交付兼容的 P1/P2a/P3，不能为等待它停下独立工作，也不能在未确认时自动迁移用户会话。GUI/TUI 新能力接线与 Runtime 服务 PR 分开审查，最终使用相同候选版本联合验收。

建议 Runtime 修改范围：src/session、src/app.rs、src/agent 的配置/结果边界、src/client 的连接组装、src/model 的已加载容量、src/tools 的 MCP adapter、对应模块测试与 examples。必要依赖修改 Cargo.toml/Cargo.lock。src/frontend、src/command、desktop 的消费接线需交给相应负责人；保留调用示例和错误处理清单。

### 六 确定性验收条件

每项记录候选 SHA、命令、退出码、原始输出、实际执行数和适用入口。PASS 表示行为已执行验证；静态检查单列 REVIEWED；缺夹具为 UNVERIFIED；尚未实现为 GAP。不能用之前的 148 项通过替代改造后的运行结果。

通用夹具使用受控模型 HTTP 服务、真实 Runtime/SessionStore、临时文件和真实工具适配器。每个等待点使用 channel/barrier/明确握手，timeout 只用于失败保护。除真实 provider smoke 外，禁止把模型“回复正确”作为工具确实执行或配置确实生效的唯一证据。

#### 统一服务

| ID | 场景与操作 | 必须观察到的结果 |
| --- | --- | --- |
| U01 | 对 typed 与 stdio 运行同一 create→send→read→finish→history 脚本 | 错误/状态/配置语义一致，真实工具 marker 出现在下一次模型输入；接纳在 started 前，终态唯一 |
| U02 | GUI adapter 与 TUI adapter 分别消费同一服务能力 | 均使用服务 task_id/message_id；无自建模型循环、历史 parser 或独立任务状态机 |
| U03 | A 执行期间查看/切换界面选中 B | A 不被取消；history/list/load 零模型/工具调用；选择行为不改变服务“当前会话” |
| U04 | 默认启动、显式恢复、rename、console/TUI 取消与自动标题回归 | 原有能力可用；手工标题不被迟到作业覆盖；标题不计作用户任务终态 |
| U05 | 相同 item_id/call_id 在不同任务中出现，且 final 覆盖 delta | 以 task 关联投影，无重复正文或串流；代码走查确认无第二份 worker 管理 |

#### 配置

| ID | 场景与操作 | 必须观察到的结果 |
| --- | --- | --- |
| C01 | A/B 用不同模型、prompt、cwd、预算创建并执行 | 捕获实际请求与 read 路径，各自匹配 effective_config；B 的更新不改 A |
| C02 | 同一 session 做 A→B→A 模型选择、释放、重启恢复 | 下一任务模型与预算匹配，恢复读取持久配置；旧工具不重跑，历史字节不重写 |
| C03 | 受控暂停已接纳任务，尝试更改其配置；另更新空闲 B | A 返回 busy 且请求快照不变；B 成功；接纳到开始间也不能变配置 |
| C04 | 两个调用方使用同一 expected_revision 更新 | 只有一个提交成功，另一个 config_conflict；无部分合并或覆盖 |
| C05 | 无效/未知容量、图片不兼容、opaque continuation、跨 API | 返回对应错误，旧配置、usage、上下文、磁盘保持；不降级、不丢附件 |
| C06 | 降低合法预算后发送；控制 compaction 成功/失败 | 摘要与生成使用新模型/预算；失败保留上下文；更改配置本身零推理调用 |
| C07 | 修改 prompt 后继续、compact、重启；读取旧历史 | 新请求有效 system 一致，旧记录保留；旧摘要内容不被宣称已清空 |
| C08 | 多连接路由、已绑定身份匹配；schema 1 身份未知时恢复/绑定 | 不猜旧 endpoint；单连接 legacy 明示 unknown，多连接要求显式绑定；只读历史可用，失败不改旧文件，具体子场景见下 |
| C09 | metadata 写入前失败、rename 前失败、rename 后崩溃/结果不确定 | 提交前保持旧值，提交后恢复新值，不确定时禁止按旧值继续执行并允许对账 |
| C10 | 工具绑定准备失败/no-op 更新/cwd 更新 | 失败释放临时资源且旧绑定不变；no-op 不增 revision；cwd 返回 immutable_field |

#### 并发和生命周期

| ID | 场景与操作 | 必须观察到的结果 |
| --- | --- | --- |
| N01 | 容量 2，分别提交 A/B；模型 fixture 等到两请求均到达才释放 | A/B 确实重叠，不能靠先完成 A 再启动 B 通过 |
| N02 | A/B 占满时再提交 C；同时向 A 重复 send | C busy/capacity_exceeded；A busy/session_busy；无新 task/journal 用户输入，释放后不自动执行 |
| N03 | A/B 交错产生正文、同名工具与压缩事件 | 全部事件按完整关联路由；A 完成排空不会吞掉 B 事件 |
| N04 | 取消 A，重复/错误 ID/迟到取消；B 继续 | A 一个真实终态、资源收尾后释放容量；B 不被取消；完成先赢时保持 completed |
| N05 | A 模型失败、worker panic、工具崩溃；B 正常完成 | A 明确失败且回收资源；B 结果/历史完整；故障 session 不带损坏内存继续运行 |
| N06 | 多任务活动时 shutdown/EOF，分别在输出正常/满队列/断开下执行 | 停止接纳；结果回收与持久化不依赖展示消费者；完成/取消清理后在界限内释放锁与退出，不自动重发/伪造取消 |
| N07 | 让整条事件链填满，再组合 cancel/status/EOF/writer 超时与断连 | 每级条数/字节上界均成立；控制继续处理；OutputClosed 唤醒生产者并停止展示等待，不用最终队列有界代替整链证据 |
| N08 | 创建/列出大量空闲会话，执行、release、再 load | 空闲会话不产生后台模型请求或常驻 worker；任务/工具进程数受配置限制；锁释放后可重新获取 |

#### 扩展

| ID | 场景与操作 | 必须观察到的结果 |
| --- | --- | --- |
| E01 | 固定同一 Saki 二进制，安装示例 Rust MCP 工具，按配置选择后执行 | Saki 二进制 hash 不变；真实外部工具读取唯一 marker，结果进入模型下一次输入并被历史记录 |
| E02 | A 绑定外部工具，B 未绑定/绑定另一实例 | B 的模型工具声明不包含 A 工具；schema/进程状态/凭据环境不串；禁止未开放工具调用 |
| E03 | 名称冲突、非法 schema、未知 source、工具进程容量满 | 创建/配置阶段明确失败；没有覆盖内置工具、部分生效或无声隐藏队列 |
| E04 | 业务 isError、协议错误、超时、进程退出、大输出 | tool_finished 与模型实际输入一致；错误可诊断；大输出有可用回读或明确截断，不伪造全文 |
| E05 | 外部工具受控阻塞时取消 A；B 同时调用其独立工具 | A 传递取消并按宽限回收；B 不受影响；已执行副作用不自动重试 |
| E06 | 两协商版本的通知/订阅、无通知、订阅失效与重连、分页中变更 | 版本分支正确；现代订阅确认/关联正确；每次准入重新发现；活动任务固定 schema，变更经显式确认才采用 |
| E07 | task.send 附 context；经 compact 与恢复后继续 | 输入资料计入预算、保留来源边界；不会提升 system 权限或重复注入；旧请求仍兼容 |
| E08 | release/shutdown/失败配置；另缺失外部工具后只查历史 | 自有工具进程全部回收；历史查询不启动工具；没有残留会话锁或泄露 provider key |

#### 持久化与消费方失败

| ID | 场景与操作 | 必须观察到的结果 |
| --- | --- | --- |
| D01 | 单连接 schema 1 兼容 load/send；多连接只读与显式绑定升级 | legacy 保持身份未知；无默认猜测或隐式迁移；升级保留历史/备份并仅约束绑定后的连接 |
| D02 | 旧二进制读取增强会话；升级前后注入崩溃 | 旧二进制明确拒绝；恢复选择完整旧/新 metadata；不存在一半字段新一半旧 |
| D03 | 升级后已有新任务，再尝试按旧 metadata 回退 | 拒绝不安全覆盖，保留历史；不能通过删除新任务来宣称回滚成功 |
| D04 | task 开始/消息/工具结果/最终终态各写入点确定性失败 | 不返回 completed；无可靠持久终态时标 unknown/unconfirmed；不自动重跑已执行副作用 |
| D05 | 服务接纳后丢响应，以及接纳前断连 | 消费方保留输入、标不确定，不自动重发；已知 ID 能用状态/历史对账，未知 ID 不伪造失败 |
| D06 | GUI contact→session 映射保存失败 | 保留草稿、零 task.send/模型请求；现有先保存再发送的集成继续通过 |

D04 优先用 store/journal 的可注入写入失败边界；chmod 在不同权限环境下不可靠，不能作为唯一夹具。D05 可以由代理消费响应而不转交调用方实现，真实服务不能替换为只返回固定 JSON 的假实现。

#### 初审补充的必需子场景

下列是既有验收 ID 的展开，主矩阵仍为 37 项。实施证据必须逐个覆盖，不能以总项数不变跳过。

**C08 与 D01–D03 的连接身份场景：**

- C08a：用没有 endpoint/connection_id 的原 schema 1 文件和一个同 API 连接 load/send；返回 unknown 和本次执行连接，原 metadata 不出现推测身份，历史任务不倒填。
- C08b：配置两个同 API、不同 endpoint 的连接，并交换 default；两次 load/send 均为 connection_binding_required，模型/工具计数为零；list/history/config.get 可读且文件字节不变。
- C08c：从未 loaded 状态显式绑定所选连接并升级；只做该绑定，不发送 HTTP/工具；重新 load/重启仍用已绑定身份，更改其 endpoint 后明确拒绝。
- C08d：缺少未知来源确认、revision 冲突、锁冲突、跨 API、opaque continuation、metadata 提交失败分别拒绝；不改写原 journal，不自动转成另一个 session。提交后结果未知仍按 C09 对账。
- C08e：绑定后的新任务记录实际身份，旧任务保持未知；新任务写入后禁止用 schema 1 备份覆盖回滚。单连接兼容与多连接拒绝分别测试，不能混称所有旧会话都可自动恢复执行。

**N06 与 N07 的整链背压场景：**

- N07a：用可控 writer 暂停消费，持续生成超过所有队列预算的事件，直到生产者明确报告等待容量；记录模型解码、Agent、dispatcher、writer 及 payload 字节高水位。此后处理 cancel/status，不能依赖 writer 先恢复；blocked pipe 上是否返回响应与服务是否执行控制分开判断。
- N07b：在 N07a 状态下注入输入 EOF/runtime.shutdown；受控模型/工具结束后保持 writer 不消费，推进虚拟时钟到写入期限。观察 OutputClosed、全部 sender 解除等待、工具/锁回收及非零退出，不能留下等待失效队列的 join。
- N07c：分别在生产者等待容量、取消清理、终态待投递时关闭 writer；journal 保留真实结果，不重复终态、不重跑工具；展示可能未达，不能为了断连强造 interrupted。
- N07d：writer 恢复且未超时时，已入队事件仍按任务排序；取消导致 history_required 时消费方从 history 补全。另测试控制响应缓冲满与 compact hook 的事件生产，避免只覆盖普通文本 delta。

所有组合先用握手证明队列已满/worker 已进入目标边界，再推进取消或虚拟时钟。退出时限从受控任务完成或取消清理完成开始计算，并包含配置的 writer 期限；不拿无限模型执行来偷换 shutdown 的非取消语义。

**E06 与 E08 的 MCP 版本场景：**

- E06a：2026-07-28 来源只有收到 toolsListChanged 订阅后才发通知；验证 acknowledged 过滤项、subscriptionId 匹配、仅订阅工具类别，以及通知确实使已发现列表失效。
- E06b：来源未声明 listChanged、拒绝订阅或 acknowledged 未接受过滤项；变更工具后不发通知，下一次 task.send 仍执行全部 tools/list 分页并拒绝未确认的 schema，不沿用旧缓存。
- E06c：2025-11-25 fixture 走 legacy 通知；断言客户端没有发送 subscriptions/listen，同时通知与主动重新发现均生效。
- E06d：订阅结束/lagged、连接重建、协议重新协商、旧 generation 迟到通知分别验证；重新订阅与全量发现成功前不采用缓存，旧 subscriptionId 不复用；正在失败的业务调用不自动重试。
- E06e：分页读取期间变更两次，验证有界重试后明确拒绝；只改变未选择的工具不修改 session 配置，已选工具 schema/描述变化需显式确认；不把发现通过等同于远端实现被冻结。
- E06f：release/shutdown 时订阅仍存活，reader/订阅/子进程全部回收；记录实际 SDK 版本、features、协商版本与原始协议 trace。协议版本未知时明确拒绝，不误报工具业务错误。

### 七 最终验收与负责人接手清单

Runtime v1 候选的共同验收流程：

1. 在同一候选服务上建立 A/B：不同 prompt、模型/预算、工作目录和工具选择。
2. 分别通过 GUI、TUI 的正式消费适配器执行消息→工具→下一模型请求→终态，读取同源历史。两端无需使用相同传输。
3. 容量 2 下 A/B 重叠，取消 A，B 正常完成；容量拒绝、错误定向与状态查询符合矩阵。
4. 空闲更新 B，下一任务与重启恢复均使用新配置；不重写旧历史。
5. 使用外部 Rust 工具示例证明无需修改 Saki 即可定制；工具错误、取消和资源清理可观察。
6. 用实际已支持的 Chat Completions/Responses 组合分别做小规模真实 provider smoke，记录模型、协议、请求/工具结果、用量和终态。未具备线上条件时标 UNVERIFIED，不能把离线通过升级为 v1 全面验收。
7. GUI/TUI 负责人确认其消费接线与真实交互无回归；Runtime 负责人只对服务及消费契约负责，不用界面截图代替任务/持久化证据。

代码门禁按仓库当时规则执行：Rust 改动运行 cargo fmt --check、cargo clippy --all-targets -- -D warnings、cargo test 及受影响 examples；消费接线变化运行相应 Go/终端回归与 required CI。既有五项真实 Rust 进程 Go 集成不得因缺少 SAKI_TEST_RUNTIME_BIN 被 skip。纯方案交付只核对设计、来源、链接与示例，不把这些门禁写成已执行。

负责人收到本方案后的第一步是核对最新代码、在途任务和本节差异，随后按工作包建立实施范围。需要确认的公共决策集中在：统一服务事件/错误、#32 与 config.update 的入口关系、增强会话 schema 2 及迁移、MCP 首版支持范围。建议方案和默认选择已写明；只有实际不兼容项或与现有承诺冲突时才升级决策，不为每个内部实现细节重复申请许可。

每个工作包交付时附：最终 SHA、适用验收 ID 的结果、原始日志、实际模型/协议边界、配置与持久化迁移说明、回滚条件，以及 GUI/TUI 接入需要的变更。将本节已实施部分移入上方现行契约或对应模块事实文档，并删除被替代的提案描述，保持一个权威位置。


### #104 候选实现事实与迁移入口

分类：**本 PR 的实现事实，尚未集成到 dev**。#104 提取 typed 服务和 stdio 适配器，保持 stdio v1；#109 尚未迁移终端，旧 SessionManager 保留原执行路径，因此此阶段不能宣布“两端已经统一”。#105/#106 配置、#61 并发、#107 MCP、#108 输入资料均未在本 PR 实现，不升级用户 schema 或推测旧连接身份。

`Runtime::start()` 返回可克隆 `RuntimeHandle` 和服务退出 JoinHandle。`call(RuntimeCommand)` 等待 typed Result；`submit` 以有界 mailbox 立即提交并返回回复接收器，满时 busy，不隐式排队重试。Create/Load/List/Rename/History/Status/Release/Send/Cancel/Shutdown 对应既有 v1 操作；stdio 只解码、关联、转发并序列化，不再拥有另一份 session/task 状态机。慢文件准备最多 8 项，在 blocking worker 运行，控制循环提交结果；同 session 的修改准备期间 Send 返回 busy。

Send 成功得到 `AcceptedTask { association, events }`，关联在接纳时固定。逐次 `events.next().await` 返回 EventPacket，排空后返回唯一 Finished。调用方应持续消费；丢弃未读流或丢失已接纳回复会关闭该服务的观察通道，停止准入并让业务继续落盘。它不是 task.cancel。正常 typed 消费取得 Finished 后释放槽；stdio 必须先成功把 terminal 帧放入有界输出队列，再确认释放。已完成但终态尚未交付时 Status 可以返回 completed，Send 仍可能 busy。

`Shutdown` 或全部 handle drop 停止新业务并 drain；已排队 Cancel/Shutdown 仍处理。默认任务槽仍为 1，同 session 和跨 session 重叠 Send 都返回 busy，不提供隐藏任务队列。运行中 Load 已加载 session 返回其 running 状态。Release 返回操作后的 `loaded:false`，不删除 journal。

持久化错误返回 failed/io_error、`persistence:"unconfirmed"`；该 session 不接受下一条 Send，需显式 Release/Load 按 journal 恢复。Start、用户消息、真实工具结果、Finish 写失败均有按单个 journal 隔离的测试故障注入。故障注入只编译进 Unix 测试；模型错误可复用会话，worker panic 隔离丢失的 Agent。没有可信磁盘终态时不推断 completed，也不承诺失败终态已写盘。

| 缓冲边界 | 默认上限与超限行为 |
| --- | --- |
| typed command / stdio 输入 | mailbox 32 项；每个 typed 命令序列化计数至多 8 MiB，stdio 输入帧至多 8 MiB；超限拒绝，不执行截断前缀 |
| 文件准备 / 独立完成槽 | 同时 8 项准备；1 个活动任务、1 个独立 worker 结果槽和每任务 1 个终态槽；事件堵塞不占控制或完成通道 |
| 模型读取 | 单次 HTTP 响应累计至多 32 MiB，SSE 和 JSON fallback 在交给解码器前检查；超限为模型失败，不自动重放已开始消费的响应 |
| Agent / compact → TaskEvents | 32 项，合计 8 MiB，单事件最多 4 MiB；可 await 的 EventSink，compact hook 同样等待；无逐事件 spawn |
| stdio 控制响应 / 事件输出 | 两条独立队列各 32 项、各 8 MiB；单帧最多 8 MiB（含换行）。先计数再分配序列化副本；控制优先，任务内部事件 FIFO |
| 等待 stdio 回复的作业 | 最多 32 项，包含持续转发任务的作业。容量耗尽转入 OutputClosed；当前已读 Cancel/Shutdown 仍提交，不能无限保留请求 |
| writer | 单帧 write_all + flush 共用 30 秒绝对期限，部分写入不重置；停止接纳且业务 worker/文件准备收尾后，剩余展示另有总计 30 秒排空期限；超时、断管或超大事件关闭观察通道并释放展示等待 |

事件的字节许可跟随帧直到 writer 处理结束，不能因 dispatcher 出队就提前归还。`TaskEvents::buffer_stats()` 提供当前及峰值 bytes/messages；该计数覆盖持有字节许可的事件，也包括转发中事件。控制与事件输出也以相同 permit 计数机制跟踪。原子快照只用于观测，不是准入依据。确定性测试记录一份出队后仍占许可的事件达到单事件字节预算，第二份发送保持 Pending，取消或 OutputClosed 后释放等待；实际 OS pipe 写满的测试验证原生写入期限。暂停 Tokio 时钟的测试验证：单帧持续有进展也不能重置整个 backlog 的排空期限；业务完成观察独立于 terminal 展示确认。测试只启用 Tokio 的 test-util feature，不改变生产时钟。

默认展示队列字节预算共 24 MiB（事件流 8 + 控制帧 8 + 事件帧 8），另有单活动生产者/转发者持有的有界事件及 JSON 映射副本（各最多 4 MiB），不使用事件数无限增长的缓存。32 个请求的输入/回复和 8 项文件准备分别有数量上限；历史、模型上下文、原始工具结果属于业务数据，沿用既有 reader/模型/工具限制，不能据此声称整个进程内存固定为 24 MiB。下层 HTTP 库的单块分配和操作系统缓冲也不属于本层队列预算。

Unix stdio pipe 使用非阻塞 AsyncFd，输出 deadline 可以中断等待内核 pipe 容量；不使用 Tokio blocking stdout 来掩盖不可取消写。普通文件重定向走 Tokio 文件路径，避免 Linux epoll 对 regular file 的 EPERM；此路径依赖文件系统 I/O，不能承诺对失去响应的内核磁盘调用硬终止。非 Unix 仍保留平台 stdout，尚未宣称 Windows 具备同样原生 pipe 期限证据。

取消可跳过后续展示发送并设置可选 `history_required:true`；外层模型 future 被取消而丢弃正在等待的发送也覆盖此标记。业务 journal 继续由 Agent/Conversation 写入，显示丢帧不成为持久化来源。OutputClosed、EOF 与 Shutdown 都不隐式取消模型或工具；输出期限不等于任务总执行期限。

#109 接入时使用以上 typed 命令和独立 TaskEvents，保留终端投影、草稿、命令解析及自动标题默认行为；自动标题服务化和旧 SessionManager 委托移除仍需 Runtime/TUI 对齐后交付。GUI 继续使用相同 stdio v1；本 PR 无桌面 UI 修改，也不提供 Rust library 打包或 HTTP 入口。

### #105 同连接配置候选实现事实

更新日期：2026-10-22。分类：**本 PR 候选实现事实，尚未合入 dev**。依赖 #104 的 typed 服务，同连接模型选择统一使用 `session.config.update`，未另建 `session.select_model` 状态机。#32/PR68 的模型容量、原子提交、opaque continuation 约束在此落实；GUI/TUI 模型选择界面仍由对应消费方负责。

| 方法 | 当前候选的实际行为 |
| --- | --- |
| `runtime.capabilities` | 返回 methods、protocol_version、max_active_tasks 和功能标志；session_configuration=true，multi_connection/external_tools/caller_context=false |
| `model.list` | 可选 connection_id，首版只允许 default；返回已加载容量 `models:[{catalog_id,name,limits}]` 和宿主显式 `configured_models:[{model,compaction}]`，不访问网络。catalog_id 只供容量查找，不能当作 endpoint 的实际模型 ID |
| `session.config.get` | session_id；返回 effective_config/config_revision/storage_schema/immutable_fields；未 loaded 也能只读查询，零模型/工具调用、不修复 journal |
| `session.config.update` | session_id、expected_revision、patch；patch 支持 model、catalog_id 或完整 compaction；working_directory 返回 immutable_field。缺省保持、显式 null 拒绝、未知字段拒绝；成功返回完整有效配置及 effective_from=next_task |

~~~json
{"version":1,"id":"config-1","method":"session.config.update","params":{"session_id":"s1","expected_revision":1,"patch":{"model":"endpoint-model-b","catalog_id":"catalog-provider/model-b"}}}
~~~

`model` 原样用于实际 endpoint 请求。`catalog_id` 和显式 `compaction` 二选一；缺少来源时只可使用宿主已配置默认模型容量或已加载 catalog 的唯一匹配，未知/歧义返回 model_limits_required，不为新模型猜 256K。显式 compaction 沿用唯一 `CompactionConfig::validate`，非法值返回 invalid_model_limits。catalog 名称、canonical ID 与 provider/model ID 的查找沿用现有规则，明确的完整 root ID 优先于其短别名；目录不证明 endpoint 已开放该模型。

新建 schema 1 会话 revision=1；旧 metadata 缺失字段按 0 读取，首次真实更新变为 1。config_revision 与 capacity_source 同 model/compaction 一起原子保存。修改目标必须 loaded 且无 accepted/running task、无其他修改准备；A 正执行不阻止空闲 B。两个同 revision 的调用先提交再等待时只有一个成功，另一个 config_conflict；冲突返回当前已提交 revision，若另一提交仍准备中，该值可能仍是旧 revision，调用方重新查询而不自动重试。有效模型/预算不变是 no-op，即使容量来源表达不同，也保持原 provenance、revision 和 metadata 字节。

配置候选先完成容量、上下文兼容和不可变字段校验，得到可无失败应用的参数；随后后台文件准备读取最新 metadata、再核对 revision，写临时文件并 sync、rename、sync 目录。提交前错误保持旧 Agent/metadata/usage/history；提交后无法确认返回 commit_unknown，隔离该 session，不允许用旧内存继续 Send。config.get 读取磁盘供对账，显式 Release/Load 后按完整新/旧 metadata 恢复。不用成功回包或 UI 选择推断是否已落盘。

成功发布后，无 await 地切换 Agent 的模型与压缩配置、清除旧 usage baseline，再返回真实有效配置。配置更新本身不发推理、摘要或业务工具请求；降低预算后下一任务沿用既有 compact-before-generation，summary 与 generation 都使用新配置，摘要失败不丢上下文、不 fallback 到原模型。配置本身不重写 history.jsonl。

TaskStarted 新增可选 `configuration:{model,api,compaction,config_revision}`，由 Runtime 在接纳时固定，后续生成/工具/压缩使用同一份 Agent 配置。旧记录没有该字段时返回缺失，不倒填当前模型或版本。模型改变且上下文含 ProviderItem/provider_data 返回 continuation_not_supported，不删除原生续接数据来迁移；可确定的协议图片不兼容返回 config_incompatible。

schema 1 仍未验证原 endpoint，effective_config 返回 connection_binding.state=unknown、connection_identity=null、execution_connection_id=default。此字段仅说明本进程执行连接，不是历史绑定证明。#106 的多连接、增强 prompt/tool selection、显式 schema 2 迁移尚未在本候选开放；#61 的容量并发仍另案。默认终端入口保持既有行为，#109 将消费同一 typed 接口。

验收固定到本 PR SHA：实际 A→B→A 请求、重启恢复 B、接纳配置 journal 快照、两个同时提交的版本冲突、不同 session 的 busy 隔离、no-op 字节不变、未知容量/非法配置/opaque 拒绝、metadata 写入前/rename 前/rename 后故障，以及新模型 summary/generation 和失败后原上下文保留。stdio 同时覆盖 capabilities/model.list、缺省/null/未知字段、版本错误和未 loaded 的只读配置查询。测试/运行/CI 证据记录在 #105 及对应 PR；尚未通过的阶段不以本节代替联合验收。

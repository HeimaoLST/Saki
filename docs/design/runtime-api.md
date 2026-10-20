# Runtime 调用契约与实施规划

更新日期：2026-10-20。状态：**调用契约已确认；首轮机器入口由 #60 交付；跨端接入与后续能力分别追踪**。

需求依据：[Agent runtime 需求](../requirements/agent-runtime.md)、[#43](https://github.com/HeimaoLST/Saki/issues/43) 及 [GUI 首轮交接](https://github.com/HeimaoLST/Saki/issues/43#issuecomment-6235030388)。本文定义跨宿主调用契约与验收边界；实时优先级、任务状态和负责人以 GitHub 为准。

## 决策确认

本契约已取得实际消费意见与独立提案评审：[GUI ACK](https://github.com/HeimaoLST/Saki/issues/43#issuecomment-6258133217)、[PM 范围 ACK](https://github.com/HeimaoLST/Saki/issues/43#issuecomment-6258394345)、[TUI 兼容 ACK](https://github.com/HeimaoLST/Saki/issues/43#issuecomment-6262714452)、[最终提案独立评审汇总](https://github.com/HeimaoLST/Saki/pull/59#issuecomment-6244513599)。[PM 实施交接](https://github.com/HeimaoLST/Saki/issues/60#issuecomment-6284753791)已确认 #60 的 DoR，首轮按 #60 → #63 推进；[QA 边界调整](https://github.com/HeimaoLST/Saki/issues/60#issuecomment-6284890371)将 #64 限定为 CI 工程，不作为逐 PR 人工产品验收前置。

以上确认针对调用契约与范围，不代表代码或跨端验收完成。具体 v1 DTO 位于 `src/session/runtime.rs`，可执行驱动位于 `examples/runtime_stdio.py`，运行证据随 #60 交付；共享 typed 事件的实际字段与消费接线按 TUI ACK 继续协调。多会话并发、压缩后原文检索及 #31／#32／#33 仍为独立后续任务。

## 现状与目标

实现事实（#60；以同 PR 的代码和运行证据为准）：`src/session/runtime.rs` 提供 stdio v1 调用边界，按 ID 保留真实 loaded sessions，首轮只有一个执行槽。`Agent` 继续承担唯一模型／工具循环，`SessionStore` 和 journal reader 继续承担持久化／恢复，旧 TUI／console 入口保持。GUI 联系人接入由 #63 交付；重叠执行和 compact 后原文检索仍分别由 #61／#62 交付。

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

# Builtin tools：第一版实现契约

更新日期：2026-10-19。主要参考 Pi 的四工具与输出处理、Codex 的执行生命周期；第三方研究见 [Builtin tools](../research/systems/builtin-tools.md)。本版范围是当前 agent loop 的工具执行，不引入恢复、归档、输出文件清理、TTL、磁盘配额、后台会话或主动取消。

## 注册与执行环境

主程序默认注册 `read / bash / edit / write`。calculator 源码作为学习示例保留，不默认暴露给模型。

[register_builtins](../../src/tools/mod.rs) 复用现有 JSON Function 和 ToolRegistry。调用方注入 `ToolContext { cwd, output_dir }`，路径应为绝对路径；相对文件路径、bash 的相对 workdir 均基于 cwd，绝对路径直接使用。workdir 与命令内部 cd 只影响本次 bash，不改变其他工具的 cwd。

ToolDefinition／ToolType 已归入[公共协议](../../src/protocol/mod.rs)；registry 只负责将声明与 handler 关联并执行。Responses 原生工具声明由[客户端](../../src/client/responses.rs)转换，详见[模型调用分层](model-runtime.md)。

主程序使用启动目录作为 cwd，输出目录为已有 session 目录内的 `tool-output/`。该目录在第一次需要保存大输出时创建。

## 工具接口

| 工具 | 输入 | 正常结果 |
| --- | --- | --- |
| `read` | `path, offset?, limit?` | `content, start_line, end_line, truncated, next_offset`；超长单行另有 note |
| `bash` | `command, workdir?, timeout_ms?` | `output, exit_code, timed_out, truncated, wall_time_ms`；超限另有 output_path |
| `edit` | `path, edits:[{oldText,newText}]` | `path, applied` |
| `write` | `path, content` | `path, bytes_written` |

参数经 serde 解析，不接受未知字段。参数类型或取值错误返回 InvalidArgs，I/O 或匹配失败返回 ExecutionFailed；沿用 Agent 将 ToolError 转成模型可见 error 的 continuation 路径，不建立另一套工具错误系统。

### read

- 读取 UTF-8 文本，offset 从 1 开始，limit 默认 2,000 行；offset/limit 必须为正数。
- 每次最多 2,000 行、50 KiB 原始文本，用户增大 limit 不会突破展示上限。扫描与单行缓冲有界，不先加载整个文件。
- content 保留原始字符、BOM 和换行，行范围在元数据中，不添加行号前缀。
- 仍有内容时返回 next_offset；空文件或超过 EOF 返回空 content、next_offset=null、truncated=false。空结果 end_line=start_line-1。
- 单行自身超过额度时返回 note，提示用 bash 读取字节片段，不伪造可以继续该行的 next_offset。若此前已经返回完整行，则先给出下一未展示行的位置。
- 目录、二进制文件及行内字节切片由 bash 处理。分页不额外统计文件总行数。

### edit / write

edit 的全部替换基于原始文件。oldText 必须非空、唯一匹配，各匹配区间不能重叠；支持重叠出现的歧义检查，例如在 aaa 中查找 aa 会失败。全部校验成功后才写回，任何参数或匹配校验失败均保持原文件不变。

匹配仅将 CRLF 与 LF 视为等价，不做空白、引号或 Unicode 的 fuzzy matching。匹配前分离 BOM，写回恢复；只替换对应原始区间，未修改字节保持原样，因此不会整体重排混合换行。替换内容的新行采用目标位置附近的原有换行方式。

write 创建或整体覆盖 UTF-8 文件，创建缺失父目录，不回显内容。edit 与 write 均采用普通文件写入，保持现有符号链接目标与文件模式的通常行为；不承诺写入故障、进程崩溃或外部并发修改时的事务保证。

小范围修改优先 edit，创建或整体重写使用 write；批量转换仍可通过 bash 执行脚本。

### bash

- 当前实现支持 Unix，使用 PATH 中的 bash，继承环境，非交互式执行且 stdin 关闭。
- timeout_ms 默认 120,000，必须为正数，可以显式调整；等待命令退出，不暴露 PTY/stdin/session API。
- stdout/stderr 在子进程内合并到同一管道（stderr 重定向到 stdout），按实际写出顺序返回；不再依赖两路读取的完成顺序。
- 非零退出是正常结果，保留退出码和输出供模型修复；无法启动、读取或保存输出的错误走 ToolError。
- Unix 子进程使用独立进程组，超时与采集错误终止该组并等待 shell 退出。shell 正常退出后也终止剩余组成员，避免后台子进程持有管道导致无限等待。进程组终止是幂等的：一次成功发送信号（或 ESRCH）后不再重复终止，后续清理不会把已完成的超时变成新的错误。macOS 的 EPERM 仅在 libproc 确认组内成员均已退出或消失时视为清理完成；仍有存活成员、查询失败或列表截断时保留错误。spawn、wait 与 group kill 错误包含失败操作，避免只返回无法定位的 errno。主动脱离该进程组的后台任务不属于本版支持范围。
- 信号终止时 exit_code 可以为 null；timed_out 单独说明是否触发本工具的时限。

macOS 的特殊分支依据 [Apple `killpg1` 实现](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/kern/kern_sig.c#L1675)：进程组遍历跳过僵尸成员，没有可发送信号的成员时也可能返回 EPERM。回归测试用 `waitid(WEXITED | WNOWAIT)` 保持一个已退出但未回收的组长，确定性验证清理成功与 guard 解除；另验证存活进程组不能被当作已退出。检查结束前再次枚举，出现未经检查的新成员时保留原错误。这不将旧版只含裸 errno 的历史失败追溯归因为同一调用。

## 输出边界

read 返回原文件的一页；bash 保留末尾最多 2,000 行 / 50 KiB 文本预览，包装字段不计入这两个额度。bash 内存保留有限尾部，输出超过行数、字节数或文本转换后的展示额度时，才流式保存收到的完整原始字节。

output_path 指向实际可读的输出文件；文件在返回结果前完成 flush。可以用 read 分页读取 UTF-8 日志，或者用 bash 中的 rg 定位内容。非 UTF-8 输出的预览采用替换字符，保存文件仍保留原始字节，需通过 bash 处理。

这些文件是工具执行产物，不宣称是可重新计算的 cache。本版不自动删除、清理或限制落盘总量，也不提供跨重启恢复保证。

输出在进入 Conversation 前有界；Conversation 继续负责 compact。当前最新用户任务的整条工具链会被保留，单次上限不能保证无限次数调用的整轮永不超限，已有 overflow recovery 仍可能明确失败。

## 验收

- `cargo test --locked --bin Saki`：43 项，包括原有 29 项、13 项工具边界与 1 项 Agent continuation 测试。
- 工具边界覆盖分页与超长行、UTF-8/CRLF/BOM、原文多处替换、歧义/重叠/后一项失败、symlink 与可执行权限、大输出双流合并消费（预览保留最终标记）、行数单独触发落盘、非 UTF-8 输出、超时子进程终止、正常退出后的管道持有者，以及输出文件创建失败后的进程终止。
- Agent 测试使用生产 Registry 和四工具，验证读取→非零退出→edit 校验失败→修正→写文件→执行验证，失败信息确实回到下一次模型请求。
- `cargo test --locked --example growth_experiment`：原有 29 项通过。
- `cargo fmt --check` 与 `git diff --check` 通过。Cargo.lock 与 calculator 示例保持原样。

### 真实模型验收

使用代码默认的官方端点 `https://api.deepseek.com`、默认模型 ID `deepseek-v4-flash` 和已有环境变量 `API_KEY`，在隔离工作目录运行实际主程序。验证时显式设置 1M context budget；本次验证工具行为，不重新验证模型容量或 compact。端点使用方式与 [DeepSeek 官方 Responses API 文档](https://api-docs.deepseek.com/guides/responses_api/)一致。

两项均通过：

1. **四工具闭环**：模型依次调用 read → bash → edit → bash → write。第一次执行断言失败，退出码 1；精确修改后退出码 0，生成 report.txt，内容为 VERIFIED。没有通过脚本代替 edit/write。
2. **大输出回读**：bash 产生 4,001 行输出，预览保留末尾 2,000 行，返回 truncated=true 和 output_path；完整日志确有 4,001 行。模型调用 read(offset=2001, limit=1)，取回不在预览中的 MIDDLE_TOKEN_SAKI_2001。

原始报告：[官方端点 report.json](../../.saki/verification/builtin-tools/1791214395230601000/report.json)。报告包含工具结果与工作目录，每个工作目录保留 stdout/stderr 和 session journal；不保存认证信息。两项分别耗时约 4.70 秒和 3.44 秒。这是两个受控场景的兼容性验证，不代表长期复杂任务的质量评测。

此前使用授权的本地网关与 opencode-go/deepseek-v4-flash 时，网关返回 HTTP 429（OpenCode Go: Go usage limit exceeded），没有执行任何工具调用；这次失败保留在[网关报告](../../.saki/verification/builtin-tools/1791213882040009000/report.json)，不计为工具测试通过。随后用户明确授权使用官方 key，完成上述复验。

### Magpie 网关复验（2026-10-06）

用户告知额度恢复后，使用原有本地端点 `http://127.0.0.1:3425/v1`、模型 ID `opencode-go/deepseek-v4-flash` 运行实际主程序。网关配置仅注入测试子进程，项目 .env 中的官方配置保留。验证显式使用 1M context budget，不重新测定模型容量。

严格复验两项均通过：四工具闭环约 10.39 秒，大输出回读约 6.08 秒。闭环中模型直接执行 python3 calc.py，首次工具退出码为 1，edit 后再次执行退出码为 0，并用 write 创建内容为 VERIFIED 的报告。大输出返回 2,000 行预览与完整 4,001 行日志，随后 read 正确取回第 2,001 行的标记。未再出现此前的 usage limit 429。

## edit 已应用变更结果（#33，设计提案）

更新日期：2026-10-18。**未批准、未实施。** [#33](https://github.com/HeimaoLST/Saki/issues/33) 的问题是 edit 成功结果只有 path/applied，调用方无法显示实际应用的变更。本节推荐候选 A：成功结果增加结构化 changes，保留旧字段。候选 B 的参数预览不能证明应用成功，候选 C 的 read 复查仍可用，但不能让前端把参数猜测冒充执行结果。

首版范围仅 edit；write、bash、全局 diff 框架及前端 diff renderer 不自动纳入。复用现有 ToolFinished.output 和 journal 工具结果，不新增另一种公共 tool result/Agent 事件或 diff 依赖。前端展示另按业务线交付。

### 成功结果与坐标

小结果在原成功对象上增加 changes：

```json
{"path":"/work/source.txt","applied":1,"changes":[{"edit_index":0,"before":{"start_byte":0,"end_byte":3,"text":"old"},"after":{"start_byte":0,"end_byte":3,"text":"new"}}]}
```

- before 是本次实际读取原始快照的字节区间与文本；after 是本次最终提交的更新快照的字节区间与文本，不直接复制未经转换的 oldText/newText 参数。
- 坐标为完整 UTF-8 文件中的零起始、右端不含字节偏移；BOM 计入偏移，CRLF 按实际两个字节计数。before/after 各自对应其快照，不是行号或 Unicode 字符索引。
- edit_index 是原 edits 参数数组的零起始索引；changes 按原文件位置排序，多个不同长度替换后的 after 偏移须正确反映前序长度变化。未改变字节的 replacement 不产生 change；applied 继续表示已处理替换条目数，因此可以大于 changes 的长度。
- path 沿用现有结果含义；符号链接与文件模式遵守原 edit 行为。工具提交的快照不等于之后该路径永远不变，也不承诺隔离外部并发写入。

数据直接来自已校验的匹配区间、实际行尾转换和已构造 updated 内容。实现可以按排序区间维护原始/新内容游标生成结果，不为已知精确替换再次跑全文件 diff 算法。只返回被本工具修改的片段，不采集无关文件快照。

### 大结果与写入边界

changes 的序列化内容遵守既有工具输出的字节/行边界。超过边界时，在同一个 session 的 tool-output 下保留完整 changes JSON；结果明确省略完整内联数组，并提供文件路径、有限预览和 truncated 标记：

```json
{"path":"/work/source.txt","applied":1,"changes":null,"changes_file":"/sessions/s1/tool-output/123-0.log","changes_preview":"bounded tail of the changes JSON","truncated":true}
```

changes_file 内容是完整 changes 数组的 JSON，复用现有输出累积／UTF-8 预览／落盘路径；预览可能是 JSON 尾部，不能作为完整结构解析或伪装完整 diff。普通 read 可按既有工具能力读取产物。首轮 renderer 若不能读取完整产物，应显示截断和文件引用，不从输入参数补造缺失部分。旧成功结果没有 changes 表示“不提供该证据”；新成功结果 changes 为空数组表示“没有字节变化”，二者不能混同。

建议实现顺序：

1. 沿用全部参数、唯一匹配、重叠检查；构造 updated 和实际 changes，原文件尚未变更。
2. 预先准备成功 JSON；如需完整产物，先通过现有输出路径写入/finish。准备失败沿用 ToolError，目标文件不写入。
3. 沿用现有一次目标文件写入；成功后返回已准备结果，不再做可能失败的 diff/read/artifact IO。

这样不会在文件已改完后又因输出产物失败而把成功 edit 包装成未知的普通错误。目标文件写入本身仍非事务：IO 失败/进程崩溃可能已产生部分外部变化，不能由“没有成功 changes”推导“磁盘完全未变”。失败沿用现有错误工具结果，不发布成功 applied/changes；调用方显示失败/结果不确定并允许 read 检查，不伪造权威差异。未发布的预备产物不作为已应用证据。

### 验收与实施范围

定稿后另建 runtime 实施 issue：`src/tools/edit.rs`、必要的既有 `output.rs` 接线及 `tests/tools/`、事实文档；不改其他工具语义、锁策略或全局协议。成功结构按 call_id 沿用现有 Agent/持久化路径进入下一模型请求和历史，恢复不重跑 edit。TUI/GUI 通过实际结果选择显示方式，不解析参数来伪造成功。

确定性验收包括：

- 多个乱序参数、不同长度替换：按 before 坐标重建后与实际写入字节完全一致，after 切片等于返回文本，edit_index 保留原参数关联。
- BOM、Unicode、LF/CRLF 等价匹配和混合行尾：结果反映实际保留/转换的字节，未触碰字节和模式/符号链接回归保留。
- 未找到、歧义、重叠、空参数等校验失败原文件不变且无成功 changes；no-op 保留 applied 语义但 changes 为空。
- 大 changes 的完整产物可读回、UTF-8 预览有界且明确 truncated；结果/历史不谎称完整数组。产物准备失败发生在目标写入前并保持目标字节。
- 文件写入失败不发布成功差异，不新承诺 IO 事务保证；工具错误仍回传模型。恢复读取相同 tool result/call_id，不重复执行。
- Agent 级真实临时 edit→工具结果回传→后续回答闭环；最终实现 SHA 的工具/Agent回归、fmt、strict Clippy、all-target tests及必需CI。真实 provider 验证与离线证据分开。

需要 runtime/TUI/GUI/PM 对坐标、no-op、截断和失败语义的实际 ACK，独立内容评审与未来代码验收分开。新增字段不改变现有 path/applied；如果实际实施会破坏已承诺的工具能力或持久化，则单列迁移与回滚，而不是从本提案默认获准。

原始证据：[Magpie 严格复验 report.json](../../.saki/verification/builtin-tools/1791263972642569000/report.json)。

此前同日首轮已经完成文件修复与报告写入，但模型使用 python3 calc.py 后追加 echo 的命令，整个 shell 的退出码被 echo 的成功遮蔽；严格要求工具返回非零退出码的检查因此失败。这属于该次模型生成命令的行为，bash 返回了实际 shell 退出状态。随后明确要求两次测试命令均恰好为 python3 calc.py，再运行上述严格复验。首轮结果保留在[首轮报告](../../.saki/verification/builtin-tools/1791263925128818000/report.json)，不改写其失败判定。

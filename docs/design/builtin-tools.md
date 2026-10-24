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

原始证据：[Magpie 严格复验 report.json](../../.saki/verification/builtin-tools/1791263972642569000/report.json)。

此前同日首轮已经完成文件修复与报告写入，但模型使用 python3 calc.py 后追加 echo 的命令，整个 shell 的退出码被 echo 的成功遮蔽；严格要求工具返回非零退出码的检查因此失败。这属于该次模型生成命令的行为，bash 返回了实际 shell 退出状态。随后明确要求两次测试命令均恰好为 python3 calc.py，再运行上述严格复验。首轮结果保留在[首轮报告](../../.saki/verification/builtin-tools/1791263925128818000/report.json)，不改写其失败判定。

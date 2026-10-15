# 图片输入

更新日期：2026-10-12。

图片和文字属于同一条用户消息，进入已有 Agent 与 Conversation。TUI 负责获得附件和展示名称，模型客户端负责按 Responses API 发送；Agent 不依赖终端或剪贴板。

## 输入方式

| 操作 | 行为 |
| --- | --- |
| 输入区域按 `Ctrl+V` | 优先粘贴剪贴板图片；没有图片时粘贴文字 |
| 粘贴单个完整图片路径 | 加载图片为附件，支持带空格的引号路径与常见拖入路径转义 |
| `Enter` | 发送文字与全部附件；允许只发送图片 |
| 文字为空时按 `Backspace` | 删除最后一张附件 |
| `Ctrl+C` | 将完整草稿保存到输入历史后清空；可用 `↑` 恢复文字与附件 |

可连续添加多张图片。终端的 `Cmd+V` 是否能直接传递截图取决于终端自身；粘贴截图使用 SAKI 的 `Ctrl+V`。拖入文件只有在终端将完整路径作为粘贴事件发送时才会转成附件。

支持 PNG、JPEG、GIF、WebP。文件内容决定 MIME 类型，不依据扩展名发送错误类型。附件读取失败时显示错误，并保留原粘贴文本和已有草稿。普通文字中提到路径仍作为文字输入。执行任务期间可以编辑附件，但仍需等当前任务结束后再提交。

输入区和用户记录显示附件名称，不显示 base64。记录的原文复制保留文字正文。当前不提供终端内图片缩略图。

## 模块与契约

| 模块 | 职责 |
| --- | --- |
| [agent/message.rs](../../src/agent/message.rs) | `UserMessage`、图片内容快照及图片路径识别 |
| [frontend/tui/](../../src/frontend/tui/) | 剪贴板／文件适配、附件草稿与历史、展示和删除 |
| [agent/mod.rs](../../src/agent/mod.rs) | 接收文字或图文消息，执行同一个模型／工具循环 |
| [agent/conversation.rs](../../src/agent/conversation.rs) | 保存图文消息、usage 校准、自动压缩及原始输入日志 |
| [protocol/](../../src/protocol/mod.rs) | 公共 `InputMessageContent::Parts`、文字和图片内容 |
| [client/responses.rs](../../src/client/responses.rs) | 将公共图文内容转换为 Responses 的 `input_text`／`input_image` |

纯文字保持原来的字符串序列化。公共协议中图文内容使用 `Text`／`Image { url }`；发往模型时，客户端转换为 `input_text` 与 `input_image` 的数组，图片使用 `data:image/...;base64,...`。图片不会替换成让模型自行读取的本地路径。新日志记录公共格式并带版本号，见[模型调用分层](model-runtime.md)。

附件保存读取时的字节快照。后续修改或删除源文件，不会改变草稿、输入历史或已发送的图片。console 保留纯文字交互；其消息也经过同一个通道。现有直接传入字符串的 Agent／Conversation 调用继续有效。

## 图片与自动压缩

base64 大小不等于模型图像 token 数。估算时将图像数据替换为短引用，按每张图片 1,024 token 作初步近似；已有 usage 继续校准上下文用量。这个近似不是模型容量保证，实际超限仍由既有 overflow recovery 处理。

压缩旧对话时，摘要请求的文字 transcript 保留图片序号引用，原图片作为真正的 `input_image` 附在同一摘要请求中。模型能看到需要总结的图片；不会接收到一大串 base64 文字。最新用户消息和完整工具调用边界继续按原规则保留。

原始输入日志保存完整图片内容。图片消失于压缩后的活跃上下文时，其长期信息取决于摘要质量；原始日志仍保留输入依据。本次不增加日志清理或会话恢复设计。

## 模型与验证

DeepSeek 官方在 2026-09-10 发布原生视觉的 V4.1 Flash，并说明旧 `deepseek-v4-flash` 别名临时映射到新版。其 Responses API 支持用户消息内的 `input_image` 和上述四种格式。来源：[官方更新日志](https://api-docs.deepseek.com/updates/)、[Responses API](https://api-docs.deepseek.com/api/create-response/)。其他网关或模型仍以实际能力为准。

10 月 8 日配置 `opencode-go/deepseek-v4-flash` 经 Magpie 网关真实测试：读取随机生成 PNG 内的五位数字与三个色块顺序，结果与独立 ground truth 完全一致，HTTP 200，报告 input 190／output 65 token。该结果只证明整条入口能回答图片问题，不证明原模型直接收到了像素。10 月 11 日排查确认该入口由 Magpie 代读，详见下一节；此前“能处理图片”的结果不能作为原生视觉验收。

### 原生视觉与网关代读

2026-10-11 用户发现模型收到的是 GPT 的图片描述。本机会话日志仍保存真实 `input_image`，说明 SAKI 没有自行把图片替换为文字。运行中的 Magpie 版本为 0.1.1190；对应 [vision.go](https://github.com/yetone/magpie/blob/v0.1.1190/internal/gateway/vision.go) 的 `blindTo` 根据 provider 的输入能力决定是否改写，`seenBody` 将原图片换成视觉模型的描述。其描述标记与用户会话中的文本一致。

本机 `/v1/models` 将 `opencode-go/deepseek-v4-flash` 标为 `input: [text]`，而 `opencode-go/deepseek-v4.1-flash` 标为 `input: [text, image]`。同名／别名在不同 provider 的能力可能不同，不能用 DeepSeek 官方别名行为推断 OpenCode Go 的行为，也不能通过模型自述或读对数字独立证明原生视觉。

按用户切换 V4.1 的意图，本地 `.env` 已改为 `SAKI_MODEL=opencode-go/deepseek-v4.1-flash`；已有进程不热加载模型配置，需要重新启动。实际二进制通过独立 tmux TUI 粘贴图片路径，日志记录 `input_text + input_image`；模型返回五位数字和三个色块的顺序，与 ground truth 一致。结合网关能力元数据、匹配版本的改写条件与本机 usage 记录核对路径；观测边界止于本地网关，不声称能审计 provider 内部实现。记录聚合于 `.saki/verification/clipboard-vision/`。

10 月 8 日验收时，`cargo fmt --check`、`cargo check --all-targets`、`cargo build`、`cargo test --all-targets` 均通过：144 个主程序测试、34 个实验模块测试。测试覆盖图文／纯图请求、截断图片保留草稿、实际格式与扩展名不一致、删除源文件后恢复附件快照，以及最新图片与压缩前缀的处理。

独立 tmux 验收通过四组检查：

- 图片输入：4 个受控请求，实际 macOS 图片剪贴板、文字回退、引号／转义路径、附件删除、忙时清空和召回、读取失败保留草稿、原始日志。
- 真实网关 TUI：从粘贴图片路径到最终模型回答，数字与颜色顺序完全匹配 ground truth。
- 原有输入与循环：9 个文字输入请求及 7 个工具／压缩／错误恢复请求通过。首次窄窗口驱动在 resize 与输入之间缺少重绘屏障；等待实际光标位置确认新宽度生效后，同一原文断言通过，未修改文字导航规则。
- 真实图片压缩：摘要请求读取旧图片，活跃上下文移除图片后，后续请求仍正确回答数字和颜色；估算上下文由 1,237 降至 432 token。这是单一受控样例，不代表复杂图片摘要的保真率。

验收原始记录位于本地忽略目录 `.saki/verification/image-input/`：`root-validation.json`、`image-tui-result.json`、`live-tui-result.json`、`input-smoke-result.json`、`core-smoke-result.json`、`compact-probe-result.json`。所有交互均使用独立测试会话，未操作用户原有 pane。当前验收平台为 macOS + tmux。

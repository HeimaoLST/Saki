# Desktop contacts：首版运行时接入

更新日期：2026-10-21。状态：**实现事实（#63 分支；依赖 #60 已合入的 stdio v1 契约）**。原生 MyGo 应用已用 `Saki --stdio` 消费真实 runtime；contact store、任务事件与解析后的历史由 GUI 自身维护，GUI 不读取 Rust journal。最终合并以 dev 为准。

[previews/desktop-contacts.html](previews/desktop-contacts.html) 仍是早期浏览器示意，不是当前原生应用的验收对象；仓库内 `desktop/` 是当前可运行的原生应用。调色板与布局设计沿用下文。concurrency、`search_history`／`read_history`、附件、模型切换等仍是后续独立任务，不在首轮。

## 已确认需求与交付边界

需求／产品决定（用户确认，#44）：首次使用无需先定义 agent。添加联系人即得到可输入的默认通用助手；name／purpose／instructions／workspace 为可选设置。默认助手不解决 host 的模型、凭据与工具配置。

实现事实（#63）：
- 正常启动从 GUI contact store 恢复；默认创建一个 `Saki` 联系人，零配置即可看到界面。测试/截图 fixture 仍保留在 `seededApp()`，正常真实启动不会混入种子历史。
- Add contact 一次点击创建并选中 `New contact N`、`Role=Assistant`、Ready 的空联系人，composer 取得一次焦点；不创建 runtime session，直到第一次发送。
- contact → runtime `session_id` 的映射、显示名、profile、草稿和选中项由 GUI 持久化；消息正文不复制，历史只来自 `session.history` 的已解析 journal 记录。
- Settings：显示名始终可改；`purpose/instructions/workspace` 只在首次 `session.create` 时作为 profile/system prompt 与 working directory 使用。已有 `SessionID` 或首次 create 在途时，这三项只读，保存设置也只更新名字，不重建 session、不丢历史/草稿、不伪称热更新生效。
- 首轮单执行槽：`busy` 明确拒绝且草稿保留；不做队列或并发。查看另一个联系人不会 cancel/release 当前任务，事件按 `(session,task)` 路由。
- 接纳 response 与 `task_started` 分开：前者显示“已接受，等待开始”，只有 `task_started` 才进入 Working；delta 追加、final 覆盖同一 `(session,task,item_id)`，工具按 `call_id` 归属，工具业务错误不自动判整轮失败。
- `task_finished` 是唯一终态；`failed/interrupted` 的已保存原因在历史刷新后仍可见。取消按 `(session,task)`，迟到 cancel 不会改写已完成或别的 task。
- 断连/协议失败标记为未知；`sendUnknown` 只有在同一连接、`session.load/status` 确认 idle 且 history 取回后才恢复可发送状态。连接已退出时不提供“Reload”假恢复，明确提示重启应用；不做透明重连框架。
- 退出（关窗与 Cmd+Q）先阻止默认关闭，持久化草稿，异步 `runtime.shutdown` 等待已接纳任务收尾和进程 Wait，再退出；不阻塞 UI 线程、不静默 Kill。

## 文件与配置（实现事实）

- `desktop/runtime.go`：stdio v1 client／DTO（request id 路由、持续读 stdout、stderr 诊断、协议失败单一边界、迟到 response 丢弃、读完后 Wait）。
- `desktop/runtime_model.go`：启动/恢复、真实发送、事件应用、history 转换、取消、退出与配置。
- `desktop/store.go`：GUI contact store（原子写，损坏不覆盖）。
- `desktop/model.go`／`view.go`／`main.go`：状态模型、原生布局与入口；`seededApp` 仅测试/截图。
- Store：`SAKI_GUI_STORE`，否则 `<UserConfigDir>/Saki/contacts.json`（macOS 通常 `~/Library/Application Support/Saki/contacts.json`）。
- Session 目录：`SAKI_SESSION_DIR`，否则 `<store 目录>/sessions`；作为子进程环境注入。
- Runtime 可执行：`SAKI_RUNTIME_BIN`；否则查找 GUI 可执行文件旁的 `Saki-runtime`／`saki-runtime`、`../Resources/Saki-runtime`／`../Resources/Saki`；最后查 `PATH` 的 `Saki`。
- 工作目录：`SAKI_GUI_WORKDIR`；否则当前目录（Finder 的 `/` 除外，退回 home）。作为子进程 cwd 与默认 `session.create.working_directory`。
- 模型/API/base URL/凭据沿用宿主环境或子进程 cwd 的 `.env`；GUI 不持久化、不打印 key，也不提供 Keychain/向导/全配置系统。Finder 启动不继承 shell cwd/env，需要在明确位置提供 `.env` 或从终端启动。

## 验证与证据

- Go：`CGO_ENABLED=0 go test ./... -count=1`、`go vet ./...`、`go build ./...`、`go test -race ./...` 均通过（本分支候选）。
- 真实 runtime 集成（opt-in）：`SAKI_TEST_RUNTIME_BIN=/abs/path/to/Saki CGO_ENABLED=0 go test -run TestRuntimeProcessIntegration -count=1 .`。使用本地受控 Responses fixture 与候选进程，覆盖 read 工具、task/message 关联、跨进程恢复零模型调用、history input 形状与继续；默认 CI 未设置该 env 时跳过，需要 CI 提供编译后的 runtime binary。
- 最终候选 `4001fd4` binary sha256 `f095bb77…588f6`；官方 driver `--self-test` 单次 PASS（real read、correlated journal、cross-process restore、invalid startup、stdout-loss drain/exit，4 model requests）。
- 真实 provider smoke（宿主已有 `.env`，临时 session 目录，只读 `README.md`、提示不写文件）：显式 `SAKI_API=openai-chat-completions` 时完成并报告首个 heading；同一 host 使用默认 Responses 时 runtime 返回 `model_error: missing field status`，属该 provider 与 Responses 形状不兼容，GUI 继承宿主配置、不自行改写。不要把单次结果写成所有 provider 普遍兼容。
- macOS 候选包：`/private/tmp/saki-gui-63-app/Saki.app`，`Contents/MacOS/Saki` 为 GUI，`Saki-runtime` 为候选 runtime；Info.plist `dev.saki.desktop`，ad-hoc 签名，`codesign --verify --strict` 与 `--deep --strict` 通过。未在真实窗口中人工操作，也未验证系统键盘/IME。

## 已知限制

- 单 active task；并发/后台执行由 #61 承载，不承诺 UI 的并行观感。
- `search_history`／`read_history` 未实现；History 只对已加载 transcript 做本地过滤，不是模型检索工具。
- profile 热更新不支持；绑定后只有名字可改，purpose/instructions/workspace 不生效于既有 session。
- 无自动重连；断连后重启应用重新加载 history。未知接纳不自动重发。
- 无附件、托盘/菜单、contact 级模型配置、头像 picker；真实 macOS 窗口、滚动、输入法与真实 provider 的紧张组合仍需人工验收。
- GUI 不解析 Rust JSONL；旧 journal 的零时间戳显示为空，不伪造日期，缺失 message_id 保持缺失。

## 产品模型

- 用户添加 **CONTACTS**：独立 agent，具有稳定的 name／avatar／profile／requirements 与长期对话；没有 New session 或 session picker 入口。
- 每个 contact 拥有自己的 profile、历史与未来的 memory，不再继承单一全局 Saki 身份。
- 历史查询计划新增 `search_history`／`read_history` 工具，绑定当前 contact。UI 只呈现可理解的活动与带出处的历史消息；primary flow 不暴露 compact、JSONL、token budget 或内部工具名。
- source/history 与当前 settings 分离：编辑 profile 只影响之后的回复，不改写已保存的消息。

## 与当前 runtime 的差距（需要 runtime 变更，proposed）

| 差距 | 当前实际 | 原型中的呈现 |
| --- | --- | --- |
| Contact 持久边界 | Session + JSONL，单一 active job | UI 假定每个 contact 有 profile 与独立历史 |
| 切换期间继续工作 | 未实现后台/并发执行 | Working 定时器按 contact 保存，切走不丢；这是 UI 模拟 |
| 绑定历史工具 | 尚未实现 `search_history`／`read_history` | Activity 与 Sources 用样例历史数据演示 |
| 附件 | 图片输入存在；未定义长期存储 | 只保留本地 filename chip，不保存文件内容 |
| 模型配置 | host 环境变量/启动配置 | contact 级模型设置 **deferred**，不在本流程 |

不得把本原型的并行观感当作后端承诺；实现前需要单独的 contact store、历史工具和任务归属设计。

## 布局与断点（浏览器示意）

- **≥920px**：`contact rail (默认 220px)` ｜ conversation ｜ 右侧 aux 300px，仅在 source/settings 等操作后出现；没有常驻第三栏。
- **640–919px**：rail + 单栏内容；source/settings 直接替换主内容，而不是压缩两栏。
- **<640px**：contact list 变为可展开 compact 列表；aux 同样替换主内容。
- 不使用 viewport-height 布局、fixed 定位或内部滚动盒；内容随页面增长，任何宽度都不出现横向溢出。
- Tweak（宿主支持时）：contact list width、message spacing、reading width 三个只影响布局的参数。

## 视觉语言

| 角色 | Dawn (light) | Main (dark) | 用途 |
| --- | --- | --- | --- |
| base | `#faf4ed` | `#191724` | rail / 应用底层 |
| surface | `#fffaf3` | `#1f1d2e` | 会话与表单主表面 |
| overlay | `#f2e9e1` | `#26233a` | composer、卡片、次要表面 |
| text | `#575279` | `#e0def4` | 正文与关键值 |
| subtle | `#797593` | `#908caa` | 时间、说明、次级文本 |
| rose | `#d7827e` | `#ebbcba` | 主按钮、选中 contact |
| pine | `#286983` | `#31748f` | 链接、hover、Working |
| foam | `#56949f` | `#9ccfd8` | Sources、Activity、历史 |
| gold | `#ea9d34` | `#f6c177` | Needs attention |
| love | `#b4637a` | `#eb6f92` | 校验错误 |
| iris | `#907aa9` | `#c4a7e7` | 焦点环、定位高亮 |
| highlight_med | `#dfdad9` | `#403d52` | user 消息面、分隔线 |

统一组件规则：12px 面板圆角／10px 控件／999px chip；1px 边框只用 `highlight_med` 与 `overlay` 两级；同一层级只使用一种 surface 步进；正文 14px，次级 12px，标题 15–16px；间距以 4px 为步长，靠留白而不是线框表达层级。可读的链接与引用元信息使用 `--link = Dawn pine #286983 / Main foam #9ccfd8`；深色 pine 与浅色 foam 只作为点、填充和图标色，不承担小字号文本。Dawn 的 subtle 文本略微加深到 `#6e6a86`，保证 12px 的对比度。

## 头像（Saki mascot）

- 当前默认（candidate default，proposed）：内置 imagegen 生成的原图（1254×1254 RGBA），只做 256px 等比缩放，未做二次创作。Prompt：original minimal cat head, pale lavender, Rose Pine rose tuft/ears, dark plum outline, transparent background, readable at small size。
- 资产：[previews/assets/saki-avatar-default.png](previews/assets/saki-avatar-default.png)，256×256 RGBA PNG。同一份优化字节以单个 data URI 内嵌在 fragment 中，供离线 sandbox 使用；不写入 widget state，也不引入 picker、上传或 contact 级头像抽象。
- 使用：所有 contact（包括新建与重命名）共用这一默认头像，出现在 contact rail、会话 header、消息与 Working 行；圆角方形 tile 使用 overlay 背景与固定内边距，rail／消息为 28px、header 为 36px，light／dark 下都可读。头像是装饰（`alt=""`），名字始终保持可见；选中态属于行与名字，不改变 mascot 颜色，也与 contact 身份、历史和草稿无关——编辑 profile 不会重置历史。
- 未来：same-family 的可选头像集合，保持一致的 silhouette、linework 与 Rose Pine／Dawn 调色。
- 验证：头像针对性检查 27/27 通过——内嵌字节与 workspace 资产逐字节一致，256px RGBA，各位置 `naturalWidth` 256，新建／重命名／回复／Working 均使用默认头像，选中不重着色，1024／320 light／dark 无横向溢出，0 运行时错误。

## 关键流程（界面设计与本地演示）

1. **选择 contact**：切换 name、profile、messages 与 draft；草稿按 contact 保存。未选中的 contact 可以处于 Working，查看与切走都不会清空它。状态词只描述工作：Ready／Working／Needs attention，不假装在线。
2. **Add contact（当前原生预览）**：一次点击直接创建、选中 Ready 默认联系人并进入空会话；无需填写 Name／Purpose／Instructions／Workspace。composer 只取得一次焦点，连续创建也可直接输入；个性化通过可选 Settings 编辑。浏览器旧示意的 Create／Cancel 表单仅保留为历史原型。
3. **Contact settings**：编辑 name／purpose／instructions，并明确提示“changes apply to future messages; saved history stays intact”。Save 更新 header 与列表。
4. **Composer**：textarea、Send、附件控件；Enter 发送、Shift+Enter 换行、IME 组合期不发送。发送后进入有限的本地 working→reply 过渡；切换 contact 不影响回复归属。浏览器示意的附件只是可移除 filename chip；当前原生预览没有附件入口。
5. **Source action**：回复中的 “2 earlier messages” 打开 contact 范围的 sources 区域，显示日期、原说话人与原文；View in conversation 会把该消息插回会话顶部并高亮定位。返回时草稿与进行中的工作保持。
6. **History action**：contact 范围的本地搜索，实际过滤该 contact 的样例历史；无结果有明确说明；不会出现其他 contact 的消息。不会实现或暗示 shared memory 与多 agent 协作。

## 状态与反馈

| 状态 | 表现与下一步 |
| --- | --- |
| Empty conversation | 可直接写第一条消息，或按需编辑 Settings；无 profile 不阻止开始 |
| Working | 会话尾部有可理解的进行中行（如 Running the parser tests）；Send 暂时不可用；切走保留状态 |
| Needs attention | gold 状态 + 失败摘要 + 样例 “Try again”；不显示假装可取消的 Stop |
| No history results | 说明该 query 在此 contact 历史中无匹配，建议更短的短语 |
| Validation | Settings 保存时名称不得为空；创建默认联系人无需表单校验 |
| Sources | foam 强调引用来源，不铺满页面 |

## 可访问性与可视化契约（浏览器示意）

原生 button／input／textarea，语义标签与可见 focus；aria-live 只播报有意义的更新（联系人切换、回复到达、创建/保存、无结果）；用户文本一律通过 textContent 写入 DOM。所有 ID 唯一，CSS 限定在 `#saki-desktop-contacts` 下。1024／736／375／320 宽度与 light/dark 均可读，无横向溢出。

## 验收清单（2026-10-14 浏览器历史记录）

以下是浏览器旧原型的历史结果，不替代当前原生流程验收。当前创建与设置证据见上文 Native preview；未实测项目继续保留限制。

- [x] Saki 默认选中，MyGo 桌面 shell 会话可读，最终回复带 “2 earlier messages”，Activity 可展开为 search／read 进度，无原始 reasoning。
- [x] 选择切换 name/profile/messages/draft；Ren 的 Working 与切走/切回保持；Mio unread 清除。
- [x] Add contact 的校验、创建、取消；Settings 保存后 header 与列表更新。
- [x] Composer 的 Enter／Shift+Enter；发送后 working→reply 归属正确；附件 chip 可移除。
- [~] IME 组合保护只做了代码走查（`event.isComposing` 与 `keyCode === 229`），未用真实输入法组合输入实测。
- [x] Sources 打开、引用内容与原文、View in conversation 定位高亮；History 搜索、无结果、仅当前 contact。
- [x] 1024／736／375／320 无横向溢出；light／dark 正常；fragment 自身无网络请求。
- [x] 所有 contact（含新建与重命名）共用同一默认 mascot，rail／header／messages／working 均加载；装饰 `alt=""`；选中不改变 mascot 颜色。可选头像家族属于后续工作。

## 浏览器验证（2026-10-14）

- 环境：本地 Chrome（headless）＋ Playwright；对象是 [previews/desktop-contacts.html](previews/desktop-contacts.html) 经 `render.py` 生成的独立预览。截图与脚本只放在 QA 临时目录，不进入仓库。
- 结果：57/57 项交互检查通过，0 个页面脚本错误；1024／736／375／320 均无横向溢出；Rose Pine Main／Dawn 的 `light-dark()` 角色可解析。
- 覆盖：默认会话与 Activity、逐 contact 草稿、切换期间回复归属与 unread、Add／Settings、附件 chip、Sources 揭示与高亮、History 范围与无结果、Ren 的 Working→Needs attention→Try again→Ready。最后两处文案调整（m2／m4 的引用配对与 composer 工作态文案）另做了 16/16 的针对性回归检查（copy-check.log）。
- 头像更新的针对性验证：27/27 通过——内嵌字节与 workspace 资产逐字节一致（sha256）、256px RGBA、各位置 `naturalWidth` 256、新建／重命名／回复／Working 均使用默认头像、选中不重着色、1024／320 light／dark 无横向溢出、0 运行时错误。
- 限制：所有 Working／回复／历史都是本地模拟，不代表后端并行或真实模型调用；sandbox 预览禁止原生表单提交，因此发送／创建／保存走显式本地处理；Lucide 图标由宿主沙箱提供，脱离宿主的独立预览会显示空占位；host 模型配置、后台并行与 contact 级持久化仍 deferred。

## 当前设计预览不做

多 agent 协作、shared memory、后台并行执行的真实承诺、Stop／取消控件、原始推理链展示、contact 级模型/provider/key 设置（deferred）、头像 picker／上传／contact 级头像设置（same-family 集合以后再评估）。

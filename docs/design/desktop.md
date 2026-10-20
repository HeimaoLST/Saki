# Desktop contacts：首版运行时接入

更新日期：2026-10-20。状态：**实现事实（#63 分支；依赖 #60 已合入的 stdio v1 契约）**。原生 MyGo 应用已用 `Saki --stdio` 消费真实 runtime，并已在真实 macOS 窗口完成发送/切换/设置/退出/重启实测；contact store、任务事件与解析后的历史由 GUI 自身维护，GUI 不读取 Rust journal。最终合并以 dev 为准。

[previews/desktop-contacts.html](previews/desktop-contacts.html) 仍是早期浏览器示意，不是当前原生应用的验收对象；仓库内 `desktop/` 是当前可运行的原生应用。调色板与布局设计沿用下文。concurrency、`search_history`／`read_history`、附件、模型切换等仍是后续独立任务，不在首轮。

## 已确认需求与交付边界

需求／产品决定（用户确认，#44）：首次使用无需先定义 agent。添加联系人即得到可输入的默认通用助手；name／purpose／instructions／workspace 为可选设置。默认助手不解决 host 的模型、凭据与工具配置。

实现事实（#63）：
- 正常启动从 GUI contact store 恢复；默认创建一个 `Saki` 联系人，零配置即可看到界面。测试/截图 fixture 仍保留在 `seededApp()`，正常真实启动不会混入种子历史。
- Add contact 一次点击创建并选中 `New contact N`、`Role=Assistant`、Ready 的空联系人，composer 取得一次焦点；不创建 runtime session，直到第一次发送。
- contact → runtime `session_id` 的映射、显示名、profile、草稿和选中项由 GUI 持久化；消息正文不复制，历史只来自 `session.history` 的已解析 journal 记录。
- Settings：显示名始终可改；`purpose/instructions/workspace` 只在首次 `session.create` 时作为 profile/system prompt 与 working directory 使用。已有 `SessionID` 或首次 create 在途时，这三项只读，保存设置也只更新名字，不重建 session、不丢历史/草稿、不伪称热更新生效。
- 首轮单执行槽：`busy` 明确拒绝且草稿保留；不做队列或并发。查看另一个联系人不会 cancel/release 当前任务，事件按 `(session,task)` 路由。
- 首次 create 后，GUI 先回到 UI 线程设置 `session_id` 并确认 `store.save` 成功，才调用 `task.send`；保存失败不发送、保留草稿并显示错误。已有 session 的发送同样要求 contact/draft 持久化成功，不把未保存的映射用于执行。
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

可复制的本机构建与启动（不含 key；key 由宿主 `.env`/环境提供）：

```sh
cd desktop
CGO_ENABLED=0 go build -o /private/tmp/saki-gui/o .
SAKI_RUNTIME_BIN=/abs/path/to/Saki \
SAKI_GUI_STORE=/private/tmp/saki-gui/contacts.json \
SAKI_SESSION_DIR=/private/tmp/saki-gui/sessions \
SAKI_GUI_WORKDIR=/abs/working-directory \
/private/tmp/saki-gui/o
```

打包应用：
```sh
# Contents/MacOS/Saki 为 GUI，Contents/MacOS/Saki-runtime 为 runtime，无需 SAKI_RUNTIME_BIN
open "/private/tmp/saki-gui-63-app/Saki.app"
# 需要显式 env 时也可从终端启动 GUI 可执行文件
SAKI_GUI_WORKDIR=/abs/working-directory \
  "/private/tmp/saki-gui-63-app/Saki.app/Contents/MacOS/Saki"
```
Finder 启动使用默认 store／session／home workdir，且不继承 shell 环境；把 `.env` 放在该 workdir，或从终端带 env 启动。不要把 key 放进 Info.plist 或仓库。

## 验证与证据

- Go：`CGO_ENABLED=0 go test ./... -count=1`、`go vet ./...`、`go build ./...`、`go test -race ./...` 均通过（本分支候选）。
- 真实 runtime + 应用层集成（opt-in，受控 fixture）：
  ```sh
  SAKI_TEST_RUNTIME_BIN=/abs/path/to/Saki CGO_ENABLED=0 go test \
    -run 'TestRuntimeProcessIntegration|TestGUIAppRuntimeFlow|TestGUIFirstCreateUsesProfileSnapshot|TestGUIAppPersistsMappingBeforeSend|TestGUIAppSaveFailureStopsBeforeSend' -count=1 .
  ```
  覆盖 read 工具、task/message 关联、GUI send/切换 B 草稿隔离、绑定设置只改名、drain/重启零 provider 调用、继续前文、首次 create 使用 profile 快照、mapping 持久化先于 task.send，以及 store 保存失败时零模型/工具调用且草稿保留。默认 CI 未设置 `SAKI_TEST_RUNTIME_BIN` 时 skip；QA 需要提供候选 binary 才能执行。
- 线上 provider 应用层 smoke（opt-in，不使用受控 fixture）：
  ```sh
  SAKI_TEST_ONLINE=1 SAKI_TEST_ONLINE_WORKDIR=/path/to/repo \
  SAKI_TEST_RUNTIME_BIN=/abs/path/to/Saki CGO_ENABLED=0 go test \
    -run TestGUIOnlineSmoke -count=1 .
  ```
  本次在宿主 `.env` 组合上以 `SAKI_API=openai-chat-completions` 运行，模型为 `opencode-go/deepseek-v4.1-flash`；GUI 应用管线完成真实 read `probe.txt`（唯一 marker）→ tool result → final，任务 completed。默认 Responses 对同一 host 返回 `model_error: missing field status`，属 provider 与 Responses 形状不兼容，不是 GUI 改写；不要把单次结果写成所有 provider 普遍兼容。Python driver 的 transport 级 smoke 不等同于本项 GUI 应用管线证据。
- 最终候选 `4001fd4` binary sha256 `f095bb77…588f6`；官方 driver `--self-test` 单次 PASS（real read、correlated journal、cross-process restore、invalid startup、stdout-loss drain/exit，4 model requests）。
- macOS 最终候选包（产品修正后重建）：`/private/tmp/saki-gui-63-app/Saki.app`，`Contents/MacOS/Saki` 为 GUI（sha256 `be4cbadc6279685b720b78d012e7627cd114348a020b2b5f623887505167d9a5`），`Saki-runtime` 为候选 runtime（sha256 `0828c59072c313e74e458f78e7f25122c30d1e838ebc17db3c77eac6aafb0379`）；构建源码 SHA `a794bc970a4622228b2d453eb1e9f5c7d837f1a1`，日志在 `/private/tmp/saki-gui-63-app/logs/`。Info.plist `dev.saki.desktop`，ad-hoc 签名，`codesign --verify --strict` 与 `--deep --strict` 通过；bundled helper 官方 driver PASS。
- 产品源码在本校准前冻结于 `a794bc9`；`12f631f` 为 docs-only head，本证据范围校准是其后唯一 docs 提交。旧签名包（GUI sha256 `17456503365950cd201ad0d3cebcc6f56aed4feb0d8cd8bc22bb537cf31a2006`）保留为历史完整交互证据，不与最终包混记。

### 原生窗口实测（实现事实，按包区分）

**旧签名包的完整原生验收**（GUI `17456503…2006`；早于 mapping 持久化/断连文案修正）：

- 无配置启动：界面明确显示 `API_KEY` 缺失原因，Send 禁用；不是只以 exit status 1 退出。
- 原生 Add contact 一次点击即可直接输入；Shift+Enter 换行、Unicode 粘贴多行草稿、改名、切换回来后草稿保留均实测。
- 配置后默认 Saki 以零 profile 发送 `Use read to inspect README.md. Report only its first Markdown heading. Do not write or edit any files.`：Working 期间切到 B 输入独立草稿；切回 A 看到真实 read 工具结果与 `# Saki`，B 历史仍为空且草稿保留。
- 真实 metadata 确认模型 `opencode-go/deepseek-v4.1-flash`、协议 `openai-chat-completions`。
- 绑定后 Settings 的 Purpose/Instructions/Workspace 经 AX 确认为 disabled，Name 可改；改名为 `Saki Native` 后 A 历史不变、B 草稿保留。
- Cmd+Q 退出进程 exit 0；同 store 重启后 B 仍选中且草稿在，A 恢复同一 session、工具与最终文字；仅打开恢复时 journal 字节 SHA 保持 `7b75d436b414e91e7fe0fab08c869742b7298cc0bab51599d7dfb8e161c2a43a`。受控 Go 集成另行证明 load/history 零 provider 调用。
- 重启后继续问 “first heading”，真实回答 `# Saki`；再发送 read `Cargo.toml`，点击 Stop 后 UI 为 Needs attention / `Task interrupted`，已有工具结果与部分输出仍保留（取消不回滚）。History 过滤 “first heading” 与 View in conversation 定位通过。
- 以上完整交互属于旧签名包的历史证据；不能写成全部在最终 `be4cbadc…` 包上逐项跑过。未变行为的证据仍有效。

**最终候选包的定向重验**（GUI `be4cbadc…`，源码 `a794bc9`，修正 mapping 顺序与 unknown 文案）：

- 全新 native-final store，零 profile 首条 read README 通过真实 GUI 提交。
- Working 时 Cmd+Q，app 进程 exit 0；Rust 持久终态 completed，同一 contact/session 映射已保存到 `contacts.json`（session `1792495911452586000-32493`）；主责正在恢复该最终包做结果核对。
- 最终源码差异由受控 Go 应用层回归覆盖：`TestGUIAppPersistsMappingBeforeSend`、`TestGUIAppSaveFailureStopsBeforeSend`，以及 live UI 的断连重启提示。
- 以上是主责 CUA 验收，不是用户试用完成或最终产品验收；最终包未重复旧包的全部交互项。
- 本机验收 env：`SAKI_GUI_WORKDIR=/Users/heimaolst/Workspace/Rust/Saki`，`SAKI_GUI_STORE=/private/tmp/saki-gui-63-native-review/contacts.json`，`SAKI_SESSION_DIR=/private/tmp/saki-gui-63-native-review/sessions`，`SAKI_API=openai-chat-completions`，root 已有 `.env`；重启时 unset `SAKI_RUNTIME_BIN`，应用包内 helper 可直接恢复。
- **未验范围**：CUA 的 typeText 对中文有工具注入限制，因此不能宣称真实系统 IME 组合期已验；VoiceOver 与 light 系统主题也未实测（light/dark 只有 headless 覆盖）。Shift+Enter 与 Unicode 粘贴已验。

## 已知限制

- 单 active task；并发/后台执行由 #61 承载，不承诺 UI 的并行观感。
- `search_history`／`read_history` 未实现；History 只对已加载 transcript 做本地过滤，不是模型检索工具。
- profile 热更新不支持；绑定后只有名字可改，purpose/instructions/workspace 不生效于既有 session。
- 无自动重连；断连后重启应用重新加载 history。未知接纳不自动重发。
- 无附件、托盘/菜单、contact 级模型配置、头像 picker；原生窗口的发送/切换/设置/Stop/History/退出/重启已实测，但真实系统 IME 组合、VoiceOver、light 系统主题、复杂滚动与更多 provider 组合仍需后续验收。
- GUI 不解析 Rust JSONL；旧 journal 的零时间戳显示为空，不伪造日期，缺失 message_id 保持缺失。

## 产品模型

- 用户添加 **CONTACTS**：独立 agent，具有稳定的 name／avatar／profile／requirements 与长期对话；没有 New session 或 session picker 入口。
- 每个 contact 拥有自己的 profile、历史与未来的 memory，不再继承单一全局 Saki 身份。
- 历史查询计划新增 `search_history`／`read_history` 工具，绑定当前 contact。UI 只呈现可理解的活动与带出处的历史消息；primary flow 不暴露 compact、JSONL、token budget 或内部工具名。
- source/history 与当前 settings 分离：绑定后 profile（purpose/instructions/workspace）锁定，只有显示名可改；保存不会改写已保存的消息或草稿。

## 与完整 runtime 需求的剩余差距（实现事实）

| 能力 | 当前实现 | 说明 |
| --- | --- | --- |
| Contact 持久边界 | GUI contact store + `session_id` 映射；历史由 `session.history` 提供 | GUI 不解析 JSONL；重启恢复同一 ID |
| 查看 B 与 A 执行 | 单执行槽；A 运行时切到 B 查看不会 cancel A，B 的发送返回 busy | 同宿主 A/B 同时执行仍由 #61 承载，不是首轮承诺 |
| 历史检索工具 | 未实现 `search_history`／`read_history` | History 只对已加载 transcript 做本地过滤 |
| 附件 | 未接入 GUI；runtime 现有图片输入能力未暴露 | 不保存文件内容 |
| 模型配置 | host 环境变量／启动配置；contact 级配置 deferred | GUI 不改写 API/key |

上述是当前交付边界；不得把单执行槽或本地过滤写成并发执行／模型检索已完成。

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

## 关键流程（当前原生应用）

1. **选择 contact**：切换 name、profile、messages 与 draft；草稿按 contact 保存。A 在 runtime 执行时切到 B 查看不会 cancel A；首轮 B 的发送返回 busy。状态词只描述工作：Ready／Working／Needs attention，不假装在线。
2. **Add contact**：一次点击直接创建、选中 Ready 默认联系人并进入空会话；无需填写 Name／Purpose／Instructions／Workspace。composer 只取得一次焦点，连续创建也可直接输入。个性化通过可选 Settings 编辑；首次发送才创建 runtime session。浏览器旧示意的 Create／Cancel 表单是历史原型。
3. **Contact settings**：显示名始终可编辑；purpose／instructions／workspace 只在首次 create 时使用，绑定后只读。不会提示“changes apply to future messages”，也不会重建 session 或丢历史/草稿。
4. **Composer**：textarea、Send 与 Stop；Enter 发送、Shift+Enter 换行、IME 组合期不发送。发送进入真实 runtime：接纳后显示等待开始，`task_started` 后才 Working；delta 流式、final 覆盖同一项；工具与最终文字按 task 归属。当前没有附件入口。
5. **Source action（仅浏览器/本地 fixture 历史）**：样例来源面板与 View in conversation 是旧演示能力；真实模式暂不展示样例 Sources，也不伪造引用。
6. **History action**：对当前 contact 已加载的 transcript 做本地过滤；无结果有明确说明。它不等于 runtime 的 `search_history`，也不会出现其他 contact 的消息或暗示 shared memory。

## 状态与反馈

| 状态 | 表现与下一步 |
| --- | --- |
| Empty conversation | 可直接写第一条消息，或按需编辑 Settings；无 profile 不阻止开始 |
| Working | 会话尾部有进行中行；Send 暂时不可用，Stop 可用；切走保留状态 |
| Needs attention | gold 状态 + 已保存的失败/中断原因；不在未知接纳时自动重发 |
| No history results | 说明该 query 在此 contact 已加载历史中无匹配，建议更短的短语 |
| Validation | Settings 保存时名称不得为空；绑定后 profile 字段只读 |
| Sources | 仅旧浏览器/fixture 历史；真实模式不显示样例引用 |

## 可访问性与可视化契约（浏览器示意）

原生 button／input／textarea，语义标签与可见 focus；aria-live 只播报有意义的更新（联系人切换、回复到达、创建/保存、无结果）；用户文本一律通过 textContent 写入 DOM。所有 ID 唯一，CSS 限定在 `#saki-desktop-contacts` 下。1024／736／375／320 宽度与 light/dark 均可读，无横向溢出。

## 验收清单（2026-10-14 浏览器历史记录）

以下是浏览器旧原型的历史结果，不替代当前原生流程验收。当前创建与设置证据见上文 Runtime-backed native app；本节仅是浏览器历史快照。

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

## 当前不做

多 agent 协作、shared memory、同宿主后台并行执行、原始推理链展示、contact 级模型/provider/key 设置（deferred）、附件、头像 picker／上传／contact 级头像设置（same-family 集合以后再评估）。Stop／取消已实现，只按 `(session,task)` 生效。

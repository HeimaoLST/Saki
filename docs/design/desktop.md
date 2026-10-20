# Desktop contacts：首版运行时接入

更新日期：2026-10-20。状态：**实现事实（#63 运行时接入、#90 局部布局打磨）；#92 主题系统仅完成设计，功能尚未实现**。原生 MyGo 应用已用 `Saki --stdio` 消费真实 runtime，并已在真实 macOS 窗口完成发送/切换/设置/退出/重启实测；contact store、任务事件与解析后的历史由 GUI 自身维护，GUI 不读取 Rust journal。最终合并以 dev 为准。

[previews/desktop-contacts.html](previews/desktop-contacts.html) 仍是早期浏览器示意，不是当前原生应用的验收对象；仓库内 `desktop/` 是当前可运行的原生应用。当前代码仍使用 Main/Dawn 派生色；#92 设计目标是用户确认的 Moon/Dawn，但本轮只交付设计，不代表换色功能已实现。concurrency、`search_history`／`read_history`、附件、模型切换等仍是后续独立任务，不在首轮。

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

## 布局打磨（#90，实现事实）

一次局部布局调整，不改发送/取消/runtime/store 语义：
- 联系人侧栏保持 220px：品牌与 Contacts/Add contact 收敛为轻量入口；行内统一 32px 头像、名称和单行摘要；选中态只使用 surface hover，无边框；Working/Needs attention 合并在摘要行显示，未读仍归原联系人。
- 顶部降为 28px 头像、15px 名称、11px 状态/角色；History/Settings 作为次级按钮，移除 header 下的 divider。History/Settings/Add contact/Details 使用至少 12px 文字和可用点击 padding，靠背景/对比分层，不引入图标库。
- 浅色小字可读性在 `theme.go` 统一语义色收口（Dawn；Dark 不变）：Warning `#985900` 对 Surface/SurfaceHover/Background = 5.37/4.65/5.11；TextMuted `#64607b` = 5.78/5.00/5.49；Danger `#9c4a62` = 5.66/4.91/5.39（WCAG 计算值，均 ≥4.5）。未读数字改用可读 Warning，Located 标签改用 TextMuted；未增加 literal 颜色测试。
- 聊天主体采用 `WidthPercent(86) + MaxWidth(760)` 的同一居中列（header/chat/composer 共用），宽窗口有绝对阅读上限，窄窗口按 86% 收缩；用户气泡 `MaxWidthPercent(80)`。助手正文不再重复头像/姓名/时间；历史引用仍保留日期/说话人。
- 工具活动收敛为 `name · path/command`、摘要和 Details。摘要优先显示 `error`、`timed_out`、非零 `exit_code`，再显示普通 output，因此失败/超时默认可见；Details 展开保留原始结果截断。工具失败仍作为工具结果交给模型，不把整轮 Task 判失败。
- 真实终态原因始终由 `StatusDetail` 显示，不因最后一条 message 有 AlertTitle 就隐藏；错误展示不做 fixture 猜测。
- Composer 贴底，输入区固定 44px（多行时内部滚动，不自动增高），圆角 14；保留多行、Enter/Shift+Enter、IME 组合不发送、一次性焦点、Send/Stop/错误与 unknown 恢复提示。
- 960×640 与打开 settings 的 300px aux 下，阅读列按百分比收缩，不横向溢出；没有提高最小窗口。

Headless 证据（fixture，不等于线上模型或原生窗口）：
- before：`/private/tmp/saki-gui-90/before/`（12 张 demo + 11 张同场景 `headless-*runtime-*`，由临时 `git archive 50d6b9c` baseline module 加上同一截图 fixture 生成；旧 view）。before/after 的 runtime fixture 都先执行 `loadAvatar()`，同一 mascot 资产，避免 before 头像为空。
- after：`/private/tmp/saki-gui-90/after/`（12 张 demo + 11 张 `headless-*runtime-*`，含 1600×900 宽窗口；runtime-backed 形状的消息/工具/Working/错误/只读 settings）。
- 生成：`SAKI_PREVIEW_DIR=/private/tmp/saki-gui-90/after CGO_ENABLED=0 go test -run 'TestPreviewImages|TestPreviewRuntimeStates' -count=1 .`
- 真实 runtime 五项集成：`SAKI_TEST_RUNTIME_BIN=/abs/Saki go test -run '^(TestRuntimeProcessIntegration|TestGUIAppRuntimeFlow|TestGUIFirstCreateUsesProfileSnapshot|TestGUIAppPersistsMappingBeforeSend|TestGUIAppSaveFailureStopsBeforeSend)$' -count=1 -v .`
- 不新增样式数值测试；键盘焦点、草稿隔离、绑定 profile 只读、运行中切换/Stop/history 的回归仍由现有 Go 测试与真实 runtime 集成覆盖。
- 本轮已 merge `origin/dev=6a682d8`（含 #87 required CI 五项 GUI runtime 门禁）。

### 原生窗口验收（实现事实，范围受限）

主责使用独立 `fe964f4` app + `0828c5` helper，将 #63 的实际 session 历史复制到 `/private/tmp/saki-gui-90-native`，真实启动 Go→Rust restore（未新增付费模型调用）。已验：
- Ready 加载 README 实际历史；Details/Hide 展开收起原始工具结果；History 用 first Markdown 过滤，View in conversation 返回并显示 Located。
- 绑定联系人 Purpose/Instructions/Workspace 为 disabled，仅 Name 可编辑；改名 `Saki Layout` 后同一 session 历史保留。
- Add contact 自动聚焦；粘贴中文多行 + ShiftEnter + 第三行不发送；切换 A/B 草稿隔离。
- CmdQ 进程退出 0；核对 `contacts.json`，同一 session id 与完整三行草稿已落盘。
- 主责看到完整暗色真实窗口聊天/草稿图，默认尺寸未观察到布局遮挡。

**未验/限制**：后续 CUA 截图先退化为 230px 透视缩略，再使用 ScreenCaptureKit `-3812/-3811`；重绑和 REPL 重建仍失败。因此本轮**不声称**原生最小 960×640、浅色切换、重启后窗口、真实系统 IME、VoiceOver 或用户试用通过。960×640+aux、两色与宽窗口仍由 headless 截图验证；运行中切换/Stop 及恢复语义由受控本地 provider 五项测试和现有 Go UI/model 测试验证。本轮未重复付费线上 read，沿用 #63 已有证据。

独立复核：`SAKI_TEST_RUNTIME_BIN=<90 helper> CGO_ENABLED=0 go test -race ./... -count=1 -v` 全通过（11.934s），五项 PASS 均存在，工具失败/timeout 回归通过；日志 `/private/tmp/saki-gui-90-app/logs/main-race-full.log`。`go vet`/gofmt/diffcheck、hash/go version/codesign deep strict 均已核对一致。

独立 #90 候选包（不覆盖 #63 试用包；已冻结）：
- `/private/tmp/saki-gui-90-app/Saki.app`；GUI sha256 `ba5fda24c811fb226995c4f2fae6fd6fa6203976c7557789d5bc8d39fe1855c1`，runtime helper sha256 `0828c59072c313e74e458f78e7f25122c30d1e838ebc17db3c77eac6aafb0379`（复用 #63 helper）。
- 构建源码 SHA `fe964f4769d78f845dc8d5487b0ac8cc434fd7fd`；`go version -m` 记录 `vcs.revision=fe964f4`、`vcs.modified=false`，日志 `/private/tmp/saki-gui-90-app/logs/`；ad-hoc 签名，`codesign --verify --strict` / `--deep --strict` 通过，arm64。

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
- Working 时 Cmd+Q，app 进程 exit 0；Rust 持久终态 completed，同一 contact/session 映射已保存到 `contacts.json`（session `1792495911452586000-32493`）。同一 native-final store 重启后，原生窗口恢复同一 session，看到实际 read README 与最终 `# Saki`，状态 Ready；再次 Cmd+Q 仍 exit 0。
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
- #92 的 Moon/Dawn、Appearance 模式/持久化、系统外观切换和自定义主题加载**尚未实现**；当前代码仍是 Main/Dawn 派生。设计完成不等于功能交付。

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

## 布局与断点（浏览器历史示意）

本节保留浏览器历史示意；当前原生布局以“布局打磨（#90）”为准。

- **≥920px**：`contact rail (默认 220px)` ｜ conversation ｜ 右侧 aux 300px，仅在 source/settings 等操作后出现；没有常驻第三栏。
- **640–919px**：rail + 单栏内容；source/settings 直接替换主内容，而不是压缩两栏。
- **<640px**：contact list 变为可展开 compact 列表；aux 同样替换主内容。
- 不使用 viewport-height 布局、fixed 定位或内部滚动盒；内容随页面增长，任何宽度都不出现横向溢出。
- Tweak（宿主支持时）：contact list width、message spacing、reading width 三个只影响布局的参数。

## 主题系统（#92 设计）

状态：**设计完成，功能未实现**（[#92](https://github.com/HeimaoLST/Saki/issues/92)）。用户已确认：夜间使用 Rosé Pine Moon，白天使用 Rosé Pine Dawn；未来需要更多配色和用户自定义。本设计只定义 GUI 配色数据、模式、持久化与自定义边界，不把设计关闭写成切换功能已经交付。

### 当前实现事实（不是目标）

- 当前 `desktop/theme.go:8-53`（[源码](https://github.com/HeimaoLST/Saki/blob/3c449404a3648ae0a7932bab749adf3a884ef062/desktop/theme.go#L8-L53)）的深色是 **Main**：`base #191724`、`surface #1f1d2e`、`overlay #26233a`、`border #403d52`、`accent #ebbcba`、`accentText #191724`；不能把它称为 Moon。
- 当前浅色是 Dawn 派生：`text #575279` 是旧值，官方 Dawn 现为 `#464261`；`TextMuted #64607b`、`Warning #985900`、`Danger #9c4a62` 是 #90 为可读性做的派生，不是官方 raw 值。
- 当前 `desktop/view.go:11-13`（[源码](https://github.com/HeimaoLST/Saki/blob/3c449404a3648ae0a7932bab749adf3a884ef062/desktop/view.go#L11-L13)）每帧用 `c.Theme().Dark` 选择并 `SetTheme`；没有 app 级 mode/theme ID。
- 当前 `desktop/store.go:32-36`（[源码](https://github.com/HeimaoLST/Saki/blob/3c449404a3648ae0a7932bab749adf3a884ef062/desktop/store.go#L32-L36)）只有 `Version/Selected/Contacts`；`:56-99` 读取并严格检查 `Version==1`；`:138-161` 保存时重建 state。没有 appearance 字段，也没有主题目录/loader。
- 当前 `desktop/main.go:41-48` 使用默认 titlebar，未设置 `BackgroundColor`/`TitleBarStyle`/`Vibrancy`。
- 当前没有 app 级 `Link/Progress/BubbleUser/QuoteHighlight` token；`t.Selection` 同时用于 user 气泡 `view.go:273` 和 history 高亮 `view.go:197`。A 必须拆出应用 token，不能把 MyGo 的文字选择色当聊天气泡色。
- 旧浏览器原型的视觉规则与头像尺寸只在文末历史验证节保留，只作历史快照；当前原生尺寸见 #90 节与头像节，不是主题目标。

### 官方原始 palette

来源：[Rosé Pine 官方 palette](https://rosepinetheme.com/palette/)（2026-10-20 核对原始 HTML；Dawn 的 `#464261` 存在，旧文档 `#575279` 不在官方表中）。

| 原始角色 | Moon（dark） | Dawn（light） |
| --- | --- | --- |
| base | `#232136` | `#faf4ed` |
| surface | `#2a273f` | `#fffaf3` |
| overlay | `#393552` | `#f2e9e1` |
| muted | `#6e6a86` | `#9893a5` |
| subtle | `#908caa` | `#797593` |
| text | `#e0def4` | `#464261` |
| love | `#eb6f92` | `#b4637a` |
| gold | `#f6c177` | `#ea9d34` |
| rose | `#ea9a97` | `#d7827e` |
| pine | `#3e8fb0` | `#286983` |
| foam | `#9ccfd8` | `#56949f` |
| iris | `#c4a7e7` | `#907aa9` |
| highlightLow | `#2a283e` | `#f4ede8` |
| highlightMed | `#44415a` | `#dfdad9` |
| highlightHigh | `#56526e` | `#cecacd` |

**决定**：
- 内置 night 方案 ID `rose-pine-moon`，显示名 `Rosé Pine Moon`；day 方案 ID `rose-pine-dawn`，显示名 `Rosé Pine Dawn`。
- 不把 Main 加入内置方案；当前 Main 只作为实现历史/迁移来源。
- 原始 palette 只是数据源；组件不读取 `rose/pine/foam` 等品牌角色，不按 `themeID` 写分支。

### 语义映射与可读派生

应用只向组件暴露语义角色；下表的 Moon/Dawn 值是设计基线（原始 + 有依据的派生）。`SurfacePressed` 明确复用 `SurfaceHover`，避免把 `TextMuted` 放到更深的 highlightMed 上造成低于 4.5；控件按下仍由框架的按钮状态表达。

| 语义角色 | Moon | Dawn | 说明 |
| --- | --- | --- | --- |
| Background | `#232136` | `#faf4ed` | 官方 base |
| Surface | `#2a273f` | `#fffaf3` | 官方 surface |
| SurfaceHover | `#393552` | `#f2e9e1` | 官方 overlay |
| SurfacePressed | `#393552` | `#f2e9e1` | 与 hover 同色；不把 muted 小字放此层 |
| Border | `#44415a` | `#dfdad9` | 官方 highlightMed；只作非文字边界，不单独承担控件识别 |
| Text | `#e0def4` | `#464261` | 官方 text |
| TextMuted | `#b8b4cc` | `#64607b` | Moon 派生；Dawn 沿用 #90 派生 |
| Accent | `#ea9a97` | `#d7827e` | 官方 rose；主按钮背景 |
| AccentHover | `#f2b0ad` | `#e09a96` | 由 rose 派生 |
| AccentPressed | `#d98885` | `#d0736f` | 由 rose 派生；保证 AccentText ≥4.5 |
| AccentText | `#232136` | `#232136` | 深色前景，不用白字（白字仅 2.19/2.84） |
| Danger | `#f79bb2` | `#9c4a62` | Moon 派生；Dawn 沿用 #90 派生 |
| Warning | `#f6c177` | `#985900` | Moon 官方 gold；Dawn 派生（官方 gold 仅 2.16/1.87） |
| Success | `#9ccfd8` | `#286983` | Moon foam / Dawn pine；用于可读文本/状态 |
| Selection | `#44415a` | `#dfdad9` | MyGo 的文字选择高亮（highlightMed） |
| Focus | `#c4a7e7` | `#907aa9` | 官方 iris；非文字 3:1 |
| Link（app） | `#9ccfd8` | `#286983` | 与 Success 初始复用，但独立语义 |
| Progress（app） | `#9ccfd8` | `#286983` | 与 Link 初始复用，但独立语义 |
| BubbleUser（app） | `#393552` | `#f2e9e1` | user 气泡背景；不再借用 Selection |
| QuoteHighlight（app） | `#2a283e` | `#f4ede8` | history 定位高亮 |
| Scrollbar（app） | `TextMuted` @ 35% alpha | `TextMuted` @ 35% alpha | 非文字；无对比度门槛 |
| Inverse（MyGo） | `Text` | `Text` | 零值 fallback 即可 |
| InverseText（MyGo） | `Background` | `Background` | 零值 fallback 即可 |

**可读性计算**（WCAG；正文/小字目标 ≥4.5:1，背景列为 base / surface / overlay）：

| 前景 | Moon | Dawn |
| --- | --- | --- |
| Text | 11.86 / 10.90 / 8.82 | 8.69 / 9.14 / 7.92 |
| TextMuted | 7.78 / 7.15 / 5.78 | 5.49 / 5.78 / 5.00 |
| Danger | 7.66 / 7.04 / 5.70 | 5.39 / 5.66 / 4.91 |
| Warning | 9.55 / 8.77 / 7.10 | 5.11 / 5.37 / 4.65 |
| Link/Success | 9.19 / 8.44 / 6.83 | 5.59 / 5.88 / 5.09 |
| AccentText on Accent | 7.13 | 5.50 |
| Focus（非文字 3:1） | 7.47 / 6.87 / 5.56 | 3.47 / 3.65 / 3.16 |

- 官方 raw 的 `subtle/love/gold` 在 Dawn 不足 4.5，`subtle` 在 Moon 的 overlay 也不足；因此 TextMuted/Danger/Warning 采用派生值。
- `Border`（highlightMed）对 Surface 对比不足 3:1，不能作为唯一控件边界；控件通过 surface 层级、文字和 Focus 识别。`TextMuted` 不放在 SurfacePressed/highlightMed 上。
- 不为每个 hex 写机械测试；对比度作为设计计算，实施验收检查真实组合。

### Appearance、模式与解析

- **入口**：应用级 `Appearance`，独立于 Contact Settings；建议放在联系人侧栏底部的低权重入口，显示当前有效主题名。Contact Settings 仍只编辑联系人 profile。
- **模式**：`system`（默认）/`light`/`dark`；按 OS 明暗，不做定时切换。light 与 dark 各自记录 scheme ID，默认 `rose-pine-dawn` / `rose-pine-moon`。当前每类只有一个内置方案，A 阶段只显示“当前有效主题”（不显示没有选择意义的空下拉）；B 阶段有多个方案/自定义后再显示选择器。
- **app model**：持有 `Appearance{mode, lightID, darkID}`；每帧只有一个 resolver：根据 mode 与 OS 明暗选出 scheme，再映射成 `ui.Theme`。`view` 只在入口调用一次 resolver，后续组件只接收 `*ui.Theme` 并读取语义角色；不在组件里判断 Moon/Dawn/品牌名。
- **解析顺序**：`mode=system` 时读取当帧 `c.Theme().Dark`（这是 SetTheme 之前的默认主题，反映 OS/后端 effective appearance）；`mode=light|dark` 时直接使用存储的 mode，不把 `SetSource` 后的 forced Dark 当 OS。解析后立即 `c.SetTheme`，本帧后续使用该主题。
- **禁用项**：不改布局、字体大小、圆角、spacing；不重建 runtime/session/contact，不取消/重发任务，不清草稿、焦点、选择项、滚动位置。换主题不是换 app 状态。
- **框架重绘**：`Context.SetTheme` 每帧局部（`ui/context.go:305-315`，[链接](https://github.com/egoist/mygo/blob/fea932a7a2a9/ui/context.go#L305-L315)）；`ui/runtime.go:118` 每帧重置默认主题。因此不要只在启动时 SetTheme，也不要注册重复 listener 来维护 effective 状态；每帧 resolver 即可。
- **禁止重复 SetSource**：`mygo.Theme.SetSource` 只在启动和模式变更时调用一次；不在每帧调用，以免每帧触发 `contentThemeChanged`。系统模式恢复 `ThemeSystem`，手动模式设 `ThemeLight/ThemeDark`；`SetSource` 的说明见 `modules.go:411-429`（[链接](https://github.com/egoist/mygo/blob/fea932a7a2a9/modules.go#L411-L429)）。

### 持久化

- **单一字段**：contact store 顶层可选 `appearance`，不写进 contact/session/profile：
  ```json
  {
    "appearance": {
      "mode": "system",
      "light": "rose-pine-dawn",
      "dark": "rose-pine-moon"
    }
  }
  ```
- **兼容**：保持 `Version=1`；旧文件缺 `appearance` 时使用默认 `{system, rose-pine-dawn, rose-pine-moon}`。`store.save` 必须保留该字段；当前 `store.go:138-161` 重建 state 的实现边界必须同步改，不能一次联系人保存把主题偏好抹掉。未来若字段形状不兼容再 bump store 版本并提供迁移，不在本次实现。
- **失败**：切换先让当次会话生效（内存/当前窗口），随后保存；保存失败显示明确“主题已生效但未保存”，不伪称重启后保留。保留当前生效，不因保存失败重启 runtime、回滚任务或清草稿。
- **边界**：主题持久化只走现有的 `contactStore` 保存入口，不新建独立配置文件、不复制 store、不在 view 散落写盘。

### 系统外观、窗口 chrome 与系统偏好

- **系统来源**：`ui.Context.Theme()` 在 SetTheme 前返回默认主题；`Dark` 来自后端 `IsDark`（`content.go:51`），默认主题还会应用桌面偏好（`ui/preferences.go:49-77`）。`mygo.Theme` 提供 `Source/SetSource/IsDark/OnUpdated`（`modules.go:299-453`）；系统变化链路是 `app.go:593-600` → `content.go:302-307` → `ui/window.go:54-58`，会请求重绘并触发 `OnUpdated`。`SetSource` 会重绘但不会触发 `OnUpdated`。[链接](https://github.com/egoist/mygo/blob/fea932a7a2a9/modules.go#L299-L453)
- **设计使用**：框架已因系统变化重绘，不需要额外 listener 来维护主题；A 不注册 `OnUpdated`，不缓存 effective theme。仅启动/模式变更调用一次 `SetSource`；system 模式使用系统来源，manual 模式固定 light/dark。
- **窗口 chrome / backing color**：A 只保证平台明暗：`SetSource` 让 native titlebar/menus 跟随 light/dark；`WindowOptions.BackgroundColor` / `Window.SetBackgroundColor` 设置窗口 backing color（`window.go:934-946`，[链接](https://github.com/egoist/mygo/blob/fea932a7a2a9/window.go#L934-L946)）。**`light-dark()` 不是只跟随原始 OS**：`backgroundColor()` 用 `backend().Theme().IsDark()` 选择 light/dark 分支（当前 `window.go:393-414`，[链接](https://github.com/egoist/mygo/blob/fea932a7a2a9/window.go#L393-L414)；最新 `window.go:405-426`，[链接](https://github.com/egoist/mygo/blob/163d50e2acc1b5361d29bc6d26a8a0e11ebdb847/window.go#L405-L426)），而 `SetSource` 走同一个 backend override 并调用 `updateBackgrounds()`（当前 `modules.go:414-429`，[链接](https://github.com/egoist/mygo/blob/fea932a7a2a9/modules.go#L414-L429)；`window.go:406-414`）。因此它跟随 **effective appearance**，手动 light/dark 也会生效。
  - A 的做法：system 模式设置 `light-dark(lightSchemeBase, darkSchemeBase)`；manual 模式设置 `light-dark(modeBase, modeBase)`（两 stop 同色），仅在模式/有效 scheme 变更时更新，不每帧设置。系统变化由框架的 `updateBackgrounds` 处理，不需要为背景重复注册 listener。
  - B 阶段若任意 theme ID/自定义选择无法用两个固定 stop 表达，可在 scheme 变更时用显式 `SetBackgroundColor(resolvedBase)`；system 模式下若 OS 变化也需要同步该背景（只更新窗口背景，不干预主题 resolver）。A 不实现这条额外路径。
  - native titlebar/menus 仍只有 light/dark，不承诺任意品牌色填充；自绘 titlebar 需要 hidden titlebar/frameless + `Context.TitleBar`/拖动区域，另立 feature 并做真实窗口验收。
- **系统偏好边界**（基于源码，不凭空承诺全系统 accessibility）：
  - `ReduceMotion`：MyGo 的 `Animate`/`Transition`/`Timeline` 会遵循；A 没有新增持续动画，因此无额外承诺。
  - `HighContrast`/`TextScale`：默认主题的 `Theme.follow` 会应用（`ui/preferences.go:49-77`），但 `Context.SetTheme` 的自定义主题不会自动 follow。当前 #90 的 `roseTheme` 已经绕过它们；A 明确**不新增**全系统 HighContrast/TextScale 适配，也不声称已支持。若产品要求，需在 resolver 中集中处理并单独验收，不扩本次配色功能。
  - 系统 Accent：A 使用 Moon/Dawn 自有 accent，不用系统 accent 替换品牌色；这是配色选择，不作为系统 accent 已适配的声明。
  - `ReduceTransparency`：最新 MyGo 的 `ThemePreferences` 已增加（[定义](https://github.com/egoist/mygo/blob/163d50e2acc1b5361d29bc6d26a8a0e11ebdb847/modules.go#L320-L350)）；当前 app 不使用 `Vibrancy`/透明材质，A 无需新增机制。若未来使用材质，应在该偏好为 true 时关闭/blur 替换为不透明 surface，单独验收。
- **模式切换与字体**：换主题不改变 FontSize/Radius/Spacing，也不改布局；只有颜色变化。系统 TextScale 若未来实现，必须在所有 scheme 上统一处理，不能因换色改变字号。

### 自定义主题（B 阶段设计）

- **目录**：`UserConfigDir()/Saki/themes/*.json`；一个文件一个主题。`id` 必须是小写 kebab-case 且与文件名（去 `.json`）一致；内置 ID 前缀 `rose-pine-` 保留，用户不能覆盖。
- **格式**：完整语义颜色定义，不做继承、表达式、脚本、模板或品牌角色输入。`schema_version` 当前为 `1`，必须与 `id`、`name`、`kind`（`light|dark`）、`colors` 一起提供。
- **必填 colors**：`background/surface/surfaceHover/border/text/textMuted/accent/accentHover/accentPressed/accentText/danger/warning/success/selection/focus`。
- **可选 colors 与唯一 fallback**：`surfacePressed→surfaceHover`、`link→success`、`progress→link`、`bubbleUser→selection`、`quoteHighlight→selection`、`scrollbar→textMuted`、`inverse→text`、`inverseText→background`。可选字段缺失时使用该 fallback，不继承其他主题。
- **颜色格式**：`#rrggbb` 或 `#rrggbbaa`；不接受命名色、函数色或透明度运算，保证单一 validator 可判定。
- **完整可解析示例**（`my-moon.json`）：
  ```json
  {
    "schema_version": 1,
    "id": "my-moon",
    "name": "My Moon",
    "kind": "dark",
    "colors": {
      "background": "#232136",
      "surface": "#2a273f",
      "surfaceHover": "#393552",
      "surfacePressed": "#393552",
      "border": "#44415a",
      "text": "#e0def4",
      "textMuted": "#b8b4cc",
      "accent": "#ea9a97",
      "accentHover": "#f2b0ad",
      "accentPressed": "#d98885",
      "accentText": "#232136",
      "danger": "#f79bb2",
      "warning": "#f6c177",
      "success": "#9ccfd8",
      "selection": "#44415a",
      "focus": "#c4a7e7",
      "link": "#9ccfd8",
      "progress": "#9ccfd8",
      "bubbleUser": "#393552",
      "quoteHighlight": "#2a283e",
      "scrollbar": "#908caa",
      "inverse": "#e0def4",
      "inverseText": "#232136"
    }
  }
  ```
- **单一 loader 边界**：读取目录、解析 JSON、schema/字段/颜色/`kind`/ID 唯一性/内置冲突；错误必须报告 `file` 与字段（例如 `colors.textMuted`）；不把坏文件静默覆盖。一个文件失败只影响该文件，同 kind 回退默认内置 Moon/Dawn。
- **冲突与缺失**：
  - 内置 ID 冲突：用户文件无效，内置优先；报告 `id`。
  - 自定义 ID 重复：所有该 ID 文件无效（不任意选一个），报告全部文件；不会覆盖内置。
  - 已选文件被删除/改名/变无效：回退同 kind 默认内置；保留 store 中的选择 ID，便于文件恢复；显示非阻塞提示；不删除/改写用户文件。
  - 缺字段/坏颜色/未知字段：拒绝该文件并报 file/field；未知字段按 v1 拒绝，避免拼写错误静默生效。
- **对比度**：loader 可对 `text/background`、`text/surface`、`text/surfaceHover`、`accentText/accent` 计算并提示低于 4.5；这是提示，不阻止保存/加载（按用户自定义自由）；内置 Moon/Dawn 必须通过验收。不要把审美校验扩展成通用主题引擎。
- **B 的非目标**：不做主题商城、下载、在线同步、可视化编辑器、热重载、跨 TUI/Runtime 主题系统。B 只做加载/发现/选择与单一错误边界。

### 分阶段与验收

- **A（本次设计后的实现任务）**：内置 Moon/Dawn + 单一语义映射 + Appearance 模式/ID + 持久化 + 系统/手动切换 + 窗口平台明暗/背景。要求：
  - #90 最终布局下，1040×720、1600×900、960×640（含 300px aux）的聊天/工具/错误/历史/Settings 截图，light/dark 两套；headless 与实际 macOS 窗口分开记录。
  - system/light/dark 切换、重启恢复、系统外观变化（system 模式）；切换中保留草稿/选中/滚动/任务，不重启 runtime、不重建 session、不取消/重发。
  - 4.5:1 文字对比、焦点可见、状态不只靠颜色；窗口背景/平台 chrome 一致。
- **B（后续独立任务）**：用户主题目录/loader/选择；合法/非法 JSON、缺字段、坏颜色、ID 冲突/重复、已选文件删除的回退与 file/field 错误；内置验收仍强制，自定义对比提示不阻断。
- **设计完成 ≠ 功能实现**：本文件提交后只是设计；A/B 分别建 feature 并验收，未实现前不得在 UI 文案或 README 声称已支持 Moon/Dawn 切换/自定义。
- 依赖升级独立验收：MyGo 更新不混入本设计 PR 的配色实现；升级后重新核对 `SetTheme/SetSource/OnUpdated/Preferences/markdown/animations` 接口并跑目标测试。

### 依赖快照与上游差异（只读核对，不作为冻结政策）

- 当前 `desktop/go.mod:5`（[源码](https://github.com/HeimaoLST/Saki/blob/3c449404a3648ae0a7932bab749adf3a884ef062/desktop/go.mod#L5)）依赖 `github.com/egoist/mygo v0.4.9-0.20261014025635-fea932a7a2a9`（10/14，[Theme 源码](https://github.com/egoist/mygo/blob/fea932a7a2a9/ui/theme.go)）。这是当前 app 的已验证快照，不是长期冻结版本。
- 用户报告 `@latest` 为 v0.5.5（10/16）；本次用 `go mod download -json` 只读核对官方 main commit `163d50e2acc1b5361d29bc6d26a8a0e11ebdb847`（10/20），解析为 `v0.5.6-0.20261020115920-163d50e2acc1`。仅缓存，未改 `go.mod/sum`。
- 已核对差异：
  - `ui.Theme` 字段、`Context.Theme/SetTheme`、`ui.Preferences`、`mygo.Theme.SetSource/Source/IsDark/OnUpdated` 的语义未变。
  - `mygo.ThemePreferences`：`Accent` 从 `ui.Color` 改为 `color.NRGBA`，新增 `ReduceTransparency`，删除 `AccentColor/ReduceMotion/HighContrast/TextScale` 便利方法（[最新定义](https://github.com/egoist/mygo/blob/163d50e2acc1b5361d29bc6d26a8a0e11ebdb847/modules.go#L320-L350)）；设计使用 view 内的 `c.Preferences()`（`ui.Preferences` 未变）避免绑定这些便利方法。
  - `ui/markdown` 的 `View/Stream/Theme` 未变，链接颜色应用有修正；本设计不需要新 API。
  - 动画 `Animate/AnimateColor/Keyframes/Transition` 仍存在；新版本增加 `Timeline`（[`ui/timeline.go`](https://github.com/egoist/mygo/blob/163d50e2acc1b5361d29bc6d26a8a0e11ebdb847/ui/timeline.go#L27)），A 不使用。
  - 窗口 `SetBackgroundColor`、`TitleBarStyle/Transparent/Vibrancy/BackgroundColor` 仍存在。
  - 其他为内部布局/选择/性能变化，不构成本设计的主题契约。
- 每个依赖版本按“已验证快照”记录；后续独立依赖更新任务负责重新验收，不在本设计 PR 中升级。

## 头像（Saki mascot）

- 当前默认（candidate default，proposed）：内置 imagegen 生成的原图（1254×1254 RGBA），只做 256px 等比缩放，未做二次创作。Prompt：original minimal cat head, pale lavender, Rose Pine rose tuft/ears, dark plum outline, transparent background, readable at small size。
- 资产：[previews/assets/saki-avatar-default.png](previews/assets/saki-avatar-default.png)，256×256 RGBA PNG。同一份优化字节以单个 data URI 内嵌在 fragment 中，供离线 sandbox 使用；不写入 widget state，也不引入 picker、上传或 contact 级头像抽象。
- 当前原生（#90）：rail 32px、header 28px；主聊天不再逐条重复头像，历史引用/demo 仍可在需要时显示 28px；同一默认 mascot，圆角 tile 使用 overlay 背景。旧浏览器原型曾用 rail/消息 28px、header 36px，已属历史，不作为当前尺寸契约。
- 头像是装饰（`alt=""`），名字始终保持可见；选中态属于行与名字，不改变 mascot 颜色。改名/切换主题不会重置历史；绑定后 profile 锁定，只有显示名可改。
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
- 结果：57/57 项交互检查通过，0 个页面脚本错误；1024／736／375／320 均无横向溢出；历史浏览器预览使用旧 Main／Dawn 表，`light-dark()` 角色可解析；这不是 #92 的 Moon/Dawn 设计验收。
- 覆盖：默认会话与 Activity、逐 contact 草稿、切换期间回复归属与 unread、Add／Settings、附件 chip、Sources 揭示与高亮、History 范围与无结果、Ren 的 Working→Needs attention→Try again→Ready。最后两处文案调整（m2／m4 的引用配对与 composer 工作态文案）另做了 16/16 的针对性回归检查（copy-check.log）。
- 头像更新的针对性验证：27/27 通过——内嵌字节与 workspace 资产逐字节一致（sha256）、256px RGBA、各位置 `naturalWidth` 256、新建／重命名／回复／Working 均使用默认头像、选中不重着色、1024／320 light／dark 无横向溢出、0 运行时错误。
- 限制：所有 Working／回复／历史都是本地模拟，不代表后端并行或真实模型调用；sandbox 预览禁止原生表单提交，因此发送／创建／保存走显式本地处理；Lucide 图标由宿主沙箱提供，脱离宿主的独立预览会显示空占位；host 模型配置、后台并行与 contact 级持久化仍 deferred。

## 当前不做

多 agent 协作、shared memory、同宿主后台并行执行、原始推理链展示、contact 级模型/provider/key 设置（deferred）、附件、头像 picker／上传／contact 级头像设置（same-family 集合以后再评估）。Stop／取消已实现，只按 `(session,task)` 生效。主题编辑器、主题商城/下载/同步、主题热重载、自绘 titlebar 不在当前承诺；#92 只交付设计，A/B 功能另立任务。

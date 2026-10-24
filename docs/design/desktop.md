# Desktop contacts：首版运行时接入

更新日期：2026-10-24。状态：**实现事实基线为 dev@47d01ee（#63、#90）；主题与新工作台为待实施设计**。本次仅更新产品定义引用，不新增原生验收结论。原生 MyGo 应用已用 `Saki --stdio` 消费真实 runtime，并已在真实 macOS 窗口完成发送/切换/设置/退出/重启实测；contact store、任务事件与解析后的历史由 GUI 自身维护，GUI 不读取 Rust journal。最终合并以 dev 为准。

现行产品定义以 [Nagi 产品定义与需求](../requirements/desktop.md) 为准，视觉与交互以 [Desktop workbench](desktop-workbench.md) 为准；#130 实施阶段性的原生初版，#96 实施外观。旧浏览器预览与被替代的布局/配色要求已移除，历史可查 Git。本文保留原生实现事实和主题接线契约；单会话映射、单执行槽及代码中的 Main/Dawn 派生色均为基线事实，不是最终产品要求。

## 已确认需求与交付边界

本节记录 #63 阶段行为。Nagi 的联系人可以关联持续对话与多项工作的执行会话；以下 contact → 单 `session_id` 是旧版存储映射，不对最终产品模型作限制。本次修订不改字段、已有数据或迁移方式。

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

## 已知限制（dev@47d01ee 的 GUI 基线）

- 单 active task；并发/后台执行由 #61 承载，不承诺 UI 的并行观感。
- `search_history`／`read_history` 未实现；History 只对已加载 transcript 做本地过滤，不是模型检索工具。
- profile 热更新不支持；绑定后只有名字可改，purpose/instructions/workspace 不生效于既有 session。
- 无自动重连；断连后重启应用重新加载 history。未知接纳不自动重发。
- 无附件、托盘/菜单、contact 级模型配置、头像 picker；原生窗口的发送/切换/设置/Stop/History/退出/重启已实测，但真实系统 IME 组合、VoiceOver、light 系统主题、复杂滚动与更多 provider 组合仍需后续验收。
- GUI 不解析 Rust JSONL；旧 journal 的零时间戳显示为空，不伪造日期，缺失 message_id 保持缺失。
- Appearance 模式/持久化、系统外观切换和自定义主题加载尚未实现；#96 按当前纸白/夜林设计实施，#97 是后续自定义。旧 Main/Dawn 仅为代码事实。

## 产品模型

联系人、事项、对话会话与执行会话的关系，以及自主工作与 GUI/TUI 协作目标，统一在 [Nagi 产品定义与需求](../requirements/desktop.md) 维护。本文件只记录实现事实和接线；[Desktop workbench](desktop-workbench.md) 记录对应界面及阶段原型，不再分别定义产品。

## 与完整产品目标的差距（dev@47d01ee 的 GUI 基线）

| 能力 | 当前实现 | 说明 |
| --- | --- | --- |
| Contact 持久边界 | GUI contact store + `session_id` 映射；历史由 `session.history` 提供 | GUI 不解析 JSONL；重启恢复同一 ID |
| 查看 B 与 A 执行 | 单执行槽；A 运行时切到 B 查看不会 cancel A，B 的发送返回 busy | 同宿主 A/B 同时执行仍由 #61 承载，不是首轮承诺 |
| 历史检索工具 | 未实现 `search_history`／`read_history` | History 只对已加载 transcript 做本地过滤 |
| 附件 | 未接入 GUI；runtime 现有图片输入能力未暴露 | 不保存文件内容 |
| 模型配置 | host 环境变量／启动配置；contact 级配置 deferred | GUI 不改写 API/key |

上述是该 GUI 基线的交付边界，不表示最新 Runtime 仍缺少同样能力；当前服务实现见 [Runtime API](runtime-api.md)。不得把单执行槽或本地过滤写成 GUI 已完成并发执行／模型检索，也不能把旧限制继续当作产品目标。

## 主题系统

状态：**当前设计，待 #96 实施**。用户于 2026-10-23 明确要求旧设计及时移除，当前原生视觉基准为 [纸与苔 / Paper & Moss](desktop-workbench.md#第-06-版纸与苔--paper--moss)。内置浅色 ID `paper`（纸白）、深色 ID `night`（夜林）。旧 Moon/Dawn 目标和原始 palette 表已移除，不同时作为实施要求。更多方案与用户自定义保留在 #97。

### 语义映射

完整颜色数值、层级、文字对比度检查范围与视觉状态只在 [工作台视觉规范](desktop-workbench.md#第-06-版纸与苔--paper--moss) 和其 HTML 原型维护。以下规定原生消费方式，不再复制另一份 hex 表。

| 原生角色 | 原型语义变量 | 用途 |
| --- | --- | --- |
| Background / Surface / SurfaceHover | `bg` / `surface` / `hover` | 页面、容器、悬停 |
| SurfacePressed / Border | `hover` / `border` | 按压底色与分隔；按压仍需可辨反馈，边框不单独承担控件识别 |
| Text / TextMuted | `text` / `muted` | 正文和辅助文字 |
| Accent / AccentHover / AccentText | `accent` / `accent-hover` / `accent-text` | 主要动作；AccentPressed 可复用 AccentHover，配合现有按压反馈 |
| Danger / Warning / Success / Focus | `danger` / `warning` / `success` / `focus` | 错误、注意、状态与键盘焦点 |
| Selection / Scrollbar | `hover` / `muted` | 文本选择背景与滚动条，不能冒充用户气泡背景 |
| Link / Progress / QuoteHighlight | `success` / `success` / `hover` | 链接、执行提示与历史定位；保持各自语义 |
| BubbleUser / Rail | `user-bubble` / `rail` | 用户气泡与联系人栏 |
| WorkSurface / WorkText / WorkMuted / WorkAttention | `hero` / `hero-text` / `hero-muted` / `hero-alert` | 当前工作卡的底色、正文、说明与异常 |
| AttentionText / AttentionSurface | `clay` / `clay-soft` | 需要用户处理的内容 |

框架已有角色使用 `ui.Theme`，应用专属角色放在同一个小型 resolved 值中，按实际组件需要提供；不往第三方结构强塞字段，不按品牌名在组件内分支，不预建组件主题框架。普通文件/工具内容复用 surface 与正文角色，装饰 canvas/书册不要求提前映射为原生公共 API。

### Appearance、模式与解析

- **入口**：联系人侧栏底部的应用级 Appearance；与联系人设置独立，后者仅编辑联系人 profile。
- **模式**：`system`（默认）/`light`/`dark`；跟随系统或用户手动选择，不做定时切换。light/dark 分别默认 `paper`/`night`；当前每类仅一个内置方案，展示有效主题名，不设置空的方案下拉。
- **唯一解析路径**：应用保存 `Appearance{mode, lightID, darkID}`；view 入口解析一次并向组件传递颜色。组件不自行读系统明暗、判断主题 ID 或写盘。
- **异常偏好**：非法 mode 回退 system；缺失/未知/类型不符 ID 回退对应内置方案并保留原选择，显示非阻塞说明。外观错误不能令联系人存储整体不可读。
- **切换保留状态**：只改颜色；任务、会话、草稿、联系人、焦点、滚动和字体/间距不因换色重置。失败说明不能伪称保存成功。

### 持久化

只扩展现有 contact store 顶层可选 `appearance`，不放到 contact/session/profile，不另建配置文件或存储入口：

```json
{"appearance":{"mode":"system","light":"paper","dark":"night"}}
```

保持 store `Version=1`；旧数据无此字段时采用上述默认值，联系人保存必须保留外观字段。切换先在内存生效，再保存；失败提示“主题已生效但未保存”，不回滚正在执行的任务。当前基线未实现 theme ID 保存；若开发发现其他在途分支或实际用户已有保存的 ID，先核对事实并保留读取兼容，不借配色变更删除用户偏好。

### 系统外观与窗口

以下接线依据已核对的 MyGo `fea932a7a2a9`；依赖升级由 #98 重新验证，不把快照当永久约束。

- `Context.SetTheme` 是帧内设置。先读取默认 `c.Theme().Dark`，解析本帧主题，再 SetTheme；不只在启动时设置，也不每帧注册监听。见 [Context](https://github.com/egoist/mygo/blob/fea932a7a2a9/ui/context.go#L305-L315)。
- `Theme.SetSource` 仅启动和模式改变时调用；system 恢复 ThemeSystem，手动模式选择 ThemeLight/ThemeDark。框架负责系统变化重绘，不另造 effective appearance 缓存/监听。见 [Theme](https://github.com/egoist/mygo/blob/fea932a7a2a9/modules.go#L299-L453)。
- 窗口 backing color 使用 `light-dark(paper.Background, night.Background)`，按后端 effective appearance 选择。启动恢复偏好后设置 pair 与 Source；仅 scheme ID/解析颜色改变时更新 pair。native chrome 的品牌色外观由 #101 独立处理，不在主题任务重做标题栏。见 [window](https://github.com/egoist/mygo/blob/fea932a7a2a9/window.go#L393-L414)。
- 减少动态效果、系统字体缩放和高对比度按实际原生支持分别验收，不由 HTML 或主题 resolver 自动推定已支持。当前没有新增透明材质或常驻动画；不扩大为系统偏好框架。

### 更多方案与自定义（#97，后续）

保留 `UserConfigDir()/Saki/themes/*.json` 的本地加载方向：稳定且唯一的小写 kebab-case ID、显示名、light/dark kind、schema version 和语义颜色。复用 #96 的唯一解析/校验边界，不做品牌继承、脚本、表达式、商城、同步或热重载。完整字段集合应以 #96 实际消费的语义角色定稿，不继续执行已被新工作卡设计替代的旧字段清单。

内置 ID 不能被覆盖；重复 ID/无效文件给出文件与字段错误，仅影响对应方案。当前选中的文件丢失或无效时，回退同 kind 的内置方案，保留用户所选 ID 便于恢复，不改写文件。自定义对比度可提示；内置方案必须通过实际使用组合的可读性检查。

### 验收

- #96 交付内置方案、system/light/dark、保存和恢复；#97 后续交付文件加载/发现/选择。布局和工作区本身由 #130 实施，不互相复制。
- 两色下正常/工作/失败/禁用/焦点状态，默认/960×640/1040×720/宽窗口，包含独立 Appearance 与联系人设置；浏览器、headless、真实原生窗口分别标注。
- 切换与重启后方案正确，任务、草稿、焦点、选择与滚动保留；旧 store 和保存失败不损坏联系人。
- 正文/小字按实际前后景组合检查 4.5:1；焦点可见、状态不只靠颜色。原型已有 18 组计算不代表全原生控件通过。
- 设计与文档更新不等于产品已实现，Go 测试、真实系统切换与候选 SHA 证据分别记录。

### 依赖快照与上游差异（只读核对，不作为冻结政策）

- 当前 `desktop/go.mod:5`（[源码](https://github.com/HeimaoLST/Saki/blob/3c449404a3648ae0a7932bab749adf3a884ef062/desktop/go.mod#L5)）依赖 `github.com/egoist/mygo v0.4.9-0.20261014025635-fea932a7a2a9`（10/14，[Theme 源码](https://github.com/egoist/mygo/blob/fea932a7a2a9/ui/theme.go)）。这是当前 app 的已验证快照，不是长期冻结版本。
- 主责 sora 实际执行 `go list -m` 查询到 `@latest` 为 v0.5.5（10/16）；本次用 `go mod download -json` 只读核对官方 main commit `163d50e2acc1b5361d29bc6d26a8a0e11ebdb847`（10/20），解析为 `v0.5.6-0.20261020115920-163d50e2acc1`。仅缓存，未改 `go.mod/sum`。
- 已核对差异：
  - `ui.Theme` 字段、`Context.Theme/SetTheme`、`ui.Preferences`、`mygo.Theme.SetSource/Source/IsDark/OnUpdated` 的语义未变。
  - `mygo.ThemePreferences`：`Accent` 从 root `mygo.Color`（[color.go:12-16](https://github.com/egoist/mygo/blob/fea932a7a2a9/color.go#L12-L16)）改为 `color.NRGBA`，新增 `ReduceTransparency`，删除 `AccentColor/ReduceMotion/HighContrast/TextScale` 便利方法（[最新定义](https://github.com/egoist/mygo/blob/163d50e2acc1b5361d29bc6d26a8a0e11ebdb847/modules.go#L320-L350)）；设计使用 view 内的 `c.Preferences()`（`ui.Preferences` 未变）避免绑定这些便利方法。
  - `ui/markdown` 的 `View/Stream/Theme` 未变，链接颜色应用有修正；本设计不需要新 API。这些是 MyGo 框架能力，Saki 尚未接入 Markdown/RichText/Timeline，不据此宣称产品支持。
  - 动画 `Animate/AnimateColor/Keyframes/Transition` 仍存在；新版本增加 `Timeline`（[`ui/timeline.go`](https://github.com/egoist/mygo/blob/163d50e2acc1b5361d29bc6d26a8a0e11ebdb847/ui/timeline.go#L27)），A 不使用。
  - 窗口 `SetBackgroundColor`、`TitleBarStyle/Transparent/Vibrancy/BackgroundColor` 仍存在。
  - 其他为内部布局/选择/性能变化，不构成本设计的主题契约。
- 每个依赖版本按“已验证快照”记录；后续独立依赖更新任务负责重新验收，不在本设计 PR 中升级。

## 头像（Saki mascot）

沿用现有 [mascot 资产](previews/assets/saki-avatar-default.png)，不新增上传、picker 或身份/成长机制。基线 #90 原生 rail 32px、header 28px 是旧代码事实；目标尺寸与圆形气泡头像只按 [当前视觉规则](desktop-workbench.md#美学规则) 实施。名称与选中态保持可辨，不依靠 mascot 颜色表达状态。可选头像集合留后续，不绑定已移除的旧 palette。

## 关键流程（当前原生应用）

1. **选择 contact**：切换 name、profile、messages 与 draft；草稿按 contact 保存。A 在 runtime 执行时切到 B 查看不会 cancel A；首轮 B 的发送返回 busy。状态词只描述工作：Ready／Working／Needs attention，不假装在线。
2. **Add contact**：一次点击直接创建、选中 Ready 默认联系人并进入空会话；无需填写 Name／Purpose／Instructions／Workspace。composer 只取得一次焦点，连续创建也可直接输入。个性化通过可选 Settings 编辑；首次发送才创建 runtime session。
3. **Contact settings**：显示名始终可编辑；purpose／instructions／workspace 只在首次 create 时使用，绑定后只读。不会提示“changes apply to future messages”，也不会重建 session 或丢历史/草稿。
4. **Composer**：textarea、Send 与 Stop；Enter 发送、Shift+Enter 换行、IME 组合期不发送。发送进入真实 runtime：接纳后显示等待开始，`task_started` 后才 Working；delta 流式、final 覆盖同一项；工具与最终文字按 task 归属。当前没有附件入口。
5. **History action**：对当前 contact 已加载的 transcript 做本地过滤；无结果有明确说明。它不等于 runtime 的 `search_history`，也不会出现其他 contact 的消息或暗示 shared memory。

## 状态与反馈（dev@47d01ee 的实现事实）

| 状态 | 表现与下一步 |
| --- | --- |
| Empty conversation | 可直接写第一条消息，或按需编辑 Settings；无 profile 不阻止开始 |
| Working | 会话尾部有进行中行；Send 暂时不可用，Stop 可用；切走保留状态 |
| Needs attention | gold 状态 + 已保存的失败/中断原因；不在未知接纳时自动重发 |
| No history results | 说明该 query 在此 contact 已加载历史中无匹配，建议更短的短语 |
| Validation | Settings 保存时名称不得为空；绑定后 profile 字段只读 |

## 该阶段未覆盖的能力

多 agent 协作、shared memory、同宿主后台并行执行、原始推理链展示、contact 级模型/provider/key 设置（deferred）、附件、头像 picker／上传／contact 级头像设置（same-family 集合以后再评估）。Stop／取消已实现，只按 `(session,task)` 生效。主题编辑器、主题商城/下载/同步、主题热重载不在当前范围；主题切换与自定义分别由 #96/#97 交付，标题栏按 #101 在途任务推进。当前基线限制不覆盖后续任务已明确的实施范围。

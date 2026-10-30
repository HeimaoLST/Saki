# Desktop contacts：首版运行时接入

更新日期：2026-10-30。当前设置页面见文末 **#199 独立设置页面**。本文保留各阶段的实现与验证记录；连接配置见末尾 **#116 首用配置**，会话模型选择见 **#115 会话模型选择**，阅读与独立工作会话分别见 #113/#118，资料快照与递交见 **#117 资料快照、阅读与递交**，联系人编辑、归档及旧对话访问见 **#152 联系人管理与已保存对话**。早期原生窗口证据只适用于其注明的包，不代表新候选已完成原生验收。原生 MyGo 应用通过 `Saki --stdio` 消费 Runtime；GUI 维护联系人、事项与历史展示，执行历史由 Runtime 提供，GUI 不解析 Rust journal。最终集成状态以对应 PR 与 dev 为准。

[previews/desktop-contacts.html](previews/desktop-contacts.html) 仍是早期浏览器示意，不是当前原生应用的验收对象；仓库内 `desktop/` 是当前可运行的原生应用。当前主题事实见 `desktop/theme.go` 的 `resolveWorkbenchTheme`（Paper/Moss；light/dark 语义角色）；#92 的 Moon/Dawn 和 #90 的 11px/12px 数值只保留为历史设计/实现记录，不再作为当前规范。此前将这些能力或颜色列为后续独立任务的说明已失效，不再作为当前实现或未来承诺；具体状态以对应任务段、PR 和 `dev` 为准。

<a id="gui-design-constraints"></a>

## GUI 设计约束（当前规范）

更新日期：2026-10-27。本节是 GUI 可读性、操作可发现性与布局验收的唯一规范入口；`AGENTS.md` §10 和 PR 模板只链接本节，不重复具体数值。本节替代早期 #90 的 11px 状态/12px 按钮规则；旧记录只作为历史证据保留。

- **字号基线**：正文、要求和结果摘要使用 14 逻辑 px；普通动作 13；辅助状态、来源和日期至少 12。已有更大的正文不缩小；重要标题用字号、字重和留白分层。禁止为了装下内容退回 9–11 px；字号只是基线，必须结合实际渲染验证可读性。
- **主动作与次动作**：真正的下一步使用 `ui.PrimaryButton`；普通次操作复用 `desktop/theme.go` 的 `secondaryButton`，静止态要有可见底面/边界、至少 32 逻辑 px 的有效高度和内边距；状态标签不能伪装成按钮。不要把通用动作做成无底无框灰字，也不要靠 hover 才揭示操作；保留 hover/pressed/focus/disabled 语义。既有 Markdown Copy 的受控实现继续保留，统一相同外观即可。
- **主题与样式复用**：颜色走现有 theme 语义角色，避免局部散写颜色或重复同值字体/padding 覆盖。优先使用现有控件；不为本规范新增多层 UI 框架。
- **主次区域与布局偏好**：中间对话是主要交互区域；三栏时中心应保持最宽。右侧工作/详情默认关闭，开合由用户选择并跨重启恢复；切换联系人、收消息或 task start/finish 不自动改变它。两侧可真实调宽；空间不足时右侧改用覆盖抽屉，临时窄布局不得覆盖保存的宽屏偏好。具体宽度、中心下限和持久化值见 [可调三栏具体值](#adjustable-layout-values)。
- **布局与响应宽度**：用换行、折叠、重排和正常纵向滚动解决空间；不靠缩小文字掩盖溢出，也不用横向滚动访问普通操作。默认 1040×720、紧凑 960×640、最小 720×640 和宽 1380×900；按改动涉及的状态检查，明暗两色分别检查。抽屉 320/360 宽度可独立验证边界，但不能把 320 宽测试称作原生支持窗口。
- **可交互性验收**：相关按钮要默认可辨、完整可见、点击有效；键盘焦点返回正确且不被裁切。测试必须定位目标本身；必要时检查边界、滚动后点击和业务状态，不用 `HasText`、重复文案或字号字面量充当交互验收。
- **交付证据**：每次 GUI 变更先查本节和现有样式。交付按受影响流程提供同 SHA 截图与实际交互记录，分别标明 headless/native/未验；不借 CI 绿或旧包绿称产品通过，也不要求无关的全量人工 QA。偏离规范需在当前 PR 说明具体原因、替代方案及证据，由既有评审流程处理，不新增人类审批队列。

## 已确认需求与交付边界

需求／产品决定（用户确认，#44）：首次使用无需先定义 agent。添加联系人即得到可输入的默认通用助手；name／purpose／instructions／workspace 为可选设置。联系人 profile 与宿主连接分开；#116 在 GUI 提供宿主模型连接的首用配置。

首轮实现事实（#63，后续新增行为以文末对应任务为准）：
- 正常启动从 GUI contact store 恢复；默认创建一个 `Saki` 联系人，零配置即可看到界面。测试/截图 fixture 仍保留在 `seededApp()`，正常真实启动不会混入种子历史。
- Add contact 一次点击创建并选中 `New contact N`、`Role=Assistant`、Ready 的空联系人，composer 取得一次焦点；不创建 runtime session，直到第一次发送。
- contact → runtime `session_id` 的映射、显示名、profile、草稿和选中项由 GUI 持久化；消息正文不复制，历史只来自 `session.history` 的已解析 journal 记录。
- Settings：#63 首轮在已有 `SessionID` 或首次 create 在途时锁定 `purpose/instructions/workspace`。#152 已替换此限制：允许保存未来默认值，已有执行上下文仍保留原快照；显式新对话保留旧历史，详见文末。
- 首轮单执行槽：`busy` 明确拒绝且草稿保留；不做队列或并发。查看另一个联系人不会 cancel/release 当前任务，事件按 `(session,task)` 路由。
- 首次 create 后，GUI 先回到 UI 线程设置 `session_id` 并确认 `store.save` 成功，才调用 `task.send`；保存失败不发送、保留草稿并显示错误。已有 session 的发送同样要求 contact/draft 持久化成功，不把未保存的映射用于执行。
- 接纳 response 与 `task_started` 分开：前者显示“已接受，等待开始”，只有 `task_started` 才进入 Working；delta 追加、final 覆盖同一 `(session,task,item_id)`，工具按 `call_id` 归属，工具业务错误不自动判整轮失败。
- `task_finished` 是唯一终态；`failed/interrupted` 的已保存原因在历史刷新后仍可见。取消按 `(session,task)`，迟到 cancel 不会改写已完成或别的 task。
- 断连/协议失败标记为未知；`sendUnknown` 只有在同一连接、`session.load/status` 确认 idle 且 history 取回后才恢复可发送状态。连接已退出时不提供“Reload”假恢复，明确提示重启应用；不做透明重连框架。
- 退出（关窗与 Cmd+Q）先阻止默认关闭，持久化草稿，异步 `runtime.shutdown` 等待已接纳任务收尾和进程 Wait，再退出；不阻塞 UI 线程、不静默 Kill。

## 布局打磨（#90，历史证据；已被顶部当前规范替代）

一次局部布局调整，不改发送/取消/runtime/store 语义：
- 联系人侧栏保持 220px：品牌与 Contacts/Add contact 收敛为轻量入口；行内统一 32px 头像、名称和单行摘要；选中态只使用 surface hover，无边框；Working/Needs attention 合并在摘要行显示，未读仍归原联系人。
- 顶部使用 28px 头像、15px 名称；状态/角色和 History/Settings/Add contact/Details 使用当轮的较小字号与可用点击 padding，靠背景/对比分层。该轮 11px 状态/至少 12px 按钮的数值规则已被顶部“GUI 设计约束（当前规范）”替代，仅保留当轮证据；不引入图标库。
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
- `desktop/contact_profile.go`、`contact_management.go`、`contact_history.go`：联系人表单与唯一保存路径、归档/恢复、联系人筛选、指定会话范围的历史搜索及原文定位。
- `desktop/connection_config.go`、`connection_app.go`、`connection_runtime.go`、`connection_settings.go`：GUI 配置来源、保存/应用生命周期、既有 stdio 接口适配与表单；不实现模型协议客户端。
- `desktop/model.go`／`view.go`／`main.go`：状态模型、原生布局与入口；`seededApp` 仅测试/截图。
- Store：`SAKI_GUI_STORE`，否则 `<UserConfigDir>/Saki/contacts.json`（macOS 通常 `~/Library/Application Support/Saki/contacts.json`）。
- Session 目录：`SAKI_SESSION_DIR`，否则 `<store 目录>/sessions`；作为子进程环境注入。
- Runtime 可执行：`SAKI_RUNTIME_BIN`；否则查找 GUI 可执行文件旁的 `Saki-runtime`／`saki-runtime`、`../Resources/Saki-runtime`／`../Resources/Saki`；最后查 `PATH` 的 `Saki`。
- 工作目录：有 GUI 配置时使用已应用的默认目录；旧联系人仍保留自身 Workspace。外部配置模式沿用 `SAKI_GUI_WORKDIR`，否则当前目录（Finder 的 `/` 除外，退回 home）。
- 模型连接：`<store 目录>/connection.json` 是唯一 GUI 可编辑来源，保存协议、endpoint、key、默认模型、显式 context/output 容量、默认目录与活动任务上限。无该文件时兼容既有宿主环境/`.env`；损坏或失效的 GUI 配置必须在界面修复，不能静默回退。详见末尾 #116。

可复制的本机构建与启动（不含 key；key 由宿主 `.env`/环境提供）：

```sh
cd desktop
python3 ../scripts/gui-go.py build -o /private/tmp/saki-gui/o .
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
Finder 启动不继承 shell 环境。空配置时通过 Connection settings 保存并 Apply；已有 GUI 配置从持久文件恢复。上述命令保留为开发者外部环境模式入口。不要把 key 放进 Info.plist、候选包或仓库。

## 原生工作台（#130，历史实现事实；固定布局比例已由 #184 替代）

更新于 2026-10-23。当前实现/测试 HEAD `c1b804e`（栈上合并 112 `31d3cff`；产品 formatter merge `4ec3ad5`）；本条只保留当轮 GUI 实现事实。当前布局规范和宽度偏好以顶部 [GUI 设计约束（当前规范）](#gui-design-constraints) 为准；当轮的固定 1120/960 列宽已由 [#184 可调三栏具体值](#adjustable-layout-values) 替代。

- 三栏布局（历史，已由 #184 可调三栏替代）：宽度 ≥1120 时联系人栏 200px、工作区 360px；960–1119 使用 160px／320px 紧凑列；低于 960 时工作区通过带遮罩的模态覆盖打开，Close 始终可见。原生窗口最低宽度调整为 720px（高度仍 640px），使 900px 的覆盖分支可由实际 `ContentSize` 到达；960/1040/宽屏同样由实际内容宽度分类，headless 显式设置对应宽度。联系人栏可收起并由页头 Contacts 重开；工作区可收起并由 Work 重开；收起、覆盖、关闭和 Escape 后恢复入口焦点，侧栏/工作区状态不停止任务。
- 颜色：按 PM 固定 `2182c1a` 的 root/night 数值，一次解析为小型 `workbenchTheme`（既有 `ui.Theme` + `Rail`/`BubbleUser`/`WorkSurface`/`WorkText`/`WorkMuted`/`WorkAttention`），组件不写 hex、不按品牌分支。用户气泡使用 `BubbleUser`，联系人栏使用 `Rail`，当前工作卡使用深色 `WorkSurface` hero（#96 已替换为中性工作面；Appearance 模式与持久化见 #96，联系人设置不承载应用外观）。
- 消息与工作分开：用户/助手真实文本留在左右气泡，按 task/item 稳定 key 聚合增量、最终和历史，不因工具或完成追加占位回复。工具活动进入按 task/call 归属的 Work 投影，显示状态、摘要、详情和原文 Copy；工具失败只影响工具/工作呈现，不直接把任务判为失败。没有 Task ID 的旧工具记录保留为 “Earlier activity”，不伪造 task 归属。当前工作卡是 `WorkSurface` hero，标题只保留一次 Current/Recent work，任务目的、状态与最新动作分层；工具正文不套用 hero。
- 执行事实：`task_started`、`tool_started/finished`、`task_finished` 与 cancel 回复驱动 accepted/running/stopping/completed/failed/interrupted/unknown；Stop 针对当前 `ActiveTaskID`，取消结果不直接生成“已停止”，等待 `task_finished`；由 Stop 请求后的 interrupted 显示 `Stopped · interrupted`，保留中断原因。同进程的 `StopRequestedTasks` 本地 map 让自动 history reload 保留同一 task 的停因；该事实不写入 store，重启后没有停因契约时仍诚实显示 Interrupted。未请求停止的历史也显示 Interrupted。旧任务迟到 start/terminal 不会复活或污染新任务。单执行槽下其他联系人明确显示 busy/disabled，草稿不丢、不停止当前任务。
- 聊天未读只由新 Agent 文本消息增加；工具、普通完成、reload 和 restart 不重复计数；失败/未知仍通过 contact attention 与工作详情持续可见。未加载 session 的聊天/工作/History 显示未加载或不可用，不声称“没有历史”。Store 失败仍走既有本地 store fallback。
- History：使用带遮罩的临时 modal 覆盖聊天/工作三栏，不把 `screen` 替换成主视图；Close/Escape/Backdrop 关闭后回到原三栏并恢复 History 入口焦点。搜索合并已落盘 `History` 与当前 `ct.Messages` 的真实文本，运行中尚未 reload 的消息仍可读；query 为空且无记录时显示 “No saved messages”，不伪报 “Try a shorter phrase”；session 未加载时显示未加载。View in conversation 关闭 modal 并定位原消息。
- 认证错误：130 合并 112 的单一 `taskFailureDetail`/`safeDiagnostic`，live、history、Work.Detail 与 composer StatusDetail 共用；稳定 `HTTP 401/403` 不展示 provider 正文，非认证错误保留有界诊断并遮蔽显式秘密。已有 112 真实 GUI auth 测试扩展 Work.Detail，不另造第二套测试；原始 032 401 FAIL 保留，v2 修复证据独立。
- `TestGUIAppRuntimeFlow` 保留 112 的 `session.load` state/metadata 与 `EvalSymlinks` cwd 断言，并同时等待/校验 130 的 Works；不因工作投影替换恢复回归。
- 恢复：`applyHistory` 从现有 Runtime 记录重建最近 task/call，保留文本、草稿和可查看工具输出；缺 task ID 的旧记录只保留可读活动，不新造关联。关闭/切换/History/Settings 往返不停止执行。
- 测试：`go test ./...`、vet/build/gofmt/diffcheck 通过；最终合并后本地 `-race ./...` PASS 12.460s（日志 `/private/tmp/saki-gui-130/race-final-v3.log`），主责在 `c1b804e` 以最终 real helper 复跑 `-race` PASS **11.686s**（日志 `/private/tmp/saki-gui-130/main-final-c1b804e-race.log`）；5 项既有 + 真实 auth/reload/restart + 真实 cancel/reload/restart 回归均通过。同源 Tester 图在 `/private/tmp/saki-gui-130/previews-c1b804e/`：32 张，覆盖 1040×720、960×640、1380×900、900×640 覆盖、明暗、hero/progress/empty/history；不是 native 窗口、IME 或显式 copy 验收。旧 d4/v2/9e53 图保留，不覆盖。
- 旧 d4 候选包保留：`/private/tmp/saki-gui-130-app/Saki.app`，bundle `dev.saki.desktop.workbench130`，source `d4b4499`，GUI `90f85e6924e79235d15b70c631b2da8720fb3269d186163c0a6d6ffdcdb981fa`，helper `aea5ff1142c989bf793c13ab5468e6f0c62a67d327a20868e3c4188935b2f8d8`。不覆盖。
- v2 候选包保留：`/private/tmp/saki-gui-130-v2-app/Saki.app`，bundle `dev.saki.desktop.workbench130v2`，source `9e53bb9`，GUI `76aff30dc64b3e7f18bb714f01b28d5562c3eb98c81c85a1a5172d892ad8fd31`；未覆盖。
- v3 候选包：`/private/tmp/saki-gui-130-v3-app/Saki.app`，bundle `dev.saki.desktop.workbench130v3`，source `c1b804e`，GUI sha256 `78fee44118d22ba94d77adbeb63e819ae76129d8edfaeb6862445cca7020dd15`；复用 47d01ee helper `aea5ff1142c989bf793c13ab5468e6f0c62a67d327a20868e3c4188935b2f8d8`；继续使用 `/private/tmp/saki-gui-130-native` 数据，未重置；720×640、ad-hoc deep/strict 通过，不是发行包。v3 已冻结，主责 CUA 打开被 `cgWindowNotFound` 阻断，不能据此写 native PASS；不再重编。
- 受控 fixture：`/private/tmp/saki-gui-130/provider_fixture.py`，端口 8140；`tool-progress` 用 Event gate 停住第二/第三请求，`quiet` 无文本 completed，`fail` 合成失败；`GET /control/count`、`POST /control/release`、`POST /control/mode`；日志不记 Authorization，marker 在隔离 workdir。真实 Rust tool-progress 与 quiet smoke 已过；包 GUI 原生 CUA 由主责执行。
- 原生 round1（d4）：系统 Night、1040×720；Zoom 到 2048×1122，宽侧栏 200/工作 360；内建屏 Move&Resize Left 到 757×870，真实 overlay 的 Close/Tab/Esc 返回 Work focus。真实 Runtime 47 + tool-progress 验证 request1 read/request2 hold 无 Agent 气泡、tool 结果可见；Work/History 往返不停；B 草稿保留/禁发；release2 progress+read、A 未读 1、回 A 仍 running+2 tools；Stop 后真实 interrupted、无新气泡、A 草稿保留、迟到 response3 不改终态；quiet 无文本 completed、重启恢复工具/草稿；证据 `01..12` 在 `/private/tmp/saki-gui-130-native/evidence/`（旧 `.png` 文件字节实为 JPEG，按原文件名保留）。
- 尺寸证据分层：`c1b804e` 最终同源 Tester 明确验证 960×640、1040×720 与 1380×900（light/dark，另有 900 overlay）；native 实际只观察到 d4 的 1040×720、2048×1122 和 757×870 overlay，未把 757 等同精确 960。PM comment 6346384656 接受以最终同 SHA Tester 证明尺寸分支，native 仅记录实际逻辑尺寸；精确拖拽不是当前代码阻塞。
- v3 CUA 状态：主责对 `v3` 执行 `getApp(path)` 与 bundle 打开均返回 `cgWindowNotFound`；进程/helper 启动但在本轮不可观测，故 **不是 native PASS**，没有继续 UI 操作。v3 的 hero/history/copy、原生 clipboard、精确 960 原生窗口和 IME/VoiceOver 仍未验。
- 未验：真实 MyGo 窗口的精确 960/720、原生剪贴板、用户动效/IME、PM 视觉复核；不以 headless 或 Tester 结果替代。当前工作区仍是默认单执行槽，不宣称并发或运行中 follow-up。
## 启动诊断与配置状态（#112，实现事实）

更新于 2026-10-23。修复后产品源码 `8a04d63`（#112 v2 auth-history 修复），Runtime helper 源码 `47d01ee`。本轮完成 G1-1..G1-4 的局部实现；原生验收只完成一部分，未声称 G1 全部通过。原 `032339a` 包与证据永久保留。不新增 Runtime 协议、凭据库或应用配置系统。

### 实现
- 启动阶段：`Runtime helper`（未找到 helper）、`Runtime start`（exec 启动失败）、`Runtime configuration`（仅已识别的 `API_KEY`/`.env` 配置错误）、`Runtime connection`（handshake 后连接丢失）、`Runtime protocol`（协议错误）。未 handshake 的未知退出归 `Runtime startup`，不误报配置；正常 drain 不作为故障。协议失败保留协议诊断，并继续执行 active 任务的未知结果清理，不清成配置诊断。
- 可操作提示：缺 helper 显示期望的 `Saki-runtime` 位置和 `SAKI_RUNTIME_BIN`；启动失败显示实际 helper 路径与权限检查；配置失败提示 `API_KEY` 为必需，`SAKI_API`/`SAKI_BASE_URL`/`SAKI_MODEL` 有 runtime 默认值，并给出实际 workdir `.env` 与重启提示。Runtime ready 仅表示连接就绪，不表示模型已验证。
- 凭据边界：只对稳定 `HTTP 401/403` 前缀（数字边界，`HTTP 4010` 不算）显示 provider 认证失败，且不展示 provider 正文；其他有界诊断集中遮蔽已知 `API_KEY`/token/Authorization/Bearer、JSON 秘密字段、`sk-` 和 URL userinfo。
- 配置界面：独立 aux `Startup & configuration`，从错误 banner 的 `Configuration details` 或 Contact Settings 的 `Startup status` 进入；Close 在顶部。原 Contact Settings 的 Name/Purpose/Instructions/Workspace/Cancel/Save 结构保留。面板显示 Runtime 阶段、helper、store、session 目录、GUI workdir、当前 session workdir、session ID/state、API/model。
- 事实来源：session state 只消费 `session.State`；working dir/model/API 只消费 `session.load`/`session.create` metadata。`task_finished` 的 completed/failed 只是任务结局，不冒充 session state；状态过期时由现有 reload 刷新，不用环境变量猜。恢复、reload、首次 create 三条路径现在都应用 state/metadata。
- 本地 store 失败：保留 `store=nil`，标 `Local contact store`，显示实际 store/session/workdir 路径和原始错误；明确未覆盖文件、未创建替代 store，store 不可用期间不承诺草稿已持久化。不新增恢复/迁移/备用 store 系统。
- 草稿边界：提交前被拒绝或本地保存失败保留原草稿；任务已接纳的输入进入消息历史，绝不自动还原为待发送；之后编辑的未发送草稿单独保留。关闭/未知结果不自动重试或重跑。
- Finder 测试隔离：测试 bundle 用 `LSEnvironment`（Apple LaunchServices，<https://developer.apple.com/documentation/bundleresources/information-property-list/lsenvironment>）注入非秘密 store/session/workdir/API 路径，固定 `PATH`；API_KEY 只放在临时 workdir `.env` 的假值中，不进入 Info.plist。这是测试打包方式，不是新增产品配置层。

### 原生证据（部分）
已用 22ba 阶段的隔离测试包和 Finder 实测：
- `01-finder-missing-config`：无 `.env` 冷启动，实际显示 `Runtime configuration`/`API_KEY is not set` 和准确 workdir `.env` 路径；查看详情不产生推理请求（count 0）。
- `02-config-details`：独立配置面板可打开，Close 可见，session/model 保持 unknown，不猜。
- `03-ready-no-inference`：复制临时假 key `.env` 后 CmdQ + Finder 重开，仅连接 ready；恢复零模型请求（count 0）。
- `04-controlled-success`：显式发送 `draft-before-configuration`，真实 Rust `47d01ee` + 本地受控 provider 返回 `Saki 112 fixture ready`，count 1。
- `05-restart-restored`：编辑 `later-unsent-draft` 后 CmdQ + Finder 重开，同 session 历史与 later 草稿恢复，count 仍为 1。此步同时发现 `restoreSession` 未消费已返回的 state/metadata；该缺口已由 `f213bdd` 修复，并有真实集成回归。**因此不把 G1-3 写成完整原生通过**。

证据目录：`/private/tmp/saki-gui-112-native/evidence/`（上述 01..05 的 `.png` 与 `.ax.txt`）。这些是 22ba 阶段证据，不是最终 `032339a` 包的复验。

原 `032339a` 包的后续原生证据（06..12）：
- `06-final-restored-metadata`：恢复 metadata/state 实际 passed。
- `07-final-finder-missing-config`：Finder 缺配置实际 passed。
- `08-final-nohelper`、`09-final-execfailure`、`10-final-corruptstore`：NoHelper/ExecFailure/CorruptStore 三条 fault 路径实际 passed；坏 store hash 保持 `9a3ed7…`。
- `11-final-auth-held-laterdraft` / `12-final-auth-failed-keeps-laterdraft`：auth hold/later-draft 现场；`12` 的 401 history 仍实际 FAIL，正文含假哨兵，**不能因 v2 修复而覆盖这条失败证据**。

v2 `8a04d63` 的原生修复证据（13..18）：
- 从 Finder 打开 v2 恢复旧 401 历史，错误提示已净化；新 `auth--hold` 显式发送 `v2-accepted-auth-check` 后编辑 `v2-later-unsent-draft`，release 401 后历史/later 草稿/净化提示正确、不自动 retry；CmdQ + Finder 重启后同 session 与提示仍正确，provider count 仍为 1。
- 切换 success fixture 启动 count 0；显式新任务 `v2-explicit-recovery-success` 得到真实 final，编辑 `v2-preserved-final-draft`，重启恢复同 session 历史/draft/idle/model/API 正确，count 仍为 1；计数路径 0→1→重启仍 1。
- 证据见 `/private/tmp/saki-gui-112-native/evidence/` 的 13..18 图片/AX 与 counter JSON；这是受控 provider 的 native GUI 验证，不是外部计费 provider、IME 或 VoiceOver 验证。

### 测试与最终包
- 主责在最终 `032339a` 执行 `SAKI_TEST_RUNTIME_BIN=<worktree>/target/debug/Saki CGO_ENABLED=0 go test -race ./... -count=1 -v`，PASS 11.578s；5 项既有真实 runtime 测试 + `TestRealRuntimeStartupConfigurationFailure` + `TestRealProviderAuthFailureClassification` 共 7 项均 PASS 非 skip；vet/build/gofmt/diffcheck 也通过。日志：`/private/tmp/saki-gui-112/logs/main-final-032339a-race.log`。
- `8a04d63` 修复 history/reload/restart 的失败正文回显：live、history、重启统一走 `taskFailureDetail`，`HTTP 401/403` 只显示认证分类，不经原样历史错误；`TestGUIAppProviderAuthHistoryRedaction` 用真实 Rust/fixture 覆盖 accepted history、later draft、no retry、请求次数、reload 与 restart。`8a04d63` 全量 `-race ./...` PASS 12.443s，日志 `/private/tmp/saki-gui-112/logs/main-8a04d63-auth-race.log`；5 项既有 + 2 项 startup/auth + 新增真实 GUI auth 共 8 项实际执行。`032339a` 仅删除无调用的 `runtimeConfigHint`，原冻结包保持不动。PR126 head `1b1210d` 的 CI run 39816472088 全绿；本 docs 覆盖更新后需重新跑最终 head CI。
- 原冻结包 `/private/tmp/saki-gui-112-app/Saki.app`，bundle `dev.saki.desktop.issue112`，源码 `032339a`，GUI `b07c8f60e1805e702a5cc616ec232d50e03a5538c13d5d740ab4333f65c38aa8`；原包、变体、数据和证据保持，不回写。
- v2 修复包 `/private/tmp/saki-gui-112-v2-app/Saki.app`，bundle `dev.saki.desktop.issue112v2`，源码 `8a04d63`，GUI sha256 `d02a82bb180201ecbbaf9e59036be9359100f514ed6141bb2467a84f3fe73470`；helper 构建源 `47d01ee`，unsigned `236e12aaa4fd6235b035fc956432de2b8988a3506531f305abc4acb6da96245d`，bundle 内 signed helper `aea5ff1142c989bf793c13ab5468e6f0c62a67d327a20868e3c4188935b2f8d8`。ad-hoc 签名通过，但不是发行包；复用原非秘密 store/session/workdir 路径。
- Headless：`/private/tmp/saki-gui-112/previews/` 9 张（config-failure 与 configuration-details；1040×720/960×640 明暗，另 1600×900），生成自 `032339a`；v2 只修 history 文案，未重出图。`ui.Tester` 生成，命令 `SAKI_PREVIEW_DIR=... CGO_ENABLED=0 go test -run '^TestPreviewStartupDiagnostics$' -count=1 .`。这些只验证布局/可读性，不是 native 窗口、浅深色系统或 IME 证据。

### 仍未验
- `032339a` 包的 Finder 原生恢复 metadata/state 已复验正确；v2 的 401+hold、later draft、reload/restart、显式恢复成功和计数 0→1→重启仍 1 已 native 通过；原 032 的 401 FAIL 仍保留为旧包证据。
- NoHelper/ExecFailure/CorruptStore 的原生 fault 路径已由 08/09/10 通过；坏 store `9a3ed742b35edc2fcf25f15822cf6412635a4995744d4766060fbbef6436fbde` 在原生检查中保持不变。
- 精确 960×640、真实系统 IME/VoiceOver、外部计费 provider 仍未验；受控本地 provider 的 v2 native 结果不能扩大为这些结论。

## 验证与证据

- Go：`CGO_ENABLED=0 go test ./... -count=1`、`go vet ./...`、`go build ./...`、`go test -race ./...` 均通过（本分支候选）。
- 真实 runtime + 应用层集成（opt-in，受控 fixture）：
  ```sh
  SAKI_TEST_RUNTIME_BIN=/abs/path/to/Saki CGO_ENABLED=0 go test \
    -run 'TestRuntimeProcessIntegration|TestGUIAppRuntimeFlow|TestGUIFirstCreateUsesProfileSnapshot|TestGUIAppPersistsMappingBeforeSend|TestGUIAppSaveFailureStopsBeforeSend' -count=1 .
  ```
  覆盖 read 工具、task/message 关联、GUI send/切换 B 草稿隔离、绑定设置只改名、drain/重启零 provider 调用、继续前文、首次 create 使用 profile 快照、mapping 持久化先于 task.send，以及 store 保存失败时零模型/工具调用且草稿保留。当前 `.github/workflows/ci.yml` 的 rust job 会构建真实 Rust binary 并设置 `SAKI_TEST_RUNTIME_BIN`，执行上述 5 项既有集成，且逐项检查 `--- PASS`；未设置该变量的普通本地 `go test` 才 skip。新增的 `TestRealRuntimeStartupConfigurationFailure`/`TestRealProviderAuthFailureClassification` 未写入 CI gate，不能写成 CI 已运行。
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
| 模型配置 | #116 GUI 宿主配置；无 GUI 文件时兼容外部环境；会话内模型切换由 #115 继续 | 默认值与已存在会话的实际配置分开 |

上述是当前交付边界；不得把单执行槽或本地过滤写成并发执行／模型检索已完成。

## 布局与断点（浏览器历史示意）

本节保留早期浏览器历史示意；当前原生布局以顶部 [GUI 设计约束（当前规范）](#gui-design-constraints) 和 [#184 可调三栏具体值](#adjustable-layout-values) 为准，不再以 #90 或本节比例作为当前实现。

- **≥920px**：`contact rail (默认 220px)` ｜ conversation ｜ 右侧 aux 300px，仅在 source/settings 等操作后出现；没有常驻第三栏。
- **640–919px**：rail + 单栏内容；source/settings 直接替换主内容，而不是压缩两栏。
- **<640px**：contact list 变为可展开 compact 列表；aux 同样替换主内容。
- 不使用 viewport-height 布局、fixed 定位或内部滚动盒；内容随页面增长，任何宽度都不出现横向溢出。
- Tweak（宿主支持时）：contact list width、message spacing、reading width 三个只影响布局的参数。

## 主题系统（#92 设计）

状态：**历史设计记录**（[#92](https://github.com/HeimaoLST/Saki/issues/92)）。用户当时的 Moon/Dawn 方向已由 #96 的 Paper & Moss 当前实现替代；本节的 Moon/Dawn 数值、B 阶段目录与自定义主题提案只保留为历史设计，不表示当前能力或未来承诺。当前实现见末尾 [#96 Paper/Moss 外观、联系人 scope 与 composer 模型入口](#96-paper-moss-外观联系人-scope-与-composer-模型入口实现事实2026-10-28)。

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

应用只向组件暴露语义角色；下表的 Moon/Dawn 值是设计基线（原始 + 有依据的派生）。`SurfacePressed` 明确复用 `SurfaceHover`；该 overlay 上的 `TextMuted` 已 ≥4.5（Moon 5.78 / Dawn 5.00）。只有 `highlightMed` 不承载 muted 小字；pressed/hover 同色，真实按压/焦点反馈列入实施验收，不假称已有额外视觉区分。

| 语义角色 | Moon | Dawn | 说明 |
| --- | --- | --- | --- |
| Background | `#232136` | `#faf4ed` | 官方 base |
| Surface | `#2a273f` | `#fffaf3` | 官方 surface |
| SurfaceHover | `#393552` | `#f2e9e1` | 官方 overlay |
| SurfacePressed | `#393552` | `#f2e9e1` | 与 hover 同色；muted 在此 overlay 仍 ≥4.5 |
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
| Scrollbar（MyGo） | `TextMuted`（不透明） | `TextMuted`（不透明） | `ui.Theme` 字段；对 base/surface/overlay ≥3（Moon 7.78/7.15/5.78，Dawn 5.49/5.78/5.00） |
| Inverse（MyGo） | `Text` | `Text` | 零值 fallback 即可 |
| InverseText（MyGo） | `Background` | `Background` | 零值 fallback 即可 |

**唯一小型 resolved 容器**：`ui.Theme` 没有 Link/Progress/BubbleUser/QuoteHighlight 字段。resolver 返回一个容器，例如：

```go
type resolvedTheme struct {
    Widgets        *ui.Theme
    Link           ui.Color
    Progress       ui.Color
    BubbleUser     ui.Color
    QuoteHighlight ui.Color
}
```

`view` 把 `resolvedTheme` 传给组件；组件读取 `resolved.Widgets` 的框架语义与四个 app 角色。不要往 `ui.Theme` 强塞新字段，不要复用 `Selection`/`Success` 顶替这四个职责，也不预建组件主题框架。A 只有这四个 app 角色；未来 tool/code 块若需要独立 surface，优先复用 `Surface`/`TextMuted`，不为尚未接入的 Markdown 预建主题系统。

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
- `Border`（highlightMed）对 Surface 对比不足 3:1，不能作为唯一控件边界；控件通过 surface 层级、文字和 Focus 识别。`TextMuted` 不放在 highlightMed 上；当前 `SurfacePressed` 是 overlay，已 ≥4.5。pressed/hover 同色，真实按压/焦点反馈列入实施验收。
- 不为每个 hex 写机械测试；对比度作为设计计算，实施验收检查真实组合。

### Appearance、模式与解析

- **入口**：应用级 `Appearance`，独立于 Contact Settings；建议放在联系人侧栏底部的低权重入口，显示当前有效主题名。Contact Settings 仍只编辑联系人 profile。
- **模式**：`system`（默认）/`light`/`dark`；按 OS 明暗，不做定时切换。light 与 dark 各自记录 scheme ID，默认 `rose-pine-dawn` / `rose-pine-moon`。当前每类只有一个内置方案，A 阶段只显示“当前有效主题”（不显示没有选择意义的空下拉）；B 阶段有多个方案/自定义后再显示选择器。
- **app model**：持有 `Appearance{mode, lightID, darkID}`；每帧只有一个 resolver：根据 mode 与 OS 明暗选出 scheme，再生成上文的 `resolvedTheme`（`Widgets *ui.Theme` + 四个 app 角色）。`view` 只在入口调用一次 resolver，后续组件接收 `resolvedTheme` 并读取语义角色；不在组件里判断 Moon/Dawn/品牌名。
- **解析顺序**：`mode=system` 时读取当帧 `c.Theme().Dark`（这是 SetTheme 之前的默认主题，反映 OS/后端 effective appearance）；`mode=light|dark` 时直接使用存储的 mode，不把 `SetSource` 后的 forced Dark 当 OS。解析后立即 `c.SetTheme`，本帧后续使用该主题。
- **无效设置值与 fallback**：appearance 独立校验，不能因 mode/ID 错误把整个 contacts store 判坏。`mode` 非法→按 `system`；light/dark ID 缺失、未知或 kind 不匹配（例如 light 槽指向 dark 主题）→该槽回退内置默认同 kind 主题，保留用户选择 ID 并显示非阻塞提示；不覆写用户偏好。恢复 `system` 时必须让 backend source 为 `ThemeSystem`；首次创建窗口时按两类 scheme 的 base 设置 `light-dark` pair，避免先显示错误色再切换。
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
  - **统一 A/B 底色路径**：`light-dark(lightID 对应 scheme.Background, darkID 对应 scheme.Background)`；`Theme.SetSource(system/light/dark)` 决定 effective 明暗，A 与 B 共用这一条路径。手动模式不再另造两个相同 stop，B 也不另换显式 base 路径。
  - **更新时机**：启动顺序为恢复 preferences → 解析 light/dark 两类 scheme 与背景 pair → 在 `App.Run` 前调用 `SetSource`；运行期模式改动只调用 `SetSource`，只有 light/dark ID 变更或自定义文件派生值变更才更新两个 stop。OS 变化由 backend 的 `updateBackgrounds` 处理；不为窗口底色注册 OS listener，也不每帧写入。
  - native titlebar/menus 仍只有 light/dark，不承诺任意品牌色填充；自绘 titlebar 需要 hidden titlebar/frameless + `Context.TitleBar`/拖动区域，另立 feature 并做真实窗口验收。
- **系统偏好边界**（基于源码，不凭空承诺全系统 accessibility）：
  - `ReduceMotion`：MyGo 的 `Animate`/`Transition`/`Timeline` 会遵循；A 没有新增持续动画，因此无额外承诺。
  - `HighContrast`/`TextScale`：默认主题的 `Theme.follow` 会应用（`ui/preferences.go:49-77`），但 `Context.SetTheme` 的自定义主题不会自动 follow。当前 #90 的 `roseTheme` 已经绕过它们；A 明确**不新增**全系统 HighContrast/TextScale 适配，也不声称已支持。若产品要求，需在 resolver 中集中处理并单独验收，不扩本次配色功能。
  - 系统 Accent：A 使用 Moon/Dawn 自有 accent，不用系统 accent 替换品牌色；这是配色选择，不作为系统 accent 已适配的声明。
  - `ReduceTransparency`：最新 MyGo 的 `ThemePreferences` 已增加（[定义](https://github.com/egoist/mygo/blob/163d50e2acc1b5361d29bc6d26a8a0e11ebdb847/modules.go#L320-L350)）；当前 app 不使用 `Vibrancy`/透明材质，A 无需新增机制。若未来使用材质，应在该偏好为 true 时关闭/blur 替换为不透明 surface，单独验收。
- **模式切换与字体**：换主题不改变 FontSize/Radius/Spacing，也不改布局；只有颜色变化。系统 TextScale 若未来实现，必须在所有 scheme 上统一处理，不能因换色改变字号。

### 自定义主题（B 阶段设计）

- **目录**：`UserConfigDir()/Saki/themes/*.json`；一个文件一个主题。文件名自由；`id` 必须是小写 kebab-case、稳定且唯一，不能与任何注册内置 ID 相同。不强制 `id` 等于文件名，也不另设前缀命名系统；内置注册表 ID 受保护。
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

## #141 产品事项与执行关联（实现事实，2026-10-24）

代码基线：`9de72fd`（上一片模型/store 为 `e0749fe`，派发/结果接线为 `4e63ef8`/`9d98765`；本轮仅收紧准入并删除未用代码）。本段是实现事实，不是原生窗口验收；原生窗口/IME 仍由 GUI 主责实例或人工在 macOS 上复核。

- **显式事项**：`productIssue` 是 GUI 自己的产品对象，包含稳定 issue ID、`ContactID`、原始 Goal/Requirements、开放状态、显式 Evidence 与时间戳；`issueExecution` 是一次真实 Runtime 关联，记录 execution ID、SessionID、TaskID、MessageID、发送时的 prompt、状态与错误摘要。普通聊天不会自动创建事项；只有 workspace 的 New issue/Save issue 会创建。
- **存储**：contact store 升到 v2，在原有 contact/session/draft 字段旁增量保存 issues；v1 文件仍可读取并原样保留 contact/session/draft，首次保存后写回 v2。加载时校验 issue ID 非空且唯一、`ContactID` 非空且对应当前 store 中的 contact；空/未知 ContactID 报错，不再把同一 issue 归给所有联系人。旧 store 恢复、原子保存失败与重复/孤儿 ID 有确定性测试。v2 是单向升级：旧 app 不能读取 v2；回滚必须保留原 v1 文件/备份，且不要让旧包继续写 v2 文件。
- **保存顺序**：New issue 先写入 store，成功后才关闭表单并清空 Goal/Requirements；保存失败回滚追加项、恢复原选择、保留表单文字并显示错误。Run issue 在派发前先持久化 execution 快照（已有 SessionID 也先写入）；预派发保存失败不调用 Runtime，移除临时 execution，普通 composer 不变。
- **执行状态**：`pending`（仅进程内、派发前）→ `accepted` → `running` → `completed`/`failed`/`interrupted`；另有 `not_dispatched`（确认未发出，可明确再 Run）与 `rejected`（Runtime 明确拒绝，可明确再 Run）。Runtime 的 `completed` 只映射到 execution 状态，不改变事项为完成；本阶段事项始终 Open，尚无 Evidence/关闭入口，完成状态保持未核实。
- **在途身份**：发送身份只在 `canSend` 准入成功后写入/替换；busy 被拒的普通 composer 发送不会清空在途事项的 `pendingIssueExecution`。只有被接纳的普通发送才清掉旧失败残留，接纳的事项发送才能把 association 挂回。mapping callback 被暂停时，普通 Send 被拒后恢复 mapping，最终 TaskID/SessionID 仍精确归属原事项；真实 Runtime 测试覆盖。
- **未知与重启**：Accepted 后关联保存失败时显示 unknown，保留已接受的 task/message 关联快照和 later draft，不自动重发；重启后 `pending` 一律显示 unknown；无 TaskID 的 accepted/running 也转 unknown；有真实 TaskID 的 accepted/running 仅由 Runtime history/task 关联恢复，缺失 history 时转 unknown。unknown/active execution 阻止 Run，不凭 prompt 猜测 TaskID；`not_dispatched`/`rejected` 允许用户再次显式 Run。
- **结果可读性**：workspace 的 execution 行优先展示该 TaskID 的真实 assistant 消息片段；没有助手消息是正常的 P5 场景，显示中性的 “No assistant message was recorded … Use View execution” 提示，工具输出只作为 “tool output only” 辅助，不足以宣称阶段结果。`View result` 只定位属于当前事项的 TaskID，展示该次 work/tool details；旧事项不会把另一事项的 current work 当作自己的结果。选中事项与 result 的归属标签固定为 `Run N … · <issue goal>`；执行行只保留一份 Runtime 状态，不再重复 Work 状态。
- **事项边界**：原需求显示为 “Original requirements · unverified”，Completion 只说明证据未记录；“Runtime completed” 仅表示一次执行结束，不等于事项要求已满足。本阶段所有事项都保持 Open，没有自动满足或自动关闭判定；只完成显式事项/执行关联这一子集，不宣称 P6 全量完成，也不包含 P3 并行、P4 补充/采纳、看板或后台能力。
- **窄屏与键盘**：open issues 使用纵向列表，不横向撑宽；320/360 是 headless 工作面板宽度测试，work overlay 收窄到窗口宽度，结果卡、两轮执行与无工具结果均可读，无横向滚动；它不表示原生应用最小宽度，原生最小仍为 720。New issue 打开聚焦 Goal，Cancel/保存后回到 New issue；未保存 Goal/Requirements 时切换联系人（或 Add contact）被拒绝，内容不串到另一联系人。
- **验证证据（代码 `9d98765`）**：
  - `SAKI_TEST_RUNTIME_BIN=/private/tmp/saki-gui-141/runtime-target/debug/Saki CGO_ENABLED=0 go test -race ./... -count=1 -v`：110 PASS / 0 FAIL / 5 SKIP，12.865s；5 个 SKIP 均为在线 smoke 或未设置 `SAKI_PREVIEW_DIR` 的 headless preview。
  - 九个既有真实 Runtime 用例全部 PASS；#141 新增 `TestGUIAppIssueExecutionsPersistAndRestart`、`TestGUIAppIssueSaveFailureStopsBeforeDispatch`、`TestGUIAppIssueStopKeepsItemAndExecution`、`TestGUIAppRejectedComposerSendKeepsInFlightIssueAssociation` 也全部 PASS（同一 race 运行）。
  - Rust 二进制 sha256 `3553a52c…`（`cargo build --locked --bin Saki`，独立 target `/private/tmp/saki-gui-141/runtime-target`）；desktop native build sha256 `690ec1d6…`；`go vet ./...` 通过。
  - headless MyGo Tester 截图在 `/private/tmp/saki-gui-141/previews/`：light/dark 960×640、1040×720、1380×900、900×640 overlay、320×640/320×900、360×800，以及 issue form 960×640、working/unknown；包含两个事项、两轮执行、无工具/无助手消息结果。它们不是 macOS 原生窗口截图；320 仅作为工作面板宽度测试。
- **限制**：当前没有“写入完成证据/关闭事项”的 UI；unknown execution 需要重新加载或另建事项，未提供强行解决入口；未验证原生 CUA（`Sky native pipe startup failed` 由父级记录），不把 headless 截图写成 native PASS。事项执行失败/完成都不会自动满足或关闭事项，这是本阶段的明确产品边界。

## #113 Markdown 阅读与复制（实现事实，2026-10-25）

代码来源：reader/Iris 分支提交 `75fdfc7400c223dcffd9bc86a9d7f02119f76393`，经 #118 分支普通 merge 合成为 `79299b3`；当前组合代码基线见下一节。此处记录实际写入的 reader 行为，不把 Markdown 组件扩称为完整 Nagi MVP。

- 新增 `desktop/markdown.go`、`markdown_test.go`、`markdown_view_test.go`：`markdownReader` 只是 MyGo Markdown 组件的消费层，不复制 parser、不另造文档模型；保留完整源码复制、代码块复制、表格渲染、流式文本、选择与键盘行为。
- 对话、历史、来源和工具详情通过该组件显示；用户文本保持 literal/plain-text 路径，工具输出只作为 received output 显示和复制，不推断交付文件或权威 diff。工具详情折叠时仍可复制完整 received output，展开后不重复第二个全文复制控件。
- `view.go` 保留 Back to latest/FollowEnd 行为；键盘焦点、消息选择和复制反馈沿用 reader 的实现。`workspace_test.go` 新增 received-output copy 回归；本次 #118 UI 改动没有修改 parser，也没有把 reader 测试改写成 #118 路由测试。
- 证据边界：reader 的 headless 测试与 #118 的 app/real-runtime 测试分开记录；原生窗口/输入法仍未由 #118 执行，不能把 headless 通过写成 native PASS。

## #118 独立工作会话与事项 UI（实现事实，2026-10-25）

代码来源：本分支 `agent/gui/118-independent-execution`；reader 精确提交 `75fdfc7` 的 merge 为 `79299b3`，UI/路由与 rail 整理最终为 `c4779f9`（中间 UI 提交 `3197849`、preview `6702e6a`）。本节只陈述 #118 阶段事实，不表示完整 MVP、#142/#153/#154 或全部 MVP 已完成。

- **数据**：contact store 升到 v3，`issue.work_session_id` 持久化事项工作会话；v1/v2 可读，旧程序对 v3 的版本检查会拒绝而不是静默覆盖。`contact.SessionID` 继续是持续对话；每个事项只保存一个 `WorkSessionID`，运行时代码不再另存 `Work.SessionID`。每个 `issueExecution.SessionID` 保留该次执行的真实 session；旧混合 session 的执行不迁移。
- **路由**：事件按 `session_id` 先区分对话/事项工作，再按 task/message 归属；issue work 不写 `contact.ActiveTaskID`，不进入 contact.Messages，不增加聊天 unread。工作消息和工具结果进入 `issue.Work.Messages`/`ct.Works` 投影；重启分别恢复 contact 与 issue work 会话，历史投影互不覆盖。
- **容量**（#196 候选更新，2026-10-28）：启动后读取 `runtime.capabilities.max_active_tasks`。仅在 Runtime 连接存在且 ready、容量未知或加载失败时，composer 显示容量提示和 `Retry capacity`；未配置、尚未就绪或已断开时不显示该提示及无效重试，仍保留真实启动错误和 `Configuration details`。已加载的真实单槽不显示容量错误。未知容量继续保守用 1，不改容量值、准入规则或公共协议。工作准入最多占用 `n-1` 个槽，`max=1` 允许串行工作、`max=2` 保留一对话槽，`max=3` 已实测两事项并发；实际 Runtime busy 仍由 Runtime 拒绝，reason 保留在 DTO。#196 的本地 headless/Go 证据见对应交付记录，真实窗口复验由 PM 执行。
- **发送/停止**：issue work 发送使用独立 work session，派发前保存；accepted 不恢复为草稿、不自动重发。右工作卡 `Stop issue task` 只取消当前事项的真实 task；composer `Stop conversation` 只取消对话 task，两者状态/标签分开。对话在另一事项工作时可发送（容量允许时）。
- **恢复/核对**：`task.status` 核对、重启 history 核对、缺 TaskID 的显式“确认未派发”动作均可用；核对旧 execution 只更新该 execution 行，不清除/覆盖另一个 active task。错 issue 的 execution 不会被核对动作修改；不凭 prompt 猜测 ID、不自动重试。
- **可见性**：`issueWorkNotice` 提供 rail/隐藏工作区的派生摘要和工作数；失败/未知即使没有聊天消息也能显示 `Work attention`，不增加聊天 unread。事项结果优先读取 `issue.Work.Messages`，旧 execution 按实际 SessionID/历史兜底。
- **验证（当前代码承诺范围）**：`go test ./...`、`go vet ./...`、真实 Runtime 受控双 gate 的 A/B 会话隔离、对话/事项双在途 Stop、max1/max2/max3 容量、跨联系人/事项历史、profile admission snapshot、未知恢复、quit 多 session；最新完整 Go race 为 157 PASS / 0 FAIL / 7 SKIP（固定到合并后的工作树；具体 SHA 与日志由 #118 交付评论记录）。测试使用本地 fixture，不连接付费在线模型。
- **UI 边界**：右工作卡只展示当前事项的真实 work；事项结果预览使用工作会话消息，不把聊天记录当工作结果。普通对话 composer 与事项 Stop 不共享控制路径。容量未知提示不会在真实 1 槽时出现。
- **未包含/后续**：settings 模块（`5551b76` 主责）未接入本 PR；#142 补充递交、#153 生命周期、#154 对话协作不随本阶段扩称完成。原生 CUA/窗口/输入法仍由父级验证；`Sky native pipe startup failed` 不得被 headless 截图替代。完整 Nagi MVP 仍需后续 issue 与真实试用。

## #116 首用配置与安全重载（候选实现，2026-10-25）

代码来源：PR158，保留独立模块 `5551b76`，在普通 merge `37a3b3d` 消费冻结组合 `021bf575`（#118/#113）后接线。需求依据 PR157 `a0d4af7` 的完整 MVP；本节不改变 #113/#118 已记录的事项、历史与路由语义。验证 SHA、命令结果与未验项以本单交付证据为准。

- **首次使用**：配置读取先于默认联系人持久化。没有 GUI 文件也没有既有外部配置时，显示可编辑设置；保存选定目录后才生成真实默认联系人。已有联系人的目录、草稿、会话与事项不被新默认值批量覆盖。可解析但失效的设置保留值供修正；坏文件保留并报错。
- **唯一来源与凭据**：`connection.json` 通过同目录临时文件、sync、rename 原子保存，权限 0600；key 是本机私有明文，界面明确说明，不声称 Keychain 加密。凭据不存入联系人/事项文件。HostConfig 是不可编辑的派生启动快照，仅含凭据环境变量名称；实际 key 由独立 child env 注入。受管配置覆盖继承的旧连接与容量环境，不改变 GUI 进程的环境。
- **保存与应用**：Save 只保存配置，失败保留原文件及编辑值；当前 helper 和有效默认值不随保存改变。Apply saved 验证候选 helper 的 ready 与 `connection.list` 后才替换旧 helper，失败保留原连接可用性。切换期间禁止新派发，切换成功重读真实 capabilities；配置与历史恢复均不自动发送任务。
- **活动工作保护**：使用 #118 的 `activeTaskCount`/`sessionBusy` 与 `issueRunBlocked`，涵盖所有联系人对话、事项工作、派发中、取消中和未确认执行。保存可独立进行；有活动目标时 Apply 明确拒绝。未知派发使用现有 Check execution/确认未派发入口处置，不盲目重发；旧 helper 已实际退出时可启动新 helper 进行只读恢复。
- **显式测试**：Test connection and model 使用表单中的值发起短模型测试；它不保存或应用设置，不改变当前联系人历史。测试在独立临时会话目录中运行、禁用工具，有时限；退出先取消测试 task，待临时 helper 和目录清理完成后再关闭窗口。ready、保存成功与 Apply 成功均不表示供应商已接受模型请求；测试结果与主 helper 生效状态分开显示。
- **实际配置与旧会话**：连接来自 `connection.list`，模型/目录/容量来自临时会话的 `session.config.get`，并非把输入值直接标为生效。受管新对话和新事项工作使用同一个配置创建入口，显式 schema 2 与连接 ID，持久化 endpoint 身份。旧会话不隐式升级或重新绑定；绑定未知/endpoint 不兼容时保留历史查看，用户显式创建新上下文继续。旧联系人与事项不会为“应用设置”而被删除。
- **#115/#152 接点**：#115 负责已有会话内模型选择与 config revision，不能把宿主默认值当作所有旧 session 已更新；#152 继续联系人/历史的产品管理，已有 Workspace 保留，新联系人采用已应用默认目录。外部 HostConfig 的多连接/MCP 目录不会自动合并进本单单连接 GUI 来源，本单没有新增 MCP 管理器、OAuth、云同步或通用插件框架。
- **验证范围**：`connection_app_test.go` 验证空配置不提前落盘、旧联系人保持、无效设置修复及联系人目录创建失败后可修复；`connection_app_integration_test.go` 通过实际 app.view/actions 和真实 Rust helper 验证 Save/Apply/Test、首次发送、重启、保存失败、后台事项保护、候选失败回退、未知恢复、换端点后的历史边界、测试错误修正和退出取消。`connection_integration_test.go` 继续覆盖 Responses/Chat 两协议。使用受控本地 endpoint，不代表任意外部 provider 均兼容。
- **未验与外部缺口**：本轮原生 CUA 仍返回 `Sky Computer Use native pipe startup failed`，Finder 冷启动/原生键盘交互未验，headless 截图与真实进程测试不替代。#160 已复现 Runtime 把 provider 回显的当前 key 写入错误事件与历史；GUI 显示层处理不能修复服务端 journal，本单不宣称该全链路边界已通过。没有真实凭据用于该复现或候选包。

## #153 事项生命周期、要求核对与归档（实现事实，2026-10-25）

代码来源：`agent/gui/153-issue-lifecycle`，base `885fb1f`（#116 配置组合），生产代码最终为 `817d9c9`（生命周期实现 `23929d9`，之后仅有归档标题/截图测试整理）。只描述 #153 范围，不等于完整 MVP；#142/#154、看板、日程和跨人转交未随本阶段完成。设置模块仍由 #115/#116 负责，本单未改 `connection_app.go` 或 `runtime.go`。

- **状态**：事项使用持久化 `not_started / in_progress / waiting_user / needs_attention / completed / canceled`；`Archived` 是独立组织状态，不并入生命周期。旧 v1-v3 的 `open` 在加载时收敛为对应初始/进行状态；运行时 completed 不自动把 issue 设为 completed。
- **持久化**：store 升到 v4，写入结构化 `requirement_items`、`requirements_revision`、`archived`、execution 的 `requirements_revision` 字段；原 `1700f8d` 创建新执行时漏赋值，后由 #117 组合在派发前捕获当前 revision；已有 0 保留为未记录，不按当前要求回填。v1-v3 仍可读。旧整段 `requirements` 只在迁移/序列化边界解析，运行时 live 状态只有 `productIssue.Requirements []*issueRequirement`，不长期双写两套真相。新版本写入后旧 app 明确拒绝，不静默覆盖。
- **要求与证据**：每项要求有稳定 ID、text、`satisfied/unmet/unverified`、evidence；证据可由用户判断、可核对执行/task 或文件/检查来源；不新增 Source 枚举或自动审批关卡。用户可修正状态与证据，完成由用户显式动作产生，不要求逐项审批，也不因 execution completed 自动 satisfied/Done。
- **编辑诚实性**：保存 Goal/Requirements 递增 `RequirementsRevision`；旧 execution 的 `Prompt`、SessionID/TaskID/MessageID 和 revision 不被编辑改写。Goal 改变时旧的 issue Evidence 清空，同文本要求也只保留为“Previous evidence (goal changed)”并回到 unverified；需求文本改变的新项为 unverified。旧 execution 仍以 sent snapshot 展示。
- **动作**：创建、编辑、Start/Continue、Stop issue task、Cancel issue、Mark waiting、Resume、Complete、Reopen、Archive/Restore 均落到同一 `productIssue` 与现有 `runIssue`/`cancelIssue` 路径；Continue 复用同一 `WorkSessionID` 追加 execution，不复制 issue。完成 evidence 由用户输入或明确用户判断；没有自动完成。
- **Stop 与 Cancel**：Stop 只结束当前真实 execution，issue 不进入 canceled/completed；Cancel issue 是独立产品动作，保留 execution history。若用户在派发重试等待期间 Cancel issue，先把 Canceled 持久化并记录进程内 cancel-after-accept 意图；TaskID 出现后由实际 owner contact/issue 定向 Stop，不依赖 UI 当前选中，不把产品 Canceled 伪称 Runtime 终态，也不自动重发。
- **恢复与保护**：重启/历史恢复会从最新真实 execution 收敛 in_progress/needs_attention；旧 execution 不覆盖新 active，也不覆盖用户显式 completed/canceled/waiting。归档只隐藏分类、不取消 active task、不丢 WorkSession/execution；Apply 使用既有 activeTaskCount/issueRunBlocked 扫描全部（含 archived）事项。未知执行保持核查/确认入口，不自动重试。
- **UI**：状态筛选提供 Active/Waiting/Attention/Done/Canceled/Archived，归档恢复、详情、编辑要求、逐项证据与动作入口共用同一事实；右侧 Stop issue task 与 composer Stop conversation 分离；长内容在 320/360/720/960/1040/宽屏下使用垂直滚动，不增加横向溢出。旧 execution 显示 sent snapshot/revision，而不是用当前要求冒充旧提交。
- **验证**：本阶段完整 `go test -race ./...` 为 203 PASS / 0 FAIL / 8 SKIP（其中在线 smoke 1、环境 preview 7；本地真实 helper 使用 #116 核验 SHA `3c19b03f…`，不连接付费在线模型）。真实 process 覆盖 Stop/Continue、取消 pending acceptance、重启、归档 active、策略/owner 与失败回滚；headless 截图 50 张，light/dark，960/1040/1380 wide/900 overlay/320/360，另含 completed/dual working/unknown/archive/edit/long content。原生窗口、CUA、输入法和真实应用内完整回归尚未因此宣称通过。
- **退出间歇**：曾有一次完整 race 在 `TestIndependentIssueWorkAndConversationCapacityTwo` 的 quit drain 处挂起（父级 SIGQUIT 收栈，原 `race-final.log` 与栈保留）；之后同用例 `-count=5` 与多轮完整 race 通过，父级 885 基线也有独立 PASS，尚不能宣称 Runtime 基线 bug 或已修复。该失败未被抹去，#160 修复不被当作退出已过；后续组合复验才作结论。
- **边界**：设置保存/Apply 仍由 #116 负责；#154 后续必须复用这里的事项动作和事实，不另造生命周期；#142 的补充递交不在本单。用户判断可作完成依据，但不是自动完成；无依据要求保持未核实。

## #115 会话模型选择（候选实现，2026-10-26）

代码来源：`agent/gui/115-session-model`，在#116固定`885fb1f`上普通合入#153固定`1700f8d`与Runtime #160固定`722de4b`。这是会话配置消费能力，不改变Rust公共协议、不迁移联系人store，也不代表整套Nagi MVP已交付。

- **作用对象**：Conversation model读取联系人的持续对话`SessionID`；Issue model读取选中事项的`WorkSessionID`。选择器显示Runtime返回的有效模型、容量、来源、revision和目标，更新只用于该会话的下一次任务。旧execution快照、消息历史和联系人ID不改写；宿主默认值继续由#116管理。
- **未创建上下文**：打开或取消模型面板不创建会话。界面说明新上下文使用已应用的Connection settings默认值；已有会话才能在这里独立切换，不把默认值冒充旧会话的生效值。
- **单一配置来源**：沿既有config DTO消费`model.list`、`session.config.get/update`。provider model ID与catalog容量ID分别提交；目录/宿主声明不保证endpoint支持该模型。没有连接身份时不猜测默认连接，没有目录时可显式输入容量；服务端校验保留其错误原因。
- **提交与取消**：以读取快照的revision提交一次更新，成功返回值才标为生效。busy、revision冲突、非法容量或原生续接不兼容均保留历史与可修正草稿，不重建联系人绕过错误。取消、Escape或关闭面板不提交修改；写入在途时关闭不能绕过操作保护。
- **不确定结果**：任何更新失败先读取一次实际配置，不自动重发。读回成功显示实际值并保留原错误；读回失败只显示Last read，关闭面板也不解除该会话的发送限制。成功重读可解除待刷新状态；`commit_unknown`需要显式Release/Load恢复，单纯Refresh不解除限制，也不会自动提交用户修正草稿。
- **并发与退出**：另一会话的活动任务不阻止当前空闲会话切换。正在更新/恢复的目标暂停接纳新任务，其他目标保持独立；Apply helper重载等待操作完成，Quit沿已有退出路径等待更新/读回收尾。异步读取按面板代次与helper身份归属，旧目标迟到结果不覆盖新面板。
- **#117接点**：已有`sendTask`增加可选`runtimeContextInput{Name,Text}`，非空只进入`params.context`一次；旧两参调用兼容。资料快照/存储/递交行为仍由#117交付，不属于本单完成范围。
- **验证边界**：`model_selection_integration_test.go`验证真实双协议模型请求、目录容量与provider ID分离、失败保持、丢回复和显式Release/Load；`model_selection_app_integration_test.go`从实际app视图/动作验证对话与事项隔离、重启、拒绝/冲突/取消、未知恢复、退出互锁及逆序读取。故障代理在真实Rust提交后丢弃或替换指定回复；`commit_unknown`代理只验证GUI消费语义，Runtime磁盘故障由其既有测试覆盖。使用固定#160 binary与本地受控HTTP provider，不声称任意外部模型或真实付费模型已验证。
- **本机组合结果**：以#160 binary（SHA256 `c738c63374c0222b8e4397a15e1fb4109c8ff6bf34b89b8219cdecd7e0fa5516`）执行全部Go race并开启预览，240 PASS / 0 FAIL / 3 opt-in SKIP，136.672s；`go vet`、GUI build、diff检查通过。实际app明暗960/1280预览24张，选择器560/320预览8张；这些是受控数据下的headless页面。此前一次Chat子用例等待/quit超时未稳定复现，原日志保留，不能将本次通过写成已修复所有退出问题。
- **原生限制**：2026-10-26再次读取CUA状态仍返回`Sky Computer Use native pipe startup failed`，未取得本候选原生点击/键盘/输入法证据。headless布局与真实进程消费者验收独立记录，不替代#114原生验收。最终验证SHA、门禁和日志随PR165/issue #115更新；尚未集成dev不标done。

### #206 已释放事项的模型更新（实现事实与待验，2026-10-30）

代码来源：`agent/gui/206-released-issue-model`，基于固定组合 `12774bbd18afb03407b366e1213bc799ba0095e4`。该基线的事项在执行完成、历史读回后会自动释放 Runtime session；配置读取仍可从持久化记录返回，但模型更新要求 session 已加载。原双协议实际 GUI 模型切换用例因此返回 `not_found`，下一次事项执行仍用旧模型；原始诊断见 [#200 的只读报告](https://github.com/HeimaoLST/Saki/issues/200#issuecomment-6491851126)。

- **更新接点**：打开 Issue model 仍只读配置；实际提交更新时，若该事项 session 已释放，在同一次既有模型操作内先加载，再以原 revision 执行配置更新。释放进行中或待释放的目标拒绝提交；其他 session 的操作不因此被全局阻塞。没有取消任务完成后的自动 release，也没有修改 Runtime 对卸载会话的配置更新契约。
- **状态与异步归属**：沿用 `modelOpActive` / `modelOpSessionID` 的任务准入、连接重载和退出保护。加载成功后更新原事项的 Released / Loaded 状态，即使配置更新随后失败；回调先核对原 Runtime、连接代次、联系人、事项与 session。切换联系人或隐藏页面后，原会话仍须完成读回及未知结果保护，不能因为页面不可见而丢失 quarantine；结果只写回捕获的原表单与原会话，不投到新对象。
- **失败语义**：加载失败时不发送模型更新，保留旧有效配置、修正草稿与已释放状态并显示错误。加载成功后的配置冲突、提交未知、读回失败继续走既有单次更新与读回路径；不自动重发或显示虚假的已应用状态。
- **验证边界**：保留原双协议 GUI 入口、下一次实际请求和重启后配置断言；定向回归使用真实 Runtime 与受控 provider，补充实际 release 回调未收尾时拒绝更新、加载失败无更新，以及切换联系人后成功或未知结果仍只归属原会话。测试源码与外部 helper 的 SHA 分别记录在 #206；本线不单独重建最终包或重跑全包，精确修复交 #200 组合后统一完整检查、打包与 PM 原生复验。旧 `6339e23` 四阅读面原生通过保留原 SHA 范围，不代表本修复已原生通过或已集成。

## #117 资料快照、阅读与递交（候选实现，2026-10-26）

代码来源：reader/reader-iris 的 agent/gui/117-task-context，固定 settings #115 d9872e8（含 #153 1700f8d 与 Runtime #160 722de4b）之上。最终源码、门禁和交付 SHA 以 PR170 为准；本节不等于完整 MVP 或已集成 dev。

- **输入与快照**：命名文字和用户明确填写路径的 UTF-8 文件使用唯一 taskContextItem 值模型。Name/Source/Text/ID/Revision 保存当次快照；Source为明确导入的绝对路径，显示名称和原始来源一起投影到 Runtime context.name，text 不改写。文件变化不会自动重读；Refresh from file 才生成新版本，编辑只改变快照不写源文件，刷新/读取/格式/总预算错误保留原值。PDF/Office/二进制容器不做提取。
- **界面与归属**：对话 composer 和事项详情各有轻量 Task context 入口，编辑在既有有界侧面板，联系人与每个事项各自保存待发清单。未保存编辑在联系人/页面切换时留在原对象，只有明确保存成功的快照保证重启恢复。英文提示沿用 Paper/Moss 主题；不新增附件平台。
- **单次输入**：对话和事项 Run/Continue 都沿既有 sendTask 的可选 context 参数。派发前固定并保存该次输入；正文只放 message.text、资料只放 context，不拼进 system 或再次附进正文。每个 issueExecution.Context 是独立不可变快照；后续编辑/刷新不改旧执行。接纳只移除同ID同Revision待发项，后来的编辑仍保留；拒绝、容量满和派发前保存失败不清空。
- **预算**：GUI检查序列化 caller_context 包装后的合计1MiB，包含名称和来源标签；不是裸文件大小。Go JSON转义相对Rust略保守，最终接纳验证仍由Runtime负责。不静默截断或部分读取后当成功。
- **历史与复制**：用户资料保持字面文字，独立展示名称/来源和精确原文复制；历史资料是只读记录，不成为待发草稿。Runtime将资料编码为普通text part，本轮GUI保留非空正文首part，后续wrapper才按契约显示；这不提供通用的来源证明。外部context-only输入的首part无法仅凭形状判定，保留原始可读文字而不猜测。
- **持久化**：contact store v5读原v1-v4，保留既有联系人/会话/事项与旧execution身份；新增待发资料、执行资料快照和未确认对话提交意图。旧v4程序拒绝v5，避免保存时静默丢新字段；降级使用升级前备份或隔离数据目录，不直接交旧程序重写v5。#142 进一步升到 v6，增加事项补充 ledger；#152 的 v7 保留这些字段并读 v1-v6，详见对应章节。Runtime消息历史仍是执行记录权威，未另存一份消息库。
- **未确认与恢复**：派发前保存的提交意图在接纳响应故障/重启后保持 unknown；有会话映射的 submitting 恢复为 unknown，没有映射则按“保存会话映射先于派发”的既有顺序视为未派发。自动历史刷新不清除该意图，不自动重发。用户明确 Reload 且成功读到闲置会话与历史后，才保存解除本地提交保护；保留资料并提示先检查记录再手动发送或移除。这不证明不确定消息已经送达/未送达，不按正文匹配猜 task。保存解除状态失败仍保留意图。
- **验证范围**：`task_context_integration_test.go`通过实际GUI→Rust→本地受控provider及Runtime历史核对单次输入、移除、文件快照、事项/对话归属、重启和只读资料复制；stdio故障代理在真实Rust接纳后将回复替换为协议错误并屏蔽该连接事件，证明未知恢复与不自动重提，不冒称真实网络超时/磁盘故障。容量、派发前保存失败与受控接纳回调中的后续编辑分别覆盖。精确最终组合SHA、全包结果、原始失败与CI见PR170/issue117交付证据。明暗720/1040的对话、事项编辑、执行资料预览均为headless页面，非macOS原生通过。
- **边界**：没有PDF/OCR/Office、扫描目录、索引、云附件或第二个执行循环。#142补充会消费同一资料快照与发送投影，其自动递交状态机不属于本单。原生文件选择器/IME/系统剪贴板行为仍须原生证据；headless截图和注入剪贴板不替代。


## #142 事项补充与自动继续（候选实现，2026-10-26）

代码来源：在本分支 `agent/gui/142-issue-supplements` 的 #117/#115/#153 组合上增量实现；`04e2caa` 为首片，后续 UI/恢复/错误边界与验证 SHA 以本单最终交付为准。本节只描述实际落地的补充 consumption，不表示完整 MVP、原生 CUA 或长期 QA 已通过。

- **数据与原子派发**：`productIssue.Supplements` 持久化 receipt、batch 和自动意图；store v6 读 v1-v5，保留既有 Context/pendingSend/旧 execution 身份。新 `issueExecution` 记录 `RequirementsRevision`、不可变 Context 和 `SupplementBatchID`，batch 记录 `ExecutionID`。`PrepareDispatch` 在唯一 dispatch-before-send save 中同时写入 execution、dispatching batch、snapshot items 与 Work submitting；save 失败回滚未发出的 association，不调用 Runtime。
- **单一输入**：普通 Run 与自动继续共用 `issueRunCaptured`/`runIssueFor` 的 captured 准备路径；`Prompt` 由当前 Goal/Requirements + 本批补充文本组成，Context 只走既有 `sendTask` 的可选参数，不拼进 system/正文，不复制聊天记录。每条 receipt 保留收到时的 `RequirementsRevision`；当前 batch target 是同一 contact/issue 的当前 revision，`pendingFor` 只取 `<= current`。同 issue 的 dispatching/unknown batch 跨 revision 阻塞新批次。
- **自动边界**：已保存补充在已授权（至少一次 execution）的事项上由 Add/真实 terminal/容量/恢复边界做一次单次尝试；未运行事项必须显式 Start。没有 timer、第二 scheduler/queue/history parser；容量/busy 时保留 pending 和自动意图，确认拒绝/unknown/保存失败关闭自动并保留文字与真实 IDs。Stop/Cancel/waiting/failure 撤销未来自动意图，late accepted 仍记录关联，不重发。
- **接受与回执**：accepted 绑定 session/task/message/execution 和 batch；同一 save 清 exact-ID+Revision 的 Context，之后才处理 buffered terminal/自动下一批。`Accept` 保存失败保留 accepted receipt/真实 IDs、automatic off，不回 pending/not_dispatched。UI 只显示“submitted to runtime execution”，不把 receipt 说成模型采用/要求满足。
- **恢复与 UI**：重启把 dispatching/unknown ledger 标记为需核对并关闭自动；不自动重派。`Check execution` 先更新真实 execution/task 状态；仅凭 task.status 不把 batch 冒充完整输入已核对，只有可证明完整 Prompt+Context semantic history 时才收敛 receipt。用户“确认未派发”在同一 save 中收敛 execution、ledger batch 和 items；拒绝/未派发批次保留 snapshot，可由明确 Retry 把原文字放回待发并显式 Continue 一次；unknown/accepted 不走该 retry。事项卡新增 target revision、已保存/待递交/等待容量/递交到执行/unknown 列表、全文预览/复制、旧批次展开、Stop/Resume auto 与伴随说明；普通聊天不自动归入事项。不再保留无生产窗口的 prepared/ReleasePrepared 双路径；admission 只使用 PrepareDispatch。
- **验证边界**：真实 Runtime 测试覆盖初始 A 显式 Run 后在途 B+C、revision 变化、同 contact 对话与独立 contact Y 隔离、A terminal 后唯一自动后续；Stop/Cancel pending/auto、显式 Resume 一次、waiting 答案、真实 failed、Cancel 前延迟 accept 的真实 IDs、lost task.send 回包→unknown→restart 不重派、实际 Runtime history 的完整 Prompt+Context 匹配，以及 MyGo UI Tester 的 Save failure/确认未派发 save 失败/全文 preview+copy/owner draft 隔离/unknown Check 控件。preview 明暗 320/360/720/960/1040/1380 共 12 张，其中 320/360 是独立工作栏组件 layout，不是应用承诺支持 320 窗口。固定 Runtime `c738c633…`；历史 d4e 完整 race 的 320 PASS 是旧精确源，不是本最终 SHA 的计数。当前实现/受控测试与在线/原生分层记录；native/CUA 仍未验证。
- **Magpie 实际尝试（未通过）**：使用隔离 connection/store/session，Runtime effective config 实测 `model=deepseek/deepseek-flash`、`api=openai-chat-completions`、`endpoint=http://127.0.0.1:3425/v1`、`connection_id=default`；首轮只读 probeA，补充在首 execution 后加入。第一个真实 task 随即 `failed`，Runtime 错误为 `HTTP 400 invalid_request_error: DeepSeek: reasoning_content in the thinking mode must be passed back`。此时 pending=2、issue needs_attention、auto off、requirement unverified，未产生第二 task；验收 trace 保存在 `/private/tmp/saki-gui-142/magpie-acceptance/20261026-161209/trace.json`；该 trace 的 `raw_history` 不能作为原始 SSE/wire 证据，持久 session 的 `history.jsonl` 保留 actual Runtime shape（父已核）。这不是模型质量结论，且未再重试/换 provider。旧的 OpenCode 在线失败日志保留，旧 trace 未保留 raw tool 明细。
- **原失败结论**：上述 `c738c633…` 场景首 task failed 时，GUI 保持 needs_attention、auto off 和 pending；该次完整真实模型两 task 验收未通过，原 400 现场不被后续结果覆盖。Runtime 后续在 [#177](https://github.com/HeimaoLST/Saki/issues/177) 交付 `addb4e0`；其组合与 GUI 消费证据如下，不把单项 Runtime 修复等同 GUI 全流程通过。
- **修复后同场景事实（作者报告与本地证据核对，2026-10-26）**：固定 GUI `15234eabaeb35a76c69eeb5f45372eccac204611` 与完整 Runtime 组合 `e7511d95922ed326a05c80aabf42c749adf2ef7b`，helper SHA256 为 `79eef3082bac2a04abbaa610a6a3d679ef772991999ef99f4d96266828edf065`，保留 #160/#146/#167。沿同一 Magpie `deepseek/deepseek-flash`、Chat `/v1` 连接，仅开放真实 read。首次 Run 后两条在途补充自动合为 **1 batch / 2 items**，没有第二次人工 Run/Continue；同场景退出 drain 后的 Runtime journal 和 GUI store 分别记录两 task completed、首 task read A/第二 task read A+B、Context 数量 1/0、pending 0、没有第三 execution。事项仍为 in_progress，要求仍 unverified；任务完成不等于产品事项完成。
- **在线证据边界**：原临时 `TestIssueSupplementMagpie177Acceptance` 命令仍 **FAIL**（测试 6.15s、包 6.751s）：observer 提前以 unknown/中间状态结束观察，且错误要求两 batch。pre-drain trace 和 FAIL 原文保留；post-drain 是同次现场的只读核验，没有新请求或重跑，不能改写成自动化 PASS，也未证明退出前稳定终态、原生窗口、IME 或完整 MVP 通过。正式来源为 [#177 消费记录](https://github.com/HeimaoLST/Saki/issues/177#issuecomment-6413113586) 与 [#156 组合回执](https://github.com/HeimaoLST/Saki/issues/156#issuecomment-6413155978)。本地原场景 `/private/tmp/saki-gui-142-online-177/20261026-174135/` 的 `postrun-report.txt`、trace/store/journal 保留；reader 核对三份材料 hash 与该交付一致，不把作者报告扩称为本线重新执行的在线验收。后续正式在线 observer 须等待权威终态与 GUI 稳定投影，不凭此先判定生产缺陷或盲目重试付费模型。

## #152 联系人管理与已保存对话（候选实现，2026-10-26）

代码来源：`agent/gui/152-contact-management`，普通合入 #142 固定 `15234ea`，首个实际接线提交 `ff51594`，后续修正和最终验证 SHA 由 PR #171 记录。本节描述真实 app 路径；早期 profile 独立组件不作为完整交付证据。

- **保存与生效范围**：联系人 ID 稳定；名称保存成功后立即用于界面，Purpose/Instructions/Workspace 保存为未来新执行上下文的默认值。Instructions 原文保留空白与换行。现有对话和已有事项工作会话不热更新；已开始的 create/send 使用开始时捕获的配置。活动工作中仍可保存未来默认值。Name 不可为空；非空 Workspace 必须是存在的绝对目录，空值表示使用已应用的宿主默认目录。表单错误保留输入，磁盘保存失败回滚联系人与 store 内存；非 demo 且 store 不可用时不报告保存成功。
- **新对话**：`Save and start new conversation` 先原子保存 profile 和旧 `SessionID` 引用，清空当前对话绑定；下次正常 Send 才沿已有 `configuredCreate`/`sendTask` 创建新会话，不在按钮点击时调用模型或宣称 Runtime 已接受配置。原联系人、事项及其 WorkSessionID/execution/要求版本、待发草稿与 Context 保留。后续 Runtime 拒绝新 cwd 时显示发送错误、保留待发输入和旧历史入口，用户修正后显式重试；不会自动返回旧会话执行，也不静默重发未知提交。
- **归档与恢复**：归档标记和时间随同一 contact store 保存，不删除会话文件、草稿、Context 或事项。对话/事项 submitting、active、unknown、未确认提交、待自动派发补充或所属模型更新在途时，阻止归档和新对话并解释原因。归档联系人可检索历史，禁止聊天发送及事项执行；恢复后沿原会话继续。归档/恢复失败保持原状态。列表按 Active/Archived/All 与名称/职责筛选；归档后当前联系人仍可见并可恢复。
- **历史读取**：保留原来当前对话的即时搜索，并提供 `Search saved contexts`。范围是该联系人的当前对话与显式保留的旧对话 ID，不扫描其他联系人或事项工作会话。查询通过 Runtime `session.history` 和既有 `parseHistory`，不解析 JSONL、不调用模型、不另建索引；结果按会话去重，最多 100 条，部分会话不可读与截断明确显示。每条命中携带会话 ID/EntryID，可读取完整消息和其原始 Context，查看不会修改当前执行绑定。异步结果绑定联系人、Runtime 实例及查询版本，迟到结果不能覆盖其他对象；退出等待读取回调完成后关闭 UI。
- **数据兼容**：store v7 新增 `archived`、`archived_at`、`previous_sessions`，读 v1-v6；保留 v6 的 Context、pendingSend、要求版本、execution 与 supplement ledger 关联。只保存旧会话引用，不复制消息正文。升级后旧 v6 应用会拒绝 v7，防止保存时丢字段；降级使用升级前备份或隔离的数据目录，不能把新 store 直接交旧包写入。Runtime 协议无变更，#117 exact-ID/revision 清理、#142 单一补充派发路径及 #173 停止回归继续保留。
- **验证与限制**：实际 GUI action + 固定 Rust helper 的受控 provider 测试覆盖保存后现有请求快照、新对话/cwd、活动事项保护、旧对话和 Context 定位、不同联系人隔离、重启、归档恢复、保存失败回滚，以及 Runtime 拒绝新目录后的明确重试。组件/应用截图属于 headless；不代表原生 IME/VoiceOver 或线上模型兼容通过。本单测试使用 #160 固定 helper `c738c633…`，没有付费模型重试；#177 的 Runtime 修复交付与 #142 GUI 在线补充复验另记，不能把 Runtime 受控成功当作 GUI 全流程通过。最终测试计数与 CI 以精确候选的交付记录为准。

## #155 执行成果与已报告文件（候选实现，2026-10-26）

代码来源：`agent/gui/155-issue-results`，独立投影/阅读组件为 `929deba`、`1b49693`；普通 merge #152 固定 `00cb5fd` 后，实际页面首片为 `9b222d9`。本节随该候选更新，最终 SHA、完整测试与集成状态以 PR #174 为准，不把阶段代码或本地测试扩称为完整 MVP。

- **阅读入口与归属**：事项每次执行提供 `Open result`，完整阅读页与原 `View execution` 工具活动入口分开。页面由持久化 execution/session/task/message 身份读取 Runtime history；每页响应的 session header 必须匹配。`Reload result` 只重读同一执行的已记录输出，不发送任务；读取失败仍保留此前可读结果和重试入口。页面是已记录内容的快照，不另建 live 投影。不同执行的成果不会被最后一条全局消息覆盖，查看不会改变事项完成状态。完整消息记录仍在 Runtime，GUI 不新增结果库、文件索引或持久化正文缓存；store 继续沿用 #152 的 v7。
- **文件来源**：仅该次执行中已完成且成功的 `write`/`edit` 工具结果形成 reported-file 行；保留 call ID 和原始结果。模型正文中的任意路径、`read`/`bash` 输出、失败/中断的工具记录不构成已生成文件证明；相同路径的不同 call/执行仍可分别核对。工作目录读取对应 Runtime 会话的真实配置/metadata，不从当前可编辑联系人 profile 推断。没有权威 diff 时不伪造 diff。
- **内容与复制**：复用 #113 的 Markdown reader，提供完整结果、代码、精确文件路径、当前文件内容及工具原始记录复制；错误反馈沿用同一 reader。默认界面突出结果和文件，session/task/message/call 等来源细节按需展开。历史工具结果与文件当前内容分开：当前文件可能在执行后被外部修改，不声称保存了执行时文件快照。
- **当前文件与错误**：文件检查在用户查看/重新检查时读取，UTF-8 文本预览上限为 256KiB；超限不静默截断，空文件可复制空内容。缺失/移动、非普通文件、读取/格式失败保持具体说明、原路径和可用动作，不搜索替代文件或自动重新生成。系统打开/定位前再检查实际路径；两个动作分别调用对应系统入口，系统请求被接受不等于已观察到窗口显示成功。Finder 定位使用 macOS `open -R` 的可返回错误路径。重新检查/再次操作清除相应过期错误，不让成功恢复后的文件继续显示旧的操作失败。
- **异步边界**：渲染不读文件或调用 Runtime；读取经既有 UI 队列回投，并检查页面、owner、helper 和请求代次。连续检查同一文件只应用最新请求；切换联系人或关闭页面后的迟到读取不能覆盖新页面或重新打开旧页面。退出沿统一收尾等待本线读取回调，不能在 UI 已关闭后投递。
- **验证与限制**：`issue_results_app_integration_test.go` 同时覆盖真实历史恢复页和应用 `runIssue`→Rust write→结果页、Continue→edit→另一执行/重启的生产关联；首尾空白原文、完整复制、实际 OS 错误消费与过期错误清理、Open/Reveal 不交叉调用、读取不增加模型请求均有回归。受控 barrier 验证连续检查与重载期间迟到检查的收尾。新的明暗 720/1040/1280 页面覆盖 loading、空结果、缺失文件、来源展开、长路径和多个 call；最终完整组合结果以精确候选交付记录为准。受控 HTTP、headless 控件与注入系统动作不证明任意在线 provider、macOS 原生 Finder/剪贴板/IME 或完整 MVP 通过；不把 #142 原自动验收 FAIL 改写成成功。本单没有 PDF/OCR/Office、通用文件管理器或另一 Agent loop。

## #154 对话内事项工具（实现事实，2026-10-27）

代码来源：本分支 `agent/gui/154-conversation-issue-tools`，普通合入 #155 冻结候选后 base `4dc6388`；最终源固定 `736880ba6c39a0ea4fd74ab0651145f438203788`（资料关联、工作流指导、scope-ref wire、typed-refresh、关闭 drain 已合并）。本节只记录本片已接线的 query/create/run、wait/question_ref、回答续跑、资料关联、scope 投影与持久化边界，不表示完整 154 或完整 MVP 已完成；最终 SHA 与完整证据以交付记录为准。

- **同 binary helper 与 HostConfig**：GUI 启动时在 Runtime 之前创建单一 Unix socket，并把 `runtimeToolSource` 写入既有 HostConfig `tool_sources`；source 显式 `command=<当前 Go binary>`、`args=["--issue-tool-helper"]`、`env_from=["NAGI_ISSUE_TOOL_SOCKET"]`、`protocol="legacy"`。Runtime 环境携带该 socket；helper 进程只运行 MCP 2025-11-25 stdio 循环并通过既有 IPC 转发，不启动 GUI、窗口或第二个 store。未设置 `max_tool_processes`，沿用 Runtime 默认，不另立配额策略。connection probe 不选择 external 工具；conversation 只选择 issue_list/issue_get/issue_action（保留原 builtin），work 只选择窄 issue_wait（保留原 builtin），不能递归委派事项工具。
- **会话与 scope**：managed conversation 新 session 显式选择 four builtins 与 `issue_list`/`issue_get`/`issue_action` external tools。已有 schema 2 session 在发送前用既有 `session.config.get/update`（expected revision）只补/核对 `tool_selection` 与缺失的 role prompt；若两者已 exact，普通 restart 不发送 no-op config update，config revision 不变。只有 Runtime 明确返回 typed `tool_schema_changed` 时设置下一次显式重试的 refresh 事实；refresh 成功清除，native continuation 拒绝时保留 SendError/草稿，不自动重试或迁移。显式空 builtin 保留为空，已有 external alias 保留，新增 canonical binding 冲突才失败；不改 model/connection/cwd/compaction。schema 1/legacy 既有 session 的启用仍未验证，不盲迁移。app 在每次 conversation `task.send` 前生成 captured contact/conversation/turn scope，存入同一 contact store，并只把该 scope 作为独立 caller-context 注入既有 sendTask；scope ref 随 pendingSend 关联，accepted 后绑定真实 task ID。scope 不是永久授权：新 turn、换 conversation、归档或终态后的旧 scope 只能读回既有 operation receipt，不能发新动作；旧 scope 的只读 query 仍按 captured owner/allowed IDs 与真实 history 归属返回。同 turn 工具 create 的新 issue 加入该 scope 后才能继续 run。
- **对话资料关联**：工具 create 只接受当前 app-issued scope/turn 的已捕获资料：优先匹配 `pendingSend` 的 exact scope snapshot，否则按 `scope.RuntimeTaskID` 匹配 `msgUser.Context`；同时要求 contact/scope RuntimeSession 一致，不读取当前 `ct.Context`、其他联系人草稿或整段聊天。捕获值经现有 `validateTaskContext` 后写入新 `productIssue.Context`，create 的 receipt 同一 save 落盘；后续 `Run` 继续沿 `issue.Context -> execution.Context -> sendTask` 单一路径提交，不把资料拼进 Goal/Requirements，不另建材料库。create 与同 turn 后续补充按 item ID+Revision 去重；未运行事项不会自动首次 Run。补充和 question_ref 回答也在既有 `addIssueSupplementBefore`/`AddWith` 的同一 save 回调中关联本轮快照，保存失败恢复 issue Context/question/receipt，不广播到其他 issue。
- **默认工作流指导**：managed conversation 的应用层 system prompt 由简短 role guidance 与用户 Purpose/Instructions 组合；空 profile 也包含 guidance。新会话在 create 时使用组合 prompt；已有 idle schema2 会话按当前 effective `system_prompt` 升级已知应用旧 conversation guidance 前缀，并只补缺失 role；旧前缀之后的用户原文、当前 contact profile 与显式 builtin/alias/tool selection 保留，不重绑、不替换会话、不改 model/cwd/compaction；用户主动 settings 更新仍走既有 profile 路径。work session 另有窄 `issue_wait` guidance。guidance 不按关键词分支、不新增分类器、队列或执行循环。
- **重启无 no-op 配置更新**：restart 后若既存会话的 tool_selection 与 role/system prompt 已 exact，`issueToolEnsure*Session` 不再发送同值 `session.config.update`，普通 restart 的 config revision 不变。marker 只表示 Runtime 明确返回 typed `tool_schema_changed` 后的“下一次显式重试需要刷新”，不是第二份 tool schema/ready 缓存；刷新成功清除，刷新被 native continuation 拒绝时保留明确 SendError 与原草稿，不自动重试、不迁移、不重放。此前同值更新会被 Runtime 以 `continuation_not_supported: native continuation cannot be reinterpreted by connection, prompt or tool changes` 拒绝，且 GUI 只保留 SendError/草稿、不产生新 task。离线 exact-attempt store 复现先得到该 RED；`TestIssueToolEnsureSkipsNoOpConfigUpdate` 固定无 revision 变化，`TestIssueToolSchemaRefreshRefusalKeepsDraft` 覆盖 typed 刷新被拒后保留草稿，`TestIssueToolTypedErrorOnlyMarksSchemaRefresh` 覆盖仅 typed 错误设置刷新事实。
- **动作投影**：UI `createIssue` 与工具 create 共用 `createIssueForWithSave`；UI 在同一个真实 store save 中写新事项和 `SelectedIssueID`，失败恢复旧选择；工具路径只使用 captured contact，不改变当前选中联系人、表单或他人 draft。run 走 `issueRunCapturedWithOperation`→`startIssueTask`→真实 `acceptIssueTask`，只有 session/task/message/execution IDs 齐全才写 accepted；未接纳/保存失败写 unknown 并保留可读错误，不把 bool 启动当 accepted。stop 只调用 `cancelIssueFor` 停止当前执行，不调用会取消产品事项的 `cancelIssueProductFor`；回执最多 `stop_requested`，不冒称 Interrupted。
- **work 主动等待**：issue work session 只额外选择窄 `issue_wait` 工具；app 在工作 session 建立后、task.send 前生成 work mode scope（contact+issue+execution+通用 RuntimeSessionID；旧字段只读兼容；真实 task 未知时先留空，accept 后绑定）。issue_wait 的 expected revision 由 captured execution.RequirementsRevision 派生，真实 revision>1 fixture 不旁路填参；stale 仅返回 revision_mismatch/current_revision，后续有界失败路径另做，不伪装 needs_attention/成功保存。，随既有 caller-context 进入同一 sendTask。工具只允许 wait，不能 create/run/委派；conversation 仍用原有 issue tools。wait 以同一 `markIssueWaitingForQuestion` 原子保存 question/waiting/auto/receipt，随后用现有 `task.cancel` 暂停该真实 session/task；只在真正 TaskFinished/Interrupted 后保留 execution 终态。等待不占用聊天/补充 queue；回答仍走同一 142 ledger，真实终态/容量边界后唯一继续。工作 session 任务结束并完成 history 投影后复用 `session.release` 释放 MCP 进程；只有真实 idle、无 submitting/pending 才 release，ReleasePending/Releasing 阻止新 Run，release UI 回调计入既有 shutdown drain，下次真正 Run 前按 released 标记 load；不持久改 tool_selection。
- **内部 scope 与用户资料**：app-issued scope 在 wire 上是最后一个 caller-context；模型可见的 `nagi.issue_tools.scope` text 现在只是唯一 opaque `scope.Ref` 字符串，工具 `scope` 参数也使用同一 ref。完整 `IssueToolScope` 仍只在 app map/store 中用于授权、owner/session/turn 来源核对，不作为模型参数暴露。GUI history 只过滤 exact trailing persisted scope 的不可变发行身份（ref/contact/session/turn/created time 匹配；allowed IDs 与 RuntimeTaskID 会随 create/accept 变化，不参与原文匹配），并同时识别旧 JSON wire 和新 token；不按名称或 JSON 形态全局删用户材料，未知 token/同名用户资料与 raw Runtime history 保留。异步 contact-history 读取在 UI 先捕获不可变 scope 索引，不读活动 app map。发送前按实际 wire 检查用户资料 + scope 的 1 MiB 预算，超限在 task.send 前可修正失败并保留草稿/资料。issue execution 持久 `ToolScopeRef/ToolScopeInput`；新 execution 保存实际发出的 ref/token，旧 execution 的 JSON wire 不重算，142 丢回包历史核对按对应旧/新 wire 比较。
- **查询与成果**：`issue_list` 直接读既有 `productIssue` 事实；`issue_get` 用既有 execution/requirements 与 typed `historyAllForSession`/`deriveIssueResultFiles` 形成有界结果（文件数、call/path、UTF-8 安全截断正文），不打开 `Open result` 页面、不切 `a.aux`、不读 JSONL 或伪造 diff。history 读取后再次核对 captured contact/issue/execution、同 Runtime helper/generation 与 session/task 身份；迟到/换 owner 的结果丢弃。list 的 `cursor` 按既有 next_cursor 消费；Runtime execution completed 不变成产品事项 completed。查询/回执写 4MiB frame 上限内的短定位 ID；查询响应采用 16 KiB 模型级上限（不是 50 KiB/4 MiB transport 上限），预算覆盖完整 tool response JSON；长 goal/requirement/evidence/detail/excerpt UTF-8 截断，excerpt 在 requirements/executions/files 缩减后仍超预算时先缩/省略并标记 result_excerpt_has_more；文件路径不截断。list 超预算时按真实 cursor/has_more 减少条目，detail 减少 requirements/executions/files 并标记 has_more/计数。
- **缺信息问答**：`issue_action` 增加 `wait`/`resume`；wait 需要 owner/captured scope、issue `expected_revision` 与非空问题文本，保存 `issueQuestion`（ref、原文、要求版本、真实 SourceTaskID、SourceScopeRef/TurnID、open/superseded 状态）。UI Mark waiting 与工具 wait 共用 `markIssueWaitingForQuestion` 一次业务保存：先验证 active/unresolved，再同一次 persist 写 waiting 状态、AutoContinue、旧问题 supersede/新问题与 operation receipt；save 失败恢复旧 question 对象/status/AutoContinue/receipt。同一问题重复返回同 ref；requirements/文本变化保留旧问题为 superseded 后建新问题。query 显示当前开放问题、ref 与 revision。resume 复用既有 `resumeIssueFor`；有结构化开放问题时必须先回答，普通无 question 的旧手动等待/显式 Resume 保留；Continue/run 也由 `issueLifecycleStartBlocked` 拒绝绕过开放问题。answer 通过 `add_supplement` 的 `question_ref` 校验 issue/开放问题/revision，调用 #142 唯一 ledger 的 `AddWith` 经既有 `addIssueSupplementBefore` 在同一 save 写 supplement、question answered 关联和真实 execution 后的自动继续意图；本轮附件与 create/add_supplement 一样按 captured turn 关联当前 issue Context，不另起输入路径；未运行事项不会因回答首次 Run。answered 答案直接从 ledger `AnswerSupplementID` 记录比较，exact 重复返回旧 SupplementID，不同答案 conflict，ledger 记录缺失返回 unknown；错误 ref 返回候选，stale revision 明确拒绝。工作区显示等待问题原文。
- **同 store 持久化**：store v9 在 v8 上增量保存 `questions`（含来源 scope/turn、答案 supplement ID）与 execution wait/tool-scope 关联；v8 已有 `issue_tool_operations`、`issue_tool_scopes`、pendingSend `tool_scope_ref`、execution `tool_operation_id` 与 contact `selected_issue_id`，继续读 v1-v8；旧 v8 应用拒绝 v9，避免保存时丢字段。operation 先 Reserve 再执行副作用；重复同 turn/action/target/payload 返回原 receipt，异 payload 冲突；无 receipt 的旧 operation 只返回 unknown，不盲目重放。reserve 与 actual action 在同一 UI 序列继续核对 captured scope；若中间归档/换 owner，已持 operation 保留真实 unknown/receipt，不重放或删账。create/update/add 的 saved receipt 与 run/stop 的 accepted/stop_requested 都投影既有事实，不另建业务状态机。
- **退出**：IPC handler 回到同一 UI 执行既有动作，只等保存/实际接纳或 unknown，不等 work task 终态；`beginShutdown` 后 active scope 拒绝新的 issue-tool 动作，不 Reserve 或写业务 store，已有 receipt/已进入的 accept/terminal 回调仍完整 drain。restore/reload contact/issue history 的异步读取计入既有 `historyAsyncStarted/Finished`；成功、stale、error 和 read-only 回调都归还计数后才允许 `finishShutdown` 启动 Runtime shutdown，shutdown 完成回调再 requestQuit。Runtime shutdown 完成、UI 回调收尾后，IPC 阻塞 Close 在后台执行，UI 线程不等待 Close，也不丢已进回调。result/history/model/config 既有退出 guard 保留。
- **已验证事实（最终固定源）**：GUI `736880ba6c39a0ea4fd74ab0651145f438203788` 搭配 Runtime `e751` 组合/固定 binary `dfdc5358ca107827b11bc21066f6cd7d683cd7bf036714dfea37eaf2b34da75b`。父级完整 `go test -race` 为 **475 named + 1 package PASS / 0 FAIL / 1 onlineSkip**，181.508s；88 目标 PASS，4 个 required CI 全绿（精确 run 40387975214）。受控 local/MCP/IPC 测试覆盖 create/get/run、A/B 隔离、wait/answer、scope token/旧 JSON filter、history reload shutdown、typed refresh 与 no-op restart；`TestIssueToolConversationRealChainQueryCreateRun`、`TestIssueToolScopeTokenRealChain`、`TestIssueToolAnswerQuestionCarriesAttachmentThroughRealChain`、`TestIssueToolEnsureSkipsNoOpConfigUpdate`、`TestIssueToolSchemaRefreshRefusalKeepsDraft`、`TestHistoryReloadCallbackFinishesBeforeShutdownQuit` 均在最终目标中通过。原 19f 及更早 online FAIL、完整 panic 日志保留，不改写为 PASS。
- **最终在线试用（独立证据）**：隔离 store/session/workspace、仅 snapshot fixture，`deepseek/deepseek-flash` + Chat Completions + Magpie；observer 允许 24 上游、共享 180s，实际 **23 请求全部 200**、exit 0、36.344s。五阶段新 task：discussion `1793043578025997000-96774`、delegation `1793043579607681000-96774`、work `1793043583314141000-96774`、query `1793043597185680000-96774`、restart `1793043605750879000-97545`；restart task 不同于前一查询 task，事项仍为 1、execution 仍为 1，无重派。结果文件 `result.txt` 保留青岚/林/result.txt 事实。旧 19f/1012 等 FAIL 原样保留。
- **未完成边界**：schema 1/legacy 既有 session、容量下问答自动继续的独立矩阵、多个 work session 的 tool-process release/load 压力、完整动作成熟度与完整 MVP 仍未完成；native/CUA 未验，不把 headless/Go 结果扩称为原生验收。

- **#200 定向真实链回归（2026-10-29）**：新增测试走真实 conversation → same-binary MCP helper → Unix IPC → app → Rust `task.cancel` 链路；A 的 stop 以 Runtime 实际 `interrupted` 终态和 GUI execution 同步为准，不只接受 `stop_requested`/stopping。下一 conversation turn 以合法 `run` 继续同一事项，产生新的 execution/task 并实际 `completed`；本用例 Stop 后事项仍 `in_progress`，`resume` 用于 waiting/needs_attention，不能替代这里创建下一执行的 `run`。界面保持选中已完成执行的 B，B 的 goal、requirements、非空 execution/session/task/message 与内容不变。同一 scope/turn 内重复 stop/run 工具往返返回原 receipt，不新增 operation、execution、Runtime task 或 provider work 请求。精确结果见 [#200](https://github.com/HeimaoLST/Saki/issues/200)。证据来自受控本地 provider、headless/Go 链路与真实 Rust candidate，不是 native、真实 IME 或在线 provider 验收。默认 test helper 构建统一经 `scripts/gui-go.py` 的 patched build；显式 `SAKI_TEST_ISSUE_TOOL_HELPER` 覆盖仍支持；RED/GREEN 范围见 #200。

- **#207 委派后不重复执行（机制与边界，2026-10-30）**：6339 原生首用证据中，conversation 已通过 `issue_action:run` accepted 独立事项，issue work 先写 `plan.md` 6090B（SHA `4b8225…`），conversation 随后又写同路径 5544B（SHA `c5d23a…`）覆盖；当前文件等于 conversation 版本，而 issue 仍 `in_progress`、7 条 requirements unverified。该原始失败保留，不把两个 completed task、HTTP 200 或文件存在当作验收通过。conversation role guidance 现在明确：委派后 issue work 拥有该工作，不在 conversation 重复执行或覆盖其文件；`accepted` 只是 started，不是 completed；用 `issue_get` 查询真实 progress/executions/requirements/results；issue 仍 running 时可以正常结束响应，不忙轮询，成果声明必须来自实际 `issue_get`；补充/继续/要求变化沿用同一 issue 路径。`issue_get`/`issue_action` 工具描述同步说明查询与 accepted 语义。已有 idle session 只替换精确的上一版 conversation guidance 前缀，保留用户 profile/custom prompt 与显式 tool selection；未变的 work guidance 不复制、不参与升级。以上是模型提示/tool description 的软约束，离线定向 race/vet 只证明 config readback、旧 guidance 替换、幂等与真实 receipt/query 接线，不证明任意模型一定遵守。049 的同源临时 harness 本次已做一次原样 Magpie DeepSeek Flash 复验：模型 create 后调用 wait 时缺少 expected_revision，返回 `invalid_action`，停在向用户澄清熟练度，未产生 run accepted、work execution 或 plan.md；5 次上游请求均 200；raw journal 的 task_finished 表明初回已在澄清后结束，是本轮 harness 继续等待未产生的 work terminal 才触发共享 180s 超时 FAIL，不能据此证明 overwrite 修复，也不能当作完成摘要或软约束硬保证。详见 [#207](https://github.com/HeimaoLST/Saki/issues/207) 与 [#207 comment 6513209612](https://github.com/HeimaoLST/Saki/issues/207#issuecomment-6513209612)。

- **#207 续测与 query-tail 证据（2026-10-30）**：首轮 049 原样复验保持 FAIL，5 次上游请求、无 run accepted/work/plan；首轮之后先有一次 0 请求的 setup RED（contact history 尚未 loaded 时 send 被拒，随后仅增加 restore 的 Loaded/HistoryDirty 准入，无产品改动）。真实补答阶段 9 次上游 200：同一 issue 的 `add_supplement` 返回 saved，store supplement ledger 为 submitted/accepted 并绑定唯一 execution `execution-bbe5b4528fa94e68`，execution Prompt 含 Python/7 自然日；work 最后写 `plan.md` 7434B（SHA `392dd08e…`），conversation 无 write/edit/bash 修改产物。该阶段 validator FAIL 是因为测试把模型整理后的 supplement 文本与用户原回答做逐字相等比较（源 `Text==answer` 误判），不是产品未补充；不覆盖原 FAIL JSON。随后 query-tail 仅发一次原文“刚才那件事完成了吗？请告诉我目前进度和成果在哪里。”，3 次上游 200，test PASS 7.37s/package PASS 8.887s；新工具仅 `issue_get` 与只读 bash，返回同一 issue/唯一 completed execution，最终 assistant 明确 Issue 仍 in_progress、6 条 requirements unverified；plan/notes hash 未变，旧 history 为字节前缀。总计 5+9+3=17 次上游。该证据是当前合成样本的一次有限真实链，不是重复写修复的硬保证，也不把 query-tail PASS 扩张为全量或 native 验收；详见 [#207 result 6513483964](https://github.com/HeimaoLST/Saki/issues/207#issuecomment-6513483964) 与 [PM review 6513478045](https://github.com/HeimaoLST/Saki/issues/207#issuecomment-6513478045)。

## #182 可读事项工作台与恢复边界（实现事实，2026-10-27）

代码来源：`agent/gui/182-readable-workspace`，从 QA 基线 `7a6b9b1` 开出；当前增量实现源 `ad8998930bc5dc55a543b2d1cc450dc9640953a1`（前序 `003df7a`、`7f37afc` 的完整矩阵证据是历史候选，不自动覆盖此增量）。本节只描述本轮工作台可读性修复，不代表 #156 全 MVP、原生包或在线模型验收通过。

- **工具卡**：工具摘要独占整行，Copy received output/Details 放进独立操作行；长中文、未换行路径和 JSON 不再把按钮推出卡片。结构化 `write`/`edit` 结果没有可读 content/stdout/error 等字段时只显示中性的“Structured output received; open Details for the exact record.”，原始记录仍只在 Copy/Details 中；错误/超时事实摘要继续可见。1040×720、960×640、720×640 drawer 和宽屏使用实际 `ui.Tester` bounds + 点击副作用验证，预览为 headless，不是 native。
- **事项详情**：默认顺序为事项目的/状态、当前开放问题、最新执行（`Latest execution`）、简短要求状态、继续/回答主动作；最新执行直接显示既有 `issueExecutionLabel`；pending/rejected/failed/unknown 无助手文本时显示简短原因，原始 Detail 仍在展开的执行详情中。latest 始终取最后 execution，不能用更早成功冒充；completed 无助手但有工具输出时引导查看执行/成果，不推断成功。`More issue actions` 才展开编辑要求/证据、任务上下文、补充、生命周期和完成表单；执行快照/工具原文默认收起。已完成执行详情和工具原文默认不进入右侧 Work 区；当前 active work 保留顶部 hero 与定向 Stop，completed execution 通过明确 `View execution` 打开，顶部有 `Back to issue` 回到 compact，不改变 `SelectedWorkTaskID` 的单一选择事实。浏览、展开和折叠不执行任务、不进行业务保存；切换联系人只沿既有 `selectContact` 路径保存选择并保持各 owner 的草稿隔离。
- **排版与操作层级**：直接相关的 workspace、事项/补充/任务上下文、联系人/聊天导航、设置、模型选择、结果面板/阅读器和 Markdown Copy/Details 使用同一套层级：正文、表单、结果和阅读正文不低于 14px；普通操作按钮 13px；时间、计数、来源等辅助信息优先用 12px 保持层次；这些直接相关视图不再出现 9/10/11px 正文。`secondaryButton` 只设置 13px、`MinHeight(32)` 和 padding，保留框架默认的可见背景/边框及 hover/pressed/focus；调用方不再叠加同值字体或 padding，也不用 `Background` 覆盖框架状态。可点击的过滤、模型/协议和主导航选中态统一用 accent 边框表达，不填充背景；Start/Continue 等主动作继续使用实色 `PrimaryButton`，More/结果/详情为次按钮，状态文本不画边框。
- **空间重排**：160px 联系人栏的 Add contact 独占一行、Active/Archived/All 纵向排列并全部可见；联系人标题和按钮分行。主 header 为 profile 状态行加导航行；窄宽度下 Contacts/Work/History、Conversation model、Settings 分行，避免被 Work 面板遮住或互相裁切。Issue list 使用可换行的两行 goal + 12px 状态，不再把长中文目标挤压成横向裁切；320px 下 Add contact、过滤、长事项条目和滚动后的行内操作均验证可点击。Markdown 的代码语言和 Table 辅助标签下限为 12px；代码块 Copy 与整条消息 Copy 在 720px 通过真实滚动/`Back to latest` 到达，保持全文 exact-source 断言，不放大窗口、不缩小控件。
- **证据与术语**：完成依据在展开详情中按原文阅读；重开后仍显示 `Previous completion evidence:`；缺依据只写 `Completion not verified.`。执行标签使用 `Execution completed/failed/interrupted`，不再默认展示 req r1/Runtime 完成等实现术语。空态说明 profile 可在 Settings 修改、新对话使用新 profile，不用欢迎语解释 session。
- **连接恢复呈现**：`connection_mismatch` 只在既有 `runtimeMessage` 的精确 code 前缀上识别；摘要为“Connection unavailable or changed”，提供既有 Connection settings 入口和 Details/Copy error details。未知 Load/Send 诊断独立保留，不被另一个 known 错误隐藏，也不自动重绑、重发或改变未知状态。
- **清理**：调用方直接使用既有 `toolTitleFor`，删除 `toolTitle` wrapper；移除纯转发连接 helper；`issueWork` 去掉未使用的第二返回值；测试仅使用 `issueStatusNotStarted`，保留 `normalize` 对旧 `open` 的迁移；冗余状态映射分支合并但保留状态语义。对应独立 cleanup commit 与行为测试保留。
- **验证**：历史 `7f37afc` 候选已完成完整 race 204.713s、exit 0，checker 18 MUST PASS，来源前后 clean/包 hash 不变；该计数不转给本增量。当前增量完成定向 UI/race 回归（含 1040×720 bounded scroll、latest pending/rejected/failed/unknown/tool-only、View/Back、completion evidence、known/unknown connection、rail/nav 320/960/1040 bounds、长中文 issue list、720px reader copy）；Go 全包最终由父在新 SHA 重跑。真实 Rust 离线链使用 `SAKI_TEST_RUNTIME_BIN=dfdc5358...`，未设置 `SAKI_TEST_ONLINE`，没有模型请求。headless 预览覆盖 light/dark、1040×720、960×640、720×640 drawer、320×900、360×800 和 1380×900；本增量另以 bounds/点击测试验证 320px 联系人栏、事项列表与滚动后的行内操作，并保留 720px Markdown exact-source copy 断言。最后原生图和最终包由父/PM 在冻结 SHA 重做，旧图不作为最终证据。原生窗口、IME、原生包仍由 PM／父验证，本单不新造打包流程。

<a id="adjustable-layout-values"></a>

## #184 可调三栏与全局布局偏好（实现事实，2026-10-28）

代码来源：`agent/gui/184-adjustable-layout`，从 `549ac98` 开出；本片源 `6b2f67ae8808f4430b1035b02caf3fa6a6d17f23`。本节只记录本片接线的布局与持久化事实，不代表原生拖动或最终包验收。

- **全局偏好与默认**：`storedState` v9 增加可选 `layout`（`left_width`、`right_width`、`workspace_open`），仍读旧 v1-v9；缺失时右栏关闭、左 200/右 340，不迁移消息、草稿、execution 或 issue-tool ledger，也不把布局放进 contact 或 Runtime。`newApp`、`newRuntimeApp`、`newUnavailableApp` 都从关闭状态开始；已有偏好由 `applyStoreSelection` 读回。
- **分栏与约束**：复用 MyGo `ui.Split` 的 first-width、1 DIP 分隔和键盘焦点；外层 rail/main、内层 main/work，均用 `OnChange` 在 bound width 更新后立即 clamp。左 180–280、右 280–440、三栏中心至少 480 且宽于两侧；1040 默认 200/498/340，1380 将剩余空间给中心。960/720 在默认偏好下自动使用既有右侧抽屉，Close/Escape/焦点恢复保留；左栏仍可收起。右侧没有使用 RTL Direction，main/work 子树保持正常文字方向。
- **交互与事件**：开/关右栏和显式 `View execution` 通过同一条设置入口；切换联系人、收消息、task start/finish 不改开合。右栏关闭时 Work 入口保留，并用既有 active work/issue notice/contact status 显示 `Work running` 或 `Work needs attention`，不生成聊天消息或伪完成。
- **保存边界**：显式开/关、正常 `beginShutdown` 和既有 `persist` 保存当前偏好；拖动只改内存，不在每次 mousemove 落盘。窄窗口的 overlay/有效宽度不覆盖保存的偏好宽度；宽度在读取时 clamp。
- **阶段记录（旧 f5/ab，非当前结论）**：`layout_test.go` 用真实 `ui.Tester` Press→Move（仍 held）→Release 验证两条分隔边、中心最小值和应用宽度上限，另测键盘物理方向、OnChange clamp、960/720 Close/Escape/focus、旧/新 store、显式开合重启、事件不强开、右栏关闭提示。`layout_preview_test.go` 生成明暗 1040/1380/960/720 的 closed/open/dragged 24 张 headless 图，位于 `/private/tmp/saki-gui-184/previews-final/`；其中 `dragged` 是直接赋宽度偏好的静态场景，不是实际拖动证据；真实 Press/Move/Release 和方向键只在 `layout_test.go`。早期 `go test ./...` 4.565s PASS、定向 race 2.631s PASS 保留为阶段记录。包源固定 `f5b154b`：GUI `477dd2d51514618ee407609f565c96b74313f9bd3bc8c7aa6b84bc248599a17e`、helper `d7a8755c3b7b245bf8b5228ed4c4e5995bb9102523cf22c1201c58c120a23ef5`；`f5b154b..ab390b0` 只增加三项测试修复与 docs，生产 Go 无差异。固定 `ab390b0` 的父级完整 race 为 **512 named PASS / 0 FAIL / 1 明确 `TestGUIOnlineSmoke` onlineSkip + 1 package PASS（220.722s）**；vet/checker exit 0，18 MUST 全 PASS；原 f5 三条 FAIL 日志保留，测试显式 Work 修复不变。固定源 review 见 [PR #185 comment 6441501965](https://github.com/HeimaoLST/Saki/pull/185#issuecomment-6441501965)。以上是旧阶段事实，不代表当前正式新源已完成最终无 trace 包。
- **原生 trace 与候选（2026-10-28）**：native trace 左 seq159–166、右 195–202 显示 PointerDown/Move/Up 全部发生在下一 frame 前且无位移，和 release-before-frame 批次结果一致。PM [comment 6459688039](https://github.com/HeimaoLST/Saki/issues/184#issuecomment-6459688039) 的候选左右同批/反向 native PASS 且保存 190/316；[comment 6459745772](https://github.com/HeimaoLST/Saki/issues/184#issuecomment-6459745772) 的重启 190/316、758 overlay/Esc/放大恢复、键盘 +10/−10 保存均 PASS，PID 正常退出。composer/editor 仍保持原行为；这些原生结论固定于 ef6 候选，正式构建的按 SHA 证据见 #184/PR185，不外推 composer 或完整 MVP。
- **受控依赖接入（Split only）**：`scripts/gui-go.py` 是当前 CI、macOS 打包脚本和本地 GUI Go 命令的唯一入口，仅允许 `test`/`vet`/`build`/`run`。它通过 `go list -m -json` 核实际 selected MyGo：`Version` 必须为固定 `fea932a7a2a9` 且不得有 `Replace`；通过 `go mod download -json` 返回的 `Dir` 复制到唯一临时目录，校验 `ui/split.go` 原 SHA，应用 `desktop/patches/split-release.patch`（Split 末位移 hunk + `ui/split_release_before_frame_test.go`），校验 patched SHA，再用 `go mod edit -modfile` 写入临时 replace 并在真实 `desktop` cwd 执行 Go，随后清理临时目录。它支持含空格 TMPDIR；prepare/patch 输出只进 stderr，`go test -json` 的 stdout 只透传 Go JSON；不重写 `desktop/go.mod`、module cache 或 VCS；patch 不匹配、patch 失败、post-SHA 不符或版本/Replace 不符均明确非零退出，不回退。
- **普通命令边界**：直接运行 `go test ./...` 或 `go build` 不会携带 Split patch；当前只通过上面的统一入口执行带 patch 的 GUI Go。CI 的 GUI vet/test/build/preview、Rust job 的真实 GUI 全包和 `scripts/build-macos-app.sh` 已统一走该入口；显式回归是 `python3 scripts/gui-go.py test github.com/egoist/mygo/ui -run '^TestSplitReleaseBeforeFrame$' -count=1`，没有新增 check 名。`desktop/patches/split-release.patch` 是 `.patch`，不会被 desktop 的普通 Go 测试编译。
- **范围与退出条件**：该 patch 只修 Split release-before-frame；不含 editor/composer、trace、第二布局或 vendoring。官方 release/main 同批问题记录仍保留；上游发布同批回归绿后应移除 `gui-go.py`/patch，恢复普通 Go 入口。普通 `gofmt` 仍直接使用。

## #96 Paper/Moss 外观、联系人 scope 与 composer 模型入口（实现事实，2026-10-28）

代码来源：`agent/gui/96-v07-appearance`，从 `33fc398` 开出；本节记录首个可运行切片，不把 #184 的正式包、原生窗口或 #186/#187 的未完成范围写成本单结果。

- **一套语义 resolver**：`desktop/theme.go` 的 `resolveWorkbenchTheme` 仍返回单一 `workbenchTheme`；Paper 从 `ui.LightTheme()`、Night 从 `ui.DarkTheme()` 起步（`Theme.Dark` 正确），再覆盖 v07 语义 token。保留 `paper`/`night` 身份、mascot、左右气泡；工作卡改为中性 `WorkSurface` 与轻边界，不再使用深绿 hero。普通联系人、事项和状态不复用按钮描边；控件保留 MyGo 默认按钮面，调用方不以 `Background` 覆盖导致 hover/pressed 早退。
- **排版**：消息正文 15px/1.8，普通操作 13px，辅助信息不低于 12px；标题用 16–19px 分层。消息 Markdown 的 15px/1.8 由 `view` 调用侧的 `markdownReaderOptions.Theme{FontSize:15, LineHeight:1.8}` 与根字号完成，未修改 `markdown.go` 或 reader 接口；中文长段落实际行带中心间距实测为 27px（15×1.8）。工具卡、Connection、Task context 与两个 Stop 入口保留。
- **联系人范围**：三个纵向 scope 按钮替换为一个 `ui.Select`，保留 `Active`/`Archived`/`All` 的值与筛选行为，以及 Add contact、完整 Contact profile、Save and start new conversation、Archive/Restore。Appearance 从联系人侧栏底部的应用级入口打开，不进入 Contact Settings。
- **模型入口**：Conversation model 从 header 移到 composer 旁，仍调用 `openSessionModel(ct.SessionID, "conversation", ...)`；首次未绑定明确显示连接已保存的默认模型，不声称立即生效。Issue model 仍在事项管理入口，两个目标不混用。
- **Appearance 存储**：`storedState` 的 v9 仅增加可选 `appearance` 字符串（`system`/`light`/`dark`），不新增第二个 store、preferences 文件或版本。缺字段的旧 store 解析为 `system`；联系人保存保留该字段。没有 store 的首次启动（未配置连接但当前 placeholder 联系人可保存）通过既有 `persist` 惰性写入同一 `contacts.json`，保留当前 contact ID、名称与草稿，不启动或连接 Runtime；store 不可读时仍走 `newUnavailableApp`，不覆盖损坏文件。
- **失败与恢复**：模式切换先立即生效；保存失败显示“Applied but not saved”并提供明确的 `Retry save`，不把页面回退到旧模式。重试与后续任何成功的 `persist`（包括联系人保存）走同一保存路径并清除该未保存提示；持久化错误只清除自己写入的 contact-save 错误，不覆盖无关的 Runtime 诊断。没有 Runtime 重建、任务取消/重发、草稿清空、选择或焦点迁移。
- **范围边界**：#184 的默认右侧关闭、拖动/键盘调宽、中心最宽、覆盖抽屉与持久宽度保持不变；本单不改 reader 文件（#187 独占）、事项概览/筛选（#186）、Runtime 协议、执行生命周期或 store 现有执行契约。
- **验证边界**：首轮 review set 在 `/private/tmp/saki-gui-96/previews-key/`（Paper/Night 1040 主面、Appearance、720 composer 共 6 张）；完整 16 张 headless 状态在 `/private/tmp/saki-gui-96/previews-first/`（Paper/Night × 1040/1380/960/720 normal，加 1040 working/attention/error/empty）。新增图片由既有 `TestPreviewImages` 入口生成，未增加独立 opt-in 测试。两者均为 `ui.Tester`，属 headless only；exact candidate tests/CI/native evidence 见 #96/PR；native/IME 尚未声称。

<a id="issue-navigation"></a>

## #186 事项概览、单事项详情与筛选（实现事实，2026-10-28）

代码来源：`agent/gui/186-issue-navigation`，从 `6101820` 开出。本节记录事项导航切片；#184 布局、#96 外观/Runtime 契约和 #187 reader 文件不在本切片的改写范围。

- **短生命周期状态**：每个 contact 持有自己的 `issueViewState`：`Filter`、`DetailID`、overview/detail 的 `ui.ScrollState`、返回行焦点和 conversation-work 局部视图。它不写入 store，不替代持久的 `contact.SelectedIssueID`；返回概览不清空 selected issue，也不改草稿、pending send 或 issue execution。
- **概览与筛选**：Work 打开默认是事项概览，只显示连续事项行（goal、真实近况、管理状态和进入箭头），不铺详情或六颗状态按钮。近况优先显示开放的等待问题；否则只取最新 execution。terminal/uncertain/pending 必须显示其 execution 标签，只有当前 accepted/running 的匹配 work 才可附工具摘要；旧 completed 执行不能遮住更新的 pending/unknown。单一 `ui.Select` 的映射为：Unfinished = 未归档且不是 completed/canceled（包含 waiting/attention）；Completed = 未归档 completed；All = 包含 canceled 与 archived。只改展示投影，不新增持久状态枚举；每 contact 保留筛选。
- **详情与返回**：点击行或显式 create/select/View execution 进入详情；`Back to issues` 只清短生命周期 `DetailID`，保留 `SelectedIssueID`、原筛选、overview scroll 和行焦点。行用真实 `ScrollState.TrackScroll`；返回时对仍属于当前筛选的行使用局部 `ScrollIntoView`/焦点；焦点目标取自当时显示的 `DetailID`，不取可能被后台/captured work 更新的 `SelectedIssueID`。筛选外的事项不会被自动改筛选或滚动去寻找。详情使用独立 scroll。
- **详情顺序与动作**：返回/当前 issue Stop/管理状态在目标标题之前；正在 stopping 时沿用 `Stopping issue…` 且按钮 disabled，不显示可再次 Stop；目标 20px/1.5；之后是真实问题、latest result、requirements/evidence、折叠的 More 与 execution history。Start/Continue 使用既有 lifecycle 条件和 dispatched snapshot；Edit、Issue model、Task context、Cancel、Archive/Restore、Reopen 和 Mark complete 仍在 More。Conversation work 有独立的局部入口，不成为事项，也不隐藏既有 conversation Stop/tools。
- **终态与完成依据**：Runtime execution completed 仍只是 execution fact；事项未完成且仍有 unverified/unmet requirement 时显示“This execution ended; the issue remains open.”。显式 Mark complete 仍保留既有运行/unknown 限制，显示 `Marked by user · <evidence>`，不把未核实 requirement 改成 satisfied。pending/unknown 的 Check execution/Confirm not dispatched/Reload 继续复用现有路径。
- **分层退出**：#187 reader 仍独立；当前 shared workspace 不改 reader files。aux Escape 先关闭 aux 并保留 detail/work；宽屏和窄窗 overlay 中 Escape 先从 execution 回 detail、再从 detail 回 overview，最后才关闭 work 抽屉；窄窗点击抽屉外 backdrop 仍是显式关闭，不触发 Back，保留 detail/filter/draft。Header `Close work` 仍是显式整体关闭，不改成返回。切换到其他 contact 时各自保留 detail/filter/scroll，主聊天阅读位置不被重置。
- **验证边界**：`issue_navigation_test.go` 覆盖默认概览、三种筛选、概览/详情/返回的 row+scroll+focus、contact 隔离、accepted task 不抢详情、Escape 分层、窄 work 面板动作行不溢出、completed execution 与手动完成差异；既有 issue/detail/recovery/Stop/archive/complete 回归保留。`issues_preview_test.go` 的既有 preview 入口生成 `issue-overview`/`issue-detail`/`issue-back` 等 headless 图，当前 review 图在 `/private/tmp/saki-gui-186/previews-key/`。headless 不是原生窗口或 IME 验收。


## #187 成果文件阅读与原上下文返回（候选实现，2026-10-28）

代码来源：`agent/gui/187-result-reader` 的独立 reader `28c24a4`，普通消费 #96/#186 固定 `205fa65`，接线 `9776d3e`；同时纳入 #186 的 tests-only `b512b64`。最终可运行候选、门禁与原生状态以 [PR #188](https://github.com/HeimaoLST/Saki/pull/188) 和 [#187](https://github.com/HeimaoLST/Saki/issues/187) 的精确 SHA 记录为准；此节不表示已集成 dev 或完整 MVP 通过。

- **入口与层级**：事项详情中最近一次结果使用单一 `Read latest result →` 行；执行记录仍可选择特定 run。结果页中的 reported-file 行本身是 `Read file →` 入口，不再并列重复 `View file` 按钮。文件行仍仅来自 #155 规定的真实成功 write/edit 记录，未从正文猜路径或建立第二份文件索引。没有已报告文件时保留真实结果与空态，不伪造文件、图表或成果。
- **文件阅读**：`.md` / `.markdown`（扩展名大小写不敏感）的 UTF-8 当前文件复用 #113 的 Markdown reader，支持中文段落、列表、代码及已有的换行基础表格；其他 UTF-8 文件按普通文本显示。表格沿用明确标识的 plain-text 单元格，不宣称实现未支持的嵌套富文本或图表。读取上限仍为 256 KiB；空文件可复制空字符串，过大内容不静默截断，非普通文件、非 UTF-8、失效路径和读取失败均保留说明与可用下一步，不自动搜索替代文件或重新生成。
- **阅读面与动作**：使用既有 `DialogBase` 提供局部较宽文件阅读面，最大宽度 760 逻辑 px；文件名为标题，报告路径只显示一处。详细工作目录和执行来源位于已有 `Source & raw record` 中。原文、单个代码块、Copy path / current file / tool record 的精确复制与成功/失败反馈，Check again、Open file、Show in Finder 均保留。读取的是当前磁盘内容，可能与执行结束时不同，不声称保存了执行时文件快照；系统请求接受与实际窗口打开成功仍分开验收。
- **上下文返回**（内层焦点返修候选，2026-10-28）：文件阅读 Escape / Close reading 只退到原文件行；结果页 Escape / Close results 回到打开它的最近结果行或特定 run 入口。259d799 的 PM 原生检查确认外层结果返回事项正常，但关闭内层文件阅读时焦点错误落到 `Reload result`；此前独立 panel 测试不能证明实际嵌套 Dialog 的焦点契约。返修在已有 result panel 内按 session/task/call 保存一次待返回的文件 key，关闭按钮与 Escape 汇入同一局部关闭路径，下一帧仅恢复原文件行焦点，不再次滚动或新增通用 modal 栈；原文件消失或既有 panel 重置时清除该局部状态。关闭结果的两个宿主路径统一经过 `closeIssueResult`，仅记录局部入口类别与待返回焦点，身份复用既有 reader page，不另建导航快照或通用弹层栈。#186 的 DetailID、筛选、DetailScroll、持久 SelectedIssueID、选中的执行、聊天草稿和 workspaceOpen 保持；窄窗返回重新显示同一工作抽屉。强制关闭、联系人或连接切换沿原取消/代次路径处理，不把旧阅读结果或返回焦点投到新联系人。
- **证据边界**：259d799 的原生已过/未过范围以 [PM 实际记录](https://github.com/HeimaoLST/Saki/issues/156#issuecomment-6467917618) 为准；内层文件焦点失败与单次 AX 关闭后的画面残留分开，不推定为同一根因。新增回归使用同一个持续存在的 Tester、真实 app.view 外层 aux Dialog 与内层文件 Dialog，并经真实 Rust write/history 生成文件；在 1040×720 长结果实际滚到文件行后，比较 Close / Escape 返回后的行位置、焦点和事项/执行/草稿。固定 259 源复现 `row=false/reload=true`，组件局部返修后通过；这仍是 headless RED/GREEN，等待 PM 用返修同源包原生复验。此前确定性 headless 控件与真实 Runtime 离线 fixture 覆盖 Markdown/普通文本、精确复制、错误、过期读回、跨执行恢复，以及 1040×720 / 720×720 下实际事项行、文件行、分层 Escape 和 run 入口返回。返回测试实际滚到非零详情位置，并核对原行可见/焦点、滚动、非空执行身份及草稿，而非只切模型宽度或检查文字存在。两色 720/1040/1280 截图是 headless 证据；最小 720×640、原生剪贴板、文件动作、焦点与新共同包仍由 PM 协调隔离窗口实际验收，不把旧 #156 原生图、旧主题或单元测试转记为新候选通过。原测试入口失败与修正后的运行记录保留，不通过恢复旧生产默认、跳过测试或删除精确内容断言获得通过。

<a id="input-selection-114"></a>

## #114 输入、拖选与焦点（实现事实与待验，2026-10-28）

代码来源：`agent/gui/114-input-selection`，从 `259d7994` 开出。本节只记录输入/选择收尾与 native 准备；不改 Runtime 执行、发送、草稿模型或其他业务线文件。

- **已定位的同一批次边界**：固定 MyGo 的编辑器把 pointer down 的 anchor/caret 立即设置，但同一批次里的 pointer move 只记录 drag 位移；`editor.drag` 仅在下一帧且 `state.pressed` 时运行，而 `pointerUp` 会先调用 `editor.release()`。同一 Down/Move/Up（以及 Down 后一帧再 Move/Up）因此可能只留下点击 caret。已在临时固定 MyGo 副本用单行/多行、正向/反向、跨行和分帧同坐标对照证明 RED；将最后位置在 release 前消费一次的 7 行 `ui/input.go` hunk 后，所有对照 PASS。该实验没有改写 module cache 或 `desktop/go.mod`。
- **正式单入口**：`desktop/patches/editor-release.patch` 使用相对 `a/ui/input.go` / `b/ui/input.go`，并带可发现的 `ui/editor_selection_test.go` 回归；`scripts/gui-go.py` 仍只接受 `test`/`vet`/`build`/`run`，先用 `go list -m -json` 核 selected `fea932` 且无 Replace，再用 `go mod download -json` 核固定目录；Split patch 原文件未改，Editor patch 另加，前后分别核 `ui/split.go` 和 `ui/input.go` 的生产 SHA；具名 editor 回归由 patch 非零失败和测试结果核验。临时 `-modfile`、JSON stdout、含空格 TMPDIR、patch 失败与 post-SHA 失败均保持显式非零；模块 cache 和真实源码不写。
- **应用层回归**：`desktop/input_test.go` 使用真实 1040×720 composer bounds，覆盖单/多行中文拖选的复制、正反向、拖选替换、CmdA/CmdC、Shift+Left、多行粘贴不发送及草稿/消息不变。公开 `ui.Tester` 的 Press/Move/Release 会逐步 settle，因此应用级同批次由固定 MyGo 的控件回归证明，不把测试器的分帧 PASS 当作同批次 PASS。
- **G4 现状**：G4-2 的 Shift+Enter、多行粘贴、Add contact 焦点和 Settings/History 焦点走已有测试；G4-3 的 A/B 草稿、拒收/保存失败草稿与重启路径沿用已有测试和 d7 helper；G4-4 现有 Enter、Escape、Tab、焦点恢复覆盖，完整键盘-only 联系人/发送/停止串联仍无单一回归。**G4-1 真实 macOS 中文 IME 尚未验证**。
- **IME 证据边界**：现有 `TestComposerCompositionDoesNotSendUntilCommit` 是 Compose→结束 composition→Type→Enter 的事件级回归，不能证明 active composition 的 Enter。`ui.Tester.Compose` 只注入 `TextComposition`，`Tester.Key` 直接注入按键；Darwin `internal/darwin/surface.go` 的 `keyDown` 在 `ime && hasMarkedText` 时不发送 KeyPressed，而走 `interpretKeyEvents`（`doCommandBySelector` 为空）。因此 synthetic active-composition Enter 可能发送，和真实 macOS IME 路径不同；本单不据此改发送逻辑，也不声称 IME 通过。真实 IME、原生窗口和剪贴板仍需 PM/原生隔离验收。
- **验证边界**：正式 Editor 回归在 patched MyGo 上 PASS，未 patch 的同源副本保留 RED；`scripts/gui-go.py test github.com/egoist/mygo/ui -run '^TestEditorSelectionReleaseBeforeFrame$'`、应用 `TestComposer*`、既有 G4/G5 helper 子集和普通 Go 包均通过。以上是 headless/event-level 证据；没有 native/CUA/系统 IME、没有在线模型调用，也不把旧包或旧图转记为当前候选通过。

- **历史44b限定原生证据**：PM [#114 comment 6468384981](https://github.com/HeimaoLST/Saki/issues/114#issuecomment-6468384981) 已验证冻结 `44b81c1` 的 1040×720 浅色真实双向/跨行准确复制及局部替换、A/B 草稿、同版退出恢复、Shift+Enter 不误发 PASS；真实 IME marked text、完整 Send/Stop 串联及所有宽度/主题仍未验，不能作为本组合的 native PASS。


## #199 独立设置页面（实现事实与待验，2026-10-29）

代码来源：`agent/gui/199-settings-page`，精确基线 `f3d8485c36f4345cfc52c6857ac4d35991009528`（PR #198）；按 [v08 固定规范](https://github.com/HeimaoLST/Saki/blob/074d9eeb9d3ef8f4b4c70ed82a27a3b03c39c6e7/docs/design/desktop-workbench.md#统一设置页面) 实施。此节替代本文早期 settings/configuration/appearance 浮层的入口说明；最终提交与验证证据以 #199 的交付 PR 为准，不表示已经集成 dev 或通过原生验收。

- **唯一页面**：工作台左下 Settings 进入独立应用内页面；仅渲染设置导航与内容，联系人列表、聊天、工作区不在视图或键盘顺序中。Appearance、Connection & model、Contact profile、Advanced 共用该页面；页头 Contact profile 与连接/启动错误入口定位对应分类。会话模型选择仍是作用于当前对话/事项的独立上下文操作，不替换成全局默认配置。1040/1380 使用约 200px 分类栏、最多 720px 正文；720 使用顶部可换行导航和单列内容。
- **草稿与联系人范围**：连接表单和每个联系人的资料编辑器在本次应用生命周期中保留。分类切换、返回工作台及再次进入不隐式保存或丢弃；Discard 只恢复当前有修改的表单，不清空聊天草稿或别人的资料草稿。联系人编辑对象独立于工作台选中对象，名称与选择器按同一内部 ID 对齐；界面不展示原始 ID，重名使用可读区分。保存、保存并新建对话、归档/恢复继续走既有持久化与活动工作约束，原会话快照和历史保留。
- **连接生命周期**：页面复用唯一连接表单与 `saveConnection` / `applyConnection` / `testConnection`，区分输入、已保存和当前生效值。保存不替换正在运行的 helper；Apply 使用已保存配置，活动或状态未知的工作仍阻止重载。每帧根据现有 `connectionReloadBlocked()` 刷新操作原因，工作结束后解除过时禁用。Test 仍需显式点击并使用当前表单值；忙碌与失败保留原有处理，已应用配置重开页面不会重新标为待应用。容量/执行限制可展开，字段校验错误会展开对应区域。
- **外观与诊断**：Appearance 沿用现有 System/Light/Dark 模式及持久化；预览与选择即时生效，不新增主题 ID。Advanced 使用原启动状态与路径事实；没有配置、尚未检查 helper 时不称缺失，只有实际探测失败才显示 not found。启动错误快捷入口展开诊断。首用没有 Runtime 时继续不显示容量警告与无效 Retry；已有 ready Runtime 的容量恢复入口保留。
- **返回与键盘**：Back to workbench 恢复原联系人、聊天草稿、工作区开合/宽度、事项与成果上下文；消息与详情阅读位置保留。Cmd+, 支持工作台及既有历史/成果阅读层；Escape 只处理临时控件，不把整页设置关闭。打开设置本身不创建会话、不重建 Runtime、不取消工作、不调用模型。
- **成果焦点返修**：PM 在 `ae4beb3` 的[原生验收](https://github.com/HeimaoLST/Saki/issues/199#issuecomment-6489027456)发现文件行经 Cmd+, / Back 后错误聚焦 Close results，Return 会关闭成果页；此前仅覆盖 Reload / Check again 的通过结果不覆盖此路径。返修沿用 `ui.Handle`，按 session/task/call 标识绑定动态文件行，并补齐自有复制、打开、定位与代码块复制按钮的焦点恢复；文件阅读层正常 Close/Escape 回原文件行的路径保留。固定 `4c93d37` 的这些自有控件已有 [PM 原生回执](https://github.com/HeimaoLST/Saki/issues/199#issuecomment-6489460712)；内联链接是该版本的剩余缺口，后续实现及验证边界见下节 #203，不能用旧控件通过替代链接验收。
- **验证边界**：本轮沿统一 `scripts/gui-go.py` 入口使用 headless UI 和本地受控 provider/真实 helper 验证页面、草稿隔离、真实 Save/Apply/Test、滚动与焦点；包含 720×640 / 1040×720 / 1380×900 的浅深色截图与控件边界检查。固定 SHA、最终完整测试、同源签名包及 CI 账户阻塞事实在 #199 交付记录，不把早期 f3 图片或局部检查转记为新包通过。原生窗口、系统输入法及用户试用仍由 PM 协调；#193 偶发退出问题继续独立记录，本功能不声称修复它。

## #203 设置返回原 Markdown 链接（实现事实与待验，2026-10-29）

代码来源：`agent/gui/203-markdown-link-focus`，直接基于 PR #201 的 `4c93d378bdb0fa3ec62b9d4740f7cf912204c532`。该基线的聊天、历史、执行成果和文件阅读中，第二个同址链接经 Cmd+, / Back 会落到设置或关闭按钮，PM 已保存[四处真实原生 RED](https://github.com/HeimaoLST/Saki/issues/203#issuecomment-6490007977)。以下为返修实现及 headless 证据，不表示已集成或原生通过。

- **精确身份**：复用 MyGo `FocusBind` / `FocusedValue`，按联系人、消息或历史阅读来源、事项/执行/文件、AST block/inline 位置及目的地址标识链接；同址链接、同一历史条目的摘要与完整阅读分别绑定。Cmd+, 在实际所属的根视图或 Dialog 捕获当前链接，Back 后 Return 只激活该位置的原 URL，不用 Copy message 或第一个同址链接代替。
- **一次返回与失效回退**：在工作台实际重建期间确认原目标是否存在，不按渲染次数或延时判断。原链接被删除或地址替换时清除请求；聊天回 composer，历史回 Close history，成果或文件回当前层已有关闭入口。原内容后来重现不再抢焦点。显式点击 Settings 使用原入口返回行为，不复用先前链接请求；普通关闭历史同时清除该阅读面的旧链接焦点。
- **唯一渲染路径**：沿固定 MyGo `v0.4.9-0.20261014025635-fea932a7a2a9` 的既有临时补丁流程增加可选 `Markdown.Options.LinkElement` 通知，在原 Link 构造、children 和 tooltip 后提供元素与 AST 位置，不替换 renderer 或打开回调。`scripts/gui-go.py` 检查 Markdown 补丁前后 SHA，继续检查 Split / Editor 补丁；真实 go.mod 与共享 module cache 不改。nil hook、OnLink、未允许地址及流式未完成链接的原行为有针对性键盘回归，未新增 anchor/router 的独立自动化证明。
- **证据边界**：持续存在的实际 app Tester 覆盖四阅读面的第二个同址链接、历史摘要/完整条目隔离、删除/替换后重现、显式 Settings 返回及旧自有控件。结果与文件测试从生产页面初始化接点建立，读取内容使用 fixture；定向 race 另含真实 helper 的嵌套文件返回回归。最终完整 helper / 18 MUST、同源包与原生复验按 [PM 组合安排](https://github.com/HeimaoLST/Saki/issues/203#issuecomment-6490132162)统一在 #200 接纳精确 #203 后进行，实际验证 SHA 与结果写入交付记录；本节不把阶段定向通过写成完整门禁通过。#193 和远端 Actions 账户阻塞保持独立记录。

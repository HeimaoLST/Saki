# Terminal UI：布局、滚动与视觉风格研究

核对日期：2026-10-06。范围：Pi、Codex、Crush、Lazygit 的官方资料与部分源码，以及 Ratatui 官方文档。没有运行这些第三方程序进行可用性测试；Pi 的官方示意图已查看。布局的适用性判断是设计推论，尚未经过用户测试。候选方案见 [TUI 设计](../../design/tui.md)。

## 1. 核对版本与证据

| 项目 | 固定版本 | 主要检查范围 |
| --- | --- | --- |
| Pi | `28dcce2ba45ce4a9efeb0f5b686f0be830fd89b9`，2026-10-05 UTC | usage、themes、interactive-mode 示意图 |
| Codex | `822e58cc3d666166c7446c5b1ea2e52f5d09594c`，2026-10-06 UTC | tui/lib.rs 的屏幕模式、transcript_mode.rs |
| Crush | `8b1d542c2ef1be9a3022f67363d696e53f31b0d4`，2026-10-06 UTC | model/ui.go、sidebar.go、header.go、chat.go，以及 styles/palette.go、themes.go |
| Lazygit | `c5f7158154602d23b0750d4304c73e2aead8df5b`，2026-10-05 UTC | docs/Config.md |
| Ratatui | 核对当日官方文档 | alternate screen 与 backend 行为 |

Pi 当前官方仓库为 earendil-works/pi。此前研究引用的 badlogic/pi-mono 版本仍然是历史快照；本研究不按旧资料推断当前默认模式。网页抓取的 main 文档可能落后于最新 commit，以下事实优先采用直接下载并检查的固定版本。

## 2. 值得借鉴的实际机制

### Pi：连续阅读、可折叠执行记录

[usage.md](https://github.com/earendil-works/pi/blob/28dcce2ba45ce4a9efeb0f5b686f0be830fd89b9/packages/coding-agent/docs/usage.md) 描述 transcript、editor、footer 三部分：对话中展示工具调用、结果与错误，工具输出可折叠；输入和状态集中在下方。

该版本同时支持 fullscreen 与 regular：文档明确 fullscreen 是默认模式，固定输入与状态区域，由程序滚动 transcript；regular 使用终端原生 scrollback。不能把“Pi 看起来是单栏”推导为“Pi 只能在普通终端缓冲区输出”。

[themes.md](https://github.com/earendil-works/pi/blob/28dcce2ba45ce4a9efeb0f5b686f0be830fd89b9/packages/coding-agent/docs/themes.md) 的默认 system theme 尝试使用终端配色，并提供能力不足时的回退。这表明配色可建立统一的角色语义，同时兼容用户自己的终端外观。

SAKI 推论：保留对话宽度，给工具结果渐进展开的入口；主题的识别度可以来自强调色、缩进和标记规则。是否启用 fullscreen 另行评估。

### Codex：区分历史归谁管理

[transcript_mode.rs](https://github.com/openai/codex/blob/822e58cc3d666166c7446c5b1ea2e52f5d09594c/codex-rs/tui/src/transcript_mode.rs) 区分 Owned 与 Terminal 两种 transcript ownership。Owned 要求配置启用并且 alternate screen 可用；否则使用 Terminal。

[lib.rs](https://github.com/openai/codex/blob/822e58cc3d666166c7446c5b1ea2e52f5d09594c/codex-rs/tui/src/lib.rs#L2136) 的 determine_alt_screen_mode 尊重显式覆盖与终端条件。所检查版本的 auto 条件针对 Terminal.app over SSH；没有据此假定所有 tmux 或 Zellij 场景都使用相同策略。

SAKI 推论：单栏、双栏是空间布局；程序管理滚动、终端管理 scrollback 是渲染策略。两者应分开决定。固定输入区、任意折叠和选择工具详情通常更适合程序管理历史，但需要兑现复制、滚动锚点和退出恢复。

### Crush：品牌主题与紧凑布局

[ui.go](https://github.com/charmbracelet/crush/blob/8b1d542c2ef1be9a3022f67363d696e53f31b0d4/internal/ui/model/ui.go#L4424) 的 chat 布局有 compact 分支，使用紧凑 header、正文、editor 等区域；普通布局有 sidebar。[sidebar.go](https://github.com/charmbracelet/crush/blob/8b1d542c2ef1be9a3022f67363d696e53f31b0d4/internal/ui/model/sidebar.go#L66) 在 compact 时不更新常规 sidebar 内容。

[themes.go](https://github.com/charmbracelet/crush/blob/8b1d542c2ef1be9a3022f67363d696e53f31b0d4/internal/ui/styles/themes.go) 和 [palette.go](https://github.com/charmbracelet/crush/blob/8b1d542c2ef1be9a3022f67363d696e53f31b0d4/internal/ui/styles/palette.go) 给文本、背景、强调、错误、diff 等角色统一提供颜色。本次未运行 Crush，也未据 README 推断每个版本的实际视觉效果。

SAKI 推论：可以建立自己的色彩与排版语言，但需要同时考虑紧凑模式。常驻大 logo、侧栏、会话树会占用面积；是否保留应根据任务需要判断。

### Lazygit：选择对象，再展示详情

[Config.md](https://github.com/jesseduffield/lazygit/blob/c5f7158154602d23b0750d4304c73e2aead8df5b/docs/Config.md) 提供侧栏比例、焦点面板扩展、normal/half/full 屏幕模式，以及宽度足够时左右分割、否则上下分割的 flexible 配置。其文档也说明捕获鼠标会影响原生文本选择。

SAKI 推论：工具调用是可以选择的对象，输出与修改可以作为详情；宽屏用侧边检视，窄屏用下方详情或临时全屏。借鉴对象与详情的关系，不直接把 Git 的多面板结构移到对话工具中。

### Ratatui：alternate screen 的实际约束

[官方 alternate screen 文档](https://ratatui.rs/concepts/backends/alternate-screen/) 说明 alternate screen 与普通缓冲区不同，退出后恢复普通屏幕；不同终端支持与行为有差异。它不会自动提供 transcript 的历史导航或复制功能。

SAKI 推论：不能仅凭一张静态稿宣称滚动、tmux 和复制已经解决；真实终端中的验证必须成为后续验收内容。

## 3. 四种候选布局的比较

| 候选 | 主要阅读方式 | 适合的任务 | 主要代价 |
| --- | --- | --- | --- |
| 流式对话 | 消息和工具按发生顺序连续排列 | 短任务、讨论、窄分屏 | 长工具链推远最终结论；重复解释占高度 |
| 工作记录 | 每轮任务中聚合执行记录，保留结论 | 常规工程任务、回看共同工作 | 聚合时必须保留真实顺序，避免把未完成步骤展示成结论 |
| 双栏工作台 | 对话与选中工具的输出／diff 同时可见 | 阅读 diff、诊断长日志 | 正文变窄；焦点与选择状态增加；窄屏必须改布局 |
| 底部检视抽屉 | 对话主导，详情需要时占用下部 | tmux 窄 pane、偶尔查证执行依据 | 详情压缩纵向空间；短窗口宜改为临时详情视图 |

上述适用性为设计判断，没有可用性实验支持某项稳定优于其他项。四种候选使用同一任务与相同结果，尽量让比较集中在信息组织。

## 4. 设计结论与下一轮验证

推荐继续探索“工作记录作为默认、详情按需打开”的组合。正文占据完整宽度，完成后容易找到结论；工程证据仍然可以逐项回看。宽屏详情可在右侧，窄屏放下方，短屏使用临时详情页。不要把四种候选都做成正式产品配置，先确定一种主体布局。

自己的风格可以来自统一的青瓷强调色、细线、角色缩进、执行记录与结果之间的清晰层次。暂不加入人格分数、情绪仪表或未经实现的长期记忆入口。

后续应在真实终端比较：找到最后结论所需操作、定位一次失败的操作、复制命令／代码、回看时新消息是否抢走位置，以及缩放后焦点和草稿是否保留。宽度与高度阈值需要根据 CJK 显示宽度和实际字体校准。

## 5. Rust 技术栈与组件核对

核对日期：2026-10-06。版本来自 crates.io API 和实际发布包中的 Cargo.toml／README／部分源码，优先于可能落后的 GitHub main 页面或 docs.rs 缓存。具体版本组合已在独立小样中编译运行，项目依赖未修改。

| 已发布组件 | 本次版本 | 结论 |
| --- | --- | --- |
| [Ratatui](https://crates.io/crates/ratatui/0.30.2) | 0.30.2 | 默认 Crossterm backend，与布局、文本和基础 widgets 配合 |
| [Crossterm](https://crates.io/crates/crossterm/0.29.0) | 0.29.0 | 与本组合匹配；事件读取应只有一个入口 |
| [ratatui-textarea](https://crates.io/crates/ratatui-textarea/0.9.3) | 0.9.3 | Ratatui 维护的 fork，发布包依赖 ratatui-core 0.1、widgets 0.3 和 backend 0.1 系列 |
| [tui-textarea](https://crates.io/crates/tui-textarea/0.7.0) | 0.7.0 | 发布依赖 Ratatui 0.29；不作为本次 0.30 组合的输入组件 |
| [tui-markdown](https://crates.io/crates/tui-markdown/0.3.10) | 0.3.10 | 转换为 Ratatui Text，可配置 StyleSheet 和代码高亮主题；维护者仍标实验性质 |
| [tui-widget-list](https://crates.io/crates/tui-widget-list/0.15.3) | 0.15.3 | 支持状态化的自定义 widget 列表、可变尺寸、选择和命中；适合先评估工作记录 |
| [tui-scrollview](https://crates.io/crates/tui-scrollview/0.6.8) | 0.6.8 | 提供滚动视图；为内容尺寸分配完整 Buffer，不默认承载全部长期历史 |
| [rat-widget](https://crates.io/crates/rat-widget/3.2.1) | 3.2.1 | 更完整的表单、菜单、输入、事件与焦点组件；本版需求更适合少量专用组件组合 |
| [iocraft](https://crates.io/crates/iocraft/0.9.1) | 0.9.1 | 声明式、flexbox、hooks 的 Rust TUI 路线，可作为替代；没有做本地接入对比 |

### 多行编辑与版本匹配

[ratatui-textarea 官方 README](https://github.com/ratatui/ratatui-textarea) 明确其为独立维护的 fork，提供多行编辑、撤销／重做、文本选择和软换行。浏览器抓取的 Cargo.toml 曾显示 0.9.2；crates.io 已发布 0.9.3，因此本次采用发布包验证。Ratatui [backend 官方说明](https://ratatui.rs/concepts/backends/) 建议避免不兼容的 Crossterm 版本导致事件队列和 raw mode 状态不一致。

### Markdown 的能力与限制

[tui-markdown 发布包](https://crates.io/api/v1/crates/tui-markdown/0.3.10/download) 的文档说明它输出 Text，支持表格按 pane 宽度布局、样式和代码主题；图片是文本回退，原始 HTML 和数学内容不会变成浏览器级排版。默认高亮特性会带入 syntect 等依赖。不能把“有 Markdown 库”视为复杂模型输出已经全部验证。

推荐保留模型原文供复制，渲染文本用于显示；宽度变化时重算需要宽度的表格和文本布局，普通帧不要重复解析未变化的全文。正式采用前以长代码、嵌套列表、表格和未闭合代码块检查可观察的结果。

### 长历史与有限详情的区别

[tui-widget-list 的发布源码](https://docs.rs/crate/tui-widget-list/0.15.3/source/src/view.rs) 先解析可见 viewport，再渲染可见项；部分被裁剪的大项会申请该项完整尺寸的隐藏 Buffer。因此它是合适的候选，但不是任意长单条内容的零成本解决方案，也没有替 SAKI 定义消息锚点与跟随策略。

[tui-scrollview 的发布源码](https://docs.rs/crate/tui-scrollview/0.6.8/source/src/scroll_view.rs) 在构造时以内容尺寸调用 Buffer::empty。有限日志页可以使用它；将长期对话整体扩展为一个巨大的 cell 缓冲会把存储和绘制成本随历史一起增长。SAKI 应分项维护工作记录，并继续分页读取有界工具输出。

### 完整组件库与替代框架

[rat-widget 官方文档](https://docs.rs/rat-widget/3.2.1/rat_widget/) 提供菜单、表单、选择框、输入框等，并使用 rat-event／rat-focus／rat-scrolled 等配套机制。这是真正的扩展组件集合；当前单栏对话的核心需求较少，整套采用的收益尚不明确。

[iocraft 官方 README](https://github.com/ccbrown/iocraft) 提供声明式组件、flexbox 和 hooks，适合喜欢 React 式组织方式的 Rust 项目。SAKI 当前需要明确控制每轮工具记录、折叠和滚动锚点；优先 Ratatui 是本地设计判断，不是已做性能实验后的优劣结论。

### 异步接入

[Crossterm 事件文档](https://docs.rs/crossterm/0.29.0/crossterm/event/index.html) 明确禁止 EventStream 与 read/poll 混用。推荐一个 UI 输入读取入口，经由已有 Tokio 调度 Agent 工作与展示事件；后台网络和工具不直接打印到 TUI 控制的终端。

独立小样编译执行通过，中文多行编辑、Markdown 与列表可以使用同一组 Ratatui 类型。实际解析结果为 ratatui-core 0.1.2、ratatui-widgets 0.3.2、ratatui-crossterm 0.1.2、Crossterm 0.29.0，各仅一个版本。未测试真实终端、输入法、长期性能或其他平台。

## 6. Codex 的自有组件与交互机制

核对日期：2026-10-06。本节重新查询官方仓库，固定到 [`c0c230e6730b3b3c9101b8aff4b9aea4027cea5b`](https://github.com/openai/codex/tree/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b)，commit 时间为 2026-10-06 12:32:27 UTC。与第 1 节的早先布局快照分别记录。实际下载并检查源码及部分测试，未编译或运行 Codex；下述 SAKI 建议属于设计推论。

### 基础库与复用边界

[workspace Cargo.toml](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/Cargo.toml#L436) 声明 Ratatui 0.30.2、Crossterm 0.29.0。不过，[patch.crates-io](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/Cargo.toml#L643) 将 Crossterm 替换为 openai-oss-forks 的固定 Git revision；不能把这些版本号视为未经修改的官方发布组合。

[ComposerInput](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/public_widgets/composer_input.rs) 包装内部 ChatComposer，提供输入、粘贴、提交、高度、光标和渲染接口。该接口在 lib.rs 中公开导出，说明成熟组件可以供其他内部产品复用。然而 [codex-tui 的依赖](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/Cargo.toml) 包含许多 Codex 协议与产品模块；它不是一个已经与业务隔离的通用组件包。

SAKI 建议：保持已经确定的 Ratatui 原生组件优先路线，参考 Codex 的职责和交互契约。出现复用后才提取自有组件，按实际问题决定是否引入专用依赖或 terminal fork。

### 输入编辑与提交语义分开

[TextArea](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/bottom_pane/textarea.rs) 保存 UTF-8 原文、光标、选择、换行缓存和原子文本元素；[wrapping.rs](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/bottom_pane/textarea/wrapping.rs) 按 grapheme 边界构建显示行。[ChatComposer](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/bottom_pane/chat_composer.rs) 在其上管理提交、草稿、历史回忆、附件与弹出菜单。这些职责没有全部交给基础 Paragraph。

SAKI 建议：输入编辑器负责编辑状态与光标，应用负责接受任务和发送语义。字节索引、grapheme 边界和终端 cell 宽度是三种不同坐标；中文、组合字符、emoji 的删除和移动需要以可观察行为验收。原生组件提供展示基础，完整编辑器仍有实际实现成本。

首版可以限定多行文本、基本光标编辑、粘贴与换行；Vim、附件、补全、运行中排队分别等到对应产品能力存在时再引入。发送／换行快捷键仍待确定，需要同时考虑支持与不支持增强键盘协议的终端。

### 粘贴是一种输入事件

[paste_input.rs](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/bottom_pane/chat_composer/paste_input.rs) 将显式粘贴与缓冲后的粘贴统一接入；大段内容可显示为占位元素，并在提交时恢复完整内容。[PasteBurst](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/bottom_pane/paste_burst.rs) 处理没有 bracketed paste 的终端中快速到达的字符／Enter／Tab，并专门区分非 ASCII 输入路径。

SAKI 建议：优先支持 Crossterm Paste 事件，粘贴中的换行不能逐次提交任务。长输入限制显示高度，保留完整原文；占位折叠与 paste-burst 时间启发式属于后续按真实终端问题评估的增强，不能因为快速中文输入就随意引入等待。

### 展示历史由可更新的记录构成

[HistoryCell](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/history_cell/mod.rs#L199) 是一个历史展示单元，提供紧凑、展开与原始内容入口。[ChatWidget](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/chatwidget.rs) 区分完成的历史记录和仍可改变的 active cell，使工具进行中状态无需伪装成最终记录。

[ExecCell 的模型](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/exec_cell/model.rs#L141) 用 call_id 匹配完成事件，找不到对应调用时返回不匹配，避免把结果附到另一个工具上。[紧凑展示](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/exec_cell/compact.rs) 显示 Running／Ran／Failed、退出码和少量尾部输出，并提供详情展开。

SAKI 建议：每次工具调用有稳定标识，开始与结束更新同一条记录；失败依据工具结果显示。任务内按照真实时间顺序组织工具组，暂不采用 Codex 的 shell 命令语义分类。exit 0 仅表示命令正常退出，不能自动宣称修改已经验证。Conversation 的压缩与展示记录分别管理。

### 阅读位置属于内容

[TranscriptView](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/transcript_view.rs) 区分 Latest 与 Reading(Anchor)，锚点关联保留的记录身份和内容偏移；渲染循环只绘制视口中的行。Codex 当前用保留的 Arc 身份定位记录，SAKI 可以根据自己的数据模型使用明确的记录 ID。

[bookmark.rs](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/transcript_view/bookmark.rs) 保存浏览位置；[mutations.rs](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/transcript_view/mutations.rs) 处理记录替换、分页插入和未读活动。[owned_transcript_follow_tests.rs](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/app/owned_transcript_follow_tests.rs) 包含鼠标回到底部、缩放时草稿和光标几何位置保持的测试。本次只阅读，未执行。

SAKI 建议：默认跟随最新；主动上滚进入回看，后续输出提示有更新；明确回到底部后恢复跟随。锚点保存记录 ID 与原文位置，窗口缩放重新排版时再映射显示行。展开／折叠导致当前内容消失时需要明确回退规则，不能承诺任何情况下都维持完全相同的屏幕位置。

### 内容源与显示装饰分开

[HistoryCell 的 copy_source／raw_lines](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/history_cell/mod.rs#L216) 保留语义复制与原始展示路径；工具折叠控制不会作为源内容加入选择文本。[Markdown renderer](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/markdown_render.rs) 用 pulldown-cmark 解析后生成 Ratatui 行，窄表格可退化为键值记录；代码高亮使用专用解析依赖。

SAKI 建议：保留模型与工具原文，再生成 styled lines，复制命令和代码时排除细线、状态和折叠提示。原生展示组件与 Markdown 解析库可以配合。完整复制、链接点击和表格回退分别验收，不复制 Codex 的全部数学／Mermaid／附件能力。

### 重绘和缓存有边界

[FrameRequester](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/tui/frame_requester.rs) 合并重绘请求，[FrameRateLimiter](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/tui/frame_rate_limiter.rs) 将通知上限设为 120 FPS。这是上限，不代表空闲时持续按此频率画完整历史。

[布局缓存](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/transcript_view/layout.rs) 区分宽度、展示状态和稳定／动态记录，最多保留 64 项、以 8 MiB 文本作为驱逐阈值；单个超大条目仍可保留，因此不是严格的整体内存上限。[MarkdownRenderCache](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/history_cell/markdown_render_cache.rs) 还考虑主题与终端颜色变化。[tool_output.rs](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/tool_output.rs) 在换行前同时限制预览行和输入字节，避免一条巨长日志导致渲染成本失控。

SAKI 建议：输入、任务事件、resize 和活动计时器触发重绘，合并同一时段的请求；普通帧复用未变记录的布局。缓存是可丢弃的派生状态，原始记录仍由应用保存。本段讨论内存中的绘制缓存，不重新引入用户暂缓的磁盘日志清理与跨重启持久化。

### 焦点和终端生命周期集中处理

[BottomPane](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/bottom_pane/mod.rs#L861) 先将事件送给当前活动视图，否则送给 composer；[BottomPaneView](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/bottom_pane/bottom_pane_view.rs) 定义活动视图的输入与结束语义。[tui.rs](https://github.com/openai/codex/blob/c0c230e6730b3b3c9101b8aff4b9aea4027cea5b/codex-rs/tui/src/tui.rs#L463) 集中初始化与恢复终端，并在 panic hook 中先恢复终端再显示错误。

SAKI 建议：保留唯一终端事件入口，详情或弹出层优先处理自己的键盘输入；Esc 先关闭局部视图，具体取消任务的语义另行设计。编辑器草稿独立保存，切换详情不销毁。后台 Agent 与工具发事件，UI 统一绘制；正常退出、初始化中途失败与 panic 的终端恢复使用同一路径。

### 第一版的提取范围

建议先明确 TranscriptView（记录与阅读）、Composer（输入与提交）、ToolEntry（工具记录）和状态栏这四处职责。可先用普通 struct／enum 和 Ratatui 原生 widget 表达；Codex 的宽泛 HistoryCell／Renderable／BottomPaneView trait 是其大量实际组件的产物，当前没有必要逐个复制。

验收优先覆盖：中文／组合字符删除和换行；粘贴多行只作为一个输入；上滚后工具完成不抢位置；缩放保留草稿与阅读内容；call_id 不串结果；工具失败仍可回看；复制不带装饰；长单行日志不会造成全量重排；退出恢复 shell。之后再验证流式正文、命令系统和取消等新能力。

## 7. Codex 输入框的编辑手感

核对日期：2026-10-08。本机 `codex --version` 为 0.161.0；本节检查官方 `rust-v0.161.0` tag 对应的 commit `979011409de0a60b52f179721948e65531d26144`，与第 6 节的旧快照分别记录。实际阅读按键配置、编辑器、历史与取消处理及部分测试；没有运行 Codex 的物理键盘交互测试。下表描述默认非 Vim 模式，没有弹出层占用输入时的行为。

### 按键与功能

| 操作 | 默认按键 | 可观察行为 |
| --- | --- | --- |
| 前后一个字符 | ← / →；Ctrl+B / F | 按 grapheme 移动，组合字符和 emoji 保持完整 |
| 前后一个词 | Ctrl+← / →；Alt+← / →；Alt+B / F | 按 Unicode 词边界及分隔符移动 |
| 行首／行尾 | Home / End；Ctrl+A / E | Home/End 定位当前逻辑行；Ctrl+A 已在行首时到上一行行首，Ctrl+E 已在行尾时到下一行行尾 |
| 上下移动／前后历史 | ↑ / ↓；Ctrl+P / N | 编辑器按视觉换行移动，保留目标显示列；满足历史导航条件时改为召回输入 |
| 向后删一个字符 | Backspace；Ctrl+H | 删除光标前的字符 |
| 向前删一个字符 | Delete；Ctrl+D | 非空输入中向前删除；空输入且无弹出层时 Ctrl+D 退出 |
| 向后删一个词 | Ctrl+W；Alt+Backspace；Ctrl+Backspace | 删除前一个词并保存到内部删除缓冲 |
| 向前删一个词 | Alt+D；Alt+Delete；Ctrl+Delete | 删除后一个词并保存到内部删除缓冲 |
| 删除到行首／行尾 | Ctrl+U / K | 操作当前逻辑行；已在相应边界时可以删除相邻换行 |
| 恢复最近删除的片段 | Ctrl+Y | 插入内部单条 kill buffer，不读取系统剪贴板，也不是通用撤销 |
| 清空草稿 | Ctrl+C | 保存非空草稿到本地输入历史，再清空；之后 ↑ 可召回 |
| 搜索输入历史 | Ctrl+R | 搜索中 Ctrl+R/S 向旧／新匹配移动；Enter 接受为草稿，不发送；Esc 或 Ctrl+C 取消并恢复原草稿 |
| 发送／换行 | Enter；Shift+Enter / Alt+Enter / Ctrl+J | composer 拦截普通 Enter 用于提交；其余组合键可插入换行。Ctrl+M 也配置为换行，但传统终端可能将其编码为普通 Enter |
| 外部编辑器 | Ctrl+G | 使用 VISUAL，未设置时使用 EDITOR；保存退出后回到草稿 |
| 最新完整回复复制 | Ctrl+O | 复制最新已完成回复；与输入框编辑、终端复制分别处理 |

默认按键依据：[keymap.rs](https://github.com/openai/codex/blob/979011409de0a60b52f179721948e65531d26144/codex-rs/tui/src/keymap.rs#L1642)。编辑边界、视觉移动与 kill buffer 依据：[textarea.rs](https://github.com/openai/codex/blob/979011409de0a60b52f179721948e65531d26144/codex-rs/tui/src/bottom_pane/textarea.rs#L1415)。历史搜索、复制、运行中输入与回退也见[官方交互说明](https://learn.chatgpt.com/docs/developer-commands?surface=cli#interactive-shortcuts)；外部编辑器见[官方 CLI customization](https://learn.chatgpt.com/docs/cli-customization#prompt-editor)。具体组合键能否传到程序取决于终端和 tmux，绑定存在不等于所有环境都能收到。

### 历史导航不会覆盖正在写的草稿

[chat_composer_history.rs](https://github.com/openai/codex/blob/979011409de0a60b52f179721948e65531d26144/codex-rs/tui/src/bottom_pane/chat_composer_history.rs#L384) 的条件不是“到了第一行就翻历史”：输入为空时可导航；非空时必须仍等于最近召回的历史文本，并且光标在全文开头或结尾。中间编辑或召回后改过文本时，↑/↓ 和 Ctrl+P/N 都回到正常光标移动。向下越过最新历史恢复为空输入。

因此，多行光标和输入历史共用按键，但有明确的边界。新写的非空草稿不会因为在首行按 ↑ 而消失；要从非空草稿查找历史，可通过 Ctrl+R 搜索，并在取消后恢复草稿。

### Ctrl+C 是按状态处理的

[BottomPane::on_ctrl_c](https://github.com/openai/codex/blob/979011409de0a60b52f179721948e65531d26144/codex-rs/tui/src/bottom_pane/mod.rs#L955) 先让活动视图处理，再取消搜索，再保存并清空非空草稿。只有没有被这些状态消耗时，[ChatWidget::on_ctrl_c](https://github.com/openai/codex/blob/979011409de0a60b52f179721948e65531d26144/codex-rs/tui/src/chatwidget/interaction.rs#L510) 才中断可取消的工作，或在空闲时退出。运行中存在非空草稿时，第一次 Ctrl+C 可以仅清草稿，不能笼统说“总是取消任务”。

这个版本 `DOUBLE_PRESS_QUIT_SHORTCUT_ENABLED = false`，源码说明双击退出实验已经关闭。不要把旧资料中的“必须连续按两次 Ctrl+C 才退出”当作当前默认契约。非空草稿清空后再按一次可以退出，是输入状态已经变为空，并非一秒内双击的统一规则。

[clear_for_ctrl_c](https://github.com/openai/codex/blob/979011409de0a60b52f179721948e65531d26144/codex-rs/tui/src/bottom_pane/chat_composer.rs#L1699) 保存清空的草稿与关联的大段粘贴内容到输入历史。Ctrl+C 的恢复入口是历史召回；Ctrl+Y 则恢复 Ctrl+U/K/W 等最近删除的片段，二者不要混为同一种 undo。

### 其他能力与范围

大段粘贴超过 1000 个 Unicode scalar values 时可显示为占位元素，提交时展开原文；不是截断或 conversation compact。普通多行粘贴作为输入，另有 PasteBurst 兼容未报告 bracketed paste 的终端。依据：[paste_input.rs](https://github.com/openai/codex/blob/979011409de0a60b52f179721948e65531d26144/codex-rs/tui/src/bottom_pane/chat_composer/paste_input.rs#L116) 与 chat_composer.rs 的阈值定义。

Codex 还有 @ 文件检索、命令弹出层、? 快捷键提示、可配置 keymap 和 Vim 编辑。运行中 Tab 排队、Enter 补充当前任务，以及空输入时 Esc 两次编辑旧消息并分叉，都涉及输入之外的执行或会话语义。当前 SAKI 的 Tab 已用于输入／记录焦点切换，Alt+↑/↓ 用于滚动；迁移这些键位需要明确取舍。

### 对照 SAKI 与建议顺序

本次检查的 `src/frontend/tui/editor.rs` 已有 grapheme 编辑、多行插入、字符删除、逻辑行移动和 Home/End；`src/frontend/tui/mod.rs::handle_key` 已有发送、Shift/Alt+Enter、多行 Paste 和记录焦点。缺少词移动／删除、行删除与恢复、输入历史、历史搜索和视觉行移动。现有 Ctrl+C 空闲时退出、运行中等待结束后退出，不会清空草稿；Alt 字母目前也可能被作为普通文本插入，需要随 Meta 编辑键接入一起修正。

推荐下一轮先完成字符／词／行快捷编辑、单条 kill buffer、当前会话输入历史与 Ctrl+C 保存清空，以及视觉换行移动和显示列保持。验收应覆盖中文、组合字符、软换行、召回后修改、清空后召回，以及空／忙状态的 Ctrl+C。历史搜索、长粘贴占位和外部编辑器再按使用需要增加。

Editor 负责编辑内容与光标，TUI 负责输入历史、按键路由与焦点；这些状态不需要进入 Conversation 或 Agent。输入历史是用户提交／暂存草稿的 UI 历史，不应从被 compact 的 conversation 重建。第一步仅保存在当前进程，不因此引入持久化。

真正中断任务、运行中补充指令、排队与修改旧消息分叉需要单独设计执行契约。当前 worker 不支持取消，UI 不能只换提示就声称 Ctrl+C 已中断任务。本节写成时是研究与实施建议；同日经用户授权实现了快捷编辑、当前进程输入历史与视觉行导航，实际契约和验收见 [TUI 设计](../../design/tui.md#输入框优化验收)。历史搜索、长粘贴占位与外部编辑器尚未接入。

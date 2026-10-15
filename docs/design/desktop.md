# Desktop contacts：首版交互设计

更新日期：2026-10-15。状态：**设计原型**：浏览器示意与可运行 MyGo 原生 UI 预览均已实现；native runtime（contact store、IPC／后端模型调用）仍属 future work，Rust core 尚未实现 contact 模型。
[previews/desktop-contacts.html](previews/desktop-contacts.html) 是可交互浏览器示意；仓库内 `desktop/` 是可运行的 MyGo **原生 UI** 设计预览（非 WebView 包 HTML）。两者都是 UI demo，只含本地演示数据；Working／回复／历史检索／source 跳转均为本地状态机模拟，不代表后端已支持并行执行、后台任务或真实模型调用。

调色板：Rose Pine（dark）与 Rosé Pine Dawn（light），来源 [rosepinetheme.com/palette](https://rosepinetheme.com/palette/)，跟随宿主外观。角色分配保持克制：rose 只用于主操作与选中态，可读的 `--link`（Dawn pine / Main foam）用于链接、来源与进行中状态，foam 的填充与图标用于引用和历史，gold 只用于 needs attention，love 只用于校验错误，iris 只用于焦点环与临时的消息定位高亮。

## Native preview（已实现）

- 形态：仓库内单独立 Go module `desktop/`，用 MyGo 的**原生 UI**（`ui` 包，非 WebView 包 HTML）承载同一套 contact 界面。它是设计预览而不是 native runtime：没有 contact store、没有 IPC、没有真实模型调用，也不修改 Rust/Cargo。
- 依赖：Go 1.27.1；`github.com/egoist/mygo v0.4.9-0.20261014025635-fea932a7a2a9`（commit `fea932a7a2a9`），`CGO_ENABLED=0`。`go.mod` 固定该 pseudo-version，没有 replace。
- 文件：`desktop/main.go`（窗口入口、embed+decode 默认 mascot）、`desktop/model.go`（contact／消息／草稿／来源与本地演示调度）、`desktop/view.go`（原生布局）、`desktop/theme.go`（Rose Pine Main／Dawn）、`desktop/model_test.go`、`desktop/view_test.go`、`desktop/assets/saki-avatar-default.png`。
- 已实现行为：contact rail／会话／按需 aux；每个 contact 独立 profile、消息与草稿（切换保留）；Create（Name 必填）／Cancel；Settings 只影响后续回复并保留历史；多行 composer 的 Enter 发送与 Shift+Enter 换行（`ui.Enter.Text()`／`SetText()`，IME 组合期不发送）；Sources 与 contact 范围 History；Ren 的 Working→Needs attention→Try again；发送后的回复按 contact 归属。native 窗口允许内部滚动。
- 命令（均在 `desktop/`）：
  - `CGO_ENABLED=0 go build -o /private/tmp/saki-desktop-design-qa/saki-desktop .`
  - `CGO_ENABLED=0 go run .`（直接运行原生预览）
  - `CGO_ENABLED=0 go vet ./...` 与 `CGO_ENABLED=0 go test ./... -count=1`
  - `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /private/tmp/saki-desktop-design-qa/saki-desktop-linux-amd64 .`
  - `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /private/tmp/saki-desktop-design-qa/saki-desktop-windows-amd64.exe .`
  - `SAKI_PREVIEW_DIR=/private/tmp/saki-desktop-design-qa/previews CGO_ENABLED=0 go test -run TestPreviewImages -count=1 .`
- 证据：默认 `go test ./...` 14/14 通过；设置 `SAKI_PREVIEW_DIR` 时 15/15 通过（含 preview 导出测试）。`go vet` 通过；macOS arm64、Linux amd64、Windows amd64 构建通过；headless 预览图 12 张（light/dark × chat/add/settings/sources/history，以及 960×640 的 chat/settings）。macOS QA 用 `.app` bundle 只在 `/private/tmp/saki-desktop-design-qa` 生成。作者尚未在真实窗口里操作 GUI（computer-use 访问未获批），视觉验证来自 headless `ui.Tester` 渲染的 PNG。
- macOS QA bundle 打包：按 Apple 本地开发做法在组装后做 bundle 级 ad-hoc 签名（`codesign --force --sign - --identifier dev.saki.desktop.preview`），绑定现有 Info.plist 与资源。`codesign --verify --strict` 与 `--deep --strict` 均 exit 0；`Identifier=dev.saki.desktop.preview`，Info.plist 已绑定，Sealed Resources 存在。bundle id／显示名／路径不变；独立原始二进制保留，bundle 内可执行文件代码未改（哈希因签名而不同）。打包脚本只保存在 QA 目录，不进入仓库。
- 浏览器（WebAssembly）预览：同一 `desktop/` Go UI 用固定 MyGo 版本构建为 wasm。在 `desktop/` 运行 `go run github.com/egoist/mygo/cmd/mygo@v0.4.9-0.20261014025635-fea932a7a2a9 build -platform web -o /private/tmp/saki-desktop-design-qa .`，产物为 `web/index.html`、内容哈希 `desktop.ea7d761f7873.wasm`（约 11.2 MB）与 `wasm_exec.js`；产物只在 QA 目录。本地 URL `http://127.0.0.1:8137/`（`python3 -m http.server 8137 --bind 127.0.0.1`）供手动查看。
- 验证边界：浏览器版本尚未实际在浏览器中操作或核对（当前 CUA 无浏览器连接：iab unavailable、browsers=[]，Chrome 也未获批准）；这条路径不覆盖 macOS 原生窗口、系统主题与输入法行为。原生 app 已按本地开发做法 ad-hoc 签名且 `--verify --strict` 通过，但 Computer Use 依旧拒绝访问，因此不能声称签名修复解决了授权问题。
- 真实 vs 模拟：真实的是 MyGo 原生窗口、布局、主题、输入与 contact 状态隔离；模拟的是回复内容、Working／attention、history 检索与 sources 引用。contact 状态归 app（Go）所有；Saki 仍是单 active job 的 Rust 进程，两者尚未连接。
- 已知限制：mascot 仍是 candidate；没有附件、托盘／菜单与 picker；QA app 仅做本地 ad-hoc 签名，未使用开发者证书、未打包分发；真实窗口的键盘／滚动行为尚未由作者实测。

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

## 布局与断点

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

## 关键流程

1. **选择 contact**：切换 name、profile、messages 与 draft；草稿按 contact 保存。未选中的 contact 可以处于 Working，查看与切走都不会清空它。状态词只描述工作：Ready／Working／Needs attention，不假装在线。
2. **Add contact**：主区域表单，Name 必填，Purpose／Instructions 可选，Workspace 放在 Advanced 折叠组；Create 后进入空会话，Cancel 返回且不丢草稿。校验只给最小 inline 错误。
3. **Contact settings**：编辑 name／purpose／instructions，并明确提示“changes apply to future messages; saved history stays intact”。Save 更新 header 与列表。
4. **Composer**：textarea、Send、附件控件；Enter 发送、Shift+Enter 换行、IME 组合期不发送。发送后进入有限的本地 working→reply 过渡；切换 contact 不影响回复归属。附件只是可移除的 filename chip。
5. **Source action**：回复中的 “2 earlier messages” 打开 contact 范围的 sources 区域，显示日期、原说话人与原文；View in conversation 会把该消息插回会话顶部并高亮定位。返回时草稿与进行中的工作保持。
6. **History action**：contact 范围的本地搜索，实际过滤该 contact 的样例历史；无结果有明确说明；不会出现其他 contact 的消息。不会实现或暗示 shared memory 与多 agent 协作。

## 状态与反馈

| 状态 | 表现与下一步 |
| --- | --- |
| Empty conversation | 说明该 contact 的 profile 与用途，引导写第一条消息或编辑 settings |
| Working | 会话尾部有可理解的进行中行（如 Running the parser tests）；Send 暂时不可用；切走保留状态 |
| Needs attention | gold 状态 + 失败摘要 + 样例 “Try again”；不显示假装可取消的 Stop |
| No history results | 说明该 query 在此 contact 历史中无匹配，建议更短的短语 |
| Validation | Name 必填的 inline 错误，focus 回到字段 |
| Sources | foam 强调引用来源，不铺满页面 |

## 可访问性与可视化契约

原生 button／input／textarea，语义标签与可见 focus；aria-live 只播报有意义的更新（联系人切换、回复到达、创建/保存、无结果）；用户文本一律通过 textContent 写入 DOM。所有 ID 唯一，CSS 限定在 `#saki-desktop-contacts` 下。1024／736／375／320 宽度与 light/dark 均可读，无横向溢出。

## 验收清单

仅标记实际验证过的项目；未实测的保留说明，而不是当作通过。

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

## 明确不做

多 agent 协作、shared memory、后台并行执行的真实承诺、Stop／取消控件、原始推理链展示、contact 级模型/provider/key 设置（deferred）、头像 picker／上传／contact 级头像设置（same-family 集合以后再评估）。

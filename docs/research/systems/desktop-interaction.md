# Desktop interaction：桌面交互与视觉参考

更新日期：2026-10-28。调研与设计：`agent:pm#mira`，任务 [#124](https://github.com/HeimaoLST/Saki/issues/124)。

目标是理解用户喜欢的桌面产品如何组织对话、过程和结果，为现有 Saki GUI 提供设计依据。这里只讨论本次实际看到的界面，不作两款产品的完整能力评测。

## 方法与证据边界

**实现事实（本地 UI 观察）**：通过 computer use 操作已安装的 `/Applications/Grok Bot.app`（bundle ID `com.anysphere.sand`）与 Qoder CN（`com.qodercn.app`），同时读取 accessibility tree 和窗口截图。Grok Bot 是用户所说的 grokbot；Qoder 指本机 Qoder CN 桌面应用，不推定其他发行版相同。本次未核对版本号，结论限定于 2026-10-22 的本地安装状态。

只打开、关闭已有界面与菜单；没有发送消息、执行任务、切换实际模型、连接服务或改写应用设置。Qoder 启动后出现一次活动弹窗，关闭后继续观察；不能由此推断每次启动均弹出。调研结束关闭临时菜单，收起工具组并返回最新消息。

原始截图保存在本机目录 `/Users/heimaolst/Workspace/design-evidence/Saki/gui-references-20261022/`，`manifest.json` 记录文件尺寸、SHA-256 和截图作用。截图含用户既有会话与账户界面，未提交到仓库。仓库保存交互事实和设计推论，不复制私人会话正文。没有这些本地文件的协作者可依下表复现路径，但不能声称已经检查过原始截图。

| 编号 | 文件 | 操作与覆盖范围 |
| --- | --- | --- |
| G1 | `grok-home.jpg` | 已选联系人主页面：联系人列表、对话、输入区 |
| G2 | `grok-details.jpg` | 点击顶部联系人名称／View conversation details；右侧详情展开 |
| G3 | `grok-details-close-0.jpg` 至 `-2.jpg` | 关闭详情后的连续捕获；仅作布局终态证据 |
| G4 | `grok-composer-actions.jpg` | 输入区 Composer actions 弹出 Attach files / Teach a task |
| G5 | `grok-search.jpg` | 左侧 Search 打开搜索浮层；Escape 关闭 |
| Q1 | `qoder-conversation.jpg` | 启动时活动弹窗；只证明本次出现 |
| Q2 | `qoder-conversation-clean.jpg` | 关闭活动弹窗后的对话、文件改动与右侧任务摘要 |
| Q3 | `qoder-model-menu.jpg` | 点击输入区模型按钮，展示 Custom 分类和当前选择；未更改选择 |
| Q4 | `qoder-tool-group.jpg` | 展开既有多工具组，展示过程行、状态图标及回到最新消息入口 |

## Grok Bot：把「与谁对话」作为主线

以下是**实现事实（本地 UI 观察，G1–G5）**：

- 左侧是 bot 联系人，使用头像、名称和一行最近消息；选中行使用整块浅灰背景。主对话区域明显更亮，依靠背景和留白区分区域。
- 顶部以一个可点击的头像／名称胶囊呈现当前联系人；附件和教任务的入口收在 composer 的加号菜单。主页面暴露的操作较少。
- 对话使用圆角气泡，用户消息使用橙色、助手消息使用浅灰。日期分隔符与消息组间距明显；短对话尤其宽松。
- 点击名称打开右侧详情，原有对话区随之收窄。详情包含头像、名称、标签及 Details / Library / Computer 标签页；本次仅检查 Details 的 recurring tasks 空态，没有验证另外两页功能。
- Search 是带背景遮罩的浮层，结果同时包含 bot、创建操作、设置与主题入口。它是对象和操作的统一入口，不只是聊天全文搜索。本次只检查初始列表，没有验证检索范围、排序和键盘全流程。
- 加号菜单锚定在输入区上方，展开后仍能辨认正在操作的对话；Escape 能退出菜单和搜索浮层。

**设计推论**：亲切感主要来自联系人身份、少量常驻按钮、大留白和清晰分区。Saki 可以借鉴这套信息层级。用户在 2026-10-22 的原型反馈中明确希望保留气泡对话；长文、表格和图表的阅读问题通过气泡内部排版、表格自动换行与内容展开解决，不能据此直接改成文档式主界面。具体方案见[原型设计](../../design/desktop-workbench.md#消息富文本与输入)。橙色、品牌形状、语音与 recurring tasks 不是本轮 Saki 的需求。

## Qoder：把「正在做什么、产出了什么」拆开

以下是**实现事实（本地 UI 观察，Q2–Q4）**：

- 左侧包含工作模式、工作区与最近任务；中间是对话，右侧 Task monitor 展示 Recap、产物、来源等分区。它的导航单位更偏向任务／工作区。
- 助手正文直接排在内容面上，支持标题、列表、行内代码、代码块和表格。代码块与文件改动有单独的轻边框容器，正文没有层层套大卡片。
- 多个工具调用先收成一行带数量的摘要；样本中摘要同时保留失败数。展开后呈现工具／过程行、状态图标与可点击文件或详情入口，显示回到最新消息按钮。一次工具失败和最终回复区分展示。
- 文件改动块展示文件数、增删数量、文件列表，以及 Review / Undo；回复尾部另有复制和更多操作。本次没有点击 Undo 或检查其真实回滚语义。
- 输入区分成上方文字区域和下方操作栏；上下文、附件、权限、模型、语音和发送可分辨。模型弹层显示分类、提供方、当前选中标记和管理入口；只验证了显示与关闭，没有验证切换契约。
- 在本次长对话中，右侧摘要帮助回到任务上下文；但摘要中的「仍待确认」与较后的对话结果存在表面不一致。没有验证摘要刷新策略，不能把摘要视为权威执行状态。

**设计推论**：成熟感来自对内容类型的区分和按需展开，而不只是功能数量。Saki 优先借鉴正文排版、工具摘要、结果复制与来源可见性。任务总览、编辑器、扩展中心、应用市场等会显著扩大应用范围，当前不纳入原型。摘要如果未来引入，应注明生成时间和来源，不能替代 Runtime 的任务终态。

## 动效：已看到什么，尚未测到什么

**实现事实**：Grok Bot 的详情展开／关闭改变对话可用宽度；搜索浮层和输入菜单改变焦点与前景层级。Qoder 的工具展开增加过程区域，并提供回到最新消息入口。

**待验证**：动画时长、缓动、帧率、掉帧、减弱动态效果偏好。Grok 关闭详情后的三个捕获返回时间约为操作后 608、658、708ms；第一帧已是关闭后的布局。这是捕获链路耗时，不能换算成动画时长，也不能据此声称「600ms 的关闭动画」。本次未获得足够中间帧，未作动画流畅度结论。

对 Saki 的动效提案见[原型设计](../../design/desktop-workbench.md#动效与焦点)，参数属于我们的设计起点，不是对参考产品的逆向测量。

## Magpie 与 Cumora（2026-10-28）

用户明确要求借鉴 Magpie 与 yetone 的设计审美，同时不要照抄。以下将可核验的公开界面与 PM 的审美判断分开，不将仓库作者身份推定为每个设计细节的唯一作者。

### 已核对的来源

| 来源 | 固定版本与检查范围 |
| --- | --- |
| Magpie | [4655e165](https://github.com/yetone/magpie/tree/4655e16599046c02679a1477afece01d51eae686)；README、[app.css 前 180 行](https://github.com/yetone/magpie/blob/4655e16599046c02679a1477afece01d51eae686/internal/gui/assets/app.css#L1)；官方 [供应商浅色](https://github.com/yetone/magpie/blob/4655e16599046c02679a1477afece01d51eae686/site/public/img/providers-light.png)、[Agent 中文页](https://github.com/yetone/magpie/blob/4655e16599046c02679a1477afece01d51eae686/site/public/img/agents-zh-light.png)、[用量深色](https://github.com/yetone/magpie/blob/4655e16599046c02679a1477afece01d51eae686/site/public/img/usage-dark.png)、[菜单栏面板](https://github.com/yetone/magpie/blob/4655e16599046c02679a1477afece01d51eae686/site/public/img/panel-light.png) |
| Cumora | yetone GitHub 主页置顶项目；[e160a0a1 README](https://github.com/yetone/cumora/blob/e160a0a17304be305cd8cfd41bec7c0103cda273/README.md) 与官方 [桌面截图](https://github.com/yetone/cumora/blob/e160a0a17304be305cd8cfd41bec7c0103cda273/website/assets/product-screenshot.png) |

**实现事实／公开界面事实**：上述 PNG 已实际打开查看；CSS 已读取。图像来自项目自己的演示样本，本地只读副本位于 `/private/tmp/nagi-magpie-study/`，不以其临时存在作为长期证据。未操作 Magpie 的真实账户、网关或模型设置，没有测量它的动画时长／帧率，没有启动第三方源码。

Magpie 观察：

- 中性灰背景、白色内容面、细分隔线；多数对象放在连续列表中。一个分组共享外框，行内保持一致的名称／辅助说明／控件对齐。
- 当前视图使用浅底与轻阴影区别于未选项；模型选择是明确的弹出式控件，带值与下箭头。没有把每个标签做成全宽动作。
- 品牌／模型图标帮助识别，主要颜色集中在少量选择和语义状态；主背景并不被品牌色覆盖。
- 源码把 `ctl-fg` 与 `muted` 分开，明确为可操作文字维持对比；焦点轮廓存在。不能把其 13px body、11.5px code 或截图淡灰文字直接作为 Nagi 的字号下限。
- 深色用量页用深浅相近的表面、对齐数字与细线组织密集信息，图表负责信息展示，不是装饰。

Cumora 观察：

- 联系人／群聊列表、头像身份、对话气泡和 composer 是清楚的主线；浅色底和轻边界保持内容连续。
- 主动作有显著填充，其余操作较轻。它比 Magpie 使用更多颜色与人物身份，所以不能把 yetone 的作品概括为同一套冷灰模板。
- 截图中的多人、群组和宽导航属于 Cumora 的产品范围，不是 Nagi 本次需要补齐的能力。

**设计推论**：值得借用的是控件职责明确、分组连续、紧凑但有秩序、重要动作才增加视觉重量。对 Nagi，应把对象列表和对象详情分开呈现，保留气泡和联系人身份，同时减少大型等重按钮、装饰书册与嵌套重点卡。文字应更易读，不能用更小字号换取紧凑。

**不复制的部分**：不复制鸟形标志、品牌紫、供应商表格内容、团队导航或示例文案；不因为参考项目功能多就扩大 Nagi Runtime 的职责。新的 token、尺寸、例子和交互由 PM 为 Nagi 独立制定。

技能库检索的 progressive disclosure 结果实际是 Web 标题换行，未采纳。系统级搜索输出混入 landing page / conversion / GSAP 方案，也不适合此桌面工作台；只使用技能 quick-reference 中可核验的可读性、焦点、内容优先和按职责区分控件的一般检查项。核心方向来自用户反馈与上述一手界面。

## 对 Nagi 的采用与取舍

当前设计只有一个入口：[Nagi 工作台设计规范](../../design/desktop-workbench.md)，[可交互 HTML](../../design/previews/desktop-workbench.html)。研究记录只保存来源、观察及推论，不维护另一份尺寸或配色规范。v07 替代 v06 的视觉约束；产品定义和现有能力不能被旧原型阶段限制覆盖。

# Saki

**Saki 是一个正在构建中的 Agent Runtime，目标是让用户基于同一套运行核心，构建自己的 Agent 实现。**

Runtime 承担模型调用、工具执行、会话、上下文与执行事件等基础职责。开发者可以围绕自己的场景构建 Agent 产品。仓库同时开发联系人式桌面工作台 Nagi（GUI 暂定名）与 TUI；它们是使用 Saki 的产品，也用于验证共同运行能力。

当前 GUI 产品方向是提供开源、好用的 Grok Bot 式体验：用户与长期联系人交流、交代事项，由联系人自主推进，并在需要时参与具体工作。完整定义、行为需求和待定边界见 [Nagi 产品定义](docs/requirements/desktop.md)。

## 产品定位

构建一个 Agent 应用，需要把模型、工具、上下文和执行过程连接起来。Saki 希望提供这部分共同的运行基础，让使用者把精力放在自己的产品和 Agent 行为上。

我们希望支持的使用方式包括终端工具、桌面应用、Web、Bot，以及 API 或脚本调用。不同宿主复用相同的执行语义，按需提供配置与工具，并决定怎样接收输入、路由消息、展示结果。

这形成了三个清晰的职责边界：

| 层次 | 职责 |
| --- | --- |
| Runtime | 管理会话与工作上下文，执行模型／工具循环，记录历史并报告执行事实 |
| 应用 | 定义 Agent 的用途、profile、预设、历史归属及其他产品规则，组装配置与工具 |
| 宿主与界面 | 接收输入、路由请求、展示输出和活动，处理连接与交互 |

三个产品有不同的使用重点：

| 产品 | 定位 |
| --- | --- |
| Saki | 供应用复用的通用 Agent Runtime，让开发者聚焦产品行为 |
| Nagi（GUI 暂定名） | 以长期联系人为核心的自主 Agent 桌面工作台，侧重委派、持续交流与事项跟进 |
| TUI（名称待定） | 终端中的执行与协作入口，侧重用户直接观察、指导和参与工作 |

联系人可以管理多件事项，并通过独立执行会话工作；联系人不等同于单个 session。事项是用户需要完成的事情，一次执行结束不等于事项完成。联系人、事项及其关联属于应用层规则，通用 Runtime 不强制其他应用采用同一产品模型。GUI 与 TUI 围绕同一工作切换、无需复制上下文，是协作目标，尚不能因两端复用代码就宣称已实现。

**目前项目仍处于早期开发阶段。** 核心职责已经在 Rust 源码中分离，但尚未封装为可独立依赖的 library／SDK，也没有稳定的公共嵌入 API。现阶段构建自己的实现，需要基于仓库源码进行集成；多宿主承载是项目的设计方向。

## 我们的想法

### 让执行能力独立于界面

模型调用、工具执行和上下文压缩应当有统一的实现路径。界面提交请求、观察事件，无需自行编排模型与工具循环。这样，交互形态可以变化，运行行为仍然可理解、可验证。

当前 `Agent` 已通过事件报告文本增量、工具调用、用量、压缩与任务结果，不依赖终端绘制。TUI／console 通过终端适配层使用 Runtime 服务，GUI 通过 stdio 机器接口连接真实 Runtime。它们复用运行实现，当前入口不代表已连接到同一个跨客户端运行实例。

### 区分会话、执行与长期状态

Session 承载一段工作的上下文与历史；一次任务执行有自己的开始、结果与失败；长期经历、判断和承诺则可能跨越多个 session。新建会话、结束任务与改变长期身份，分别属于不同的操作。

这些边界为不同的 Agent 应用留下空间。当前已实现会话持久化与恢复；Nagi 已有联系人的持续对话与事项独立工作会话，联系人、事项、资料和补充等应用层状态由 GUI store 保存（[#118](docs/design/desktop.md#118-独立工作会话与事项-ui实现事实2026-10-25)、[#141](docs/design/desktop.md#141-产品事项与执行关联实现事实2026-10-24)）。补充自动继续、wait/answer 续跑等有限跟进路径已实现；GUI/TUI 围绕同一工作跨端接续仍是产品目标，完整的跨端协调与迁移仍待交付。联系人持续存在不以完整人格或成长机制为前提。

### 长期研究：让经历影响未来的行动

我们希望 Agent 能延续共同项目的背景，记住有用的方法，在遇到反例时修正旧判断。人格与协作习惯也应当能在长期经历中逐渐形成，而不是完全依靠预先写好的角色描述。

当前研究围绕四个概念展开：

- **Experience（经历）**：保留交互、工具行动与结果，以及可追溯的来源。
- **Belief（判断）**：从经历中形成有适用条件、允许不确定和反对证据的判断。
- **Reflection（反思）**：有选择地重新解释经历，提出局部修正。
- **Intention（意图）**：将当前目标和相关判断连接到下一步行动；执行仍遵循宿主提供的权限。

记录应尽量便宜，反思应有选择，长期变化需要谨慎。运行时只引入当前任务相关的少量状态，为实际工作保留上下文和成本预算。

这部分仍是研究方向，不作为 Nagi 产品成立或首版可用的前置。独立实验已比较反思所得判断、原始经历与无历史条件，但目前的小样本结果尚未证明稳定收益，成长机制也尚未接入主运行路径。我们会以任务质量、用户投入、成本和行为稳定性评价它的价值；如果直接检索经历已经足够，就优先选择更简单的实现。参见[成长实验报告](docs/experiments/growth-pilot-report.md)。

### 用可观察的行为验证设计

一次消息被接受、一次模型请求开始、一次工具调用完成，是不同的事实。Runtime 应让调用方看到发生了什么，以及失败发生在哪里。

同样，历史保存、上下文摘要与长期学习各自解决不同的问题。当前自动压缩用于管理有限的模型工作上下文，原始历史继续保留；摘要质量仍有边界，会话恢复本身也不能证明 Agent 已经学会了过去的经验。

## 当前进度

运行能力与候选以 `7145225` 基线核对；完整产品状态、跨线依赖与候选证据见 [#140](https://github.com/HeimaoLST/Saki/issues/140)、[#156](https://github.com/HeimaoLST/Saki/issues/156) 与 [#200](https://github.com/HeimaoLST/Saki/issues/200)。本文档接入不改变代码或已打包产物的 SHA。

| 范围 | 当前状态 |
| --- | --- |
| 模型调用 | 统一 `ModelClient` 支持 Responses（默认）与 Chat Completions；共用模型／工具循环，具体能力与限制见[模型调用分层](docs/design/model-runtime.md) |
| 工具执行 | 已实现工具注册与调用，默认提供 `read`、`bash`、`edit`、`write`，支持有界输出与大输出落盘 |
| 工作上下文 | 已实现 usage 校准、自动 compact、有限超限恢复及 checkpoint |
| 会话 | 已实现 JSONL 历史、显式恢复、新建／切换／重命名；Runtime 管理执行归属，同一会话仍不能无约束并发写入 |
| TUI／console 展示 | 已接入 Rust 核心；TUI 提供工作记录、Markdown、工具展开、图文输入与会话操作 |
| GUI | `desktop/` 提供 MyGo 原生应用，已连接真实 Runtime；联系人、事项、独立工作、设置连接与结果阅读已有实现，完整 MVP 与原生验收仍按 #140/#156 推进，设计原型不等于产品交付 |
| 多 session 重叠执行 | Runtime 已有容量配置与多会话执行实现；默认容量为 1，GUI 的产品并发验收单独推进，见 [Runtime API](docs/design/runtime-api.md) |
| 联系人与事项 | 已有联系人资料保存未来默认值、事项与独立工作会话、补充自动继续、文字与 UTF-8 文件资料快照、结果阅读等实现（[#152](docs/design/desktop.md#152-联系人管理与已保存对话候选实现2026-10-26)、[#117](docs/design/desktop.md#117-资料快照阅读与递交候选实现2026-10-26)、[#142](docs/design/desktop.md#142-事项补充与自动继续候选实现2026-10-26)）；GUI/TUI 跨端接续和完整 MVP 仍按 #140/#156 验收，不等于已经交付 |
| 长期成长 | 已有设计与独立实验，尚未接入主 Agent |

## 运行终端展示

准备支持 Rust 2024 edition 的 Rust 工具链，以及支持所选 Responses 或 Chat Completions 协议的模型服务。模型服务需提供所用场景需要的 function tools 能力；图片输入还受具体协议与模型支持限制。

在项目根目录创建 `.env`，将下面的占位值替换为实际配置：

```dotenv
API_KEY=your-api-key
SAKI_API=openai-responses
SAKI_BASE_URL=https://your-provider.example/v1
SAKI_MODEL=your-model-id
```

`SAKI_API` 默认是 `openai-responses`，也可选择 `openai-chat-completions`；未知值会在发请求前报错。`SAKI_BASE_URL` 是 API 基础地址，按所选协议追加 `/responses` 或 `/chat/completions`。配置也可直接通过进程环境变量提供。

```sh
cargo run
```

例如使用 Chat Completions，保留上述 `API_KEY`、`SAKI_BASE_URL` 与 `SAKI_MODEL`，只切换协议：

```sh
SAKI_API=openai-chat-completions cargo run
```

Chat 首版支持文字和 function tools，不支持图片输入，也不能把 Responses 的 ProviderItem continuation 跨协议搬运；已有会话不支持跨协议恢复，API 不匹配时明确拒绝。Chat 适配器保留并回传供应商返回的 `reasoning_content`/`ProviderData`，包括 #177 对 `null`、空字符串与字段缺失的区分；`reasoning_effort` 等未建模参数仍在请求前拒绝。已有 Magpie DeepSeek Flash 的有限续接证据见 [#177](https://github.com/HeimaoLST/Saki/issues/177) 与 [#207](https://github.com/HeimaoLST/Saki/issues/207)，不能扩称所有 DeepSeek、thinking 模型或兼容服务均支持。完整能力、已实测组合与失败边界见[模型调用分层](docs/design/model-runtime.md)，恢复契约见[会话管理](docs/design/session-management.md)。

stdin 与 stdout 都是终端时默认使用 TUI；管道与重定向自动使用 console。也可显式选择纯文本展示：

```sh
SAKI_UI=console cargo run
```

默认每次启动创建新会话。输入 `/sessions` 查看已保存的会话，使用 `/resume <id>` 恢复；也可以在启动时指定：

```sh
SAKI_SESSION=your-session-id cargo run
```

历史默认保存在当前工作目录的 `.saki/sessions/`。恢复会话会读取已保存状态，不自动重跑未完成的模型或工具任务。更多操作见[会话管理](docs/design/session-management.md)、[本地命令](docs/design/command-system.md)和 [TUI 使用说明](docs/design/tui.md)。

## 运行桌面应用（macOS arm64）

Nagi（GUI 暂定名）以联系人为入口，通过 GUI 连接真实 Agent Runtime。当前只支持 macOS arm64，以及已经明确验证过的模型协议组合；其他系统或仅 API 名称兼容的方案，在没有真实运行证据前不列为已支持。当前提供的是开发候选，不是公开发布的公证安装版；ad-hoc 签名只用于开发验证，不应写成公开安装包，也不要绕过系统安全警告。

首次试用不需要手动编辑 `.env`。从当前开发候选启动后，按应用内完整路径配置：

1. 打开 **Settings -> Connection & model**，填写 protocol、API base、model、key 和 default working directory。
2. 选择 **Save configuration**，再执行 **Apply saved**；需要确认供应商是否真正接受请求时，显式执行 **Test connection and model**。
3. 添加 **Add contact**，在 **Contact profile** 中按需补充职责或工作要求；联系人名称始终可编辑。
4. 在聊天中交办事项；在 **Work** 中查看事项、执行状态和结果，有结果时使用 **Read latest result** 打开阅读。

Test connection and model 会使用当前表单值发起一次短模型请求，用于检查连接与模型的基本可用性；保存和应用只写入/重载配置，不是验证。Test 成功只证明本次短请求通过，不代替工具调用、复杂或长任务、上下文资料或所有模型组合的验收；失败也不改写已运行工作的会话快照。已经运行的 session 保留其配置快照，修改连接不会静默改写正在运行的工作；需要新配置时使用新会话或明确的模型切换路径。候选版本、完整 MVP 验收和原生限制分别见 [#156](https://github.com/HeimaoLST/Saki/issues/156)、[#200](https://github.com/HeimaoLST/Saki/issues/200) 与[桌面应用文档](docs/design/desktop.md)。

## 从哪里开始理解与构建

当前源码按职责组织，应用组装入口位于 [`src/app.rs`](src/app.rs)：

| 路径 | 职责 |
| --- | --- |
| [`src/agent/`](src/agent/) | 模型／工具执行循环、事件、工作上下文与历史日志 |
| [`src/session/`](src/session/) | 会话存储、恢复与管理请求 |
| [`src/protocol/`](src/protocol/) | 模型请求、消息、结果、usage 与工具声明 |
| [`src/client/`](src/client/) | 模型协议适配、HTTP 与流式响应解析 |
| [`src/model/`](src/model/) | 模型目录、匹配与容量信息 |
| [`src/tools/`](src/tools/) | 工具注册、内置工具与输出处理 |
| [`src/frontend/`](src/frontend/) | TUI 与 console 输入、展示适配 |
| [`desktop/`](desktop/) | 通过 stdio 连接 Agent Runtime 的独立 Go GUI |
| [`examples/`](examples/) | 独立实验入口与实验材料 |

构建自己的宿主时，可以从 `RuntimeHandle` 与 [Runtime API](docs/design/runtime-api.md) 理解调用和事件边界；`SessionCommand`／`SessionEvent` 是终端适配接口。扩展工具可以从 `ToolRegistry` 与现有工具入手。应用负责组装，Runtime 保持唯一的执行循环。具体契约见[模型调用分层](docs/design/model-runtime.md)和[工具实现](docs/design/builtin-tools.md)。

完整文档入口是 [`docs/README.md`](docs/README.md)。GUI 的权威产品定义见 [Nagi 产品定义与需求](docs/requirements/desktop.md)；长期状态研究见[持续身份研究](docs/design/persistent-identity.md)，经历如何影响行动见[研究架构提案](docs/design/experience-to-behavior.md)。文档中的产品需求、实现事实、设计提案与待验证问题分别标注。

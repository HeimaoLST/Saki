# Saki、Nagi 与 Rous 的独立仓库

更新日期：2026-11-09。状态：用户已确认的产品边界与实施约束；迁移阶段、代码评审和验收证据以 [Saki#333](https://github.com/HeimaoLST/Saki/issues/333) 为准，不把方案或旧测试当新交付。

## 已采用的边界

| 仓库 | 唯一职责 | 对其他产品的关系 |
| --- | --- | --- |
| [Saki](https://github.com/HeimaoLST/Saki) | Agent Runtime：模型、工具/MCP、context/compact、session/task、持久化、容量、取消及公开执行事实 | 对外接口属于 Runtime；不依赖任何产品前端 |
| [Nagi](https://github.com/HeimaoLST/Nagi) | 联系人、事项、要求、补充/澄清、完成规则、产品工具与桌面 UI | 通过 version 1 stdio 消费独立 Saki 产物 |
| [Rous](https://github.com/HeimaoLST/Rous) | 终端输入/展示、本地命令、草稿、选中会话和终端生命周期 | 通过同一公开服务协议消费 Saki，不链接执行核心 |

各仓库拥有自己的 Git 工作目录、版本、构建、测试和 CI。消费者不能通过相邻目录导入 Saki 源码或自行读写 Runtime journal。显示投影使用公开返回的数据，不重建 Conversation/ToolRegistry，不实现另一套模型/工具循环。

对外服务并不意味着本次新增常驻 daemon。当前继续使用调用方拥有的独占 stdio 进程；多客户端常驻、远程执行、事件续传、durable queue 和自动重试都不由仓库拆分隐含授权。

## 从真实消费者完善 Runtime

Rous 原终端适配依赖内部 Replay、会话类型与上下文估算。迁移把通用事实集中到 Runtime 的 `session.snapshot`，并为异步标题/配置变化提供显式 `runtime.metadata.subscribe`。协议字段、边界与错误只在 [Runtime API](runtime-api.md) 维护。

Runtime 单独构建不带终端绘制、剪贴板、Go/MyGo 或 Nagi。模型调用、工具、compact、store/journal 与既有执行 actor 继续各自只有一套实现。Nagi 事项 MCP helper 及产品状态仍属于 Nagi；Saki 仅消费通用工具配置，不理解联系人或事项完成规则。

## 来源与保留成果

Runtime 的迁移基线是 `dev@ddf1dfbfce134dcc084188126737d2b91956091b`。Nagi 和 Rous 从组合候选 `00298ae22bb95afc11366597e517a3b1ceba21e7` 导出；该候选包含 #322/#324/#327/#328 与已集成 #331 的成果。两消费者的 `docs/design/source-import.json` 记录原路径、目标路径及 blob SHA。

[导出工具](../../scripts/export-products.py)克隆固定 Git 提交、保留原始提交历史、移除源 remote，再以普通迁移提交形成独立产品；不会重写原仓库、force-push 或夹带主检出未跟踪文件。该方式保留完整原历史，代价是新仓库包含拆分前的历史对象；未来当前构建不依赖那些历史文件。

旧 PR 和证据继续链接原仓库，不伪造跨仓库 PR 历史。PR326 的旧候选与包、CI、有限原生证据保留原身份；新仓库源码变化需要重新记录实际版本和验证。完整 Nagi MVP、六组原生验收、完整真实 A/B 与真人使用仍未完成，不因迁移关闭。

## 数据与行为兼容

既有 session/task/message IDs、metadata schema 1/2、API/connection identity、journal/tool-output 继续有效。Nagi 的既有产品 store 和会话映射继续归 Nagi；本次保留旧数据位置和 bundle identity，不静默迁移或生成替代会话。

Nagi 产品版本独立于 Saki Cargo.version。Nagi.app 可组合明确指定的预构建 Runtime，manifest 分别记录两端版本、源码/来源和 binary hash；便利捆绑不要求两仓锁步发布。

Saki 的默认 CLI 现在是机器服务，`--stdio` 保持兼容；原终端入口移至 `rous`。原终端的 fresh/显式恢复、草稿、历史/context、标题、定向停止与终端恢复属于 Rous 的迁移验收。自动标题由 Rous 独占宿主显式启用，现有 Nagi/stdio 默认关闭不变。

提交接纳、开始、模型请求、工具结果、任务终态和产品事项完成仍是不同事实。查询/恢复不重跑执行；取消只请求协作停止；输出丢失、落盘失败和回包丢失保持不确定性，不自动重试副作用。

## 独立交付与回滚

Saki 检查自身 Rust 行为和真实 stdio 流程，产物记录其源码 SHA/run/attempt/hash。Nagi/Rous 检查自身产品行为，再对已核身份的 Runtime 产物执行真实兼容回归，缺失或跳过必需场景不能通过。当前实现不创建 tag、GitHub Release 或部署。

原 Saki master 与旧候选、各迁移前提交均保留，可回退对应代码或使用原产物；数据格式和目录本轮不改变，不需要破坏性数据回滚。Saki 普通集成目标为 dev，生产 master 发布仍按单独范围与门禁执行。两个 Herdr space 仅管理各自仓库，不是源码边界的替代品；Rous 本轮不单设 space。

---
description: 以指定业务线/实例身份，按 AGENTS.md 的 Spec 驱动流程开工
argument-hint: "<line>#<name>"
---
你是 agent:$1。先主动完整读取仓库根目录 `AGENTS.md`（不要假设工具会自动加载），并确认当前 Spec 的固定评论、版本、owner、可写路径、base 与目标分支。

- 参数应为 `<line>#<name>`（例如 `runtime#kira`）。业务线不明确时先确认；仅缺实例名或名字冲突时选择新的唯一实例名并报告，不冒用他人身份。
- PM 负责写 Spec、分派唯一 owner、验收、集成顺序与 PR 收尾；发布协调由 PM 组织，发布授权由用户决定。业务主 agent 接受精确 Spec 版本后组织实施。以下 inspect/GitHub ACK 只针对独立主责；child 只执行父 agent 的 tmux 分派，不巡检/认领/走 GitHub。
- 开始通知/接收前使用 `python3 scripts/agent_notify.py inspect --agent-id <id>` 动态核验唯一主 pane；不要使用固定 pane 编号或继承的 `TMUX_PANE` 推断归属。PM 用 `send` 绑定已核指纹、稳定 ID 与固定评论 URL；收到后必须以 GitHub 顶层 ACK（`FROM`/`KIND: ack`/`ACK`/`SPEC`/`SCOPE`/`NEXT`）作为接收。Enter 成功或通知运输成功不等于接收；脚本失败不重试 Enter。
- 先清点遗留：核对未接收的 handoff、返修未闭环、已合并且验收通过但未收尾、阻塞与只读任务；能处理就处理，不能处理明确下一步。需要盘点时运行只读 `python3 scripts/collaboration_check.py --limit 50`，它只给候选，不改标签、不关 issue、不删资源。
- 文件修改在指定 worktree 内完成，主 agent 对实现、commit/push、测试与逐条 AC 证据负责；子 agent 的具体 Git 动作以主 agent 明确分派为准。`scripts/agent-worktree.sh` 只提供 add/list/path/安全 remove；finish/prune/--force 已移除，不自动删除分支或远端。
- **merge 只按 AGENTS §0.2**：记录该 PR 的明确授权来源与评审/AC/CI 的 SHA；PM 组织集成、Ready、CI 绿、ACK 都不代替授权。发布授权由用户决定；breaking change 按 §0.2／§8 升级人类。`master` 保持生产稳定，`dev` 是开发集成分支。
- 只读任务交付报告；未获任务或队列执行授权时只读巡检。不要自行增加验收项、缩减 Spec 或把通知运输成功写成任务已接收。

本次身份：`agent:$1`

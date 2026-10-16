---
description: 以指定业务线/实例身份，按 AGENTS.md 开工
argument-hint: "<line>#<name>"
---
你是 agent:$1。先主动完整读取仓库根目录 `AGENTS.md`（不要假设工具会自动加载），再按下面执行。

- 参数应为 `<line>#<name>`（例如 `runtime#kira`）。业务线不明确时先确认；仅缺实例名或名字冲突时选择新的唯一实例名并报告，不冒用他人身份。
- 先确认角色：独立任务主责按 §5 巡检／认领；由主 agent 通过 tmux 分派的子 agent，只执行该范围，不另巡检认领、不改标签、不通过 GitHub 与主 agent 对话。
- 主 agent 按 §2.2 动态发现并核对父子 pane 的稳定实例 ID、任务与状态；不用固定 session／pane 编号或仅凭继承的 `TMUX_PANE` 定位。通过 split-window、send-keys／paste-buffer 与 capture-pane 分派和监督。
- 子 agent 通过 pane 回报结果与日志路径，主 agent 复核并汇总关键证据到 GitHub；跨独立业务线的需求和交接仍按标签路由。
- 已授权任务的文件修改在任务 worktree 内完成，主 agent 对 commit／push／PR 闭环负责，可明确分派相应步骤；子 agent 未获分派不自行执行其他 Git 动作。只读任务交付报告，base 与 issue 引用按 §7。
- 未获任务或队列执行授权时：只读巡检并报告 `ready`／`review`／`blocked` 状态，不自行扩展范围或创建空 worktree。
- 不自动 merge／auto-merge。`master` 是生产稳定分支，`dev` 是开发集成分支；清理只针对 §9 条件已满足的自有临时资源，保留长期分支。授权例外与独立评审按 §0／§5 执行。

本次身份：`agent:$1`

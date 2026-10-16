---
description: 以指定业务线/实例身份，按 AGENTS.md 开工
argument-hint: "<line>#<name>"
---
你是 agent:$1。先主动完整读取仓库根目录 `AGENTS.md`（不要假设工具会自动加载），再按下面执行。

- 参数应为 `<line>#<name>`（例如 `runtime#kira`）。业务线不明确时先确认；仅缺实例名或名字冲突时选择新的唯一实例名并报告，不冒用他人身份。
- 已指定任务／issue，或已授权从业务线队列认领时：按 §5 核对 DoR、认领与文件范围。只读任务交付报告；文件修改在独立 worktree 中完成，按 §7 commit／push／开 PR，base 与 issue 引用以实际目标为准。
- 未获任务或队列执行授权时：只读巡检并报告 `ready`／`review`／`blocked` 状态，不自行扩展范围或创建空 worktree。
- 不自动 merge／auto-merge。`master` 是生产稳定分支，`dev` 是开发集成分支；清理只针对 §9 条件已满足的自有临时资源，保留长期分支。授权例外与独立评审按 §0／§5 执行。

本次身份：`agent:$1`

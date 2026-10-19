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
- 开工先清点遗留：核对未接收的 `handoff`、`REQUEST-CHANGES` 未闭环、已合并但未 `done`／未关闭的收尾；能处理就处理，不能处理就明确移交或记下一步。已提交待审、等外部评审（`needs:review`）、阻塞、只读研究与未完成任务不算未收尾，不阻止开展不冲突的新任务。结束前按 AGENTS.md §5 步骤 10 按真实阶段分流核对，并给保留资源写明用途／解除条件。
- 需要盘点时运行只读检查 `python3 scripts/collaboration_check.py --limit 50`；它只给候选，不改标签、不关 issue、不删资源。
- `agent:pm` 可通过动态核实的 tmux 通知其他独立主 agent 读取 GitHub 任务；正式接收／决定／证据仍在 GitHub，不直接派他线子 agent、不打断执行中的任务、不按固定 pane 编号。
- 未获任务或队列执行授权时：只读巡检并报告 `ready`／`review`／`blocked` 状态，不自行扩展范围或创建空 worktree。
- 非 breaking change 已获用户持续预授权：独立 agent 评审、必需检查与适用 QA 通过后，由主 agent 按 §5 自行合并并收尾，不逐 PR 询问用户；子 agent 的 merge 动作须由主 agent 明确分派。breaking change 按 §0.2／§8 升级人类。`master` 保持生产稳定，发布需确定范围并全套验证；`dev` 是开发集成分支。清理按 §9，保留长期分支。

本次身份：`agent:$1`

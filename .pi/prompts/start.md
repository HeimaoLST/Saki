---
description: 以指定业务线/实例身份，按 AGENTS.md 开工
argument-hint: "<line>#<name>"
---
你是 agent:${1:-runtime#default}。业务线取 `#` 前的部分，实例名取 `#` 后的部分。

先完整阅读仓库根目录的 `AGENTS.md`，然后按 §0 快速开工与 §5 标准工作循环执行：

1. 巡检队列：`gh issue list --state open --label "agent:<line>,status:ready"`、`status:review`，以及仓库的 `status:blocked`。
2. 认领一个 issue（或从 triage 开始），用 `./scripts/agent-worktree.sh add agent/<line>/<issue>-<slug>` 开独立 worktree。
3. 不要在主检出 `git switch`；编码完成后自行 commit + push，然后开 PR 到 `dev`，用 `Closes #N` 关联 issue。
4. 合并后按 §9 清理：`./scripts/agent-worktree.sh finish <branch>`。

本次身份：`agent:${1:-runtime#default}`

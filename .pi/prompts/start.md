你是 agent:$ARGUMENTS。先完整阅读本工作区 AGENTS.md，核对实际路径/分支、脏文件来源和用户授权。

本仓库只开发 Saki Runtime；Nagi/Rous分别在独立仓库。核对当前任务或已授权队列、唯一主责、Spec版本、路径、base和AC后再动手。主检出固定master，开发进入独立worktree。

主agent使用hc动态核对成员身份/任务/状态，再在既定范围内派活和监督；子agent只执行本地分派并向主agent回报，不自行认领、发GitHub或启动更多agent。不得沿用旧tmux地址。

主责负责实际测试、评审、commit/push/PR和验收闭环；Git权限与合并按实际AGENTS及当前用户授权，不把CI绿当产品完成，不扩展为tag/Release/部署。

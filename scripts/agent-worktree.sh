#!/usr/bin/env bash
# Agent 实例的 git worktree 管理器。
#
# 主检出固定 master；每个 agent 实例/活跃 issue 用独立 worktree 开发，
# 避免共享 cwd 下一个实例切分支导致所有实例跟着切。规则见 AGENTS.md §9。
#
# worktree 放在 Workspace 级的 worktrees/Saki/ 下（默认 $HOME/Workspace，
# 可用 SAKI_WORKTREE_ROOT 覆盖整个 worktree 根目录），目录名为分支名中的
# "/" 替换为 "-"。不放仓库内，也不依赖语言分类目录，便于仓库未来迁移。
#
# 用法：
#   ./scripts/agent-worktree.sh add <branch> [base]   # base 默认 origin/master
#   ./scripts/agent-worktree.sh list
#   ./scripts/agent-worktree.sh path <branch>
#   ./scripts/agent-worktree.sh remove <branch> [--force]
set -euo pipefail

WORKTREES_DIR="${SAKI_WORKTREE_ROOT:-$HOME/Workspace/worktrees/Saki}"

slug() { printf '%s' "$1" | tr '/' '-'; }
dir_for() { printf '%s/%s' "$WORKTREES_DIR" "$(slug "$1")"; }

usage() {
  sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
}

case "${1:-}" in
  add)
    branch="${2:?用法: add <branch> [base]}"
    base="${3:-origin/master}"
    dir="$(dir_for "$branch")"
    mkdir -p "$WORKTREES_DIR"
    if git show-ref --verify --quiet "refs/heads/$branch"; then
      git worktree add "$dir" "$branch"
    else
      git worktree add -b "$branch" "$dir" "$base"
    fi
    echo "worktree: $dir"
    ;;
  list)
    git worktree list
    ;;
  path)
    branch="${2:?用法: path <branch>}"
    dir_for "$branch"
    echo
    ;;
  remove)
    branch="${2:?用法: remove <branch> [--force]}"
    dir="$(dir_for "$branch")"
    if [ "${3:-}" = "--force" ]; then
      git worktree remove --force "$dir"
    else
      git worktree remove "$dir"
    fi
    echo "removed: $dir"
    echo "分支未删除；确认已合并后运行: git branch -d $branch"
    ;;
  *)
    usage
    ;;
esac

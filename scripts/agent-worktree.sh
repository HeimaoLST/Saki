#!/usr/bin/env bash
# Agent worktree helper: create/list/path/remove only.
#
# PM owns integration and cleanup. Branch deletion and automatic pruning are
# deliberately absent; finish/prune invocations fail without touching resources.
set -euo pipefail

WORKTREES_DIR="${SAKI_WORKTREE_ROOT:-$HOME/Workspace/worktrees/Saki}"
INTEGRATION_BRANCH="${SAKI_INTEGRATION_BRANCH:-dev}"

slug() { printf '%s' "$1" | tr '/' '-'; }
dir_for() { printf '%s/%s' "$WORKTREES_DIR" "$(slug "$1")"; }

usage() {
  cat >&2 <<'EOF'
用法:
  ./scripts/agent-worktree.sh add <branch> [base]
  ./scripts/agent-worktree.sh list
  ./scripts/agent-worktree.sh path <branch>
  ./scripts/agent-worktree.sh remove <branch>
EOF
  exit 2
}

remove_worktree() {
  local branch="$1" dir current
  dir="$(dir_for "$branch")"
  if [ ! -d "$dir" ]; then
    echo "worktree 不存在: $dir" >&2
    return 1
  fi
  current="$(pwd -P)"
  if [ "$(cd "$dir" && pwd -P)" = "$current" ]; then
    echo "不能在自身 worktree 内移除: $dir" >&2
    return 1
  fi
  git worktree remove "$dir"
  echo "已移除 worktree: ${dir}（分支未动；由 PM 核验后处理）"
}

case "${1:-}" in
  add)
    branch="${2:?用法: add <branch> [base]}"
    base="${3:-origin/$INTEGRATION_BRANCH}"
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
    branch="${2:?用法: remove <branch>}"
    if [ "$#" -ne 2 ]; then
      echo "remove 不接受额外参数（--force 已移除）" >&2
      exit 2
    fi
    remove_worktree "$branch"
    ;;
  finish|prune)
    echo "已移除自动清理入口: $1；请由 PM 核验后手动处理" >&2
    exit 2
    ;;
  *)
    usage
    ;;
esac

#!/usr/bin/env bash
# Agent 实例的 git worktree 管理器。
#
# 主检出固定 master；每个独立开发 issue 使用隔离 worktree，
# 同任务主子 agent 的文件范围与 Git 操作由主 agent 协调。规则见 AGENTS.md §9。
#
# worktree 放在 Workspace 级的 worktrees/Saki/ 下（默认 $HOME/Workspace，
# 可用 SAKI_WORKTREE_ROOT 覆盖整个 worktree 根目录）。不放仓库内，也不依赖
# 语言分类目录，便于仓库未来迁移。
#
# 分支模型：master 稳定、dev 集成（默认分支）、agent/* 工作分支。
# 工作分支默认从 origin/dev 开出；可用 SAKI_INTEGRATION_BRANCH 覆盖集成分支名。
#
# 用法：
#   ./scripts/agent-worktree.sh add <branch> [base]         # base 默认 origin/dev
#   ./scripts/agent-worktree.sh list
#   ./scripts/agent-worktree.sh path <branch>
#   ./scripts/agent-worktree.sh remove <branch> [--force]
#   ./scripts/agent-worktree.sh finish <branch> [--force]   # 收尾：worktree + 本地/远端分支
#   ./scripts/agent-worktree.sh prune                       # 清理已合并分支与失效 worktree
set -euo pipefail

WORKTREES_DIR="${SAKI_WORKTREE_ROOT:-$HOME/Workspace/worktrees/Saki}"
INTEGRATION_BRANCH="${SAKI_INTEGRATION_BRANCH:-dev}"

slug() { printf '%s' "$1" | tr '/' '-'; }
dir_for() { printf '%s/%s' "$WORKTREES_DIR" "$(slug "$1")"; }

worktree_for_branch() {
  git worktree list --porcelain | awk -v target="refs/heads/$1" '
    /^worktree / { path = substr($0, 10) }
    /^branch / { if ($2 == target) print path }
  '
}

remote_branch_exists() {
  git ls-remote --exit-code --heads origin "$1" >/dev/null 2>&1
}

delete_remote_branch() {
  if remote_branch_exists "$1"; then
    git push origin --delete "$1"
  fi
}

pr_merged() {
  command -v gh >/dev/null 2>&1 || return 1
  local count
  count="$(gh pr list --head "$1" --state merged --json number -q 'length' 2>/dev/null)" || return 1
  [ -n "$count" ] && [ "$count" != "0" ]
}

is_merged() {
  git merge-base --is-ancestor "refs/heads/$1" "origin/$INTEGRATION_BRANCH" 2>/dev/null && return 0
  # Squash merges rewrite history, so the branch tip is not an ancestor of dev.
  pr_merged "$1"
}

remove_worktree() {
  local branch="$1" force="${2:-}" dir current
  dir="$(dir_for "$branch")"
  [ -d "$dir" ] || return 0
  current="$(pwd -P)"
  if [ "$(cd "$dir" && pwd -P)" = "$current" ]; then
    echo "跳过当前所在的 worktree：$dir" >&2
    return 1
  fi
  if [ "$force" = "--force" ]; then
    git worktree remove --force "$dir"
  else
    git worktree remove "$dir"
  fi
  echo "已移除 worktree: $dir"
}

usage() {
  sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
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
    branch="${2:?用法: remove <branch> [--force]}"
    remove_worktree "$branch" "${3:-}" || true
    echo "分支未删除；确认已合并后运行: $0 finish $branch"
    ;;
  finish)
    branch="${2:?用法: finish <branch> [--force]}"
    force="${3:-}"
    remove_worktree "$branch" "$force" || exit 1
    git fetch --prune --quiet origin || echo "警告：git fetch 失败，继续本地清理" >&2
    if git show-ref --verify --quiet "refs/heads/$branch"; then
      if is_merged "$branch"; then
        # Squash merges rewrite history, so -d can refuse a branch whose PR is
        # already merged; keep -d when possible and fall back to -D otherwise.
        git branch -d "$branch" 2>/dev/null || git branch -D "$branch"
        echo "已删除本地分支: $branch"
      elif [ "$force" = "--force" ]; then
        git branch -D "$branch"
        echo "已强制删除本地分支: ${branch}（内容未合入 origin/${INTEGRATION_BRANCH}）"
      else
        echo "本地分支未合入 origin/${INTEGRATION_BRANCH}，保留: ${branch}（确认合并后重试，或加 --force）" >&2
      fi
    fi
    delete_remote_branch "$branch"
    ;;
  prune)
    git fetch --prune --quiet origin || echo "警告：git fetch 失败，继续本地清理" >&2
    git worktree prune
    while IFS= read -r branch; do
      [ -z "$branch" ] && continue
      [ "$branch" = master ] && continue
      [ "$branch" = "$INTEGRATION_BRANCH" ] && continue
      if ! is_merged "$branch"; then
        echo "跳过未合并分支: $branch" >&2
        continue
      fi
      if [ -n "$(worktree_for_branch "$branch")" ]; then
        remove_worktree "$branch" || continue
      fi
      if git branch -d "$branch" >/dev/null 2>&1 || git branch -D "$branch" >/dev/null 2>&1; then
        echo "已删除已合并本地分支: $branch"
      else
        echo "跳过（仍被占用）: $branch" >&2
      fi
    done < <(git for-each-ref --format='%(refname:short)' refs/heads)
    git worktree prune
    ;;
  *)
    usage
    ;;
esac

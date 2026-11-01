#!/usr/bin/env bash
# 创建 / 更新 Saki 多 agent 协作标签体系（可重复执行）。
#
# 用法:
#   ./scripts/setup-labels.sh
#
# 需要 GitHub CLI（gh）已登录，且对仓库有写权限。
# 标签含义与状态机见仓库根目录 AGENTS.md §4。

set -euo pipefail

if ! command -v gh >/dev/null 2>&1; then
  echo "错误：未找到 GitHub CLI（gh）。安装见 https://cli.github.com/" >&2
  exit 1
fi

auth_ok=0
for attempt in 1 2 3; do
  if gh auth status >/dev/null 2>&1; then
    auth_ok=1
    break
  fi
  echo "  登录状态检查失败（第 ${attempt} 次），重试..." >&2
  sleep 2
done

if [ "$auth_ok" -ne 1 ]; then
  echo "错误：gh 未登录或 API 不可达（已重试 3 次）。请先运行 gh auth login / gh auth refresh" >&2
  exit 1
fi

labels=(
  # 类型 type:* —— 分诊完成后恰好 1 个
  "type:bug|d73a4a|行为与预期不符，需要修复"
  "type:feature|a2eeef|新能力或行为增强"
  "type:test|0e8a16|需要补充/修复测试或 CI 自动检查"
  "type:refactor|c5def5|不改变外部行为的重构"
  "type:docs|0075ca|文档新增或修正"
  "type:research|7057ff|调研、实验、证据核对"
  "type:decision|5319e7|架构/协议决策（RFC/ADR）"
  "type:question|d876e3|需要澄清的问题"

  # 状态 status:* —— 每个 issue 恰好 1 个
  "status:triage|ededed|待分诊：补类型、归属、优先级与迭代安排"
  "status:ready|c2e0c6|已就绪，等待负责业务线认领"
  "status:in-progress|fbca04|唯一主责实例组织执行，子任务通过 tmux 分派"
  "status:review|1d76db|待评审、验收或合并；breaking change 等人类决定"
  "status:blocked|b60205|被依赖阻塞（正文写 BLOCKED-BY: #N 或具体原因与解除条件）"
  "status:done|0e8a16|验收通过且交付完成，待关闭/已关闭"

  # 归属 agent:* —— 业务线（每条业务线可有多个实例），分诊完成后恰好 1 个
  "agent:runtime|006b75|Agent Runtime 开发：执行循环/模型/工具/会话/协议"
  "agent:tui|6f42c1|TUI 开发：TUI/console/本地命令"
  "agent:gui|00add8|GUI 开发：desktop Go contacts 预览"
  "agent:qa|0e8a16|CI 工程与自动检查运行可靠性，不承担产品人工验收"
  "agent:pm|fbca04|产品经理：需求/优先级/文档分类/协作配置与验收标准"

  # 优先级 priority:* —— triage 时确定
  "priority:p0|b60205|阻断/安全/数据损坏，立即处理"
  "priority:p1|d93f0b|当前迭代必须完成"
  "priority:p2|fbca04|计划内，可排队"
  "priority:p3|c5def5|低优先，有空再做"

  # 协作需求 needs:* —— 可叠加，表示「等对方产出」
  "needs:design|d4c5f9|需要设计或决策产出"
  "needs:review|5319e7|需要代码/文档评审"
  "needs:test|0e8a16|需要补充/修复测试或 CI 检查，由实现线或 CI 工程处理"
  "needs:info|d876e3|需要补充信息"
  "needs:repro|e99695|需要最小复现"
  "needs:human|b60205|需要人类决策/介入"

  # 流转与其他
  "handoff|ff7619|独立任务主责交接；主子内部派活用 tmux"
  "epic|3e4b9e|跨多个 issue 的目标追踪"
  "platform:rust|dea584|涉及 Rust core"
  "platform:go|00add8|涉及 desktop Go 模块"
  "platform:ci|6e7781|涉及 CI/Actions/仓库配置"
)

create_with_retry() {
  local name="$1" color="$2" description="$3" attempt
  for attempt in 1 2 3; do
    if gh label create "$name" --color "$color" --description "$description" --force >/dev/null; then
      return 0
    fi
    echo "  第 ${attempt} 次失败，重试 ${name}..." >&2
    sleep 2
  done
  echo "错误：创建 ${name} 失败（网络或 API 错误）" >&2
  return 1
}

echo "创建/更新标签："
for entry in "${labels[@]}"; do
  IFS='|' read -r name color description <<<"$entry"
  create_with_retry "$name" "$color" "$description"
  printf '  ✓ %s\n' "$name"
done

echo
echo "完成，共 ${#labels[@]} 个标签。"
echo
echo "提示：以下旧标签已被替代，确认无引用后可删除："
echo "  bug / enhancement / documentation / question（已由 type:* 取代）"
echo "  agent:tools / agent:frontend / agent:desktop / agent:docs / agent:research（已由五条业务线接管）"

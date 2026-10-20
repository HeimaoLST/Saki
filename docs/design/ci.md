# CI governance（Runtime／TUI／GUI）

更新日期：2026-10-20。状态：**实现事实**（workflow、本地验证、真实 GitHub Actions 运行结果；`dev`／`master` 分支保护已于 2026-10-17 启用）。workflow 配置基线仍为 2026-10-17；2026-10-20 只调整职责描述，未修改 `.github/workflows`。

## 目的与范围

本仓库的 CI 目标不是“有一个绿色徽章”，而是让失败集成在 PR 阶段可见、可定位、可复验：

- Runtime 与 TUI 位于同一个 Rust crate，共享一组 Rust 门禁，不为两条线复制两套实现。
- GUI 是独立 `desktop/` Go module，使用 Go 门禁与 headless 预览产物。
- 所有 PR、`dev`／`master` 的 push 和手动触发使用同一套检查；首期不做路径分类器，避免 required check 因 workflow 被跳过而永久 pending。
- CI 通过只代表机器门禁通过，**不替代主 agent 代码评审、适用验收或 PM／用户真实试用**，也不授权 breaking change 的迁移。

## 职责调整（2026-10-20）

用户明确调整现阶段 QA 职责，避免团队继续按旧要求排队等待独立 QA：

- **QA**：只负责 CI 工程与自动检查的运行可靠性（现有 `.github/workflows`、CI 支撑脚本、失败定位与复验），不接产品人工验收或独立 `APPROVE` 队列。
- **实现 agent（runtime／tui／gui）**：负责功能实现与有价值的回归测试；模块行为测试归实现模块。
- **主 agent**：负责代码评审和适用实际运行／验收；可评审自己或子 agent 的产物，但须真实记录作者／评审者，不冒充独立评审或人类结论。独立 peer review 可按需，不是强制前置。
- **PM／用户**：负责真实试用与“是否好用”的判断；CI 绿不能证明用户体验。

本文 2026-10-17 出现的「独立评审」要求由上述主 agent 评审口径取代；workflow、required check 与分支保护的真实配置未因此改动，仍以仓库实际状态为准。

## 检查矩阵

| 业务线 | 检查 job | 实际命令 | 覆盖 |
| --- | --- | --- | --- |
| Runtime + TUI（共享 crate） | `rust` | `cargo fmt --all -- --check` | Rust 格式 |
| Runtime + TUI | `rust` | `cargo clippy --locked --all-targets -- -D warnings` | 所有 target 的静态检查，warnings 视为失败 |
| Runtime + TUI | `rust` | `cargo test --locked --all-targets` | 单元、集成与 example target（当前 313 主程序 + 42 成长实验；example tests 实际执行） |
| TUI（真实 PTY） | `rust` | `cargo test --locked --bin Saki frontend::tui::tests::pty::pty_panic_restores_terminal -- --ignored --exact --test-threads=1` | 真实 PTY 上进入／退出终端并验证 panic 后 cooked mode 恢复 |
| GUI（独立 Go module） | `gui` | `gofmt -l .`（必须为空）、`CGO_ENABLED=0 go vet ./...`、`go test ./... -count=1`、`go build` | 格式、vet、测试、构建 |
| GUI（可复现图像） | `gui` | `SAKI_PREVIEW_DIR=... go test -run TestPreviewImages -count=1 .` | headless `ui.Tester` 生成 light/dark add／settings 等场景 PNG，作为 artifact 保留 14 天 |

聚合检查：`ci-required`（`if: always()`，依赖 `rust` 与 `gui`）只在两者结果都为 `success` 时通过。`cancelled`／`failed`／`skipped` 都不算成功。若未来新增 job，必须同时更新 `ci-required` 的 `needs` 与判定。

稳定检查名：`rust`、`gui`、`ci-required`。分支保护只需要引用聚合检查 `ci-required`，避免逐个维护子 job 名称。

## 触发、并发与权限

- 触发：所有 `pull_request`；`push` 到 `dev`／`master`；`workflow_dispatch`。发布 PR（`dev` → `master`）、hotfix PR（→ `master`）与回流 PR（`master` → `dev`）因此都跑全量检查。
- 并发：同一 workflow + ref 分组；`pull_request` 的新推送取消旧运行，`dev`／`master` 的 push 不取消，保留最终集成证据。
- 权限：workflow 顶层 `permissions: contents: read`；不使用任何 secret、模型密钥、新付费服务。
- 运行平台：`ubuntu-24.04`（GitHub 官方 `ubuntu-latest` 当前指向的镜像；显式固定避免标签漂移）。
- 工具链：Rust 固定 `1.99.0`（rustfmt + clippy，官方 rustup 安装，已验证该版本可从 static.rust-lang.org 下载）；Go 由 `desktop/go.mod`（当前 `1.27.1`）通过 `actions/setup-go` 的 `go-version-file` 决定，不使用本机未来版本随意钉死不可下载版本。
- 缓存：Cargo registry／git／`target` 以 `runner.os + Rust 版本 + Cargo.lock` 为 key；Go 使用 `actions/setup-go` 内置缓存（`desktop/go.sum`）。缓存只加速，不改变 `--locked` 校验。
- 超时：Rust 30 分钟，GUI 20 分钟，聚合 5 分钟。

## 失败处理与责任

| 角色 | 责任 |
| --- | --- |
| 实现作者（runtime／tui／gui） | 维护受影响测试与命令，提供功能与有价值的回归；PR 前在本地执行同一组命令；CI 失败先定位并修复，不以 skip、`continue-on-error` 或放宽 lint 掩盖真实失败。 |
| QA | 维护现有 workflow、CI 支撑脚本与自动检查运行可靠性；核对 PR 的实际 run URL、SHA 与退出码；区分“本次改动失败”与“基线失败”，把真实失败反馈给对应 owner。不接产品人工验收或独立 `APPROVE` 队列，不代替作者修产品代码。 |
| PM | 维护需求边界、验收标准、协作规范与文档结构；确认 CI 约定与仓库流程一致；真实试用与用户体验判断由 PM／用户负责。 |
| 主责／合并者 | 主 agent 代码评审与适用验收通过、required CI 真实通过、无未解决阻塞且属非 breaking change 后，按 AGENTS.md §0.2 预授权合并；CI 绿不替代评审或用户试用，也不授权 breaking migration。 |

真实失败的处理顺序：

1. 打开对应 job 日志，记录失败命令、原始输出摘要与失败 SHA。
2. 判断是本次改动引入，还是 `dev` 基线已有；后者单独标注并交 owner，不能写成本次通过。
3. 修复后重新推送；分支保护启用时，需在最新 base 上重新通过 `ci-required`（strict 模式要求 up-to-date）。
4. 不通过修改 workflow 来绕开真实失败；确需调整门禁时，作为独立 PR 说明覆盖变化。

## 已知边界

- 私有仓库的 Actions 分钟数与套餐信息无法从当前 API 直接确认；以真实 PR run 的成功／失败为准。若 hosted runner 因账单或权限不可用，应记录 GitHub 返回的具体错误，不使用“套餐应该可以”作为结论。
- GUI 检查是 headless MyGo `ui.Tester`：验证布局、文字与确定性渲染，**不验证真实 macOS 窗口、键盘焦点或 IME**。
- TUI PTY fixture 验证真实 PTY 上的终端生命周期；键盘／渲染行为由 `cargo test --all-targets` 中的 TestBackend／单元测试覆盖。`/tmp` 中的多场景 PTY harness 依赖外部夹具与 mock，不作为本仓库 CI 的必需入口。
- 离线测试与本地 demo 回复不证明真实 provider、真实模型质量或 Rust runtime 端到端；这些仍按各自任务单独验收。
- 缓存命中与否不影响门禁结论；首次运行可能较慢。

## 分支保护（2026-10-17 已配置）

实现事实（2026-10-17 主 agent 应用并读回）：`dev`／`master` 均要求 `ci-required`（GitHub Actions app id `15368`，strict=true）；`enforce_admins=true`；PR 必需但 `required_approving_review_count=0`（共享账号不要求原生审批；主 agent 代码评审与适用验收仍按 AGENTS.md §5 执行）；force-push 与分支删除均禁止；同时核对 rulesets 为空。

以下 schema／runbook 保留用于变更前重读、复验或回滚；再次应用前仍须读取现状，若已有不同规则先合并而不是覆盖：

```sh
gh api repos/HeimaoLST/Saki/branches/dev/protection || true
gh api repos/HeimaoLST/Saki/branches/master/protection || true
gh api repos/HeimaoLST/Saki/rulesets
```

建议的 API 载荷（`ci-required` 绑定 GitHub Actions app id `15368`，已由真实 check-run 核对；PR 必需但不要求原生审批人数，避免共享账号无法自我审批；strict、禁 force-push／删除，保护规则同样约束 admin）：

```json
{
  "required_status_checks": {
    "strict": true,
    "checks": [{"context": "ci-required", "app_id": 15368}]
  },
  "enforce_admins": true,
  "required_pull_request_reviews": {
    "dismiss_stale_reviews": false,
    "require_code_owner_reviews": false,
    "required_approving_review_count": 0,
    "require_last_push_approval": false
  },
  "restrictions": null,
  "allow_force_pushes": false,
  "allow_deletions": false
}
```

应用示例（`dev`，`master` 同理）：

```sh
gh api -X PUT repos/HeimaoLST/Saki/branches/dev/protection \
  --input branch-protection-dev.json
```

发布／hotfix 流程仍按 AGENTS.md §7。结构化评论不能替代平台原生审批；若未来启用必须的审批人数，由 maintainer 处理账号层面限制。

## 复现命令

```sh
# Rust（Runtime + TUI）
cargo fmt --all -- --check
cargo clippy --locked --all-targets -- -D warnings
cargo test --locked --all-targets
cargo test --locked --bin Saki frontend::tui::tests::pty::pty_panic_restores_terminal -- --ignored --exact --test-threads=1

# GUI
cd desktop
gofmt -l .
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test ./... -count=1
CGO_ENABLED=0 go build -o /tmp/saki-desktop .
SAKI_PREVIEW_DIR=/tmp/saki-desktop-previews go test -run TestPreviewImages -count=1 .
```

## 一手来源（2026-10 核对）

- GitHub Actions workflow 语法、并发与权限：<https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax>
- 必需状态检查（rulesets）：<https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets#require-status-checks-to-pass-before-merging>
- 分支保护 REST API：<https://docs.github.com/en/rest/branches/branch-protection>
- Runner 镜像与 `ubuntu-latest` 对应版本：<https://github.com/actions/runner-images>
- Rust 工具链安装：<https://rust-lang.github.io/rustup/concepts/toolchains.html>；版本清单：<https://static.rust-lang.org/dist/channel-rust-stable.toml>
- Go 官方下载与版本：<https://go.dev/dl/>
- `actions/checkout`、`actions/cache`、`actions/upload-artifact`、`actions/setup-go` 官方仓库与 releases：<https://github.com/actions>
- actionlint（workflow 静态检查）：<https://github.com/rhysd/actionlint>

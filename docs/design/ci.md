# CI governance（Runtime／TUI／GUI）

更新日期：2026-11-01。状态：**实现事实（固定 dev 基线的 workflow 变更与本地验证；hosted run 待组合后记录）**；`dev`／`master` 分支保护已于 2026-10-17 启用。当前固定基线为 `dev@fd58ec25`，保留既有四个 job 的 Rust／GUI／Python／聚合门禁；本轮仅把现有 job 明确到 macOS runner，并扩展 Python stdlib discovery。本文不把后续产品候选的门禁或打包链写入固定 dev 基线。

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

本文 2026-10-17 出现的「独立评审」要求由上述主 agent 评审口径取代；实际配置与证据见下文，required check 名称和分支保护未因职责调整而改变。

## 检查矩阵

| 业务线 | 检查 job | 实际命令 | 覆盖 |
| --- | --- | --- | --- |
| Runtime + TUI（共享 crate） | `rust` | `cargo fmt --all -- --check` | Rust 格式 |
| Runtime + TUI | `rust` | `cargo clippy --locked --all-targets -- -D warnings` | 所有 target 的静态检查，warnings 视为失败 |
| Runtime + TUI | `rust` | `cargo test --locked --all-targets` | 单元、集成与 example target；测试数量以对应 SHA 的实际 run 为准 |
| TUI（真实 PTY） | `rust` | `cargo test --locked --bin Saki frontend::tui::tests::pty::pty_panic_restores_terminal -- --ignored --exact --test-threads=1` | 真实 PTY 上进入／退出终端并验证 panic 后 cooked mode 恢复 |
| Runtime stdio（真实子进程） | `rust` | `cargo build --locked --bin Saki`；`python3 examples/runtime_stdio.py --self-test --binary target/debug/Saki` | 两个真实 OS 进程、临时 read、task/message/journal 关联、恢复/history 零重放、继续前文与 stdout 断连 drain；driver 自带本地 fixture，无外部 provider/key |
| GUI→Runtime（五个离线测试） | `rust` | 见下方「GUI→Runtime 进程集成」权威命令（与 workflow 使用同一命令） | Go 客户端→真实 Saki `--stdio`、本地 httptest provider、真实 read、事件/工具关联、应用层流程、profile snapshot、mapping 保存顺序/失败边界、跨进程恢复零重放与继续；逐项断言 PASS（非 Skip/空跑）；线上 smoke 不接入 |
| GUI（独立 Go module） | `gui` | `gofmt -l .`（必须为空）、`CGO_ENABLED=0 go vet ./...`、`go test ./... -count=1`、`go build` | 格式、vet、测试、构建 |
| GUI（可复现图像） | `gui` | `SAKI_PREVIEW_DIR=... go test -run TestPreviewImages -count=1 .` | headless `ui.Tester` 生成 light/dark add／settings 等场景 PNG，作为 artifact 保留 14 天 |
| Python 回归 | `python` | 现有 step 内 `unittest.TestLoader.discover("scripts", pattern="test_*.py")`；`result.testsRun > 0` 且 `wasSuccessful()` | 执行 `scripts/test_*.py` 全部 stdlib unittest；发现/导入失败、测试失败、空 suite 均非零；不依赖文件数或新框架 |

聚合检查：`ci-required`（`if: always()`，依赖 `rust`、`gui` 与 `python`）只在三者结果都为 `success` 时通过。`cancelled`／`failed`／`skipped` 都不算成功。若未来新增 job，必须同时更新 `ci-required` 的 `needs` 与判定。

稳定检查名：`rust`、`gui`、`python`、`ci-required`。分支保护只需要引用聚合检查 `ci-required`，避免逐个维护子 job 名称。

固定 dev 基线只有这四个 job，全部明确 `runs-on: macos-15`，不新增 Linux／Windows 矩阵。`rust` 保留 Rustfmt、clippy、all-targets、真实 PTY 与 stdio；`gui` 保留 Go format／vet／test／build／previews；`rust` 中的 GUI→Runtime 仍是既有五个真实 Runtime、非 Skip 的离线测试。`ci-required` 的依赖与失败语义不变。

Python 门禁的真实失败传播：临时探针 `9480847` 的 [run 39381934207](https://github.com/HeimaoLST/Saki/actions/runs/39381934207) 中 Python 与聚合均失败，Rust／Go 成功；恢复探针并同步后的最终候选 `96ad987` 的 [run 39384000031](https://github.com/HeimaoLST/Saki/actions/runs/39384000031) 四个 job 均成功。探针已用追加提交撤回，不在最终树中。

## Runtime stdio 门禁（2026-10-20 已启用）

`rust` job 在已有 Rust 测试后显式构建真实二进制，并运行 Runtime 提供的无 GUI driver：

```sh
cargo build --locked --bin Saki
python3 examples/runtime_stdio.py --self-test --binary target/debug/Saki
```

driver 自建本地 fixture HTTP 服务，不需要外部 provider/key；其断言覆盖两个真实 OS 进程、临时文件 read、task/message/journal 关联、跨进程保存/恢复/history 零模型重放、继续前文，以及 stdout 断连且 stdin 仍打开时的 drain/exit。Python 由 `actions/setup-python@v7` 显式固定为 3.12；step 使用 `set -o pipefail` + `tee`，step 超时 5 分钟，非零退出不会被后续命令吞掉。该步骤属于既有 `rust` job，失败沿 `rust` → `ci-required` 传播，不使用 if-file-exists、continue-on-error 或 skip。

## GUI→Runtime 进程集成（2026-10-20 已启用）

当前 dev 的 GUI 交付提供五个已有离线入口，均在 `rust` job 的真实 binary 构建之后、用同一 `SAKI_TEST_RUNTIME_BIN` 运行：

```sh
cd desktop
SAKI_TEST_RUNTIME_BIN="$GITHUB_WORKSPACE/target/debug/Saki" CGO_ENABLED=0 \
  go test -run '^(TestRuntimeProcessIntegration|TestGUIAppRuntimeFlow|TestGUIFirstCreateUsesProfileSnapshot|TestGUIAppPersistsMappingBeforeSend|TestGUIAppSaveFailureStopsBeforeSend)$' -count=1 -v .
```

覆盖：真实 `Saki --stdio` 双进程、真实 read、事件/工具关联、应用层 UI 流程、首建 profile snapshot、session mapping 持久化顺序/保存失败边界、跨进程恢复/history 零重放与继续。五个测试都通过 `requireRuntimeBinary`：未设置 `SAKI_TEST_RUNTIME_BIN` 会 Skip，设置但 binary 不存在会失败；测试自带本地 httptest provider，不需要真实凭据/外部 provider。Go 由 `actions/setup-go@v7` 按 `desktop/go.mod` 提供并缓存 `desktop/go.sum`；`-count=1` 禁止缓存替代新 binary。step 使用 `pipefail`+`tee`，并逐项检查 `--- PASS: <test> `；任何一项 Skip、未匹配/空跑或失败都会失败。`TestGUIOnlineSmoke` 保持 opt-in（`SAKI_TEST_ONLINE=1` + 宿主凭据），不进入本 CI。超时 5 分钟；macOS arm64/headless，不覆盖真实窗口/IME。失败留在既有 `rust` job，沿 `rust` → `ci-required` 传播；无 if-file-exists、continue-on-error 或 skip。

## 触发、并发与权限

- 触发：所有 `pull_request`；`push` 到 `dev`／`master`；`workflow_dispatch`。发布 PR（`dev` → `master`）、hotfix PR（→ `master`）与回流 PR（`master` → `dev`）因此都跑全量检查。
- 并发：同一 workflow + ref 分组；`pull_request` 的新推送取消旧运行，`dev`／`master` 的 push 不取消，保留最终集成证据。
- 权限：workflow 顶层 `permissions: contents: read`；不使用任何 secret、模型密钥、新付费服务。
- 运行平台：现有四个 job 全部固定 `macos-15`（arm64）；不新增 Linux／Windows runner 或矩阵。
- 工具链：Rust 固定 `1.99.0`（rustfmt + clippy，官方 rustup 安装，已验证该版本可从 static.rust-lang.org 下载）；Go 由 `desktop/go.mod`（当前 `1.27.1`）通过 `actions/setup-go` 的 `go-version-file` 决定，不使用本机未来版本随意钉死不可下载版本。
- 缓存：Cargo registry／git／`target` 以 `runner.os + Rust 版本 + Cargo.lock` 为 key；Go 使用 `actions/setup-go` 内置缓存（`desktop/go.sum`）。缓存只加速，不改变 `--locked` 校验。
- Python：`actions/setup-python@v7` 显式提供 3.12；Python job 在现有 step 内用 stdlib `unittest` discovery 执行全部 `scripts/test_*.py`，以实际 `testsRun` 非零和 `wasSuccessful()` 作为门禁；rust job 的 stdio self-test 使用同一版本。
- 超时：Rust 30 分钟（含 stdio self-test step 的 5 分钟上限），GUI 20 分钟，Python 10 分钟，聚合 5 分钟。

## 失败处理与责任

| 角色 | 责任 |
| --- | --- |
| 实现作者（runtime／tui／gui） | 维护受影响测试与命令，提供功能与有价值的回归；PR 前在本地执行同一组命令；CI 失败先定位并修复，不以 skip、`continue-on-error` 或放宽 lint 掩盖真实失败。 |
| QA | 维护现有 workflow、CI 支撑脚本与自动检查运行可靠性；核对 PR 的实际 run URL、SHA 与退出码；区分“本次改动失败”与“基线失败”，把真实失败反馈给对应 owner。不接产品人工验收或独立 `APPROVE` 队列，不代替作者修产品代码。 |
| PM | 维护需求边界、验收标准、协作规范与文档结构；确认 CI 约定与仓库流程一致；真实试用与用户体验判断由 PM／用户负责。 |
| 主责／合并者 | 主 agent 代码评审与适用验收通过、required CI 真实通过、无未解决阻塞后，按 AGENTS.md §0.2 的逐 PR 明确授权执行；PM 统筹集成顺序与收尾。CI 绿不替代评审或用户试用，也不授权 breaking migration。 |

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

# Python（全部 scripts/test_*.py；与 workflow 相同的 stdlib discovery）
python3 - <<'PY'
import sys
import unittest

loader = unittest.TestLoader()
suite = loader.discover("scripts", pattern="test_*.py")
result = unittest.TextTestRunner(verbosity=2).run(suite)
print(f"DISCOVERED {result.testsRun} tests")
if result.testsRun == 0:
    print("python unittest discovery found zero tests", file=sys.stderr)
    sys.exit(1)
if not result.wasSuccessful():
    print("python unittest suite failed", file=sys.stderr)
    sys.exit(1)
PY

# Runtime stdio 跨进程 smoke（dev@4001fd4 起）
cargo build --locked --bin Saki
python3 examples/runtime_stdio.py --self-test --binary target/debug/Saki

# GUI→Runtime 进程集成（五个离线入口；子 shell 保持后续命令仍在仓库根）
(
  cd desktop
  SAKI_TEST_RUNTIME_BIN="$PWD/../target/debug/Saki" CGO_ENABLED=0 \
    go test -run '^(TestRuntimeProcessIntegration|TestGUIAppRuntimeFlow|TestGUIFirstCreateUsesProfileSnapshot|TestGUIAppPersistsMappingBeforeSend|TestGUIAppSaveFailureStopsBeforeSend)$' -count=1 -v .
)

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
- Runner 镜像与 `macos-15` 对应版本：<https://github.com/actions/runner-images>
- Rust 工具链安装：<https://rust-lang.github.io/rustup/concepts/toolchains.html>；版本清单：<https://static.rust-lang.org/dist/channel-rust-stable.toml>
- Go 官方下载与版本：<https://go.dev/dl/>
- `actions/checkout`、`actions/cache`、`actions/upload-artifact`、`actions/setup-go` 官方仓库与 releases：<https://github.com/actions>
- actionlint（workflow 静态检查）：<https://github.com/rhysd/actionlint>

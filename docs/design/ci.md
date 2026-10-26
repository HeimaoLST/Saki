# CI governance（Runtime／TUI／GUI）

更新日期：2026-10-26。状态：**实现事实**（workflow、本地验证、真实 GitHub Actions 运行结果；`dev`／`master` 分支保护已于 2026-10-17 启用）。当前 CI 事实基线：`dev@fd58ec2` + 本 PR 候选 `8f2cf422`（stacked 到 `agent/pm/156-mvp-candidate`；含 GUI/Runtime 固定输入）。本文职责调整本身不修改 workflow 或分支保护。

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
| GUI→Runtime（完整离线 desktop 包） | `rust` | `go test -json ./...` + `scripts/check-gui-runtime-results.py`（见下方权威命令） | 真实 Saki `--stdio` 双进程、首用 Save/Apply/Test/重启、活动配置变更/失败重载保留 helper、工作/对话并发、定向停止、容量与恢复；18 个必须场景逐项 PASS；仅 8 个显式预览/线上 opt-in Skip 可接受；线上 smoke 不接入 |
| macOS arm64 工程候选 | `macos-app` | `bash scripts/build-macos-app.sh /absolute/out`（输出在 checkout 外） | Go GUI + Rust `Saki-runtime` helper、`Saki.app/Contents/MacOS/{Saki,Saki-runtime}`、Info.plist `dev.saki.desktop`、arm64 Mach-O、ad-hoc 签名；manifest/zip/SHA256SUMS；工程候选，不是 Nagi MVP |
| GUI（独立 Go module） | `gui` | `gofmt -l .`（必须为空）、`CGO_ENABLED=0 go vet ./...`、`go test ./... -count=1`、`go build` | 格式、vet、测试、构建 |
| GUI（可复现图像） | `gui` | `SAKI_PREVIEW_DIR=... go test -run TestPreviewImages -count=1 .` | headless `ui.Tester` 生成 light/dark add／settings 等场景 PNG，作为 artifact 保留 14 天 |
| 协作检查 | `python` | `python3 scripts/test_collaboration_check.py`；断言运行测试数非零 | 既有协作检查回归；pipefail 保留失败，空套件不能静默通过 |

聚合检查：`ci-required`（`if: always()`，依赖 `rust`、`gui`、`python` 与 `macos-app`）只在四者结果都为 `success` 时通过。`cancelled`／`failed`／`skipped` 都不算成功。若未来新增 job，必须同时更新 `ci-required` 的 `needs` 与判定。

稳定检查名：`rust`、`gui`、`python`、`macos-app`、`ci-required`。分支保护只需要引用聚合检查 `ci-required`，避免逐个维护子 job 名称。

Python 门禁的真实失败传播：临时探针 `9480847` 的 [run 39381934207](https://github.com/HeimaoLST/Saki/actions/runs/39381934207) 中 Python 与聚合均失败，Rust／Go 成功；恢复探针并同步后的最终候选 `96ad987` 的 [run 39384000031](https://github.com/HeimaoLST/Saki/actions/runs/39384000031) 四个 job 均成功。探针已用追加提交撤回，不在最终树中。

## Runtime stdio 门禁（2026-10-20 已启用）

`rust` job 在已有 Rust 测试后显式构建真实二进制，并运行 Runtime 提供的无 GUI driver：

```sh
cargo build --locked --bin Saki
python3 examples/runtime_stdio.py --self-test --binary target/debug/Saki
```

driver 自建本地 fixture HTTP 服务，不需要外部 provider/key；其断言覆盖两个真实 OS 进程、临时文件 read、task/message/journal 关联、跨进程保存/恢复/history 零模型重放、继续前文，以及 stdout 断连且 stdin 仍打开时的 drain/exit。Python 由 `actions/setup-python@v7` 显式固定为 3.12；step 使用 `set -o pipefail` + `tee`，step 超时 5 分钟，非零退出不会被后续命令吞掉。该步骤属于既有 `rust` job，失败沿 `rust` → `ci-required` 传播，不使用 if-file-exists、continue-on-error 或 skip。

## GUI→Runtime 完整离线 package（2026-10-26 已启用）

`rust` job 在构建本次真实 `target/debug/Saki` 后，运行**完整离线 desktop Go package**，不靠手写 selector 只选少数测试：

```sh
cd desktop
SAKI_TEST_RUNTIME_BIN="$GITHUB_WORKSPACE/target/debug/Saki" \
SAKI_TEST_ONLINE=0 CGO_ENABLED=0 \
  go test -json ./... -count=1 > "$RUNNER_TEMP/desktop-go-test.json"
python3 "$GITHUB_WORKSPACE/scripts/check-gui-runtime-results.py" \
  "$RUNNER_TEMP/desktop-go-test.json"
```

`go test` 的 stderr 直接进入 CI 日志，stdout 才是 JSON；不使用会混入 stderr 的 `2>&1`。`SAKI_TEST_RUNTIME_BIN` 必须指向本次构建的绝对 binary，`SAKI_TEST_ONLINE=0` 显式关闭线上入口，`-count=1` 禁止缓存替代新 binary。Go test 非零直接失败；checker 再验证 package PASS 与场景合同，任一不符都失败。

checker（`scripts/check-gui-runtime-results.py`）的**必须 PASS** 清单（原 #87 五项 + 当前 Nagi 场景）：

```text
TestRuntimeProcessIntegration
TestGUIAppRuntimeFlow
TestGUIFirstCreateUsesProfileSnapshot
TestGUIAppPersistsMappingBeforeSend
TestGUIAppSaveFailureStopsBeforeSend
TestConnectionGUIFirstSaveApplyTestSendAndRestart
TestConnectionGUISaveDuringIssueAndReloadFailurePreservesHelper
TestConnectionGUIFailedTestCanBeCorrectedWithoutSaving
TestConnectionGUISaveFailureAndUnknownExecutionRecovery
TestConnectionGUIEndpointChangePreservesOldContextAndNewDefaults
TestConnectionProcessProbeBothProtocolsAndRepair
TestConnectionProcessSavedChangeKeepsActiveSnapshotAndHistory
TestIndependentIssueWorkAndConversationCapacityTwo
TestIndependentExecutionCapacityOneSerializesConversation
TestStoppingIssueWorkDoesNotStopConversation
TestCapacityTwoReservesOneSlotForConversation
TestUnknownIssueExecutionIsRecoveredFromHistoryAfterRestart
TestIssueWorkStopButtonStopsIssueNotConversation
```

checker 还要求至少一个 PASS、`saki/desktop` package 最终 PASS、无任何 FAIL；缺任一必须用例、关键用例 Skip、未知 Skip、零执行/空 JSON 都会失败。明确允许的 opt-in Skip 仅这 8 个：

```text
TestGUIOnlineSmoke
TestIntegratedMarkdownPreview
TestMarkdownReaderScreenshot
TestPreviewConnectionSettings
TestPreviewImages
TestPreviewIssueStates
TestPreviewRuntimeStates
TestPreviewStartupDiagnostics
```

这些预览测试只在显式目录 opt-in 时运行；线上 smoke 仍需 `SAKI_TEST_ONLINE=1` + 宿主凭据，不进入本 CI。完整 package 让未来新增离线测试自然执行；若新增合法 opt-in，必须同步检查脚本 allowlist，不能静默放开未知 Skip。step 超时 10 分钟；Linux/headless，不覆盖真实窗口/IME。失败留在既有 `rust` job，沿 `rust` → `ci-required` 传播；无 if-file-exists/continue-on-error/skip。

## macOS arm64 工程候选（2026-10-24）

`macos-app` 在标准 `macos-15`（arm64）runner 上运行 `scripts/build-macos-app.sh <checkout 外的绝对目录>`。脚本要求 clean checkout、外部输出目录，显式构建：

```sh
cargo build --locked --release --bin Saki
# desktop/:
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -mod=readonly -buildvcs=true -o <app>/Contents/MacOS/Saki .
```

产物为 `Saki.app`，其中 `Contents/MacOS/Saki` 为 GUI、`Saki-runtime` 为 Rust helper；Info.plist 使用既有 `dev.saki.desktop` / `Saki` / `0.1.0` / build `1` / minimum 11.0 契约。脚本校验 `uname -m`、`go env GOOS/GOARCH`、`rustc` host、`file`/`lipo` 为 arm64，`plutil` 字段，先签 helper 再签 app，分别 `codesign --verify --strict`，app 再 `--deep --strict`；对已签名 bundle 内 helper 调用现有 `examples/runtime_stdio.py --self-test`。`ditto` 生成保留权限/签名的 `Saki.app.zip`；`manifest.json`、`SHA256SUMS`、`codesign.log`、`validation.log`、`helper-smoke.log` 与 `build.log` 一并上传，artifact 名含实际 source SHA，保留 14 天，`if-no-files-found: error`。

- artifact 只含 `Saki.app.zip`、`manifest.json`、`SHA256SUMS` 与日志；下载后先在**新目录**执行 `ditto -x -k Saki.app.zip .` 解包，再 `shasum -a 256 -c SHA256SUMS`（此时 app/helper/plist/zip/manifest 的相对路径均存在）。manifest 记录 actual checkout/source SHA、CI event SHA、run URL、`uname`/macOS/runner image、Go/Rust/cargo/Xcode/SDK、app/helper/Info.plist/zip SHA256、签名与文件清单；build log 在脚本结束后由 CI 完整保存，不作为“写入中”的 hash 对象。
- ad-hoc only；不做证书/公证/Gatekeeper 承诺；minimumOS 字段不表示已在旧 OS 或真实 Finder/IME 实测。
- 当前 dev 未含 #141/PR126/PR132 产品切片，因此该 artifact 只能称 **CI 工程候选**，不是 Nagi MVP 完成；真实窗口/IME、外部 provider、用户试用由对应 owner/PM 负责。
- 本地构建与下载核验命令（macOS arm64）：

```sh
bash scripts/build-macos-app.sh /private/tmp/saki-macos-candidate
# download artifact 到新目录后，先解包再校验：
ditto -x -k Saki.app.zip .
shasum -a 256 -c SHA256SUMS
plutil -p Saki.app/Contents/Info.plist
file Saki.app/Contents/MacOS/Saki Saki.app/Contents/MacOS/Saki-runtime
lipo -archs Saki.app/Contents/MacOS/Saki Saki.app/Contents/MacOS/Saki-runtime
codesign --verify --strict Saki.app
codesign --verify --deep --strict Saki.app
```

首次候选历史证据：run 40107805903，source `04d579a`；最终 head/run/artifact 以 PR 正式过程记录为准，失败时不以计划或本地 exit 冒充 CI 证据。

## 触发、并发与权限

- 触发：所有 `pull_request`；`push` 到 `dev`／`master`；`workflow_dispatch`。发布 PR（`dev` → `master`）、hotfix PR（→ `master`）与回流 PR（`master` → `dev`）因此都跑全量检查。
- 并发：同一 workflow + ref 分组；`pull_request` 的新推送取消旧运行，`dev`／`master` 的 push 不取消，保留最终集成证据。
- 权限：workflow 顶层 `permissions: contents: read`；不使用任何 secret、模型密钥、新付费服务。
- 运行平台：`ubuntu-24.04`（GitHub 官方 `ubuntu-latest` 当前指向的镜像；显式固定避免标签漂移）。
- 工具链：Rust 固定 `1.99.0`（rustfmt + clippy，官方 rustup 安装，已验证该版本可从 static.rust-lang.org 下载）；Go 由 `desktop/go.mod`（当前 `1.27.1`）通过 `actions/setup-go` 的 `go-version-file` 决定，不使用本机未来版本随意钉死不可下载版本。
- 缓存：Cargo registry／git／`target` 以 `runner.os + Rust 版本 + Cargo.lock` 为 key；Go 使用 `actions/setup-go` 内置缓存（`desktop/go.sum`）。缓存只加速，不改变 `--locked` 校验。
- Python：`actions/setup-python@v7` 显式提供 3.12；协作检查 job 运行现有 stdlib unittest，rust job 的 stdio self-test 使用同一版本，不依赖 runner 偶然环境。
- 超时：Rust 30 分钟（含 stdio self-test step 的 5 分钟上限），GUI 20 分钟，Python 10 分钟，macOS candidate 25 分钟，聚合 5 分钟。

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

# Python（协作检查回归）
python3 scripts/test_collaboration_check.py

# Runtime stdio 跨进程 smoke（dev@4001fd4 起）
cargo build --locked --bin Saki
python3 examples/runtime_stdio.py --self-test --binary target/debug/Saki

# GUI→Runtime 完整离线 desktop package（子 shell 保持后续命令仍在仓库根）
(
  cd desktop
  SAKI_TEST_RUNTIME_BIN="$PWD/../target/debug/Saki" SAKI_TEST_ONLINE=0 CGO_ENABLED=0 \
    go test -json ./... -count=1 > /tmp/desktop-go-test.json
  python3 ../scripts/check-gui-runtime-results.py /tmp/desktop-go-test.json
)

# macOS arm64 engineering candidate（需 macOS arm64；输出在 checkout 外）
bash scripts/build-macos-app.sh /private/tmp/saki-macos-candidate

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

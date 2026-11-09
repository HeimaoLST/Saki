# Saki CI 与 Runtime 产物

更新日期：2026-11-09。状态：配置与脚本实现事实，基于 #333 产品仓库拆分。独立 Runtime 首次真实产物来自 [CI42613582733/attempt1](https://github.com/HeimaoLST/Saki/actions/runs/42613582733)，源码 `a00c587e5407cd5b06b81470aad0fb8394710003`；三个 Saki job 全部成功。后续提交与消费者的实际验证以 #333 和各自 PR/run 为准，不以旧组合候选或本地检查代替。

配置权威为 [ci.yml](../../.github/workflows/ci.yml)。Saki 只交付 Runtime、服务入口与契约。Nagi 和 Rous 在各自仓库构建并消费显式固定的 Saki 产物，不编译相邻 Saki 源码。Nagi 的 GUI 打包、原生体验与 Rous 的终端交互由各产品负责。

## Saki 检查矩阵

| Job | 命令或检查 | 失败条件 |
| --- | --- | --- |
| `runtime` | `cargo tree --locked --prefix none --format '{p}'` | 依赖图残留 ratatui、crossterm、termina、arboard 或 syntect |
| `runtime` | `cargo fmt --all -- --check` | 格式检查非零 |
| `runtime` | `cargo clippy --locked --all-targets -- -D warnings` | 静态检查或 warning 非零 |
| `runtime` | `cargo test --locked --all-targets` | Runtime 与现有 example 目标的测试失败 |
| `runtime` | `cargo build --locked --release --bin Saki` | 独立服务二进制构建失败 |
| `runtime` | `python3 examples/runtime_stdio.py --self-test --binary target/release/Saki` | 真实子进程 stdio 自测失败，step 上限 5 分钟 |
| `runtime` | `scripts/package-runtime.py` 与 artifact 上传 | 源码身份、版本、平台或产物生成错误，或者上传失败 |
| `python` | stdlib discovery，`scripts/test_*.py` | 导入错误、测试失败或实际执行数为零 |
| `ci-required` | `if: always()`，依赖 `runtime` 与 `python` | 任一依赖不是 `success`，包括 failed、cancelled、skipped |

Saki workflow 没有 Go 安装、GUI 构建、桌面打包或 TUI PTY 步骤。终端依赖检查在编译前执行。源码目录中未被目标引用的残留文件不能替代依赖图或实际构建证据。

stdio driver 自建受控本地 provider，验证真实进程、工具执行、任务与历史关联、恢复零重放、继续前文与输出断连后的排空退出。它不需要外部模型密钥，也不证明真实 provider 组合或产品体验。driver 与 [Python 生命周期回归](../../scripts/test_runtime_stdio.py) 的覆盖分别保留。

Python job 使用 `unittest.TestLoader().discover("scripts", pattern="test_*.py")`，要求 `result.testsRun` 非零且 `wasSuccessful()`。测试数量以当次源码和日志为准。移往 Nagi 的 macOS 打包脚本与 `test_macos_minimum.py` 已从 Saki 移除，Saki discovery 不继续导入已删除的产品模块。

## 触发、环境与门禁

所有 pull request、推送到 `dev`、`master`、`main` 以及手动触发均运行该 workflow。没有路径过滤器。checkout 显式使用 PR head SHA 或 push SHA，产物身份记录实际 checkout，而非把 PR 测试合并提交混为源提交。

Rust 固定为 `1.99.0`，安装 rustfmt 与 clippy。Python 固定为 `3.12`。`runtime` 和 `python` 使用 `macos-15`，超时分别为 30 和 10 分钟。`ci-required` 使用 `macos-15-intel`，只汇总结果，上限 5 分钟，不表示 Intel 产品构建或运行已被验证。当前产物目标为 `aarch64-apple-darwin`。

workflow 权限为 `contents: read` 与 `actions: read`。没有模型密钥。PR 同一 workflow/ref 的新运行可取消旧运行，集成分支 push 不取消旧运行。`PYTHONDONTWRITEBYTECODE=1` 避免支撑脚本的导入缓存影响 clean source 检查。

`ci-required` 名称保持不变。本轮没有修改 GitHub 分支保护。此前读回的 Saki `dev` 与 `master` 保护为 strict `ci-required`，绑定 GitHub Actions app 15368、约束管理员、原生审批数 0、禁止 force-push 和删除。该读回不证明新工作区已经进入远端或新消费者仓库已有同样保护。

## 独立 Runtime 产物

[package-runtime.py](../../scripts/package-runtime.py) 只打包 workflow 已构建并完成 stdio 自测的 `target/release/Saki`。它不构建 Nagi，也不启动模型。打包要求 clean checkout，`SAKI_SOURCE_SHA` 等于实际 HEAD，Rust host 为 `aarch64-apple-darwin`，并且二进制 `--version` 输出与 Cargo package version 一致。输出目录必须位于 checkout 外。

上传目录包含以下三个文件：

| 文件 | 内容 |
| --- | --- |
| `Saki` | 独立服务二进制 |
| `manifest.json` | schema 1、产品名与版本、repository、source SHA 与 tree、dirty=false、run ID、run attempt、target、Rust 工具链信息、protocol version 1、binary name/hash |
| `SHA256SUMS` | `Saki` 与 `manifest.json` 的内容 SHA256 |

artifact 名为 `saki-runtime-aarch64-apple-darwin-<source_sha>-run-<run_id>-attempt-<run_attempt>`。不同 attempt 使用不同名字，不覆盖旧身份。上传设置 `archive: true`、`if-no-files-found: error`，保留期为 90 天，仍受仓库实际策略约束。文件缺失与过期均不能靠选择另一个“最新”产物处理。

workflow summary 输出 `artifact-id`、`artifact-digest` 和 manifest。源码 SHA 来自 CI checkout；二进制版本是程序自报值。它们不是“二进制嵌入了源码 SHA”的证明。固定 run 的构建与自测、manifest 来源及最终 binary hash 共同描述该次工程产物。它不是公开 Release、公证安装包或产品人工验收结果。

artifact 可在 `runtime` job 完成后出现，但消费者还要求整个固定 run/attempt 为 completed SUCCESS。未完成或失败的 run 不能因为已有下载地址就成为有效 pin。

## `upload-artifact@v7` 的匹配版本核对

本次沿用 base `ddf1dfbfce134dcc084188126737d2b91956091b` 的 `actions/upload-artifact@v7` 引用。原 [workflow 第 199、333 行](https://github.com/HeimaoLST/Saki/blob/ddf1dfbfce134dcc084188126737d2b91956091b/.github/workflows/ci.yml#L199) 已使用它，拆仓没有升级这个 action 的主版本。

2026-11-09 只读查询官方 tag 得到 `v7 → cf430e030ddbb5b0abf93d22962f4752f3646cd9`，该提交的 [package.json](https://github.com/actions/upload-artifact/blob/cf430e030ddbb5b0abf93d22962f4752f3646cd9/package.json) 版本为 `7.0.2`。[action.yml](https://github.com/actions/upload-artifact/blob/cf430e030ddbb5b0abf93d22962f4752f3646cd9/action.yml) 使用 Node 24，定义 `artifact-id` 与 `artifact-digest`，`archive` 默认值为 `true`。Saki 显式设置相同值，以固定下载 ZIP 的约定。tag 存在与版本定义匹配已核实；首次真实 run 的上传、外层 ZIP digest 和二进制 hash 已下载核对，证据见 #333。

归档与二进制校验分别对应不同内容：

- `archive: true` 时，上传流是 action 生成的外层 ZIP。该版本的[上传实现](https://github.com/actions/upload-artifact/blob/cf430e030ddbb5b0abf93d22962f4752f3646cd9/dist/upload/index.js#L123987) 对上传流计算 SHA256，并在 finalize 请求中写成 `sha256:<hex>`。[输出映射](https://github.com/actions/upload-artifact/blob/cf430e030ddbb5b0abf93d22962f4752f3646cd9/src/shared/upload-artifact.ts#L19) 将返回 digest 作为 `artifact-digest`，不带该前缀。
- consumer pin 的 `archive_sha256` 保存这个裸十六进制 digest。fetcher 同时比对 REST artifact `digest` 的 `sha256:` 值和实际下载 ZIP 的 SHA256。
- `binary_sha256` 是 ZIP 内 `Saki` 字节的另一个 hash，还须与 manifest 和 `SHA256SUMS` 一致。外层 ZIP digest 不能代替它。
- 此版本的[权限说明](https://github.com/actions/upload-artifact/blob/cf430e030ddbb5b0abf93d22962f4752f3646cd9/README.md#permission-loss) 明确 ZIP 上传不保留文件执行权限。fetcher 在内容验证通过后，给落盘的 `Saki` 设置 `0755`。

REST 字段与下载入口以 [GitHub artifact API](https://docs.github.com/en/rest/actions/artifacts) 为准。上述匹配版本源码说明校验语义，不构成新 Saki 源码已实际完成上传与取件的证据。

## 消费者的严格 pin

Nagi 与 Rous 各自的 `.github/saki-runtime.json` 固定以下身份，由各自 `scripts/fetch-saki-runtime.py` 消费。它们没有相邻源码路径或自动选择最新运行的回退。

| 字段 | 约束 |
| --- | --- |
| `repository` | 产物所在的 `owner/name` |
| `run_id`、`run_attempt` | 明确的正整数，整个指定 attempt 已完成且 SUCCESS |
| `source_sha` | 完整源码 commit hash，与 run、artifact 来源及 manifest 一致 |
| `artifact_id`、`artifact_name` | 明确 artifact ID，名称同时对应 target、source、run 与 attempt |
| `archive_sha256` | 固定 ZIP 字节 SHA256，与 REST digest 及下载内容一致 |
| `binary_sha256` | 固定 Runtime 字节 SHA256，与 manifest 及内容一致 |
| `target`、`protocol_version` | 当前为 `aarch64-apple-darwin` 与整数 `1` |

fetcher 拒绝过期产物、失败或未完成的 run、身份不符、错误 hash、缺失或额外归档成员，以及校验文件不符。只有验证通过后才写出可执行文件，并向 job 导出 `SAKI_TEST_RUNTIME_BIN`、`SAKI_RUNTIME_BIN`、`SAKI_RUNTIME_SOURCE_SHA` 和 `SAKI_RUNTIME_VERSION`。

没有已核实的产物时，pin 使用 `status=pending`，fetcher 在网络取件前失败，兼容 job 和聚合检查随之失败。工程初版按此方式拒绝未提供的身份；首个真实产物验证后，消费者固定其 run/attempt/源码/双 hash。实际选择以各仓库的 pin 为准，不选择旧 GUI 组合包、虚构身份或“最新”回退。

现行消费者 workflow 读取 `SAKI_ARTIFACT_READ_TOKEN`，未提供时尝试 `github.token`。这段回退不证明跨公共仓下载可用。[GITHUB_TOKEN 官方约束](https://docs.github.com/en/actions/concepts/security/github_token) 将权限限定到 workflow 所在仓库；artifact REST 的公开资源免鉴权例外只列在元数据查询，下载接口仍要求源仓库 Actions read。公开仓库不等于匿名可下载 Actions artifact，消费者的 `actions: read` 也不自动扩大到 Saki。

当前 fetcher 对 run、artifact 元数据和下载都执行 `gh api`，尚无独立的跨仓凭据获取流程。跨仓下载是否被授权必须由消费者自身 job 实证；本机授权 gh 的下载成功不能代替该证据。权限不足会使兼容 job 与聚合检查失败，没有改为跳过检查、使用相邻源码或忽略 hash。

GitHub 原生下载路径需要实际具有 Saki Actions read 的 App 安装令牌或 fine-grained PAT。令牌必须由真实授权产生并提供给 workflow，secret 名本身不是凭据。[App 安装令牌](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app) 可以限定 repository 与 permissions。脚本不创建 App、PAT 或 secret；所需凭据由维护者按仓库权限配置。当前决定不新建或转存凭据，不引入 Release、部署或其他取件绕路。严格门禁保持，真实源产物出现后再验证消费者取件并记录结果。

## 各产品的兼容检查

各产品仓库自己的 workflow 和检查器是其权威配置，本节只记录与 Saki 产物的边界。

- Nagi 的 Go 格式、vet、测试、MyGo 回归与 headless 预览独立运行，不安装 Cargo。兼容 job 下载固定 Runtime，构建同 Nagi checkout 的 issue helper，运行完整离线 Go race 包。检查器以 `github.com/HeimaoLST/Nagi` package 与 test 名为键，保留 18 个 MUST、现有 8 项 opt-in skip allowlist、非零执行与 package PASS 要求。任何 FAIL 都保留，不能被后续 PASS 覆盖。缺必需项、必需项 SKIP、未知 SKIP 或其他 package FAIL 均失败。
- Nagi 打包接口为 `scripts/build-macos-app.sh OUT RUNTIME_BIN NAGI_VERSION RUNTIME_SOURCE_SHA`，产品版本来自其根 `VERSION`。Saki 由已核 pin 提供，不在打包时重编译。Nagi schema 2 manifest 分列产品源码与版本、调用方提供的 Runtime source SHA、自报版本和输入/包内 hash；source SHA 的 CI 来源由 pin 负责核对。
- Rous 独立运行自身 Rust 检查与 PTY 夹具。兼容 job 使用已核 pin 的 `SAKI_TEST_RUNTIME_BIN`，显式执行 `cargo test --locked --bin rous client::tests::real_runtime_contract -- --ignored --exact --nocapture --test-threads=1`。检查器要求该具名真实客户端测试实际运行，恰好 1 PASS、0 FAIL、0 ignored；零执行、缺失与旧 FAIL 均失败。不用通用 Python driver 冒充 Rous 客户端验证。

两个消费者的 `ci-required` 都使用 `if: always()` 并检查产品 job 与兼容 job 为 `success`。取消、失败或跳过必需 job 都不能通过。artifact 获取失败没有 `continue-on-error` 或缺文件即跳过分支。

## 证据与责任

本轮删除旧产品脚本后的 Saki Python 3.12 discovery 已通过，无已删除模块的导入错误。工作流 actionlint、Python AST、workflow shell 语法、受控产物格式回环与检查器负向场景已有本地证据。首次 Saki hosted CI 与实际产物核验已有记录；消费者自身取件和产品完整兼容分别按实际源码/run记录，不能从上述工具测试推导为通过。

实现 owner 维护功能与有价值的行为回归。QA 维护 workflow、取件校验、结果检查和自动检查可靠性，不改产线源码、不承担产品人工验收或额外独立 `APPROVE` 队列。PM 统筹评阅、集成和适用验收，真人体验由 PM/用户判断。CI 通过不扩大 commit、push、merge、Release 或部署授权。

历史三产品共享 crate、完整 Go 门禁、macOS 组合包与分支保护配置保留在 [ddf1dfbf 的 CI 文档](https://github.com/HeimaoLST/Saki/blob/ddf1dfbfce134dcc084188126737d2b91956091b/docs/design/ci.md)。旧 [CI42211260305](https://github.com/HeimaoLST/Saki/actions/runs/42211260305) 与 PR326 只说明旧组合候选的范围，不是本次仓库拆分的通过证据。#193/#259 等历史失败与未完成的原生、真实模型和真人验收保持各自原记录，不因调整 CI 而被宣称根治或完成。

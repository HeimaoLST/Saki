<!-- 协作协议见根目录 AGENTS.md。普通 PR 一个目标；发布/回流可汇总多个 issue。Spec/AC 证据见 §6/§10，PM 按 §0.2 集成处置权处理（§5/§7）。 -->

## 关联与 Spec

Closes #  <!-- 目标为默认分支 dev 且完成该任务时填写；hotfix、发布、回流和 stacked 中间 PR 使用 Refs #N -->

- Spec 版本/固定评论：<!-- 例如 r1 / https://github.com/HeimaoLST/Saki/issues/236#issuecomment-... -->
- 唯一 owner / 可写路径 / base / 目标分支：

## 动机

<!-- 为什么需要这个改动？关联的 issue / 文档是什么？ -->

## 改动内容

<!-- 分点说明改了什么；涉及公共接口、协议或行为的改动要单独标出 -->

## 证据与 AC 映射

<!-- 逐条列出 Spec 的 R/AC；不要用 CI 绿、Ready 或通知 ACK 代替验收。不适用项写 N/A 与理由，仓库必需门禁不能自行标 N/A 跳过。 -->

| AC | 实现/验证证据（命令、SHA、日志或限制） |
| --- | --- |
| AC-01 | |

<!-- 行为证据按 §10 选择；先贴命令与原始结果摘要，再给判断。 -->

- base / 同步状态：<!-- 目标分支；是否已同步最新 base（未发布分支 rebase / 共享分支 merge commit）；最新验证 SHA：____ -->
- 合并方法：<!-- 短期工作分支默认 squash；dev→master 发布、master→dev 回流使用 merge commit，保留长期分支 -->
- [ ] `cargo fmt --check` / `cargo clippy --all-targets -- -D warnings` / `cargo test`：
- [ ] `cd desktop && go test ./...`：
- [ ] CI workflow：PR run URL / 最新验证 SHA / `ci-required` 结论：
- [ ] 工具链与复现：Rust 1.99.0、Go 由 `desktop/go.mod` 决定；命令与 `.github/workflows/ci.yml` 一致：
- [ ] 离线确定性测试（禁止 sleep）：
- [ ] 真实模型验证（如适用，注明模型 ID 与摘要）：
- [ ] PTY / 真实终端验证（如适用）：
- [ ] 行为变化已同步既有 `docs/` 文档，或已开 follow-up issue：
- 已知基线失败（与本次改动无关，附证据）：
- CI 失败处理：定位失败 job 与 owner；不得用 skip／`continue-on-error`／放宽 lint 掩盖真实失败。基线失败单独列出，不能写成本次通过。CI 绿只表示门禁通过，不替代主 agent 代码评审、适用验收或 PM／用户真实试用，也不授权 breaking migration。详见 `docs/design/ci.md`。
- 职责分工：PM 负责 Spec/验收/集成/收尾；业务主 agent 负责实现与逐条证据；子 agent 只走 tmux；QA 维护 CI／自动检查运行可靠性。CI 绿不证明用户觉得好用。

## 合并后收尾（主责执行，按真实阶段分流）

已合并且验收通过：

- [ ] 记录最终合并 SHA 与验证证据（与评审 SHA 不一致时说明原因）
- [ ] 对应 issue 置 `status:done`；已满足的 `needs:*` 移除，未满足的保留并写清依赖
- [ ] issue 已关闭（或注明 `Closes` 未生效并附最终分支／合并证据）
- [ ] 自有 worktree／分支已回收，或保留并写明用途与解除条件

尚未完成（待审／阻塞／只读／未完成）时不要勾选上面的 `done`／关闭项，只记录当前阶段、下一步与阻塞原因。

## 影响与风险

- 涉及共享文件（`Cargo.toml` / `AGENTS.md` / `README.md` / `.github/`）：
- 其他 agent 需要知晓的接口变化：
- 已知限制 / 未覆盖场景：
- 回滚方式：

## Agent 声明

FROM: agent:<line>#<instance>      <!-- 例如 agent:tui#nova -->
IMPLEMENTER: <!-- 有子 agent 时注明实际实施实例；内部指导走 tmux，由主 agent 汇总，不写固定 pane 地址 -->
REVIEWER: agent:<line>#<instance>  <!-- 实际评审者；主 agent 可评审自己或子 agent 产物，但须如实记录作者／评审者，不能冒充独立评审或人类结论；peer review 按需 -->
兼容性: <!-- 非 breaking change：说明依据；breaking change：影响、迁移／回滚方案与人类确认来源 -->
MERGE 责任与授权: <!-- 见 AGENTS §0.2；记录 PM 集成责任与时间，或 breaking change 的人类批准。业务主责不脱离 PM 安排自行合并；force/tag/Release/deploy 不由此扩展。 -->

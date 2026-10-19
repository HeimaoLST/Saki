<!-- 协作协议见根目录 AGENTS.md。普通 PR 一个目标；发布/回流可汇总多个 issue。证据见 §10，合并预授权见 §0.2，执行门槛见 §5/§7。 -->

## 关联

Closes #  <!-- 目标为默认分支 dev 且完成该任务时填写；hotfix、发布、回流和 stacked 中间 PR 使用 Refs #N -->

## 动机

<!-- 为什么需要这个改动？关联的 issue / 文档是什么？ -->

## 改动内容

<!-- 分点说明改了什么；涉及公共接口、协议或行为的改动要单独标出 -->

## 证据

<!-- 行为证据按 §10 选择，不适用项写 N/A 与理由；仓库必需门禁不能自行标 N/A 跳过。先贴命令与原始结果摘要，再给判断。 -->

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
- CI 失败处理：定位失败 job 与 owner；不得用 skip／`continue-on-error`／放宽 lint 掩盖真实失败。基线失败单独列出，不能写成本次通过。CI 绿只表示门禁通过，不替代独立评审，也不授权 breaking migration。详见 `docs/design/ci.md`。

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
REVIEWER: agent:<line>#<instance>  <!-- 实际独立审查者；主 agent 可附已发生的审查证据汇总，不能伪造结论或将同一交付的主子互审称为独立评审 -->
兼容性: <!-- 非 breaking change：说明依据；breaking change：影响、迁移／回滚方案与人类确认来源 -->
MERGE 责任与授权: <!-- 主责实例；非 breaking change 引用 AGENTS.md §0.2 持续预授权，无需逐 PR 询问；breaking change 记录人类明确批准。独立评审／门禁／适用 QA 与发布协调仍须满足。 -->

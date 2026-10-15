<!-- 协作协议见根目录 AGENTS.md。一个 PR 只解决一个 issue；证据规则见 AGENTS.md §10。 -->

## 关联

Closes #  <!-- Closes 只在 base 为 master 时生效；stacked 中间 PR 请改用 Refs #N -->

## 动机

<!-- 为什么需要这个改动？关联的 issue / 文档是什么？ -->

## 改动内容

<!-- 分点说明改了什么；涉及公共接口、协议或行为的改动要单独标出 -->

## 证据

<!-- 粘贴命令与输出摘要。先贴原始输出，再给判断。不要把运行产物入库（.saki/ 已 gitignore）。 -->

- [ ] 离线确定性测试（禁止 sleep）：
- [ ] `cargo fmt --check` / `cargo clippy --all-targets -- -D warnings` / `cargo test`：
- [ ] 真实模型验证（如适用，注明模型 ID 与摘要）：
- [ ] PTY / 真实终端验证（如适用）：
- [ ] `cd desktop && go test ./...`（如适用）：
- [ ] 行为变化已同步 `docs/` 对应文档，或已开 follow-up issue：

## 影响与风险

- 涉及共享文件（`Cargo.toml` / `AGENTS.md` / `README.md` / `.github/`）：
- 其他 agent 需要知晓的接口变化：
- 已知限制 / 未覆盖场景：
- 回滚方式：

## Agent 声明

FROM: agent:<line>#<instance>      <!-- 例如 agent:tui#nova -->
REVIEWER: agent:<line>#<instance>  <!-- 必须是非作者实例 -->

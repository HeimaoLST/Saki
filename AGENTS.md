# AGENTS.md — Saki Runtime 协作规则

更新日期：2026-11-09。用户当前指令优先于本规则；用户已批准将 Saki、Nagi、Rous 拆成三个独立 Git 仓库，迁移任务见 #333。用户级编码、测试、注释与授权约束继续有效。

## 产品边界

Saki 是完整 Agent Runtime，对外服务接口属于 Runtime。只在本仓库维护模型调用、Agent/tool/compact 循环、session/task、配置、持久化、容量、取消与执行事实。

Nagi 桌面产品在 HeimaoLST/Nagi；Rous 终端产品在 HeimaoLST/Rous。联系人/事项/完成规则归 Nagi，草稿/选中项/终端命令归 Rous；消费者通过公开协议调用，不导入私有核心、共享相邻源码或读写 Runtime journal。公开契约以 docs/design/runtime-api.md 为准。

## 主责、范围与通道

- 使用稳定实例 ID `agent:<line>#<name>`，不冒用作者、执行者、评审者或人类。同一 issue 同时只有一个主责，主 agent 明确子任务文件范围，避免并发写同一文件。
- Runtime 线维护 src/、Cargo.toml/Cargo.lock、对应 tests/ 与 examples/；QA 维护 .github/workflows/ 及 CI 工程脚本，不改产线；PM 维护产品/协议协调、文档索引、协作模板与集成安排。模块实现者在同一交付维护自己的事实文档。
- 用户指定 hc/Herdr 为本团队主子执行通道。操作前 `hc ls`、`hc get` 核对实例标签、任务与状态，使用动态终端地址；不按固定 tmux pane 或旧数字分派。
- 主 agent 用 `hc send` 给自己的成员具体范围、工作区/分支、验收与 Git 权限；子 agent 只执行分派并向主 agent 回报，不自行开 agent、巡检认领其他任务、写 GitHub 或合并。
- 跨独立任务及正式阶段结果在 GitHub issue/PR 记录；不把子 agent 内部步骤逐条搬上 GitHub。GitHub 使用稳定实例/issue/branch/commit，不保存固定运行终端地址作为长期路由。
- 未经用户或分派明确允许，不通过消息工具联系他人，不清理他人会话、分支、worktree 或进程。

## Spec 与授权

开始前完整读实际工作区 AGENTS，核对 cwd/branch/脏文件来源与当前 issue/Spec。Review/audit/解释默认只读，提案不授权实施。只对实质影响范围、风险或授权的歧义提问。

PM 按用户目标给出固定版本 Spec，写明 owner、路径、base/目标分支、依赖、AC与证据。主责接收后可在指定 worktree 编辑、测试、小步 commit、push 自己分支与开 PR；子 agent 的 Git 动作须明确分派。Git 操作串行，署实际身份。

2026-11-01 用户给予的 Saki PM 集成处置权保持：PM 在逐条AC、代码评审、适用实际运行与 required CI 对应SHA通过后，可处理非breaking兼容集成，无需逐PR再请示。业务成员按PM集成安排执行。公共契约/配置不兼容、破坏持久数据或移除承诺能力须人类确认；用户已批准的具体迁移入口退役不重复确认。

不从实现/合并授权推导 force-push、tag、GitHub Release、部署或任意立即发布 master。用户直接授权无需先有issue才能执行，重要范围与默认流程差异事后如实补记；GitHub不可访问时保留现场，不虚报已认领或已读记录。

## 标准工作循环

1. 核对当前授权、相关 ready/review/blocked、交接/返修和未收尾交付，避免抢占。正常开发先 fetch，从最新 origin/dev 建独立 worktree；主检出固定 master。hotfix基于master，stacked基于直接父分支。
2. 在issue认领并写稳定实例、范围与计划，读回核对唯一主责；已有用户授权不因文书延迟阻塞执行。一个任务共用一个受控worktree，主子文件不重叠，禁止在主检出切分支。
3. 实施前查已有 helper/约定，保持唯一实现。为真实回归/非平凡不变量写确定性测试，复用已有行为覆盖；不为字面量、映射或已移除功能堆测试。
4. 小步提交，Conventional Commits；body包含 `Refs: #N` 和 `Agent: agent:<line>#<name>`。提交前检查staged diff，排除无关文件、密钥、.env和运行产物；不删除改动伪装干净。
5. 有修改走PR，使用模板，说明问题/结果、兼容、验证SHA、实际评审/验收身份和风险/回滚；原作者与主责自评如实标注，不冒称独立/人类评审。未完成可保留draft，不虚报所有门禁通过。
6. feature/fix/docs/test最终进dev；普通短期PR默认squash。dev→master发布和master→dev回流用merge commit，发布范围明确、全套门禁与回归完成。长期分支永久保留。
7. 同步已推分支用merge commit，不force-push。普通push non-fast-forward时核对并报告；不覆盖他人历史。共享API变更检查真实调用方、记录影响并取得必要ACK。
8. 未合并保持review；目标集成与验收均完成才done/关闭。Closes只用于最终默认目标，stacked/发布用Refs；不靠空PR关issue。
9. 回收自己已合并、无脏文件/新提交/活跃实例/上层依赖的临时分支和worktree。不要全局prune或把清理他人资源当收尾；保留资源写清用途与解除条件。

## 证据与自动检查

Rust变化：cargo fmt --check、cargo clippy --locked --all-targets -- -D warnings、cargo test --locked --all-targets，并验证真实受影响行为。跨进程服务使用 examples/runtime_stdio.py --self-test，必要时运行外部消费者兼容；provider付费/native/真人验证按明确任务范围。

脚本/CI改动检查语法和真实正/负路径；日志/命名/skip清单不能替代被测行为。required检查缺失、失败、取消或意外skip不能当通过。QA维护检查可靠性，产品实现者提供行为测试，PM/业务主责负责适用实际验收。

长命令/测试用hc后台终端保持可观察，使用hc wait而非sleep轮询；并发测试用受控协调，不靠时间碰运气。固定源码SHA、环境/产物身份、命令/退出码与证据路径；局部通过不扩称全产品/所有平台通过。旧失败根因未证实即保留未知。

## 文档和任务记录

需求在docs/requirements；设计/API在docs/design；研究在docs/research；实验在docs/experiments。docs/README只作索引，GitHub维护任务/优先级/实时进度。一个主题一份权威内容，先更新已有文档，避免复制事实；新目录先由PM确认。

文档标更新日期，区分实现事实/作者报告/提案/待验证。issue关闭前沉淀长期结论或说明无需沉淀；不在根目录/src/tests/.saki散落临时md计划，临时过程放本地不入库目录。注释只说明非显然理由、不变量或外部特殊行为。

GitHub消息写 TO/FROM/KIND/REFS，FROM是真实实例；需要Spec接收时按固定版本ACK，说明范围和第一步。阻塞写原因、真实依赖和解除条件，不能把没做完当阻塞；重要阶段由主责汇总，成员不重复发帖。

标签沿用现有type/status/agent/priority/needs体系，每个已分诊issue各有唯一type/status/agent/priority，挂适用milestone；不随意新建标签。跨仓任务保留来源issue/PR/commit证据，迁移不伪造原验收或关闭未完成MVP。

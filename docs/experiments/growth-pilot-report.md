# Growth pilot report — 2026-10-05 (two rounds)

状态：**已运行（真实本地 proxy）**。协议见 [history-dependence.md](history-dependence.md)。本文件记录结果，不作架构决策。执行者：实验子代理；主 pane 复核。

两轮使用相同协议、fixture、预算和组序，第二轮纠正进程 `API_KEY` 配置：

- Round 1 `growth-pilot-2026-10-05-01`：**配置偏差**——启动时引用的 bearer 变量不存在，进程 `API_KEY` 实际为空；本地 proxy 仍返回 200，认证路径未被检验。保留全部原始记录。
- Round 2 `growth-pilot-2026-10-05-02`：**按指定配置**——测试 bearer 经 Python subprocess 的 `env["API_KEY"]` 显式注入（仅进程环境，值未打印、未写入任何文件）。以本轮作为按指定配置的结果。

两轮均：请求模型名 `opencode-go/deepseek-v4-flash`，返回名 `deepseek-v4-flash`（路由别名，非精确权重）；单次尝试/调用、60s 超时、`reasoning.effort=none`、`temperature=0`、`max_output_tokens=4096`；harness 22/22 HTTP，无 API 错误、无截断。结果为**描述性计数，n 极小，不构成准确率或因果证明**；未跑 D/E、未进行固定配置下的足量重复、未随机化顺序。

两轮**同协议但实际状态不同**：模型两轮生成的反思指南不同（R1 233 B / R2 204 B），在线行为也有波动。不能把轮间行为差异归因于 bearer；本实验没有固定派生指南来单独检验认证配置的影响，也没有验证代理的认证与路由实现。

实际请求总计：两轮 pilot 各 22 次，加上共享的 3 次预检，共 47 次 HTTP 请求；没有额外兼容性探测或挑选结果后的补跑。

## 结果（每轮 2 个实例）

Round 1（配置偏差）：

| 实例 | A（判断） | B（无历史） | C（原始 trace） |
| --- | --- | --- | --- |
| S `E42` | pass | pass | pass |
| X `E99` | pass | fail (`InstanceCallBudget`) | pass |
| 计数 | 2/2 | 1/2 | 2/2 |

Round 2（按指定配置）：

| 实例 | A（判断） | B（无历史） | C（原始 trace） |
| --- | --- | --- | --- |
| S `E42` | fail (`InstanceCallBudget`) | pass | pass |
| X `E99` | fail (`InstanceCallBudget`) | pass | pass |
| 计数 | 0/2 | 2/2 | 2/2 |

不合并成准确率：两轮认证配置和派生指南不同，而且重复使用同两个实例，无法当作四个独立任务。

## 摘要与解释

- **组间区分度不足，且轮间波动大**：A 从 2/2 变 0/2，B 从 1/2 变 2/2，C 两轮均为 2/2。Round 1 的“A 与 C 同分”未检出派生判断相对原始 trace 的质量增益；Round 2 的 A 反而更差，也**不能**据此说所有历史通道无区别。两轮都不足以支持 Belief 层有效或无效的结论。
- **Belief 必须用质量/成本/规模增益证明，不强制在成功数上超过 C。** 两轮中 A／C 成功的测试实例都利用了当场 `inspect`；C（原始 trace，注入 1,113 B）在本任务中已是强基线。
- Round 2 A 失败的直接原因：`A/S` 为 `check → inspect → resolve E42 + read config`，`A/X` 为 `check → inspect → read config + README`，**两次都在 3 次预算内没有执行 edit**，并非被错误信息误导。Round 1 `B/X` 则是 call 13 `inspect` 已给出 `mode=safe`、call 14 `resolve E99` 得过期值、call 15 仍 `edit retry_budget=3` 后触预算。
- S 只使用**一个同类未见实例，两轮各运行一次**；广泛迁移与长期成长**未验证**。fixture 是模拟配置环境，不是真实工程项目。
- 反思（仅 A）两轮都有更新，但内容均针对训练 symptom `E17`（Round 1 233 B / Round 2 204 B），未包含区分 S/X 所需的“优先 inspect、别盲信 resolve”。

## 成本（输入/输出分开；A 含反思准备开销）

Round 1：

| 项 | 请求 | 输入 | 输出 |
| --- | --- | --- | --- |
| 训练（共享） | 3 | 1,968 | 376 |
| 反思（A 准备开销） | 1 | 482 | 300 |
| A 在线（S+X） | 6 | 4,347 | 787（工具 12） |
| B 在线 | 6 | 4,058 | 752（工具 12） |
| C 在线 | 6 | 5,895 | 731（工具 9） |
| 合计 | 22 | 16,750 | 2,946（reasoning 1,327） |

Round 2：

| 项 | 请求 | 输入 | 输出 |
| --- | --- | --- | --- |
| 训练（共享） | 3 | 2,127 | 415 |
| 反思（A 准备开销） | 1 | 482 | 213 |
| A 在线（S+X） | 6 | 3,627 | 376（工具 8） |
| B 在线 | 6 | 3,999 | 765（工具 12） |
| C 在线 | 6 | 5,817 | 675（工具 8） |
| 合计 | 22 | 16,052 | 2,444（reasoning 1,047） |

- A 的在线输入两轮都低于 C，但 A 的反思是额外开销；在线输出与工具次数轮间不一致。**不能笼统称“更低成本”**，也不能据此判断工具效率。
- 仅报 token 与请求数，不等同货币成本。

## 限制（必须随结果阅读）

- **该 pilot 未集成主 agent loop、未调用 compact**：实验为新建 `Conversation` + 有限 state 注入，不能声称验证长会话 context 管理。后续自动 compact 的实现与验收见[实现文档](../design/conversation-compaction.md)，不改变这里的实验范围。
- **用户投入未测**（无统一人工纠正/救援协议）；模型/工具调用仅执行开销。
- fixture 为模拟配置，非真实工程；S 仅一个同类未见实例；n 极小、轮间波动大。
- Proxy 控制参数转发除 `max_output_tokens` 外未验证；`effort=none` 仍产生 reasoning；返回名是别名。
- Round 1 认证未检验；Round 2 已显式配置，但认证是否被 proxy 校验仍未单独确认。

## 复现

必须使用**新 run-id**；已存在的非空 artifact 目录会被拒绝覆盖（`prepare_artifacts`）。

```
python3 - <<'PY'
import os, subprocess
env = dict(os.environ); env["API_KEY"] = "<test-bearer>"   # 仅进程环境
subprocess.run(["cargo","run","--locked","--example","growth_experiment","--",
    "--real","--run-id","<new-run-id>",
    "--model","opencode-go/deepseek-v4-flash",
    "--base-url","http://127.0.0.1:3425/v1"],
    cwd="/Users/heimaolst/Workspace/Rust/Saki", env=env)
PY
```

原始数据（相对链接）。两轮 `preflight/` 保留了本次手动预检的记录副本；交付后的 harness 仅创建独立 run 目录，不再依赖临时预检文件：

- Round 1（配置偏差）：[raw_results.json](../../examples/growth_experiment_artifacts/growth-pilot-2026-10-05-01/raw_results.json) · [state/A/belief.txt](../../examples/growth_experiment_artifacts/growth-pilot-2026-10-05-01/state/A/belief.txt) · [state/C/trace.json](../../examples/growth_experiment_artifacts/growth-pilot-2026-10-05-01/state/C/trace.json) · [preflight/](../../examples/growth_experiment_artifacts/growth-pilot-2026-10-05-01/preflight/)
- Round 2（按指定配置）：[raw_results.json](../../examples/growth_experiment_artifacts/growth-pilot-2026-10-05-02/raw_results.json) · [state/A/belief.txt](../../examples/growth_experiment_artifacts/growth-pilot-2026-10-05-02/state/A/belief.txt) · [state/C/trace.json](../../examples/growth_experiment_artifacts/growth-pilot-2026-10-05-02/state/C/trace.json) · [preflight/](../../examples/growth_experiment_artifacts/growth-pilot-2026-10-05-02/preflight/)

# 自动压缩：百万窗口网关实验

日期：2026-10-05。模型：`opencode-go/deepseek-v4-flash`；入口：本地网关 `/v1/responses`。用户授权按真实百万窗口测试，不降低应用容量，不使用 tokenizer。初始轮只创建实验 harness 和文档；随后用户授权参考 Pi 实施恢复，修复后的结果见本文末尾。

## 配置与原始证据

直接复用当前 `Agent`、`Conversation`、`DeepseekClient` 与 `ToolRegistry`，应用容量为 **1,000,000**，正常输出预留 8,192，摘要输出上限 4,096，近期原文目标 8,192。请求前触发线为 991,808。录制代理向真实网关转发 JSON，保存请求、响应、状态、usage 和 request hash，不保存认证头。

原始目录：`.saki/verification/compaction-1m/1791197454583429000/`。`report.json` 包含实验结果、源码 SHA256 和验收检查；`http.jsonl` 是每次上游尝试的索引；`http/` 保存原始请求与响应；`cases/` 保存 stdin、stdout、stderr 和原始会话日志；`harness/` 保存初始实验实现。数据目录已被 gitignore 排除，避免将大块重复材料加入源码仓库。

以重复的无执行意义 Rust 文本 `let x = 0; ` 构造容量数据，只要求保留 `ion-42` 和 `observe` 两个标记。两次小请求实际 usage 为 6,364 和 60,364，测得每次重复增量为 6 tokens。此结果只用于生成同一种测试材料，不能当作任意文本的通用计数规则。

近窗口前缀只真实生成一次响应，后续分支重放同一份原始输入、模型输出和 usage，然后进入当前 Agent。这个实验分叉不代表产品已实现恢复会话 API。

## 实际结果

| 场景 | 真实请求与观测 | 结论 |
| --- | --- | --- |
| 近窗口前缀 | input **979,997**、output 39；36.073 秒，HTTP 200、completed | 入口能完成接近百万 token 的请求 |
| 追加 64,320 bytes 用户输入 | 自动压缩计数 1,001,492 → 21,888；摘要实际 input 979,901、output 470；继续请求 input 35,531、output 46 | 完整保留新增用户消息，正确恢复两个旧标记；此分支通过 |
| 加入 96,000 bytes 工具结果 | 工具调用 input 980,075；压缩计数 1,012,174 → 32,620；摘要 input 979,949、output 574 | checkpoint 成功，但继续请求收到协议 400，验收失败 |
| 首条输入超窗口 | 上游报告 messages **1,100,003** + completion 8,192 = **1,108,195**；三次 HTTP 400，code=`context_length_exceeded` | 当前程序原样重试三次后退出，没有 overflow recovery |
| 加入 3,300,000 bytes 工具结果 | 真实工具调用完成，完整结果进入原始日志；摘要未发往上游，被实验本地调用次数上限拦截 | 此场景未完成，不能用来证明真实摘要后的预算拒绝行为 |
| 工具协议定位探针 | 在失败请求中仅补回原来的最近用户消息；input 53,007、output 112；HTTP 200、completed | 返回 `project=ion-42;mode=observe;result=37`，定位到当前切分边界问题 |

before／after 是应用计数，其中新增内容和压缩后的首次请求使用字节估算，不能与实际 API usage 混用。

网关 `/v1/models` 宣告 1,000,000 容量，超长错误正文报告硬上限 **1,048,576**。两者在本次入口不同；实验仍按用户要求使用 1,000,000 应用预算。未做边界二分，也不能据单次错误证明所有路由、模型版本都采用同一硬上限。

实际共 **15 次上游 POST 尝试**。最初上限 14 次；工具协议失败增加了两次原样重试，耗尽初始预算，因此巨大工具场景没有完成。随后仅放宽到最多 16 次，为小窗口协议定位预留两次；第一条探针即成功，没有追加百万输入请求。成功响应的 usage 合计 input **5,055,263**、output **1,404**；缓存量已包含在 input 中，不重复相加。错误响应没有 usage，不能据此计算完整账单。

## 两个可复现问题

### 1. 确定性错误被当作可重试错误

[Client](../../src/client/responses.rs#L305) 将非成功 HTTP 响应转为文本错误，[respond](../../src/client/responses.rs#L330) 对所有发送／状态错误原样尝试三次。本轮三个超长请求的 request hash 完全相同。协议 400 也重复三次。

首次巨大输入还没有 usage 基线；约 2 MB 的序列化请求按 bytes/3 估算只有约 672K，因此没有提前 compact。这验证了估算不能保证首次请求不会溢出，也说明必须接收上游错误作为兜底。这个首次消息没有可压缩的旧历史，强行 compact 也不能凭空腾出空间。

### 2. 工具配对完整，不代表协议上下文完整

[切分函数](../../src/agent/conversation.rs#L338) 允许 tail 从 reasoning 开始，本例 `keep_from=5`，把最近用户请求移入摘要。发送的 tail 确实保留了 reasoning、function call、同 call_id 的完整 output，但上游仍返回 `reasoning_text` 缺失错误。

原失败请求 `http/06-tool-trigger/request.json` 与定位探针 `http/15-protocol-preserve-user/request.json` 的唯一内容变化是补回原来的最近用户消息；摘要角色、reasoning、调用和结果保持不变。定位探针成功，但没有把这项变更应用到生产代码，因此不能将原工具分支标记为通过。

[OpenAI reasoning 文档](https://developers.openai.com/api/docs/guides/reasoning#keeping-reasoning-items-in-context) 也要求手动裁剪时保留最近用户消息到 function output 之间的完整项。该说明针对 OpenAI 模型；本轮本地 DeepSeek 路由的约束由实际对照请求支持，不是据 OpenAI 文档推断。

## 初始建议与结论边界

初始轮建议先修复工具 continuation 的切分边界，再增加分类明确、次数有限的 overflow recovery：Client 返回上下文超限类型；Agent 在当前模型请求失败后调用 Conversation 强制压缩；上下文确实缩减后才重试一次。已经执行过的工具结果沿用日志和内存，不重新执行工具。

若最新单条输入本身放不下，或摘要请求自身仍然超限，需要明确失败或另行设计裁剪／分块；不能宣称 compact 可以处理所有超长请求。第三方具体实现与方案权衡见[研究文档](../research/systems/context-compaction.md#上游超限错误恢复)。上述机制在初始轮未实施；后续已按用户授权实现，见下节。

本实验主要覆盖计数、触发、协议与失败行为。重复文本容易总结，不能证明真实长工程对话的摘要质量；百万窗口、复杂多工具、重复 compact 的真实模型验收也不能由本轮小样本替代。原 mock 回归通过与本轮工具分支失败同时成立，说明既有测试缺少该 provider continuation 约束。

## Pi 风格恢复实施后的复验

同日用户授权实现。修改范围：Client 错误分类与重试、Conversation 强制 compact 和用户消息切点、Agent 单轮执行与有限恢复。仍然使用 1,000,000 应用容量，无 tokenizer，无 slash command。主程序现在在单轮失败后继续终端会话，不再直接退出。

原始目录：`.saki/verification/overflow-recovery/1791199185398761000/`，包含新 `report.json`、逐请求 HTTP 记录、原始 journal、输入／输出及实际使用的 harness。复用前一轮真实前缀与 READY 响应，无需再次生成百万前缀。

| 场景 | 观测 | 验收 |
| --- | --- | --- |
| usage 基线缺失时的真实 overflow recovery | 刻意不恢复原 usage 基线，模拟用量缺失；本地估算 654,068，上游报告 input 1,070,069 + output reserve 8,192 超过硬上限；随后摘要 input 979,901，重试 input **90,485**，正确保留旧标记与新增用户原文 | HTTP 顺序 **400 → 200 → 200**；一次 checkpoint，通过 |
| 工具 continuation 回归 | 96 KB 结果进入 history，自动压缩计数 1,012,168 → 32,528；最新用户消息、reasoning、call 与完整 output 原样保留；继续请求 input **52,889** | HTTP 全为 200，回答 `project=ion-42;mode=observe;result=37`，通过 |
| 首条输入自身超窗口 | 同一 1,100,003-token 输入仅发送一次；没有可压缩前缀，未调用摘要，原文保留 | 当前任务明确失败，终端主循环正常返回 EOF；没有原样重试，通过 |
| 3.3 MB 工具结果自身过大 | 实际工具调用与旧前缀摘要均完成；替换后仍超过应用预算，拒绝安装 checkpoint；完整工具结果保存在原始日志，巨大结果未发给正常模型请求 | 两次上游请求；当前任务明确失败，通过 |

`unmeasured` 仅是实验中不恢复 usage 的状态设置，不是新的产品选项，也不代表已提供产品会话恢复 API。摘要、正常请求与错误均来自真实网关；没有用 mock overflow 替代第一行的超长请求。

本轮共 **9 次上游 POST**，上限 10 次；有 usage 的成功响应合计 input **5,043,227**、output **1,768**，缓存量已包含在 input。首条超长输入的错误响应此次耗时约 183 秒；不会重试同一确定性错误，不意味着第一次错误能够立即返回。

离线主程序 **29 项通过**：12 项 Conversation、6 项 Agent、5 项 Client、6 项模型目录测试。成长实验 **29 项通过**：原有 12 项加复用的 12 项 Conversation 和 5 项 Client。另通过构建、相关文件格式检查与 `git diff --check`，没有改动用户已有的 Cargo.lock。

离线覆盖还验证了真实复验没有刻意重复触发的边界：第二次 overflow 不再 compact，摘要 overflow 不递归，成功工具响应后下一模型 step 可以重新恢复，工具执行计数始终为一次。单条巨大输入、巨大最新工具链仍然不能靠旧历史摘要解决；巨大工具结果目前还会消耗一次摘要调用后才发现无法满足预算。有界工具输出／分块仍未实现，复杂任务摘要质量也未由本实验验证。

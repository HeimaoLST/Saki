# Saki

Saki 是独立的 Agent Runtime，为 Nagi、Rous 和其他应用提供统一的 Agent 执行服务。对外调用接口属于 Runtime 自身。

Saki 负责模型调用、工具与 MCP、工作上下文和压缩、session/task、持久化、取消、容量以及执行事实。联系人、事项和产品完成规则归 [Nagi](https://github.com/HeimaoLST/Nagi)；终端交互、草稿与本地命令归 [Rous](https://github.com/HeimaoLST/Rous)。两个产品分别构建和发布，通过公开协议消费 Saki。

## 构建与启动

使用 Rust 1.99.0 或兼容 Rust 2024 edition 的工具链：

```sh
cargo build --locked
./target/debug/Saki --version
API_KEY=your-key SAKI_BASE_URL=https://provider.example/v1 \
  SAKI_MODEL=your-model SAKI_API=openai-responses ./target/debug/Saki
```

默认入口与 `--stdio` 都提供 version 1 JSONL 服务。stdout 仅输出机器响应和事件，stderr 输出诊断；不会选择或启动终端界面。`--help` 和 `--version` 不初始化模型服务。连接与工具也可通过 `SAKI_RUNTIME_CONFIG` 指定的 HostConfig 配置。

启动不会隐式创建或恢复 session。调用方通过 `session.create/load` 选择会话、`task.send` 提交输入，并按 session/task/message ID 接收事件；`task.cancel` 定向取消。历史和状态查询不会重跑模型或工具。

```json
{"version":1,"id":"create-1","method":"session.create","params":{}}
{"version":1,"id":"send-1","method":"task.send","params":{"session_id":"RETURNED_ID","message":{"text":"Read README.md"}}}
```

完整协议、能力发现、错误、快照、元数据订阅与连接生命周期见 [Runtime API](docs/design/runtime-api.md)。不同 session 可在容量内并发，同一 session 只接受一个活动任务；不隐式排队，不承诺工作目录文件隔离。

## 配置、历史与生命周期

- 模型协议：`openai-responses` 或 `openai-chat-completions`。已验证组合与限制见[模型调用分层](docs/design/model-runtime.md)。
- 数据位置：`SAKI_SESSION_DIR`，默认工作目录下 `.saki/sessions/`；拆仓不会移动旧数据或更换已有 session ID。
- 输入/观察：`session.snapshot` 返回 Runtime 计算的历史、会话状态与上下文信息；`runtime.metadata.subscribe` 可显式订阅元数据失效通知。
- 自动标题：`SAKI_AUTOMATIC_TITLES=true` 显式启用，默认 false；Rous 的独占宿主按自己的行为启用。
- 停机：stdin EOF 或 `runtime.shutdown` 停止接纳并排空已接受任务；取消与关闭不同。当前是调用方拥有的独占进程，没有常驻多客户端、透明重连或断线续传承诺。

现有 metadata/schema、API 和连接身份检查继续有效。恢复只报告已保存或不确定状态，不自动重放工具，也不把接纳当作执行完成。

## 开发与验证

```sh
cargo fmt --check
cargo clippy --locked --all-targets -- -D warnings
cargo test --locked --all-targets
python3 examples/runtime_stdio.py --self-test --binary target/debug/Saki
python3 -m unittest discover -s scripts -p 'test_*.py'
```

stdio 自测使用受控本地 provider 和真实工具，不调用付费模型。实际第三方兼容、产品原生交互和真人使用分别记录，不能由这些检查代替。

Runtime 构建不需要 Go、MyGo、ratatui、clipboard 或前端源码。CI 生成带版本、源码 SHA、run/attempt 和文件 hash 的独立 Runtime 产物；消费者核对公开协议和实际产物身份，而非要求三仓同一个提交。[CI 契约](docs/design/ci.md)

## 来源与产品边界

本次仓库拆分由 [#333](https://github.com/HeimaoLST/Saki/issues/333) 跟踪；边界、兼容与回滚见[产品仓库划分](docs/design/product-repositories.md)。Nagi/Rous 接手原候选 `00298ae22bb95afc11366597e517a3b1ceba21e7` 的产品成果，保留原始提交历史和未完成验收记录。拆仓不表示原 Nagi MVP、原生、真实 A/B 或真人试用通过。

[文档索引](docs/README.md)包含实现事实、历史证据、设计与研究。长期身份和成长仍是独立研究，尚未接入主 Agent，也不作为当前 Runtime 或产品交付前置。

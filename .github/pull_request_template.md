## 问题与结果

说明具体触发条件、变更后的行为与用户/调用方影响。

Refs: #
FROM: agent:
REVIEWER: agent:
SPEC:

## 范围与兼容

- 影响的运行能力、公开方法/事件/错误、配置、持久化及真实消费者。
- 声明共享文件：Cargo.toml/Cargo.lock、AGENTS.md、README.md、.github/。
- 兼容性判断、迁移与回滚；不把客户端展示或产品规则加入 Runtime。
- base/目标分支、依赖和合并责任；用户授权或PM集成处置权来源。

## 验证与证据

记录实际源码SHA、命令/退出码、产物身份和适用边界；未运行/失败/既有Skip如实说明。

- [ ] cargo fmt --check
- [ ] cargo clippy --locked --all-targets -- -D warnings
- [ ] cargo test --locked --all-targets
- [ ] 受影响的行为回归及真实 stdio/外部消费者调用（适用时）
- [ ] Python/CI支持检查（适用时）
- [ ] required CI在实际交付SHA真实通过
- [ ] Runtime产物、协议/存储兼容、docs事实与来源核对

自动检查、局部受控运行、真实provider、原生产品和真人使用分别记录；不以本仓绿灯宣布Nagi/Rous产品验收完成。不适用项说明理由，不补无关测试。

## 收尾

实际作者、评审者、验收者如实标注。最终集成前保持review；集成后附合并证据并处理对应issue与自有资源。不要关闭未完成产品任务或清理他人资源。

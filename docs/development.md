# 开发与验证

## 架构边界

模块路径是 `github.com/liuchong/lark-agent`。生产代码、测试和依赖图不得引用
`github.com/larksuite/cli` Go 包。所有飞书调用只能经过 `internal/lark` 调用官方公开
Go SDK，并把 SDK HTTP 响应或 WebSocket 事件转为有类型数据。

不得复制官方 `internal/**`、`events/**` 或 `cmd/event/**`，不得执行 `lark-cli`
子进程作为生产协议。

## 目录

- `cmd/lark-agent`：进程入口。
- `agent/cmd`：命令、daemon 组合和公开 help。
- `agent/storage`：SQLite 接收回执、工作队列、动作和恢复。
- `internal/lark`：唯一飞书 SDK 适配层。
- `internal/github`：唯一 GitHub HTTP、事件解析和可信通知适配层。
- `integration_test/lark_agent`：跨包行为和隔离安装验收。
- `spec`：合并后长期行为、架构和资源订阅契约。

## 开发流程

任何行为变化先更新长期规格和 Given/When/Then 场景，再写能因缺少实现而失败的
测试，最后按 red、green、refactor 推进。每项逻辑变化必须有
`integration_test/` 覆盖。

常用验证：

```bash
make build
make unit-test
make integration-test
make harness-eval
make verify
bash -n scripts/macos/install-lark-agent.sh
swiftc macos/LarkAgentStatus/main.swift -framework AppKit -o /tmp/LarkAgentStatus
```

## Prompt 与收敛评测

模型指令分为稳定的身份/权限核心、按工作类型生成的任务流程，以及每轮由 Go 重新
生成的运行状态。运行状态包含剩余预算、已完成检查、未知项和最后失败门；上下文压缩
后也会重新注入。提示词只解释当前处境，Workspace、权限、证据、预算和发送边界仍由
Go 拒绝路径强制执行。

`make harness-eval` 使用脱敏的脚本模型和固定工具回执，逐项显示并检查：不依赖固定
措辞的部分成果、无需伪造代码读取的澄清、相同结果的重复调用熔断，以及无依据代码
断言被拒绝后安全收敛。场景目录检查终态类型以及声明的模型/工具调用上限；各可执行
用例断言对应行为所需的终态和调用边界。它不访问网络、不读取模型密钥，也不替代
`make verify`。候选持久化、语义复查、重复发送和 Owner 终止通知由同一集成测试包中
的其他测试覆盖。

评测 fixture 只能使用合成消息号、会话号、路径和源码，不得保存真实飞书消息、凭据
或私有数据库内容。当前门禁固定使用脚本模型，尚不提供真实模型探针；更换真实模型
不属于默认离线验证范围。

完整交付还必须执行：

```bash
gofmt -w .
go test -race ./...
go vet ./...
go mod tidy
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.6 run
actionlint .github/workflows/lark-notify.yml
```

并确认：

- `gofmt -l .` 无输出；
- `go.mod`、`go.sum` tidy 后无意外变化；
- `go list -m all` 不包含官方 CLI Go 模块；
- 生产代码无旧 service label、旧内嵌包路径或官方内部命令；
- shell、plist、Swift 和临时 HOME 安装测试全绿；
- 没有 secret、token、私钥或无关文件进入提交。

## SDK 边界测试

单元和集成测试使用可控的 fake SDK caller、`httptest` 服务和 typed event fixture，
覆盖成功、结构化错误、超时、断流、重复和缺字段。测试不得模拟或复制官方 CLI
内部包。

live 验收必须在代码完成验证、提交并安装该提交之后执行；不得部署未提交二进制。

## GitHub Action 边界测试

`workflow_run` 具有读取受保护 secret 的能力，因此必须把触发事件全部当作不可信
数据。集成测试必须证明工作流只检出默认分支的 Action 实现，不引用触发 run 的
`head_sha`，不下载 artifacts，也没有执行 PR 内容的 `run` 步骤。

本地可用 `github notify --dry-run` 验证消息结构。真实同账号并发验收时，只启动现有
daemon 的一个 WebSocket 消费者，再独立运行一次 HTTP-only notify；不能为 Action
启动第二个 daemon。消息必须发送到用户明确授权的精确 chat ID，不能按群名猜测。

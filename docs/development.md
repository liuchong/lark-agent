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
make verify
bash -n scripts/macos/install-lark-agent.sh
swiftc macos/LarkAgentStatus/main.swift -framework AppKit -o /tmp/LarkAgentStatus
```

完整交付还必须执行：

```bash
gofmt -w .
go test -race ./...
go vet ./...
go mod tidy
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.6 run
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

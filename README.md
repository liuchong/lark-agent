# lark-agent

`lark-agent` 是一个基于官方公开 Go SDK（official Go SDK）的个人 AI Agent。它在 macOS 本地运行，
通过 `github.com/larksuite/oapi-sdk-go/v3` 读取消息、消费 WebSocket 实时事件并
发送回复；它不执行 `lark-cli` 子进程，不解析 stdout/NDJSON，也不复制官方 CLI 的
内部代码。

## 能做什么

- Owner 在“Assistant Bot”私聊机器人，或在群里直接 @机器人时，机器人回答
  Owner；处理期间添加键盘工作表情，结束后删除。
- 他人在群里直接 @Owner 时，Agent 可按策略用 Owner 身份回复；回复成功后再由
  机器人私聊 Owner 说明已经回复以及仍需处理的事项。
- 没有显式引用时使用同一会话内的邻近消息；有引用或 thread 时沿引用关系读取，
  不会把其他群或私聊的内容混入上下文。
- 编程问题可在配置的 Workspace 内使用有边界的代码搜索、文件读取和 shell
  工具。路径逃逸、符号链接逃逸、秘密文件和无边界搜索会被代码拒绝。
- 所有消息、模型步骤和外部动作都写入 SQLite 账本。跨重启工作不会自动回放。

## 要求

- macOS
- Go 1.25 或更新版本（从源码构建时）
- Lark 自建应用的 `app_id`，以及写入 macOS Keychain 的 app secret；用户 token 可选，
  只用于用户身份轮询和代回复
- 一个 OpenAI 兼容模型；密钥只放在当前用户的环境或私有工具配置中

`lark-agent` 不维护第二个 `lark-cli` Fork，也没有 Linux/Windows 安装器或传输插件
接口。

## 快速开始

先初始化独立配置：

```bash
go build -o ./lark-agent ./cmd/lark-agent
./lark-agent init \
  --workspace /absolute/path/to/workspace \
  --app-id cli_xxx \
  --owner-open-id ou_xxx
./lark-agent auth login < /path/to/private-lark-credentials.json
./lark-agent doctor --lark-only
```

需要模型时，在安装前为当前 shell 设置模型环境：

```bash
export OPENAI_API_KEY='...'
export OPENAI_BASE_URL='https://example.com/v1'
export OPENAI_MODEL='model-name'
```

安装当前用户的 macOS LaunchAgent 和状态栏：

```bash
./scripts/macos/install-lark-agent.sh
```

安装器按顺序执行完整 SDK/Keychain doctor、编译状态栏，最后才加载
`com.liuchong.lark-agent`。新进程没有进入 ready 状态时会立即卸载。安装器不读取
旧目录，也不迁移历史数据。

首次 live 验收只在“Test Group”和“Assistant Bot”私聊进行，不在 Example Group 群发送测试
消息。

## 常用操作

```bash
lark-agent daemon status
lark-agent mode paused
lark-agent mode auto
lark-agent queue summary
lark-agent queue inspect --message-id om_xxx
lark-agent queue resume --message-id om_xxx
```

离线积压和中断工作只有在指定 work ID 或 message ID 后才可恢复。已经完成、忽略、
取消或进入死信的终态工作还必须明确加 `--force-terminal`。结果不确定的外部动作
不会自动重发。

## 文档

- [macOS 安装](docs/install-macos.md)
- [配置说明](docs/configuration.md)
- [运行、恢复与故障处理](docs/operations.md)
- [开发与验证](docs/development.md)
- [长期行为规格](spec/behavior.md)
- [架构边界](spec/architecture.md)
- [Lark SDK 边界](spec/lark-sdk-boundary.md)

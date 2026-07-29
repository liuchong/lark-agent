# lark-agent

`lark-agent` 是一个基于官方公开 Go SDK（official Go SDK）的个人 AI Agent。它在 macOS 本地运行，
通过 `github.com/larksuite/oapi-sdk-go/v3` 读取消息、消费 WebSocket 实时事件并
发送回复；它不执行 `lark-cli` 子进程，不解析 stdout/NDJSON，也不复制官方 CLI 的
内部代码。

## 能做什么

- 只有 Owner 可以私聊“Assistant Bot”或在允许的群里直接 @机器人提问、要求执行
  操作，机器人用机器人身份回答。可以通过 `assistant.reply_scope` 选择所有群或仅
  配置群；处理期间添加键盘工作表情，结束后删除。
- 非 Owner 私聊机器人或直接 @机器人时保持静默；用户身份可见的真人私聊，以及
  群里直接 @Owner 的消息，才可能触发只读的智能代回复。
- 他人在任意用户可见群里直接 @Owner 时，Agent 默认可按策略用 Owner 身份回复；
  回复成功后再由机器人私聊 Owner 说明已经回复以及仍需处理的事项。可以通过
  `policy.reply_scope` 改回仅允许配置群。
- 代回复先进入 `policy.owner_wait`（默认 3 分钟）的持久等待。到期后按语义判断
  Owner 是否已经回答每一条具体消息；只处理仍未回答的消息。判断不清、上下文不全
  或模型输出异常时不会瞎答，而是按 `policy.owner_reply_retry` 延后重试。
- 代回复会读取目标消息之后的同会话讨论和并发待回复问题，判断 Owner 的非引用回复
  实际对应哪一条；不会把其他群或其他私聊内容混入上下文。
- 编程问题可在配置的 Workspace 内使用有边界的代码搜索、文件读取和 shell
  工具。路径逃逸、符号链接逃逸、秘密文件和无边界搜索会被代码拒绝。
- 非 Owner 触发的群内请求只能读取当前群上下文和 Workspace 业务证据，不能执行
  shell、跨群搜索、修改、删除、提交或部署。环境刺探和工作目录外路径请求在模型
  调研前直接拒绝。
- 代回复必须先完成可审计的相关读取，并简要说明已检查内容、初步发现或明确未知；
  “已提醒 Owner”、复述原问题和未经批准的后续承诺不会发送。
- GitHub Actions 可以用同一个 Lark 应用身份把可信的工作流结果发进指定群。Action
  只通过 HTTP 发送一次消息，不启动第二个 WebSocket 监听；常驻 Agent 验证被引用的
  机器人消息及其 HMAC 签名后，才允许模型按该引用读取有界、只读的 GitHub 事实。
- 所有消息、语义判断、模型步骤和外部动作都写入 SQLite 账本。纯等待消息可在重启
  后重新读取 Lark 再判断；旧模型草稿、审批和外部动作不会自动回放。

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

`CHAT_QUERY` 只用于发现和标记配置群与验收群。默认
`assistant.reply_scope: all_groups`、`policy.reply_scope: all_groups` 和
`policy.private_reply_scope: all_private` 时，它不会
限制 Owner 在其他群正常 `@机器人`，也不会限制其他人在其他群 `@Owner`。真实 live
验收仍只在本次明确授权的群和机器人私聊中发送测试消息。

## 常用操作

```bash
lark-agent daemon status
lark-agent mode paused
lark-agent mode auto
lark-agent queue summary
lark-agent queue inspect --message-id om_xxx
lark-agent queue resume --message-id om_xxx
lark-agent github auth status
```

离线积压和中断工作只有在指定 work ID 或 message ID 后才可恢复。已经完成、忽略、
取消或进入死信的终态工作还必须明确加 `--force-terminal`。结果不确定的外部动作
不会自动重发。

## GitHub 与 Lark

仓库根目录的 `action.yml` 和 `.github/workflows/lark-notify.yml` 提供完整通知路径。
`workflow_run` 工作流只检出可信默认分支上的 Action 实现，不检出 PR 头、不下载
不可信产物，也不执行 PR 中的代码。GitHub Environment `lark-production` 保存 Lark
app secret 和目标 chat ID；仓库配置不保存这些秘密或部署值。

本地常驻 Agent 的 GitHub 读取令牌单独放在 macOS Keychain：

```bash
lark-agent github auth login < /path/to/private-github-token.json
lark-agent github auth status
lark-agent doctor
```

登录 JSON 只有 `token` 字段。该令牌只用于读取已验证引用所指向的仓库、PR、
workflow run、检查结果和审查信息，不提供评论、合并、重跑、取消或其他写能力。

## 文档

- [macOS 安装](docs/install-macos.md)
- [配置说明](docs/configuration.md)
- [运行、恢复与故障处理](docs/operations.md)
- [开发与验证](docs/development.md)
- [长期行为规格](spec/behavior.md)
- [架构边界](spec/architecture.md)
- [Lark SDK 边界](spec/lark-sdk-boundary.md)

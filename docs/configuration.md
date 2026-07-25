# 配置说明

配置默认位于 `~/.config/lark-agent/config.yaml`。建议先运行：

```bash
lark-agent init \
  --workspace /absolute/path/to/workspace \
  --app-id cli_xxx \
  --owner-open-id ou_xxx
lark-agent auth login < /path/to/private-lark-credentials.json
lark-agent config show
```

`auth login` 从标准输入读取 JSON，字段为 `app_secret`、可选的 `user_access_token`
和可选的 `refresh_token`。这些值只写入 macOS Keychain，不会写入配置文件、stdout
或 plist。首次登录必须提供 `app_secret`；如果 Keychain 里已经有 `app_secret`，
后续可以只提供 `user_access_token` 和可选的 `refresh_token` 来补齐用户身份能力。

## 关键字段

- `lark.app_id`：Lark 自建应用的 app id，用于官方公开 Go SDK。
- `lark.keychain_service`、`lark.app_secret_keychain_key`、
  `lark.user_token_keychain_key`、`lark.refresh_token_keychain_key`：macOS
  Keychain 中的凭据引用。配置只保存引用，不保存 secret 或 token。
- `lark.base_url`：可选的 Lark/Feishu OpenAPI 域名覆盖，通常留空。
- `lark.subscriptions`：文档、Wiki、Base 监控订阅的非秘密配置投影；运行态状态仍以
  SQLite 为准。
- `owner.open_id`：唯一 Owner 的 open ID。
- `assistant.open_ids`、`assistant.names`：Owner 私聊和群 @机器人的识别身份。
- `assistant.owner_direct.enabled`：是否接受 Owner 直接发给机器人的请求。
- `workspace.root`：唯一 Workspace，必须是绝对路径。
- `workspace.excludes`：不可读写的秘密或构建目录模式。
- `policy.mode`：`auto`、`approval` 或 `paused`。
- `policy.allow_chats`、`block_chats`、`block_users`：确定性的会话和用户边界。
- `scheduler.*`：不同工作通道的 lease 和 worker 数量。
- `agent.*`、`tool_policy.*`、`goal.*`：模型轮次、工具输出、无进展和长任务上限。

配置不保存官方 Lark 凭据，也没有模型密钥字段。Lark app secret 放在 macOS
Keychain；用户 access token 和 refresh token 可选，只用于用户身份轮询和代回复。
模型密钥放在当前用户环境或安装器创建的权限为 `0600` 的私有 env 文件中。

## 模式

```bash
lark-agent mode auto
lark-agent mode approval
lark-agent mode paused
```

- `auto`：通过代码策略门禁后可自主回复。
- `approval`：保存精确草稿，等待 Owner 批准。
- `paused`：停止领取新工作和未发送副作用；已完成审计记录保留。

模式变化不会扩大 Workspace、身份、会话或飞书权限边界。

## Workspace

Workspace 是 Agent 唯一可以操作的本地业务目录。路径解析会拒绝：

- 相对路径和 `..` 逃逸；
- 指向 Workspace 外部的绝对路径；
- 逃出 Workspace 的符号链接；
- `.env*`、私钥和配置中排除的路径；
- 未建立 macOS 沙箱时的 shell 执行。

这些限制由 Go 代码和操作系统沙箱执行，不依赖模型遵守提示词。

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
- `assistant.reply_scope`：群内 @机器人范围。`all_groups` 是默认值，允许任意真人在
  任意机器人可见群里原生 `@机器人`；`configured_groups` 只允许 daemon
  `--chat-query` 发现的群，并要求启动时查询至少匹配一个机器人可见群。
- `workspace.root`：唯一 Workspace，必须是绝对路径。
- `workspace.excludes`：不可读写的秘密或构建目录模式。
- `policy.mode`：`auto`、`approval` 或 `paused`。
- `policy.reply_scope`：群内代回复范围。`all_groups` 是默认值，允许任意可见群里的
  直接 `@Owner` 进入其余回复门禁；`configured_groups` 只允许 daemon
  `--chat-query` 发现的群，并要求启动参数提供非空群关键词。
- `policy.allow_chats`、`block_chats`、`block_users`：确定性的会话和用户边界。
- `scheduler.*`：不同工作通道的 lease 和 worker 数量。
- `agent.*`、`tool_policy.*`、`goal.*`：模型轮次、工具输出、无进展和长任务上限。

默认 `agent.max_context_bytes` 为 `65536`，初始业务上下文进一步限制在约
48 KiB；规则、代码和技能按需读取，避免把大目录和历史消息一次性塞给模型。
`tool_policy.coding_max_tool_calls` 默认为 `16`，只有成功执行的调查工具才消耗这项
额度；参数或策略校验失败仍受模型轮次和无进展上限约束，但不会挤掉后续生产代码读取。

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

## 强制保护

以下保护没有关闭开关：

- 任何本地读取和操作都限制在 `workspace.root` 的真实路径内；明确要求访问目录外
  路径时直接拒绝。
- 只回答具体业务问题，不回答凭据、用户目录、环境变量、进程、网络、已安装工具等
  工作环境刺探问题。
- 非 Owner 触发的 `@机器人` 或 `@Owner` 工作一律只读，只能使用当前群上下文和
  Workspace/代码读取工具；shell、跨群搜索和所有副作用工具不会出现在模型工具列表，
  即使伪造调用也会在执行器前被拒绝。
- 代回复交办或调研问题前必须产生成功的相关读取记录，并在短回复中说明已完成检查、
  初步发现或明确未知。空确认、复述和未经批准的未来承诺会被发送前门禁拒绝。
- 所有自动回复使用 `policy.reply_confidence_min` 的实际配置值，直接 @Owner 或
  @机器人不会获得更低的隐藏阈值。

## 群回复范围

当前实际安装建议明确配置：

```yaml
assistant:
  reply_scope: all_groups

policy:
  reply_scope: all_groups
```

这两个字段相互独立：

- `assistant.reply_scope` 控制任意真人在群里 `@机器人` 后，机器人是否接收并用
  机器人身份回答。
- `policy.reply_scope` 控制任意真人在群里 `@Owner` 后，Agent 是否可以用 Owner
  身份代回复。

它们只放开“群范围”这一道门。黑名单、模型相关性与风险判断、置信度和审批模式、
撤回检查与幂等发送仍然生效。Owner 等待和“Owner 已回复”检查只适用于代回复，
不适用于直接发给机器人的问题。

需要重新限制到验收群时改为：

```yaml
assistant:
  reply_scope: configured_groups

policy:
  reply_scope: configured_groups
```

此模式必须同时为 daemon 提供 `--chat-query`。`--chat-query` 负责发现并标记允许群；
机器人范围在启动时用机器人身份把查询解析成具体群 ID；查询不到任何机器人可见群时
启动会明确失败。在 `all_groups` 模式下，查询只保留群发现和验收用途，不会限制其他
群里的正常 `@机器人` 或 `@Owner`。切换范围不会自动重放历史终态或中断工作。

## Workspace

Workspace 是 Agent 唯一可以操作的本地业务目录。路径解析会拒绝：

- 相对路径和 `..` 逃逸；
- 指向 Workspace 外部的绝对路径；
- 逃出 Workspace 的符号链接；
- `.env*`、私钥和配置中排除的路径；
- 未建立 macOS 沙箱时的 shell 执行。

这些限制由 Go 代码和操作系统沙箱执行，不依赖模型遵守提示词。

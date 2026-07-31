# 配置说明

配置默认位于 `~/.config/lark-agent/config.yaml`。建议先运行：

```bash
lark-agent init \
  --workspace /absolute/path/to/workspace \
  --app-id cli_xxx \
  --owner-open-id ou_xxx \
  --owner-name "姓名" \
  --preferred-language zh-CN
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
- `lark.base_url`：可选的 Lark/Feishu OpenAPI 域名覆盖。本地已有配置可留空沿用 SDK
  默认值；GitHub Action 必须显式传入，国际版使用
  `https://open.larksuite.com`。
- `lark.subscriptions`：文档、Wiki、Base 监控订阅的非秘密配置投影；运行态状态仍以
  SQLite 为准。
- `github.enabled`：是否启用可信 GitHub 证据桥。默认关闭。
- `github.allowed_repositories`：允许通知和后续读取的精确 `owner/repository`
  列表；不支持通配符，也不能由模型或消息正文扩大。
- `github.api_base_url`：GitHub 或 GitHub Enterprise 的 API 根地址。
- `github.token_keychain_service`、`github.token_keychain_key`：本地只读 GitHub
  令牌的 Keychain 引用。
- `github.max_files`、`max_patch_bytes`、`max_annotations`、`max_reviews`：
  单次模型读取的硬上限。
- `owner.open_id`：唯一 Owner 的 open ID。
- `owner.name`：必填。智能助手代回复和私聊通知中使用的具体姓名。缺失时配置校验
  直接失败，代回复不会用“用户”或“负责人”代替。
- `owner.preferred_language`：`auto`、`zh-CN` 或 `en-US`。配置为具体语言时优先
  使用；`auto` 按当前消息和有界同会话上下文判断。
- `owner.fallback_language`：自动判断不明确时使用的 `zh-CN` 或 `en-US`。
- `assistant.open_ids`、`assistant.names`：Owner 私聊和群 @机器人的识别身份。
- `assistant.owner_direct.enabled`：是否接受 Owner 直接发给机器人的请求。
- `assistant.reply_scope`：Owner 群内 @机器人范围。`all_groups` 是默认值，允许
  Owner 在任意机器人可见群里原生 `@机器人`；`configured_groups` 只允许 Owner
  在 daemon `--chat-query` 发现的群里调用，并要求启动时查询至少匹配一个机器人
  可见群。非 Owner 私聊或 @机器人始终静默。
- `workspace.root`：唯一 Workspace，必须是绝对路径。
- `workspace.excludes`：不可读写的秘密或构建目录模式。
- `policy.mode`：`auto`、`approval` 或 `paused`。
- `policy.reply_scope`：群内代回复范围。`all_groups` 是默认值，允许任意可见群里的
  直接 `@Owner` 进入其余回复门禁；`configured_groups` 只允许 daemon
  `--chat-query` 发现的群，并要求启动参数提供非空群关键词。
- `policy.private_reply_scope`：真人私聊代回复范围，默认 `all_private`；设为
  `disabled` 时关闭。
- `policy.owner_wait`、`owner_reply_confidence_min`、`owner_reply_retry`：
  本人优先回复窗口、语义判断最低置信度和不确定结果的重试间隔。
- `policy.reply_confidence_min`：低风险代回复自动发送的最低可信度，默认 `0.70`。
  中高风险、承诺、删除修改和不确定外部动作不因高可信度绕过审批。
- `policy.investigation_progress`：`enabled` 或 `disabled`，默认 `enabled`。开启后，
  高置信度的调查或代码问题会先持久化任务、通知具体姓名的 Owner，再用智能助手身份
  在原会话发送一次进度；该任务随后必须以结果、本人已处理或明确阻塞收口。
- `policy.allow_chats`、`block_chats`、`block_users`：确定性的会话和用户边界。
- `scheduler.*`：不同工作通道的 lease 和 worker 数量。
- `agent.*`、`tool_policy.*`、`goal.*`：模型轮次、工具输出、无进展和长任务上限。
- `agent.vision_model`：可选的图片理解模型名。未配置时，相关图片会明确标记为不可读，
  不会当成空证据或猜测图片内容。
- `agent.max_context_images`、`max_context_image_bytes`、
  `max_context_image_total_bytes`：上下文图片硬上限，默认分别为 `2`、`1048576`
  和 `2097152`。图片串行读取，编码后的图片负载计入模型请求字节，只在首个模型回合
  发送；后续回合用明确的临时图片移除标记代替。原始字节不写入 SQLite。

默认 `agent.max_context_bytes` 为 `65536`，
`agent.context_compaction_ratio` 为 `0.80`。每次模型调用都会收到轮次和上下文的
当前值、总上限和剩余值；达到软阈值后，旧工具结果会压缩成保留来源、动作回执和
明确未知项的结构化检查点。初始业务上下文进一步限制在约 48 KiB。
`fast_path.simple_max_turns` 默认为 `3`，给需要证据的简单请求保留“检索、精读、
提交结论”三个模型轮次。
`tool_policy.coding_max_tool_calls` 默认为 `16`，只有成功执行的调查工具才消耗这项
额度；参数或策略校验失败仍受模型轮次和无进展上限约束，但不会挤掉后续生产代码读取。
一旦工具额度、无进展或证据充分条件要求提交结论，后续只允许
`submit_decision`，最多给模型 3 次强制收尾机会；仍调用旧工具会立即结束该次运行，
并把该工作连同原因直接放入死信，不会继续消耗剩余通用轮次或自动重复同一调查。
网络、限流等明确可重试错误仍按原有有界退避处理。

配置不保存官方 Lark 凭据，也没有模型密钥字段。Lark app secret 放在 macOS
Keychain；用户 access token 和 refresh token 可选，只用于用户身份轮询和代回复。
模型密钥放在当前用户环境或安装器创建的权限为 `0600` 的私有 env 文件中。

## GitHub 只读证据桥

启用示例：

```yaml
github:
  enabled: true
  api_base_url: https://api.github.com
  token_keychain_service: lark-agent
  token_keychain_key: github_token
  allowed_repositories:
    - owner/repository
  max_files: 50
  max_patch_bytes: 65536
  max_annotations: 50
  max_reviews: 50
```

本地令牌用 `lark-agent github auth login` 从标准输入写入 Keychain，输入格式为
`{"token":"..."}`。GitHub Action 运行时使用当前仓库的只读 `GITHUB_TOKEN`；
Lark app secret 只来自受保护的 GitHub Environment。两者都不会写入 YAML、消息、
日志或命令参数。

消息中的仓库名、PR 号或 run ID 不是权限来源。只有当前 Lark 应用自己发送、同群
引用关系可验证、标记 HMAC 签名能由相同 Lark app secret 验证且仓库在 allowlist
中的通知，才能建立后续 GitHub 读取范围。工具参数只能选择 `summary`、`checks`、
`files`、`reviews`，不能改变仓库、PR 或 workflow run。

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

owner:
  name: 姓名
  preferred_language: zh-CN
  fallback_language: zh-CN

policy:
  reply_scope: all_groups
  private_reply_scope: all_private
  investigation_progress: enabled
  owner_wait: 3m
  owner_reply_confidence_min: 0.85
  owner_reply_retry: 30s
  reply_confidence_min: 0.70
```

这些字段相互独立：

- `assistant.reply_scope` 控制 Owner 在群里 `@机器人` 后，机器人是否接收并用
  机器人身份回答。
- `policy.reply_scope` 控制任意真人在群里 `@Owner` 后，Agent 是否可以用 Owner
  身份代回复。
- `policy.private_reply_scope` 控制用户身份可见的真人私聊是否进入代回复；
  `all_private` 启用语义候选，`disabled` 关闭。启用不代表每条消息都要回复：
  对方回答 Owner 主动问题、确认、反应或没有新增请求的对话续接会静默结束。
- `policy.owner_wait` 是本人优先回复窗口。等待由 SQLite 队列承担，不占用工作线程；
  到期后才读取目标前后有界的同一会话并逐条做语义判断。
- `policy.owner_reply_confidence_min` 是“本人已处理/无需回复/尚未回答”的最低可信度；
  `policy.owner_reply_retry` 是判断不清、上下文不完整或模型异常后的静默重试间隔。
- `policy.reply_confidence_min` 是主模型完成只读核对后自动代回复的最低可信度。
  达到 `0.70` 的低风险答复会先私聊通知 Owner，再直接发送，不会等待 Owner 在线。

这些不含凭据的当前值会作为权威运行策略随每个普通模型请求发送。助手回答“当前
怎么配置、是否自动发送、两个阈值分别管什么”时必须直接使用这份策略，不能从
Workspace 中其他项目的规则文件推测。`owner_reply_confidence_min` 判断聊天证据
是否足以说明 Owner 已经回复；`reply_confidence_min` 才是低风险代回复自动发送的
门槛。

它们只放开“群范围”这一道门。黑名单、模型相关性与风险判断、置信度和审批模式、
撤回检查与幂等发送仍然生效。Owner 等待和语义复核只适用于代回复，不适用于直接
发给机器人的问题。发送前会再次读取最新消息；晚到的本人回复会取消发送。

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
群里的正常 `@机器人` 或 `@Owner`。切换范围不会重放历史终态；安全中断工作仍按
启动收敛规则自动续跑。

## Workspace

Workspace 是 Agent 唯一可以操作的本地业务目录。路径解析会拒绝：

- 相对路径和 `..` 逃逸；
- 指向 Workspace 外部的绝对路径；
- 逃出 Workspace 的符号链接；
- `.env*`、私钥和配置中排除的路径；
- 未建立 macOS 沙箱时的 shell 执行。

这些限制由 Go 代码和操作系统沙箱执行，不依赖模型遵守提示词。

初始模型上下文会列出 Workspace 内最多五层、600 个条目的有界目录，并优先给出
Go、Rust、Zig、Node、Python、Java 等项目清单。`inspect_git_history` 只读取
Workspace 内仓库最近最多 20 条本地提交和 8 KiB 结果；仓库、`.git`、common dir
或 alternates 逃出 Workspace 时会在执行 Git 前拒绝。继承的 `GIT_DIR`、
`GIT_WORK_TREE` 等 `GIT_*` 重定向变量会在子进程启动前清除。

## 持久记忆

Owner 可以通过智能助手私聊或本地命令保存、评价和删除记忆：

```text
/memory list
/memory add fact|preference|project|response_feedback 内容
/memory feedback 记忆号 confirm|reject|helpful|unhelpful [说明]
/memory delete 记忆号 confirm
```

本地等价命令为 `lark-agent memory list|add|feedback|delete`。明确添加的内容直接标为
已确认；候选内容必须经 Owner 确认后才进入模型上下文。检索受作用域、关键词、可信度、
数量和字节上限约束。原始聊天记录、凭据、模型思维过程和未经验证的推测不会作为记忆。

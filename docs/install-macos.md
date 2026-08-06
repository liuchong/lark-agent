# macOS 安装

## 安装边界

安装只写当前用户目录，不需要管理员权限：

- 配置：`~/.config/lark-agent/config.yaml`
- 状态和已安装二进制：`~/Library/Application Support/lark-agent/`
- 日志：`~/Library/Logs/lark-agent/`
- 服务：`~/Library/LaunchAgents/com.liuchong.lark-agent.plist`
- 状态栏：`~/Applications/Lark Agent.app`

Agent 通过官方公开 Go SDK 访问 Lark。配置只保存 app id 和 Keychain 引用；app secret
必须在 macOS Keychain 中。用户 token 可选，只用于用户身份轮询、读取消息确认表情和
代回复。凭据不写入 plist、脚本参数或仓库文件。

可选的 GitHub 证据桥使用独立的只读 GitHub token Keychain 引用。启用
`github.enabled` 时，还必须配置精确的 `github.allowed_repositories`，并在安装前
执行 `lark-agent github auth status`。缺少该令牌不会扩大或改变 Lark 权限。

## 新安装

```bash
go build -o ./lark-agent ./cmd/lark-agent
./lark-agent init \
  --workspace /absolute/path/to/workspace \
  --app-id cli_xxx \
  --owner-open-id ou_xxx \
  --owner-name "姓名" \
  --preferred-language zh-CN
./lark-agent auth login < /path/to/private-lark-credentials.json
./lark-agent github auth login < /path/to/private-github-token.json
./lark-agent doctor --lark-only
./scripts/macos/install-lark-agent.sh
```

安装脚本执行以下门禁：

1. 获取当前用户安装锁，从当前源码构建候选 Agent，但不覆盖正在运行的已安装二进制；并发安装会在停止服务前失败。
2. 在停止服务前编译候选状态栏；如当前独立服务已加载，再通过现有安装执行正常停止，并在有界等待内确认异步 `bootout` 最终使 label 消失。
3. 停止后备份配置、状态、二进制、wrapper、私有环境、状态栏和 plist，并暂时移除已卸载的 plist；随后执行完整 `lark-agent doctor`。
4. doctor 通过 SDK/Keychain 验证 app id、凭据引用、Workspace 和状态库；需要真实远端权限预检时设置 `LARK_AGENT_REMOTE_DOCTOR=1`。
5. 所有候选检查通过后才原子替换二进制。
6. 原子迁移模型配置：旧 `OPENAI_API_KEY` 写入 `primary` 档案对应的 Keychain，
   `OPENAI_BASE_URL` 和 `OPENAI_MODEL` 写入 `model.profiles.primary`；迁移前会备份
   配置、私有 env 和原 Keychain 值。只有配置写回、Keychain 回读和候选
   `model doctor primary` 都通过后，才移除旧 env 中这三项；env 里的其他私有设置保留。
   任一步失败都会恢复旧配置、env 和 Keychain。
7. 原子安装候选状态栏并写入 LaunchAgent。
8. 仅在新 daemon 进入 ready 后保留已加载状态；任何失败都会恢复整套旧安装，并重新加载原先处于 loaded 状态的服务。

本安装器只处理新的独立配置和状态，不读取旧目录，不迁移历史数据。

## 安装参数

安装脚本支持以下环境变量：

- `CONFIG_PATH`、`STATE_PATH`：覆盖独立目标路径。
- `CHAT_QUERY`：发现并标记配置群和验收群的关键词，默认“Test Group”。默认
  `assistant.reply_scope: all_groups`、`policy.reply_scope: all_groups` 和
  `policy.private_reply_scope: all_private` 时不会据此
  限制其他群；任一字段设为 `configured_groups` 时，该字段对应的群范围由它限制。
- `POLL_INTERVAL`：轮询间隔，默认 `10s`。
- `INSTALL_LOAD=0`：只安装，不加载，供隔离验证使用。
- `OPEN_STATUS_APP=0`：安装后不打开状态栏，供隔离验证使用。
- `OPENAI_API_KEY`、`OPENAI_BASE_URL`、`OPENAI_MODEL`：仅作为旧安装或临时 shell 的
  迁移输入。显式传入会更新 `primary` 档案或对应 Keychain；全部不传时保留既有
  profile/Keychain。不要把这些变量写入仓库脚本或 plist。

不要把 token、私钥或模型密钥写进仓库、plist 或命令参数。

当前全群安装应在配置中明确保存：

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
  owner_wait: 3m
  owner_reply_confidence_min: 0.85
  owner_reply_retry: 30s
  owner_reply_max_retries: 3
  reply_confidence_min: 0.70
```

前者控制 Owner 可在哪些群里 `@机器人`，后者控制其他人可在哪些群里 `@Owner`
后触发智能代回复。用户身份可见的真人私聊由 `private_reply_scope` 单独控制。
`all_private` 表示所有对方发来的真人私聊都进入语义判断，不表示每条消息都要回复；
对方只是在回答 Owner 主动发起的话题、确认或继续闲聊且没有新增请求时会静默结束。
私聊被判定为未处理请求时，目标消息本身必须包含新的问题、请求、邀请或协调义务；
只从 Owner 先前问题里推断出来的任务不会进入调查。Owner 在目标消息上添加
`Get`、`OK`、`DONE`、`THUMBSUP`、`CheckMark`、`Yes` 或 `LGTM` 这类确认表情，也会
被视为已经回复。读取确认表情需要发布态应用具备 `im:message.reactions:read` scope，
并且本机 user token 已按当前应用重新授权。
Owner 发给其他真人的普通私聊消息不会进入代回复队列。
非 Owner 私聊机器人或直接 `@机器人` 始终静默；这些范围配置不会互相代替。

要启用文档、Wiki 和 Base 的 `@Owner` 监控，发布态应用还必须在飞书开放平台订阅
WebSocket 事件 `drive.notice.comment_add_v1` 和
`drive.file.bitable_record_changed_v1`、`drive.file.bitable_field_changed_v1`，并授予当前应用/用户读取云文档评论、订阅文件、
读取 Base 字段与记录所需权限。若允许 Agent 更新状态或回复评论，还需分别授予 Base
记录写入和云文档评论回复权限。不同飞书版本在控制台展示的权限名称可能不同，以
`lark-agent subscription sync` 返回的缺失权限为准，不能在同步失败时把本地
`pending` 记录当成已监控。

安装后，Owner 可在智能助手私聊中发送 `/help`、`/status`、`/tasks` 和 `/memory`
查询运行状态、待处理任务与持久记忆。任务详情会给出精确的恢复、取消、确认、核对
或审批命令。私聊中紧跟唯一动作通知的“确认”“不用继续了”等自然语言可以映射到
同一套命令；普通业务问题不会因为包含命令词而被误执行。群聊控制
命令不会展示队列详情，非 Owner 控制命令不响应。命令语言优先使用
`owner.preferred_language`；未配置时按现有 Owner 语言解析结果选择中文或英文，
同一条消息不混用两种说明语言。

建议同时使用当前默认调查预算：

```yaml
agent:
  max_context_bytes: 65536
  context_compaction_ratio: 0.80
  vision_model: <已验证支持图片的模型名>
  max_context_images: 2
  max_context_image_bytes: 1048576
  max_context_image_total_bytes: 2097152
fast_path:
  simple_max_turns: 3
  coding_max_turns: 100
tool_policy:
  coding_max_tool_calls: 16
policy:
  investigation_progress: enabled
```

这不会放宽安全边界。`coding_max_turns` 只是代码调查的模型轮次上限，不会同步放宽
工具调用次数；模型仍被提示尽早收敛。已有安装如果保留旧 `config.yaml`，安装后需确认
`fast_path.coding_max_turns: 100` 已写入本机配置。非 Owner 请求仍是同群和 Workspace
只读，环境刺探与工作目录外访问仍被强制拒绝。

## GitHub Environment

仓库工作流使用名为 `lark-production` 的受保护 GitHub Environment：

- secret `LARK_APP_SECRET`：与本地 daemon 相同的 Lark 应用 secret；
- variable `LARK_APP_ID`：同一个 Lark 应用 ID；
- variable `LARK_CHAT_ID`：通知目标的精确 chat ID。
- variable `LARK_BASE_URL`：显式 OpenAPI 根地址；国际版 Lark 使用
  `https://open.larksuite.com`，飞书中国站使用 `https://open.feishu.cn`。

这不会创建第二个实时监听实例。Action 只执行
`lark-agent github notify --chat-id ...` 并通过 Lark HTTP API 发送一次消息；本地
LaunchAgent 继续独占 WebSocket 事件消费。Environment 应只允许默认分支部署，避免
不可信分支取得 Lark secret。

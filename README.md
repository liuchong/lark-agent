<p align="center">
  <img src="assets/brand/lark-agent-mark.svg" width="156" alt="Lark Agent logo">
</p>

<h1 align="center">lark-agent</h1>

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
- 他人在任意用户可见群里直接 @Owner 时，Agent 默认可按策略通过 Owner 的用户
  身份发送，但正文会明确标注“智能助手”，说明已完成的前期工作，并写明已经通知
  Owner 的具体姓名；随后机器人再私聊 Owner 说明仍需处理的事项。可以通过
  `policy.reply_scope` 改回仅允许配置群。
- 代回复先进入 `policy.owner_wait`（默认 3 分钟）的持久等待。到期后按语义判断
  每一条具体消息是否确实在向 Owner 提出尚未处理的问题或请求。对方只是在回答
  Owner 主动发起的话题、确认、回应或继续闲聊时不会强行代答；判断不清、上下文不全
  或模型输出异常时不会瞎答，而是按 `policy.owner_reply_retry` 延后重试。
- 代回复会读取目标消息前后有界的同会话讨论和并发待回复问题，判断对话由谁发起、
  Owner 的非引用回复实际对应哪一条；不会把其他群或其他私聊内容混入上下文。
- 有关图片会按数量和字节上限串行读取，并只作为单次模型输入；图片不可用、超限或
  模型不支持时会明确标记为不可读，不会把空内容算作调查证据。
- 高置信度的调查或代码问题会先建立可恢复的调查记录、通知具体姓名的 Owner，再由
  智能助手在原线程发送一次进度。最终必须以有证据的结果、Owner 已处理或明确阻塞
  收口；重启不会重复已完成的进度消息。
- Owner 发给其他真人的私聊消息不会进入代回复队列，也不会因为出现业务关键词而让
  Agent 回答 Owner 自己的问题。
- 编程问题可在配置的 Workspace 内使用有边界的代码搜索、文件读取和 shell
  工具。初始上下文包含最多五层的有界目录和项目清单，也可以读取 Workspace 内
  仓库最近的本地 Git 提交；不会执行 fetch、checkout 或其他网络和写操作。路径逃逸、
  Git 元数据逃逸、符号链接逃逸、秘密文件和无边界搜索会被代码拒绝。
- 非 Owner 触发的群内请求只能读取当前群上下文和 Workspace 业务证据，不能执行
  shell、跨群搜索、修改、删除、提交或部署。环境刺探和工作目录外路径请求在模型
  调研前直接拒绝。
- 代回复必须先完成可审计的相关读取，并简要说明已检查内容、初步发现或明确未知；
  “已提醒 Owner”、复述原问题和未经批准的后续承诺不会发送。
- GitHub Actions 可以用同一个 Lark 应用身份把可信的工作流结果发进指定群。Action
  只通过 HTTP 发送一次消息，不启动第二个 WebSocket 监听；常驻 Agent 验证被引用的
  机器人消息及其 HMAC 签名后，才允许模型按该引用读取有界、只读的 GitHub 事实。
- 所有消息、语义判断、模型步骤和外部动作都写入 SQLite 账本。重启后，无状态只读
  工作可以安全重算；对话调查和未发送回复候选保持中断，必须由 Owner 核对并显式
  恢复后重新识别；待批准草稿会保留并私聊 Owner 精确操作方式；
  结果不确定的外部动作绝不重放，而是收口为死信并发送核对指令。
- 每次模型调用都会收到当前/总计/剩余轮次、有效调查调用次数和上下文容量。必需的
  调查计划不占有效调查调用额度；接近任一上限时，旧证据会压缩成保留来源与动作回执
  的检查点，模型必须尽快提交结论。
- Owner 可保存事实、偏好、项目知识和回复评价。记忆持久化在 SQLite 中，重启后仍
  可检索；只有已确认且未删除的有界内容会进入模型上下文，原始聊天、凭据和模型推测
  不会被自动当作记忆。
- 对外语言优先使用 `owner.preferred_language`；`auto` 会按当前 Lark 会话判断，
  判断不清时使用 `owner.fallback_language`。一条消息不会混用中英文解释性正文。

## 要求

- macOS
- Go 1.25 或更新版本（从源码构建时）
- Lark 自建应用的 `app_id`，以及写入 macOS Keychain 的 app secret；用户 token 可选，
  只用于用户身份轮询和代回复
- 一个已配置的模型档案；模型密钥写入 macOS Keychain，不写入配置文件或日志

`lark-agent` 不维护第二个 `lark-cli` Fork，也没有 Linux/Windows 安装器或传输插件
接口。

## 快速开始

先初始化独立配置：

```bash
go build -o ./lark-agent ./cmd/lark-agent
./lark-agent init \
  --workspace /absolute/path/to/workspace \
  --app-id cli_xxx \
  --owner-open-id ou_xxx \
  --owner-name "姓名" \
  --preferred-language zh-CN
./lark-agent auth login < /path/to/private-lark-credentials.json
./lark-agent doctor --lark-only
```

需要模型时，先配置模型档案并把密钥写入 Keychain。默认档案是 Kimi `k3-256k`：

```bash
lark-agent model profile list
lark-agent model profile set primary \
  --provider kimi \
  --protocol openai_chat \
  --base-url https://api.kimi.com/coding/v1 \
  --model k3-256k
lark-agent model auth login primary < /path/to/private-model-key.json
lark-agent model doctor primary
```

安装器仍识别显式传入的 `OPENAI_API_KEY`、`OPENAI_BASE_URL` 和 `OPENAI_MODEL`，只作为
旧安装迁移输入；成功迁移到 profile 和 Keychain 后，不再把它们作为长期唯一配置来源。

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
lark-agent queue tasks --view action
lark-agent queue inspect --message-id om_xxx
lark-agent queue resume --message-id om_xxx
lark-agent queue acknowledge --work-id 123 --reason "reviewed and closed"
lark-agent queue reconcile --work-id 456 --result unknown --reason "external result could not be verified"
lark-agent queue cancel --work-id 123 --reason "superseded"
lark-agent queue cancel --all-interrupted --keep-work-id 456 --reason "audited stale work"
lark-agent memory list
lark-agent memory add preference "优先使用中文回复"
lark-agent memory feedback MEMORY_ID helpful "这条经验有效"
lark-agent memory delete MEMORY_ID --confirm
lark-agent github auth status
```

Owner 还可以在智能助手私聊中发送 `/help` 查看控制命令，用 `/status` 查看当前会话，
用 `/tasks` 查看需要人工处理的任务，用 `/task 工作号` 查看调查主题、状态、上下文
证据和最近错误，并用 `/memory` 管理持久记忆。私聊中的自然语言只有在当前上下文
能唯一对应某个控制命令时才会执行，例如紧跟唯一审批通知发送“确认”；普通业务问题
即使包含“确认”“状态”等词也仍按问题回答。群聊中的
控制命令只会提示转到私聊，不会泄露队列内容；非 Owner 私聊或群聊控制命令保持静默。

无状态的安全中断工作会在新会话 ready 后自动续跑；对话调查和旧回复候选保持中断。
`queue resume` 用于经过人工核对的对话调查、手动暂停、离线补录或终态工作，并会
废弃旧调查上下文和旧回复候选后重新识别；已经完成、忽略、取消或进入死信的终态工作
还必须明确加 `--force-terminal`。结果不确定的外部动作不会自动重发。审核后确认无用的
历史工作使用 `queue acknowledge` 或 `queue cancel` 做可审计收口；外部结果不确定
时使用 `queue reconcile` 记录人工核对结论。命令保留全部历史记录。

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

## 文档与 Base 监控

`lark-agent subscription add URL` 登记 Wiki、文档或 Base，随后运行
`lark-agent subscription sync` 建立远端订阅并完成首次对账。Agent 会接收云文档
应用通知、评论 `@Owner` 和 Base 记录变更，但不会回复通知应用或把通知正文当成指令。
状态写入前必须关联唯一记录、读取项目 `AGENTS.md` 规则、核对实现/回归测试/Git 证据、
检查实时字段选项并比较当前值；`approval` 模式先生成可审计动作号，`paused` 模式不写。

## 文档

- [macOS 安装](docs/install-macos.md)
- [配置说明](docs/configuration.md)
- [运行、恢复与故障处理](docs/operations.md)
- [开发与验证](docs/development.md)
- [长期行为规格](spec/behavior.md)
- [架构边界](spec/architecture.md)
- [Lark SDK 边界](spec/lark-sdk-boundary.md)

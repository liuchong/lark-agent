# 配置说明

配置默认位于 `~/.config/lark-agent/config.yaml`。字段含义写在这里；安装步骤见
[macOS 安装](install-macos.md)，GitHub Actions 见 [智能命令与 GitHub](smart-command.md)。

## 目录

- [关键字段](#关键字段)
- [GitHub 只读证据桥](#github-只读证据桥)
- [模式](#模式)
- [强制保护](#强制保护)
- [群回复范围](#群回复范围)
- [私人任务规则](#私人任务规则)
- [Workspace](#workspace)
- [持久记忆](#持久记忆)

建议先运行：

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
  SQLite 为准。也可以用 `lark-agent subscription add URL` 登记本机订阅，再用
  `lark-agent subscription sync` 解析 Wiki、建立远端文件订阅并完成首次 Base
  基线对账。配置或本机记录中的 `active` 不能替代远端同步结果；同步失败会落为
  `degraded` 或 `forbidden`。
- `github.enabled`：是否启用可信 GitHub 证据桥。默认关闭。
- `github.allowed_repositories`：允许通知和后续读取的精确 `owner/repository`
  列表；不支持通配符，也不能由模型或消息正文扩大。
- `github.api_base_url`：GitHub 或 GitHub Enterprise 的 API 根地址。
- `github.token_keychain_service`、`github.token_keychain_key`：本地只读 GitHub
  令牌的 Keychain 引用。
- `github.max_files`、`max_patch_bytes`、`max_annotations`、`max_reviews`：
  单次模型读取的硬上限。
- `output.language`：全产品对外语言，`auto`、`zh-CN` 或 `en-US`，默认 `auto`。
  所有对外消息都遵守它，包括没有人类会话可参考的智能命令。
- `output.fallback_language`：`zh-CN` 或 `en-US`，默认 `zh-CN`。`auto` 判断不
  明确时使用；智能命令在 `output.language` 为 `auto` 时直接使用它，不会拿提示词
  文本当语言样本。
- `owner.open_id`：唯一 Owner 的 open ID。
- `owner.name`：必填。智能助手代回复和私聊通知中使用的具体姓名。缺失时配置校验
  直接失败，代回复不会用“用户”或“负责人”代替。
- `owner.preferred_language`：`auto`、`zh-CN` 或 `en-US`。仅覆盖面向 Owner 的会话
  工作。配置为具体语言时优先于 `output.language`；`auto` 时按当前消息和有界同会话
  上下文判断。
- `owner.fallback_language`：自动判断不明确时使用的 `zh-CN` 或 `en-US`。未配置时
  回落到 `output.fallback_language`。
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
- `policy.owner_wait`、`owner_reply_confidence_min`、`owner_reply_retry`、
  `owner_reply_max_retries`：本人优先回复窗口、语义判断最低置信度、不确定结果的
  重试间隔和纯语义复查上限。复查上限默认 `3`，不会重新调用主回答模型。
- `policy.reply_confidence_min`：低风险代回复自动发送的最低可信度，默认 `0.70`。
  中高风险、承诺、删除修改和不确定外部动作不因高可信度绕过审批。
- `policy.investigation_progress`：`enabled` 或 `disabled`，默认 `enabled`。开启后，
  高置信度的调查或代码问题会先持久化任务、通知具体姓名的 Owner，再用智能助手身份
  在原会话发送一次进度；该任务随后必须以结果、本人已处理或明确阻塞收口。
- `policy.allow_chats`、`block_chats`、`block_users`：确定性的会话和用户边界。
- `scheduler.*`：不同工作通道的 lease 和 worker 数量。
- `model.profiles`：模型档案。每个档案明确写出供应商、协议、API URL、模型名、
  Keychain 凭据引用、流式和思考配置。默认 `primary` 使用 Kimi `k3-256k` 与
  `openai_chat` 协议。配置只保存 Keychain 引用，不保存 API key。
- `model.profiles.<名字>.timeout`：单次模型尝试的超时，默认 `120s`。这是一次尝试的
  上限，不是一次调用的上限。默认值按“推理模型读长提示词后作答”来定，例如从整个 diff
  生成 changelog；把它调到很短会让重的提示词稳定超时。
- `model.profiles.<名字>.max_attempts`：一次模型调用最多用几次尝试，默认 `3`，允许
  `1` 到 `10`。只有被判为可重试的失败才会再发一次：连接中断、单次尝试超时、429、5xx、
  529，以及解出来没有内容的应答。400、401、403、404 和额度耗尽是确定性失败，只花一次
  往返就返回。重试之间按 2 秒起指数退避，供应商给了 `Retry-After` 就至少等那么久；调用方
  取消或到期时立刻停止，不会再发下一次。
  一次调用的最坏耗时是 `timeout × max_attempts` 加退避，智能命令的整体上限仍是 8 分钟
  循环预算，所以这两个值要留在这个预算里。
- `model.profiles.<名字>.reasoning`、`capabilities`：档案声明的推理模式、推理强度和
  能力上限。这些声明会真正出现在请求里，常驻助手和 GitHub Actions 里的一次性智能命令
  用同一套档案语义，不存在“某个字段只在其中一边生效”。当前会落到请求线上的能力包括：
  `tool_use=false` 时不发送工具，`parallel_tool_call=false` 时声明禁止并行工具调用，
  `image_input=false` 时不发送图片内容，`max_output_tokens` 控制 `max_tokens`。
- `model.roles`：角色绑定。`agent`、`semantic`、`finalizer`、`compactor`、`vision`
  默认都绑定 `primary`，也可以显式绑定到不同档案；运行时不会在未授权时自动跨供应商
  故障切换。
  当前 daemon 主循环只启用 `openai_chat` 档案；`openai_responses` 和
  `anthropic_messages` 已有 codec 与 fixture，但在旧 chat-model 桥完全替换前，绑定到
  主运行角色会由 doctor 明确失败，避免把请求误发成 Chat Completions。
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
`fast_path.coding_max_turns` 默认为 `100`，给代码调查留出更深的模型轮次上限；这不是
要求模型用满 100 轮，每轮提示都会继续显示当前轮、总上限、剩余轮、工具额度和上下文
额度，并要求证据足够时尽早提交完整、部分或澄清结论。
`tool_policy.coding_max_tool_calls` 默认为 `16`，只有成功执行的调查工具才消耗这项
额度；参数或策略校验失败仍受模型轮次和无进展上限约束，但不会挤掉后续生产代码读取。
`coding.enabled=false` 会把代码类请求留在简单问答通道，不进入代码调查 lane。
`coding.max_evidence_files` 限制一次代码调查中 `read_workspace` 的成功执行次数，默认
`12`；`coding.max_lark_context_calls` 限制一次代码调查中同会话上下文读取次数，默认
`2`。代码事实仍始终要求当前运行里的来源引用；该规则不是可配置开关。
一旦工具额度、无进展或证据充分条件要求提交结论，后续只允许
`submit_decision`，最多给模型 3 次强制收尾机会；如果模型仍调用旧工具，运行时会再发起
一次无工具的终端收尾请求，只能根据已保留工具回执生成同形结构化结论。该结论仍走来源、
质量、代码证据和发送门禁；收尾也失败时才把该工作连同原因放入死信，不会继续消耗剩余
通用轮次或自动重复同一调查。
相同工具、规范化参数和结果连续出现时，运行时会先要求改变策略，再限制广搜，最后
只允许提交完整结论、部分结论或澄清请求。网络、限流等明确可重试错误仍使用
`agent.max_retries` 和原有有界退避；确定性输出错误不会借此重复整个调查。

配置不保存官方 Lark 凭据，也不保存模型 API key。Lark app secret、用户 token 和模型
API key 都放在 macOS Keychain；用户 token 可选，只用于用户身份轮询、读取确认表情和
代回复。旧安装中的私有 `OPENAI_*` env 只作为 `primary` 档案迁移和显式覆盖输入，成功
迁移后不应长期作为唯一模型配置来源。

模型配置常用命令：

```bash
lark-agent model profile list
lark-agent model profile set primary \
  --provider kimi \
  --protocol openai_chat \
  --base-url https://api.kimi.com/coding/v1 \
  --model k3-256k
lark-agent model role list
lark-agent model role set finalizer primary
lark-agent model auth login primary < /path/to/private-model-key.json
lark-agent model auth status primary
lark-agent model doctor primary
```

Kimi 开启思考能力并使用工具时，运行时只提供 `tools`，默认不发送
`tool_choice=required`。如果模型服务返回 HTTP 400、认证/权限、profile/协议不匹配或
配额耗尽，这类确定性错误会直接进入可诊断终态，不再消耗整单重试次数。

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
`{"token":"..."}`。GitHub Action 运行时使用当前仓库的 `GITHUB_TOKEN`；
Lark app secret 只来自受保护的 GitHub Environment。智能命令还从进程环境读取
`OPENAI_API_KEY`（可选 `OPENAI_BASE_URL`、`OPENAI_MODEL`）。这些值都不会写入
YAML、消息、日志或命令参数，也不会通过 `/actions/secrets` HTTP 去拉。

GitHub Actions 的 `mode`、Environment 变量和评论唤醒词见
[智能命令与 GitHub](smart-command.md)。常驻助手引用已发送通知后再读 GitHub，见
[运行、恢复与故障处理](operations.md)。

普通文本中的仓库名、PR 号或 run ID 不是权限来源。可信范围有两个代码验证入口：
当前 Lark 应用发送且同群引用关系和 HMAC 签名均有效的通知；或 Owner/智能助手明确
收到的完整 PR URL。后者由 Go 解析 URL、核对精确仓库 allowlist 并保存发送者和授权
路由，模型不能提供或改写仓库、PR、API 地址或凭据。工具参数只能选择 `summary`、
`checks`、`files`、`reviews`。

PR 审查直接读取远程 GitHub API，并在一次取证前后核对相同的 head SHA；同一运行中
后续读取也必须匹配首次绑定的 head。`checks` 会读取该 head 对应的检查运行与有界注释。
它不依赖 `gh` 登录状态，不向当前仓库执行 fetch/pull/checkout，不运行 PR 代码。
GitHub 未启用或只读凭据缺失时会直接说明配置阻塞，不会把“本地仓库里没找到”当作
远程 PR 不存在。

## 本地审计保留

```yaml
retention:
  days: 30
```

`retention.days` 必须大于 0。daemon 启动时会删除超过保留期且已经终止的运行轨迹、
外部引用和临时审计行；仍在处理、等待用户、等待审批或被中断的工作不会被删除。

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

output:
  language: zh-CN
  fallback_language: zh-CN

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
  owner_reply_max_retries: 3
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
- `policy.owner_reply_max_retries` 是上述只读语义复查的上限，默认 `3`。主模型已经
  产出的安全草稿会持久保全，复查期间不会重新调查；触顶后只私聊 Owner，并明确草稿
  尚未发送给原提问者。该次数由 SQLite 独立记录，不会与 `agent.max_retries` 的网络、
  限流和模型供应商瞬时失败次数互相占用。
- `policy.reply_confidence_min` 是主模型完成只读核对后自动代回复的最低可信度。
  达到 `0.70` 的低风险答复会先私聊通知 Owner，再直接发送，不会等待 Owner 在线。

## 私人任务规则

规则正文是私人配置，不是编译进代码的业务策略。默认文件是配置目录旁的
`TASK_RULES.md`。已有安装保持 `task_rules.enabled: false`，直到本机执行
`lark-agent rules init`，或新安装的 `lark-agent init` 写入通用模板并启用。

```yaml
task_rules:
  enabled: false
  path: TASK_RULES.md
  max_bytes: 32768
```

- `path` 必须是配置目录内的相对路径，不能使用 `..` 或绝对路径。
- 分类器、主 Agent、收尾修复和发送前检查每次按摘要重新加载同一份快照。
- 群里 `@Owner` 只是候选；没有目标原文或当前快照里的义务证据时，不会创建代回复任务。
- 规则可以收窄或补充工作，但不能扩大 Workspace、跳过审批、授予写入或改发送身份。
- 推荐把本机安装实例里不含秘密的配置备份到仓库 `/.local/owner-config/`。该目录
  已被 gitignore；换上下文时优先在这里找本地实例材料。运行中的配置仍以
  `~/.config/lark-agent/` 为准，token 和密钥不要放进这个备份。
- `/rules` 和 `lark-agent rules check` 只显示启用状态、文件状态、字节数、摘要和文件名，不输出正文或绝对路径。

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
群里的正常 `@机器人` 或 `@Owner`。切换范围不会重放历史终态；无状态中断工作仍按
启动收敛规则安全重算，对话调查和未发送回复候选则保持中断，等待 Owner 显式恢复并
重新识别。

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

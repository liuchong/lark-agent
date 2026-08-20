# 运行、恢复与故障处理

这份文档写常驻助手：服务、队列、审批、恢复、云文档监控和代码调查。一次性 GitHub 命令和工作流见 [智能命令与 GitHub](smart-command.md)。文档分层见 [文档地图](README.md)。

## 目录

- [服务状态](#服务状态)
- [队列检查](#队列检查)
- [智能助手私聊控制](#智能助手私聊控制)
- [自动收敛与显式恢复](#自动收敛与显式恢复)
- [审核后取消](#审核后取消)
- [授权缺失后的显式补录](#授权缺失后的显式补录)
- [审批](#审批)
- [云文档、Wiki 与 Base 监控](#云文档wiki-与-base-监控)
- [诊断](#诊断)
- [GitHub 证据追问](#github-证据追问)
- [权限与回复质量](#权限与回复质量)
- [代码调查](#代码调查)
- [故障回退](#故障回退)

## 服务状态

```bash
lark-agent daemon status
lark-agent daemon start
lark-agent daemon stop
lark-agent daemon restart
```

LaunchAgent label 是 `com.liuchong.lark-agent`。日志位于
`~/Library/Logs/lark-agent/`。菜单栏左键打开结构化状态面板，打开后先显示服务是否
在跑、待审批条目、需要关注的队列计数和最近工作；任务规则、回复范围和诊断在
`doctor` 返回后补齐。右键仍是启动、停止、暂停和打开配置/日志。面板不展示令牌、
密钥、私人规则正文、审批请求/响应正文或原始命令 JSON，也不拉取全量审批历史。
10 秒刷新只更新图标上的运行/待办计数，完整诊断只在打开面板或点刷新时加载。

官方 Lark SDK 中可能携带连接凭据的调试和普通信息日志会被抑制；事件分发器固定的
无凭据 ready 提示可以保留，警告与错误会在凭据字段脱敏后保留。日志中不得出现
WebSocket `access_key`、`ticket`、token 或 app secret；诊断连接问题时使用结构化
生命周期状态、`doctor` 和不含凭据的错误原因，不要恢复 SDK 默认详细日志。

正常启动会先完成 SDK/Keychain、配置、状态库、接收入口和调度器初始化，再把在线会话
标记为 ready，并由机器人私聊 Owner 上线通知。正常停止会先暂停当前未完成工作，
发送离线通知，再结束在线会话。异常崩溃不会伪造离线通知。

实时消息由官方 Go SDK 的 WebSocket 长连接接收，用户身份轮询作为兜底。日志若持续
出现 `lark SDK websocket event consumer exited`，说明实时入口没有保持连接，应检查
app id、Keychain 凭据、事件订阅和权限；不能只用轮询回复成功代替实时验收。

## 队列检查

```bash
lark-agent queue summary
lark-agent queue list
lark-agent queue tasks --view action
lark-agent queue tasks --view running
lark-agent queue inspect --work-id 123
lark-agent queue inspect --message-id om_xxx
```

inspect 会显示接收回执、工作项、最近模型步骤、最近外部动作和中断快照，用于判断
消息是离线积压、重复、排队、处理中、中断、已回复还是终态。
模型诊断字段会展示真实的 role/profile/provider/protocol/model、请求 ID、phase、
attempt、finish reason、HTTP 状态、失败类别和恢复动作；不会展示 API key、原始思考
内容或完整请求体。看到 `invalid_request`、认证/权限、profile/协议不匹配或配额耗尽
时，先修配置或 Keychain 并运行 `lark-agent model doctor primary`，不要用
`queue retry` 反复跑同一个确定性失败。

`queue tasks` 是面向处置的有界列表：

- `action` 只列出需要 Owner 决策或收口的任务；
- `running` 列出正在执行或会自动继续等待的任务；
- `recent` 和 `all` 用于查看近期历史，不代表每条都需要操作。

机器人上线或离线通知只显示非零分类。显示“需要你处理 0 条”表示当前没有人工待办，
不会产生悬空任务。Owner 在智能助手私聊中发送 `/tasks` 能看到工作号、最新可靠事实
和可执行的下一条命令；发送 `/task 工作号` 查看单条详情。调查型任务还会显示具体
调查主题、调查状态、上下文证据是否已固定和最近错误。正在自动调查时再次发送
`/task 工作号` 刷新；中断、失败或结果不确定时，详情会给出恢复、取消、确认或核对
所需的精确命令。

## 智能助手私聊控制

只有配置的 Owner 可以使用，且只在智能助手私聊中返回任务或审批详情：

```text
/help
/status
/doctor
/tasks [action|running|recent|all] [页码]
/task 工作号
/approvals [页码]
/approval 动作号
/recent [数量]
/memory list [页码]
/memory add fact|preference|project|response_feedback 内容
/memory delete 记忆号 confirm
/memory feedback 记忆号 confirm|reject|helpful|unhelpful [说明]
/rules
/version
/ping
```

修改状态的命令会先校验当前持久化状态，重复投递同一条 Lark 消息不会重复修改：

```text
/task retry 工作号
/task resume 工作号 [confirm]
/task cancel 工作号 原因
/task acknowledge 工作号 说明
/task reconcile 工作号 completed|not-completed|unknown 核对说明
/approval approve 动作号 confirm
/approval reject 动作号 原因
```

这些命令共用一份命令目录，显式解析、`/help` 和语义判断不会各维护一套清单。
Owner 在智能助手私聊中可以用“确认”“拒绝这条”“看看最近任务”等自然语言操作，
但必须由相邻助手通知和当前持久化候选唯一确定动作；不唯一时只询问准确的任务号、
动作号或记忆号。普通业务提问即使包含“确认”“状态”“任务”等词也不会被当成命令。

本地记忆命令与机器人私聊落到同一个 SQLite 状态库：

```bash
lark-agent memory list
lark-agent memory add project "示例事件服务位于 sample-service"
lark-agent memory feedback MEMORY_ID helpful "项目定位正确"
lark-agent memory delete MEMORY_ID --confirm
lark-agent rules check
lark-agent rules init
```

删除采用可审计墓碑，不会进入后续模型上下文；凭据样式内容会被拒绝。
`lark-agent rules check` 和私聊 `/rules` 只显示私人任务规则的启用状态、文件状态、
字节数、摘要和文件名，不输出正文或绝对路径。规则内容是配置目录旁的 Markdown，
不是编译进代码的业务策略。群里 `@Owner` 只是候选；没有义务证据时不会创建代回复任务。

Owner 在群聊中发送这些命令时只收到转到私聊的提示，群里不展示任务数量和内容。
同样，群里 `@机器人 status`、`doctor`、`queue summary`、`help` 或询问“为什么没回答”
这类状态问题，也只会提示去智能助手私聊，不会在群里展示任务数量、工作号、审批命令或
详细帮助。时间、日期、`ping` 和“在吗”不含私有任务状态，仍可在群里直接回答。
非 Owner 私聊机器人或直接 `@机器人` 仍然静默。

高置信度的调查或代码问题在 `policy.investigation_progress: enabled` 时会先写入可审计
的调查记录，再通知 Owner，并由智能助手在原消息线程发送一次进度。进度和最终结果
使用不同的幂等动作；重启后不会重复已完成的进度消息，也不会自动续跑上一会话的
调查上下文或发送旧草稿。Owner 核对并显式恢复后会重新识别原消息并开启新调查。
私聊方向判断优先区分“对方提出新请求”和“对方回答 Owner 的问题”。只有目标消息
本身包含新的问题、请求、邀请或行动义务时，普通真人私聊才会进入调查或代回复；
回答、确认、继续 Owner 发起的话题、产品/设计信息同步且没有新增义务时会记录为无需
回复。群聊仍必须有原生 `@Owner` 才能进入代回复判断；但显式 `@Owner` 后如果只是
点赞、夸图、确认或分享信息，也会在语义阶段收口为无需回复。
本地审计保留完整动作键；发送给 Lark 的消息 UUID 使用稳定短摘要，满足公开接口
最多 50 个字符的限制，并保持通知与进度互不冲突。

图片证据采用额外硬上限并串行读取。编码后的多模态负载计入首回合上下文预算，只在
首个模型回合发送；后续回合改为明确的临时图片移除标记，避免每轮重复携带大图片。
重启恢复的调查快照不保存图片字节，会把相关图片明确标记为需要重新核对。

## 自动收敛与显式恢复

新会话进入 ready 后会先处理全部中断工作：

- 不带对话调查上下文和回复草稿的无状态只读工作可自动分配给新会话并重新读取当前证据；
- 已保存对话调查上下文或未发送回复候选的工作保持中断，私聊 Owner 核对和恢复命令；
- 尚未批准的精确草稿保持等待，并私聊 Owner 工作号、动作号和批准命令；
- 中断时正在执行的 shell、回复或通知不会重放，工作会收口为死信并私聊核对命令；
- 模型不收敛或有界重试耗尽也会私聊已完成检查、停止原因和精确 inspect 命令；代码
  调查在强制收尾阶段拒绝 `submit_decision` 时，会先尝试一次无工具终端收尾，把已完成
  工具回执整理成部分结论或明确失败说明。
- 语义上下文持续缺失或含糊达到重试上限时，工作会原子转为死信、清除下次执行时间，
  并在同一事务记录待发送的 Owner 总结；立即处理、定时维护或重启恢复会私聊
  `/task 工作号` 和 `/task resume 工作号 confirm`，不会继续排队、遗漏通知或向
  过时的原对话补发。
- 每次语义判断、调查结束检查和候选发送前都会实时读取 exact target 上的 Owner
  reaction。Owner 以用户身份添加 `Get`、`OK`、`DONE`、`THUMBSUP`、`CheckMark`、
  `Yes` 或 `LGTM` 时，视为已经处理并取消后续代回复；他人、机器人、非允许列表表情
  或其他消息上的 reaction 不算。reaction 读取缺权限、失败或分页超过上限时按明确
  的 `owner_reaction_read_failed` 有界重试，不会假定“没有 reaction”继续发送。
- 主模型已形成且通过发送前校验的回复，会在最后一次“Owner 是否已处理”检查前保存为
  回复候选。同一在线会话内上下文仍含糊时只复查会话，不重跑模型。`/task 工作号`
  会明确显示“尚未回复原提问者”、未发送草稿、已核对、未知和下一步；随后确认仍未
  回复时才按原有幂等链路发送一次，发现 Owner 已处理或原消息撤回则取消候选。进程
  重启后候选只供检查，不会跨会话发送。同一会话重新领取时仍会重跑暂停、黑名单和
  回复范围规则，规则收紧后取消候选。候选的写入、持有、消费和取消都受当前工作租约
  约束，过期进程不能改写新一轮调查结果。
- 每次进入死信都有独立的提醒编号。Owner 在提醒开始发送前恢复工作会同时取消旧
  提醒；发送结果尚不确定时恢复会被拒绝。Lark 已明确返回失败的提醒可由 Owner
  主动恢复操作取消，不再发送过期摘要。恢复后的工作再次失败会生成新提醒，历史提醒
  不能把它误判成已经通知。

`queue resume` 用于人工核对后的对话调查、手动暂停、离线补录或终态工作：

```bash
lark-agent queue resume --work-id 123
lark-agent queue resume --message-id om_xxx
```

显式恢复表示重新调查：它会归档上一代调查、取消尚未发送的旧回复候选，并重新运行
当前路由和语义识别，不会把旧任务摘要、旧上下文或旧草稿直接交给新调查。上一代已经
完成的回复、Owner 通知和调查进度仍保留为审计记录，但不能让新一代直接完成；新结论
会使用新的消息幂等身份。资源写入等有副作用的工具动作仍使用独立幂等保护，不会仅因
恢复代际变化而重复执行。

恢复已完成、已忽略、已取消或死信工作时还必须明确：

```bash
lark-agent queue resume --work-id 123 --force-terminal
```

中断时正在执行的 shell、回复、Owner 通知或生命周期通知属于结果不确定的外部
动作。系统不会自动重发；Owner 必须先根据飞书和本地审计证据确认实际结果。工作
本身不会继续停在中断队列里。

核对后用下列命令记录结果，不会触发原外部动作重放：

```bash
lark-agent queue reconcile --work-id 123 --result completed --reason "reply exists in Lark"
lark-agent queue reconcile --work-id 123 --result not-completed --reason "no remote side effect found"
lark-agent queue reconcile --work-id 123 --result unknown --reason "remote evidence unavailable"
```

已经审查且不再需要推进的中断或终态任务可以直接收口：

```bash
lark-agent queue acknowledge --work-id 123 --reason "superseded and reviewed"
```

## 审核后取消

确认历史工作已经过期、被后续讨论取代、错误入队或只是验收样例后，做可审计取消：

```bash
lark-agent queue cancel --work-id 123 --reason "superseded by later discussion"
lark-agent queue cancel --message-id om_xxx --reason "misclassified owner message"
lark-agent queue cancel --all-interrupted \
  --keep-work-id 4387 \
  --keep-work-id 4670 \
  --reason "audited stale, superseded, or test work"
```

`--all-interrupted` 只选择当前中断项，`--keep-work-id` 保留审核后仍要继续的工作。
命令原子执行：任一选中项正在处理、正在执行外部动作或外部动作结果不确定时，整批
不改变。成功取消会保留消息回执、模型步骤、原决定和动作历史，取消尚未发送的审批
草稿，关闭中断记录并写入包含 `--reason` 的 `operator_cancel` 审计动作；不会删除
数据库记录，也不会发送 Lark 消息。

## 授权缺失后的显式补录

如果 user token 曾经缺失，用户身份轮询无法读取群里 @Owner 的消息，这些消息没有
本地队列回执，不能用 `queue resume` 恢复。授权补齐后，只补录一个明确群和时间范围：

```bash
lark-agent queue backfill --chat-query "Test Group" --since 8h --until 0s
lark-agent queue backfill --chat-id oc_xxx --since 2026-07-25T08:00:00+08:00 --until 2026-07-25T15:30:00+08:00
```

`queue backfill` 只搜索 @Owner 消息，使用正常入队、路由和去重逻辑，且不会推进常规
轮询 cursor。补录后用 `queue summary` 查看新增工作，再由常驻 daemon 按当前策略处理。

`queue retry` 只用于加速当前在线会话中处于 `retry_wait` 的普通瞬时失败，并且
该工作不能有关联的执行中或结果不确定外部动作。它不能恢复处理中、中断、终态
或其他会话的工作；这些情况必须先 inspect，再按上面的精确 resume 流程处理。
模型在强制收尾阶段连续 3 次拒绝提交结构化结论后，运行时会发起一次独立、无工具的
终端收尾请求。它只能使用本 run 已保留的工具回执，产出完整、部分或澄清类
`submit_decision` 同形 JSON，并继续通过现有质量、来源、代码证据和发送门禁。收尾也
未通过时才属于确定性 `model_non_convergence` 并进入死信。需要先检查该 run 的步骤、
证据和收尾失败原因，再决定是否用 `queue resume --force-terminal` 显式重做。

## 审批

```bash
lark-agent approval list
lark-agent approval status
lark-agent approval approve ACTION_ID
lark-agent approval reject ACTION_ID
```

`approval status` 给状态栏和图标刷新使用：只返回各状态计数，以及最多 5 条待审批
公开字段（动作号、类型、工作号、状态）。不会输出 `request_json` /
`response_json`。查看某条拟执行内容仍用 `approval show`。`approval list` 仍是
运维全量记录，状态栏不得调用它。

批准和拒绝只作用于指定 action ID，不会重新运行模型改写草稿。
当代回复因可信度不足或明确风险进入审批时，智能助手会主动私聊 Owner，明确说明
草稿尚未发送，并同时给出审批动作号、完整拟回复、剩余本人事项以及以下两条精确命令：

```text
/approval approve 动作号 confirm
/approval reject 动作号 原因
```

该通知使用与审批动作绑定的稳定幂等键；服务重试不会重复发送同一条审批通知。普通
自动代回复的预通知只描述“准备回复”，不会在权限和可信度检查完成前声称一定会发送。
明确会进入审批的草稿不先发送普通预通知，只发送包含动作号和命令的审批通知。
审批通知按原始请求标明身份：他人 `@Owner` 或发给 Owner 的消息显示“代回复草稿”，
Owner 自己向智能助手发出的请求显示“助手答复草稿”。审批通过后的代回复即使运行在
全局审批模式，也仍然先完成 Owner 私聊通知，再向原发送者发出已经批准的精确草稿。
常驻 daemon 正在短暂写入状态库时，审批命令会在 SQLite 的 5 秒有界等待内取得写锁
并原子更新动作与工作项；不会因为先建立旧读快照再升级写事务而失败。写锁超过等待
上限仍会明确报错，失败时不得假定审批已经生效。
旧会话草稿获批时，只有最新活动会话已经 ready，工作才会迁移到该会话继续处理。
最新会话仍在 starting 或没有活动会话时，草稿保持 ready、工作保持 interrupted；
服务进入 ready 后，启动收敛会自动把它分配给当前会话。
审批记录同时保存原请求的回复身份：`@机器人` 和 Owner 私聊草稿获批后仍由机器人
回复；他人 `@Owner` 的代回复草稿获批后仍由用户身份回复。批准正文不会改变原消息
是发给机器人还是发给 Owner 的事实。升级前创建的旧审批会从工作项原始决策恢复该
身份并消费旧审批键；如果两处都没有合法身份，恢复会明确失败而不是猜测发送者。

## 云文档、Wiki 与 Base 监控

先登记要监控的资源，再按资源类型激活监控并执行首次对账：

```bash
lark-agent subscription add 'https://example.larksuite.com/wiki/RESOURCE?table=TABLE'
lark-agent subscription sync
lark-agent subscription list
```

`subscription sync` 会解析 Wiki 到实际文档或 Base 坐标。文档资源调用飞书云文档评论
订阅接口，确认成功并保存远端订阅号后才标记为 `active`。Base 没有逐资源评论订阅：
解析成功后启用本地应用事件和定期对账路径，远端订阅号保持为空，并读取字段与记录完成
首次冷启动基线；`active` 表示这条本地监控路径已配置，不表示控制台事件已经实际投递。
缺少权限标记为 `forbidden`，其他读取或协议错误标记为 `degraded`。首次基线不会生成
历史任务，后续变更和重连缺口通过定期对账补齐；上线验收仍需发送一次真实 Base 变更
事件，确认 WebSocket 入口收到。

`subscription remove URL --remote` 对文档先取消远端评论订阅，再标记本地删除；Base
没有对应的逐资源远端订阅，因此跳过云文档取消接口并直接标记本地删除。

运行中的 Agent 同时接收两类线索：用户身份轮询看到的飞书文档应用通知，以及官方
WebSocket 的评论通知和 Base 记录变更事件。应用消息只用于提取资源位置，不会作为真人
指令，也不会回复通知应用。当前 Agent 自己发送的应用消息会被排除，避免通知循环。

当受监控的 Base 记录或评论明确提到 Owner 时，会生成 `resource_handoff` 工作。Agent
必须先读取关联资源证据，再按工作区目录清单选择项目并调用
`read_workspace_rules` 读取该项目适用的 `AGENTS.md` 与附属规则，然后核对实现、回归
测试和 Git 历史。只有唯一记录、Owner 为经办人或明确被提及、实时状态字段和选项仍
匹配、修复证据满足项目规则时，才能执行状态更新。
真人会话中“修复后更新状态”这类交接如果引用了一条记录，Agent 只接受当前有界会话
中实际出现、且能解析为唯一 Base app/table/record 坐标的链接；文档、Base 首页或仅
表格链接不会取得状态修改权限。该交接直接把核对结果回复原发送者，不另发一条 Owner
预通知。如果用户身份缺少 Base 记录读取权限，Agent 会停止后续代码检索和状态修改，
直接说明缺失权限；运行 `lark-agent auth login` 重新授权后再恢复该工作。语义模型临时
不可用时，只有“直接 @Owner、明确要求修复后更新状态、回复唯一记录链接、且 Owner
尚未在该线程答复”同时成立，才会按资源交接继续；其他消息仍保持不确定并停止。

Base 状态更新和评论回复都是持久化外部动作：`auto` 模式在全部硬门禁通过后执行一次
并回读验证；`approval` 模式先产生精确动作号，可用 `/approval approve 动作号 confirm`
批准；`paused` 模式不写入。进程在写入期间中断或结果不确定时不会自动重放，必须先核对
Base 当前值再处理。

## 诊断

只检查 SDK 配置和 Keychain 凭据：

```bash
lark-agent doctor --lark-only
```

检查 SDK/Keychain、配置、Workspace、SQLite 和调度状态：

```bash
lark-agent doctor
```

缺少 app id、Keychain 凭据、配置无效、Workspace 越界或状态库不可读都会返回非零
退出码和结构化错误。需要检查远端应用发布态和实时事件权限时，设置
`LARK_AGENT_REMOTE_DOCTOR=1` 后再运行 doctor。

Owner 私聊 Assistant Bot 发送“在吗”或简单问候时走本地快速回复，不依赖模型和
会话历史。其他问题在构建上下文时，会把飞书会话历史作为可选增强；历史读取暂时失败
不会中断当前消息处理。

只有 Owner 在机器人可见群里原生 `@Assistant Bot` 时，官方 Go SDK 的实时事件入口
才会接收并以机器人身份回答。非 Owner 私聊机器人或直接 `@Assistant Bot` 会在排队和
模型调用前静默丢弃。`assistant.reply_scope: all_groups` 允许 Owner 在所有机器人可见
群调用；`configured_groups` 只允许 Owner 在 `--chat-query` 启动时用机器人身份解析
出的群调用，查询为空或没有匹配群都会使启动明确失败。

群内其他人直接 `@Owner` 由用户身份轮询接收，再经过模型判断和代回复策略。
`policy.reply_scope: all_groups` 允许所有可见群进入其余门禁；改为
`configured_groups` 后只允许 daemon `--chat-query` 发现的群。运行
`lark-agent doctor` 可在 `reply_scopes.assistant_mentions` 和
`reply_scopes.owner_mentions` 分别确认实际值。用户身份可见的真人私聊由
`reply_scopes.private_messages` 显示，生产安装应为 `all_private`。
其他群消息即使因为包含“任务”“项目”等内容被判断为 Owner 相关背景，也只能忽略、
记录或私聊通知 Owner，不能向群里回复。每次真正发送或发起审批前，Go 运行时都会
重新执行当前路由；没有原生 `@Owner` 的群消息即使模型返回回复、存在旧候选草稿或
已经批准的旧审批，也会被阻止。被阻止的已批准审批会以 `blocked` 状态留下审计记录，
不会保持 `ready` 等待后续意外发送，也不会把被阻止的发送报告成成功；当前不会为这类
策略拦截再发送一条私聊通知，可用 `/approval 动作号` 查看 `blocked` 原因。

群 @Owner 和对方发来的真人私聊先进入 3 分钟持久等待，再按同一会话语义逐条判断
Owner 是否已处理，以及该消息本身是否合理期待回复。真人私聊如果只是回答 Owner
先前的问题、确认、反应或没有新增请求的对话续接，会静默结束，不会生成多余回复；
群内明确 `@Owner` 不能使用这一静默结果。Owner 发给其他真人的消息在轮询入队前
直接丢弃，不能因业务关键词再次进入模型。`doctor.delegated_reply` 显示等待时长、
最低置信度和不确定重试间隔。发送前还会重新读取一次；读取失败、上下文不全、低
置信度或非法模型输出都不会发送。纯等待消息可在重启后重新读取并判断；已进入对话
调查、保存回复候选或尚未完成对话回复的旧工作保持中断，必须由 Owner 检查并显式
恢复。无状态只读工作可以安全重算，精确审批继续等待本人处理，正在执行或结果不确定
的外部动作不会自动重放。

一条代回复消息通过上述语义判断并被确认仍未处理后，主模型只能生成有内容的回复，
或提交一条带准确草稿的审批请求；不能再用“忽略”“仅记录”或“仅通知”覆盖语义判断。
本人在目标消息之前参与过同一讨论，不算回复了后来的目标消息。无法安全给出具体事实
时，智能助手应说明已完成的有限核对和准确的未知项或拒绝原因，不能编造，也不能静默
吞掉消息。

## GitHub 证据追问

常驻助手可以核对已验证的 GitHub 引用，再只读读取摘要、检查、文件或审查。
`github notify`、`github run`、评论语法和工作流不在这里展开，见
[智能命令与 GitHub](smart-command.md)。

```bash
lark-agent github auth status
lark-agent doctor
```

`doctor.github` 会显示是否启用、精确仓库 allowlist、令牌是否可读、`read_only` 和
`single_lark_listener`。GitHub 未启用或令牌缺失都不阻止普通 Lark 消息链路；如果
当前请求明确要求审查远程 PR，系统会直接返回对应配置阻塞，不会转而搜索本地 Git
历史。

GitHub Action 与已安装 daemon 可以共用同一个 Lark 应用，但 Action 不会启动
WebSocket；已安装 daemon 始终是唯一实时事件监听者。
Action 消息带有机器可验证的引用标记。用户在 Lark 中回复或引用该消息后，daemon
只在以下条件全部成立时开放 `get_github_context`：

- 引用根消息由当前 Lark 应用发送；
- 根消息和当前问题属于同一 chat；
- 引用标记结构及 HMAC 签名有效，且与持久化记录不冲突；
- 仓库位于 `github.allowed_repositories`；
- 本地只读 GitHub 令牌可用。

另一个入口是 Owner 或智能助手明确收到的完整 PR URL。Go 会解析配置对应的 GitHub
Web 主机、仓库和正整数 PR 号，拒绝凭据、查询参数、片段、额外路径和白名单外仓库，
并持久化来源发送者与授权路由。模型不能自行拼出或替换引用。

模型只能选择读取摘要、检查、文件或审查；仓库、PR 和 run 身份来自已验证引用。
GitHub API 不可用、限流、拒绝或返回不一致对象时，回复必须标明证据不完整，不能
根据通知标题补写不存在的原因。

PR 摘要会绑定远程 head SHA；读取文件、审查或检查后会再次读取摘要，同一运行中的
后续 GitHub 读取也必须匹配首次绑定的 head。若 head 已变化，本次调用不返回证据，
不能把两个版本混成一个结论。`checks` 读取该 commit 的检查运行和有界注释。默认
路径只调用 GitHub API，不创建 checkout，不修改当前仓库的 HEAD、索引、文件、
remote 或 hook，也不执行 PR 代码。

Environment 名称、密钥变量和 Action `mode` 写在 [智能命令与 GitHub](smart-command.md)
和 [macOS 安装](install-macos.md)。

## 权限与回复质量

非 Owner 只有群内直接 `@Owner` 或发给 Owner 的真人私聊代回复请求会进入运行，
并且只按只读权限执行：只能读取来源会话的有界上下文和配置 Workspace 内的业务代码，
不能执行 shell、搜索其他会话、修改、删除、提交或部署。
要求读取工作目录外路径，或询问凭据、环境变量、用户目录、进程、网络和主机清单时，
系统会在模型和工具调查前直接拒绝。

代回复交办或调研请求时，运行记录中应至少出现一次成功的相关读取，回复正文应简要
说明“检查了什么、初步发现或未知是什么、给 Owner 传递了什么”。如果 inspect 里只有
`submit_decision`，正文又只是“已提醒”或后续承诺，发送前质量门禁应将其打回。
低风险答复的 `reply_confidence` 达到 `policy.reply_confidence_min`（默认 `0.70`）
时通常会先私聊通知 Owner，再立即代回复，不等待 Owner 确认；真人会话的
`resource_handoff` 不发送这条重复预通知。低于阈值进入审批。
中高风险、承诺、删除修改和结果不确定的外部动作即使可信度很高也不会直接发送。
模型请求中会同时携带经过配置校验的非敏感运行策略。排查助手错误描述自身行为时，
应核对模型输入里的 `runtime_policy`：其中 `owner_reply_confidence_min` 是判断
Owner 是否已经回复的语义门槛，`reply_confidence_min` 是低风险草稿直接发送门槛；
Workspace 项目规则不能覆盖或替代这组当前配置事实。
语义门槛按动作方向区分：`unanswered` 会启动代回复或调查，仍要求达到
`owner_reply_confidence_min`；`answered`、`no_reply_needed`、`withdrawn` 不会替
Owner 发送任何内容，且 `answered` 必须引用有效的 Owner 后续消息或 Owner reaction，
因此达到较低的安全收口阈值即可完成，避免把已处理对话反复重试成死信。

## 代码调查

明确要求检查源码、生产入口、代码入口、API、处理函数或数据库依据的消息会进入
`coding_question` 代码调查链路，而不是简单问答链路。单独提到 Workspace 或业务
仓库不会触发代码调查。该链路允许受 Workspace 边界约束的代码检索和精读，并要求
最终结论引用实际读取到的生产代码或明确写出未知项。
初始上下文包含最多五层的有界目录和优先项目清单；模型还可用
`inspect_git_history` 读取 Workspace 内仓库最近的本地提交。该工具不会联网、fetch、
checkout 或修改仓库，Git 元数据或 alternates 逃出 Workspace 时直接拒绝；继承的
`GIT_*` 仓库、对象库和配置重定向变量会在子进程启动前清除。
代码索引和工作区搜索只用于定位候选文件，不会触发收敛；`read_workspace` 真正读取
并返回可引用的生产源码后，模型会对照原问题和调查计划逐项判断。一个文件只覆盖部分
问题时，剩余的有界代码工具仍可继续使用；全部字段已有证据时应立即提交结论。除下述
序列化结构证据补全外，有可引用证据后，最后两个模型回合只允许提交结论和进行一次
有界修正，不能继续消耗调查轮次。
代码调查默认只读。只有目标消息明确要求修改、修复或实现时，Owner 才会看到
`edit_workspace` 和 `write_workspace`。前者对已有文件做精确、唯一、互不重叠的原文
替换；后者新建文件或整文件覆盖，并可在工作区内创建父目录。改文件后必须再次
`read_workspace`，旧摘要不能再当证据。改文件不走 `shell` 凑合，最终仍用
`submit_decision` 收口。非 Owner 始终只读。
`read_workspace` 可用从 1 开始的 `offset` 和 `limit` 按行切片读取，来源摘要仍是整
个文件。`search_workspace` 可带 `glob`、`literal`、`regex` 和 `context_lines`。
`list_workspace` 可带 `glob` 列出匹配文件。命令输出超过上限时，完整内容落到工作区
内 `.local/lark-agent/runtime/shell-output/`，回执只带预览、路径、摘要和字节数。
模型输出被截断且带有工具调用时，这些调用不会执行。
当一个模型回合并行请求多个只读工具时，运行时会先按调用编号记录完全部结果，再追加
预算或收敛提示。上下文超过上限后，最新这一整轮调用作为一个整体保留并压缩结果正文，
较旧的整轮调用才会被结构化摘要替换；不会把工具结果与发起它的模型消息拆开。若模型
服务返回“参数无效”，可通过 `queue inspect` 检查最近一轮是否同时包含多个工具调用，
以及下一轮请求前是否发生上下文压缩。历史调用参数过大时会替换成包含原始字节数和摘要
校验值的合法 JSON；旧并行轮次的摘要会逐个保留调用编号、工具名和参数。包含运行进度
提示在内的最终请求仍受 `max_context_bytes` 硬限制，无法压缩的协议元数据会在本地报错，
不会继续向模型服务发送超限请求。
确定性代码结论至少要引用一个本轮 `read_workspace` 返回的生产来源；只引用搜索候选
会被打回继续精读。用户明确写出工作区内的仓库或相对路径时，调查计划、搜索和读取必须
保持原始大小写并限制在该子树。该精确子树会明确告诉模型是当前配置 Workspace 内
已经可读的范围，不需要也不允许为此切换 Workspace；无法携带路径范围的全局索引、
调用链、全局浏览、shell、Workspace 规则和技能读取工具不会出现在本次模型工具
列表中，即使模型仍提交旧调用也会被运行时拒绝。
工具回执里的“结果摘要”标识整次工具输出，`sources` 里的“来源摘要”才标识具体文件；
模型应原样引用 `sources`。若模型误把结果摘要填入来源引用，运行时只在该路径和来源
类型于本轮唯一对应一个来源摘要时纠正；同一路径读到多个版本时仍拒绝，不能猜测版本。
模型传入配置工作区目录名前缀时会规范化为工作区相对路径；`search_workspace` 未显式
传入 `path` 时由运行时注入该精确子树；传入 `sample-client/...` 这类仓库内部相对路径时，
运行时会补上精确子树前缀。大小写不同的同名仓库仍按兄弟项目替换拒绝，不能被自动
改写。执行列表、搜索和读取前还会逐级核对磁盘实际大小写，并解析精确子树和目标的
真实路径；子树内符号链接若指向 Workspace 中的兄弟项目，会在内容进入模型前被拒绝。
精确范围调查最多先做两次有界定位，然后应读取候选生产文件；必需的
`submit_investigation_plan` 只记录控制信息，不消耗有效调查调用额度。每轮模型调用
都会收到已用、总计和剩余调查调用次数，以及模型轮次和上下文容量。搜索先匹配完整
短语，未命中时要求空格分隔的每个查询词都出现在同一文件中，仍不会扫描或返回精确
子树以外的兄弟项目。
`reply` 决策必须显式填写回复可信度；显式低于配置阈值才进入审批，漏填会在有界模型
循环内打回补全，不能按零可信度静默转审批。
代码回复必须显式声明 `evidence_status`。`verified` 结论必须满足上述生产源码精读
要求；`insufficient` 表示证据不足，运行时会丢弃模型的自由文本并发送固定的保守
答复，避免在“没有找到”“无法确认”等话术后夹带未经核实的推断。证据不足答复也
必须先完成至少一次真实的工作区代码检索、路径追踪、浏览或读取；只读聊天历史或
直接提交保守话术会被打回继续调查。

如果问题要求某个序列化字段的具体结构，只读到 `String`、字节数组、原始 JSON
或其他不透明容器类型不算完成。模型必须在剩余预算内继续读取当前文档样例、测试
fixture、协议定义或序列化实现；如果问题明确询问具体结构，运行时也会要求已读来源
中存在对象样例、协议结构或序列化操作，不能只靠提示词约束。发送前还会核对回复中的
仓库相对文件和目录都来自本轮实际读取，而不是仅搜索到的候选，并核对驼峰代码字段以
完整标识符出现在所引用的读取内容中；路径、字段、具体 JSON 样例或回调名对不上时会
打回修正，不会把相似名称当成已验证事实。
问题明确点名 `sampleContent` 这类字段时，JSON 样例还必须与该字段出现在同一局部说明
或序列化语句中；同一文件或其他文件里的无关 JSON、无关序列化调用不能冒充这个字段
的结构证据。同一条回答同时列出请求、响应、推送或本地状态 JSON 时，所有 JSON 都
必须原样出现在本轮引用的读取内容中；只有明确作为 `sampleContent` 结构讲述的 JSON
还要额外通过字段局部绑定校验。响应 JSON 不会再被错误拿去和 `sampleContent` 样例比较，
但任何未在引用来源中出现的 JSON 仍会被拒绝。
如果一份已经完成真实代码读取的 `verified` 草稿只因回答中出现来源没有逐字支持的
路径、驼峰字段、回调或 JSON 样例而被拒绝，运行时会追加一次仅允许
`submit_decision` 的修正回合。这个回合会显示扩展后的当前/总模型回合数，不增加
调查工具次数，也不会放宽 Workspace 或权限。运行时仍保留本轮原始读取内容；模型
不能把“回答措辞未通过校验”或“旧消息已压缩”说成证据丢失并立即降级为证据不足，
而应删除无证据标识符，或改成来源实际支持的自然语言后重新提交。若纠错时确实发现
原始来源没有定义用户所问的关键事实，仍可准确说明这个新证据缺口并按正常的证据不足
流程保守退出，不会被强迫生成确定结论。
一旦本轮读取已经看到该字段的不透明声明，并且距离最终提交还至少有两个模型回合，
运行时会提前进入有界补全：下一回合只允许在既有精确项目范围内按字段名做一次
`search_workspace`；搜索路径可以省略或完整重复该项目范围，不能缩小到子目录，也
不能用大小写相似的范围替代，也不能自行设置结果条数来截断搜索。即使此前普通搜索
已经达到“连续无可引用来源”的限制，或模型先直接读到了字段声明而尚未提交调查计划，
这一次由运行时指定的专用字段恢复搜索仍有独立机会；模型自行发起的普通宽泛搜索仍
必须先提交调查计划。搜索结果中只有局部片段同时包含字段名和具体对象样例或序列化
操作的路径会被保留；如果该字段被说明为未知、未定义，后面另一个字段的 JSON 不会
被误绑为它的证据。再下一回合只允许 `read_workspace` 读取其中一个候选路径，其他
生产文件、无关 JSON 文件、再次搜索和提前提交“证据不足”都会被拒绝。
如果直到倒数第二个模型回合仍只拿到不透明字段声明，并且还剩一次调查调用，运行时会
把这一回合严格限制为一次 `read_workspace`：只能读取一个已经定位到的当前文档、
fixture、协议或序列化实现，不能再搜索、列目录、执行 shell、拉聊天记录或提前提交。
最后一个回合只开放 `submit_decision`，必须引用本轮实际读取的证据，证据仍不足就明确
说明未知，不能编造结构。

Owner 可以先在同一会话说明“这次只看 `sample-org/sample-project/sample-module`”，下一条再用
“当前项目”“这个项目”“该项目”等自然说法提问。运行时只从同一发送者最近的有界
上下文继承该精确路径，并立即启用大小写、兄弟目录和符号链接边界；不会从其他人的
消息继承工作区范围。
`coding_question` 不能以 `ignore`、`record`、`notify` 或 `request_approval`
结束；代码事实问答必须直接返回 `verified` 或完成上述调查后的 `insufficient`，
避免正常问题被静默、只记录、只通知或沉入审批而无回复。

用户身份接口若发现进程缓存的 `user_access_token` 已过期，会先重读 Keychain；
如果没有更新的凭据，再通过官方 SDK 使用 `refresh_token` 刷新，并把轮换后的两个
token 写回 Keychain 后重放原请求一次。若刷新凭据也过期，运行
`lark-agent auth login` 重新授权，再执行 `lark-agent doctor --lark-only`。

守护进程内的 worker 共用一个串行 SQLite 连接，避免并发写入彼此返回
`database is locked`。如果 `queue inspect`、`queue resume` 等另一进程短暂占用写锁，
守护进程最多等待 5 秒；超时仍会明确记录存储错误，不会把未持久化的状态当成成功。

## 故障回退

- 安装失败：不要加载新服务；保留当前独立状态和安装备份，修复原因后重新 check。
- 新服务未 ready：安装控制器会卸载新 label；检查 stderr 日志和 doctor。
- live 行为异常：停止新服务，恢复安装备份，只加载已知可运行的旧版本。
- 外部动作不确定：保持工作暂停，不要用 `queue retry` 或重启来猜测性重发。

真实验收只允许在用户本次明确授权的精确 chat ID 进行。具体机器人名、群名和 chat
ID 属于本机安装与部署配置，不写入项目通用代码或仓库文档；不得按相似名称猜测目标。

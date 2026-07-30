# 运行、恢复与故障处理

## 服务状态

```bash
lark-agent daemon status
lark-agent daemon start
lark-agent daemon stop
lark-agent daemon restart
```

LaunchAgent label 是 `com.liuchong.lark-agent`。日志位于
`~/Library/Logs/lark-agent/`。

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

`queue tasks` 是面向处置的有界列表：

- `action` 只列出需要 Owner 决策或收口的任务；
- `running` 列出正在执行或会自动继续等待的任务；
- `recent` 和 `all` 用于查看近期历史，不代表每条都需要操作。

机器人上线或离线通知只显示非零分类。显示“需要你处理 0 条”表示当前没有人工待办，
不会产生悬空任务。Owner 在智能助手私聊中发送 `/tasks` 能看到工作号、最新可靠事实
和可执行的下一条命令；发送 `/task 工作号` 查看单条详情。

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

Owner 在群聊中发送这些命令时只收到转到私聊的提示，群里不展示任务数量和内容。
非 Owner 私聊机器人或直接 `@机器人` 仍然静默。

## 自动收敛与显式恢复

新会话进入 ready 后会先处理全部中断工作：

- 没有不确定外部动作的只读或模型工作自动分配给新会话并重新读取当前证据；
- 尚未批准的精确草稿保持等待，并私聊 Owner 工作号、动作号和批准命令；
- 中断时正在执行的 shell、回复或通知不会重放，工作会收口为死信并私聊核对命令；
- 模型不收敛或有界重试耗尽也会私聊已完成检查、停止原因和精确 inspect 命令。

`queue resume` 只用于人工核对后的手动暂停、离线补录或终态工作：

```bash
lark-agent queue resume --work-id 123
lark-agent queue resume --message-id om_xxx
```

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
模型在强制收尾阶段连续 3 次拒绝提交结构化结论属于确定性
`model_non_convergence`，会直接进入死信而不自动重试。需要先检查该 run 的步骤和
证据，再决定是否用 `queue resume --force-terminal` 显式重做。

## 审批

```bash
lark-agent approval list
lark-agent approval status
lark-agent approval approve ACTION_ID
lark-agent approval reject ACTION_ID
```

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

群 @Owner 和对方发来的真人私聊先进入 3 分钟持久等待，再按同一会话语义逐条判断
Owner 是否已处理，以及该消息本身是否合理期待回复。真人私聊如果只是回答 Owner
先前的问题、确认、反应或没有新增请求的对话续接，会静默结束，不会生成多余回复；
群内明确 `@Owner` 不能使用这一静默结果。Owner 发给其他真人的消息在轮询入队前
直接丢弃，不能因业务关键词再次进入模型。`doctor.delegated_reply` 显示等待时长、
最低置信度和不确定重试间隔。发送前还会重新读取一次；读取失败、上下文不全、低
置信度或非法模型输出都不会发送。纯等待消息可在重启后重新读取并判断，已进入模型、
只读调查或尚未执行外部动作的旧工作会在新会话 ready 后重新读取当前证据；精确审批
继续等待本人处理。只有正在执行或结果不确定的外部动作不会自动重放。

一条代回复消息通过上述语义判断并被确认仍未处理后，主模型只能生成有内容的回复，
或提交一条带准确草稿的审批请求；不能再用“忽略”“仅记录”或“仅通知”覆盖语义判断。
本人在目标消息之前参与过同一讨论，不算回复了后来的目标消息。无法安全给出具体事实
时，智能助手应说明已完成的有限核对和准确的未知项或拒绝原因，不能编造，也不能静默
吞掉消息。

## GitHub 通知与追问

本地令牌状态：

```bash
lark-agent github auth status
lark-agent doctor
```

`doctor.github` 会显示是否启用、精确仓库 allowlist、令牌是否可读、`read_only` 和
`single_lark_listener`。令牌缺失只会禁用 GitHub 增强，不会阻止普通 Lark 消息链路。

GitHub Action 是一次性 HTTP 发送者。它可以与已安装 daemon 使用同一个 Lark app
ID 和 app secret，但不会启动 WebSocket；已安装 daemon 始终是唯一实时事件监听者。
Action 消息带有机器可验证的引用标记。用户在 Lark 中回复或引用该消息后，daemon
只在以下条件全部成立时开放 `get_github_context`：

- 引用根消息由当前 Lark 应用发送；
- 根消息和当前问题属于同一 chat；
- 引用标记结构及 HMAC 签名有效，且与持久化记录不冲突；
- 仓库位于 `github.allowed_repositories`；
- 本地只读 GitHub 令牌可用。

模型只能选择读取摘要、检查、文件或审查；仓库、PR 和 run 身份来自已验证引用。
GitHub API 不可用、限流、拒绝或返回不一致对象时，回复必须标明证据不完整，不能
根据通知标题补写不存在的原因。

生产工作流使用受保护的 GitHub Environment `lark-production`。其中
`LARK_APP_SECRET` 是 secret，`LARK_APP_ID`、`LARK_CHAT_ID` 和 `LARK_BASE_URL`
是部署变量。`LARK_BASE_URL` 必须显式设置，国际版使用
`https://open.larksuite.com`。仓库工作流只检出默认分支的 Action 实现，不检出触发
run 的 PR 头，不下载 artifacts，不执行外部贡献代码。

非 Owner 只有群内直接 `@Owner` 或发给 Owner 的真人私聊代回复请求会进入运行，
并且只按只读权限执行：只能读取来源会话的有界上下文和配置 Workspace 内的业务代码，
不能执行 shell、搜索其他会话、修改、删除、提交或部署。
要求读取工作目录外路径，或询问凭据、环境变量、用户目录、进程、网络和主机清单时，
系统会在模型和工具调查前直接拒绝。

代回复交办或调研请求时，运行记录中应至少出现一次成功的相关读取，回复正文应简要
说明“检查了什么、初步发现或未知是什么、给 Owner 传递了什么”。如果 inspect 里只有
`submit_decision`，正文又只是“已提醒”或后续承诺，发送前质量门禁应将其打回。
`reply_confidence` 低于 `policy.reply_confidence_min` 时统一进入审批，不再对直接
@Owner 的低风险草稿使用隐藏的较低阈值。

明确要求检查源码、生产入口、代码入口、API、处理函数或数据库依据的消息会进入
`coding_question` 代码调查链路，而不是简单问答链路。单独提到 Workspace 或业务
仓库不会触发代码调查。该链路允许受 Workspace 边界约束的代码检索和精读，并要求
最终结论引用实际读取到的生产代码或明确写出未知项。
代码索引和工作区搜索只用于定位候选文件，不会触发收敛；只有 `read_workspace` 真正
读取并返回可引用的生产源码后，下一轮才只允许提交结论，不再继续读取聊天历史、规则、
测试或执行额外搜索和 shell。确定性代码结论至少要引用一个本轮 `read_workspace`
返回的生产来源；只引用搜索候选会被打回继续精读。`reply` 决策必须显式填写回复
可信度；显式低于配置阈值才进入审批，漏填会在有界模型循环内打回补全，不能按零
可信度静默转审批。
代码回复必须显式声明 `evidence_status`。`verified` 结论必须满足上述生产源码精读
要求；`insufficient` 表示证据不足，运行时会丢弃模型的自由文本并发送固定的保守
答复，避免在“没有找到”“无法确认”等话术后夹带未经核实的推断。证据不足答复也
必须先完成至少一次真实的工作区代码检索、路径追踪、浏览或读取；只读聊天历史或
直接提交保守话术会被打回继续调查。
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

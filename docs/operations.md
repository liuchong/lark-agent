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
lark-agent queue inspect --work-id 123
lark-agent queue inspect --message-id om_xxx
```

inspect 会显示接收回执、工作项、最近模型步骤、最近外部动作和中断快照，用于判断
消息是离线积压、重复、排队、处理中、中断、已回复还是终态。

## 显式恢复

跨重启任务不会自动回放。只恢复一条明确消息：

```bash
lark-agent queue resume --work-id 123
lark-agent queue resume --message-id om_xxx
```

恢复已完成、已忽略、已取消或死信工作时还必须明确：

```bash
lark-agent queue resume --work-id 123 --force-terminal
```

中断时正在执行的 shell、回复、Owner 通知或生命周期通知属于结果不确定的外部
动作。系统不会自动重发；Owner 必须先根据飞书和本地审计证据确认实际结果。

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

## 审批

```bash
lark-agent approval list
lark-agent approval status
lark-agent approval approve ACTION_ID
lark-agent approval reject ACTION_ID
```

批准和拒绝只作用于指定 action ID，不会重新运行模型改写草稿。
常驻 daemon 正在短暂写入状态库时，审批命令会在 SQLite 的 5 秒有界等待内取得写锁
并原子更新动作与工作项；不会因为先建立旧读快照再升级写事务而失败。写锁超过等待
上限仍会明确报错，失败时不得假定审批已经生效。
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
`reply_scopes.owner_mentions` 分别确认实际值。两个范围相互独立，范围变化不会自动
重放旧消息。

非 Owner 只有群内直接 `@Owner` 的代回复请求会进入运行，并且只按只读权限执行：
只能读取来源群的有界上下文和配置 Workspace 内的业务代码，不能执行 shell、搜索
其他群、修改、删除、提交或部署。
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

真实验收只允许在用户本次明确授权的会话进行。当前验收目标是“龙虾群🦞”和
“测试负责人的智能助手”私聊；不得向名称相近的会话或 Example Group 群发送测试消息。

# macOS 安装

## 安装边界

安装只写当前用户目录，不需要管理员权限：

- 配置：`~/.config/lark-agent/config.yaml`
- 状态和已安装二进制：`~/Library/Application Support/lark-agent/`
- 日志：`~/Library/Logs/lark-agent/`
- 服务：`~/Library/LaunchAgents/com.liuchong.lark-agent.plist`
- 状态栏：`~/Applications/Lark Agent.app`

Agent 通过官方公开 Go SDK 访问 Lark。配置只保存 app id 和 Keychain 引用；app secret
必须在 macOS Keychain 中。用户 token 可选，只用于用户身份轮询和代回复。凭据不写入
plist、脚本参数或仓库文件。

## 新安装

```bash
go build -o ./lark-agent ./cmd/lark-agent
./lark-agent init \
  --workspace /absolute/path/to/workspace \
  --app-id cli_xxx \
  --owner-open-id ou_xxx
./lark-agent auth login < /path/to/private-lark-credentials.json
./lark-agent doctor --lark-only
./scripts/macos/install-lark-agent.sh
```

安装脚本执行以下门禁：

1. 获取当前用户安装锁，从当前源码构建候选 Agent，但不覆盖正在运行的已安装二进制；并发安装会在停止服务前失败。
2. 在停止服务前编译候选状态栏；如当前独立服务已加载，再通过现有安装执行正常停止，并在有界等待内确认异步 `bootout` 最终使 label 消失。
3. 停止后备份配置、状态、二进制、wrapper、私有环境、状态栏和 plist，并暂时移除已卸载的 plist；随后执行完整 `lark-agent doctor`。
4. doctor 通过 SDK/Keychain 验证 app id、凭据引用、Workspace 和状态库；需要真实远端权限预检时设置 `LARK_AGENT_REMOTE_DOCTOR=1`。
5. 所有候选检查通过后才原子替换二进制。
6. 把模型环境写入权限为 `0600` 的当前用户私有文件，不写入 plist。
7. 原子安装候选状态栏并写入 LaunchAgent。
8. 仅在新 daemon 进入 ready 后保留已加载状态；任何失败都会恢复整套旧安装，并重新加载原先处于 loaded 状态的服务。

本安装器只处理新的独立配置和状态，不读取旧目录，不迁移历史数据。

## 安装参数

安装脚本支持以下环境变量：

- `CONFIG_PATH`、`STATE_PATH`：覆盖独立目标路径。
- `CHAT_QUERY`：发现并标记配置群和验收群的关键词，默认“Test Group”。默认
  `assistant.reply_scope: all_groups` 和 `policy.reply_scope: all_groups` 时不会据此
  限制其他群；任一字段设为 `configured_groups` 时，该字段对应的群范围由它限制。
- `POLL_INTERVAL`：轮询间隔，默认 `10s`。
- `INSTALL_LOAD=0`：只安装，不加载，供隔离验证使用。
- `OPEN_STATUS_APP=0`：安装后不打开状态栏，供隔离验证使用。

不要把 token、私钥或模型密钥写进仓库、plist 或命令参数。

当前全群安装应在配置中明确保存：

```yaml
assistant:
  reply_scope: all_groups
policy:
  reply_scope: all_groups
```

前者控制 Owner 可在哪些群里 `@机器人`，后者控制其他人可在哪些群里 `@Owner`
后触发智能代回复。非 Owner 私聊机器人或直接 `@机器人` 始终静默；两种群范围
配置不会互相代替。

建议同时使用当前默认调查预算：

```yaml
agent:
  max_context_bytes: 65536
fast_path:
  simple_max_turns: 3
tool_policy:
  coding_max_tool_calls: 16
```

这不会放宽安全边界。非 Owner 请求仍是同群和 Workspace 只读，环境刺探与工作目录
外访问仍被强制拒绝。

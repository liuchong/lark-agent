# 智能命令与 GitHub

智能命令是一次跑完就退出的 Agent 主循环：不启动飞书长连接，只有允许名单里的工具真正需要时才发飞书 HTTP，结束后进程退出。

常驻助手怎么安装、怎么在聊天里追问已验证的 GitHub 引用，见 [macOS 安装](install-macos.md) 和 [运行、恢复与故障处理](operations.md)。这份文档只写一次性命令和 GitHub Actions。

## 选哪条命令

| 场景 | 命令 | 会不会跑模型 |
| --- | --- | --- |
| 把 CI / 工作流结果发到飞书 | `lark-agent github notify` | 否 |
| 根据 GitHub 事件做一次判断，并可能发评论、改标题、写检查、发飞书或写 job output | `lark-agent github run` | 是 |
| 不在 GitHub 上，但要用同一套主循环跑完一条任务 | `lark-agent run` | 是 |

`github run` 先读事件 JSON 和仓库引用。代码只通过 GitHub HTTP 按事件提交 SHA 读取，不打开工作区 shell，也不执行 PR 头上的代码。

`github notify` 只把可信工作流事实用 HTTP 发到指定飞书群。现有 `.github/workflows/lark-notify.yml` 不用加 `mode`。

## GitHub 评论怎么唤醒

评论或 PR review 评论里要有完整词 `@lark-agent`。后面默认是自然语言。只有三个斜杠命令：`/review`、`/title`、`/check`。评论里唯一允许的开关是 `--dry-run`。

`@lark-agent review` 没有斜杠，按普通句子处理，不会当成 `/review`。

未知的 `/foo`：如果允许发评论，会回一条帮助后成功退出，不跑模型；不允许发评论则退出码 2。

## GitHub Actions

仓库根目录 `action.yml` 的 `mode` 默认是 `notify`，继续走 `github notify`。`mode: run` 才走 `github run`。

两种形态都可以和已安装 daemon 使用同一个 Lark 应用，但都不会启动 WebSocket；本机 LaunchAgent 始终是唯一实时事件监听者。

需要飞书或模型密钥的 job 使用受保护 Environment `lark-production`：

| 名称 | 类型 | 用途 |
| --- | --- | --- |
| `LARK_APP_SECRET` | secret | 与本机 daemon 相同的应用 secret |
| `OPENAI_API_KEY` | secret | 仅 `mode: run` 需要 |
| `LARK_APP_ID` | variable | 同一个应用 ID |
| `LARK_CHAT_ID` | variable | 精确 chat ID，不能按群名猜测 |
| `LARK_BASE_URL` | variable | 必须显式设置。国际版 `https://open.larksuite.com`，飞书中国站 `https://open.feishu.cn` |

模型密钥只从进程环境读取，不会调用 GitHub 的 secrets HTTP API。工作流只检出仓库默认分支上的 Action 实现，不检出 PR 头，不下载产物，不执行外部贡献代码。

可复用的 YAML 和 prompt 在 [examples/github-agent](../examples/github-agent/README.md)。本仓库正在跑的是 `.github/workflows/lark-agent-*.yml` 里同一套文件。

| 工作流 | 做什么 |
| --- | --- |
| `lark-notify.yml` | CI 完成后普通通知，不加 `mode` |
| `lark-agent-comment.yml` | 评论里 `@lark-agent` 后回答 |
| `lark-agent-review-dispatch.yml` | 手动触发 PR 审查 |
| `lark-agent-pr-review.yml` | 新开或打标签的 PR 审查 |
| `lark-agent-event-summary.yml` | Issue / PR / 非 CI 工作流摘要 |
| `lark-agent-master-changelog.yml` | 默认分支 changelog |
| `lark-agent-release.yml` | 发布说明，验证后再建草稿 release |
| `lark-agent-pr-summary.yml` | PR 摘要 |
| `lark-agent-merge-check.yml` | 合并门禁检查 `lark-agent-gate` |
| `lark-agent-notify-style.yml` | 通知口吻的事件摘要 |
| `lark-agent-title.yml` | 标题改写 |

Fork 来的 PR 会跳过。名为 `CI` 的 `workflow_run` 不进事件摘要和通知口吻工作流，避免和 `lark-notify.yml` 抢同一条 CI 完成事件。

## 写操作允许名单

`--allowed-actions` 只能出现这些名字：

`post_github_comment`、`upsert_github_check`、`update_github_issue_title`、`send_lark_message`、`write_job_output`。

`/review` 和 `/check` 在有 PR 号时会额外允许检查写入。`--dry-run` 会清空全部写入。检查名固定为 `lark-agent-gate`，不会调用 merge API。

## 输出语言

对外语言由配置决定，不看提示词是什么语言写的。提示词、规则文件和 `--message` 是给模型的指令，不是语言样本；早期版本靠数汉字和拉丁词猜语言，结果所有英文提示词都产出英文内容。

优先级是 `--output-language` > `LARK_AGENT_OUTPUT_LANGUAGE` > `output.language`（具体值）> `output.fallback_language`。取值只有 `auto`、`zh-CN`、`en-US`，其他值在跑模型和发任何 HTTP 之前以退出码 2 失败。Actions 里用 `action.yml` 的 `output_language` 输入。

解析结果会出现在 stdout 的 `data.output_language`，也会作为“必须使用的对外语言”进入每一轮模型上下文。

真正拦住语言不符的是写入门禁，不是提示词：`post_github_comment` 的 `body`、`send_lark_message` 的 `text`、`upsert_github_check` 的 `summary` 和 `text`、`write_job_output` 的 `value` 语言不符时返回有类型的工具错误，不发 HTTP，一次性写入额度也不消耗，模型可以改写后重试。

标题不受语言约束。Issue、PR 和检查的标题是仓库产物，遵守仓库自己的英文 Conventional Commits 约定。

未知斜杠命令和 `/review`、`/check` 用在非 PR 上的帮助评论也跟随这个语言。

## 结束方式

工作类型是 `smart_command`。模型必须调用 `submit_decision`，且 `decision` 只能是 `record`。这个工具本身不发飞书、不写 GitHub。真正的评论、检查、标题、飞书消息和 job output 只能通过上面的具名写入工具，每种进程最多成功一次。

如果模型反复给出无效的终结决定，用完限定的几次终结尝试后，会像常驻助手那样交给收尾模型再收一次；收尾模型拿不到任何工具，所以 `--dry-run` 仍然是干跑。收尾也收不住，或者用尽轮数上限，进程以 1 退出并且不再写入。收尾模型取 `model.roles.finalizer` 绑定的 profile。

场景编号、事件 JSON 样例和退出码见 [智能命令规格](../spec/smart-command.md)。

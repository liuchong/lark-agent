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

| 输入 | 必填 | 说明 |
| --- | --- | --- |
| `mode` | 否 | 默认 `notify`；`run` 才跑模型 |
| `lark_app_id` | 是 | 与本机 daemon 同一个应用 ID |
| `lark_app_secret` | 是 | 来自受保护 Environment |
| `lark_base_url` | 是 | 必须显式给出，不猜站点 |
| `github_token` | 是 | 一般是 `github.token` |
| `lark_chat_id` | 视情况 | 需要发飞书时必填，必须是精确 chat ID |
| `prompt_file` | 视情况 | `mode: run` 的提示词，仓库相对路径 |
| `message` | 视情况 | 行内消息，可替代 `prompt_file` |
| `rules_file` | 否 | 追加的规则文件 |
| `allowed_actions` | 否 | 逗号分隔的写入工具白名单；不给则没有写入能力 |
| `output_language` | 否 | `auto`、`zh-CN` 或 `en-US` |
| `model_reasoning_effort` | 否 | 主模型档案的推理强度，例如 `high` |
| `model_timeout` | 否 | 单次模型尝试超时，Go duration，例如 `180s` |
| `dry_run` | 否 | `true` 时清空全部写入 |

Actions 里没有配置文件，配置由默认档案加环境变量组成，所以 `model_reasoning_effort` 和 `model_timeout` 是在 CI 里调整档案的唯一入口。留空就用默认：单次尝试 120 秒，一次调用最多 3 次尝试。这两个值一起决定一次调用的最坏耗时，要留在 8 分钟循环预算里。

唯一的输出是 `changelog`，来自 `write_job_output` 工具。

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

| 工作流 | 做什么 | 落地面与语言 |
| --- | --- | --- |
| `lark-notify.yml` | CI 完成后普通通知，不加 `mode` | 飞书，不跑模型 |
| `lark-agent-comment.yml` | 评论里 `@lark-agent` 后回答 | GitHub 评论，`en-US` |
| `lark-agent-review-dispatch.yml` | 手动触发 PR 审查 | GitHub 评论与检查，`en-US` |
| `lark-agent-pr-review.yml` | 新开或打标签的 PR 审查 | GitHub 评论与检查，`en-US` |
| `lark-agent-pr-summary.yml` | PR 摘要 | GitHub 评论，`en-US` |
| `lark-agent-merge-check.yml` | 合并门禁检查 `lark-agent-gate` | GitHub 检查，`en-US` |
| `lark-agent-release.yml` | 发布说明，验证后再建草稿 release | release 正文，`en-US` |
| `lark-agent-title.yml` | 标题改写 | GitHub 标题，标题不受语言约束 |
| `lark-agent-event-summary.yml` | 新开 Issue / PR 摘要 | 飞书，沿用配置语言 |
| `lark-agent-master-changelog.yml` | 默认分支 changelog | 飞书，沿用配置语言 |
| `lark-agent-notify-style.yml` | CI 失败时用通知口吻解释一次 | 飞书，沿用配置语言 |

Fork 来的 PR 会跳过。发飞书的智能命令工作流之间不共用触发事件：新开的 Issue 和 PR 归事件摘要，CI 完成归 `lark-notify.yml` 的确定性通知，只有 CI 失败才额外由通知口吻工作流写一段解释。所以一次仓库事件不会产生两条模型写的消息。

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

示例工作流按落地面选语言：写 GitHub 评论、检查结论或 release 正文的是公开仓库内容，固定 `output_language: en-US`；只发飞书的沿用配置里的语言，不写这个输入。

## 内容质量约束

下面这些要求写在智能命令的系统提示里，对所有仓库生效。自己的 `prompt_file` 不需要重复它们，只写这次要做什么。

- 先给结论再给细节。不允许把 diff 的复述当成结论。
- 不许出现只在本仓库内部有意义的编号和符号：规格场景号、内部工单号、测试名、源码标识符。只能通过这类编号解释的改动，改写成可观察行为。
- 工作流名、分支名、标签名、release 名、文件名和命令名是专名，照抄事件或仓库给的原文。即使整句是中文也不许翻译，更不许替换成自己对它用途的描述。

飞书消息是纯文本类型，Markdown 链接语法会原样显示，所以 `send_lark_message` 的工具描述要求写裸 URL。

## 模型调用预算

智能命令和常驻助手用同一套模型档案语义。档案声明的推理模式、推理强度、能力上限、单次尝试超时和尝试次数都会真正生效，不存在“某个字段只在常驻助手里管用”。

一次调用的预算来自档案：单次尝试默认 120 秒，一次调用最多 3 次尝试。连接中断、单次尝试超时、429、5xx、529 和解出来没有内容的应答会按 2 秒起的指数退避再试；供应商给了 `Retry-After` 就至少等那么久。400、401、403、404 和额度耗尽是确定性失败，只花一次往返，不会把尝试预算耗在同一个错误上。进程被取消或到期时立刻停止，不会再发一次请求。

一次调用的最坏耗时是超时乘尝试次数加退避，而整个智能命令仍有 8 分钟循环预算和 YAML 的 `timeout-minutes`。调大 `model_timeout` 时要一起算这三个上限，否则重的提示词会从“模型超时”变成“循环超时”。

## 跑完之后看什么

进程在 stdout 输出一个 JSON 信封，Actions 日志里可以直接读：

| 字段 | 含义 |
| --- | --- |
| `skipped` | 这次没有任何对外写入。非干跑时由写入门禁推导，不取决于模型自己怎么说 |
| `partial` | 富化、比较或文件读取失败，或者证据不足 |
| `output_language` | 本次解析出的对外语言 |
| `comment_id`、`check_id`、`message_id`、`title` | 各类写入成功后的标识，没写就是空 |
| `outputs` | `write_job_output` 写出的 job output |
| `reference` | 已验证的 GitHub 引用 |

退出码 0 是成功，包含“该跳过所以什么都没发”。2 是参数或配置不合法，此时不跑模型也不发 HTTP。1 是跑到一半失败，例如飞书 HTTP 失败或模型收不住。

## 结束方式

工作类型是 `smart_command`。模型必须调用 `submit_decision`，且 `decision` 只能是 `record`。这个工具本身不发飞书、不写 GitHub。真正的评论、检查、标题、飞书消息和 job output 只能通过上面的具名写入工具，每种进程最多成功一次。

如果模型反复给出无效的终结决定，用完限定的几次终结尝试后，会像常驻助手那样交给收尾模型再收一次；收尾模型拿不到任何工具，所以 `--dry-run` 仍然是干跑。收尾也收不住，或者用尽轮数上限，进程以 1 退出并且不再写入。收尾模型取 `model.roles.finalizer` 绑定的 profile。

场景编号、事件 JSON 样例和逐条退出码规则见 [智能命令规格](../spec/smart-command.md)。

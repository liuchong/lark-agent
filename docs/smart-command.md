# 智能命令与内建 GitHub 支持

智能命令是一次跑完就退出的 Agent 主循环：不启动飞书长连接，只有在允许名单里的工具真正需要时才发飞书 HTTP，结束后进程退出。

两条入口：

- `lark-agent run`：本地或任意环境里跑一条智能命令。可以读当前工作区文件，不能开 shell，也不能改文件。
- `lark-agent github run`：先读 GitHub 事件 JSON 和仓库引用，再跑同一套智能命令。代码只通过 GitHub HTTP 按事件提交 SHA 读取，不打开工作区 shell，也不执行 PR 头上的代码。

`lark-agent github notify` 仍是普通命令：不跑模型，只把可信工作流事实用 HTTP 发到指定飞书群。现有 `.github/workflows/lark-notify.yml` 不用加 `mode`。

## 何时用哪条命令

- 只要把 CI / 工作流结果发到飞书：用 `github notify`。
- 要根据评论、PR、Issue、push 或 release 做一次有模型的判断，并可能发评论、改标题、写检查、发飞书或写 job output：用 `github run`。
- 不在 GitHub 上、但要用同一套主循环跑完一条任务：用 `run`。

## GitHub 评论怎么唤醒

评论或 PR review 评论里要有完整词 `@lark-agent`。后面默认是自然语言。只有三个斜杠命令：`/review`、`/title`、`/check`。评论里唯一允许的开关是 `--dry-run`。

`@lark-agent review` 没有斜杠，按普通句子处理，不会当成 `/review`。

未知的 `/foo`：如果允许发评论，会回一条帮助后成功退出，不跑模型；不允许发评论则退出码 2。

## GitHub Actions

仓库根目录 `action.yml` 的 `mode` 默认是 `notify`，会继续走 `github notify`。`mode: run` 才走 `github run`。

示范工作流在 `.github/workflows/lark-agent-*.yml`，同样的文件也放在 `examples/github-agent/workflows/`。它们都检出仓库默认分支上的 Action 实现，不检出 PR 头，不下载产物，需要飞书或模型密钥的 job 使用 Environment `lark-production`。

模型密钥从进程环境读取 `OPENAI_API_KEY`（可选 `OPENAI_BASE_URL`、`OPENAI_MODEL`），不会去调 GitHub 的 secrets HTTP API。

## 写操作允许名单

`--allowed-actions` 只能出现这些名字：

`post_github_comment`、`upsert_github_check`、`update_github_issue_title`、`send_lark_message`、`write_job_output`。

`/review` 和 `/check` 在有 PR 号时会额外允许检查写入。`--dry-run` 会清空全部写入。检查名固定为 `lark-agent-gate`，不会调用 merge API。

## 结束方式

工作类型是 `smart_command`。模型必须调用 `submit_decision`，且 `decision` 只能是 `record`。这个工具本身不发飞书、不写 GitHub。真正的评论、检查、标题、飞书消息和 job output 只能通过上面的具名写入工具，每种进程最多成功一次。

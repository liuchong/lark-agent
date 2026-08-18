# GitHub 示范工作流

把智能命令接到其他仓库时，从这里复制 YAML 和 prompt，不要从 `changes/` 里拼。

| 目录 | 内容 |
| --- | --- |
| `workflows/` | GitHub Actions YAML |
| `lark-agent/prompts/` | `mode: run` 使用的提示词 |

本仓库自己也在跑同一套文件，位置是：

- `.github/workflows/lark-agent-*.yml`
- `.github/lark-agent/prompts/`

复制后需要改的是 Environment 变量、`prompt_file` 路径，以及是否仍用 `uses: ./`。行为、评论语法和允许写入的工具见 [智能命令与 GitHub](../../docs/smart-command.md)。

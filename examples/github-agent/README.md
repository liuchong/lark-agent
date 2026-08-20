# GitHub 示范工作流

把智能命令接到其他仓库时，从这里复制 YAML 和 prompt，不要从 `changes/` 里拼。

| 目录 | 内容 |
| --- | --- |
| `workflows/` | GitHub Actions YAML |
| `lark-agent/prompts/` | `mode: run` 使用的提示词 |

本仓库自己也在跑同一套文件，位置是：

- `.github/workflows/lark-agent-*.yml`
- `.github/lark-agent/prompts/`

## 复制后要改什么

1. Environment 名字和里面的 secret / variable，见 [智能命令与 GitHub](../../docs/smart-command.md)。
2. `prompt_file` 和 `rules_file` 的路径。
3. `uses: ./` 换成 `uses: liuchong/lark-agent@<ref>`，除非目标仓库自己就带 Action 实现。
4. `output_language`：写 GitHub 评论、检查结论或 release 正文的步骤保持 `en-US`，因为那是公开仓库内容；只发飞书的步骤删掉这个输入，沿用配置里的语言。
5. 触发事件的分配：发飞书的几个工作流故意不共用触发事件，一次仓库事件只产生一条模型写的消息。`lark-agent-event-summary.yml` 收新开的 Issue 和 PR，CI 完成由不跑模型的 `lark-notify.yml` 报一次，只有 CI 失败才由 `lark-agent-notify-style.yml` 额外解释一次。照抄多个工作流时，先确认它们的触发事件没有重叠。
6. 需要更强推理或更长单次超时时用 `model_reasoning_effort` 和 `model_timeout`；Actions 里没有配置文件，这是调整模型档案的唯一入口。

## 写提示词的边界

提示词只写这次要做什么：范围、什么时候跳过、发什么。不要在里面重复通用要求。结论优先、禁止内部编号和源码符号、专名照抄、飞书纯文本写裸 URL，这些由智能命令的系统提示统一保证，写进提示词只会让它变长且更容易和系统提示打架。

`notify-style.md` 是个可以照抄的结构：`Scope` 限定只描述触发事件本身，`Skip` 说明什么时候不发，`Send` 说明发什么。

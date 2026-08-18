# 文档地图

先分清三类材料，再打开具体文件：

| 层级 | 目录 | 用途 |
| --- | --- | --- |
| 怎么用 | `docs/`、根目录 `README.md` | 安装、配置、日常操作、GitHub 工作流 |
| 现行契约 | `spec/` | 合并后系统实际如何工作；测试按这里的场景验收 |
| 某次为什么改 | `changes/` | 历史设计说明。与 `spec/` 冲突时以规格为准 |

不要从 `changes/` 或 `.agents/experience/` 推断当前产品行为。

## 使用

1. [macOS 安装](install-macos.md)：本机配置、LaunchAgent、状态栏、GitHub Environment。
2. [配置说明](configuration.md)：`config.yaml` 字段、范围、Workspace、记忆、GitHub 只读桥。
3. [运行、恢复与故障处理](operations.md)：常驻助手的队列、审批、恢复、云文档监控、代码调查。
4. [智能命令与 GitHub](smart-command.md)：`run` / `github run` / `github notify`、评论语法、本仓库工作流。

根目录 [README](../README.md) 只保留产品入口和最短上手步骤。

## 开发

- [开发与验证](development.md)：目录职责、验证命令、Action 测试边界。
- [长期行为规格](../spec/behavior.md)
- [架构边界](../spec/architecture.md)
- [智能命令规格](../spec/smart-command.md)
- [Lark SDK 边界](../spec/lark-sdk-boundary.md)

把示范工作流拷到其他仓库时，看 [examples/github-agent](../examples/github-agent/README.md)。本仓库正在跑的副本在 `.github/workflows/lark-agent-*.yml`。

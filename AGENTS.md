# lark-agent 开发规则

本文件适用于仓库全部目录。每次开始任务、切换阶段、上下文恢复、开始实现、
进入 TDD green、验证、提交、推送、安装和部署前，都必须重新读取本文件、
`/workspace/AGENTS.md`、`/workspace/rules/bdd-tdd-development-flow.md`
以及 `.agents/00-entry/rule-trigger-index.md` 命中的规则。

## 硬边界

- 任何方案必须先经用户确认再实施。
- 所有行为变更先更新长期规格和 Given/When/Then 场景，再写失败测试，按
  red -> green -> refactor 实现。
- 每项逻辑变化必须有 `integration_test/` 覆盖；没有相应集成测试时不得宣称完成。
- `*.go`、`go.mod`、`go.sum` 不得引用 `github.com/larksuite/cli`，不得复制官方
  `internal/**`、`events/**`、`cmd/event/**`。
- 飞书生产调用必须统一经过 `internal/lark`，只能调用官方公开 Go SDK
  `github.com/larksuite/oapi-sdk-go/v3`；不得执行 `lark-cli` 子进程、解析 stdout/NDJSON
  作为生产协议，或读取官方 CLI 的内部配置。
- 命令 stdout 只输出结构化业务数据，进度、告警和提示输出到 stderr。
- 外部 JSON/NDJSON 必须在边界转换为有类型结构；未知字段形状或缺少关键字段时
  明确失败，禁止静默猜测。
- Workspace shell 必须由代码限制在配置的 Workspace 真实路径内；提示词不能代替
  路径、符号链接和子进程边界检查。
- secret、token、私钥和连接凭据不得进入仓库、测试 fixture、日志或提交。
- 公开仓库的代码、测试、fixture、文档、规则、示例、提交信息和全部 Git 历史必须
  遵守 `.agents/knowledge/public-repository-safety.md`：不得保留真实标识、人员、
  私有项目结构或具体业务场景，历史清理必须验证远端隐藏引用。
- 用户要求本地语音通知时，只能在提交、推送和远端验证成功后朗读本地提供的称呼；
  称呼和通知内容不得写入公开仓库。
- 新源码不增加版权或 SPDX 文件头；第三方归属统一记录在
  `THIRD_PARTY_NOTICES.md` 和 `LICENSES/`。

## 开发和交付门禁

- Go 代码提交前执行 `gofmt -w .`、`go test -race ./...`、`go vet ./...`、
  lint、`go mod tidy` 稳定性检查和独立依赖边界检查。
- 每次提交前展示完整 `git status`、staged/unstaged diff、未跟踪文件和测试证据。
- 使用英文 Conventional Commits，禁止跳过 hooks，禁止提交秘密或无关文件。
- 同时包含提交和安装时，必须先验证并提交，再从该提交构建和安装。
- Review 结论必须由主 Agent 根据代码、测试和运行证据逐条核验；只自动修复阻塞
  当前核心验收的问题，范围外改进交由用户决定。
- 每阶段结束判断是否应将可复用经验写入 `.agents/experience/` 或将重复易错操作
  写成 `.agents/tools/`；不得为形式完整无限扩项。
- 本机安装实例的非秘密配置建议备份到 `/.local/owner-config/`（已被 gitignore）。
  换上下文查找本地实例时先看这里；secret 仍只放 Keychain。

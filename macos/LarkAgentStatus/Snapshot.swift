import Foundation

struct CommandResult {
    let output: String
    let ok: Bool
}

struct Envelope {
    let ok: Bool
    let data: [String: Any]
    let errorMessage: String
}

struct ApprovalRow {
    let id: Int
    let kind: String
    let workItemID: Int
    let status: String
}

struct RecentRow {
    let workItemID: Int
    let kind: String
    let status: String
    let durationMS: Int
    let modelTurns: Int
}

struct StatusSnapshot {
    var running = false
    var installed = false
    var loaded = false
    var pid: Int?
    var lastError = ""
    var mode = ""
    var queueCounts: [String: Int] = [:]
    var laneCounts: [String: Int] = [:]
    var staleProcessing = 0
    var pendingApprovals = 0
    var approvals: [ApprovalRow] = []
    var taskRulesEnabled = false
    var taskRulesStatus = ""
    var taskRulesFile = ""
    var taskRulesBytes = 0
    var taskRulesDigest = ""
    var assistantScope = ""
    var ownerScope = ""
    var privateScope = ""
    var ownerWait = ""
    var githubEnabled = false
    var githubTokenConfigured = false
    var githubReadOnly = true
    var larkAppIDConfigured = false
    var larkUserToken = ""
    var larkRealtime = ""
    var workspaceConfigured = false
    var recent: [RecentRow] = []
    var sectionErrors: [String] = []
    var doctorLoaded = false
}

enum PanelAction {
    case start, stop, restart, pause, resumeAuto, refresh, openConfig, openLogs, quit
    case approve(Int)
    case reject(Int)
}

struct PanelCopy {
    let chinese: Bool

    init(locale: Locale = .current) {
        if #available(macOS 13.0, *) {
            chinese = locale.language.languageCode?.identifier == "zh"
        } else {
            chinese = locale.languageCode == "zh"
        }
    }

    var appName: String { "Lark Agent" }
    var service: String { chinese ? "服务" : "Service" }
    var queue: String { chinese ? "队列" : "Queue" }
    var approvals: String { chinese ? "待审批" : "Approvals" }
    var taskRules: String { chinese ? "任务规则" : "Task Rules" }
    var replyScopes: String { chinese ? "回复范围" : "Reply Scopes" }
    var github: String { "GitHub" }
    var recentWork: String { chinese ? "最近工作" : "Recent Work" }
    var diagnosis: String { chinese ? "诊断" : "Diagnosis" }
    var running: String { chinese ? "运行中" : "Running" }
    var stopped: String { chinese ? "已停止" : "Stopped" }
    var notInstalled: String { chinese ? "未安装" : "Not Installed" }
    var error: String { chinese ? "错误" : "Error" }
    var checking: String { chinese ? "检查中…" : "Checking…" }
    var loadingDiagnosis: String { chinese ? "正在加载诊断…" : "Loading diagnosis…" }
    var diagnosisUnavailable: String { chinese ? "诊断暂不可用" : "Diagnosis unavailable" }
    var none: String { chinese ? "无" : "None" }
    var enabled: String { chinese ? "已启用" : "Enabled" }
    var disabled: String { chinese ? "未启用" : "Disabled" }
    var configured: String { chinese ? "已配置" : "Configured" }
    var missing: String { chinese ? "未配置" : "Missing" }
    var yes: String { chinese ? "是" : "Yes" }
    var no: String { chinese ? "否" : "No" }
    var loaded: String { chinese ? "已加载" : "Loaded" }
    var pid: String { "PID" }
    var mode: String { chinese ? "模式" : "Mode" }
    var lastError: String { chinese ? "最近错误" : "Last error" }
    var stale: String { chinese ? "过期处理中" : "Stale processing" }
    var lanes: String { chinese ? "工作类型" : "Work kinds" }
    var history: String { chinese ? "历史" : "History" }
    var start: String { chinese ? "启动" : "Start" }
    var stop: String { chinese ? "停止" : "Stop" }
    var restart: String { chinese ? "重启" : "Restart" }
    var pause: String { chinese ? "暂停" : "Pause" }
    var auto: String { chinese ? "自动" : "Auto" }
    var refresh: String { chinese ? "刷新" : "Refresh" }
    var config: String { chinese ? "配置" : "Config" }
    var logs: String { chinese ? "日志" : "Logs" }
    var quit: String { chinese ? "退出" : "Quit" }
    var approve: String { chinese ? "批准" : "Approve" }
    var reject: String { chinese ? "拒绝" : "Reject" }
    var commandFailed: String { chinese ? "命令失败" : "Command failed" }
    var agentMissing: String { chinese ? "未找到已安装的 Agent" : "Installed agent is missing" }
    var updated: String { chinese ? "更新于" : "Updated" }
    var file: String { chinese ? "文件" : "File" }
    var digest: String { chinese ? "摘要" : "Digest" }
    var bytes: String { chinese ? "大小" : "Size" }
    var status: String { chinese ? "状态" : "Status" }
    var assistantMentions: String { chinese ? "助手提及" : "Assistant mentions" }
    var ownerMentions: String { chinese ? "Owner 提及" : "Owner mentions" }
    var privateMessages: String { chinese ? "私聊" : "Private messages" }
    var ownerWait: String { chinese ? "等待 Owner" : "Owner wait" }
    var token: String { chinese ? "令牌" : "Token" }
    var readOnly: String { chinese ? "只读" : "Read-only" }
    var appID: String { chinese ? "应用编号" : "App ID" }
    var userToken: String { chinese ? "用户令牌" : "User token" }
    var realtime: String { chinese ? "实时通道" : "Realtime" }
    var workspace: String { chinese ? "工作区" : "Workspace" }
    var turns: String { chinese ? "轮次" : "turns" }
}

func parseEnvelope(_ output: String) -> Envelope {
    let trimmed = output.trimmingCharacters(in: .whitespacesAndNewlines)
    guard let data = trimmed.data(using: .utf8),
          let raw = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
        return Envelope(ok: false, data: [:], errorMessage: trimmed)
    }
    let ok = jsonBool(raw["ok"])
    let payload = asDict(raw["data"])
    var message = jsonString(asDict(raw["error"])["message"])
    if message.isEmpty, !ok {
        message = jsonString(raw["error"])
    }
    if message.isEmpty, !ok {
        message = trimmed
    }
    return Envelope(ok: ok, data: payload, errorMessage: message)
}

func summarizeEnvelope(_ result: CommandResult, copy: PanelCopy) -> String {
    let envelope = parseEnvelope(result.output)
    if envelope.ok {
        if let action = envelope.data["action"] as? String {
            let nested = asDict(envelope.data["status"])
            if jsonBool(nested["running"]) {
                return "\(action) · \(copy.running)"
            }
            if jsonBool(nested["installed"]) {
                return "\(action) · \(copy.stopped)"
            }
            return action
        }
        if let mode = envelope.data["mode"] as? String, !mode.isEmpty {
            return "\(copy.mode) · \(mode)"
        }
        return copy.updated
    }
    if envelope.errorMessage.isEmpty {
        return result.ok ? copy.updated : copy.commandFailed
    }
    return envelope.errorMessage
}

func asDict(_ value: Any?) -> [String: Any] {
    value as? [String: Any] ?? [:]
}

func asArray(_ value: Any?) -> [[String: Any]] {
    if let rows = value as? [[String: Any]] {
        return rows
    }
    return []
}

func jsonBool(_ value: Any?) -> Bool {
    if let flag = value as? Bool {
        return flag
    }
    if let number = value as? NSNumber {
        return number.boolValue
    }
    if let text = value as? String {
        return text == "true" || text == "configured" || text == "yes"
    }
    return false
}

func jsonInt(_ value: Any?) -> Int {
    if let number = value as? NSNumber {
        return number.intValue
    }
    if let number = value as? Int {
        return number
    }
    if let text = value as? String, let number = Int(text) {
        return number
    }
    return 0
}

func jsonString(_ value: Any?) -> String {
    if let text = value as? String {
        return text
    }
    if let number = value as? NSNumber {
        return number.stringValue
    }
    return ""
}

func jsonIntMap(_ value: Any?) -> [String: Int] {
    guard let raw = value as? [String: Any] else {
        return [:]
    }
    var mapped: [String: Int] = [:]
    for (key, item) in raw {
        mapped[key] = jsonInt(item)
    }
    return mapped
}

func applyService(_ snapshot: inout StatusSnapshot, data: [String: Any]) {
    snapshot.running = jsonBool(data["running"])
    snapshot.installed = jsonBool(data["installed"])
    snapshot.loaded = jsonBool(data["loaded"])
    let pid = jsonInt(data["pid"])
    snapshot.pid = pid > 0 ? pid : nil
    snapshot.lastError = jsonString(data["last_error"])
}

func applyQueue(_ snapshot: inout StatusSnapshot, data: [String: Any]) {
    snapshot.queueCounts = jsonIntMap(data["status_counts"])
    snapshot.laneCounts = jsonIntMap(data["lane_counts"])
    snapshot.staleProcessing = jsonInt(data["stale_processing"])
    snapshot.recent = asArray(data["recent"]).prefix(8).map { row in
        RecentRow(
            workItemID: jsonInt(row["work_item_id"]),
            kind: jsonString(row["work_kind"]),
            status: jsonString(row["status"]),
            durationMS: jsonInt(row["duration_ms"]),
            modelTurns: jsonInt(row["model_turns"])
        )
    }
}

func applyApprovals(_ snapshot: inout StatusSnapshot, data: [String: Any]) {
    let counts = asDict(data["counts"])
    if !counts.isEmpty {
        snapshot.pendingApprovals = jsonInt(counts["awaiting_approval"])
    }
    if data["actions"] != nil {
        snapshot.approvals = asArray(data["actions"]).compactMap { row in
            let status = jsonString(row["status"])
            guard status == "awaiting_approval" else {
                return nil
            }
            return ApprovalRow(
                id: jsonInt(row["id"]),
                kind: jsonString(row["kind"]),
                workItemID: jsonInt(row["work_item_id"]),
                status: status
            )
        }
        if snapshot.pendingApprovals == 0 {
            snapshot.pendingApprovals = snapshot.approvals.count
        }
    }
}

func overlayCheap(_ displayed: inout StatusSnapshot, cheap: StatusSnapshot) {
    displayed.running = cheap.running
    displayed.installed = cheap.installed
    displayed.loaded = cheap.loaded
    displayed.pid = cheap.pid
    displayed.lastError = cheap.lastError
    displayed.queueCounts = cheap.queueCounts
    displayed.laneCounts = cheap.laneCounts
    displayed.staleProcessing = cheap.staleProcessing
    displayed.pendingApprovals = cheap.pendingApprovals
    displayed.approvals = cheap.approvals
    displayed.recent = cheap.recent
    displayed.sectionErrors = cheap.sectionErrors
    if !cheap.taskRulesStatus.isEmpty || cheap.taskRulesEnabled {
        displayed.taskRulesEnabled = cheap.taskRulesEnabled
        displayed.taskRulesStatus = cheap.taskRulesStatus
        displayed.taskRulesFile = cheap.taskRulesFile
        displayed.taskRulesBytes = cheap.taskRulesBytes
        displayed.taskRulesDigest = cheap.taskRulesDigest
    }
}

func applyTaskRules(_ snapshot: inout StatusSnapshot, data: [String: Any]) {
    let view = asDict(data["task_rules"]).isEmpty ? data : asDict(data["task_rules"])
    snapshot.taskRulesEnabled = jsonBool(view["enabled"])
    snapshot.taskRulesStatus = jsonString(view["status"])
    snapshot.taskRulesFile = jsonString(view["file_name"])
    snapshot.taskRulesBytes = jsonInt(view["bytes"])
    snapshot.taskRulesDigest = jsonString(view["digest"])
}

func applyDoctor(_ snapshot: inout StatusSnapshot, data: [String: Any]) {
    snapshot.doctorLoaded = true
    snapshot.mode = jsonString(data["mode"])
    applyQueue(&snapshot, data: asDict(data["queue"]))
    applyTaskRules(&snapshot, data: data)
    let scopes = asDict(data["reply_scopes"])
    snapshot.assistantScope = jsonString(scopes["assistant_mentions"])
    snapshot.ownerScope = jsonString(scopes["owner_mentions"])
    snapshot.privateScope = jsonString(scopes["private_messages"])
    let delegated = asDict(data["delegated_reply"])
    snapshot.ownerWait = jsonString(delegated["owner_wait"])
    let github = asDict(data["github"])
    snapshot.githubEnabled = jsonBool(github["enabled"])
    snapshot.githubTokenConfigured = jsonBool(github["token_configured"])
    snapshot.githubReadOnly = github["read_only"] == nil ? true : jsonBool(github["read_only"])
    let lark = asDict(data["lark"])
    snapshot.larkAppIDConfigured = jsonBool(lark["app_id_configured"])
    snapshot.larkUserToken = jsonString(lark["user_token"])
    snapshot.larkRealtime = jsonString(lark["realtime"])
    let workspace = asDict(data["workspace"])
    snapshot.workspaceConfigured = !jsonString(workspace["configured_root"]).isEmpty
}

func queueStatusTitle(_ key: String, copy: PanelCopy) -> String {
    switch key {
    case "processing":
        return copy.chinese ? "处理中" : "processing"
    case "interrupted":
        return copy.chinese ? "已中断" : "interrupted"
    case "dead_letter":
        return copy.chinese ? "死信" : "dead letter"
    case "awaiting_approval":
        return copy.chinese ? "待审批" : "awaiting approval"
    case "completed":
        return copy.chinese ? "已完成" : "completed"
    case "ignored":
        return copy.chinese ? "已忽略" : "ignored"
    case "cancelled":
        return copy.chinese ? "已取消" : "cancelled"
    case "received":
        return copy.chinese ? "已接收" : "received"
    case "ready":
        return copy.chinese ? "就绪" : "ready"
    default:
        return key.replacingOccurrences(of: "_", with: " ")
    }
}

func truncatedDigest(_ digest: String) -> String {
    guard digest.count > 19 else {
        return digest
    }
    let prefix = digest.prefix(16)
    return "\(prefix)…"
}

func formatDuration(_ milliseconds: Int) -> String {
    if milliseconds < 1000 {
        return "\(milliseconds)ms"
    }
    let seconds = Double(milliseconds) / 1000
    if seconds < 10 {
        return String(format: "%.1fs", seconds)
    }
    return String(format: "%.0fs", seconds)
}

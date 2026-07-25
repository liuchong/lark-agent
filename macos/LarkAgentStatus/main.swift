import AppKit
import Foundation

struct CommandResult {
    let output: String
    let ok: Bool
}

final class AgentMenuApp: NSObject, NSApplicationDelegate {
    private let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    private let menu = NSMenu()
    private let home = FileManager.default.homeDirectoryForCurrentUser.path
    private lazy var agentPath = "\(home)/Library/Application Support/lark-agent/bin/lark-agent-daemon"
    private lazy var config = loadInstallConfig()

    override init() {
        super.init()
        statusItem.button?.title = "LA"
        statusItem.button?.toolTip = "Lark Agent"
        statusItem.menu = menu
        rebuildMenu()
        Timer.scheduledTimer(withTimeInterval: 10, repeats: true) { [weak self] _ in
            self?.refreshStatus()
        }
        refreshStatus()
    }

    private func rebuildMenu(status: String = "Checking...") {
        menu.removeAllItems()
        menu.addItem(withTitle: "Lark Agent: \(status)", action: #selector(showStatus), keyEquivalent: "")
        menu.addItem(NSMenuItem.separator())
        menu.addItem(withTitle: "Start", action: #selector(startAgent), keyEquivalent: "")
        menu.addItem(withTitle: "Stop", action: #selector(stopAgent), keyEquivalent: "")
        menu.addItem(withTitle: "Restart", action: #selector(restartAgent), keyEquivalent: "")
        menu.addItem(NSMenuItem.separator())
        menu.addItem(withTitle: "Pause", action: #selector(pauseAgent), keyEquivalent: "")
        menu.addItem(withTitle: "Resume Auto", action: #selector(resumeAgent), keyEquivalent: "")
        menu.addItem(withTitle: "Review Approvals", action: #selector(reviewApprovals), keyEquivalent: "")
        menu.addItem(withTitle: "Inspect Interrupted Work", action: #selector(inspectInterruptedWork), keyEquivalent: "")
        menu.addItem(NSMenuItem.separator())
        menu.addItem(withTitle: "Open Config", action: #selector(openConfig), keyEquivalent: "")
        menu.addItem(withTitle: "Open Logs", action: #selector(openLogs), keyEquivalent: "")
        menu.addItem(NSMenuItem.separator())
        menu.addItem(withTitle: "Quit Menu App", action: #selector(quit), keyEquivalent: "q")
    }

    private func refreshStatus() {
        let result = runAgent(["daemon", "status"])
        let pending = pendingApprovalCount()
        let interrupted = interruptedWorkCount()
        let status: String
        if result.output.contains("\"running\":true") {
            let details = statusDetails(pending: pending, interrupted: interrupted)
            status = details.isEmpty ? "Running" : "Running · \(details)"
            statusItem.button?.title = interrupted > 0 ? "LA ⏸\(interrupted)" : (pending > 0 ? "LA \(pending)" : "LA ●")
        } else if result.output.contains("\"installed\":true") {
            let details = statusDetails(pending: pending, interrupted: interrupted)
            status = details.isEmpty ? "Stopped" : "Stopped · \(details)"
            statusItem.button?.title = interrupted > 0 ? "LA ⏸\(interrupted)" : (pending > 0 ? "LA \(pending)" : "LA ○")
        } else {
            status = result.ok ? "Not Installed" : "Error"
            statusItem.button?.title = "LA !"
        }
        rebuildMenu(status: status)
    }

    @objc private func showStatus() {
        show(runAgent(["daemon", "status"]).output)
        refreshStatus()
    }

    @objc private func startAgent() {
        show(runAgent(["daemon", "start"]).output)
        refreshStatus()
    }

    @objc private func stopAgent() {
        show(runAgent(["daemon", "stop"]).output)
        refreshStatus()
    }

    @objc private func restartAgent() {
        show(runAgent(["daemon", "restart"]).output)
        refreshStatus()
    }

    @objc private func pauseAgent() {
        show(runAgent(["mode", "paused"]).output)
        refreshStatus()
    }

    @objc private func resumeAgent() {
        show(runAgent(["mode", "auto"]).output)
        refreshStatus()
    }

    @objc private func reviewApprovals() {
        let result = runAgent(["approval", "list"])
        guard result.ok,
              let data = result.output.data(using: .utf8),
              let envelope = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let payload = envelope["data"] as? [String: Any],
              let actions = payload["actions"] as? [[String: Any]],
              let action = actions.first(where: { ($0["status"] as? String) == "awaiting_approval" }),
              let id = action["id"] as? NSNumber else {
            show(result.ok ? "No pending approvals." : result.output)
            refreshStatus()
            return
        }
        let alert = NSAlert()
        alert.messageText = "Pending Lark Agent Action #\(id)"
        alert.informativeText = action["request_json"] as? String ?? String(describing: action)
        alert.addButton(withTitle: "Approve Exact Action")
        alert.addButton(withTitle: "Reject")
        alert.addButton(withTitle: "Cancel")
        let response = alert.runModal()
        if response == .alertFirstButtonReturn {
            show(runAgent(["approval", "approve", id.stringValue]).output)
        } else if response == .alertSecondButtonReturn {
            show(runAgent(["approval", "reject", id.stringValue]).output)
        }
        refreshStatus()
    }

    @objc private func inspectInterruptedWork() {
        show(runAgent(["queue", "list"]).output)
        refreshStatus()
    }

    @objc private func openConfig() {
        NSWorkspace.shared.open(URL(fileURLWithPath: config.configPath))
    }

    @objc private func openLogs() {
        NSWorkspace.shared.open(
            URL(fileURLWithPath: "\(home)/Library/Logs/lark-agent", isDirectory: true)
        )
    }

    @objc private func quit() {
        NSApp.terminate(nil)
    }

    private func runAgent(_ args: [String]) -> CommandResult {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: agentPath)
        process.arguments = ["--config", config.configPath, "--state", config.statePath] + args
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        do {
            try process.run()
            process.waitUntilExit()
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            return CommandResult(
                output: String(data: data, encoding: .utf8) ?? "",
                ok: process.terminationStatus == 0
            )
        } catch {
            return CommandResult(output: error.localizedDescription, ok: false)
        }
    }

    private func show(_ message: String) {
        let alert = NSAlert()
        alert.messageText = "Lark Agent"
        alert.informativeText = message.isEmpty ? "(no output)" : message
        alert.addButton(withTitle: "OK")
        alert.runModal()
    }

    private func pendingApprovalCount() -> Int {
        let result = runAgent(["approval", "status"])
        guard result.ok,
              let data = result.output.data(using: .utf8),
              let envelope = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let payload = envelope["data"] as? [String: Any],
              let counts = payload["counts"] as? [String: Any],
              let count = counts["awaiting_approval"] as? NSNumber else {
            return 0
        }
        return count.intValue
    }

    private func interruptedWorkCount() -> Int {
        let result = runAgent(["queue", "summary"])
        guard result.ok,
              let data = result.output.data(using: .utf8),
              let envelope = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let payload = envelope["data"] as? [String: Any],
              let counts = payload["status_counts"] as? [String: Any],
              let count = counts["interrupted"] as? NSNumber else {
            return 0
        }
        return count.intValue
    }

    private func statusDetails(pending: Int, interrupted: Int) -> String {
        var parts: [String] = []
        if pending > 0 {
            parts.append("\(pending) approval(s)")
        }
        if interrupted > 0 {
            parts.append("\(interrupted) interrupted")
        }
        return parts.joined(separator: " · ")
    }

    private func loadInstallConfig() -> (configPath: String, statePath: String) {
        let path = "\(home)/Library/Application Support/lark-agent/agent.conf"
        let defaultConfig = "\(home)/.config/lark-agent/config.yaml"
        let defaultState = "\(home)/Library/Application Support/lark-agent/state.db"
        guard let content = try? String(contentsOfFile: path, encoding: .utf8) else {
            return (defaultConfig, defaultState)
        }
        var configPath = defaultConfig
        var statePath = defaultState
        for line in content.split(separator: "\n") {
            let parts = line.split(separator: "=", maxSplits: 1).map(String.init)
            if parts.count == 2 && parts[0] == "CONFIG_PATH" {
                configPath = parts[1]
            }
            if parts.count == 2 && parts[0] == "STATE_PATH" {
                statePath = parts[1]
            }
        }
        return (configPath, statePath)
    }
}

let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let delegate = AgentMenuApp()
app.delegate = delegate
app.run()

import AppKit
import Foundation

final class AgentMenuApp: NSObject, NSApplicationDelegate {
    private let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    private let menu = NSMenu()
    private let popover = NSPopover()
    private let panel = StatusPanelController()
    private let copy = PanelCopy()
    private let home = FileManager.default.homeDirectoryForCurrentUser.path
    private lazy var agentPath = "\(home)/Library/Application Support/lark-agent/bin/lark-agent-daemon"
    private lazy var config = loadInstallConfig()
    private var bannerText = ""
    private var cheapSnapshot = StatusSnapshot()
    private var displayedSnapshot = StatusSnapshot()
    private var detailLoadInFlight = false
    private var detailLoadGeneration = 0

    override init() {
        super.init()
        panel.copy = copy
        panel.onAction = { [weak self] action in
            self?.handlePanelAction(action)
        }
        popover.contentViewController = panel
        popover.behavior = .transient
        popover.animates = true
        popover.contentSize = NSSize(width: 400, height: 640)

        configureStatusIcon()
        statusItem.button?.toolTip = copy.appName
        statusItem.button?.target = self
        statusItem.button?.action = #selector(handleStatusItemClick(_:))
        statusItem.button?.sendAction(on: [.leftMouseUp, .rightMouseUp])
        rebuildMenu()
        Timer.scheduledTimer(withTimeInterval: 10, repeats: true) { [weak self] _ in
            self?.refreshStatus()
        }
        refreshStatus()
    }

    private func configureStatusIcon() {
        guard let iconURL = Bundle.main.url(
            forResource: "StatusIconTemplate",
            withExtension: "png"
        ), let image = NSImage(contentsOf: iconURL) else {
            statusItem.button?.title = "LA"
            return
        }
        image.isTemplate = true
        image.size = NSSize(width: 18, height: 18)
        statusItem.button?.image = image
        statusItem.button?.imagePosition = .imageOnly
        statusItem.button?.title = ""
        statusItem.button?.setAccessibilityLabel(copy.appName)
    }

    private func setStatusIndicator(_ suffix: String) {
        guard let button = statusItem.button else {
            return
        }
        if button.image == nil {
            button.title = suffix.isEmpty ? "LA" : "LA \(suffix)"
            return
        }
        button.imagePosition = suffix.isEmpty ? .imageOnly : .imageLeading
        button.title = suffix
    }

    private func rebuildMenu(status: String = "") {
        let title = status.isEmpty ? copy.checking : "\(copy.appName): \(status)"
        menu.removeAllItems()
        menu.addItem(withTitle: title, action: #selector(openPopoverFromMenu), keyEquivalent: "")
        menu.addItem(NSMenuItem.separator())
        menu.addItem(withTitle: copy.start, action: #selector(startAgent), keyEquivalent: "")
        menu.addItem(withTitle: copy.stop, action: #selector(stopAgent), keyEquivalent: "")
        menu.addItem(withTitle: copy.restart, action: #selector(restartAgent), keyEquivalent: "")
        menu.addItem(NSMenuItem.separator())
        menu.addItem(withTitle: copy.pause, action: #selector(pauseAgent), keyEquivalent: "")
        menu.addItem(withTitle: copy.auto, action: #selector(resumeAgent), keyEquivalent: "")
        menu.addItem(NSMenuItem.separator())
        menu.addItem(withTitle: copy.config, action: #selector(openConfig), keyEquivalent: "")
        menu.addItem(withTitle: copy.logs, action: #selector(openLogs), keyEquivalent: "")
        menu.addItem(NSMenuItem.separator())
        menu.addItem(withTitle: copy.quit, action: #selector(quit), keyEquivalent: "q")
    }

    private func refreshStatus() {
        cheapSnapshot = collectCheapSnapshot()
        let pending = cheapSnapshot.pendingApprovals
        let interrupted = cheapSnapshot.queueCounts["interrupted"] ?? 0
        let status: String
        if cheapSnapshot.running {
            let details = statusDetails(pending: pending, interrupted: interrupted)
            status = details.isEmpty ? copy.running : "\(copy.running) · \(details)"
            setStatusIndicator(interrupted > 0 ? "⏸\(interrupted)" : (pending > 0 ? "\(pending)" : ""))
        } else if cheapSnapshot.installed {
            let details = statusDetails(pending: pending, interrupted: interrupted)
            status = details.isEmpty ? copy.stopped : "\(copy.stopped) · \(details)"
            setStatusIndicator(interrupted > 0 ? "⏸\(interrupted)" : (pending > 0 ? "\(pending)" : "○"))
        } else if !cheapSnapshot.sectionErrors.isEmpty {
            status = copy.error
            setStatusIndicator("!")
        } else {
            status = copy.notInstalled
            setStatusIndicator("!")
        }
        rebuildMenu(status: status)
        overlayCheap(&displayedSnapshot, cheap: cheapSnapshot)
        if popover.isShown {
            panel.render(displayedSnapshot, banner: bannerText, doctorLoading: detailLoadInFlight)
        }
    }

    @objc private func handleStatusItemClick(_ sender: NSStatusBarButton) {
        guard let event = NSApp.currentEvent, event.type == .rightMouseUp else {
            togglePopover()
            return
        }
        statusItem.menu = menu
        statusItem.button?.performClick(nil)
        DispatchQueue.main.async { [weak self] in
            self?.statusItem.menu = nil
        }
    }

    @objc private func openPopoverFromMenu() {
        togglePopover(forceOpen: true)
    }

    private func togglePopover(forceOpen: Bool = false) {
        if popover.isShown && !forceOpen {
            popover.performClose(nil)
            return
        }
        guard let button = statusItem.button else {
            return
        }
        if !popover.isShown {
            refreshStatus()
            panel.render(displayedSnapshot, banner: bannerText, doctorLoading: true)
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
            NSApp.activate(ignoringOtherApps: true)
            loadDetailedSnapshot()
        }
    }

    private func loadDetailedSnapshot() {
        detailLoadInFlight = true
        detailLoadGeneration += 1
        let generation = detailLoadGeneration
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            var snapshot = self.cheapSnapshot
            snapshot = self.collectPanelSnapshot(base: snapshot)
            let doctor = self.runAgent(["doctor"])
            let envelope = parseEnvelope(doctor.output)
            if envelope.ok {
                applyDoctor(&snapshot, data: envelope.data)
            } else {
                snapshot.sectionErrors.append(envelope.errorMessage.isEmpty ? self.copy.commandFailed : envelope.errorMessage)
            }
            DispatchQueue.main.async {
                guard generation == self.detailLoadGeneration else { return }
                self.detailLoadInFlight = false
                self.displayedSnapshot = snapshot
                overlayCheap(&self.displayedSnapshot, cheap: self.cheapSnapshot)
                self.panel.render(self.displayedSnapshot, banner: self.bannerText, doctorLoading: false)
            }
        }
    }

    private func collectCheapSnapshot() -> StatusSnapshot {
        var snapshot = StatusSnapshot()
        if !FileManager.default.isExecutableFile(atPath: agentPath) {
            snapshot.sectionErrors.append(copy.agentMissing)
            return snapshot
        }
        let service = runAgent(["daemon", "status"])
        let serviceEnvelope = parseEnvelope(service.output)
        if serviceEnvelope.ok {
            applyService(&snapshot, data: serviceEnvelope.data)
        } else {
            snapshot.sectionErrors.append(serviceEnvelope.errorMessage)
        }
        let queue = runAgent(["queue", "summary"])
        let queueEnvelope = parseEnvelope(queue.output)
        if queueEnvelope.ok {
            applyQueue(&snapshot, data: queueEnvelope.data)
        } else {
            snapshot.sectionErrors.append(queueEnvelope.errorMessage)
        }
        let approvalStatus = runAgent(["approval", "status"])
        let approvalEnvelope = parseEnvelope(approvalStatus.output)
        if approvalEnvelope.ok {
            applyApprovals(&snapshot, data: approvalEnvelope.data)
        }
        return snapshot
    }

    private func collectPanelSnapshot(base: StatusSnapshot) -> StatusSnapshot {
        var snapshot = base
        let rules = runAgent(["rules", "check"])
        let rulesEnvelope = parseEnvelope(rules.output)
        if rulesEnvelope.ok {
            applyTaskRules(&snapshot, data: rulesEnvelope.data)
        }
        return snapshot
    }

    private func handlePanelAction(_ action: PanelAction) {
        switch action {
        case .start:
            present(runAgent(["daemon", "start"]))
        case .stop:
            present(runAgent(["daemon", "stop"]))
        case .restart:
            present(runAgent(["daemon", "restart"]))
        case .pause:
            present(runAgent(["mode", "paused"]))
        case .resumeAuto:
            present(runAgent(["mode", "auto"]))
        case .refresh:
            bannerText = ""
            refreshStatus()
            if popover.isShown {
                panel.render(displayedSnapshot, banner: bannerText, doctorLoading: true)
                loadDetailedSnapshot()
            }
        case .openConfig:
            openConfig()
        case .openLogs:
            openLogs()
        case .quit:
            quit()
        case .approve(let id):
            present(runAgent(["approval", "approve", "\(id)"]))
        case .reject(let id):
            present(runAgent(["approval", "reject", "\(id)"]))
        }
    }

    @objc private func startAgent() {
        present(runAgent(["daemon", "start"]))
    }

    @objc private func stopAgent() {
        present(runAgent(["daemon", "stop"]))
    }

    @objc private func restartAgent() {
        present(runAgent(["daemon", "restart"]))
    }

    @objc private func pauseAgent() {
        present(runAgent(["mode", "paused"]))
    }

    @objc private func resumeAgent() {
        present(runAgent(["mode", "auto"]))
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

    private func present(_ result: CommandResult) {
        bannerText = summarizeEnvelope(result, copy: copy)
        refreshStatus()
        if !popover.isShown {
            let alert = NSAlert()
            alert.messageText = copy.appName
            alert.informativeText = bannerText
            alert.addButton(withTitle: "OK")
            alert.runModal()
        }
    }

    private func runAgent(_ args: [String]) -> CommandResult {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: agentPath)
        process.arguments = ["--config", config.configPath, "--state", config.statePath] + args
        let stdout = Pipe()
        process.standardOutput = stdout
        process.standardError = FileHandle.nullDevice
        do {
            try process.run()
            let data = stdout.fileHandleForReading.readDataToEndOfFile()
            process.waitUntilExit()
            return CommandResult(
                output: String(data: data, encoding: .utf8) ?? "",
                ok: process.terminationStatus == 0
            )
        } catch {
            return CommandResult(output: error.localizedDescription, ok: false)
        }
    }

    private func statusDetails(pending: Int, interrupted: Int) -> String {
        var parts: [String] = []
        if pending > 0 {
            parts.append("\(pending) \(copy.approvals.lowercased())")
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

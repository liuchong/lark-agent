import AppKit

final class StatusPanelController: NSViewController {
    var copy = PanelCopy()
    var onAction: ((PanelAction) -> Void)?

    private let effect = NSVisualEffectView()
    private let scroll = NSScrollView()
    private let documentView = FlippedView()
    private let stack = NSStackView()
    private let banner = NSTextField(labelWithString: "")
    private let spinner = NSProgressIndicator()
    private let footer = NSStackView()
    private var snapshot = StatusSnapshot()
    private var bannerText = ""

    override func loadView() {
        effect.material = .popover
        effect.blendingMode = .behindWindow
        effect.state = .active
        effect.wantsLayer = true
        view = effect
        view.frame = NSRect(x: 0, y: 0, width: 400, height: 640)

        scroll.drawsBackground = false
        scroll.hasVerticalScroller = true
        scroll.hasHorizontalScroller = false
        scroll.autohidesScrollers = true
        scroll.borderType = .noBorder
        scroll.translatesAutoresizingMaskIntoConstraints = false
        scroll.documentView = documentView

        documentView.translatesAutoresizingMaskIntoConstraints = false
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 12
        stack.translatesAutoresizingMaskIntoConstraints = false
        documentView.addSubview(stack)

        banner.font = NSFont.systemFont(ofSize: 11, weight: .medium)
        banner.textColor = .secondaryLabelColor
        banner.lineBreakMode = .byWordWrapping
        banner.maximumNumberOfLines = 3
        banner.isHidden = true

        spinner.style = .spinning
        spinner.controlSize = .small
        spinner.isDisplayedWhenStopped = false

        footer.orientation = .vertical
        footer.alignment = .leading
        footer.spacing = 6
        footer.translatesAutoresizingMaskIntoConstraints = false

        view.addSubview(scroll)
        view.addSubview(footer)
        NSLayoutConstraint.activate([
            scroll.topAnchor.constraint(equalTo: view.topAnchor, constant: 14),
            scroll.leadingAnchor.constraint(equalTo: view.leadingAnchor, constant: 14),
            scroll.trailingAnchor.constraint(equalTo: view.trailingAnchor, constant: -8),
            scroll.bottomAnchor.constraint(equalTo: footer.topAnchor, constant: -10),
            footer.leadingAnchor.constraint(equalTo: view.leadingAnchor, constant: 14),
            footer.trailingAnchor.constraint(equalTo: view.trailingAnchor, constant: -14),
            footer.bottomAnchor.constraint(equalTo: view.bottomAnchor, constant: -14),
            documentView.widthAnchor.constraint(equalTo: scroll.contentView.widthAnchor),
            stack.topAnchor.constraint(equalTo: documentView.topAnchor),
            stack.leadingAnchor.constraint(equalTo: documentView.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: documentView.trailingAnchor, constant: -8),
            stack.bottomAnchor.constraint(equalTo: documentView.bottomAnchor, constant: -8),
            stack.widthAnchor.constraint(equalTo: documentView.widthAnchor, constant: -8),
        ])
        rebuildFooter()
    }

    func render(_ snapshot: StatusSnapshot, banner: String, doctorLoading: Bool) {
        self.snapshot = snapshot
        bannerText = banner
        if spinner.superview == nil {
            _ = view
        }
        if doctorLoading {
            spinner.startAnimation(nil)
        } else {
            spinner.stopAnimation(nil)
        }
        rebuildContent()
    }

    private func rebuildContent() {
        stack.arrangedSubviews.forEach { subview in
            stack.removeArrangedSubview(subview)
            subview.removeFromSuperview()
        }
        stack.addArrangedSubview(headerView())
        if !bannerText.isEmpty {
            banner.stringValue = bannerText
            banner.isHidden = false
            banner.preferredMaxLayoutWidth = 360
            stack.addArrangedSubview(banner)
        } else {
            banner.isHidden = true
        }
        snapshot.sectionErrors.forEach { message in
            stack.addArrangedSubview(errorLine(message))
        }
        stack.addArrangedSubview(card(title: copy.service, views: serviceRows()))
        stack.addArrangedSubview(card(title: copy.queue, views: queueViews()))
        stack.addArrangedSubview(card(title: copy.approvals, views: approvalViews()))
        stack.addArrangedSubview(card(title: copy.taskRules, views: taskRuleRows()))
        stack.addArrangedSubview(card(title: copy.replyScopes, views: scopeRows()))
        stack.addArrangedSubview(card(title: copy.github, views: githubRows()))
        stack.addArrangedSubview(card(title: copy.diagnosis, views: diagnosisRows()))
        stack.addArrangedSubview(card(title: copy.recentWork, views: recentViews()))
        view.layoutSubtreeIfNeeded()
    }

    private func rebuildFooter() {
        footer.arrangedSubviews.forEach { subview in
            footer.removeArrangedSubview(subview)
            subview.removeFromSuperview()
        }
        footer.addArrangedSubview(buttonRow([
            (copy.start, PanelAction.start),
            (copy.stop, PanelAction.stop),
            (copy.restart, PanelAction.restart),
        ]))
        footer.addArrangedSubview(buttonRow([
            (copy.pause, PanelAction.pause),
            (copy.auto, PanelAction.resumeAuto),
            (copy.refresh, PanelAction.refresh),
        ]))
        footer.addArrangedSubview(buttonRow([
            (copy.config, PanelAction.openConfig),
            (copy.logs, PanelAction.openLogs),
            (copy.quit, PanelAction.quit),
        ]))
    }

    private func headerView() -> NSView {
        let title = NSTextField(labelWithString: copy.appName)
        title.font = NSFont.systemFont(ofSize: 13, weight: .semibold)
        title.textColor = .secondaryLabelColor

        let status = NSTextField(labelWithString: headline())
        status.font = NSFont.systemFont(ofSize: 22, weight: .semibold)
        status.textColor = headlineColor()

        let detail = NSTextField(labelWithString: headerDetail())
        detail.font = NSFont.systemFont(ofSize: 12)
        detail.textColor = .secondaryLabelColor

        let dot = NSView(frame: NSRect(x: 0, y: 0, width: 10, height: 10))
        dot.wantsLayer = true
        dot.layer?.cornerRadius = 5
        dot.layer?.backgroundColor = headlineColor().cgColor
        dot.translatesAutoresizingMaskIntoConstraints = false
        NSLayoutConstraint.activate([
            dot.widthAnchor.constraint(equalToConstant: 10),
            dot.heightAnchor.constraint(equalToConstant: 10),
        ])

        let statusRow = NSStackView(views: [dot, status, spinner])
        statusRow.orientation = .horizontal
        statusRow.alignment = .centerY
        statusRow.spacing = 8

        let header = NSStackView(views: [title, statusRow, detail])
        header.orientation = .vertical
        header.alignment = .leading
        header.spacing = 2
        return header
    }

    private func headline() -> String {
        if snapshot.running {
            return copy.running
        }
        if snapshot.installed {
            return copy.stopped
        }
        if snapshot.sectionErrors.isEmpty {
            return copy.notInstalled
        }
        return copy.error
    }

    private func headlineColor() -> NSColor {
        if snapshot.running {
            return .systemGreen
        }
        if !snapshot.lastError.isEmpty || !snapshot.sectionErrors.isEmpty {
            return .systemRed
        }
        if snapshot.installed {
            return .systemOrange
        }
        return .systemGray
    }

    private func headerDetail() -> String {
        var parts: [String] = []
        if !snapshot.mode.isEmpty {
            parts.append(snapshot.mode)
        }
        if let pid = snapshot.pid {
            parts.append("pid \(pid)")
        }
        if snapshot.pendingApprovals > 0 {
            parts.append("\(snapshot.pendingApprovals) \(copy.approvals.lowercased())")
        }
        let interrupted = snapshot.queueCounts["interrupted"] ?? 0
        if interrupted > 0 {
            parts.append("\(interrupted) interrupted")
        }
        return parts.isEmpty ? copy.checking : parts.joined(separator: " · ")
    }

    private func serviceRows() -> [NSView] {
        var rows: [NSView] = [
            labeled(copy.status, headline()),
            labeled(copy.loaded, snapshot.loaded ? copy.yes : copy.no),
            labeled(copy.pid, snapshot.pid.map(String.init) ?? "—"),
            labeled(copy.mode, snapshot.mode.isEmpty ? "—" : snapshot.mode),
        ]
        if !snapshot.lastError.isEmpty {
            rows.append(labeled(copy.lastError, snapshot.lastError, valueColor: .systemRed))
        }
        return rows
    }

    private func queueViews() -> [NSView] {
        let keys = [
            "ready", "processing", "interrupted", "dead_letter",
            "awaiting_approval", "completed", "ignored", "cancelled",
        ]
        var tiles: [NSView] = keys.compactMap { key in
            let count = snapshot.queueCounts[key] ?? 0
            if count == 0 && key != "processing" && key != "interrupted" && key != "dead_letter" {
                return nil
            }
            return metricTile(title: key.replacingOccurrences(of: "_", with: " "), value: "\(count)", emphasize: count > 0 && (key == "interrupted" || key == "dead_letter"))
        }
        if snapshot.staleProcessing > 0 {
            tiles.append(metricTile(title: copy.stale, value: "\(snapshot.staleProcessing)", emphasize: true))
        }
        if tiles.isEmpty {
            return [muted(copy.none)]
        }
        var views: [NSView] = [wrapTiles(tiles)]
        let lanes = snapshot.laneCounts.filter { $0.value > 0 }.sorted { $0.key < $1.key }
        if !lanes.isEmpty {
            let text = lanes.map { "\($0.key) \($0.value)" }.joined(separator: " · ")
            views.append(labeled(copy.lanes, text))
        }
        return views
    }

    private func approvalViews() -> [NSView] {
        if snapshot.approvals.isEmpty {
            let count = snapshot.pendingApprovals
            return [muted(count == 0 ? copy.none : "\(count)")]
        }
        return snapshot.approvals.prefix(5).map { row in
            approvalRow(row)
        }
    }

    private func approvalRow(_ row: ApprovalRow) -> NSView {
        let label = NSTextField(labelWithString: "#\(row.id)  \(row.kind)  work \(row.workItemID)")
        label.font = NSFont.monospacedDigitSystemFont(ofSize: 12, weight: .regular)
        label.lineBreakMode = .byTruncatingTail
        let approve = smallButton(copy.approve, tag: row.id, action: #selector(approveClicked(_:)))
        let reject = smallButton(copy.reject, tag: row.id, action: #selector(rejectClicked(_:)))
        let buttons = NSStackView(views: [approve, reject])
        buttons.orientation = .horizontal
        buttons.spacing = 4
        let line = NSStackView(views: [label, buttons])
        line.orientation = .horizontal
        line.alignment = .centerY
        line.distribution = .fill
        label.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        return line
    }

    private func taskRuleRows() -> [NSView] {
        if !snapshot.doctorLoaded && snapshot.taskRulesStatus.isEmpty {
            return [muted(copy.loadingDiagnosis)]
        }
        var rows = [
            labeled(copy.status, snapshot.taskRulesEnabled ? "\(copy.enabled) · \(snapshot.taskRulesStatus)" : copy.disabled),
        ]
        if !snapshot.taskRulesFile.isEmpty {
            rows.append(labeled(copy.file, snapshot.taskRulesFile))
        }
        if snapshot.taskRulesBytes > 0 {
            rows.append(labeled(copy.bytes, "\(snapshot.taskRulesBytes) B"))
        }
        if !snapshot.taskRulesDigest.isEmpty {
            rows.append(labeled(copy.digest, truncatedDigest(snapshot.taskRulesDigest)))
        }
        return rows
    }

    private func scopeRows() -> [NSView] {
        if !snapshot.doctorLoaded {
            return [muted(copy.loadingDiagnosis)]
        }
        return [
            labeled(copy.assistantMentions, blank(snapshot.assistantScope)),
            labeled(copy.ownerMentions, blank(snapshot.ownerScope)),
            labeled(copy.privateMessages, blank(snapshot.privateScope)),
            labeled(copy.ownerWait, blank(snapshot.ownerWait)),
        ]
    }

    private func githubRows() -> [NSView] {
        if !snapshot.doctorLoaded {
            return [muted(copy.loadingDiagnosis)]
        }
        return [
            labeled(copy.enabled, snapshot.githubEnabled ? copy.yes : copy.no),
            labeled(copy.token, snapshot.githubTokenConfigured ? copy.configured : copy.missing),
            labeled(copy.readOnly, snapshot.githubReadOnly ? copy.yes : copy.no),
        ]
    }

    private func diagnosisRows() -> [NSView] {
        if !snapshot.doctorLoaded {
            return [muted(copy.loadingDiagnosis)]
        }
        return [
            labeled(copy.appID, snapshot.larkAppIDConfigured ? copy.configured : copy.missing),
            labeled(copy.userToken, blank(snapshot.larkUserToken)),
            labeled(copy.realtime, blank(snapshot.larkRealtime)),
            labeled(copy.workspace, snapshot.workspaceConfigured ? copy.configured : copy.missing),
        ]
    }

    private func recentViews() -> [NSView] {
        if snapshot.recent.isEmpty {
            return [muted(copy.none)]
        }
        return snapshot.recent.map { row in
            let parts = [
                "#\(row.workItemID)",
                row.kind.replacingOccurrences(of: "_", with: " "),
                row.status,
                formatDuration(row.durationMS),
            ]
            var text = parts.filter { !$0.isEmpty && $0 != "#" }.joined(separator: "  ·  ")
            if row.modelTurns > 0 {
                text += "  ·  \(row.modelTurns) \(copy.turns)"
            }
            let field = NSTextField(labelWithString: text)
            field.font = NSFont.monospacedDigitSystemFont(ofSize: 11.5, weight: .regular)
            field.textColor = row.status == "interrupted" || row.status == "dead_letter" ? .systemOrange : .labelColor
            field.lineBreakMode = .byTruncatingTail
            field.isSelectable = true
            return field
        }
    }

    private func card(title: String, views: [NSView]) -> NSView {
        let heading = NSTextField(labelWithString: title.uppercased())
        heading.font = NSFont.systemFont(ofSize: 11, weight: .semibold)
        heading.textColor = .secondaryLabelColor

        let inner = NSStackView(views: [heading] + views)
        inner.orientation = .vertical
        inner.alignment = .leading
        inner.spacing = 6
        inner.translatesAutoresizingMaskIntoConstraints = false

        let box = NSView()
        box.wantsLayer = true
        box.layer?.cornerRadius = 10
        box.layer?.borderWidth = 1
        box.layer?.borderColor = NSColor.separatorColor.withAlphaComponent(0.7).cgColor
        box.layer?.backgroundColor = NSColor.labelColor.withAlphaComponent(0.035).cgColor
        box.translatesAutoresizingMaskIntoConstraints = false
        box.addSubview(inner)
        NSLayoutConstraint.activate([
            inner.topAnchor.constraint(equalTo: box.topAnchor, constant: 10),
            inner.leadingAnchor.constraint(equalTo: box.leadingAnchor, constant: 12),
            inner.trailingAnchor.constraint(equalTo: box.trailingAnchor, constant: -12),
            inner.bottomAnchor.constraint(equalTo: box.bottomAnchor, constant: -10),
            box.widthAnchor.constraint(equalToConstant: 360),
        ])
        return box
    }

    private func labeled(_ label: String, _ value: String, valueColor: NSColor = .labelColor) -> NSView {
        let name = NSTextField(labelWithString: label)
        name.font = NSFont.systemFont(ofSize: 12)
        name.textColor = .secondaryLabelColor
        name.setContentHuggingPriority(.defaultHigh, for: .horizontal)
        let field = NSTextField(labelWithString: value)
        field.font = NSFont.systemFont(ofSize: 12, weight: .medium)
        field.textColor = valueColor
        field.lineBreakMode = .byTruncatingTail
        field.isSelectable = true
        field.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        let row = NSStackView(views: [name, field])
        row.orientation = .horizontal
        row.alignment = .centerY
        row.spacing = 8
        row.distribution = .fill
        name.widthAnchor.constraint(greaterThanOrEqualToConstant: 92).isActive = true
        return row
    }

    private func muted(_ text: String) -> NSView {
        let field = NSTextField(labelWithString: text)
        field.font = NSFont.systemFont(ofSize: 12)
        field.textColor = .tertiaryLabelColor
        return field
    }

    private func errorLine(_ text: String) -> NSView {
        let field = NSTextField(labelWithString: text)
        field.font = NSFont.systemFont(ofSize: 12)
        field.textColor = .systemRed
        field.lineBreakMode = .byWordWrapping
        field.maximumNumberOfLines = 4
        field.preferredMaxLayoutWidth = 360
        return field
    }

    private func metricTile(title: String, value: String, emphasize: Bool) -> NSView {
        let number = NSTextField(labelWithString: value)
        number.font = NSFont.monospacedDigitSystemFont(ofSize: 16, weight: .semibold)
        number.textColor = emphasize ? .systemOrange : .labelColor
        let caption = NSTextField(labelWithString: title)
        caption.font = NSFont.systemFont(ofSize: 10)
        caption.textColor = .secondaryLabelColor
        let tile = NSStackView(views: [number, caption])
        tile.orientation = .vertical
        tile.alignment = .leading
        tile.spacing = 0
        tile.wantsLayer = true
        tile.layer?.cornerRadius = 8
        tile.layer?.backgroundColor = NSColor.labelColor.withAlphaComponent(0.05).cgColor
        tile.edgeInsets = NSEdgeInsets(top: 8, left: 8, bottom: 8, right: 8)
        tile.widthAnchor.constraint(equalToConstant: 108).isActive = true
        return tile
    }

    private func wrapTiles(_ tiles: [NSView]) -> NSView {
        let grid = NSStackView()
        grid.orientation = .vertical
        grid.alignment = .leading
        grid.spacing = 6
        stride(from: 0, to: tiles.count, by: 3).forEach { start in
            let end = min(start + 3, tiles.count)
            let row = NSStackView(views: Array(tiles[start..<end]))
            row.orientation = .horizontal
            row.spacing = 6
            grid.addArrangedSubview(row)
        }
        return grid
    }

    private func buttonRow(_ items: [(String, PanelAction)]) -> NSView {
        let buttons = items.map { title, action -> NSButton in
            let button = NSButton(title: title, target: self, action: #selector(footerClicked(_:)))
            button.bezelStyle = .rounded
            button.controlSize = .small
            button.tag = tag(for: action)
            button.widthAnchor.constraint(equalToConstant: 112).isActive = true
            return button
        }
        let row = NSStackView(views: buttons)
        row.orientation = .horizontal
        row.spacing = 6
        row.distribution = .fillEqually
        return row
    }

    private func smallButton(_ title: String, tag: Int, action: Selector) -> NSButton {
        let button = NSButton(title: title, target: self, action: action)
        button.bezelStyle = .rounded
        button.controlSize = .mini
        button.tag = tag
        return button
    }

    private func tag(for action: PanelAction) -> Int {
        switch action {
        case .start: return 1
        case .stop: return 2
        case .restart: return 3
        case .pause: return 4
        case .resumeAuto: return 5
        case .refresh: return 6
        case .openConfig: return 7
        case .openLogs: return 8
        case .quit: return 9
        case .approve, .reject: return 0
        }
    }

    private func action(for tag: Int) -> PanelAction? {
        switch tag {
        case 1: return .start
        case 2: return .stop
        case 3: return .restart
        case 4: return .pause
        case 5: return .resumeAuto
        case 6: return .refresh
        case 7: return .openConfig
        case 8: return .openLogs
        case 9: return .quit
        default: return nil
        }
    }

    private func blank(_ value: String) -> String {
        value.isEmpty ? "—" : value
    }

    @objc private func footerClicked(_ sender: NSButton) {
        if let action = action(for: sender.tag) {
            onAction?(action)
        }
    }

    @objc private func approveClicked(_ sender: NSButton) {
        onAction?(.approve(sender.tag))
    }

    @objc private func rejectClicked(_ sender: NSButton) {
        onAction?(.reject(sender.tag))
    }
}

final class FlippedView: NSView {
    override var isFlipped: Bool { true }
}

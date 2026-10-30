import SwiftUI
import AimuxKit

/// Now: the session last in front on the Mac, the pinned ones, what needs you, what just finished, what is
/// working, and what sits open on your Mac. With the server's `describe` capability, a long press pins or unpins.
/// (`NowScreen`, because AimuxKit's ranking result is already called `NowView`.)
struct NowScreen: View {
    let model: NowModel
    let chat: ChatContext
    @Binding var serverAddress: String
    /// Switches to the All tab with this filter.
    let showAll: (AllFilter) -> Void

    @State private var path: [Session] = DebugHooks.initialPath
    /// Folders start open; these are the ones closed.
    @State private var collapsed = Set([DebugHooks.collapsedProject].compactMap { $0 })
    /// Approvals opened in place from their compact row when Now is busy.
    @State private var openCards: Set<String> = []
    @State private var hasLoaded = false
    @State private var showingSettings = DebugHooks.openSettings
    private let format = SessionRowFormat()

    private var view: AimuxKit.NowView { model.view }
    private var isQuiet: Bool { view.needsYou.isEmpty && view.finished.isEmpty && view.working.isEmpty }
    /// More than 5 needing you, finished or working: approvals become rows, like On your Mac shrinking.
    private var isBusy: Bool { view.needsYou.count + view.finished.count + view.summary.working > 5 }

    var body: some View {
        NavigationStack(path: $path) {
            ScrollViewReader { proxy in
                List {
                    Section {
                        ScreenHeader(title: "Now") {
                            HeaderButton(label: "Server", systemImage: "gearshape") { showingSettings = true }
                                .accessibilityIdentifier("settingsButton")
                        } below: {
                            NowSummaryLine(counts: view.summary, isQuiet: isQuiet, hasLoaded: hasLoaded)
                        }
                    }
                    .listSectionSpacing(8)
                    if let error = model.loadError {
                        Section {
                            ErrorBanner(message: error.userMessage) {
                                Task { await Interaction.perform { await model.refresh() } }
                            }
                        }
                    }
                    PlacedSections(view: view, model: model, format: format) { open($0) }
                    needsYouSections
                    finishedSection
                    workingSection
                    onMacSection
                }
                .listStyle(.insetGrouped)
                .listSectionSpacing(20)
                .environment(\.defaultMinListRowHeight, 44)
                .navigationTitle("Now")
                .toolbar(.hidden, for: .navigationBar)
                .chatDestination(chat, path: $path)
                .refreshable { await Interaction.perform { await model.refresh() } }
                .task {
                    // Poll while visible: one refresh at a time, 15 s apart; leaving the tab cancels the sleep.
                    // The first refresh is the person opening the tab; the rest are polls.
                    var opened = true
                    while !Task.isCancelled {
                        if opened {
                            await Interaction.perform { await model.refresh() }
                            opened = false
                        } else {
                            await model.refresh()
                        }
                        if !hasLoaded, DebugHooks.scrollToEnd { await scrollToEnd(proxy) }
                        hasLoaded = true
                        do { try await Task.sleep(for: .seconds(15)) } catch { break }
                    }
                }
                .sheet(isPresented: $showingSettings) {
                    NavigationStack { ServerSettingsView(serverAddress: $serverAddress, isFirstRun: false) }
                }
            }
        }
    }

    /// DEBUG `-scrollToEnd` screenshots: wait for the new rows to lay out, then show the bottom.
    private func scrollToEnd(_ proxy: ScrollViewProxy) async {
        try? await Task.sleep(for: .milliseconds(400))
        if let last = lastRowID { proxy.scrollTo(last, anchor: .bottom) }
    }

    /// The bottom row, for the DEBUG `-scrollToEnd` screenshots.
    private var lastRowID: String? {
        if let group = view.onMacFolders.last { return group.project }
        switch view.working {
        case .list(let items): return items.last?.session.id
        case .groups(let groups, _): return groups.last?.project
        }
    }

    private func sessionButton<Label: View>(_ session: Session, seen: Bool = false,
                                            @ViewBuilder label: () -> Label) -> some View {
        PinnableRow(session: session, model: model, open: { open(session, seen: seen) }, label: label)
            .compactRow()
    }

    // MARK: Needs you

    @ViewBuilder private var needsYouSections: some View {
        let failures = view.needsYou.filter { if case .failed = $0.activity { true } else { false } }
        if isBusy, !view.needsYou.isEmpty {
            Section {
                ForEach(approvals, id: \.1.approvalId) { item, approval in
                    if openCards.contains(approval.approvalId) {
                        card(item, approval)
                    } else {
                        Button { _ = withAnimation(.snappy) { openCards.insert(approval.approvalId) } } label: {
                            ApprovalRow(session: item.session, approval: approval, format: format)
                        }
                        .buttonStyle(.plain)
                        .compactRow()
                        .accessibilityIdentifier("approvalRow-\(approval.approvalId)")
                    }
                }
                ForEach(failures, id: \.session.id) { failedRow($0).compactRow() }
            } header: {
                SectionLabel("Needs you", detail: "\(view.needsYou.count)")
            }
        } else if !isBusy {
            ForEach(approvals, id: \.1.approvalId) { item, approval in
                Section { card(item, approval) }
            }
            if !failures.isEmpty {
                Section {
                    ForEach(failures, id: \.session.id) { failedRow($0).compactRow() }
                }
            }
        }
    }

    private var approvals: [(NowItem, Approval)] {
        view.needsYou.compactMap { item in
            if case .needsApproval(let approval) = item.activity { return (item, approval) }
            return nil
        }
    }

    private func card(_ item: NowItem, _ approval: Approval) -> some View {
        ApprovalCard(session: item.session, approval: approval,
                     isAnswering: model.answering.contains(approval.approvalId),
                     error: model.answerErrors[approval.approvalId], format: format,
                     answer: { option in
                         Task { await model.answer(approval, option: option, session: item.session.id) }
                     },
                     open: { path.append(item.session) })
    }

    @ViewBuilder private func failedRow(_ item: NowItem) -> some View {
        if case .failed(let error, _) = item.activity {
            let session = item.session
            FailedRow(session: session, error: error, isRetrying: model.retrying.contains(session.id),
                      retryError: model.retryErrors[session.id], format: format,
                      open: { open(session) }, retry: { Task { await model.retry(session) } })
        }
    }

    // MARK: Finished

    @ViewBuilder private var finishedSection: some View {
        if !view.finished.isEmpty {
            Section {
                ForEach(view.finished, id: \.session.id) { item in
                    sessionButton(item.session, seen: true) {
                        DotRow(status: .idle, title: item.session.displayTitle,
                               description: item.session.descriptionLine, date: finishedAt(item), format: format) {
                            Text(bidi: "Finished · " + isolated(format.projectName(item.session.project)))
                        }
                    }
                }
            } header: {
                SectionLabel("Just finished")
            }
        }
    }

    private func finishedAt(_ item: NowItem) -> Date {
        if case .finished(let time) = item.activity { return time }
        return item.session.lastActive
    }

    // MARK: Working

    @ViewBuilder private var workingSection: some View {
        switch view.working {
        case .list(let items) where !items.isEmpty:
            Section {
                ForEach(items, id: \.session.id) { item in
                    sessionButton(item.session) { WorkingRow(item: item, format: format) }
                }
            } header: {
                SectionLabel("Working")
            }
        case .groups(let groups, let more):
            Section {
                ForEach(groups, id: \.project) { group in
                    groupRows(group)
                }
                if let more {
                    Button { showAll(AllFilter(sort: .mostActive)) } label: {
                        HStack(spacing: 10) {
                            FolderIcon()
                            Text(more.projects == 1 ? "1 more project" : "\(more.projects) more projects")
                                .font(.body.weight(.semibold))
                                .frame(maxWidth: .infinity, alignment: .leading)
                            Text("\(more.sessions)").foregroundStyle(.secondary).monospacedDigit()
                            Image(systemName: "chevron.right")
                                .font(.footnote.weight(.semibold))
                                .foregroundStyle(.tertiary)
                        }
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .compactRow()
                }
            } header: {
                SectionLabel("Working", detail: "\(view.summary.working), by project")
            }
        default:
            EmptyView()
        }
    }

    /// A Working folder (`onMac` false: working dots and steps) or an On your Mac one (idle rows, no step,
    /// collapsed apart from a Working folder of the same project).
    @ViewBuilder private func groupRows(_ group: ProjectGroup, onMac: Bool = false) -> some View {
        if group.count == 1 {
            sessionButton(group.newest.session) { SingleFolderRow(group: group, format: format, showsStep: !onMac) }
        } else {
            let key = (onMac ? "mac:" : "") + group.project
            let isOpen = !collapsed.contains(key)
            Button {
                withAnimation(.snappy) {
                    if isOpen { collapsed.insert(key) } else { collapsed.remove(key) }
                }
            } label: {
                ProjectGroupRow(group: group, isExpanded: isOpen)
            }
            .buttonStyle(.plain)
            .compactRow()
            .accessibilityIdentifier((onMac ? "mac-group-" : "group-") + group.name)
            if isOpen {
                ForEach(group.items, id: \.session.id) { item in
                    FolderItem {
                        PinnableRow(session: item.session, model: model, open: { open(item.session) }, label: {
                            if onMac {
                                OnMacRow(session: item.session, format: format)
                            } else {
                                CompactWorkingRow(item: item, format: format)
                            }
                        })
                    }
                    .listRowInsets(.vertical, 0)
                }
            }
        }
    }

    // MARK: On your Mac

    @ViewBuilder private var onMacSection: some View {
        if view.onMacTotal > 0 {
            Section {
                ForEach(view.onMacFolders, id: \.project) { group in
                    groupRows(group, onMac: true)
                }
            } header: {
                SectionLabel(title: "On your Mac", detail: "read-only, by project") {
                    Button("All \(view.onMacTotal)") { showAll(AllFilter(onMacOnly: true)) }
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .accessibilityIdentifier("allOnMac")
                }
            }
        }
    }

    private func open(_ session: Session, seen: Bool = false) {
        if seen { model.markSeen(session.id) }
        path.append(session)
    }
}

/// Last on your Mac (with how long ago it was in front) and Pinned, above the sections of state.
private struct PlacedSections: View {
    let view: AimuxKit.NowView
    let model: NowModel
    let format: SessionRowFormat
    let open: (Session) -> Void

    var body: some View {
        if let item = view.lastOnMac {
            Section {
                row(item)
            } header: {
                SectionLabel("Last on your Mac", detail: view.lastOnMacAt.map { format.since($0) })
            }
        }
        if !view.pinned.isEmpty {
            Section {
                ForEach(view.pinned, id: \.session.id) { row($0) }
            } header: {
                SectionLabel("Pinned")
            }
        }
    }

    private func row(_ item: PlacedItem) -> some View {
        PinnableRow(session: item.session, model: model, open: { open(item.session) },
                    label: { PlacedRow(item: item, format: format) })
            .compactRow()
    }
}

/// "2 need you · 2 working · 14 on your Mac": counts in bold, only the first part amber;
/// "Nothing needs you" when nothing needs you, finished or is working.
private struct NowSummaryLine: View {
    let counts: NowSummary
    let isQuiet: Bool
    let hasLoaded: Bool

    var body: some View {
        if isQuiet {
            Text(hasLoaded ? "Nothing needs you" : " ")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .accessibilityIdentifier("nowSummary")
        } else {
            Text(summary)
                .font(.subheadline)
                .accessibilityIdentifier("nowSummary")
        }
    }

    /// "2 need you · 2 working · 14 on your Mac": counts in bold, only the first part amber.
    private var summary: AttributedString {
        var parts: [AttributedString] = []
        func part(_ count: Int, _ label: String, color: Color) -> AttributedString {
            var number = AttributedString("\(count)")
            number.font = .subheadline.weight(.semibold)
            number.foregroundColor = color == .secondary ? .primary : color
            var rest = AttributedString(" \(label)")
            rest.foregroundColor = color
            return number + rest
        }
        if counts.needYou > 0 { parts.append(part(counts.needYou, "need you", color: .amber)) }
        if counts.working > 0 { parts.append(part(counts.working, "working", color: .secondary)) }
        if counts.onMac > 0 { parts.append(part(counts.onMac, "on your Mac", color: .secondary)) }
        var separator = AttributedString(" · ")
        separator.foregroundColor = .secondary
        return parts.enumerated().reduce(into: AttributedString()) { line, entry in
            if entry.offset > 0 { line += separator }
            line += entry.element
        }
    }
}

private extension WorkingSection {
    var isEmpty: Bool {
        switch self {
        case .list(let items): items.isEmpty
        case .groups(let groups, _): groups.isEmpty
        }
    }
}

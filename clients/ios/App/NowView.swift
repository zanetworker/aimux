import SwiftUI
import AimuxKit

/// Now: what needs you, the pinned sessions, the Mac's iTerm2 windows and tabs, and the rest open elsewhere, as a
/// flat list until the screens are rebuilt on the Mac layout. With the server's `describe` capability, a long
/// press pins or unpins.
/// (`NowScreen`, because AimuxKit's ranking result is already called `NowView`.)
struct NowScreen: View {
    let model: NowModel
    let chat: ChatContext
    @Binding var serverAddress: String
    /// Switches to the All tab with this filter.
    let showAll: (AllFilter) -> Void

    @State private var path: [Session] = DebugHooks.initialPath
    /// Approvals opened in place from their compact row when Now is busy.
    @State private var openCards: Set<String> = []
    @State private var hasLoaded = false
    @State private var showingSettings = DebugHooks.openSettings
    private let format = SessionRowFormat()

    private var view: AimuxKit.NowView { model.view }
    private var isQuiet: Bool { view.needsYou.isEmpty && view.working == 0 }
    /// More than 5 needing you or working: approvals become rows.
    private var isBusy: Bool { view.needsYou.count + view.working > 5 }

    var body: some View {
        NavigationStack(path: $path) {
            ScrollViewReader { proxy in
                List {
                    Section {
                        ScreenHeader(title: "Now") {
                            HeaderButton(label: "Server", systemImage: "gearshape") { showingSettings = true }
                                .accessibilityIdentifier("settingsButton")
                        } below: {
                            NowSummaryLine(view: view, isQuiet: isQuiet, hasLoaded: hasLoaded)
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
                    needsYouSections
                    pinnedSection
                    windowSections
                    elsewhereSection
                    otherSection
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
        view.other.last?.id ?? view.elsewhere.last?.sessions.last?.id
            ?? view.windows.last?.tabs.last?.sessions.last?.id ?? view.pinned.last?.session.id
    }

    private func sessionButton<Label: View>(_ session: Session, @ViewBuilder label: () -> Label) -> some View {
        PinnableRow(session: session, model: model, open: { open(session) }, label: label)
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

    // MARK: Sessions

    private func placedRow(_ session: Session) -> some View {
        let activity = model.activity[session.id] ?? .none
        let item = PlacedItem(session: session, activity: activity,
                              isWorking: session.state == .busy || session.working || activity.isWorking)
        return sessionButton(session) { PlacedRow(item: item, format: format) }
    }

    @ViewBuilder private var pinnedSection: some View {
        if !view.pinned.isEmpty {
            Section {
                ForEach(view.pinned, id: \.session.id) { placedRow($0.session) }
            } header: {
                SectionLabel("Pinned")
            }
        }
    }

    @ViewBuilder private var windowSections: some View {
        ForEach(view.windows, id: \.id) { window in
            Section {
                ForEach(window.tabs, id: \.index) { tab in
                    ForEach(tab.sessions) { placedRow($0) }
                }
            } header: {
                SectionLabel(window.name, detail: "on your Mac")
            }
        }
    }

    @ViewBuilder private var elsewhereSection: some View {
        ForEach(view.elsewhere, id: \.label) { group in
            Section {
                ForEach(group.sessions) { placedRow($0) }
            } header: {
                SectionLabel(group.label)
            }
        }
    }

    @ViewBuilder private var otherSection: some View {
        if !view.other.isEmpty {
            Section {
                ForEach(view.other) { placedRow($0) }
            } header: {
                SectionLabel("Other sessions")
            }
        }
    }

    private func open(_ session: Session) {
        path.append(session)
    }
}

/// "2 need you · 2 working · 14 on your Mac": counts in bold, only the first part amber;
/// "Nothing needs you" when nothing needs you, finished or is working.
private struct NowSummaryLine: View {
    let view: AimuxKit.NowView
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
        if !view.needsYou.isEmpty { parts.append(part(view.needsYou.count, "need you", color: .amber)) }
        if view.working > 0 { parts.append(part(view.working, "working", color: .secondary)) }
        if view.open > 0 { parts.append(part(view.open, "open", color: .secondary)) }
        var separator = AttributedString(" · ")
        separator.foregroundColor = .secondary
        return parts.enumerated().reduce(into: AttributedString()) { line, entry in
            if entry.offset > 0 { line += separator }
            line += entry.element
        }
    }
}

import SwiftUI
import AimuxKit

/// Now: the search box and Working chip, what needs you, the pinned sessions that are not open, the Mac's iTerm2
/// windows and tabs as they are on the Mac, and the open sessions elsewhere. Typing hides the panes that do not
/// match in place and lists closed matches under Other sessions, then conversation hits. With the server's
/// `describe` capability, a long press pins or unpins.
/// (`NowScreen`, because AimuxKit's ranking result is already called `NowView`.)
struct NowScreen: View {
    let model: NowModel
    let search: SearchModel
    let chat: ChatContext
    @Binding var serverAddress: String

    @State private var path: [Session] = DebugHooks.initialPath
    /// Approvals opened in place from their compact row when Now is busy.
    @State private var openCards: Set<String> = []
    @State private var hasLoaded = false
    @State private var showingSettings = DebugHooks.openSettings
    /// Tabs opened or folded by hand, by `OpenMemory.tabKey`.
    @AppStorage("openTabs") private var openTabs = ""
    private let format = SessionRowFormat()

    private var view: AimuxKit.NowView { model.view }
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
                            NowSummaryLine(view: view, hasLoaded: hasLoaded, matches: matches)
                            SearchField(text: query, prompt: "Search all sessions").padding(.top, 4)
                            if view.working > 0 || model.workingOnly {
                                WorkingChip(isOn: workingOnly, count: view.working)
                            }
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
                    ConversationsSection(search: search, query: model.query, shown: shownIDs, format: format) { hit in
                        Button { open(hit.session) } label: { SearchHitRow(hit: hit, format: format) }
                            .buttonStyle(.plain)
                            .compactRow()
                            .accessibilityIdentifier("hit-\(hit.id)")
                    }
                }
                .listStyle(.insetGrouped)
                .scrollDismissesKeyboard(.immediately)
                .conversationSearch(search, query: model.query)
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

    /// Needs you in ranking order: an approval as its card (a row when busy), a terminal session waiting for
    /// permission as its session row (the chat's prompt card answers), then failures.
    @ViewBuilder private var needsYouSections: some View {
        let failures = view.needsYou.filter { if case .failed = $0.activity { true } else { false } }
        let asks = view.needsYou.filter { if case .failed = $0.activity { false } else { true } }
        if isBusy, !view.needsYou.isEmpty {
            Section {
                ForEach(asks, id: \.session.id) { compactAsk($0) }
                ForEach(failures, id: \.session.id) { failedRow($0).compactRow() }
            } header: {
                SectionLabel("Needs you", detail: "\(view.needsYou.count)")
            }
        } else if !isBusy {
            ForEach(asks, id: \.session.id) { item in
                Section {
                    if case .needsApproval(let approval) = item.activity {
                        card(item, approval)
                    } else {
                        row(item.session, inPane: false)
                    }
                }
            }
            if !failures.isEmpty {
                Section {
                    ForEach(failures, id: \.session.id) { failedRow($0).compactRow() }
                }
            }
        }
    }

    @ViewBuilder private func compactAsk(_ item: NowItem) -> some View {
        if case .needsApproval(let approval) = item.activity {
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
        } else {
            row(item.session, inPane: false)
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

    private var query: Binding<String> { Binding { model.query } set: { model.query = $0 } }
    private var workingOnly: Binding<Bool> { Binding { model.workingOnly } set: { model.workingOnly = $0 } }
    /// A search or the Working chip: every tab left is held open, and counts read "1 of 4 panes".
    private var isFiltered: Bool { !model.query.trimmingCharacters(in: .whitespaces).isEmpty || model.workingOnly }
    private var hasQuery: Bool { !model.query.trimmingCharacters(in: .whitespaces).isEmpty }
    /// With a query, the open sessions that match: in the Mac's panes and elsewhere.
    private var matches: Int? {
        guard hasQuery else { return nil }
        return view.windows.flatMap(\.tabs).map(\.sessions.count).reduce(0, +)
            + view.elsewhere.map(\.sessions.count).reduce(0, +)
    }
    /// Every session the screen lists, so conversation hits do not repeat them.
    private var shownIDs: Set<String> {
        Set(view.needsYou.map(\.session.id) + view.pinned.map(\.session.id)
            + view.windows.flatMap(\.tabs).flatMap(\.sessions).map(\.id)
            + view.elsewhere.flatMap(\.sessions).map(\.id) + view.other.map(\.id))
    }

    private func row(_ session: Session, inPane: Bool) -> some View {
        let activity = model.activity[session.id] ?? .none
        let state = rowState(session, activity: activity, now: .now, format: format)
        return sessionButton(session) {
            SessionLine(session: session, activity: activity, state: state, place: rowPlace(session, withTab: !inPane),
                        showsPin: inPane && session.pinned, showsTime: state == nil, format: format)
        }
    }

    @ViewBuilder private var pinnedSection: some View {
        if !view.pinned.isEmpty {
            Section {
                ForEach(view.pinned, id: \.session.id) { row($0.session, inPane: false) }
            } header: {
                SectionLabel("Pinned")
            }
        }
    }

    @ViewBuilder private var windowSections: some View {
        ForEach(Array(view.windows.enumerated()), id: \.element.id) { position, window in
            Section {
                ForEach(window.tabs, id: \.index) { tab in
                    let isOpen = isOpen(tab, in: window)
                    Button { if !isFiltered { toggle(tab, in: window) } } label: {
                        FoldLine(name: tab.name, position: "tab \(tab.index)", counts: counts(tab), isOpen: isOpen,
                                 canFold: !isFiltered)
                    }
                    .buttonStyle(.plain)
                    .compactRow()
                    .accessibilityIdentifier("tab-\(window.id)-\(tab.index)")
                    if isOpen {
                        ForEach(tab.sessions) { session in
                            FolderItem { row(session, inPane: true) }.listRowInsets(.vertical, 0)
                        }
                    }
                }
            } header: {
                VStack(alignment: .leading, spacing: 10) {
                    if position == 0 { SectionLabel("On your Mac") }
                    Text(bidi: isolated(window.name))
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.primary)
                        .textCase(nil)
                        .accessibilityAddTraits(.isHeader)
                        .accessibilityIdentifier("window-\(window.id)")
                }
            } footer: {
                if position == view.windows.count - 1, view.hiddenTabs > 0 { hiddenTabsLine }
            }
        }
        if view.windows.isEmpty, view.hiddenTabs > 0 {
            Section {} header: { SectionLabel("On your Mac") } footer: { hiddenTabsLine }
        }
    }

    /// Open sessions in no iTerm2 pane. With no layout from the Mac at all, they are the Mac's sessions by folder,
    /// under its header.
    @ViewBuilder private var elsewhereSection: some View {
        if !view.elsewhere.isEmpty {
            Section {
                ForEach(view.elsewhere.flatMap(\.sessions)) { row($0, inPane: false) }
            } header: {
                SectionLabel(view.windows.isEmpty && view.hiddenTabs == 0 ? "On your Mac" : "Elsewhere")
            }
        }
    }

    @ViewBuilder private var otherSection: some View {
        if !view.other.isEmpty {
            Section {
                ForEach(view.other) { row($0, inPane: false) }
            } header: {
                SectionLabel("Other sessions")
            }
        }
    }

    private func open(_ session: Session) {
        path.append(session)
    }
}

// MARK: Tabs

extension NowScreen {
    private var hiddenTabsLine: some View {
        let count = view.hiddenTabs
        return Text(count == 1 ? "1 tab with no match is hidden" : "\(count) tabs with no match are hidden")
            .accessibilityIdentifier("hiddenTabs")
    }

    /// "4 panes · 1 working"; with a search or the chip, "1 of 4 panes".
    private func counts(_ tab: NowTab) -> String {
        let panes = tab.panes == 1 ? "1 pane" : "\(tab.panes) panes"
        if isFiltered { return "\(tab.sessions.count) of \(panes)" }
        return tab.working > 0 ? "\(panes) · \(tab.working) working" : panes
    }

    private func key(_ tab: NowTab, in window: NowWindow) -> String {
        OpenMemory.tabKey(window: window.name, index: tab.index, tab: tab.name)
    }

    private func isOpen(_ tab: NowTab, in window: NowWindow) -> Bool {
        isFiltered || OpenMemory(openTabs).isOpen(key(tab, in: window), otherwise: tab.open)
    }

    private func toggle(_ tab: NowTab, in window: NowWindow) {
        let open = !isOpen(tab, in: window)
        withAnimation(.snappy) {
            openTabs = OpenMemory(openTabs).setting(key(tab, in: window), open: open).raw
        }
    }
}

/// "2 need you · 25 open"; with a query "2 open match"; "Nothing open" once loaded with nothing open.
/// One quiet line: counts in semibold, no colour.
private struct NowSummaryLine: View {
    let view: AimuxKit.NowView
    let hasLoaded: Bool
    /// With a query, the open sessions that match.
    let matches: Int?

    var body: some View {
        Text(summary)
            .font(.subheadline)
            .foregroundStyle(.secondary)
            .accessibilityIdentifier("nowSummary")
    }

    private var summary: AttributedString {
        func part(_ count: Int, _ label: String) -> AttributedString {
            var number = AttributedString("\(count)")
            number.font = .subheadline.weight(.semibold)
            number.foregroundColor = .primary
            return number + AttributedString(" " + label)
        }
        if let matches {
            return matches == 0 ? AttributedString("No open match") : part(matches, "open match")
        }
        guard hasLoaded || view.open > 0 || !view.needsYou.isEmpty else { return AttributedString(" ") }
        var parts: [AttributedString] = []
        if !view.needsYou.isEmpty { parts.append(part(view.needsYou.count, "need you")) }
        parts.append(view.open == 0 ? AttributedString("Nothing open") : part(view.open, "open"))
        return parts.enumerated().reduce(into: AttributedString()) { line, entry in
            if entry.offset > 0 { line += AttributedString(" · ") }
            line += entry.element
        }
    }
}

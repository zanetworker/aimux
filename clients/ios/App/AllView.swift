import SwiftUI
import AimuxKit

/// Recent or Folders, remembered on the device; Recent the first time.
enum AllMode: String, CaseIterable {
    case recent, folders

    var title: String { self == .recent ? "Recent" : "Folders" }
}

/// Every session: the search box, then Recent (newest first, by day) or Folders (repos A to Z, the main checkout
/// first, then worktrees). With a query, the same results as Now's search: open matches, then the rest, then
/// conversation hits.
struct AllScreen: View {
    let list: SessionListModel
    let search: SearchModel
    /// What Now last read each session doing, for search's open test and the dots.
    let activity: [String: Activity]
    let chat: ChatContext
    @State private var path: [Session] = []
    @State private var query = DebugHooks.initialQuery ?? ""
    @AppStorage("allMode") private var mode = AllMode.recent
    /// Folders opened or folded by hand, by `OpenMemory.folderKey`.
    @AppStorage("openFolders") private var openFolders = ""
    private let format = SessionRowFormat()

    private var hasQuery: Bool { !query.trimmingCharacters(in: .whitespaces).isEmpty }
    private var sections: [DaySection] {
        allSections(list.sessions, project: nil, now: .now, timeZone: .current)
    }

    var body: some View {
        NavigationStack(path: $path) {
            ScrollViewReader { proxy in
                List {
                    Section {
                        ScreenHeader(title: "All") { EmptyView() } below: {
                            Text(list.sessions.count == 1 ? "1 session" : "\(list.sessions.count) sessions")
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                                .monospacedDigit()
                            SearchField(text: $query, prompt: "Search all sessions").padding(.top, 4)
                            if !hasQuery {
                                Picker("Show", selection: $mode) {
                                    ForEach(AllMode.allCases, id: \.self) { Text($0.title).tag($0) }
                                }
                                .pickerStyle(.segmented)
                                .padding(.top, 2)
                                .accessibilityIdentifier("allMode")
                            }
                        }
                    }
                    .listSectionSpacing(8)
                    if let error = list.error {
                        Section {
                            ErrorBanner(message: error.userMessage) {
                                Task { await Interaction.perform { await list.refresh() } }
                            }
                        }
                    }
                    if hasQuery {
                        resultSections
                    } else if mode == .folders {
                        folderSection
                    } else {
                        recentSections
                    }
                    if let cachedAt = list.cachedAt {
                        Section {} footer: {
                            Text("Saved list from \(format.relativeTime(cachedAt)). Pull to refresh.")
                        }
                    }
                }
                .listStyle(.insetGrouped)
                .listSectionSpacing(20)
                .scrollDismissesKeyboard(.immediately)
                .overlay { emptyState }
                .environment(\.defaultMinListRowHeight, 44)
                .navigationTitle("All")
                .toolbar(.hidden, for: .navigationBar)
                .chatDestination(chat, path: $path)
                .refreshable { await Interaction.perform { await list.refresh() } }
                .conversationSearch(search, query: query)
                .task {
                    await Interaction.perform { await list.refresh() }
                    guard DebugHooks.scrollToEnd,
                          let last = sections.last?.sessions.last else { return }
                    try? await Task.sleep(for: .milliseconds(400))
                    proxy.scrollTo(last.id, anchor: .bottom)
                }
            }
        }
    }

    /// A session row: "Waiting for permission" at a permission prompt, its place with the tab when open on the
    /// Mac, the time on the right.
    private func sessionRow(_ session: Session, place: String? = nil) -> some View {
        Button { path.append(session) } label: {
            SessionLine(session: session, activity: activity[session.id] ?? .none,
                        state: session.isWaitingForPermission ? .waitingForPermission : nil,
                        place: place ?? rowPlace(session, withTab: true), format: format)
        }
        .buttonStyle(.plain)
        .compactRow()
        .accessibilityIdentifier("session-\(session.id)")
    }

    // MARK: Recent

    @ViewBuilder private var recentSections: some View {
        ForEach(sections, id: \.day) { section in
            Section {
                ForEach(section.sessions) { sessionRow($0) }
            } header: {
                SectionLabel(title(section.day))
            }
        }
    }

    /// "Today", "Yesterday", "Mon, Oct 20".
    private func title(_ day: String) -> String {
        switch day {
        case "today": return "Today"
        case "yesterday": return "Yesterday"
        default:
            let parser = DateFormatter()
            parser.calendar = Calendar(identifier: .gregorian)
            parser.locale = Locale(identifier: "en_US_POSIX")
            parser.dateFormat = "yyyy-MM-dd"
            guard let date = parser.date(from: day) else { return day }
            let sameYear = Calendar.current.isDate(date, equalTo: .now, toGranularity: .year)
            return sameYear ? date.formatted(.dateTime.weekday(.abbreviated).month(.abbreviated).day())
                : date.formatted(.dateTime.month(.abbreviated).day().year())
        }
    }

    // MARK: Folders

    @ViewBuilder private var folderSection: some View {
        let repos = folderTree(list.sessions, now: .now)
        if !repos.isEmpty {
            Section {
                ForEach(repos, id: \.repoPath) { repo in
                    let key = OpenMemory.folderKey(repoPath: repo.repoPath)
                    let isOpen = OpenMemory(openFolders).isOpen(key, otherwise: repo.open)
                    foldButton(key, isOpen: isOpen, id: "folder-\(repo.name)") {
                        FoldLine(name: repo.name, counts: counts(repo.count, working: repo.working), isOpen: isOpen)
                    }
                    if isOpen {
                        ForEach(repo.sessions) { session in
                            FolderItem { sessionRow(session, place: folderPlace(session)) }
                                .listRowInsets(.vertical, 0)
                        }
                        ForEach(repo.worktrees, id: \.name) { worktree in
                            worktreeRows(worktree, in: repo)
                        }
                    }
                }
            }
        }
    }

    @ViewBuilder private func worktreeRows(_ worktree: WorktreeNode, in repo: RepoNode) -> some View {
        let key = OpenMemory.folderKey(repoPath: repo.repoPath, worktree: worktree.name)
        let isOpen = OpenMemory(openFolders).isOpen(key, otherwise: worktree.open)
        FolderItem {
            foldButton(key, isOpen: isOpen, id: "worktree-\(repo.name)-\(worktree.name)") {
                FoldLine(name: worktree.name, counts: counts(worktree.count, working: worktree.working),
                         isOpen: isOpen)
            }
        }
        .listRowInsets(.vertical, 0)
        if isOpen {
            ForEach(worktree.sessions) { session in
                FolderItem(depth: 2) { sessionRow(session, place: folderPlace(session)) }
                    .listRowInsets(.vertical, 0)
            }
        }
    }

    private func foldButton<Label: View>(_ key: String, isOpen: Bool, id: String,
                                         @ViewBuilder label: () -> Label) -> some View {
        Button {
            withAnimation(.snappy) { openFolders = OpenMemory(openFolders).setting(key, open: !isOpen).raw }
        } label: { label() }
            .buttonStyle(.plain)
            .compactRow()
            .accessibilityIdentifier(id)
    }

    /// Inside a folder its name is already above: only the subfolder and the Mac place, if any.
    private func folderPlace(_ session: Session) -> String {
        var parts: [String] = []
        if let sub = session.folder?.sub { parts.append(sub) }
        if let mac = session.mac { parts += ["tab \(mac.tab.index)", "pane \(mac.pane)"] }
        return parts.joined(separator: " · ")
    }

    /// "38 · 2 working", "12".
    private func counts(_ count: Int, working: Int) -> String {
        working > 0 ? "\(count) · \(working) working" : "\(count)"
    }

    // MARK: Search

    @ViewBuilder private var resultSections: some View {
        let found = searchSessions(list.sessions, query: query, activity: activity, now: .now)
        SearchResultSections(found: found) { sessionRow($0) }
        ConversationsSection(search: search, query: query, shown: Set((found.open + found.other).map(\.id)),
                             format: format) { hit in
            Button { path.append(hit.session) } label: { SearchHitRow(hit: hit, format: format) }
                .buttonStyle(.plain)
                .compactRow()
                .accessibilityIdentifier("hit-\(hit.id)")
        }
    }

    @ViewBuilder private var emptyState: some View {
        if list.sessions.isEmpty && list.error == nil {
            if list.isLoading {
                ProgressView("Loading sessions")
            } else {
                ContentUnavailableView("No sessions", systemImage: "terminal",
                                       description: Text("Sessions from Claude Code on your Mac show up here."))
            }
        }
    }
}

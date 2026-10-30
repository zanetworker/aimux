import SwiftUI
import AimuxKit

/// What the All screen shows; Now sets it when it links here ("All 14", "N more projects").
struct AllFilter: Equatable {
    /// A session's full `project` path, or nil for all projects.
    var project: String?
    /// Only sessions open in a terminal on the Mac.
    var onMacOnly = false
    var sort: AllSort = .newest
}

/// Every session, grouped by day with counts, filtered by project and sorted three ways.
struct AllScreen: View {
    let list: SessionListModel
    let chat: ChatContext
    @Binding var filter: AllFilter
    @State private var path: [Session] = []
    private let format = SessionRowFormat()

    private var sections: [DaySection] {
        let shown = filter.onMacOnly ? list.sessions.filter { $0.state == .openInTerminal } : list.sessions
        return allSections(shown, project: filter.project, sort: filter.sort, now: .now, timeZone: .current)
    }

    var body: some View {
        NavigationStack(path: $path) {
            ScrollViewReader { proxy in
                List {
                    Section {
                        ScreenHeader(title: "All Sessions") { EmptyView() } below: { filterMenus }
                    }
                    .listSectionSpacing(8)
                    if filter.onMacOnly {
                        Section {
                            HStack {
                                Label("Open on your Mac", systemImage: "lock")
                                Spacer()
                                Button("Show all") { filter.onMacOnly = false }
                                    .buttonStyle(.borderless)
                                    .foregroundStyle(.secondary)
                                    .accessibilityIdentifier("clearOnMac")
                            }
                        }
                    }
                    if let error = list.error {
                        Section {
                            ErrorBanner(message: error.userMessage) {
                                Task { await Interaction.perform { await list.refresh() } }
                            }
                        }
                    }
                    ForEach(sections, id: \.day) { section in
                        Section {
                            ForEach(section.sessions) { session in
                                Button { path.append(session) } label: { SessionRow(session: session, format: format) }
                                    .buttonStyle(.plain)
                                    .compactRow()
                                    .accessibilityIdentifier("session-\(session.id)")
                            }
                        } header: {
                            SectionLabel(title: title(section.day)) {
                                Text("\(section.sessions.count)").monospacedDigit()
                            }
                        }
                    }
                    if let cachedAt = list.cachedAt {
                        Section {} footer: {
                            Text("Saved list from \(format.relativeTime(cachedAt)). Pull to refresh.")
                        }
                    }
                }
                .listStyle(.insetGrouped)
                .listSectionSpacing(20)
                .overlay { emptyState }
                .environment(\.defaultMinListRowHeight, 44)
                .navigationTitle("All Sessions")
                .toolbar(.hidden, for: .navigationBar)
                .chatDestination(chat, path: $path)
                .refreshable { await Interaction.perform { await list.refresh() } }
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

    private var filterMenus: some View {
        HStack(spacing: 8) {
            Menu {
                Picker("Project", selection: $filter.project) {
                    Text("All projects").tag(String?.none)
                    ForEach(ProjectSummary.list(list.sessions)) { project in
                        Text("\(project.name)  \(project.count)").tag(String?.some(project.path))
                    }
                }
            } label: {
                MenuLabel(text: filter.project.map(format.projectName) ?? "All projects")
            }
            .accessibilityIdentifier("projectMenu")
            Menu {
                Picker("Sort", selection: $filter.sort) {
                    ForEach(AllSort.allCases, id: \.self) { Text(sortName($0)).tag($0) }
                }
            } label: {
                MenuLabel(text: sortName(filter.sort))
            }
            .accessibilityIdentifier("sortMenu")
            Spacer()
        }
    }

    private func sortName(_ sort: AllSort) -> String {
        switch sort {
        case .newest: "Newest"
        case .mostActive: "Most active"
        case .project: "Project"
        }
    }

    /// "Today", "Yesterday", "Mon, Oct 20"; the folder name when sorted by project.
    private func title(_ day: String) -> String {
        if filter.sort == .project { return format.projectName(day) }
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

    @ViewBuilder private var emptyState: some View {
        if list.sessions.isEmpty && list.error == nil {
            if list.isLoading {
                ProgressView("Loading sessions")
            } else {
                ContentUnavailableView("No sessions", systemImage: "terminal",
                                       description: Text("Sessions from Claude Code on your Mac show up here."))
            }
        } else if sections.isEmpty && !list.sessions.isEmpty {
            ContentUnavailableView("No sessions here", systemImage: "line.3.horizontal.decrease")
        }
    }
}

/// A compact filter button: its value and a small chevron.
private struct MenuLabel: View {
    let text: String

    var body: some View {
        HStack(spacing: 4) {
            Text(text).lineLimit(1)
            Image(systemName: "chevron.down").font(.caption2.weight(.semibold))
        }
        .font(.subheadline)
        .foregroundStyle(.primary)
        .padding(.horizontal, 12)
        .padding(.vertical, 7)
        .background(Color(.tertiarySystemFill), in: Capsule())
    }
}

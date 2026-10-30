import SwiftUI
import AimuxKit

/// Search over all sessions: title and description matches from the loaded list at once (then folder and last
/// prompt), the server's hits after a pause.
struct SearchScreen: View {
    let list: SessionListModel
    let search: SearchModel
    let chat: ChatContext
    @State private var query = DebugHooks.initialQuery ?? ""
    @State private var path: [Session] = []
    private let format = SessionRowFormat()

    var body: some View {
        NavigationStack(path: $path) {
            List {
                if !SessionFilter(query: query).words.isEmpty {
                    let local = localMatches(list.sessions, query: query)
                    if !local.isEmpty {
                        Section {
                            ForEach(local) { session in
                                link(session) { SessionRow(session: session, format: format) }
                            }
                        } header: {
                            SectionLabel("Titles and descriptions")
                        }
                    }
                    if search.isAvailable { conversations(excluding: Set(local.map(\.id))) }
                }
            }
            .listStyle(.insetGrouped)
            .navigationTitle("Search")
            .chatDestination(chat, path: $path)
            .searchable(text: $query, placement: .navigationBarDrawer(displayMode: .always),
                        prompt: "Search \(list.sessions.count) sessions")
            .task {
                // Title matches need the list; Now and All usually loaded it already.
                if list.sessions.isEmpty || list.cachedAt != nil { await Interaction.perform { await list.refresh() } }
            }
            .task(id: query) {
                // Debounce typing; a newer query cancels this one.
                try? await Task.sleep(for: .milliseconds(400))
                guard !Task.isCancelled else { return }
                await Interaction.perform { await search.run(query) }
            }
        }
    }

    @ViewBuilder private func conversations(excluding shown: Set<String>) -> some View {
        let hits = search.hits.filter { !shown.contains($0.id) }
        Section {
            ForEach(hits) { hit in
                link(hit.session, id: "hit-\(hit.id)") { SearchHitRow(hit: hit, format: format) }
            }
            if hits.isEmpty {
                if search.isSearching {
                    HStack { ProgressView(); Text("Searching your Mac…").foregroundStyle(.secondary) }
                } else if let error = search.error {
                    Text(error.userMessage.title).foregroundStyle(.secondary)
                } else {
                    Text("No other matches in conversations").foregroundStyle(.secondary)
                }
            }
        } header: {
            SectionLabel("In conversations")
        }
    }

    private func link<Row: View>(_ session: Session, id: String? = nil,
                                 @ViewBuilder row: () -> Row) -> some View {
        Button { path.append(session) } label: { row() }
            .buttonStyle(.plain)
            .compactRow()
            .accessibilityIdentifier(id ?? "session-\(session.id)")
    }
}

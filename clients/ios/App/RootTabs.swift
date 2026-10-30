import SwiftUI
import AimuxKit

/// Everything the tabs read from: the server, or the DEBUG demo data.
typealias AppSource = SessionSource & SearchSource & ActivitySource & ChatSource & SessionMetaSource

enum AppTab: String, Hashable {
    case now, all, search
}

/// Now and All as labelled tabs, search as the separate round button beside them.
struct RootTabs: View {
    @Binding var serverAddress: String
    @State private var list: SessionListModel
    @State private var now: NowModel
    @State private var search: SearchModel
    @State private var tab = DebugHooks.initialTab
    @State private var allFilter = AllFilter(project: DebugHooks.initialProject)
    @State private var drafts = ChatDrafts()
    /// The server's Jira settings for reference links; nil until read, or when it has none.
    @State private var links: HealthLinks?
    /// Whether the health read that gives `links` has succeeded; until then the app retries when it becomes active.
    @State private var linksRead = false
    /// The server keeps a chat queue (health capability `queue`).
    @State private var queueEnabled = false
    @Environment(\.scenePhase) private var scenePhase
    private let source: any AppSource
    private let stream: any EventStreaming

    init(source: any AppSource, stream: any EventStreaming, cache: SessionCache?, limit: Int,
         serverAddress: Binding<String>) {
        self.source = source
        self.stream = stream
        let list = SessionListModel(source: source, cache: cache, limit: limit)
        list.showCached()
        _list = State(initialValue: list)
        _now = State(initialValue: NowModel(list: list, source: source, meta: source))
        _search = State(initialValue: SearchModel(source: source))
        _serverAddress = serverAddress
    }

    var body: some View {
        let chat = ChatContext(source: source, stream: stream, now: now, drafts: drafts, links: links,
                               queueEnabled: queueEnabled)
        TabView(selection: $tab) {
            Tab("Now", systemImage: "record.circle", value: AppTab.now) {
                NowScreen(model: now, chat: chat, serverAddress: $serverAddress) { filter in
                    allFilter = filter
                    tab = .all
                }
            }
            Tab("All", systemImage: "line.3.horizontal", value: AppTab.all) {
                AllScreen(list: list, chat: chat, filter: $allFilter)
            }
            Tab(value: AppTab.search, role: .search) {
                SearchScreen(list: list, search: search, chat: chat)
            }
        }
        .tint(.primary)
        .task { await readLinks() }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active && !linksRead { Task { await readLinks() } }
        }
    }

    private func readLinks() async {
        guard let health = try? await source.health() else { return }
        links = health.links
        queueEnabled = health.supports(.queue)
        linksRead = true
    }
}

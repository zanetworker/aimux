import SwiftUI
import AimuxKit

/// Everything the tabs read from: the server, or the DEBUG demo data.
typealias AppSource = SessionSource & SearchSource & ActivitySource & ChatSource & SessionMetaSource

enum AppTab: String, Hashable {
    case now, all
}

/// Now and All as labelled tabs; each has its own search box.
struct RootTabs: View {
    @Binding var serverAddress: String
    @State private var list: SessionListModel
    @State private var now: NowModel
    /// The conversation search of each screen's box, so one screen's hits never land on the other.
    @State private var nowSearch: SearchModel
    @State private var allSearch: SearchModel
    @State private var tab = DebugHooks.initialTab
    @State private var drafts = ChatDrafts()
    /// The server's Jira settings for reference links; nil until read, or when it has none.
    @State private var links: HealthLinks?
    /// Whether the health read that gives `links` has succeeded; until then the app retries when it becomes active.
    @State private var linksRead = false
    /// The server keeps a chat queue (health capability `queue`).
    @State private var queueEnabled = false
    /// The server offers Send now (health capability `send_now`).
    @State private var sendNowEnabled = false
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
        let now = NowModel(list: list, source: source, meta: source)
        now.query = DebugHooks.initialQuery ?? ""
        _now = State(initialValue: now)
        _nowSearch = State(initialValue: SearchModel(source: source))
        _allSearch = State(initialValue: SearchModel(source: source))
        _serverAddress = serverAddress
    }

    var body: some View {
        let chat = ChatContext(source: source, stream: stream, now: now, drafts: drafts, links: links,
                               queueEnabled: queueEnabled, sendNowEnabled: sendNowEnabled)
        TabView(selection: $tab) {
            Tab("Now", systemImage: "record.circle", value: AppTab.now) {
                NowScreen(model: now, search: nowSearch, chat: chat, serverAddress: $serverAddress)
            }
            Tab("All", systemImage: "line.3.horizontal", value: AppTab.all) {
                AllScreen(list: list, search: allSearch, activity: now.activity, chat: chat)
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
        sendNowEnabled = health.supports(.sendNow)
        linksRead = true
    }
}

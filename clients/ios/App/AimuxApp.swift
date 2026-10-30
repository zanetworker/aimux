import SwiftUI
import AimuxKit

@main
struct AimuxApp: App {
    @AppStorage("serverAddress") private var serverAddress = ""

    var body: some Scene {
        WindowGroup {
            RootView(serverAddress: $serverAddress)
        }
    }
}

struct RootView: View {
    @Binding var serverAddress: String

    var body: some View {
        if let demo = DebugHooks.demoSource, let stream = DebugHooks.demoStream {
            RootTabs(source: demo, stream: stream, cache: nil, limit: 50, serverAddress: $serverAddress)
                .deviceLogin(DebugHooks.demoLogin, serverAddress: $serverAddress)
        } else if let url = ServerAddress.parse(DebugHooks.serverOverride ?? serverAddress) {
            ConnectedRoot(url: url, serverAddress: $serverAddress)
                .id(url)
        } else {
            NavigationStack {
                ServerSettingsView(serverAddress: $serverAddress, isFirstRun: true)
            }
        }
    }
}

/// The tabs for one server, behind its device login.
private struct ConnectedRoot: View {
    let url: URL
    @Binding var serverAddress: String
    @State private var login: DeviceLogin
    @Environment(\.scenePhase) private var scenePhase

    init(url: URL, serverAddress: Binding<String>) {
        self.url = url
        _serverAddress = serverAddress
        _login = State(initialValue: DeviceLogin(baseURL: url, keys: Self.keys))
    }

    /// The simulator has no Secure Enclave; DEBUG builds there use in-memory keys.
    private static var keys: any DeviceKeyStore {
        #if DEBUG && targetEnvironment(simulator)
        SoftwareKeyStore()
        #else
        EnclaveKeyStore()
        #endif
    }

    var body: some View {
        let client = APIClient(baseURL: url, auth: login)
        // All sessions up front (500 load in about 0.3 s), so title search and project counts see everything.
        RootTabs(source: client, stream: EventStream(baseURL: url, auth: login), cache: SessionCache(),
                 limit: SessionListModel.maxLimit, serverAddress: $serverAddress)
            .deviceLogin(login, serverAddress: $serverAddress)
            .task { await readHealth(client) }
            .onDisappear { login.discardUnusedKey() }  // another Mac address
            .onChange(of: scenePhase) { _, phase in
                if phase == .active { Task { await readHealth(client) } }
            }
    }

    /// Whether the server asks for device login; offline, a 401 later brings the right screen anyway.
    private func readHealth(_ client: APIClient) async {
        guard let health = try? await client.health() else { return }
        login.start(health)
    }
}

extension View {
    /// Covers the app with pairing or unlock while the device login needs it; the screens under it keep their
    /// state, and the requests they made wait for the unlock.
    func deviceLogin(_ login: DeviceLogin?, serverAddress: Binding<String>) -> some View {
        modifier(DeviceLoginCover(login: login, serverAddress: serverAddress))
    }
}

private struct DeviceLoginCover: ViewModifier {
    let login: DeviceLogin?
    @Binding var serverAddress: String

    func body(content: Content) -> some View {
        content
            .accessibilityHidden(isCovered)
            .overlay {
                if let login {
                    switch login.state {
                    case .unpaired(let reason): PairingView(login: login, reason: reason, serverAddress: $serverAddress)
                    case .locked: UnlockView(login: login, serverAddress: $serverAddress)
                    case .unknown, .off, .live: EmptyView()
                    }
                }
            }
            .environment(login)
    }

    private var isCovered: Bool {
        switch login?.state {
        case .locked?, .unpaired?: true
        default: false
        }
    }
}

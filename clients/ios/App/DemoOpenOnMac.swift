import Foundation
import Synchronization
import AimuxKit

#if DEBUG
/// Open on Mac in the demos: opens after a moment, and the session then reads as open in a terminal.
enum DemoOpenOnMac {
    private static let opened = Mutex<Set<String>>([])

    static func open(_ id: String) async throws -> OpenOnMacResult {
        try await Task.sleep(for: .seconds(1.5))
        opened.withLock { _ = $0.insert(id) }
        return OpenOnMacResult(state: .openInTerminal)
    }

    static func state(_ id: String, _ listed: SessionState) -> SessionState {
        opened.withLock { $0.contains(id) } ? .openInTerminal : listed
    }
}
#endif

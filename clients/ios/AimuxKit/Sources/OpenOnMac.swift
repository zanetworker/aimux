import Foundation
import Observation

/// Open on Mac: hands a session the Mac runs in the background to a new iTerm2 pane
/// (`POST /v1/sessions/{id}/open-on-mac`). The copy is api/testdata/clients/terminal/open-on-mac.json's,
/// pinned by OpenOnMacTests.
public enum OpenOnMacCopy {
    public static let texts: [String: String] = [
        "action": "Open on Mac",
        "progress": "Opening on your Mac…",
        "done": "Open in the aimux window on your Mac",
        "busy": "Claude is answering. Wait for it to finish, or Stop it first.",
        "open_in_terminal": "Already open in a terminal on your Mac.",
        "no_terminal": "Open on Mac needs iTerm2 on your Mac.",
        "not_authorized": "aimux may not control iTerm2. Allow it in System Settings, Privacy & Security, Automation.",
        "in_use": "Another aimux process is using this session.",
        "not_started": "This session has no messages yet, so there is nothing to open.",
        "timeout": "The pane opened, but Claude did not pick up the session. Check the aimux window on your Mac."
    ]

    public static var action: String { text("action") }
    public static var progress: String { text("progress") }

    /// The server waits up to 20 s for Claude to take the session; the client allows a minute, as the web client.
    public static let requestTimeout: TimeInterval = 60

    /// The text for a failed open: the refusal's own text, else (unknown codes, timeouts, no answer) the timeout text.
    public static func message(for error: Error) -> String {
        guard case APIError.server(_, let code, _)? = error as? APIError, let key = key(code) else {
            return text("timeout")
        }
        return texts[key] ?? text("timeout")
    }

    private static func key(_ code: APIErrorCode) -> String? {
        switch code {
        case .busy: "busy"
        case .openInTerminal: "open_in_terminal"
        case .inUse: "in_use"
        case .unknown(let raw): raw
        default: nil
        }
    }

    private static func text(_ key: String) -> String { texts[key] ?? "" }
}

/// Whether to offer Open on Mac: the server can (health `openOnMac`) and the session is not already in a terminal.
public func canOpenOnMac(_ session: Session, health: Health?) -> Bool {
    canOpenOnMac(state: session.state, health: health)
}

/// The same for a session known by its state (the chat's detail).
public func canOpenOnMac(state: SessionState, health: Health?) -> Bool {
    guard let health, health.openOnMac else { return false }
    return state != .openInTerminal
}

public struct OpenOnMacResult: Decodable, Equatable, Sendable {
    public let state: SessionState

    public init(state: SessionState) {
        self.state = state
    }
}

public protocol OpenOnMacSource: Sendable {
    func openOnMac(sessionID: String) async throws -> OpenOnMacResult
}

extension APIClient: OpenOnMacSource {}

/// The opens in flight, one set for the whole app, so the chat menu and the row menus never send a second
/// open for the same session; and why the last open of each session failed, until it is tried again.
@MainActor @Observable
public final class OpenOnMacModel {
    public private(set) var opening: Set<String> = []
    public private(set) var errors: [String: String] = [:]

    private let source: any OpenOnMacSource

    public init(source: any OpenOnMacSource) {
        self.source = source
    }

    /// True when the session is now in a terminal on the Mac, false when refused (see `errors`), nil when an
    /// open for it was already on its way and nothing was sent.
    public func open(_ sessionID: String) async -> Bool? {
        guard !opening.contains(sessionID) else { return nil }
        opening.insert(sessionID)
        errors[sessionID] = nil
        defer { opening.remove(sessionID) }
        do {
            _ = try await source.openOnMac(sessionID: sessionID)
            return true
        } catch {
            errors[sessionID] = OpenOnMacCopy.message(for: error)
            return false
        }
    }

    public func clearError(_ sessionID: String) {
        errors[sessionID] = nil
    }
}

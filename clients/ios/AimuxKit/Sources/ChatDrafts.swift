import Foundation

/// What a chat keeps per session while the app runs, so closing and reopening it loses nothing:
/// the unsent draft, and a message that failed to send (to retry with the same key).
@MainActor
public final class ChatDrafts {
    private var drafts: [String: String] = [:]
    private var failed: [String: PendingMessage] = [:]

    public init() {}

    /// Nothing is kept for any session.
    public var isEmpty: Bool { drafts.isEmpty && failed.isEmpty }

    /// The unsent text; "" when there is none.
    public func draft(for sessionID: String) -> String { drafts[sessionID] ?? "" }

    /// Keeps the unsent text; an empty one is forgotten.
    public func setDraft(_ text: String, for sessionID: String) {
        drafts[sessionID] = text.isEmpty ? nil : text
    }

    /// The message that failed to send, if any.
    public func pending(for sessionID: String) -> PendingMessage? { failed[sessionID] }

    /// Keeps the message only while it has failed; one sending or accepted is forgotten.
    public func setPending(_ message: PendingMessage?, for sessionID: String) {
        failed[sessionID] = message?.failure == nil ? nil : message
    }
}

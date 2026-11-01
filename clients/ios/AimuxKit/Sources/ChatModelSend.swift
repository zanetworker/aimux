import Foundation

// Sending the composer's message: to the server's session, or typed into the terminal on the Mac (also with
// Send now, during Claude's turn).
extension ChatModel {
    /// Sends a message. The user item shows at once; sending the same text again after a failure
    /// is a retry and keeps the idempotency key, anything else is a new message with a new key.
    /// A read-only session takes no messages; a refusal because it is open in a terminal loads it again.
    public func send(_ text: String) async {
        await send(text, now: false)
    }

    /// Send now for the composer's text: typed into the terminal during Claude's turn.
    public func sendNow(_ text: String) async {
        guard state == .terminal else { return }
        await send(text, now: true)
    }

    func send(_ text: String, now sendNow: Bool) async {
        guard state != .readOnly else { return }
        let key: String
        var sendNow = sendNow
        if let previous = pending, previous.text == text, previous.failure != nil {
            key = previous.key
            sendNow = sendNow || pendingIsNow  // a Retry of a Send now is a Send now
        } else {
            key = UUID().uuidString
        }
        pending = PendingMessage(text: text, key: key, failure: nil, turn: nil, sentAt: now())
        pendingIsNow = sendNow
        sendNowNotice = nil
        if state == .terminal {
            await typeIntoTerminal(text, key: key, now: sendNow)
            return
        }
        do {
            let accepted = try await source.sendMessage(sessionID: sessionID, text: text, key: key)
            guard pending?.key == key else { return }
            pending?.turn = accepted.turn
            clearShownPending()
        } catch {
            guard pending?.key == key else { return }
            let failure = error as? APIError ?? .transport(.unknown)
            pending?.failure = failure
            await queueIfRefusedBusy(failure)
            // Open in a terminal meanwhile: this view is out of date, and a retry could never succeed.
            if case .server(_, .openInTerminal, _) = failure { await load() }
        }
    }
}

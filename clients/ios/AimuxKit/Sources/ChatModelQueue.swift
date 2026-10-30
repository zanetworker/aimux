import Foundation

// The chat's queue on the Mac: added to from the composer (or a refused message, or a lost edit), edited, moved,
// deleted and resumed through the server. What it shows comes from ChatQueue (the shared fixtures).
extension ChatModel {
    /// The composer's main button: Send, or Add to queue while the session works, its terminal cannot take
    /// typing, its queue is sending, or a send was just refused busy.
    public var composerAction: ComposerAction {
        guard queueEnabled else { return .send }
        if state == .terminal {
            return ChatQueue.composerAction(state: session?.state, terminal: session?.terminal, queue: queue,
                                            refusedBusy: refusedBusy)
        }
        return ChatQueue.composerAction(state: result.working ? .busy : .idle, terminal: nil, queue: queue,
                                        refusedBusy: refusedBusy)
    }

    func offerQueueIfBusy(_ failure: APIError) {
        if queueEnabled, ChatQueue.sendRefusal(failure) != nil { refusedBusy = true }
    }

    /// The text of a message refused busy, for an empty composer: the failed message goes away, as its text is
    /// back in the box. Nil when there is none.
    public func reclaimRefusedMessage() -> String? {
        guard queueEnabled, let message = pending, let failure = message.failure,
              ChatQueue.sendRefusal(failure) != nil else { return nil }
        pending = nil
        return message.text
    }

    /// Queues the failed message (its row's Add to queue); it goes away once queued.
    public func queuePending() async -> Bool {
        guard let message = pending, message.failure != nil else { return false }
        guard await addToQueue(message.text) else { return false }
        if pending?.key == message.key { pending = nil }
        return true
    }

    /// Adds `raw` (trimmed) to the end of the queue. False when it was not queued; `addNotice` says why and the
    /// text should stay where it was. A retry of the same text after a failure keeps its idempotency key.
    public func addToQueue(_ raw: String) async -> Bool {
        let text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else {
            addNotice = ChatQueue.noText
            return false
        }
        guard !isAdding else { return false }
        isAdding = true
        defer { isAdding = false }
        addNotice = nil
        let key = failedAdd.flatMap { $0.text == text ? $0.key : nil } ?? UUID().uuidString
        do {
            _ = try await source.addToQueue(sessionID: sessionID, text: text, key: key)
        } catch {
            failedAdd = (text, key)
            addNotice = ChatQueue.errorText(error)
            return false
        }
        failedAdd = nil
        // Queued; if this read fails, the next detail or event shows it.
        if let status = try? await source.queue(sessionID: sessionID) { queue = status }
        if state == .terminal {
            // Read often while the Mac may send it.
            lastTerminalSend = now()
            pollTerminal()
        }
        return true
    }

    /// The composer's text changed: a notice about the earlier text no longer applies.
    public func clearAddNotice() { addNotice = nil }

    /// Saves new text for a waiting message.
    public func editQueueItem(_ id: String, text raw: String) async -> Bool {
        let text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else {
            queueError = ChatQueue.noText
            return false
        }
        let source = source, sessionID = sessionID
        return await changeQueue { try await source.editQueueItem(sessionID: sessionID, itemID: id, text: text) }
    }

    /// Moves a waiting message one place up (-1) or down (+1) among the waiting ones.
    public func moveQueueItem(_ id: String, by delta: Int) async {
        guard let position = ChatQueue.position(in: queue, moving: id, by: delta) else { return }
        await moveQueueItem(id, to: position)
    }

    /// A drag in the list: `onMove` offsets among the rows shown.
    public func moveQueueItem(from source: Int, to destination: Int) async {
        guard let move = ChatQueue.move(in: queue, from: source, to: destination) else { return }
        await moveQueueItem(move.id, to: move.position)
    }

    /// Shows the new order at once; a refusal puts the old one back (unless the queue changed meanwhile).
    private func moveQueueItem(_ id: String, to position: Int) async {
        guard let previous = queue, !isChangingQueue else { return }
        let moved = ChatQueue.moved(previous, id: id, to: position)
        queue = moved
        let source = source, sessionID = sessionID
        let saved = await changeQueue {
            try await source.moveQueueItem(sessionID: sessionID, itemID: id, position: position)
        }
        if !saved && queue == moved { queue = previous }
    }

    public func deleteQueueItem(_ id: String) async {
        let source = source, sessionID = sessionID
        _ = await changeQueue { try await source.deleteQueueItem(sessionID: sessionID, itemID: id) }
    }

    /// The only way a paused queue runs again.
    public func resumeQueue() async {
        let source = source, sessionID = sessionID
        _ = await changeQueue { try await source.resumeQueue(sessionID: sessionID) }
    }

    /// Sends one change; its answer is the queue now. On failure `queueError` says why and nothing changes.
    private func changeQueue(_ request: () async throws -> QueueStatus) async -> Bool {
        guard !isChangingQueue else { return false }
        isChangingQueue = true
        defer { isChangingQueue = false }
        queueError = nil
        do {
            let status = try await request()
            queue = status
            return true
        } catch {
            queueError = ChatQueue.errorText(error)
            return false
        }
    }

    // Lost edits: an edit whose message was sent (or deleted) before it was saved.

    /// Where a lost edit's text goes: returned for an empty composer, else kept in `lostEdits` with its own
    /// Add to queue and Dismiss.
    public func editLost(_ text: String, draftIsEmpty: Bool) -> String? {
        if draftIsEmpty { return text }
        lostEdits.append(LostEdit(text: text))
        return nil
    }

    public func dismissLostEdit(_ id: UUID) {
        lostEdits.removeAll { $0.id == id }
    }

    /// Queues a kept lost edit; it stays offered if the add is refused.
    public func queueLostEdit(_ id: UUID) async -> Bool {
        guard let edit = lostEdits.first(where: { $0.id == id }) else { return false }
        guard await addToQueue(edit.text) else { return false }
        dismissLostEdit(id)
        return true
    }
}

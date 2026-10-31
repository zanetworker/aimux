import Foundation

// A chat on a session open in a terminal on the Mac (state `.terminal`): its history is read again on a timer,
// messages are typed into the terminal, and its permission prompt is answered by pressing an option's key.
extension ChatModel {
    /// Whether a failed message can be tried again now: in a terminal session only while it can be typed into.
    public var canRetryPending: Bool { state != .terminal || session?.terminal?.canType == true }

    /// The Mac's input box holds text and nothing else stops a message: the person may replace that text.
    public var canReplaceBox: Bool {
        guard state == .terminal, let terminal = session?.terminal else { return false }
        return terminal.boxHasText && terminal.prompt == nil && !terminal.working
    }

    /// How long to wait before reading a terminal session again: 2 s while Claude works there or within
    /// 2 minutes of a message typed from here, else 15 s.
    static func terminalPollInterval(busy: Bool, lastSend: Date?, now: Date) -> Duration {
        if busy { return terminalFast }
        if let lastSend, now.timeIntervalSince(lastSend) < terminalFastWindow { return terminalFast }
        return terminalSlow
    }

    /// Reads the terminal session again and again until the chat closes or the session leaves the terminal.
    func pollTerminal() {
        follower?.cancel()
        let sleep = sleep
        follower = Interaction.background { Task { [weak self] in
            while let pause = self?.terminalPause() {
                do { try await sleep(pause) } catch { return }
                guard await self?.readTerminal() == true else { return }
            }
        } }
    }

    /// Nil once polling should stop.
    func terminalPause() -> Duration? {
        guard !closed, !Task.isCancelled, state == .terminal else { return nil }
        return Self.terminalPollInterval(busy: session?.terminal?.isBusy ?? false, lastSend: lastTerminalSend,
                                         now: now())
    }

    /// Reads the session once; a session that left the terminal is loaded again as a live chat. A failed read
    /// keeps what is shown and sets `refreshFailure`. False when there is nothing more to poll (closed, reloaded
    /// meanwhile, or no longer in the terminal).
    func readTerminal() async -> Bool {
        let mine = generation
        let revision = queueRevision
        let detail: SessionDetail
        do {
            detail = try await source.session(id: sessionID, turns: Self.historyTurns)
        } catch {
            guard mine == generation, !closed else { return false }
            refreshFailure = error as? APIError ?? .transport(.unknown)
            return true
        }
        guard mine == generation, !closed else { return false }
        refreshFailure = nil
        guard Self.isTerminal(detail) else {
            await load()
            return false
        }
        adopt(detail, readSince: revision)
        pruneAnsweredPrompts()
        rebuild()
        return true
    }

    /// The reload line's Retry: reads the session now, as something the person asked for.
    public func retryRefresh() async {
        _ = await Interaction.perform { await readTerminal() }
    }

    /// Forgets answers for prompts the screen no longer shows.
    func pruneAnsweredPrompts() {
        guard let showing = session?.terminal?.prompt?.promptId else {
            answeredPrompts = []
            pressKeys = [:]
            return
        }
        answeredPrompts = answeredPrompts.filter { $0 == showing }
        pressKeys = pressKeys.filter { $0.key.hasPrefix(showing + "/") }
    }

    /// Types the message into the terminal. Never retried here: a timeout may have typed part of it already,
    /// so only the person retries (same key; the server refuses with not_at_input if the box is not empty).
    func typeIntoTerminal(_ text: String, key: String, replacing: Bool = false) async {
        do {
            if replacing {
                try await source.terminalReplace(sessionID: sessionID, text: text, key: key)
            } else {
                try await source.terminalSend(sessionID: sessionID, text: text, key: key)
            }
            guard pending?.key == key else { return }
            lastTerminalSend = now()
            clearShownPending()
            pollTerminal()
        } catch {
            guard pending?.key == key else { return }
            let failure = error as? APIError ?? .transport(.unknown)
            pending?.failure = failure
            offerQueueIfBusy(failure)
            // The box is not showing, or a prompt came up before Enter: it shows as a card once read.
            switch failure {
            case .server(_, .notAtInput, _), .server(_, .notSubmitted, _), .server(_, .boxNotCleared, _):
                _ = await readTerminal()
            default: break
            }
        }
    }

    /// Presses one of the terminal prompt's options. A prompt that changed or went away collapses; the screen
    /// is read again either way.
    public func answer(_ prompt: TerminalPrompt, option: TerminalOption) async {
        let id = prompt.promptId
        guard !answering.contains(id) else { return }
        answering.insert(id)
        answerErrors[id] = nil
        defer { answering.remove(id) }
        let slot = "\(id)/\(option.key)"
        let key = pressKeys[slot] ?? UUID().uuidString
        pressKeys[slot] = key
        do {
            try await source.terminalPress(sessionID: sessionID, promptID: id, option: option.key, key: key)
        } catch APIError.server(_, .noPrompt, _), APIError.server(_, .promptChanged, _) {
            // Answered on the Mac, or another prompt is up: this card is stale, not failed.
        } catch let failure as APIError {
            answerErrors[id] = failure
            return
        } catch {
            answerErrors[id] = .transport(.unknown)
            return
        }
        pressKeys[slot] = nil
        answeredPrompts.insert(id)
        rebuild()
        guard state == .terminal else { return }
        _ = await readTerminal()
        // Collapsed only until the screen was read again: the same prompt still showing is asked again.
        answeredPrompts.remove(id)
        rebuild()
    }
}

extension ChatSource {
    /// terminalSend that first clears the text in the Mac's input box (the person asked to replace it). Sources
    /// without Replace refuse it, so nothing is typed over the box.
    public func terminalReplace(sessionID: String, text: String, key: String) async throws {
        throw APIError.http(status: 501)
    }
}

extension ChatModel {
    /// Reads the session again once a turn ends, so the turn's record and the stats come from the session file.
    /// A failure is left to the next load; a load or close meanwhile drops the result.
    func refreshDetail() {
        let mine = generation
        let revision = queueRevision
        let source = source
        let sessionID = sessionID
        _ = Interaction.background { Task { [weak self] in
            guard let detail = try? await source.session(id: sessionID, turns: Self.historyTurns) else { return }
            guard let self, mine == self.generation, !self.closed else { return }
            self.adopt(detail, readSince: revision)
            self.rebuild()
        } }
    }
}

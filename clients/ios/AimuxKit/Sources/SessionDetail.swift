import Foundation

// `ok` is the wire name.
// swiftlint:disable identifier_name

/// One tool a history turn used. `snippet` is its target (a path, a command), "" when the server sent none.
public struct TurnTool: Decodable, Equatable, Hashable, Sendable {
    public let name: String
    public let snippet: String
    public let ok: Bool
    /// Its input, error and edit text; empty when the server sent none.
    public let detail: StepDetail

    public init(name: String, snippet: String, ok: Bool, detail: StepDetail = .empty) {
        self.name = name
        self.snippet = snippet
        self.ok = ok
        self.detail = detail
    }

    private enum CodingKeys: String, CodingKey { case name, snippet, ok, detail }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        name = try container.decode(String.self, forKey: .name)
        snippet = (try? container.decodeIfPresent(String.self, forKey: .snippet)) ?? ""
        ok = try container.decode(Bool.self, forKey: .ok)
        detail = (try? container.decodeIfPresent(StepDetail.self, forKey: .detail)) ?? .empty
    }
}

// swiftlint:enable identifier_name

/// One turn from the session file. `number` orders turns only; it is not an identifier.
public struct Turn: Decodable, Equatable, Hashable, Sendable {
    public let number: Int
    public let time: Date
    public let user: String
    /// Cut at 8 KiB by the server.
    public let reply: String
    public let tools: [TurnTool]

    public init(number: Int, time: Date, user: String, reply: String, tools: [TurnTool]) {
        self.number = number
        self.time = time
        self.user = user
        self.reply = reply
        self.tools = tools
    }

    private enum CodingKeys: String, CodingKey { case number, time, user, reply, tools }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        number = (try? container.decodeIfPresent(Int.self, forKey: .number)) ?? 0
        time = try container.decode(Date.self, forKey: .time)
        user = (try? container.decodeIfPresent(String.self, forKey: .user)) ?? ""
        reply = (try? container.decodeIfPresent(String.self, forKey: .reply)) ?? ""
        tools = (try? container.decodeIfPresent(LossyList<TurnTool>.self, forKey: .tools))?.elements ?? []
    }
}

/// What the session has cost and used so far.
public struct SessionStats: Decodable, Equatable, Sendable {
    public let costUsd: Double
    public let tokensIn: Int
    public let tokensOut: Int
    public let cacheReadTokens: Int
    public let cacheWriteTokens: Int
    public let turns: Int
    /// Absent when the server's index has no start time.
    public let startTime: Date?
    public let durationMin: Double
    public let model: String?
    public let gitBranch: String?
}

/// One numbered choice of a terminal prompt; `key` is what to press ("1" to "9").
public struct TerminalOption: Decodable, Equatable, Hashable, Sendable {
    public let key: String
    public let label: String

    public init(key: String, label: String) {
        self.key = key
        self.label = label
    }
}

/// The permission prompt showing on the terminal's screen. The same prompt keeps its `promptId`.
public struct TerminalPrompt: Decodable, Equatable, Hashable, Sendable {
    public let promptId: String
    public let question: String
    /// What is being approved (the command, or the file and its preview); absent when the screen shows none.
    public let detail: String?
    public let options: [TerminalOption]

    public init(promptId: String, question: String, detail: String? = nil, options: [TerminalOption]) {
        self.promptId = promptId
        self.question = question
        self.detail = detail
        self.options = options
    }

    /// The prompt as the chat's approval card: its options keep their keys as ids; "Yes" is the one-time allow,
    /// another "Yes, ..." the lasting one, "No ..." the reject (the kinds only pick the buttons' looks). The detail
    /// is the card's command, shown in monospace.
    public var approvalItem: ApprovalItem {
        var allowed = false
        let options = options.map { option in
            let label = option.label.lowercased()
            let kind: String
            if label.hasPrefix("no") {
                kind = "reject_once"
            } else if label.hasPrefix("yes") && !allowed {
                kind = "allow_once"
                allowed = true
            } else {
                kind = "allow_always"
            }
            return PermissionOption(optionId: option.key, name: option.label, kind: kind)
        }
        let command = detail.flatMap { $0.isEmpty ? nil : $0 }
        return ApprovalItem(approvalId: promptId, title: question, command: command, options: options, outcome: nil)
    }
}

/// A session open in a terminal on the Mac: whether the phone can type into it now, and why not.
public struct TerminalStatus: Decodable, Equatable, Sendable {
    /// `busy`, `idle` or `unknown` (as sent), from Claude Code's session registry.
    public let status: String
    public let canType: Bool
    /// Why `canType` is false (as sent): `mac_in_use`, `not_at_input`, ...
    public let reason: String?
    public let prompt: TerminalPrompt?
    /// Claude Code is working on a turn there: a direct message is refused `409 busy`; false from older servers.
    public let working: Bool
    /// While working, how long Claude Code's working line says the turn has run; 0 when it shows no time.
    public let workingSeconds: Int

    public init(status: String, canType: Bool, reason: String?, prompt: TerminalPrompt?, working: Bool = false,
                workingSeconds: Int = 0) {
        self.status = status
        self.canType = canType
        self.reason = reason
        self.prompt = prompt
        self.working = working
        self.workingSeconds = workingSeconds
    }

    public var isBusy: Bool { status == "busy" }

    private enum CodingKeys: String, CodingKey { case status, canType, reason, prompt, working, workingSeconds }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        status = (try? container.decodeIfPresent(String.self, forKey: .status)) ?? "unknown"
        canType = try container.decode(Bool.self, forKey: .canType)
        reason = try? container.decodeIfPresent(String.self, forKey: .reason)
        prompt = try? container.decodeIfPresent(TerminalPrompt.self, forKey: .prompt)
        working = (try? container.decodeIfPresent(Bool.self, forKey: .working)) ?? false
        workingSeconds = max(0, (try? container.decodeIfPresent(Int.self, forKey: .workingSeconds)) ?? 0)
    }
}

/// One session and its latest turns (`GET /v1/sessions/{id}`).
public struct SessionDetail: Decodable, Equatable, Sendable {
    public let id: String
    public let provider: String
    public let title: String
    public let project: String
    public let lastActive: Date
    public let state: SessionState
    public let lastPrompt: String?
    /// One or two sentences about the session; absent when there is none, and from older servers.
    public let description: String?
    /// Pinned to the top of Now; false from older servers.
    public let pinned: Bool
    /// Oldest first.
    public let turns: [Turn]
    /// Absent while the session is not in the server's index yet, or when it could not be read.
    public let stats: SessionStats?
    /// Only for a session open in a terminal on the Mac, from a server that can type into it.
    public let terminal: TerminalStatus?
    /// The session's GitHub or GitLab repository, for reference links; absent elsewhere and from older servers.
    public let repo: SessionRepo?
    /// The session's chat queue; absent when it holds no message, and from servers without the queue.
    public let queue: QueueStatus?

    public init(id: String, provider: String = "claude", title: String, project: String, lastActive: Date,
                state: SessionState, lastPrompt: String? = nil, turns: [Turn], stats: SessionStats? = nil,
                terminal: TerminalStatus? = nil, repo: SessionRepo? = nil, queue: QueueStatus? = nil,
                description: String? = nil, pinned: Bool = false) {
        self.id = id
        self.provider = provider
        self.title = title
        self.project = project
        self.lastActive = lastActive
        self.state = state
        self.lastPrompt = lastPrompt
        self.turns = turns
        self.stats = stats
        self.terminal = terminal
        self.repo = repo
        self.queue = queue
        self.description = description
        self.pinned = pinned
    }

    private enum CodingKeys: String, CodingKey {
        case id, provider, title, project, lastActive, state, lastPrompt, turns, stats, terminal, repo, queue
        case description, pinned
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        id = try container.decode(String.self, forKey: .id)
        provider = try container.decode(String.self, forKey: .provider)
        title = try container.decode(String.self, forKey: .title)
        project = try container.decode(String.self, forKey: .project)
        lastActive = try container.decode(Date.self, forKey: .lastActive)
        state = try container.decode(SessionState.self, forKey: .state)
        lastPrompt = try container.decodeIfPresent(String.self, forKey: .lastPrompt)
        turns = try container.decode(LossyList<Turn>.self, forKey: .turns).elements
        stats = try? container.decodeIfPresent(SessionStats.self, forKey: .stats)
        terminal = try? container.decodeIfPresent(TerminalStatus.self, forKey: .terminal)
        repo = try? container.decodeIfPresent(SessionRepo.self, forKey: .repo)
        queue = try? container.decodeIfPresent(QueueStatus.self, forKey: .queue)
        description = try? container.decodeIfPresent(String.self, forKey: .description)
        pinned = (try? container.decodeIfPresent(Bool.self, forKey: .pinned)) ?? false
    }

    /// The title to show: "Session " + the id's first 8 characters when the title is only the id.
    public var displayTitle: String { SessionRowFormat.displayTitle(title, id: id) }
}

import Foundation

/// v1 only adds things, so every enum keeps values it does not know.
public enum SessionState: Equatable, Hashable, Sendable, Codable {
    case idle, openInTerminal, busy, unavailable
    case unknown(String)

    public init(rawValue: String) {
        switch rawValue {
        case "idle": self = .idle
        case "open_in_terminal": self = .openInTerminal
        case "busy": self = .busy
        case "unavailable": self = .unavailable
        default: self = .unknown(rawValue)
        }
    }

    public init(from decoder: Decoder) throws {
        self.init(rawValue: try decoder.singleValueContainer().decode(String.self))
    }

    public var rawValue: String {
        switch self {
        case .idle: "idle"
        case .openInTerminal: "open_in_terminal"
        case .busy: "busy"
        case .unavailable: "unavailable"
        case .unknown(let raw): raw
        }
    }

    public func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(rawValue)
    }

    /// Only idle sessions take a new message; open_in_terminal is read only here.
    public var canContinue: Bool { self == .idle }
}

/// Where a session ran, named by the server (repo, worktree, sub-folder and the label to print).
public struct Folder: Codable, Equatable, Hashable, Sendable {
    public let repo: String
    public let repoPath: String
    public let worktree: String?
    public let sub: String?
    public let label: String

    public init(repo: String, repoPath: String, worktree: String? = nil, sub: String? = nil, label: String) {
        self.repo = repo
        self.repoPath = repoPath
        self.worktree = worktree
        self.sub = sub
        self.label = label
    }
}

public struct MacWindow: Codable, Equatable, Hashable, Sendable {
    public let id: String
    public let index: Int
    public let name: String

    public init(id: String, index: Int, name: String) {
        self.id = id
        self.index = index
        self.name = name
    }
}

public struct MacTab: Codable, Equatable, Hashable, Sendable {
    public let index: Int
    public let name: String

    public init(index: Int, name: String) {
        self.index = index
        self.name = name
    }
}

/// Where a session's claude sits in iTerm2 on the Mac; `panes` counts the tab's panes, shells included.
public struct MacPlace: Codable, Equatable, Hashable, Sendable {
    public let window: MacWindow
    public let tab: MacTab
    public let pane: Int
    public let panes: Int
    public let lastInFront: Bool

    public init(window: MacWindow, tab: MacTab, pane: Int, panes: Int, lastInFront: Bool) {
        self.window = window
        self.tab = tab
        self.pane = pane
        self.panes = panes
        self.lastInFront = lastInFront
    }
}

/// A terminal session waiting for the owner at a permission prompt, from its Claude Code hook.
public struct Waiting: Codable, Equatable, Hashable, Sendable {
    /// "permission".
    public let kind: String
    /// Claude Code's notification line, never the tool's input.
    public let message: String
    public let since: Date

    public init(kind: String = "permission", message: String, since: Date) {
        self.kind = kind
        self.message = message
        self.since = since
    }
}

public struct Session: Codable, Equatable, Hashable, Sendable, Identifiable {
    public let id: String
    public let provider: String
    public let title: String
    /// The session's folder.
    public let project: String
    public let lastActive: Date
    public let state: SessionState
    /// The last message the person typed.
    public let lastPrompt: String?
    /// One or two sentences about the session; absent when there is none, and from older servers.
    public let description: String?
    /// Pinned to the top of Now (aimux's star, shared with the TUI); false from older servers.
    public let pinned: Bool
    /// Where it ran; absent from older servers.
    public let folder: Folder?
    /// Its iTerm2 pane on the Mac; absent when it has none, iTerm2 is not readable, and from older servers.
    public let mac: MacPlace?
    /// Open in a terminal whose Claude the registry calls busy; false when absent.
    public let working: Bool
    /// At a permission prompt in a terminal on the Mac; absent when not, and from older servers.
    public let waiting: Waiting?

    public init(id: String, provider: String = "claude", title: String, project: String, lastActive: Date,
                state: SessionState, lastPrompt: String? = nil, description: String? = nil, pinned: Bool = false,
                folder: Folder? = nil, mac: MacPlace? = nil, working: Bool = false,
                waiting: Waiting? = nil) {
        self.id = id
        self.provider = provider
        self.title = title
        self.project = project
        self.lastActive = lastActive
        self.state = state
        self.lastPrompt = lastPrompt
        self.description = description
        self.pinned = pinned
        self.folder = folder
        self.mac = mac
        self.working = working
        self.waiting = waiting
    }

    private enum CodingKeys: String, CodingKey {
        case id, provider, title, project, lastActive, state, lastPrompt, description, pinned, folder, mac, working
        case waiting
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
        description = try? container.decodeIfPresent(String.self, forKey: .description)
        pinned = (try? container.decodeIfPresent(Bool.self, forKey: .pinned)) ?? false
        folder = try? container.decodeIfPresent(Folder.self, forKey: .folder)
        mac = try? container.decodeIfPresent(MacPlace.self, forKey: .mac)
        working = (try? container.decodeIfPresent(Bool.self, forKey: .working)) ?? false
        waiting = try? container.decodeIfPresent(Waiting.self, forKey: .waiting)
    }

    /// At a permission prompt: `waiting` of kind "permission" (other kinds are not known yet and count as none).
    public var isWaitingForPermission: Bool { waiting?.kind == "permission" }

    /// The title to show: "Session " + the id's first 8 characters when the title is only the id.
    public var displayTitle: String { SessionRowFormat.displayTitle(title, id: id) }

    /// The row's second line: the description trimmed, or nil when it has no text.
    public var descriptionLine: String? {
        let trimmed = description?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? nil : trimmed
    }

    /// This session with the title, description and pin clients show now (a meta or describe answer);
    /// a nil `pinned` keeps the pin.
    public func taking(title: String, description: String?, pinned: Bool?) -> Session {
        Session(id: id, provider: provider, title: title, project: project, lastActive: lastActive, state: state,
                lastPrompt: lastPrompt, description: description, pinned: pinned ?? self.pinned,
                folder: folder, mac: mac, working: working, waiting: waiting)
    }

    /// This session with another Mac place (or none).
    public func placed(_ place: MacPlace?) -> Session {
        Session(id: id, provider: provider, title: title, project: project, lastActive: lastActive, state: state,
                lastPrompt: lastPrompt, description: description, pinned: pinned,
                folder: folder, mac: place, working: working, waiting: waiting)
    }
}

/// The session list; older servers also send `lastOnMac`, which is ignored.
public struct SessionList: Decodable, Equatable, Sendable {
    public let sessions: [Session]

    private enum CodingKeys: String, CodingKey { case sessions }

    public init(sessions: [Session]) {
        self.sessions = sessions
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        sessions = try container.decode(LossyList<Session>.self, forKey: .sessions).elements
    }
}

public struct SearchHit: Decodable, Equatable, Sendable, Identifiable {
    public let session: Session
    /// Where it matched; matches are in [brackets] (see `Snippet`).
    public let snippet: String
    public var id: String { session.id }

    private enum CodingKeys: String, CodingKey { case snippet }

    public init(from decoder: Decoder) throws {
        session = try Session(from: decoder)
        snippet = try decoder.container(keyedBy: CodingKeys.self).decodeIfPresent(String.self, forKey: .snippet) ?? ""
    }
}

struct SearchResults: Decodable {
    let results: [SearchHit]

    private enum CodingKeys: String, CodingKey { case results }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        results = try container.decode(LossyList<SearchHit>.self, forKey: .results).elements
    }
}

public struct Health: Decodable, Equatable, Sendable {
    public enum Capability: String, Sendable {
        case sessions, search
        case newSessions = "new_sessions"
        case deviceLogin = "device_login"
        case queue
        case describe
    }

    public let status: String
    /// The caller's Tailscale login.
    public let login: String
    public let version: String
    public let acpProtocolVersion: Int
    /// Raw strings: unknown capabilities from newer servers are kept, not rejected.
    public let capabilities: [String]
    /// Jira settings for reference links; absent when the server has none, and from older servers.
    public let links: HealthLinks?
    /// Today's automatic naming; present with the `describe` capability.
    public let naming: NamingStatus?

    public init(status: String, login: String, version: String, acpProtocolVersion: Int, capabilities: [String],
                links: HealthLinks? = nil, naming: NamingStatus? = nil) {
        self.status = status
        self.login = login
        self.version = version
        self.acpProtocolVersion = acpProtocolVersion
        self.capabilities = capabilities
        self.links = links
        self.naming = naming
    }

    private enum CodingKeys: String, CodingKey {
        case status, login, version, acpProtocolVersion, capabilities, links, naming
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        status = try container.decode(String.self, forKey: .status)
        login = try container.decode(String.self, forKey: .login)
        version = try container.decode(String.self, forKey: .version)
        acpProtocolVersion = try container.decode(Int.self, forKey: .acpProtocolVersion)
        capabilities = try container.decode([String].self, forKey: .capabilities)
        links = try? container.decodeIfPresent(HealthLinks.self, forKey: .links)
        naming = try? container.decodeIfPresent(NamingStatus.self, forKey: .naming)
    }

    public func supports(_ capability: Capability) -> Bool {
        capabilities.contains(capability.rawValue)
    }
}

/// The answer to a new message: the turn it started and where its events begin.
public struct TurnAccepted: Decodable, Equatable, Sendable {
    public let id: String
    public let turn: String
    /// The events cursor just before the turn.
    public let after: Int
    public let events: String

    public init(id: String, turn: String, after: Int, events: String) {
        self.id = id
        self.turn = turn
        self.after = after
        self.events = events
    }
}

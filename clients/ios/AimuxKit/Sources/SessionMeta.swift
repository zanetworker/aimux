import Foundation

/// Health's automatic naming today: `today` of `limit` sessions; `failing` after three failed calls in a row,
/// `paused` while automatic naming waits after them (Update summary still runs).
public struct NamingStatus: Decodable, Equatable, Sendable {
    public let today: Int
    public let limit: Int
    public let failing: Bool
    public let reason: String?
    public let paused: Bool?

    public init(today: Int, limit: Int, failing: Bool, reason: String?, paused: Bool?) {
        self.today = today
        self.limit = limit
        self.failing = failing
        self.reason = reason
        self.paused = paused
    }
}

/// `PATCH /v1/sessions/{id}/meta`: only the fields set are sent; "" clears the owner's value.
public struct MetaChange: Encodable, Equatable, Sendable {
    public var title: String?
    public var description: String?
    public var pinned: Bool?

    public init(title: String? = nil, description: String? = nil, pinned: Bool? = nil) {
        self.title = title
        self.description = description
        self.pinned = pinned
    }
}

/// The title, description and pin clients show now.
public struct SessionMeta: Decodable, Equatable, Sendable {
    public let title: String
    public let description: String?
    public let pinned: Bool

    public init(title: String, description: String?, pinned: Bool) {
        self.title = title
        self.description = description
        self.pinned = pinned
    }
}

/// Update summary's answer: what clients show now, the fields the model wrote and the ones left as they were.
public struct DescribeResult: Decodable, Equatable, Sendable {
    public let title: String
    public let description: String?
    /// Raw field names ("title", "description"), so a newer server's fields are kept.
    public let changed: [String]
    public let kept: [String]

    public init(title: String, description: String?, changed: [String], kept: [String]) {
        self.title = title
        self.description = description
        self.changed = changed
        self.kept = kept
    }
}

public protocol SessionMetaSource: Sendable {
    func health() async throws -> Health
    func editMeta(sessionID: String, _ change: MetaChange) async throws -> SessionMeta
    func describe(sessionID: String) async throws -> DescribeResult
}

extension APIClient: SessionMetaSource {}

/// The two fields the owner can write.
public enum MetaField: String, Sendable {
    case title, description

    /// The server's limit in characters (code points, after trimming).
    public var limit: Int { self == .title ? 80 : 300 }
    public var label: String { self == .title ? "Title" : "Description" }
}

/// A title or description being edited in the info sheet.
public struct MetaEdit: Equatable, Sendable {
    public let field: MetaField
    /// The value when editing began.
    public let original: String
    public var draft: String

    public init(field: MetaField, original: String) {
        self.field = field
        self.original = original
        draft = original
    }

    /// Takes the field's new text. A typed Return (one newline more than before, nothing else) is left out and
    /// answers true: save now. Pasted lines stay, so the field says why it can't be saved until they are fixed.
    public mutating func type(_ text: String) -> Bool {
        var withoutOne = text
        if text.count == draft.count + 1, let index = text.firstIndex(where: \.isNewline) {
            withoutOne.remove(at: index)
            if withoutOne == draft {
                return true
            }
        }
        draft = text
        return false
    }

    private var trimmed: String { draft.trimmingCharacters(in: .whitespacesAndNewlines) }

    /// Save sends nothing then: an unchanged value never becomes the owner's.
    public var isUnchanged: Bool { trimmed == original.trimmingCharacters(in: .whitespacesAndNewlines) }
    /// Characters as the server counts them.
    public var count: Int { trimmed.unicodeScalars.count }
    /// Why the server would refuse it, or nil.
    public var problem: String? { SessionMetaText.ownerTextError(draft, limit: field.limit) }
    /// What is sent for this field.
    var change: MetaChange {
        field == .title ? MetaChange(title: trimmed) : MetaChange(description: trimmed)
    }
}

/// What a Save of a title or description came to.
public enum MetaSaveOutcome: Equatable, Sendable {
    /// Nothing was sent: the value was unchanged.
    case unchanged
    /// Saved; the line to announce.
    case saved(String)
    /// Not saved; the line to show.
    case failed(String)
}

/// The fixed lines both clients show for titles, descriptions, pins and Update summary
/// (the web client's are in clients/web/webapp/sessionInfo.mjs). The server's text is never shown,
/// except a Save refusal, whose 400 reasons are fixed text about the field.
public enum SessionMetaText {
    public static let updateHint = "Update summary sends up to 1,200 characters of this session to Anthropic."

    private static let describeErrors: [APIErrorCode: String] = [
        .describeRunning: "An update for this session is already running.",
        .ownerSet: "You wrote both the title and the description, so there is nothing to update.",
        .noContent: "This session has no messages to summarise yet.",
        .namingTimeout: "The model took too long. Try again.",
        .namingAuth: "The model API refused the Mac's API key.",
        .namingModel: "The naming model was not found. Check sessions.title_model on the Mac.",
        .namingUnreadable: "The model's answer could not be read. Try again.",
        .namingFailed: "The model call failed. Try again later.",
        .namingUnavailable: "The Mac has no API key and no claude command to make a summary."
    ]

    private static func status(_ error: Error) -> Int? {
        switch error as? APIError {
        case .server(let status, _, _)?, .http(let status)?: status
        default: nil
        }
    }

    public static func describeError(_ error: Error) -> String {
        guard let status = status(error) else { return "Could not reach your Mac." }
        if case .server(_, let code, _)? = error as? APIError, let line = describeErrors[code] { return line }
        return "Could not update the summary (HTTP \(status))."
    }

    /// "Updated the title and description." / "Updated the description. The title stays as it was."
    public static func describeResult(changed: [String]) -> String {
        if changed.isEmpty { return "Nothing changed." }
        if changed.count >= 2 { return "Updated the title and description." }
        let other = changed[0] == "title" ? "description" : "title"
        return "Updated the \(changed[0]). The \(other) stays as it was."
    }

    /// "Named 4 of 20 today", or "Naming paused"; nil without the naming status.
    public static func namingLine(_ naming: NamingStatus?) -> String? {
        guard let naming else { return nil }
        if naming.paused == true { return "Naming paused" }
        return "Named \(naming.today) of \(naming.limit) today"
    }

    /// The line under the sheet's actions: today's naming, then what Update summary sends.
    public static func hint(_ naming: NamingStatus?) -> String {
        namingLine(naming).map { "\($0). \(updateHint)" } ?? updateHint
    }

    /// Why the server would refuse an owner title or description, or nil: it trims, counts characters
    /// (code points) and refuses control characters.
    public static func ownerTextError(_ text: String, limit: Int) -> String? {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.unicodeScalars.count > limit { return "At most \(limit) characters." }
        if trimmed.unicodeScalars.contains(where: { $0.properties.generalCategory == .control }) {
            return "One line, without control characters."
        }
        return nil
    }

    public static func pinError(_ error: Error, pinned: Bool) -> String {
        let verb = pinned ? "pin" : "unpin"
        guard let status = status(error) else { return "Could not \(verb): your Mac could not be reached." }
        return "Could not \(verb) (HTTP \(status))."
    }

    /// What a finished Save announces. An empty value clears only the owner's own: when the shown value comes
    /// back unchanged, it was the model's or Claude Code's, so nothing was cleared.
    public static func saveNote(_ edit: MetaEdit, answer: SessionMeta) -> String {
        let field = edit.field
        let back = ((field == .title ? answer.title : answer.description) ?? "")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        let original = edit.original.trimmingCharacters(in: .whitespacesAndNewlines)
        if edit.count == 0, !back.isEmpty, back == original {
            return "Nothing to clear: this \(field.rawValue) was not written by you."
        }
        return "Saved"
    }

    public static func saveError(_ error: Error) -> String {
        switch error as? APIError {
        case .server(_, _, let message)?: "Could not save: \(message)"
        case .http(let status)?: "Could not save (HTTP \(status))."
        default: "Could not reach your Mac."
        }
    }
}

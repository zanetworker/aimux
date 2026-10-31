import Foundation

/// Folds opened or closed by hand, remembered on the device as a JSON object of key to `true` or `false`.
/// A key with no choice falls back to the screen's own default.
public struct OpenMemory: Equatable, Sendable {
    private let choices: [String: Bool]

    /// Reads the stored text; anything that is not an object of booleans is forgotten.
    public init(_ raw: String) {
        let object = (try? JSONSerialization.jsonObject(with: Data(raw.utf8))) as? [String: Any] ?? [:]
        choices = object.compactMapValues { value in
            guard let number = value as? NSNumber, CFGetTypeID(number) == CFBooleanGetTypeID() else { return nil }
            return number.boolValue
        }
    }

    private init(choices: [String: Bool]) {
        self.choices = choices
    }

    /// The text to store: sorted keys, so the same choices always read the same.
    public var raw: String {
        let data = try? JSONSerialization.data(withJSONObject: choices, options: [.sortedKeys])
        return data.flatMap { String(data: $0, encoding: .utf8) } ?? "{}"
    }

    public func isOpen(_ key: String, otherwise: Bool) -> Bool { choices[key] ?? otherwise }

    public func setting(_ key: String, open: Bool) -> OpenMemory {
        var changed = choices
        changed[key] = open
        return OpenMemory(choices: changed)
    }

    /// A Now tab: "<window name>|<tab name>", best effort across iTerm2 restarts.
    public static func tabKey(window: String, tab: String) -> String { window + "|" + tab }

    /// An All folder: the repo path, or "<repo path>|<worktree>".
    public static func folderKey(repoPath: String, worktree: String? = nil) -> String {
        worktree.map { repoPath + "|" + $0 } ?? repoPath
    }
}

/// What an open session is doing, for the line under its title.
public enum RowState: Equatable, Sendable {
    case working(step: String?)
    case yourTurn(since: String)

    /// "Working · go test ./...", "Working", "Your turn · 4 min".
    public var text: String {
        switch self {
        case .working(let step?): "Working · " + step
        case .working: "Working"
        case .yourTurn(let since): "Your turn · " + since
        }
    }
}

/// Working (Now's test, with activity) or, when open, your turn since its last activity; nil when not open.
public func rowState(_ session: Session, activity: Activity, now: Date, format: SessionRowFormat) -> RowState? {
    if isWorking(session, activity: activity) {
        if case .working(let step) = activity { return .working(step: step) }
        return .working(step: nil)
    }
    guard isOpen(session, activity: activity) else { return nil }
    let since = format.relativeTime(min(session.lastActive, now), now: now)
    return .yourTurn(since: since == "now" ? "just now" : since)
}

/// The quiet line: the folder label (or the project's last component), then "pane N", with "tab N" before it
/// when `withTab` (search results and All, away from the tab's own heading).
public func rowPlace(_ session: Session, withTab: Bool) -> String {
    var parts = [session.folder?.label ?? projectName(session.project)]
    if let mac = session.mac {
        if withTab { parts.append("tab \(mac.tab.index)") }
        parts.append("pane \(mac.pane)")
    }
    return parts.joined(separator: " · ")
}

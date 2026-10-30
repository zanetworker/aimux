import Foundation

/// What a session row shows besides the title: folder name, age and state badge.
public struct SessionRowFormat: Sendable {
    private let locale: Locale
    private let timeZone: TimeZone

    public init(locale: Locale = .current, timeZone: TimeZone = .current) {
        self.locale = locale
        self.timeZone = timeZone
    }

    /// A session with no real title is "Session <uuid>" or the bare id; show "Session " + the id's first 8 characters.
    public static func displayTitle(_ title: String, id: String) -> String {
        let trimmed = title.trimmingCharacters(in: .whitespacesAndNewlines)
        let uuid = "[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"
        let idOnly = trimmed.isEmpty || trimmed == id
            || trimmed.range(of: "^(Session )?" + uuid + "$", options: [.regularExpression, .caseInsensitive]) != nil
        return idOnly ? "Session " + id.prefix(8) : trimmed
    }

    public func projectName(_ path: String) -> String {
        let parts = path.split(separator: "/")
        if let last = parts.last { return String(last) }
        return path
    }

    /// "now", "5 min", "3 h" (same day), "Yesterday", "Oct 20", "Sep 20, 2025".
    public func relativeTime(_ date: Date, now: Date = .now) -> String {
        let seconds = now.timeIntervalSince(date)
        if seconds < 60 { return "now" }
        if seconds < 3600 { return "\(Int(seconds / 60)) min" }
        var calendar = Calendar(identifier: .gregorian)
        calendar.locale = locale
        calendar.timeZone = timeZone
        if calendar.isDate(date, inSameDayAs: now) { return "\(Int(seconds / 3600)) h" }
        if let yesterday = calendar.date(byAdding: .day, value: -1, to: now),
           calendar.isDate(date, inSameDayAs: yesterday) { return "Yesterday" }
        var style = Date.FormatStyle(date: .omitted, time: .omitted,
                                     locale: locale, calendar: calendar, timeZone: timeZone)
            .month(.abbreviated).day()
        if calendar.component(.year, from: date) != calendar.component(.year, from: now) {
            style = style.year()
        }
        return date.formatted(style)
    }

    /// "just now", "4 min ago", "3 h ago"; "Yesterday" and dates as `relativeTime` has them.
    public func since(_ date: Date, now: Date = .now) -> String {
        let text = relativeTime(min(date, now), now: now)
        if text == "now" { return "just now" }
        return text.hasSuffix(" min") || text.hasSuffix(" h") ? text + " ago" : text
    }

    /// No badge for idle sessions, the normal case.
    public func badge(_ state: SessionState) -> String? {
        switch state {
        case .idle: nil
        case .openInTerminal: "On Mac"
        case .busy: "Running"
        case .unavailable: "Not started"
        case .unknown(let raw): raw
        }
    }
}

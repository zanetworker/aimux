import Foundation
import Observation

// Media cards (api/testdata/clients/README.md, "Media"): a paragraph that is only one link to a video, image or
// audio file, a YouTube or a Vimeo video becomes `.media`. Runs after the references pass, on a reply's tree only.
// A port of mediaCards in the web client's references.mjs.

/// The blocks with every paragraph that is only one media link made a card, inside quotes and list items too.
public func mediaCards(_ blocks: [MDBlock]) -> [MDBlock] {
    blocks.map { block in
        switch block {
        case .paragraph(let inlines):
            guard inlines.count == 1, case .link(let href, let content) = inlines[0],
                  let kind = mediaKind(href) else { return block }
            let text = content.map { inline -> String in
                if case .text(let value) = inline { return value }
                return ""
            }.joined()
            return .media(MDMedia(kind: kind, href: href, title: mediaTitle(kind, href: href, text: text)))
        case .list(let list):
            return .list(MDList(ordered: list.ordered, start: list.start, items: list.items.map {
                MDListItem(task: $0.task, blocks: mediaCards($0.blocks))
            }))
        case .quote(let inner):
            return .quote(mediaCards(inner))
        case .heading, .code, .table, .rule, .media:
            return block
        }
    }
}

/// An http(s) address's authority as written, lowercased host (no user or port), path and query.
struct MediaAddress {
    let authority: Substring
    let host: String
    let path: String
    let query: String

    init?(_ href: String) {
        let lower = href.lowercased()
        let scheme = lower.hasPrefix("http://") ? 7 : lower.hasPrefix("https://") ? 8 : 0
        guard scheme > 0 else { return nil }
        let rest = href.dropFirst(scheme)
        let authEnd = rest.firstIndex { $0 == "/" || $0 == "?" || $0 == "#" } ?? rest.endIndex
        var auth = rest[..<authEnd]
        authority = auth
        if let user = auth.lastIndex(of: "@") { auth = auth[auth.index(after: user)...] }
        if let colon = auth.lastIndex(of: ":"), auth[auth.index(after: colon)...].allSatisfy(\.isASCIIDigitChar) {
            auth = auth[..<colon]
        }
        host = auth.lowercased()
        var after = rest[authEnd...]
        if let hash = after.firstIndex(of: "#") { after = after[..<hash] }
        if let mark = after.firstIndex(of: "?") {
            path = String(after[..<mark])
            query = String(after[after.index(after: mark)...])
        } else {
            path = String(after)
            query = ""
        }
    }

    var lastSegment: Substring {
        guard let slash = path.lastIndex(of: "/") else { return Substring(path) }
        return path[path.index(after: slash)...]
    }
}

private extension Character {
    var isASCIIDigitChar: Bool { ("0"..."9").contains(self) }
}

/// The host a card shows: lowercased, without user or port; nil when the address is not http(s).
public func mediaHost(_ href: String) -> String? { MediaAddress(href)?.host }

private let youTubeHosts: Set<String> = ["youtube.com", "www.youtube.com", "m.youtube.com"]
private let vimeoHosts: Set<String> = ["vimeo.com", "www.vimeo.com"]
private let extensionKinds: [String: MediaKind] = [
    "mp4": .video, "mov": .video, "m4v": .video, "webm": .video,
    "png": .image, "jpg": .image, "jpeg": .image, "gif": .image, "webp": .image,
    "mp3": .audio, "m4a": .audio, "wav": .audio
]

/// The kind of media an address points at, or nil.
func mediaKind(_ href: String) -> MediaKind? {
    guard let address = MediaAddress(href), parsedHostMatches(href, address) else { return nil }
    let path = address.path
    func oneSegment(after prefix: String) -> Bool {
        path.hasPrefix(prefix) && path.count > prefix.count && !path.dropFirst(prefix.count).contains("/")
    }
    if youTubeHosts.contains(address.host) {
        let hasVideo = address.query.split(separator: "&", omittingEmptySubsequences: false)
            .contains { $0.hasPrefix("v=") && $0.count > 2 }
        if path == "/watch" && hasVideo { return .youtube }
        if oneSegment(after: "/shorts/") { return .youtube }
    }
    if address.host == "youtu.be" && oneSegment(after: "/") { return .youtube }
    if vimeoHosts.contains(address.host), path.count > 1, path.hasPrefix("/"),
       path.dropFirst().allSatisfy(\.isASCIIDigitChar) {
        return .vimeo
    }
    let segment = address.lastSegment
    guard let dot = segment.lastIndex(of: ".") else { return nil }
    return extensionKinds[segment[segment.index(after: dot)...].lowercased()]
}

/// False for a `\` or `%5C` before the path (a browser reads `\` as `/`, so the host would be another one) and for a
/// host Foundation's URL parser reads differently from the split (README, "Media"): the card would name the wrong host.
private func parsedHostMatches(_ href: String, _ address: MediaAddress) -> Bool {
    let authority = address.authority.lowercased()
    guard !authority.contains("\\"), !authority.contains("%5c"),
          var parsed = URL(string: href)?.host(percentEncoded: false)?.lowercased() else { return false }
    if parsed.contains(":") { parsed = "[" + parsed + "]" } // an IPv6 literal comes back without its brackets
    return parsed == address.host
}

private func mediaTitle(_ kind: MediaKind, href: String, text: String) -> String {
    if text != href { return text }
    switch kind {
    case .youtube: return "YouTube video"
    case .vimeo: return "Vimeo video"
    default:
        // The file name; the card shows the host below it.
        guard let address = MediaAddress(href) else { return href }
        let segment = String(address.lastSegment)
        return segment.removingPercentEncoding ?? segment
    }
}

/// Which media card is playing in the chat: at most one, so starting a card stops any other.
@MainActor @Observable public final class MediaPlayback {
    public private(set) var playing: UUID?

    public init() {}

    public func isPlaying(_ card: UUID) -> Bool { playing == card }

    public func start(_ card: UUID) { playing = card }

    /// The card stopped; true when nothing plays any more, so the audio session can be released (another card
    /// that took over keeps it).
    @discardableResult public func stop(_ card: UUID) -> Bool {
        if playing == card { playing = nil }
        return playing == nil
    }
}

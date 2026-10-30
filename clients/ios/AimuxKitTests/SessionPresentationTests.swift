import Foundation
import Testing
@testable import AimuxKit

@Suite struct SessionRowFormatTests {
    private let format = SessionRowFormat(locale: Locale(identifier: "en_US"), timeZone: TimeZone(identifier: "UTC")!)
    // Sunday 2026-10-25 18:00:00 UTC
    private let now = Date(timeIntervalSince1970: 1_792_951_200)

    @Test(arguments: [
        ("/Users/me/src/aimux-ios", "aimux-ios"),
        ("/Users/me/src/research/", "research"),
        ("/", "/"),
        ("", "")
    ])
    func projectNameIsTheLastFolder(path: String, name: String) {
        #expect(format.projectName(path) == name)
    }

    private static let timeCases: [(Double, String)] = [
        (-30.0, "now"),
        (-90.0, "now"),           // clock skew: a little in the future still reads "now"
        (90.0, "1 min"),
        (59.0, "now"),
        (60.0, "1 min"),
        (3540.0, "59 min"),
        (10_800.0, "3 h"),
        (61_200.0, "17 h"),    // 01:00 the same day
        (68_400.0, "Yesterday"),
        (432_000.0, "Oct 20"),
        (34_560_000.0, "Sep 20, 2025")
    ]

    @Test(arguments: timeCases)
    func relativeTime(secondsAgo: Double, text: String) {
        #expect(format.relativeTime(now.addingTimeInterval(-secondsAgo), now: now) == text)
    }

    @Test func badgeForEachState() {
        #expect(format.badge(.idle) == nil)
        #expect(format.badge(.openInTerminal) == "On Mac")
        #expect(format.badge(.busy) == "Running")
        #expect(format.badge(.unavailable) == "Not started")
        #expect(format.badge(.unknown("hibernating")) == "hibernating")
    }
}

@Suite struct ErrorMessageTests {
    @Test(arguments: [URLError.Code.cannotFindHost, .dnsLookupFailed, .cannotConnectToHost, .timedOut])
    func unreachableMac(code: URLError.Code) {
        #expect(APIError.transport(code).userMessage.title == "Can't reach your Mac")
    }

    @Test func offline() {
        #expect(APIError.transport(.notConnectedToInternet).userMessage.title == "You're offline")
    }

    @Test func aimuxNotRunningBehindTailscale() {
        let msg = APIError.http(status: 502).userMessage
        #expect(msg.title == "aimux isn't running")
        #expect(msg.detail.contains("aimux remote serve"))
    }

    @Test func loginNotAllowedShowsServerText() {
        let msg = APIError.server(status: 403, code: .forbidden, message: "login x is not allowed").userMessage
        #expect(msg.title == "This login isn't allowed")
        #expect(msg.detail.contains("login x is not allowed"))
    }

    @Test func loginRequiredSaysPairAgain() {
        let msg = APIError.server(status: 401, code: .loginRequired, message: "log in with a paired device").userMessage
        #expect(msg.title == "This device is no longer paired")
        #expect(msg.detail.contains("aimux remote pair"))
    }

    @Test func aBare401AsksToTryAgain() {
        let msg = APIError.http(status: 401).userMessage
        #expect(msg.title == "Something went wrong")
        #expect(msg.detail == "Try again.")
    }

    @Test func unknownCodeFallsBackToStatus() {
        let msg = APIError.server(status: 429, code: .unknown("rate_limited"), message: "slow down").userMessage
        #expect(msg.title == "Something went wrong")
        #expect(msg.detail.contains("slow down"))
        #expect(msg.detail.contains("429"))
    }
}

@Suite struct SessionCacheTests {
    private func tempDir() -> URL {
        let dir = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    private let rows = [
        Session(id: "a", provider: "claude", title: "One", project: "/p",
                lastActive: Date(timeIntervalSince1970: 100.25),
                state: .openInTerminal, lastPrompt: "hi"),
        Session(id: "b", provider: "claude", title: "Two", project: "/q", lastActive: Date(timeIntervalSince1970: 200),
                state: .unknown("hibernating"), lastPrompt: nil)
    ]

    @Test func roundTripsSessionsAndSaveTime() throws {
        let cache = SessionCache(directory: tempDir())
        let savedAt = Date(timeIntervalSince1970: 500)
        try cache.save(rows, at: savedAt)
        let loaded = try #require(cache.load())
        #expect(loaded.sessions == rows)
        #expect(loaded.savedAt == savedAt)
    }

    @Test func emptyCacheLoadsNothing() {
        #expect(SessionCache(directory: tempDir()).load() == nil)
    }

    @Test func corruptCacheLoadsNothing() throws {
        let dir = tempDir()
        try Data("{not json".utf8).write(to: dir.appending(path: SessionCache.fileName))
        #expect(SessionCache(directory: dir).load() == nil)
    }
}

@Suite struct DisplayTitleTests {
    private static let id = "d12ce59e-17e0-4534-8027-eac22f02c34f"

    @Test(arguments: [
        ("Session " + id, "Session d12ce59e"),
        ("", "Session d12ce59e"),
        ("   ", "Session d12ce59e"),
        (id, "Session d12ce59e"),
        ("SESSION " + id.uppercased(), "Session d12ce59e"),
        ("  Fix login page  ", "Fix login page"),
        ("Session notes for Monday", "Session notes for Monday")
    ])
    func idOnlyTitlesBecomeShort(title: String, want: String) {
        #expect(SessionRowFormat.displayTitle(title, id: Self.id) == want)
    }
}

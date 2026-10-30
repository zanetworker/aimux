import Foundation
import Testing
@testable import AimuxKit

// Finding sessions: descriptions, pins, Last on your Mac, owner edits and Update summary.

private func json(_ text: String) -> Data { Data(text.utf8) }

@Suite struct FindingSessionsDecodingTests {
    @Test func sessionReadsDescriptionAndPin() throws {
        let list = try APIDecoder.decode(SessionList.self, from: json(
            #"{"sessions":[{"id":"a","provider":"claude","title":"T","project":"/p","# +
            #""lastActive":"2026-10-30T11:00:00Z","state":"idle","description":"  Sorts the jars. ","pinned":true},"# +
            #"{"id":"b","provider":"claude","title":"U","project":"/p","lastActive":"2026-10-30T11:00:00Z","# +
            #""state":"idle"}],"lastOnMac":{"id":"a","at":"2026-10-30T11:55:00Z"}}"#
        ))
        #expect(list.sessions[0].description == "  Sorts the jars. ")
        #expect(list.sessions[0].descriptionLine == "Sorts the jars.")
        #expect(list.sessions[0].pinned)
        // Older servers send neither.
        #expect(list.sessions[1].description == nil)
        #expect(list.sessions[1].descriptionLine == nil)
        #expect(!list.sessions[1].pinned)
        #expect(list.lastOnMac == LastOnMac(id: "a", at: try #require(APIDate.parse("2026-10-30T11:55:00Z"))))
    }

    @Test func listWithoutLastOnMacOrWithABrokenOneHasNone() throws {
        let row = #"{"id":"a","provider":"claude","title":"T","project":"/p","lastActive":"2026-10-30T11:00:00Z","# +
            #""state":"idle"}"#
        let without = try APIDecoder.decode(SessionList.self, from: json(#"{"sessions":["# + row + "]}"))
        #expect(without.lastOnMac == nil)
        let broken = try APIDecoder.decode(SessionList.self, from: json(
            #"{"sessions":["# + row + #"],"lastOnMac":{"id":"a"}}"#))
        #expect(broken.lastOnMac == nil)
        #expect(broken.sessions.map(\.id) == ["a"])
    }

    @Test func detailReadsDescriptionAndPin() throws {
        let detail = try APIDecoder.decode(SessionDetail.self, from: json(
            #"{"id":"s1","provider":"claude","title":"T","project":"/p","lastActive":"2026-10-30T11:00:00Z","# +
            #""state":"idle","turns":[],"description":"Finds the loop.","pinned":true}"#))
        #expect(detail.description == "Finds the loop.")
        #expect(detail.pinned)
        let old = try APIDecoder.decode(SessionDetail.self, from: json(
            #"{"id":"s1","provider":"claude","title":"T","project":"/p","lastActive":"2026-10-30T11:00:00Z","# +
            #""state":"idle","turns":[]}"#))
        #expect(old.description == nil)
        #expect(!old.pinned)
    }

    @Test func healthReadsDescribeAndNaming() throws {
        let health = try APIDecoder.decode(Health.self, from: json(
            #"{"status":"ok","login":"a","version":"v","acpProtocolVersion":1,"# +
            #""capabilities":["sessions","describe"],"# +
            #""naming":{"today":4,"limit":20,"failing":true,"reason":"naming_auth","paused":true}}"#))
        #expect(health.supports(.describe))
        #expect(health.naming == NamingStatus(today: 4, limit: 20, failing: true, reason: "naming_auth", paused: true))
        let old = try APIDecoder.decode(Health.self, from: json(
            #"{"status":"ok","login":"a","version":"v","acpProtocolVersion":1,"capabilities":["sessions"]}"#))
        #expect(!old.supports(.describe))
        #expect(old.naming == nil)
    }

    @Test func describeErrorCodesAreKnown() {
        let codes: [String: APIErrorCode] = [
            "owner_set": .ownerSet, "describe_running": .describeRunning, "no_content": .noContent,
            "naming_timeout": .namingTimeout, "naming_auth": .namingAuth, "naming_model": .namingModel,
            "naming_unreadable": .namingUnreadable, "naming_failed": .namingFailed,
            "naming_unavailable": .namingUnavailable
        ]
        for (raw, code) in codes { #expect(APIErrorCode(rawValue: raw) == code) }
    }
}

@Suite struct FindingSessionsClientTests {
    private func client(_ host: String) -> APIClient {
        APIClient(baseURL: URL(string: "https://\(host):8443")!, session: StubProtocol.session())
    }

    private func body(_ request: URLRequest) throws -> [String: Any] {
        let data = try #require(request.httpBody ?? request.httpBodyStream.map(readAll))
        return try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    @Test func editMetaPatchesOnlyTheGivenFields() async throws {
        let host = "meta.test"
        StubProtocol.stub(host: host, .success((200, json(#"{"title":"New name","pinned":true}"#))))
        let meta = try await client(host).editMeta(sessionID: "s1", MetaChange(title: "New name"))
        #expect(meta == SessionMeta(title: "New name", description: nil, pinned: true))
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.httpMethod == "PATCH")
        #expect(request.url?.path() == "/v1/sessions/s1/meta")
        let sent = try body(request)
        #expect(Set(sent.keys) == ["title"])
        #expect(sent["title"] as? String == "New name")
    }

    @Test func pinSendsOnlyPinnedAndNoInteractionHeader() async throws {
        let host = "pin.test"
        StubProtocol.stub(host: host, .success((200, json(#"{"title":"T","description":"D","pinned":false}"#))))
        // Asked for by the person, but a PATCH counts as use on the server by itself.
        let meta = try await Interaction.perform {
            try await client(host).editMeta(sessionID: "s1", MetaChange(pinned: false))
        }
        #expect(meta == SessionMeta(title: "T", description: "D", pinned: false))
        let request = try #require(StubProtocol.requests(host: host).first)
        let sent = try body(request)
        #expect(Set(sent.keys) == ["pinned"])
        #expect(sent["pinned"] as? Bool == false)
        #expect(request.value(forHTTPHeaderField: "Aimux-Interaction") == nil)
    }

    @Test func getsTheyAskedForStillCarryTheInteractionHeader() async throws {
        let host = "meta-get.test"
        StubProtocol.stub(host: host, .success((200, json(#"{"sessions":[]}"#))))
        _ = try await Interaction.perform { try await client(host).sessions(limit: 5) }
        #expect(StubProtocol.requests(host: host).first?.value(forHTTPHeaderField: "Aimux-Interaction") == "1")
    }

    @Test func describePostsAndReadsWhatChanged() async throws {
        let host = "describe.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"title":"Fix the login loop","description":"Finds why login loops.","changed":["description"],"# +
            #""kept":["title"]}"#))))
        let result = try await client(host).describe(sessionID: "s1")
        #expect(result == DescribeResult(title: "Fix the login loop", description: "Finds why login loops.",
                                         changed: ["description"], kept: ["title"]))
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path() == "/v1/sessions/s1/describe")
    }

    @Test func describeRefusalIsTyped() async {
        let host = "describe-running.test"
        StubProtocol.stub(host: host, .success((409, json(#"{"error":"already running","code":"describe_running"}"#))))
        await #expect(throws: APIError.server(status: 409, code: .describeRunning, message: "already running")) {
            try await client(host).describe(sessionID: "s1")
        }
    }

    @Test func listCarriesLastOnMac() async throws {
        let host = "last-on-mac.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"sessions":[{"id":"a","provider":"claude","title":"T","project":"/p","# +
            #""lastActive":"2026-10-30T11:00:00Z","state":"idle"}],"lastOnMac":{"id":"a","at":"2026-10-30T11:55:00Z"}}"#
        ))))
        let list = try await client(host).sessionList(limit: 9)
        #expect(list.lastOnMac?.id == "a")
        #expect(StubProtocol.requests(host: host).first?.url?.query() == "limit=9")
    }
}

@Suite struct SessionMetaTextTests {
    @Test func describeErrorsAreFixedLines() {
        func line(_ code: APIErrorCode, _ status: Int = 502) -> String {
            SessionMetaText.describeError(APIError.server(status: status, code: code, message: "server words"))
        }
        #expect(line(.describeRunning, 409) == "An update for this session is already running.")
        #expect(line(.ownerSet, 409) == "You wrote both the title and the description, so there is nothing to update.")
        #expect(line(.noContent, 409) == "This session has no messages to summarise yet.")
        #expect(line(.namingTimeout, 504) == "The model took too long. Try again.")
        #expect(line(.namingAuth) == "The model API refused the Mac's API key.")
        #expect(line(.namingModel) == "The naming model was not found. Check sessions.title_model on the Mac.")
        #expect(line(.namingUnreadable) == "The model's answer could not be read. Try again.")
        #expect(line(.namingFailed) == "The model call failed. Try again later.")
        #expect(line(.namingUnavailable, 503) == "The Mac has no API key and no claude command to make a summary.")
        #expect(line(.internal, 500) == "Could not update the summary (HTTP 500).")
        #expect(SessionMetaText.describeError(APIError.http(status: 502)) == "Could not update the summary (HTTP 502).")
        #expect(SessionMetaText.describeError(APIError.transport(.notConnectedToInternet))
                == "Could not reach your Mac.")
    }

    @Test func describeResultSaysWhatChanged() {
        #expect(SessionMetaText.describeResult(changed: []) == "Nothing changed.")
        #expect(SessionMetaText.describeResult(changed: ["title", "description"])
                == "Updated the title and description.")
        #expect(SessionMetaText.describeResult(changed: ["description"])
                == "Updated the description. The title stays as it was.")
        #expect(SessionMetaText.describeResult(changed: ["title"])
                == "Updated the title. The description stays as it was.")
    }

    @Test func namingLineAndHint() {
        #expect(SessionMetaText.namingLine(nil) == nil)
        let named = NamingStatus(today: 4, limit: 20, failing: false, reason: nil, paused: nil)
        #expect(SessionMetaText.namingLine(named) == "Named 4 of 20 today")
        let paused = NamingStatus(today: 4, limit: 20, failing: true, reason: "naming_auth", paused: true)
        #expect(SessionMetaText.namingLine(paused) == "Naming paused")
        #expect(SessionMetaText.hint(named) == "Named 4 of 20 today. Update summary sends up to 1,200 characters "
                + "of this session to Anthropic.")
        #expect(SessionMetaText.hint(nil)
                == "Update summary sends up to 1,200 characters of this session to Anthropic.")
    }

    @Test func ownerTextIsCheckedLikeTheServer() {
        // Trimmed, counted in code points, inclusive limit.
        #expect(SessionMetaText.ownerTextError("  " + String(repeating: "a", count: 80) + " ", limit: 80) == nil)
        #expect(SessionMetaText.ownerTextError(String(repeating: "a", count: 81), limit: 80)
                == "At most 80 characters.")
        #expect(SessionMetaText.ownerTextError(String(repeating: "👍🏽", count: 40), limit: 80) == nil)
        #expect(SessionMetaText.ownerTextError(String(repeating: "👍🏽", count: 41), limit: 80)
                == "At most 80 characters.")
        #expect(SessionMetaText.ownerTextError("two\nlines", limit: 80) == "One line, without control characters.")
        #expect(SessionMetaText.ownerTextError("", limit: 80) == nil)
    }

    @Test func pinAndSaveErrors() {
        #expect(SessionMetaText.pinError(APIError.transport(.timedOut), pinned: true)
                == "Could not pin: your Mac could not be reached.")
        let gone = APIError.server(status: 404, code: .unknownSession, message: "x")
        #expect(SessionMetaText.pinError(gone, pinned: false)
                == "Could not unpin (HTTP 404).")
        #expect(SessionMetaText.saveError(APIError.server(status: 400, code: .badRequest, message: "title too long"))
                == "Could not save: title too long")
        #expect(SessionMetaText.saveError(APIError.http(status: 502)) == "Could not save (HTTP 502).")
        #expect(SessionMetaText.saveError(APIError.transport(.timedOut)) == "Could not reach your Mac.")
    }
}

@Suite struct MetaEditTests {
    @Test func unchangedIgnoresSpaceAtTheEnds() {
        var edit = MetaEdit(field: .title, original: "Fix the login loop")
        #expect(edit.isUnchanged)
        edit.draft = " Fix the login loop  "
        #expect(edit.isUnchanged)
        edit.draft = "Fix the login loops"
        #expect(!edit.isUnchanged)
        edit.draft = ""
        #expect(!edit.isUnchanged, "empty restores the automatic value")
    }

    @Test func countAndProblemFollowTheFieldLimit() {
        var edit = MetaEdit(field: .description, original: "")
        edit.draft = "  " + String(repeating: "b", count: 300) + "  "
        #expect(edit.count == 300)
        #expect(edit.problem == nil)
        edit.draft = String(repeating: "b", count: 301)
        #expect(edit.count == 301)
        #expect(edit.problem == "At most 300 characters.")
        #expect(MetaField.title.limit == 80)
    }
}

@Suite struct MetaEditTypingTests {
    /// `#expect` cannot call a mutating method itself.
    private func type(_ edit: inout MetaEdit, _ text: String) -> Bool { edit.type(text) }

    @Test func aTypedReturnAsksToSaveWithoutTheNewline() {
        var edit = MetaEdit(field: .title, original: "Fix the login")
        #expect(!type(&edit, "Fix the login loop"))
        #expect(type(&edit, "Fix the login loop\n"))
        #expect(edit.draft == "Fix the login loop")
        // Return in the middle of the text too.
        #expect(type(&edit, "Fix the\n login loop"))
        #expect(edit.draft == "Fix the login loop")
    }

    @Test func pastedLinesStayAndAreRefusedUntilFixed() {
        var edit = MetaEdit(field: .description, original: "")
        #expect(!type(&edit, "First line\nSecond line"))
        #expect(edit.draft == "First line\nSecond line")
        #expect(edit.problem == "One line, without control characters.")
        // A Return typed while the pasted lines are still there does not save either.
        #expect(!type(&edit, "First line\nSecond line\n"))
    }
}

@Suite struct LocalSearchTests {
    private func session(_ id: String, title: String, description: String? = nil, project: String = "/src/x",
                         prompt: String? = nil) -> Session {
        Session(id: id, title: title, project: project, lastActive: Date(timeIntervalSince1970: 0), state: .idle,
                lastPrompt: prompt, description: description)
    }

    @Test func titlesAndDescriptionsComeFirstInListOrder() {
        let sessions = [
            session("a", title: "Garden notes", project: "/src/login"),
            session("b", title: "Fix the redirect", description: "Finds why the login page loops."),
            session("c", title: "Login page", prompt: "x"),
            session("d", title: "Other", prompt: "check the login")
        ]
        #expect(localMatches(sessions, query: "login").map(\.id) == ["b", "c", "a", "d"])
    }

    @Test func everyWordMustMatchAndEmptyMatchesNothing() {
        let sessions = [session("a", title: "Login", description: "loops back"), session("b", title: "Login")]
        #expect(localMatches(sessions, query: "LOGIN loops").map(\.id) == ["a"])
        #expect(localMatches(sessions, query: "   ").isEmpty)
    }
}

@Suite struct SinceTextTests {
    private let format = SessionRowFormat(locale: Locale(identifier: "en_US"), timeZone: TimeZone(identifier: "UTC")!)
    // Sunday 2026-10-25 18:00:00 UTC
    private let now = Date(timeIntervalSince1970: 1_792_951_200)

    @Test(arguments: [
        (-120.0, "just now"),  // a future time counts as now
        (30.0, "just now"),
        (240.0, "4 min ago"),
        (10_800.0, "3 h ago"),
        (68_400.0, "Yesterday")
    ])
    func lastOnMacSaysHowLongAgo(secondsAgo: Double, text: String) {
        #expect(format.since(now.addingTimeInterval(-secondsAgo), now: now) == text)
    }
}

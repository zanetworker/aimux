import Foundation
import Testing
@testable import AimuxKit

/// api/testdata/clients/terminal/open-on-mac.json: the copy and the refusal codes both clients use.
private struct OpenOnMacFixture: Decodable {
    let copy: [String: String]
    let codes: [String: Int]
}

private final class OpenOnMacBundleToken {}

private func openOnMacFixture() throws -> OpenOnMacFixture {
    let bundle = Bundle(for: OpenOnMacBundleToken.self)
    let url = try #require(bundle.url(forResource: "open-on-mac", withExtension: "json",
                                      subdirectory: "clients/terminal"))
    return try JSONDecoder().decode(OpenOnMacFixture.self, from: Data(contentsOf: url))
}

private func session(_ state: SessionState) -> Session {
    Session(id: "s1", title: "T", project: "/p", lastActive: Date(timeIntervalSince1970: 0), state: state)
}

private func health(openOnMac: Bool?) throws -> Health {
    let flag = openOnMac.map { #","openOnMac":\#($0)"# } ?? ""
    let json = #"{"status":"ok","login":"l","version":"v","acpProtocolVersion":1,"capabilities":[]\#(flag)}"#
    return try APIDecoder.decode(Health.self, from: Data(json.utf8))
}

@Suite struct OpenOnMacCopyTests {
    @Test func copyEqualsTheFixture() throws {
        let fixture = try openOnMacFixture()
        #expect(OpenOnMacCopy.texts == fixture.copy)
        #expect(OpenOnMacCopy.action == fixture.copy["action"])
        #expect(OpenOnMacCopy.progress == fixture.copy["progress"])
    }

    @Test func everyRefusalCodeMapsToItsText() throws {
        let fixture = try openOnMacFixture()
        let codes = fixture.codes.filter { $0.key != "unknown_session" }
        #expect(codes.count == 7)
        for (code, status) in codes {
            let error = APIError.server(status: status, code: APIErrorCode(rawValue: code), message: "refused")
            #expect(OpenOnMacCopy.message(for: error) == fixture.copy[code], "code \(code)")
        }
    }

    @Test func unknownCodesAndTimeoutsUseTheTimeoutText() throws {
        let timeout = try #require(try openOnMacFixture().copy["timeout"])
        #expect(OpenOnMacCopy.message(for: APIError.transport(.timedOut)) == timeout)
        let unknown = APIError.server(status: 500, code: .unknown("later"), message: "")
        #expect(OpenOnMacCopy.message(for: unknown) == timeout)
        #expect(OpenOnMacCopy.message(for: APIError.http(status: 502)) == timeout)
    }
}

@Suite struct CanOpenOnMacTests {
    @Test func offeredWhenTheServerCanAndTheSessionIsNotInATerminal() throws {
        #expect(canOpenOnMac(session(.idle), health: try health(openOnMac: true)))
        #expect(canOpenOnMac(session(.busy), health: try health(openOnMac: true)))
    }

    @Test func hiddenForASessionAlreadyInATerminal() throws {
        #expect(!canOpenOnMac(session(.openInTerminal), health: try health(openOnMac: true)))
    }

    @Test func hiddenWithoutTheHealthFlag() throws {
        #expect(!canOpenOnMac(session(.idle), health: try health(openOnMac: false)))
        #expect(!canOpenOnMac(session(.idle), health: try health(openOnMac: nil)))
        #expect(!canOpenOnMac(session(.idle), health: nil))
    }

    @Test func theChatAsksByTheDetailState() throws {
        #expect(canOpenOnMac(state: .idle, health: try health(openOnMac: true)))
        #expect(!canOpenOnMac(state: .openInTerminal, health: try health(openOnMac: true)))
        #expect(!canOpenOnMac(state: .idle, health: nil))
    }

    @Test func healthDecodesTheFlag() throws {
        #expect(try health(openOnMac: true).openOnMac)
        #expect(try !health(openOnMac: nil).openOnMac)
    }
}

@Suite struct OpenOnMacAPITests {
    private func client(_ host: String) -> APIClient {
        APIClient(baseURL: URL(string: "https://\(host):8443")!, session: StubProtocol.session())
    }

    @Test func postsToTheSessionRoute() async throws {
        let host = "openonmac-ok.test"
        StubProtocol.stub(host: host, .success((200, Data(#"{"state":"open_in_terminal"}"#.utf8))))
        let result = try await client(host).openOnMac(sessionID: "3f2a")
        #expect(result.state == .openInTerminal)
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path() == "/v1/sessions/3f2a/open-on-mac")
        #expect(request.timeoutInterval == OpenOnMacCopy.requestTimeout)
    }

    @Test func busyRefusalMapsToTheBusyText() async throws {
        let host = "openonmac-busy.test"
        StubProtocol.stub(host: host, .success((409, Data(#"{"error":"session busy","code":"busy"}"#.utf8))))
        let fixture = try openOnMacFixture()
        do {
            _ = try await client(host).openOnMac(sessionID: "3f2a")
            Issue.record("expected a refusal")
        } catch {
            #expect(error as? APIError == .server(status: 409, code: .busy, message: "session busy"))
            #expect(OpenOnMacCopy.message(for: error) == fixture.copy["busy"])
        }
    }
}

/// Counts the opens; with `held`, each waits until `release()`.
private actor FakeOpener: OpenOnMacSource {
    var calls: [String] = []
    private let failure: APIError?
    private var waiting: [CheckedContinuation<Void, Never>] = []
    private var released: Bool

    init(failure: APIError? = nil, held: Bool = false) {
        self.failure = failure
        released = !held
    }

    func openOnMac(sessionID: String) async throws -> OpenOnMacResult {
        calls.append(sessionID)
        if !released { await withCheckedContinuation { waiting.append($0) } }
        if let failure { throw failure }
        return OpenOnMacResult(state: .openInTerminal)
    }

    func release() {
        released = true
        waiting.forEach { $0.resume() }
        waiting = []
    }
}

@MainActor @Suite struct OpenOnMacModelTests {
    @Test func successClearsPendingAndReportsTrue() async {
        let model = OpenOnMacModel(source: FakeOpener())
        let result = await model.open("a")
        #expect(result == true)
        #expect(model.opening.isEmpty)
        #expect(model.errors["a"] == nil)
    }

    @Test func refusalKeepsItsTextForTheSession() async {
        let model = OpenOnMacModel(source: FakeOpener(failure: .server(status: 409, code: .busy, message: "")))
        let result = await model.open("a")
        #expect(result == false)
        #expect(model.errors["a"] == OpenOnMacCopy.texts["busy"])
        #expect(model.opening.isEmpty)
    }

    @Test func aSecondTapWhileOpeningSendsNothing() async {
        let source = FakeOpener(held: true)
        let model = OpenOnMacModel(source: source)
        let first = Task { await model.open("a") }
        while model.opening.isEmpty { await Task.yield() }
        #expect(model.opening == ["a"])
        let second = await model.open("a")
        #expect(second == nil)
        await source.release()
        #expect(await first.value == true)
        #expect(await source.calls == ["a"])
        #expect(model.opening.isEmpty)
    }

    @Test func aNewTryClearsTheLastError() async {
        let model = OpenOnMacModel(source: FakeOpener(failure: .server(status: 503, code: .unknown("no_terminal"),
                                                                       message: "")))
        _ = await model.open("a")
        #expect(model.errors["a"] == OpenOnMacCopy.texts["no_terminal"])
        model.clearError("a")
        #expect(model.errors["a"] == nil)
    }
}

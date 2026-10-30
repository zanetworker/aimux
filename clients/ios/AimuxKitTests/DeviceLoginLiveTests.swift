import Foundation
import Testing
@testable import AimuxKit

/// Against a real `aimux remote serve` (scratch HOME, `hostnames: [localhost]`), with a code from
/// `aimux remote pair`. Skipped unless both are given:
/// `TEST_RUNNER_AIMUX_E2E_URL=http://localhost:7393 TEST_RUNNER_AIMUX_E2E_CODE=XXXX-XXXX xcodebuild test ...`
/// The test sends the Tailscale-User-Login header that tailscale serve would add (allowed login me@example.com).
@MainActor @Suite(.enabled(if: ProcessInfo.processInfo.environment["AIMUX_E2E_URL"] != nil))
struct DeviceLoginLiveTests {
    @Test func pairsLocksUnlocksAndRetriesAgainstTheRealServer() async throws {
        let environment = ProcessInfo.processInfo.environment
        let url = try #require(environment["AIMUX_E2E_URL"].flatMap(URL.init(string:)))
        let code = try #require(environment["AIMUX_E2E_CODE"])
        let config = URLSessionConfiguration.ephemeral
        config.httpAdditionalHeaders = ["Tailscale-User-Login": "me@example.com"]
        let session = URLSession(configuration: config)
        let defaults = try #require(UserDefaults(suiteName: "aimux-live-\(UUID().uuidString)"))
        let login = DeviceLogin(baseURL: url, session: session, keys: SoftwareKeyStore(), defaults: defaults)
        let client = APIClient(baseURL: url, session: session, auth: login)

        let health = try await client.health()
        #expect(health.supports(.deviceLogin))
        login.start(health)
        #expect(login.state == .unpaired(nil))

        await login.pair(code: code.lowercased())  // as typed: lower case, with the dash
        #expect(login.message == nil)
        #expect(login.state == .live)
        _ = try await client.sessions(limit: 5)

        // Lock ends the session on the Mac; a request waits, the unlock signs a challenge, the request goes on.
        await login.lock()
        #expect(login.state == .locked)
        let request = Task { try await client.sessions(limit: 5) }
        try await Task.sleep(for: .milliseconds(200))
        await login.unlock()
        #expect(login.message == nil)
        #expect(login.state == .live)
        _ = try await request.value

        // The stream takes the Bearer token: still open after a second (without it the server answers 401 at once).
        let stream = EventStream(baseURL: url, session: session, auth: login)
        let follow = Task { for try await _ in stream.events(sessionID: "no-such-session", after: 0, log: "") {} }
        try await Task.sleep(for: .seconds(1))
        follow.cancel()
        let ended = await follow.result
        if case .failure(let error) = ended { #expect(!(error is APIError), "the stream failed: \(error)") }
        #expect(login.state == .live)
        let unsigned = EventStream(baseURL: url, session: session)
        await #expect(throws: loginRequiredError) {
            for try await _ in unsigned.events(sessionID: "no-such-session", after: 0, log: "") {}
        }
        await login.lock()
    }

    private let loginRequiredError = APIError.server(status: 401, code: .loginRequired,
                                                     message: "log in with a paired device")
}

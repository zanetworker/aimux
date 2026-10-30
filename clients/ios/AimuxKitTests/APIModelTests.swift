import Foundation
import Testing
@testable import AimuxKit

private final class FixtureToken {}

private func fixture(_ name: String) throws -> Data {
    let url = try #require(Bundle(for: FixtureToken.self).url(forResource: name, withExtension: "json"))
    return try Data(contentsOf: url)
}

@Suite struct DateParsingTests {
    @Test(arguments: [
        ("2026-10-25T18:35:27.606Z", 1_792_953_327.606),
        ("2026-10-25T18:35:03.34Z", 1_792_953_303.34),
        ("2026-10-25T18:32:44Z", 1_792_953_164.0),
        ("2026-10-25T20:35:27.123456789+02:00", 1_792_953_327.123456789)
    ])
    func parsesServerTimestamps(text: String, epoch: Double) throws {
        let date = try #require(APIDate.parse(text))
        #expect(abs(date.timeIntervalSince1970 - epoch) < 0.001)
    }

    @Test(arguments: ["yesterday", "", "2026-10-25", "2026-13-45T99:00:00Z"])
    func rejectsNonTimestamps(text: String) {
        #expect(APIDate.parse(text) == nil)
    }
}

@Suite struct SessionListDecodingTests {
    @Test func decodesRowsAndSkipsBrokenOnes() throws {
        let list = try APIDecoder.decode(SessionList.self, from: fixture("sessions"))
        // Five rows on the wire; the one without an id is dropped, the rest survive.
        #expect(list.sessions.map(\.id) == [
            "d12ce59e-17e0-4534-8027-eac22f02c34f",
            "6a320ebb-c7a5-46c5-a8d2-6d4723471b56",
            "a17fd8be-5fbe-47df-bba0-cbc38aec9675",
            "11111111-2222-3333-4444-555555555555"
        ])
    }

    @Test func mapsKnownAndUnknownStates() throws {
        let states = try APIDecoder.decode(SessionList.self, from: fixture("sessions")).sessions.map(\.state)
        #expect(states == [.openInTerminal, .idle, .busy, .unknown("hibernating")])
    }

    @Test func keepsOptionalLastPromptAndDates() throws {
        let rows = try APIDecoder.decode(SessionList.self, from: fixture("sessions")).sessions
        #expect(rows[0].lastPrompt == "Read the handoff and propose the app structure")
        #expect(rows[2].lastPrompt == nil)
        #expect(abs(rows[1].lastActive.timeIntervalSince1970 - 1_792_953_303.34) < 0.001)
        #expect(rows[0].project == "/Users/me/src/aimux-ios")
    }

    @Test func onlyIdleSessionsCanBeContinued() {
        #expect(SessionState.idle.canContinue)
        #expect(!SessionState.openInTerminal.canContinue)
        #expect(!SessionState.busy.canContinue)
        #expect(!SessionState.unknown("hibernating").canContinue)
    }
}

@Suite struct HealthDecodingTests {
    @Test func decodesHealthAndKeepsUnknownCapabilities() throws {
        let health = try APIDecoder.decode(Health.self, from: fixture("health"))
        #expect(health.login == "someone@example.com")
        #expect(health.acpProtocolVersion == 1)
        #expect(health.supports(.search))
        #expect(health.supports(.newSessions))
        #expect(health.capabilities.contains("push"))
    }

    @Test func missingCapabilityIsUnsupported() throws {
        let json = #"{"status":"ok","login":"a","version":"v","acpProtocolVersion":1,"capabilities":["sessions"]}"#
        let health = try APIDecoder.decode(Health.self, from: Data(json.utf8))
        #expect(health.supports(.sessions))
        #expect(!health.supports(.search))
    }
}

@Suite struct APIErrorTests {
    @Test func mapsKnownErrorCode() throws {
        let err = APIError.from(status: 409, body: try fixture("error-open-in-terminal"))
        #expect(err == .server(status: 409, code: .openInTerminal,
                               message: "read only here while a terminal on the Mac holds this session"))
    }

    @Test func keepsUnknownErrorCode() throws {
        let err = APIError.from(status: 429, body: try fixture("error-unknown-code"))
        #expect(err == .server(status: 429, code: .unknown("rate_limited"), message: "something new"))
    }

    @Test func nonJSONBodyBecomesHTTPError() {
        // tailscale serve answers 502 with a plain body when aimux is down.
        #expect(APIError.from(status: 502, body: Data("Bad Gateway".utf8)) == .http(status: 502))
    }
}

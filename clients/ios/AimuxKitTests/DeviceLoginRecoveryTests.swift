import Foundation
import LocalAuthentication
import Testing
@testable import AimuxKit

/// Ways back to a working pairing: a changed Face ID enrollment, pairing again from the lock screen,
/// two servers on one host, a server left behind, and a retry that lost a race with another login.
@MainActor @Suite struct DeviceLoginRecoveryTests {
    private let defaults = UserDefaults(suiteName: "aimux-recovery-\(UUID().uuidString)")!
    private let keys = TestKeys()
    private let biometry = TestBiometry()

    private func login(_ url: URL) -> DeviceLogin {
        let biometry = biometry
        return DeviceLogin(baseURL: url, session: ScriptedProtocol.session(), keys: keys, defaults: defaults,
                           biometryState: { biometry.reading })
    }

    private func paired(_ url: URL) async -> DeviceLogin {
        let login = login(url)
        login.start(health(deviceLogin: true))
        await login.pair(code: "ABCD-1234")
        return login
    }

    @Test func aChangedEnrollmentIsSeenBeforeFaceIDIsAsked() async throws {
        let mac = FakeMac(host: "statehash.test") { _ in (200, emptyList) }
        let login = await paired(mac.url)
        await login.lock()
        biometry.state = Data("faces-2".utf8)
        await login.unlock()
        #expect(login.state == .unpaired(.keyChanged))
        #expect(keys.reasons == ["Pair with your Mac"], "no signature asked for with a stale key")
        #expect(!mac.requests.contains { $0.path == "/v1/auth/challenge" })
        #expect(try keys.load(tag: DeviceLogin.keyTag(for: mac.url)) == nil)
    }

    @Test func theSameEnrollmentUnlocksWithItsReason() async {
        let mac = FakeMac(host: "samehash.test") { _ in (200, emptyList) }
        let login = await paired(mac.url)
        await login.lock()
        await login.unlock()
        #expect(login.state == .live)
        #expect(keys.reasons == ["Pair with your Mac", "Unlock aimux"])
    }

    @Test func allFacesRemovedCountsAsChanged() async {
        let mac = FakeMac(host: "nofaces.test") { _ in (200, emptyList) }
        let login = await paired(mac.url)
        await login.lock()
        biometry.state = nil
        biometry.error = .biometryNotEnrolled
        await login.unlock()
        #expect(login.state == .unpaired(.keyChanged))
    }

    /// No state hash because Face ID is off for the app, locked out or not available: the enrollment may be
    /// the same, so the key stays and the person is told how to get Face ID back.
    @Test(arguments: [LAError.Code.biometryLockout, .biometryNotAvailable])
    func faceIDUnavailableKeepsTheKey(why: LAError.Code) async throws {
        let mac = FakeMac(host: "unavailable-\(why.rawValue).test") { _ in (200, emptyList) }
        let login = await paired(mac.url)
        await login.lock()
        biometry.state = nil
        biometry.error = why
        await login.unlock()
        #expect(login.state == .locked)
        #expect(login.message == "Face ID is unavailable. Turn it on for aimux in Settings, or try again.")
        #expect(!login.canPairAgain)
        #expect(!mac.requests.contains { $0.path == "/v1/auth/challenge" })
        #expect(try keys.load(tag: DeviceLogin.keyTag(for: mac.url)) != nil)

        biometry.state = Data("faces-1".utf8)  // Face ID back, the same faces
        await login.unlock()
        #expect(login.state == .live)
    }

    @Test func aPairingWithoutAHashLeavesTheCheckToTheKey() async {
        let mac = FakeMac(host: "nohash.test") { _ in (200, emptyList) }
        biometry.state = nil
        let login = await paired(mac.url)
        await login.lock()
        biometry.state = Data("faces-1".utf8)
        await login.unlock()
        #expect(login.state == .live)
    }

    @Test(arguments: [("https://mac.test:443", "https://mac.test"), ("http://mac.test:80", "http://mac.test"),
                      ("https://Mac.Test.:443", "https://mac.test")])
    func anExplicitDefaultPortSharesTheRecord(explicit: String, plain: String) throws {
        let one = try #require(URL(string: explicit))
        let other = try #require(URL(string: plain))
        #expect(DeviceLogin.keyTag(for: one) == DeviceLogin.keyTag(for: other))
    }

    @Test func anExplicitDefaultPortFindsThePairing() async throws {
        let mac = FakeMac(host: "port443.test") { _ in (200, emptyList) }
        let plain = try #require(URL(string: "https://port443.test"))
        _ = await paired(plain)
        let explicit = login(try #require(URL(string: "https://port443.test:443")))
        explicit.start(health(deviceLogin: true))
        #expect(explicit.state == .locked)
        withExtendedLifetime(mac) {}  // its handler answers the pairing
    }

    @Test func pairingAgainIsOfferedWhenTheMacRefusesNotWhenItIsUnreachable() async {
        let mac = FakeMac(host: "offer.test") { _ in (200, emptyList) }
        let login = await paired(mac.url)
        await login.lock()
        #expect(!login.canPairAgain)

        ScriptedProtocol.script(host: mac.host) { _ in (401, #"{"error":"login failed","code":"login_failed"}"#) }
        await login.unlock()
        #expect(login.state == .locked)
        #expect(login.canPairAgain)

        ScriptedProtocol.unscript(host: mac.host)
        await login.unlock()
        #expect(login.message == "Could not reach your Mac. Check the connection and try again.")
        #expect(!login.canPairAgain)
    }

    @Test func pairAgainFromTheLockForgetsThisPairing() async throws {
        let mac = FakeMac(host: "pairagain.test") { _ in (200, emptyList) }
        let login = await paired(mac.url)
        await login.lock()
        login.pairAgain()
        #expect(login.state == .unpaired(nil))
        #expect(try keys.load(tag: DeviceLogin.keyTag(for: mac.url)) == nil)
        let relaunch = self.login(mac.url)
        relaunch.start(health(deviceLogin: true))
        #expect(relaunch.state == .unpaired(nil))
    }

    @Test func twoPortsOnOneHostKeepTheirOwnPairing() async throws {
        let mac = FakeMac(host: "ports.test") { _ in (200, emptyList) }
        let other = try #require(URL(string: "https://ports.test:9443"))
        #expect(DeviceLogin.keyTag(for: mac.url) != DeviceLogin.keyTag(for: other))

        _ = await paired(mac.url)
        let second = login(other)
        second.start(health(deviceLogin: true))
        #expect(second.state == .unpaired(nil), "the other port has no pairing yet")
        await second.pair(code: "ABCD1234")
        #expect(second.state == .live)
        let pair = try #require(mac.requests.last { $0.path == "/v1/auth/pair" })
        #expect(verifies(pair.json["signature"], over: SignedMessage.pair(host: "ports.test", code: "ABCD1234"),
                         by: pair.json["publicKey"]), "the signed host has no port")

        let first = login(mac.url)
        first.start(health(deviceLogin: true))
        #expect(first.state == .locked)
        #expect(try keys.load(tag: DeviceLogin.keyTag(for: mac.url)) != nil)
        #expect(try keys.load(tag: DeviceLogin.keyTag(for: other)) != nil)
    }

    @Test func leavingAServerDropsAKeyWithoutAPairing() async throws {
        let mac = FakeMac(host: "leave.test") { _ in (200, emptyList) }
        let tag = DeviceLogin.keyTag(for: mac.url)
        _ = try keys.create(tag: tag)  // left over: pairing never finished
        login(mac.url).discardUnusedKey()
        #expect(try keys.load(tag: tag) == nil)

        let kept = await paired(mac.url)
        kept.discardUnusedKey()
        #expect(try keys.load(tag: tag) != nil, "a paired key stays")
    }

    @Test func aRetryRefusedAfterAnotherLoginIsAGenericError() async throws {
        // tok2's retry is held until another login (tok3) replaced it, then refused.
        let mac = FakeMac(host: "race.test") { token in
            switch token {
            case "tok2": (0, "")
            case "tok3": (200, emptyList)
            default: (401, loginRequired)
            }
        }
        let login = await paired(mac.url)
        let client = APIClient(baseURL: mac.url, session: ScriptedProtocol.session(), auth: login)
        let request = Task { try await client.sessions() }
        await waitFor(login, .locked)
        await login.unlock()
        for _ in 0..<200 where ScriptedProtocol.held(host: mac.host) == 0 {
            try await Task.sleep(for: .milliseconds(10))
        }
        await login.lock()
        await login.unlock()
        #expect(login.state == .live)
        ScriptedProtocol.release(host: mac.host, status: 401, body: loginRequired)

        await #expect(throws: APIError.http(status: 401)) { try await request.value }
        #expect(login.state == .live, "the newer login stays")
        #expect(try keys.load(tag: DeviceLogin.keyTag(for: mac.url)) != nil)
    }
}

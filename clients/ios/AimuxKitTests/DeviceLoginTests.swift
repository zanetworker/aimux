import CryptoKit
import Foundation
import Security
import Testing
@testable import AimuxKit

@MainActor @Suite struct DeviceLoginTests {
    private let defaults = UserDefaults(suiteName: "aimux-device-login-\(UUID().uuidString)")!
    private let keys = TestKeys()

    private func login(_ mac: FakeMac) -> DeviceLogin {
        DeviceLogin(baseURL: mac.url, session: ScriptedProtocol.session(), keys: keys, defaults: defaults)
    }

    private func paired(_ mac: FakeMac) async -> DeviceLogin {
        let login = login(mac)
        login.start(health(deviceLogin: true))
        await login.pair(code: "abcd-1234")
        return login
    }

    @Test func withoutDeviceLoginRequestsGoAsToday() async throws {
        let mac = FakeMac(host: "off.test") { _ in (200, emptyList) }
        let login = login(mac)
        login.start(health(deviceLogin: false))
        #expect(login.state == .off)
        let client = APIClient(baseURL: mac.url, session: ScriptedProtocol.session(), auth: login)
        #expect(try await client.sessions().isEmpty)
        #expect(mac.requests.map(\.path) == ["/v1/sessions"])
        #expect(mac.requests.first?.headers["Authorization"] == nil)
    }

    @Test func aServerThatTurnedDeviceLoginOnAsksForPairing() async throws {
        let mac = FakeMac(host: "turnedon.test") { _ in (401, loginRequired) }
        let login = login(mac)
        login.start(health(deviceLogin: false))
        let client = APIClient(baseURL: mac.url, session: ScriptedProtocol.session(), auth: login)
        let request = Task { try await client.sessions() }
        await waitFor(login, .unpaired(nil))
        #expect(login.state == .unpaired(nil))
        request.cancel()
        _ = try? await request.value
    }

    @Test func deviceLoginWithoutAStoredDeviceAsksForPairing() {
        let mac = FakeMac(host: "fresh.test") { _ in (200, emptyList) }
        let login = login(mac)
        login.start(health(deviceLogin: true))
        #expect(login.state == .unpaired(nil))
    }

    @Test func pairingSendsTheCanonicalCodeSPKIAndAValidSignature() async throws {
        let mac = FakeMac(host: "pair.test") { _ in (200, emptyList) }
        let login = await paired(mac)
        #expect(login.state == .live)

        let pair = try #require(mac.requests.first { $0.path == "/v1/auth/pair" })
        #expect(Set(pair.json.keys) == ["code", "kind", "publicKey", "signature"])
        #expect(pair.json["code"] == "ABCD1234")
        #expect(pair.json["kind"] == "secure-enclave")
        let key = try #require(try keys.load(tag: DeviceLogin.keyTag(for: mac.url)))
        #expect(pair.json["publicKey"] == key.publicKeyDER.base64EncodedString())
        #expect(verifies(pair.json["signature"], over: SignedMessage.pair(host: "pair.test", code: "ABCD1234"),
                         by: pair.json["publicKey"]))

        let client = APIClient(baseURL: mac.url, session: ScriptedProtocol.session(), auth: login)
        _ = try await client.sessions()
        #expect(mac.requests.last?.headers["Authorization"] == "Bearer tok1")
    }

    @Test func aWrongCodeKeepsPairingAndDropsTheNewKey() async throws {
        let mac = FakeMac(host: "wrong.test") { _ in (200, emptyList) }
        let login = login(mac)
        login.start(health(deviceLogin: true))
        await login.pair(code: "ZZZZ-ZZZZ")
        #expect(login.state == .unpaired(nil))
        #expect(login.message == "That code didn't work. Run aimux remote pair on your Mac for a new one.")
        #expect(try keys.load(tag: DeviceLogin.keyTag(for: mac.url)) == nil)
    }

    @Test func aMalformedCodeSendsNothing() async {
        let mac = FakeMac(host: "format.test") { _ in (200, emptyList) }
        let login = login(mac)
        login.start(health(deviceLogin: true))
        await login.pair(code: "ABCD-12")
        #expect(login.message == "Enter the 8-character code that aimux remote pair shows on your Mac.")
        #expect(mac.requests.isEmpty)
    }

    @Test func cancellingFaceIDWhilePairingSendsNothing() async throws {
        let mac = FakeMac(host: "paircancel.test") { _ in (200, emptyList) }
        let login = login(mac)
        login.start(health(deviceLogin: true))
        keys.failSigning(.cancelled)
        await login.pair(code: "ABCD1234")
        #expect(login.state == .unpaired(nil))
        #expect(login.message == "Face ID was cancelled. Tap Pair this device to try again.")
        #expect(mac.requests.isEmpty)
        #expect(try keys.load(tag: DeviceLogin.keyTag(for: mac.url)) == nil)
    }

    @Test func aStoredDeviceWithoutATokenIsLocked() async {
        let mac = FakeMac(host: "relaunch.test") { _ in (200, emptyList) }
        _ = await paired(mac)
        let again = login(mac)  // a relaunch: same defaults and keys, no token
        again.start(health(deviceLogin: true))
        #expect(again.state == .locked)
        #expect(again.promptOnAppear)
    }

    @Test func a401LogsInOnceAndRetriesOnce() async throws {
        let mac = FakeMac(host: "retry.test") { token in token == "tok2" ? (200, emptyList) : (401, loginRequired) }
        let login = await paired(mac)
        let client = APIClient(baseURL: mac.url, session: ScriptedProtocol.session(), auth: login)

        let request = Task { try await client.sessions() }
        await waitFor(login, .locked)
        #expect(login.state == .locked)
        await login.unlock()
        #expect(try await request.value.isEmpty)

        let after = mac.requests.drop { $0.path != "/v1/sessions" }
        #expect(after.map(\.path) == ["/v1/sessions", "/v1/auth/challenge", "/v1/auth/login", "/v1/sessions"])
        #expect(after.map(\.bearer) == ["tok1", nil, nil, "tok2"])
        let challenge = try #require(after.first { $0.path == "/v1/auth/challenge" })
        #expect(challenge.json == ["deviceId": "dev-1"])
        let loginRequest = try #require(after.first { $0.path == "/v1/auth/login" })
        #expect(Set(loginRequest.json.keys) == ["deviceId", "challenge", "signature"])
        #expect(login.state == .live)
    }

    @Test func aSecond401AfterLoginMeansNoLongerPaired() async throws {
        let mac = FakeMac(host: "revoked.test") { _ in (401, loginRequired) }
        let login = await paired(mac)
        let client = APIClient(baseURL: mac.url, session: ScriptedProtocol.session(), auth: login)

        let request = Task { try await client.sessions() }
        await waitFor(login, .locked)
        await login.unlock()
        await #expect(throws: APIError.self) { try await request.value }
        #expect(login.state == .unpaired(.revoked))
        #expect(mac.requests.filter { $0.path == "/v1/sessions" }.count == 2)
        #expect(try keys.load(tag: DeviceLogin.keyTag(for: mac.url)) == nil)

        let relaunch = self.login(mac)
        relaunch.start(health(deviceLogin: true))
        #expect(relaunch.state == .unpaired(nil))
    }

    @Test func anUnknownDeviceAtUnlockMeansNoLongerPaired() async {
        let mac = FakeMac(host: "gone.test") { _ in (200, emptyList) }
        let login = await paired(mac)
        await login.lock()
        ScriptedProtocol.script(host: mac.host) { request in
            request.path == "/v1/auth/challenge"
                ? (404, #"{"error":"this device is not paired","code":"unknown_device"}"#) : (200, emptyList)
        }
        await login.unlock()
        #expect(login.state == .unpaired(.revoked))
    }

    @Test func cancellingFaceIDStaysLocked() async {
        let mac = FakeMac(host: "cancel.test") { _ in (200, emptyList) }
        let login = await paired(mac)
        await login.lock()
        keys.failSigning(.cancelled)
        await login.unlock()
        #expect(login.state == .locked)
        #expect(login.message == "Unlock was cancelled. Tap to try again.")
        #expect(!mac.requests.contains { $0.path == "/v1/auth/login" })
    }

    @Test func aChangedFaceIDEnrollmentAsksForPairingAgain() async throws {
        let mac = FakeMac(host: "enrolled.test") { _ in (200, emptyList) }
        let login = await paired(mac)
        await login.lock()
        keys.failSigning(.invalidated)
        await login.unlock()
        #expect(login.state == .unpaired(.keyChanged))
        #expect(try keys.load(tag: DeviceLogin.keyTag(for: mac.url)) == nil)
    }

    @Test func lockLogsOutAndDoesNotPromptByItself() async throws {
        let mac = FakeMac(host: "lock.test") { _ in (200, emptyList) }
        let login = await paired(mac)
        await login.lock()
        #expect(login.state == .locked)
        #expect(!login.promptOnAppear)
        let logout = try #require(mac.requests.last)
        #expect(logout.path == "/v1/auth/logout")
        #expect(logout.bearer == "tok1")
    }

    @Test func healthIsNeverHeldWhileLocked() async throws {
        let mac = FakeMac(host: "health.test") { _ in (200, emptyList) }
        let login = await paired(mac)
        await login.lock()
        let client = APIClient(baseURL: mac.url, session: ScriptedProtocol.session(), auth: login)
        #expect(try await client.health().supports(.deviceLogin))
        #expect(mac.requests.last?.headers["Authorization"] == nil)
    }

    @Test func aHeldRequestEndsWhenItsTaskIsCancelled() async {
        let mac = FakeMac(host: "held.test") { _ in (200, emptyList) }
        let login = await paired(mac)
        await login.lock()
        let client = APIClient(baseURL: mac.url, session: ScriptedProtocol.session(), auth: login)
        let request = Task { try await client.sessions() }
        try? await Task.sleep(for: .milliseconds(50))
        request.cancel()
        await #expect(throws: CancellationError.self) { try await request.value }
        #expect(!mac.requests.contains { $0.path == "/v1/sessions" })
    }

    @Test func theTokenIsNeverWrittenToDefaultsOrTheKeychain() async throws {
        let mac = FakeMac(host: "secret.test") { token in token == "tok2" ? (200, emptyList) : (401, loginRequired) }
        let login = await paired(mac)
        let client = APIClient(baseURL: mac.url, session: ScriptedProtocol.session(), auth: login)
        let request = Task { try await client.sessions() }
        await waitFor(login, .locked)
        await login.unlock()
        _ = try await request.value

        for store in [defaults, UserDefaults.standard] {
            let values = store.dictionaryRepresentation().map { "\($0.key)=\($0.value)" }
            #expect(!values.contains { $0.contains("tok1") || $0.contains("tok2") })
        }
        #expect(!keychainHolds("tok1") && !keychainHolds("tok2"))
    }

    private func keychainHolds(_ text: String) -> Bool {
        for itemClass in [kSecClassGenericPassword, kSecClassInternetPassword] {
            let query: [CFString: Any] = [kSecClass: itemClass, kSecMatchLimit: kSecMatchLimitAll,
                                          kSecReturnData: true, kSecReturnAttributes: true]
            var result: CFTypeRef?
            guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess,
                  let items = result as? [[String: Any]] else { continue }
            for item in items {
                let data = item[kSecValueData as String] as? Data ?? Data()
                let value = String(bytes: data, encoding: .utf8) ?? ""
                if value.contains(text) || "\(item)".contains(text) { return true }
            }
        }
        return false
    }

    @Test func interactionHeaderOnlyOnWhatThePersonAskedFor() async throws {
        let mac = FakeMac(host: "interaction.test") { _ in (200, emptyList) }
        let login = await paired(mac)
        let client = APIClient(baseURL: mac.url, session: ScriptedProtocol.session(), auth: login)
        _ = try await client.sessions()
        try await Interaction.$isActive.withValue(true) { _ = try await client.sessions() }
        let headers = mac.requests.filter { $0.path == "/v1/sessions" }.map { $0.headers["Aimux-Interaction"] }
        #expect(headers == [nil, "1"])
    }
}

@MainActor @Suite struct EventStreamLoginTests {
    @Test func sendsTheBearerTokenAndEndsOn401() async throws {
        let host = "stream401.test"
        let mac = FakeMac(host: host) { _ in (200, emptyList) }
        let defaults = UserDefaults(suiteName: "aimux-stream-\(UUID().uuidString)")!
        let login = DeviceLogin(baseURL: mac.url, session: ScriptedProtocol.session(), keys: TestKeys(),
                                defaults: defaults)
        login.start(health(deviceLogin: true))
        await login.pair(code: "ABCD1234")
        #expect(login.state == .live)

        SSEStubProtocol.stub(host: host, status: 401, chunks: [loginRequired])
        let stream = EventStream(baseURL: mac.url, session: SSEStubProtocol.session(), auth: login)
        let expected = APIError.server(status: 401, code: .loginRequired, message: "log in with a paired device")
        await #expect(throws: expected) {
            for try await _ in stream.events(sessionID: "s1", after: 0, log: "L") {}
        }
        #expect(SSEStubProtocol.requests(host: host).first?.value(forHTTPHeaderField: "Authorization") == "Bearer tok1")
        #expect(SSEStubProtocol.requests(host: host).first?.value(forHTTPHeaderField: "Aimux-Interaction") == nil)
        #expect(login.state == .locked)
    }
}

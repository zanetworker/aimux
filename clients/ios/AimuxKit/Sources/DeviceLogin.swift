import Foundation
import LocalAuthentication
import Observation

public enum UnpairedReason: Equatable, Sendable {
    /// The Mac no longer knows this device (revoked).
    case revoked
    /// The key is gone or unusable: Face ID enrollment changed, or the app moved to another iPhone.
    case keyChanged
}

public enum DeviceLoginState: Equatable, Sendable {
    /// Health not read yet: requests go without a token.
    case unknown
    /// The server has no device login: requests go as before.
    case off
    /// Paired, no session: unlock with Face ID. Requests wait.
    case locked
    /// A live session: requests carry its token.
    case live
    /// No paired key here: pair with a code from `aimux remote pair`. Requests wait.
    case unpaired(UnpairedReason?)
}

/// Device login for one server: pairing, Face ID unlock, the session token (in memory only) and what a
/// `401 login_required` does to requests. Requests made while locked or unpaired wait for the unlock.
@MainActor @Observable
public final class DeviceLogin {
    public private(set) var state: DeviceLoginState = .unknown
    /// One line for the pairing or unlock screen; never server text or the code.
    public private(set) var message: String?
    /// Whether the unlock screen should ask for Face ID as soon as it shows (not after Lock).
    public private(set) var promptOnAppear = false
    public private(set) var busy = false
    /// After an unlock the Mac refused (not one that never reached it): offer pairing again.
    public private(set) var canPairAgain = false

    private let baseURL: URL
    /// The host name the key signs (no port).
    private let host: String
    /// The server's host and port: one pairing, key and record each.
    private let server: String
    private let biometryState: @Sendable () -> BiometryState
    private let session: URLSession
    private let keys: any DeviceKeyStore
    private let defaults: UserDefaults
    @ObservationIgnored private var token: String?
    @ObservationIgnored private var waiters: [UUID: CheckedContinuation<String?, Error>] = [:]

    public init(baseURL: URL, session: URLSession = .shared, keys: any DeviceKeyStore,
                defaults: UserDefaults = .standard,
                biometryState: @escaping @Sendable () -> BiometryState = DeviceLogin.enrolledFaces) {
        self.baseURL = baseURL
        self.host = SignedMessage.host(of: baseURL)
        self.server = Self.server(of: baseURL)
        self.biometryState = biometryState
        self.session = session
        self.keys = keys
        self.defaults = defaults
    }

    /// The Keychain tag of this server's key (host and port).
    public static func keyTag(for url: URL) -> String { "me.adelzaalouk.Aimux.device-key." + server(of: url) }

    /// Host and port, without a port that is the scheme's default (`https://mac:443` is `https://mac`).
    private static func server(of url: URL) -> String {
        let defaultPort = url.scheme?.lowercased() == "http" ? 80 : 443
        guard let port = url.port, port != defaultPort else { return SignedMessage.host(of: url) }
        return SignedMessage.host(of: url) + ":\(port)"
    }

    /// The enrolled faces as LocalAuthentication reports them, or why it gives none. The state is filled in
    /// only after a policy check, so the context evaluates one first; its error says why there is no hash.
    public static let enrolledFaces: @Sendable () -> BiometryState = {
        let context = LAContext()
        var error: NSError?
        _ = context.canEvaluatePolicy(.deviceOwnerAuthenticationWithBiometrics, error: &error)
        let hash = context.domainState.biometry.stateHash
        let code = error.flatMap { $0.domain == LAErrorDomain ? LAError.Code(rawValue: $0.code) : nil }
        return BiometryState(stateHash: hash, error: hash == nil ? code : nil)
    }

    /// Reads the server's capabilities (at launch and when the app becomes active).
    public func start(_ health: Health) {
        guard health.supports(.deviceLogin) else {
            token = nil
            setState(.off)
            return
        }
        guard state == .unknown || state == .off else { return }
        promptOnAppear = true
        setState(record == nil ? .unpaired(nil) : .locked)
    }

    /// Makes a key (Face ID signs the pairing message with it) and pairs it with `code`.
    public func pair(code typed: String) async {
        guard case .unpaired = state, !busy else { return }
        guard let code = PairingCode.canonical(typed) else {
            message = Text.badFormat
            return
        }
        busy = true
        message = nil
        defer { busy = false }
        let tag = Self.keyTag(for: baseURL)
        keys.delete(tag: tag)
        let key: any DeviceKey
        do {
            key = try keys.create(tag: tag)
        } catch {
            message = Text.noKey
            return
        }
        do {
            let signature = try await key.sign(SignedMessage.pair(host: host, code: code), reason: Text.pairReason)
            let answer: AuthAnswer = try await post("/v1/auth/pair", body: [
                "code": code, "kind": "secure-enclave", "publicKey": key.publicKeyDER.base64EncodedString(),
                "signature": signature.base64EncodedString()
            ])
            guard let token = answer.token, !answer.deviceId.isEmpty else { throw APIError.decoding("no token") }
            var stored = ["deviceId": answer.deviceId, "keyTag": tag]
            stored["biometry"] = biometryState().stateHash?.base64EncodedString()
            defaults.set(stored, forKey: recordKey)
            becomeLive(token)
        } catch {
            keys.delete(tag: tag)
            message = Text.pairing(error)
        }
    }

    /// Asks Face ID to sign a fresh challenge and logs in; the requests that waited go on.
    public func unlock() async {
        guard state == .locked, !busy else { return }
        busy = true
        message = nil
        canPairAgain = false
        promptOnAppear = false
        defer { busy = false }
        guard let record else {
            setState(.unpaired(nil))
            return
        }
        guard enrollmentUnchanged(since: record.biometry) else { return }
        do {
            guard let key = try keys.load(tag: record.keyTag) else {
                forget(.keyChanged)
                return
            }
            let challenge: ChallengeAnswer = try await post("/v1/auth/challenge", body: ["deviceId": record.deviceId])
            let signature = try await key.sign(SignedMessage.login(host: host, challenge: challenge.challenge),
                                         reason: Text.unlockReason)
            let answer: AuthAnswer = try await post("/v1/auth/login", body: [
                "deviceId": record.deviceId, "challenge": challenge.challenge,
                "signature": signature.base64EncodedString()
            ])
            guard let token = answer.token else { throw APIError.decoding("no token") }
            becomeLive(token)
        } catch DeviceKeyError.invalidated {
            forget(.keyChanged)
        } catch APIError.server(_, .unknownDevice, _) {
            forget(.revoked)
        } catch {
            message = Text.unlock(error)
            // Pairing again cannot help when the Mac was not reached.
            if case APIError.transport = error {} else { canPairAgain = true }
        }
    }

    /// Checks Face ID enrollment against `saved`, the state hash from pairing, before Face ID is asked for in
    /// vain. A changed enrollment (all faces removed included) has invalidated the key: forgets it. No hash for
    /// another reason (Face ID off for the app, locked out) says nothing about the enrollment: the key stays
    /// and the message says how to get Face ID back. A pairing that saved no state leaves it to the key.
    private func enrollmentUnchanged(since saved: Data?) -> Bool {
        guard let saved else { return true }
        let now = biometryState()
        if now.stateHash == saved { return true }
        if now.stateHash == nil && now.error != .biometryNotEnrolled {
            message = Text.faceIDUnavailable
        } else {
            forget(.keyChanged)
        }
        return false
    }

    /// From the lock screen: forgets this pairing (key and record) and shows pairing.
    public func pairAgain() {
        guard state == .locked, !busy else { return }
        forget(nil)
    }

    /// When the app leaves this server: drops a key no pairing uses (one left by a pairing that never finished).
    public func discardUnusedKey() {
        guard record == nil else { return }
        keys.delete(tag: Self.keyTag(for: baseURL))
    }

    /// Ends the session here and on the Mac, and shows the unlock screen without asking for Face ID.
    public func lock() async {
        guard state == .live, let token else { return }
        self.token = nil
        promptOnAppear = false
        setState(.locked)
        // Best effort: offline, the session still ends on the Mac within the hour.
        let _: Status? = try? await post("/v1/auth/logout", body: nil, token: token)
    }

    #if DEBUG
    /// Puts the login in `state` without a server, for screenshots and UI tests (`-deviceLogin`).
    public func showForDemo(_ state: DeviceLoginState, refusedUnlock: Bool = false) {
        if state == .live || state == .locked {  // paired: a key and a record, so Unlock goes as far as the network
            let tag = Self.keyTag(for: baseURL)
            if (try? keys.load(tag: tag)) == nil { _ = try? keys.create(tag: tag) }
            defaults.set(["deviceId": "demo", "keyTag": tag], forKey: recordKey)
        }
        token = state == .live ? "demo" : nil
        promptOnAppear = false
        setState(state)
        if refusedUnlock {
            message = Text.unlockFailed
            canPairAgain = true
        }
    }
    #endif

    // MARK: For APIClient and EventStream

    /// The token to send, nil for none; waits while locked or unpaired.
    func credential() async throws -> String? {
        switch state {
        case .unknown, .off: return nil
        case .live: return token
        case .locked, .unpaired: break
        }
        let id = UUID()
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { continuation in
                if Task.isCancelled {
                    continuation.resume(throwing: CancellationError())
                } else {
                    waiters[id] = continuation
                }
            }
        } onCancel: {
            Task { @MainActor in self.waiters.removeValue(forKey: id)?.resume(throwing: CancellationError()) }
        }
    }

    /// A request sent with `token` got `401 login_required`: lock (or ask for pairing) unless a newer login
    /// already happened. The caller asks `credential()` again, which waits for the unlock.
    func rejected(token sent: String?) {
        guard token == nil || token == sent else { return }
        token = nil
        if case .unpaired = state { return }
        promptOnAppear = true
        setState(record == nil ? .unpaired(nil) : .locked)
    }

    /// The retry after a fresh login got `401` too: the Mac no longer takes this device. False when another
    /// login replaced that token meanwhile (nothing is forgotten).
    @discardableResult
    func refusedAfterLogin(token sent: String?) -> Bool {
        guard let sent, sent == token else { return false }
        forget(.revoked)
        return true
    }

    // MARK: State

    private struct Record {
        let deviceId: String
        let keyTag: String
        /// The enrolled faces when the key was made.
        let biometry: Data?
    }

    private var recordKey: String { "deviceLogin." + server }

    /// The paired device: its id and key tag (not secret). The token is never stored.
    private var record: Record? {
        guard let stored = defaults.dictionary(forKey: recordKey) as? [String: String],
              let id = stored["deviceId"], let tag = stored["keyTag"] else { return nil }
        return Record(deviceId: id, keyTag: tag, biometry: stored["biometry"].flatMap { Data(base64Encoded: $0) })
    }

    private func becomeLive(_ token: String) {
        self.token = token
        message = nil
        setState(.live)
    }

    /// Drops the key and the record: the next step is pairing again.
    private func forget(_ reason: UnpairedReason?) {
        if let record { keys.delete(tag: record.keyTag) }
        defaults.removeObject(forKey: recordKey)
        token = nil
        message = nil
        setState(.unpaired(reason))
    }

    private func setState(_ new: DeviceLoginState) {
        state = new
        canPairAgain = false
        switch new {
        case .unknown, .off, .live:
            let waiting = waiters.values
            waiters.removeAll()
            for continuation in waiting { continuation.resume(returning: token) }
        case .locked, .unpaired:
            break
        }
    }
}

extension DeviceLogin {
    // MARK: Requests

    private struct AuthAnswer: Decodable {
        let deviceId: String
        let token: String?
    }

    private struct ChallengeAnswer: Decodable { let challenge: String }
    private struct Status: Decodable { let status: String }

    private func post<T: Decodable>(_ path: String, body: [String: String]?, token: String? = nil) async throws -> T {
        var request = URLRequest(url: baseURL.appending(path: path))
        request.httpMethod = "POST"
        request.timeoutInterval = 20
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let body {
            request.httpBody = try JSONEncoder().encode(body)
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        if let token { request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch let error as URLError {
            throw APIError.transport(error.code)
        }
        guard let http = response as? HTTPURLResponse else { throw APIError.transport(.badServerResponse) }
        guard (200..<300).contains(http.statusCode) else { throw APIError.from(status: http.statusCode, body: data) }
        do {
            return try JSONDecoder().decode(T.self, from: data)
        } catch {
            throw APIError.decoding(String(describing: error))
        }
    }

    // MARK: Copy (the web client's)

    private enum Text {
        static let badFormat = "Enter the 8-character code that aimux remote pair shows on your Mac."
        static let codeFailed = "That code didn't work. Run aimux remote pair on your Mac for a new one."
        static let conflict = "A device with that name is already paired. "
            + "Revoke it on your Mac, or pair under another name."
        static let pairFailed = "Pairing failed. Run aimux remote pair on your Mac for a new code."
        static let pairCancelled = "Face ID was cancelled. Tap Pair this device to try again."
        static let noKey = "This iPhone could not make a key. Face ID must be set up."
        static let unlockCancelled = "Unlock was cancelled. Tap to try again."
        static let unlockFailed = "Could not unlock. Try again."
        static let faceIDUnavailable = "Face ID is unavailable. Turn it on for aimux in Settings, or try again."
        static let offline = "Could not reach your Mac. Check the connection and try again."
        static let pairReason = "Pair with your Mac"
        static let unlockReason = "Unlock aimux"

        static func pairing(_ error: Error) -> String {
            switch error {
            case DeviceKeyError.cancelled: pairCancelled
            case APIError.server(_, .pairingFailed, _): codeFailed
            case APIError.server(_, .pairingConflict, _): conflict
            case APIError.transport: offline
            default: pairFailed
            }
        }

        static func unlock(_ error: Error) -> String {
            switch error {
            case DeviceKeyError.cancelled: unlockCancelled
            case APIError.transport: offline
            default: unlockFailed
            }
        }
    }
}

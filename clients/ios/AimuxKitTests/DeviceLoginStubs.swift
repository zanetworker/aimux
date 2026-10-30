import CryptoKit
import Foundation
import LocalAuthentication
import Testing
@testable import AimuxKit

/// A fake server per host: a handler answers each request, and every request is kept with its body.
final class ScriptedProtocol: URLProtocol, @unchecked Sendable {
    struct Seen {
        let method: String
        let path: String
        let headers: [String: String]
        let body: Data

        var json: [String: String] { (try? JSONSerialization.jsonObject(with: body)) as? [String: String] ?? [:] }
        var bearer: String? { headers["Authorization"].map { String($0.dropFirst("Bearer ".count)) } }
    }

    /// Status 0 holds the request until `release(host:status:body:)`.
    typealias Handler = @Sendable (Seen) -> (Int, String)
    nonisolated(unsafe) private static var handlers: [String: Handler] = [:]
    nonisolated(unsafe) private static var seen: [String: [Seen]] = [:]
    nonisolated(unsafe) private static var heldRequests: [String: [ScriptedProtocol]] = [:]
    private static let lock = NSLock()
    private var loadingThread: Thread?
    private var heldReply: (Int, String) = (0, "")

    static func held(host: String) -> Int { lock.withLock { heldRequests[host]?.count ?? 0 } }

    /// Answers the oldest held request, on the thread URLSession started it on.
    static func release(host: String, status: Int, body: String) {
        let next = lock.withLock { heldRequests[host]?.isEmpty == false ? heldRequests[host]?.removeFirst() : nil }
        guard let instance = next,
              let thread = instance.loadingThread else { return }
        instance.heldReply = (status, body)
        instance.perform(#selector(deliverHeld), on: thread, with: nil, waitUntilDone: false)
    }

    @objc private func deliverHeld() { respond(heldReply) }

    static func script(host: String, _ handler: @escaping Handler) {
        lock.withLock { handlers[host] = handler; seen[host] = [] }
    }

    static func requests(host: String) -> [Seen] { lock.withLock { seen[host] ?? [] } }

    /// No handler: requests to `host` fail as if the Mac were unreachable.
    static func unscript(host: String) { _ = lock.withLock { handlers.removeValue(forKey: host) } }

    static func session() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [ScriptedProtocol.self]
        return URLSession(configuration: config)
    }

    override static func canInit(with request: URLRequest) -> Bool { true }
    override static func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        let host = request.url?.host() ?? ""
        let entry = Seen(method: request.httpMethod ?? "GET", path: request.url?.path() ?? "",
                         headers: request.allHTTPHeaderFields ?? [:], body: Self.body(of: request))
        let handler = Self.lock.withLock {
            Self.seen[host, default: []].append(entry)
            return Self.handlers[host]
        }
        guard let handler else {
            client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
            return
        }
        let reply = handler(entry)
        if reply.0 == 0 {
            loadingThread = Thread.current
            Self.lock.withLock { Self.heldRequests[host, default: []].append(self) }
            return
        }
        respond(reply)
    }

    private func respond(_ reply: (Int, String)) {
        let (status, body) = reply
        let response = HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1",
                                       headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    /// URLProtocol sees the body as a stream.
    private static func body(of request: URLRequest) -> Data {
        if let data = request.httpBody { return data }
        guard let stream = request.httpBodyStream else { return Data() }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let count = stream.read(&buffer, maxLength: buffer.count)
            if count <= 0 { break }
            data.append(buffer, count: count)
        }
        return data
    }
}

/// Software keys whose signing can be made to fail, as Face ID would.
final class TestKeys: DeviceKeyStore, @unchecked Sendable {
    private let inner = SoftwareKeyStore()
    private let lock = NSLock()
    private var failure: DeviceKeyError?
    private var reasonList: [String] = []

    func failSigning(_ error: DeviceKeyError?) { lock.withLock { failure = error } }
    var signingFailure: DeviceKeyError? { lock.withLock { failure } }
    /// The Face ID reason of each signature, in order.
    var reasons: [String] { lock.withLock { reasonList } }
    func record(_ reason: String) { lock.withLock { reasonList.append(reason) } }

    func create(tag: String) throws -> any DeviceKey { Wrapped(inner: try inner.create(tag: tag), keys: self) }
    func load(tag: String) throws -> (any DeviceKey)? {
        try inner.load(tag: tag).map { Wrapped(inner: $0, keys: self) }
    }
    func delete(tag: String) { inner.delete(tag: tag) }

    private struct Wrapped: DeviceKey {
        let inner: any DeviceKey
        let keys: TestKeys
        var publicKeyDER: Data { inner.publicKeyDER }
        func sign(_ message: Data, reason: String) async throws -> Data {
            keys.record(reason)
            if let failure = keys.signingFailure { throw failure }
            return try await inner.sign(message, reason: reason)
        }
    }
}

func health(deviceLogin: Bool) -> Health {
    Health(status: "ok", login: "me@example.com", version: "test", acpProtocolVersion: 1,
           capabilities: deviceLogin ? ["sessions", "device_login"] : ["sessions"])
}

let loginRequired = #"{"error":"log in with a paired device","code":"login_required"}"#
let emptyList = #"{"sessions":[]}"#

func verifies(_ signature: String?, over message: Data, by publicKey: String?) -> Bool {
    guard let signature, let publicKey, let sig = Data(base64Encoded: signature),
          let der = Data(base64Encoded: publicKey),
          let key = try? P256.Signing.PublicKey(derRepresentation: der),
          let parsed = try? P256.Signing.ECDSASignature(derRepresentation: sig) else { return false }
    return key.isValidSignature(parsed, for: message)
}

/// Waits (at most 2 s) until the login reaches `state`.
@MainActor func waitFor(_ login: DeviceLogin, _ state: DeviceLoginState) async {
    for _ in 0..<200 where login.state != state {
        try? await Task.sleep(for: .milliseconds(10))
    }
}

/// A fake Mac for one test: pairing with `code`, logins that hand out tok1, tok2, ..., and `/v1/sessions`
/// answered by `sessions` from the Bearer token it got.
final class FakeMac: @unchecked Sendable {
    let host: String
    private let lock = NSLock()
    private var issued = 0
    private var publicKey: String?

    init(host: String, code: String = "ABCD1234", sessions: @escaping @Sendable (String?) -> (Int, String)) {
        self.host = host
        ScriptedProtocol.script(host: host) { [unowned self] request in
            switch (request.method, request.path) {
            case ("POST", "/v1/auth/pair"):
                guard request.json["code"] == code else {
                    return (401, #"{"error":"pairing failed","code":"pairing_failed"}"#)
                }
                lock.withLock { publicKey = request.json["publicKey"] }
                return (200, #"{"deviceId":"dev-1","name":"iphone","token":"\#(nextToken())"}"#)
            case ("POST", "/v1/auth/challenge"):
                return (200, #"{"challenge":"chal-\#(lock.withLock { issued })"}"#)
            case ("POST", "/v1/auth/login"):
                let message = SignedMessage.login(host: host, challenge: request.json["challenge"] ?? "")
                guard request.json["deviceId"] == "dev-1",
                      verifies(request.json["signature"], over: message, by: lock.withLock({ publicKey })) else {
                    return (401, #"{"error":"login failed","code":"login_failed"}"#)
                }
                return (200, #"{"deviceId":"dev-1","name":"iphone","token":"\#(nextToken())"}"#)
            case ("POST", "/v1/auth/logout"):
                return (200, #"{"status":"logged_out"}"#)
            case ("GET", "/v1/health"):
                return (200, #"{"status":"ok","login":"me@example.com","version":"t","acpProtocolVersion":1,"#
                        + #""capabilities":["device_login"],"session":"none"}"#)
            default:
                return sessions(request.bearer)
            }
        }
    }

    private func nextToken() -> String {
        lock.withLock { issued += 1; return "tok\(issued)" }
    }

    var requests: [ScriptedProtocol.Seen] { ScriptedProtocol.requests(host: host) }
    var url: URL { URL(string: "https://\(host):8443")! }
}

/// The biometry state hash a test sets: what LAContext's domain state would report.
final class TestBiometry: @unchecked Sendable {
    private let lock = NSLock()
    private var value: Data? = Data("faces-1".utf8)
    private var why: LAError.Code = .biometryNotEnrolled
    /// The enrolled faces' state hash; nil when Face ID gives none.
    var state: Data? {
        get { lock.withLock { value } }
        set { lock.withLock { value = newValue } }
    }
    /// Why there is no state hash (canEvaluatePolicy's error), when `state` is nil.
    var error: LAError.Code {
        get { lock.withLock { why } }
        set { lock.withLock { why = newValue } }
    }
    var reading: BiometryState {
        lock.withLock { BiometryState(stateHash: value, error: value == nil ? why : nil) }
    }
}

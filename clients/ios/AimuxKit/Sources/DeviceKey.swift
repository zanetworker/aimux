import Foundation
import LocalAuthentication
import Security

/// The P-256 key this iPhone proves itself with: made once at pairing, it signs the pairing and each login.
public protocol DeviceKey: Sendable {
    /// SPKI DER, as the server's ParseP256 reads it.
    var publicKeyDER: Data { get }
    /// ECDSA P-256 over SHA-256 of `message`, ASN.1 DER. A Secure Enclave key asks for Face ID first.
    func sign(_ message: Data, reason: String) async throws -> Data
}

/// Where device keys live, by tag.
public protocol DeviceKeyStore: Sendable {
    func create(tag: String) throws -> any DeviceKey
    /// Nil when there is no key under `tag`: never made, deleted, or invalidated by a Face ID change.
    func load(tag: String) throws -> (any DeviceKey)?
    func delete(tag: String)
}

public enum DeviceKeyError: Error, Equatable, Sendable {
    /// The person cancelled Face ID.
    case cancelled
    /// The key cannot be used again (Face ID enrollment changed): pair again.
    case invalidated
    /// Anything else: no Secure Enclave, Face ID locked out or failed.
    case failed

    /// Reads an error from making or using a key.
    init(signing error: Error) {
        let error = error as NSError
        switch (error.domain, error.code) {
        case (NSOSStatusErrorDomain, Int(errSecUserCanceled)),
             (LAErrorDomain, LAError.userCancel.rawValue), (LAErrorDomain, LAError.systemCancel.rawValue),
             (LAErrorDomain, LAError.appCancel.rawValue), (Self.tokenDomain, -4):  // TKError.canceledByUser
            self = .cancelled
        case (NSOSStatusErrorDomain, Int(errSecItemNotFound)),
             (Self.tokenDomain, -6), (Self.tokenDomain, -7):  // TKError.objectNotFound, .tokenNotFound
            self = .invalidated
        default:
            self = .failed
        }
    }

    /// CryptoTokenKit's error domain, which Secure Enclave keys report in.
    private static let tokenDomain = "CryptoTokenKit"
}

/// The SubjectPublicKeyInfo of a P-256 key: the Security framework gives the raw X9.63 point.
enum P256SPKI {
    /// SEQUENCE { SEQUENCE { id-ecPublicKey, prime256v1 }, BIT STRING (0 unused bits) }.
    private static let header = Data([
        0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x02, 0x01,
        0x06, 0x08, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00
    ])

    /// Nil unless `x963` is an uncompressed point (0x04, X, Y; 65 bytes).
    static func der(x963: Data) -> Data? {
        guard x963.count == 65, x963.first == 0x04 else { return nil }
        return header + x963
    }
}

/// Signs with `key` (ECDSA P-256 over SHA-256, ASN.1 DER). A Secure Enclave key asks for Face ID, which
/// blocks the calling thread until the person answers, so callers run it off the main actor.
private func signature(by key: SecKey, of message: Data) throws -> Data {
    var error: Unmanaged<CFError>?
    guard let signature = SecKeyCreateSignature(key, .ecdsaSignatureMessageX962SHA256, message as CFData,
                                                &error) as Data? else {
        throw DeviceKeyError(signing: error?.takeRetainedValue() ?? DeviceKeyError.failed as Error)
    }
    return signature
}

/// The SPKI DER of a private key's public half.
private func publicKeyDER(of privateKey: SecKey) throws -> Data {
    guard let publicKey = SecKeyCopyPublicKey(privateKey),
          let point = SecKeyCopyExternalRepresentation(publicKey, nil) as Data?,
          let der = P256SPKI.der(x963: point) else { throw DeviceKeyError.failed }
    return der
}

/// A P-256 private key held in memory by the Security framework (tests and the simulator).
final class SecDeviceKey: DeviceKey, @unchecked Sendable {  // SecKey is immutable and thread-safe
    private let privateKey: SecKey
    let publicKeyDER: Data

    init(privateKey: SecKey) throws {
        self.privateKey = privateKey
        self.publicKeyDER = try AimuxKit.publicKeyDER(of: privateKey)
    }

    func sign(_ message: Data, reason: String) async throws -> Data {
        let key = privateKey
        return try await Task.detached { try signature(by: key, of: message) }.value
    }
}

/// A Secure Enclave key, found by its tag each time it signs, through an LAContext that carries the reason
/// Face ID shows.
struct EnclaveKey: DeviceKey {
    let tag: String
    let publicKeyDER: Data

    func sign(_ message: Data, reason: String) async throws -> Data {
        let tag = tag
        return try await Task.detached {
            let context = LAContext()
            context.localizedReason = reason
            guard let key = try EnclaveKeyStore.privateKey(tag: tag, context: context) else {
                throw DeviceKeyError.invalidated
            }
            return try signature(by: key, of: message)
        }.value
    }
}

/// Keys in the Secure Enclave, kept in the Keychain under their tag: usable only on this iPhone, while it is
/// unlocked, after Face ID for the faces enrolled now (a change invalidates the key).
public struct EnclaveKeyStore: DeviceKeyStore {
    public init() {}

    public func create(tag: String) throws -> any DeviceKey {
        var error: Unmanaged<CFError>?
        guard let access = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly,
                                                           [.privateKeyUsage, .biometryCurrentSet], &error) else {
            throw DeviceKeyError(signing: error?.takeRetainedValue() ?? DeviceKeyError.failed as Error)
        }
        let attributes: [CFString: Any] = [
            kSecAttrKeyType: kSecAttrKeyTypeECSECPrimeRandom,
            kSecAttrKeySizeInBits: 256,
            kSecAttrTokenID: kSecAttrTokenIDSecureEnclave,
            kSecPrivateKeyAttrs: [
                kSecAttrIsPermanent: true,
                kSecAttrApplicationTag: Data(tag.utf8),
                kSecAttrAccessControl: access
            ] as [CFString: Any]
        ]
        guard let key = SecKeyCreateRandomKey(attributes as CFDictionary, &error) else {
            throw DeviceKeyError(signing: error?.takeRetainedValue() ?? DeviceKeyError.failed as Error)
        }
        return EnclaveKey(tag: tag, publicKeyDER: try publicKeyDER(of: key))
    }

    public func load(tag: String) throws -> (any DeviceKey)? {
        guard let key = try Self.privateKey(tag: tag, context: nil) else { return nil }
        return EnclaveKey(tag: tag, publicKeyDER: try publicKeyDER(of: key))
    }

    public func delete(tag: String) {
        let query: [CFString: Any] = [kSecClass: kSecClassKey, kSecAttrApplicationTag: Data(tag.utf8)]
        SecItemDelete(query as CFDictionary)  // nothing to delete is fine
    }

    /// The key under `tag`, nil when there is none. With `context`, its use asks Face ID with that context.
    static func privateKey(tag: String, context: LAContext?) throws -> SecKey? {
        var query: [CFString: Any] = [
            kSecClass: kSecClassKey,
            kSecAttrApplicationTag: Data(tag.utf8),
            kSecAttrKeyType: kSecAttrKeyTypeECSECPrimeRandom,
            kSecAttrTokenID: kSecAttrTokenIDSecureEnclave,
            kSecReturnRef: true
        ]
        if let context { query[kSecUseAuthenticationContext] = context }
        var item: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let item, CFGetTypeID(item) == SecKeyGetTypeID() else {
            throw DeviceKeyError.failed
        }
        return (item as! SecKey)  // swiftlint:disable:this force_cast
    }
}

#if DEBUG
/// In-memory keys for tests and the simulator, which has no Secure Enclave. Never in a release build.
public final class SoftwareKeyStore: DeviceKeyStore, @unchecked Sendable {
    private let lock = NSLock()
    private var keys: [String: SecDeviceKey] = [:]

    public init() {}

    public func create(tag: String) throws -> any DeviceKey {
        let attributes: [CFString: Any] = [kSecAttrKeyType: kSecAttrKeyTypeECSECPrimeRandom, kSecAttrKeySizeInBits: 256]
        var error: Unmanaged<CFError>?
        guard let privateKey = SecKeyCreateRandomKey(attributes as CFDictionary, &error) else {
            throw DeviceKeyError.failed
        }
        let key = try SecDeviceKey(privateKey: privateKey)
        lock.withLock { keys[tag] = key }
        return key
    }

    public func load(tag: String) throws -> (any DeviceKey)? { lock.withLock { keys[tag] } }

    public func delete(tag: String) { _ = lock.withLock { keys.removeValue(forKey: tag) } }
}
#endif

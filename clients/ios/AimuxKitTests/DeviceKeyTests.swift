import CryptoKit
import Foundation
import LocalAuthentication
import Testing
@testable import AimuxKit

@Suite struct DeviceKeyTests {
    @Test func wrapsTheX963PointInTheP256SPKIHeader() throws {
        // CryptoKit's DER is what Go's x509.ParsePKIXPublicKey (the server's ParseP256) reads.
        for _ in 0..<5 {
            let key = P256.Signing.PrivateKey().publicKey
            #expect(P256SPKI.der(x963: key.x963Representation) == key.derRepresentation)
        }
    }

    @Test(arguments: [Data(), Data(repeating: 4, count: 64), Data([0x02] + Array(repeating: 1, count: 64)),
                      Data(repeating: 4, count: 66)])
    func refusesAnythingButAnUncompressedP256Point(point: Data) {
        #expect(P256SPKI.der(x963: point) == nil)
    }

    @Test func softwareKeySignsWhatCryptoKitVerifies() async throws {
        let store = SoftwareKeyStore()
        let key = try store.create(tag: "t")
        let message = Data("aimux-login-v1\nmy-mac.tail1234.ts.net\nabc".utf8)
        let signature = try await key.sign(message, reason: "Unlock aimux")

        let publicKey = try P256.Signing.PublicKey(derRepresentation: key.publicKeyDER)
        let parsed = try P256.Signing.ECDSASignature(derRepresentation: signature)
        #expect(publicKey.isValidSignature(parsed, for: message))
        #expect(!publicKey.isValidSignature(parsed, for: Data("aimux-login-v1\nother\nabc".utf8)))
    }

    @Test func storeLoadsAndDeletesByTag() throws {
        let store = SoftwareKeyStore()
        let made = try store.create(tag: "a")
        #expect(try store.load(tag: "a")?.publicKeyDER == made.publicKeyDER)
        #expect(try store.load(tag: "b") == nil)
        store.delete(tag: "a")
        #expect(try store.load(tag: "a") == nil)
    }

    @Test(arguments: [
        (NSError(domain: NSOSStatusErrorDomain, code: Int(errSecUserCanceled)), DeviceKeyError.cancelled),
        (NSError(domain: LAErrorDomain, code: LAError.userCancel.rawValue), .cancelled),
        (NSError(domain: LAErrorDomain, code: LAError.systemCancel.rawValue), .cancelled),
        (NSError(domain: LAErrorDomain, code: LAError.appCancel.rawValue), .cancelled),
        (NSError(domain: "CryptoTokenKit", code: -4), .cancelled),
        (NSError(domain: "CryptoTokenKit", code: -6), .invalidated),
        (NSError(domain: "CryptoTokenKit", code: -7), .invalidated),
        (NSError(domain: NSOSStatusErrorDomain, code: Int(errSecItemNotFound)), .invalidated),
        (NSError(domain: LAErrorDomain, code: LAError.authenticationFailed.rawValue), .failed),
        (NSError(domain: "CryptoTokenKit", code: -5), .failed),
        (NSError(domain: NSOSStatusErrorDomain, code: -50), .failed)
    ])
    func readsSigningErrors(error: NSError, expected: DeviceKeyError) {
        #expect(DeviceKeyError(signing: error) == expected)
    }
}

@Suite struct PairingCodeTests {
    // The server's TestCanonicalCode, TestParseCode_CrockfordAliases and TestParseCode_Rejects cases.
    @Test(arguments: [
        ("ABCD-1234", "ABCD1234"), (" abcd1234 ", "ABCD1234"), ("il0o-ABCD", "1100ABCD"),
        ("OIL1-0abc", "01110ABC"), ("oil10ABC", "01110ABC"), ("0l1i-oAbC", "01110ABC"),
        ("\tabcd-1234\n", "ABCD1234"), ("ıııı-ıııı", "11111111"), ("ſbcd-1234", "SBCD1234")
    ])
    func canonicalFormMatchesTheServer(typed: String, expected: String) {
        #expect(PairingCode.canonical(typed) == expected)
    }

    @Test(arguments: ["", "0123456789", "ABCD-123", "ABCU1234", "ABCD-EFG", "ABCDEFG", "ABCD-EFGHJ", "ABCDEFGHJ",
                      "ABCD-EFGU", "ABCD_EFGH", "AB-CD-EFGH", "ABCD-ÉFGH", "abcdefß", "ABCD–1234", "ＡBCD1234"])
    func refusesWhatTheServerRefuses(typed: String) {
        #expect(PairingCode.canonical(typed) == nil)
    }
}

@Suite struct SignedMessageTests {
    @Test func pairAndLoginStringsAreByteExact() {
        #expect(SignedMessage.pair(host: "my-mac.tail1234.ts.net", code: "ABCD1234")
                == Data("aimux-pair-v1\nmy-mac.tail1234.ts.net\nABCD1234".utf8))
        #expect(SignedMessage.login(host: "my-mac.tail1234.ts.net", challenge: "q-_9")
                == Data("aimux-login-v1\nmy-mac.tail1234.ts.net\nq-_9".utf8))
    }

    @Test(arguments: [
        ("https://my-mac.tail1234.ts.net:8443", "my-mac.tail1234.ts.net"),
        ("https://My-Mac.Tail1234.TS.net", "my-mac.tail1234.ts.net"),
        ("https://my-mac.tail1234.ts.net.:443", "my-mac.tail1234.ts.net"),
        ("http://localhost:7391", "localhost")
    ])
    func hostIsTheLowercaseNameWithoutPort(url: String, expected: String) {
        #expect(SignedMessage.host(of: URL(string: url)!) == expected)
    }
}

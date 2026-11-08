import Foundation

/// A pairing code as the server reads it (devicelogin.CanonicalCode): 8 Crockford base32 characters,
/// upper case, no dash; I and L read as 1, O as 0. The iPhone signs this form.
public enum PairingCode {
    private static let alphabet = Set("0123456789ABCDEFGHJKMNPQRSTVWXYZ".unicodeScalars)

    /// Nil when `typed` is not a code (with or without its dash, any case, surrounding spaces allowed).
    public static func canonical(_ typed: String) -> String? {
        // Go's ToUpper maps one rune to one rune; a letter whose upper case is longer (ß) stays as it is.
        var scalars = typed.trimmingCharacters(in: .whitespacesAndNewlines).unicodeScalars.map { scalar in
            let upper = scalar.properties.uppercaseMapping.unicodeScalars
            return upper.count == 1 ? upper.first! : scalar
        }
        if scalars.count == 9, scalars[4] == "-" { scalars.remove(at: 4) }
        guard scalars.count == 8 else { return nil }
        var code = String.UnicodeScalarView()
        for scalar in scalars {
            let mapped: Unicode.Scalar = switch scalar {
            case "I", "L": "1"
            case "O": "0"
            default: scalar
            }
            guard alphabet.contains(mapped) else { return nil }
            code.append(mapped)
        }
        return String(code)
    }
}

/// The strings the device key signs (UTF-8, `\n` between parts).
public enum SignedMessage {
    public static func pair(host: String, code: String) -> Data { Data("aimux-pair-v1\n\(host)\n\(code)".utf8) }

    public static func login(host: String, challenge: String) -> Data {
        Data("aimux-login-v1\n\(host)\n\(challenge)".utf8)
    }

    /// The server's host name as it signs and checks it: lower case, no port, no trailing dot.
    public static func host(of url: URL) -> String {
        var host = (url.host(percentEncoded: false) ?? "").lowercased()
        if host.hasSuffix(".") { host.removeLast() }
        return host
    }
}

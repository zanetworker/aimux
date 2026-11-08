import Foundation

/// Turns what someone types into the server field into the API's base URL:
/// adds https:// when the scheme is missing and drops any path ("/app/", "/v1/health").
public enum ServerAddress {
    public static func parse(_ text: String) -> URL? {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        let withScheme = trimmed.contains("://") ? trimmed : "https://" + trimmed
        guard let parsed = URLComponents(string: withScheme),
              let scheme = parsed.scheme?.lowercased(), ["https", "http"].contains(scheme),
              let host = parsed.host, !host.isEmpty else { return nil }
        var base = URLComponents()
        base.scheme = scheme
        base.host = host
        base.port = parsed.port
        return base.url
    }
}

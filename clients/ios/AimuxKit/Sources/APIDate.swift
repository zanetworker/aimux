import Foundation

/// Parses the server's RFC 3339 timestamps. Go writes 0-9 fractional digits
/// ("18:35:03.34Z", "18:35:27.606Z"), which Foundation's `.iso8601` strategy rejects.
public enum APIDate {
    public static func parse(_ text: String) -> Date? {
        guard let timeMark = text.firstIndex(of: "T") else { return nil }
        var base = text
        var fraction = 0.0
        if let dot = text[timeMark...].firstIndex(of: ".") {
            let start = text.index(after: dot)
            let end = text[start...].firstIndex { !("0"..."9").contains($0) } ?? text.endIndex
            guard start < end, let value = Double("0." + text[start..<end]) else { return nil }
            fraction = value
            base = String(text[..<dot]) + String(text[end...])
        }
        guard let date = try? Date(base, strategy: .iso8601) else { return nil }
        return date.addingTimeInterval(fraction)
    }
}

/// The decoder every API response goes through.
public enum APIDecoder {
    public static func decode<T: Decodable>(_ type: T.Type, from data: Data) throws -> T {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let text = try container.decode(String.self)
            guard let date = APIDate.parse(text) else {
                throw DecodingError.dataCorruptedError(in: container, debugDescription: "not a timestamp: \(text)")
            }
            return date
        }
        return try decoder.decode(type, from: data)
    }
}

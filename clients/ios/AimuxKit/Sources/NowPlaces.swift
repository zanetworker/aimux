import Foundation

/// The Mac places of the last list that had any, and when it came.
public struct HeldPlaces: Equatable, Sendable {
    public let places: [String: MacPlace]
    public let since: Date

    public init(places: [String: MacPlace], since: Date) {
        self.places = places
        self.since = since
    }
}

/// How long the layout stays after the places stop coming.
let placesHoldWindow: TimeInterval = 30

/// A list that has no `mac` at all while the last one had some keeps those places (by session id, for sessions
/// still listed) until 30 s after the last list that had places; a list with places replaces them.
public func holdPlaces(previous: HeldPlaces?, current: [Session], now: Date)
    -> (sessions: [Session], held: HeldPlaces?) {
    let fresh = current.compactMap { session in session.mac.map { (session.id, $0) } }
    if !fresh.isEmpty {
        return (current, HeldPlaces(places: Dictionary(fresh, uniquingKeysWith: { _, last in last }), since: now))
    }
    guard let previous, now.timeIntervalSince(previous.since) <= placesHoldWindow else { return (current, nil) }
    return (current.map { $0.placed(previous.places[$0.id]) }, previous)
}

import Foundation
import Testing
@testable import AimuxKit

@Suite struct HoldPlacesTests {
    private let start = Date(timeIntervalSince1970: 1_800_000_000)

    private func place(_ pane: Int) -> MacPlace {
        MacPlace(window: MacWindow(id: "w1", index: 1, name: "Window 1"), tab: MacTab(index: 1, name: "t"),
                 pane: pane, panes: 2, lastInFront: false)
    }

    private func session(_ id: String, pane: Int? = nil) -> Session {
        Session(id: id, title: id, project: "/src/x", lastActive: start, state: .openInTerminal,
                mac: pane.map(place))
    }

    @Test func aListWithPlacesIsKeptAndRemembered() {
        let current = [session("a", pane: 1), session("b")]
        let result = holdPlaces(previous: nil, current: current, now: start)
        #expect(result.sessions == current)
        #expect(result.held == HeldPlaces(places: ["a": place(1)], since: start))
    }

    @Test func placesAbsentForTenSecondsAreKept() {
        let held = HeldPlaces(places: ["a": place(1), "gone": place(2)], since: start)
        let result = holdPlaces(previous: held, current: [session("a"), session("b")],
                                now: start.addingTimeInterval(10))
        #expect(result.sessions.map(\.mac) == [place(1), nil])
        #expect(result.held == held, "the clock runs from the last list that had places")
    }

    @Test func placesAbsentFor31SecondsAreDropped() {
        let held = HeldPlaces(places: ["a": place(1)], since: start)
        let result = holdPlaces(previous: held, current: [session("a")], now: start.addingTimeInterval(31))
        #expect(result.sessions.map(\.mac) == [nil])
        #expect(result.held == nil)
    }

    @Test func thirtySecondsExactlyStillKeeps() {
        let held = HeldPlaces(places: ["a": place(1)], since: start)
        let result = holdPlaces(previous: held, current: [session("a")], now: start.addingTimeInterval(30))
        #expect(result.sessions.map(\.mac) == [place(1)])
    }

    @Test func newPlacesReplaceTheOldOnes() {
        let held = HeldPlaces(places: ["a": place(1)], since: start)
        let later = start.addingTimeInterval(20)
        let result = holdPlaces(previous: held, current: [session("a", pane: 3), session("b")], now: later)
        #expect(result.sessions.map(\.mac) == [place(3), nil])
        #expect(result.held == HeldPlaces(places: ["a": place(3)], since: later))
    }

    @Test func nothingHeldAndNoPlacesChangesNothing() {
        let current = [session("a")]
        let result = holdPlaces(previous: nil, current: current, now: start)
        #expect(result.sessions == current)
        #expect(result.held == nil)
    }
}

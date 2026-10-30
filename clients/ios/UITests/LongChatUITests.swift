import XCTest

/// Long terminal chats (`-demo chat-long`): demo-18's latest turns move on with every read, demo-17's stay.
final class LongChatUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    @MainActor
    private func launch(_ session: String = "demo-18") -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "chat-long", "-openChat", session, "-serverAddress", ""]
        app.launch()
        return app
    }

    /// Some of the timeline is in the accessibility tree, so on screen (the rows are lazy).
    @MainActor
    private func timelineShows(_ app: XCUIApplication) -> Bool {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'Reply' OR label CONTAINS 'Question'")).count > 0
    }

    @MainActor
    func testTimelineStaysOnScreenWhileTheHistoryMovesOn() {
        let app = launch()
        XCTAssertTrue(app.staticTexts["Reply 40 ends here."].waitForExistence(timeout: 10))
        for _ in 0..<4 { app.swipeDown(velocity: .fast) }
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "scrolled"
        shot.lifetime = .keepAlways
        add(shot)
        for round in 0..<3 {
            sleep(2)  // a read every 2 s while Claude works on the Mac
            XCTAssertTrue(timelineShows(app), "read \(round): the timeline is blank")
            let after = XCTAttachment(screenshot: app.screenshot())
            after.name = "read-\(round)"
            after.lifetime = .keepAlways
            add(after)
        }
        print("LATEST-VISIBLE-AFTER-READS:", app.buttons["jumpToLatest"].exists)
    }

    /// At the bottom there is no Latest button; scrolled up it shows, and takes you back to the newest content.
    @MainActor
    func testLatestJumpsToTheNewestContent() {
        let app = launch("demo-17")
        XCTAssertTrue(app.staticTexts["Reply 40 ends here."].waitForExistence(timeout: 10))
        let latest = app.buttons["jumpToLatest"]
        XCTAssertFalse(latest.exists, "at the bottom: no Latest")

        for _ in 0..<4 { app.swipeDown(velocity: .fast) }
        XCTAssertTrue(latest.waitForExistence(timeout: 5))
        XCTAssertEqual(latest.label, "Jump to latest")
        XCTAssertGreaterThanOrEqual(latest.frame.height, 44)
        XCTAssertGreaterThanOrEqual(latest.frame.width, 44)
        XCTAssertTrue(latest.isHittable, "above the bottom bar, not under it")

        latest.tap()
        XCTAssertTrue(latest.waitForNonExistence(timeout: 5), "back at the newest content")
        XCTAssertTrue(app.staticTexts["Reply 40 ends here."].waitForExistence(timeout: 5))
    }
}

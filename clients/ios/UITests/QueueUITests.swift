import XCTest

/// The chat queue on the demo data (`-demo chat-queue`): demo-1 is free with nothing queued, demo-2 works with
/// three messages waiting, demo-3 is paused with one answering and one waiting.
final class QueueUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    @MainActor
    private func launch(_ arguments: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "chat-queue"] + arguments + ["-serverAddress", ""]
        app.launch()
        return app
    }

    @MainActor
    private func row(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any)["queueItem-\(id)"]
    }

    @MainActor
    func testWhileWorkingTheComposerAddsToTheQueue() {
        let app = launch(["-openChat", "demo-2"])

        XCTAssertTrue(row(app, "dq1").waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["stopButton"].exists)
        XCTAssertFalse(app.buttons["sendButton"].exists, "working: the main action is Add to queue")

        let field = app.textFields["messageField"]
        field.tap()
        field.typeText("Check the docs links")
        app.buttons["queueButton"].tap()

        let added = row(app, "dq7")
        XCTAssertTrue(added.waitForExistence(timeout: 5))
        XCTAssertEqual(added.label, "Check the docs links")
        XCTAssertEqual(added.value as? String, "Waiting")
        XCTAssertEqual(field.value as? String, "Message", "the box empties once queued")
    }

    /// demo-1 is a free terminal session with nothing queued: Send is the main action, the second button
    /// queues, and once a message waits in the running queue the main action is Add to queue.
    @MainActor
    func testAFreeSessionStillQueuesFromTheSecondButton() {
        let app = launch(["-openChat", "demo-1"])

        let field = app.textFields["messageField"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["sendButton"].exists)
        field.tap()
        field.typeText("Then the appendix")
        app.buttons["queueAddButton"].tap()

        XCTAssertTrue(row(app, "dq7").waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["1 message"].exists)
        XCTAssertTrue(app.buttons["sendButton"].waitForNonExistence(timeout: 5),
                      "a message waits in the running queue: the main action is Add to queue")
    }

    /// demo-3's queue is paused with one message answering: the server refuses a direct send, so the main
    /// action is Add to queue.
    @MainActor
    func testAQueueWithOneInFlightAddsToTheQueue() {
        let app = launch(["-openChat", "demo-3"])

        XCTAssertTrue(row(app, "dq4").waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["sendButton"].exists)
        XCTAssertTrue(app.buttons["queueButton"].exists)
    }

    @MainActor
    func testEditInPlaceSavesTheText() {
        let app = launch(["-openChat", "demo-2"])

        let target = row(app, "dq2")
        XCTAssertTrue(target.waitForExistence(timeout: 10))
        target.tap()
        let editor = app.textFields["queueEditField"]
        XCTAssertTrue(editor.waitForExistence(timeout: 5))
        editor.typeText(" for the PR")
        app.buttons["queueEditSave"].tap()

        XCTAssertTrue(target.waitForExistence(timeout: 5))
        XCTAssertEqual(target.label, "Summarize the changes in three bullets for the PR")
        XCTAssertFalse(editor.exists)
    }

    @MainActor
    func testSwipeDeletesAMessage() {
        let app = launch(["-openChat", "demo-2"])

        let target = row(app, "dq3")
        XCTAssertTrue(target.waitForExistence(timeout: 10))
        target.swipeLeft()
        app.buttons["queueSwipeDelete"].tap()

        XCTAssertTrue(target.waitForNonExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["2 messages"].exists)
    }

    @MainActor
    func testDragReordersTheQueue() {
        let app = launch(["-openChat", "demo-2"])

        let first = row(app, "dq1")
        let last = row(app, "dq3")
        XCTAssertTrue(last.waitForExistence(timeout: 10))
        XCTAssertGreaterThan(last.frame.minY, first.frame.minY)

        last.press(forDuration: 1.2, thenDragTo: first)

        let moved = expectation(for: NSPredicate { _, _ in last.frame.minY < first.frame.minY }, evaluatedWith: nil)
        wait(for: [moved], timeout: 5)
    }

    @MainActor
    func testPausedQueueShowsWhyAndResumes() {
        let app = launch(["-openChat", "demo-3"])

        let line = app.staticTexts["queueLine"]
        XCTAssertTrue(line.waitForExistence(timeout: 10))
        XCTAssertEqual(line.label, "You used this session on your Mac")
        XCTAssertEqual(row(app, "dq4").value as? String, "Answer in progress")

        app.buttons["queueResume"].tap()

        XCTAssertTrue(app.buttons["queueResume"].waitForNonExistence(timeout: 5))
        XCTAssertFalse(line.exists)
    }

    @MainActor
    func testQueueButtonsAreAtLeast44PointsTall() {
        let app = launch(["-openChat", "demo-3"])

        let resume = app.buttons["queueResume"]
        XCTAssertTrue(resume.waitForExistence(timeout: 10))
        XCTAssertGreaterThanOrEqual(resume.frame.height, 44)

        row(app, "dq5").tap()
        let save = app.buttons["queueEditSave"]
        XCTAssertTrue(save.waitForExistence(timeout: 5))
        XCTAssertGreaterThanOrEqual(save.frame.height, 44)
        XCTAssertGreaterThanOrEqual(app.buttons["queueEditCancel"].frame.height, 44)
    }

    @MainActor
    func testAnotherRowWhileAnEditIsUnsavedSaysToSaveFirst() {
        let app = launch(["-openChat", "demo-2"])

        let target = row(app, "dq2")
        XCTAssertTrue(target.waitForExistence(timeout: 10))
        target.tap()
        let editor = app.textFields["queueEditField"]
        XCTAssertTrue(editor.waitForExistence(timeout: 5))
        editor.typeText(" now")
        row(app, "dq1").tap()

        XCTAssertTrue(app.staticTexts["Save or cancel this edit first."].waitForExistence(timeout: 3))
        XCTAssertEqual(editor.value as? String, "Summarize the changes in three bullets now", "the edit stays open")
    }
}

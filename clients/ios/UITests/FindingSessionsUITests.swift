import XCTest

/// Last on your Mac, pins, folders and owner edits on the `-demo finding` data (Update summary answered by a fake).
final class FindingSessionsUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    @MainActor
    private func launch(_ arguments: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "finding", "-tab", "now", "-serverAddress", ""] + arguments
        app.launch()
        return app
    }

    @MainActor
    func testNowLeadsWithLastOnYourMacAndPinned() {
        let app = launch()
        XCTAssertTrue(app.staticTexts["LAST ON YOUR MAC"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["session-fd-mac"].exists)
        XCTAssertTrue(app.staticTexts["PINNED"].exists)
        XCTAssertTrue(app.buttons["session-fd-pin1"].exists)
        XCTAssertTrue(app.buttons["session-fd-pin2"].exists)
        // A pinned working session is shown once: in Pinned, not in its folder.
        XCTAssertTrue(app.buttons["group-aimux"].exists)
        XCTAssertFalse(app.buttons["group-release-tools"].exists)
    }

    @MainActor
    func testFoldersStartOpenAndCollapse() {
        let app = launch()
        let folder = app.buttons["group-aimux"]
        XCTAssertTrue(folder.waitForExistence(timeout: 10))
        XCTAssertEqual(folder.value as? String, "Expanded")
        XCTAssertEqual(plain(folder.label), "aimux, 3 sessions")
        XCTAssertTrue(app.buttons["session-fd-a2"].exists)

        folder.tap()
        XCTAssertTrue(app.buttons["session-fd-a2"].waitForNonExistence(timeout: 5))
        XCTAssertEqual(folder.value as? String, "Collapsed")
        folder.tap()
        XCTAssertTrue(app.buttons["session-fd-a2"].waitForExistence(timeout: 5))

        // A folder of one session is a row that opens the session.
        XCTAssertFalse(app.buttons["group-trips"].exists)
        XCTAssertTrue(open(app.buttons["session-fd-t1"], in: app))
    }

    @MainActor
    func testLongPressPinsAndUnpins() {
        let app = launch()
        let row = app.buttons["session-fd-a2"]
        XCTAssertTrue(row.waitForExistence(timeout: 10))

        row.press(forDuration: 1.2)
        let pin = app.buttons["Pin"]
        XCTAssertTrue(pin.waitForExistence(timeout: 5))
        pin.tap()
        // Pinned: it leaves its folder for the Pinned section (a row with its project), and the 5 working
        // sessions left are a plain list again.
        XCTAssertTrue(waitFor(row, label: "Working, Fix the icon spacing, aimux, Active, 3 min"))
        let folder = app.buttons["group-aimux"]
        XCTAssertTrue(folder.waitForNonExistence(timeout: 5))

        row.press(forDuration: 1.2)
        let unpin = app.buttons["Unpin"]
        XCTAssertTrue(unpin.waitForExistence(timeout: 5))
        unpin.tap()
        XCTAssertTrue(waitFor(folder, label: "aimux, 3 sessions"))
    }

    @MainActor
    func testEditTitleShowsOnNow() {
        let app = launch()
        XCTAssertTrue(app.buttons["group-aimux"].waitForExistence(timeout: 10))
        XCTAssertTrue(open(app.buttons["session-fd-n2"], in: app))
        app.buttons["infoButton"].tap()

        let edit = app.buttons["edit-title"]
        XCTAssertTrue(edit.waitForExistence(timeout: 5))
        let field = app.textFields["metaInput"]
        // The sheet may still be sliding in: tap Edit again until the field shows.
        for _ in 0..<3 where !field.exists {
            edit.tap()
            _ = field.waitForExistence(timeout: 2)
        }
        XCTAssertTrue(field.exists)
        // Edit focuses the field with the caret at the end (a tap would move the caret).
        // The field's value reads truncated; delete more than the title holds.
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 40))
        field.typeText("Tidy the seed notes")
        app.buttons["metaSave"].tap()

        let shown = app.staticTexts["meta-title"]
        XCTAssertTrue(shown.waitForExistence(timeout: 5))
        XCTAssertEqual(plain(shown.label), "Tidy the seed notes")
        app.buttons["Done"].tap()
        app.navigationBars.buttons.element(boundBy: 0).tap()
        let renamed = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Tidy the seed notes"))
        XCTAssertTrue(renamed.firstMatch.waitForExistence(timeout: 5))
    }

    @MainActor
    func testUpdateSummaryShowsTheResult() {
        let app = launch(["-openChat", "fd-a2", "-openInfo", "YES"])
        let update = app.buttons["updateSummary"]
        XCTAssertTrue(update.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["meta-description"].exists)
        XCTAssertEqual(app.staticTexts["meta-description"].label, "None yet")
        update.tap()
        XCTAssertTrue(waitFor(update, label: "Updating summary…"))

        let note = app.staticTexts["metaNote"]
        XCTAssertTrue(note.waitForExistence(timeout: 5))
        XCTAssertEqual(note.label, "Updated the title and description.")
        XCTAssertEqual(plain(app.staticTexts["meta-description"].label),
                       "A made-up summary of this demo session, written by the fake model.")
        XCTAssertEqual(plain(app.staticTexts["meta-title"].label), "Summarised: Fix the icon spacing")
    }

    @MainActor
    func testSheetButtonsHave44PointTargets() {
        let app = launch(["-openChat", "fd-a2", "-openInfo", "YES"])
        let editTitle = app.buttons["edit-title"]
        XCTAssertTrue(editTitle.waitForExistence(timeout: 10))
        assertTarget(editTitle)
        assertTarget(app.buttons["edit-description"])
        let field = app.textFields["metaInput"]
        for _ in 0..<3 where !field.exists {
            editTitle.tap()
            _ = field.waitForExistence(timeout: 2)
        }
        assertTarget(app.buttons["metaSave"])
        assertTarget(app.buttons["metaCancel"])
    }

    @MainActor
    func testAnIdOnlyTitleIsPrefilledAsShownAndAnUnchangedSaveKeepsIt() {
        let app = launch(["-openChat", "c0ffee12-3c4d-4e5f-8a6b-7c8d9e0f1a2b", "-openInfo", "YES",
                          "-editField", "title"])
        let field = app.textFields["metaInput"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        XCTAssertEqual(field.value as? String, "Session c0ffee12")
        app.buttons["metaSave"].tap()
        XCTAssertTrue(field.waitForNonExistence(timeout: 5))
        XCTAssertEqual(plain(app.staticTexts["meta-title"].label), "Session c0ffee12")
    }

    @MainActor
    private func assertTarget(_ button: XCUIElement, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(button.exists, "missing \(button)", file: file, line: line)
        XCTAssertGreaterThanOrEqual(button.frame.width, 44, file: file, line: line)
        XCTAssertGreaterThanOrEqual(button.frame.height, 44, file: file, line: line)
    }

    /// Scrolls Now until the row is loaded and clear of the floating tab bar, taps it and waits for its chat.
    @MainActor
    private func open(_ row: XCUIElement, in app: XCUIApplication) -> Bool {
        for _ in 0..<4 where !row.exists { app.swipeUp() }
        let info = app.buttons["infoButton"]
        for _ in 0..<2 where row.exists {
            row.tap()
            if info.waitForExistence(timeout: 3) { return true }
            app.swipeUp()
        }
        return false
    }

    @MainActor
    private func waitFor(_ element: XCUIElement, label: String) -> Bool {
        for _ in 0..<50 {
            if element.exists && plain(element.label) == label { return true }
            Thread.sleep(forTimeInterval: 0.1)
        }
        return false
    }

    /// The label without the bidi isolates the app puts around session text.
    private func plain(_ label: String) -> String {
        label.filter { $0 != "\u{2068}" && $0 != "\u{2069}" }
    }
}

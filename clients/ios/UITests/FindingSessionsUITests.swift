import XCTest

/// Now mirroring the Mac, All's Recent and Folders, one search box, pins and owner edits on the `-demo finding`
/// data (Update summary answered by a fake). Each launch forgets the folds and All's mode unless `keep` is set.
final class FindingSessionsUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    @MainActor
    private func launch(_ arguments: [String] = [], tab: String = "now", keep: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "finding", "-tab", tab, "-serverAddress", ""]
            + (keep ? [] : ["-resetFinding", "YES"]) + arguments
        app.launch()
        return app
    }

    @MainActor
    func testNowMirrorsTheMacsWindowsAndTabs() {
        let app = launch()
        let window = app.staticTexts["window-w-1"]
        XCTAssertTrue(window.waitForExistence(timeout: 10))
        XCTAssertEqual(plain(window.label), "Window 1")
        // The tab holding the pane last in front starts open; the others are one line each.
        XCTAssertTrue(app.buttons["session-fd-a1"].exists)
        XCTAssertTrue(app.buttons["session-fd-q1"].exists)
        let folded = app.buttons["tab-w-1-2"]
        XCTAssertEqual(plain(folded.label), "openshell · tab 2 · 2 panes · 1 working")
        XCTAssertEqual(folded.value as? String, "Collapsed")
        XCTAssertFalse(app.buttons["session-fd-o1"].exists)

        folded.tap()
        XCTAssertTrue(app.buttons["session-fd-o1"].waitForExistence(timeout: 5))
        XCTAssertEqual(folded.value as? String, "Expanded")
        folded.tap()
        XCTAssertTrue(app.buttons["session-fd-o1"].waitForNonExistence(timeout: 5))

        // The renamed window keeps the owner's title.
        for _ in 0..<4 where !app.staticTexts["window-w-2"].exists { app.swipeUp() }
        XCTAssertEqual(plain(app.staticTexts["window-w-2"].label), "Reviews")
    }

    @MainActor
    func testAFoldChosenByHandIsRemembered() {
        let app = launch()
        let tab = app.buttons["tab-w-1-2"]
        XCTAssertTrue(tab.waitForExistence(timeout: 10))
        tab.tap()
        XCTAssertTrue(app.buttons["session-fd-o1"].waitForExistence(timeout: 5))
        app.terminate()

        let again = launch(keep: true)
        XCTAssertTrue(again.buttons["session-fd-o1"].waitForExistence(timeout: 10))
    }

    @MainActor
    func testTheTabBarHasNowAndAllOnly() {
        let app = launch()
        XCTAssertTrue(app.tabBars.buttons["Now"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.tabBars.buttons["All"].exists)
        XCTAssertEqual(app.tabBars.buttons.count, 2)
    }

    @MainActor
    func testAllStartsOnRecentAndRemembersFolders() {
        let app = launch(tab: "all")
        let recent = app.buttons["Recent"]
        XCTAssertTrue(recent.waitForExistence(timeout: 10))
        XCTAssertTrue(recent.isSelected)
        XCTAssertTrue(app.staticTexts["TODAY"].exists)
        app.buttons["Folders"].tap()
        let aimux = app.buttons["folder-aimux"]
        XCTAssertTrue(aimux.waitForExistence(timeout: 5))
        // The folder (and worktree) holding the newest session starts open; the rest are closed.
        XCTAssertEqual(aimux.value as? String, "Expanded")
        XCTAssertEqual(app.buttons["worktree-aimux-clients"].value as? String, "Expanded")
        XCTAssertTrue(app.buttons["session-fd-a1"].exists)
        let openshell = app.buttons["folder-openshell"]
        for _ in 0..<4 where !openshell.exists { app.swipeUp() }
        XCTAssertEqual(openshell.value as? String, "Collapsed")
        XCTAssertEqual(plain(openshell.label), "openshell · 4 · 2 working")
        app.terminate()

        let again = launch(tab: "all", keep: true)
        XCTAssertTrue(again.buttons["folder-aimux"].waitForExistence(timeout: 10))
        XCTAssertTrue(again.buttons["Folders"].isSelected)
    }

    @MainActor
    func testTypingOnNowHidesOtherTabsAndListsOtherSessions() {
        let app = launch()
        let field = app.textFields["searchField"]
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["session-fd-a1"].waitForExistence(timeout: 10))
        field.tap()
        field.typeText("queue")

        XCTAssertTrue(app.buttons["session-fd-a1"].waitForNonExistence(timeout: 5))
        XCTAssertTrue(app.buttons["session-fd-q1"].exists, "an open match stays in its pane")
        XCTAssertEqual(plain(app.buttons["tab-w-1-3"].label), "aimux · tab 3 · 1 of 4 panes")
        XCTAssertFalse(app.buttons["tab-w-1-1"].exists)
        XCTAssertEqual(app.staticTexts["hiddenTabs"].label, "4 tabs with no match are hidden")
        XCTAssertTrue(app.buttons["session-fd-v1"].exists)
        for _ in 0..<3 where !app.buttons["hit-fd-fv"].exists { app.swipeUp() }
        XCTAssertTrue(app.buttons["session-fd-c1"].exists, "a closed match is under Other sessions")
        XCTAssertTrue(app.buttons["hit-fd-fv"].waitForExistence(timeout: 5), "then conversation hits")
    }

    @MainActor
    func testTheWorkingChipNarrowsToWorkingSessions() {
        let app = launch()
        let chip = app.descendants(matching: .any)["workingChip"]
        XCTAssertTrue(chip.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["session-fd-q1"].waitForExistence(timeout: 10))
        XCTAssertEqual(chip.label, "Working · 5")
        chip.tap()
        XCTAssertTrue(app.buttons["session-fd-q1"].waitForNonExistence(timeout: 5))
        XCTAssertTrue(app.buttons["session-fd-a1"].exists)
        XCTAssertTrue(app.buttons["session-fd-o1"].exists, "a filtered tab is held open")
    }

    @MainActor
    func testLongPressPinsInPlaceAndUnpins() {
        let app = launch()
        let row = app.buttons["session-fd-a2"]
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        XCTAssertFalse(plain(row.label).contains("Pinned"))

        row.press(forDuration: 1.2)
        let pin = app.buttons["Pin"]
        XCTAssertTrue(pin.waitForExistence(timeout: 5))
        pin.tap()
        // Pinned and open: it stays in its pane, with the pin mark, not in Pinned.
        XCTAssertTrue(waitFor(row) { $0.contains("Pinned") })

        row.press(forDuration: 1.2)
        let unpin = app.buttons["Unpin"]
        XCTAssertTrue(unpin.waitForExistence(timeout: 5))
        unpin.tap()
        XCTAssertTrue(waitFor(row) { !$0.contains("Pinned") })
    }

    @MainActor
    func testEditTitleShowsOnNow() {
        let app = launch()
        XCTAssertTrue(open(app.buttons["session-fd-a2"], in: app))
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
        field.typeText("Tidy the icon grid")
        app.buttons["metaSave"].tap()

        let shown = app.staticTexts["meta-title"]
        XCTAssertTrue(shown.waitForExistence(timeout: 5))
        XCTAssertEqual(plain(shown.label), "Tidy the icon grid")
        app.buttons["Done"].tap()
        app.navigationBars.buttons.element(boundBy: 0).tap()
        let renamed = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Tidy the icon grid"))
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
        waitFor(element) { $0 == label }
    }

    @MainActor
    private func waitFor(_ element: XCUIElement, _ test: (String) -> Bool) -> Bool {
        for _ in 0..<50 {
            if element.exists && test(plain(element.label)) { return true }
            Thread.sleep(forTimeInterval: 0.1)
        }
        return false
    }

    /// The label without the bidi isolates the app puts around session text.
    private func plain(_ label: String) -> String {
        label.filter { $0 != "\u{2068}" && $0 != "\u{2069}" }
    }
}

import XCTest

final class ChatUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    @MainActor
    private func launch(_ arguments: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = arguments + ["-serverAddress", ""]
        app.launch()
        return app
    }

    @MainActor
    func testNowRowOpensTheChatWithStepsAndInfo() {
        let app = launch(["-demo", "chat", "-tab", "now"])

        let row = app.buttons["session-demo-2"]
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        row.tap()

        // The steps fold into one line; opening it shows each step's target.
        let steps = app.buttons["stepsToggle"].firstMatch
        XCTAssertTrue(steps.waitForExistence(timeout: 5))
        XCTAssertFalse(app.staticTexts["remote/stream_test.go"].exists)
        steps.tap()
        XCTAssertTrue(app.staticTexts["remote/stream_test.go"].waitForExistence(timeout: 5))

        // Working: send is stop; the Next bar points at the session that needs you.
        XCTAssertTrue(app.buttons["stopButton"].exists)
        XCTAssertTrue(app.buttons["nextBar"].exists)

        app.buttons["infoButton"].tap()
        XCTAssertTrue(app.staticTexts["Cost"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["$0.42"].exists)
    }

    @MainActor
    func testStepOpensItsCommandAndItsFailure() {
        let app = launch(["-demo", "chat", "-openChat", "demo-2"])

        let steps = app.buttons["stepsToggle"].firstMatch
        XCTAssertTrue(steps.waitForExistence(timeout: 10))
        steps.tap()

        // A step with nothing to show is a plain line, not a button.
        XCTAssertTrue(app.staticTexts["remote/stream_test.go"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["step-0"].exists)

        // The running test opens to its whole command.
        let run = app.buttons["step-5"]
        XCTAssertTrue(run.exists)
        XCTAssertEqual(run.value as? String, "Collapsed")
        XCTAssertFalse(app.staticTexts[demoCommand].exists)
        run.tap()
        XCTAssertTrue(app.staticTexts[demoCommand].waitForExistence(timeout: 5))
        XCTAssertEqual(run.value as? String, "Expanded")

        // The failed vet opens to "Failed" and its error.
        let failed = app.buttons["step-4"]
        failed.tap()
        XCTAssertTrue(app.staticTexts["Failed"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts[demoError].exists)

        run.tap()
        XCTAssertFalse(app.staticTexts[demoCommand].waitForExistence(timeout: 2))
    }

    private let demoCommand = "go test ./remote/... -run 'TestReconnect|TestResume' -count=1 -race"
    private let demoError = "remote/stream.go:88:2: unreachable code\nexit status 1"

    @MainActor
    func testAllowCollapsesTheApprovalCard() {
        let app = launch(["-demo", "chat-approval", "-openChat", "demo-13"])

        let allow = app.buttons["Allow"]
        XCTAssertTrue(allow.waitForExistence(timeout: 10))
        // An approval is open in this chat: no Next bar.
        XCTAssertFalse(app.buttons["nextBar"].exists)
        allow.tap()
        XCTAssertTrue(app.staticTexts["Allowed"].waitForExistence(timeout: 5))
        XCTAssertFalse(allow.exists)
    }

    @MainActor
    func testMediaCardsPlayInPlaceAndStop() {
        let app = launch(["-demo", "chat-format", "-openChat", "demo-4"])

        // The last reply: YouTube, a video, an image and an audio file, each a card; the inline link is not.
        let video = app.buttons.matching(identifier: "media-card")
            .matching(NSPredicate(format: "label BEGINSWITH 'Video: Rotation walkthrough'")).firstMatch
        XCTAssertTrue(video.waitForExistence(timeout: 10))
        XCTAssertEqual(app.buttons.matching(identifier: "media-card").count, 4)
        XCTAssertFalse(app.otherElements["media-player"].exists)

        video.tap()
        XCTAssertTrue(app.descendants(matching: .any)["media-player"].waitForExistence(timeout: 5))
        video.tap()
        XCTAssertFalse(app.descendants(matching: .any)["media-player"].waitForExistence(timeout: 2))

        // One player at a time: starting the audio card stops the video.
        let audio = app.buttons.matching(identifier: "media-card")
            .matching(NSPredicate(format: "label BEGINSWITH 'Audio:'")).firstMatch
        video.tap()
        XCTAssertTrue(app.descendants(matching: .any)["media-player"].waitForExistence(timeout: 5))
        audio.tap()
        XCTAssertTrue(app.descendants(matching: .any)["media-player"].waitForExistence(timeout: 5))
        let players = app.descendants(matching: .any).matching(identifier: "media-player")
        let one = expectation(for: NSPredicate(format: "count == 1"), evaluatedWith: players)
        wait(for: [one], timeout: 5)
    }

    @MainActor
    func testWrappingTableIsAsTallAsItsRowsAndTextFollowsBelow() {
        let app = launch(["-demo", "chat-format", "-openChat", "demo-4"])

        // The last reply: a two-column table whose three long cells wrap at the cell width, a ragged last row,
        // then a paragraph. The table's own frame is its measured height (its row elements are synthetic, so
        // their frames say nothing about the drawn rows).
        let after = app.staticTexts["All images are pinned by digest, and nothing was changed."]
        XCTAssertTrue(after.waitForExistence(timeout: 10))
        let table = app.descendants(matching: .any)
            .matching(NSPredicate(format: "label == 'Table, 4 rows'")).firstMatch
        XCTAssertTrue(table.exists)

        // Five rows of at least one line (about 38 pt with padding) and three of them at least two lines (a
        // footnote or subheadline line is over 16 pt): a table measured as five one-line rows is about 192 pt,
        // shorter than the cells it draws, which then run over the rows and the paragraph below.
        XCTAssertGreaterThanOrEqual(table.frame.height, 5 * 38 + 3 * 16,
                                    "the table is shorter than its wrapped rows: \(table.frame)")
        XCTAssertGreaterThanOrEqual(after.frame.minY, table.frame.maxY)
    }

    @MainActor
    func testReadOnlyChatHasNoComposer() {
        let app = launch(["-demo", "chat-readonly", "-openChat", "demo-16"])

        XCTAssertTrue(app.staticTexts["readOnlyLine"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["sendButton"].exists)
        XCTAssertFalse(app.textFields["messageField"].exists)
    }

    @MainActor
    func testNextBarFollowsNowAfterAnAnswer() {
        let app = launch(["-demo", "chat", "-openChat", "demo-2"])

        let next = app.buttons["nextBar"]
        XCTAssertTrue(next.waitForExistence(timeout: 10))
        XCTAssertTrue(next.label.contains("Database migration plan"))
        next.tap()

        // Answer the approval there, then move on: the session just answered no longer needs you.
        let allow = app.buttons["Allow"]
        XCTAssertTrue(allow.waitForExistence(timeout: 5))
        allow.tap()
        XCTAssertTrue(app.staticTexts["Allowed"].waitForExistence(timeout: 5))
        XCTAssertTrue(next.waitForExistence(timeout: 5))
        XCTAssertTrue(next.label.contains("Gateway certificate check"), next.label)
        next.tap()
        XCTAssertTrue(app.staticTexts["kubectl apply exited 1"].waitForExistence(timeout: 5))
        XCTAssertTrue(next.waitForExistence(timeout: 5))
        XCTAssertFalse(next.label.contains("Database migration plan"), next.label)
    }

    @MainActor
    func testTitlesReadWithoutIsolateMarks() {
        let app = launch(["-demo", "chat", "-tab", "now"])

        let title = "Fix flaky SSE reconnect test in the remote client"
        XCTAssertTrue(app.staticTexts[title].waitForExistence(timeout: 10))
        app.buttons["session-demo-2"].tap()

        let header = app.descendants(matching: .any)["chatHeader"]
        XCTAssertTrue(header.waitForExistence(timeout: 5))
        XCTAssertTrue(header.label.hasPrefix(title), header.label)
        XCTAssertTrue(header.label.contains("Working · aimux"), header.label)
        XCTAssertFalse(header.label.contains("\u{2068}") || header.label.contains("\u{2069}"), header.label)
    }

    @MainActor
    func testTerminalChatTypesIntoTheMac() {
        let app = launch(["-demo", "chat-terminal", "-openChat", "demo-1"])

        let header = app.descendants(matching: .any)["chatHeader"]
        XCTAssertTrue(header.waitForExistence(timeout: 10))
        XCTAssertTrue(header.label.contains("On your Mac · Your turn"), header.label)
        XCTAssertFalse(app.staticTexts["readOnlyLine"].exists)

        let field = app.descendants(matching: .any)["messageField"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText("are you still blocked?")
        app.buttons["sendButton"].tap()
        XCTAssertTrue(app.staticTexts["Sent to your Mac's terminal"].waitForExistence(timeout: 5))
    }

    @MainActor
    func testTerminalPromptCardAnswers() {
        let app = launch(["-demo", "chat-terminal", "-openChat", "demo-17"])

        let card = app.descendants(matching: .any)["approval-demo-prompt-17"]
        XCTAssertTrue(card.waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["Do you want to make this edit to CHANGELOG.md?"].exists)
        let detail = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "Edit file\nCHANGELOG.md"))
        XCTAssertEqual(detail.count, 1, "the card shows what is being approved")
        XCTAssertFalse(app.buttons["nextBar"].exists)
        XCTAssertFalse(app.staticTexts["readOnlyLine"].exists, "the card says what to do; no lock line under it")
        app.buttons["Yes"].tap()
        let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: card)
        wait(for: [gone], timeout: 5)
        // Claude works on; a message typed now waits in its input box.
        XCTAssertTrue(app.descendants(matching: .any)["messageField"].waitForExistence(timeout: 5))
        XCTAssertFalse(app.staticTexts["readOnlyLine"].exists)
    }

    @MainActor
    func testTerminalRefusalSaysWhy() {
        let app = launch(["-demo", "chat-terminal", "-openChat", "demo-18"])

        let line = app.staticTexts["readOnlyLine"]
        XCTAssertTrue(line.waitForExistence(timeout: 10))
        XCTAssertEqual(line.label, "Your Mac is in use right now")
        XCTAssertFalse(app.buttons["sendButton"].exists)
    }
}

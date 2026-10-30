import XCTest

final class NowAllUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    @MainActor
    private func launch(_ arguments: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        // An empty saved address each run; an earlier run saved one through the sheet.
        app.launchArguments = arguments + ["-serverAddress", ""]
        app.launch()
        return app
    }

    @MainActor
    func testNowAnswersAnApprovalAndTheTabsReachAllAndSearch() {
        let app = launch(["-demo", "list", "-tab", "now"])

        // The approval card shows each of its options as a button.
        let allow = app.buttons["Allow once"]
        XCTAssertTrue(allow.waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["Always allow"].exists)
        XCTAssertTrue(app.buttons["Reject"].exists)

        // "Open ›" pushes the chat; it has its own bar with a way back, although Now hides its bar.
        app.buttons["Open ›"].tap()
        XCTAssertTrue(app.buttons["infoButton"].waitForExistence(timeout: 5))
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(allow.waitForExistence(timeout: 5))

        allow.tap()
        XCTAssertTrue(allow.waitForNonExistence(timeout: 5), "answering removes the card")

        app.tabBars.buttons["All"].tap()
        let today = app.staticTexts.matching(NSPredicate(format: "label ==[c] 'Today'")).firstMatch
        XCTAssertTrue(today.waitForExistence(timeout: 5))

        app.tabBars.buttons["Search"].tap()
        let field = app.searchFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText("weekly")
        XCTAssertTrue(app.buttons["session-demo-3"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.staticTexts["Weekly report"].exists)
    }

    @MainActor
    func testBusyNowShowsApprovalsAsRowsThatOpenInPlace() {
        let app = launch(["-demo", "twenty", "-tab", "now"])

        // More than 5 needing you, finished or working: approvals are rows, the groups fit below them.
        let row = app.buttons["approvalRow-ap-tw-a1"]
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["Allow once"].exists)
        XCTAssertTrue(app.buttons["group-research"].exists)
        row.tap()
        XCTAssertTrue(app.buttons["Allow once"].waitForExistence(timeout: 5))
    }

    @MainActor
    func testServerSheetConnectsWithTheGoKey() {
        let app = launch(["-demo", "list", "-tab", "now"])

        let settings = app.buttons["settingsButton"]
        XCTAssertTrue(settings.waitForExistence(timeout: 10))
        settings.tap()
        let field = app.textFields["serverField"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        XCTAssertFalse(app.buttons["connectButton"].isEnabled)
        field.tap()
        field.typeText("my-mac.example.ts.net:8443")
        XCTAssertTrue(app.buttons["connectButton"].isEnabled)
        // The keyboard's Go key connects, like the button: the sheet closes.
        field.typeText("\n")
        XCTAssertTrue(settings.waitForExistence(timeout: 5))
        XCTAssertFalse(field.exists)
    }
}

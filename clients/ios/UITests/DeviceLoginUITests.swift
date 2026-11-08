import XCTest

/// Pairing and unlock on the demo data. The demo's Mac is unreachable, so pairing and unlocking end
/// with the offline line; the flows with a server are in the AimuxKit tests.
final class DeviceLoginUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    @MainActor
    private func launch(_ state: String) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "list", "-tab", "now", "-deviceLogin", state, "-serverAddress", ""]
        app.launch()
        return app
    }

    @MainActor
    func testPairingTakesALowerCaseCodeWithADash() {
        let app = launch("pairing")
        XCTAssertTrue(app.staticTexts["Pair this device"].waitForExistence(timeout: 10))
        let button = app.buttons["pairButton"]
        XCTAssertFalse(button.isEnabled, "nothing to pair without a code")

        let field = app.textFields["pairCode"]
        field.tap()
        field.typeText("abcd-1234")
        XCTAssertEqual(field.value as? String, "abcd-1234")
        XCTAssertTrue(button.isEnabled)
        button.tap()

        let message = app.staticTexts["loginMessage"]
        XCTAssertTrue(message.waitForExistence(timeout: 30))
        XCTAssertEqual(message.label, "Could not reach your Mac. Check the connection and try again.")
        XCTAssertTrue(app.staticTexts["Pair this device"].exists, "a failed pairing stays on the screen")
    }

    @MainActor
    func testARevokedDeviceSaysSoOnThePairingScreen() {
        let app = launch("revoked")
        XCTAssertTrue(app.staticTexts["pairNote"].waitForExistence(timeout: 10))
        XCTAssertEqual(app.staticTexts["pairNote"].label, "This device is no longer paired.")
    }

    @MainActor
    func testAChangedEnrollmentSaysPairingAgainPlainly() {
        let app = launch("keychanged")
        XCTAssertTrue(app.staticTexts["pairNote"].waitForExistence(timeout: 10))
        XCTAssertEqual(app.staticTexts["pairNote"].label, "This iPhone needs pairing again.")
    }

    @MainActor
    func testTheLockScreenHasAWayOut() {
        let app = launch("locked")
        XCTAssertTrue(app.staticTexts["aimux is locked"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.buttons["changeServer"].exists, "another Mac address is always reachable")
        XCTAssertFalse(app.buttons["pairAgainButton"].exists, "pairing again is offered once an unlock failed")

        // The demo Mac is unreachable: pairing again would not help, so it is not offered.
        app.buttons["unlockButton"].tap()
        XCTAssertTrue(app.staticTexts["loginMessage"].waitForExistence(timeout: 30))
        XCTAssertFalse(app.buttons["pairAgainButton"].exists)
    }

    @MainActor
    func testARefusedUnlockOffersPairingAgain() {
        let app = launch("refused")
        let pairAgain = app.buttons["pairAgainButton"]
        XCTAssertTrue(pairAgain.waitForExistence(timeout: 10))
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "unlock-failed"
        shot.lifetime = .keepAlways
        add(shot)
        pairAgain.tap()
        let warning = "The old pairing stops working. Revoke its entry on your Mac with aimux remote revoke."
        XCTAssertTrue(app.staticTexts[warning].waitForExistence(timeout: 5))
        app.buttons["Pair Again"].tap()
        XCTAssertTrue(app.textFields["pairCode"].waitForExistence(timeout: 5))
    }

    @MainActor
    func testLockInServerSettingsShowsTheUnlockScreen() {
        let app = launch("live")
        let settings = app.buttons["settingsButton"]
        XCTAssertTrue(settings.waitForExistence(timeout: 10))
        settings.tap()
        let lock = app.buttons["lockButton"]
        XCTAssertTrue(lock.waitForExistence(timeout: 5))
        lock.tap()

        XCTAssertTrue(app.staticTexts["aimux is locked"].waitForExistence(timeout: 5))
        let unlock = app.buttons["unlockButton"]
        XCTAssertTrue(unlock.exists)
        XCTAssertFalse(app.staticTexts["loginMessage"].exists, "after Lock, Face ID waits for a tap")

        unlock.tap()
        XCTAssertTrue(app.staticTexts["loginMessage"].waitForExistence(timeout: 30))
        XCTAssertTrue(app.staticTexts["aimux is locked"].exists, "an unlock that fails stays locked")
    }
}

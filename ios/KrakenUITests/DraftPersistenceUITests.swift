import XCTest

// Exercises carl's exact complaint: a half-written steer/answer must not be
// lost when the sheet is dismissed and the composer reopened. Drives the real
// UI (types into the real TextEditor) in the headless simulator, which simctl
// can't do. Relies on the live board: job "kraken-ios" is a running, steerable
// cell while this build is under test.
final class DraftPersistenceUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    func testSteerDraftSurvivesSheetDismissAndReopen() {
        let app = XCUIApplication()
        // -KrakenRoute <url> lands in UserDefaults (the same test hook the
        // orchestrator's screenshot drive uses) → opens the job's steer sheet.
        app.launchArguments = ["-KrakenRoute", "kraken://job/kraken-ios/steer"]
        app.launch()

        let draft = "half-written steer draft \(Int.random(in: 1000...9999))"

        let editor = app.textViews.firstMatch
        XCTAssertTrue(editor.waitForExistence(timeout: 15), "steer composer never appeared")
        editor.tap()
        editor.typeText(draft)

        // dismiss the steer sheet without sending (swipe the sheet down)
        app.swipeDown(velocity: .fast)

        // navigate away and back: Board tab is where the job detail lives; hop
        // to Sessions and back to prove the draft store outlives a tab switch
        app.buttons["Sessions"].tap()
        app.buttons["Board"].tap()

        // reopen steer from the job detail's action bar (icon-only button,
        // reachable by its accessibility label)
        let steerButton = app.buttons["Steer"]
        XCTAssertTrue(steerButton.waitForExistence(timeout: 10), "Steer button missing after navigation")
        steerButton.tap()

        let reopened = app.textViews.firstMatch
        XCTAssertTrue(reopened.waitForExistence(timeout: 10), "steer composer did not reopen")
        // the draft must be exactly what was typed
        let restored = reopened.value as? String ?? ""
        XCTAssertEqual(restored, draft, "draft was not restored after dismiss + navigation")
    }
}

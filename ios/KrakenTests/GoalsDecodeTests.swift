import XCTest
@testable import Kraken

// Pins the goal wire shapes (see ios/API.md's board-level `goals` row and
// its job-level `goal`/`goals` rows under "GET /api/board", its "POST
// /api/goals" section, and internal/api/goals.go) — a goal item with and
// without `parent_id`, a board WITH the additive keys and a board WITHOUT
// any of them, and a job linking MANY goals as well as one.
//
// Two theses, and both are about absence and drift rather than the happy
// path. First: every one of these keys was added additively, so `contract`
// stays 6 and cannot be used to tell an older server apart — the ABSENCE of
// a key is the whole signal, and a decode that threw on it would cost the
// entire board. Second: a job's goals are cosmetic chips on a card while
// the board carrying them is not, so `goals` is decoded tolerantly at both
// levels (a non-array degrades to an empty list, a malformed element is
// dropped and the rest survive) — a drifted server may cost chips and must
// never cost the jobs they are drawn on.
//
// Mirrors ShipIOSDecodeTests, which pins its own additive key the same way.
final class GoalsDecodeTests: XCTestCase {

    private func board(extraKeys: String = "", jobs: String = "[]") -> String {
        """
        {
          "daemon": {"pid": 4242, "started_at": 1780000000000, "last_tick": 1780000004000, "cells": 2},
          "daemon_ok": true,
          "cell_cap": 4,
          "live_cells": 2,
          "jobs": \(jobs),
          "contract": 6,
          "stale": false\(extraKeys)
        }
        """
    }

    // MARK: - the item

    func testDecodesWeekGoal() throws {
        let json = """
        {"id": 7, "scope": "week", "period": "2026-08-10", "text": "ship goal tracking",
         "done": false, "position": 2}
        """
        let g = try JSONDecoder().decode(Goal.self, from: Data(json.utf8))
        XCTAssertEqual(g.id, 7)
        XCTAssertEqual(g.scope, "week")
        XCTAssertEqual(g.period, "2026-08-10")
        XCTAssertEqual(g.text, "ship goal tracking")
        XCTAssertFalse(g.done)
        XCTAssertEqual(g.position, 2)
        // `parent_id` is omitempty and a week goal can never carry one.
        XCTAssertNil(g.parentID)
        XCTAssertTrue(g.isWeek)
        XCTAssertFalse(g.isDay)
    }

    func testDecodesDayGoalWithParent() throws {
        let json = """
        {"id": 11, "scope": "day", "period": "2026-08-13", "text": "lock the spec",
         "done": true, "position": 0, "parent_id": 7}
        """
        let g = try JSONDecoder().decode(Goal.self, from: Data(json.utf8))
        XCTAssertEqual(g.parentID, 7)
        XCTAssertTrue(g.done)
        XCTAssertTrue(g.isDay)
    }

    // MARK: - the board keys

    func testDecodesBoardGoalsKey() throws {
        let json = board(extraKeys: """
        ,
          "goals": {
            "week": [
              {"id": 1, "scope": "week", "period": "2026-08-10", "text": "ship goals", "done": false, "position": 0},
              {"id": 2, "scope": "week", "period": "2026-08-10", "text": "csr cutover", "done": true, "position": 1}
            ],
            "day": [
              {"id": 3, "scope": "day", "period": "2026-08-13", "text": "lock the spec", "done": false, "position": 0, "parent_id": 1},
              {"id": 4, "scope": "day", "period": "2026-08-13", "text": "answer carl", "done": true, "position": 1}
            ]
          }
        """)
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
        let goals = try XCTUnwrap(b.goals)
        XCTAssertEqual(goals.week.map(\.id), [1, 2])
        XCTAssertEqual(goals.day.map(\.id), [3, 4])
        XCTAssertEqual(goals.day.first?.parentID, 1)
        XCTAssertNil(goals.day.last?.parentID)
        // Additive: an older client's contract number is untouched by them.
        XCTAssertEqual(b.contract, 6)
    }

    /// One job, with whatever goal keys the case under test needs.
    private func jobs(goalKeys: String) -> String {
        """
        [{"slug": "kraken-goal-tracking", "repo": "kraken", "state": "working", "live": true,
          "mtime": 1780000000000\(goalKeys)}]
        """
    }

    // A server that predates multi-goal links serves `goal` alone. It still
    // decodes, and `goals` is nil rather than empty — the card falls back to
    // the single key on exactly that nil.
    func testDecodesJobGoalKey() throws {
        let job = jobs(goalKeys: #","goal": {"id": 1, "text": "ship goal tracking", "done": false}"#)
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(board(jobs: job).utf8))
        let g = try XCTUnwrap(b.jobs.first?.goal)
        XCTAssertEqual(g.id, 1)
        XCTAssertEqual(g.text, "ship goal tracking")
        XCTAssertFalse(g.done)
        XCTAssertNil(b.jobs.first?.goals, "absent `goals` is nil, never an empty list")
    }

    // The many-goal shape: `goals` carries every link in position order and
    // `goal` is durably its head, so a client reading either key agrees with
    // one reading the other about which goal comes first.
    func testDecodesJobGoalsArrayInLinkOrder() throws {
        let job = jobs(goalKeys: """
        ,
          "goal": {"id": 3, "text": "ship goals", "done": false},
          "goals": [{"id": 3, "text": "ship goals", "done": false},
                    {"id": 9, "text": "csr cutover", "done": true},
                    {"id": 4, "text": "answer carl", "done": false}]
        """)
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(board(jobs: job).utf8))
        let list = try XCTUnwrap(b.jobs.first?.goals)
        XCTAssertEqual(list.items.map(\.id), [3, 9, 4], "link order, not id order")
        XCTAssertEqual(list.items.map(\.done), [false, true, false])
        XCTAssertEqual(list.items.first, b.jobs.first?.goal, "`goal` is durably goals[0]")
    }

    // The whole additive claim, in one test: a server predating goals serves
    // none of the keys, and that board must decode with everything else
    // intact.
    func testBoardWithoutGoalKeysDecodes() throws {
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(board(jobs: jobs(goalKeys: "")).utf8))
        XCTAssertNil(b.goals)
        XCTAssertNil(b.jobs.first?.goal)
        XCTAssertNil(b.jobs.first?.goals)
        XCTAssertEqual(b.jobs.count, 1)
        XCTAssertEqual(b.cellCap, 4)
        XCTAssertEqual(b.contract, 6)
    }

    // A null `goals` is the absent case, absorbed by WebJob's synthesized
    // `decodeIfPresent` before WebJobGoalList's init ever runs: nil property,
    // not an empty list. The distinction is what lets the card fall back to
    // the single `goal` key on nil and draw nothing on an empty list.
    func testNullJobGoalsKeyDecodesToNil() throws {
        let job = jobs(goalKeys: #","goals": null"#)
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(board(jobs: job).utf8))
        XCTAssertNil(b.jobs.first?.goals)
    }

    // Drift, container level: `goals` present but not an array. Every shape
    // here reaches WebJobGoalList's init (only null/absent is intercepted
    // above), so the `try?` on the array decode is what keeps the JOB — and
    // with it the whole strictly-decoded `jobs` array — alive.
    //
    // Discriminating: with that init's body replaced by a plain
    // `items = try decoder.singleValueContainer().decode([WebJobGoal].self)`,
    // every case below fails the WHOLE board decode with
    // `typeMismatch(Swift.Array<Any>)` — verified this session by making
    // exactly that edit, running the suite, and restoring the original bytes.
    func testNonArrayJobGoalsYieldsAnEmptyListAndAUsableBoard() throws {
        let shapes = [#""a string""#, "42", "true", #"{"id": 1, "text": "x", "done": false}"#]
        for (i, shape) in shapes.enumerated() {
            let job = jobs(goalKeys: #","goal": {"id": 1, "text": "x", "done": false}, "goals": "# + shape)
            let b = try JSONDecoder().decode(WebBoard.self, from: Data(board(jobs: job).utf8))
            XCTAssertEqual(b.jobs.count, 1, "case \(i): the job itself must survive")
            XCTAssertEqual(b.jobs.first?.goals?.items, [], "case \(i)")
            XCTAssertEqual(b.jobs.first?.goal?.id, 1, "case \(i): the primary key is untouched")
            XCTAssertEqual(b.liveCells, 2, "case \(i): and so must the board")
        }
    }

    // Drift, element level: one malformed entry costs its own chip and no
    // more. A plain `[WebJobGoal]` decode cannot do this — one bad element
    // throws for the entire array — which is what TolerantWebJobGoal exists
    // for.
    //
    // Discriminating: with the same strict-init edit as above, this case
    // fails the whole board decode with
    // `typeMismatch(Swift.Int64, … codingPath: jobs / Index 0 / goals /
    // Index 1 / id)` — the first bad element, reached THROUGH the jobs
    // array, which is the blast radius this tolerance caps — rather than
    // returning the two good goals; verified the same way, by making the
    // edit, running, and restoring.
    func testMalformedGoalElementIsDroppedAndTheRestSurvive() throws {
        let job = jobs(goalKeys: """
        ,
          "goals": [{"id": 1, "text": "ship goals", "done": false},
                    {"id": "not a number", "text": "drifted", "done": false},
                    "a bare string",
                    {"text": "no id at all", "done": true},
                    {"id": 4, "text": "answer carl", "done": true}]
        """)
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(board(jobs: job).utf8))
        let list = try XCTUnwrap(b.jobs.first?.goals)
        XCTAssertEqual(list.items.map(\.id), [1, 4], "the three malformed elements are dropped")
        XCTAssertEqual(list.items.map(\.text), ["ship goals", "answer carl"], "order survives")
        XCTAssertEqual(b.liveCells, 2, "the board itself must survive")
    }

    // An EMPTY array is a shape the server never sends (`goals` is
    // omitempty, so zero links means no key), but a client must not crash on
    // one: it decodes to an empty list, which the card reads as "no chips"
    // rather than falling back to `goal`.
    func testEmptyJobGoalsArrayDecodesToAnEmptyList() throws {
        let job = jobs(goalKeys: #","goals": []"#)
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(board(jobs: job).utf8))
        XCTAssertEqual(b.jobs.first?.goals?.items, [])
    }

    // Blast radius (see WebGoals' header in Models.swift): the drawer is
    // cosmetic and the board is not, so a drifted goal costs the drawer.
    func testMalformedGoalsStillYieldAUsableBoard() throws {
        let json = board(extraKeys: """
        ,
          "goals": {"week": [{"id": "1", "scope": "week"}], "day": null}
        """)
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
        XCTAssertEqual(b.liveCells, 2, "the board itself must survive")
        XCTAssertEqual(b.goals?.week, [])
        XCTAssertEqual(b.goals?.day, [])
    }

    // The container-level half of the same guarantee, and the one the
    // per-key `try?`s do NOT cover: `goals` present but not an OBJECT.
    // `container(keyedBy:)` throws for a string/number/array, and that throw
    // escapes WebBoard's synthesized `decodeIfPresent` exactly as an inner
    // failure would — so without the `try?` on the container itself the
    // whole board is lost. Discriminating: with the guard reverted to a
    // plain `try decoder.container(...)`, every case below fails with
    // `typeMismatch(Swift.Dictionary<String, Any>)`, verified by reverting
    // it and re-running this test.
    func testNonObjectGoalsKeyStillYieldsAUsableBoard() throws {
        let shapes = [#""a string""#, "42", "[]", #"[{"id": 1}]"#, "true"]
        for (i, shape) in shapes.enumerated() {
            let json = board(extraKeys: ",\n  \"goals\": \(shape)")
            let b = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
            XCTAssertEqual(b.liveCells, 2, "case \(i): the board itself must survive")
            XCTAssertEqual(b.goals?.week, [], "case \(i)")
            XCTAssertEqual(b.goals?.day, [], "case \(i)")
        }
    }

    // An absent or null KEY is a different case, absorbed by decodeIfPresent
    // before WebGoals' init runs at all: it nils the whole property rather
    // than producing an empty one.
    func testNullGoalsKeyDecodesToNil() throws {
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(board(extraKeys: ",\n  \"goals\": null").utf8))
        XCTAssertNil(b.goals)
    }

    // MARK: - grouping

    func testGroupingNestsDaysAndCounts() {
        let week = [
            Goal(id: 1, scope: "week", period: "2026-08-10", text: "a", done: false, position: 0, parentID: nil),
            Goal(id: 2, scope: "week", period: "2026-08-10", text: "b", done: true, position: 1, parentID: nil),
        ]
        let day = [
            Goal(id: 3, scope: "day", period: "2026-08-13", text: "c", done: false, position: 0, parentID: 1),
            Goal(id: 4, scope: "day", period: "2026-08-13", text: "d", done: true, position: 1, parentID: nil),
            // A parent that is not in THIS payload's week list (last week's
            // goal, say) must still render — as unassigned, not nowhere.
            Goal(id: 5, scope: "day", period: "2026-08-13", text: "e", done: false, position: 2, parentID: 99),
        ]
        let g = GoalGrouping(WebGoals(week: week, day: day))

        XCTAssertEqual(g.weeks.map(\.goal.id), [1, 2])
        XCTAssertEqual(g.weeks.first?.days.map(\.id), [3])
        XCTAssertEqual(g.weeks.last?.days.map(\.id), [])
        XCTAssertEqual(g.unassigned.map(\.id), [4, 5])
        XCTAssertEqual(g.weekDone, 1)
        XCTAssertEqual(g.weekTotal, 2)
        // Day counts span nested and unassigned alike.
        XCTAssertEqual(g.dayDone, 1)
        XCTAssertEqual(g.dayTotal, 3)
        XCTAssertEqual(g.summary, "week 1/2 · today 1/3")
        XCTAssertFalse(g.isEmpty)
    }

    func testEmptyGoalsGroupToEmpty() {
        let g = GoalGrouping(WebGoals(week: [], day: []))
        XCTAssertTrue(g.isEmpty)
        XCTAssertEqual(g.summary, "week 0/0 · today 0/0")
    }

    // The drawer gates on THIS predicate, so it has to be the one that knows
    // about a scope neither client recognizes: a week array holding only
    // such rows is a non-empty payload and an empty grouping, and the drawer
    // draws nothing rather than a header with no rows under it.
    func testUnrecognizedWeekScopeGroupsToEmpty() {
        let payload = WebGoals(
            week: [Goal(id: 1, scope: "month", period: "2026-08-10", text: "a", done: false, position: 0, parentID: nil)],
            day: []
        )
        XCTAssertFalse(payload.week.isEmpty, "the payload itself is not empty")
        let g = GoalGrouping(payload)
        XCTAssertTrue(g.isEmpty, "but there is nothing to draw")
        XCTAssertEqual(g.weekTotal, 0, "and nothing to count either")
    }

    // MARK: - the card chip's text

    // The rule web's `goalChipText` states, pinned on this side too: the two
    // clients truncate the same goal to the same string, so a card seen on
    // the phone and the same card seen on the board read identically.
    func testChipTextLeavesShortTextAlone() {
        XCTAssertEqual(goalChipText("ship goals"), "ship goals")
        // Exactly at the limit is UNCHANGED — the boundary is `>`, not `>=`,
        // so a 24-character goal never pays an ellipsis for nothing.
        let exact = String(repeating: "x", count: 24)
        XCTAssertEqual(goalChipText(exact), exact)
    }

    func testChipTextTruncatesWithOneEllipsis() {
        let long = String(repeating: "x", count: 40)
        let out = goalChipText(long)
        XCTAssertEqual(out.count, 25, "never longer than max + 1")
        XCTAssertTrue(out.hasSuffix("…"))
        XCTAssertEqual(out, String(repeating: "x", count: 24) + "…")
    }

    // A cut landing on whitespace must not leave "word …" — and the trim is
    // `\s+$`, not ` +$`, so a tab or a newline goes the same way a space
    // does (the TS twin trims whitespace, and a rule pinned in two clients
    // has to agree on every input, not just the common one).
    func testChipTextDropsTrailingWhitespaceBeforeTheEllipsis() {
        XCTAssertEqual(goalChipText("ship goal tracking into the kraken board", max: 18), "ship goal tracking…")
        XCTAssertEqual(goalChipText("ship goal tracking\tinto the board", max: 19), "ship goal tracking…")
        XCTAssertEqual(goalChipText("ship goal tracking \u{00a0}into the board", max: 20), "ship goal tracking…")
        XCTAssertFalse(goalChipText("abc def", max: 4).contains(" …"))
    }

    // A degenerate max is a caller error, not a crash: 0 yields the bare
    // ellipsis (what the TS expression yields at 0), and a negative clamps
    // to 0 rather than trapping in `prefix(_:)`.
    func testChipTextHandlesADegenerateMax() {
        XCTAssertEqual(goalChipText("ship goals", max: 0), "…")
        XCTAssertEqual(goalChipText("ship goals", max: -5), "…")
        XCTAssertEqual(goalChipText("", max: 0), "", "nothing to truncate")
    }

    // Swift counts grapheme clusters where the TS twin counts code points.
    // They agree on single-scalar emoji like this one — which is what this
    // test pins — and diverge only on a multi-scalar cluster (ZWJ family,
    // flag, skin tone), where Swift's cut is the more conservative of the
    // two. Both stay inside their own max + 1; see goalChipText's comment.
    func testChipTextCountsGraphemeClusters() {
        let text = String(repeating: "🐙", count: 30)
        XCTAssertEqual(goalChipText(text).count, 25)
        XCTAssertEqual(goalChipText(String(repeating: "🐙", count: 5)).count, 5)
        // The divergent case, stated rather than hidden: one family emoji is
        // ONE Character here, so a 24-max leaves it untouched.
        let family = "👨‍👩‍👧‍👦 ship goals"
        XCTAssertEqual(goalChipText(family), family)
    }

    // MARK: - the one write the phone makes

    func testGoalUpdateRequestEncodesTheUpdateOp() throws {
        let data = try JSONEncoder().encode(GoalUpdateRequest(id: 12, done: true))
        let obj = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        XCTAssertEqual(obj["op"] as? String, "update")
        XCTAssertEqual(obj["id"] as? Int, 12)
        XCTAssertEqual(obj["done"] as? Bool, true)
        // No text, no parent_id, no scope: an absent field means unchanged
        // server-side, and the phone changes exactly one thing.
        XCTAssertEqual(obj.keys.count, 3)
    }
}

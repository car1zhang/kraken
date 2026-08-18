import type { WebJob } from "../../../api/types.ts";
import { age } from "../../../lib/format.ts";

/**
 * The chip's own rule, split out so the card can ask whether the chip row is
 * empty without duplicating it — the same split StageChip, DocQuestionChip and
 * FeedbackChip make.
 *
 * Two conditions:
 *
 *   1. the server sent an account. Absence is the wire's ONLY spelling of
 *      unbound (see WebJob.account), and an unbound cell shows NOTHING here
 *      rather than a "main" chip: main is what every cell ran on before this
 *      feature, so a badge for it would put a token on almost every card that
 *      says only "nothing unusual". The steer modal still answers the question
 *      outright, which is where a reader who is actually asking it goes.
 *   2. the card must read as LIVE — `job.state` is `working` or `blocked`. A
 *      binding is a fact about a cell that is RUNNING: which login it is
 *      spending on right now. A done or draft card has no cell, so the same
 *      badge there would be a dead fact reading as a current one, which is the
 *      argument LoadChip makes for its own column test.
 *
 * Condition 2 reads `job.state` rather than the display column, and that is
 * deliberate rather than a shortcut. `col` carries bucketJobs' health override
 * (a live cell whose file still says "working" is filed under blocked), and
 * that override moves a card between the two states this predicate ALREADY
 * accepts — so it cannot change the answer. LoadChip needs `col` because only
 * ONE of the two qualifies there; here either does, and reading the raw state
 * keeps the chip identical on the card, in the drag overlay, and anywhere else
 * the card is rendered with a column of the caller's choosing.
 */
export function accountChipVisible(job: WebJob): boolean {
  return !!job.account && (job.state === "working" || job.state === "blocked");
}

/**
 * The email's LOCAL PART — everything before the first `@` — or the whole
 * string when there is no `@` in it.
 *
 * Split on the FIRST `@` rather than the last: an address may legally carry
 * more in a quoted local part, and the leading run is the human-recognisable
 * half either way. A leading `@` yields "", which the caller treats exactly as
 * an empty email and falls back to the slug for — a chip reading `@gmail.com`
 * would name the provider and not the account.
 */
export function accountLocalPart(email: string): string {
  const at = email.indexOf("@");
  return at < 0 ? email : email.slice(0, at);
}

/**
 * The card's account badge: WHICH pool account this cell is burning, as the
 * email's local part behind a `⬢` glyph — `⬢ dorisexplorer4`.
 *
 * The local part and not the full address, because the card is SCANNED: every
 * pool account shares one provider domain, so the domain is the half that
 * carries no information and costs the most width in a row that wraps. The
 * full sentence — address, slot slug and bind age — rides `title` and
 * `aria-label`, which is the same split LoadChip makes for the same reason.
 * The age is in it rather than beside it because it is what makes a REBIND
 * legible: the limit rescue rebinds one cell mid-run, and "bound 4m ago" on a
 * two-hour-old card is the whole story of what just happened to it.
 *
 * The visible text falls back to the slot SLUG when the email is empty (a slot
 * ccp has no identity for yet) rather than rendering nothing: the slug is what
 * kraken actually selected and what the usage rows are keyed by, so it answers
 * the question — less pleasantly, but it answers it.
 *
 * Class rules, both inherited from the sibling chips and both load-bearing:
 *
 *   1. `ac-chip`, and NOT `chip` — `.chip` is the artifact-overlay click
 *      target, so a chip-classed element would try to open an artifact with an
 *      undefined url instead of falling through to the card's own click
 *      handler. Same hazard `st-chip`, `ct-chip`, `dq-chip`, `fb-chip` and
 *      `ld-chip` were each named around. A plain span rather than a button for
 *      the same reason: the badge is a name to read, not a new click target,
 *      and the binding is READ-ONLY on every surface — nothing in the UI
 *      switches a cell's account. style.css's pointer cursor on it is the
 *      CARD's affordance showing through an element that does not intercept
 *      the click, never a claim that the badge is clickable.
 *   2. every colour is a theme token (style.css's `.ac-chip`), so both
 *      palettes follow without a hex anywhere here.
 *
 * The glyph is aria-hidden: a screen reader gets the full sentence through
 * `aria-label`, and `⬢` has no useful spoken form. The same `⬢` is the mockup
 * set's account mark; it is deliberately not one of the six stage glyphs.
 */
export function AccountChip({ job }: { job: WebJob }) {
  const account = job.account;
  // The visibility rule is asked in full rather than re-spelled: a component
  // that renders under conditions its own `*Visible` export disagrees with is
  // exactly how a chip appears in a row the card decided was empty.
  if (!account || !accountChipVisible(job)) return null;
  const local = accountLocalPart(account.email);
  // Slug is the fallback for BOTH an empty email and an address whose local
  // part is empty — see accountLocalPart.
  const label = local || account.slug;
  // `at` is unix MILLIS and age() takes SECONDS — the same divide LoadDrill and
  // TokensPanel each spell out. A zero stamp drops the clause rather than
  // rendering "0s ago", which would date a binding to right now.
  const when = age(account.at ? Math.floor(account.at / 1000) : 0);
  // One sentence on both attributes, LoadChip's split: `title` is what a
  // pointer gets and reaches nothing else (touch has no hover, and screen
  // readers announce a title only inconsistently and as a fallback), so the
  // sentence is also the element's accessible NAME — which here REPLACES the
  // visible text, because that text is a bare local part and the address plus
  // the slot it names is what makes it mean anything.
  const title =
    "bound to " +
    (account.email || account.slug) +
    " (slot " +
    account.slug +
    ")" +
    (when ? ", " + when + " ago" : "");
  return (
    <span className="ac-chip" title={title} aria-label={title}>
      <span aria-hidden="true">⬢ </span>
      {label}
    </span>
  );
}

// The board's five columns, in display order.
//
// This lives in its own leaf module — importing nothing — to break a real
// import cycle. COLUMNS used to be declared in hooks/useBoard.ts, which was
// fine only while the dependency ran one way: format.ts imported COLUMNS from
// the hook. The moment useBoard started importing bucketJobs back from
// format.ts (so that the render layer and the drag layer share ONE bucketing —
// see useBoard.ts), the two modules formed a cycle, and the symptom was not an
// import error but a bare `TypeError: usageColorClass is not a function` from
// an unrelated overlay test: under a cycle, whichever module is evaluated
// second sees the first's exports still uninitialized.
//
// Keeping the vocabulary in a module with no imports of its own makes that
// class of failure impossible rather than merely absent.

export const COLUMNS = ["draft", "queued", "working", "blocked", "done"] as const;

export type Column = (typeof COLUMNS)[number];

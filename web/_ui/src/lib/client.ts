// The kronos slice's contract names `lib/client.ts` as the module holding
// `fetchKronos` and `KronosStatus`. This board's one typed-endpoint module is
// `src/api/client.ts` (with its wire types in `src/api/types.ts`), and putting a
// second fetch layer beside it would be two clients — so the functions live
// where every other endpoint's does and this file re-exports them under the
// contracted path.
//
// Re-exports only: nothing is defined here, so there is one implementation and
// one wire type no matter which path a caller imports.

export {
  fetchKronos,
  fetchOrchInbox,
  fetchPaneRead,
  sendToPane,
} from "../api/client.ts";

export type {
  KronosStatus,
  OrchInbox,
  OrchItem,
  PeersRead,
} from "../api/types.ts";

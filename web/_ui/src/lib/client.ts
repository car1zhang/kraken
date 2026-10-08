// This board's one typed-endpoint module is `src/api/client.ts` (with its wire
// types in `src/api/types.ts`), and putting a second fetch layer beside it
// would be two clients — so the peers functions live where every other
// endpoint's does and this file only re-exports them.
//
// Re-exports only: nothing is defined here, so there is one implementation and
// one wire type no matter which path a caller imports.

export { fetchPaneRead, sendToPane } from "../api/client.ts";

export type { PeersRead } from "../api/types.ts";

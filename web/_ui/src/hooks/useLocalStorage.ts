import { useCallback, useState } from "react";

/**
 * localStorage that cannot throw.
 *
 * Every access is guarded. The vanilla board only wrapped three of its seven
 * keys (the theme stamp and the two banner dismissals); the other four —
 * kraken-view, kraken-archive-open, kraken-expanded-artifacts,
 * kraken-thread-mirror — would throw outright in a blocked-storage or
 * sandboxed context and take the board down with them. Fixing that was called
 * out in artifacts/51-code-map.md as a port-time cleanup.
 */
export function readLS(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function writeLS(key: string, value: string | null): void {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, value);
  } catch {
    // a dead store must still leave the board working for this page load
  }
}

/** String-valued key. */
export function useLocalStorage(
  key: string,
  initial: string,
): [string, (v: string) => void] {
  const [value, setValue] = useState(() => readLS(key) ?? initial);
  const set = useCallback(
    (v: string) => {
      setValue(v);
      writeLS(key, v);
    },
    [key],
  );
  return [value, set];
}

/** Boolean key stored as "1"/"0", matching the vanilla board's encoding. */
export function useLocalStorageFlag(
  key: string,
  initial: boolean,
): [boolean, (v: boolean) => void] {
  const [value, setValue] = useState(() => {
    const raw = readLS(key);
    return raw === null ? initial : raw === "1";
  });
  const set = useCallback(
    (v: boolean) => {
      setValue(v);
      writeLS(key, v ? "1" : "0");
    },
    [key],
  );
  return [value, set];
}

/**
 * A Set of ids persisted as a JSON array. Backs kraken-expanded-artifacts,
 * which the vanilla board persisted for a reason that no longer applies —
 * it had to survive the 3s innerHTML rebuild. It stays persisted because
 * surviving a page RELOAD is still worth having.
 */
export function useLocalStorageSet(
  key: string,
): [Set<string>, (id: string) => void] {
  const [value, setValue] = useState<Set<string>>(() => {
    const raw = readLS(key);
    if (!raw) return new Set();
    try {
      const arr: unknown = JSON.parse(raw);
      return Array.isArray(arr) ? new Set(arr.filter((x) => typeof x === "string")) : new Set();
    } catch {
      return new Set();
    }
  });
  const toggle = useCallback(
    (id: string) => {
      setValue((prev) => {
        const next = new Set(prev);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        writeLS(key, JSON.stringify([...next]));
        return next;
      });
    },
    [key],
  );
  return [value, toggle];
}

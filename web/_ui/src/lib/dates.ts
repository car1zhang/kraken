/**
 * A unix-millis instant as a local calendar day ("2026-07-28"), matching how
 * the DREAM view names a night. Local rather than UTC for the same reason the
 * dream loop's day boundary is local: the reader is in one timezone, always
 * this one. Zero and unparseable instants render as "" rather than as 1970.
 */
export function shortDate(ms: number): string {
  if (!ms) return "";
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** Best-effort ISO→display formatter for authority `observed_at` timestamps. */
export function formatAuthorityTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString();
}
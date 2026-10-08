export function normalizeTicketInput(raw: string): string | undefined {
  const value = raw.trim();
  if (!value) return "";
  if (/^[A-Z][A-Z0-9_]*-[1-9][0-9]*$/i.test(value)) {
    return `https://st.yandex-team.ru/${value.toUpperCase()}`;
  }
  if (!/^https?:\/\//i.test(value)) return undefined;
  try {
    const url = new URL(value);
    if (!url.hostname || url.username || url.password) return undefined;
    return url.href;
  } catch { return undefined; }
}

export function ticketLabel(raw: string): string {
  try {
    const url = new URL(raw);
    const segment = url.pathname.split("/").filter(Boolean).at(-1);
    if (!segment) return url.host || "Open ticket";
    try { return decodeURIComponent(segment); } catch { return segment; }
  } catch { return "Open ticket"; }
}

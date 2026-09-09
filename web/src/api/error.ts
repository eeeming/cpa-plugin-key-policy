// Extract the most actionable message from an API failure.
//
// Axios reports only "Request failed with status code 400"; the plugin's real
// text lives in the JSON error envelope the Go handlers return:
//   {"error":{"code":"invalid_policy","message":"key hash already bound to ..."}}
// Falling back to e.message keeps network/timeout errors readable.
export function errText(error: unknown, fallback: string): string {
  if (typeof error === "string" && error.trim()) return error.trim();
  const e = error as { response?: { data?: unknown }; message?: unknown } | null | undefined;
  const fromBody = messageFromPayload(e?.response?.data);
  if (fromBody) return fromBody;
  if (typeof e?.message === "string" && e.message.trim()) return e.message.trim();
  return fallback;
}

function messageFromPayload(data: unknown): string | undefined {
  if (!data || typeof data !== "object") return undefined;
  const err = (data as { error?: unknown }).error;
  if (typeof err === "string" && err.trim()) return err.trim();
  if (err && typeof err === "object") {
    const message = (err as { message?: unknown }).message;
    const code = (err as { code?: unknown }).code;
    if (typeof message === "string" && message.trim()) {
      const msg = message.trim();
      return typeof code === "string" && code.trim() && code.trim() !== msg ? `${msg} (${code.trim()})` : msg;
    }
    if (typeof code === "string" && code.trim()) return code.trim();
  }
  const top = (data as { message?: unknown }).message;
  if (typeof top === "string" && top.trim()) return top.trim();
  return undefined;
}

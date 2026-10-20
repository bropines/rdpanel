/**
 * Server share links (api/openapi.yaml, tag `share`): admin endpoints under
 * /api/v1/admin/share-links and the public page data at /s/{token}/info.
 */
import { api, type Schemas } from "@/api/client";

export type ShareLink = Schemas["ShareLink"];
export type ShareLinkInput = Schemas["ShareLinkInput"];
export type ShareLinkStatus = Schemas["ShareLinkStatus"];
export type SharePublicInfo = Schemas["SharePublicInfo"];
export type ShareScope = "mine" | "all";

export const shareKeys = {
  all: ["share-links"] as const,
  list: (scope: ShareScope) => ["share-links", scope] as const,
};

export const shareLinksApi = {
  list: (scope: ShareScope) => api.request<Schemas["ShareLinkList"]>("/share-links", { query: { scope } }),
  create: (body: ShareLinkInput) => api.request<ShareLink>("/share-links", { method: "POST", body }),
  revoke: (id: number) => api.request<void>(`/share-links/${id}`, { method: "DELETE" }),
};

/** Public base path of the share pages (served outside /api, no session). */
export const SHARE_BASE = "/s";

export type ShareInfoResult =
  | { kind: "ok"; info: SharePublicInfo }
  | { kind: "invalid" }
  | { kind: "rate_limited" }
  | { kind: "error" };

/** Loads the public page data; 404/410 mean the link is not (or no longer) valid. */
export async function fetchShareInfo(token: string, doFetch: typeof fetch = (...a) => fetch(...a)): Promise<ShareInfoResult> {
  let res: Response;
  try {
    res = await doFetch(`${SHARE_BASE}/${encodeURIComponent(token)}/info`, {
      headers: { Accept: "application/json" },
      credentials: "omit",
      cache: "no-store",
      referrerPolicy: "no-referrer",
    });
  } catch {
    return { kind: "error" };
  }
  if (res.status === 404 || res.status === 410) return { kind: "invalid" };
  if (res.status === 429) return { kind: "rate_limited" };
  if (!res.ok) return { kind: "error" };
  try {
    return { kind: "ok", info: (await res.json()) as SharePublicInfo };
  } catch {
    return { kind: "error" };
  }
}

/**
 * WebAuthn glue between the server's JSON ceremony options (go-webauthn:
 * `{publicKey: {...}}` with base64url binary members) and
 * `navigator.credentials`. Works without the newer
 * `PublicKeyCredential.parse*OptionsFromJSON` / `toJSON` browser APIs.
 */

export type JsonObject = Record<string, unknown>;

/** base64url (padding optional) → ArrayBuffer. */
export function b64urlToBuffer(s: string): ArrayBuffer {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(s.length / 4) * 4, "=");
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out.buffer;
}

/** ArrayBuffer / view → base64url without padding. */
export function bufferToB64url(buf: ArrayBuffer | ArrayBufferView): string {
  const bytes = buf instanceof ArrayBuffer ? new Uint8Array(buf) : new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength);
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** Whether this browser can use passkeys at all (secure context + API). */
export function passkeysSupported(): boolean {
  return typeof window !== "undefined" && window.isSecureContext !== false && typeof window.PublicKeyCredential === "function" && !!navigator.credentials;
}

type Descriptor = { id: string; type: string; transports?: string[] };

function descriptors(list: unknown): PublicKeyCredentialDescriptor[] | undefined {
  if (!Array.isArray(list)) return undefined;
  return (list as Descriptor[]).map((d) => ({
    type: "public-key",
    id: b64urlToBuffer(d.id),
    ...(d.transports ? { transports: d.transports as AuthenticatorTransport[] } : {}),
  }));
}

function publicKeyOf(options: JsonObject): JsonObject {
  const pk = options.publicKey;
  if (!pk || typeof pk !== "object") throw new Error("invalid WebAuthn options");
  return pk as JsonObject;
}

/** Server creation options → `CredentialCreationOptions`. */
export function toCreationOptions(options: JsonObject): CredentialCreationOptions {
  const pk = publicKeyOf(options);
  const user = pk.user as JsonObject;
  const publicKey = {
    ...pk,
    challenge: b64urlToBuffer(pk.challenge as string),
    user: { ...user, id: b64urlToBuffer(user.id as string) },
    excludeCredentials: descriptors(pk.excludeCredentials) ?? [],
  } as unknown as PublicKeyCredentialCreationOptions;
  return { publicKey };
}

/** Server request options → `CredentialRequestOptions`. */
export function toRequestOptions(options: JsonObject): CredentialRequestOptions {
  const pk = publicKeyOf(options);
  const publicKey = { ...pk, challenge: b64urlToBuffer(pk.challenge as string) } as unknown as PublicKeyCredentialRequestOptions;
  const allow = descriptors(pk.allowCredentials);
  if (allow) publicKey.allowCredentials = allow;
  else delete (publicKey as { allowCredentials?: unknown }).allowCredentials;
  return { publicKey, ...(options.mediation ? { mediation: options.mediation as CredentialMediationRequirement } : {}) };
}

/** `PublicKeyCredential` → the JSON the server parses (like `toJSON()`). */
export function credentialToJSON(cred: PublicKeyCredential): JsonObject {
  const r = cred.response as AuthenticatorResponse & Partial<AuthenticatorAttestationResponse & AuthenticatorAssertionResponse>;
  const response: JsonObject = { clientDataJSON: bufferToB64url(r.clientDataJSON) };
  if (r.attestationObject) {
    response.attestationObject = bufferToB64url(r.attestationObject);
    if (typeof r.getTransports === "function") response.transports = r.getTransports();
  }
  if (r.authenticatorData) response.authenticatorData = bufferToB64url(r.authenticatorData);
  if (r.signature) response.signature = bufferToB64url(r.signature);
  if (r.userHandle) response.userHandle = bufferToB64url(r.userHandle);
  return {
    id: cred.id,
    rawId: bufferToB64url(cred.rawId),
    type: cred.type,
    authenticatorAttachment: cred.authenticatorAttachment ?? undefined,
    clientExtensionResults: cred.getClientExtensionResults?.() ?? {},
    response,
  };
}

/** Runs navigator.credentials.create() for server options. */
export async function createPasskey(options: JsonObject): Promise<JsonObject> {
  const cred = (await navigator.credentials.create(toCreationOptions(options))) as PublicKeyCredential | null;
  if (!cred) throw new DOMException("no credential", "NotAllowedError");
  return credentialToJSON(cred);
}

/** Runs navigator.credentials.get() for server options. */
export async function getPasskey(options: JsonObject): Promise<JsonObject> {
  const cred = (await navigator.credentials.get(toRequestOptions(options))) as PublicKeyCredential | null;
  if (!cred) throw new DOMException("no credential", "NotAllowedError");
  return credentialToJSON(cred);
}

/** i18n key for a browser-side WebAuthn failure, or null when it is not one. */
export function webauthnErrorKey(err: unknown): string | null {
  if (!(err instanceof DOMException) && !(err instanceof Error && err.name)) return null;
  switch ((err as { name: string }).name) {
    case "NotAllowedError":
    case "AbortError":
      return "passkeys.cancelled";
    case "InvalidStateError":
      return "passkeys.alreadyRegistered";
    case "SecurityError":
      return "passkeys.securityError";
    case "NotSupportedError":
      return "passkeys.unsupported";
    default:
      return null;
  }
}

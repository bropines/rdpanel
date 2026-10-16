import { describe, expect, it } from "vitest";

import { ApiError } from "@/api/client";
import { b64urlToBuffer, bufferToB64url, credentialToJSON, toCreationOptions, toRequestOptions, webauthnErrorKey } from "@/lib/webauthn";
import { defaultPasskeyName } from "@/pages/my/PasskeysSection";
import { loginErrorKey } from "@/pages/login-errors";

const bytes = (b: ArrayBuffer | BufferSource | undefined) => Array.from(new Uint8Array(b as ArrayBuffer));

describe("webauthn helpers", () => {
  it("round-trips base64url without padding", () => {
    for (const data of [[], [0], [251, 255], [1, 2, 3, 4, 5], Array.from({ length: 64 }, (_, i) => (i * 37) % 256)]) {
      const s = bufferToB64url(new Uint8Array(data));
      expect(s).not.toMatch(/[+/=]/);
      expect(bytes(b64urlToBuffer(s))).toEqual(data);
    }
    expect(bytes(b64urlToBuffer("-_8"))).toEqual([251, 255]);
  });

  it("decodes creation options from the server JSON", () => {
    const opts = toCreationOptions({
      publicKey: {
        rp: { id: "panel.example.com", name: "Panel" },
        user: { id: "AQID", name: "alice", displayName: "Alice" },
        challenge: "BAUG",
        pubKeyCredParams: [{ type: "public-key", alg: -7 }],
        excludeCredentials: [{ type: "public-key", id: "Bwg", transports: ["usb"] }],
        authenticatorSelection: { residentKey: "required" },
      },
    });
    const pk = opts.publicKey!;
    expect(bytes(pk.challenge)).toEqual([4, 5, 6]);
    expect(bytes(pk.user.id)).toEqual([1, 2, 3]);
    expect(pk.user.name).toBe("alice");
    expect(pk.rp.id).toBe("panel.example.com");
    expect(bytes(pk.excludeCredentials![0]!.id)).toEqual([7, 8]);
    expect(pk.excludeCredentials![0]!.transports).toEqual(["usb"]);
  });

  it("decodes request options with and without allowCredentials", () => {
    const usernameless = toRequestOptions({ publicKey: { challenge: "AQID", rpId: "x.test", userVerification: "required" } });
    expect(usernameless.publicKey!.allowCredentials).toBeUndefined();
    expect(bytes(usernameless.publicKey!.challenge)).toEqual([1, 2, 3]);
    const mfa = toRequestOptions({ publicKey: { challenge: "AQID", allowCredentials: [{ type: "public-key", id: "CQ" }] }, mediation: "optional" });
    expect(bytes(mfa.publicKey!.allowCredentials![0]!.id)).toEqual([9]);
    expect(mfa.mediation).toBe("optional");
    expect(() => toRequestOptions({})).toThrow();
  });

  it("serialises attestation and assertion credentials", () => {
    const buf = (...b: number[]) => new Uint8Array(b).buffer;
    const attestation = {
      id: "AQ",
      rawId: buf(1),
      type: "public-key",
      authenticatorAttachment: "platform",
      getClientExtensionResults: () => ({}),
      response: { clientDataJSON: buf(2), attestationObject: buf(3), getTransports: () => ["internal"] },
    } as unknown as PublicKeyCredential;
    expect(credentialToJSON(attestation)).toEqual({
      id: "AQ",
      rawId: "AQ",
      type: "public-key",
      authenticatorAttachment: "platform",
      clientExtensionResults: {},
      response: { clientDataJSON: "Ag", attestationObject: "Aw", transports: ["internal"] },
    });
    const assertion = {
      id: "AQ",
      rawId: buf(1),
      type: "public-key",
      authenticatorAttachment: null,
      response: { clientDataJSON: buf(2), authenticatorData: buf(4), signature: buf(5), userHandle: buf(6) },
    } as unknown as PublicKeyCredential;
    expect(credentialToJSON(assertion).response).toEqual({ clientDataJSON: "Ag", authenticatorData: "BA", signature: "BQ", userHandle: "Bg" });
  });

  it("maps browser and API failures to messages", () => {
    expect(webauthnErrorKey(new DOMException("x", "NotAllowedError"))).toBe("passkeys.cancelled");
    expect(webauthnErrorKey(new DOMException("x", "InvalidStateError"))).toBe("passkeys.alreadyRegistered");
    expect(webauthnErrorKey(new DOMException("x", "SecurityError"))).toBe("passkeys.securityError");
    expect(webauthnErrorKey(new ApiError(401, "passkey_failed", "x"))).toBeNull();
    expect(webauthnErrorKey("nope")).toBeNull();
    expect(loginErrorKey(new ApiError(401, "passkey_failed", "x"))).toBe("login.passkeyFailed");
    expect(loginErrorKey(new ApiError(409, "no_passkeys", "x"))).toBe("login.noPasskeys");
  });

  it("suggests a passkey name from the platform", () => {
    expect(defaultPasskeyName("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5)")).toBe("Mac");
    expect(defaultPasskeyName("Mozilla/5.0 (Windows NT 10.0; Win64; x64)")).toBe("Windows");
    expect(defaultPasskeyName("Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)")).toBe("iPhone / iPad");
    expect(defaultPasskeyName("curl/8")).toBe("");
  });
});

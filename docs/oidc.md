# Sign-in with OIDC

RD Panel can sign users in through GitHub, Google or any OpenID Connect provider, both in
the panel and in the RustDesk client ("Login" → provider button). Providers are managed in
**Identity providers** (admin only).

There is exactly one redirect URI to register at the provider:

```
<RDPANEL_PUBLIC_URL>/api/oidc/callback
```

It serves both the panel and the RustDesk client. With split hosts (public client API +
internal panel, see [`deploy.md`](deploy.md#split-hosts-public-client-api--internal-admin-panel))
the callback is still on the public host and the panel session is handed over to the
internal host afterwards; the reverse proxy must expose `/api/oidc/callback` publicly.

## Provider settings in the panel

| field | value |
|---|---|
| Kind | `oidc` (or `github` / `google`) |
| Slug | short id, e.g. `authentik`; part of URLs, keep it stable |
| Display name | button text in the panel and the RustDesk client |
| Client ID / secret | from the provider (the secret is stored encrypted) |
| Issuer | the provider's issuer URL; `/.well-known/openid-configuration` is fetched from it |
| Scopes | `openid profile email` |
| PKCE | on, `S256` |
| Auto-register | create a panel user on first sign-in; leave off to allow only linked accounts |
| Link by verified e-mail | link to an existing user with the same address, **only** if the provider sends `email_verified: true` |

**Test** checks discovery and the client credentials. Existing users can link a provider
themselves: sign in with the password, then **Profile & security → Linked accounts**.

## Authentik

1. **Applications → Providers → Create → OAuth2/OpenID Provider**
   - Name: `rdpanel`
   - Authorization flow: `default-provider-authorization-implicit-consent` (no extra
     consent screen) or the explicit one
   - Client type: **Confidential**; copy the Client ID and Client Secret
   - Redirect URIs: `Strict`, `https://<public host>/api/oidc/callback`
   - Signing key: `authentik Self-signed Certificate`
   - Advanced: scopes `openid`, `email`, `profile` (default mappings); subject mode
     *Based on the User's hashed ID* (default)
2. **Applications → Applications → Create**: name `RD Panel`, slug `rdpanel`, provider
   `rdpanel`. To restrict access, add a group/policy binding to the application.
3. In RD Panel add a provider with kind `oidc`, issuer
   `https://<authentik host>/application/o/rdpanel/`, the client ID/secret, scopes
   `openid profile email`, PKCE `S256`.

Authentik's default e-mail mapping may report `email_verified: false`; then either link
accounts from the profile page or use a custom scope mapping that returns the verified
state you trust.

**Passkeys for the RustDesk client:** the client cannot use WebAuthn itself, but it signs
in through the browser, so enabling passkeys/WebAuthn in Authentik (an authenticator
validation stage with WebAuthn) gives client users passkey sign-in. The panel also
supports passkeys natively (Profile & security → Passkeys).

## Pocket ID, Keycloak, Zitadel, …

Same pattern: create a confidential OIDC client with the redirect URI above, then enter
the issuer, client ID and secret in RD Panel. Keycloak's issuer is
`https://<host>/realms/<realm>`; Pocket ID's is its base URL.

## GitHub and Google

- **GitHub:** Settings → Developer settings → OAuth Apps → New; callback URL as above.
  Kind `github`, no issuer.
- **Google:** Cloud Console → Credentials → OAuth client ID (Web application);
  authorized redirect URI as above. Kind `google`.

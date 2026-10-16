package crypto

import "strconv"

// Associated-data (AD) conventions for AEAD-encrypted columns.
//
// Every *_enc column is sealed with AD "<scope>:<row id>", which binds the
// ciphertext to one column of one row: copying it into another row or column
// makes Open fail. The scope strings below are part of the on-disk format and
// MUST NOT change; every writer (client API, admin API, legacy importer) and
// reader uses these helpers instead of formatting AD strings by hand.
//
//	column                                   AD
//	address_book_entries.password_enc        "ab_entry.password:<entry id>"
//	address_book_entries.hash_enc            "ab_entry.hash:<entry id>"
//	address_books.default_password_enc       "address_book.password:<book id>"
//	identity_providers.client_secret_enc     "identity_provider.client_secret:<provider id>"
//	share_links.password_enc                 "share_link.password:<link id>"
//	oauth_states.pkce_verifier_enc           "oauth_state.pkce_verifier:<state id>"
//	totp_secrets.secret_enc                  "totp:<user id>"
//	settings["mail.smtp"].password_enc       "settings.smtp_password:0"
//	settings["client.options"].ice_servers_enc "settings.ice_servers:0"
//
// Because the AD contains the row id, a row must be inserted first (to learn
// its id) and the ciphertext written afterwards, or the id must be known in
// advance (preserved legacy ids).
const (
	ScopeABEntryPassword              = "ab_entry.password"
	ScopeABEntryHash                  = "ab_entry.hash"
	ScopeAddressBookDefaultPassword   = "address_book.password"
	ScopeIdentityProviderClientSecret = "identity_provider.client_secret"
	ScopeShareLinkPassword            = "share_link.password"
	ScopeOAuthStatePKCEVerifier       = "oauth_state.pkce_verifier"
	ScopeTOTPSecret                   = "totp"
)

// AD returns the associated data "<scope>:<id>".
func AD(scope string, id int64) string {
	return scope + ":" + strconv.FormatInt(id, 10)
}

// ABEntryPasswordAD is the AD of address_book_entries.password_enc.
func ABEntryPasswordAD(entryID int64) string { return AD(ScopeABEntryPassword, entryID) }

// ABEntryHashAD is the AD of address_book_entries.hash_enc.
func ABEntryHashAD(entryID int64) string { return AD(ScopeABEntryHash, entryID) }

// AddressBookDefaultPasswordAD is the AD of address_books.default_password_enc.
func AddressBookDefaultPasswordAD(bookID int64) string {
	return AD(ScopeAddressBookDefaultPassword, bookID)
}

// IdentityProviderClientSecretAD is the AD of identity_providers.client_secret_enc.
func IdentityProviderClientSecretAD(providerID int64) string {
	return AD(ScopeIdentityProviderClientSecret, providerID)
}

// ShareLinkPasswordAD is the AD of share_links.password_enc.
func ShareLinkPasswordAD(linkID int64) string { return AD(ScopeShareLinkPassword, linkID) }

// OAuthStatePKCEVerifierAD is the AD of oauth_states.pkce_verifier_enc.
func OAuthStatePKCEVerifierAD(stateID int64) string { return AD(ScopeOAuthStatePKCEVerifier, stateID) }

// TOTPSecretAD is the AD of totp_secrets.secret_enc.
func TOTPSecretAD(userID int64) string { return AD(ScopeTOTPSecret, userID) }

// ScopeSMTPPassword seals the SMTP password stored in the panel settings
// (settings key "mail.smtp"; a single value, so the id is always 0).
const ScopeSMTPPassword = "settings.smtp_password"

// SMTPPasswordAD is the AD of the stored SMTP password.
func SMTPPasswordAD() string { return AD(ScopeSMTPPassword, 0) }

// ScopeICEServers seals the client ICE server list (TURN credentials) stored
// in the panel settings (settings key "client.options"; a single value, id 0).
const ScopeICEServers = "settings.ice_servers"

// ICEServersAD is the AD of the stored client ICE server list.
func ICEServersAD() string { return AD(ScopeICEServers, 0) }

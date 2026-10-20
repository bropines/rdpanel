package crypto

import "testing"

// The AD strings are part of the on-disk format; changing them makes every
// stored ciphertext unreadable.
func TestADFormat(t *testing.T) {
	cases := map[string]string{
		ABEntryPasswordAD(7):              "ab_entry.password:7",
		ABEntryHashAD(7):                  "ab_entry.hash:7",
		AddressBookDefaultPasswordAD(3):   "address_book.password:3",
		IdentityProviderClientSecretAD(2): "identity_provider.client_secret:2",
		ShareLinkPasswordAD(9):            "share_link.password:9",
		ServerShareLinkTokenAD(5):         "server_share_link.token:5",
		OAuthStatePKCEVerifierAD(11):      "oauth_state.pkce_verifier:11",
		TOTPSecretAD(1):                   "totp:1",
		FederationPeerTokenAD(4):          "settings.federation_peer_token:4",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

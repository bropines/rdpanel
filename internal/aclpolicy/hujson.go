package aclpolicy

import (
	"github.com/tailscale/hujson"
)

// Standardize converts HuJSON (JSON with //, /* */ comments and trailing commas)
// into strictly standard RFC 8259 JSON using Tailscale's official HuJSON parser.
func Standardize(input []byte) ([]byte, error) {
	return hujson.Standardize(input)
}

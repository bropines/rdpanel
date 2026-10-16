// Package api embeds the admin API contract (openapi.yaml) so the server can
// serve it to signed-in panel users at /api/v1/openapi.{yaml,json}.
package api

import _ "embed"

// OpenAPIYAML is api/openapi.yaml as committed.
//
//go:embed openapi.yaml
var OpenAPIYAML []byte

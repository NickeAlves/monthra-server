// Package docs embeds the OpenAPI document so the binary can serve it.
package docs

import _ "embed"

// OpenAPI is the OpenAPI 3.1 description of the API.
//
//go:embed openapi.yaml
var OpenAPI []byte

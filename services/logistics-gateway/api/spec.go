// Package api хранит контракт OpenAPI сервиса logistics-gateway; сервер генерируется в internal/api/gen.
package api

import _ "embed"

// Spec - текст openapi.yaml для GET /openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte

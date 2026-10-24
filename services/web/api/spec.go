// Package api хранит контракт OpenAPI сервиса web (страницы и вход); кода по нему не генерируется.
package api

import _ "embed"

// Spec — текст openapi.yaml для GET /openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte

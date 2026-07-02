package masterapi

import (
	"embed"
	_ "embed"
)

//go:embed openapi.yaml
var openapiYAML []byte

//go:embed openapi.json
var openapiJSON []byte

//go:embed swagger-ui
var swaggerUIFS embed.FS

//go:embed swagger-ui/index.html
var swaggerHTML []byte

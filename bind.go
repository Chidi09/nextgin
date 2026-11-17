package nextgin

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// BindJSON decodes the request body as JSON into v using encoding/json.
// Unlike gin's ShouldBindJSON it performs no struct-tag validation, so it does
// not link gin's binding validator (go-playground/validator, ~18 MB of wasm on
// the cloudflare target). Validate required fields in the handler.
func BindJSON(c *gin.Context, v any) error {
	if c.Request == nil || c.Request.Body == nil || c.Request.Body == http.NoBody {
		return errors.New("nextgin: empty request body")
	}
	return json.NewDecoder(c.Request.Body).Decode(v)
}

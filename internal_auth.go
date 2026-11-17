package nextgin

import (
	"crypto/subtle"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
)

// InternalSecretEnv names the shared secret between the web app and the Go backend.
const InternalSecretEnv = "NEXTGIN_INTERNAL_SECRET"

// InternalHeader carries it on server-to-server requests.
const InternalHeader = "X-Nextgin-Internal"

// RequireInternal returns middleware that aborts with 401 unless the request carries
// InternalHeader equal to $NEXTGIN_INTERNAL_SECRET (constant-time compare).
// It fails closed: the secret is read when RequireInternal is called, and if it is empty
// every request is rejected (401), never allowed.
func RequireInternal() gin.HandlerFunc {
	secret := []byte(os.Getenv(InternalSecretEnv))
	return func(c *gin.Context) {
		if len(secret) == 0 {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		got := c.GetHeader(InternalHeader)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), secret) != 1 {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	}
}

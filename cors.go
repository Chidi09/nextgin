package nextgin

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const corsPanicMsg = `nextgin: CORS needs an explicit origin allow-list ("*" is not allowed with credentials)`

// CORS returns middleware for browsers that call the backend directly (SPEC §9.3 direct mode).
func CORS(origins ...string) gin.HandlerFunc {
	if len(origins) == 0 {
		panic(corsPanicMsg)
	}
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		trimmed := strings.TrimSuffix(o, "/")
		if o == "" || trimmed == "" || trimmed == "*" {
			panic(corsPanicMsg)
		}
		allowed[trimmed] = struct{}{}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}

		c.Writer.Header().Add("Vary", "Origin")

		isPreflight := c.Request.Method == http.MethodOptions && c.GetHeader("Access-Control-Request-Method") != ""
		_, ok := allowed[origin]
		if !ok {
			if isPreflight {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.Next()
			return
		}

		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Credentials", "true")

		if isPreflight {
			allowHeaders := c.GetHeader("Access-Control-Request-Headers")
			if allowHeaders == "" {
				allowHeaders = "Content-Type, Authorization"
			}
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", allowHeaders)
			c.Header("Access-Control-Max-Age", "600")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

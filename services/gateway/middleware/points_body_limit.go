package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
)

// PointsRelayBodyLimit bounds the decompressed chat request before TokenAuth
// parses the model from the body. GzipDecodeMiddleware must run first.
func PointsRelayBodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if config.PointsBillingEnabled && c.Request.URL.Path == "/v1/chat/completions" {
			maxBody := config.PointsMaxInputBytes
			if maxBody < 1024 {
				maxBody = 1024
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, int64(maxBody))
		}
		c.Next()
	}
}

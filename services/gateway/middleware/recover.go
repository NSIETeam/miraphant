package middleware

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/logger"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
	"net/http"
	"runtime/debug"
	"strings"
)

func RelayPanicRecover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				ctx := c.Request.Context()
				if config.PointsBillingEnabled && c.Request.URL.Path == "/v1/chat/completions" {
					if value, ok := c.Get("miraphant.points_usage_finalizer"); ok {
						if finalize, valid := value.(func(*relaymodel.Usage, bool) error); valid {
							_ = finalize(nil, false)
						}
					}
					logger.Errorf(ctx, "points relay panic; reserved points remain held for reconciliation")
					if c.Writer.Written() {
						if strings.Contains(strings.ToLower(c.Writer.Header().Get("Content-Type")), "text/event-stream") {
							_, _ = c.Writer.WriteString("event: error\ndata: {\"error\":{\"message\":\"request failed; funds remain held for reconciliation\",\"type\":\"points_relay_error\"}}\n\n")
							c.Writer.Flush()
						}
						c.Abort()
						return
					}
					c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "request failed; funds remain held for reconciliation", "type": "points_relay_error"}})
					c.Abort()
					return
				}
				logger.Errorf(ctx, fmt.Sprintf("panic detected: %v", err))
				logger.Errorf(ctx, fmt.Sprintf("stacktrace from panic: %s", string(debug.Stack())))
				logger.Errorf(ctx, fmt.Sprintf("request: %s %s", c.Request.Method, c.Request.URL.Path))
				body, _ := common.GetRequestBody(c)
				logger.Errorf(ctx, fmt.Sprintf("request body: %s", string(body)))
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": gin.H{
						"message": fmt.Sprintf("Panic detected, error: %v. Please submit an issue with the related log here: https://github.com/songquanpeng/one-api", err),
						"type":    "one_api_panic",
					},
				})
				c.Abort()
			}
		}()
		c.Next()
	}
}

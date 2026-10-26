package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
)

// RefundSessionAuth deliberately requires a browser login session. Legacy
// access tokens and cached role/session data are not accepted here.
func RefundSessionAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		id, ok := session.Get("id").(int)
		binding, bindingOK := session.Get("refund_auth_session").(string)
		if !ok || id <= 0 || !bindingOK || !model.RefundSessionBindingValid(binding) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "请重新登录后再继续"})
			return
		}
		var current model.User
		if err := model.DB.Select("id", "role", "status").First(&current, "id = ?", id).Error; err != nil || current.Status != model.UserStatusEnabled {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "当前账户不可执行此操作"})
			return
		}
		c.Set(ctxkey.Id, current.Id)
		c.Set(ctxkey.Role, current.Role)
		c.Set("refund_auth_session", binding)
		c.Next()
	}
}

func RefundManagerAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetInt(ctxkey.Role) != model.RoleRootUser {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "当前账户不可执行此操作"})
			return
		}
		c.Next()
	}
}

func RefundCapabilityAuth(capability string) gin.HandlerFunc {
	return func(c *gin.Context) {
		allowed, err := model.HasRefundCapability(c.GetInt(ctxkey.Id), capability)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "暂时无法校验操作权限"})
			return
		}
		if !allowed {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "当前账户不可执行此操作"})
			return
		}
		c.Next()
	}
}

// RefundStepUpBodyLimit applies before JSON decoding of password-bearing input.
func RefundStepUpBodyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8*1024)
		c.Next()
	}
}

// RefundStepUpUserRateLimit adds an actor budget to the independent IP budget
// attached to the route. Both limits are intentionally conservative.
func RefundStepUpUserRateLimit() gin.HandlerFunc {
	if !common.RedisEnabled {
		inMemoryRateLimiter.Init(time.Minute)
	}
	return func(c *gin.Context) {
		id := c.GetInt(ctxkey.Id)
		if id <= 0 {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		key := strconv.Itoa(id)
		if common.RedisEnabled {
			redisRateLimiterKey(c, 5, 60, "REFUNDSTEPUSER", key)
		} else {
			memoryRateLimiterForKey(c, 5, 60, "REFUNDSTEPUSER"+key)
		}
	}
}

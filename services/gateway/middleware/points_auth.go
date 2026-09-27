package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
)

func PointsCSRFToken(userID int) string {
	mac := hmac.New(sha256.New, []byte(config.SessionSecret))
	_, _ = mac.Write([]byte("miraphant-points-csrf:" + strconv.Itoa(userID)))
	return hex.EncodeToString(mac.Sum(nil))
}

func PointsCSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			if ref := c.GetHeader("Referer"); ref != "" {
				if parsed, err := url.Parse(ref); err == nil {
					origin = parsed.Scheme + "://" + parsed.Host
				}
			}
		}
		parsed, err := url.Parse(origin)
		serverURL, serverErr := url.Parse(config.ServerAddress)
		expectedScheme, expectedHost := "http", c.Request.Host
		if c.Request.TLS != nil {
			expectedScheme = "https"
		}
		if serverErr == nil && serverURL.Scheme != "" && serverURL.Host != "" {
			expectedScheme, expectedHost = serverURL.Scheme, serverURL.Host
		}
		if err != nil || serverErr != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, expectedHost) || !strings.EqualFold(parsed.Scheme, expectedScheme) || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "same-origin request required"})
			return
		}
		id, ok := c.Get(ctxkey.Id)
		userID, valid := id.(int)
		provided := c.GetHeader("X-CSRF-Token")
		if !ok || !valid || provided == "" || !hmac.Equal([]byte(provided), []byte(PointsCSRFToken(userID))) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "valid points CSRF token required"})
			return
		}
		c.Next()
	}
}

func pointsSessionAuth(minRole int) gin.HandlerFunc {
	return func(c *gin.Context) {
		session := sessions.Default(c)
		id, ok := session.Get("id").(int)
		if !ok || id <= 0 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "session login required"})
			return
		}
		var user model.User
		if err := model.DB.Select("id", "role", "status").First(&user, "id = ?", id).Error; err != nil || user.Status != model.UserStatusEnabled || user.Role < minRole {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "current user is not authorized"})
			return
		}
		c.Set(ctxkey.Id, user.Id)
		c.Set(ctxkey.Role, user.Role)
		c.Next()
	}
}

func PointsUserAuth() gin.HandlerFunc  { return pointsSessionAuth(model.RoleCommonUser) }
func PointsAdminAuth() gin.HandlerFunc { return pointsSessionAuth(model.RoleAdminUser) }

func PointsBillingAvailable() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !config.PointsBillingEnabled {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "points billing is not enabled"})
			return
		}
		c.Next()
	}
}

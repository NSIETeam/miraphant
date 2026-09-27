package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
	"gorm.io/gorm"
)

func RefundAuthorizationCSRF(c *gin.Context) { PointsCSRF(c) }

func RefundAuthorizationSelf(c *gin.Context) {
	userID := c.GetInt(ctxkey.Id)
	capabilities, err := model.RefundCapabilitiesForUser(userID)
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法读取权限")
		return
	}
	var credential struct{ Password string }
	if err := model.DB.Model(&model.User{}).Select("password").First(&credential, "id = ?", userID).Error; err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法读取账户状态")
		return
	}
	c.JSON(http.StatusOK, gin.H{"capabilities": capabilities, "local_password_available": credential.Password != "", "refund_operations_enabled": config.PointRefundOperationsEnabled})
}

type refundStepUpRequest struct {
	Action       string   `json:"action"`
	TargetUserID int      `json:"target_user_id"`
	Capabilities []string `json:"capabilities"`
	Reason       string   `json:"reason"`
	Password     string   `json:"password"`
}

func decodeRefundAuthJSON(c *gin.Context, target interface{}) error {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("invalid request body")
	}
	return nil
}

func IssueRefundAuthorizationStepUp(c *gin.Context) {
	var req refundStepUpRequest
	if err := decodeRefundAuthJSON(c, &req); err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "请求参数无效")
		return
	}
	ticket, err := model.IssueRefundCapabilityStepUpTicket(c.GetInt(ctxkey.Id), c.GetString("refund_auth_session"), req.Password, req.Action, req.TargetUserID, req.Capabilities, req.Reason)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrRefundCapabilityDenied):
			paymentHTTPError(c, http.StatusForbidden, "当前账户不可执行此操作")
		case errors.Is(err, model.ErrRefundAuthUnavailable):
			paymentHTTPError(c, http.StatusServiceUnavailable, "退款操作尚未开放")
		case errors.Is(err, model.ErrRefundStepUpInvalid):
			paymentHTTPError(c, http.StatusUnauthorized, "验证未通过，请检查本地密码或重新登录")
		case errors.Is(err, model.ErrRefundAuthConflict), errors.Is(err, gorm.ErrRecordNotFound):
			paymentHTTPError(c, http.StatusBadRequest, "请求无法处理，请检查对象和操作范围")
		default:
			paymentHTTPError(c, http.StatusInternalServerError, "暂时无法完成验证")
		}
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"ticket": ticket, "expires_in_seconds": 300})
}

type refundCapabilityChangeRequest struct {
	Action       string   `json:"action"`
	TargetUserID int      `json:"target_user_id"`
	Capabilities []string `json:"capabilities"`
	Reason       string   `json:"reason"`
	Ticket       string   `json:"step_up_ticket"`
}

func ChangeRefundCapabilities(c *gin.Context) {
	var req refundCapabilityChangeRequest
	if err := decodeRefundAuthJSON(c, &req); err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "请求参数无效")
		return
	}
	scope := model.RefundStepUpScope{Action: req.Action, TargetUserID: req.TargetUserID, Capabilities: req.Capabilities, Reason: req.Reason}
	err := model.ApplyRefundCapabilityChange(c.GetInt(ctxkey.Id), c.GetString("refund_auth_session"), req.Ticket, scope)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrRefundCapabilityDenied):
			paymentHTTPError(c, http.StatusForbidden, "当前账户不可执行此操作")
		case errors.Is(err, model.ErrRefundStepUpInvalid):
			paymentHTTPError(c, http.StatusUnauthorized, "验证凭据无效或已过期，请重新验证")
		case errors.Is(err, model.ErrRefundAuthConflict), errors.Is(err, gorm.ErrRecordNotFound):
			paymentHTTPError(c, http.StatusBadRequest, "请求无法处理，请检查对象和操作范围")
		default:
			paymentHTTPError(c, http.StatusInternalServerError, "暂时无法更新权限")
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func AdminRefundCapabilityGrants(c *gin.Context) {
	targetID, err := strconv.Atoi(c.Param("id"))
	if err != nil || targetID <= 0 {
		paymentHTTPError(c, http.StatusBadRequest, "用户编号无效")
		return
	}
	grants, err := model.ListRefundCapabilityGrants(targetID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		paymentHTTPError(c, http.StatusNotFound, "用户不存在")
		return
	}
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法读取权限")
		return
	}
	rows := make([]gin.H, 0, len(grants))
	for _, grant := range grants {
		rows = append(rows, gin.H{"capability": grant.Capability, "granted_by": grant.GrantedBy, "granted_at": grant.GrantedAt, "revoked_at": grant.RevokedAt})
	}
	c.JSON(http.StatusOK, gin.H{"user_id": targetID, "grants": rows})
}

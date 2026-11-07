package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/common/logger"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/payment"
	"github.com/songquanpeng/one-api/service/payments"
	"gorm.io/gorm"
)

type pointRefundView struct {
	RefundKey        string    `json:"refund_key"`
	OrderKey         string    `json:"order_key"`
	Channel          string    `json:"channel"`
	AmountFen        int64     `json:"amount_fen"`
	Currency         string    `json:"currency"`
	PurchaseMicro    int64     `json:"purchase_micro"`
	BonusRevokeMicro int64     `json:"bonus_revoke_micro"`
	State            string    `json:"state"`
	Reason           string    `json:"reason"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func refundView(refund model.PointRefund) pointRefundView {
	return pointRefundView{RefundKey: refund.RefundKey, OrderKey: refund.OrderKey, Channel: refund.Channel, AmountFen: refund.AmountFen, Currency: refund.Currency,
		PurchaseMicro: refund.PurchaseMicro, BonusRevokeMicro: refund.BonusRevokeMicro, State: refund.State, Reason: refund.Reason, CreatedAt: refund.CreatedAt, UpdatedAt: refund.UpdatedAt}
}

func refundSchemaReady(c *gin.Context) bool {
	if err := model.RequirePointsSchema(); err != nil {
		paymentHTTPError(c, http.StatusServiceUnavailable, "退款账本暂不可用")
		return false
	}
	return true
}

func parseRefundCursor(c *gin.Context, defaultLimit int) (uint, int, bool) {
	var cursor uint64
	var err error
	if value := c.Query("before_id"); value != "" {
		cursor, err = strconv.ParseUint(value, 10, 64)
		if err != nil || cursor == 0 {
			paymentHTTPError(c, http.StatusBadRequest, "分页位置无效")
			return 0, 0, false
		}
	}
	limit := defaultLimit
	if value := c.Query("limit"); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			paymentHTTPError(c, http.StatusBadRequest, "每页数量须为 1 至 100")
			return 0, 0, false
		}
	}
	return uint(cursor), limit, true
}

func customerRefundList(userID int, orderKey string, before uint, limit int) ([]model.PointRefund, bool, error) {
	return model.ListPointRefunds(model.PointRefundListFilter{UserID: userID, OrderKey: orderKey, BeforeID: before, Limit: limit})
}

func CustomerPointRefundQuote(c *gin.Context) {
	if !refundSchemaReady(c) {
		return
	}
	quote, err := model.PointRefundableQuote(c.GetInt(ctxkey.Id), c.Param("key"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		paymentHTTPError(c, http.StatusNotFound, "订单不存在")
		return
	}
	if err != nil {
		if errors.Is(err, model.ErrPointRefundUnavailable) {
			paymentHTTPError(c, http.StatusServiceUnavailable, "退款账本暂不可用")
			return
		}
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法读取可退金额")
		return
	}
	allowed := config.PointsBillingEnabled && config.PointRefundOperationsEnabled && quote.MaxRefundFen > 0
	reason := ""
	if !config.PointsBillingEnabled {
		reason = "积分账本暂未开放"
	} else if !config.PointRefundOperationsEnabled {
		reason = "退款申请暂未开放"
	} else if quote.HasInFlight {
		reason = "该订单已有退款正在处理中"
	} else if quote.MaxRefundFen <= 0 {
		reason = "当前没有可申请的退款金额"
	}
	c.JSON(http.StatusOK, gin.H{"order_key": quote.OrderKey, "channel": quote.Channel, "order_amount_fen": quote.OrderAmountFen, "refunded_fen": quote.RefundedFen,
		"max_refund_fen": quote.MaxRefundFen, "has_in_flight": quote.HasInFlight, "can_request": allowed, "reason": reason, "is_estimate": true})
}

func CreateCustomerPointRefund(c *gin.Context) {
	if !refundSchemaReady(c) {
		return
	}
	var req struct {
		AmountFen      int64  `json:"amount_fen"`
		Reason         string `json:"reason"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := decodeRefundAuthJSON(c, &req); err != nil || req.AmountFen <= 0 || strings.TrimSpace(req.Reason) == "" || len([]byte(req.Reason)) > 256 || len(req.IdempotencyKey) < 8 || len(req.IdempotencyKey) > 180 {
		paymentHTTPError(c, http.StatusBadRequest, "退款金额、原因或请求编号无效")
		return
	}
	orderKey := c.Param("key")
	prior, priorErr := model.GetPointRefundByIdempotency(c.GetInt(ctxkey.Id), orderKey, req.IdempotencyKey)
	if priorErr == nil {
		if prior.AmountFen != req.AmountFen || prior.Reason != strings.TrimSpace(req.Reason) {
			paymentHTTPError(c, http.StatusConflict, "同一请求编号不能更改退款金额或原因")
			return
		}
		c.JSON(http.StatusOK, refundView(*prior))
		return
	}
	if !errors.Is(priorErr, gorm.ErrRecordNotFound) {
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法核实请求状态")
		return
	}
	if !config.PointsBillingEnabled || !config.PointRefundOperationsEnabled {
		paymentHTTPError(c, http.StatusServiceUnavailable, "退款申请暂未开放")
		return
	}
	quote, err := model.PointRefundableQuote(c.GetInt(ctxkey.Id), orderKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		paymentHTTPError(c, http.StatusNotFound, "订单不存在")
		return
	}
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法核实退款额度")
		return
	}
	maxReasonBytes := 256
	if quote.Channel == "wechat" {
		maxReasonBytes = 80
	}
	if len([]byte(strings.TrimSpace(req.Reason))) > maxReasonBytes {
		paymentHTTPError(c, http.StatusBadRequest, "退款原因长度超过该支付渠道限制")
		return
	}
	// This quote is informational only. The model transaction is the source of
	// truth for amount limits and same-key idempotency. In particular, two
	// concurrent retries can both observe no prior row here; an early check of
	// HasInFlight would reject the loser before the transaction can return the
	// already-created refund for the same idempotency key.
	requestID := uuid.NewString()
	providerKey := "R" + strings.ReplaceAll(requestID, "-", "")
	refund, err := model.RequestPointRefund(model.PointRefundRequest{UserID: c.GetInt(ctxkey.Id), OrderKey: orderKey, IdempotencyKey: req.IdempotencyKey,
		RefundKey: "refund-" + requestID, ProviderRefundKey: providerKey, AmountFen: req.AmountFen, Reason: strings.TrimSpace(req.Reason)})
	if err != nil {
		writeRefundModelError(c, err)
		return
	}
	c.JSON(http.StatusCreated, refundView(*refund))
}

func CustomerPointRefundsForOrder(c *gin.Context) {
	if !refundSchemaReady(c) {
		return
	}
	if _, err := model.GetPointPurchaseOrderForUser(c.GetInt(ctxkey.Id), c.Param("key")); errors.Is(err, gorm.ErrRecordNotFound) {
		paymentHTTPError(c, http.StatusNotFound, "订单不存在")
		return
	} else if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法读取退款进度")
		return
	}
	before, limit, ok := parseRefundCursor(c, 25)
	if !ok {
		return
	}
	rows, hasMore, err := customerRefundList(c.GetInt(ctxkey.Id), c.Param("key"), before, limit)
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法读取退款进度")
		return
	}
	out := make([]pointRefundView, 0, len(rows))
	for _, row := range rows {
		out = append(out, refundView(row))
	}
	var next uint
	if hasMore && len(rows) > 0 {
		next = rows[len(rows)-1].ID
	}
	c.JSON(http.StatusOK, gin.H{"refunds": out, "has_more": hasMore, "next_before_id": next})
}

func CustomerPointRefund(c *gin.Context) {
	if !refundSchemaReady(c) {
		return
	}
	refund, err := model.GetPointRefundForUser(c.GetInt(ctxkey.Id), c.Param("key"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		paymentHTTPError(c, http.StatusNotFound, "退款申请不存在")
		return
	}
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法读取退款进度")
		return
	}
	c.JSON(http.StatusOK, refundView(*refund))
}

func AdminPointRefunds(c *gin.Context) {
	if !refundSchemaReady(c) {
		return
	}
	before, limit, ok := parseRefundCursor(c, 25)
	if !ok {
		return
	}
	filter := model.PointRefundListFilter{BeforeID: before, Limit: limit, State: c.Query("state"), Channel: c.Query("channel")}
	if value := c.Query("user_id"); value != "" {
		id, err := strconv.Atoi(value)
		if err != nil || id <= 0 {
			paymentHTTPError(c, http.StatusBadRequest, "客户编号无效")
			return
		}
		filter.UserID = id
	}
	rows, hasMore, err := model.ListPointRefunds(filter)
	if err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "退款筛选条件无效或暂不可用")
		return
	}
	out := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		out = append(out, gin.H{"refund": refundView(row), "user_id": row.UserID})
	}
	var next uint
	if hasMore && len(rows) > 0 {
		next = rows[len(rows)-1].ID
	}
	c.JSON(http.StatusOK, gin.H{"refunds": out, "has_more": hasMore, "next_before_id": next})
}

func AdminPointRefund(c *gin.Context) {
	if !refundSchemaReady(c) {
		return
	}
	refund, err := model.GetPointRefundByKey(c.Param("key"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		paymentHTTPError(c, http.StatusNotFound, "退款申请不存在")
		return
	}
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法读取退款详情")
		return
	}
	var decisions []model.PointRefundDecision
	if err := model.DB.Where("refund_id = ?", refund.ID).Order("id DESC").Limit(50).Find(&decisions).Error; err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法读取处理记录")
		return
	}
	var evidence []model.PointRefundEvidence
	if err := model.DB.Where("refund_key = ?", refund.RefundKey).Order("id DESC").Limit(50).Find(&evidence).Error; err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法读取渠道进度")
		return
	}
	decisionViews := make([]gin.H, 0, len(decisions))
	for _, row := range decisions {
		decisionViews = append(decisionViews, gin.H{"business_key": row.DecisionKey, "actor_user_id": row.ActorUserID, "action": row.Action, "reason": row.Reason, "created_at": row.CreatedAt})
	}
	evidenceViews := make([]gin.H, 0, len(evidence))
	for _, row := range evidence {
		evidenceViews = append(evidenceViews, gin.H{"outcome": row.Outcome, "amount_fen": row.AmountFen, "created_at": row.CreatedAt})
	}
	c.JSON(http.StatusOK, gin.H{"refund": refundView(*refund), "user_id": refund.UserID, "decisions": decisionViews, "evidence": evidenceViews})
}

type adminRefundDecisionRequest struct {
	BusinessKey string `json:"business_key"`
	Reason      string `json:"reason"`
	Ticket      string `json:"step_up_ticket"`
}

func DecideAdminPointRefund(c *gin.Context) {
	if !refundSchemaReady(c) {
		return
	}
	var req adminRefundDecisionRequest
	if err := decodeRefundAuthJSON(c, &req); err != nil || len(req.BusinessKey) < 8 || len(req.BusinessKey) > 180 || strings.TrimSpace(req.Reason) == "" || len([]byte(req.Reason)) > 512 {
		paymentHTTPError(c, http.StatusBadRequest, "确认编号、处理原因或再确认票据无效")
		return
	}
	action := "approve"
	if strings.HasSuffix(c.Request.URL.Path, "/reject") {
		action = "reject"
	}
	decisionAction := "refund." + action
	reason := strings.TrimSpace(req.Reason)
	matched, matchErr := model.MatchPointRefundDecision(c.Param("key"), req.BusinessKey, c.GetInt(ctxkey.Id), action, reason)
	if matchErr != nil {
		writeRefundModelError(c, matchErr)
		return
	}
	if matched {
		refund, err := model.GetPointRefundByKey(c.Param("key"))
		if err != nil {
			paymentHTTPError(c, http.StatusInternalServerError, "处理结果暂不可读取")
			return
		}
		c.JSON(http.StatusOK, refundView(*refund))
		return
	}
	if req.Ticket == "" {
		paymentHTTPError(c, http.StatusBadRequest, "再确认票据无效")
		return
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := model.ConsumeRefundDecisionStepUpTx(tx, req.Ticket, c.GetInt(ctxkey.Id), c.GetString("refund_auth_session"), c.Param("key"), decisionAction, req.BusinessKey, reason, time.Now().UTC()); err != nil {
			return err
		}
		return model.DecidePointRefundTx(tx, c.Param("key"), req.BusinessKey, c.GetInt(ctxkey.Id), action, reason, false)
	})
	if err != nil {
		// A concurrent identical request may have committed after our initial
		// lookup. Resolve that race as an idempotent replay, without consuming a
		// second ticket or writing another decision.
		if matched, replayErr := model.MatchPointRefundDecision(c.Param("key"), req.BusinessKey, c.GetInt(ctxkey.Id), action, reason); replayErr == nil && matched {
			refund, getErr := model.GetPointRefundByKey(c.Param("key"))
			if getErr == nil {
				c.JSON(http.StatusOK, refundView(*refund))
				return
			}
		}
		writeRefundModelError(c, err)
		return
	}
	refund, err := model.GetPointRefundByKey(c.Param("key"))
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "处理结果暂不可读取")
		return
	}
	c.JSON(http.StatusOK, refundView(*refund))
}

func SubmitAdminPointRefund(c *gin.Context) {
	if !refundSchemaReady(c) {
		return
	}
	var req adminRefundDecisionRequest
	if err := decodeRefundAuthJSON(c, &req); err != nil || len(req.BusinessKey) < 8 || len(req.BusinessKey) > 180 || strings.TrimSpace(req.Reason) == "" || len([]byte(req.Reason)) > 512 {
		paymentHTTPError(c, http.StatusBadRequest, "确认编号、处理原因或再确认票据无效")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	matched, matchErr := model.MatchPointRefundDecision(c.Param("key"), req.BusinessKey, c.GetInt(ctxkey.Id), "submit", reason)
	if matchErr != nil {
		writeRefundModelError(c, matchErr)
		return
	}
	refund, err := model.GetPointRefundByKey(c.Param("key"))
	if err != nil {
		writeRefundModelError(c, err)
		return
	}
	if matched {
		dispatchAdminRefundIntent(c, refund.RefundKey)
		return
	}
	if req.Ticket == "" {
		paymentHTTPError(c, http.StatusBadRequest, "再确认票据无效")
		return
	}
	registered, configured := payments.ProviderFor(refund.Channel)
	if !configured || registered.Identity.Provider != refund.Channel || registered.Identity.MerchantID != refund.ProviderMerchantID || registered.Identity.AppID != refund.ProviderAppID {
		paymentHTTPError(c, http.StatusServiceUnavailable, "原支付渠道暂不可用，申请仍保持冻结")
		return
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := model.ConsumeRefundDecisionStepUpTx(tx, req.Ticket, c.GetInt(ctxkey.Id), c.GetString("refund_auth_session"), refund.RefundKey, "refund.submit", req.BusinessKey, reason, time.Now().UTC()); err != nil {
			return err
		}
		return model.DecidePointRefundTx(tx, refund.RefundKey, req.BusinessKey, c.GetInt(ctxkey.Id), "submit", reason, true)
	})
	if err != nil {
		if matched, replayErr := model.MatchPointRefundDecision(refund.RefundKey, req.BusinessKey, c.GetInt(ctxkey.Id), "submit", reason); replayErr == nil && matched {
			dispatchAdminRefundIntent(c, refund.RefundKey)
			return
		}
		writeRefundModelError(c, err)
		return
	}
	dispatchAdminRefundIntent(c, refund.RefundKey)
}

func dispatchAdminRefundIntent(c *gin.Context, refundKey string) {
	refund, err := model.GetPointRefundByKey(refundKey)
	if err != nil {
		paymentHTTPError(c, http.StatusAccepted, "提交意图已保存，状态正在核实")
		return
	}
	if refund.State == "succeeded" || refund.State == "definite_failed" || refund.State == "rejected" {
		c.JSON(http.StatusOK, refundView(*refund))
		return
	}
	ctx, cancel := requestRefundContext(c)
	defer cancel()
	dispatch, dispatchErr := payments.DispatchPointRefundOperation(ctx, refundKey)
	latest, getErr := model.GetPointRefundByKey(refundKey)
	if getErr != nil {
		paymentHTTPError(c, http.StatusAccepted, "提交意图已保存，状态正在核实")
		return
	}
	if latest.State == "succeeded" || latest.State == "definite_failed" || latest.State == "rejected" {
		c.JSON(http.StatusOK, refundView(*latest))
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"refund": refundView(*latest), "dispatch_pending": dispatchErr != nil || dispatch.State != "processed"})
}

func requestRefundContext(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), 25*time.Second)
}

func ReconcileAdminPointRefund(c *gin.Context) {
	if !refundSchemaReady(c) {
		return
	}
	refund, err := model.GetPointRefundByKey(c.Param("key"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		paymentHTTPError(c, http.StatusNotFound, "退款申请不存在")
		return
	}
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "暂时无法读取退款状态")
		return
	}
	if refund.State != "submitted" && refund.State != "unknown" && refund.State != "needs_manual_review" && refund.State != "submitting" && !(refund.State == "approved" && refund.RecoveryAction == "apply") {
		paymentHTTPError(c, http.StatusConflict, "当前状态不能发起渠道核实")
		return
	}
	ctx, cancel := requestRefundContext(c)
	defer cancel()
	_, err = payments.DispatchPointRefundOperation(ctx, refund.RefundKey)
	if err != nil {
		// Query failures never release the frozen allocation.
		latest, getErr := model.GetPointRefundByKey(refund.RefundKey)
		if getErr == nil {
			c.JSON(http.StatusAccepted, refundView(*latest))
			return
		}
		paymentHTTPError(c, http.StatusServiceUnavailable, "核实请求已保留，当前无法确认渠道状态")
		return
	}
	latest, err := model.GetPointRefundByKey(refund.RefundKey)
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "核实已提交，暂时无法读取状态")
		return
	}
	c.JSON(http.StatusOK, refundView(*latest))
}

func WeChatRefundNotify(c *gin.Context) {
	if !refundSchemaReady(c) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, (1<<20)+1))
	if err != nil || len(body) == 0 || len(body) > 1<<20 {
		paymentHTTPError(c, http.StatusBadRequest, "通知内容无效")
		return
	}
	inbox, _, err := payments.VerifyAndPersistWeChatRefundNotification(c.Request.Header, body)
	if err != nil {
		if errors.Is(err, payment.ErrInvalidNotice) || errors.Is(err, payment.ErrOrderMismatch) || errors.Is(err, gorm.ErrRecordNotFound) {
			paymentHTTPError(c, http.StatusBadRequest, "退款通知验证失败")
			return
		}
		paymentHTTPError(c, http.StatusServiceUnavailable, "退款通知未能可靠记录")
		return
	}
	if _, processErr := payments.ProcessPointRefundInbox(inbox.ID); processErr != nil {
		logger.SysError("verified refund notification stored for recovery; inbox processing failed")
	}
	c.JSON(http.StatusOK, gin.H{"code": "SUCCESS", "message": "成功"})
}

func writeRefundModelError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		paymentHTTPError(c, http.StatusNotFound, "退款申请不存在")
	case errors.Is(err, model.ErrRefundStepUpInvalid):
		paymentHTTPError(c, http.StatusUnauthorized, "再确认票据无效、已过期或已使用")
	case errors.Is(err, model.ErrRefundCapabilityDenied):
		paymentHTTPError(c, http.StatusForbidden, "当前账户不可执行此操作")
	case errors.Is(err, model.ErrRefundAuthUnavailable):
		paymentHTTPError(c, http.StatusServiceUnavailable, "退款操作尚未开放")
	case errors.Is(err, model.ErrPointsConflict), errors.Is(err, model.ErrPointRefundInFlight), errors.Is(err, model.ErrPointRefundState), errors.Is(err, model.ErrPointsInsufficient):
		paymentHTTPError(c, http.StatusConflict, "退款申请或处理状态已变化，请刷新后核对")
	default:
		paymentHTTPError(c, http.StatusInternalServerError, "退款操作暂未完成")
	}
}

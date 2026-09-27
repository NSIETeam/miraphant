package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/payment"
	"github.com/songquanpeng/one-api/service/payments"
	"gorm.io/gorm"
)

type pointOrderView struct {
	OrderKey       string    `json:"order_key"`
	Channel        string    `json:"channel"`
	PackageID      string    `json:"package_id"`
	PackageVersion string    `json:"package_version"`
	PackageName    string    `json:"package_name"`
	Currency       string    `json:"currency"`
	AmountFen      int64     `json:"amount_fen"`
	PurchaseMicro  int64     `json:"purchase_micro"`
	BonusMicro     int64     `json:"bonus_micro"`
	State          string    `json:"state"`
	ExpiresAt      *int64    `json:"expires_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

func toPointOrderView(order model.PointPurchaseOrder) pointOrderView {
	var snapshot model.PointPackageSnapshot
	_ = json.Unmarshal([]byte(order.PackageSnapshot), &snapshot)
	return pointOrderView{OrderKey: order.OrderKey, Channel: order.Channel, PackageID: order.PackageID, PackageVersion: order.PackageVersion, PackageName: snapshot.Name, Currency: order.Currency, AmountFen: order.AmountFen, PurchaseMicro: order.PurchaseMicro, BonusMicro: order.BonusMicro, State: order.State, ExpiresAt: order.ExpiresAt, CreatedAt: order.CreatedAt}
}

func PaymentPackages(c *gin.Context) {
	packagesList, err := model.ListActivePointPackages()
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "could not load packages")
		return
	}
	out := make([]gin.H, 0, len(packagesList))
	for _, p := range packagesList {
		out = append(out, gin.H{"package_id": p.PackageID, "version": p.Version, "name": p.Name, "amount_fen": p.AmountFen, "currency": p.Currency, "purchase_micro": p.PurchaseMicro, "bonus_micro": p.BonusMicro, "bonus_validity_secs": p.BonusValiditySecs})
	}
	c.JSON(http.StatusOK, gin.H{"packages": out, "currency": "CNY", "points_per_yuan": 100})
}

func CreatePointPackage(c *gin.Context) {
	var req struct {
		PackageID         string `json:"package_id"`
		Version           string `json:"version"`
		Name              string `json:"name"`
		AmountFen         int64  `json:"amount_fen"`
		BonusMicro        int64  `json:"bonus_micro"`
		BonusValiditySecs int64  `json:"bonus_validity_secs"`
		BusinessKey       string `json:"business_key"`
		Activate          bool   `json:"activate"`
	}
	maxFen := int64(^uint64(0)>>1) / model.PointMicroPerPoint
	if c.ShouldBindJSON(&req) != nil || req.AmountFen > maxFen {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid package"})
		return
	}
	actorID := c.GetInt(ctxkey.Id)
	p := &model.PointPackage{PackageID: strings.TrimSpace(req.PackageID), Version: strings.TrimSpace(req.Version), Name: strings.TrimSpace(req.Name), AmountFen: req.AmountFen, PurchaseMicro: req.AmountFen * model.PointMicroPerPoint, BonusMicro: req.BonusMicro, BonusValiditySecs: req.BonusValiditySecs, Currency: "CNY", CreatedBy: actorID}
	if err := model.CreatePointPackageVersion(p, actorID, strings.TrimSpace(req.BusinessKey), req.Activate); err != nil {
		if errors.Is(err, model.ErrPointsConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "package publication conflicts with an earlier request"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid package publication"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"package_id": p.PackageID, "version": p.Version, "published": true, "active": req.Activate})
}

func PointPurchaseOrders(c *gin.Context) {
	rows, err := model.ListPointPurchaseOrdersForUser(c.GetInt(ctxkey.Id), 50)
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "could not load orders")
		return
	}
	out := make([]pointOrderView, 0, len(rows))
	for _, row := range rows {
		out = append(out, toPointOrderView(row))
	}
	c.JSON(http.StatusOK, gin.H{"orders": out})
}

func PointPurchaseOrder(c *gin.Context) {
	order, err := model.GetPointPurchaseOrderForUser(c.GetInt(ctxkey.Id), c.Param("key"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "could not load order")
		return
	}
	c.JSON(http.StatusOK, toPointOrderView(order))
}

func CreatePointPurchaseOrder(c *gin.Context) {
	var req struct {
		PackageID string `json:"package_id"`
		Channel   string `json:"channel"`
	}
	if c.ShouldBindJSON(&req) != nil || strings.TrimSpace(req.PackageID) == "" || (req.Channel != "wechat" && req.Channel != "alipay") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "package_id and supported channel are required"})
		return
	}
	if err := model.RequirePointsSchema(); err != nil {
		paymentHTTPError(c, http.StatusServiceUnavailable, "payment service is not ready")
		return
	}
	runtime, ok := payments.ProviderFor(req.Channel)
	if !ok || runtime.Provider == nil || runtime.Identity.MerchantID == "" || runtime.Identity.AppID == "" {
		paymentHTTPError(c, http.StatusServiceUnavailable, "payment channel is unavailable")
		return
	}
	activePackages, err := model.ListActivePointPackages()
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "could not validate packages")
		return
	}
	found := false
	for _, p := range activePackages {
		if p.PackageID == req.PackageID {
			found = true
			break
		}
	}
	if !found {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "package is unavailable"})
		return
	}
	hasModel, err := payments.HasBillableModel(c.GetInt(ctxkey.Id))
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "could not validate model availability")
		return
	}
	if !hasModel {
		paymentHTTPError(c, http.StatusServiceUnavailable, "no priced model is currently available")
		return
	}
	idempotencyKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idempotencyKey == "" || len(idempotencyKey) > 180 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Idempotency-Key header is required"})
		return
	}
	userID := c.GetInt(ctxkey.Id)
	previous, lookupErr := model.GetPointPurchaseOrderByIdempotency(userID, idempotencyKey)
	if lookupErr == nil {
		if previous.PackageID != req.PackageID || previous.Channel != req.Channel {
			c.JSON(http.StatusConflict, gin.H{"error": "idempotency key conflicts with an earlier order"})
			return
		}
		runtime, ok := payments.ProviderFor(previous.Channel)
		if !ok || runtime.Provider == nil || runtime.Identity.MerchantID != previous.ProviderMerchantID || runtime.Identity.AppID != previous.ProviderAppID {
			paymentHTTPError(c, http.StatusServiceUnavailable, "the frozen payment identity is unavailable")
			return
		}
		if previous.State == "pending" && previous.CheckoutSnapshot != "" && previous.ExpiresAt != nil && *previous.ExpiresAt > time.Now().UTC().Unix() {
			var checkout payment.Checkout
			if json.Unmarshal([]byte(previous.CheckoutSnapshot), &checkout) != nil {
				paymentHTTPError(c, http.StatusServiceUnavailable, "saved checkout data is unavailable")
				return
			}
			c.JSON(http.StatusOK, gin.H{"order": toPointOrderView(previous), "checkout": checkout})
			return
		}
		c.JSON(http.StatusAccepted, gin.H{"order": toPointOrderView(previous), "checkout_pending": true, "message": "use order query or close to confirm this existing order"})
		return
	}
	if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
		paymentHTTPError(c, http.StatusInternalServerError, "could not load order")
		return
	}
	if !config.PointsBillingEnabled || !config.PaymentNewOrdersEnabled {
		paymentHTTPError(c, http.StatusServiceUnavailable, "new purchases are unavailable")
		return
	}
	order, err := model.CreatePointPurchaseOrder(model.CreatePointPurchaseOrderRequest{UserID: userID, PackageID: req.PackageID, Channel: req.Channel, MerchantID: runtime.Identity.MerchantID, AppID: runtime.Identity.AppID, IdempotencyKey: idempotencyKey})
	if err != nil {
		if errors.Is(err, model.ErrPointsConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "idempotency key conflicts with an earlier order"})
			return
		}
		if errors.Is(err, model.ErrPaymentPackageUnavailable) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "package is unavailable"})
			return
		}
		paymentHTTPError(c, http.StatusInternalServerError, "could not create order")
		return
	}
	if order.ProviderMerchantID != runtime.Identity.MerchantID || order.ProviderAppID != runtime.Identity.AppID {
		c.JSON(http.StatusConflict, gin.H{"error": "the payment identity for this order is no longer available"})
		return
	}
	wasCreated := order.State == "created"
	if order.State == "created" {
		if err := model.MarkPointPurchaseOrderPending(order.OrderKey); err != nil {
			paymentHTTPError(c, http.StatusInternalServerError, "order was created but its checkout is pending")
			return
		}
		order.State = "pending"
	}
	if order.State != "pending" && order.State != "created" {
		c.JSON(http.StatusConflict, gin.H{"error": "order is no longer open for checkout", "order": toPointOrderView(order)})
		return
	}
	if order.ExpiresAt != nil && *order.ExpiresAt <= time.Now().UTC().Unix() {
		c.JSON(http.StatusConflict, gin.H{"error": "order has expired; query or close it before creating another order"})
		return
	}
	if order.ProviderCreateState == "ready" && order.CheckoutSnapshot != "" {
		var checkout payment.Checkout
		if json.Unmarshal([]byte(order.CheckoutSnapshot), &checkout) != nil {
			paymentHTTPError(c, http.StatusServiceUnavailable, "saved checkout data is unavailable")
			return
		}
		c.JSON(http.StatusOK, gin.H{"order": toPointOrderView(order), "checkout": checkout})
		return
	}
	staleRetry := false
	if !wasCreated && order.ProviderCreateState == "started" {
		if order.ProviderCreateStartedAt == nil || time.Now().UTC().Unix()-*order.ProviderCreateStartedAt < 30 {
			c.JSON(http.StatusAccepted, gin.H{"order": toPointOrderView(order), "checkout_pending": true})
			return
		}
		trade, queryErr := runtime.Provider.Query(c.Request.Context(), order.OrderKey)
		if queryErr == nil {
			if !validateQueriedTrade(order, runtime.Identity, trade) {
				c.JSON(http.StatusConflict, gin.H{"error": "provider query did not match this order"})
				return
			}
			if isSuccessfulTrade(trade) {
				finalizeQueryEvidence(c, order, runtime, trade)
				return
			}
			if trade.Status == "CLOSED" || trade.Status == "TRADE_CLOSED" {
				if err := model.MarkPointPurchaseOrderClosed(order.OrderKey, "provider_confirmed_closed"); err != nil {
					paymentHTTPError(c, http.StatusServiceUnavailable, "provider status could not be saved")
					return
				}
				latest, err := model.GetPointPurchaseOrderForUser(c.GetInt(ctxkey.Id), order.OrderKey)
				if err != nil {
					paymentHTTPError(c, http.StatusServiceUnavailable, "could not refresh order")
					return
				}
				c.JSON(http.StatusOK, gin.H{"order": toPointOrderView(latest), "provider_status": trade.Status})
				return
			}
			if !isUnpaidTrade(trade) {
				c.JSON(http.StatusAccepted, gin.H{"order": toPointOrderView(order), "checkout_pending": true, "provider_status": trade.Status})
				return
			}
			staleRetry = true
		} else if errors.Is(queryErr, payment.ErrNotPaid) {
			staleRetry = true
		} else {
			c.JSON(http.StatusAccepted, gin.H{"order": toPointOrderView(order), "checkout_pending": true, "message": "payment status is being confirmed"})
			return
		}
	}
	order, err = model.BeginPointProviderCreate(order.OrderKey, staleRetry)
	if errors.Is(err, model.ErrPaymentOperationPending) {
		c.JSON(http.StatusAccepted, gin.H{"order": toPointOrderView(order), "checkout_pending": true})
		return
	}
	if err != nil {
		paymentHTTPError(c, http.StatusConflict, "order is not ready for checkout")
		return
	}
	expires := time.Now().UTC().Add(15 * time.Minute)
	if order.ExpiresAt != nil {
		expires = time.Unix(*order.ExpiresAt, 0).UTC()
	}
	checkout, err := runtime.Provider.Create(c.Request.Context(), payment.Order{OrderKey: order.OrderKey, AmountFen: order.AmountFen, Currency: order.Currency, Description: packageDescription(order), ExpiresAt: expires})
	if err != nil {
		c.JSON(http.StatusAccepted, gin.H{"order": toPointOrderView(order), "checkout_pending": true, "message": "payment status is being confirmed; use the same idempotency key to retry or query this order"})
		return
	}
	checkoutJSON, err := json.Marshal(checkout)
	if err != nil || model.SavePointPurchaseCheckout(order.OrderKey, checkoutJSON) != nil {
		paymentHTTPError(c, http.StatusServiceUnavailable, "checkout was created and order remains pending; query it before retrying")
		return
	}
	order.CheckoutSnapshot = string(checkoutJSON)
	order.ProviderCreateState = "ready"
	c.JSON(http.StatusCreated, gin.H{"order": toPointOrderView(order), "checkout": checkout})
}

func packageDescription(order model.PointPurchaseOrder) string {
	var snapshot model.PointPackageSnapshot
	_ = json.Unmarshal([]byte(order.PackageSnapshot), &snapshot)
	if snapshot.Name == "" {
		return "Miraphant积分套餐"
	}
	return snapshot.Name
}

func PointPurchaseOrderQuery(c *gin.Context) {
	order, runtime, ok := ownOrderAndProvider(c)
	if !ok {
		return
	}
	trade, err := runtime.Provider.Query(c.Request.Context(), order.OrderKey)
	if errors.Is(err, payment.ErrNotPaid) {
		c.JSON(http.StatusOK, gin.H{"order": toPointOrderView(order), "provider_status": "unpaid"})
		return
	}
	if err != nil {
		paymentHTTPError(c, http.StatusBadGateway, "payment status is not confirmed")
		return
	}
	if !validateQueriedTrade(order, runtime.Identity, trade) {
		c.JSON(http.StatusConflict, gin.H{"error": "provider query did not match this order"})
		return
	}
	if isSuccessfulTrade(trade) {
		finalizeQueryEvidence(c, order, runtime, trade)
		return
	}
	if trade.Status == "CLOSED" || trade.Status == "TRADE_CLOSED" {
		if err := model.MarkPointPurchaseOrderClosed(order.OrderKey, "provider_confirmed_closed"); err != nil {
			paymentHTTPError(c, http.StatusServiceUnavailable, "provider status could not be saved")
			return
		}
		latest, err := model.GetPointPurchaseOrderForUser(c.GetInt(ctxkey.Id), order.OrderKey)
		if err != nil {
			paymentHTTPError(c, http.StatusServiceUnavailable, "could not refresh order")
			return
		}
		order = latest
	}
	c.JSON(http.StatusOK, gin.H{"order": toPointOrderView(order), "provider_status": trade.Status})
}

func stableQueryEventID(provider, transactionID string) string {
	return queryEvidenceID(provider, transactionID, "transaction")
}

func queryEvidenceID(provider, transactionID, evidence string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + transactionID + "\x00" + evidence))
	return "query-" + hex.EncodeToString(sum[:])
}

func isSuccessfulTrade(trade payment.Trade) bool {
	return trade.Status == "SUCCESS" || trade.Status == "TRADE_SUCCESS" || trade.Status == "TRADE_FINISHED"
}

func isUnpaidTrade(trade payment.Trade) bool {
	return trade.Status == "NOTPAY" || trade.Status == "WAIT_BUYER_PAY"
}

func validateQueriedTrade(order model.PointPurchaseOrder, identity payments.MerchantIdentity, trade payment.Trade) bool {
	if trade.Provider != identity.Provider || trade.OrderKey != order.OrderKey || trade.MerchantID != order.ProviderMerchantID || trade.AppID != order.ProviderAppID || identity.MerchantID != order.ProviderMerchantID || identity.AppID != order.ProviderAppID {
		return false
	}
	if trade.AmountFen != 0 && trade.AmountFen != order.AmountFen {
		return false
	}
	if trade.Currency != "" && trade.Currency != order.Currency {
		return false
	}
	if isSuccessfulTrade(trade) {
		return trade.TransactionID != "" && trade.AmountFen == order.AmountFen && trade.Currency == order.Currency
	}
	return true
}

func finalizeQueryEvidence(c *gin.Context, order model.PointPurchaseOrder, runtime payments.RuntimeProvider, trade payment.Trade) {
	trade.EvidenceSource = "signed_query"
	canonical := struct {
		Provider, OrderKey, TransactionID, MerchantID, AppID, Currency, Status string
		AmountFen                                                              int64
		OccurredAt                                                             time.Time
	}{trade.Provider, trade.OrderKey, trade.TransactionID, trade.MerchantID, trade.AppID, trade.Currency, trade.Status, trade.AmountFen, trade.ProviderOccurredAt.UTC()}
	encoded, _ := json.Marshal(canonical)
	trade.ProviderEventID = queryEvidenceID(trade.Provider, trade.TransactionID, string(encoded))
	inbox, persistErr := payments.PersistVerifiedNotification(trade, encoded)
	if persistErr != nil {
		paymentHTTPError(c, http.StatusBadGateway, "verified payment evidence could not be stored")
		return
	}
	result, processErr := payments.ProcessVerifiedPaymentEvent(inbox.EventID, runtime.Identity)
	if processErr != nil && result.State != "quarantined" {
		paymentHTTPError(c, http.StatusServiceUnavailable, "payment is recorded and awaiting recovery")
		return
	}
	latest, err := model.GetPointPurchaseOrderForUser(c.GetInt(ctxkey.Id), order.OrderKey)
	if err != nil {
		paymentHTTPError(c, http.StatusServiceUnavailable, "payment evidence was stored but order status could not be refreshed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"order": toPointOrderView(latest), "provider_status": trade.Status, "evidence_state": result.State})
}

func PointPurchaseOrderClose(c *gin.Context) {
	order, runtime, ok := ownOrderAndProvider(c)
	if !ok {
		return
	}
	if order.State == "credited" {
		c.JSON(http.StatusConflict, gin.H{"error": "order is already paid", "order": toPointOrderView(order)})
		return
	}
	if order.State == "closed" {
		c.JSON(http.StatusOK, gin.H{"order": toPointOrderView(order)})
		return
	}
	trade, queryErr := runtime.Provider.Query(c.Request.Context(), order.OrderKey)
	if errors.Is(queryErr, payment.ErrNotPaid) && order.ExpiresAt != nil && *order.ExpiresAt <= time.Now().UTC().Unix() && (order.ProviderCreateState != "started" || order.ProviderCreateStartedAt == nil || time.Now().UTC().Unix()-*order.ProviderCreateStartedAt >= 30) {
		if err := model.MarkPointPurchaseOrderClosed(order.OrderKey, "provider_confirmed_absent_after_expiry"); err != nil {
			paymentHTTPError(c, http.StatusServiceUnavailable, "provider status could not be saved")
			return
		}
		latest, err := model.GetPointPurchaseOrderForUser(c.GetInt(ctxkey.Id), order.OrderKey)
		if err != nil {
			paymentHTTPError(c, http.StatusServiceUnavailable, "could not refresh order")
			return
		}
		c.JSON(http.StatusOK, gin.H{"order": toPointOrderView(latest), "provider_status": "not_found_after_expiry"})
		return
	}
	if queryErr != nil || !validateQueriedTrade(order, runtime.Identity, trade) {
		paymentHTTPError(c, http.StatusConflict, "provider did not confirm this order status")
		return
	}
	if isSuccessfulTrade(trade) {
		finalizeQueryEvidence(c, order, runtime, trade)
		return
	}
	if trade.Status == "CLOSED" || trade.Status == "TRADE_CLOSED" {
		if err := model.MarkPointPurchaseOrderClosed(order.OrderKey, "provider_confirmed_closed"); err != nil {
			paymentHTTPError(c, http.StatusServiceUnavailable, "provider status could not be saved")
			return
		}
		latest, err := model.GetPointPurchaseOrderForUser(c.GetInt(ctxkey.Id), order.OrderKey)
		if err != nil {
			paymentHTTPError(c, http.StatusServiceUnavailable, "could not refresh order")
			return
		}
		c.JSON(http.StatusOK, gin.H{"order": toPointOrderView(latest)})
		return
	}
	if !isUnpaidTrade(trade) {
		paymentHTTPError(c, http.StatusConflict, "provider has not confirmed this order is unpaid")
		return
	}
	err := runtime.Provider.Close(c.Request.Context(), order.OrderKey)
	if errors.Is(err, payment.ErrAlreadyPaid) {
		c.JSON(http.StatusConflict, gin.H{"error": "provider reports this order is paid; wait for verified payment processing"})
		return
	}
	if err != nil {
		paymentHTTPError(c, http.StatusConflict, "provider did not confirm that this order can be closed")
		return
	}
	if err := model.MarkPointPurchaseOrderClosed(order.OrderKey, "provider_confirmed_unpaid_and_closed"); err != nil {
		paymentHTTPError(c, http.StatusServiceUnavailable, "provider closed the order; local status requires reconciliation")
		return
	}
	order, err = model.GetPointPurchaseOrderForUser(c.GetInt(ctxkey.Id), order.OrderKey)
	if err != nil {
		paymentHTTPError(c, http.StatusServiceUnavailable, "order was closed but local status could not be refreshed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"order": toPointOrderView(order)})
}

func ownOrderAndProvider(c *gin.Context) (model.PointPurchaseOrder, payments.RuntimeProvider, bool) {
	order, err := model.GetPointPurchaseOrderForUser(c.GetInt(ctxkey.Id), c.Param("key"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return order, payments.RuntimeProvider{}, false
	}
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "could not load order")
		return order, payments.RuntimeProvider{}, false
	}
	runtime, ok := payments.ProviderFor(order.Channel)
	if !ok || runtime.Provider == nil || order.ProviderMerchantID == "" || order.ProviderAppID == "" || runtime.Identity.MerchantID != order.ProviderMerchantID || runtime.Identity.AppID != order.ProviderAppID {
		paymentHTTPError(c, http.StatusServiceUnavailable, "the frozen payment identity is unavailable")
		return order, runtime, false
	}
	return order, runtime, true
}

func WeChatPaymentNotify(c *gin.Context) { paymentNotify(c, "wechat") }
func AlipayPaymentNotify(c *gin.Context) { paymentNotify(c, "alipay") }

func paymentNotify(c *gin.Context, channel string) {
	if err := model.RequirePointsSchema(); err != nil {
		paymentHTTPError(c, http.StatusServiceUnavailable, "payment inbox is not ready")
		return
	}
	runtime, ok := payments.ProviderFor(channel)
	if !ok || runtime.Provider == nil {
		paymentHTTPError(c, http.StatusServiceUnavailable, "payment verification is unavailable")
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, (1<<20)+1))
	if err != nil || len(body) == 0 || len(body) > 1<<20 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid notification"})
		return
	}
	trade, err := runtime.Provider.VerifyNotification(c.Request.Header, body)
	if err != nil || trade.Provider != channel {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid notification signature"})
		return
	}
	inbox, err := payments.PersistVerifiedNotification(trade, body)
	if err != nil {
		if errors.Is(err, model.ErrPointsConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": "conflicting notification evidence"})
			return
		}
		paymentHTTPError(c, http.StatusServiceUnavailable, "notification could not be durably recorded")
		return
	}
	result, processErr := payments.ProcessVerifiedPaymentEvent(inbox.EventID, runtime.Identity)
	if processErr != nil && result.State != "quarantined" {
		paymentHTTPError(c, http.StatusServiceUnavailable, "notification recorded; processing is pending recovery")
		return
	}
	if channel == "wechat" {
		c.JSON(http.StatusOK, gin.H{"code": "SUCCESS", "message": "成功"})
	} else {
		c.String(http.StatusOK, "success")
	}
}

func AdminRecoverPaymentEvents(c *gin.Context) {
	if err := model.RequirePointsSchema(); err != nil {
		paymentHTTPError(c, http.StatusServiceUnavailable, "payment inbox is not ready")
		return
	}
	after, _ := strconv.ParseUint(c.Query("after_id"), 10, 64)
	limit, _ := strconv.Atoi(c.Query("limit"))
	identities := map[string]payments.MerchantIdentity{}
	for _, name := range []string{"wechat", "alipay"} {
		if runtime, ok := payments.ProviderFor(name); ok {
			identities[name] = runtime.Identity
		}
	}
	report, err := payments.RetryReceivedPaymentEventsDetailed(uint(after), limit, identities)
	if err != nil {
		paymentHTTPError(c, http.StatusServiceUnavailable, "recovery scan failed")
		return
	}
	c.JSON(http.StatusOK, report)
}

func paymentHTTPError(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": message})
}

package controller

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/model"
	"gorm.io/gorm"
)

type adminOrderView struct {
	ID                      uint      `json:"id"`
	OrderKey                string    `json:"order_key"`
	UserID                  int       `json:"user_id"`
	Channel                 string    `json:"channel"`
	PackageID               string    `json:"package_id"`
	PackageVersion          string    `json:"package_version"`
	PackageName             string    `json:"package_name"`
	Currency                string    `json:"currency"`
	AmountFen               int64     `json:"amount_fen"`
	PurchaseMicro           int64     `json:"purchase_micro"`
	BonusMicro              int64     `json:"bonus_micro"`
	State                   string    `json:"state"`
	ProviderTransactionHint string    `json:"provider_transaction_hint,omitempty"`
	ExpiresAt               *int64    `json:"expires_at,omitempty"`
	CreatedAt               time.Time `json:"created_at"`
}

func toAdminOrderView(order model.PointPurchaseOrder) adminOrderView {
	view := toPointOrderView(order)
	return adminOrderView{
		ID: order.ID, OrderKey: order.OrderKey, UserID: order.UserID, Channel: order.Channel,
		PackageID: order.PackageID, PackageVersion: order.PackageVersion, PackageName: view.PackageName,
		Currency: order.Currency, AmountFen: order.AmountFen, PurchaseMicro: order.PurchaseMicro,
		BonusMicro: order.BonusMicro, State: order.State,
		ProviderTransactionHint: maskPaymentIdentifier(order.ProviderTransactionID),
		ExpiresAt:               order.ExpiresAt, CreatedAt: order.CreatedAt,
	}
}

func AdminPaymentOrders(c *gin.Context) {
	if !requirePaymentAdminSchema(c) {
		return
	}
	filter, limit, ok := parseAdminOrderFilter(c)
	if !ok {
		return
	}
	filter.Limit = limit + 1
	rows, err := model.ListPurchaseOrdersForAdmin(filter)
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "订单记录暂时无法读取")
		return
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
	}
	items := make([]adminOrderView, 0, len(rows))
	for _, order := range rows {
		items = append(items, toAdminOrderView(order))
	}
	c.JSON(http.StatusOK, gin.H{"orders": items, "next_cursor": next})
}

type adminCreditLedgerView struct {
	ID             uint      `json:"id"`
	Kind           string    `json:"kind"`
	AvailableDelta int64     `json:"available_delta"`
	AvailableAfter int64     `json:"available_after"`
	CreatedAt      time.Time `json:"created_at"`
}

type adminPaymentEventView struct {
	ID                 uint       `json:"id"`
	Provider           string     `json:"provider"`
	ProviderEventHint  string     `json:"provider_event_hint,omitempty"`
	TransactionHint    string     `json:"transaction_hint,omitempty"`
	Status             string     `json:"status,omitempty"`
	AmountFen          int64      `json:"amount_fen,omitempty"`
	Currency           string     `json:"currency,omitempty"`
	ProviderOccurredAt *time.Time `json:"provider_occurred_at,omitempty"`
	Verification       string     `json:"verification"`
	State              string     `json:"state"`
	ErrorCode          string     `json:"error_code,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	ProcessedAt        *time.Time `json:"processed_at,omitempty"`
}

func toAdminPaymentEventView(event model.PaymentEvent) adminPaymentEventView {
	var payload struct {
		Status             string    `json:"status"`
		AmountFen          int64     `json:"amount_fen"`
		Currency           string    `json:"currency"`
		ProviderOccurredAt time.Time `json:"provider_occurred_at"`
	}
	_ = json.Unmarshal([]byte(event.Payload), &payload)
	var providerAt *time.Time
	if !payload.ProviderOccurredAt.IsZero() {
		providerAt = &payload.ProviderOccurredAt
	}
	return adminPaymentEventView{
		ID: event.ID, Provider: event.Provider, ProviderEventHint: maskPaymentIdentifier(event.ProviderEventID),
		TransactionHint: maskPaymentIdentifier(event.ProviderTransactionID), Status: payload.Status,
		AmountFen: payload.AmountFen, Currency: payload.Currency, ProviderOccurredAt: providerAt, Verification: event.Verification,
		State: event.State, ErrorCode: event.ErrorCode, CreatedAt: event.CreatedAt, ProcessedAt: event.ProcessedAt,
	}
}

func AdminPaymentOrder(c *gin.Context) {
	if !requirePaymentAdminSchema(c) {
		return
	}
	orderKey := strings.TrimSpace(c.Param("key"))
	if !validAdminOrderKey(orderKey) {
		paymentHTTPError(c, http.StatusBadRequest, "订单编号格式无效")
		return
	}
	order, err := model.GetPurchaseOrderForAdmin(orderKey)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			paymentHTTPError(c, http.StatusNotFound, "订单不存在")
		} else {
			paymentHTTPError(c, http.StatusInternalServerError, "订单记录暂时无法读取")
		}
		return
	}
	ledger, err := model.GetPurchaseOrderCreditLedger(order)
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "入账记录暂时无法读取")
		return
	}
	eventBefore, err := parseOptionalPositiveUint(c.Query("event_before_id"), "event_before_id")
	if err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "事件游标无效")
		return
	}
	eventLimit, err := parseOptionalLimit(c.Query("event_limit"), 20, 100)
	if err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "事件分页数量无效")
		return
	}
	events, err := model.ListPurchaseOrderEvents(order.OrderKey, eventBefore, eventLimit+1)
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "核验事件暂时无法读取")
		return
	}
	next := ""
	if len(events) > eventLimit {
		events = events[:eventLimit]
		next = strconv.FormatUint(uint64(events[len(events)-1].ID), 10)
	}
	var credit *adminCreditLedgerView
	if ledger != nil {
		credit = &adminCreditLedgerView{ID: ledger.ID, Kind: ledger.Kind, AvailableDelta: ledger.AvailableDelta, AvailableAfter: ledger.AvailableAfter, CreatedAt: ledger.CreatedAt}
	}
	eventViews := make([]adminPaymentEventView, 0, len(events))
	for _, event := range events {
		eventViews = append(eventViews, toAdminPaymentEventView(event))
	}
	c.JSON(http.StatusOK, gin.H{"order": toAdminOrderView(order), "credit_ledger": credit, "events": eventViews, "events_next_cursor": next})
}

type adminAuditSummary struct {
	PackageID   string `json:"package_id,omitempty"`
	Version     string `json:"version,omitempty"`
	ModelID     string `json:"model_id,omitempty"`
	AmountMicro int64  `json:"amount_micro,omitempty"`
	Action      string `json:"decision_action,omitempty"`
	UsageMicro  int64  `json:"usage_micro,omitempty"`
	Activated   *bool  `json:"activated,omitempty"`
}

type adminAuditView struct {
	ID                uint              `json:"id"`
	ActorUserID       int               `json:"actor_user_id"`
	ActorLabel        string            `json:"actor_label"`
	TargetUserID      int               `json:"target_user_id"`
	Action            string            `json:"action"`
	BusinessReference string            `json:"business_reference"`
	Summary           adminAuditSummary `json:"summary"`
	Reason            string            `json:"reason,omitempty"`
	RelatedOrder      string            `json:"related_order_key,omitempty"`
	RelatedRequest    string            `json:"related_request_key,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
}

func toAdminAuditView(audit model.PointAdminAudit) (adminAuditView, error) {
	actorLabel := "管理员 #" + strconv.Itoa(audit.ActorUserID)
	if audit.ActorUserID == 0 {
		actorLabel = "系统"
	}
	view := adminAuditView{ID: audit.ID, ActorUserID: audit.ActorUserID, ActorLabel: actorLabel, TargetUserID: audit.TargetUserID, Action: audit.Action, BusinessReference: safeAuditBusinessReference(audit), CreatedAt: audit.CreatedAt}
	links, err := model.PointAdminAuditLinks(audit)
	if err != nil {
		return adminAuditView{}, err
	}
	view.RelatedOrder, view.RelatedRequest = links.OrderKey, links.RequestKey
	switch audit.Action {
	case "package_publish":
		var d struct {
			PackageID string `json:"package_id"`
			Version   string `json:"version"`
			Activate  bool   `json:"activate"`
		}
		if json.Unmarshal([]byte(audit.Details), &d) == nil {
			view.Summary.PackageID, view.Summary.Version = d.PackageID, d.Version
			view.Summary.Activated = &d.Activate
		}
	case "price_publish":
		var d struct {
			ModelID string `json:"model_id"`
			Version string `json:"version"`
		}
		if json.Unmarshal([]byte(audit.Details), &d) == nil {
			view.Summary.ModelID, view.Summary.Version = d.ModelID, d.Version
		}
	case "points_grant":
		var d struct {
			AmountMicro int64  `json:"amount_micro"`
			Reason      string `json:"reason"`
		}
		if json.Unmarshal([]byte(audit.Details), &d) == nil {
			view.Summary.AmountMicro = d.AmountMicro
			view.Reason = safeAuditReason(d.Reason)
		}
	case "hold_resolution":
		var d struct {
			Action     string `json:"action"`
			UsageMicro int64  `json:"usage_micro"`
			Reason     string `json:"reason"`
		}
		if json.Unmarshal([]byte(audit.Details), &d) == nil {
			view.Summary.Action, view.Summary.UsageMicro = d.Action, d.UsageMicro
			view.Reason = safeAuditReason(d.Reason)
		}
	case "offline_hold_recovery":
		var d struct {
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(audit.Details), &d) == nil {
			view.Reason = safeAuditReason(d.Reason)
		}
	}
	return view, nil
}

func safeAuditBusinessReference(audit model.PointAdminAudit) string {
	prefix := ""
	switch audit.Action {
	case "package_publish":
		prefix = "package_publish:"
	case "price_publish":
		prefix = "price:"
	case "points_grant":
		prefix = "adjustment:"
	case "hold_resolution":
		prefix = "hold-decision:"
	case "offline_hold_recovery":
		if strings.HasPrefix(audit.BusinessKey, "offline-recovery-hold:") {
			prefix = "offline-recovery-hold:"
		} else {
			prefix = "offline-recovery-batch:"
		}
	default:
		return "audit:" + strconv.FormatUint(uint64(audit.ID), 10)
	}
	if !strings.HasPrefix(audit.BusinessKey, prefix) {
		return "audit:" + strconv.FormatUint(uint64(audit.ID), 10)
	}
	suffix := strings.TrimPrefix(audit.BusinessKey, prefix)
	if len(suffix) == 0 || len(suffix) > 96 {
		return "audit:" + strconv.FormatUint(uint64(audit.ID), 10)
	}
	for _, r := range suffix {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == ':') {
			return "audit:" + strconv.FormatUint(uint64(audit.ID), 10)
		}
	}
	return prefix + suffix
}

func safeAuditReason(reason string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(reason) {
		if r == '\n' || r == '\r' || r == '\t' || r < 0x20 || r == 0x7f {
			r = ' '
		}
		b.WriteRune(r)
		if b.Len() >= 512 {
			break
		}
	}
	return strings.TrimSpace(b.String())
}

func AdminPaymentAudit(c *gin.Context) {
	if !requirePaymentAdminSchema(c) {
		return
	}
	filter, limit, ok := parseAdminAuditFilter(c)
	if !ok {
		return
	}
	filter.Limit = limit + 1
	page, err := model.ListPointAdminAudits(filter)
	if err != nil {
		paymentHTTPError(c, http.StatusInternalServerError, "审计记录暂时无法读取")
		return
	}
	rows := page.Rows
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
	} else if page.ScanLimitReached && page.ScannedThroughID > 0 {
		next = strconv.FormatUint(uint64(page.ScannedThroughID), 10)
	}
	items := make([]adminAuditView, 0, len(rows))
	for _, row := range rows {
		view, err := toAdminAuditView(row)
		if err != nil {
			paymentHTTPError(c, http.StatusInternalServerError, "审计关联暂时无法读取")
			return
		}
		items = append(items, view)
	}
	c.JSON(http.StatusOK, gin.H{"audits": items, "next_cursor": next})
}

func requirePaymentAdminSchema(c *gin.Context) bool {
	if err := model.RequirePointsSchema(); err != nil {
		paymentHTTPError(c, http.StatusServiceUnavailable, "管理数据暂不可用")
		return false
	}
	return true
}

func parseAdminOrderFilter(c *gin.Context) (model.AdminPurchaseOrderFilter, int, bool) {
	var filter model.AdminPurchaseOrderFilter
	limit, err := parseOptionalLimit(c.Query("limit"), 30, 100)
	if err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "分页数量无效")
		return filter, 0, false
	}
	before, err := parseOptionalPositiveUint(c.Query("before_id"), "before_id")
	if err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "分页游标无效")
		return filter, 0, false
	}
	filter.Limit = limit
	filter.BeforeID = before
	filter.OrderKey = strings.TrimSpace(c.Query("order_key"))
	if filter.OrderKey != "" && !validAdminOrderKey(filter.OrderKey) {
		paymentHTTPError(c, http.StatusBadRequest, "订单编号格式无效")
		return filter, 0, false
	}
	if raw := strings.TrimSpace(c.Query("user_id")); raw != "" {
		filter.UserID, err = strconv.Atoi(raw)
		if err != nil || filter.UserID <= 0 {
			paymentHTTPError(c, http.StatusBadRequest, "客户编号无效")
			return filter, 0, false
		}
	}
	filter.Channel = strings.TrimSpace(c.Query("channel"))
	if filter.Channel != "" && filter.Channel != "wechat" && filter.Channel != "alipay" {
		paymentHTTPError(c, http.StatusBadRequest, "支付渠道筛选无效")
		return filter, 0, false
	}
	filter.State = strings.TrimSpace(c.Query("state"))
	if filter.State != "" && !validPurchaseOrderState(filter.State) {
		paymentHTTPError(c, http.StatusBadRequest, "订单状态筛选无效")
		return filter, 0, false
	}
	filter.From, err = parseUTCFilter(c.Query("from"))
	if err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "开始时间须使用UTC ISO8601格式")
		return filter, 0, false
	}
	filter.To, err = parseUTCFilter(c.Query("to"))
	if err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "结束时间须使用UTC ISO8601格式")
		return filter, 0, false
	}
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		paymentHTTPError(c, http.StatusBadRequest, "开始时间不能晚于结束时间")
		return filter, 0, false
	}
	return filter, limit, true
}

func parseAdminAuditFilter(c *gin.Context) (model.AdminPointAuditFilter, int, bool) {
	var filter model.AdminPointAuditFilter
	limit, err := parseOptionalLimit(c.Query("limit"), 30, 100)
	if err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "分页数量无效")
		return filter, 0, false
	}
	filter.Limit = limit
	filter.BeforeID, err = parseOptionalPositiveUint(c.Query("before_id"), "before_id")
	if err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "分页游标无效")
		return filter, 0, false
	}
	if raw := strings.TrimSpace(c.Query("actor_id")); raw != "" {
		id, parseErr := strconv.Atoi(raw)
		if parseErr != nil || id < 0 {
			paymentHTTPError(c, http.StatusBadRequest, "操作者编号无效")
			return filter, 0, false
		}
		filter.ActorID = &id
	}
	filter.Action = strings.TrimSpace(c.Query("action"))
	if filter.Action != "" && !model.IsKnownPointAuditAction(filter.Action) {
		paymentHTTPError(c, http.StatusBadRequest, "审计动作筛选无效")
		return filter, 0, false
	}
	filter.OrderKey = strings.TrimSpace(c.Query("order_key"))
	if filter.OrderKey != "" && !validAdminOrderKey(filter.OrderKey) {
		paymentHTTPError(c, http.StatusBadRequest, "订单编号格式无效")
		return filter, 0, false
	}
	filter.RequestKey = strings.TrimSpace(c.Query("request_key"))
	if len(filter.RequestKey) > 180 || strings.ContainsAny(filter.RequestKey, "\r\n\x00") {
		paymentHTTPError(c, http.StatusBadRequest, "请求编号无效")
		return filter, 0, false
	}
	filter.From, err = parseUTCFilter(c.Query("from"))
	if err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "开始时间须使用UTC ISO8601格式")
		return filter, 0, false
	}
	filter.To, err = parseUTCFilter(c.Query("to"))
	if err != nil {
		paymentHTTPError(c, http.StatusBadRequest, "结束时间须使用UTC ISO8601格式")
		return filter, 0, false
	}
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		paymentHTTPError(c, http.StatusBadRequest, "开始时间不能晚于结束时间")
		return filter, 0, false
	}
	return filter, limit, true
}

func parseOptionalLimit(raw string, fallback, max int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > max {
		return 0, gorm.ErrInvalidData
	}
	return value, nil
}

func parseOptionalPositiveUint(raw, field string) (uint, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 || uint64(uint(value)) != value {
		return 0, gorm.ErrInvalidData
	}
	return uint(value), nil
}

func parseUTCFilter(raw string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if !strings.HasSuffix(raw, "Z") {
		return nil, gorm.ErrInvalidData
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, err
	}
	utc := value.UTC()
	return &utc, nil
}

func validAdminOrderKey(value string) bool {
	if len(value) == 0 || len(value) > 160 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func validPurchaseOrderState(value string) bool {
	switch value {
	case "created", "pending", "paid", "credited", "closed", "paid_review", "refunded":
		return true
	default:
		return false
	}
}

func maskPaymentIdentifier(value string) string {
	if value == "" {
		return ""
	}
	if len(value) <= 6 {
		return "••••"
	}
	return "••••" + value[len(value)-6:]
}

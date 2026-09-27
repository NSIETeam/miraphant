package controller

import (
	"context"
	"crypto/rand"
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
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
	"github.com/songquanpeng/one-api/service/payments"
	"gorm.io/gorm"
)

type reconciliationBatchDTO struct {
	ID                   uint      `json:"id"`
	BatchKey             string    `json:"batch_key"`
	Provider             string    `json:"provider"`
	MerchantID           string    `json:"merchant_id"`
	AppID                string    `json:"app_id"`
	BillDate             string    `json:"bill_date"`
	BillType             string    `json:"bill_type"`
	FormatVersion        string    `json:"format_version"`
	ImportVersion        int       `json:"import_version"`
	Timezone             string    `json:"timezone"`
	Status               string    `json:"status"`
	SourceSHA256         string    `json:"source_sha256"`
	ProviderHashType     string    `json:"provider_hash_type,omitempty"`
	ProviderHashVerified bool      `json:"provider_hash_verified"`
	SourceSize           int64     `json:"source_size"`
	RowCount             int       `json:"row_count"`
	CreatedAt            time.Time `json:"created_at"`
}

type reconciliationRowDTO struct {
	ID                 uint      `json:"id"`
	SourceLine         int       `json:"source_line"`
	SourceDigest       string    `json:"source_digest"`
	Provider           string    `json:"provider"`
	Kind               string    `json:"kind"`
	ProviderStatus     string    `json:"provider_status"`
	RefundStatus       string    `json:"refund_status,omitempty"`
	OccurredAt         time.Time `json:"occurred_at"`
	MerchantID         string    `json:"merchant_id"`
	AppID              string    `json:"app_id"`
	OrderKey           string    `json:"order_key,omitempty"`
	TransactionID      string    `json:"transaction_id,omitempty"`
	MerchantRefundKey  string    `json:"merchant_refund_key,omitempty"`
	ProviderRefundID   string    `json:"provider_refund_id,omitempty"`
	Currency           string    `json:"currency"`
	GrossFen           int64     `json:"gross_fen"`
	SettlementFen      int64     `json:"settlement_fen"`
	DiscountFen        int64     `json:"discount_fen"`
	RefundFen          int64     `json:"refund_fen"`
	CouponRefundFen    int64     `json:"coupon_refund_fen"`
	RefundRequestedFen int64     `json:"refund_requested_fen"`
	FeeFen             int64     `json:"fee_fen"`
	NetFen             int64     `json:"net_fen"`
	HasNetFen          bool      `json:"has_net_fen"`
	Result             string    `json:"result"`
}

type reconciliationDifferenceDTO struct {
	ID                  uint      `json:"id"`
	DifferenceKey       string    `json:"difference_key"`
	BatchID             uint      `json:"batch_id"`
	RowID               *uint     `json:"row_id,omitempty"`
	LocalOrderID        *uint     `json:"local_order_id,omitempty"`
	LocalRefundID       *uint     `json:"local_refund_id,omitempty"`
	PaymentEventID      *uint     `json:"payment_event_id,omitempty"`
	Classification      string    `json:"classification"`
	Severity            string    `json:"severity"`
	EvidenceFingerprint string    `json:"evidence_fingerprint"`
	CreatedAt           time.Time `json:"created_at"`
}

type reconciliationActionDTO struct {
	ID           uint      `json:"id"`
	DifferenceID uint      `json:"difference_id"`
	ActorUserID  int       `json:"actor_user_id"`
	Action       string    `json:"action"`
	Reason       string    `json:"reason"`
	BusinessRef  string    `json:"business_ref,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type reconciliationImportAuditDTO struct {
	ID           uint      `json:"id"`
	RequestKey   string    `json:"request_key"`
	Phase        string    `json:"phase"`
	ActorUserID  int       `json:"actor_user_id"`
	Provider     string    `json:"provider"`
	BillDate     string    `json:"bill_date"`
	MerchantID   string    `json:"merchant_id,omitempty"`
	AppID        string    `json:"app_id,omitempty"`
	BatchID      *uint     `json:"batch_id,omitempty"`
	SourceSHA256 string    `json:"source_sha256,omitempty"`
	Outcome      string    `json:"outcome"`
	ErrorCode    string    `json:"error_code,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func AdminReconciliationStatus(c *gin.Context) {
	if !reconciliationSchemaReady(c) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"providers": payments.ReconciliationImportStatuses(), "source_encryption_ready": payments.ReconciliationSourceEncryptionReady()})
}

func AdminReconciliationBatches(c *gin.Context) {
	if !reconciliationSchemaReady(c) {
		return
	}
	limit, err := reconciliationLimit(c.Query("limit"))
	if err != nil {
		reconciliationHTTPError(c, http.StatusBadRequest, "分页数量无效")
		return
	}
	before, err := reconciliationCursor(c.Query("before_id"))
	if err != nil {
		reconciliationHTTPError(c, http.StatusBadRequest, "分页游标无效")
		return
	}
	filter := model.PointReconciliationBatchFilter{BeforeID: before, Limit: limit, Provider: strings.TrimSpace(c.Query("provider")), BillDate: strings.TrimSpace(c.Query("bill_date")), Status: strings.TrimSpace(c.Query("status"))}
	if (filter.Provider != "" && filter.Provider != "wechat" && filter.Provider != "alipay") ||
		(filter.Status != "" && filter.Status != "imported" && filter.Status != "unsupported_format") ||
		(filter.BillDate != "" && !validReconciliationDate(filter.BillDate)) {
		reconciliationHTTPError(c, http.StatusBadRequest, "筛选条件无效")
		return
	}
	rows, more, err := model.ListPointReconciliationBatches(filter)
	if err != nil {
		reconciliationHTTPError(c, http.StatusInternalServerError, "账单批次暂时无法读取")
		return
	}
	items := make([]reconciliationBatchDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, toReconciliationBatchDTO(row))
	}
	next := ""
	if more && len(rows) > 0 {
		next = strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
	}
	c.JSON(http.StatusOK, gin.H{"batches": items, "next_cursor": next})
}

func AdminReconciliationBatch(c *gin.Context) {
	if !reconciliationSchemaReady(c) {
		return
	}
	batch, err := model.GetPointReconciliationBatch(strings.TrimSpace(c.Param("key")))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		reconciliationHTTPError(c, http.StatusNotFound, "账单批次不存在")
		return
	}
	if err != nil {
		reconciliationHTTPError(c, http.StatusInternalServerError, "账单批次暂时无法读取")
		return
	}
	c.JSON(http.StatusOK, gin.H{"batch": toReconciliationBatchDTO(batch)})
}

func AdminReconciliationRows(c *gin.Context) {
	if !reconciliationSchemaReady(c) {
		return
	}
	key := strings.TrimSpace(c.Param("key"))
	if _, err := model.GetPointReconciliationBatch(key); errors.Is(err, gorm.ErrRecordNotFound) {
		reconciliationHTTPError(c, http.StatusNotFound, "账单批次不存在")
		return
	} else if err != nil {
		reconciliationHTTPError(c, http.StatusInternalServerError, "账单批次暂时无法读取")
		return
	}
	before, err := reconciliationCursor(c.Query("before_id"))
	if err != nil {
		reconciliationHTTPError(c, http.StatusBadRequest, "分页游标无效")
		return
	}
	limit, err := reconciliationLimit(c.Query("limit"))
	if err != nil {
		reconciliationHTTPError(c, http.StatusBadRequest, "分页数量无效")
		return
	}
	rows, more, err := model.ListPointReconciliationRows(key, before, limit)
	if err != nil {
		reconciliationHTTPError(c, http.StatusInternalServerError, "账单明细暂时无法读取")
		return
	}
	items := make([]reconciliationRowDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, toReconciliationRowDTO(row))
	}
	next := ""
	if more && len(rows) > 0 {
		next = strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
	}
	c.JSON(http.StatusOK, gin.H{"rows": items, "next_cursor": next})
}

func AdminReconciliationDifferences(c *gin.Context) {
	if !reconciliationSchemaReady(c) {
		return
	}
	key := strings.TrimSpace(c.Param("key"))
	batch, err := model.GetPointReconciliationBatch(key)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		reconciliationHTTPError(c, http.StatusNotFound, "账单批次不存在")
		return
	}
	if err != nil {
		reconciliationHTTPError(c, http.StatusInternalServerError, "账单批次暂时无法读取")
		return
	}
	before, err := reconciliationCursor(c.Query("before_id"))
	if err != nil {
		reconciliationHTTPError(c, http.StatusBadRequest, "分页游标无效")
		return
	}
	limit, err := reconciliationLimit(c.Query("limit"))
	if err != nil {
		reconciliationHTTPError(c, http.StatusBadRequest, "分页数量无效")
		return
	}
	classification, severity := strings.TrimSpace(c.Query("classification")), strings.TrimSpace(c.Query("severity"))
	if (classification != "" && !validReconciliationHTTPClassification(classification)) || (severity != "" && severity != "attention" && severity != "informational") {
		reconciliationHTTPError(c, http.StatusBadRequest, "差异筛选条件无效")
		return
	}
	rows, more, err := model.ListPointReconciliationDifferences(model.PointReconciliationDifferenceFilter{BatchID: batch.ID, BeforeID: before, Limit: limit, Classification: classification, Severity: severity})
	if err != nil {
		reconciliationHTTPError(c, http.StatusInternalServerError, "差异记录暂时无法读取")
		return
	}
	items := make([]reconciliationDifferenceDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, toReconciliationDifferenceDTO(row))
	}
	next := ""
	if more && len(rows) > 0 {
		next = strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
	}
	c.JSON(http.StatusOK, gin.H{"differences": items, "next_cursor": next})
}

func AdminReconciliationActions(c *gin.Context) {
	if !reconciliationSchemaReady(c) {
		return
	}
	diffID, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || diffID == 0 {
		reconciliationHTTPError(c, http.StatusBadRequest, "差异编号无效")
		return
	}
	if !reconciliationDifferenceExists(uint(diffID), c) {
		return
	}
	before, err := reconciliationCursor(c.Query("before_id"))
	if err != nil {
		reconciliationHTTPError(c, http.StatusBadRequest, "分页游标无效")
		return
	}
	limit, err := reconciliationLimit(c.Query("limit"))
	if err != nil {
		reconciliationHTTPError(c, http.StatusBadRequest, "分页数量无效")
		return
	}
	rows, more, err := model.ListPointReconciliationActions(uint(diffID), before, limit)
	if err != nil {
		reconciliationHTTPError(c, http.StatusInternalServerError, "处理记录暂时无法读取")
		return
	}
	items := make([]reconciliationActionDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, toReconciliationActionDTO(row))
	}
	next := ""
	if more && len(rows) > 0 {
		next = strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
	}
	c.JSON(http.StatusOK, gin.H{"actions": items, "next_cursor": next})
}

func AdminReconciliationImportAudits(c *gin.Context) {
	if !reconciliationSchemaReady(c) {
		return
	}
	before, err := reconciliationCursor(c.Query("before_id"))
	if err != nil {
		reconciliationHTTPError(c, http.StatusBadRequest, "分页游标无效")
		return
	}
	limit, err := reconciliationLimit(c.Query("limit"))
	if err != nil {
		reconciliationHTTPError(c, http.StatusBadRequest, "分页数量无效")
		return
	}
	provider, outcome := strings.TrimSpace(c.Query("provider")), strings.TrimSpace(c.Query("outcome"))
	if (provider != "" && provider != "wechat" && provider != "alipay") || (outcome != "" && outcome != "started" && outcome != "imported" && outcome != "replayed" && outcome != "unsupported" && outcome != "failed") {
		reconciliationHTTPError(c, http.StatusBadRequest, "导入记录筛选条件无效")
		return
	}
	rows, more, err := model.ListPointReconciliationImportAudits(before, limit, provider, outcome)
	if err != nil {
		reconciliationHTTPError(c, http.StatusInternalServerError, "导入记录暂时无法读取")
		return
	}
	items := make([]reconciliationImportAuditDTO, 0, len(rows))
	for _, row := range rows {
		items = append(items, toReconciliationImportAuditDTO(row))
	}
	next := ""
	if more && len(rows) > 0 {
		next = strconv.FormatUint(uint64(rows[len(rows)-1].ID), 10)
	}
	c.JSON(http.StatusOK, gin.H{"attempts": items, "next_cursor": next})
}

func AdminImportReconciliationBill(c *gin.Context) {
	if !reconciliationSchemaReady(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var request struct {
		Provider string `json:"provider"`
		BillDate string `json:"bill_date"`
	}
	if err := decoder.Decode(&request); err != nil {
		reconciliationHTTPError(c, http.StatusBadRequest, "导入参数无效")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		reconciliationHTTPError(c, http.StatusBadRequest, "导入参数无效")
		return
	}
	request.Provider = strings.TrimSpace(request.Provider)
	request.BillDate = strings.TrimSpace(request.BillDate)
	if (request.Provider != "wechat" && request.Provider != "alipay") || !validReconciliationDate(request.BillDate) {
		reconciliationHTTPError(c, http.StatusBadRequest, "渠道或账单日期无效")
		return
	}
	requestKey, err := newReconciliationRequestKey()
	if err != nil {
		reconciliationHTTPError(c, http.StatusInternalServerError, "无法记录本次导入请求")
		return
	}
	identity := providerReconciliationIdentity(request.Provider)
	actorID := c.GetInt(ctxkey.Id)
	startAudit := model.RecordPointReconciliationImportAuditRequest{RequestKey: requestKey, Phase: "started", Outcome: "started", ActorUserID: actorID, Provider: request.Provider, BillDate: request.BillDate, MerchantID: identity.MerchantID, AppID: identity.AppID}
	startCtx, startCancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	auditErr := model.RecordPointReconciliationImportAuditContext(startCtx, startAudit)
	startCancel()
	if auditErr != nil {
		reconciliationHTTPError(c, http.StatusInternalServerError, "无法记录本次导入请求")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	resultAudit := startAudit
	resultAudit.Phase = "result"
	batch, created, importErr := payments.ImportConfiguredReconciliationBill(ctx, request.Provider, request.BillDate, resultAudit)
	if importErr != nil {
		resultAudit.Outcome = "failed"
		resultAudit.ErrorCode = reconciliationImportErrorCode(importErr)
		// Use a fresh bounded persistence path so client cancellation does not erase
		// the accepted attempt's final failure evidence.
		finishCtx, finishCancel := context.WithTimeout(context.Background(), 5*time.Second)
		auditErr := model.RecordPointReconciliationImportAuditContext(finishCtx, resultAudit)
		finishCancel()
		if auditErr != nil {
			reconciliationHTTPError(c, http.StatusInternalServerError, "导入结果暂未确认，请查询导入记录后重试")
			return
		}
	}
	if importErr != nil {
		status := http.StatusBadGateway
		message := "渠道账单暂时无法获取或校验"
		if errors.Is(importErr, payments.ErrReconciliationImportBusy) {
			status = http.StatusConflict
			message = "已有账单导入正在处理"
		} else if errors.Is(importErr, payments.ErrReconciliationSourceKeyUnavailable) {
			status = http.StatusServiceUnavailable
			message = "账单加密配置未就绪，暂不能导入"
		} else if errors.Is(importErr, payments.ErrReconciliationProviderUnavailable) {
			status = http.StatusServiceUnavailable
			message = "该支付渠道尚未配置"
		}
		reconciliationHTTPError(c, status, message)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	c.JSON(status, gin.H{"batch": toReconciliationBatchDTO(model.PointReconciliationBatchView{ID: batch.ID, BatchKey: batch.BatchKey, Provider: batch.Provider, MerchantID: batch.MerchantID, AppID: batch.AppID, BillDate: batch.BillDate, BillType: batch.BillType, FormatVersion: batch.FormatVersion, ImportVersion: batch.ImportVersion, Timezone: batch.Timezone, Status: batch.Status, SourceSHA256: batch.SourceSHA256, ProviderHashType: batch.ProviderHashType, ProviderHashVerified: batch.ProviderHashVerified, SourceSize: batch.SourceSize, RowCount: batch.RowCount, CreatedAt: batch.CreatedAt}), "created": created})
}

func AdminRecordReconciliationAction(c *gin.Context) {
	if !reconciliationSchemaReady(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	var request struct {
		ActionKey   string `json:"action_key"`
		Action      string `json:"action"`
		Reason      string `json:"reason"`
		BusinessRef string `json:"business_ref"`
	}
	if err := decoder.Decode(&request); err != nil {
		reconciliationHTTPError(c, http.StatusBadRequest, "处理记录参数无效")
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		reconciliationHTTPError(c, http.StatusBadRequest, "处理记录参数无效")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		reconciliationHTTPError(c, http.StatusBadRequest, "差异编号无效")
		return
	}
	if !reconciliationDifferenceExists(uint(id), c) {
		return
	}
	request.ActionKey = strings.TrimSpace(request.ActionKey)
	request.Action = strings.TrimSpace(request.Action)
	request.Reason = strings.TrimSpace(request.Reason)
	request.BusinessRef = strings.TrimSpace(request.BusinessRef)
	if request.ActionKey == "" || len(request.ActionKey) > 180 || request.Reason == "" || len(request.Reason) > 512 || len(request.BusinessRef) > 180 ||
		(request.Action != "note" && request.Action != "request_query" && request.Action != "record_reference") || (request.Action != "note" && request.BusinessRef == "") {
		reconciliationHTTPError(c, http.StatusBadRequest, "处理记录参数无效")
		return
	}
	entry, err := model.RecordPointReconciliationAction(model.RecordPointReconciliationActionRequest{ActionKey: request.ActionKey, DifferenceID: uint(id), ActorUserID: c.GetInt("id"), Action: request.Action, Reason: request.Reason, BusinessRef: request.BusinessRef})
	if errors.Is(err, model.ErrReconciliationConflict) {
		reconciliationHTTPError(c, http.StatusConflict, "该业务键已用于不同的处理记录")
		return
	}
	if err != nil {
		reconciliationHTTPError(c, http.StatusInternalServerError, "处理记录暂时无法保存")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"action": toReconciliationActionDTO(entry)})
}

func reconciliationDifferenceExists(id uint, c *gin.Context) bool {
	var count int64
	if err := model.DB.Model(&model.PointReconciliationDifference{}).Where("id = ?", id).Count(&count).Error; err != nil {
		reconciliationHTTPError(c, http.StatusInternalServerError, "差异记录暂时无法读取")
		return false
	}
	if count == 0 {
		reconciliationHTTPError(c, http.StatusNotFound, "差异记录不存在")
		return false
	}
	return true
}

func reconciliationSchemaReady(c *gin.Context) bool {
	if err := model.RequirePointsSchema(); err != nil {
		reconciliationHTTPError(c, http.StatusServiceUnavailable, "对账数据尚未就绪")
		return false
	}
	return true
}
func reconciliationHTTPError(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": message})
}
func reconciliationLimit(raw string) (int, error) {
	if raw == "" {
		return 30, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 100 {
		return 0, errors.New("invalid limit")
	}
	return n, nil
}
func reconciliationCursor(raw string) (uint, error) {
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint(n), nil
}
func validReconciliationDate(raw string) bool {
	parsed, err := time.Parse("2006-01-02", raw)
	return err == nil && parsed.Format("2006-01-02") == raw
}

func validReconciliationHTTPClassification(value string) bool {
	switch value {
	case "other_scope", "missing_local", "missing_provider", "duplicate", "identity_mismatch", "amount_mismatch", "state_mismatch", "historical_processing":
		return true
	default:
		return false
	}
}
func newReconciliationRequestKey() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw[:])
	return hex.EncodeToString(digest[:]), nil
}
func reconciliationImportErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, payments.ErrReconciliationSourceKeyUnavailable):
		return "source_key_unconfigured"
	case errors.Is(err, payments.ErrReconciliationProviderUnavailable):
		return "provider_unconfigured"
	case errors.Is(err, payments.ErrReconciliationImportBusy):
		return "import_busy"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, model.ErrReconciliationDisabled):
		return "source_key_unconfigured"
	default:
		return "download_or_import_failed"
	}
}
func providerReconciliationIdentity(name string) payments.MerchantIdentity {
	runtime, ok := payments.ProviderFor(name)
	if !ok {
		return payments.MerchantIdentity{}
	}
	return runtime.Identity
}
func toReconciliationBatchDTO(row model.PointReconciliationBatchView) reconciliationBatchDTO {
	return reconciliationBatchDTO{ID: row.ID, BatchKey: row.BatchKey, Provider: row.Provider, MerchantID: row.MerchantID, AppID: row.AppID, BillDate: row.BillDate, BillType: row.BillType, FormatVersion: row.FormatVersion, ImportVersion: row.ImportVersion, Timezone: row.Timezone, Status: row.Status, SourceSHA256: row.SourceSHA256, ProviderHashType: row.ProviderHashType, ProviderHashVerified: row.ProviderHashVerified, SourceSize: row.SourceSize, RowCount: row.RowCount, CreatedAt: row.CreatedAt}
}
func toReconciliationRowDTO(row model.PointReconciliationRow) reconciliationRowDTO {
	return reconciliationRowDTO{ID: row.ID, SourceLine: row.SourceLine, SourceDigest: row.SourceDigest, Provider: row.Provider, Kind: row.Kind, ProviderStatus: row.ProviderStatus, RefundStatus: row.RefundStatus, OccurredAt: row.OccurredAt, MerchantID: row.MerchantID, AppID: row.AppID, OrderKey: row.OrderKey, TransactionID: row.TransactionID, MerchantRefundKey: row.MerchantRefundKey, ProviderRefundID: row.ProviderRefundID, Currency: row.Currency, GrossFen: row.GrossFen, SettlementFen: row.SettlementFen, DiscountFen: row.DiscountFen, RefundFen: row.RefundFen, CouponRefundFen: row.CouponRefundFen, RefundRequestedFen: row.RefundRequestedFen, FeeFen: row.FeeFen, NetFen: row.NetFen, HasNetFen: row.HasNetFen, Result: row.ReconciliationResult}
}
func toReconciliationDifferenceDTO(row model.PointReconciliationDifference) reconciliationDifferenceDTO {
	return reconciliationDifferenceDTO{ID: row.ID, DifferenceKey: row.DifferenceKey, BatchID: row.BatchID, RowID: row.RowID, LocalOrderID: row.LocalOrderID, LocalRefundID: row.LocalRefundID, PaymentEventID: row.PaymentEventID, Classification: row.Classification, Severity: row.Severity, EvidenceFingerprint: row.EvidenceFingerprint, CreatedAt: row.CreatedAt}
}
func toReconciliationActionDTO(row model.PointReconciliationAction) reconciliationActionDTO {
	return reconciliationActionDTO{ID: row.ID, DifferenceID: row.DifferenceID, ActorUserID: row.ActorUserID, Action: row.Action, Reason: row.Reason, BusinessRef: row.BusinessRef, CreatedAt: row.CreatedAt}
}
func toReconciliationImportAuditDTO(row model.PointReconciliationImportAudit) reconciliationImportAuditDTO {
	return reconciliationImportAuditDTO{ID: row.ID, RequestKey: row.RequestKey, Phase: row.Phase, ActorUserID: row.ActorUserID, Provider: row.Provider, BillDate: row.BillDate, MerchantID: row.MerchantID, AppID: row.AppID, BatchID: row.BatchID, SourceSHA256: row.SourceSHA256, Outcome: row.Outcome, ErrorCode: row.ErrorCode, CreatedAt: row.CreatedAt}
}

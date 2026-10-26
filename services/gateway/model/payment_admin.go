package model

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

type AdminPurchaseOrderFilter struct {
	BeforeID uint
	Limit    int
	OrderKey string
	UserID   int
	Channel  string
	State    string
	From     *time.Time
	To       *time.Time
}

func ListPurchaseOrdersForAdmin(filter AdminPurchaseOrderFilter) ([]PointPurchaseOrder, error) {
	if filter.Limit < 1 || filter.Limit > 101 {
		filter.Limit = 31
	}
	query := DB.Model(&PointPurchaseOrder{})
	if filter.BeforeID > 0 {
		query = query.Where("id < ?", filter.BeforeID)
	}
	if filter.OrderKey != "" {
		query = query.Where("order_key = ?", filter.OrderKey)
	}
	if filter.UserID > 0 {
		query = query.Where("user_id = ?", filter.UserID)
	}
	if filter.Channel != "" {
		query = query.Where("channel = ?", filter.Channel)
	}
	if filter.State != "" {
		query = query.Where("state = ?", filter.State)
	}
	if filter.From != nil {
		query = query.Where("created_at >= ?", filter.From.UTC())
	}
	if filter.To != nil {
		query = query.Where("created_at <= ?", filter.To.UTC())
	}
	var rows []PointPurchaseOrder
	err := query.Order("id DESC").Limit(filter.Limit).Find(&rows).Error
	return rows, err
}

func GetPurchaseOrderForAdmin(orderKey string) (PointPurchaseOrder, error) {
	var order PointPurchaseOrder
	err := DB.Where("order_key = ?", orderKey).First(&order).Error
	return order, err
}

func GetPurchaseOrderCreditLedger(order PointPurchaseOrder) (*PointLedger, error) {
	var ledger PointLedger
	err := DB.Where("user_id = ? AND kind = ? AND business_key = ?", order.UserID, "purchase_credit", "credit:"+order.OrderKey).Order("id ASC").First(&ledger).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ledger, nil
}

func ListPurchaseOrderEvents(orderKey string, beforeID uint, limit int) ([]PaymentEvent, error) {
	if limit < 1 || limit > 101 {
		limit = 21
	}
	query := DB.Model(&PaymentEvent{}).Where("order_key = ?", orderKey)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	var rows []PaymentEvent
	err := query.Order("id DESC").Limit(limit).Find(&rows).Error
	return rows, err
}

type AdminPointAuditFilter struct {
	BeforeID   uint
	Limit      int
	ActorID    *int
	Action     string
	From       *time.Time
	To         *time.Time
	OrderKey   string
	RequestKey string
}

type PointAuditLinks struct {
	OrderKey   string
	RequestKey string
}

type PointAdminAuditPage struct {
	Rows             []PointAdminAudit
	ScannedThroughID uint
	ScanLimitReached bool
}

type pointAuditPackageDetails struct {
	PackageID string `json:"package_id"`
	Version   string `json:"version"`
}
type pointAuditPriceDetails struct {
	ModelID string `json:"model_id"`
	Version string `json:"version"`
}
type pointAuditGrantDetails struct {
	AmountMicro int64 `json:"amount_micro"`
}
type pointAuditRequestDetails struct {
	LogicalRequestKey string `json:"logical_request_key"`
	DecisionKey       string `json:"decision_key"`
}
type pointAuditPaymentDetails struct {
	OrderKey string `json:"order_key"`
}

func PointAdminAuditLinks(audit PointAdminAudit) (PointAuditLinks, error) {
	var links PointAuditLinks
	switch audit.Action {
	case "offline_hold_recovery":
		var details pointAuditRequestDetails
		if json.Unmarshal([]byte(audit.Details), &details) == nil {
			links.RequestKey = details.LogicalRequestKey
		}
	case "hold_resolution":
		var details pointAuditRequestDetails
		if json.Unmarshal([]byte(audit.Details), &details) == nil && details.DecisionKey != "" {
			var decision PointHoldDecision
			if err := DB.Where("decision_key = ?", details.DecisionKey).First(&decision).Error; err == nil {
				var hold PointHold
				if err := DB.Select("logical_request_key").First(&hold, decision.HoldID).Error; err == nil {
					links.RequestKey = hold.LogicalRequestKey
				} else if !errors.Is(err, gorm.ErrRecordNotFound) {
					return PointAuditLinks{}, err
				}
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return PointAuditLinks{}, err
			}
		}
	case "payment_event_quarantined", "late_payment_after_close":
		var details pointAuditPaymentDetails
		if json.Unmarshal([]byte(audit.Details), &details) == nil {
			links.OrderKey = details.OrderKey
		}
	}
	return links, nil
}

func ListPointAdminAudits(filter AdminPointAuditFilter) (PointAdminAuditPage, error) {
	if filter.Limit < 1 || filter.Limit > 101 {
		filter.Limit = 31
	}
	query := DB.Model(&PointAdminAudit{})
	if filter.BeforeID > 0 {
		query = query.Where("id < ?", filter.BeforeID)
	}
	if filter.ActorID != nil {
		query = query.Where("actor_user_id = ?", *filter.ActorID)
	}
	if filter.Action != "" {
		query = query.Where("action = ?", filter.Action)
	}
	if filter.From != nil {
		query = query.Where("created_at >= ?", filter.From.UTC())
	}
	if filter.To != nil {
		query = query.Where("created_at <= ?", filter.To.UTC())
	}

	// Association filters are resolved only from known action DTOs. Scan in
	// descending ID windows and apply the requested page after association
	// filtering so matches beyond the first database page remain reachable.
	page := PointAdminAuditPage{Rows: []PointAdminAudit{}}
	scanBefore := filter.BeforeID
	scanCapped := false
	for scanned := 0; scanned < 10000 && len(page.Rows) < filter.Limit; scanned += 100 {
		window := query
		if scanBefore > 0 {
			window = window.Where("id < ?", scanBefore)
		}
		var rows []PointAdminAudit
		if err := window.Order("id DESC").Limit(100).Find(&rows).Error; err != nil {
			return PointAdminAuditPage{}, err
		}
		if len(rows) == 0 {
			break
		}
		for _, audit := range rows {
			links, err := PointAdminAuditLinks(audit)
			if err != nil {
				return PointAdminAuditPage{}, err
			}
			if filter.OrderKey != "" && links.OrderKey != filter.OrderKey {
				continue
			}
			if filter.RequestKey != "" && links.RequestKey != filter.RequestKey {
				continue
			}
			page.Rows = append(page.Rows, audit)
			if len(page.Rows) == filter.Limit {
				break
			}
		}
		scanBefore = rows[len(rows)-1].ID
		page.ScannedThroughID = scanBefore
		if len(rows) < 100 {
			break
		}
		if scanned+100 >= 10000 {
			scanCapped = true
		}
	}
	if scanCapped && len(page.Rows) < filter.Limit && page.ScannedThroughID > 0 {
		// The scan cap bounds pathological sparse association searches. Confirm
		// older candidates exist before returning a continuation cursor.
		var remaining int64
		if err := query.Where("id < ?", page.ScannedThroughID).Count(&remaining).Error; err != nil {
			return PointAdminAuditPage{}, err
		}
		page.ScanLimitReached = remaining > 0
	}
	return page, nil
}

func IsKnownPointAuditAction(action string) bool {
	switch strings.TrimSpace(action) {
	case "package_publish", "price_publish", "points_grant", "offline_hold_recovery", "hold_resolution", "payment_event_quarantined", "late_payment_after_close":
		return true
	default:
		return false
	}
}

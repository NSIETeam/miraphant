package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrPaymentPackageUnavailable = errors.New("payment package is unavailable")
var ErrPaymentOperationPending = errors.New("payment provider operation is already in progress")

// WithPaymentTransaction exposes the same bounded transaction/retry policy as
// the points ledger for payment event state transitions. Callers must not do
// provider network I/O inside fn.
func WithPaymentTransaction(fn func(*gorm.DB) error) error {
	if fn == nil {
		return errors.New("payment transaction callback is required")
	}
	return pointsTransaction(fn)
}

type PointPackageSnapshot struct {
	PackageID         string `json:"package_id"`
	Version           string `json:"version"`
	Name              string `json:"name"`
	AmountFen         int64  `json:"amount_fen"`
	Currency          string `json:"currency"`
	PurchaseMicro     int64  `json:"purchase_micro"`
	BonusMicro        int64  `json:"bonus_micro"`
	BonusValiditySecs int64  `json:"bonus_validity_secs"`
}

func packageSnapshot(p PointPackage) PointPackageSnapshot {
	return PointPackageSnapshot{
		PackageID: p.PackageID, Version: p.Version, Name: p.Name,
		AmountFen: p.AmountFen, Currency: p.Currency,
		PurchaseMicro: p.PurchaseMicro, BonusMicro: p.BonusMicro,
		BonusValiditySecs: p.BonusValiditySecs,
	}
}

func validatePointPackage(p *PointPackage) error {
	if p == nil || p.PackageID == "" || p.Version == "" || p.Name == "" || p.AmountFen <= 0 || p.Currency != "CNY" || p.BonusMicro < 0 || p.BonusValiditySecs < 0 {
		return errors.New("invalid point package")
	}
	if p.AmountFen > int64(^uint64(0)>>1)/PointMicroPerPoint || p.PurchaseMicro != p.AmountFen*PointMicroPerPoint {
		return errors.New("purchase points must equal the CNY fen snapshot at 100 points per CNY")
	}
	if p.BonusMicro == 0 && p.BonusValiditySecs != 0 {
		return errors.New("bonus validity requires a bonus amount")
	}
	if p.BonusMicro > 0 && p.BonusValiditySecs == 0 {
		return errors.New("bonus points require an explicit expiry")
	}
	return nil
}

// CreatePointPackageVersion stores immutable terms and optionally activates
// that version for new orders. Replaying the same business key/terms succeeds;
// reusing it with different terms conflicts.
func CreatePointPackageVersion(p *PointPackage, actorID int, businessKey string, activate bool) error {
	if err := validatePointPackage(p); err != nil {
		return err
	}
	if actorID <= 0 || businessKey == "" {
		return errors.New("actor and audit business key are required")
	}
	return pointsTransaction(func(tx *gorm.DB) error {
		var prior PointPackage
		err := tx.Where("package_id = ? AND version = ?", p.PackageID, p.Version).First(&prior).Error
		if err == nil {
			if packageSnapshot(prior) != packageSnapshot(*p) {
				return ErrPointsConflict
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		} else {
			copy := *p
			copy.Published = true
			copy.CreatedBy = actorID
			if err := tx.Create(&copy).Error; err != nil {
				return err
			}
		}
		details, _ := json.Marshal(map[string]any{"package_id": p.PackageID, "version": p.Version, "activate": activate})
		auditKey := "package_publish:" + businessKey
		var priorAudit PointAdminAudit
		err = tx.Where("business_key = ?", auditKey).First(&priorAudit).Error
		if err == nil {
			if priorAudit.ActorUserID != actorID || priorAudit.Action != "package_publish" || priorAudit.Details != string(details) {
				return ErrPointsConflict
			}
			// The key represents the complete publication operation, including
			// activation. Replaying an old publication must not roll back a newer
			// active package version.
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		} else if err := tx.Create(&PointAdminAudit{ActorUserID: actorID, Action: "package_publish", BusinessKey: auditKey, Details: string(details)}).Error; err != nil {
			return err
		}
		if activate {
			active := PointActivePackage{PackageID: p.PackageID, Version: p.Version, UpdatedBy: actorID}
			result := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "package_id"}}, DoUpdates: clause.Assignments(map[string]any{"version": p.Version, "updated_by": actorID, "updated_at": time.Now().UTC()})}).Create(&active)
			return result.Error
		}
		return nil
	})
}

func ListActivePointPackages() ([]PointPackage, error) {
	var active []PointActivePackage
	if err := DB.Order("package_id ASC").Find(&active).Error; err != nil {
		return nil, err
	}
	if len(active) == 0 {
		return []PointPackage{}, nil
	}
	type pair struct{ PackageID, Version string }
	pairs := make([]pair, 0, len(active))
	for _, item := range active {
		pairs = append(pairs, pair{PackageID: item.PackageID, Version: item.Version})
	}
	var packages []PointPackage
	for _, item := range pairs {
		var p PointPackage
		if err := DB.Where("package_id = ? AND version = ? AND published = ?", item.PackageID, item.Version, true).First(&p).Error; err != nil {
			return nil, err
		}
		packages = append(packages, p)
	}
	return packages, nil
}

type CreatePointPurchaseOrderRequest struct {
	UserID         int
	PackageID      string
	Channel        string
	MerchantID     string
	AppID          string
	IdempotencyKey string
	ExpiresIn      time.Duration
}

func sameCreateOrderRequest(order PointPurchaseOrder, req CreatePointPurchaseOrderRequest) bool {
	return order.UserID == req.UserID && order.PackageID == req.PackageID && order.Channel == req.Channel && order.IdempotencyKey != nil && *order.IdempotencyKey == req.IdempotencyKey
}

// CreatePointPurchaseOrder snapshots server-published package terms before
// contacting any provider. A repeated user idempotency key returns the same
// order only when every client-controlled argument matches.
func CreatePointPurchaseOrder(req CreatePointPurchaseOrderRequest) (PointPurchaseOrder, error) {
	if req.UserID <= 0 || req.PackageID == "" || req.IdempotencyKey == "" || req.MerchantID == "" || req.AppID == "" {
		return PointPurchaseOrder{}, errors.New("user, package, provider identity, and idempotency key are required")
	}
	if req.Channel != "wechat" && req.Channel != "alipay" {
		return PointPurchaseOrder{}, errors.New("unsupported payment channel")
	}
	if req.ExpiresIn <= 0 || req.ExpiresIn > 30*time.Minute {
		req.ExpiresIn = 15 * time.Minute
	}
	orderKey := strings.ReplaceAll(uuid.NewString(), "-", "")
	idempotencyKey := req.IdempotencyKey
	var result PointPurchaseOrder
	err := pointsTransaction(func(tx *gorm.DB) error {
		var old PointPurchaseOrder
		err := tx.Where("user_id = ? AND idempotency_key = ?", req.UserID, req.IdempotencyKey).First(&old).Error
		if err == nil {
			if !sameCreateOrderRequest(old, req) {
				return ErrPointsConflict
			}
			result = old
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var active PointActivePackage
		if err := tx.Where("package_id = ?", req.PackageID).First(&active).Error; err != nil {
			return ErrPaymentPackageUnavailable
		}
		var pkg PointPackage
		if err := tx.Where("package_id = ? AND version = ? AND published = ?", active.PackageID, active.Version, true).First(&pkg).Error; err != nil {
			return ErrPaymentPackageUnavailable
		}
		if err := validatePointPackage(&pkg); err != nil {
			return fmt.Errorf("invalid published package: %w", err)
		}
		snapshot, err := json.Marshal(packageSnapshot(pkg))
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		expires := now.Add(req.ExpiresIn).Unix()
		result = PointPurchaseOrder{
			OrderKey: orderKey, UserID: req.UserID, IdempotencyKey: &idempotencyKey,
			Channel: req.Channel, ProviderMerchantID: req.MerchantID, ProviderAppID: req.AppID, PackageID: pkg.PackageID, PackageVersion: pkg.Version,
			PackageSnapshot: string(snapshot), Currency: pkg.Currency,
			AmountFen: pkg.AmountFen, PurchaseMicro: pkg.PurchaseMicro,
			BonusMicro: pkg.BonusMicro, BonusValiditySecs: pkg.BonusValiditySecs, ExpiresAt: &expires, State: "created", ProviderCreateState: "new",
		}
		return tx.Create(&result).Error
	})
	if err != nil {
		// A concurrent request may have won the unique idempotency insert.
		var old PointPurchaseOrder
		if lookupErr := DB.Where("user_id = ? AND idempotency_key = ?", req.UserID, req.IdempotencyKey).First(&old).Error; lookupErr == nil {
			if sameCreateOrderRequest(old, req) {
				return old, nil
			}
			return PointPurchaseOrder{}, ErrPointsConflict
		}
		return PointPurchaseOrder{}, err
	}
	return result, nil
}

func GetPointPurchaseOrderForUser(userID int, orderKey string) (PointPurchaseOrder, error) {
	var order PointPurchaseOrder
	err := DB.Where("user_id = ? AND order_key = ?", userID, orderKey).First(&order).Error
	return order, err
}

func GetPointPurchaseOrderByIdempotency(userID int, key string) (PointPurchaseOrder, error) {
	var order PointPurchaseOrder
	err := DB.Where("user_id = ? AND idempotency_key = ?", userID, key).First(&order).Error
	return order, err
}

func ListPointPurchaseOrdersForUser(userID int, limit int) ([]PointPurchaseOrder, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	var orders []PointPurchaseOrder
	err := DB.Where("user_id = ?", userID).Order("id DESC").Limit(limit).Find(&orders).Error
	return orders, err
}

// ListPointPurchaseOrdersPage uses a descending database ID cursor so customer
// order history remains complete without offset scans or a fixed first page.
func ListPointPurchaseOrdersPage(userID int, beforeID uint, limit int) ([]PointPurchaseOrder, error) {
	// The HTTP layer asks for one extra row to determine whether a cursor is
	// available, so allow a limit of 101 here (100 visible + 1 probe).
	if limit <= 0 || limit > 101 {
		limit = 30
	}
	query := DB.Where("user_id = ?", userID)
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	var orders []PointPurchaseOrder
	err := query.Order("id DESC").Limit(limit).Find(&orders).Error
	return orders, err
}

type PointPackageAdminRecord struct {
	Package PointPackage
	Active  bool
	Audit   *PointAdminAudit
}

// ListPointPackagesForAdmin returns immutable published versions, their active
// status, and the append-only publication audit entry for traceability.
func ListPointPackagesForAdmin() ([]PointPackageAdminRecord, error) {
	var packages []PointPackage
	if err := DB.Where("published = ?", true).Order("package_id ASC, created_at DESC, version DESC").Find(&packages).Error; err != nil {
		return nil, err
	}
	var activeRows []PointActivePackage
	if err := DB.Find(&activeRows).Error; err != nil {
		return nil, err
	}
	active := make(map[string]string, len(activeRows))
	for _, row := range activeRows {
		active[row.PackageID] = row.Version
	}
	var audits []PointAdminAudit
	if err := DB.Where("action = ?", "package_publish").Order("id DESC").Find(&audits).Error; err != nil {
		return nil, err
	}
	auditByVersion := make(map[string]PointAdminAudit, len(audits))
	for _, audit := range audits {
		var details struct {
			PackageID string `json:"package_id"`
			Version   string `json:"version"`
		}
		if json.Unmarshal([]byte(audit.Details), &details) == nil && details.PackageID != "" && details.Version != "" {
			key := details.PackageID + "\x00" + details.Version
			if _, exists := auditByVersion[key]; !exists {
				auditByVersion[key] = audit
			}
		}
	}
	records := make([]PointPackageAdminRecord, 0, len(packages))
	for _, pkg := range packages {
		record := PointPackageAdminRecord{Package: pkg, Active: active[pkg.PackageID] == pkg.Version}
		if audit, exists := auditByVersion[pkg.PackageID+"\x00"+pkg.Version]; exists {
			record.Audit = &audit
		}
		records = append(records, record)
	}
	return records, nil
}

func MarkPointPurchaseOrderPending(orderKey string) error {
	return pointsTransaction(func(tx *gorm.DB) error {
		var order PointPurchaseOrder
		if err := tx.Where("order_key = ?", orderKey).First(&order).Error; err != nil {
			return err
		}
		if order.State == "pending" || order.State == "credited" || order.State == "closed" {
			return nil
		}
		if order.State != "created" {
			return ErrPointsConflict
		}
		result := tx.Model(&PointPurchaseOrder{}).Where("id = ? AND state = ?", order.ID, "created").Update("state", "pending")
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPointsConflict
		}
		order.State = "pending"
		return nil
	})
}

// BeginPointProviderCreate leases a provider create attempt. A stale previous
// attempt may only be retried after the caller has queried the same frozen
// merchant order and confirmed it remains unpaid.
func BeginPointProviderCreate(orderKey string, staleRetry bool) (PointPurchaseOrder, error) {
	var result PointPurchaseOrder
	err := pointsTransaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_key = ?", orderKey).First(&result).Error; err != nil {
			return err
		}
		if result.State != "pending" {
			return ErrPointsConflict
		}
		if result.CheckoutSnapshot != "" && result.ProviderCreateState == "ready" {
			return ErrPointsConflict
		}
		now := time.Now().UTC().Unix()
		if result.ProviderCreateState == "started" {
			if !staleRetry || result.ProviderCreateStartedAt == nil || now-*result.ProviderCreateStartedAt < 30 {
				return ErrPaymentOperationPending
			}
		} else if result.ProviderCreateState != "" && result.ProviderCreateState != "new" {
			return ErrPointsConflict
		}
		result.ProviderCreateState = "started"
		result.ProviderCreateStartedAt = &now
		update := tx.Model(&PointPurchaseOrder{}).Where("id = ? AND state = ?", result.ID, "pending").Updates(map[string]any{"provider_create_state": "started", "provider_create_started_at": now})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrPointsConflict
		}
		return nil
	})
	return result, err
}

func SavePointPurchaseCheckout(orderKey string, checkout []byte) error {
	if len(checkout) == 0 || len(checkout) > 64*1024 {
		return errors.New("invalid checkout snapshot")
	}
	return pointsTransaction(func(tx *gorm.DB) error {
		var order PointPurchaseOrder
		if err := tx.Where("order_key = ?", orderKey).First(&order).Error; err != nil {
			return err
		}
		if order.ProviderCreateState == "ready" && order.CheckoutSnapshot != "" {
			if order.CheckoutSnapshot != string(checkout) {
				return ErrPointsConflict
			}
			return nil
		}
		if order.State != "pending" || order.ProviderCreateState != "started" {
			return ErrPointsConflict
		}
		result := tx.Model(&PointPurchaseOrder{}).Where("id = ? AND state = ? AND provider_create_state = ?", order.ID, "pending", "started").Updates(map[string]any{"provider_create_state": "ready", "checkout_snapshot": string(checkout)})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPointsConflict
		}
		return nil
	})
}

func MarkPointPurchaseOrderClosed(orderKey, reason string) error {
	return pointsTransaction(func(tx *gorm.DB) error {
		result := tx.Model(&PointPurchaseOrder{}).Where("order_key = ? AND state IN ?", orderKey, []string{"created", "pending"}).Updates(map[string]any{"state": "closed", "closed_reason": reason})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		var order PointPurchaseOrder
		if err := tx.Where("order_key = ?", orderKey).First(&order).Error; err != nil {
			return err
		}
		if order.State == "closed" {
			return nil
		}
		return ErrPointsConflict
	})
}

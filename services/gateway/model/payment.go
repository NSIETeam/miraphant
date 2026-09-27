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
	if req.UserID <= 0 || req.PackageID == "" || req.IdempotencyKey == "" {
		return PointPurchaseOrder{}, errors.New("user, package, and idempotency key are required")
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
			Channel: req.Channel, PackageID: pkg.PackageID, PackageVersion: pkg.Version,
			PackageSnapshot: string(snapshot), Currency: pkg.Currency,
			AmountFen: pkg.AmountFen, PurchaseMicro: pkg.PurchaseMicro,
			BonusMicro: pkg.BonusMicro, BonusValiditySecs: pkg.BonusValiditySecs, ExpiresAt: &expires, State: "created",
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

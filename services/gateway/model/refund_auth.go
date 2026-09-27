package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"gorm.io/gorm"
)

const (
	RefundCapabilityRead      = "refund.read"
	RefundCapabilityReview    = "refund.review"
	RefundCapabilitySubmit    = "refund.submit"
	RefundCapabilityReconcile = "refund.reconcile"
	RefundCapabilityAudit     = "refund.audit"
	RefundCapabilityManage    = "capability.manage"

	RefundStepUpGrant  = "capability.grant"
	RefundStepUpRevoke = "capability.revoke"
)

var delegatedRefundCapabilities = map[string]struct{}{
	RefundCapabilityRead: {}, RefundCapabilityReview: {}, RefundCapabilitySubmit: {},
	RefundCapabilityReconcile: {}, RefundCapabilityAudit: {},
}

var ErrRefundAuthUnavailable = errors.New("refund authorization is unavailable")
var ErrRefundStepUpInvalid = errors.New("step-up verification failed or expired")
var ErrRefundCapabilityDenied = errors.New("refund capability denied")
var ErrRefundAuthConflict = errors.New("refund authorization request conflicts with current state")

// RefundCapabilityGrant is the current delegated capability state. Root is
// resolved from the exact role value and is never materialized as a grant.
type RefundCapabilityGrant struct {
	ID               uint      `gorm:"primaryKey"`
	UserID           int       `gorm:"not null;uniqueIndex:idx_refund_capability_user"`
	Capability       string    `gorm:"size:48;not null;uniqueIndex:idx_refund_capability_user"`
	GrantedBy        int       `gorm:"not null"`
	CredentialDigest string    `json:"-" gorm:"size:64;not null;default:''"`
	CredentialEpoch  int64     `json:"-" gorm:"not null;default:0"`
	GrantedAt        time.Time `gorm:"not null"`
	RevokedAt        *time.Time
	UpdatedAt        time.Time
}

// RefundAuthorizationAudit intentionally contains only bounded administrative
// metadata; it never stores credentials, tickets, or provider payloads.
type RefundAuthorizationAudit struct {
	ID            uint      `gorm:"primaryKey"`
	ActorUserID   int       `gorm:"not null;index"`
	TargetUserID  int       `gorm:"not null;index"`
	Action        string    `gorm:"size:32;not null;index"`
	Capabilities  string    `gorm:"type:text;not null"`
	Reason        string    `gorm:"size:256;not null"`
	RequestDigest string    `gorm:"size:64;not null"`
	CreatedAt     time.Time `gorm:"not null;index"`
}

func (*RefundAuthorizationAudit) BeforeUpdate(*gorm.DB) error {
	return errors.New("refund authorization audit is append-only")
}
func (*RefundAuthorizationAudit) BeforeDelete(*gorm.DB) error {
	return errors.New("refund authorization audit is append-only")
}

// RefundStepUpTicket persists only a digest of the one-time bearer value.
// The session and password digests invalidate it after logout/relogin or a
// password change/reset. ScopeDigest binds the ticket to one exact action.
type RefundStepUpTicket struct {
	ID              uint       `json:"-" gorm:"primaryKey"`
	TokenDigest     string     `json:"-" gorm:"size:64;not null;uniqueIndex"`
	ActorUserID     int        `json:"-" gorm:"not null;index"`
	ActorAuthEpoch  int64      `json:"-" gorm:"not null;default:1"`
	TargetAuthEpoch int64      `json:"-" gorm:"not null;default:0"`
	SessionDigest   string     `json:"-" gorm:"size:64;not null"`
	PasswordDigest  string     `json:"-" gorm:"size:64;not null"`
	ScopeDigest     string     `json:"-" gorm:"size:64;not null"`
	ExpiresAt       time.Time  `json:"-" gorm:"not null;index"`
	ConsumedAt      *time.Time `json:"-"`
	CreatedAt       time.Time  `json:"-" gorm:"not null"`
}

func (t *RefundStepUpTicket) BeforeUpdate(tx *gorm.DB) error {
	values, ok := tx.Statement.Dest.(map[string]interface{})
	if !ok || len(values) != 1 || t.ID == 0 || t.ConsumedAt != nil {
		return errors.New("step-up tickets may only be consumed once")
	}
	for key := range values {
		if strings.ToLower(key) != "consumed_at" {
			return errors.New("step-up tickets may only be consumed once")
		}
	}
	if values["consumed_at"] == nil {
		return errors.New("step-up tickets cannot be unconsumed")
	}
	return nil
}
func (*RefundStepUpTicket) BeforeDelete(*gorm.DB) error {
	return errors.New("step-up tickets cannot be deleted")
}

type RefundStepUpScope struct {
	Action       string   `json:"action"`
	TargetUserID int      `json:"target_user_id,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	RefundKey    string   `json:"refund_key,omitempty"`
	BusinessKey  string   `json:"business_key,omitempty"`
	AmountFen    int64    `json:"amount_fen,omitempty"`
}

func normalizeRefundStepUpScope(scope RefundStepUpScope) (RefundStepUpScope, error) {
	scope.Action = strings.TrimSpace(scope.Action)
	scope.Reason = strings.TrimSpace(scope.Reason)
	maxReasonBytes := 256
	if scope.Action == "refund.approve" || scope.Action == "refund.reject" || scope.Action == "refund.submit" {
		maxReasonBytes = 512
	}
	if len([]byte(scope.Reason)) > maxReasonBytes {
		return RefundStepUpScope{}, ErrRefundAuthConflict
	}
	switch scope.Action {
	case RefundStepUpGrant, RefundStepUpRevoke:
		if scope.TargetUserID <= 0 || len(scope.Capabilities) == 0 || scope.Reason == "" || scope.RefundKey != "" || scope.BusinessKey != "" || scope.AmountFen != 0 {
			return RefundStepUpScope{}, ErrRefundAuthConflict
		}
		seen := make(map[string]struct{}, len(scope.Capabilities))
		for _, capability := range scope.Capabilities {
			if _, ok := delegatedRefundCapabilities[capability]; !ok {
				return RefundStepUpScope{}, ErrRefundAuthConflict
			}
			if _, duplicate := seen[capability]; duplicate {
				return RefundStepUpScope{}, ErrRefundAuthConflict
			}
			seen[capability] = struct{}{}
		}
		sort.Strings(scope.Capabilities)
	case "refund.review", "refund.approve", "refund.reject", "refund.submit", "refund.reconcile":
		if scope.TargetUserID != 0 || len(scope.Capabilities) != 0 || scope.RefundKey == "" || len(scope.RefundKey) > 180 || scope.AmountFen <= 0 {
			return RefundStepUpScope{}, ErrRefundAuthConflict
		}
		if scope.Action == "refund.approve" || scope.Action == "refund.reject" || scope.Action == "refund.submit" {
			if scope.BusinessKey == "" || len(scope.BusinessKey) > 180 || scope.Reason == "" || len(scope.Reason) > 512 {
				return RefundStepUpScope{}, ErrRefundAuthConflict
			}
		} else if scope.BusinessKey != "" || scope.Reason != "" {
			return RefundStepUpScope{}, ErrRefundAuthConflict
		}
	default:
		return RefundStepUpScope{}, ErrRefundAuthConflict
	}
	return scope, nil
}

func refundDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func userCredentialDigest(user User) string {
	identity, _ := json.Marshal([]string{user.Password, user.Email, user.GitHubId, user.WeChatId, user.LarkId, user.OidcId})
	return refundDigest(string(identity))
}

func refundScopeDigest(scope RefundStepUpScope) (string, error) {
	canonical, err := json.Marshal(scope)
	if err != nil {
		return "", err
	}
	return refundDigest(string(canonical)), nil
}

func RefundSessionBindingValid(binding string) bool { return len(binding) >= 32 && len(binding) <= 128 }

func HasRefundCapability(userID int, capability string) (bool, error) {
	if userID <= 0 || !isKnownRefundCapability(capability) {
		return false, nil
	}
	var user User
	if err := DB.Select("id", "role", "status", "password", "email", "github_id", "wechat_id", "lark_id", "oidc_id", "refund_auth_epoch").First(&user, "id = ?", userID).Error; err != nil {
		return false, err
	}
	if user.Status != UserStatusEnabled {
		return false, nil
	}
	if user.Role == RoleRootUser {
		return true, nil
	}
	if capability == RefundCapabilityManage {
		return false, nil
	}
	var grant RefundCapabilityGrant
	err := DB.Where("user_id = ? AND capability = ? AND revoked_at IS NULL", userID, capability).First(&grant).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil && grant.CredentialDigest != "" && grant.CredentialDigest == userCredentialDigest(user) && grant.CredentialEpoch == user.RefundAuthEpoch, err
}

func isKnownRefundCapability(capability string) bool {
	if capability == RefundCapabilityManage {
		return true
	}
	_, ok := delegatedRefundCapabilities[capability]
	return ok
}

func RefundCapabilitiesForUser(userID int) ([]string, error) {
	var user User
	if err := DB.Select("id", "role", "status", "password", "email", "github_id", "wechat_id", "lark_id", "oidc_id", "refund_auth_epoch").First(&user, "id = ?", userID).Error; err != nil {
		return nil, err
	}
	if user.Status != UserStatusEnabled {
		return []string{}, nil
	}
	if user.Role == RoleRootUser {
		return []string{RefundCapabilityRead, RefundCapabilityReview, RefundCapabilitySubmit, RefundCapabilityReconcile, RefundCapabilityAudit, RefundCapabilityManage}, nil
	}
	var grants []RefundCapabilityGrant
	if err := DB.Where("user_id = ? AND revoked_at IS NULL", userID).Order("capability ASC").Find(&grants).Error; err != nil {
		return nil, err
	}
	capabilities := make([]string, 0, len(grants))
	for _, grant := range grants {
		if _, ok := delegatedRefundCapabilities[grant.Capability]; ok && grant.CredentialDigest != "" && grant.CredentialDigest == userCredentialDigest(user) && grant.CredentialEpoch == user.RefundAuthEpoch {
			capabilities = append(capabilities, grant.Capability)
		}
	}
	return capabilities, nil
}

func issueRefundStepUpTicket(actorID int, sessionBinding, password string, requested RefundStepUpScope) (string, error) {
	scope, err := normalizeRefundStepUpScope(requested)
	if err != nil || actorID <= 0 || !RefundSessionBindingValid(sessionBinding) || len(password) == 0 || len(password) > 256 {
		return "", ErrRefundStepUpInvalid
	}
	if strings.HasPrefix(scope.Action, "refund.") && !config.PointRefundOperationsEnabled {
		return "", ErrRefundAuthUnavailable
	}
	var tokenBytes [32]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return "", err
	}
	plainToken := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
	scopeHash, err := refundScopeDigest(scope)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	err = DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := tx.Select("id", "role", "status", "password", "refund_auth_epoch").First(&user, "id = ?", actorID).Error; err != nil || user.Status != UserStatusEnabled || user.Password == "" || !common.ValidatePasswordAndHash(password, user.Password) {
			return ErrRefundStepUpInvalid
		}
		targetEpoch := int64(0)
		if scope.Action == RefundStepUpGrant || scope.Action == RefundStepUpRevoke {
			if user.Role != RoleRootUser {
				return ErrRefundCapabilityDenied
			}
			var target User
			if err := tx.Select("id", "status", "role", "password", "email", "github_id", "wechat_id", "lark_id", "oidc_id", "refund_auth_epoch").First(&target, "id = ?", scope.TargetUserID).Error; err != nil || target.Status != UserStatusEnabled || target.Role == RoleRootUser {
				return ErrRefundAuthConflict
			}
			targetEpoch = target.RefundAuthEpoch
		} else {
			var refund PointRefund
			if err := tx.First(&refund, "refund_key = ?", scope.RefundKey).Error; err != nil || refund.AmountFen != scope.AmountFen || !refundActionAllowedTx(tx, user.Id, scope.Action) {
				return ErrRefundAuthConflict
			}
		}
		ticket := RefundStepUpTicket{TokenDigest: refundDigest(plainToken), ActorUserID: actorID, ActorAuthEpoch: user.RefundAuthEpoch, TargetAuthEpoch: targetEpoch, SessionDigest: refundDigest(sessionBinding), PasswordDigest: refundDigest(user.Password), ScopeDigest: scopeHash, ExpiresAt: now.Add(5 * time.Minute), CreatedAt: now}
		return tx.Create(&ticket).Error
	})
	if err != nil {
		return "", err
	}
	return plainToken, nil
}

func IssueRefundCapabilityStepUpTicket(actorID int, sessionBinding, password, action string, targetUserID int, capabilities []string, reason string) (string, error) {
	if action != RefundStepUpGrant && action != RefundStepUpRevoke {
		return "", ErrRefundAuthConflict
	}
	return issueRefundStepUpTicket(actorID, sessionBinding, password, RefundStepUpScope{Action: action, TargetUserID: targetUserID, Capabilities: capabilities, Reason: reason})
}

func refundActionAllowedTx(tx *gorm.DB, actorID int, action string) bool {
	capability := map[string]string{"refund.review": RefundCapabilityReview, "refund.approve": RefundCapabilityReview, "refund.reject": RefundCapabilityReview, "refund.submit": RefundCapabilitySubmit, "refund.reconcile": RefundCapabilityReconcile}[action]
	if capability == "" {
		return false
	}
	var user User
	if tx.Select("id", "role", "status", "password", "email", "github_id", "wechat_id", "lark_id", "oidc_id", "refund_auth_epoch").First(&user, "id = ?", actorID).Error != nil || user.Status != UserStatusEnabled {
		return false
	}
	if user.Role == RoleRootUser {
		return true
	}
	var grant RefundCapabilityGrant
	if tx.Where("user_id = ? AND capability = ? AND revoked_at IS NULL", actorID, capability).First(&grant).Error != nil {
		return false
	}
	return grant.CredentialDigest != "" && grant.CredentialDigest == userCredentialDigest(user) && grant.CredentialEpoch == user.RefundAuthEpoch
}

// IssueRefundOperationStepUpTicket derives the ticket amount from the persisted
// refund snapshot. Public callers only select the refund and intended action.
func IssueRefundOperationStepUpTicket(actorID int, sessionBinding, password, refundKey, action string) (string, error) {
	if !config.PointRefundOperationsEnabled {
		return "", ErrRefundAuthUnavailable
	}
	var refund PointRefund
	if err := DB.Select("refund_key", "amount_fen").First(&refund, "refund_key = ?", refundKey).Error; err != nil {
		return "", ErrRefundAuthConflict
	}
	scope := RefundStepUpScope{Action: action, RefundKey: refund.RefundKey, AmountFen: refund.AmountFen}
	return issueRefundStepUpTicket(actorID, sessionBinding, password, scope)
}

// IssueRefundDecisionStepUpTicket binds a confirmation to one persisted refund,
// exact action, server-side amount, client idempotency key, and decision reason.
func IssueRefundDecisionStepUpTicket(actorID int, sessionBinding, password, refundKey, action, businessKey, reason string) (string, error) {
	if !config.PointRefundOperationsEnabled {
		return "", ErrRefundAuthUnavailable
	}
	var refund PointRefund
	if err := DB.Select("refund_key", "amount_fen").First(&refund, "refund_key = ?", refundKey).Error; err != nil {
		return "", ErrRefundAuthConflict
	}
	scope := RefundStepUpScope{Action: action, RefundKey: refund.RefundKey, AmountFen: refund.AmountFen, BusinessKey: businessKey, Reason: reason}
	return issueRefundStepUpTicket(actorID, sessionBinding, password, scope)
}

func consumeRefundStepUpTicketTx(tx *gorm.DB, rawTicket string, actorID int, sessionBinding string, scope RefundStepUpScope, now time.Time) error {
	if rawTicket == "" || actorID <= 0 || !RefundSessionBindingValid(sessionBinding) {
		return ErrRefundStepUpInvalid
	}
	normalized, err := normalizeRefundStepUpScope(scope)
	if err != nil {
		return ErrRefundStepUpInvalid
	}
	if strings.HasPrefix(normalized.Action, "refund.") && !config.PointRefundOperationsEnabled {
		return ErrRefundAuthUnavailable
	}
	scopeHash, err := refundScopeDigest(normalized)
	if err != nil {
		return err
	}
	var user User
	if err := tx.Select("id", "role", "status", "password", "refund_auth_epoch").First(&user, "id = ?", actorID).Error; err != nil || user.Status != UserStatusEnabled || user.Password == "" {
		return ErrRefundStepUpInvalid
	}
	if normalized.Action == RefundStepUpGrant || normalized.Action == RefundStepUpRevoke {
		if user.Role != RoleRootUser {
			return ErrRefundCapabilityDenied
		}
		var target User
		if err := tx.Select("id", "status", "role", "password", "email", "github_id", "wechat_id", "lark_id", "oidc_id", "refund_auth_epoch").First(&target, "id = ?", normalized.TargetUserID).Error; err != nil || target.Status != UserStatusEnabled || target.Role == RoleRootUser {
			return ErrRefundAuthConflict
		}
		var ticket RefundStepUpTicket
		if err := tx.Where("token_digest = ? AND actor_user_id = ?", refundDigest(rawTicket), actorID).First(&ticket).Error; err != nil {
			return ErrRefundStepUpInvalid
		}
		if ticket.TargetAuthEpoch != target.RefundAuthEpoch {
			return ErrRefundStepUpInvalid
		}
	} else {
		var refund PointRefund
		if err := tx.First(&refund, "refund_key = ?", normalized.RefundKey).Error; err != nil || refund.AmountFen != normalized.AmountFen {
			return ErrRefundAuthConflict
		}
		if !refundActionAllowedTx(tx, actorID, normalized.Action) {
			return ErrRefundCapabilityDenied
		}
	}
	var ticket RefundStepUpTicket
	if err := tx.Where("token_digest = ? AND actor_user_id = ?", refundDigest(rawTicket), actorID).First(&ticket).Error; err != nil {
		return ErrRefundStepUpInvalid
	}
	if ticket.ConsumedAt != nil || !now.Before(ticket.ExpiresAt) || ticket.ActorAuthEpoch != user.RefundAuthEpoch || ticket.SessionDigest != refundDigest(sessionBinding) || ticket.PasswordDigest != refundDigest(user.Password) || ticket.ScopeDigest != scopeHash {
		return ErrRefundStepUpInvalid
	}
	result := tx.Model(&ticket).Where("id = ? AND consumed_at IS NULL AND expires_at > ?", ticket.ID, now).Updates(map[string]interface{}{"consumed_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrRefundStepUpInvalid
	}
	return nil
}

// ConsumeRefundOperationStepUpTx is intentionally transaction-scoped and has
// no HTTP counterpart. The persisted refund supplies the amount bound into the
// ticket; callers cannot provide a client-selected amount.
func ConsumeRefundOperationStepUpTx(tx *gorm.DB, rawTicket string, actorID int, sessionBinding, refundKey, action string, now time.Time) error {
	var refund PointRefund
	if err := tx.First(&refund, "refund_key = ?", refundKey).Error; err != nil {
		return err
	}
	scope := RefundStepUpScope{Action: action, RefundKey: refund.RefundKey, AmountFen: refund.AmountFen}
	return consumeRefundStepUpTicketTx(tx, rawTicket, actorID, sessionBinding, scope, now.UTC())
}

// ConsumeRefundDecisionStepUpTx is used inside the same transaction as the
// decision/audit write. Amount is always loaded from the immutable refund row.
func ConsumeRefundDecisionStepUpTx(tx *gorm.DB, rawTicket string, actorID int, sessionBinding, refundKey, action, businessKey, reason string, now time.Time) error {
	var refund PointRefund
	if err := tx.First(&refund, "refund_key = ?", refundKey).Error; err != nil {
		return err
	}
	scope := RefundStepUpScope{Action: action, RefundKey: refund.RefundKey, AmountFen: refund.AmountFen, BusinessKey: businessKey, Reason: reason}
	return consumeRefundStepUpTicketTx(tx, rawTicket, actorID, sessionBinding, scope, now.UTC())
}

func ApplyRefundCapabilityChange(actorID int, sessionBinding, rawTicket string, scope RefundStepUpScope) error {
	normalized, err := normalizeRefundStepUpScope(scope)
	if err != nil || (normalized.Action != RefundStepUpGrant && normalized.Action != RefundStepUpRevoke) {
		return ErrRefundAuthConflict
	}
	return DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := consumeRefundStepUpTicketTx(tx, rawTicket, actorID, sessionBinding, normalized, now); err != nil {
			return err
		}
		requestHash, err := refundScopeDigest(normalized)
		if err != nil {
			return err
		}
		var credentialUser User
		if err := tx.Select("id", "password", "email", "github_id", "wechat_id", "lark_id", "oidc_id", "refund_auth_epoch").First(&credentialUser, "id = ?", normalized.TargetUserID).Error; err != nil {
			return err
		}
		credentialDigest := userCredentialDigest(credentialUser)
		newEpoch := credentialUser.RefundAuthEpoch + 1
		bump := tx.Model(&User{}).Where("id = ? AND refund_auth_epoch = ?", normalized.TargetUserID, credentialUser.RefundAuthEpoch).UpdateColumn("refund_auth_epoch", newEpoch)
		if bump.Error != nil {
			return bump.Error
		}
		if bump.RowsAffected != 1 {
			return ErrRefundAuthConflict
		}
		if err := tx.Model(&RefundCapabilityGrant{}).Where("user_id = ? AND revoked_at IS NULL AND credential_epoch = ? AND credential_digest = ?", normalized.TargetUserID, credentialUser.RefundAuthEpoch, credentialDigest).UpdateColumn("credential_epoch", newEpoch).Error; err != nil {
			return err
		}
		for _, capability := range normalized.Capabilities {
			var grant RefundCapabilityGrant
			queryErr := tx.Where("user_id = ? AND capability = ?", normalized.TargetUserID, capability).First(&grant).Error
			if normalized.Action == RefundStepUpGrant {
				if errors.Is(queryErr, gorm.ErrRecordNotFound) {
					grant = RefundCapabilityGrant{UserID: normalized.TargetUserID, Capability: capability, GrantedBy: actorID, CredentialDigest: credentialDigest, CredentialEpoch: newEpoch, GrantedAt: now, UpdatedAt: now}
					if err := tx.Create(&grant).Error; err != nil {
						return err
					}
				} else if queryErr != nil {
					return queryErr
				} else if grant.RevokedAt != nil || grant.CredentialDigest != credentialDigest || grant.CredentialEpoch != newEpoch {
					if err := tx.Model(&grant).Updates(map[string]interface{}{"revoked_at": nil, "granted_by": actorID, "credential_digest": credentialDigest, "credential_epoch": newEpoch, "granted_at": now, "updated_at": now}).Error; err != nil {
						return err
					}
				}
			} else if queryErr == nil && grant.RevokedAt == nil {
				if err := tx.Model(&grant).Updates(map[string]interface{}{"revoked_at": now, "updated_at": now}).Error; err != nil {
					return err
				}
			} else if queryErr != nil && !errors.Is(queryErr, gorm.ErrRecordNotFound) {
				return queryErr
			}
		}
		audit := RefundAuthorizationAudit{ActorUserID: actorID, TargetUserID: normalized.TargetUserID, Action: normalized.Action, Capabilities: strings.Join(normalized.Capabilities, ","), Reason: normalized.Reason, RequestDigest: requestHash, CreatedAt: now}
		if err := tx.Create(&audit).Error; err != nil {
			return err
		}
		return nil
	})
}

func ListRefundCapabilityGrants(targetUserID int) ([]RefundCapabilityGrant, error) {
	if targetUserID <= 0 {
		return nil, ErrRefundAuthConflict
	}
	var target User
	if err := DB.Select("id", "role", "status").First(&target, "id = ?", targetUserID).Error; err != nil {
		return nil, err
	}
	var grants []RefundCapabilityGrant
	err := DB.Where("user_id = ?", targetUserID).Order("capability ASC").Find(&grants).Error
	return grants, err
}

func (s RefundStepUpScope) String() string {
	return fmt.Sprintf("%s:%d", s.Action, s.TargetUserID)
}

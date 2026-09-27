package model

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"gorm.io/gorm"
)

func setupRefundAuthModelTest(t *testing.T) (*gorm.DB, User, User, User) {
	t.Helper()
	oldDB, oldEnabled := DB, config.PointsBillingEnabled
	oldRefundEnabled := config.PointRefundOperationsEnabled
	config.PointsBillingEnabled = true
	config.PointRefundOperationsEnabled = false
	db := openPointsTestDB(t, filepath.Join(t.TempDir(), "refund-auth.db"))
	DB = db
	if err := db.AutoMigrate(&User{}, &RefundCapabilityGrant{}, &RefundAuthorizationAudit{}, &RefundStepUpTicket{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		DB, config.PointsBillingEnabled, config.PointRefundOperationsEnabled = oldDB, oldEnabled, oldRefundEnabled
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	hash, err := common.Password2Hash("local-password-123")
	if err != nil {
		t.Fatal(err)
	}
	root := User{Id: 901, Username: "authroot", Password: hash, Role: RoleRootUser, Status: UserStatusEnabled, AffCode: "authroot", AccessToken: "authroot-access"}
	admin := User{Id: 902, Username: "oldadmin", Password: hash, Role: RoleAdminUser, Status: UserStatusEnabled, AffCode: "oldadmin", AccessToken: "oldadmin-access"}
	finance := User{Id: 903, Username: "finance", Password: hash, Role: RoleCommonUser, Status: UserStatusEnabled, AffCode: "finance", Email: "finance@example.test", AccessToken: "finance-access"}
	oauthOnly := User{Id: 904, Username: "oauthonly", Role: RoleCommonUser, Status: UserStatusEnabled, AffCode: "oauthonly", AccessToken: "oauthonly-access"}
	if err := db.Create(&[]User{root, admin, finance, oauthOnly}).Error; err != nil {
		t.Fatal(err)
	}
	return db, root, admin, finance
}

func authScope(action string, target int, caps ...string) RefundStepUpScope {
	return RefundStepUpScope{Action: action, TargetUserID: target, Capabilities: caps, Reason: "职责变更单 REQ-2026-19"}
}

func getCapabilityTicket(t *testing.T, actor User, scope RefundStepUpScope, session string) string {
	t.Helper()
	return getCapabilityTicketWithPassword(t, actor, scope, session, "local-password-123")
}

func getCapabilityTicketWithPassword(t *testing.T, actor User, scope RefundStepUpScope, session, password string) string {
	t.Helper()
	ticket, err := IssueRefundCapabilityStepUpTicket(actor.Id, session, password, scope.Action, scope.TargetUserID, scope.Capabilities, scope.Reason)
	if err != nil {
		t.Fatalf("issue step-up ticket: %v", err)
	}
	return ticket
}

func TestRefundActorPasswordAndDisableChangesInvalidateTickets(t *testing.T) {
	db, root, _, finance := setupRefundAuthModelTest(t)
	session := "root-session-binding-01234567890123456789"
	scope := authScope(RefundStepUpGrant, finance.Id, RefundCapabilityRead)
	passwordTicket := getCapabilityTicket(t, root, scope, session)
	rootPasswordChange := User{Id: root.Id, Password: "replacement-root-password-789"}
	if err := rootPasswordChange.Update(true); err != nil {
		t.Fatal(err)
	}
	if err := ApplyRefundCapabilityChange(root.Id, session, passwordTicket, scope); !errors.Is(err, ErrRefundStepUpInvalid) {
		t.Fatalf("password change did not invalidate actor ticket: %v", err)
	}
	disableTicket := getCapabilityTicketWithPassword(t, root, scope, session, "replacement-root-password-789")
	var currentRoot User
	if err := db.First(&currentRoot, root.Id).Error; err != nil {
		t.Fatal(err)
	}
	currentRoot.Status = UserStatusDisabled
	if err := currentRoot.Update(false); err != nil {
		t.Fatal(err)
	}
	currentRoot.Status = UserStatusEnabled
	if err := currentRoot.Update(false); err != nil {
		t.Fatal(err)
	}
	if err := ApplyRefundCapabilityChange(root.Id, session, disableTicket, scope); !errors.Is(err, ErrRefundStepUpInvalid) {
		t.Fatalf("disable/reenable did not invalidate actor ticket: %v", err)
	}
	var count int64
	if err := db.Model(&RefundCapabilityGrant{}).Where("user_id = ? AND revoked_at IS NULL", finance.Id).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("invalidated tickets changed recipient grant: count=%d err=%v", count, err)
	}
}

func TestRefundCapabilityRoleMappingAndStepUpScope(t *testing.T) {
	db, root, admin, finance := setupRefundAuthModelTest(t)
	for _, cap := range []string{RefundCapabilityRead, RefundCapabilityReview, RefundCapabilitySubmit, RefundCapabilityReconcile, RefundCapabilityAudit, RefundCapabilityManage} {
		allowed, err := HasRefundCapability(root.Id, cap)
		if err != nil || !allowed {
			t.Fatalf("exact root role should have %s: allowed=%v err=%v", cap, allowed, err)
		}
		allowed, err = HasRefundCapability(admin.Id, cap)
		if err != nil || allowed {
			t.Fatalf("legacy role 10 must have no default %s: allowed=%v err=%v", cap, allowed, err)
		}
	}
	session := "root-session-binding-01234567890123456789"
	scope := authScope(RefundStepUpGrant, finance.Id, RefundCapabilityReview)
	if _, err := IssueRefundCapabilityStepUpTicket(root.Id, session, "wrong-password", scope.Action, scope.TargetUserID, scope.Capabilities, scope.Reason); !errors.Is(err, ErrRefundStepUpInvalid) {
		t.Fatalf("wrong password accepted: %v", err)
	}
	ticket := getCapabilityTicket(t, root, scope, session)
	var stored RefundStepUpTicket
	if err := db.First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.TokenDigest == ticket || stored.SessionDigest == session || stored.PasswordDigest == root.Password {
		t.Fatal("raw ticket, session binding, or password hash was stored")
	}
	if err := ApplyRefundCapabilityChange(root.Id, session, ticket, scope); err != nil {
		t.Fatal(err)
	}
	allowed, err := HasRefundCapability(finance.Id, RefundCapabilityReview)
	if err != nil || !allowed {
		t.Fatalf("grant not active: allowed=%v err=%v", allowed, err)
	}
	var audit RefundAuthorizationAudit
	if err := db.First(&audit).Error; err != nil || audit.ActorUserID != root.Id || audit.TargetUserID != finance.Id || audit.Reason != scope.Reason {
		t.Fatalf("missing safe grant audit: row=%+v err=%v", audit, err)
	}
	if err := db.Model(&RefundAuthorizationAudit{}).Where("id = ?", audit.ID).Update("reason", "").Error; err == nil {
		t.Fatal("authorization audit update was accepted")
	}
}

func TestRefundCapabilityEpochInvalidatesOldTicketsAndLegacyPasswordChanges(t *testing.T) {
	db, root, admin, finance := setupRefundAuthModelTest(t)
	session := "root-session-binding-01234567890123456789"
	grantScope := authScope(RefundStepUpGrant, finance.Id, RefundCapabilityReview)
	oldGrantTicket := getCapabilityTicket(t, root, grantScope, session)
	revokeScope := authScope(RefundStepUpRevoke, finance.Id, RefundCapabilityReview)
	revokeTicket := getCapabilityTicket(t, root, revokeScope, session)
	if err := ApplyRefundCapabilityChange(root.Id, session, revokeTicket, revokeScope); err != nil {
		t.Fatal(err)
	}
	if err := ApplyRefundCapabilityChange(root.Id, session, oldGrantTicket, grantScope); !errors.Is(err, ErrRefundStepUpInvalid) {
		t.Fatalf("pre-revocation ticket remained valid: %v", err)
	}
	newGrantTicket := getCapabilityTicket(t, root, grantScope, session)
	if err := ApplyRefundCapabilityChange(root.Id, session, newGrantTicket, grantScope); err != nil {
		t.Fatal(err)
	}
	allowed, err := HasRefundCapability(finance.Id, RefundCapabilityReview)
	if err != nil || !allowed {
		t.Fatalf("explicit regrant failed: allowed=%v err=%v", allowed, err)
	}
	var staleSnapshot User
	if err := db.First(&staleSnapshot, finance.Id).Error; err != nil {
		t.Fatal(err)
	}
	_ = admin // the legacy route authorizes this role-10 actor to edit role-1 users.
	adminSnapshot := User{Id: finance.Id, Password: "replacement-password-456"}
	if err := adminSnapshot.Update(true); err != nil {
		t.Fatal(err)
	}
	allowed, err = HasRefundCapability(finance.Id, RefundCapabilityReview)
	if err != nil || allowed {
		t.Fatalf("legacy password reset inherited finance capability: allowed=%v err=%v", allowed, err)
	}
	staleSnapshot.DisplayName = "stale profile write"
	staleSnapshot.Password = ""
	if err := staleSnapshot.Update(false); err != nil {
		t.Fatal(err)
	}
	var after User
	if err := db.First(&after, finance.Id).Error; err != nil {
		t.Fatal(err)
	}
	if after.RefundAuthEpoch != staleSnapshot.RefundAuthEpoch+1 {
		t.Fatalf("stale user snapshot rewound or skipped the auth epoch: before=%d after=%d", staleSnapshot.RefundAuthEpoch, after.RefundAuthEpoch)
	}
}

func TestUserProfileUpdatePreservesRefundGrantAndStepUpTicket(t *testing.T) {
	db, root, _, finance := setupRefundAuthModelTest(t)
	session := "root-session-binding-01234567890123456789"
	scope := authScope(RefundStepUpGrant, finance.Id, RefundCapabilityReview)
	ticket := getCapabilityTicket(t, root, scope, session)
	var stale User
	if err := db.First(&stale, finance.Id).Error; err != nil {
		t.Fatal(err)
	}
	beforeEpoch := stale.RefundAuthEpoch
	var current User
	if err := DB.Omit("password").First(&current, finance.Id).Error; err != nil {
		t.Fatal(err)
	}
	current.DisplayName = "Finance Reviewer"
	if err := current.Update(false); err != nil {
		t.Fatal(err)
	}
	var afterProfile User
	if err := db.First(&afterProfile, finance.Id).Error; err != nil {
		t.Fatal(err)
	}
	if afterProfile.RefundAuthEpoch != beforeEpoch {
		t.Fatalf("non-sensitive profile update advanced auth epoch: before=%d after=%d", beforeEpoch, afterProfile.RefundAuthEpoch)
	}
	if err := ApplyRefundCapabilityChange(root.Id, session, ticket, scope); err != nil {
		t.Fatalf("profile update invalidated a valid step-up ticket: %v", err)
	}
	allowed, err := HasRefundCapability(finance.Id, RefundCapabilityReview)
	if err != nil || !allowed {
		t.Fatalf("profile update prevented grant: allowed=%v err=%v", allowed, err)
	}
	stale.DisplayName = "Stale but harmless profile edit"
	stale.Password = ""
	if err := stale.Update(false); err != nil {
		t.Fatal(err)
	}
	var afterStale User
	if err := db.First(&afterStale, finance.Id).Error; err != nil {
		t.Fatal(err)
	}
	if afterStale.RefundAuthEpoch != beforeEpoch+1 {
		t.Fatalf("stale non-sensitive snapshot changed the current auth epoch: before=%d after=%d", beforeEpoch, afterStale.RefundAuthEpoch)
	}
}

func TestRefundCapabilityTicketRollbackAndDisableReenableInvalidation(t *testing.T) {
	db, root, _, finance := setupRefundAuthModelTest(t)
	session := "root-session-binding-01234567890123456789"
	scope := authScope(RefundStepUpGrant, finance.Id, RefundCapabilityRead)
	ticket := getCapabilityTicket(t, root, scope, session)
	if err := db.Exec("CREATE TRIGGER fail_refund_auth_audit BEFORE INSERT ON refund_authorization_audits BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyRefundCapabilityChange(root.Id, session, ticket, scope); err == nil {
		t.Fatal("expected audit insert failure")
	}
	if err := db.Exec("DROP TRIGGER fail_refund_auth_audit").Error; err != nil {
		t.Fatal(err)
	}
	var stored RefundStepUpTicket
	if err := db.First(&stored, "token_digest = ?", refundDigest(ticket)).Error; err != nil || stored.ConsumedAt != nil {
		t.Fatalf("failed transaction burned step-up ticket: row=%+v err=%v", stored, err)
	}
	var grants int64
	if err := db.Model(&RefundCapabilityGrant{}).Where("user_id = ? AND revoked_at IS NULL", finance.Id).Count(&grants).Error; err != nil || grants != 0 {
		t.Fatalf("failed transaction left a grant: count=%d err=%v", grants, err)
	}
	if err := ApplyRefundCapabilityChange(root.Id, session, ticket, scope); err != nil {
		t.Fatal(err)
	}
	var financeRow User
	if err := db.First(&financeRow, finance.Id).Error; err != nil {
		t.Fatal(err)
	}
	financeRow.Status = UserStatusDisabled
	if err := financeRow.Update(false); err != nil {
		t.Fatal(err)
	}
	financeRow.Status = UserStatusEnabled
	if err := financeRow.Update(false); err != nil {
		t.Fatal(err)
	}
	allowed, err := HasRefundCapability(finance.Id, RefundCapabilityRead)
	if err != nil || allowed {
		t.Fatalf("disable/reenable revived a stale grant: allowed=%v err=%v", allowed, err)
	}
}

func TestRefundCapabilityRegrantDoesNotReviveOtherStaleGrants(t *testing.T) {
	_, root, _, finance := setupRefundAuthModelTest(t)
	session := "root-session-binding-01234567890123456789"
	grantAll := authScope(RefundStepUpGrant, finance.Id, RefundCapabilityRead, RefundCapabilitySubmit)
	ticket := getCapabilityTicket(t, root, grantAll, session)
	if err := ApplyRefundCapabilityChange(root.Id, session, ticket, grantAll); err != nil {
		t.Fatal(err)
	}
	var target User
	if err := DB.First(&target, finance.Id).Error; err != nil {
		t.Fatal(err)
	}
	target.Status = UserStatusDisabled
	if err := target.Update(false); err != nil {
		t.Fatal(err)
	}
	target.Status = UserStatusEnabled
	if err := target.Update(false); err != nil {
		t.Fatal(err)
	}
	readOnly := authScope(RefundStepUpGrant, finance.Id, RefundCapabilityRead)
	ticket = getCapabilityTicket(t, root, readOnly, session)
	if err := ApplyRefundCapabilityChange(root.Id, session, ticket, readOnly); err != nil {
		t.Fatal(err)
	}
	read, err := HasRefundCapability(finance.Id, RefundCapabilityRead)
	if err != nil || !read {
		t.Fatalf("explicit read grant missing: allowed=%v err=%v", read, err)
	}
	submit, err := HasRefundCapability(finance.Id, RefundCapabilitySubmit)
	if err != nil || submit {
		t.Fatalf("regranting read revived stale submit: allowed=%v err=%v", submit, err)
	}
}

func TestDelegatedRefundStepUpBindsServerSnapshotAndConsumesInBusinessTransaction(t *testing.T) {
	db, root, _, finance := setupRefundAuthModelTest(t)
	session := "finance-session-binding-012345678901234567890123"
	grantScope := authScope(RefundStepUpGrant, finance.Id, RefundCapabilityReview)
	grantTicket := getCapabilityTicket(t, root, grantScope, "root-session-binding-01234567890123456789")
	if err := ApplyRefundCapabilityChange(root.Id, "root-session-binding-01234567890123456789", grantTicket, grantScope); err != nil {
		t.Fatal(err)
	}
	refunds := []PointRefund{
		{RefundKey: "refund-auth-100", UserID: finance.Id, OrderKey: "order-auth-100", Channel: "alipay", ProviderMerchantID: "merchant", ProviderAppID: "app", ProviderTransactionID: "trade-100", ProviderRefundKey: "merchant-refund-100", Currency: "CNY", AmountFen: 100, PurchaseMicro: 100 * PointMicroPerPoint, OriginalAmountFen: 100, OriginalPurchaseMicro: 100 * PointMicroPerPoint, Reason: "verified request", State: "awaiting_review"},
		{RefundKey: "refund-auth-200", UserID: finance.Id, OrderKey: "order-auth-200", Channel: "alipay", ProviderMerchantID: "merchant", ProviderAppID: "app", ProviderTransactionID: "trade-200", ProviderRefundKey: "merchant-refund-200", Currency: "CNY", AmountFen: 200, PurchaseMicro: 200 * PointMicroPerPoint, OriginalAmountFen: 200, OriginalPurchaseMicro: 200 * PointMicroPerPoint, Reason: "verified request", State: "awaiting_review"},
	}
	if err := db.Create(&refunds).Error; err != nil {
		t.Fatal(err)
	}
	config.PointRefundOperationsEnabled = false
	if _, err := IssueRefundOperationStepUpTicket(finance.Id, session, "local-password-123", refunds[0].RefundKey, "refund.review"); !errors.Is(err, ErrRefundAuthUnavailable) {
		t.Fatalf("refund gate off still issued an action ticket: %v", err)
	}
	config.PointRefundOperationsEnabled = true
	var oauthOnly User
	if err := db.First(&oauthOnly, 904).Error; err != nil {
		t.Fatal(err)
	}
	oauthGrant := RefundCapabilityGrant{UserID: oauthOnly.Id, Capability: RefundCapabilityReview, GrantedBy: root.Id, CredentialDigest: userCredentialDigest(oauthOnly), CredentialEpoch: oauthOnly.RefundAuthEpoch, GrantedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := db.Create(&oauthGrant).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := IssueRefundOperationStepUpTicket(oauthOnly.Id, "oauth-session-binding-01234567890123456789", "", refunds[0].RefundKey, "refund.review"); !errors.Is(err, ErrRefundStepUpInvalid) {
		t.Fatalf("OAuth-only user received sensitive ticket without local password: %v", err)
	}
	ticket, err := IssueRefundOperationStepUpTicket(finance.Id, session, "local-password-123", refunds[0].RefundKey, "refund.review")
	if err != nil {
		t.Fatalf("delegated finance step-up was rejected: %v", err)
	}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		return ConsumeRefundOperationStepUpTx(tx, ticket, finance.Id, "other-finance-session-binding-01234567890123456789", refunds[0].RefundKey, "refund.review", time.Now())
	}); !errors.Is(err, ErrRefundStepUpInvalid) {
		t.Fatalf("ticket accepted from another session: %v", err)
	}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		return ConsumeRefundOperationStepUpTx(tx, ticket, finance.Id, session, refunds[1].RefundKey, "refund.review", time.Now())
	}); !errors.Is(err, ErrRefundStepUpInvalid) {
		t.Fatalf("ticket accepted for another refund snapshot: %v", err)
	}
	var stored RefundStepUpTicket
	if err := db.First(&stored, "token_digest = ?", refundDigest(ticket)).Error; err != nil {
		t.Fatal(err)
	}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		return ConsumeRefundOperationStepUpTx(tx, ticket, finance.Id, session, refunds[0].RefundKey, "refund.submit", time.Now())
	}); !errors.Is(err, ErrRefundCapabilityDenied) {
		t.Fatalf("finance used an ungranted operation: %v", err)
	}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		return ConsumeRefundOperationStepUpTx(tx, ticket, finance.Id, session, refunds[0].RefundKey, "refund.review", stored.ExpiresAt.Add(time.Second))
	}); !errors.Is(err, ErrRefundStepUpInvalid) {
		t.Fatalf("expired ticket was accepted: %v", err)
	}
	rollbackErr := errors.New("simulated review write failure")
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := ConsumeRefundOperationStepUpTx(tx, ticket, finance.Id, session, refunds[0].RefundKey, "refund.review", time.Now()); err != nil {
			return err
		}
		if err := tx.Model(&PointRefund{}).Where("refund_key = ?", refunds[0].RefundKey).Update("state", "approved").Error; err != nil {
			return err
		}
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("expected simulated transaction failure, got %v", err)
	}
	var afterRollback PointRefund
	if err := db.First(&afterRollback, "refund_key = ?", refunds[0].RefundKey).Error; err != nil || afterRollback.State != "awaiting_review" {
		t.Fatalf("refund mutation survived rollback: state=%q err=%v", afterRollback.State, err)
	}
	if err := db.First(&stored, "token_digest = ?", refundDigest(ticket)).Error; err != nil || stored.ConsumedAt != nil {
		t.Fatalf("rollback burned one-time ticket: consumed=%v err=%v", stored.ConsumedAt, err)
	}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		if err := ConsumeRefundOperationStepUpTx(tx, ticket, finance.Id, session, refunds[0].RefundKey, "refund.review", time.Now()); err != nil {
			return err
		}
		return tx.Model(&PointRefund{}).Where("refund_key = ?", refunds[0].RefundKey).Update("state", "approved").Error
	}); err != nil {
		t.Fatalf("same transaction retry did not succeed: %v", err)
	}
	if err := DB.Transaction(func(tx *gorm.DB) error {
		return ConsumeRefundOperationStepUpTx(tx, ticket, finance.Id, session, refunds[0].RefundKey, "refund.review", time.Now())
	}); !errors.Is(err, ErrRefundStepUpInvalid) {
		t.Fatalf("ticket replay was accepted: %v", err)
	}
	if err := db.First(&stored, "token_digest = ?", refundDigest(ticket)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&stored).Update("consumed_at", nil).Error; err == nil {
		t.Fatal("consumed step-up ticket was reset to unused")
	}
}

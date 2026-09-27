package model

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

func openPointsTestDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	dsn, err := sqliteDSN(path, 10000)
	if err != nil {
		t.Fatal(err)
	}
	dsn += "&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(8)
	if err = db.AutoMigrate(&Token{}); err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&PointsSchemaMigration{}, &PointAccount{}, &PointLot{}, &PointLedger{}, &PointPriceVersion{}, &PointActivePrice{}, &PointHold{}, &PointHoldAllocation{}, &PointHoldAttempt{}, &PointHoldDecision{}, &PointAdminAudit{}, &PointTokenBudget{}, &PointPurchaseOrder{}, &PointPackage{}, &PointActivePackage{}, &PaymentEvent{}, &PaymentTransaction{}, &PointRefund{}, &PointRefundAllocation{}, &PointRefundDecision{}, &PointRefundEvidence{}, &PointRefundProviderOwner{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSQLiteDSNForcesSingleImmediateLockAndBusyTimeout(t *testing.T) {
	dsn, err := sqliteDSN("file:points.db?cache=shared&_txlock=deferred&_busy_timeout=999&_txlock=exclusive", 4321)
	if err != nil {
		t.Fatal(err)
	}
	base, query, ok := strings.Cut(dsn, "?")
	if !ok || base != "file:points.db" {
		t.Fatalf("unexpected DSN base: %q", dsn)
	}
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatal(err)
	}
	if got := values["_txlock"]; len(got) != 1 || got[0] != "immediate" {
		t.Fatalf("txlock must be forced exactly once: %#v", got)
	}
	if got := values["_busy_timeout"]; len(got) != 1 || got[0] != "4321" {
		t.Fatalf("busy timeout must be forced exactly once: %#v", got)
	}
	if got := values.Get("cache"); got != "shared" {
		t.Fatalf("unrelated query setting was lost: %q", got)
	}
	if _, err := sqliteDSN("file:points.db?cache=%", 4321); err == nil {
		t.Fatal("malformed SQLite DSN query was silently discarded")
	}
}

func TestSQLiteImmediateTransactionSerializesIndependentHandles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "immediate.db")
	open := func(timeout int) *gorm.DB {
		dsn, err := sqliteDSN(path, timeout)
		if err != nil {
			t.Fatal(err)
		}
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(2)
		return db
	}
	dbA, dbB := open(1000), open(1)
	defer func() { sqlDB, _ := dbA.DB(); _ = sqlDB.Close() }()
	defer func() { sqlDB, _ := dbB.DB(); _ = sqlDB.Close() }()
	if err := dbA.AutoMigrate(&PointsSchemaMigration{}); err != nil {
		t.Fatal(err)
	}
	enteredA := make(chan struct{})
	releaseA := make(chan struct{})
	resultA := make(chan error, 1)
	var releaseOnce sync.Once
	aDone := false
	release := func() { releaseOnce.Do(func() { close(releaseA) }) }
	defer func() {
		release()
		if !aDone {
			select {
			case <-resultA:
			case <-time.After(time.Second):
				t.Error("connection A transaction did not exit after release")
			}
		}
	}()
	go func() {
		resultA <- dbA.Transaction(func(tx *gorm.DB) error {
			close(enteredA) // BEGIN IMMEDIATE already reserved the writer slot.
			<-releaseA
			return nil
		})
	}()
	select {
	case <-enteredA:
	case <-time.After(time.Second):
		release()
		t.Fatal("connection A transaction did not enter")
	}
	enteredB := make(chan struct{}, 1)
	err := dbB.Transaction(func(tx *gorm.DB) error {
		enteredB <- struct{}{}
		return nil
	})
	if !isSQLiteBusy(err) {
		t.Fatal("connection B entered while A held the immediate transaction")
	}
	select {
	case <-enteredB:
		t.Fatal("connection B closure ran before A released the writer reservation")
	default:
	}
	release()
	if err := <-resultA; err != nil {
		t.Fatal(err)
	}
	aDone = true
	enteredAfterRelease := false
	if err := dbB.Transaction(func(tx *gorm.DB) error {
		enteredAfterRelease = true
		return nil
	}); err != nil {
		t.Fatalf("connection B could not enter after A released: %v", err)
	}
	if !enteredAfterRelease {
		t.Fatal("connection B closure did not run after A released")
	}
}

func seedPointUser(t *testing.T, db *gorm.DB, balance, budget int64, unlimited bool) {
	t.Helper()
	if err := db.Create(&Token{Id: 77, UserId: 41, Key: "points-test-token", Status: TokenStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointAccount{UserID: 41, AvailableMicro: balance}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointLot{UserID: 41, BusinessKey: "seed:lot", Kind: "grant", InitialMicro: balance, AvailableMicro: balance}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointTokenBudget{TokenID: 77, UserID: 41, LimitMicro: budget, Unlimited: unlimited}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointPriceVersion{Version: "test-v1", ModelID: "test-model", InputMicroPer1K: 1000, CachedInputMicroPer1K: 100, OutputMicroPer1K: 2000, Source: "test fixture"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointActivePrice{ModelID: "test-model", Version: "test-v1"}).Error; err != nil {
		t.Fatal(err)
	}
}

func withPointsFixture(t *testing.T, balance, budget int64, unlimited bool) (*gorm.DB, func()) {
	t.Helper()
	oldDB, oldSQLite, oldEnabled := DB, common.UsingSQLite, config.PointsBillingEnabled
	path := filepath.Join(t.TempDir(), "points.db")
	db := openPointsTestDB(t, path)
	DB, common.UsingSQLite, config.PointsBillingEnabled = db, true, true
	if err := db.Create(&PointsSchemaMigration{Version: pointsSchemaVersion, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	seedPointUser(t, db, balance, budget, unlimited)
	return db, func() {
		sqlDB, _ := db.DB()
		_ = sqlDB.Close()
		DB, common.UsingSQLite, config.PointsBillingEnabled = oldDB, oldSQLite, oldEnabled
	}
}

func TestPointArithmeticExactHalfUp(t *testing.T) {
	got, err := ParseMicroPoints("12.000001")
	if err != nil || got != 12_000_001 {
		t.Fatalf("ParseMicroPoints got %d, %v", got, err)
	}
	for _, bad := range []string{"1.-1", "1.0000001", "-1", "x"} {
		if _, err := ParseMicroPoints(bad); err == nil {
			t.Fatalf("expected rejection for %q", bad)
		}
	}
	usage, err := CalculatePointUsage(500, 0, 0, PointPriceVersion{InputMicroPer1K: 1, ExtraMicro: 5})
	if err != nil || usage != 6 {
		t.Fatalf("expected half-up 1 plus extra 5 micro-points, got %d, %v", usage, err)
	}
	if _, err := CalculatePointUsage(1, 2, 0, PointPriceVersion{}); err == nil {
		t.Fatal("cached input greater than prompt must fail")
	}
}

func TestLegacyQuotaWritesAreBlockedInPointsMode(t *testing.T) {
	db, cleanup := withPointsFixture(t, 1_000_000, 1_000_000, false)
	defer cleanup()
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	user := User{Id: 41, Username: "legacy-quota-user", Password: "fixture", AccessToken: "legacy-quota-access", AffCode: "legacy-quota-code", Role: RoleCommonUser, Status: UserStatusEnabled, Quota: 1234}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&Token{}).Where("id = ?", 77).Updates(map[string]any{"remain_quota": 5000, "used_quota": 100}).Error; err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"increase user":  func() error { return IncreaseUserQuota(41, 100) },
		"decrease user":  func() error { return DecreaseUserQuota(41, 100) },
		"queued user":    func() error { return increaseUserQuota(41, 100) },
		"increase token": func() error { return IncreaseTokenQuota(77, 100) },
		"decrease token": func() error { return DecreaseTokenQuota(77, 100) },
		"queued token":   func() error { return increaseTokenQuota(77, 100) },
	} {
		if err := call(); !errors.Is(err, ErrPointsLegacyQuotaDisabled) {
			t.Errorf("%s error=%v, want legacy quota disabled", name, err)
		}
	}
	if _, err := Redeem("any-code", 41); !errors.Is(err, ErrPointsLegacyQuotaDisabled) {
		t.Fatalf("redemption should be blocked in points mode: %v", err)
	}
	if err := (&User{Id: 41}).Delete(); !errors.Is(err, ErrPointsUserDeletionDisabled) {
		t.Fatalf("user deletion should preserve point ownership: %v", err)
	}
	user.Quota = 0
	if err := user.Update(false); err != nil {
		t.Fatal(err)
	}
	token, err := GetTokenById(77)
	if err != nil {
		t.Fatal(err)
	}
	token.RemainQuota = 0
	token.UnlimitedQuota = false
	if err := token.Update(); err != nil {
		t.Fatal(err)
	}
	var persistedUser User
	if err := db.First(&persistedUser, "id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	var persistedToken Token
	if err := db.First(&persistedToken, "id = ?", 77).Error; err != nil {
		t.Fatal(err)
	}
	if persistedUser.Quota != 1234 || persistedToken.RemainQuota != 5000 || persistedToken.UsedQuota != 100 || persistedToken.UnlimitedQuota {
		t.Fatalf("legacy quota columns changed: user=%d token=%+v", persistedUser.Quota, persistedToken)
	}
}

func TestNewUsersDoNotReceiveLegacyQuotaInPointsMode(t *testing.T) {
	db, cleanup := withPointsFixture(t, 1_000_000, 1_000_000, false)
	defer cleanup()
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&User{Id: 41, Username: "inviter", Password: "fixture", AccessToken: "inviter-access", AffCode: "invite", Role: RoleCommonUser, Status: UserStatusEnabled, Quota: 2500}).Error; err != nil {
		t.Fatal(err)
	}
	oldNew, oldInviter, oldInvitee := config.QuotaForNewUser, config.QuotaForInviter, config.QuotaForInvitee
	config.QuotaForNewUser, config.QuotaForInviter, config.QuotaForInvitee = 1000, 500, 300
	t.Cleanup(func() {
		config.QuotaForNewUser, config.QuotaForInviter, config.QuotaForInvitee = oldNew, oldInviter, oldInvitee
	})
	user := User{Username: "new-points-user", Password: "fixture", Role: RoleCommonUser, Status: UserStatusEnabled}
	if err := user.Insert(41); err != nil {
		t.Fatal(err)
	}
	var persisted User
	if err := db.First(&persisted, "id = ?", user.Id).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.Quota != 0 {
		t.Fatalf("new points user received legacy quota %d", persisted.Quota)
	}
	var inviter User
	if err := db.First(&inviter, "id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if inviter.Quota != 2500 {
		t.Fatalf("new-user invite bonus changed inviter legacy quota to %d", inviter.Quota)
	}
	var defaultToken Token
	if err := db.Where("user_id = ? AND name = ?", user.Id, "default").First(&defaultToken).Error; err != nil {
		t.Fatal(err)
	}
	if defaultToken.RemainQuota != 0 || defaultToken.UnlimitedQuota {
		t.Fatalf("default token received legacy quota: %+v", defaultToken)
	}
}

func TestPointsTokenAuthenticationIgnoresLegacyExhaustion(t *testing.T) {
	db, cleanup := withPointsFixture(t, 1_000_000, 1_000_000, false)
	defer cleanup()
	if err := db.Model(&Token{}).Where("id = ?", 77).Updates(map[string]any{"status": TokenStatusExhausted, "remain_quota": 0, "unlimited_quota": false}).Error; err != nil {
		t.Fatal(err)
	}
	token, err := ValidateUserToken("points-test-token")
	if err != nil {
		t.Fatalf("legacy exhausted state blocked points token: %v", err)
	}
	if token.Status != TokenStatusExhausted || token.RemainQuota != 0 {
		t.Fatalf("authentication did not read current token record: %+v", token)
	}
	if err := db.Model(&Token{}).Where("id = ?", 77).Update("status", TokenStatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateUserToken("points-test-token"); err == nil {
		t.Fatal("disabled token was accepted in points mode")
	}
}

func TestOfflineRecoveryMarksHeldPendingAndIsIdempotent(t *testing.T) {
	db, cleanup := withPointsFixture(t, 1_000_000, 1_000_000, false)
	defer cleanup()
	hold, err := ReservePoints(PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "recover:logical-1", AttemptKey: "primary", LocalRequestID: "local-1", AttemptFingerprint: "fingerprint-1", BudgetMicro: 100_000, PriceVersion: "test-v1"})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverOrphanedPointHolds("ops-2026-09-27-a", "process stopped during provider response")
	if err != nil || recovered != 1 {
		t.Fatalf("recovery count=%d err=%v", recovered, err)
	}
	var stored PointHold
	if err := db.First(&stored, "id = ?", hold.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.State != "pending" || stored.UsageMicro != 0 {
		t.Fatalf("recovery settled or released hold: %+v", stored)
	}
	var account PointAccount
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 900_000 || account.HeldMicro != 100_000 || account.SpentMicro != 0 {
		t.Fatalf("recovery changed balances: %+v", account)
	}
	var audits int64
	if err := db.Model(&PointAdminAudit{}).Where("action = ?", "offline_hold_recovery").Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("expected batch and hold audit rows, got %d", audits)
	}
	later, err := ReservePoints(PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "recover:later", AttemptKey: "primary", LocalRequestID: "local-later", AttemptFingerprint: "fingerprint-later", BudgetMicro: 100_000, PriceVersion: "test-v1"})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err = RecoverOrphanedPointHolds("ops-2026-09-27-a", "process stopped during provider response")
	if err != nil || recovered != 0 {
		t.Fatalf("idempotent rerun count=%d err=%v", recovered, err)
	}
	var laterStored PointHold
	if err := db.First(&laterStored, "id = ?", later.ID).Error; err != nil {
		t.Fatal(err)
	}
	if laterStored.State != "held" {
		t.Fatalf("replay recovered a later request: %+v", laterStored)
	}
	if _, err := RecoverOrphanedPointHolds("ops-2026-09-27-a", "different reason"); !errors.Is(err, ErrPointsConflict) {
		t.Fatalf("conflicting recovery reason accepted: %v", err)
	}
	if err := db.Model(&PointAdminAudit{}).Where("action = ?", "offline_hold_recovery").Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("repeated recovery duplicated audit rows: %d", audits)
	}
}

func TestPointsSchemaUpgradesVersionOne(t *testing.T) {
	oldDB, oldSQLite := DB, common.UsingSQLite
	t.Cleanup(func() { DB, common.UsingSQLite = oldDB, oldSQLite })
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "upgrade.db")), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	DB, common.UsingSQLite = db, true
	if err := db.AutoMigrate(&PointsSchemaMigration{}, &PointAccount{}, &PointLot{}, &PointLedger{}, &PointPriceVersion{}, &PointActivePrice{}, &PointHold{}, &PointHoldAllocation{}, &PointHoldAttempt{}, &PointHoldDecision{}, &PointTokenBudget{}, &PointPurchaseOrder{}); err != nil {
		t.Fatal(err)
	}
	// Simulate the known v1 schema by removing fields/tables added in v3 and v4.
	if err := db.Migrator().DropColumn(&PointHoldAttempt{}, "local_request_id"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&PointHoldAttempt{}, "provider_request_id"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&PointHoldAttempt{}, "provider_response_id"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&PointHoldAttempt{}, "prompt_tokens"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&PointHoldAttempt{}, "cached_prompt_tokens"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&PointHoldAttempt{}, "completion_tokens"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&PointHoldAttempt{}, "reasoning_tokens"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&PointAdminAudit{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointsSchemaMigration{Version: 1, AppliedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointAccount{UserID: 91, AvailableMicro: 700, SpentMicro: 30}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointLot{UserID: 91, BusinessKey: "v1-lot", Kind: "grant", InitialMicro: 730, AvailableMicro: 700, ConsumedMicro: 30}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&PointLedger{UserID: 91, BusinessKey: "v1-ledger", Kind: "settle", AvailableDelta: 700, SpentDelta: 30, AvailableAfter: 700, SpentAfter: 30, Reason: "pre-upgrade record"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := MigratePointsSchema(); err != nil {
		t.Fatal(err)
	}
	if err := MigratePointsSchema(); err != nil {
		t.Fatalf("repeated migration failed: %v", err)
	}
	var current PointsSchemaMigration
	if err := db.First(&current, "version = ?", pointsSchemaVersion).Error; err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable(&PointAdminAudit{}) || !db.Migrator().HasColumn(&PointHoldAttempt{}, "provider_response_id") || !db.Migrator().HasColumn(&PointHoldAttempt{}, "local_request_id") {
		t.Fatal("schema upgrade omitted newly required audit or provider evidence fields")
	}
	var account PointAccount
	var lot PointLot
	var ledger PointLedger
	if err := db.First(&account, "user_id = ?", 91).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&lot, "business_key = ?", "v1-lot").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&ledger, "business_key = ?", "v1-ledger").Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 700 || account.SpentMicro != 30 || lot.AvailableMicro != 700 || lot.ConsumedMicro != 30 || ledger.AvailableAfter != 700 || ledger.SpentAfter != 30 {
		t.Fatalf("v1 data changed across upgrade: account=%+v lot=%+v ledger=%+v", account, lot, ledger)
	}
	var migrationCount int64
	db.Model(&PointsSchemaMigration{}).Where("version = ?", pointsSchemaVersion).Count(&migrationCount)
	if migrationCount != 1 {
		t.Fatalf("expected one current schema marker, got %d", migrationCount)
	}
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()
}

func TestAdjustmentReplayIsIdempotentAndChecksArguments(t *testing.T) {
	db, cleanup := withPointsFixture(t, 0, 100, true)
	defer cleanup()
	if err := db.AutoMigrate(&User{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&User{Id: 41, Username: "grant-target", Password: "fixture", AccessToken: "grant-target-token", AffCode: "grant-target-code", Role: RoleCommonUser, Status: UserStatusEnabled}).Error; err != nil {
		t.Fatal(err)
	}
	if err := AdjustPoints(5, 41, 123456, "grant-1", "support adjustment"); err != nil {
		t.Fatal(err)
	}
	if err := AdjustPoints(5, 41, 123456, "grant-1", "support adjustment"); err != nil {
		t.Fatalf("identical replay failed: %v", err)
	}
	if err := AdjustPoints(5, 41, 123457, "grant-1", "support adjustment"); !errors.Is(err, ErrPointsConflict) {
		t.Fatalf("changed replay should conflict, got %v", err)
	}
	var account PointAccount
	_ = DB.First(&account, "user_id = ?", 41)
	if account.AvailableMicro != 123456 {
		t.Fatalf("replayed grant credited twice: %+v", account)
	}
}

func TestPointReserveConcurrentHandlesCannotOverdraw(t *testing.T) {
	db, cleanup := withPointsFixture(t, 100, 100, false)
	defer cleanup()
	path := strings.SplitN(db.Dialector.(*sqlite.Dialector).DSN, "?", 2)[0]
	db2 := openPointsTestDB(t, path)
	defer func() { sqlDB, _ := db2.DB(); _ = sqlDB.Close() }()
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes, insufficient, unexpected := 0, 0, []error{}
	start := make(chan struct{})
	handles := []*gorm.DB{db, db2}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int, handle *gorm.DB) {
			defer wg.Done()
			<-start
			_, err := reservePointsOn(handle, PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: fmt.Sprintf("concurrent-%d", i), AttemptKey: "attempt-1", AttemptFingerprint: fmt.Sprintf("request-%d", i), BudgetMicro: 20, PriceVersion: "test-v1"})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				successes++
			} else if errors.Is(err, ErrPointsInsufficient) {
				insufficient++
			} else {
				unexpected = append(unexpected, err)
			}
		}(i, handles[i%len(handles)])
	}
	close(start)
	wg.Wait()
	if successes != 5 || insufficient != 3 || len(unexpected) != 0 {
		t.Fatalf("success=%d insufficient=%d unexpected=%v", successes, insufficient, unexpected)
	}
	var account PointAccount
	if err := db.First(&account, "user_id = ?", 41).Error; err != nil {
		t.Fatal(err)
	}
	if account.AvailableMicro != 0 || account.HeldMicro != 100 {
		t.Fatalf("unexpected account after concurrent reserves: %+v", account)
	}
}

func TestPointReserveRollsBackWhenTokenBudgetFails(t *testing.T) {
	db, cleanup := withPointsFixture(t, 100, 50, false)
	defer cleanup()
	_, err := ReservePoints(PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "rollback", AttemptKey: "a1", AttemptFingerprint: "rollback-fp", BudgetMicro: 60, PriceVersion: "test-v1"})
	if err == nil {
		t.Fatal("expected token budget rejection")
	}
	var account PointAccount
	_ = db.First(&account, "user_id = ?", 41)
	var lot PointLot
	_ = db.First(&lot, "business_key = ?", "seed:lot")
	var ledgerCount, holdCount int64
	db.Model(&PointLedger{}).Count(&ledgerCount)
	db.Model(&PointHold{}).Count(&holdCount)
	if account.AvailableMicro != 100 || account.HeldMicro != 0 || lot.AvailableMicro != 100 || lot.HeldMicro != 0 || ledgerCount != 0 || holdCount != 0 {
		t.Fatalf("failed reserve leaked partial writes: account=%+v lot=%+v ledger=%d holds=%d", account, lot, ledgerCount, holdCount)
	}
}

func TestPendingUsageCanBeAuthoritativelyResolvedOnce(t *testing.T) {
	db, cleanup := withPointsFixture(t, 100, 100, false)
	defer cleanup()
	req := PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "pending-case", AttemptKey: "attempt-1", AttemptFingerprint: "pending-fp", BudgetMicro: 60, PriceVersion: "test-v1"}
	if _, err := ReservePoints(req); err != nil {
		t.Fatal(err)
	}
	hold, err := RecordPointAttemptUsage(PointAttemptUsage{LogicalRequestKey: req.LogicalRequestKey, AttemptKey: req.AttemptKey, State: "unknown", UsageMicro: 10, UsageSource: "estimated", Authoritative: false, Billable: true})
	if err != nil || hold.State != "pending" {
		t.Fatalf("expected pending hold, got %+v err=%v", hold, err)
	}
	hold, err = RecordPointAttemptUsage(PointAttemptUsage{LogicalRequestKey: req.LogicalRequestKey, AttemptKey: req.AttemptKey, State: "succeeded", UsageMicro: 20, UsageSource: "provider-final", Authoritative: true, Fingerprint: "sha256:test", Billable: true})
	if err != nil || hold.State != "settled" {
		t.Fatalf("expected authoritative settlement, got %+v err=%v", hold, err)
	}
	_, err = RecordPointAttemptUsage(PointAttemptUsage{LogicalRequestKey: req.LogicalRequestKey, AttemptKey: req.AttemptKey, State: "succeeded", UsageMicro: 20, UsageSource: "provider-final", Authoritative: true, Fingerprint: "sha256:test", Billable: true})
	if err != nil {
		t.Fatal(err)
	}
	var account PointAccount
	_ = db.First(&account, "user_id = ?", 41)
	var settleCount int64
	db.Model(&PointLedger{}).Where("kind = ?", "settle").Count(&settleCount)
	if account.AvailableMicro != 80 || account.HeldMicro != 0 || account.SpentMicro != 20 || settleCount != 1 {
		t.Fatalf("duplicate settlement or bad balance: account=%+v settle rows=%d", account, settleCount)
	}
}

func TestExpiredBonusReleaseDoesNotRestoreAvailability(t *testing.T) {
	db, cleanup := withPointsFixture(t, 100, 100, false)
	defer cleanup()
	if err := db.Model(&PointLot{}).Where("business_key = ?", "seed:lot").Updates(map[string]interface{}{"kind": "bonus", "expires_at": time.Now().UTC().Unix() - 60}).Error; err != nil {
		t.Fatal(err)
	}
	var expiredCount int64
	if err := db.Model(&PointLot{}).Where("expires_at IS NOT NULL AND expires_at <= ?", time.Now().UTC().Unix()).Count(&expiredCount).Error; err != nil || expiredCount != 1 {
		t.Fatalf("test setup did not persist expired lot: count=%d err=%v", expiredCount, err)
	}
	req := PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "expiry-release", AttemptKey: "a1", AttemptFingerprint: "expiry-fp", BudgetMicro: 50, PriceVersion: "test-v1"}
	if _, err := ReservePoints(req); err == nil {
		t.Fatal("expired lot must not fund a new hold")
	}
	// Seed a hold before expiry, then verify an adjudicated release does not revive the expired lot.
	if err := db.Model(&PointLot{}).Where("business_key = ?", "seed:lot").Update("available_micro", 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PointAccount{}).Where("user_id = ?", 41).Update("available_micro", 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PointLot{}).Where("business_key = ?", "seed:lot").Update("expires_at", time.Now().UTC().Unix()+3600).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ReservePoints(req); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&PointLot{}).Where("business_key = ?", "seed:lot").Update("expires_at", time.Now().UTC().Unix()-60).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := RecordPointAttemptUsage(PointAttemptUsage{LogicalRequestKey: req.LogicalRequestKey, AttemptKey: "a1", State: "unknown", UsageSource: "missing", Authoritative: false}); err != nil {
		t.Fatal(err)
	}
	if err := ResolvePointHold(req.LogicalRequestKey, "provider-decision-1", "release", 0, "provider confirmed no billable usage"); err != nil {
		t.Fatal(err)
	}
	var account PointAccount
	_ = db.First(&account, "user_id = ?", 41)
	var lot PointLot
	_ = db.First(&lot, "business_key = ?", "seed:lot")
	if account.AvailableMicro != 0 || account.HeldMicro != 0 || lot.AvailableMicro != 0 || lot.HeldMicro != 0 {
		t.Fatalf("expired release revived funds: account=%+v lot=%+v", account, lot)
	}
}

func TestPaidOrderCreditIsIdempotent(t *testing.T) {
	db, cleanup := withPointsFixture(t, 0, 100, true)
	defer cleanup()
	paidEvent := "verified-notification-1"
	expires := time.Now().UTC().Add(time.Hour).Unix()
	order := PointPurchaseOrder{OrderKey: "order-1", UserID: 41, AmountFen: 10, PurchaseMicro: 10_000_000, BonusMicro: 2_000_000, BonusValiditySecs: 3600, BonusExpiresAt: &expires, State: "paid", PaidEventKey: &paidEvent}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if err := CreditPaidPointOrder(order.OrderKey, paidEvent); err != nil {
		t.Fatal(err)
	}
	if err := CreditPaidPointOrder(order.OrderKey, paidEvent); err != nil {
		t.Fatal(err)
	}
	var account PointAccount
	_ = db.First(&account, "user_id = ?", 41)
	var lots, credits int64
	db.Model(&PointLot{}).Where("source_ref = ?", order.OrderKey).Count(&lots)
	db.Model(&PointLedger{}).Where("kind = ?", "purchase_credit").Count(&credits)
	if account.AvailableMicro != 12_000_000 || lots != 2 || credits != 1 {
		t.Fatalf("unexpected paid order credit: account=%+v lots=%d credits=%d", account, lots, credits)
	}
}

func TestReviewedUsageAboveHoldRequiresAtomicBudgetTopUp(t *testing.T) {
	db, cleanup := withPointsFixture(t, 100, 100, false)
	defer cleanup()
	req := PointReserveRequest{UserID: 41, TokenID: 77, LogicalRequestKey: "over-budget", AttemptKey: "attempt-1", AttemptFingerprint: "route-fingerprint", BudgetMicro: 50, PriceVersion: "test-v1"}
	if _, err := ReservePoints(req); err != nil {
		t.Fatal(err)
	}
	hold, err := RecordPointAttemptUsage(PointAttemptUsage{LogicalRequestKey: req.LogicalRequestKey, AttemptKey: req.AttemptKey, State: "succeeded", UsageMicro: 80, UsageSource: "provider-final", Authoritative: true, Fingerprint: "usage-fingerprint", Billable: true})
	if err != nil || hold.State != "needs_review" {
		t.Fatalf("usage above hold must wait for review: hold=%+v err=%v", hold, err)
	}
	var before PointAccount
	_ = db.First(&before, "user_id = ?", 41)
	if before.AvailableMicro != 50 || before.HeldMicro != 50 || before.SpentMicro != 0 {
		t.Fatalf("excess usage was silently charged or truncated: %+v", before)
	}
	if err := ResolvePointHold(req.LogicalRequestKey, "review-approve-1", "settle", 80, "provider usage confirmed"); err != nil {
		t.Fatal(err)
	}
	var account PointAccount
	_ = db.First(&account, "user_id = ?", 41)
	var holdAfter PointHold
	_ = db.First(&holdAfter, "logical_request_key = ?", req.LogicalRequestKey)
	if holdAfter.State != "settled" || account.AvailableMicro != 20 || account.HeldMicro != 0 || account.SpentMicro != 80 {
		t.Fatalf("reviewed topup did not settle exact usage: hold=%+v account=%+v", holdAfter, account)
	}
}

// Offline recovery exits before initializing the optional log database.
func TestOfflineRecoveryCloseDBWithoutLogDB(t *testing.T) {
	oldDB, oldLogDB := DB, LOG_DB
	t.Cleanup(func() { DB, LOG_DB = oldDB, oldLogDB })
	db := openPointsTestDB(t, filepath.Join(t.TempDir(), "close.db"))
	DB, LOG_DB = db, nil
	if err := CloseDB(); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Ping(); err == nil {
		t.Fatal("main database remained open")
	}
}

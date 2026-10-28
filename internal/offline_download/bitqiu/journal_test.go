package bitqiu

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alist-org/alist/v3/internal/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func journalFixture(t *testing.T) (submissionJournal, submissionScope) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cloud.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&model.TaskItem{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	hash := func(v string) string { sum := sha256.Sum256([]byte(v)); return hex.EncodeToString(sum[:]) }
	return submissionJournal{database: db}, submissionScope{CreatorID: 1, StorageID: 2, AccountHash: hash("account"), ParentID: "3", URLHash: hash("https://example.test/file?temporary-secret=do-not-store")}
}
func TestCloudSubmissionSurvivesRestartWithoutResubmission(t *testing.T) {
	j, scope := journalFixture(t)
	_, create, err := j.Reserve("local-task", scope)
	if err != nil || !create {
		t.Fatal(create, err)
	}
	if err = j.Confirm("local-task", scope, "remote-task"); err != nil {
		t.Fatal(err)
	}
	restarted := submissionJournal{database: j.database}
	r, create, err := restarted.Reserve("local-task", scope)
	if err != nil || create || r.RemoteID != "remote-task" {
		t.Fatal(r, create, err)
	}
	var item model.TaskItem
	if err = j.database.Where("key = ?", journalPrefix+"local-task").First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(item.PersistData, "temporary-secret") || strings.Contains(item.PersistData, "https://") {
		t.Fatal("source URL leaked into submission journal")
	}
}
func TestCloudUncertainResponseNeverAuthorizesRetry(t *testing.T) {
	j, scope := journalFixture(t)
	_, _, err := j.Reserve("lost-response", scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.Uncertain("lost-response", scope); err != nil {
		t.Fatal(err)
	}
	r, create, err := j.Reserve("lost-response", scope)
	if err != nil || create || r.State != "uncertain" {
		t.Fatal(r, create, err)
	}
	if err = j.Confirm("lost-response", scope, "late-remote-id"); err != nil {
		t.Fatal(err)
	}
	if err = j.Confirm("lost-response", scope, "different-id"); err == nil {
		t.Fatal("overwrote remote identity")
	}
	if err = j.Uncertain("lost-response", scope); err != nil {
		t.Fatal(err)
	}
	r, create, err = j.Reserve("lost-response", scope)
	if err != nil || create || r.RemoteID != "late-remote-id" {
		t.Fatal(r, create, err)
	}
}
func TestCloudSubmissionOwnershipAndDestinationCannotChange(t *testing.T) {
	j, scope := journalFixture(t)
	_, _, err := j.Reserve("owned-task", scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*submissionScope){func(s *submissionScope) { s.CreatorID++ }, func(s *submissionScope) { s.StorageID++ }, func(s *submissionScope) { s.ParentID = "other" }, func(s *submissionScope) { s.AccountHash = strings.Repeat("b", 64) }, func(s *submissionScope) { s.URLHash = strings.Repeat("c", 64) }} {
		different := scope
		change(&different)
		if _, _, err = j.Reserve("owned-task", different); err == nil {
			t.Fatal("scope change accepted")
		}
		if err = j.Confirm("owned-task", different, "other-remote"); err == nil {
			t.Fatal("foreign scope confirmation accepted")
		}
	}
}
func TestCloudConcurrentReservationsAuthorizeOnlyOneCreate(t *testing.T) {
	j, scope := journalFixture(t)
	var count atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, create, err := j.Reserve("concurrent", scope)
			if err != nil {
				t.Error(err)
			}
			if create {
				count.Add(1)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal("duplicate create authorization", count.Load())
	}
}
func TestCloudBrokenJournalAndDatabaseFailClosed(t *testing.T) {
	j, scope := journalFixture(t)
	_, _, err := j.Reserve("broken", scope)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.database.Model(&model.TaskItem{}).Where("key = ?", journalPrefix+"broken").Update("persist_data", "{broken").Error; err != nil {
		t.Fatal(err)
	}
	if _, create, err := j.Reserve("broken", scope); err == nil || create {
		t.Fatal("corrupt journal authorized a submission")
	}
	var item model.TaskItem
	_ = j.database.Where("key = ?", journalPrefix+"broken").First(&item).Error
	if item.PersistData != "{broken" {
		t.Fatal("overwrote corrupt evidence")
	}
	sqlDB, _ := j.database.DB()
	_ = sqlDB.Close()
	if _, create, err := j.Reserve("offline-db", scope); err == nil || create {
		t.Fatal("database failure authorized a submission")
	}
	if _, create, err := (submissionJournal{}).Reserve("no-db", scope); err == nil || create {
		t.Fatal("missing database authorized a submission")
	}
}

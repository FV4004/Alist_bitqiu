package op_test

import (
	"context"
	"fmt"
	"github.com/alist-org/alist/v3/drivers/local"
	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/errs"
	"strings"
	"testing"

	"github.com/alist-org/alist/v3/internal/conf"
	"github.com/alist-org/alist/v3/internal/db"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/internal/op"
	"github.com/alist-org/alist/v3/pkg/utils"
	mapset "github.com/deckarep/golang-set/v2"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func init() {
	dB, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		panic("failed to connect database")
	}
	conf.Conf = conf.DefaultConfig()
	db.Init(dB)
}

func TestCreateStorage(t *testing.T) {
	var storages = []struct {
		storage model.Storage
		isErr   bool
	}{
		{storage: model.Storage{Driver: "Local", MountPath: "/local", Addition: `{"root_folder_path":"."}`}, isErr: false},
		{storage: model.Storage{Driver: "Local", MountPath: "/local", Addition: `{"root_folder_path":"."}`}, isErr: true},
		{storage: model.Storage{Driver: "None", MountPath: "/none", Addition: `{"root_folder_path":"."}`}, isErr: true},
	}
	for _, storage := range storages {
		_, err := op.CreateStorage(context.Background(), storage.storage)
		if err != nil {
			if !storage.isErr {
				t.Errorf("failed to create storage: %+v", err)
			} else {
				t.Logf("expect failed to create storage: %+v", err)
			}
		}
	}
}

func TestGetStorageVirtualFilesByPath(t *testing.T) {
	setupStorages(t)
	virtualFiles := op.GetStorageVirtualFilesByPath("/a")
	var names []string
	for _, virtualFile := range virtualFiles {
		names = append(names, virtualFile.GetName())
	}
	var expectedNames = []string{"b", "c", "d"}
	if utils.SliceEqual(names, expectedNames) {
		t.Logf("passed")
	} else {
		t.Errorf("expected: %+v, got: %+v", expectedNames, names)
	}
}

func TestGetBalancedStorage(t *testing.T) {
	set := mapset.NewSet[string]()
	for i := 0; i < 5; i++ {
		storage := op.GetBalancedStorage("/a/d/e1")
		set.Add(storage.GetStorage().MountPath)
	}
	expected := mapset.NewSet([]string{"/a/d/e1", "/a/d/e1.balance"}...)
	if !expected.Equal(set) {
		t.Errorf("expected: %+v, got: %+v", expected, set)
	}
}

func setupStorages(t *testing.T) {
	var storages = []model.Storage{
		{Driver: "Local", MountPath: "/a/b", Order: 0, Addition: `{"root_folder_path":"."}`},
		{Driver: "Local", MountPath: "/adc", Order: 0, Addition: `{"root_folder_path":"."}`},
		{Driver: "Local", MountPath: "/a/c", Order: 1, Addition: `{"root_folder_path":"."}`},
		{Driver: "Local", MountPath: "/a/d", Order: 2, Addition: `{"root_folder_path":"."}`},
		{Driver: "Local", MountPath: "/a/d/e1", Order: 3, Addition: `{"root_folder_path":"."}`},
		{Driver: "Local", MountPath: "/a/d/e", Order: 4, Addition: `{"root_folder_path":"."}`},
		{Driver: "Local", MountPath: "/a/d/e1.balance", Order: 4, Addition: `{"root_folder_path":"."}`},
	}
	for _, storage := range storages {
		_, err := op.CreateStorage(context.Background(), storage)
		if err != nil {
			t.Fatalf("failed to create storage: %+v", err)
		}
	}
}

// Waiting for SMS is a persisted state, not a failed create/update operation.
type pendingLoginStorage struct{ local.Local }

func (d *pendingLoginStorage) Config() driver.Config { return driver.Config{Name: "TestPendingLogin"} }
func (d *pendingLoginStorage) Init(context.Context) error {
	return fmt.Errorf("%w: SMS sent", errs.DriverInitPending)
}
func TestPendingLoginCanBeCreatedAndEdited(t *testing.T) {
	op.RegisterDriver(func() driver.Driver { return &pendingLoginStorage{} })
	ctx := context.Background()
	id, err := op.CreateStorage(ctx, model.Storage{Driver: "TestPendingLogin", MountPath: "/pending-login-test", Addition: `{"root_folder_path":"."}`})
	if err != nil || id == 0 {
		t.Fatalf("create waiting storage: id=%d err=%v", id, err)
	}
	saved, err := db.GetStorageById(id)
	if err != nil || !strings.Contains(saved.Status, "SMS sent") {
		t.Fatalf("waiting state lost: %v/%v", saved, err)
	}
	if saved.Status == op.WORK {
		t.Fatal("unauthenticated driver marked working")
	}
	if err := op.UpdateStorage(ctx, *saved); err != nil {
		t.Fatalf("edit waiting storage: %v", err)
	}
}

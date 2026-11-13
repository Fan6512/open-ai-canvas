package platform

import (
	"encoding/json"
	"testing"

	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestAccessURLTTLDefaultsAndStoredOverrides(t *testing.T) {
	defaults := DefaultRuntimePolicy()
	if defaults.Storage.AccessURLTTLSeconds != 600 || defaults.Storage.ProviderAccessURLTTLSeconds != 600 {
		t.Fatalf("default storage policy = %+v", defaults.Storage)
	}
	for _, tc := range []struct {
		name, stored             string
		wantAccess, wantProvider int
	}{
		{"unconfigured", "", 600, 600},
		{"omitted", `{"storage":{}}`, 600, 600},
		{"explicit-both", `{"storage":{"accessURLTTLSeconds":14400,"providerAccessURLTTLSeconds":14400}}`, 14400, 14400},
		{"provider-only", `{"storage":{"providerAccessURLTTLSeconds":14400}}`, 600, 14400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sqlDB.Close() })
			if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
				t.Fatal(err)
			}
			if tc.stored != "" {
				if err := db.Create(&model.SystemSetting{Key: runtimePolicySettingKey, ValueJSON: tc.stored}).Error; err != nil {
					t.Fatal(err)
				}
			}
			policy, err := New(repository.New(db), nil, nil).RuntimePolicy()
			if err != nil {
				t.Fatal(err)
			}
			if policy.Storage.AccessURLTTLSeconds != tc.wantAccess || policy.Storage.ProviderAccessURLTTLSeconds != tc.wantProvider {
				t.Fatalf("storage policy = %+v, want access %d and provider %d", policy.Storage, tc.wantAccess, tc.wantProvider)
			}
		})
	}
}

func TestPublicRuntimeLimitsStoredFileGB(t *testing.T) {
	for _, configured := range []bool{false, true} {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqlDB.Close() })
		if err := db.AutoMigrate(&model.SystemSetting{}); err != nil {
			t.Fatal(err)
		}
		policy := DefaultRuntimePolicy()
		if configured {
			policy.Resource.StoredFileGB = 37
			data, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&model.SystemSetting{Key: runtimePolicySettingKey, ValueJSON: string(data)}).Error; err != nil {
				t.Fatal(err)
			}
		}
		limits, err := New(repository.New(db), nil, nil).PublicRuntimeLimits()
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(limits)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if got := fields["storedFileGB"]; got != float64(policy.Resource.StoredFileGB) {
			t.Errorf("configured=%v: storedFileGB = %v, want %d; JSON=%s", configured, got, policy.Resource.StoredFileGB, data)
		}
	}
}

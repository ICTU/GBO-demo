package main

import "testing"

func TestLoadConfig(t *testing.T) {
	t.Setenv("PORT", "9999")
	t.Setenv("DATABASE_PATH", ":memory:")
	t.Setenv("SEED_DEMO_DATA", "true")
	cfg := loadConfig()
	if cfg.Port != "9999" || cfg.DatabasePath != ":memory:" || !cfg.SeedDemoData {
		t.Fatalf("config = %+v", cfg)
	}
}

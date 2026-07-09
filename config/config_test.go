package config

import (
	"testing"
	"time"
)

// clearEnv unsets every env var Load reads, so a test starts from a known state.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"SERVICE_NAME", "PORT", "VERSION", "ENV",
		"TRACING_ENABLED", "OTEL_COLLECTOR_ENDPOINT", "OTEL_SAMPLE_RATE",
		"PROFILING_ENABLED", "PYROSCOPE_ENDPOINT",
		"LOG_LEVEL", "LOG_FORMAT",
		"DB_HOST", "DB_PORT", "DB_NAME", "DB_USER", "DB_PASSWORD", "DB_SSLMODE",
		"DB_POOL_MAX_CONNECTIONS", "DB_POOL_MODE", "DB_POOLER_TYPE",
		"CACHE_ENABLED", "CACHE_HOST", "CACHE_PORT", "CACHE_PASSWORD", "CACHE_DB",
		"CACHE_TTL_PRODUCT_LIST", "CACHE_TTL_PRODUCT_DETAIL",
		"GRPC_PORT", "REVIEW_GRPC_ADDR",
		"SHUTDOWN_TIMEOUT", "READINESS_DRAIN_DELAY",
	} {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	c := Load()
	if c.Service.Port != "8080" {
		t.Errorf("Port = %q, want 8080", c.Service.Port)
	}
	if c.Service.Name != defaultServiceName {
		t.Errorf("Name = %q, want %q", c.Service.Name, defaultServiceName)
	}
	if !c.Tracing.Enabled {
		t.Error("Tracing.Enabled = false, want true")
	}
	if c.Tracing.SampleRate != 0.1 {
		t.Errorf("SampleRate = %v, want 0.1", c.Tracing.SampleRate)
	}
	if c.ShutdownTimeout != 10 {
		t.Errorf("ShutdownTimeout = %d, want 10", c.ShutdownTimeout)
	}
	if c.ReadinessDrainDelay != 5 {
		t.Errorf("ReadinessDrainDelay = %d, want 5", c.ReadinessDrainDelay)
	}
}

func TestLoadFromEnv(t *testing.T) {
	clearEnv(t)
	t.Setenv("SERVICE_NAME", "product")
	t.Setenv("PORT", "9000")
	t.Setenv("ENV", "production")
	t.Setenv("TRACING_ENABLED", "false")
	t.Setenv("OTEL_SAMPLE_RATE", "0.5")
	t.Setenv("SHUTDOWN_TIMEOUT", "20s")
	t.Setenv("READINESS_DRAIN_DELAY", "999s") // over max(30) -> default 5
	c := Load()
	if c.Service.Name != "product" || c.Service.Port != "9000" || c.Service.Env != "production" {
		t.Errorf("service = %+v", c.Service)
	}
	if c.Tracing.Enabled {
		t.Error("Tracing.Enabled = true, want false")
	}
	if c.Tracing.SampleRate != 0.5 {
		t.Errorf("tracing = %+v", c.Tracing)
	}
	if c.ShutdownTimeout != 20 {
		t.Errorf("ShutdownTimeout = %d, want 20", c.ShutdownTimeout)
	}
	if c.ReadinessDrainDelay != 5 {
		t.Errorf("ReadinessDrainDelay = %d, want 5 (over-max falls back)", c.ReadinessDrainDelay)
	}
}

func validConfig() *Config {
	c := &Config{}
	c.Service.Name = "product"
	c.Service.Port = "8080"
	c.Service.Env = "production"
	c.Tracing.Enabled = true
	c.Tracing.Endpoint = "otel:4318"
	c.Tracing.SampleRate = 0.1
	c.Tracing.ServiceName = "product"
	c.Profiling.Enabled = true
	c.Profiling.Endpoint = "pyro:4040"
	c.Profiling.ServiceName = "product"
	c.Logging.Level = "info"
	c.Logging.Format = "json"
	return c
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"valid", func(*Config) {}, false},
		{"missing service name", func(c *Config) { c.Service.Name = "" }, true},
		{"default service name", func(c *Config) { c.Service.Name = defaultServiceName }, true},
		{"empty port", func(c *Config) { c.Service.Port = "" }, true},
		{"non-numeric port", func(c *Config) { c.Service.Port = "abc" }, true},
		{"bad env", func(c *Config) { c.Service.Env = "qa" }, true},
		{"tracing endpoint empty", func(c *Config) { c.Tracing.Endpoint = "" }, true},
		{"sample rate too high", func(c *Config) { c.Tracing.SampleRate = 2 }, true},
		{"tracing disabled skips checks", func(c *Config) { c.Tracing.Enabled = false; c.Tracing.Endpoint = "" }, false},
		{"profiling endpoint empty", func(c *Config) { c.Profiling.Endpoint = "" }, true},
		{"bad log level", func(c *Config) { c.Logging.Level = "trace" }, true},
		{"bad log format", func(c *Config) { c.Logging.Format = "xml" }, true},
		{"db host set, name missing", func(c *Config) { c.Database.Host = "h"; c.Database.User = "u"; c.Database.Password = "p" }, true},
		{"db host set, complete", func(c *Config) {
			c.Database.Host = "h"
			c.Database.Name = "n"
			c.Database.User = "u"
			c.Database.Password = "p"
			c.Database.Port = "5432"
		}, false},
		{"db bad port", func(c *Config) {
			c.Database.Host = "h"
			c.Database.Name = "n"
			c.Database.User = "u"
			c.Database.Password = "p"
			c.Database.Port = "abc"
		}, true},
		{"cache enabled, host missing", func(c *Config) { c.Cache.Enabled = true; c.Cache.Host = "" }, true},
		{"cache bad port", func(c *Config) { c.Cache.Enabled = true; c.Cache.Host = "h"; c.Cache.Port = "abc" }, true},
		{"cache negative db", func(c *Config) { c.Cache.Enabled = true; c.Cache.Host = "h"; c.Cache.DB = -1 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(c)
			err := c.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestBuildDSN(t *testing.T) {
	db := &DatabaseConfig{Host: "localhost", Port: "5432", Name: "product", User: "product", Password: "secret", SSLMode: "disable"}
	got := db.BuildDSN()
	want := "postgresql://product:secret@localhost:5432/product?sslmode=disable"
	if got != want {
		t.Errorf("BuildDSN() = %q, want %q", got, want)
	}
}

func TestEnvHelpers(t *testing.T) {
	clearEnv(t)
	if getEnv("PORT", "x") != "x" {
		t.Error("getEnv default failed")
	}
	t.Setenv("PORT", "1")
	if getEnv("PORT", "x") != "1" {
		t.Error("getEnv value failed")
	}
	t.Setenv("TRACING_ENABLED", "yes")
	if !getEnvBool("TRACING_ENABLED", false) {
		t.Error("getEnvBool yes failed")
	}
	if getEnvInt("CACHE_DB", 7) != 7 {
		t.Error("getEnvInt default failed")
	}
	t.Setenv("CACHE_DB", "bad")
	if getEnvInt("CACHE_DB", 7) != 7 {
		t.Error("getEnvInt bad-value fallback failed")
	}
	t.Setenv("OTEL_SAMPLE_RATE", "bad")
	if getEnvFloat("OTEL_SAMPLE_RATE", 0.2) != 0.2 {
		t.Error("getEnvFloat bad-value fallback failed")
	}
}

// TestDurationHelpers exercises the duration env helpers with VARIED keys,
// defaults (and max) — not just the production constants — so every branch is
// covered and the params are genuinely tested (also keeps unparam quiet).
func TestDurationHelpers(t *testing.T) {
	// getEnvDuration -> time.Duration
	if getEnvDuration("D_UNSET", 3*time.Second) != 3*time.Second {
		t.Error("getEnvDuration: unset should return default")
	}
	t.Setenv("D_VALID", "2m")
	if getEnvDuration("D_VALID", time.Second) != 2*time.Minute {
		t.Error("getEnvDuration: valid value should parse")
	}
	t.Setenv("D_BAD", "nope")
	if getEnvDuration("D_BAD", 4*time.Second) != 4*time.Second {
		t.Error("getEnvDuration: invalid should return default")
	}
	t.Setenv("D_NEG", "-1s")
	if getEnvDuration("D_NEG", 6*time.Second) != 6*time.Second {
		t.Error("getEnvDuration: non-positive should return default")
	}

	// getEnvDurationSeconds -> int (cap 60)
	if getEnvDurationSeconds("DS_UNSET", 10) != 10 {
		t.Error("getEnvDurationSeconds: unset should return default")
	}
	t.Setenv("DS_VALID", "20s")
	if getEnvDurationSeconds("DS_VALID", 7) != 20 {
		t.Error("getEnvDurationSeconds: valid value should parse")
	}
	t.Setenv("DS_BAD", "bad")
	if getEnvDurationSeconds("DS_BAD", 5) != 5 {
		t.Error("getEnvDurationSeconds: invalid should return default")
	}
	t.Setenv("DS_OVERMAX", "999s")
	if getEnvDurationSeconds("DS_OVERMAX", 3) != 3 {
		t.Error("getEnvDurationSeconds: over-max should return default")
	}

	// getEnvDurationSecondsWithMax -> int (caller-supplied max)
	if getEnvDurationSecondsWithMax("DSM_UNSET", 5, 30) != 5 {
		t.Error("getEnvDurationSecondsWithMax: unset should return default")
	}
	t.Setenv("DSM_VALID", "15s")
	if getEnvDurationSecondsWithMax("DSM_VALID", 4, 20) != 15 {
		t.Error("getEnvDurationSecondsWithMax: valid value should parse")
	}
	t.Setenv("DSM_BAD", "x")
	if getEnvDurationSecondsWithMax("DSM_BAD", 8, 25) != 8 {
		t.Error("getEnvDurationSecondsWithMax: invalid should return default")
	}
	t.Setenv("DSM_OVERMAX", "60s")
	if getEnvDurationSecondsWithMax("DSM_OVERMAX", 9, 40) != 9 {
		t.Error("getEnvDurationSecondsWithMax: over-max should return default")
	}
}

func TestPredicatesContainsAndDurations(t *testing.T) {
	c := &Config{}
	c.Service.Env = "dev"
	if !c.IsDevelopment() || c.IsProduction() {
		t.Error("dev predicates wrong")
	}
	c.Service.Env = "prod"
	if c.IsDevelopment() || !c.IsProduction() {
		t.Error("prod predicates wrong")
	}
	c.ShutdownTimeout = 15
	if c.GetShutdownTimeoutDuration() != 15*time.Second {
		t.Error("GetShutdownTimeoutDuration wrong")
	}
	c.ReadinessDrainDelay = 5
	if c.GetReadinessDrainDelayDuration() != 5*time.Second {
		t.Error("GetReadinessDrainDelayDuration wrong")
	}

	if !contains([]string{"a", "B", "c"}, "b") {
		t.Error("contains: case-insensitive match expected")
	}
	if contains([]string{"a", "b"}, "z") {
		t.Error("contains: no match expected")
	}
}

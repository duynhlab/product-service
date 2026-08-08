package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestLoggingMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LoggingMiddleware(zap.NewNop()))
	r.GET("/ok", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	r.GET("/bad", func(c *gin.Context) { c.String(http.StatusInternalServerError, "err") })

	for _, tc := range []struct {
		path string
		want int
	}{{"/ok", http.StatusOK}, {"/bad", http.StatusInternalServerError}} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("%s: code = %d, want %d", tc.path, w.Code, tc.want)
		}
		if w.Header().Get(TraceIDHeader) == "" {
			t.Errorf("%s: missing %s response header", tc.path, TraceIDHeader)
		}
	}
}

func TestGetTraceID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func(header, value string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		if header != "" {
			c.Request.Header.Set(header, value)
		}
		return c
	}

	if got := GetTraceID(newCtx(TraceParentHeader, "00-abcd1234-parent01-01")); got != "abcd1234" {
		t.Errorf("traceparent: GetTraceID = %q, want abcd1234", got)
	}
	if got := GetTraceID(newCtx(TraceIDHeader, "xyz")); got != "xyz" {
		t.Errorf("x-trace-id: GetTraceID = %q, want xyz", got)
	}
	if got := GetTraceID(newCtx("", "")); len(got) != 32 {
		t.Errorf("generated: len = %d, want 32 hex chars", len(got))
	}
}

func TestGetLoggerFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := zap.NewNop()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if got := GetLoggerFromContext(c, base); got != base {
		t.Error("no trace_id in context should return the base logger")
	}
	c.Set("trace_id", "t1")
	if got := GetLoggerFromContext(c, base); got == nil {
		t.Error("with trace_id should return a derived logger")
	}
}

// observedLogger returns a logger whose records land in the returned slice.
func observedLogger() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return zap.New(core), logs
}

// The access log must skip routine SUCCESSFUL probes and keep failing ones —
// the contract in docs/api/observability.md claims this middleware shares
// TracingMiddleware's skip list, and telemetry audit F-2 found it did not.
func TestLoggingMiddlewareSkipsSuccessfulProbesOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name       string
		path       string
		status     int
		wantRecord bool
	}{
		{"healthy probe is silent", "/health", http.StatusOK, false},
		{"ready probe is silent", "/readyz", http.StatusOK, false},
		{"metrics scrape is silent", "/metrics", http.StatusOK, false},
		{"FAILING probe is logged", "/health", http.StatusServiceUnavailable, true},
		{"real traffic is logged", "/product/v1/public/products", http.StatusOK, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, logs := observedLogger()
			r := gin.New()
			r.Use(LoggingMiddleware(logger))
			r.GET(tc.path, func(c *gin.Context) { c.String(tc.status, "x") })

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))

			got := logs.FilterMessage("HTTP request").Len()
			if tc.wantRecord && got != 1 {
				t.Errorf("%s %d: got %d access-log records, want 1", tc.path, tc.status, got)
			}
			if !tc.wantRecord && got != 0 {
				t.Errorf("%s %d: got %d access-log records, want 0", tc.path, tc.status, got)
			}
		})
	}
}

// A rejected request is not a broken service: observability.md's error-ownership
// rule says expected business rejections must not read as infrastructure errors.
func TestLoggingMiddlewareLevelByStatusClass(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		status int
		want   zapcore.Level
	}{
		{http.StatusOK, zapcore.InfoLevel},
		{http.StatusNotFound, zapcore.WarnLevel},
		{http.StatusConflict, zapcore.WarnLevel},
		{http.StatusInternalServerError, zapcore.ErrorLevel},
	} {
		logger, logs := observedLogger()
		r := gin.New()
		r.Use(LoggingMiddleware(logger))
		r.GET("/x", func(c *gin.Context) { c.String(tc.status, "x") })

		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

		rec := logs.FilterMessage("HTTP request").All()
		if len(rec) != 1 {
			t.Fatalf("status %d: got %d records, want 1", tc.status, len(rec))
		}
		if rec[0].Level != tc.want {
			t.Errorf("status %d: level = %s, want %s", tc.status, rec[0].Level, tc.want)
		}
	}
}

// Without an active span there is no trace to join, so the record must carry no
// trace_id at all rather than a generated one (telemetry audit F-1).
func TestLoggingMiddlewareOmitsTraceIDWithoutSpan(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger, logs := observedLogger()
	r := gin.New()
	r.Use(LoggingMiddleware(logger))
	r.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "x") })

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	rec := logs.FilterMessage("HTTP request").All()
	if len(rec) != 1 {
		t.Fatalf("got %d records, want 1", len(rec))
	}
	for _, f := range rec[0].Context {
		if f.Key == "trace_id" {
			t.Errorf("no span, yet the record carries trace_id=%q — a fabricated id joins to nothing", f.String)
		}
	}
	// The response header keeps correlate-by-header working for clients.
	if w.Header().Get(TraceIDHeader) == "" {
		t.Errorf("missing %s response header", TraceIDHeader)
	}
}

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
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

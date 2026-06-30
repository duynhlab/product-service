package middleware

import (
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func TestNewLogger(t *testing.T) {
	l, err := NewLogger()
	if err != nil {
		t.Fatalf("NewLogger() error = %v", err)
	}
	if l == nil {
		t.Fatal("NewLogger() returned nil logger")
	}
}

func TestNewDevelopmentLogger(t *testing.T) {
	l, err := NewDevelopmentLogger()
	if err != nil {
		t.Fatalf("NewDevelopmentLogger() error = %v", err)
	}
	if l == nil {
		t.Fatal("NewDevelopmentLogger() returned nil logger")
	}
}

func TestGetLoggerFromGinContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("returns the logger stored in the context", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		want := zap.NewNop()
		c.Set("logger", want)
		if got := GetLoggerFromGinContext(c); got != want {
			t.Error("GetLoggerFromGinContext did not return the stored logger")
		}
	})

	t.Run("falls back to a new logger when absent", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		if got := GetLoggerFromGinContext(c); got == nil {
			t.Error("GetLoggerFromGinContext returned nil fallback")
		}
	})

	t.Run("falls back when the stored value is the wrong type", func(t *testing.T) {
		c, _ := gin.CreateTestContext(nil)
		c.Set("logger", "not-a-logger")
		if got := GetLoggerFromGinContext(c); got == nil {
			t.Error("GetLoggerFromGinContext returned nil on type mismatch")
		}
	})
}

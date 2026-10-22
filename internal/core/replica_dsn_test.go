package database

import (
	"net/url"
	"testing"

	"github.com/duynhlab/product-service/config"
)

// The replica DSN is the primary DSN plus the PgDog routing option, with the
// credentials and sslmode left as they were.
func TestReplicaDSN(t *testing.T) {
	cfg := config.DatabaseConfig{Host: "pgdog-product", Port: "6432", Name: "product", User: "product_runtime", Password: "p@ss", SSLMode: "verify-full"}
	u, err := url.Parse(replicaDSN(cfg))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if got := q.Get("options"); got != "-c pgdog.role=replica" {
		t.Errorf("options = %q, want -c pgdog.role=replica", got)
	}
	if got := q.Get("sslmode"); got != "verify-full" {
		t.Errorf("sslmode = %q, want verify-full", got)
	}
	if pw, _ := u.User.Password(); pw != "p@ss" || u.Host != "pgdog-product:6432" || u.Path != "/product" {
		t.Errorf("primary DSN parts changed: %s", u.Redacted())
	}
}

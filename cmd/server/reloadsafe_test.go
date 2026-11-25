package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestReloadSafeOpts(t *testing.T) {
	t.Setenv("WACALLS_RELOAD_SAFE", "")
	if reloadSafeFrom(nil, 0).enabled {
		t.Fatal("padrão deve ser desligado")
	}
	tr := true
	o := reloadSafeFrom(&tr, 9999)
	if !o.enabled || o.grace != reloadSafeMaxGrace {
		t.Fatalf("%+v", o)
	}
	t.Setenv("WACALLS_RELOAD_SAFE", "1")
	if !reloadSafeFrom(nil, 0).enabled {
		t.Fatal("env deve ligar")
	}
	f := false
	if reloadSafeFrom(&f, 0).enabled {
		t.Fatal("pedido false vence o env")
	}
	q := reloadSafeFromQuery(httptest.NewRequest("GET", "/x?reloadSafe=0&graceSeconds=30", nil))
	if q.enabled || q.grace != 30*time.Second {
		t.Fatalf("%+v", q)
	}
}

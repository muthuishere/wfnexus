package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
)

// A running service module reaches its own door off loopback with its scoped
// token, and with nothing else; the operator routes stay behind sign-in.
func TestAServiceReachesItsOwnDoorWithItsScopedTokenOnly(t *testing.T) {
	h, st := testServer(t, ":8090")
	ctx := context.Background()
	reg, err := st.SettingOnce(ctx, "runner.token", auth.NewToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutState(ctx, "service", "napper/ops", "spec", `{"module":"napper","account":"ops"}`); err != nil {
		t.Fatal(err)
	}
	if err := st.PutState(ctx, "service", "napper/ops", "watch:w", `{"name":"w","chat":"*","hook":"h"}`); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(reg))
	mac.Write([]byte("service:napper/ops"))
	tok := "wfs_" + hex.EncodeToString(mac.Sum(nil))

	if w := do(h, "POST", "/api/service-self/napper/ops/heartbeat", "wfs_wrong", nil); w.Code != 401 {
		t.Fatalf("wrong token: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/service-self/napper/other/heartbeat", tok, nil); w.Code != 401 {
		t.Fatalf("another service's door: %d %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/service-self/napper/ops/heartbeat", tok, nil); w.Code != 200 {
		t.Fatalf("heartbeat: %d %s", w.Code, w.Body)
	}
	if _, ok, _ := st.GetState(ctx, "service", "napper/ops", "beat"); !ok {
		t.Fatal("the service's own beat was not recorded")
	}
	w := do(h, "GET", "/api/service-self/napper/ops/watches", tok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"w"`) {
		t.Fatalf("watches: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/modules/napper/services/ops/watches", tok, nil); w.Code != 401 {
		t.Fatalf("a service token opened an operator route: %d %s", w.Code, w.Body)
	}
}

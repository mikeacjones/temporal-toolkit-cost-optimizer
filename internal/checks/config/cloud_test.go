package config

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/cloudauth"
)

// TestOpsSourceLive reads one Namespace's settings and the account's usage
// through the Cloud Ops API. It only reads. Set TCOPT_CFG_APIKEY_FILE to a
// JSON file with a secretKey field, and TCOPT_CFG_NAMESPACE to the
// Namespace.
func TestOpsSourceLive(t *testing.T) {
	path, ns := os.Getenv("TCOPT_CFG_APIKEY_FILE"), os.Getenv("TCOPT_CFG_NAMESPACE")
	if path == "" || ns == "" {
		t.Skip("set TCOPT_CFG_APIKEY_FILE and TCOPT_CFG_NAMESPACE to read a real account")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var k struct{ SecretKey string }
	if err := json.Unmarshal(b, &k); err != nil {
		t.Fatal(err)
	}
	ops, err := cloudauth.NewOpsWithAPIKey(k.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	defer ops.Close()
	src := OpsSource{Ops: ops}
	ctx := context.Background()

	info, err := src.Namespace(ctx, ns)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("namespace %+v", *info)
	sinks, err := src.ExportSinks(ctx, ns)
	t.Logf("sinks %d, err %v", len(sinks), err)

	end := time.Now().UTC().Truncate(24 * time.Hour)
	usage, err := src.Usage(ctx, end.Add(-3*24*time.Hour), end)
	if err != nil {
		t.Logf("usage: %v", err)
		return
	}
	t.Logf("usage covers %d Namespaces; %s: %+v", len(usage), ns, usage[ns])
}

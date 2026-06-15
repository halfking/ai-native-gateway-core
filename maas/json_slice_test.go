package maas

import (
	"encoding/json"
	"testing"
)

func TestJSONSlice_emptyEncodesAsArray(t *testing.T) {
	type wrap struct {
		Items []LedgerEntry `json:"items"`
	}
	b, err := json.Marshal(wrap{Items: jsonSlice([]LedgerEntry(nil))})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"items":[]}` {
		t.Fatalf("got %s, want empty JSON array", b)
	}
}

func TestAccountView_emptyListsEncodeAsArrays(t *testing.T) {
	v := AccountView{
		Wallet:       WalletView{TenantID: "acme"},
		RecentLedger: jsonSlice([]LedgerEntry(nil)),
		RecentOrders: jsonSlice([]BillingOrder(nil)),
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"recent_ledger", "recent_orders"} {
		raw, ok := m[key]
		if !ok {
			t.Fatalf("missing %s", key)
		}
		if _, isArr := raw.([]any); !isArr {
			t.Fatalf("%s should be array, got %T", key, raw)
		}
	}
}

package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeBalance serves the balance endpoint with status and body, recording the
// Authorization header it was called with.
func fakeBalance(t *testing.T, status int, body string) *string {
	t.Helper()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := deepseekBalanceURL
	deepseekBalanceURL = srv.URL
	t.Cleanup(func() { deepseekBalanceURL = old })
	return &auth
}

// isolateDeepSeekKey leaves no key in the environment or in Reasonix's home.
func isolateDeepSeekKey(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("REASONIX_HOME", home)
	t.Setenv(deepseekKeyVar, "")
	return home
}

func TestDeepSeekUsageReadsTheBalance(t *testing.T) {
	isolateDeepSeekKey(t)
	t.Setenv(deepseekKeyVar, "sk-env")
	auth := fakeBalance(t, http.StatusOK, `{"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"4.20","granted_balance":"0.00","topped_up_balance":"4.20"}]}`)

	usage, err := AgentDeepSeek.Usage(context.Background(), UsageEnv{Now: func() time.Time { return usageNow }})
	require.NoError(t, err)
	require.Equal(t, "Bearer sk-env", *auth)
	require.Equal(t, []Balance{{Amount: "4.20", Currency: "USD", Available: true}}, usage.Balances)
	require.Empty(t, usage.Windows)
	require.Equal(t, usageNow, usage.Updated)
}

func TestDeepSeekUsageFallsBackToReasonixDotEnv(t *testing.T) {
	home := isolateDeepSeekKey(t)
	require.NoError(t, os.WriteFile(filepath.Join(home, ".env"), []byte("OTHER=x\nexport DEEPSEEK_API_KEY=\"sk-file\"\n"), 0o600))
	auth := fakeBalance(t, http.StatusOK, `{"is_available":false,"balance_infos":[{"currency":"CNY","total_balance":"0.00"}]}`)

	usage, err := deepseekUsage(context.Background(), UsageEnv{})
	require.NoError(t, err)
	require.Equal(t, "Bearer sk-file", *auth)
	require.Equal(t, []Balance{{Amount: "0.00", Currency: "CNY", Available: false}}, usage.Balances)
}

func TestDeepSeekUsageErrors(t *testing.T) {
	t.Run("no key", func(t *testing.T) {
		isolateDeepSeekKey(t)
		_, err := deepseekUsage(context.Background(), UsageEnv{})
		require.ErrorContains(t, err, "API キーがありません")
	})
	for name, tc := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"rejected key":  {http.StatusUnauthorized, `{"error":{"message":"Authentication Fails"}}`, "HTTP 401"},
		"malformed":     {http.StatusOK, `{"is_available":`, "parse DeepSeek balance"},
		"no balances":   {http.StatusOK, `{"is_available":true,"balance_infos":[]}`, "残高が返ってきませんでした"},
		"server failed": {http.StatusServiceUnavailable, ``, "HTTP 503"},
	} {
		t.Run(name, func(t *testing.T) {
			isolateDeepSeekKey(t)
			t.Setenv(deepseekKeyVar, "sk-env")
			fakeBalance(t, tc.status, tc.body)
			_, err := deepseekUsage(context.Background(), UsageEnv{})
			require.ErrorContains(t, err, tc.want)
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		isolateDeepSeekKey(t)
		t.Setenv(deepseekKeyVar, "sk-env")
		old := deepseekBalanceURL
		deepseekBalanceURL = "http://127.0.0.1:1"
		t.Cleanup(func() { deepseekBalanceURL = old })
		_, err := deepseekUsage(context.Background(), UsageEnv{})
		require.ErrorContains(t, err, "DeepSeek の残高")
	})
}

func TestDotenvValue(t *testing.T) {
	text := "# comment\nDEEPSEEK_API_KEY_OLD=no\n  DEEPSEEK_API_KEY = 'sk-single'  \nexport OTHER=x"
	require.Equal(t, "sk-single", dotenvValue(text, "DEEPSEEK_API_KEY"))
	require.Equal(t, "x", dotenvValue(text, "OTHER"))
	require.Equal(t, "sk-plain", dotenvValue("DEEPSEEK_API_KEY=sk-plain", "DEEPSEEK_API_KEY"))
	require.Empty(t, dotenvValue("", "DEEPSEEK_API_KEY"))
	require.Empty(t, dotenvValue("DEEPSEEK_API_KEY", "DEEPSEEK_API_KEY"), "a line without = has no value")
}

func TestBalanceSymbol(t *testing.T) {
	require.Equal(t, "$", Balance{Currency: "USD"}.Symbol())
	require.Equal(t, "¥", Balance{Currency: "CNY"}.Symbol())
	require.Equal(t, "EUR ", Balance{Currency: "EUR"}.Symbol())
}

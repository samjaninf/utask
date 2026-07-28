package opsgenie

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/opsgenie/opsgenie-go-sdk-v2/alert"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorder is an OpsGenie api stub, keeping every request it received
type recorder struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
}

func newRecorder() *recorder {
	rec := &recorder{}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.requests = append(rec.requests, r)
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result":"ok","took":0.1,"requestId":"foo"}`))
	}))
	return rec
}

// lastRequest returns the only request the stub received
func (rec *recorder) lastRequest(t *testing.T) *http.Request {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.requests, 1)
	return rec.requests[0]
}

// host returns the stub host, as accepted by the legacy bare host configuration
func (rec *recorder) host() string {
	return strings.TrimPrefix(rec.URL, "http://")
}

func TestNewOpsGenieNotificationSenderAPIURL(t *testing.T) {
	for _, tt := range []struct {
		name string
		// apiurl is built from the stub URL, which is only known at runtime
		apiurl       func(rec *recorder) string
		expectedPath string
	}{
		{
			name:         "base url with a path prefix",
			apiurl:       func(rec *recorder) string { return rec.URL + "/jsm/ops/integration" },
			expectedPath: "/jsm/ops/integration/v2/alerts",
		},
		{
			name:         "base url with a trailing slash",
			apiurl:       func(rec *recorder) string { return rec.URL + "/jsm/ops/" },
			expectedPath: "/jsm/ops/v2/alerts",
		},
		{
			name:         "base url without a path prefix",
			apiurl:       func(rec *recorder) string { return rec.URL },
			expectedPath: "/v2/alerts",
		},
		{
			// legacy form, handled by the sdk itself
			name:         "bare host",
			apiurl:       func(rec *recorder) string { return rec.host() },
			expectedPath: "/v2/alerts",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := newRecorder()
			defer rec.Close()

			ns, err := NewOpsGenieNotificationSender(ZoneDefault, tt.apiurl(rec), "secret", "5s")
			require.NoError(t, err)

			_, err = ns.client.Create(t.Context(), &alert.CreateAlertRequest{
				Message: "hello",
				Alias:   "dead-beef",
			})
			require.NoError(t, err)

			req := rec.lastRequest(t)
			assert.Equal(t, tt.expectedPath, req.URL.Path)
			assert.Equal(t, rec.host(), req.Host)
			assert.Equal(t, "GenieKey secret", req.Header.Get("Authorization"))
		})
	}
}

// TestNewOpsGenieNotificationSenderCloseAPIURL covers the request utask sends the most,
// the only one with both a dynamic path segment and a query string
func TestNewOpsGenieNotificationSenderCloseAPIURL(t *testing.T) {
	rec := newRecorder()
	defer rec.Close()

	ns, err := NewOpsGenieNotificationSender(ZoneDefault, rec.URL+"/jsm/ops/integration", "secret", "5s")
	require.NoError(t, err)

	_, err = ns.client.Close(t.Context(), &alert.CloseAlertRequest{
		IdentifierType:  alert.ALIAS,
		IdentifierValue: "dead-beef",
	})
	require.NoError(t, err)

	req := rec.lastRequest(t)
	assert.Equal(t, "/jsm/ops/integration/v2/alerts/dead-beef/close", req.URL.Path)
	assert.Equal(t, "alias", req.URL.Query().Get("identifierType"))
}

func TestNewOpsGenieNotificationSenderErrors(t *testing.T) {
	for _, tt := range []struct {
		name          string
		zone, apiurl  string
		expectedError string
	}{
		{
			name:          "unknown zone",
			zone:          "nowhere",
			expectedError: `opsgenie zone "nowhere"`,
		},
		{
			name:          "missing host",
			zone:          ZoneDefault,
			apiurl:        "https:///v2",
			expectedError: "missing host",
		},
		{
			name:          "unsupported url parts",
			zone:          ZoneDefault,
			apiurl:        "https://user:pass@api.atlassian.com/jsm/ops/integration?foo=bar",
			expectedError: "only scheme, host and path are supported",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewOpsGenieNotificationSender(tt.zone, tt.apiurl, "secret", "")
			assert.ErrorContains(t, err, tt.expectedError)
		})
	}
}

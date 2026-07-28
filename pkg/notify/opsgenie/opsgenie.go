package opsgenie

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/juju/errors"
	"github.com/opsgenie/opsgenie-go-sdk-v2/alert"
	"github.com/opsgenie/opsgenie-go-sdk-v2/client"

	"github.com/ovh/utask/models/task"
	"github.com/ovh/utask/pkg/notify"
)

const (
	// Type represents OpsGenie as notify backend
	Type = "opsgenie"

	// Zone are opsgenie api zones
	ZoneSandbox = "sandbox"
	ZoneDefault = "global"
	ZoneEU      = "eu"
)

// NotificationSender is a notify.NotificationSender implementation
// capable of sending formatted notifications over OpsGenie (https://www.atlassian.com/software/opsgenie)
type NotificationSender struct {
	opsGenieZone    string
	opsGenieAPIKey  string
	opsGenieTimeout time.Duration
	client          *alert.Client
}

// NewOpsGenieNotificationSender instantiates a NotificationSender.
// apiurl overrides the API URL derived from zone, it accepts either a bare host
// (e.g. "api.eu.opsgenie.com") or a full base URL, scheme and path prefix included
// (e.g. "https://api.atlassian.com/jsm/ops/integration").
func NewOpsGenieNotificationSender(zone, apiurl, apikey, timeout string) (*NotificationSender, error) {
	cfg := client.Config{ApiKey: apikey}

	switch {
	case apiurl == "":
		zonesToAPIUrls := map[string]client.ApiUrl{
			ZoneDefault: client.API_URL,
			ZoneEU:      client.API_URL_EU,
			ZoneSandbox: client.API_URL_SANDBOX,
		}
		apiURL, present := zonesToAPIUrls[zone]
		if !present {
			return nil, errors.NotFoundf("opsgenie zone %q", zone)
		}
		cfg.OpsGenieAPIURL = apiURL
	case strings.Contains(apiurl, "/"):
		// the sdk only knows how to target a host, requests have to be rewritten
		// to honor the scheme and the path prefix of the configured base URL
		base, err := parseBaseURL(apiurl)
		if err != nil {
			return nil, err
		}
		cfg.OpsGenieAPIURL = client.ApiUrl(base.Host)
		cfg.HttpClient = &http.Client{Transport: &baseURLTransport{base: base}}
	default:
		cfg.OpsGenieAPIURL = client.ApiUrl(apiurl)
	}

	client, err := alert.NewClient(&cfg)
	if err != nil {
		return nil, err
	}
	timeoutDuration := 30 * time.Second
	if timeout != "" {
		timeoutDuration, err = time.ParseDuration(timeout)
		if err != nil {
			return nil, err
		}
	}

	return &NotificationSender{
		opsGenieZone:    zone,
		opsGenieAPIKey:  apikey,
		opsGenieTimeout: timeoutDuration,
		client:          client,
	}, nil
}

// parseBaseURL validates a configured OpsGenie base URL, defaulting its scheme to https.
// The trailing slash is trimmed before parsing, to keep Path and RawPath consistent.
func parseBaseURL(apiurl string) (*url.URL, error) {
	if !strings.Contains(apiurl, "://") {
		apiurl = "https://" + apiurl
	}
	base, err := url.Parse(strings.TrimSuffix(apiurl, "/"))
	if err != nil {
		return nil, errors.NewNotValid(err, "invalid opsgenie api url")
	}
	if base.Host == "" {
		return nil, errors.NotValidf("opsgenie api url %q: missing host", apiurl)
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.NotValidf("opsgenie api url %q: only scheme, host and path are supported", apiurl)
	}
	return base, nil
}

// baseURLTransport reroutes the requests built by the OpsGenie sdk to a base URL,
// prefixing their path so that endpoints such as
// https://api.atlassian.com/jsm/ops/integration can be targeted
type baseURLTransport struct {
	base *url.URL
}

func (t *baseURLTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.URL.Scheme = t.base.Scheme
	r.URL.Host = t.base.Host
	r.URL.Path = t.base.Path + r.URL.Path
	if r.URL.RawPath != "" {
		r.URL.RawPath = t.base.EscapedPath() + r.URL.RawPath
	}
	r.Host = ""
	return http.DefaultTransport.RoundTrip(r)
}

// Send dispatches a notify.Message to OpsGenie
func (ns *NotificationSender) Send(msg *notify.Message, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), ns.opsGenieTimeout)
	defer cancel()

	var err error

	// Generate an alias to support alert deduplication
	// cf. https://support.atlassian.com/opsgenie/docs/what-is-alert-de-duplication/
	alias := msg.TaskID()

	if msg.TaskState() == task.StateDone {
		_, err = ns.client.Close(ctx, &alert.CloseAlertRequest{
			IdentifierType:  alert.ALIAS,
			IdentifierValue: alias,
		})
	} else {
		req := &alert.CreateAlertRequest{
			Message:     msg.MainMessage,
			Description: msg.MainMessage,
			Details:     msg.Fields,
			Alias:       alias,
		}
		msgContent, _ := json.Marshal(msg.Fields)
		if msgContent != nil {
			req.Note = string(msgContent)
		}
		_, err = ns.client.Create(ctx, req)
	}
	if err != nil {
		notify.WrappedSendError(err, msg, Type, name)
	}
}

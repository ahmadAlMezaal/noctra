package ghauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultGitHubWebURL = "https://github.com"

type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type DeviceFlow struct {
	WebURL   string
	ClientID string
	HTTP     *http.Client
	Sleep    func(context.Context, time.Duration) error
}

func NewDeviceFlow(clientID string) *DeviceFlow {
	return &DeviceFlow{
		WebURL:   DefaultGitHubWebURL,
		ClientID: clientID,
		HTTP:     &http.Client{Timeout: 20 * time.Second},
		Sleep:    sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (f *DeviceFlow) Start(ctx context.Context) (DeviceCode, error) {
	var dc DeviceCode
	err := f.post(ctx, "/login/device/code", url.Values{"client_id": {f.ClientID}}, &dc)
	if err == nil && (dc.DeviceCode == "" || dc.UserCode == "") {
		err = errors.New("GitHub returned an incomplete device code; is device flow enabled on the app?")
	}
	return dc, err
}

type tokenPoll struct {
	AccessToken string `json:"access_token"`
	Error       string `json:"error"`
	Description string `json:"error_description"`
	Interval    int    `json:"interval"`
}

func (f *DeviceFlow) Wait(ctx context.Context, dc DeviceCode) (string, error) {
	interval := time.Duration(max(dc.Interval, 5)) * time.Second
	deadline := time.Now().Add(time.Duration(max(dc.ExpiresIn, 60)) * time.Second)
	for time.Now().Before(deadline) {
		if err := f.Sleep(ctx, interval); err != nil {
			return "", err
		}
		var res tokenPoll
		err := f.post(ctx, "/login/oauth/access_token", url.Values{
			"client_id":   {f.ClientID},
			"device_code": {dc.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}, &res)
		if err != nil {
			return "", err
		}
		switch res.Error {
		case "":
			if res.AccessToken == "" {
				return "", errors.New("GitHub returned no access token")
			}
			return res.AccessToken, nil
		case "authorization_pending":
		case "slow_down":
			interval = time.Duration(max(res.Interval, int(interval/time.Second)+5)) * time.Second
		case "expired_token":
			return "", errors.New("the code expired before it was approved; run `noctra github login` again")
		case "access_denied":
			return "", errors.New("authorisation was declined in the browser")
		default:
			return "", fmt.Errorf("GitHub device flow: %s %s", res.Error, res.Description)
		}
	}
	return "", errors.New("the code expired before it was approved; run `noctra github login` again")
}

func (f *DeviceFlow) post(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(f.WebURL, "/")+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "noctra")
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub device flow: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub device flow %s: HTTP %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("GitHub device flow %s: decode response: %w", path, err)
	}
	return nil
}

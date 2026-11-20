package groupsync

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

// Federated is the keyless way in, the same chain the mail relay uses. The
// Google organization forbids service account keys, so there is none:
//
//  1. a Vord token for this client (client credentials), whose audience is the
//     workload identity provider;
//  2. Google's STS turns it into a federated access token;
//  3. that token asks IAM to sign the domain-wide delegation assertion as the
//     service account (it holds roles/iam.serviceAccountTokenCreator on it);
//  4. the signed assertion becomes the Directory API token, as the admin.
//
// Nothing in this struct is a Google credential. ClientSecret is Vord's.
type Federated struct {
	HTTP           *http.Client
	VordTokenURL   string // the realm's token endpoint
	ClientID       string
	ClientSecret   string
	Audience       string // //iam.googleapis.com/projects/N/locations/global/workloadIdentityPools/P/providers/X
	ServiceAccount string // the account that holds the delegation
	STSURL         string // default https://sts.googleapis.com/v1/token
	IAMBase        string // default https://iamcredentials.googleapis.com/v1
}

func (f *Federated) client() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (f *Federated) post(ctx context.Context, u string, form url.Values, bearer string, js any, out any) (int, error) {
	var body io.Reader
	ct := "application/x-www-form-urlencoded"
	if js != nil {
		b, _ := json.Marshal(js)
		body, ct = strings.NewReader(string(b)), "application/json"
	} else {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	req.Header.Set("Content-Type", ct)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = json.Unmarshal(b, out)
	return resp.StatusCode, nil
}

// Signer returns the Sign function for Google.
func (f *Federated) Signer() func(ctx context.Context, claims []byte) (string, error) {
	return func(ctx context.Context, claims []byte) (string, error) {
		if f.ClientID == "" || f.ClientSecret == "" || f.VordTokenURL == "" || f.Audience == "" || f.ServiceAccount == "" {
			return "", errors.New("federated sign in needs the Vord token URL, client id and secret, the provider audience and the service account")
		}
		var vt struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		code, err := f.post(ctx, f.VordTokenURL, url.Values{
			"grant_type": {"client_credentials"}, "client_id": {f.ClientID}, "client_secret": {f.ClientSecret},
		}, "", nil, &vt)
		if err != nil {
			return "", err
		}
		if code != 200 || vt.AccessToken == "" {
			return "", fmt.Errorf("vord token: %d %s", code, vt.Error)
		}
		sts := f.STSURL
		if sts == "" {
			sts = "https://sts.googleapis.com/v1/token"
		}
		var st struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		code, err = f.post(ctx, sts, url.Values{
			"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
			"audience":             {f.Audience},
			"scope":                {"https://www.googleapis.com/auth/cloud-platform"},
			"requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
			"subject_token_type":   {"urn:ietf:params:oauth:token-type:jwt"},
			"subject_token":        {vt.AccessToken},
		}, "", nil, &st)
		if err != nil {
			return "", err
		}
		if code != 200 || st.AccessToken == "" {
			return "", fmt.Errorf("google sts: %d %s (is the Vord client trusted by the provider?)", code, st.Error)
		}
		iam := strings.TrimRight(f.IAMBase, "/")
		if iam == "" {
			iam = "https://iamcredentials.googleapis.com/v1"
		}
		var sj struct {
			SignedJWT string `json:"signedJwt"`
		}
		code, err = f.post(ctx, iam+"/projects/-/serviceAccounts/"+url.PathEscape(f.ServiceAccount)+":signJwt",
			nil, st.AccessToken, map[string]string{"payload": string(claims)}, &sj)
		if err != nil {
			return "", err
		}
		if code != 200 || sj.SignedJWT == "" {
			return "", fmt.Errorf("sign delegation assertion: %d (does the federated identity hold tokenCreator on %s?)", code, f.ServiceAccount)
		}
		return sj.SignedJWT, nil
	}
}

// NewFederatedGoogle is the keyless Directory client.
func NewFederatedGoogle(f *Federated, subject string) (*Google, error) {
	if strings.TrimSpace(subject) == "" {
		return nil, errors.New("an admin subject is required")
	}
	return &Google{Account: f.ServiceAccount, Subject: subject, Sign: f.Signer()}, nil
}

package provider

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cnjack/jcloud/internal/safehttp"
)

// InstallationToken is the short-lived GitHub App credential safe to deliver
// to a run snapshot. The App private key never leaves the control plane.
type InstallationToken struct {
	Token               string
	ExpiresAt           time.Time
	RepositorySelection string
	Permissions         map[string]string
}

// AppInstallation is the non-secret subset returned by GitHub's App API.
type AppInstallation struct {
	ID                  string `json:"id"`
	AccountID           string `json:"account_id"`
	Account             string `json:"account"`
	TargetType          string `json:"target_type"`
	RepositorySelection string `json:"repository_selection"`
}

func (i *GitHubAppIssuer) ListInstallations(ctx context.Context) ([]AppInstallation, error) {
	jwt, err := i.signedJWT()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(i.apiBase, "/")+"/app/installations", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := i.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list GitHub App installations: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("list GitHub App installations: GitHub returned %s", resp.Status)
	}
	var raw []struct {
		ID                  int64  `json:"id"`
		TargetType          string `json:"target_type"`
		RepositorySelection string `json:"repository_selection"`
		Account             struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"account"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode GitHub App installations: %w", err)
	}
	out := make([]AppInstallation, 0, len(raw))
	for _, v := range raw {
		out = append(out, AppInstallation{ID: strconv.FormatInt(v.ID, 10), AccountID: strconv.FormatInt(v.Account.ID, 10), Account: v.Account.Login, TargetType: v.TargetType, RepositorySelection: v.RepositorySelection})
	}
	return out, nil
}

// ListUserInstallations returns only GitHub App installations the authenticated
// GitHub user is allowed to manage. This must be used for Project selection:
// ListInstallations is intentionally cluster-wide and is suitable only for
// Cluster Admin diagnostics.
func (i *GitHubAppIssuer) ListUserInstallations(ctx context.Context, userAccessToken string) ([]AppInstallation, error) {
	userAccessToken = strings.TrimSpace(userAccessToken)
	if userAccessToken == "" {
		return nil, errors.New("GitHub user access token is required")
	}
	out := []AppInstallation{}
	for page := 1; ; page++ {
		url := strings.TrimRight(i.apiBase, "/") + "/user/installations?per_page=100&page=" + strconv.Itoa(page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+userAccessToken)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		resp, err := i.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list manageable GitHub App installations: %w", err)
		}
		var body struct {
			Installations []struct {
				ID                  int64  `json:"id"`
				TargetType          string `json:"target_type"`
				RepositorySelection string `json:"repository_selection"`
				Account             struct {
					ID    int64  `json:"id"`
					Login string `json:"login"`
				} `json:"account"`
			} `json:"installations"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("list manageable GitHub App installations: GitHub returned %s", resp.Status)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode manageable GitHub App installations: %w", decodeErr)
		}
		for _, v := range body.Installations {
			out = append(out, AppInstallation{
				ID: strconv.FormatInt(v.ID, 10), AccountID: strconv.FormatInt(v.Account.ID, 10),
				Account: v.Account.Login, TargetType: v.TargetType, RepositorySelection: v.RepositorySelection,
			})
		}
		if len(body.Installations) < 100 {
			return out, nil
		}
	}
}

// GitHubAppIssuer exchanges a control-plane GitHub App private key for
// short-lived installation tokens. It deliberately has no cache: GitHub tokens
// are cheap to mint and callers control refresh cadence.
type GitHubAppIssuer struct {
	appID      string
	privateKey *rsa.PrivateKey
	apiBase    string
	http       *http.Client
	now        func() time.Time
}

func NewGitHubAppIssuer(appID string, privateKeyPEM []byte) (*GitHubAppIssuer, error) {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, errors.New("GitHub App id is required")
	}
	key, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse GitHub App private key: %w", err)
	}
	return &GitHubAppIssuer{
		appID: appID, privateKey: key, apiBase: "https://api.github.com",
		http: safehttp.NewProviderClient("https://api.github.com", 15*time.Second), now: time.Now,
	}, nil
}

func (i *GitHubAppIssuer) IssueInstallationToken(ctx context.Context, installationID string) (InstallationToken, error) {
	installationID = strings.TrimSpace(installationID)
	if err := ValidateGitHubInstallationID(installationID); err != nil {
		return InstallationToken{}, err
	}
	jwt, err := i.signedJWT()
	if err != nil {
		return InstallationToken{}, err
	}
	url := strings.TrimRight(i.apiBase, "/") + "/app/installations/" + installationID + "/access_tokens"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return InstallationToken{}, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.http.Do(req)
	if err != nil {
		return InstallationToken{}, fmt.Errorf("issue GitHub installation token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return InstallationToken{}, fmt.Errorf("issue GitHub installation token: GitHub returned %s", resp.Status)
	}
	var body struct {
		Token               string            `json:"token"`
		ExpiresAt           time.Time         `json:"expires_at"`
		RepositorySelection string            `json:"repository_selection"`
		Permissions         map[string]string `json:"permissions"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return InstallationToken{}, fmt.Errorf("decode GitHub installation token: %w", err)
	}
	if strings.TrimSpace(body.Token) == "" || body.ExpiresAt.IsZero() {
		return InstallationToken{}, errors.New("GitHub returned an incomplete installation token")
	}
	return InstallationToken{
		Token: body.Token, ExpiresAt: body.ExpiresAt,
		RepositorySelection: body.RepositorySelection, Permissions: body.Permissions,
	}, nil
}

func (i *GitHubAppIssuer) signedJWT() (string, error) {
	now := i.now().UTC()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": i.appID,
	})
	if err != nil {
		return "", err
	}
	unsigned := rawURLBase64(header) + "." + rawURLBase64(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, i.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return unsigned + "." + rawURLBase64(signature), nil
}

func parseRSAPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("PEM block not found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return key, nil
}

func rawURLBase64(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

// ValidateGitHubInstallationID rejects path separators and non-decimal values
// before an id is interpolated into the GitHub API path.
func ValidateGitHubInstallationID(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("installation id is required")
	}
	if _, err := strconv.ParseInt(value, 10, 64); err != nil {
		return errors.New("installation id must be a positive decimal integer")
	}
	if strings.HasPrefix(value, "-") || value == "0" {
		return errors.New("installation id must be a positive decimal integer")
	}
	return nil
}

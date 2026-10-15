// Package cloudauth logs the user in to Temporal Cloud through the browser,
// the same OAuth device flow tcld uses, and then calls the public Cloud Ops
// API as that user: to find their identity, list the Namespaces they can
// reach, and create a short-lived API key that can read histories.
//
// The login token only works with the Cloud Ops API. Namespace endpoints
// take an API key or mTLS, which is why tcopt creates a key after login.
package cloudauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"go.temporal.io/cloud-sdk/api/cloudservice/v1"
	identityv1 "go.temporal.io/cloud-sdk/api/identity/v1"
	"go.temporal.io/cloud-sdk/cloudclient"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/mikeacjones/temporal-toolkit-cost-optimizer/internal/secrets"
)

// The login endpoints and client ID are tcld's. tcopt should get its own
// OAuth client ID before release (PLAN.md section 14); TCOPT_OAUTH_CLIENT_ID
// overrides it until then.
const (
	DefaultDomain   = "login.tmprl.cloud"
	DefaultAudience = "https://saas-api.tmprl.cloud"
	defaultClientID = "d7V5bZMLCbRLfRVpqC567AqjAERaWHhl"

	tokenKey = "cloud-login-token"
)

// ErrNotLoggedIn means there's no stored login, or it can't be refreshed.
var ErrNotLoggedIn = errors.New("not logged in to Temporal Cloud")

// Auth runs the login flow and holds the resulting token.
type Auth struct {
	domain   string
	audience string
	oauth    oauth2.Config

	mu    sync.Mutex
	token *oauth2.Token
}

// New returns an Auth for the default Temporal Cloud login domain.
func New() *Auth {
	clientID := os.Getenv("TCOPT_OAUTH_CLIENT_ID")
	if clientID == "" {
		clientID = defaultClientID
	}
	domain := "https://" + DefaultDomain
	return &Auth{
		domain:   domain,
		audience: DefaultAudience,
		oauth: oauth2.Config{
			ClientID: clientID,
			Endpoint: oauth2.Endpoint{
				DeviceAuthURL: domain + "/oauth/device/code",
				TokenURL:      domain + "/oauth/token",
				AuthStyle:     oauth2.AuthStyleInParams,
			},
			Scopes: []string{"openid", "profile", "user", "offline_access"},
		},
	}
}

// Pending is a login waiting for the user to approve it in the browser.
type Pending struct {
	URL      string // opens the approval page with the code filled in
	UserCode string // the code the page shows, for the user to compare
	Expires  time.Time
	resp     *oauth2.DeviceAuthResponse
}

// Start asks the login server for a device code.
func (a *Auth) Start(ctx context.Context) (*Pending, error) {
	resp, err := a.oauth.DeviceAuth(ctx, oauth2.SetAuthURLParam("audience", a.audience))
	if err != nil {
		return nil, fmt.Errorf("start login: %w", err)
	}
	// Only open a page on the login domain, so a bad response can't send the
	// user somewhere else.
	verify, err := url.Parse(resp.VerificationURIComplete)
	if err != nil {
		return nil, fmt.Errorf("login page URL: %w", err)
	}
	domain, _ := url.Parse(a.domain)
	if verify.Scheme != "https" || verify.Hostname() != domain.Hostname() {
		return nil, fmt.Errorf("login page %q isn't on %s", verify.Hostname(), domain.Hostname())
	}
	return &Pending{URL: verify.String(), UserCode: resp.UserCode, Expires: resp.Expiry, resp: resp}, nil
}

// Wait blocks until the user approves the login, it expires, or ctx ends,
// then stores the token in the keychain.
func (a *Auth) Wait(ctx context.Context, p *Pending) error {
	tok, err := a.oauth.DeviceAccessToken(ctx, p.resp)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	return a.setToken(tok)
}

// Load reads a stored login from the keychain. It returns ErrNotLoggedIn
// when there isn't one.
func (a *Auth) Load() error {
	raw, err := secrets.Get(tokenKey)
	if errors.Is(err, secrets.ErrNotFound) {
		return ErrNotLoggedIn
	}
	if err != nil {
		return err
	}
	tok := &oauth2.Token{}
	if err := json.Unmarshal([]byte(raw), tok); err != nil {
		return ErrNotLoggedIn
	}
	a.mu.Lock()
	a.token = tok
	a.mu.Unlock()
	return nil
}

// Logout forgets the stored login.
func (a *Auth) Logout() error {
	a.mu.Lock()
	a.token = nil
	a.mu.Unlock()
	return secrets.Delete(tokenKey)
}

func (a *Auth) setToken(tok *oauth2.Token) error {
	a.mu.Lock()
	a.token = tok
	a.mu.Unlock()
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return secrets.Set(tokenKey, string(b))
}

// GetAPIKey returns a current access token, refreshing it when it has
// expired. It implements cloudclient.APIKeyReader.
func (a *Auth) GetAPIKey(ctx context.Context) (string, error) {
	a.mu.Lock()
	tok := a.token
	a.mu.Unlock()
	if tok == nil {
		return "", ErrNotLoggedIn
	}
	if tok.Valid() {
		return tok.AccessToken, nil
	}
	if tok.RefreshToken == "" {
		return "", ErrNotLoggedIn
	}
	fresh, err := a.oauth.TokenSource(ctx, tok).Token()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotLoggedIn, err)
	}
	if err := a.setToken(fresh); err != nil {
		return "", err
	}
	return fresh.AccessToken, nil
}

// Session is a Cloud Ops API client acting as the logged-in user.
type Session struct {
	client *cloudclient.Client
	UserID string
	Email  string
}

// Connect opens a Cloud Ops API client with the login token and looks up
// who the user is.
func (a *Auth) Connect(ctx context.Context) (*Session, error) {
	if _, err := a.GetAPIKey(ctx); err != nil {
		return nil, err
	}
	c, err := cloudclient.New(cloudclient.Options{APIKeyReader: a, UserAgent: "tcopt"})
	if err != nil {
		return nil, err
	}
	resp, err := c.CloudService().GetCurrentIdentity(ctx, &cloudservice.GetCurrentIdentityRequest{})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("look up your Temporal Cloud user: %w", err)
	}
	u := resp.GetUser()
	if u == nil {
		c.Close()
		return nil, errors.New("this login isn't a Temporal Cloud user")
	}
	return &Session{client: c, UserID: u.GetId(), Email: u.GetSpec().GetEmail()}, nil
}

// Close closes the client.
func (s *Session) Close() { s.client.Close() }

// Namespace is one Namespace the user can see.
type Namespace struct {
	Name       string
	Address    string // the Namespace's gRPC endpoint
	Region     string
	APIKeyAuth bool // tcopt connects with API keys, so it needs this on
}

// Namespaces lists the Namespaces the user can see, sorted by name.
func (s *Session) Namespaces(ctx context.Context) ([]Namespace, error) {
	var out []Namespace
	token := ""
	for {
		resp, err := s.client.CloudService().GetNamespaces(ctx, &cloudservice.GetNamespacesRequest{PageSize: 100, PageToken: token})
		if err != nil {
			return nil, fmt.Errorf("list Namespaces: %w", err)
		}
		for _, ns := range resp.GetNamespaces() {
			out = append(out, Namespace{
				Name:       ns.GetNamespace(),
				Address:    ns.GetEndpoints().GetGrpcAddress(),
				Region:     ns.GetActiveRegion(),
				APIKeyAuth: ns.GetSpec().GetApiKeyAuth().GetEnabled(),
			})
		}
		token = resp.GetNextPageToken()
		if token == "" {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Key is an API key tcopt created for the user.
type Key struct {
	ID      string
	Name    string
	Secret  string
	Expires time.Time
}

// CreateKey creates an API key owned by the user. It has the user's own
// permissions and expires after ttl.
func (s *Session) CreateKey(ctx context.Context, name string, ttl time.Duration) (*Key, error) {
	expires := time.Now().Add(ttl).UTC().Truncate(time.Second)
	resp, err := s.client.CloudService().CreateApiKey(ctx, &cloudservice.CreateApiKeyRequest{
		Spec: &identityv1.ApiKeySpec{
			OwnerId:     s.UserID,
			OwnerType:   identityv1.OwnerType_OWNER_TYPE_USER,
			DisplayName: name,
			Description: "Created by tcopt to read Workflow histories",
			ExpiryTime:  timestamppb.New(expires),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create API key: %w", err)
	}
	return &Key{ID: resp.GetKeyId(), Name: name, Secret: resp.GetToken(), Expires: expires}, nil
}

// KeyName returns the display name for a key created on this machine.
func KeyName() string {
	host, _ := os.Hostname()
	host = strings.Split(host, ".")[0]
	if host == "" {
		host = "local"
	}
	return "tcopt-" + strings.ToLower(host)
}

// OpenBrowser opens a URL in the default browser. Failing to open it isn't
// fatal; the TUI shows the URL too.
func OpenBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "linux":
		cmd = exec.Command("xdg-open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		return fmt.Errorf("can't open a browser on %s", runtime.GOOS)
	}
	return cmd.Start()
}

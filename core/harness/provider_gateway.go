package harness

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/core/workers"
	"github.com/telemetryos/tos-tag/types"
)

const providerGatewayBodyLimit = 32 << 20

type ProviderGatewayOptions struct {
	ProviderID string
	BaseURL    string
	APIKey     string
	Timeout    time.Duration
	Jobs       jobs.Queue
}

type ProviderGatewayScope struct {
	AttemptID     string
	JobID         string
	LeaseToken    string
	SteeringEpoch int64
	ExpiresAt     time.Time
}

// ProviderGateway keeps the upstream provider credential in the control plane.
// Disposable workers receive only an attempt-scoped loopback capability.
type ProviderGateway struct {
	mu         sync.Mutex
	providerID string
	upstream   *url.URL
	apiKey     string
	tokens     map[string]ProviderGatewayScope
	jobs       jobs.Queue
	server     *http.Server
	listener   net.Listener
	proxy      *httputil.ReverseProxy
}

func NewProviderGateway(options ProviderGatewayOptions) (*ProviderGateway, error) {
	baseURL, err := url.Parse(strings.TrimRight(strings.TrimSpace(options.BaseURL), "/"))
	if err != nil || baseURL.Host == "" || (baseURL.Scheme != "https" && baseURL.Scheme != "http") || baseURL.User != nil {
		return nil, errors.New("provider gateway requires an HTTP(S) upstream base URL")
	}
	if options.ProviderID == "" || options.APIKey == "" || options.Timeout <= 0 || options.Jobs == nil {
		return nil, errors.New("provider gateway requires provider ID, upstream credential, timeout, and job queue")
	}
	gateway := &ProviderGateway{
		providerID: options.ProviderID,
		upstream:   baseURL,
		apiKey:     options.APIKey,
		tokens:     make(map[string]ProviderGatewayScope),
		jobs:       options.Jobs,
	}
	gateway.proxy = &httputil.ReverseProxy{
		Rewrite: func(proxyRequest *httputil.ProxyRequest) {
			contentType := proxyRequest.In.Header.Get("Content-Type")
			accept := proxyRequest.In.Header.Get("Accept")
			beta := proxyRequest.In.Header.Get("OpenAI-Beta")
			target := *gateway.upstream
			target.Path = proxyRequest.In.URL.Path
			target.RawPath = proxyRequest.In.URL.RawPath
			target.RawQuery = proxyRequest.In.URL.RawQuery
			proxyRequest.Out.URL = &target
			proxyRequest.Out.Host = target.Host
			proxyRequest.Out.Header = make(http.Header)
			proxyRequest.Out.Header.Set("Authorization", "Bearer "+gateway.apiKey)
			if contentType != "" {
				proxyRequest.Out.Header.Set("Content-Type", contentType)
			}
			if accept != "" {
				proxyRequest.Out.Header.Set("Accept", accept)
			}
			if beta != "" {
				proxyRequest.Out.Header.Set("OpenAI-Beta", beta)
			}
		},
		Transport:     &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: options.Timeout},
		FlushInterval: -1,
		ErrorHandler: func(response http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(response, "provider unavailable", http.StatusBadGateway)
		},
	}
	return gateway, nil
}

func (g *ProviderGateway) Register(scope ProviderGatewayScope) (workers.ProviderRoute, error) {
	if scope.AttemptID == "" || scope.JobID == "" || scope.LeaseToken == "" || scope.SteeringEpoch <= 0 || !scope.ExpiresAt.After(time.Now().UTC()) {
		return workers.ProviderRoute{}, errors.New("provider gateway scope is incomplete")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.startLocked(); err != nil {
		return workers.ProviderRoute{}, err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return workers.ProviderRoute{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	g.tokens[token] = scope
	return workers.ProviderRoute{ID: g.providerID, BaseURL: "http://" + g.listener.Addr().String() + g.upstream.EscapedPath(), Token: token}, nil
}

func (g *ProviderGateway) Revoke(attemptID string) {
	if g == nil || attemptID == "" {
		return
	}
	g.mu.Lock()
	for token, scope := range g.tokens {
		if scope.AttemptID == attemptID {
			delete(g.tokens, token)
		}
	}
	g.mu.Unlock()
}

func (g *ProviderGateway) Close(ctx context.Context) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	server := g.server
	g.server = nil
	g.listener = nil
	g.tokens = make(map[string]ProviderGatewayScope)
	g.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown(ctx)
}

func (g *ProviderGateway) startLocked() error {
	if g.server != nil {
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: g, ReadHeaderTimeout: 5 * time.Second}
	g.listener = listener
	g.server = server
	go func() { _ = server.Serve(listener) }()
	return nil
}

func (g *ProviderGateway) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost && request.Method != http.MethodGet {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !strings.HasPrefix(request.URL.Path, g.upstream.EscapedPath()+"/") && request.URL.Path != g.upstream.EscapedPath() {
		http.Error(response, "provider path denied", http.StatusForbidden)
		return
	}
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	if !g.authorized(request.Context(), token) {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, providerGatewayBodyLimit)
	g.proxy.ServeHTTP(response, request)
}

func (g *ProviderGateway) authorized(ctx context.Context, token string) bool {
	if token == "" {
		return false
	}
	g.mu.Lock()
	var scope ProviderGatewayScope
	for candidate, candidateScope := range g.tokens {
		if len(candidate) == len(token) && subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			scope = candidateScope
		}
	}
	g.mu.Unlock()
	if scope.AttemptID == "" || !scope.ExpiresAt.After(time.Now().UTC()) {
		return false
	}
	job, err := g.jobs.Get(ctx, types.JobID(scope.JobID))
	return err == nil && job.State == jobs.StateRunning && job.Lease.Token == scope.LeaseToken && job.SteeringEpoch == scope.SteeringEpoch
}

func (g *ProviderGateway) String() string {
	return fmt.Sprintf("provider-gateway(%s)", g.providerID)
}

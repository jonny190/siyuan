// Package orchestrator drives per-user kernel containers via the Docker HTTP API.
//
// We intentionally do NOT pull in github.com/docker/docker/client: it drags ~40MB of
// transitive dependencies and pins a specific API version. Instead we make raw HTTP calls
// over the unix socket using stdlib http.Client with a custom DialContext. The subset of
// the Docker API we need (containers/*, networks/*) is small, stable, and well-documented.
//
// The same transport works transparently against docker-socket-proxy (Tecnativa) over TCP
// if PORTAL_DOCKER_HOST is set to tcp://docker-proxy:2375, which is the recommended
// defense-in-depth deployment path — see docker-compose.yml.
package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DockerClient is a thin wrapper around http.Client configured to talk to a Docker daemon
// either over a unix socket (unix:///var/run/docker.sock) or over TCP (tcp://host:port).
type DockerClient struct {
	httpClient *http.Client
	// baseURL is the URL prefix used for Docker API calls. For unix sockets we use a
	// synthetic host (docker-api) that the custom transport ignores; for TCP we pass it
	// through unchanged.
	baseURL string
}

// NewDockerClient parses a PORTAL_DOCKER_HOST-style string and returns a ready client.
// Supported schemes: unix:// (default Docker socket path) and tcp://.
func NewDockerClient(host string) (*DockerClient, error) {
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parse docker host %q: %w", host, err)
	}

	transport := &http.Transport{
		MaxIdleConns:    8,
		IdleConnTimeout: 30 * time.Second,
	}

	switch u.Scheme {
	case "unix":
		// Path is the socket path. Dial unix ignores the host field entirely.
		socketPath := u.Path
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		}
		// Synthetic HTTP base: the host name is discarded by DialContext, but
		// net/http still insists on a valid Host header, so any placeholder works.
		return &DockerClient{
			httpClient: &http.Client{Transport: transport, Timeout: 60 * time.Second},
			baseURL:    "http://docker-api",
		}, nil

	case "tcp":
		// Straight HTTP over TCP to an API proxy. We do not configure TLS here because
		// the recommended topology is docker-socket-proxy on an internal-only network.
		return &DockerClient{
			httpClient: &http.Client{Transport: transport, Timeout: 60 * time.Second},
			baseURL:    "http://" + u.Host,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported docker host scheme %q", u.Scheme)
	}
}

// apiVersion pins the Docker Engine API version we target. 1.41 shipped with Docker 20.10
// (November 2020), which is the oldest version we still care about supporting. Newer
// daemons accept it via version negotiation.
const apiVersion = "v1.41"

// call performs a single Docker API request and decodes the response body into out (which
// may be nil if the caller doesn't care about the body). A non-2xx response is turned
// into a structured dockerError.
func (c *DockerClient) call(ctx context.Context, method, path string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal docker request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	endpoint := c.baseURL + "/" + apiVersion + path
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reqBody)
	if err != nil {
		return fmt.Errorf("build docker request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("docker %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(resp.Body)
		return &DockerError{
			Status:  resp.StatusCode,
			Path:    path,
			Method:  method,
			Message: strings.TrimSpace(string(msg)),
		}
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("decode docker response: %w", err)
		}
	}
	return nil
}

// DockerError is a structured error returned from the Docker API. The orchestrator uses
// IsNotFound to distinguish "container does not exist" from other failures.
type DockerError struct {
	Status  int
	Method  string
	Path    string
	Message string
}

func (e *DockerError) Error() string {
	return fmt.Sprintf("docker %s %s: %d %s", e.Method, e.Path, e.Status, e.Message)
}

// IsNotFound reports whether err is a 404 from the Docker API. Used by Start() to decide
// whether it needs to create the container before starting it.
func IsNotFound(err error) bool {
	var de *DockerError
	if errors.As(err, &de) {
		return de.Status == http.StatusNotFound
	}
	return false
}

// --- minimal types used in our API calls --------------------------------------------------

// ContainerCreateRequest is the subset of the Docker CreateContainer request we populate.
// Fields we don't need are intentionally omitted.
type ContainerCreateRequest struct {
	Image           string              `json:"Image"`
	Cmd             []string            `json:"Cmd,omitempty"`
	Env             []string            `json:"Env,omitempty"`
	ExposedPorts    map[string]struct{} `json:"ExposedPorts,omitempty"`
	Labels          map[string]string   `json:"Labels,omitempty"`
	HostConfig      *HostConfig         `json:"HostConfig,omitempty"`
	NetworkingConfig *NetworkingConfig  `json:"NetworkingConfig,omitempty"`
}

// HostConfig holds resource limits and bind mounts.
type HostConfig struct {
	Binds         []string      `json:"Binds,omitempty"`
	Memory        int64         `json:"Memory,omitempty"`
	PidsLimit     int64         `json:"PidsLimit,omitempty"`
	CPUShares     int64         `json:"CpuShares,omitempty"`
	RestartPolicy RestartPolicy `json:"RestartPolicy,omitempty"`
	NetworkMode   string        `json:"NetworkMode,omitempty"`
	AutoRemove    bool          `json:"AutoRemove,omitempty"`
}

// RestartPolicy mirrors the Docker API type.
type RestartPolicy struct {
	Name string `json:"Name"` // "", "no", "always", "on-failure", "unless-stopped"
}

// NetworkingConfig attaches the container to one or more user-defined networks at create time.
type NetworkingConfig struct {
	EndpointsConfig map[string]EndpointSettings `json:"EndpointsConfig,omitempty"`
}

// EndpointSettings is the per-network settings used when attaching at create time.
type EndpointSettings struct {
	Aliases []string `json:"Aliases,omitempty"`
}

// ContainerCreateResponse is what Docker returns from POST /containers/create.
type ContainerCreateResponse struct {
	ID       string   `json:"Id"`
	Warnings []string `json:"Warnings"`
}

// ContainerInspectResponse is the subset of the inspect response we read.
type ContainerInspectResponse struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Status   string `json:"Status"` // "created","running","paused","restarting","removing","exited","dead"
		Running  bool   `json:"Running"`
		ExitCode int    `json:"ExitCode"`
		Error    string `json:"Error"`
	} `json:"State"`
}

// --- public methods ------------------------------------------------------------------------

// CreateContainer POSTs /containers/create?name=<name>.
func (c *DockerClient) CreateContainer(ctx context.Context, name string, spec ContainerCreateRequest) (*ContainerCreateResponse, error) {
	path := "/containers/create?name=" + url.QueryEscape(name)
	var resp ContainerCreateResponse
	if err := c.call(ctx, http.MethodPost, path, spec, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// StartContainer POSTs /containers/<id>/start.
func (c *DockerClient) StartContainer(ctx context.Context, nameOrID string) error {
	return c.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(nameOrID)+"/start", nil, nil)
}

// StopContainer POSTs /containers/<id>/stop?t=<timeoutSec>. A zero timeoutSec tells Docker
// to skip SIGTERM and go straight to SIGKILL; non-zero values wait N seconds for graceful
// shutdown before killing.
func (c *DockerClient) StopContainer(ctx context.Context, nameOrID string, timeoutSec int) error {
	path := fmt.Sprintf("/containers/%s/stop?t=%d", url.PathEscape(nameOrID), timeoutSec)
	return c.call(ctx, http.MethodPost, path, nil, nil)
}

// RemoveContainer DELETEs /containers/<id>?force=1. Used when recreating with changed spec.
func (c *DockerClient) RemoveContainer(ctx context.Context, nameOrID string) error {
	return c.call(ctx, http.MethodDelete, "/containers/"+url.PathEscape(nameOrID)+"?force=1", nil, nil)
}

// InspectContainer GETs /containers/<id>/json.
func (c *DockerClient) InspectContainer(ctx context.Context, nameOrID string) (*ContainerInspectResponse, error) {
	var resp ContainerInspectResponse
	if err := c.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(nameOrID)+"/json", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// NetworkInspect confirms a user-defined network exists; used during portal startup to
// fail-fast with a clear error if siyuan-net was never created.
func (c *DockerClient) NetworkInspect(ctx context.Context, name string) error {
	return c.call(ctx, http.MethodGet, "/networks/"+url.PathEscape(name), nil, nil)
}

// NetworkCreate POSTs /networks/create for an internal user-defined bridge. Safe to call
// even if the network already exists; the 409 response is treated as success.
func (c *DockerClient) NetworkCreate(ctx context.Context, name string, internal bool) error {
	body := map[string]any{
		"Name":     name,
		"Driver":   "bridge",
		"Internal": internal,
	}
	err := c.call(ctx, http.MethodPost, "/networks/create", body, nil)
	if err != nil {
		var de *DockerError
		if errors.As(err, &de) && de.Status == http.StatusConflict {
			return nil
		}
		return err
	}
	return nil
}

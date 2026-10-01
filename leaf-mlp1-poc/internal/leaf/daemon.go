package leaf

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const daemonFrameLimit = 1024 * 1024

var ErrDaemonUnavailable = errors.New("Jawaka suspend protection unavailable")

type daemonResponse struct {
	Type               string `json:"type"`
	Token              string `json:"token"`
	Message            string `json:"message"`
	Action             string `json:"action"`
	TitleHintsAccepted int    `json:"title_hints_accepted"`
}

type LibraryTitleGroup struct {
	Provider string   `json:"provider"`
	Title    string   `json:"title"`
	ROMPaths []string `json:"rom_paths"`
}

// DaemonClient owns one reference-counted block-suspend lease for the app.
// Nested and concurrent operations share it; only the first acquire and last
// release cross the Unix socket.
type DaemonClient struct {
	socketPath string
	timeout    time.Duration

	mu         sync.Mutex
	references int
	token      string
	protected  bool
}

type OperationLease struct {
	client    *DaemonClient
	Protected bool
	once      sync.Once
}

func ResolveDaemonSocket(getenv func(string) string, runtimePath string) (string, error) {
	if explicit := getenv("UMRK_DAEMON_SOCKET"); explicit != "" {
		return filepath.Clean(explicit), nil
	}
	if compatibility := getenv("JAWAKA_SOCKET_PATH"); compatibility != "" {
		return filepath.Clean(compatibility), nil
	}
	if runtimePath == "" {
		return "", fmt.Errorf("%w: UMRK_RUNTIME_PATH is empty", ErrDaemonUnavailable)
	}
	return filepath.Join(runtimePath, "jawakad.sock"), nil
}

func NewDaemonClient(socketPath string) *DaemonClient {
	if socketPath != "" {
		socketPath = filepath.Clean(socketPath)
	}
	return &DaemonClient{socketPath: socketPath, timeout: 2 * time.Second}
}

func (c *DaemonClient) Begin(ctx context.Context, reason string, allowUninhibited bool) (*OperationLease, error) {
	if c == nil || c.socketPath == "" {
		if allowUninhibited {
			return &OperationLease{}, nil
		}
		return nil, ErrDaemonUnavailable
	}
	if reason == "" || len(reason) >= 64 {
		return nil, fmt.Errorf("invalid suspend inhibitor reason")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.references > 0 {
		c.references++
		return &OperationLease{client: c, Protected: c.protected}, nil
	}

	response, err := c.request(ctx, map[string]any{
		"type": "suspend-inhibit-acquire", "scope": "block-suspend", "reason": reason,
	})
	if err != nil {
		if !allowUninhibited {
			return nil, fmt.Errorf("%w: %v", ErrDaemonUnavailable, err)
		}
		c.references = 1
		c.token = ""
		c.protected = false
		return &OperationLease{client: c, Protected: false}, nil
	}
	if response.Type != "suspend-inhibit-acquired" || len(response.Token) != 32 {
		return nil, fmt.Errorf("%w: malformed acquire reply", ErrDaemonUnavailable)
	}
	c.references = 1
	c.token = response.Token
	c.protected = true
	return &OperationLease{client: c, Protected: true}, nil
}

// ScanLibraryWithTitles asks Jawaka to start or queue its canonical
// non-destructive library scan and optionally attach source-provided display
// titles to the game rows created by that scan. The pak never opens library.db
// directly.
func (c *DaemonClient) ScanLibraryWithTitles(ctx context.Context, groups []LibraryTitleGroup) (string, error) {
	if c == nil || c.socketPath == "" {
		return "", ErrDaemonUnavailable
	}
	request := map[string]any{"type": "scan-library"}
	titlePathCount := 0
	if len(groups) > 0 {
		sealed := make([]LibraryTitleGroup, len(groups))
		for i, group := range groups {
			if group.Provider == "" || len(group.Provider) >= 96 || group.Title == "" || len(group.Title) >= 256 || len(group.ROMPaths) == 0 {
				return "", fmt.Errorf("request library rescan: invalid title group")
			}
			sealed[i] = LibraryTitleGroup{Provider: group.Provider, Title: group.Title, ROMPaths: append([]string(nil), group.ROMPaths...)}
			for _, path := range sealed[i].ROMPaths {
				if path == "" {
					return "", fmt.Errorf("request library rescan: empty title path")
				}
				titlePathCount++
			}
		}
		request["title_groups"] = sealed
	}
	response, err := c.request(ctx, request)
	if err != nil {
		return "", fmt.Errorf("request library rescan: %w", err)
	}
	if response.Type != "ok" {
		return "", fmt.Errorf("request library rescan: malformed reply %q", response.Type)
	}
	if titlePathCount > 0 && response.TitleHintsAccepted != titlePathCount {
		return "", fmt.Errorf("request library rescan: Jawaka accepted %d of %d title paths", response.TitleHintsAccepted, titlePathCount)
	}
	message := response.Message
	if message == "" {
		message = response.Action
	}
	return message, nil
}

func (c *DaemonClient) ScanLibrary(ctx context.Context) (string, error) {
	return c.ScanLibraryWithTitles(ctx, nil)
}

func (lease *OperationLease) Release() {
	if lease == nil || lease.client == nil {
		return
	}
	lease.once.Do(func() { lease.client.release() })
}

func (c *DaemonClient) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.references <= 0 {
		return
	}
	c.references--
	if c.references > 0 {
		return
	}
	token := c.token
	protected := c.protected
	c.token = ""
	c.protected = false
	if !protected || token == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	_, _ = c.request(ctx, map[string]any{
		"type": "suspend-inhibit-release", "token": token,
	})
}

func (c *DaemonClient) request(ctx context.Context, request any) (daemonResponse, error) {
	var response daemonResponse
	payload, err := json.Marshal(request)
	if err != nil {
		return response, err
	}
	if len(payload) == 0 || len(payload) > daemonFrameLimit {
		return response, fmt.Errorf("invalid daemon request frame size %d", len(payload))
	}
	dialer := net.Dialer{Timeout: c.timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return response, err
	}
	defer conn.Close()
	deadline := time.Now().Add(c.timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = conn.SetDeadline(deadline)

	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := conn.Write(header[:]); err != nil {
		return response, err
	}
	if _, err := conn.Write(payload); err != nil {
		return response, err
	}
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return response, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > daemonFrameLimit {
		return response, fmt.Errorf("invalid daemon frame size %d", size)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(conn, body); err != nil {
		return response, err
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return response, err
	}
	if response.Type == "error" {
		return response, errors.New(response.Message)
	}
	return response, nil
}

var (
	appDaemonMu sync.RWMutex
	appDaemon   *DaemonClient
)

func ConfigureDaemon(env Environment) error {
	socket, err := ResolveDaemonSocket(os.Getenv, env.RuntimePath)
	if err != nil {
		return err
	}
	appDaemonMu.Lock()
	appDaemon = NewDaemonClient(socket)
	appDaemonMu.Unlock()
	return nil
}

func BeginOperation(ctx context.Context, reason string, allowUninhibited bool) (*OperationLease, error) {
	appDaemonMu.RLock()
	client := appDaemon
	appDaemonMu.RUnlock()
	if client == nil {
		// Unit tests and headless tooling do not run under Jawaka.
		return &OperationLease{}, nil
	}
	return client.Begin(ctx, reason, allowUninhibited)
}

func RequestLibraryScan(ctx context.Context) (string, error) {
	appDaemonMu.RLock()
	client := appDaemon
	appDaemonMu.RUnlock()
	if client == nil {
		return "", ErrDaemonUnavailable
	}
	return client.ScanLibrary(ctx)
}

func RequestLibraryScanWithTitles(ctx context.Context, groups []LibraryTitleGroup) (string, error) {
	appDaemonMu.RLock()
	client := appDaemon
	appDaemonMu.RUnlock()
	if client == nil {
		return "", ErrDaemonUnavailable
	}
	return client.ScanLibraryWithTitles(ctx, groups)
}

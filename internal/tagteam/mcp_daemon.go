package tagteam

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
)

// ServeMCPSocket runs a local daemon that hosts one shared ControlRuntime and
// serves the MCP control protocol over a stream listener. The MCP endpoint is a
// thin client transport: runs are owned by this daemon process and the shared
// runtime, so a client can disconnect and reconnect — or a second client can
// attach — without terminating a live run. Durable, file-backed cancellation and
// stale-owner recovery still apply after the originating client exits.
//
// Each accepted connection is served by its own MCP session with independent
// initialization state; the shared runtime serializes concurrent lifecycle
// operations through its existing mutex and run lock. Cancelling ctx closes the
// listener and every live connection, then waits for in-flight sessions to drain.
func ServeMCPSocket(ctx context.Context, listener net.Listener, service ControlService, runtime *ControlRuntime) error {
	var (
		mu    sync.Mutex
		conns = map[net.Conn]struct{}{}
		wg    sync.WaitGroup
	)
	closeAll := func() {
		mu.Lock()
		for conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		closeAll()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			wg.Wait()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept MCP connection: %w", err)
		}
		mu.Lock()
		conns[conn] = struct{}{}
		mu.Unlock()
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer func() {
				mu.Lock()
				delete(conns, c)
				mu.Unlock()
				_ = c.Close()
			}()
			session := NewMCPStdioServer(service, c, c)
			if runtime != nil {
				session.WithRuntime(runtime)
			}
			// A per-connection serve error (including a client hangup) ends only
			// that session; the daemon and other sessions continue.
			_ = session.Serve(ctx)
		}(conn)
	}
}

// ListenMCPUnixSocket creates a unix-domain stream listener at path with
// owner-only permissions, first removing a stale socket left by a prior daemon.
// It refuses to replace a path that is not already a socket so it cannot clobber
// an unrelated file.
func ListenMCPUnixSocket(path string) (net.Listener, error) {
	if path == "" {
		return nil, fmt.Errorf("mcp socket path is required")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace non-socket file %q", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale mcp socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect mcp socket path: %w", err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on mcp socket: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	return listener, nil
}

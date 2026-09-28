// Control one explicit retained node without acquiring session cleanup ownership.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
)

type connection interface {
	Lifecycle(context.Context, *labv1.LifecycleRequest) (*labv1.Node, error)
	Close() error
}

type nativeConnection struct{ *client.Client }

func (c nativeConnection) Lifecycle(ctx context.Context, req *labv1.LifecycleRequest) (*labv1.Node, error) {
	return c.RPC().Lifecycle(ctx, req)
}

func run(args []string, output io.Writer, dial func(context.Context, string) (connection, error)) (err error) {
	flags := flag.NewFlagSet("lifecycle-session", flag.ContinueOnError)
	flags.SetOutput(output)
	socket := flags.String("socket", "", "explicit existing absolute daemon socket")
	session := flags.String("session", "", "explicit existing session ID")
	node := flags.String("node", "", "explicit node name")
	action := flags.String("action", "", "stop, start, or restart (stop is not a guest graceful-shutdown guarantee)")
	timeout := flags.Duration("timeout", 2*time.Minute, "RPC deadline (maximum 10m)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	actions := map[string]labv1.LifecycleAction{"stop": labv1.LifecycleAction_POWER_OFF, "start": labv1.LifecycleAction_START, "restart": labv1.LifecycleAction_RESTART}
	selected, ok := actions[*action]
	if !ok || !filepath.IsAbs(*socket) || filepath.Clean(*socket) == "/" || strings.TrimSpace(*session) == "" || strings.TrimSpace(*node) == "" || *timeout <= 0 || *timeout > 10*time.Minute || len(flags.Args()) != 0 {
		return fmt.Errorf("explicit absolute socket, session, node, action stop/start/restart, and timeout (0,10m] required; positional arguments are not accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	c, err := dial(ctx, *socket)
	if err != nil {
		return err
	}
	// Dial does not Resume/Start a lab. Closing only releases this connection.
	defer func() {
		if closeErr := c.Close(); err == nil {
			err = closeErr
		}
	}()
	result, err := c.Lifecycle(ctx, &labv1.LifecycleRequest{Node: &labv1.NodeRef{SessionId: *session, Node: *node}, Action: selected})
	if err != nil {
		return err
	}
	if result == nil || result.GetName() != *node || result.GetState() == "" {
		return fmt.Errorf("invalid lifecycle response: %v", result)
	}
	_, err = fmt.Fprintf(output, "session=%s node=%s action=%s state=%s\n", *session, result.GetName(), *action, result.GetState())
	return err
}

func main() {
	err := run(os.Args[1:], os.Stdout, func(ctx context.Context, socket string) (connection, error) {
		c, err := client.Dial(ctx, socket)
		if err != nil {
			return nil, err
		}
		return nativeConnection{c}, nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

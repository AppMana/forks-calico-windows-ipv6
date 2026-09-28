package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	labv1 "github.com/appmana/labcontainers/api/v1"
)

type fakeConnection struct {
	request *labv1.LifecycleRequest
	result  *labv1.Node
	failure error
	closed  bool
}

func (f *fakeConnection) Lifecycle(ctx context.Context, req *labv1.LifecycleRequest) (*labv1.Node, error) {
	f.request = req
	return f.result, f.failure
}
func (f *fakeConnection) Close() error { f.closed = true; return nil }
func validArgs() []string {
	return []string{"-socket", "/owned/labd.sock", "-session", "retained-id", "-node", "windows", "-action", "stop"}
}

func TestValidationBeforeDial(t *testing.T) {
	cases := [][]string{nil, {}, {"-action", "stop"}}
	for _, pair := range [][2]string{{"-action", ""}, {"-action", "crash"}, {"-action", "replace"}, {"-action", "STOP"}, {"-socket", "relative"}, {"-socket", "/"}, {"-session", " "}, {"-node", ""}, {"-timeout", "0s"}, {"-timeout", "11m"}} {
		cases = append(cases, append(validArgs(), pair[:]...))
	}
	cases = append(cases, append(validArgs(), "unexpected"))
	for _, args := range cases {
		dialed := false
		err := run(args, &bytes.Buffer{}, func(context.Context, string) (connection, error) {
			dialed = true
			return nil, errors.New("unexpected dial")
		})
		if err == nil || dialed {
			t.Fatalf("args=%v err=%v dialed=%v", args, err, dialed)
		}
	}
}

func TestExplicitLifecycleOnly(t *testing.T) {
	for action, want := range map[string]labv1.LifecycleAction{"stop": labv1.LifecycleAction_POWER_OFF, "start": labv1.LifecycleAction_START, "restart": labv1.LifecycleAction_RESTART} {
		t.Run(action, func(t *testing.T) {
			fake := &fakeConnection{result: &labv1.Node{Name: "windows", State: "observed-state"}}
			var out bytes.Buffer
			err := run(append(validArgs(), "-action", action), &out, func(ctx context.Context, socket string) (connection, error) {
				if socket != "/owned/labd.sock" {
					t.Fatal(socket)
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded lifecycle")
				}
				return fake, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !fake.closed || fake.request.Action != want || fake.request.Node.SessionId != "retained-id" || fake.request.Node.Node != "windows" {
				t.Fatalf("wrong request/close: %+v", fake)
			}
			if !strings.Contains(out.String(), "action="+action+" state=observed-state") {
				t.Fatal(out.String())
			}
		})
	}
}

func TestRPCFailureClosesWithoutSuccess(t *testing.T) {
	boom := errors.New("lifecycle failed")
	fake := &fakeConnection{failure: boom}
	var out bytes.Buffer
	err := run(validArgs(), &out, func(context.Context, string) (connection, error) { return fake, nil })
	if !errors.Is(err, boom) || !fake.closed || out.Len() != 0 {
		t.Fatalf("err=%v closed=%v out=%s", err, fake.closed, out.String())
	}
}

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestWindowsNetworkProbeServesBothAddressFamilies(t *testing.T) {
	shell := "pwsh"
	if stdruntime.GOOS == "windows" {
		// Use the same Windows PowerShell executable as the actual pod.
		shell = "powershell.exe"
	}
	pwsh, err := exec.LookPath(shell)
	if err != nil {
		t.Skip("PowerShell required to execute the actual HTTP probe")
	}
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(reservation.Addr().String())
	reservation.Close()
	if err != nil {
		t.Fatal(err)
	}
	var command []string
	for _, object := range networkProbeObjects("fixture:unused") {
		if pod, ok := object.(*v1.Pod); ok && pod.Name == "hc-windows" {
			command = pod.Spec.Containers[0].Command
		}
	}
	if len(command) != 5 {
		t.Fatalf("missing Windows probe command: %v", command)
	}
	// Run the exact pod program on an ephemeral port. No Calico routes or
	// container setup are needed to detect an IPv4-only listening socket.
	script := strings.Replace(command[4], ",8080)", ","+port+")", 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	child := exec.CommandContext(ctx, pwsh, "-NoProfile", "-NonInteractive", "-Command", script)
	outputPath := filepath.Join(t.TempDir(), "probe-output.log")
	output, err := os.Create(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = output.Close()
		if t.Failed() {
			data, err := os.ReadFile(outputPath)
			t.Logf("PowerShell probe output (read error=%v): %s", err, data)
		}
	}()
	child.Stdout, child.Stderr = output, output
	started := time.Now()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = child.Wait(); close(exited) }()
	defer func() { cancel(); <-exited }()
	// PowerShell/.NET initialization is not an HTTP response. Start the
	// unchanged per-family request deadlines only after the actual listener
	// announces readiness. A process exit or startup timeout still fails.
	startup, startupCancel := context.WithTimeout(ctx, 30*time.Second)
	defer startupCancel()
	if err := waitForProbeListener(startup, outputPath, exited); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	defer client.CloseIdleConnections()
	for _, address := range []string{"127.0.0.1", "::1"} {
		deadline := time.Now().Add(5 * time.Second)
		attempt := 0
		for {
			attempt++
			response, err := client.Get("http://" + net.JoinHostPort(address, port) + "/")
			if err == nil {
				body, readErr := io.ReadAll(response.Body)
				response.Body.Close()
				if readErr != nil || response.StatusCode != http.StatusOK || string(body) != "ok windows" {
					t.Fatalf("%s: status=%d body=%q error=%v", address, response.StatusCode, body, readErr)
				}
				break
			}
			t.Logf("probe address=%s attempt=%d elapsed=%s error=%v", address, attempt, time.Since(started), err)
			if time.Now().After(deadline) {
				t.Fatalf("probe cannot serve %s: %v", address, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func waitForProbeListener(ctx context.Context, path string, exited <-chan struct{}) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read probe startup output: %w", err)
		}
		if strings.Contains(string(data), "HTTP_PROBE_LISTENING ") {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("probe listener startup: %w", ctx.Err())
		case <-exited:
			return fmt.Errorf("probe exited before announcing its listener")
		case <-ticker.C:
		}
	}
}

func TestProbeListenerStartupRequiresMarker(t *testing.T) {
	for _, mode := range []string{"ready", "exit", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "startup.log")
			if err := os.WriteFile(path, []byte("initializing PowerShell\n"), 0600); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if mode == "ready" {
				if err := os.WriteFile(path, []byte("HTTP_PROBE_LISTENING 2026-10-04T00:00:00Z\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if mode == "exit" {
				close(exited)
			} else {
				cancel()
			}
			err := waitForProbeListener(ctx, path, exited)
			if (err == nil) != (mode == "ready") {
				t.Fatalf("mode=%s error=%v", mode, err)
			}
		})
	}
}

func networkProbeObjects(winImage string) []runtime.Object {
	psArgs := func(script string) []string {
		return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$ErrorActionPreference='Stop'; " + script}
	}
	var objects []runtime.Object
	for _, name := range []string{"linux", "windows"} {
		container := v1.Container{Name: "server", Image: "docker.io/nicolaka/netshoot@sha256:34eeca872db74067b1ed7fdc6201f278578bf57df7bd3081e99b5097a28464b5", ImagePullPolicy: v1.PullNever, Command: []string{"sh", "-ec", `mkdir -p /tmp/www; printf 'ok linux' > /tmp/www/index.html; exec httpd -f -p 8080 -h /tmp/www`}}
		if name == "windows" {
			container.Image = winImage
			container.Command = psArgs(`$l=[Net.Sockets.TcpListener]::new([Net.IPAddress]::IPv6Any,8080); $l.Server.DualMode=$true; $l.Start(); [Console]::Out.WriteLine('HTTP_PROBE_LISTENING '+[DateTime]::UtcNow.ToString('O')); [Console]::Out.Flush(); while($true){$c=$l.AcceptTcpClient();try{$s=$c.GetStream();$s.ReadTimeout=5000;$b=New-Object byte[] 4096;$null=$s.Read($b,0,$b.Length);$r=[Text.Encoding]::ASCII.GetBytes("HTTP/1.1 200 OK` + "`r`nContent-Length: 10`r`nConnection: close`r`n`r`nok windows" + `");$s.Write($r,0,$r.Length)}catch{}finally{$c.Dispose()}}`)
		}
		objects = append(objects, &v1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "hc-" + name, Namespace: "default", Labels: map[string]string{"app": "hc-" + name}}, Spec: v1.PodSpec{NodeName: name, Containers: []v1.Container{container}, RestartPolicy: v1.RestartPolicyAlways}},
			&v1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: "svc-hc-" + name + "-v4", Namespace: "default"}, Spec: v1.ServiceSpec{Selector: map[string]string{"app": "hc-" + name}, Ports: []v1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}}}})
	}
	return objects
}

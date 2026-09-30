package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func testArgs() []string {
	return []string{"--node-image=ghcr.io/appmana/node@sha256:" + strings.Repeat("a", 64), "--api-host=192.0.2.10", "--api-port=6443", "--service-cidr=10.96.0.0/12", "--dns-address=10.96.0.10", "--autodetection-method=can-reach=192.0.2.10"}
}

func TestRenderDoesNotLoadKubeconfigAndReportsExactByteHash(t *testing.T) {
	var out, diagnostics bytes.Buffer
	args := append(testArgs(), "--kubeconfig=/does/not/exist")
	if err := run(context.Background(), args, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	var report map[string]string
	if err := json.Unmarshal(diagnostics.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report["manifestSHA256"] != fmt.Sprintf("%x", sha256.Sum256(out.Bytes())) {
		t.Fatal("approval does not cover exact rendered bytes")
	}
	if strings.Contains(out.String(), "autoAllocateBlocks") {
		t.Fatal("adapter claimed unrelated cluster-wide IPAM policy")
	}
}

func TestApplyRejectsUnapprovedAndUnknownInputsBeforeConnecting(t *testing.T) {
	for _, extra := range [][]string{{"--mode=apply", "--kubeconfig=/does/not/exist"}, {"--mode=plan"}, {"--mode=typo"}, {"--timeout=0s"}, {"--ipv6"}, {"unexpected"}} {
		var out, diagnostics bytes.Buffer
		if err := run(context.Background(), append(testArgs(), extra...), &out, &diagnostics); err == nil {
			t.Fatal("accepted unsafe input", extra)
		}
		if out.Len() != 0 {
			t.Fatal("returned successful manifest on rejected request")
		}
	}
}

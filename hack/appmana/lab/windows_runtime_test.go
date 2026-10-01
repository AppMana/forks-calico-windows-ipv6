package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsRuntimeInputsAreExplicitAndVerified(t *testing.T) {
	if input, err := readWindowsRuntime(context.Background(), func(string) string { return "" }); err != nil || input != nil {
		t.Fatal(input, err)
	}
	dir := t.TempDir()
	write := func(name string, data []byte) (string, string) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
		return p, fmt.Sprintf("%x", sha256.Sum256(data))
	}
	input := windowsRuntimeInput{Version: "2.3.5-appmana.post.1", ArchiveName: "containerd.tar.gz", ArchiveSHA256: strings.Repeat("a", 64)}
	input.TransactionPath, input.TransactionSHA256 = write("transaction.ps1", []byte("deployment fixture"))
	input.ConfigPath, input.ConfigSHA256 = write("config.toml", []byte("version = 3"))
	check := func(value windowsRuntimeInput) error {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path, hash := write("inputs.json", data)
		_, err = readWindowsRuntime(context.Background(), func(key string) string {
			if key == "LABCONTAINERS_WINDOWS_RUNTIME" {
				return path
			}
			return hash
		})
		return err
	}
	if err := check(input); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*windowsRuntimeInput){
		func(v *windowsRuntimeInput) { v.ArchiveName = "../runtime.tar.gz" },
		func(v *windowsRuntimeInput) { v.Version = "2.3.5'; exit 0" },
		func(v *windowsRuntimeInput) { v.TransactionSHA256 = strings.Repeat("0", 64) },
		func(v *windowsRuntimeInput) { v.ConfigSHA256 = strings.Repeat("0", 64) },
		func(v *windowsRuntimeInput) { v.ArchiveSHA256 = "" },
	} {
		v := input
		change(&v)
		if err := check(v); err == nil {
			t.Fatalf("accepted invalid input: %+v", v)
		}
	}
}

func TestWindowsExternalRuntimeDoesNotStartBundledRuntime(t *testing.T) {
	for _, external := range []bool{false, true} {
		args := windowsWorkerInstallArgs(external)
		if strings.Contains(strings.Join(args, " "), "--cri-socket=remote:npipe:////./pipe/containerd-containerd") != external {
			t.Fatal(args)
		}
		if strings.Contains(strings.Join(args, " "), "powershell") {
			t.Fatal("worker arguments must be native argv")
		}
	}
}

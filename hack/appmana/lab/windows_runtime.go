package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"

	"github.com/appmana/labcontainers/pkg/artifact"
)

// Runtime installation remains owned by the deployment transaction. This
// fixture only supplies explicit, verified inputs and stock k0s's CRI socket.
type windowsRuntimeInput struct {
	Version             string `json:"version"`
	ArchiveName         string `json:"archiveName"`
	ArchiveSHA256       string `json:"archiveSHA256"`
	TransactionPath     string `json:"transactionPath"`
	TransactionSHA256   string `json:"transactionSHA256"`
	ConfigPath          string `json:"configPath"`
	ConfigSHA256        string `json:"configSHA256"`
	transaction, config []byte
}

func readWindowsRuntime(ctx context.Context, getenv func(string) string) (*windowsRuntimeInput, error) {
	path, digest := getenv("LABCONTAINERS_WINDOWS_RUNTIME"), getenv("LABCONTAINERS_WINDOWS_RUNTIME_SHA256")
	if path == "" && digest == "" {
		return nil, nil
	}
	data, err := artifact.ReadFile(ctx, path, digest)
	if err != nil {
		return nil, err
	}
	var input windowsRuntimeInput
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("runtime input must be one JSON object")
	}
	if !regexp.MustCompile(`^\d+\.\d+\.\d+[a-zA-Z0-9.+-]*$`).MatchString(input.Version) ||
		!regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*\.tar\.gz$`).MatchString(input.ArchiveName) ||
		!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(input.ArchiveSHA256) {
		return nil, fmt.Errorf("explicit version, media basename and archive digest required")
	}
	input.transaction, err = artifact.ReadFile(ctx, input.TransactionPath, input.TransactionSHA256)
	if err != nil {
		return nil, fmt.Errorf("runtime transaction: %w", err)
	}
	input.config, err = artifact.ReadFile(ctx, input.ConfigPath, input.ConfigSHA256)
	if err != nil {
		return nil, fmt.Errorf("runtime config: %w", err)
	}
	return &input, nil
}

func windowsWorkerInstallArgs(externalRuntime bool) []string {
	args := []string{`C:\LabQualification\k0s.exe`, "install", "worker", "--token-file", `C:\LabQualification\token`, "--kubelet-extra-args", "--node-ip=192.0.2.20 --hostname-override=windows"}
	if externalRuntime {
		args = append(args, "--cri-socket=remote:npipe:////./pipe/containerd-containerd")
	}
	return args
}

package main

import (
	_ "embed"
	"fmt"
	"path/filepath"

	labv1 "github.com/appmana/labcontainers/api/v1"
)

//go:embed controller_disk.sh
var controllerDiskScript string

// Read-only gate before writing image payloads or installing k0s. It never
// attempts recovery or formatting when the intended mount is missing.
const controllerDiskMountedScript = `
set -eu
device=/dev/disk/by-id/virtio-lc-k0s-state
state=/var/lib/k0s
uuid=$(blkid -s UUID -o value "$device")
test -n "$uuid"
test "$(findmnt -n -M "$state" -o UUID)" = "$uuid"
test "$(findmnt -n -M "$state" -o FSTYPE)" = ext4
test "$(findmnt -n -M "$state" -o MAJ:MIN)" = "$(lsblk -dnr -o MAJ:MIN "$device")"
`

func freshQualificationNodes() map[string]*labv1.NodeExtension {
	return map[string]*labv1.NodeExtension{
		"linux":   {Control: "qga", Disks: []*labv1.Disk{{Name: "k0s-state", SizeBytes: 32 << 30}}},
		"windows": {Control: "qga"},
	}
}

func qualificationStateDir(value string) (string, error) {
	if !filepath.IsAbs(value) || filepath.Clean(value) == string(filepath.Separator) {
		return "", fmt.Errorf("LABCONTAINERS_STATE_DIR must be an explicit absolute non-root persistent directory")
	}
	return filepath.Clean(value), nil
}

#!/bin/sh
# Fresh labs only. Never rerun this provisioning script to recover a mount.
set -eu
device=/dev/disk/by-id/virtio-lc-k0s-state
state=$1
fstab=$2
fail() { echo "controller disk: $*" >&2; exit 1; }
test -b "$device" || fail 'declared block device missing'
test ! -e "$state" && test ! -L "$state" || fail 'existing controller state path'
test -f "$fstab" && test ! -L "$fstab" || fail 'fstab must be a regular non-symlink file'
serial=$(lsblk -dnr -o SERIAL "$device")
test "$serial" = lc-k0s-state || fail 'unexpected disk serial'
kind=$(lsblk -dnr -o TYPE "$device")
test "$kind" = disk || fail 'not a whole disk'
size=$(blockdev --getsize64 "$device")
test "$size" = 34359738368 || fail 'unexpected disk size'
nodes=$(lsblk -nr -o TYPE "$device")
test "$nodes" = disk || fail 'disk has children or partitions'
mounted=$(lsblk -nr -o MOUNTPOINTS "$device")
test -z "$mounted" || fail 'disk is mounted'
major=$(lsblk -dnr -o MAJ:MIN "$device")
test -n "$major" || fail 'missing disk identity'
root=$(findmnt -n -o SOURCE /)
test -n "$root" || fail 'missing root source'
ancestors=$(lsblk -snr -o MAJ:MIN "$root")
test -n "$ancestors" || fail 'missing root ancestry'
for ancestor in $ancestors; do
 test "$ancestor" != "$major" || fail 'disk backs the root filesystem'
done
signatures=$(wipefs --no-act --noheadings --output TYPE "$device")
test -z "$signatures" || fail 'existing disk signatures'
if blkid -p -o export "$device"; then
 fail 'existing filesystem or partition table'
else
 code=$?
 test "$code" = 2 || fail "filesystem probe failed ($code)"
fi
# Reject a stale/conflicting entry before formatting, including comments safely.
awk -v state="$state" '$0 !~ /^[[:space:]]*#/ && $2 == state {found=1} END {exit found ? 1 : 0}' "$fstab" || fail 'existing fstab target'
mke2fs -t ext4 "$device"
uuid=$(blkid -s UUID -o value "$device")
case "$uuid" in ''|*[!a-fA-F0-9-]*) fail 'invalid filesystem UUID';; esac
test "${#uuid}" = 36 || fail 'invalid UUID length'
mkdir "$state"
mount -t ext4 -o defaults "UUID=$uuid" "$state"
actual_uuid=$(findmnt -rn -M "$state" -o UUID)
actual_type=$(findmnt -rn -M "$state" -o FSTYPE)
actual_device=$(findmnt -rn -M "$state" -o MAJ:MIN)
test "$actual_uuid" = "$uuid" && test "$actual_type" = ext4 && test "$actual_device" = "$major" || fail 'mounted filesystem identity mismatch'
# Preserve existing fstab bytes/metadata. No nofail: boot must not silently put
# controller state back on the root image when this disk is absent.
printf '\nUUID=%s %s ext4 defaults 0 2\n' "$uuid" "$state" >> "$fstab"
sync
printf 'CONTROLLER_STATE_DISK UUID=%s DEVICE=%s MOUNT=%s\n' "$uuid" "$major" "$state"

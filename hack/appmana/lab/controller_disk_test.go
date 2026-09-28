package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFreshQualificationDeclaresControllerDisk(t *testing.T) {
	// Inspect the actual Start argument, not an unused helper.
	file, err := parser.ParseFile(token.NewFileSet(), "qualification_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Start" {
			return true
		}
		ast.Inspect(call, func(n ast.Node) bool {
			field, ok := n.(*ast.KeyValueExpr)
			if !ok {
				return true
			}
			key, ok := field.Key.(*ast.Ident)
			if !ok || key.Name != "Nodes" {
				return true
			}
			value, ok := field.Value.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := value.Fun.(*ast.Ident)
			found = ok && name.Name == "freshQualificationNodes"
			return true
		})
		return true
	})
	if !found {
		t.Fatal("fresh qualification Start does not provision its dedicated controller state disk")
	}
}

func TestControllerDiskSpecification(t *testing.T) {
	nodes := freshQualificationNodes()
	disk := nodes["linux"].GetDisks()
	if len(disk) != 1 || disk[0].Name != "k0s-state" || disk[0].SizeBytes != 32<<30 || nodes["linux"].Control != "qga" {
		t.Fatalf("unexpected Linux disk: %v", nodes["linux"])
	}
	if len(nodes["windows"].GetDisks()) != 0 {
		t.Fatal("Windows unexpectedly got a disk")
	}
}

func TestQualificationPersistentStateDirectory(t *testing.T) {
	for _, input := range []string{"", ".", "relative/state", "/", "/a/.."} {
		if _, err := qualificationStateDir(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	dir := filepath.Join(t.TempDir(), "not-created")
	if got, err := qualificationStateDir(dir); err != nil || got != dir {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("validation mutated directory: %v", err)
	}
}

// Execute the actual provisioning shell, replacing only OS commands. No host
// devices, mounts or formatting are used. Real mkdir/fstab writes stay in TempDir.
const diskCommandMocks = `
test() {
 if [ "$1" = -b ]; then [ "$CASE" != missing ]; else command test "$@"; fi
}
lsblk() {
 case "$3" in
 SERIAL) [ "$CASE" != serial ] || { echo wrong; return; }; echo lc-k0s-state;;
 TYPE) [ "$CASE" != partition ] || { printf 'disk\npart\n'; return; }; echo disk;;
 MOUNTPOINTS) [ "$CASE" != mounted ] || echo /used;;
 MAJ:MIN) if [ "$1" = -snr ]; then
   [ "$CASE" != root ] || { echo 254:16; return; }; echo 8:0
  else echo 254:16; fi;;
 *) return 79;;
 esac
}
blockdev() { [ "$CASE" != size ] || { echo 1; return; }; echo 34359738368; }
wipefs() {
 [ "$CASE" != probeerror ] || return 73
 [ "$CASE" != signature ] || echo ext4
 return 0
}
blkid() {
 if [ "$1" = -p ]; then
  [ "$CASE" != blkiderror ] || return 74
  [ "$CASE" != filesystem ] || { echo TYPE=ext4; return 0; }
  return 2
 fi
 [ "$CASE" != uuid ] || { echo invalid; return; }
 echo 12345678-1234-1234-1234-123456789abc
}
findmnt() {
 if [ "$4" = / ]; then echo /dev/sda1; return; fi
 case "$5" in
 UUID) [ "$CASE" != wrongmount ] || { echo wrong; return; }; echo 12345678-1234-1234-1234-123456789abc;;
 FSTYPE) echo ext4;;
 MAJ:MIN)
  # util-linux findmnt table output pads MAJ:MIN even with --noheadings.
  # Raw output is unpadded, as observed in the Ubuntu qualification guest.
  if [ "$1" = -rn ]; then echo 254:16; else printf '254:16  \n'; fi;;
 *) return 78;;
 esac
}
mke2fs() { printf 'format\n' >> "$TRACE"; [ "$CASE" != formaterror ]; }
mount() { printf 'mount\n' >> "$TRACE"; [ "$CASE" != mounterror ]; }
sync() { printf 'sync\n' >> "$TRACE"; }
`

func TestControllerDiskProvisioningFailClosed(t *testing.T) {
	for _, scenario := range []string{"good", "missing", "serial", "size", "partition", "mounted", "root", "signature", "probeerror", "filesystem", "blkiderror", "existing", "symlink", "fstab", "uuid", "formaterror", "mounterror", "wrongmount"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			state := filepath.Join(dir, "state")
			fstab := filepath.Join(dir, "fstab")
			trace := filepath.Join(dir, "trace")
			original := "# original\nUUID=root / ext4 defaults 0 1\n"
			if scenario == "fstab" {
				original += "UUID=old " + state + " ext4 defaults 0 2\n"
			}
			if err := os.WriteFile(fstab, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			if scenario == "existing" {
				if err := os.Mkdir(state, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "symlink" {
				if err := os.Symlink(filepath.Join(dir, "absent"), state); err != nil {
					t.Fatal(err)
				}
			}
			run := func() ([]byte, error) {
				cmd := exec.Command("sh", "-ec", diskCommandMocks+controllerDiskScript, "test", state, fstab)
				cmd.Env = append(os.Environ(), "CASE="+scenario, "TRACE="+trace)
				return cmd.CombinedOutput()
			}
			out, err := run()
			events, _ := os.ReadFile(trace)
			got, _ := os.ReadFile(fstab)
			if scenario == "good" {
				if err != nil {
					t.Fatalf("%s: %v", out, err)
				}
				if string(events) != "format\nmount\nsync\n" {
					t.Fatalf("events %q", events)
				}
				want := original + "\nUUID=12345678-1234-1234-1234-123456789abc " + state + " ext4 defaults 0 2\n"
				if string(got) != want {
					t.Fatalf("fstab %q", got)
				}
				if out, err := run(); err == nil {
					t.Fatalf("repeated provisioning accepted: %s", out)
				}
				after, _ := os.ReadFile(trace)
				if string(after) != string(events) {
					t.Fatal("repeat formatted or mounted existing disk")
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted unsafe case: %s", out)
			}
			if string(got) != original {
				t.Fatalf("changed fstab on failure: %q", got)
			}
			switch scenario {
			case "uuid", "formaterror":
				if string(events) != "format\n" {
					t.Fatalf("unexpected failure ordering: %q", events)
				}
			case "mounterror", "wrongmount":
				if string(events) != "format\nmount\n" {
					t.Fatalf("unexpected failure ordering: %q", events)
				}
			default:
				if len(events) != 0 {
					t.Fatalf("destructive step before rejecting unsafe disk: %q", events)
				}
			}
		})
	}
}

func TestControllerDiskReadOnlyMountGate(t *testing.T) {
	for _, scenario := range []string{"good", "wrongmount", "uuid"} {
		t.Run(scenario, func(t *testing.T) {
			trace := filepath.Join(t.TempDir(), "trace")
			cmd := exec.Command("sh", "-ec", diskCommandMocks+controllerDiskMountedScript)
			cmd.Env = append(os.Environ(), "CASE="+scenario, "TRACE="+trace)
			out, err := cmd.CombinedOutput()
			if (err == nil) != (scenario == "good") {
				t.Fatalf("%s %v", out, err)
			}
			if _, err := os.Stat(trace); !os.IsNotExist(err) {
				t.Fatal("mount gate performed mutation")
			}
		})
	}
}

func TestControllerDiskShellSyntax(t *testing.T) {
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(controllerDiskScript)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
}

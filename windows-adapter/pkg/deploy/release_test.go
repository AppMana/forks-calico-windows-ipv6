package deploy

import (
	"encoding/json"
	"strings"
	"testing"
)

func releaseFixture() Release {
	pin := func(name, base string) ImagePin {
		return ImagePin{Reference: "registry.example/" + name + "@sha256:" + strings.Repeat("a", 64), UpstreamVersion: base, SourceRevision: strings.Repeat("b", 40)}
	}
	return Release{SchemaVersion: 1, KubernetesVersion: "v1.36.4", CalicoVersion: "v3.32.2", CalicoNode: pin("node", "v3.32.2"), CalicoWindowsNode: pin("node-windows", "v3.32.2"), CalicoCNI: pin("cni", "v3.32.2"), CalicoWindowsCNI: pin("cni-windows", "v3.32.2"), CalicoControllers: pin("controllers", "v3.32.2"), KubeProxyLinux: pin("proxy-linux", "v1.36.4"), KubeProxyWindows: pin("proxy-windows", "v1.36.4")}
}

func TestReleaseRejectsUnalignedOrMutableComponents(t *testing.T) {
	for _, mutate := range []func(*Release){
		func(r *Release) { r.KubeProxyLinux.UpstreamVersion = "v1.36.2" },
		func(r *Release) { r.KubeProxyWindows.UpstreamVersion = "v1.37.0" },
		func(r *Release) { r.CalicoCNI.UpstreamVersion = "v3.33.0" },
		func(r *Release) { r.CalicoWindowsNode.SourceRevision = strings.Repeat("c", 40) },
		func(r *Release) { r.CalicoWindowsCNI.Reference = "registry.example/cni:latest" },
		func(r *Release) { r.CalicoControllers = ImagePin{} },
		func(r *Release) { r.SchemaVersion = 2 },
	} {
		r := releaseFixture()
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Fatal("accepted unaligned release", r)
		}
	}
}

func TestReleaseGeneratesAllDistributionImageInputs(t *testing.T) {
	r := releaseFixture()
	for _, offline := range []bool{false, true} {
		images, err := r.K0sImages(offline)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(images)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"node", "node-windows", "cni", "cni-windows", "controllers", "proxy-linux", "proxy-windows"} {
			if !strings.Contains(string(data), "registry.example/"+name+`"`) {
				t.Fatal("omitted component", name)
			}
		}
		want := "IfNotPresent"
		if offline {
			want = "Never"
		}
		if images.DefaultPullPolicy != want {
			t.Fatal("incorrect image loading policy")
		}
	}
	o := options()
	o.NodeImage = ""
	o, err := r.WindowsOptions(o)
	if err != nil || o.NodeImage != r.CalicoWindowsNode.Reference {
		t.Fatal("Windows manifest and distribution image pins diverged", err)
	}
	o.NodeImage = "registry.example/wrong@sha256:" + strings.Repeat("d", 64)
	if _, err := r.WindowsOptions(o); err == nil {
		t.Fatal("accepted competing node-image override")
	}
}

func TestReleaseReaderRejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	data, err := json.Marshal(releaseFixture())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRelease(strings.NewReader(string(data))); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{string(data) + ` {}`, strings.Replace(string(data), `"schemaVersion":1`, `"schemaVersion":1,"typo":true`, 1)} {
		if _, err := ReadRelease(strings.NewReader(bad)); err == nil {
			t.Fatal("accepted malformed release")
		}
	}
}

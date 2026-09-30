package deploy

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"

	"github.com/distribution/reference"
)

// ImagePin separates the upstream base from the exact fork build. A digest
// identifies an image (or platform index); SourceRevision identifies its build
// source. These declarations require independent build/VM qualification.
type ImagePin struct {
	Reference       string `json:"reference"`
	UpstreamVersion string `json:"upstreamVersion"`
	SourceRevision  string `json:"sourceRevision"`
}

// Release is shared by distribution configuration, GitOps rendering and labs.
// In particular, Linux kube-proxy is explicit even when it is unmodified.
type Release struct {
	SchemaVersion     int      `json:"schemaVersion"`
	KubernetesVersion string   `json:"kubernetesVersion"`
	CalicoVersion     string   `json:"calicoVersion"`
	CalicoNode        ImagePin `json:"calicoNode"`
	CalicoWindowsNode ImagePin `json:"calicoWindowsNode"`
	CalicoCNI         ImagePin `json:"calicoCNI"`
	CalicoWindowsCNI  ImagePin `json:"calicoWindowsCNI"`
	CalicoControllers ImagePin `json:"calicoControllers"`
	KubeProxyLinux    ImagePin `json:"kubeProxyLinux"`
	KubeProxyWindows  ImagePin `json:"kubeProxyWindows"`
}

var sourcePattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

func (r Release) Validate() error {
	if r.SchemaVersion != 1 || !versionPattern.MatchString(r.KubernetesVersion) || !versionPattern.MatchString(r.CalicoVersion) {
		return fmt.Errorf("release requires schemaVersion 1 and explicit upstream semantic versions")
	}
	for _, component := range []struct {
		name string
		pin  ImagePin
		base string
	}{
		{"calicoNode", r.CalicoNode, r.CalicoVersion}, {"calicoWindowsNode", r.CalicoWindowsNode, r.CalicoVersion},
		{"calicoCNI", r.CalicoCNI, r.CalicoVersion}, {"calicoWindowsCNI", r.CalicoWindowsCNI, r.CalicoVersion},
		{"calicoControllers", r.CalicoControllers, r.CalicoVersion}, {"kubeProxyLinux", r.KubeProxyLinux, r.KubernetesVersion}, {"kubeProxyWindows", r.KubeProxyWindows, r.KubernetesVersion},
	} {
		if component.pin.UpstreamVersion != component.base {
			return fmt.Errorf("%s base %q does not match bundle %q", component.name, component.pin.UpstreamVersion, component.base)
		}
		if !sourcePattern.MatchString(component.pin.SourceRevision) {
			return fmt.Errorf("%s requires full source commit", component.name)
		}
		ref, err := reference.ParseNormalizedNamed(component.pin.Reference)
		if err != nil {
			return fmt.Errorf("%s image: %w", component.name, err)
		}
		digest, ok := ref.(reference.Digested)
		if !ok || digest.Digest().Algorithm().String() != "sha256" {
			return fmt.Errorf("%s requires immutable SHA256 image reference", component.name)
		}
	}
	for _, pin := range []ImagePin{r.CalicoWindowsNode, r.CalicoCNI, r.CalicoWindowsCNI} {
		if pin.SourceRevision != r.CalicoNode.SourceRevision {
			return fmt.Errorf("Calico node and CNI builds must share the release source across operating systems")
		}
	}
	return nil
}

func ReadRelease(src io.Reader) (Release, error) {
	var release Release
	decoder := json.NewDecoder(src)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&release); err != nil {
		return release, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return release, fmt.Errorf("release must contain exactly one JSON document")
	}
	return release, release.Validate()
}

// K0sImage uses the stock k0s image/version wire contract. A synthetic tag is
// required by its schema; the digest, not that tag, selects the image content.
type K0sImage struct {
	Image   string `json:"image"`
	Version string `json:"version"`
}
type K0sCalicoWindowsImages struct {
	Node K0sImage `json:"node"`
	CNI  K0sImage `json:"cni"`
}
type K0sCalicoImages struct {
	Node            K0sImage               `json:"node"`
	CNI             K0sImage               `json:"cni"`
	KubeControllers K0sImage               `json:"kubecontrollers"`
	Windows         K0sCalicoWindowsImages `json:"windows"`
}
type K0sWindowsImages struct {
	KubeProxy K0sImage `json:"kubeproxy"`
}

// K0sImageInputs is a spec.images fragment, not a replacement ClusterConfig.
// Merge this into the distribution's source configuration, never patch its
// generated DaemonSets. Unrelated DNS/pause/HA configuration remains untouched.
type K0sImageInputs struct {
	DefaultPullPolicy string           `json:"default_pull_policy"`
	KubeProxy         K0sImage         `json:"kubeproxy"`
	Windows           K0sWindowsImages `json:"windows"`
	Calico            K0sCalicoImages  `json:"calico"`
}

func (r Release) K0sImages(offline bool) (K0sImageInputs, error) {
	if err := r.Validate(); err != nil {
		return K0sImageInputs{}, err
	}
	image := func(pin ImagePin) K0sImage {
		ref, _ := reference.ParseNormalizedNamed(pin.Reference)
		return K0sImage{Image: reference.TrimNamed(ref).String(), Version: "pinned@" + ref.(reference.Digested).Digest().String()}
	}
	policy := "IfNotPresent"
	if offline {
		policy = "Never"
	}
	return K0sImageInputs{DefaultPullPolicy: policy, KubeProxy: image(r.KubeProxyLinux), Windows: K0sWindowsImages{KubeProxy: image(r.KubeProxyWindows)}, Calico: K0sCalicoImages{Node: image(r.CalicoNode), CNI: image(r.CalicoCNI), KubeControllers: image(r.CalicoControllers), Windows: K0sCalicoWindowsImages{Node: image(r.CalicoWindowsNode), CNI: image(r.CalicoWindowsCNI)}}}, nil
}

func (r Release) WindowsOptions(options WindowsBGPOptions) (WindowsBGPOptions, error) {
	if err := r.Validate(); err != nil {
		return options, err
	}
	if options.NodeImage != "" && options.NodeImage != r.CalicoWindowsNode.Reference {
		return options, fmt.Errorf("node image override conflicts with release lock")
	}
	options.NodeImage = r.CalicoWindowsNode.Reference
	return options, nil
}

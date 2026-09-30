package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
)

const FieldManager = "calico-windows-adapter"
const ownerAnnotation = "projectcalico.org/windows-adapter-owner"

type target struct {
	resource        schema.GroupVersionResource
	namespace, name string
	shared          bool
}

var targets = []target{
	{schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, "kube-system", "calico-windows-config-actual", false},
	{schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "ipamconfigs"}, "", "default", true},
	{schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, "kube-system", "calico-node-windows", false},
}

// Plan has an immutable, reviewable desired state. It does not contain secrets,
// kubeconfig, host commands or a dependency on k0s or Labcontainers.
type Plan struct {
	objects  []*unstructured.Unstructured
	manifest []byte
}

func NewPlan(o WindowsBGPOptions) (*Plan, error) {
	objects, err := WindowsBGP(o)
	if err != nil {
		return nil, err
	}
	p := &Plan{}
	for i, object := range objects {
		data, err := json.Marshal(object)
		if err != nil {
			return nil, err
		}
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(data); err != nil {
			return nil, err
		}
		// Do not claim a status subresource, even when native Go types emit it.
		delete(u.Object, "status")
		if !targets[i].shared {
			u.SetAnnotations(map[string]string{ownerAnnotation: FieldManager})
		}
		if targets[i].shared {
			// Windows requires strict affinity, not ownership of other cluster-
			// wide IPAM policy (e.g. a deliberately disabled autoAllocateBlocks).
			u.Object["spec"] = map[string]interface{}{"strictAffinity": true}
		}
		p.objects = append(p.objects, u)
	}
	p.manifest, err = json.Marshal(struct {
		APIVersion string                       `json:"apiVersion"`
		Kind       string                       `json:"kind"`
		Items      []*unstructured.Unstructured `json:"items"`
	}{"v1", "List", p.objects})
	return p, err
}

func (p *Plan) Manifest() []byte { return append([]byte(nil), p.manifest...) }
func (p *Plan) SHA256() string   { return fmt.Sprintf("%x", sha256.Sum256(p.manifest)) }

func resource(client dynamic.Interface, t target) dynamic.ResourceInterface {
	r := client.Resource(t.resource)
	if t.namespace != "" {
		return r.Namespace(t.namespace)
	}
	return r
}

// Prerequisites are intentionally not Windows NodeReady: that would deadlock
// installation of the CNI required to make those nodes ready. Linux Calico and
// its service account remain distribution-owned.
func prerequisites(ctx context.Context, client dynamic.Interface) (bool, error) {
	crd := target{resource: schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}, name: "ipamconfigs.crd.projectcalico.org"}
	for _, t := range []target{crd, {resource: schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, namespace: "kube-system", name: "calico-node"}, {resource: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, namespace: "kube-system", name: "calico-node"}} {
		u, err := resource(client, t).Get(ctx, t.name, meta.GetOptions{})
		var networkError *net.OpError
		if errors.As(err, &networkError) && networkError.Op == "dial" {
			return false, nil
		}
		if apierrors.IsNotFound(err) || apierrors.IsServiceUnavailable(err) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) || apierrors.IsTooManyRequests(err) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("prerequisite %s/%s: %w", t.resource.Resource, t.name, err)
		}
		if t == crd {
			conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
			established := false
			for _, condition := range conditions {
				c, ok := condition.(map[string]interface{})
				if ok && c["type"] == "Established" && c["status"] == "True" {
					established = true
				}
			}
			if !established {
				return false, nil
			}
		}
		if t.resource.Resource == "daemonsets" {
			containers, _, _ := unstructured.NestedSlice(u.Object, "spec", "template", "spec", "containers")
			bird := false
			for _, raw := range containers {
				container, ok := raw.(map[string]interface{})
				if !ok || container["name"] != "calico-node" {
					continue
				}
				env, _, _ := unstructured.NestedSlice(container, "env")
				for _, value := range env {
					entry, ok := value.(map[string]interface{})
					if ok && entry["name"] == "CALICO_NETWORKING_BACKEND" && entry["value"] == "bird" {
						bird = true
					}
				}
			}
			if !bird {
				return false, fmt.Errorf("stock k0s calico-node must explicitly use the bird backend; refusing unknown/VXLAN configuration")
			}
		}
	}
	return true, nil
}

func (p *Plan) checkOwnership(ctx context.Context, client dynamic.Interface) error {
	for _, t := range targets {
		if t.shared {
			continue
		}
		u, err := resource(client, t).Get(ctx, t.name, meta.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if u.GetAnnotations()[ownerAnnotation] != FieldManager {
			return fmt.Errorf("refusing to adopt existing %s/%s: use render with its current GitOps owner or explicitly migrate ownership first", t.resource.Resource, t.name)
		}
	}
	return nil
}

// Preview uses server-side dry run, not a client-side simulation. It can reject
// admission/RBAC/schema/field ownership conflicts without changing the cluster.
func (p *Plan) Preview(ctx context.Context, client dynamic.Interface, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("positive polling interval required")
	}
	if err := wait.PollUntilContextCancel(ctx, interval, true, func(ctx context.Context) (bool, error) { return prerequisites(ctx, client) }); err != nil {
		return fmt.Errorf("waiting for Calico prerequisites: %w", err)
	}
	if err := p.checkOwnership(ctx, client); err != nil {
		return err
	}
	return p.patch(ctx, client, true)
}

func (p *Plan) patch(ctx context.Context, client dynamic.Interface, dry bool) error {
	for i, t := range targets {
		data, err := p.objects[i].MarshalJSON()
		if err != nil {
			return err
		}
		opts := meta.PatchOptions{FieldManager: FieldManager}
		if dry {
			opts.DryRun = []string{meta.DryRunAll}
		}
		if _, err := resource(client, t).Patch(ctx, t.name, types.ApplyPatchType, data, opts); err != nil {
			return fmt.Errorf("apply %s/%s (dry-run=%t): %w", t.resource.Resource, t.name, dry, err)
		}
	}
	return nil
}

// Apply never forces ownership or deletes/recreates a resource. Multi-object
// Kubernetes updates are not atomic: on partial failure it returns an error;
// rerunning the same approved plan safely resumes through server-side apply.
func (p *Plan) Apply(ctx context.Context, client dynamic.Interface, approvedSHA string, interval time.Duration) error {
	if approvedSHA != p.SHA256() {
		return fmt.Errorf("approval must match exact manifest SHA256 %s", p.SHA256())
	}
	if err := p.Preview(ctx, client, interval); err != nil {
		return err
	}
	return p.patch(ctx, client, false)
}

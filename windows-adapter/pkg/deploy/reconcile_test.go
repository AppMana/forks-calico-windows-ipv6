package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	kt "k8s.io/client-go/testing"
)

func readyObjects() []runtime.Object {
	return []runtime.Object{
		&unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": map[string]interface{}{"name": "ipamconfigs.crd.projectcalico.org"}, "status": map[string]interface{}{"conditions": []interface{}{map[string]interface{}{"type": "Established", "status": "True"}}}}},
		&unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]interface{}{"name": "calico-node", "namespace": "kube-system"}}},
		&unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "apps/v1", "kind": "DaemonSet", "metadata": map[string]interface{}{"name": "calico-node", "namespace": "kube-system"}, "spec": map[string]interface{}{"template": map[string]interface{}{"spec": map[string]interface{}{"containers": []interface{}{map[string]interface{}{"name": "calico-node", "env": []interface{}{map[string]interface{}{"name": "CALICO_NETWORKING_BACKEND", "value": "bird"}}}}}}}}},
	}
}

func TestRejectsOtherBackendWithoutWrites(t *testing.T) {
	objects := readyObjects()
	ds := objects[2].(*unstructured.Unstructured)
	delete(ds.Object, "spec")
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	plan, _ := NewPlan(options())
	if err := plan.Preview(context.Background(), client, time.Millisecond); err == nil {
		t.Fatal("accepted unknown Linux backend")
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" {
			t.Fatal("wrote before checking backend")
		}
	}
}

func TestReapplyOwnedResourcesKeepsRequiredIPAMFields(t *testing.T) {
	plan, _ := NewPlan(options())
	objects := readyObjects()
	for _, object := range plan.objects {
		objects = append(objects, object.DeepCopy())
	}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	client.PrependReactor("patch", "*", func(a kt.Action) (bool, runtime.Object, error) {
		patch := a.(kt.PatchAction)
		if a.GetResource().Resource == "ipamconfigs" && !strings.Contains(string(patch.GetPatch()), `"autoAllocateBlocks":true`) {
			t.Fatal("reapply dropped required IPAM field")
		}
		return true, &unstructured.Unstructured{}, nil
	})
	for i := 0; i < 2; i++ {
		if err := plan.Apply(context.Background(), client, plan.SHA256(), time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExistingDisabledIPAMAllocationPolicyIsNeverOverwritten(t *testing.T) {
	plan, _ := NewPlan(options())
	objects := readyObjects()
	ipam := plan.objects[1].DeepCopy()
	if err := unstructured.SetNestedField(ipam.Object, false, "spec", "autoAllocateBlocks"); err != nil {
		t.Fatal(err)
	}
	objects = append(objects, ipam)
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
	if err := plan.Apply(context.Background(), client, plan.SHA256(), time.Millisecond); err == nil {
		t.Fatal("accepted allocation-policy change")
	}
	for _, action := range client.Actions() {
		if action.GetVerb() != "get" {
			t.Fatal("mutated before checking allocation policy")
		}
	}
}

func TestBootstrapIPAMIncludesRequiredSchemaFields(t *testing.T) {
	plan, err := NewPlan(options())
	if err != nil {
		t.Fatal(err)
	}
	// Upstream IPAMConfig CRD requires both booleans, including on a fresh
	// cluster before the first Windows node causes k0s to render resources.
	for _, key := range []string{"strictAffinity", "autoAllocateBlocks"} {
		value, found, err := unstructured.NestedBool(plan.objects[1].Object, "spec", key)
		if err != nil || !found || !value {
			t.Fatalf("fresh IPAMConfig missing required %s", key)
		}
	}
}

func TestApplyRequiresExactApprovalAndDryRunsAllBeforeWrites(t *testing.T) {
	plan, err := NewPlan(options())
	if err != nil {
		t.Fatal(err)
	}
	client := fake.NewSimpleDynamicClient(runtime.NewScheme(), readyObjects()...)
	var calls []bool
	client.PrependReactor("patch", "*", func(a kt.Action) (bool, runtime.Object, error) {
		opts := a.(interface{ GetPatchOptions() meta.PatchOptions }).GetPatchOptions()
		if opts.Force != nil && *opts.Force {
			t.Fatal("force ownership is forbidden")
		}
		if opts.FieldManager != FieldManager {
			t.Fatal("missing stable field manager")
		}
		calls = append(calls, len(opts.DryRun) > 0)
		return true, &unstructured.Unstructured{}, nil
	})
	// No API requests at all on a mismatched approval.
	if err := plan.Apply(context.Background(), client, "wrong", time.Millisecond); err == nil {
		t.Fatal("accepted unapproved change")
	}
	if len(client.Actions()) != 0 {
		t.Fatal("contacted API before validating approval")
	}
	if err := plan.Apply(context.Background(), client, plan.SHA256(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 6 {
		t.Fatalf("calls: %v", calls)
	}
	for i, dry := range calls {
		if dry != (i < 3) {
			t.Fatalf("mutation before all preflight checks: %v", calls)
		}
	}
}

func TestOwnershipConflictAndDryRunFailureNeverWrite(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "API rejects plan", true: "GitOps owns deployment"}[conflict], func(t *testing.T) {
			objects := readyObjects()
			if conflict {
				objects = append(objects, &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "apps/v1", "kind": "DaemonSet", "metadata": map[string]interface{}{"name": "calico-node-windows", "namespace": "kube-system"}}})
			}
			client := fake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
			writes := 0
			client.PrependReactor("patch", "*", func(a kt.Action) (bool, runtime.Object, error) {
				opts := a.(interface{ GetPatchOptions() meta.PatchOptions }).GetPatchOptions()
				if len(opts.DryRun) == 0 {
					writes++
				}
				return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "ipamconfigs"}, "default", errors.New("owned elsewhere"))
			})
			plan, _ := NewPlan(options())
			if err := plan.Apply(context.Background(), client, plan.SHA256(), time.Millisecond); err == nil {
				t.Fatal("unsafe apply passed")
			}
			if writes != 0 {
				t.Fatal("mutated rejected plan")
			}
		})
	}
}

func TestBootstrapWaitIsBoundedAndDoesNotRequireWindowsReadiness(t *testing.T) {
	plan, _ := NewPlan(options())
	client := fake.NewSimpleDynamicClient(runtime.NewScheme())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := plan.Preview(ctx, client, time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing prerequisites: %v", err)
	}
	for _, a := range client.Actions() {
		if a.GetVerb() != "get" {
			t.Fatal("mutated before prerequisites")
		}
	}
	client = fake.NewSimpleDynamicClient(runtime.NewScheme(), readyObjects()...)
	client.PrependReactor("get", "serviceaccounts", func(kt.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts"}, "calico-node", errors.New("denied"))
	})
	if err := plan.Preview(context.Background(), client, time.Millisecond); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("must fail immediately on authorization: %v", err)
	}
}

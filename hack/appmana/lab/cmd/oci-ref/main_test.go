package main

import (
	"encoding/json"
	"testing"

	digest "github.com/opencontainers/go-digest"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestExactReference(t *testing.T) {
	d := digest.FromString("image")
	ref := "example.com/image:pinned@" + d.String()
	index := oci.Index{Manifests: []oci.Descriptor{{Digest: d, Annotations: map[string]string{"keep": "yes"}}}}
	data, _ := json.Marshal(index)
	got, err := annotate(data, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &index); err != nil {
		t.Fatal(err)
	}
	if index.Manifests[0].Annotations[oci.AnnotationRefName] != ref || index.Manifests[0].Annotations["keep"] != "yes" {
		t.Fatalf("lost exact reference or existing metadata: %s", got)
	}
	for _, bad := range []string{"example.com/image:latest", "example.com/image@" + digest.FromString("different").String()} {
		if _, err := annotate(data, bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if _, err := annotate([]byte(`{"manifests":[]}`), ref); err == nil {
		t.Fatal("accepted empty layout")
	}
}

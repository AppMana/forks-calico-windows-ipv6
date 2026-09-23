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
	if len(index.Manifests) != 2 || index.Manifests[1].Annotations[oci.AnnotationRefName] != "example.com/image@"+d.String() {
		t.Fatalf("missing CRI digest-only alias: %s", got)
	}
	again, err := annotate(got, ref)
	if err != nil || string(again) != string(got) {
		t.Fatalf("annotation must be idempotent: %s: %v", again, err)
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

func TestDigestOnlyDoesNotDuplicateDescriptor(t *testing.T) {
	d := digest.FromString("image")
	data, _ := json.Marshal(oci.Index{Manifests: []oci.Descriptor{{Digest: d}}})
	got, err := annotate(data, "localhost:5000/image@"+d.String())
	if err != nil {
		t.Fatal(err)
	}
	var index oci.Index
	if err := json.Unmarshal(got, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Manifests) != 1 {
		t.Fatalf("duplicated descriptor: %s", got)
	}
}

// oci-ref preserves the exact runtime reference on a single-image OCI layout.
// Some pull tools normalize tag@digest into digest-only even with --annotate-ref.
package main

import (
	_ "crypto/sha256" // Register SHA-256 for go-digest's runtime validation.
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	digest "github.com/opencontainers/go-digest"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
)

func annotate(data []byte, ref string) ([]byte, error) {
	_, hash, ok := strings.Cut(ref, "@")
	if !ok {
		return nil, fmt.Errorf("explicit digest reference required")
	}
	d, err := digest.Parse(hash)
	if err != nil {
		return nil, err
	}
	var index oci.Index
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	if len(index.Manifests) != 1 || index.Manifests[0].Digest != d {
		return nil, fmt.Errorf("layout must have exactly one descriptor matching %s", d)
	}
	if index.Manifests[0].Annotations == nil {
		index.Manifests[0].Annotations = map[string]string{}
	}
	index.Manifests[0].Annotations[oci.AnnotationRefName] = ref
	return json.MarshalIndent(index, "", "  ")
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: oci-ref LAYOUT EXACT_RUNTIME_REFERENCE@sha256:DIGEST")
	}
	path := filepath.Join(os.Args[1], "index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	data, err = annotate(data, os.Args[2])
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

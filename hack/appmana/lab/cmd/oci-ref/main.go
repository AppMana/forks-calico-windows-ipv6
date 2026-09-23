// oci-ref preserves the exact runtime reference on a single-image OCI layout.
// Some pull tools normalize tag@digest into digest-only even with --annotate-ref.
package main

import (
	_ "crypto/sha256" // Register SHA-256 for go-digest's runtime validation.
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/distribution/reference"
	oci "github.com/opencontainers/image-spec/specs-go/v1"
)

func annotate(data []byte, ref string) ([]byte, error) {
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return nil, err
	}
	canonical, ok := named.(reference.Canonical)
	if !ok {
		return nil, fmt.Errorf("explicit digest reference required")
	}
	d := canonical.Digest()
	var index oci.Index
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, err
	}
	if len(index.Manifests) == 0 {
		return nil, fmt.Errorf("empty layout")
	}
	for _, descriptor := range index.Manifests {
		if descriptor.Digest != d {
			return nil, fmt.Errorf("all layout descriptors must match %s", d)
		}
	}
	// Keep both names: sandbox lookup uses the configured spelling, while
	// CRI's normalized image lookup strips the tag from tag@digest.
	alias := reference.TrimNamed(named).Name() + "@" + d.String()
	original := index.Manifests[0]
	index.Manifests = nil
	for _, name := range []string{ref, alias} {
		if len(index.Manifests) > 0 && name == ref {
			continue
		}
		descriptor := original
		descriptor.Annotations = make(map[string]string, len(original.Annotations)+1)
		for key, value := range original.Annotations {
			descriptor.Annotations[key] = value
		}
		descriptor.Annotations[oci.AnnotationRefName] = name
		index.Manifests = append(index.Manifests, descriptor)
	}
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

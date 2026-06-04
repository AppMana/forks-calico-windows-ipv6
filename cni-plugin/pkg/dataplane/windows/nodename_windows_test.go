// Copyright (c) 2026 Tigera, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package windows

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"

	"github.com/projectcalico/calico/cni-plugin/pkg/types"
)

func TestDetermineWindowsCNINodenameReadsNodenameFile(t *testing.T) {
	dir := t.TempDir()
	nodenameFile := filepath.Join(dir, "nodename")
	if err := os.WriteFile(nodenameFile, []byte("appmana-000\r\n"), 0600); err != nil {
		t.Fatal(err)
	}

	got := determineWindowsCNINodename(types.NetConf{
		NodenameFile:         nodenameFile,
		NodenameFileOptional: true,
	}, logrus.NewEntry(logrus.New()))
	if got != "appmana-000" {
		t.Fatalf("expected nodename from nodename_file, got %q", got)
	}
}

func TestDetermineWindowsCNINodenamePrefersExplicitNodename(t *testing.T) {
	dir := t.TempDir()
	nodenameFile := filepath.Join(dir, "nodename")
	if err := os.WriteFile(nodenameFile, []byte("from-file\n"), 0600); err != nil {
		t.Fatal(err)
	}

	got := determineWindowsCNINodename(types.NetConf{
		Nodename:             "from-conf",
		NodenameFile:         nodenameFile,
		NodenameFileOptional: true,
	}, logrus.NewEntry(logrus.New()))
	if got != "from-conf" {
		t.Fatalf("expected explicit nodename to win, got %q", got)
	}
}

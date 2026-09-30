package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestRetainFailurePowersOffBothNodes(t *testing.T) {
	for _, failure := range []string{"", "keep", "linux", "windows"} {
		var got []string
		step := func(name string) error {
			got = append(got, name)
			if name == failure {
				return errors.New(name)
			}
			return nil
		}
		err := retainFailedQualification(func() error { return step("keep") }, step)
		if !reflect.DeepEqual(got, []string{"keep", "linux", "windows"}) {
			t.Fatalf("incomplete cleanup: %v", got)
		}
		if (err != nil) != (failure != "") {
			t.Fatalf("failure=%s err=%v", failure, err)
		}
	}
}

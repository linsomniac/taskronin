package buildinfo

import "testing"

func TestAPIVersion(t *testing.T) {
	if APIVersion != "v1" {
		t.Fatalf("APIVersion = %q", APIVersion)
	}
}

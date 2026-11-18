package testutil

import (
	"flag"
	"testing"
)

func TestGoldenRejectsNonBoolUpdateFlag(t *testing.T) {
	flags := flag.NewFlagSet("conflict", flag.ContinueOnError)
	flags.String("update", "", "another helper")
	defer func() {
		if got := recover(); got != "testutil: existing -update flag must be a bool" {
			t.Fatalf("panic = %v", got)
		}
	}()
	registerUpdateFlag(flags)
}

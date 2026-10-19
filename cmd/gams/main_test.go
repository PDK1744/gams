package main

import (
	"testing"
)

func TestRunVersion(t *testing.T) {
	if err := run([]string{"version"}); err != nil {
		t.Fatalf("run version failed: %v", err)
	}
}

func TestRunHelp(t *testing.T) {
	if err := run([]string{"help"}); err != nil {
		t.Fatalf("run help failed: %v", err)
	}
}

func TestRunInvalidFlag(t *testing.T) {
	if err := run([]string{"serve", "--invalid-flag"}); err == nil {
		t.Fatalf("expected error for invalid flag, got nil")
	}
}

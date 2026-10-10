package main

import "testing"

func TestFeatureCommandRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"feature", "list"})
	if err != nil || cmd == rootCmd || cmd.Name() != "list" || cmd.Parent().Name() != "feature" {
		t.Fatalf("feature list must be registered: command=%s error=%v", cmd.Name(), err)
	}
}

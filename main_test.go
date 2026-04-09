package main

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveProfileDirUsesConfigDirForRelativePaths(t *testing.T) {
	var cfgPath, want string
	if runtime.GOOS == "windows" {
		cfgPath = `C:\Projects\nexus\nexus-config.yaml`
		want = `C:\Projects\nexus\nexus-profiles`
	} else {
		cfgPath = `/home/user/nexus/nexus-config.yaml`
		want = `/home/user/nexus/nexus-profiles`
	}
	got := resolveProfileDir("nexus-profiles", cfgPath, filepath.Dir(filepath.Dir(cfgPath)))
	if got != want {
		t.Fatalf("resolveProfileDir() = %q, want %q", got, want)
	}
}

func TestResolveProfileDirLeavesAbsolutePathUntouched(t *testing.T) {
	var want, cfgPath string
	if runtime.GOOS == "windows" {
		want = `C:\Profiles`
		cfgPath = `C:\Projects\nexus\nexus-config.yaml`
	} else {
		want = `/opt/profiles`
		cfgPath = `/home/user/nexus/nexus-config.yaml`
	}
	if got := resolveProfileDir(want, cfgPath, filepath.Dir(filepath.Dir(cfgPath))); got != want {
		t.Fatalf("resolveProfileDir() = %q, want %q", got, want)
	}
}

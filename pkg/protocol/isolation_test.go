package protocol

import "testing"

func TestComputeIsolationLadder(t *testing.T) {
	tests := []struct {
		name    string
		inside  bool
		noNet   bool
		perm    PermissionMode
		sandbox string
		want    string
	}{
		{"container", true, false, PermissionModeDefault, "off", IsolationContainer},
		{"container no net", true, true, PermissionModeYolo, "workspace-write", IsolationContainerNoNet},
		{"host yolo", false, false, PermissionModeYolo, "workspace-write", IsolationHostYolo},
		{"host yolo ignores sandbox off", false, false, PermissionModeYolo, "off", IsolationHostYolo},
		{"host default", false, false, PermissionModeDefault, "off", IsolationHostDefault},
		{"host sandbox workspace", false, false, PermissionModeDefault, "workspace-write", IsolationHostSandbox},
		{"host sandbox readonly", false, false, PermissionModeDefault, "read-only", IsolationHostSandbox},
		{"host empty sandbox → sandbox", false, false, PermissionModeDefault, "", IsolationHostSandbox},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeIsolation(tc.inside, tc.noNet, tc.perm, tc.sandbox)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestParseIsolationEnv(t *testing.T) {
	if p, ok := ParseIsolationEnv("container+no-network"); !ok || p != IsolationContainerNoNet {
		t.Fatalf("%q %v", p, ok)
	}
	if p, ok := ParseIsolationEnv("CONTAINER"); !ok || p != IsolationContainer {
		t.Fatalf("%q %v", p, ok)
	}
	if _, ok := ParseIsolationEnv(""); ok {
		t.Fatal("empty")
	}
	if p, ok := ParseIsolationEnv("host+yolo"); !ok || p != IsolationHostYolo {
		t.Fatalf("%q %v", p, ok)
	}
}

func TestIsolationDescribe(t *testing.T) {
	if IsolationDescribe(IsolationContainer) == "" {
		t.Fatal("empty describe")
	}
	if IsolationShort(IsolationHostSandbox) != IsolationHostSandbox {
		t.Fatal("short")
	}
}

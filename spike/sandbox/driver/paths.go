package main

// State directory baked into spike/sandbox/guest.nix. The runner cannot
// retarget the virtiofs source or the virtio-serial socket without a rebuild.
const (
	defaultStateDir = "/var/tmp/strike-phase0"

	workspaceName = "workspace"
	markerName    = "marker.txt"
	markerBody    = "phase0-workspace-marker\n"
	canaryName    = "host-canary"
	canaryBody    = "host-only-canary\n"
	secretRel     = "secrets/auth.json"
	// Fake marker. Not a credential. The host scan looks for this string in
	// the workspace share and fails the proof if it shows up there.
	dummySecret = "sk-phase0-dummy"

	virtiofsSocketName = "strike-phase0-virtiofs-workspace.sock"
	rpcSocketName      = "rpc.sock"
	qmpSocketName      = "qmp.sock"

	guestMemConfiguredMB = 1024
	echoPrompt           = "phase0 hello"
)

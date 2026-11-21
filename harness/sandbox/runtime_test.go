package sandbox

import (
	"bytes"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestParseRuntime(t *testing.T) {
	cases := []struct {
		in   string
		want Runtime
		ok   bool
	}{
		{"", RuntimeProcess, true},
		{"process", RuntimeProcess, true},
		{" Process ", RuntimeProcess, true},
		{"microvm", RuntimeMicroVM, true},
		{"micro_vm", RuntimeMicroVM, true},
		{"MICROVM", RuntimeMicroVM, true},
		{"vm", 0, false},
		{"bwrap", 0, false},
	}
	for _, tc := range cases {
		got, ok := ParseRuntime(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("ParseRuntime(%q) = %v,%v want %v,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if RuntimeProcess.String() != "process" || RuntimeMicroVM.String() != "microvm" {
		t.Fatalf("String: %q %q", RuntimeProcess, RuntimeMicroVM)
	}
	if DefaultRuntime != RuntimeProcess {
		t.Fatalf("DefaultRuntime = %v, want process", DefaultRuntime)
	}
}

func TestResolveRuntimeUnknownFailsClosed(t *testing.T) {
	cases := map[string]Runtime{"": RuntimeProcess, "process": RuntimeProcess, "microvm": RuntimeMicroVM, "typo": RuntimeMicroVM}
	for in, want := range cases {
		if got := ResolveRuntime(in); got != want {
			t.Errorf("ResolveRuntime(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCheckRuntimeMode(t *testing.T) {
	cases := []struct {
		mode    Mode
		rt      Runtime
		wantErr bool
	}{
		{ModeOff, RuntimeProcess, false},
		{ModeWorkspaceWrite, RuntimeProcess, false},
		{ModeOff, RuntimeMicroVM, true},
		{ModeReadOnly, RuntimeMicroVM, false},
		{ModeWorkspaceWrite, RuntimeMicroVM, false},
	}
	for _, tc := range cases {
		if err := CheckRuntimeMode(tc.mode, tc.rt); (err != nil) != tc.wantErr {
			t.Errorf("CheckRuntimeMode(%v,%v) err=%v wantErr=%v", tc.mode, tc.rt, err, tc.wantErr)
		}
	}
}

func TestDenyDegraded(t *testing.T) {
	degraded := Result{Degraded: true}
	cases := []struct {
		name string
		p    Policy
		r    Result
		want bool
	}{
		{"applied never denied", Policy{Mode: ModeWorkspaceWrite, Runtime: RuntimeMicroVM}, Result{Applied: true}, false},
		{"process off", Policy{Mode: ModeOff}, degraded, false},
		{"process fail-closed", Policy{Mode: ModeWorkspaceWrite}, degraded, true},
		{"process allow degrade", Policy{Mode: ModeWorkspaceWrite, AllowDegrade: true}, degraded, false},
		{"microvm denied", Policy{Mode: ModeWorkspaceWrite, Runtime: RuntimeMicroVM}, degraded, true},
		{"microvm ignores allow degrade", Policy{Mode: ModeWorkspaceWrite, Runtime: RuntimeMicroVM, AllowDegrade: true}, degraded, true},
		{"microvm with off denied", Policy{Mode: ModeOff, Runtime: RuntimeMicroVM, AllowDegrade: true}, degraded, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.DenyDegraded(tc.r); got != tc.want {
				t.Fatalf("DenyDegraded = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeniedMessage(t *testing.T) {
	p := Policy{Mode: ModeWorkspaceWrite, Runtime: RuntimeMicroVM}
	msg := p.DeniedMessage(Result{Degraded: true, Reason: "/dev/kvm not found"})
	for _, want := range []string{"microvm", "/dev/kvm not found", "sandboxRuntime: process"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
	proc := Policy{Mode: ModeWorkspaceWrite}.DeniedMessage(Result{Degraded: true})
	if !strings.Contains(proc, "sandboxAllowDegrade") {
		t.Errorf("process message = %q", proc)
	}
}

// fakeKVM records the ioctl steps the probe ran.
type fakeKVM struct {
	openErr, verErr, vmErr, vcpuErr error
	version                         int
	steps                           []string
}

func (f *fakeKVM) Open() error {
	f.steps = append(f.steps, "open")
	return f.openErr
}

func (f *fakeKVM) APIVersion() (int, error) {
	f.steps = append(f.steps, "version")
	return f.version, f.verErr
}

func (f *fakeKVM) CreateVM() error {
	f.steps = append(f.steps, "vm")
	return f.vmErr
}

func (f *fakeKVM) CreateVCPU() error {
	f.steps = append(f.steps, "vcpu")
	return f.vcpuErr
}

func (f *fakeKVM) Close() { f.steps = append(f.steps, "close") }

func TestProbeMicroVMWith(t *testing.T) {
	present := func() (string, bool) { return "", true }
	absent := func() (string, bool) { return "no driver here", false }
	cases := []struct {
		name      string
		kvm       fakeKVM
		driver    func() (string, bool)
		available bool
		reason    string
		steps     []string
	}{
		{"no device", fakeKVM{openErr: fs.ErrNotExist}, present, false, "/dev/kvm not found", []string{"open"}},
		{"no permission", fakeKVM{openErr: syscall.EACCES}, present, false, "permission denied", []string{"open"}},
		{"open other", fakeKVM{openErr: errors.New("boom")}, present, false, "boom", []string{"open"}},
		{"version error", fakeKVM{verErr: syscall.EINVAL}, present, false, "KVM_GET_API_VERSION", []string{"open", "version", "close"}},
		{"bad version", fakeKVM{version: 11}, present, false, "unsupported KVM API version 11", []string{"open", "version", "close"}},
		{"create vm fails", fakeKVM{version: 12, vmErr: syscall.ENOMEM}, present, false, "KVM_CREATE_VM", []string{"open", "version", "vm", "close"}},
		// The vCPU step can oops broken nested hosts: never run it without a driver.
		{"no driver skips vcpu", fakeKVM{version: 12}, absent, false, "no driver here", []string{"open", "version", "vm", "close"}},
		{"nil driver skips vcpu", fakeKVM{version: 12}, nil, false, "no microvm driver", []string{"open", "version", "vm", "close"}},
		{"vcpu fails", fakeKVM{version: 12, vcpuErr: syscall.EFAULT}, present, false, "KVM_CREATE_VCPU", []string{"open", "version", "vm", "vcpu", "close"}},
		{"available", fakeKVM{version: 12}, present, true, "", []string{"open", "version", "vm", "vcpu", "close"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := tc.kvm
			st := probeMicroVMWith(&k, tc.driver)
			if st.Available != tc.available {
				t.Fatalf("Available = %v, want %v (reason %q)", st.Available, tc.available, st.Reason)
			}
			if !strings.Contains(st.Reason, tc.reason) {
				t.Fatalf("Reason = %q, want substring %q", st.Reason, tc.reason)
			}
			if tc.available && st.Reason != "" {
				t.Fatalf("available with reason %q", st.Reason)
			}
			if !reflect.DeepEqual(k.steps, tc.steps) {
				t.Fatalf("steps = %v, want %v", k.steps, tc.steps)
			}
		})
	}
}

func TestDefaultDriverAbsent(t *testing.T) {
	if _, ok := microvmDriver(); ok {
		t.Fatal("no microvm driver ships in this build")
	}
}

func TestMicroVMAvailabilityProbesOnce(t *testing.T) {
	ResetWarnForTest()
	t.Cleanup(ResetWarnForTest)
	calls := 0
	orig := microvmProbe
	microvmProbe = func() MicroVMStatus {
		calls++
		return MicroVMStatus{Reason: "forced"}
	}
	t.Cleanup(func() { microvmProbe = orig })
	if _, ok := MicroVMStatusIfProbed(); ok {
		t.Fatal("status reported probed before any probe")
	}
	for i := 0; i < 3; i++ {
		if st := MicroVMAvailability(); st.Available || st.Reason != "forced" {
			t.Fatalf("status = %+v", st)
		}
	}
	if calls != 1 {
		t.Fatalf("probe ran %d times, want 1", calls)
	}
}

func TestWrapResultMicroVMFailsClosed(t *testing.T) {
	argv := []string{"bash", "-c", "echo hi"}
	cases := []struct {
		name   string
		status MicroVMStatus
		mode   Mode
		reason string
	}{
		{"unavailable", MicroVMStatus{Reason: "/dev/kvm not found"}, ModeWorkspaceWrite, "/dev/kvm not found"},
		{"read-only unavailable", MicroVMStatus{Reason: "no kvm"}, ModeReadOnly, "no kvm"},
		{"available but no bridge", MicroVMStatus{Available: true}, ModeWorkspaceWrite, "exec bridge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ResetWarnForTest()
			t.Cleanup(ResetWarnForTest)
			var warn bytes.Buffer
			SetWarnWriter(&warn)
			t.Cleanup(func() { SetWarnWriter(nil) })
			ForceMicroVMStatusForTest(tc.status)
			p := Policy{Mode: tc.mode, WorkDir: t.TempDir(), Runtime: RuntimeMicroVM, AllowDegrade: true}
			res := WrapResult(argv, p)
			if res.Applied || !res.Degraded || res.Backend != "" {
				t.Fatalf("result = %+v, want degraded and not applied", res)
			}
			if !reflect.DeepEqual(res.Argv, argv) {
				t.Fatalf("argv = %#v", res.Argv)
			}
			if !strings.Contains(res.Reason, tc.reason) {
				t.Fatalf("Reason = %q, want %q", res.Reason, tc.reason)
			}
			if !p.DenyDegraded(res) {
				t.Fatal("microvm degraded wrap must fail closed even with AllowDegrade")
			}
			if warn.Len() != 0 {
				t.Fatalf("microvm must not print the 'runs unsandboxed' warning: %q", warn.String())
			}
		})
	}
}

func TestWrapResultMicroVMWithModeOffIsConfigError(t *testing.T) {
	ResetWarnForTest()
	t.Cleanup(ResetWarnForTest)
	res := WrapResult([]string{"true"}, Policy{Mode: ModeOff, Runtime: RuntimeMicroVM})
	if !res.Degraded || res.Applied || !strings.Contains(res.Reason, "sandboxRuntime microvm requires") {
		t.Fatalf("result = %+v", res)
	}
	if _, ok := MicroVMStatusIfProbed(); ok {
		t.Fatal("config error path must not probe /dev/kvm")
	}
}

func TestWrapResultProcessRuntimeNeverProbesMicroVM(t *testing.T) {
	ResetWarnForTest()
	t.Cleanup(ResetWarnForTest)
	orig := microvmProbe
	microvmProbe = func() MicroVMStatus {
		t.Fatal("process runtime probed microvm")
		return MicroVMStatus{}
	}
	t.Cleanup(func() { microvmProbe = orig })
	forceSetAvailabilityForTest(availInfo{warn: "forced unavailable"})
	SetWarnWriter(&bytes.Buffer{})
	t.Cleanup(func() { SetWarnWriter(nil) })
	_ = WrapResult([]string{"true"}, Policy{Mode: ModeWorkspaceWrite, WorkDir: t.TempDir()})
	_ = Explain(Policy{Mode: ModeWorkspaceWrite, WorkDir: t.TempDir()})
}

func TestProcessRuntimeExplainAndWrapUnchanged(t *testing.T) {
	ResetWarnForTest()
	t.Cleanup(ResetWarnForTest)
	wd := t.TempDir()
	base := Policy{Mode: ModeWorkspaceWrite, WorkDir: wd, DenyWriteGlobs: []string{"**/*.env"}}
	explicit := base
	explicit.Runtime = RuntimeProcess
	if Explain(base) != Explain(explicit) {
		t.Fatal("explicit process runtime changed explain output")
	}
	if ProfileText(base) != ProfileText(explicit) {
		t.Fatal("explicit process runtime changed profile text")
	}
	got := Explain(explicit)
	for _, bad := range []string{"sandbox runtime", "microvm"} {
		if strings.Contains(got, bad) {
			t.Fatalf("process explain mentions %q:\n%s", bad, got)
		}
	}
	a := WrapResult([]string{"bash", "-c", "true"}, base)
	b := WrapResult([]string{"bash", "-c", "true"}, explicit)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("wrap differs: %+v vs %+v", a, b)
	}
	if b.Reason != "" {
		t.Fatalf("process runtime set Reason %q", b.Reason)
	}
}

func TestExplainMicroVMDoesNotProbe(t *testing.T) {
	ResetWarnForTest()
	t.Cleanup(ResetWarnForTest)
	p := Policy{Mode: ModeWorkspaceWrite, WorkDir: t.TempDir(), Runtime: RuntimeMicroVM, AllowDegrade: true}
	got := Explain(p)
	if _, ok := MicroVMStatusIfProbed(); ok {
		t.Fatal("Explain probed the microvm runtime")
	}
	for _, want := range []string{"sandbox runtime: microvm", "microvm status: unknown", "fallback: none", "degrade policy: deny"} {
		if !strings.Contains(got, want) {
			t.Errorf("explain missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "unsandboxed fallback permitted") {
		t.Errorf("microvm explain claims host fallback:\n%s", got)
	}
	ForceMicroVMStatusForTest(MicroVMStatus{Reason: "/dev/kvm not found"})
	if got := Explain(p); !strings.Contains(got, "microvm status: unavailable: /dev/kvm not found") {
		t.Errorf("explain after probe missing reason:\n%s", got)
	}
	if got := ProfileText(p); !strings.Contains(got, "microvm runtime") {
		t.Errorf("profile = %q", got)
	}
	off := Explain(Policy{Mode: ModeOff, Runtime: RuntimeMicroVM})
	if !strings.Contains(off, "config error") {
		t.Errorf("off+microvm explain missing config error:\n%s", off)
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectScript(t *testing.T) {
	good := `
exec /nix/store/abc/bin/qemu-system-x86_64 \
  '-M' 'microvm,accel=tcg,acpi=on,pcie=on' \
  '-cpu' 'max' \
  '-chardev' 'socket,id=fs0,path=strike-phase0-virtiofs-workspace.sock' \
  '-device' 'vhost-user-fs-pci,chardev=fs0,tag=workspace' \
  '-device' 'virtserialport,chardev=strikerpc,name=strike.rpc'
`
	if err := inspectScript(good); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		script string
		want   string
	}{
		{"kvm flag", good + " '-enable-kvm'", "kvm"},
		{"accel fallback", "accel=kvm:tcg vhost-user-fs-pci tag=workspace virtserialport accel=tcg", "kvm"},
		{"cpu host", good + " '-cpu' 'host,+x2apic,-sgx'", "cpu host"},
		{"netdev", good + " '-netdev' 'user,id=n'", "netdev"},
		{"vsock", good + " 'vhost-vsock-pci,guest-cid=3'", "vhost-vsock"},
		{"second share", good + " '-device' 'vhost-user-fs-pci,tag=cache'", "count = 2"},
		{"9p", good + " '-device' 'virtio-9p-pci,mount_tag=store'", "9p"},
		{"no tcg", `
'-device' 'vhost-user-fs-pci,tag=workspace' '-device' 'virtserialport' '-cpu' 'max'
`, "accel=tcg"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := inspectScript(tc.script)
			if err == nil {
				t.Fatal("expected policy error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestDirectVirtiofsdParsesConf(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(dir, "supervisord.conf")
	if err := os.WriteFile(conf, []byte("[program:virtiofsd-workspace]\ncommand="+dir+"/virtiofsd-workspace\n[supervisord]\nuser=root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexec supervisord --configuration " + conf + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "bin", "virtiofsd-run"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := directVirtiofsd(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir+"/virtiofsd-workspace" {
		t.Fatalf("got %q", got)
	}
}

func TestGuestNixIsTCGAndOneShare(t *testing.T) {
	b, err := os.ReadFile("../guest.nix")
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{
		`accel = "tcg"`,
		`cpu = "qemu64"`,
		`proto = "virtiofs"`,
		`tag = "workspace"`,
		`source = "${stateDir}/workspace"`,
		`storeOnDisk = true`,
		`vsock.cid = null`,
		`interfaces = [ ]`,
		`refusing $arg`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("guest.nix missing %q", want)
		}
	}
	if strings.Contains(text, `source = "/nix/store"`) {
		t.Fatal("guest shares the host nix store")
	}
	if strings.Contains(text, `type = "user"`) {
		t.Fatal("guest defines a user netdev")
	}
}

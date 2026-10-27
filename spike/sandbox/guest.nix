# THROWAWAY Phase 0 guest. Paths match spike/sandbox/driver/paths.go.
{ config, lib, pkgs, strike, ... }:
let
  stateDir = "/var/tmp/strike-phase0";
  # microvm's Linux runner adds -enable-kvm whenever cpu is null, and it
  # puts accel=kvm:tcg on the machine line. Both call KVM_CREATE_VCPU.
  # This wrapper is the last line of defence if the runner text changes.
  qemuTcg = pkgs.writeShellScriptBin "qemu-system-x86_64" ''
    set -eu
    real=${pkgs.qemu_kvm}/bin/qemu-system-x86_64
    out=()
    for arg in "$@"; do
      case "$arg" in
        -enable-kvm|/dev/kvm)
          echo "phase0: refusing $arg" >&2
          exit 2
          ;;
        *)
          arg=$(printf '%s' "$arg" | ${pkgs.gnused}/bin/sed -e 's/accel=kvm:tcg/accel=tcg/g' -e 's/accel=kvm/accel=tcg/g')
          case "$arg" in
            host|host,*)
              echo "phase0: rewriting cpu host to max" >&2
              arg=max
              ;;
          esac
          out+=("$arg")
          ;;
      esac
    done
    # Line-buffer QEMU stdout. A log file is not a tty, so the serial
    # console would otherwise stay stuck in a block buffer until exit.
    exec ${pkgs.coreutils}/bin/stdbuf -oL -eL "$real" "''${out[@]}"
  '';
  probe = pkgs.writeShellScript "phase0-probe" ''
    set +e
    out=/workspace/probe.txt
    {
      echo probe_format=1
      echo "hostname=$(cat /proc/sys/kernel/hostname)"
      ifaces=$(${pkgs.coreutils}/bin/ls /sys/class/net | ${pkgs.gnugrep}/bin/grep -vx lo | ${pkgs.coreutils}/bin/tr '\n' ' ' | ${pkgs.gnused}/bin/sed 's/ *$//')
      echo "non_lo_ifaces=$ifaces"
      if ${pkgs.iproute2}/bin/ip -4 route show default | ${pkgs.gnugrep}/bin/grep -q .; then
        echo default_route=present
      else
        echo default_route=absent
      fi
      if ${pkgs.iputils}/bin/ping -c 1 -W 2 1.1.1.1 >/dev/null 2>&1; then
        echo net_ping=reachable
      else
        echo net_ping=blocked
      fi
      if ${pkgs.coreutils}/bin/timeout 3 ${pkgs.bash}/bin/bash -c 'echo >/dev/tcp/1.1.1.1/443' >/dev/null 2>&1; then
        echo net_tcp=reachable
      else
        echo net_tcp=blocked
      fi
      if ${pkgs.coreutils}/bin/timeout 3 ${pkgs.bash}/bin/bash -c 'echo >/dev/tcp/169.254.169.254/80' >/dev/null 2>&1; then
        echo net_metadata=reachable
      else
        echo net_metadata=blocked
      fi
      if ${pkgs.coreutils}/bin/timeout 3 ${pkgs.glibc.bin}/bin/getent hosts example.com >/dev/null 2>&1; then
        echo net_dns=reachable
      else
        echo net_dns=blocked
      fi
      if [ -e ${stateDir}/host-canary ]; then echo host_canary=present; else echo host_canary=absent; fi
      if [ -e ${stateDir}/secrets/auth.json ]; then echo host_auth=present; else echo host_auth=absent; fi
      if [ -d /home/ubuntu ]; then echo host_home=present; else echo host_home=absent; fi
      if [ -e /workspace/../host-canary ] || [ -e /host-canary ]; then
        echo workspace_parent_escape=present
      else
        echo workspace_parent_escape=absent
      fi
      if [ -f /workspace/marker.txt ]; then echo workspace_marker=present; else echo workspace_marker=absent; fi
      if [ -e /root/.strike/auth.json ] || [ -e /tmp/strike-home/.strike/auth.json ]; then
        echo strike_auth=present
      else
        echo strike_auth=absent
      fi
      # NixOS systemd mounts an empty /run/credentials for journald.
      # A provider key would be a regular file. Do not print names.
      if ${pkgs.findutils}/bin/find /run/secrets /run/credentials -type f -print -quit 2>/dev/null | ${pkgs.gnugrep}/bin/grep -q .; then
        echo run_secrets=present
      else
        echo run_secrets=absent
      fi
      echo env_names_begin
      # Names only. Values stay off this file.
      env | ${pkgs.gawk}/bin/awk -F= '
        {
          n=tolower($1)
          if (n ~ /key/ || n ~ /token/ || n ~ /secret/ || n ~ /password/ || n ~ /api/)
            print "env_name " $1
        }'
      echo env_names_end
      echo workspace_entries_begin
      ${pkgs.coreutils}/bin/ls -A /workspace 2>/dev/null | ${pkgs.gnused}/bin/sed 's/^/entry /'
      echo workspace_entries_end
      ${pkgs.gawk}/bin/awk '/^MemTotal:/ {print "mem_total_kb="$2} /^MemAvailable:/ {print "mem_available_kb="$2}' /proc/meminfo
      # findmnt's tree glyphs hide the /workspace target from a prefix check.
      ${pkgs.util-linux}/bin/findmnt -n -T /workspace -o TARGET,FSTYPE,SOURCE | ${pkgs.gnused}/bin/sed 's/^/mount /'
      ${pkgs.util-linux}/bin/findmnt -n -o TARGET,FSTYPE,SOURCE | ${pkgs.gnused}/bin/sed 's/^/mount_tree /'
      echo probe_end=ok
    } > "$out" 2>&1
    echo PHASE0_PROBE_DONE > /dev/console
  '';
  rpcStart = pkgs.writeShellScript "strike-rpc-start" ''
    set -eu
    port=/dev/virtio-ports/strike.rpc
    i=0
    while [ ! -e "$port" ] && [ "$i" -lt 300 ]; do
      i=$((i + 1))
      sleep 0.1
    done
    if [ ! -e "$port" ]; then
      echo STRIKE_RPC_NO_PORT > /dev/console
      ls -l /dev /dev/virtio-ports > /dev/console 2>&1 || true
      exit 1
    fi
    mkdir -p /tmp/strike-home
    export HOME=/tmp/strike-home
    export XDG_CONFIG_HOME=/tmp/strike-home
    export XDG_CACHE_HOME=/tmp/strike-home
    cd /workspace
    echo STRIKE_RPC_START > /dev/console
    exec 3<>"$port"
    exec ${strike}/bin/strike rpc --provider echo --auto <&3 >&3
  '';
in
{
  networking.hostName = "strike-phase0";
  system.stateVersion = "25.11";

  # Spike-only. nokaslr skips the RDRAND poke that hung -cpu max.
  # notsc + lpj skip TSC and delay calibration, which hung -cpu qemu64
  # when the microvm machine had no PIT. lpj is a boot workaround, not a
  # measurement.
  boot.kernelParams = [
    "nokaslr"
    "notsc"
    "lpj=2500000"
  ];
  boot.consoleLogLevel = 7;

  microvm = {
    hypervisor = "qemu";
    vcpu = 1;
    # Configuration, not a measurement. 512 MB is microvm's default and is
    # tight once systemd and strike are both up.
    mem = 1024;
    # max advertises RDRAND. Under TCG the guest sat at
    # "Poking KASLR using RDRAND RDTSC..." for >6 min at 100% CPU with
    # RSS unchanged. qemu64 does not advertise that instruction.
    cpu = "qemu64";
    qemu.machine = "microvm";
    qemu.machineOpts = {
      accel = "tcg";
      mem-merge = "off";
      acpi = "on";
      # pit/pic on so TCG has a timer. Off left the guest spinning at
      # "Marking TSC unstable" with no further console output.
      pit = "on";
      pic = "on";
      pcie = "on";
      rtc = "on";
      usb = "off";
    };
    qemu.package = qemuTcg;
    qemu.serialConsole = true;
    qemu.extraArgs = [
      "-chardev" "socket,id=strikerpc,path=${stateDir}/rpc.sock,server=on,wait=off"
      "-device" "virtio-serial-pci"
      "-device" "virtserialport,chardev=strikerpc,name=strike.rpc"
    ];
    interfaces = [ ];
    forwardPorts = [ ];
    volumes = [ ];
    vsock.cid = null;
    storeOnDisk = true;
    writableStoreOverlay = null;
    graphics.enable = false;
    socket = "${stateDir}/qmp.sock";
    virtiofsd.group = null;
    # prefer can fail on some host filesystems; the share is one small directory.
    virtiofsd.inodeFileHandles = "never";
    shares = [
      {
        proto = "virtiofs";
        tag = "workspace";
        source = "${stateDir}/workspace";
        mountPoint = "/workspace";
        readOnly = false;
        # Guest writes (the probe file) must show up on the host promptly.
        cache = "never";
      }
    ];
  };

  networking.useNetworkd = lib.mkForce false;
  networking.useDHCP = false;
  networking.dhcpcd.enable = lib.mkForce false;
  systemd.network.enable = lib.mkForce false;
  services.resolved.enable = lib.mkForce false;
  networking.firewall.enable = false;

  documentation.enable = false;
  environment.defaultPackages = lib.mkForce [ ];

  systemd.services."serial-getty@ttyS0".enable = false;
  systemd.services."getty@tty1".enable = false;

  systemd.services.phase0-probe = {
    description = "Phase 0 isolation probe";
    wantedBy = [ "multi-user.target" ];
    after = [ "local-fs.target" ];
    before = [ "strike-rpc.service" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = probe;
    };
  };

  systemd.services.strike-rpc = {
    description = "strike rpc on virtio-serial";
    wantedBy = [ "multi-user.target" ];
    after = [ "phase0-probe.service" "local-fs.target" ];
    requires = [ "phase0-probe.service" ];
    serviceConfig = {
      Type = "simple";
      WorkingDirectory = "/workspace";
      ExecStart = rpcStart;
      Restart = "no";
      StandardOutput = "journal+console";
      StandardError = "journal+console";
    };
  };

  # The module argument is referenced by rpcStart; keep the package in the closure.
  environment.systemPackages = [ strike pkgs.iproute2 pkgs.iputils ];
}

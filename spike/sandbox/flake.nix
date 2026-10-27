# THROWAWAY Phase 0 image. Not a product flake. See README.md.
{
  description = "Throwaway NixOS microvm guest that runs strike rpc";

  inputs = {
    # Same nixpkgs rev as the repo flake.lock (Go 1.26).
    nixpkgs.url = "github:NixOS/nixpkgs/37bfb33419c6e367e8bb6f44041eaecf953b5236";
    # Source only. Do not evaluate microvm's flake (it pulls extra inputs).
    microvm = {
      url = "github:microvm-nix/microvm.nix/4876e2ae2de6ce225f4ccf664ae441128aa3f03a";
      flake = false;
    };
    strikeSrc = {
      url = "path:../..";
      flake = false;
    };
  };

  outputs = { self, nixpkgs, microvm, strikeSrc }:
    let
      system = "x86_64-linux";
      pkgs = import nixpkgs { inherit system; };
      # Report and spike driver edits must not rebuild the guest closure.
      strikeFiltered = pkgs.lib.cleanSourceWith {
        src = strikeSrc;
        filter = path: type:
          let
            rel = pkgs.lib.removePrefix (toString strikeSrc + "/") (toString path);
          in
          pkgs.lib.cleanSourceFilter path type
          && !(pkgs.lib.hasPrefix "spike/" rel)
          && rel != "docs/design/sandbox/phase0-report.md"
          && rel != "docs/design/sandbox/phase0-evidence.json";
      };
      strike = pkgs.buildGoModule {
        pname = "strike";
        version = "phase0";
        src = strikeFiltered;
        proxyVendor = true;
        vendorHash = "sha256-kEJmV3MQt8k5KuXBjYoUy7+ZZrD3aTljpBziaRndJcU=";
        subPackages = [ "cmd/strike" ];
        doCheck = false;
        env = {
          CGO_ENABLED = "0";
          GOTOOLCHAIN = "local";
        };
        ldflags = [
          "-s"
          "-w"
          "-X github.com/jonathanung/strike/internal/product/version.Version=phase0"
        ];
        preBuild = ''
          go generate ./internal/frontend/tui/app
        '';
      };
    in
    {
      nixosConfigurations.guest = nixpkgs.lib.nixosSystem {
        inherit system;
        specialArgs = { inherit strike; };
        modules = [
          "${microvm}/nixos-modules/microvm"
          ./guest.nix
        ];
      };
      packages.${system}.default = self.nixosConfigurations.guest.config.microvm.declaredRunner;
    };
}

{
  description = "strike-cli isolated dev environment (Go 1.26.2+ per go.mod)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    # Pinned Rust toolchain (incl. wasm32 std) for the webui — pure Nix,
    # no rustup downloads inside the dev shell.
    fenix = {
      url = "github:nix-community/fenix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, flake-utils, fenix }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
        rustToolchain = fenix.packages.${system}.combine (with fenix.packages.${system}; [
          stable.cargo
          stable.rustc
          stable.rustfmt
          targets.wasm32-unknown-unknown.stable.rust-std
        ]);
        version = "0-unstable-${builtins.substring 0 8 (self.lastModifiedDate or "19700101")}";
        commit = self.shortRev or self.dirtyShortRev or "unknown";
        strike = pkgs.buildGoModule {
          pname = "strike";
          inherit version;

          src = self;
          proxyVendor = true;
          vendorHash = "sha256-kEJmV3MQt8k5KuXBjYoUy7+ZZrD3aTljpBziaRndJcU=";

          subPackages = [ "cmd/strike" ];
          preBuild = ''
            go generate ./internal/frontend/tui/app
          '';
          # eval tests write under $HOME; the sandbox HOME (/homeless-shelter) is unwritable
          preCheck = ''
            export HOME="$(mktemp -d)"
          '';
          ldflags = [
            "-s"
            "-w"
            "-X github.com/jonathanung/strike-cli/internal/product/version.Version=${version}"
            "-X github.com/jonathanung/strike-cli/internal/product/version.Commit=${commit}"
          ];

          meta = {
            description = "Agentic coding TUI";
            homepage = "https://strike.jonathanung.ca/";
            license = pkgs.lib.licenses.asl20;
            mainProgram = "strike";
          };
        };
      in
      {
        packages = {
          inherit strike;
          default = strike;
        };

        apps = {
          strike = flake-utils.lib.mkApp { drv = strike; };
          default = flake-utils.lib.mkApp { drv = strike; };
        };

        devShells.default = pkgs.mkShell {
          name = "strike-cli";

          packages = [
            pkgs.go
            pkgs.gnumake
            pkgs.git
            pkgs.gopls
            # Rust web UI (webui/): everything needed to check/build/serve it.
            rustToolchain
            pkgs.trunk
            pkgs.wasm-bindgen-cli
            pkgs.binaryen # wasm-opt for trunk release builds
          ];

          shellHook = ''
            export GOROOT="${pkgs.go}/share/go"
            export GOPATH="$PWD/.nix-go/gopath"
            export GOCACHE="$PWD/.nix-go/gocache"
            export GOENV="$PWD/.nix-go/env"
            export GOTOOLCHAIN=local
            export PATH="$GOPATH/bin:$PATH"
            mkdir -p "$GOPATH" "$GOCACHE"
            export CARGO_HOME="$PWD/.nix-rust/cargo"
            export CARGO_TARGET_DIR="$PWD/.nix-rust/target"
            mkdir -p "$CARGO_HOME"
            echo "strike-cli dev shell: $(go version) · $(rustc --version)"
          '';
        };
      });
}

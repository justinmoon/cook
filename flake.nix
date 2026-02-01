{
  description = "Cook - AI-native software factory";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    playwright = {
      url = "github:justinmoon/playwright-web-flake/fix-aarch64-headless-shell";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, flake-utils, playwright }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs {
          inherit system;
          config.allowUnfree = true;
        };

        # Sprites CLI from Fly.io
        sprites-cli = pkgs.stdenv.mkDerivation rec {
          pname = "sprites";
          version = "dev-latest";
          src = pkgs.fetchurl {
            url = let
              arch = if pkgs.stdenv.hostPlatform.isAarch64 then "arm64" else "amd64";
              os = if pkgs.stdenv.isDarwin then "darwin" else "linux";
            in "https://sprites-binaries.t3.storage.dev/client/${version}/sprite-${os}-${arch}.tar.gz";
            sha256 = "sha256-wKP7AM/JsWANRvYXFWEDFF1SwIBOs6u+dBR56Qlhr4k=";
          };
          sourceRoot = ".";
          installPhase = ''
            mkdir -p $out/bin
            cp sprite $out/bin/sprites
            chmod +x $out/bin/sprites
          '';
        };
      in
      {
        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            # Go toolchain
            go
            gopls
            gotools
            go-tools
            air

            # NATS
            nats-server
            natscli

            # Database
            postgresql_17

            # Build tools
            gnumake

            # Git (for testing)
            git

            # E2E testing
            bun
            playwright.packages.${system}.playwright-test
            playwright.packages.${system}.playwright-driver

            # Sprites CLI
            sprites-cli
          ];

          shellHook = ''
            export GOPATH="$HOME/go"
            export PATH="$GOPATH/bin:$PATH"
            export PLAYWRIGHT_BROWSERS_PATH=${playwright.packages.${system}.playwright-driver.browsers}
            export PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS=true
            echo "cook dev shell ready"
          '';
        };

        # Package disabled - vendorHash needs computing for reproducible builds
        # To enable: nix build .# 2>&1 | grep 'got:' and set vendorHash
        # packages.default = pkgs.buildGoModule {
        #   pname = "cook";
        #   version = "0.1.0";
        #   src = ./.;
        #   vendorHash = "sha256-...";
        # };

        # Sandbox image for Modal/Docker (must be built on x86_64-linux)
        packages.sandbox-image = import ./nix/sandbox-image.nix {
          pkgs = import nixpkgs {
            system = "x86_64-linux";
            config.allowUnfree = true;
          };
        };

        # Sandbox tarball root for Sprites (must be built on x86_64-linux)
        packages.sandbox-tarball = import ./nix/sandbox-tarball.nix {
          pkgs = import nixpkgs {
            system = "x86_64-linux";
            config.allowUnfree = true;
          };
        };
      }
    );
}

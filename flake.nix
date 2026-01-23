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
        pkgs = nixpkgs.legacyPackages.${system};
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
            sqlite

            # Build tools
            gnumake

            # Git (for testing)
            git

            # E2E testing
            bun
            playwright.packages.${system}.playwright-test
            playwright.packages.${system}.playwright-driver
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
      }
    );
}

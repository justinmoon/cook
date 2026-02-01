# Build a tarball root with the sandbox environment for Sprites
# Usage: nix build .#sandbox-tarball
# Then: tar -czf sandbox.tar.gz -C result .
{ pkgs ? import <nixpkgs> { system = "x86_64-linux"; config.allowUnfree = true; } }:

let
  tailscaleTools = [
    (pkgs.writeTextFile {
      name = "cook-ts-preflight";
      text = builtins.readFile ../scripts/tailscale/fly-preflight.sh;
      executable = true;
      destination = "/bin/cook-ts-preflight";
    })
    (pkgs.writeTextFile {
      name = "cook-ts-up";
      text = builtins.readFile ../scripts/tailscale/fly-up.sh;
      executable = true;
      destination = "/bin/cook-ts-up";
    })
  ];

  env = pkgs.buildEnv {
    name = "cook-sandbox-env";
    paths = with pkgs; [
      # Core utils
      coreutils
      bashInteractive
      gnugrep
      gnused
      gawk
      findutils
      zsh

      # Dev tools
      git
      curl
      wget
      jq

      # Editors
      vim
      neovim

      # Search
      ripgrep
      fd

      # Process management
      procps

      # Networking
      iproute2
      iptables
      iputils
      libcap
      netcat-openbsd
      tailscale

      # Node.js
      nodejs_20

      # Claude Code CLI
      claude-code
      sudo

      # CA certificates for HTTPS
      cacert

      # Nix store requires these for dynamic linking
      stdenv.cc.cc.lib
    ] ++ tailscaleTools;
    pathsToLink = [ "/bin" "/lib" "/share" "/etc" ];
  };

  closure = pkgs.closureInfo {
    rootPaths = [ env ];
  };
in
pkgs.runCommand "cook-sandbox-tarball" {} ''
  set -euo pipefail

  mkdir -p "$out/nix/store"
  while read -r path; do
    cp -a "$path" "$out/nix/store/"
  done < ${closure}/store-paths

  mkdir -p "$out/opt"
  ln -s ${env} "$out/opt/sandbox"
''

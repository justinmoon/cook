# Build OCI image for Modal/Docker/Fly sandboxes
# Usage: nix build .#sandbox-image
# Push: docker load < result && docker push ghcr.io/justinmoon/cook-sandbox:latest
#
# Note: claude-code must be installed at runtime since npm install requires
# network access which isn't available during nix build.
{ pkgs ? import <nixpkgs> { system = "x86_64-linux"; config.allowUnfree = true; }
, src ? ./..
}:

let
  # Build cook-agent from source
  cook-agent = pkgs.buildGoModule {
    pname = "cook-agent";
    version = "0.1.0";
    inherit src;
    subPackages = [ "cmd/cook-agent" ];
    vendorHash = "sha256-nmhP8Hnn9sspG339GB2B9FPhRjBvU6DRiOzmzFe1uWE=";
    env.CGO_ENABLED = "0";
    proxyVendor = true;  # Don't use vendor dir
  };

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
in
pkgs.dockerTools.buildLayeredImage {
  name = "ghcr.io/justinmoon/cook-sandbox";
  tag = "latest";

  contents = with pkgs; [
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

    # Cook agent for terminal sessions
    cook-agent

    # CA certificates for HTTPS
    cacert

    # Nix store requires these for dynamic linking
    stdenv.cc.cc.lib
  ] ++ tailscaleTools;

  config = {
    Env = [
      "PATH=/bin:${cook-agent}/bin"
      "HOME=/root"
      "TERM=xterm-256color"
      "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
      "NODE_PATH=/root/.npm-global/lib/node_modules"
    ];
    WorkingDir = "/workspace";
    Cmd = [ "${pkgs.bashInteractive}/bin/bash" ];
  };
}

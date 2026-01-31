# Build OCI image for Modal/Docker sandboxes
# Usage: nix build .#sandbox-image
# Push: docker load < result && docker push ghcr.io/justinmoon/cook-sandbox:v3
{ pkgs ? import <nixpkgs> { system = "x86_64-linux"; }
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
    netcat-gnu
    
    # Node.js
    nodejs_20
    
    # Claude Code CLI
    claude-code
    
    # Cook agent for terminal sessions
    cook-agent
    
    # CA certificates for HTTPS
    cacert
    
    # Nix store requires these for dynamic linking
    stdenv.cc.cc.lib
  ];

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

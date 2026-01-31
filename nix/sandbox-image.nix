# Build OCI image for Modal/Docker sandboxes
# Usage: nix build .#sandbox-image
# Then: docker load < result && docker push ghcr.io/justinmoon/cook-sandbox:latest
#
# Note: claude-code must be installed at runtime since npm install requires
# network access which isn't available during nix build.
{ pkgs ? import <nixpkgs> { system = "x86_64-linux"; } }:

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
    
    # Node.js (for claude-code)
    nodejs_20
    
    # CA certificates for HTTPS
    cacert
    
    # Nix store requires these for dynamic linking
    stdenv.cc.cc.lib
  ];

  config = {
    Env = [
      "PATH=/bin"
      "HOME=/root"
      "TERM=xterm-256color"
      "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
      "NODE_PATH=/root/.npm-global/lib/node_modules"
    ];
    WorkingDir = "/workspace";
    Cmd = [ "${pkgs.bashInteractive}/bin/bash" ];
  };
}

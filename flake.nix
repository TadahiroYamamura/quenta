{
  description = "quenta の開発に使うツール";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      devShells = forAllSystems (pkgs: {
        # gotools は Dafny の Go への変換が goimports を呼ぶため
        default = pkgs.mkShell { packages = [ pkgs.dafny pkgs.quint pkgs.go pkgs.gotools pkgs.sqlite ]; };
      });
    };
}

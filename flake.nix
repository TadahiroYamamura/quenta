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
        # gotools は Dafny の Go への変換が goimports を呼ぶため。Python と MkDocs は、用語集を作って手元で見るため
        default = pkgs.mkShell {
          packages = [
            pkgs.dafny pkgs.quint pkgs.go pkgs.gotools pkgs.sqlite
            (pkgs.python3.withPackages (ps: [ ps.mkdocs ps.mkdocs-material ]))
          ];
        };
      });
    };
}

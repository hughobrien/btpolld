{
  # Only here so the forge's CI (a runner with nix and nothing else) can build
  # and test btpolld in a sandbox: `nix build .#checks.x86_64-linux.gate`.
  # btpolld itself is plain Go: `just test`, `just build`.
  description = "btpolld: CI gate";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forEachSystem = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      checks = forEachSystem (pkgs: {
        gate = pkgs.buildGoModule {
          pname = "btpolld-ci-gate";
          version = "0";
          src = ./.;
          # When go.mod or go.sum change, set this to pkgs.lib.fakeHash,
          # build, and paste the `got:` hash.
          vendorHash = "sha256-nDr9NDlxK0WdDhLnH/FCaeJgCLg+rBXn4WqozqQWJUM=";
          nativeBuildInputs = [ pkgs.clang ];
          doCheck = true;
          checkPhase = ''
            runHook preCheck
            export HOME="$TMPDIR"
            go vet ./...
            drift=$(gofmt -l . | grep -v '^vendor/' || true)
            [ -z "$drift" ] || { echo "gofmt drift: $drift"; exit 1; }
            go test ./...
            CGO_ENABLED=1 CC=clang go test -race ./...
            runHook postCheck
          '';
          # The gate is the tests; there is nothing to install.
          installPhase = ''
            mkdir -p "$out"
          '';
        };
      });
    };
}

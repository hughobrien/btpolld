{
  description = "btpolld: BLE sensor poller and CI gate";

  inputs = {
    ci.url = "git+https://forgejo.wg/hughobrien/ci.git?ref=main";
    nixpkgs.follows = "ci/nixpkgs";
  };

  outputs =
    {
      self,
      ci,
      nixpkgs,
    }:
    let
      allSystems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];
      linuxSystems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = nixpkgs.lib.genAttrs allSystems;
      forLinuxSystems = nixpkgs.lib.genAttrs linuxSystems;
    in
    {
      apps = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          mkApp =
            name: description: text:
            let
              drv = pkgs.writeShellApplication {
                inherit name text;
                runtimeInputs = [ pkgs.go ];
                derivationArgs.meta = {
                  inherit description;
                  mainProgram = name;
                };
              };
            in
            {
              type = "app";
              program = pkgs.lib.getExe drv;
              inherit (drv) meta;
            };
        in
        {
          build = mkApp "btpolld-build" "Build btpolld binary for the host platform" ''
            exec go build -o btpolld ./cmd/btpolld
          '';
          build-pi = mkApp "btpolld-build-pi" "Build static btpolld binary for Raspberry Pi Zero (armv6)" ''
            exec env CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=6 go build -o btpolld-arm ./cmd/btpolld
          '';
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          ciLib = ci.lib.forSystem system;
        in
        {
          default = pkgs.mkShell {
            packages = [
              pkgs.go
              pkgs.clang
            ]
            ++ ciLib.devShellPackages;
            env = ciLib.devShellEnv;
          };
        }
      );

      formatter = forAllSystems (system: nixpkgs.legacyPackages.${system}.nixfmt);

      checks = forLinuxSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          ciLib = ci.lib.forSystem system;
          lint = ciLib.lint { inherit self; };
          tests = pkgs.buildGoModule {
            pname = "btpolld-tests";
            version = "0";
            src = ./.;
            # When go.mod or go.sum change, set this to pkgs.lib.fakeHash,
            # push/build, and paste the `got:` hash from CI (`cycle errors <pr>`).
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
          gate = pkgs.symlinkJoin {
            name = "gate";
            paths = [
              lint
              tests
            ];
          };
        in
        {
          inherit
            lint
            tests
            gate
            ;
        }
      );
    };
}

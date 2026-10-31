{
  description = "SG200x-family BootROM UART Kermit loader (tested on the Pine64 Oz64)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in {
      devShells = forAll (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [ go gcc socat git python3 ];
        };
      });

      packages = forAll (pkgs:
        let version = "0.3.0";
        in {
          default = pkgs.buildGoModule {
            pname = "sg200x-uart-loader";
            inherit version;
            src = ./.;
            vendorHash = "sha256-Np+MQ+oy8nyCBIT1ivJyt0sRpxgGkwGs8M9Je4oLt1I=";
            ldflags = [ "-s" "-w" "-X" "main.version=${version}" ];
            meta = {
              description = "SG200x-family BootROM UART/E-Kermit FIP loader (tested on the Pine64 Oz64 / SG2000)";
              mainProgram = "sg200x-uart-loader";
              platforms = [ "x86_64-linux" "aarch64-linux" ];
              license = pkgs.lib.licenses.mit;
            };
          };
        });
    };
}

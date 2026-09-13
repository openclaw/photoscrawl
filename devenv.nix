{ pkgs, ... }:

{
  cachix.enable = false;

  packages = [
    pkgs.go_1_27
    pkgs.gnumake
    pkgs.sqlite
  ];

  scripts.verify.exec = ''
    make check
  '';
}

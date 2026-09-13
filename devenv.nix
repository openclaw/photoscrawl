{ pkgs, ... }:

{
  cachix.enable = false;

  packages = [
    pkgs.go
    pkgs.gnumake
    pkgs.sqlite
  ];

  scripts.verify.exec = ''
    make check
  '';
}

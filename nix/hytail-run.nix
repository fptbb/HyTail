# Temporary NixOS runtime shell for a prebuilt (non-Nix) HyTail Linux binary.
# Usage: nix-shell nix/hytail-run.nix --run '/path/to/hytail gui'
# This does not fix a missing ELF interpreter or an incompatible glibc.
{ pkgs ? import <nixpkgs> {} }:
let
  runtimeLibraries = with pkgs; [
    wayland
    libxkbcommon
    libGL
    libglvnd
    xorg.libX11
    xorg.libxcb
    xorg.libXcursor
    xorg.libXi
    xorg.libXrandr
    xorg.libXinerama
    xorg.libXext
  ];
in
pkgs.mkShell {
  shellHook = ''
    # winit uses dlopen for Wayland/X11. nix-ld alone may not add libraries to
    # the process's dlopen search path; LD_LIBRARY_PATH is needed at runtime.
    export LD_LIBRARY_PATH="${pkgs.lib.makeLibraryPath runtimeLibraries}''${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
  '';
}

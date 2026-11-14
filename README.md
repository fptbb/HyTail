# HyTail

HyTail creates private TCP/UDP bridges for games and other services using [Tailcat](https://github.com/tailscale/tailcat).

The desktop app is one executable for both GUI and CLI. An Android app is also available.

## Features

- Presets for Hytale, Minecraft Java, Minecraft Bedrock and Terraria.
- Generic TCP, UDP or combined forwarding.
- Multiple ports using lists (`25566,25567`) or ranges (`25566-25568`).
- Automatic invitation detection when joining.
- Automatic fallback when a requested local port is occupied.
- Connection diagnostics and automatic reconnection.
- Catppuccin Mocha Sapphire desktop interface.

## Use

Open `hytail` to launch the GUI. Use `hytail --help` for CLI commands.

Examples:

```sh
# Host a local service
hytail host generic --name Web --protocol tcp --target 127.0.0.1 --port 8080

# Host a Minecraft Java server
hytail host minecraft --edition java --port 25565

# Host Terraria on its default port
hytail host terraria

# Join using an invitation; HyTail detects the preset
hytail join --code-file invite.txt

# Print help or launch the GUI explicitly
hytail --help
hytail gui
```

Share the generated invitation only with people you trust. It grants access to the configured service ports until the invitation expires or the host stops.

## Important limits

- HyTail is not a VPN or exit node. It forwards only the ports and protocols selected by the host.
- Both devices need internet access for Tailcat connectivity and relay fallback.
- A reconnect can interrupt existing TCP sessions. The game or application may need to reconnect.
- Large UDP packets may exceed Tailcat's 1232-byte payload limit.
- Minecraft Bedrock may use additional dynamic gameplay ports; the preset does not discover or forward those automatically.
- Android runs the bridge as a foreground service. Android may still stop it under system power or resource policies.

Advanced users can also join with a domain configured with HyTail's SRV/TXT DNS invitation records. Do not publish an invitation in public DNS: it is a connection secret.

## Build

Builds run in one managed Ubuntu Distrobox. On Linux x86_64, it can build desktop Linux/Windows x64 and ARM64 targets, plus the Android ARM64 APK.

```sh
just setup             # Prepare the build environment
just build             # Build all targets
just build linux-x64   # Build one target
just build android     # Build the Android release APK
just test              # Run Go and Rust tests
just run               # Run the desktop development build
just clean             # Remove generated build output
```

Artifacts are written to `dist/`. See [Android build notes](android/README.md) for signing and installation details.

## License

HyTail is MIT licensed; see [LICENSE](LICENSE). Third-party attribution is summarized in [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).

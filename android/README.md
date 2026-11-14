# Android

HyTail for Android runs the bridge in a foreground service. It supports the same invitation formats as the desktop app and currently builds for ARM64.

## Build and install

Run these commands from the repository root:

```sh
just setup
just build android
just install-android
```

The APK is written to `dist/HyTail-android-arm64-release.apk`.

## Signing

The first local release build creates a signing key outside the repository:

```text
${XDG_DATA_HOME:-~/.local/share}/hytail/android-signing/
```

Back up both `release.jks` and `signing.env`. Every update must use the same key; losing it prevents updating an installed copy. Never commit signing files or passwords.

An existing keystore can be supplied with `HYTAIL_ANDROID_KEYSTORE`, `HYTAIL_ANDROID_STORE_PASSWORD`, `HYTAIL_ANDROID_KEY_PASSWORD` and `HYTAIL_ANDROID_KEY_ALIAS`.

## Runtime notes

- The bridge starts only when you tap Start and stays visible through an Android foreground-service notification.
- No VPN permission is required. HyTail forwards only the configured TCP/UDP ports.
- Join listens on `127.0.0.1` by default. LAN access is optional and should be enabled only on a trusted network.
- Android may stop background work under power or resource pressure. Stop the service to release its ports.
- Existing TCP connections are not preserved when the Tailcat transport reconnects.
- The app does not keep another game or server process alive.

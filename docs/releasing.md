# Releases

## Desktop app releases

The separate [`app-release.yml`](../.github/workflows/app-release.yml) mirrors the
agent release convention with an **`app-` prefix**:

- `app-v1.2.3` publishes a normal desktop release.
- `app-v1.2.3-anything` publishes a desktop prerelease (for example `-rc01`,
  `-beta.2`, or `-nightly-2026.09.27`).
- The app workflow listens only for `app-v*`; the existing agent workflow listens
  only for `v*`. **App tags do not trigger the agent release workflow**, and agent
  tags do not trigger desktop builds. No `install.sh` version change is required.

```bash
git tag app-v1.2.3-rc01
git push origin app-v1.2.3-rc01  # prerelease
# When ready:
git tag app-v1.2.3
git push origin app-v1.2.3       # normal release
```

Commit all app/release changes before tagging. The workflow validates the tag,
runs Go tests/vet and Flutter analysis/tests on each native runner, builds the
packages, then publishes the GitHub release only if all four builds succeed:

| Assets (example version `1.2.3-rc01`) | Runner |
|---|---|
| `Forge-1.2.3-rc01-macos-amd64.dmg` | `macos-15-intel` |
| `Forge-1.2.3-rc01-macos-arm64.dmg` | `macos-15` (Apple Silicon) |
| `Forge-1.2.3-rc01-linux-amd64.tar.gz`, `.deb`, `.rpm` | `ubuntu-24.04` |
| `Forge-1.2.3-rc01-linux-arm64.tar.gz`, `.deb`, `.rpm` | `ubuntu-24.04-arm` |
| `SHA256SUMS` | Checksums for all eight packages |

Every package includes the matching local Go agent. Flutter is pinned to
3.47.5's commit; Go comes from `go.mod`; nFPM is pinned to v2.45.0. Build artifacts
are also retained by Actions. Nothing is published when a build/test fails.
Protect `app-v*` tags with a repository ruleset, since tag builds can use signing
secrets and publish releases.

### Linux packages

The tarball is a relocatable bundle with a user-local installer. The DEB and RPM
install under `/usr/lib/forge`, link `/usr/bin/forge`, and install the desktop
entry, icon and license under `/usr/share`. Neither package installs a service,
runs an agent as root, or changes user configuration. Package name:
`forge-desktop`. RPM metadata maps amd64 to `x86_64` and arm64 to `aarch64`.

Prerelease package versions use `1.2.3~rc01-1`, which sorts before the stable
`1.2.3-1`. Hyphens within a suffix become dots in package metadata because RPM
Version cannot contain hyphens; artifact names preserve the original suffix.
The tag is used for artifact names; Flutter receives numeric `1.2.3` with the
Actions run number as its build number. No source version file is rewritten.

The builds target **Ubuntu 24.04+** and recent RPM distributions providing
**glibc 2.39+ and libstdc++ 14+** plus GTK 3 and the declared dependencies.
An RPM built from this bundle is not a claim of compatibility with older
Fedora/RHEL releases. CI checks package metadata, full asset/helper payloads and
library resolution; Wayland/X11 desktop smoke tests remain separate.

To reproduce on a matching Linux host:

```bash
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.45.0
APP_RELEASE_TAG=app-v1.2.3-rc01 APP_BUILD_NUMBER=1 scripts/build-linux-app.sh
scripts/package-linux-app.sh app-v1.2.3-rc01 amd64 dist/linux  # arm64 on ARM
```

### macOS DMGs and signing

Each DMG contains `Forge.app` and an Applications shortcut. The CI-specific
[`build-macos-dmg.sh`](../scripts/build-macos-dmg.sh) leaves the maintainer's
APNs-enabled `build-macos-app.sh` unchanged. CI apps are unsandboxed and omit the
restricted APNs entitlement, so they require no provisioning profile; **remote
APNs push is not enabled in these CI builds**. Local agents and network
connections still work.

The workflow reuses the signing/notary secrets documented below:

- With `MACOS_CERTIFICATE_P12` and `MACOS_CERTIFICATE_PASSWORD`, it signs with
  Developer ID. A configured but invalid certificate fails the build.
- With `NOTARY_KEY_P8`, `NOTARY_KEY_ID` and `NOTARY_ISSUER_ID` too, it notarizes
  and staples both the app and DMG; rejection fails the build.
- Without a certificate, it produces ad-hoc-signed DMGs with a warning.
  **Downloaded apps will not pass normal Gatekeeper checks without Developer ID
  and notarization.** Configure these secrets before public distribution.

Certificates/private keys live in a temporary keychain/files and are removed by
an `always()` cleanup step. No signing credentials run on pull requests.

Local macOS reproduction (choose `amd64` or `arm64`):

```bash
SIGN_IDENTITY='Developer ID Application: Your Name (TEAMID)' \
NOTARY_PROFILE=forge-notary \
scripts/build-macos-dmg.sh app-v1.2.3-rc01 arm64
# dist/app-release/Forge-1.2.3-rc01-macos-arm64.dmg
```

### Packaging regression checks

```bash
scripts/test-app-packaging.sh
```

This checks exact tag validation and shell syntax. With nFPM on PATH it also
builds DEB/RPM fixtures for both architectures and verifies recursive asset,
helper and launcher layout. The workflow additionally inspects real DEB/RPM
metadata and resolves the shipped libraries on Linux.

## Releasing the agent

A tag publishes a release. [`release.yml`](../.github/workflows/release.yml) tests the code on Linux and macOS, builds `pi-go-agent` on each, and publishes the archives on GitHub.

```bash
sed -i.bak 's/^RELEASE=.*/RELEASE="v1.2.3"/' install.sh && rm install.sh.bak
git commit -m "install v1.2.3" install.sh
git tag v1.2.3
git push origin main v1.2.3
```

[`install.sh`](../install.sh) names the release it installs. The workflow refuses a tag that the script does not name, so that the script on `main` never asks for a release that does not exist for long.

| Archive | Built on | Holds |
|---|---|---|
| `pi-go-agent-<version>-linux-amd64.tar.gz` | Linux | A static executable |
| `pi-go-agent-<version>-linux-arm64.tar.gz` | Linux | A static executable |
| `pi-go-agent-<version>-macos-universal.tar.gz` | macOS | A signed, notarized executable for Apple silicon and Intel |
| `SHA256SUMS` | | The checksum of each archive |

A tag with a hyphen, such as `v1.2.3-rc1`, publishes a pre-release. A run started by hand from the **Actions** page builds the archives and keeps them as artifacts of the run, without publishing.

[`scripts/build-release.sh`](../scripts/build-release.sh) does the building, and works the same on your machine:

```bash
scripts/build-release.sh v1.2.3        # writes dist/release/
```

## Signing the macOS build

macOS marks a file that a browser downloads as quarantined. Gatekeeper then refuses to run it unless it is both signed with a **Developer ID Application** certificate and notarized by Apple. A certificate of another kind, such as Apple Development or Apple Distribution, does not pass.

The workflow signs and notarizes when these secrets of the repository exist. Without them it signs ad hoc and warns.

| Secret | Value |
|---|---|
| `MACOS_CERTIFICATE_P12` | The certificate and its private key, as base64 |
| `MACOS_CERTIFICATE_PASSWORD` | The password of that file |
| `NOTARY_KEY_P8` | An App Store Connect API key, as base64 |
| `NOTARY_KEY_ID` | The ID of the key |
| `NOTARY_ISSUER_ID` | The issuer ID of the team |

**The certificate.** In Keychain Access, under **My Certificates**, select `Developer ID Application: …` together with its private key, choose **Export**, and save a `.p12` file with a password. Then:

```bash
base64 -i certificate.p12 | gh secret set MACOS_CERTIFICATE_P12
gh secret set MACOS_CERTIFICATE_PASSWORD
rm certificate.p12
```

**The notary key.** In App Store Connect, under **Users and Access**, **Integrations**, create a team key with the **Developer** role. The file can be downloaded once.

```bash
base64 -i AuthKey_XXXXXXXXXX.p8 | gh secret set NOTARY_KEY_P8
gh secret set NOTARY_KEY_ID
gh secret set NOTARY_ISSUER_ID
```

The workflow runs for tags and manual runs only, never for pull requests, so a fork cannot reach the secrets. Restrict who may push a `v*` tag with a tag ruleset.

### On your machine

```bash
xcrun notarytool store-credentials forge-notary \
  --key AuthKey_XXXXXXXXXX.p8 --key-id XXXXXXXXXX --issuer <issuer ID>

SIGN_IDENTITY='Developer ID Application: Your Name (TEAMID)' \
NOTARY_PROFILE=forge-notary \
scripts/build-release.sh v1.2.3
```

### What notarization does not do

A bare executable cannot carry a stapled ticket, unlike an app, a disk image or an installer package. Gatekeeper fetches the ticket from Apple the first time the executable runs, so that first run needs a network connection.

## Checking a download

```bash
shasum -a 256 -c SHA256SUMS --ignore-missing
gh attestation verify pi-go-agent-v1.2.3-linux-amd64.tar.gz --repo peterw22/forge
```

The second command proves that the archive was built by this workflow from the tagged commit. Attestations are written while the repository is public.

On macOS:

```bash
codesign --verify --strict --verbose=2 pi-go-agent
spctl --assess --type install --verbose=2 pi-go-agent   # "source=Notarized Developer ID"
```

# Releasing the agent

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

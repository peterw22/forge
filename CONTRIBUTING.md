# Contributing to Forge

Contributions are welcome. This page says how to send one and how it is licensed.

A security problem is not a contribution to discuss in public. Report it as [SECURITY.md](SECURITY.md) describes.

## How your contribution is licensed

Forge is licensed under the [GNU General Public License, version 3](LICENSE). Your contribution is licensed to everyone under the same terms, and you keep the copyright to it.

There is one more grant, and the reason for it follows.

### Why a second grant is needed

The maintainer publishes Forge in Apple's App Store and in Google Play, free of charge, so that people can install it without building it. The terms of an app store put conditions on the person who installs an app. The GPL does not allow anyone to add conditions to it. The holder of a copyright may publish their own work in a store all the same, but may not publish yours without your permission.

### The grant

By contributing, you give Tingou Wu, the maintainer of Forge, a permanent, worldwide, royalty-free and non-exclusive permission to distribute your contribution, as part of Forge, through app stores and under the terms those stores require.

This grant:

- covers distribution through app stores and nothing else;
- does not allow your contribution to be licensed as closed source;
- leaves your contribution under the GPL for everyone, including the maintainer, in every other respect.

### What the maintainer promises in return

- The builds of Forge in the stores are free of charge.
- The source of each build in a store is in this repository, under the GPL, at the tag of its version.

### Signing off

Add a `Signed-off-by` line to each commit:

```bash
git commit -s
```

With that line you state that you agree to the grant above and to the [Developer Certificate of Origin](https://developercertificate.org): you wrote the change, or you have the right to contribute it under these terms.

A pull request with a commit that is not signed off cannot be merged.

## Code from other projects

Code that you did not write needs a license that lets the maintainer keep the grant above.

| License | Accepted |
|---|---|
| MIT, BSD, Apache 2.0, ISC | Yes |
| MPL 2.0 | As a dependency, unchanged |
| GPL, LGPL, AGPL, of any version | No |

Code under the GPL from another author cannot be published in the App Store, since its author has given no such grant. The same holds for a new dependency. Name the license of each dependency you add in the pull request.

## Before you start

For anything larger than a small fix, open an issue first and describe what you want to change. That saves you from writing something that cannot be merged.

Changes to the handshake, the encrypted session, the push relay or the safety gate need a description of what they do to the [threat model](docs/security/threat-model.md).

## Checks

Run these before you open a pull request.

The agent:

```bash
go vet ./...
go test ./...
```

The client:

```bash
cd flutter/pi_go_app
dart format lib test
flutter analyze
flutter test
```

A change in behaviour comes with a test that fails without the change.

## Commits

- One purpose for each commit.
- The first line says what the commit does, in lower case and without a full stop: `let a client choose the directory a session works in`.
- The body says why, when the first line does not.

## Pull requests

- Say what changes and how you tested it.
- For a change to the interface, add a screenshot.
- Keep unrelated changes, such as reformatting, out of the pull request.

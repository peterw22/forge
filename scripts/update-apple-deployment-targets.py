#!/usr/bin/env python3
"""Raise Forge's Apple deployment minimums without rewriting other Xcode settings."""
from pathlib import Path

root = Path(__file__).resolve().parent.parent / "flutter" / "pi_go_app"
changes = (
    (root / "ios/Runner.xcodeproj/project.pbxproj", "IPHONEOS_DEPLOYMENT_TARGET = 13.0;", "IPHONEOS_DEPLOYMENT_TARGET = 15.0;", 6),
    (root / "macos/Runner.xcodeproj/project.pbxproj", "MACOSX_DEPLOYMENT_TARGET = 10.15;", "MACOSX_DEPLOYMENT_TARGET = 12.0;", 3),
    (root / "ios/Podfile", "platform :ios, '13.0'", "platform :ios, '15.0'", 1),
    (root / "macos/Podfile", "platform :osx, '10.15'", "platform :osx, '12.0'", 1),
)

updates = []
for path, old, new, expected in changes:
    text = path.read_text()
    count = text.count(old)
    if count == expected:
        updates.append((path, text.replace(old, new)))
    elif count == 0 and text.count(new) == expected:
        print(f"Already updated: {path.relative_to(root)}")
    else:
        raise SystemExit(f"Unexpected deployment targets in {path}: {count} old, {text.count(new)} new; no files changed")

for path, text in updates:
    path.write_text(text)
    print(f"Updated: {path.relative_to(root)}")

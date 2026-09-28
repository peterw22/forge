#!/usr/bin/env bash
# Register an extracted bundle in place. Keep the whole directory after install.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ "$ROOT" == *$'\n'* || "$ROOT" == *$'\r'* ]]; then
  echo "Desktop launcher paths cannot contain newlines." >&2
  exit 1
fi
DATA="${XDG_DATA_HOME:-$HOME/.local/share}"
BIN="$HOME/.local/bin"
mkdir -p "$BIN" "$DATA/applications" "$DATA/icons/hicolor/512x512/apps"
if [[ -e "$BIN/forge" && ! -L "$BIN/forge" ]]; then
  echo "Refusing to replace $BIN/forge; it is not a symlink." >&2
  exit 1
fi
ln -sfn "$ROOT/forge" "$BIN/forge"
# Desktop Exec is not shell syntax: quote and escape reserved characters,
# including literal percent signs used for desktop field codes.
exec_path="${ROOT//\\/\\\\}"
exec_path="${exec_path//\"/\\\"}"
exec_path="${exec_path//\`/\\\`}"
exec_path="${exec_path//\$/\\\$}"
exec_path="${exec_path//%/%%}"
# Desktop-entry string unescaping precedes Exec argument parsing.
exec_path="${exec_path//\\/\\\\}"
while IFS= read -r line; do
  if [[ "$line" == Exec=* ]]; then
    printf 'Exec="%s/forge"\n' "$exec_path"
  else
    printf '%s\n' "$line"
  fi
done < "$ROOT/share/applications/com.tingouw.forge.desktop" > "$DATA/applications/com.tingouw.forge.desktop"
cp "$ROOT/share/icons/hicolor/512x512/apps/com.tingouw.forge.png" "$DATA/icons/hicolor/512x512/apps/"
command -v update-desktop-database >/dev/null && update-desktop-database "$DATA/applications" || true
echo "Registered Forge from $ROOT. Keep this directory in place."
echo "Terminal launcher: $BIN/forge"

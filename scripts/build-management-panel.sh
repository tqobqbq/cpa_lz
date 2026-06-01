#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PANEL_DIR="$ROOT_DIR/web/management-center"
OUTPUT_FILE="$ROOT_DIR/internal/managementasset/embed/management.html"

npm --prefix "$PANEL_DIR" ci
npm --prefix "$PANEL_DIR" run build

mkdir -p "$(dirname "$OUTPUT_FILE")"
cp "$PANEL_DIR/dist/index.html" "$OUTPUT_FILE"

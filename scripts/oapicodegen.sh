#!/bin/bash

set -euo pipefail

CONFIG_PATH="api/server/config.yaml"
SPECS_PATH="api/server/specs"

OUTPUT_DIR="internal/api/server/oapicodegen"
mkdir -p "$OUTPUT_DIR"

# drop last run's output so a deleted or renamed spec leaves nothing stale behind
find "$OUTPUT_DIR" -name '*_gen.go' -delete

for file in "$SPECS_PATH"/*.yaml; do
  [ -e "$file" ] || continue

  name=$(basename "$file" .yaml)

  # a subdirectory per specification, package named after it, to avoid clashes
  mkdir -p "$OUTPUT_DIR/$name"

  echo "→ generating $name"

  go tool oapi-codegen \
    -config "$CONFIG_PATH" \
    -package "$name" \
    -o "$OUTPUT_DIR/$name/${name}_gen.go" \
    "$file"
done

# sweep up package directories the deletion above emptied
find "$OUTPUT_DIR" -type d -empty -delete
#!/bin/bash
#
# Apply tất cả Cursor patches.
# Usage:
#   ./apply-all-patches.sh            # Apply all
#   ./apply-all-patches.sh --restore  # Restore từ backup
#   ./apply-all-patches.sh --status   # Chỉ check status

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

PATCHES=(
  "patch-cursor-thinking.js"
  "patch-cursor-smooth-streaming.js"
  "patch-cursor-summarize-credentials.js"
  "patch-cursor-subagent-maxmode.js"
)

WORKBENCH="/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js"

# --- Status mode ---
if [[ "$1" == "--status" ]]; then
  echo "=== Cursor Patch Status ==="
  echo ""

  if [[ ! -f "$WORKBENCH" ]]; then
    echo "❌ Workbench not found: $WORKBENCH"
    exit 1
  fi

  # Get Cursor version
  PRODUCT="/Applications/Cursor.app/Contents/Resources/app/product.json"
  if [[ -f "$PRODUCT" ]]; then
    VERSION=$(node -e "console.log(JSON.parse(require('fs').readFileSync('$PRODUCT','utf8')).version||'unknown')")
    echo "Cursor version: $VERSION"
    echo ""
  fi

  node "$SCRIPT_DIR/check-patch-status.js"
  exit 0
fi

# --- Restore mode ---
if [[ "$1" == "--restore" ]]; then
  echo "🔄 Restoring from backup..."
  node "$SCRIPT_DIR/patch-cursor-thinking.js" --restore
  echo ""
  echo "✅ All patches restored. Restart Cursor (Cmd+Q → reopen)."
  exit 0
fi

# --- Apply mode ---
echo "🚀 Applying all Cursor patches..."
echo ""

FAILED=0
for patch in "${PATCHES[@]}"; do
  PATCH_PATH="$SCRIPT_DIR/$patch"
  if [[ ! -f "$PATCH_PATH" ]]; then
    echo "⚠️  Skipping $patch (file not found)"
    continue
  fi

  echo "━━━ $patch ━━━"
  if ! node "$PATCH_PATH"; then
    echo "❌ Failed: $patch"
    FAILED=1
  fi
  echo ""
done

if [[ $FAILED -eq 1 ]]; then
  echo "⚠️  Some patches failed. Check output above."
  exit 1
fi

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "✅ All patches applied!"
echo ""
echo "📋 Next steps:"
echo "   1. Quit Cursor completely (Cmd+Q)"
echo "   2. Reopen Cursor"
echo ""
echo "🔍 Check status anytime:"
echo "   ./apply-all-patches.sh --status"

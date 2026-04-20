#!/usr/bin/env node

/**
 * Check trạng thái các Cursor patches.
 * Được gọi từ apply-all-patches.sh --status
 */

const fs = require("fs");

const WORKBENCH_PATH =
  "/Applications/Cursor.app/Contents/Resources/app/out/vs/workbench/workbench.desktop.main.js";

const data = fs.readFileSync(WORKBENCH_PATH, "utf8");

const patches = [
  { name: "Thinking (handleTextDelta)", marker: "__thinkTagState" },
  {
    name: "Smooth streaming",
    marker: "__textWordStreamer",
  },
  {
    name: "Summarize credentials",
    marker:
      "_creds_s=this.reactiveStorageService.applicationUserPersistentStorage",
  },
  {
    name: "BugBot credentials (Agent Review)",
    marker: "_bb_ai=n.instantiationService.invokeFunction",
  },
  {
    name: "Subagent credentials",
    marker:
      '_d=this._aiService.getModelDetails({specificModelField:"composer"})',
  },
  {
    name: "OpenAI Key persistence",
    marker: "__openaiKeyPersistencePatched:login",
  },
];

patches.forEach((p) => {
  if (p.marker === null) {
    console.log(`✅ ${p.name} (${p.note})`);
    return;
  }

  // Patch có cả patch marker và native marker
  if (p.patchMarker) {
    const patched = data.includes(p.patchMarker);
    const nativeFix = data.includes(p.nativeMarker);
    if (patched) {
      console.log(`✅ ${p.name}`);
    } else if (nativeFix) {
      console.log(`✅ ${p.name} (Cursor fixed natively)`);
    } else {
      console.log(`❌ ${p.name}`);
    }
    return;
  }

  const found = data.includes(p.marker);
  const suffix = p.note ? ` (${p.note})` : "";
  console.log(`${found ? "✅" : "❌"} ${p.name}${suffix}`);
});

// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

// wiring_check.js — every element the window's script looks up must exist.
// A typo in an id would otherwise only show up as a dead button in the window.
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const html = fs.readFileSync(path.join(__dirname, "dist", "index.html"), "utf8");
const scripts = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map((m) => m[1]);
if (scripts.length !== 1) {
  console.error("FAIL expected exactly one inline script, found " + scripts.length);
  process.exit(1);
}
const script = scripts[0];

// 1) The script must at least parse.
try {
  new vm.Script(script, { filename: "index.html <script>" });
} catch (e) {
  console.error("FAIL inline script does not parse: " + e.message);
  process.exit(1);
}

// 2) Every $("id") must be declared somewhere: in the markup, or in a string
//    the script itself inserts (the question dialog builds its buttons).
const used = new Set([...script.matchAll(/\$\("([\w-]+)"\)/g)].map((m) => m[1]));
const declared = new Set([...html.matchAll(/id=\\?"([\w-]+)\\?"/g)].map((m) => m[1]));
const missing = [...used].filter((id) => !declared.has(id));
if (missing.length) {
  console.error("FAIL ids used but never declared: " + missing.join(", "));
  process.exit(1);
}

// 3) Every App method the window calls must exist in app.go.
const goSource = fs.readFileSync(path.join(__dirname, "..", "app.go"), "utf8");
const bound = new Set([...goSource.matchAll(/^func \(a \*App\) ([A-Z]\w*)\(/gm)].map((m) => m[1]));
const called = new Set([...script.matchAll(/App\(\)\.([A-Z]\w*)\(/g)].map((m) => m[1]));
const unbound = [...called].filter((m) => !bound.has(m));
if (unbound.length) {
  console.error("FAIL App methods called but not bound in app.go: " + unbound.join(", "));
  process.exit(1);
}

console.log("wiring_check: " + used.size + " element ids and " + called.size + " App methods checked");

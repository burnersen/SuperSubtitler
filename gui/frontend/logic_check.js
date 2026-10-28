// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

// logic_check.js — checks the window's logic (dist/logic.js) without a browser.
// Run: node frontend/logic_check.js  — exits non-zero on the first failure.
const assert = require("assert");
const L = require("./dist/logic.js");

let passed = 0;
function check(name, fn) {
  try {
    fn();
    passed++;
  } catch (e) {
    console.error("FAIL " + name + "\n  " + e.message);
    process.exit(1);
  }
}

check("ansi colours become spans, text is escaped", () => {
  const html = L.ansiToHtml("\x1b[30;46m INFO \x1b[0m <b>&\x1b[96mok\x1b[0m");
  assert.ok(html.includes('<span class="fg-k bg-c"> INFO </span>'), html);
  assert.ok(html.includes("&lt;b&gt;&amp;"), html);
  assert.ok(html.includes('<span class="fg-C">ok</span>'), html);
  assert.strictEqual((html.match(/<span/g) || []).length, (html.match(/<\/span>/g) || []).length);
});

check("durations and sizes", () => {
  assert.strictEqual(L.formatDuration(65), "01:05");
  assert.strictEqual(L.formatDuration(3725), "1:02:05");
  assert.strictEqual(L.formatMB(512), "512 MB");
  assert.strictEqual(L.formatMB(2048), "2.00 GB");
});

check("track answers are the exact console line", () => {
  assert.strictEqual(L.tracksAnswer(new Set([1, 2, 3]), 3), "");
  assert.strictEqual(L.tracksAnswer(new Set([3, 1]), 3), "1,3");
  assert.strictEqual(L.tracksAnswer(new Set(), 3), null, "nothing ticked is not an answer");
  assert.strictEqual(L.tracksAnswer(new Set([9]), 3), null);
});

const q = (langs) => ({ kind: "tracks", options: langs.map((l, i) => ({ n: i + 1, label: l, lang: l })) });

check("'use for all files' answers by language, not by number", () => {
  assert.strictEqual(L.autoTracksAnswer(q(["en", "de", "fr"]), ["de"]), "2");
  assert.strictEqual(L.autoTracksAnswer(q(["de", "en"]), ["de", "en"]), "");
  assert.strictEqual(L.autoTracksAnswer(q(["en", "fr"]), ["de"]), null, "missing language must show the dialog");
  assert.strictEqual(L.autoTracksAnswer(q(["de", "fr"]), ["de", "en"]), null, "a partial match must show the dialog");
  assert.strictEqual(L.autoTracksAnswer(q(["de"]), []), null);
  const question = q(["de", "en", "de"]);
  assert.deepStrictEqual(L.rememberLangs(question, new Set([1, 3])), ["de"]);
});

function run() {
  const rows = [{ id: 1, path: "C:\\V\\A.mkv" }, { id: 2, path: "C:\\V\\B.mkv" }, { id: 3, path: "C:\\V\\C.mkv" }];
  return L.createRun("subtitle", rows);
}

check("events find rows by path, case-insensitively", () => {
  const m = run();
  L.applyEvent(m, { ev: "run", files: 2 });
  assert.strictEqual(L.applyEvent(m, { ev: "file", index: 1, path: "c:/v/a.MKV", tracks: [{ n: 1 }, { n: 2 }] }), 1);
  assert.strictEqual(m.rows[1].tracks, 2);
  assert.strictEqual(L.applyEvent(m, { ev: "skip", path: "C:\\V\\B.mkv", reason: "has_subs" }), 2);
  assert.strictEqual(m.rows[2].status, "skipped");
  assert.strictEqual(L.applyEvent(m, { ev: "file", index: 2, path: "D:\\elsewhere.mkv" }), null, "unknown path touches nothing");
});

check("progress over several tracks never runs backwards", () => {
  const m = run();
  L.applyEvent(m, { ev: "file", index: 1, path: "C:\\V\\A.mkv", tracks: [{}, {}] });
  L.applyEvent(m, { ev: "progress", index: 1, track: 1, tracks: 2, pct: 0.5, eta_sec: 30 });
  assert.strictEqual(m.rows[1].pct, 0.25);
  L.applyEvent(m, { ev: "progress", index: 1, track: 2, tracks: 2, pct: 0.5 });
  assert.strictEqual(m.rows[1].pct, 0.75);
  L.applyEvent(m, { ev: "progress", index: 1, track: 1, tracks: 2, pct: 0.9 });
  assert.strictEqual(m.rows[1].pct, 0.75, "a late line for track 1 must not move the bar back");
  L.applyEvent(m, { ev: "progress", index: 1, track: 2, tracks: 2, pct: 7 });
  assert.strictEqual(m.rows[1].pct, 1, "pct is clamped");
});

check("speaker marking: the bar shares transcription and speakers, the changes are shown", () => {
  const m = run();
  L.applyEvent(m, { ev: "run", files: 1, speakers: true });
  L.applyEvent(m, { ev: "file", index: 1, path: "C:\\V\\A.mkv", tracks: [{}] });
  L.applyEvent(m, { ev: "progress", index: 1, track: 1, tracks: 1, pct: 1 });
  assert.ok(Math.abs(m.rows[1].pct - 0.875) < 1e-9, "transcription fills its share only: " + m.rows[1].pct);
  L.applyEvent(m, { ev: "stage", index: 1, track: 1, stage: "speakers" });
  assert.strictEqual(L.stageText(m.rows[1].stage), "Finding speaker changes");
  L.applyEvent(m, { ev: "progress", index: 1, track: 1, tracks: 1, pct: 0.5, stage: "speakers" });
  assert.ok(Math.abs(m.rows[1].pct - 0.9375) < 1e-9, String(m.rows[1].pct));
  assert.strictEqual(m.rows[1].stage, "speakers");
  L.applyEvent(m, { ev: "speakers", index: 1, track: 1, changes: 37 });
  assert.deepStrictEqual(m.rows[1].speakers, ["37 speaker changes"]);
  L.applyEvent(m, { ev: "speakers", index: 1, track: 1, changes: 0, message: "boom" });
  assert.deepStrictEqual(m.rows[1].speakers, ["no speaker marks"]);
  assert.ok(m.rows[1].speakerNote.includes("boom"));
  const plain = run();
  L.applyEvent(plain, { ev: "run", files: 1 });
  L.applyEvent(plain, { ev: "file", index: 1, path: "C:\\V\\A.mkv" });
  L.applyEvent(plain, { ev: "progress", index: 1, pct: 1 });
  assert.strictEqual(plain.rows[1].pct, 1, "without speaker marking transcription is the whole bar");
});

check("translation gets its share of the bar after the speakers", () => {
  const m = run();
  L.applyEvent(m, { ev: "run", files: 1, speakers: true, to: ["de"] });
  L.applyEvent(m, { ev: "file", index: 1, path: "C:\\V\\A.mkv", tracks: [{}] });
  L.applyEvent(m, { ev: "progress", index: 1, track: 1, tracks: 1, pct: 1 });
  assert.ok(Math.abs(m.rows[1].pct - 0.7) < 1e-9, String(m.rows[1].pct));
  L.applyEvent(m, { ev: "progress", index: 1, track: 1, tracks: 1, pct: 1, stage: "speakers" });
  assert.ok(Math.abs(m.rows[1].pct - 0.8) < 1e-9, String(m.rows[1].pct));
  L.applyEvent(m, { ev: "progress", index: 1, track: 1, tracks: 1, pct: 0.5, stage: "translate" });
  assert.ok(Math.abs(m.rows[1].pct - 0.9) < 1e-9, String(m.rows[1].pct));
  assert.strictEqual(L.stageText(m.rows[1].stage), "Translating");
});

check("numbering restarts with every process (batches)", () => {
  const m = run();
  L.applyEvent(m, { ev: "run", files: 1 });
  L.applyEvent(m, { ev: "file", index: 1, path: "C:\\V\\A.mkv" });
  L.applyEvent(m, { ev: "result", index: 1, status: "success", outputs: ["C:\\V\\A.de.srt"] });
  L.applyEvent(m, { ev: "run", files: 1 });
  L.applyEvent(m, { ev: "file", index: 1, path: "C:\\V\\C.mkv" });
  L.applyEvent(m, { ev: "result", index: 1, status: "failed", message: "no speech" });
  assert.strictEqual(m.rows[1].status, "done");
  assert.strictEqual(m.rows[3].status, "failed");
  assert.strictEqual(m.rows[3].message, "no speech");
});

check("languages, stages and results", () => {
  const m = run();
  L.applyEvent(m, { ev: "file", index: 1, path: "C:\\V\\A.mkv", tracks: [{}, {}] });
  L.applyEvent(m, { ev: "lang", index: 1, track: 2, out: "en", task: "transcribe" });
  L.applyEvent(m, { ev: "lang", index: 1, track: 1, src: "de", out: "en", task: "translate" });
  assert.deepStrictEqual(m.rows[1].langs, ["de → en", "en"]);
  L.applyEvent(m, { ev: "stage", index: 1, stage: "mux" });
  assert.strictEqual(m.rows[1].status, "working");
  L.applyEvent(m, { ev: "result", index: 1, status: "success", outputs: ["a", "b"] });
  assert.strictEqual(m.rows[1].pct, 1);
  L.applyEvent(m, { ev: "stage", index: 1, stage: "detect" });
  assert.strictEqual(m.rows[1].status, "done", "a late stage must not reopen a finished row");
  assert.strictEqual(L.stageText("detect"), "Detecting the language");
});

check("overall percent and finishing", () => {
  const m = run();
  L.applyEvent(m, { ev: "file", index: 1, path: "C:\\V\\A.mkv" });
  L.applyEvent(m, { ev: "result", index: 1, status: "success" });
  L.applyEvent(m, { ev: "skip", path: "C:\\V\\B.mkv", reason: "has_subs" });
  L.applyEvent(m, { ev: "file", index: 2, path: "C:\\V\\C.mkv" });
  L.applyEvent(m, { ev: "progress", index: 2, pct: 0.5 });
  assert.strictEqual(L.overallPercent(m), 83);
  L.finishRun(m, true);
  assert.strictEqual(m.rows[3].status, "stopped");
  assert.deepStrictEqual(L.counts(m), { done: 1, failed: 0, skipped: 1, stopped: 1, open: 0 });
});

check("unknown or broken events are ignored", () => {
  const m = run();
  assert.strictEqual(L.applyEvent(m, null), null);
  assert.strictEqual(L.applyEvent(m, { ev: "brand-new-event", index: 1 }), null);
  assert.strictEqual(L.applyEvent(m, { ev: "progress", index: 99, pct: 0.5 }), null);
});

check("setup progress: known size gives a percentage, unknown size does not", () => {
  const m = run();
  L.applyEvent(m, { ev: "setup", step: "ffmpeg", state: "progress", done_mb: 50, total_mb: 200, mbps: 5 });
  const p = L.setupProgress(m.setup);
  assert.strictEqual(p.pct, 25);
  assert.strictEqual(p.title, "FFmpeg");
  assert.ok(p.detail.includes("50.0 MB / 200 MB") && p.detail.includes("5.0 MB/s"), p.detail);
  L.applyEvent(m, { ev: "setup", step: "model", state: "progress", done_mb: 800 });
  assert.strictEqual(L.setupProgress(m.setup).pct, null);
  L.applyEvent(m, { ev: "setup", step: "ready", state: "done" });
  assert.strictEqual(m.setup, null);
});

check("merge selection follows the CLI's rule", () => {
  const v = { kind: "video", path: "v.mkv" };
  const a = { kind: "audio", path: "a.m4a" };
  const s = { kind: "subtitle", path: "s.srt" };
  assert.ok(L.mergeSelection([a]).error);
  assert.ok(L.mergeSelection([v, v, a]).error);
  assert.ok(L.mergeSelection([v]).error);
  assert.deepStrictEqual(L.mergeSelection([s, v, a]), { video: "v.mkv", extras: ["s.srt", "a.m4a"], error: "" });
});

check("language codes", () => {
  assert.ok(L.validLanguage("de") && L.validLanguage("yue"));
  assert.ok(!L.validLanguage("DE") && !L.validLanguage("german") && !L.validLanguage(""));
  assert.strictEqual(L.languageName("ja"), "Japanese");
});

check("settings: allowed, recommended, mistakes", () => {
  const num = { kind: "int", min: 30, max: 1800, recMin: 120, recMax: 300 };
  assert.strictEqual(L.checkSetting(num, "180").level, "ok");
  assert.strictEqual(L.checkSetting(num, "60").level, "warn");
  assert.strictEqual(L.checkSetting(num, "5").level, "error");
  assert.strictEqual(L.checkSetting(num, "abc").level, "error");
  assert.strictEqual(L.checkSetting(num, "12.5").level, "error", "an int field must refuse decimals");
  const flt = { kind: "float", min: 0, max: 1, recMin: 0.35, recMax: 0.6 };
  assert.strictEqual(L.checkSetting(flt, "0.50").level, "ok");
  assert.strictEqual(L.checkSetting(flt, "0,5").level, "error", "a comma is not a decimal point here");
  assert.strictEqual(L.checkSetting(flt, "0.9").level, "warn");
  const ch = { kind: "choice", choices: [{ value: "cuda" }, { value: "cpu" }] };
  assert.strictEqual(L.checkSetting(ch, "cpu").level, "ok");
  assert.strictEqual(L.checkSetting(ch, "gpu").level, "error");
  assert.strictEqual(L.checkSetting(Object.assign({ open: true }, ch), "my-model").level, "warn");
  assert.strictEqual(L.checkSetting({ kind: "bool" }, "yes").level, "error");
  const expert = { kind: "text", expert: true };
  assert.strictEqual(L.checkSetting(expert, "faster-whisper", "faster-whisper").level, "ok");
  assert.strictEqual(L.checkSetting(expert, "faster-whisper==1.0.3", "faster-whisper").level, "warn");
  assert.strictEqual(L.checkSetting(null, "anything").level, "ok", "unknown keys are not judged");
});

check("settings: rules across two values", () => {
  assert.ok(L.crossCheck({ sub_min_dur_ms: "8000", sub_max_dur_ms: "7000" }).sub_min_dur_ms);
  assert.deepStrictEqual(L.crossCheck({ sub_min_dur_ms: "833", sub_max_dur_ms: "7000", model_translate: "large-v3" }), {});
  assert.ok(L.crossCheck({ model_translate: "large-v3-turbo" }).model_translate);
});

console.log("logic_check: " + passed + " checks passed");

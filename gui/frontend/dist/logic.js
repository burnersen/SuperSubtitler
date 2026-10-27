// SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
// Copyright (C) 2026 burnersen
// SPDX-License-Identifier: GPL-3.0-only

// logic.js — the window's logic that needs no page: turning CLI events into
// row states, answers into the exact line the CLI expects, colours into HTML.
//
// Loaded by index.html in the window and by the *_check.js files in Node, so
// every rule here is checked without a browser and without a single real run.
(function (root) {
  "use strict";

  // ------------------------------------------------------------------------
  // Text helpers
  // ------------------------------------------------------------------------

  function escapeHtml(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  // ANSI SGR colours to spans. Only colour and bold are kept; the log box has
  // a dark ground in both themes because the CLI's colours are meant for one.
  const ansiColours = {
    30: "k", 31: "r", 32: "g", 33: "y", 34: "b", 35: "m", 36: "c", 37: "w",
    90: "K", 91: "R", 92: "G", 93: "Y", 94: "B", 95: "M", 96: "C", 97: "W"
  };

  function ansiToHtml(text) {
    let out = "";
    let fg = null;
    let bg = null;
    let bold = false;
    let open = false;
    const parts = String(text).split(/(\x1b\[[0-9;]*m)/);
    for (const part of parts) {
      const m = /^\x1b\[([0-9;]*)m$/.exec(part);
      if (!m) {
        if (part === "") continue;
        out += escapeHtml(part);
        continue;
      }
      const codes = m[1] === "" ? [0] : m[1].split(";").map(Number);
      for (const c of codes) {
        if (c === 0) { fg = null; bg = null; bold = false; }
        else if (c === 1) bold = true;
        else if (c === 22) bold = false;
        else if (c === 39) fg = null;
        else if (c === 49) bg = null;
        else if (ansiColours[c]) fg = ansiColours[c];
        else if (ansiColours[c - 10]) bg = ansiColours[c - 10];
      }
      if (open) { out += "</span>"; open = false; }
      const cls = [];
      if (fg) cls.push("fg-" + fg);
      if (bg) cls.push("bg-" + bg);
      if (bold) cls.push("b");
      if (cls.length) { out += '<span class="' + cls.join(" ") + '">'; open = true; }
    }
    if (open) out += "</span>";
    return out;
  }

  function formatDuration(sec) {
    sec = Math.max(0, Math.round(Number(sec) || 0));
    const h = Math.floor(sec / 3600);
    const m = Math.floor((sec % 3600) / 60);
    const s = sec % 60;
    const two = (n) => (n < 10 ? "0" : "") + n;
    return h > 0 ? h + ":" + two(m) + ":" + two(s) : two(m) + ":" + two(s);
  }

  function formatMB(mb) {
    mb = Number(mb) || 0;
    if (mb >= 1024) return (mb / 1024).toFixed(2) + " GB";
    if (mb >= 100) return mb.toFixed(0) + " MB";
    return mb.toFixed(1) + " MB";
  }

  // Windows paths compare case-insensitively and with either slash.
  function pathKey(p) {
    return String(p || "").replace(/\//g, "\\").replace(/\\+$/, "").toLowerCase();
  }

  function baseName(p) {
    const s = String(p || "");
    const i = Math.max(s.lastIndexOf("\\"), s.lastIndexOf("/"));
    return i >= 0 ? s.slice(i + 1) : s;
  }

  function dirName(p) {
    const s = String(p || "");
    const i = Math.max(s.lastIndexOf("\\"), s.lastIndexOf("/"));
    return i >= 0 ? s.slice(0, i) : "";
  }

  // ------------------------------------------------------------------------
  // Languages offered in the drop-down (any other ISO code can be typed in)
  // ------------------------------------------------------------------------

  const LANGUAGES = [
    ["de", "German"], ["en", "English"], ["es", "Spanish"], ["fr", "French"],
    ["it", "Italian"], ["pt", "Portuguese"], ["nl", "Dutch"], ["pl", "Polish"],
    ["ru", "Russian"], ["uk", "Ukrainian"], ["tr", "Turkish"], ["sv", "Swedish"],
    ["da", "Danish"], ["no", "Norwegian"], ["fi", "Finnish"], ["cs", "Czech"],
    ["hu", "Hungarian"], ["ro", "Romanian"], ["el", "Greek"], ["ar", "Arabic"],
    ["he", "Hebrew"], ["hi", "Hindi"], ["ja", "Japanese"], ["ko", "Korean"],
    ["zh", "Chinese"], ["th", "Thai"], ["vi", "Vietnamese"], ["id", "Indonesian"]
  ];

  function languageName(code) {
    const hit = LANGUAGES.find((l) => l[0] === code);
    if (hit) return hit[1];
    if (code === "und") return "undetermined";
    return code || "";
  }

  function validLanguage(code) {
    return /^[a-z]{2,3}$/.test(String(code || ""));
  }

  // ------------------------------------------------------------------------
  // Answers — exactly the line a person would type at the console
  // ------------------------------------------------------------------------

  // tracksAnswer: "" when every track is ticked (the CLI's "all"), "1,3" for a
  // subset, null when nothing is ticked (not a valid answer: the dialog must
  // not send it, or the CLI would read "" and take all tracks).
  function tracksAnswer(selected, total) {
    const ns = Array.from(selected).map(Number).filter((n) => n >= 1 && n <= total);
    const unique = Array.from(new Set(ns)).sort((a, b) => a - b);
    if (unique.length === 0) return null;
    if (unique.length === total) return "";
    return unique.join(",");
  }

  // autoTracksAnswer applies a remembered "use for all files" choice. It is
  // remembered BY LANGUAGE, because track numbers differ from file to file.
  // Only when every remembered language is found in this file is the answer
  // given automatically; otherwise null — the dialog is shown, so nothing is
  // quietly taken that was not chosen.
  function autoTracksAnswer(question, rememberedLangs) {
    if (!rememberedLangs || rememberedLangs.length === 0) return null;
    const options = question.options || [];
    const picked = options.filter((o) => rememberedLangs.indexOf(o.lang || "und") >= 0);
    const found = new Set(picked.map((o) => o.lang || "und"));
    if (!rememberedLangs.every((l) => found.has(l))) return null;
    return tracksAnswer(picked.map((o) => o.n), options.length);
  }

  function rememberLangs(question, selected) {
    const set = new Set();
    (question.options || []).forEach((o) => {
      if (selected.has(o.n)) set.add(o.lang || "und");
    });
    return Array.from(set);
  }

  // ------------------------------------------------------------------------
  // Run model: CLI events → row states
  // ------------------------------------------------------------------------

  const STAGES = {
    probe: "Reading the file",
    detect: "Detecting the language",
    extract: "Extracting audio",
    transcribe: "Transcribing",
    mux: "Building the MKV"
  };

  function stageText(stage) {
    return STAGES[stage] || stage || "";
  }

  // createRun starts the bookkeeping for one window run. rows are the queue
  // rows ({id, path}); events find their row by path, because the CLI numbers
  // files per process and a long list may run as several processes.
  function createRun(kind, rows) {
    const byPath = {};
    rows.forEach((r) => { byPath[pathKey(r.path)] = r.id; });
    return {
      kind,
      byPath,
      indexToRow: {},
      rows: {},          // id → state
      order: rows.map((r) => r.id),
      setup: null,
      question: null,
      summary: null,
      errors: [],
      batchFiles: 0
    };
  }

  function rowState(model, id) {
    if (!model.rows[id]) {
      model.rows[id] = {
        status: "waiting", stage: "", pct: 0, eta: 0, track: 0, tracks: 1,
        langs: [], outputs: [], message: ""
      };
    }
    return model.rows[id];
  }

  // applyEvent updates the model and returns the id of the row it touched
  // (or null). Unknown events are ignored, so a newer CLI never breaks this.
  function applyEvent(model, ev) {
    if (!ev || typeof ev.ev !== "string") return null;
    const rowOf = (index) => model.indexToRow[index];
    switch (ev.ev) {
      case "run":
        model.indexToRow = {}; // numbering restarts with every process
        model.batchFiles = ev.files || 0;
        return null;
      case "setup":
        model.setup = ev.step === "ready" ? null : {
          step: ev.step, state: ev.state, msg: ev.msg || "",
          doneMB: ev.done_mb || 0, totalMB: ev.total_mb || 0, mbps: ev.mbps || 0
        };
        return null;
      case "skip": {
        const id = model.byPath[pathKey(ev.path)];
        if (id === undefined) return null;
        const r = rowState(model, id);
        r.status = "skipped";
        r.message = ev.reason === "has_subs"
          ? "already has subtitles — tick \"Redo finished files\" to do it again"
          : "this is a SuperSubtitler result";
        return id;
      }
      case "file": {
        const id = model.byPath[pathKey(ev.path)];
        if (id === undefined) return null;
        model.indexToRow[ev.index] = id;
        const r = rowState(model, id);
        r.tracks = Math.max(1, (ev.tracks || []).length);
        if (ev.error) {
          r.status = "failed";
          r.message = ev.error;
        } else {
          r.status = "analysing";
        }
        return id;
      }
      case "stage": {
        const id = rowOf(ev.index);
        if (id === undefined) return null;
        const r = rowState(model, id);
        if (r.status === "failed" || r.status === "done") return id;
        r.stage = ev.stage;
        if (ev.track) r.track = ev.track;
        if (ev.stage === "transcribe" || ev.stage === "mux" || ev.stage === "extract") r.status = "working";
        return id;
      }
      case "lang": {
        const id = rowOf(ev.index);
        if (id === undefined) return null;
        const r = rowState(model, id);
        r.langs[(ev.track || 1) - 1] = ev.task === "translate"
          ? (ev.src || "auto") + " → en"
          : ev.out;
        return id;
      }
      case "progress": {
        const id = rowOf(ev.index);
        if (id === undefined) return null;
        const r = rowState(model, id);
        const tracks = Math.max(1, ev.tracks || r.tracks);
        const track = Math.min(Math.max(1, ev.track || 1), tracks);
        const pct = Math.min(Math.max(Number(ev.pct) || 0, 0), 1);
        // Never let a bar run backwards: a late duplicate line must not undo.
        r.pct = Math.max(r.pct, (track - 1 + pct) / tracks);
        r.eta = ev.eta_sec || 0;
        r.status = "working";
        r.stage = "transcribe";
        return id;
      }
      case "question":
        model.question = ev;
        return null;
      case "result": {
        const id = rowOf(ev.index);
        if (id === undefined) return null;
        const r = rowState(model, id);
        r.status = ev.status === "success" ? "done" : "failed";
        r.outputs = ev.outputs || [];
        r.message = ev.message || "";
        r.stage = "";
        r.eta = 0;
        if (r.status === "done") r.pct = 1;
        return id;
      }
      case "error":
        model.errors.push(ev.message || "unknown error");
        return null;
      case "summary":
        model.summary = ev;
        return null;
    }
    return null;
  }

  // finishRun marks rows that never got a result. Called when the process
  // list has ended; cancelled tells whether the user stopped it.
  function finishRun(model, cancelled) {
    model.order.forEach((id) => {
      const r = rowState(model, id);
      if (r.status === "done" || r.status === "failed" || r.status === "skipped") return;
      r.status = cancelled ? "stopped" : "failed";
      r.message = cancelled ? "not done — stopped" : (r.message || "no result was reported");
      r.stage = "";
      r.eta = 0;
    });
  }

  // overallPercent: finished rows count fully, the running ones by their bar.
  // Skipped rows count as finished — there is nothing left to do for them.
  function overallPercent(model) {
    const ids = model.order;
    if (ids.length === 0) return 0;
    let sum = 0;
    ids.forEach((id) => {
      const r = model.rows[id];
      if (!r) return;
      if (r.status === "done" || r.status === "failed" || r.status === "skipped" || r.status === "stopped") sum += 1;
      else sum += r.pct || 0;
    });
    return Math.floor((sum / ids.length) * 100);
  }

  function counts(model) {
    const c = { done: 0, failed: 0, skipped: 0, stopped: 0, open: 0 };
    model.order.forEach((id) => {
      const s = (model.rows[id] || {}).status || "waiting";
      if (c[s] !== undefined) c[s]++;
      else c.open++;
    });
    return c;
  }

  const SETUP_STEPS = {
    ffmpeg: "FFmpeg",
    python: "Python",
    pip: "pip",
    engine: "Whisper engine",
    cuda: "CUDA libraries",
    model: "Speech model"
  };

  // setupText renders the setup panel line; pct is null when the size is
  // unknown (the bar then shows activity instead of inventing a number).
  function setupProgress(setup) {
    if (!setup) return null;
    let detail = "";
    if (setup.totalMB > 0) {
      detail = formatMB(setup.doneMB) + " / " + formatMB(setup.totalMB);
    } else if (setup.doneMB > 0) {
      detail = formatMB(setup.doneMB);
    }
    if (setup.mbps > 0) detail += (detail ? " · " : "") + setup.mbps.toFixed(1) + " MB/s";
    return {
      title: SETUP_STEPS[setup.step] || setup.step,
      msg: setup.msg,
      detail,
      pct: setup.totalMB > 0 ? Math.min(100, Math.floor((setup.doneMB / setup.totalMB) * 100)) : null
    };
  }

  // ------------------------------------------------------------------------
  // Merge page
  // ------------------------------------------------------------------------

  // mergeSelection checks the merge list: exactly one video plus at least one
  // audio or subtitle file — the same rule the CLI applies.
  function mergeSelection(items) {
    const videos = items.filter((i) => i.kind === "video");
    const extras = items.filter((i) => i.kind === "audio" || i.kind === "subtitle");
    if (videos.length === 0) return { error: "Add the video the tracks should go into." };
    if (videos.length > 1) return { error: "Only one video can be merged at a time — remove the others." };
    if (extras.length === 0) return { error: "Add at least one audio or subtitle file." };
    return { video: videos[0].path, extras: extras.map((e) => e.path), error: "" };
  }

  const api = {
    escapeHtml, ansiToHtml, formatDuration, formatMB, pathKey, baseName, dirName,
    LANGUAGES, languageName, validLanguage,
    tracksAnswer, autoTracksAnswer, rememberLangs,
    stageText, createRun, applyEvent, finishRun, overallPercent, counts, setupProgress,
    mergeSelection
  };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  else root.SSLogic = api;
})(typeof window !== "undefined" ? window : this);

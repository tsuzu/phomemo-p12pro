"use strict";

// ---- logging ----------------------------------------------------------
const logEl = document.getElementById("log");
function log(msg) {
  const time = new Date().toLocaleTimeString();
  logEl.textContent += `[${time}] ${msg}\n`;
  logEl.scrollTop = logEl.scrollHeight;
}

// ---- environment checks ----------------------------------------------
const warnEl = document.getElementById("env-warning");
if (!window.isSecureContext) {
  warnEl.hidden = false;
  warnEl.textContent = "このページは安全なコンテキスト (HTTPS / localhost) ではありません。Web Bluetooth は動作しません。";
} else if (!navigator.bluetooth) {
  warnEl.hidden = false;
  warnEl.textContent = "このブラウザは Web Bluetooth に対応していません。印刷には Chrome / Edge を使ってください (プレビュー生成は動作します)。";
}

// ---- wasm boot ---------------------------------------------------------
let wasmReady = false;
async function boot() {
  const go = new Go();
  const result = await WebAssembly.instantiateStreaming(fetch("p12pro.wasm"), go.importObject)
    .catch(() => fetch("p12pro.wasm")
      .then(r => r.arrayBuffer())
      .then(buf => WebAssembly.instantiate(buf, go.importObject)));
  go.run(result.instance);
  wasmReady = true;
  log(`wasm ready (p12pro ${window.p12proVersion || ""})`);
}
boot().catch(e => log("wasm load failed: " + e));

// ---- shared state ------------------------------------------------------
let currentJob = null;     // {initPackets: [Uint8Array], payload: Uint8Array}
let imageBytes = null;     // Uint8Array
let fontBytes = null;      // Uint8Array

function commonOpts() {
  return {
    width: parseInt(document.getElementById("opt-width").value, 10) || 96,
    extraFeedMM: parseFloat(document.getElementById("opt-feed").value) || 0,
  };
}

function showResult(res, what) {
  if (!res || res.error) {
    log(`${what}: エラー: ${res ? res.error : "no result"}`);
    return;
  }
  currentJob = { initPackets: [...res.initPackets], payload: res.payload };
  const total = res.payload.length + currentJob.initPackets.reduce((n, p) => n + p.length, 0);
  const url = URL.createObjectURL(new Blob([res.preview], { type: "image/png" }));
  const img = document.getElementById("preview");
  img.src = url;
  document.getElementById("result-info").textContent =
    `ビットマップ ${res.width} x ${res.height} dots / ジョブ総量 ${total} bytes (初期化 ${currentJob.initPackets.length} パケット + 本体 ${res.payload.length} bytes)`;
  document.getElementById("result-card").hidden = false;
  log(`${what}: ジョブ生成 (${total} bytes)`);
}

// ---- tabs ---------------------------------------------------------------
document.querySelectorAll(".tab").forEach(tab => {
  tab.addEventListener("click", () => {
    document.querySelectorAll(".tab").forEach(t => t.classList.remove("active"));
    tab.classList.add("active");
    const mode = tab.dataset.mode;
    document.querySelectorAll(".mode").forEach(m => { m.hidden = m.id !== `mode-${mode}`; });
  });
});

// ---- image mode ----------------------------------------------------------
document.getElementById("img-file").addEventListener("change", async (e) => {
  const f = e.target.files[0];
  if (!f) return;
  imageBytes = new Uint8Array(await f.arrayBuffer());
  log(`画像を読み込みました: ${f.name} (${imageBytes.length} bytes)`);
});

document.getElementById("img-generate").addEventListener("click", () => {
  if (!wasmReady) return log("wasm がまだ準備できていません");
  if (!imageBytes) return log("画像ファイルを選んでください");
  const res = window.p12proRenderImage(imageBytes, {
    ...commonOpts(),
    dither: document.getElementById("img-dither").value,
    rotate: parseInt(document.getElementById("img-rotate").value, 10),
    invert: document.getElementById("img-invert").checked,
  });
  showResult(res, "画像");
});

// ---- text mode -----------------------------------------------------------
document.getElementById("font-file").addEventListener("change", async (e) => {
  const f = e.target.files[0];
  if (!f) return;
  fontBytes = new Uint8Array(await f.arrayBuffer());
  document.getElementById("font-status").textContent = `フォント読込済み: ${f.name} (${fontBytes.length} bytes)`;
  log(`フォントを読み込みました: ${f.name}`);
});

document.getElementById("font-fetch").addEventListener("click", async () => {
  const url = document.getElementById("font-url").value.trim();
  if (!url) return;
  const status = document.getElementById("font-status");
  status.textContent = "フォント取得中...";
  try {
    const r = await fetch(url);
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    fontBytes = new Uint8Array(await r.arrayBuffer());
    status.textContent = `フォント取得済み (${fontBytes.length} bytes): ${url}`;
    log("フォントをURLから取得しました");
  } catch (err) {
    status.textContent = "フォント取得に失敗: " + err;
  }
});

document.getElementById("text-generate").addEventListener("click", () => {
  if (!wasmReady) return log("wasm がまだ準備できていません");
  if (!fontBytes) return log("フォントが未読込です (ファイル選択かURL取得)");
  const lines = [
    document.getElementById("text-line1").value,
    document.getElementById("text-line2").value,
  ];
  const res = window.p12proRenderText(lines, fontBytes, {
    ...commonOpts(),
    dither: document.getElementById("text-dither").value,
    invert: document.getElementById("text-invert").checked,
    lengthMM: parseFloat(document.getElementById("text-length").value) || 0,
    fontScale: parseFloat(document.getElementById("text-scale").value) || 0.85,
  });
  showResult(res, "テキスト");
});

// ---- pattern mode ---------------------------------------------------------
document.getElementById("pattern-generate").addEventListener("click", () => {
  if (!wasmReady) return log("wasm がまだ準備できていません");
  const res = window.p12proTestPattern({
    ...commonOpts(),
    height: parseInt(document.getElementById("pattern-height").value, 10) || 320,
  });
  showResult(res, "テストパターン");
});

// ---- download --------------------------------------------------------------
document.getElementById("btn-download").addEventListener("click", () => {
  if (!currentJob) return;
  const parts = [...currentJob.initPackets, currentJob.payload];
  const url = URL.createObjectURL(new Blob(parts, { type: "application/octet-stream" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = "p12pro-job.bin";
  a.click();
  URL.revokeObjectURL(url);
});

// ---- Web Bluetooth printing -------------------------------------------------
// P12 native protocol transport (mirrors the Go CLI): send the init
// packets one write each, waiting up to 500ms for any FF03 frame
// after each; then the body (ESC/POS raster) in 128-byte
// write-without-response chunks with 20ms delays. No credit gating:
// phomymo / soburi reference implementations pace purely by delay.
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

// Keep in sync with internal/p12.InitPackets.
const P12_INIT_PACKETS = [
  [0x1f, 0x11, 0x38],
  [0x1f, 0x11, 0x11, 0x1f, 0x11, 0x12, 0x1f, 0x11, 0x09, 0x1f, 0x11, 0x13],
  [0x1f, 0x11, 0x09],
  [0x1f, 0x11, 0x19, 0x1f, 0x11, 0x11],
  [0x1f, 0x11, 0x19],
  [0x1f, 0x11, 0x07],
].map(a => new Uint8Array(a));

async function pickDevice(showAll) {
  if (showAll) {
    // Show every nearby BLE device. Needed when the printer does
    // not advertise the FF00 service UUID (the filtered picker then
    // reports "No compatible device found" even for a paired,
    // powered-on printer). FF00 access is requested via
    // optionalServices, and verified after connecting.
    return navigator.bluetooth.requestDevice({
      acceptAllDevices: true,
      optionalServices: [0xff00],
    });
  }
  // Filtered picker: match by advertised FF00 service, or by the
  // known name prefixes of Print Master family printers (their
  // advertisements do not always carry the service UUID).
  return navigator.bluetooth.requestDevice({
    filters: [
      { services: [0xff00] },
      { namePrefix: "P12" },
      { namePrefix: "D30" },
      { namePrefix: "D35" },
      { namePrefix: "Q30" },
      { namePrefix: "M02" },
      { namePrefix: "Printer" },
    ],
    optionalServices: [0xff00],
  });
}

async function openPrinter(showAll) {
  const device = await pickDevice(showAll);
  log(`選択: name=${device.name || "(なし)"} id=${device.id}`);
  let server;
  try {
    server = await device.gatt.connect();
  } catch (e1) {
    log(`GATT接続に失敗 (${e1}) — 1回だけ再試行します`);
    await sleep(600);
    try {
      server = await device.gatt.connect();
    } catch (e2) {
      if (String(e2).includes("Unsupported device")) {
        log("ヒント: 'Unsupported device' は Chrome が GATT 接続不可と判定したときのエラーです。" +
            "Mac では macOS の Bluetooth 設定でペアリング済みだと発生します。システム設定で" +
            "「このデバイスの登録を解除」し、プリンタの電源を入れ直してから再試行してください。");
      }
      throw e2;
    }
  }
  log("GATT接続 OK");
  const service = await server.getPrimaryService(0xff00);
  const writeChar = await service.getCharacteristic(0xff02);
  const waiters = [];
  try {
    const flowChar = await service.getCharacteristic(0xff03);
    await flowChar.startNotifications();
    flowChar.addEventListener("characteristicvaluechanged", (e) => {
      const v = e.target.value;
      if (v.byteLength >= 2 && v.getUint8(0) === 0x02) {
        log(`バッテリー状態通知: raw=${v.getUint8(1)}`);
      }
      const ws = waiters.splice(0);
      ws.forEach(r => r());
    });
  } catch (_) { /* FF03 is optional for pacing waits */ }
  const waitNotify = (ms) => new Promise(resolve => {
    const t = setTimeout(resolve, ms);
    waiters.push(() => { clearTimeout(t); resolve(); });
  });
  return { device, server, writeChar, waitNotify };
}

async function sendJob(job, showAll) {
  const { server, writeChar, waitNotify } = await openPrinter(showAll);
  for (let i = 0; i < job.initPackets.length; i++) {
    await writeChar.writeValueWithoutResponse(job.initPackets[i]);
    await waitNotify(500);
  }
  log("初期化シーケンス完了。本体を送信します");
  const CHUNK = 128;
  const body = job.payload;
  for (let off = 0; off < body.length; off += CHUNK) {
    await writeChar.writeValueWithoutResponse(body.subarray(off, Math.min(off + CHUNK, body.length)));
    await sleep(20);
    if (off % (CHUNK * 20) === 0) log(`送信中 ${off}/${body.length} bytes`);
  }
  log("送信完了。印刷の終了を待っています...");
  await sleep(2000);
  server.disconnect();
  log("切断しました");
}

// ---- diagnostics ------------------------------------------------------------
// Full-dump diagnostics. Enumerate the WHOLE GATT tree the page is
// allowed to see (Web Bluetooth only exposes services named in
// filters/optionalServices, so the diagnostic picker requests every
// UART-ish candidate service), subscribe to every notify
// characteristic, replay the read-only query family on FF02, then
// test two activation hypotheses: setBTType (1F B2 00) before a feed
// command, and a feed command on any alternate writable channel.
const DIAG_SERVICES = [
  0xff00, 0xffe0, 0xfff0, 0xae30, 0x18f0,
  "49535343-fe7d-4ae5-8fa9-9fafd205e455",
  "e7810a71-73ae-499d-8c15-faa9aef0c3f2",
];

function hexOf(view) {
  return [...new Uint8Array(view.buffer, view.byteOffset, view.byteLength)]
    .map(b => b.toString(16).padStart(2, "0")).join(" ");
}
function shortUuid(uuid) {
  const m = uuid.match(/^0000([0-9a-f]{4})-0000-1000-8000-00805f9b34fb$/);
  return m ? m[1] : uuid;
}

async function diagnose() {
  const device = await navigator.bluetooth.requestDevice({
    acceptAllDevices: true,
    optionalServices: DIAG_SERVICES,
  });
  log(`診断: 選択 name=${device.name || "(なし)"} id=${device.id}`);
  const server = await device.gatt.connect();
  log("診断: GATT接続 OK");

  // 1. Dump the whole visible GATT tree; subscribe to all notifiers.
  const services = await server.getPrimaryServices();
  let ff01 = null, ff02 = null;
  const altWriters = [];
  for (const s of services) {
    let chars = [];
    try { chars = await s.getCharacteristics(); } catch (e) {
      log(`診断: service ${shortUuid(s.uuid)} の特性列挙に失敗 (${e})`);
      continue;
    }
    for (const c of chars) {
      const props = ["read", "write", "writeWithoutResponse", "notify", "indicate"]
        .filter(k => c.properties[k]).join("/");
      log(`診断 GATT: ${shortUuid(s.uuid)} / ${shortUuid(c.uuid)} [${props}]`);
      if (c.properties.notify || c.properties.indicate) {
        try {
          await c.startNotifications();
          const tag = shortUuid(c.uuid);
          c.addEventListener("characteristicvaluechanged", (e) => {
            log(`診断 notify(${tag}): ${hexOf(e.target.value)}`);
          });
        } catch (e) { log(`診断: ${shortUuid(c.uuid)} の購読に失敗 (${e})`); }
      }
      if (shortUuid(c.uuid) === "ff01") ff01 = c;
      if (shortUuid(c.uuid) === "ff02") ff02 = c;
      else if (c.properties.write || c.properties.writeWithoutResponse) {
        altWriters.push({ service: shortUuid(s.uuid), char: c });
      }
    }
  }
  if (!ff02) { log("診断: FF02 が見つかりません"); server.disconnect(); return; }
  await sleep(500);

  // FF01 is a read-only characteristic: on this family it is where
  // query responses wait to be picked up. Read it after every step.
  const readFF01 = async (label) => {
    if (!ff01) return;
    try {
      const v = await ff01.readValue();
      const bytes = new Uint8Array(v.buffer, v.byteOffset, v.byteLength);
      const ascii = [...bytes].map(b => (b >= 0x20 && b < 0x7f) ? String.fromCharCode(b) : ".").join("");
      log(`診断 FF01 (${label}): ${hexOf(v)}  |${ascii}|`);
    } catch (e) {
      log(`診断 FF01 (${label}): 読み取り失敗 (${e})`);
    }
  };
  await readFF01("接続直後");

  // 2. With the GATT tree dumped and FF01 read, finish. (The old
  // Quin query family is meaningless on the P12 native protocol.)
  await readFF01("終了前");

  log("診断: 終了 (紙が動いた場面があれば教えてください)");
  server.disconnect();
}

// Paper-feed test in the P12 native protocol: init sequence, then
// ESC d 13. Proves the command path reaches the print engine
// without any image data involved.
document.getElementById("btn-feed").addEventListener("click", async () => {
  if (!navigator.bluetooth) return log("このブラウザは Web Bluetooth 非対応です");
  try {
    const { server, writeChar, waitNotify } = await openPrinter(document.getElementById("opt-show-all").checked);
    for (const pkt of P12_INIT_PACKETS) {
      await writeChar.writeValueWithoutResponse(pkt);
      await waitNotify(500);
    }
    await writeChar.writeValueWithoutResponse(new Uint8Array([0x1b, 0x64, 0x0d]));
    log("紙送り: 初期化 + ESC d 13 を送信。紙が動くか見てください (3秒待ちます)");
    await sleep(3000);
    server.disconnect();
    log("紙送り: 終了");
  } catch (err) { log("紙送りエラー: " + err); }
});

document.getElementById("btn-diagnose").addEventListener("click", async () => {
  if (!navigator.bluetooth) return log("このブラウザは Web Bluetooth 非対応です");
  try {
    await diagnose();
  } catch (err) {
    log("診断エラー: " + err);
  }
});

document.getElementById("btn-print").addEventListener("click", async () => {
  if (!currentJob) return log("先にプレビューを生成してください");
  if (!navigator.bluetooth) return log("このブラウザは Web Bluetooth 非対応です");
  const showAll = document.getElementById("opt-show-all").checked;
  try {
    await sendJob(currentJob, showAll);
  } catch (err) {
    log("印刷エラー: " + err);
    if (String(err).includes("No available device") || String(err).includes("NotFoundError")) {
      log("ヒント: デバイス一覧に出ない場合は、下の「全てのデバイスを表示」を有効にして再試行してください。" +
          "また、プリンタがスマホの Print Master アプリと接続中の間は検出できません (アプリを閉じる/スマホの Bluetooth を OFF)。" +
          "OS の Bluetooth 設定でのペアリングは Web Bluetooth には不要です。");
    }
  }
});

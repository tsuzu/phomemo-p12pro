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
let currentPayload = null; // Uint8Array
let imageBytes = null;     // Uint8Array
let fontBytes = null;      // Uint8Array

function commonOpts() {
  return {
    width: parseInt(document.getElementById("opt-width").value, 10) || 96,
    density: parseInt(document.getElementById("opt-density").value, 10) || 0,
    paper: document.getElementById("opt-paper").value,
    extraFeedMM: parseFloat(document.getElementById("opt-feed").value) || 0,
  };
}

function showResult(res, what) {
  if (!res || res.error) {
    log(`${what}: エラー: ${res ? res.error : "no result"}`);
    return;
  }
  currentPayload = res.payload;
  const url = URL.createObjectURL(new Blob([res.preview], { type: "image/png" }));
  const img = document.getElementById("preview");
  img.src = url;
  document.getElementById("result-info").textContent =
    `ビットマップ ${res.width} x ${res.height} dots / payload ${res.payload.length} bytes`;
  document.getElementById("result-card").hidden = false;
  log(`${what}: payload 生成 (${res.payload.length} bytes)`);
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
  if (!currentPayload) return;
  const url = URL.createObjectURL(new Blob([currentPayload], { type: "application/octet-stream" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = "p12pro-payload.bin";
  a.click();
  URL.revokeObjectURL(url);
});

// ---- Web Bluetooth printing -------------------------------------------------
// Same transport discipline as the CLI: write chunks of 200 bytes to
// FF02, gated by credit notifications [0x01, n] on FF03; if credits
// stall, fall back to paced writes.
const sleep = (ms) => new Promise(r => setTimeout(r, ms));

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

async function sendPayload(payload, showAll) {
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
        log("ヒント: 'Unsupported device' は、そのデバイスが GATT (BLE) 接続に対応していない" +
            "エントリを選んだときに Chrome が出すエラーです。プリンタがデュアルモードの場合、" +
            "一覧に同名の Classic 側エントリが混ざることがあります。別のエントリを選ぶか、" +
            "OS の Bluetooth 設定でこのプリンタのペアリングを一度解除してから再試行してください" +
            " (Web Bluetooth に OS ペアリングは不要です)。");
      }
      throw e2;
    }
  }
  log("GATT接続 OK");
  let service;
  try {
    service = await server.getPrimaryService(0xff00);
  } catch (e) {
    // Not a Quin / Print Master GATT layout. Dump what the device
    // actually exposes — this doubles as the probe output.
    try {
      const services = await server.getPrimaryServices();
      log("このデバイスのサービス一覧:");
      for (const s of services) log(`  service ${s.uuid}`);
    } catch (_) { /* ignore */ }
    server.disconnect();
    throw new Error("サービス FF00 が見つかりません。このプリンタは想定プロトコル (Print Master / Quin 0x1F) と異なる可能性があります");
  }
  const writeChar = await service.getCharacteristic(0xff02);

  let credits = 0;
  let flowChar = null;
  let creditWaiters = [];
  const grant = (n) => {
    credits += n;
    const w = creditWaiters;
    creditWaiters = [];
    w.forEach(resolve => resolve());
  };
  try {
    flowChar = await service.getCharacteristic(0xff03);
    await flowChar.startNotifications();
    flowChar.addEventListener("characteristicvaluechanged", (e) => {
      const v = e.target.value;
      if (v.byteLength >= 2 && v.getUint8(0) === 0x01) {
        grant(v.getUint8(1) === 0x04 ? 4 : v.getUint8(1));
      } else if (v.byteLength >= 2 && v.getUint8(0) === 0x02) {
        log(`バッテリー状態通知: raw=${v.getUint8(1)}`);
      } else {
        log(`printer notify: ${[...new Uint8Array(v.buffer, v.byteOffset, v.byteLength)].map(b => b.toString(16).padStart(2, "0")).join(" ")}`);
      }
    });
    log("フロー制御 (FF03) 有効");
  } catch (_) {
    log("フロー制御なし: ペーシングのみで送信します");
  }

  function waitCredit(timeoutMs) {
    if (credits > 0) return Promise.resolve(true);
    return new Promise(resolve => {
      const timer = setTimeout(() => resolve(false), timeoutMs);
      creditWaiters.push(() => { clearTimeout(timer); resolve(true); });
    });
  }

  const CHUNK = 200;
  const noResponse = document.getElementById("opt-write-noresponse").checked;
  if (noResponse) log("応答なし書き込みモードで送信します");
  let useFlow = !!flowChar;
  for (let off = 0; off < payload.length; off += CHUNK) {
    if (useFlow) {
      const ok = await waitCredit(off === 0 ? 5000 : 3000);
      if (!ok) {
        log("クレジットが止まったためフロー制御を解除します");
        useFlow = false;
      } else {
        credits--;
      }
    }
    const chunk = payload.subarray(off, Math.min(off + CHUNK, payload.length));
    if (noResponse) await writeChar.writeValueWithoutResponse(chunk);
    else await writeChar.writeValueWithResponse(chunk);
    await sleep(useFlow ? 1 : 2);
    if (off % (CHUNK * 20) === 0) log(`送信中 ${off}/${payload.length} bytes`);
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

  const send = async (label, bytes, opts = {}) => {
    const mode = opts.withoutResponse ? "応答なし" : "応答あり";
    log(`診断 send ${label} [${mode}]: ${bytes.map(b => b.toString(16).padStart(2, "0")).join(" ")}`);
    if (opts.withoutResponse) await ff02.writeValueWithoutResponse(new Uint8Array(bytes));
    else await ff02.writeValueWithResponse(new Uint8Array(bytes));
    await sleep(400);
    await readFF01(label);
  };

  // 2. Read-only query family on FF02.
  const queries = [
    ["printerStatus", [0x1f, 0x20, 0x00]],
    ["getPrinterModel", [0x10, 0xff, 0x20, 0xf0]],
    ["getPrinterVersion", [0x10, 0xff, 0x20, 0xf1]],
    ["getPrinterSN", [0x10, 0xff, 0x20, 0xf2]],
    ["getPrinterBatteryVol", [0x10, 0xff, 0x50, 0xf1]],
    ["getPaperType", [0x1f, 0x80, 0x00]],
  ];
  for (const [name, bytes] of queries) {
    await send(name, bytes);
    await sleep(400);
  }

  // 3. Activation hypothesis: setBTType, then feed, then self-check.
  await send("setBTType", [0x1f, 0xb2, 0x00]);
  await sleep(400);
  await send("feed(after BTType)", [0x1f, 0x11, 0x50]);
  await sleep(2000);

  // 4. Write-mode hypothesis: feed + self-check WITHOUT response.
  await send("feed(応答なし)", [0x1f, 0x11, 0x50], { withoutResponse: true });
  await sleep(2000);
  await send("selfCheck(応答なし)", [0x1f, 0x40], { withoutResponse: true });
  await sleep(2000);
  await send("selfCheck(after BTType)", [0x1f, 0x40]);
  await sleep(2000);

  // 5. Alternate writable channels: try a feed on each.
  for (const w of altWriters) {
    log(`診断 send feed via ${w.service}/${shortUuid(w.char.uuid)}`);
    try {
      if (w.char.properties.write) await w.char.writeValueWithResponse(new Uint8Array([0x1f, 0x11, 0x50]));
      else await w.char.writeValueWithoutResponse(new Uint8Array([0x1f, 0x11, 0x50]));
    } catch (e) { log(`診断: alternate feed 失敗 (${e})`); }
    await sleep(2000);
  }

  log("診断: 終了 (紙が動いた場面があれば教えてください)");
  server.disconnect();
}

// Send one small raw command over a fresh connection (self-check,
// paper feed). Confirms whether ANY command produces a physical
// reaction, independently of image encoding.
async function sendRawCommand(label, bytes, showAll) {
  const device = await pickDevice(showAll);
  log(`${label}: 選択 name=${device.name || "(なし)"}`);
  const server = await device.gatt.connect();
  const service = await server.getPrimaryService(0xff00);
  const writeChar = await service.getCharacteristic(0xff02);
  try {
    const flowChar = await service.getCharacteristic(0xff03);
    await flowChar.startNotifications();
    flowChar.addEventListener("characteristicvaluechanged", (e) => {
      const v = e.target.value;
      const hex = [...new Uint8Array(v.buffer, v.byteOffset, v.byteLength)]
        .map(b => b.toString(16).padStart(2, "0")).join(" ");
      log(`${label} notify: ${hex}`);
    });
  } catch (_) { /* flow char optional here */ }
  await writeChar.writeValueWithResponse(new Uint8Array(bytes));
  log(`${label}: コマンド送信 (${bytes.map(b => b.toString(16).padStart(2, "0")).join(" ")}) — 反応を3秒待ちます`);
  await sleep(3000);
  server.disconnect();
  log(`${label}: 終了`);
}

document.getElementById("btn-selfcheck").addEventListener("click", async () => {
  if (!navigator.bluetooth) return log("このブラウザは Web Bluetooth 非対応です");
  try {
    await sendRawCommand("セルフテスト", [0x1f, 0x40], document.getElementById("opt-show-all").checked);
  } catch (err) { log("セルフテストエラー: " + err); }
});

document.getElementById("btn-feed").addEventListener("click", async () => {
  if (!navigator.bluetooth) return log("このブラウザは Web Bluetooth 非対応です");
  try {
    await sendRawCommand("紙送り", [0x1f, 0x11, 0x50], document.getElementById("opt-show-all").checked);
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
  if (!currentPayload) return log("先にプレビューを生成してください");
  if (!navigator.bluetooth) return log("このブラウザは Web Bluetooth 非対応です");
  const showAll = document.getElementById("opt-show-all").checked;
  try {
    await sendPayload(currentPayload, showAll);
  } catch (err) {
    log("印刷エラー: " + err);
    if (String(err).includes("No available device") || String(err).includes("NotFoundError")) {
      log("ヒント: デバイス一覧に出ない場合は、下の「全てのデバイスを表示」を有効にして再試行してください。" +
          "また、プリンタがスマホの Print Master アプリと接続中の間は検出できません (アプリを閉じる/スマホの Bluetooth を OFF)。" +
          "OS の Bluetooth 設定でのペアリングは Web Bluetooth には不要です。");
    }
  }
});

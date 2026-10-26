# phomemo-p12pro

Phomemo **P12 Pro** に画像ファイル (BMP / PNG / JPEG) を印刷するための Go 製ツール。

まずは「画像を用意したら印刷できる」を目標にした最小構成:

- 画像処理 (グレースケール化 → 回転 → ヘッド幅フィット → 2値化 → ビットパック)
- **P12 ネイティブプロトコル** の生成 (初期化シーケンス + ESC/POS ラスター。下記)
- BLE 送信 (FF00 サービス / FF02 書き込み / FF03 通知)
- ハードウェアなしで検証できる `render` (バイナリ生成) と `preview` (1bit プレビュー PNG)

## 仕様前提 (P12 Pro)

| 項目 | 値 |
|---|---|
| 解像度 | 203 dpi = 8 dots/mm |
| テープ幅 | 12mm |
| ヘッド幅 (デフォルト) | 96 dots (= 12mm)。一部資料では有効印字幅 9mm とも。`testpattern` で実測して `-width` で調整 |
| テープ種別 | 連続テープ |
| 接続 | BLE (USB Type-C は充電専用) |

## プロトコルについて (実機検証で判明した経緯)

P12 Pro は公式アプリ **Print Master** を使うが、印字プロトコルは
D30 系の Zhuhai Quin 0x1F ジョブ方式 **ではない**。実機検証
(2026-10-26) の結果:

- GATT は Service `0000ff00-...`, Write `0000ff02-...`,
  Notify `0000ff03-...` で Quin 系と同一。FF03 のクレジット通知
  `[01, n]` とバッテリー通知 `[02, raw]` も来る
- しかし Quin ジョブ (画像コマンド・`1F 40` セルフチェック・
  `1F 11 50` 紙送り) は **すべて無反応** (紙が一切動かない)。
  BLE モジュールだけが ACK を返し、印字エンジンに届いていない
- 実際に動くのは **P12 ネイティブプロトコル** (`internal/p12`、
  soburi/phomemo_p12 のリバースエンジニアリングと、それを使う
  phomymo Web アプリに基づく):
  1. 初期化シーケンス: `1F 11 xx` の6パケットを1つずつ送信し、
     各パケット後にプリンタ応答 (FF03 通知) を最大500ms待つ
  2. ヘッダ: `ESC @` (初期化) + `GS v 0` ラスター開始
     `1D 76 30 00 <widthBytes LE> <rows LE>`
  3. ラスター本体: 無圧縮の 1bit ビットマップ (黒=1、MSB first、
     プリンタ向き)。128 byte ずつ応答なし書き込み、20ms 間隔
  4. 紙送り: `ESC d 13` (`1B 64 0D`) を2回

旧 Quin 実装 (`internal/quin`) は D30 系向けに `-protocol quin`
として残してある (デフォルトは `p12`)。

## Web版 (WebAssembly + Web Bluetooth)

ブラウザだけでも動く: **https://tsuzu.github.io/phomemo-p12pro/**

- 画像印刷 / テキスト2段 / テストパターンの3モード。レンダリングは
  CLI と同じ Go コードを WebAssembly 化したもの (`cmd/wasm`) で実行し、
  印刷は Web Bluetooth で FF02 に直接送る (P12 ネイティブプロトコルの
  ペーシングを JS 側で再現)
- 印刷には Chrome / Edge と HTTPS が必要 (Web Bluetooth の制約)。
  プレビュー生成だけならどのブラウザでも動く
- テキストモードのフォントはファイル指定か、デフォルト URL
  (Noto Sans CJK, jsDelivr 経由) から取得する

ローカルで動かす場合:

```console
make web   # web/p12pro.wasm と web/wasm_exec.js を生成
python3 -m http.server -d web 8080
# http://localhost:8080 を Chrome で開く
```

デプロイは `.github/workflows/pages.yml` が main への push で
GitHub Pages に自動で行う。

## 使い方

```console
# 0) プリンタを探す / プロトコル確認
go run ./cmd/p12pro scan
go run ./cmd/p12pro probe --name P12

# 1) 画像 → ペイロード生成だけ (ハード不要)
go run ./cmd/p12pro render -i label.bmp -o payload.bin

# 2) 1bit 化の結果を目視確認
go run ./cmd/p12pro preview -i label.bmp -o preview.png

# 3) 印刷 (BLE)
go run ./cmd/p12pro print -i label.bmp --name P12
go run ./cmd/p12pro print -i label.bmp --address AA:BB:CC:DD:EE:FF

# 3') 印刷 (RFCOMM などで /dev が見える環境)
go run ./cmd/p12pro print -i label.bmp --device /dev/rfcomm0

# 4) 較正パターン (枠・1mm ルーラー・グラデーション)
go run ./cmd/p12pro testpattern --name P12
```

## テキストを2段で印刷する

画像を作らなくても、文字列を直接渡せば2段組ラベルを印刷できる。
フォントは自動検出 (Linux: Noto Sans CJK / macOS: ヒラギノ /
Windows: 游ゴシック等) し、`--font` や環境変数 `P12PRO_FONT` で上書きできる。
描画は 4倍スーパーサンプリング + 縮小で行い、12mm テープの
1行あたり約42ドットでも漢字が潰れないようにしている。

```console
# 2段 (長さは文字に合わせて自動)
go run ./cmd/p12pro text "調味料" "しょうゆ" --name P12

# 1段だけ (行スロット全体を使うので大きく出る)
go run ./cmd/p12pro text "しょうゆ" --name P12

# 長さ固定 (40mm)、プレビュー付き、実印刷せずペイロードだけ
go run ./cmd/p12pro text "期限" "2026.10.26" --length-mm 40 \
    --preview preview.png -o payload.bin

# 反転 (黒地に白文字)
go run ./cmd/p12pro text "注意" "高温" --name P12 -invert
```

text 固有の主なフラグ:

```
--font          フォントファイル (.ttf/.otf/.ttc)
--length-mm 0   ラベル長固定 mm (0 = 文字にフィット)
--font-scale    行の高さに対するフォントサイズ比 (デフォルト 0.85)
--align center  行の配置 left|center|right
--gap 4         段間の余白 dots
--margin-y 4    上下の余白 dots
--pad-x 8       両端の余白 dots
--supersample 4 描画のスーパーサンプリング倍率
--dither        text ではデフォルト threshold (fs も選択可)
```

フラグは行テキストの前後どちらに置いてもよい。

## 画像の作り方

- **向き**: ラベルとして見た向きで作る。横方向 = テープの送り方向 (ラベルの長さ)、
  縦方向 = テープ幅。デフォルトで `-rotate 90` が掛かり、縦がヘッド幅 96 dots になる。
  既にプリンタ向き (幅 96) の画像なら `-rotate 0`。
- **サイズ**: 縦は 96 px ちょうどが理想 (12mm @203dpi)。違う場合は
  パディング (デフォルト、中央配置) か `-scale-up` で拡縮される。
  横 (長さ) は任意。例: 40mm ラベルなら 320 px。
- **中身**: 線画・文字は `-dither threshold` (デフォルト threshold=128)、
  写真・グラデーションは `-dither fs` (Floyd–Steinberg)。
- BMP は 1bit / 8bit / 24bit いずれも可 (内部で BT.601 輝度 → 2値化)。

主なフラグ (render / preview / print 共通):

```
-i             入力画像 (BMP/PNG/JPEG)
-width 96      ヘッド幅 dots
-rotate 90     入力への回転 (0/90/-90/180)
-dither fs     fs | threshold
-threshold 128 2値化閾値
-invert        白黒反転
-density 0     濃度 1..15 (0 = プリンタ設定のまま)
-paper continuous  continuous | gap | mark
-extra-feed-mm 0   印刷後の追加送り mm (カッター位置までのリードアウト)
-chunk 200     BLE 書き込みチャンク byte
```

## 構成

```
cmd/p12pro/          CLI (render/preview/text/print/scan/probe/testpattern)
cmd/wasm/            WebAssembly 版 (JS API: 画像/テキスト/パターン → payload+preview)
web/                 Web UI (Web Bluetooth 送信は JS 側, Pages にデプロイ)
internal/imgproc/    画像処理 (BMP読込, 回転, リサイズ, ディザ, ビットパック, 較正パターン)
internal/textlabel/  テキスト描画 (sfntフォント, 段組レイアウト, スーパーサンプリング)
internal/p12/        P12 ネイティブプロトコル (初期化列 + ESC/POS ラスター) ※デフォルト
internal/quin/       Quin 0x1F プロトコル (D30 系向け, -protocol quin)
internal/bleprinter/ BLE トランスポート (tinygo bluetooth)
```

コア (imgproc / p12 / quin) は外部依存が `golang.org/x/image` のみで、
単体テストでバイト列まで検証できる。BLE は `tinygo.org/x/bluetooth`
(BlueZ / macOS / Windows 対応)。

## テスト

```console
go build ./... && go vet ./... && go test ./...
```

## 未検証・既知の制限

- P12 ネイティブプロトコルへの切替は実施済み (2026-10-26)。初回の
  実機印刷で、印字の向き (上下・左右) と有効印字幅を最終確認する。
  上下逆に出たら `-rotate -90` に変える。
- 有効印字幅: 96 dots 前提だが、実機では左右マージンで狭い可能性がある。
  `testpattern` の枠が欠ける側を測って `-width` と配置を調整する。
- BLE 書き込みは tinygo の制約で WriteWithoutResponse のみ
  (プロトコルのペーシングは応答待ち + 遅延で再現している)。

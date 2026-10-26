# phomemo-p12pro

Phomemo **P12 Pro** に画像ファイル (BMP / PNG / JPEG) を印刷するための Go 製ツール。

まずは「画像を用意したら印刷できる」を目標にした最小構成:

- 画像処理 (グレースケール化 → 回転 → ヘッド幅フィット → 2値化 → ビットパック)
- Zhuhai Quin / "Print Master" 系プロトコル (0x1F コマンド列) の生成
- BLE 送信 (FF00 サービス、FF03 クレジットベースのフロー制御)
- ハードウェアなしで検証できる `render` (バイナリ生成) と `preview` (1bit プレビュー PNG)

## 仕様前提 (P12 Pro)

| 項目 | 値 |
|---|---|
| 解像度 | 203 dpi = 8 dots/mm |
| テープ幅 | 12mm |
| ヘッド幅 (デフォルト) | 96 dots (= 12mm)。一部資料では有効印字幅 9mm とも。`testpattern` で実測して `-width` で調整 |
| テープ種別 | 連続テープ (`-paper continuous` がデフォルト) |
| 接続 | BLE (USB Type-C は充電専用) |

## プロトコルについて — 要実機検証

P12 Pro は公式アプリ **Print Master** を使う機種で、同アプリを使う
Marklife P12 / Phomemo D30 などは **Zhuhai Quin** 社のファームウェアで、
以下の BLE GATT + 0x1F コマンド体系を使うことがリバースエンジニアリングで
判明している (Dart ライブラリ `print_master_ble` ほか)。

- Service `0000ff00-...`, Write `0000ff02-...`, Notify/Flow `0000ff03-...`
- 画像送信: `1F 10 <bytesPerRow BE> <height BE> <len BE> <ZLIB>`
  - ZLIB はヘッダ `28 15` (1KB ウィンドウ)、deflate は stored (無圧縮)、Adler-32 BE
  - ビットマップは 1bit パック、黒=1、MSB first
- ジョブ列: `SetPaperType(1F 80 01 t)` → `Start(1F C0 01 00)` →
  `AlignStart(1F 11 51)` → 画像 → `Stop(1F C0 01 01)` → `FeedEnd(1F 11 50)`
- フロー制御: FF03 の通知 `[01, n]` が n チャンク分 (200 byte) のクレジット

ただし **P12 Pro 実機での検証は未実施**。同系列でも亜種が存在する
(例: M02/M110 系は ESC/POS ラスター方式で別プロトコル)。
まず実機で次を確認すること:

```console
$ go run ./cmd/p12pro scan            # FF00 をアドバタイズするか
$ go run ./cmd/p12pro probe --name P12  # GATT ツリーに FF00/FF02/FF03 があるか
```

FF00/FF02/FF03 が確認できれば、このツールの print 経路がそのまま使える
可能性が高い。違っていたら probe の出力をもとにプロトコルを差し替える。

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
cmd/p12pro/          CLI (render/preview/print/scan/probe/testpattern)
internal/imgproc/    画像処理 (BMP読込, 回転, リサイズ, ディザ, ビットパック, 較正パターン)
internal/quin/       0x1F プロトコル (ジョブ列生成, ZLIB stored 画像コマンド)
internal/bleprinter/ BLE トランスポート (tinygo bluetooth, クレジット制御)
```

コア (imgproc / quin) は外部依存が `golang.org/x/image` のみで、
単体テストでバイト列まで検証できる。BLE は `tinygo.org/x/bluetooth`
(BlueZ / macOS / Windows 対応)。

## テスト

```console
go build ./... && go vet ./... && go test ./...
```

## 未検証・既知の制限

- **実機未検証**: プロトコル同定 (FF00 の有無) と初回印刷が最初のマイルストーン。
- 有効印字幅: 96 dots 前提だが、実機では左右マージンで狭い可能性がある。
  `testpattern` の枠が欠ける側を測って `-width` と配置を調整する。
- テープ送り方向と回転の符号 (-rotate 90 / -90) は実機で一度確認が必要。
  上下逆に出たら `-rotate -90` に変える。
- BLE 書き込みは tinygo の制約で WriteWithoutResponse + クレジット制御 +
  ペーシングで信頼性を確保している (Linux では WithResponse が使えない)。
- ステータス取得 (電池・モデル名などの 0x10 クエリ) のコマンド定義は
  `internal/quin` にあるが、応答を読む CLI は未実装。

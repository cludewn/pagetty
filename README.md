# PageTTY

対話型CLIプログラムの実行結果をPNG/SVG画像として保存するツールです。
同じ端末で繰り返し入力でき、長い出力は表示行単位で自動分割します。
授業レポートなどへの貼り付けを想定しています。

## 対応環境とインストール

初期配布対象はWindows amd64（64ビットx86）のZIPとLinux amd64のtar.gzです。
Linux版はUbuntu 24.04のWSL2で実行確認済みです。
Windows 11では以前のビルドで実CLI・ConPTYの主要機能を確認しました。
今回の配布候補はWindows amd64向けクロスビルドとアーカイブ検証までで、最終候補exeのWindows上での実行は未検証です。
独立したLinux実機や他のOS・CPUは未検証です。

配布アーカイブは[GitHub Releases](https://github.com/cludewn/pagetty/releases)から取得します。
配布前はダウンロード用アセットがまだ存在しない場合があります。

- Windows：SHA256SUMSと、PowerShellの `Get-FileHash .\pagetty-windows-amd64.zip -Algorithm SHA256` の結果を照合します。
  ZIPを展開し、PowerShellで展開先へ移動して .\pagetty.exe として実行します。
- Linux/WSL2：`grep -E '  pagetty-linux-amd64\.tar\.gz$' SHA256SUMS | sha256sum -c -` で照合し、`tar -xzf pagetty-linux-amd64.tar.gz` で展開します。
  Linux版アーカイブとSHA256SUMSだけで照合できます。対象行欠落・形式不正・不一致はエラーになります。
  展開先で ./pagetty として実行します。

pagetty で起動したい場合は展開先をPATHに追加します。
Go、画像レンダラー、フォントの別途インストールは不要ですが、対象のPython等の処理系は必要です。
警告が出た場合は配布元・対象ファイルと検知内容を確認してください。

## 基本操作

以下はPATHに追加した場合の例です。追加していない場合は上記の相対パスに置き換えます。

~~~sh
pagetty capture -- ./a.out
pagetty capture -- python main.py
pagetty capture --format svg --output-dir ./results -- ./a.out
pagetty capture --columns 80 --max-lines 44 -- ./a.out
pagetty capture --help
~~~

Windowsの実行対象は .\a.exe などを指定します。
-- の後にコマンドと引数を渡します。シェル構文は自動解釈しません。

| オプション | 既定値 | 意味 |
|---|---|---|
| --columns | 80 | 実行端末の列数・画像幅 |
| --max-lines | 44 | 画像1枚の最大表示行数 |
| --format | png | png または svg |
| --output-dir | ./captures/ | 起動時の作業フォルダを基準とする保存先 |

保存先は存在しなければ作成します。
ファイル名は capture-日時-識別子_001.png のようになり、ページ番号が続きます。
既存ファイルは上書きしません。空行付近を優先して分割するため、指定行数未満のページもできます。
正常終了・非0終了時に取得済みの表示を保存し、中断時はpartial付きの名前で部分記録の保存を試みます。
対象の終了コードを引き継ぎます（引数不正2、ツール側の失敗1、中断130）。
画像生成・保存の失敗を優先します。

## Wordへの貼り付け

「挿入」→「画像」からPNG/SVGを選び、縦横比を保って幅を調整します。
今回のA4縦テンプレートでは、幅15cm、80列・44行が本文領域に収まり、45行以上は下端を超えました。
この実測に基づく既定値であり、すべてのWord文書で1ページに収まる保証はありません。
余白・本文・挿入位置に合わせて列数や行数を調整してください。

## 入力ファイルを使う場合

PageTTY自身へのパイプ・ファイル入力リダイレクトは非対応です。
pagetty capture -- ./a.out < input.txt のような指定は使用できません。
入力ファイルは子シェル内で指定します。

Linux/WSL2：

~~~sh
pagetty capture -- sh -c 'exec ./a.out < input.txt'
~~~

Windows：

~~~powershell
pagetty capture -- cmd.exe /d /c ".\a.exe < input.txt"
~~~

これは単純なパスの基本例です。Windowsの空白入りパスや複雑な引用符は、シェルごとの解釈に注意が必要です。
ファイルから読み取った入力値は、対象プログラムが画面へ出力しない限り画像には表示されません。

## 既知の制約

- Windowsでは断続的な起動拒否が発生し、原因は未確定です。ウイルスバスター クラウドの承認要求も繰り返し発生していますが、起動拒否の原因と断定できる証拠はありません。

- 画像は端末表示の再描画です。OS画面そのもののスクリーンショットではありません。
- 同梱BIZ UDGothicにない文字・絵文字等は、表示クラスタ単位で□に置き換えます。
  元の文字を再現した画像ではありません。セル幅と表示属性は維持します。絵文字の完全対応・カラー表示は行いません。
- 全画面TUI、上書き前の画面の時系列保存、実行中の端末リサイズへの追従は保証しません。
- 非表示入力を画像へ追加しません。表示された機密情報の扱いには注意してください。
- 画像寸法には上限があり、メモリ・保存先等のエラーによって保存できない場合があります。

## ライセンス

PageTTYは[MIT License](LICENSE)です。
同梱フォントはSIL Open Font License 1.1、依存ソフトウェアは各ライセンスに従います。
詳細は[THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt)を参照してください。

# PageTTY

PageTTYは、ターミナルで実行したプログラムの表示をPNGやSVGの画像に保存するツールです。プログラムと対話しながら記録でき、長い結果は読みやすいように複数枚に分割します。授業レポートなどに実行結果を載せるときに使えます。

## インストール

**[GitHub Releases](https://github.com/cludewn/pagetty/releases/latest) から、使う環境に合ったファイルをダウンロードしてください**。Goやフォントの追加インストールは不要です。

### Windows（x86-64）

1. `pagetty-windows-amd64.zip`をすべて展開します。
2. 展開先の`install.cmd`をダブルクリックします。
3. ターミナルを閉じて開き直し、`pagetty --help`を実行します。見つからない場合は、Windowsにサインインし直してください。

インストール先は`%LOCALAPPDATA%\PageTTY`です。更新するときは新しいZIPの`install.cmd`を実行します。削除するときは、エクスプローラーで`%LOCALAPPDATA%\PageTTY`を開き、`uninstall.cmd`をダブルクリックして、ターミナルを開き直します。PATHを手動で編集した場合は、削除時に残った項目の確認を案内することがあります。

### Linux / WSL（x86-64、Bash）

ダウンロード先で次を実行し、Bashのターミナルを開き直して`pagetty --help`を実行します。

~~~sh
mkdir -p pagetty-setup
tar -xzf pagetty-linux-amd64.tar.gz -C pagetty-setup
cd pagetty-setup
bash install.sh
~~~

インストール先は`~/.local/share/pagetty`です。更新するときは新しく展開した`install.sh`を実行します。削除するときは、`bash ~/.local/share/pagetty/uninstall.sh`を実行し、ターミナルを開き直します。

Bash以外のシェルや、Bashの起動設定を独自に管理している環境では、展開先の`./pagetty`を直接実行できます。

### セキュリティソフトの警告について

PageTTYのダウンロード時や実行時に、セキュリティソフトなどの警告が表示される場合があります。

<details>

<summary>警告が表示される理由と対処方法</summary>

PageTTYのWindows実行ファイルにはコード署名がありません。未署名であることや利用実績の少なさなどが、警告の一因となる場合があります。Linuxでも、ブラウザやセキュリティソフトの設定によっては警告が発生する可能性があります。

警告は必ずしもマルウェアの検出を意味しません。ただし、未署名であることだけを理由に、警告を無視してよいと判断することはできません。

警告が表示された場合は、公式GitHub Releasesからダウンロードしたファイルであることと、警告の内容を確認してください。

ダウンロードしたファイルが正式な配布物と一致するか確認したい場合は、後述の[検証方法](#ダウンロードの検証任意)で任意に検証できます。ただし、この検証はソフトウェア自体の安全性を保証するものではありません。

</details>

## 使い方

いつもの実行コマンドの前に`pagetty --`を付けます。

例：
- WindowsでCの実行ファイルを使う場合は`pagetty -- .\a.exe`
- Linux / WSLでCの実行ファイルを使う場合は`pagetty -- ./a.out`
- `python`コマンドが使える場合は`pagetty -- python main.py`

ファイル名は自分のプログラムに合わせてください。`--`はPageTTYのオプションと実行するコマンドを区切るために必須です。

プログラムが入力を求めたら、同じターミナルで入力できます。プログラムの終了後、既定では現在のフォルダーの`captures/`にPNG画像が保存されます。画像の生成中は進み具合が、完了時は保存先が表示されます。

### 動作確認（任意）

手元にプログラムがない場合は、ターミナルで次のコマンドを試せます。この例でも`captures/`にPNG画像が保存され、フォルダーがなければ作成されます。

- Windows：`pagetty -- cmd.exe /d /c "echo Hello, PageTTY!"`
- Linux / WSL：`pagetty -- printf 'Hello, PageTTY!\n'`

## オプションと使用例

~~~text
pagetty [options] -- <command> [args...]
~~~

オプションは`--`の前に指定します。

| オプション | 既定値 | 用途 |
| --- | --- | --- |
| `--format` | `png` | 画像形式を`png`または`svg`に指定 |
| `--columns` | `80` | 1行の表示幅を列数で指定 |
| `--max-lines` | `44` | 画像1枚の最大表示行数を指定。画像寸法に応じて少ない行数で分割する場合があります |
| `--page-limit` | `16` | 保存範囲を確認する枚数を1〜64で指定 |
| `--output-dir` | `./captures/` | 画像の保存先を指定（フォルダーがなければ作成） |

Pythonの実行結果をSVGで別のフォルダーに保存する例です。

~~~sh
pagetty --format svg --output-dir ./report-images -- python main.py
~~~

Linux / WSLで表示幅を100列に広げ、画像1枚を最大30行にする例です。

~~~sh
pagetty --columns 100 --max-lines 30 -- ./a.out
~~~

画像名には`20261122-123045_001.png`のように実行開始時の日時とページ番号が付きます。既存の画像は上書きされません。上限で打ち切った記録や記録エラー時に保存できた画像には`-partial`が付きます。

### 出力が多い場合と中断

記録に取り込む端末出力は2 MiB、表示行は4,096行、列数×保持行数は131,072セルが上限です。端末寸法は3,273列・65,536セルまでです。記録を打ち切ると標準エラーに通知し、対象プログラムの実行と入出力は継続します。端末制御で履歴が一度に増えて先頭が上書きされた場合は、欠落を通知し、残った内容を部分記録として保存します。生成時には最大64枚に加え、画像1枚の辺長32,768 px・16 Miピクセル、PNGの合計32 Miピクセル、内部SVGの1枚16 MiB、最終画像の1枚8 MiB・合計64 MiBの制限が適用されます。

`--page-limit` は生成する画像の範囲を決める設定です。記録量の上限は独立して適用されます。実際のページ数が設定を超え、標準入力と標準エラーが端末の場合は、対象の終了後に保存範囲を確認します。`a` は保存可能な全ページ、`l` は設定上限まで、`n`・Enter・EOFは保存しない選択です。非対話環境では設定上限までを部分記録として保存します。安全上の上限が事前に分かる場合は、保存可能な範囲を表示します。画像データ量など、生成中に判明する制限も適用されます。

実行中にCtrl+Cを押すと、対象プログラムにキー入力が転送されます。対象が割り込みを捕捉・無視して実行を続ける場合は、PageTTYも実行を続けます。対象が割り込みで終了した場合や、対象終了後の確認・画像生成・保存をCtrl+Cで中断した場合は、今回の画像を破棄します。画像ファイルの書き込みや公開に失敗した場合も、今回の画像を破棄します。

通常は対象プログラムの終了コードを引き継ぎます。保存を辞退したり、設定上限までの保存を選んだりしただけでは終了コードは変わりません。記録の欠落、非対話時の打ち切り、資源制限による省略や処理のエラーは`1`、引数エラーは`2`、Ctrl+Cによる中断は`130`です。

## Wordなどに画像を挿入する

Wordの「挿入」→「画像」から保存したファイルを選び、縦横比を保って幅を調整します。1枚がページに収まらないときは、`--max-lines`を小さくして撮り直すと、1枚あたりの表示行数を減らせます。SVGが使えないアプリにはPNGを挿入してください。

## 入力ファイルを使う

PageTTYの標準入力にはパイプやファイルを接続できません。対象プログラムにファイルを渡す場合は、子シェルの中でリダイレクトします。

Linux / WSL：

~~~sh
pagetty -- sh -c 'exec ./a.out < input.txt'
~~~

Windows（PowerShell）：

~~~powershell
pagetty -- cmd.exe /d /c ".\a.exe < input.txt"
~~~

入力ファイルの中身は、対象プログラムが画面に表示した分だけ画像に含まれます。

## 利用上の制約

- 画像はターミナルに表示された内容を再描画したものです。画面の上書き前の状態は時系列で保存されません。全画面アプリの完全な再現や、記録中の端末サイズ変更への追従は保証しません。
- 同梱フォントにない文字や絵文字は`□`になります。
- 非表示の入力は画像に入りません。画面に出力された機密情報は共有前に確認してください。

## ダウンロードの検証（任意）

ダウンロードしたファイルが正式な配布物と一致するか確認したい場合は、以下の方法で検証できます。通常の利用に必須の手順ではありません。また、検証に成功してもソフトウェア自体の安全性が保証されるわけではありません。

以下のコマンドは、ダウンロードしたアーカイブがあるフォルダーで実行してください。

<details>

<summary>GitHub CLIがある場合</summary>

GitHubの署名付きRelease Attestationを使って検証します。対象はRelease Attestationが付いた変更不可（Immutable）のリリースです。

**Windows（PowerShell）**

~~~powershell
gh release verify-asset .\pagetty-windows-amd64.zip -R cludewn/pagetty
~~~

**Linux / WSL**

~~~sh
gh release verify-asset ./pagetty-linux-amd64.tar.gz -R cludewn/pagetty
~~~

`verify-asset`に対応したGitHub CLIと認証が必要です。タグを省略すると最新リリースを対象に検証します。過去のリリースを検証する場合は、ファイル名の前に対象のリリースタグを指定してください。

</details>

<details>

<summary>GitHub CLIがない場合</summary>

GitHub Releasesの同じリリースからアーカイブと`SHA256SUMS`をダウンロードし、同じフォルダーに置きます。

**Windows**

PowerShellで次のコマンドを実行し、表示されたSHA-256を`SHA256SUMS`のWindows版の値と比較します。

~~~powershell
Get-FileHash .\pagetty-windows-amd64.zip -Algorithm SHA256
~~~

**Linux / WSL**

次のコマンドでLinux版のSHA-256を照合します。

~~~sh
grep -E '^[^[:space:]]*  pagetty-linux-amd64\.tar\.gz$' SHA256SUMS | sha256sum --strict -c -
~~~

</details>

## ライセンス

PageTTYは[MIT License](LICENSE)です。同梱フォントと依存ソフトウェアのライセンスは[THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt)を参照してください。

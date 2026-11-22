# PageTTY

PageTTYは、ターミナルで実行したプログラムの表示をPNGやSVGの画像に保存するツールです。プログラムと対話しながら記録でき、長い結果は読みやすいように複数枚に分割します。授業レポートなどに実行結果を載せるときに使えます。

## インストール

[GitHub Releases](https://github.com/cludewn/pagetty/releases)から、使う環境に合ったファイルをダウンロードしてください。Goやフォントの追加インストールは不要です。

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

## 最初の画像を作る

ターミナルで次のコマンドを実行します。

- Windows：`pagetty -- cmd.exe /d /c "echo Hello, PageTTY!"`
- Linux / WSL：`pagetty -- printf 'Hello, PageTTY!\n'`

終了すると、現在のフォルダーの`captures/`にPNG画像ができます。画像生成中はページ数と経過時間が表示され、完了時に枚数、保存先、コマンド終了後の出力回収から画像の生成・保存までにかかった時間が分かります。自分のプログラムを記録するときは、`--`の後に普段実行するコマンドを置きます。入力が求められたら、同じターミナルで入力してください。

## オプションと使用例

~~~text
pagetty [options] -- <command> [args...]
~~~

`--`はPageTTYのオプションと、実行するコマンド・引数を分けるために必須です。ヘルプは`pagetty --help`で表示できます。

| オプション | 既定値 | 用途 |
| --- | --- | --- |
| `--format` | `png` | 画像形式を`png`または`svg`に指定 |
| `--columns` | `80` | 1行の表示幅を列数で指定 |
| `--max-lines` | `44` | 画像1枚の最大表示行数を指定 |
| `--output-dir` | `./captures/` | 画像の保存先を指定 |

~~~sh
pagetty -- ./a.out
pagetty -- python main.py
pagetty --format svg --output-dir ./report-images -- python main.py
pagetty --columns 100 --max-lines 30 -- ./a.out
~~~

Windowsでは実行ファイルに`.\a.exe`などを指定します。Pythonのプログラムを実行するにはPythonが必要です。保存先がなければ自動で作成され、画像の名前は、実行開始時のローカル日時とページ番号を使った`20261122-123045_001.png`形式です。同じ秒の別の実行には`-2`などが付き、既存ファイルは上書きされません。中断時などの部分記録には`-partial`が付きます。

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

## Wordなどに画像を挿入する

Wordの「挿入」→「画像」から保存したファイルを選び、縦横比を保って幅を調整します。1枚がページに収まらないときは、`--max-lines`を小さくして撮り直すと分割できます。SVGが使えないアプリにはPNGを挿入してください。

## 利用上の制約

- 画像はターミナルに表示された内容を再描画したものです。画面の上書き前の状態は時系列で保存されません。全画面アプリの完全な再現や、記録中の端末サイズ変更への追従は保証しません。
- 同梱フォントにない文字や絵文字は`□`になります。
- 非表示の入力は画像に入りません。画面に出力された機密情報は共有前に確認してください。

## ダウンロードの照合（任意）

Releasesの`SHA256SUMS`とアーカイブを同じフォルダーに置きます。

- Windows：PowerShellで`Get-FileHash .\pagetty-windows-amd64.zip -Algorithm SHA256`を実行し、`SHA256SUMS`のWindows版の値と照合します。
- Linux / WSL：以下のコマンドでLinux版の行を抽出して照合します。対象行がない場合、抽出した行の形式が不正な場合、ハッシュが一致しない場合は失敗します。

~~~sh
grep -E '^[^[:space:]]*  pagetty-linux-amd64\.tar\.gz$' SHA256SUMS | sha256sum --strict -c -
~~~

## ライセンス

PageTTYは[MIT License](LICENSE)です。同梱フォントと依存ソフトウェアのライセンスは[THIRD_PARTY_NOTICES.txt](THIRD_PARTY_NOTICES.txt)を参照してください。

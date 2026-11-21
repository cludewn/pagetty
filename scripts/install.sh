#!/usr/bin/env bash
set -euo pipefail

fail() { printf 'PageTTY: %s\n' "$*" >&2; exit 1; }
mode="${1:-install}"
[[ $# -le 1 && ( $mode == install || $mode == uninstall ) ]] || fail 'Use: bash install.sh [uninstall]'
[[ ${HOME:-} == /* && $HOME != *:* && $HOME != *$'\n'* ]] || fail 'HOME must be an absolute path without a colon or newline.'
dir="$HOME/.local/share/pagetty"
binary="$dir/pagetty"
state="$dir/.pagetty-install"
# ログインシェルと対話シェルで読み込まれても、PATH には一度だけ追加する。
hook='case ":${PATH-}:" in *":$HOME/.local/share/pagetty:"*|*":$HOME/.local/share/pagetty/:"*) ;; *) export PATH="${PATH:+$PATH:}$HOME/.local/share/pagetty" ;; esac # PageTTY installer'
rc_files=("$HOME/.bashrc" "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile")
if [[ $mode == install ]]; then
    login="$HOME/.profile"
    for rc in "${rc_files[@]:1}"; do
        if [[ -e $rc || -L $rc ]]; then login="$rc"; break; fi
    done
    rc_files=("$HOME/.bashrc" "$login")
    src=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
    [[ -f $src/pagetty && -f $src/LICENSE && -f $src/THIRD_PARTY_NOTICES.txt ]] ||
        fail 'Extract the Linux archive before running install.sh.'
fi

installed_hash=pending
if [[ -e $dir || -L $dir ]]; then
    [[ -d $dir && ! -L $dir && -f $state && ! -L $state ]] ||
        fail "Installation directory already exists and is not managed: $dir"
    read -r installed_hash next_hash < "$state"
    [[ $installed_hash =~ ^[0-9a-f]{64}$ || $installed_hash == pending ]] || fail 'Invalid installation record.'
    [[ -z $next_hash || $next_hash =~ ^[0-9a-f]{64}$ ]] || fail 'Invalid installation record.'
    if [[ -e $binary || -L $binary ]]; then
        [[ -f $binary && ! -L $binary ]] || fail "Unexpected file: $binary"
        actual=$(sha256sum < "$binary")
        actual=${actual%% *}
        [[ $actual == "$installed_hash" || $actual == "$next_hash" ]] ||
            fail "Existing executable was not installed by this installer: $binary"
        installed_hash=$actual
    fi
    for doc in LICENSE THIRD_PARTY_NOTICES.txt; do
        [[ ! -L $dir/$doc && ( ! -e $dir/$doc || -f $dir/$doc ) ]] || fail "Unexpected file: $dir/$doc"
    done
elif [[ $mode == uninstall ]]; then
    printf 'PageTTY is not installed.\n'
    exit 0
fi

for rc in "${rc_files[@]}"; do
    if [[ $mode == uninstall ]] && { [[ ! -f $rc ]] || ! grep -Fxq -- "$hook" "$rc"; }; then continue; fi
    [[ ! -L $rc && ( ! -e $rc || -f $rc ) ]] || fail "Cannot safely edit Bash startup file: $rc"
done

if [[ $mode == install ]]; then
    if [[ ! -d $dir ]]; then
        mkdir -p -- "${dir%/*}"
        mkdir -- "$dir"
        printf 'pending\n' > "$state"
    fi
    stage=$(mktemp -d -- "$dir/.pagetty.XXXXXXXX")
    trap 'rm -f -- "$stage/pagetty" "$stage/state"; rmdir -- "$stage"' EXIT
    install -m 644 -- "$src/LICENSE" "$src/THIRD_PARTY_NOTICES.txt" "$dir"
    install -m 755 -- "$src/pagetty" "$stage/pagetty"
    new_hash=$(sha256sum < "$stage/pagetty")
    new_hash=${new_hash%% *}
    # 更新途中で止まっても更新前後どちらの実行ファイルでも再開できるようにする。
    printf '%s %s\n' "$installed_hash" "$new_hash" > "$stage/state"
    mv -fT -- "$stage/state" "$state"
    mv -fT -- "$stage/pagetty" "$binary"
    for rc in "${rc_files[@]}"; do
        if [[ -f $rc ]] && grep -Fxq -- "$hook" "$rc"; then continue; fi
        if [[ -s $rc && -n $(tail -c 1 -- "$rc") ]]; then printf '\n' >> "$rc"; fi
        printf '%s\n' "$hook" >> "$rc"
    done
    printf 'Installed: %s\nOpen a new Bash terminal and run: pagetty --help\n' "$binary"
else
    for rc in "${rc_files[@]}"; do
        [[ -f $rc ]] && grep -Fxq -- "$hook" "$rc" || continue
        # 完全一致する行だけ除去し、利用者が書いた設定を残す。
        lines=$(grep -Fnx -- "$hook" "$rc" | cut -d: -f1 | sed 's/$/d/')
        sed -i "$lines" -- "$rc"
    done
    rm -f -- "$binary" "$dir/LICENSE" "$dir/THIRD_PARTY_NOTICES.txt" "$state"
    rmdir -- "$dir" 2>/dev/null || printf 'Other files remain in: %s\n' "$dir"
    printf 'PageTTY removed. Open a new Bash terminal to refresh PATH.\n'
fi

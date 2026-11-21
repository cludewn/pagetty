#!/usr/bin/env bash
set -euo pipefail
umask 077

fail() { printf 'PageTTY: %s\n' "$*" >&2; exit 1; }
exists() { [[ -e $1 || -L $1 ]]; }
regular() { [[ -f $1 && ! -L $1 ]]; }
move_new() { mv -nT -- "$1" "$2" && ! exists "$1"; }
src=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
mode=install
if [[ ${0##*/} == uninstall.sh ]]; then mode=uninstall; fi
[[ $# -eq 0 ]] || fail 'Run install.sh to install or the installed uninstall.sh to remove PageTTY.'
[[ ${HOME:-} == /* && $HOME != *:* && $HOME != *$'\n'* && $(realpath -e -- "$HOME") == "$HOME" ]] ||
    fail 'HOME must be a real absolute directory without a colon or newline.'
parent="$HOME/.local/share"
dir="$parent/pagetty"
work="$HOME/.pagetty-setup"
record=.pagetty-install
payload=(pagetty LICENSE THIRD_PARTY_NOTICES.txt uninstall.sh)
hook='case ":${PATH-}:" in *":$HOME/.local/share/pagetty:"*|*":$HOME/.local/share/pagetty/:"*) ;; *) export PATH="${PATH:+$PATH:}$HOME/.local/share/pagetty" ;; esac # PageTTY installer'
configs=(.bashrc .bash_profile .bash_login .profile)
owned=() planned=() applied=() created_dirs=()
moved_old=0 placed_new=0

hashes() { (cd -- "$1" && sha256sum -- "${payload[@]}"); }
check_dir() { [[ -d $1 && ! -L $1 && -O $1 ]]; }
check_config() {
    if exists "$1"; then
        regular "$1" && [[ -O $1 && -w $1 && $(stat -c %h -- "$1") == 1 ]] ||
            fail "Cannot safely edit startup file (permissions, links or file type): $1"
    fi
}

mkdir -m 700 -- "$work" || fail "Another setup is active or recovery is needed: $work"

finish() {
    local status=$? name rc broken=0 path
    trap - EXIT
    trap '' INT TERM
    set +e
    if (( status )); then
        for name in "${applied[@]}"; do
            rc="$HOME/$name"
            if ! regular "$rc" || ! cmp -s -- "$rc" "$work/after/$name"; then
                printf 'PageTTY: Could not restore changed startup file: %s\n' "$rc" >&2
                broken=1
            elif exists "$work/before/$name"; then
                mv -fT -- "$work/before/$name" "$rc" || broken=1
            else
                rm -- "$rc" || broken=1
            fi
        done
        if (( placed_new )); then move_new "$dir" "$work/new" || broken=1; fi
        if (( moved_old )); then move_new "$work/old" "$dir" || broken=1; fi
        for path in "${created_dirs[@]}"; do rmdir -- "$path" || broken=1; done
    fi
    if (( broken )); then
        printf 'PageTTY: Rollback incomplete. Recovery files are in %s\n' "$work" >&2
        exit 1
    fi
    # 既知の名前だけを削除し、不明なファイルがあれば作業領域を残す。
    for path in "$work/old" "$work/new"; do
        exists "$path" || continue
        if ! check_dir "$path"; then broken=1; continue; fi
        if [[ $path == "$work/old" ]] && [[ $(hashes "$path") != "$(printf '%s\n' "${state[@]:1:4}")" ]]; then
            broken=1
            continue
        fi
        for name in "${payload[@]}" "$record"; do rm -f -- "$path/$name" || broken=1; done
        rmdir -- "$path" || broken=1
    done
    for path in "$work/before" "$work/after" "$work/apply"; do
        exists "$path" || continue
        if ! check_dir "$path"; then broken=1; continue; fi
        for name in "${configs[@]}"; do rm -f -- "$path/$name" || broken=1; done
        rmdir -- "$path" || broken=1
    done
    rm -f -- "$work/hashes" "$work/manifest" || broken=1
    if (( broken )) || ! rmdir -- "$work"; then
        printf 'PageTTY: Could not clean up %s. Inspect before retrying.\n' "$work" >&2
        exit 1
    fi
    exit "$status"
}
trap finish EXIT
trap 'exit 1' INT TERM
for path in "$HOME/.local" "$parent"; do
    if exists "$path"; then
        check_dir "$path" && [[ $(stat -c %d -- "$path") == "$(stat -c %d -- "$HOME")" ]] ||
            fail "Installation path must be a real user directory on the HOME filesystem: $path"
    fi
done
if exists "$dir"; then
    check_dir "$dir" && regular "$dir/$record" || fail "Installation directory is not managed: $dir"
    for file in "${payload[@]}"; do regular "$dir/$file" || fail "Unexpected managed file: $dir/$file"; done
    [[ $(find "$dir" -mindepth 1 -maxdepth 1 -printf .) == ..... ]] || fail "Unexpected files in $dir"
    mapfile -t state < "$dir/$record"
    [[ ${#state[@]} -ge 5 && ${state[0]} == 'PageTTY install 1' ]] || fail "Unknown installation record: $dir/$record"
    [[ $(hashes "$dir") == "$(printf '%s\n' "${state[@]:1:4}")" ]] || fail "Managed files have changed: $dir"
    for name in "${state[@]:5}"; do
        case "$name" in .bashrc|.bash_profile|.bash_login|.profile) ;; *) fail 'Invalid startup file in installation record.' ;; esac
        [[ " ${owned[*]} " != *" $name "* ]] || fail "Duplicate startup file in installation record."
        owned+=("$name")
    done
elif [[ $mode == uninstall ]]; then
    printf 'PageTTY is not installed.\n'
    exit 0
fi

if [[ $mode == install ]]; then
    for file in pagetty LICENSE THIRD_PARTY_NOTICES.txt install.sh; do
        regular "$src/$file" && [[ -r $src/$file ]] || fail "Missing or unreadable distribution file: $src/$file"
    done
    login=.profile
    for name in "${configs[@]:1}"; do
        if exists "$HOME/$name"; then login=$name; break; fi
    done
    planned=(.bashrc "$login")
else
    planned=("${owned[@]}")
fi
for name in "${planned[@]}"; do check_config "$HOME/$name"; done
[[ -w $HOME && ( ! -d $parent || -w $parent ) ]] || fail 'Cannot write the setup directory.'
mkdir -- "$work/before" "$work/after" "$work/apply"
changes=()
for name in "${planned[@]}"; do
    rc="$HOME/$name"
    if exists "$rc"; then
        count=$(grep -Fxc -- "$hook" "$rc") || [[ $? -eq 1 ]]
    else
        count=0
    fi
    if [[ $mode == install && $count != 0 || $mode == uninstall && $count != 1 ]]; then
        if [[ $mode == uninstall && $count != 1 ]]; then
            printf 'PageTTY: Check the PATH line manually in %s (left unchanged).\n' "$rc" >&2
        fi
        continue
    fi
    if exists "$rc"; then
        cp -p -- "$rc" "$work/before/$name"
        cp -p -- "$work/before/$name" "$work/after/$name"
    else
        : > "$work/after/$name"
    fi
    if [[ $mode == install ]]; then
        target="$work/after/$name"
        if [[ -s $target && -n $(tail -c 1 -- "$target") ]]; then printf '\n' >> "$target"; fi
        printf '%s\n' "$hook" >> "$target"
        if [[ " ${owned[*]} " != *" $name "* ]]; then owned+=("$name"); fi
    else
        line=$(grep -Fnx -- "$hook" "$work/before/$name")
        sed -i "${line%%:*}d" -- "$work/after/$name"
    fi
    cp -p -- "$work/after/$name" "$work/apply/$name"
    changes+=("$name")
done

if [[ $mode == install ]]; then
    mkdir -- "$work/new"
    install -m 755 -- "$src/pagetty" "$work/new/pagetty"
    install -m 644 -- "$src/LICENSE" "$src/THIRD_PARTY_NOTICES.txt" "$work/new/"
    install -m 755 -- "$src/install.sh" "$work/new/uninstall.sh"
    hashes "$work/new" > "$work/hashes"
    { printf 'PageTTY install 1\n'; cat -- "$work/hashes"
      if (( ${#owned[@]} )); then printf '%s\n' "${owned[@]}"; fi
    } > "$work/manifest"
    mv -T -- "$work/manifest" "$work/new/$record"
    for path in "$HOME/.local" "$parent"; do
        if ! exists "$path"; then mkdir -- "$path"; created_dirs=("$path" "${created_dirs[@]}"); fi
    done
fi

if exists "$dir"; then
    check_dir "$dir" && regular "$dir/$record" || fail "Installation directory changed: $dir"
    for file in "${payload[@]}"; do regular "$dir/$file" || fail "Unexpected managed file: $dir/$file"; done
    [[ $(find "$dir" -mindepth 1 -maxdepth 1 -printf .) == ..... &&
        $(cat -- "$dir/$record") == "$(printf '%s\n' "${state[@]}")" &&
        $(hashes "$dir") == "$(printf '%s\n' "${state[@]:1:4}")" ]] || fail "Managed files have changed: $dir"
    move_new "$dir" "$work/old" || fail "Could not move $dir. Close PageTTY and try again."
    moved_old=1
fi
if [[ $mode == install ]]; then
    move_new "$work/new" "$dir" || fail "Could not place $dir"
    placed_new=1
fi
for name in "${changes[@]}"; do
    rc="$HOME/$name"
    check_config "$rc"
    if exists "$work/before/$name"; then
        cmp -s -- "$rc" "$work/before/$name" || fail "Startup file changed during setup: $rc"
        mv -fT -- "$work/apply/$name" "$rc"
    else
        move_new "$work/apply/$name" "$rc" || fail "Startup file appeared during setup: $rc"
    fi
    applied+=("$name")
done

if [[ $mode == install ]]; then
    printf 'Installed: %s/pagetty\nOpen a new Bash terminal and run: pagetty --help\n' "$dir"
else
    printf 'PageTTY removed. Open a new Bash terminal to refresh PATH.\n'
fi

#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/new-api-sync-test.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT

git config --global init.defaultBranch main >/dev/null 2>&1 || true

git init --bare "$tmp_dir/remote.git" >/dev/null
git init "$tmp_dir/seed" >/dev/null
git -C "$tmp_dir/seed" config user.name test
git -C "$tmp_dir/seed" config user.email test@example.invalid
printf 'base\n' > "$tmp_dir/seed/state.txt"
git -C "$tmp_dir/seed" add state.txt
git -C "$tmp_dir/seed" commit -m base >/dev/null
git -C "$tmp_dir/seed" branch -M main
git -C "$tmp_dir/seed" remote add origin "$tmp_dir/remote.git"
git -C "$tmp_dir/seed" push -u origin main >/dev/null

git clone "$tmp_dir/remote.git" "$tmp_dir/work" >/dev/null
git -C "$tmp_dir/work" config user.name test
git -C "$tmp_dir/work" config user.email test@example.invalid

printf 'remote change\n' > "$tmp_dir/seed/remote.txt"
git -C "$tmp_dir/seed" add remote.txt
git -C "$tmp_dir/seed" commit -m remote-change >/dev/null
git -C "$tmp_dir/seed" push >/dev/null

printf 'local uncommitted change\n' >> "$tmp_dir/work/state.txt"
"$script_dir/sync-remote.sh" --remote origin --branch main "$tmp_dir/work" >/dev/null

test "$(git -C "$tmp_dir/work" show HEAD:remote.txt)" = 'remote change'
grep -q '^local uncommitted change$' "$tmp_dir/work/state.txt"
test "$(git -C "$tmp_dir/work" status --porcelain | awk '{print $1}' | head -1)" = 'M'

sed -i.bak '1s/^base$/remote-base/' "$tmp_dir/seed/state.txt"
rm -f "$tmp_dir/seed/state.txt.bak"
git -C "$tmp_dir/seed" add state.txt
git -C "$tmp_dir/seed" commit -m conflicting-remote >/dev/null
git -C "$tmp_dir/seed" push >/dev/null
sed -i.bak '1s/^base$/local-base/' "$tmp_dir/work/state.txt"
rm -f "$tmp_dir/work/state.txt.bak"
git -C "$tmp_dir/work" add state.txt
git -C "$tmp_dir/work" commit -m conflicting-local >/dev/null

if "$script_dir/sync-remote.sh" --remote origin --branch main "$tmp_dir/work" >/dev/null 2>&1; then
  echo 'expected sync to stop on a rebase conflict' >&2
  exit 1
fi
test -d "$tmp_dir/work/.git/rebase-merge" || test -d "$tmp_dir/work/.git/rebase-apply"
git -C "$tmp_dir/work" rebase --abort >/dev/null

echo 'sync-remote tests passed'

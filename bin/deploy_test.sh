#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/new-api-deploy-test.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT

mkdir -p "$tmp_dir/bin"
cat > "$tmp_dir/bin/docker" <<'EOF'
#!/bin/sh
set -eu
case "$1" in
  image) [ "$2" = inspect ] || exit 1; exit 0 ;;
  push) printf '%s\n' "docker push $2" >> "$DEPLOY_TEST_LOG" ;;
  *) echo "unexpected docker command: $*" >&2; exit 1 ;;
esac
EOF
cat > "$tmp_dir/bin/ssh" <<'EOF'
#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$DEPLOY_TEST_LOG"
case "$*" in
  *"docker compose"*"up -d --no-build"*) exit 0 ;;
  *) echo 'deploy command omitted --no-build' >&2; exit 1 ;;
esac
EOF
chmod +x "$tmp_dir/bin/docker" "$tmp_dir/bin/ssh"

export PATH="$tmp_dir/bin:$PATH"
export DEPLOY_TEST_LOG="$tmp_dir/commands.log"
DEPLOY_HOST=deploy@example.invalid \
DEPLOY_PATH=/srv/new-api \
NEW_API_IMAGE=registry.example.invalid/new-api:test \
"$script_dir/deploy.sh"

grep -q 'docker push registry.example.invalid/new-api:test' "$DEPLOY_TEST_LOG"
grep -q 'up -d --no-build' "$DEPLOY_TEST_LOG"
echo 'deploy tests passed'

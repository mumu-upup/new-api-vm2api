#!/bin/sh
set -eu

remote=origin
branch=
repo_dir=.

usage() {
	cat >&2 <<'EOF'
Usage: sync-remote.sh [--remote <name>] [--branch <name>] [<repository>]
EOF
}

while [ "$#" -gt 0 ]; do
	case "$1" in
		--remote)
			[ "$#" -ge 2 ] || { usage; exit 2; }
			remote=$2
			shift 2
			;;
		--branch)
			[ "$#" -ge 2 ] || { usage; exit 2; }
			branch=$2
			shift 2
			;;
		-h|--help)
			usage >&1
			exit 0
			;;
		-*)
			echo "unknown option: $1" >&2
			usage
			exit 2
			;;
		*)
			[ "$repo_dir" = . ] || { echo 'repository specified more than once' >&2; exit 2; }
			repo_dir=$1
			shift
			;;
	esac
done

git_cmd() {
	git -C "$repo_dir" "$@"
}

git_cmd rev-parse --show-toplevel >/dev/null 2>&1 || {
	echo "not a git repository: $repo_dir" >&2
	exit 1
}

if [ -z "$branch" ]; then
	branch=$(git_cmd symbolic-ref --quiet --short HEAD) || {
		echo 'cannot synchronize a detached HEAD; check out a branch first' >&2
		exit 1
	}
fi

git_cmd show-ref --verify --quiet "refs/heads/$branch" || {
	echo "local branch does not exist: $branch" >&2
	exit 1
}

git_cmd remote get-url "$remote" >/dev/null 2>&1 || {
	echo "git remote does not exist: $remote" >&2
	exit 1
}

git_dir=$(git_cmd rev-parse --absolute-git-dir)
if [ -d "$git_dir/rebase-merge" ] || [ -d "$git_dir/rebase-apply" ]; then
	echo 'a rebase is already in progress; resolve it before synchronizing again' >&2
	exit 1
fi

echo "Fetching $remote..." >&2
git_cmd fetch --prune "$remote"

remote_ref="$remote/$branch"
git_cmd rev-parse --verify --quiet "refs/remotes/$remote_ref" >/dev/null || {
	echo "remote branch does not exist: $remote_ref" >&2
	exit 1
}

echo "Synchronizing $branch with $remote_ref..." >&2
if ! git_cmd rebase --autostash "$remote_ref"; then
	echo "rebase stopped because of conflicts; resolve them, run 'git rebase --continue', then rerun the build or deploy command" >&2
	exit 1
fi

echo "Synchronized $branch with $remote_ref." >&2

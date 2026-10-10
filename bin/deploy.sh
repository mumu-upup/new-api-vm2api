#!/bin/sh
set -eu

: "${DEPLOY_HOST:?DEPLOY_HOST is required, for example deploy@server.example.com}"
: "${DEPLOY_PATH:?DEPLOY_PATH is required, for example /srv/new-api}"
: "${NEW_API_IMAGE:?NEW_API_IMAGE is required; build the image before deploying}"

compose_file=${DEPLOY_COMPOSE_FILE:-docker-compose.yml}
push_image=${DEPLOY_PUSH:-true}

case "$NEW_API_IMAGE" in
	*[!A-Za-z0-9._/:@-]*)
		echo 'NEW_API_IMAGE contains unsupported characters' >&2
		exit 2
		;;
esac
case "$DEPLOY_HOST" in
	*[!A-Za-z0-9._@:-]*)
		echo 'DEPLOY_HOST contains unsupported characters' >&2
		exit 2
		;;
esac
case "$DEPLOY_PATH" in
	*[!A-Za-z0-9._/-]*)
		echo 'DEPLOY_PATH contains unsupported characters' >&2
		exit 2
		;;
esac
case "$compose_file" in
	*[!A-Za-z0-9._/-]*)
		echo 'DEPLOY_COMPOSE_FILE contains unsupported characters' >&2
		exit 2
		;;
esac

docker image inspect "$NEW_API_IMAGE" >/dev/null || {
	echo "local image does not exist: $NEW_API_IMAGE" >&2
	exit 1
}

if [ "$push_image" = true ]; then
	echo "Pushing $NEW_API_IMAGE..." >&2
	docker push "$NEW_API_IMAGE"
fi

echo "Deploying $NEW_API_IMAGE to $DEPLOY_HOST:$DEPLOY_PATH..." >&2
ssh "$DEPLOY_HOST" "set -eu
cd '$DEPLOY_PATH'
NEW_API_IMAGE='$NEW_API_IMAGE' docker compose -f '$compose_file' pull new-api
NEW_API_IMAGE='$NEW_API_IMAGE' docker compose -f '$compose_file' up -d --no-build
NEW_API_IMAGE='$NEW_API_IMAGE' docker compose -f '$compose_file' ps
"

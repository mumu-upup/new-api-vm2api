#!/bin/sh
set -eu

# Run against the built worker image on the target Linux Docker host.
worker_image=${1:?Usage: sh test-sandbox.sh WORKER_IMAGE}
worker_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
docker run --rm --network=none --user=1000:1000 \
  --read-only --cap-drop=ALL --security-opt=no-new-privileges:true \
  --security-opt="seccomp=$worker_dir/seccomp_profile.json" \
  --shm-size=256m --pids-limit=256 --memory=1g --cpus=1 \
  --tmpfs=/tmp:rw,nosuid,size=536870912,mode=1777 \
  -e HOME=/tmp -e XDG_CACHE_HOME=/tmp/.cache -e XDG_CONFIG_HOME=/tmp/.config \
  --entrypoint=node "$worker_image" --input-type=module -e '
import assert from "node:assert/strict";
import {readFileSync} from "node:fs";
import {chromium} from "playwright";
assert.equal(process.getuid(),1000);
const status=readFileSync("/proc/self/status","utf8");
assert.match(status,/CapEff:\s+0+\n/);
assert.match(status,/NoNewPrivs:\s+1\n/);
const browser=await chromium.launch({headless:true,chromiumSandbox:true,timeout:20000});
try {
  const page=await browser.newPage();
  await page.setContent("<title>sandbox-check</title>");
  assert.equal(await page.title(),"sandbox-check");
  console.log("SANDBOXED_CHROMIUM_WITHOUT_HOST_CAPABILITIES_OK");
} finally {await browser.close()}
'

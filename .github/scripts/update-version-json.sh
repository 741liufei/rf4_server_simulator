#!/usr/bin/env bash
# 把刚发布的版本合并进仓库根 version.json 并提交回 main。
# 用法: update-version-json.sh <preview|stable> <version> <build_ts> <asset_name>
# commit message 带 [skip ci]，防止回写提交再次触发推送构建。
set -euo pipefail

CHANNEL="${1:?usage: update-version-json.sh <preview|stable> <version> <build_ts> <asset_name>}"
VERSION="${2:?}"
BUILD_TS="${3:?}"
ASSET_NAME="${4:?}"
RELEASED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

git config user.name "rf4-bot"
git config user.email "rf4-bot@users.noreply.github.com"
# 回写基于远端 main 最新：tag/workflow checkout 可能是 detached 或落后于 main。
# 本地/普通发布可用 origin；版本回写 workflow 会传入专用 SSH deploy key 的 URL。
WRITE_REMOTE="${VERSION_WRITEBACK_REMOTE:-origin}"
git fetch "${WRITE_REMOTE}" main
git checkout -B write-back FETCH_HEAD

python3 - "${CHANNEL}" "${VERSION}" "${BUILD_TS}" "${RELEASED_AT}" "${ASSET_NAME}" <<'PY'
import json, sys

channel, version, build_ts, released_at, asset_name = sys.argv[1:6]
assert channel in ("preview", "stable"), channel
with open("version.json", encoding="utf-8") as f:
    data = json.load(f)
if data.get("schema") != 1:
    raise SystemExit(f"unexpected schema in version.json: {data.get('schema')!r}")

entry = {
    "version": version,
    "build_ts": build_ts,
    "released_at": released_at,
    "notes_url": f"https://github.com/glance522/rf4_server_simulator/releases/tag/{version}",
    # releases/latest 只指向正式版；preview 用户给列表页（含 Pre-release）。
    "download_url": "https://github.com/glance522/rf4_server_simulator/releases/latest"
    if channel == "stable"
    else "https://github.com/glance522/rf4_server_simulator/releases",
    # 国内加速直链：gh-proxy 包裹的本仓库 release 附件（2026-11 实测可用）。
    "download_mirror_url": (
        "https://gh-proxy.com/https://github.com/glance522/rf4_server_simulator"
        f"/releases/download/{version}/{asset_name}"
    ),
}
data[channel] = entry
with open("version.json", "w", encoding="utf-8") as f:
    json.dump(data, f, ensure_ascii=False, indent=2)
    f.write("\n")
PY

git add version.json
if git diff --cached --quiet; then
  echo "version.json 无变化，跳过提交"
else
  git commit -m "chore(release): ${CHANNEL} ${VERSION} [skip ci]"
  git push "${WRITE_REMOTE}" HEAD:main
fi
echo "version.json 已回写: ${CHANNEL}=${VERSION} build_ts=${BUILD_TS} asset=${ASSET_NAME}"

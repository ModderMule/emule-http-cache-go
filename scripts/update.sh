#!/bin/bash
set -e

if [ ! -d "$PWD/scripts" ]; then
  echo "Please run this shell script from the project's root folder."
  exit 1
fi

if [[ -z "${UPDATE_URL}" ]]; then
  echo "Set UPDATE_URL to the .tar.gz download location of the bundle."
  exit 1
fi

# TLS verification stays on unless the operator opts out for a host with a
# self-signed certificate. Turning it off by default would silently accept any
# tarball a network attacker cared to serve, and this one is executed.
CURL_OPTS=(-fL)
if [[ -n "${UPDATE_INSECURE}" ]]; then
  echo "WARNING: UPDATE_INSECURE is set — the download is not authenticated."
  CURL_OPTS+=(--insecure)
fi

rm -f "$PWD/emule-http-cache.tar.gz"
curl "${CURL_OPTS[@]}" "$UPDATE_URL" > "$PWD/emule-http-cache.tar.gz"
tar xzf "$PWD/emule-http-cache.tar.gz"

./emule-http-cache serve

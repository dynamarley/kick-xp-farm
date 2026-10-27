#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p dist
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/kick-level-farm-windows-amd64.exe .
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags '-s -w' -o dist/kick-level-farm-linux-amd64 .
echo 'Ready: dist/kick-level-farm-windows-amd64.exe'
echo 'Ready: dist/kick-level-farm-linux-amd64'

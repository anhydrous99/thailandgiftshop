#!/usr/bin/env sh
set -eu

git ls-files '*.go' | xargs -n1 dirname | sort -u | sed 's#^#./#'

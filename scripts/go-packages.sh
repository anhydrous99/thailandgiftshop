#!/usr/bin/env sh
set -eu

{
	git ls-files '*.go'
	git ls-files --others --exclude-standard '*.go'
} | while IFS= read -r file; do
	dirname "$file"
done | sort -u | sed 's#^#./#'

#!/usr/bin/env bash
set -euo pipefail

profile="${1:-coverage.out}"
tmp_profile="$(mktemp)"
trap 'rm -f "$tmp_profile"' EXIT

packages="$(go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./... | grep -vE '^$|/cmd/bench$')"

go test -covermode=atomic -coverprofile="$tmp_profile" $packages

awk '
	NR == 1 || $1 !~ /^github\.com\/mg52\/search\/cmd\/bench\// {
		print
	}
' "$tmp_profile" > "$profile"

go tool cover -func="$profile"

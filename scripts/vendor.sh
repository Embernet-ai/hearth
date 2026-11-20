#!/usr/bin/env bash
# Refreshes vendor/ from go.mod, the one way it should ever be done.
#
# `go mod vendor` copies every file of a dependency's module that sits next to its Go code,
# and a few of them ship notes written for coding assistants (a CLAUDE.md). This repository
# does not carry those, so they are removed right after vendoring. CI runs this script and
# fails if the tree it leaves differs from what is committed, so a plain `go mod vendor`
# that brings one back is caught before it lands.
set -euo pipefail
cd "$(dirname "$0")/.."
go mod vendor
find vendor -type f -name 'CLAUDE.md' -delete

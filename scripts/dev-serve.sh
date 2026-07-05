#!/usr/bin/env bash
export VALAF_DATABASE_URL="postgres://postgres:valaf@127.0.0.1:55432/valaf?sslmode=disable"
export VALAF_HTTP_ADDR="127.0.0.1:8099"
exec "$(dirname "$0")/../valaf-dev.exe" serve

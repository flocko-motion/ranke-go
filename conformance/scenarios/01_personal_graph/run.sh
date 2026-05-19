#!/usr/bin/env bash
cd "$(dirname "$0")"
rm -rf archive ids.txt
go run .

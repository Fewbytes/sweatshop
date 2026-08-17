#!/usr/bin/env bash
printf 'Overwrite ./config.yaml? [y/N] '
IFS= read -r answer
printf 'answer=%s\n' "$answer"

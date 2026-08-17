#!/usr/bin/env bash
setsid sh -c 'sleep 300' & echo "$!"; wait

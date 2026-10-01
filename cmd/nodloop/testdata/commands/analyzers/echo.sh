#!/bin/sh
# Prints its first argument and then the event it read on stdin
printf '%s ' "$1"
cat
echo
